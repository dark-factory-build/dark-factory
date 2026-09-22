//go:build darwin

package daemon

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/changeworker"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

// failBeforeRuntime moves the fixture's admitted run to finalizing with its
// whole declared footprint released, exactly as the runtime-absent failure
// edge does, leaving settlement as the only remaining authority.
func (fixture *recoveryFixture) failBeforeRuntime(t *testing.T) kernel.Run {
	t.Helper()
	ctx := context.Background()
	failure, err := kernel.NewFailureProposal(kernel.FailureSpawn, "settlement test failure")
	if err != nil {
		t.Fatal(err)
	}
	resource, found, err := fixture.store.Resource(ctx, fixture.keys.Resources.RuntimeRoot)
	if err != nil || !found {
		t.Fatalf("runtime resource: found=%v err=%v", found, err)
	}
	run, err := fixture.store.FailRunWithRuntimeAbsent(ctx, fixture.run.ID, resource.ID, fixture.currentRun(t).Revision, resource.Revision, failure, mustKernelTime(t, 400))
	if err != nil {
		t.Fatal(err)
	}
	fixture.run = run
	return run
}

func TestSettleRunFinalizesOrchestratorAndReplaysTerminal(t *testing.T) {
	fixture := newRecoveryFixture(t, 0x60)
	if _, err := fixture.daemon.settleRun(context.Background(), fixture.changeParent, fixture.run.ID); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("admitted run settlement = %v", err)
	}
	fixture.failBeforeRuntime(t)
	settled, err := fixture.daemon.settleRun(context.Background(), fixture.changeParent, fixture.run.ID)
	if err != nil || settled.Phase != kernel.RunTerminal || settled.Terminal == nil || settled.Terminal.Code() != kernel.FailureSpawn {
		t.Fatalf("settled orchestrator run = %+v, %v", settled, err)
	}
	replay, err := fixture.daemon.settleRun(context.Background(), fixture.changeParent, fixture.run.ID)
	if err != nil || replay.Phase != kernel.RunTerminal || replay.Revision != settled.Revision {
		t.Fatalf("terminal replay = %+v, %v", replay, err)
	}
}

// settlementWorktree makes the fixture's project a real repository and the
// run's Change a worktree of it, available in the store at the base, and
// returns the Change and its path.
func (fixture *recoveryFixture) settlementWorktree(t *testing.T) (kernel.Change, string) {
	t.Helper()
	ctx := context.Background()
	root := filepath.Dir(filepath.Dir(fixture.parentPath))
	repository := filepath.Join(root, "repo")
	git := change.TrustedGitExecutable
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"init", "-q"}, {"config", "user.name", "test"}, {"config", "user.email", "test@example.invalid"}} {
		settlementGit(t, git, repository, arguments...)
	}
	if err := os.WriteFile(filepath.Join(repository, "payload.txt"), []byte("exact source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	settlementGit(t, git, repository, "add", "payload.txt")
	settlementGit(t, git, repository, "commit", "-q", "-m", "base")
	identity, err := inspectRepositoryIdentity(repository)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := change.SelectGit(ctx, git, repository, "HEAD", identity)
	if err != nil {
		t.Fatal(err)
	}
	run := fixture.currentRun(t)
	if run.ChangeID == nil {
		t.Fatal("worker run without candidate change")
	}
	changeState, found, err := fixture.store.Change(ctx, *run.ChangeID)
	if err != nil || !found {
		t.Fatalf("change: found=%v err=%v", found, err)
	}
	path := filepath.Join(fixture.changeParent, changeState.ID.String())
	facts, err := change.AddWorktree(ctx, selection, path, change.BranchName(changeState.ID.String()))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	kernelSelection, err := kernelSelectionCheckpoint(changeworker.Result{Format: selection.ObjectFormat(), Base: selection.Base()}, identity)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := fixture.store.RecordChangePrepared(ctx, changeState.ID, changeState.Revision, kernelSelection, mustKernelTime(t, 300))
	if err != nil {
		t.Fatal(err)
	}
	head, err := kernelCommit(facts.Head())
	if err != nil {
		t.Fatal(err)
	}
	available, err := fixture.store.MarkChangeAvailable(ctx, changeState.ID, prepared.Revision, head, mustKernelTime(t, 310))
	if err != nil {
		t.Fatal(err)
	}
	fixture.daemon.RememberSupervisorAccount(fixture.changeParent, filepath.Join(root, "account"), git)
	return available, path
}

func settlementGit(t *testing.T, git, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command(git, append([]string{"-C", directory}, arguments...)...)
	command.Env = []string{"HOME=/var/empty", "PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=worker", "GIT_AUTHOR_EMAIL=worker@example.invalid", "GIT_COMMITTER_NAME=worker", "GIT_COMMITTER_EMAIL=worker@example.invalid"}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}

// The settlement timestamp is read after the worktree inspection, which may
// wait behind other durable updates: an inspection that took long, then a
// stale timestamp, must not refuse the settlement.
func TestSettleRunReadsTheWorktreeHeadWhileTheClockAdvances(t *testing.T) {
	fixture := newRecoveryFixtureWithRole(t, 0x68, kernel.RoleWorker)
	ctx := context.Background()
	changeState, path := fixture.settlementWorktree(t)
	if err := os.WriteFile(filepath.Join(path, "made.txt"), []byte("made\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	settlementGit(t, change.TrustedGitExecutable, path, "add", "made.txt")
	settlementGit(t, change.TrustedGitExecutable, path, "commit", "-q", "-m", "made")
	head := settlementGit(t, change.TrustedGitExecutable, path, "rev-parse", "HEAD")
	fixture.failBeforeRuntime(t)
	// Only a run that reached running had authority to move the branch
	// (ARCHITECTURE.md); settlement refuses a moved head from a run that
	// never ran. Record that this run did: the full resource-activation
	// lifecycle that would set this durably is proven elsewhere, and this
	// fixture drives settlement's own concurrency in isolation from it. The
	// terminal session's own activation must agree, or the run's relationship
	// read refuses it as corrupt before settlement is ever reached.
	execSupervisorSQL(t, fixture.storePath, `UPDATE runs SET running_at_ms = 350 WHERE id = ?`, fixture.run.ID.Bytes())
	execSupervisorSQL(t, fixture.storePath, `UPDATE terminal_sessions SET activated_at_ms = 350 WHERE run_id = ?`, fixture.run.ID.Bytes())
	var clock atomic.Int64
	clock.Store(500)
	fixture.daemon.now = func() time.Time { return time.UnixMilli(clock.Load()) }
	inspectionHeld := make(chan struct{})
	continueInspection := make(chan struct{})
	fixture.daemon.settleRetained = func(ctx context.Context, parent string, state kernel.Change) (kernel.ChangeSettlement, error) {
		close(inspectionHeld)
		<-continueInspection
		return fixture.daemon.retainedSettlement(ctx, parent, state)
	}
	type settlementResult struct {
		run kernel.Run
		err error
	}
	settledResult := make(chan settlementResult, 1)
	go func() {
		run, err := fixture.daemon.settleRun(context.Background(), fixture.changeParent, fixture.run.ID)
		settledResult <- settlementResult{run, err}
	}()
	select {
	case <-inspectionHeld:
	case <-time.After(time.Second):
		t.Fatal("settlement did not reach retained inspection")
	}
	factory, err := fixture.store.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.SetDispatch(ctx, factory.Revision, false, mustKernelTime(t, 600)); err != nil {
		t.Fatalf("concurrent dispatch update: %v", err)
	}
	clock.Store(700)
	close(continueInspection)
	result := <-settledResult
	settled, err := result.run, result.err
	if err != nil || settled.Phase != kernel.RunTerminal || settled.Terminal == nil {
		t.Fatalf("retained settlement = %+v, %v", settled, err)
	}
	retained, found, err := fixture.store.Change(ctx, changeState.ID)
	if err != nil || !found || retained.Phase != kernel.ChangeRetained || retained.HeadCommit == nil || hex.EncodeToString(retained.HeadCommit.Bytes()) != head {
		t.Fatalf("retained Change = %+v, found=%v, %v (want head %s)", retained, found, err, head)
	}
}

func TestSuccessfulWorkerOutcomeRefusesDirtySourceUntilCorrection(t *testing.T) {
	fixture := newRecoveryFixtureWithRole(t, 0x6e, kernel.RoleWorker)
	ctx := context.Background()
	worktreeChange, path := fixture.settlementWorktree(t)
	success, err := kernel.NewSuccessProposal("done")
	if err != nil {
		t.Fatal(err)
	}
	live := &liveAttempt{daemon: fixture.daemon, runID: fixture.run.ID}
	if err := fixture.daemon.validateSuccessSource(ctx, live, success); err != nil {
		t.Fatalf("clean no-change success refused: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "uncommitted.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refused := fixture.daemon.validateSuccessSource(ctx, live, success)
	if !errors.Is(refused, errDirtyWorkerChange) || !errors.Is(refused, kernel.ErrConflict) {
		t.Fatalf("dirty success refusal = %v", refused)
	}
	// This is the reason the worker is answered with. A refusal that says only
	// that something conflicts leaves finished work with no way to report it.
	var refusal *kernel.OutcomeRefusal
	if !errors.As(refused, &refusal) {
		t.Fatalf("dirty success refusal is not an outcome refusal: %v", refused)
	}
	detail := boundedDetail(refusal.Unwrap())
	if !strings.Contains(detail, errDirtyWorkerChange.Error()) || !strings.Contains(detail, worktreeChange.ID.String()) {
		t.Fatalf("dirty refusal detail does not say what to do: %q", detail)
	}
	settlementGit(t, change.TrustedGitExecutable, path, "add", "uncommitted.txt")
	settlementGit(t, change.TrustedGitExecutable, path, "commit", "-q", "-m", "corrected")
	if err := fixture.daemon.validateSuccessSource(ctx, live, success); err != nil {
		t.Fatalf("corrected success refused: %v", err)
	}
}

func TestSuccessfulWorkerOutcomeRefusesUnavailableSourceFacts(t *testing.T) {
	success, err := kernel.NewSuccessProposal("done")
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*recoveryFixture, string){
		"Store.Run": func(fixture *recoveryFixture, _ string) {
			fixture.daemon.successSourceRun = func(context.Context, kernel.RunID) (kernel.Run, bool, error) {
				return kernel.Run{}, false, errors.New("injected Store.Run failure")
			}
			err := fixture.daemon.validateSuccessSource(context.Background(), &liveAttempt{daemon: fixture.daemon, runID: fixture.run.ID}, success)
			if !errors.Is(err, kernel.ErrConflict) {
				t.Fatalf("refusal = %v", err)
			}
		},
		"Change": func(fixture *recoveryFixture, _ string) {
			fixture.daemon.successSourceChange = func(context.Context, kernel.ChangeID) (kernel.Change, bool, error) {
				return kernel.Change{}, false, errors.New("injected Change read failure")
			}
			err := fixture.daemon.validateSuccessSource(context.Background(), &liveAttempt{daemon: fixture.daemon, runID: fixture.run.ID}, success)
			if !errors.Is(err, kernel.ErrConflict) {
				t.Fatalf("refusal = %v", err)
			}
		},
		"Selection": func(fixture *recoveryFixture, _ string) {
			fixture.daemon.successSourceChange = func(ctx context.Context, id kernel.ChangeID) (kernel.Change, bool, error) {
				state, found, err := fixture.store.Change(ctx, id)
				state.Selection = nil
				return state, found, err
			}
			err := fixture.daemon.validateSuccessSource(context.Background(), &liveAttempt{daemon: fixture.daemon, runID: fixture.run.ID}, success)
			if !errors.Is(err, kernel.ErrConflict) {
				t.Fatalf("refusal = %v", err)
			}
		},
		"Store.TaskRepository": func(fixture *recoveryFixture, _ string) {
			fixture.daemon.successSourceRepository = func(context.Context, kernel.TaskID) (kernel.ProjectRepository, bool, error) {
				return kernel.ProjectRepository{}, false, errors.New("injected TaskRepository read failure")
			}
			err := fixture.daemon.validateSuccessSource(context.Background(), &liveAttempt{daemon: fixture.daemon, runID: fixture.run.ID}, success)
			if !errors.Is(err, kernel.ErrConflict) {
				t.Fatalf("refusal = %v", err)
			}
		},
		"repository identity": func(fixture *recoveryFixture, _ string) {
			execSupervisorSQL(t, fixture.storePath, `UPDATE changes SET repository_inode = CASE WHEN repository_inode = 9223372036854775807 THEN repository_inode - 1 ELSE repository_inode + 1 END WHERE id = ?`, fixture.run.ChangeID.Bytes())
			err := fixture.daemon.validateSuccessSource(context.Background(), &liveAttempt{daemon: fixture.daemon, runID: fixture.run.ID}, success)
			if !errors.Is(err, kernel.ErrConflict) {
				t.Fatalf("refusal = %v", err)
			}
		},
		"Git/toolchain/path": func(fixture *recoveryFixture, _ string) {
			fixture.daemon.gitExecutable.Store(nil)
			err := fixture.daemon.validateSuccessSource(context.Background(), &liveAttempt{daemon: fixture.daemon, runID: fixture.run.ID}, success)
			if !errors.Is(err, kernel.ErrConflict) {
				t.Fatalf("refusal = %v", err)
			}
		},
		"InspectWorktree": func(fixture *recoveryFixture, path string) {
			if err := os.Remove(filepath.Join(path, ".git")); err != nil {
				t.Fatal(err)
			}
			err := fixture.daemon.validateSuccessSource(context.Background(), &liveAttempt{daemon: fixture.daemon, runID: fixture.run.ID}, success)
			if !errors.Is(err, kernel.ErrConflict) {
				t.Fatalf("refusal = %v", err)
			}
		},
	}
	for name, inject := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newRecoveryFixtureWithRole(t, byte(len(name)+0xa0), kernel.RoleWorker)
			_, path := fixture.settlementWorktree(t)
			inject(fixture, path)
			observed, found, err := fixture.store.Run(context.Background(), fixture.run.ID)
			if err != nil || !found || observed.Phase != kernel.RunAdmitted || observed.Proposal != nil {
				t.Fatalf("unavailable source changed durable run: %+v found=%v err=%v", observed, found, err)
			}
		})
	}
}

func TestSettleRunAbandonsUnpublishedWorkerChange(t *testing.T) {
	fixture := newRecoveryFixtureWithRole(t, 0x70, kernel.RoleWorker)
	fixture.failBeforeRuntime(t)
	settled, err := fixture.daemon.settleRun(context.Background(), fixture.changeParent, fixture.run.ID)
	if err != nil || settled.Phase != kernel.RunTerminal || settled.Terminal == nil {
		t.Fatalf("settled worker run = %+v, %v", settled, err)
	}
	if settled.ChangeID == nil {
		t.Fatal("worker run lost its candidate change")
	}
	changeState, found, err := fixture.store.Change(context.Background(), *settled.ChangeID)
	if err != nil || !found || changeState.Phase != kernel.ChangeAbandoned || changeState.SettledRunID == nil || *changeState.SettledRunID != settled.ID {
		t.Fatalf("abandoned change = %+v found=%v err=%v", changeState, found, err)
	}
}

// An available Change whose path holds something that is not its worktree
// settles nothing: the run stays finalizing and discoverable. A path that is
// gone is a refusal: the run fails visibly and the Change is abandoned.
func TestSettleRunRefusesUnverifiableAndAbandonsMissingWorktree(t *testing.T) {
	fixture := newRecoveryFixtureWithRole(t, 0x80, kernel.RoleWorker)
	ctx := context.Background()
	changeState, path := fixture.settlementWorktree(t)
	before := fixture.failBeforeRuntime(t)
	if err := os.Remove(filepath.Join(path, ".git")); err != nil {
		t.Fatal(err)
	}
	settled, err := fixture.daemon.settleRun(context.Background(), fixture.changeParent, fixture.run.ID)
	if !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("unverifiable worktree settlement = %+v, %v", settled, err)
	}
	after := fixture.currentRun(t)
	if after.Phase != kernel.RunFinalizing || after.Revision != before.Revision {
		t.Fatalf("refused settlement mutated the run: %+v -> %+v", before, after)
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	settled, err = fixture.daemon.settleRun(context.Background(), fixture.changeParent, fixture.run.ID)
	if err != nil || settled.Phase != kernel.RunTerminal || settled.Terminal == nil || settled.Terminal.Code() != kernel.FailureSource || !strings.Contains(settled.Terminal.Detail(), "worktree is gone") {
		t.Fatalf("missing worktree settlement = %+v, %v", settled, err)
	}
	if abandoned, found, err := fixture.store.Change(ctx, changeState.ID); err != nil || !found || abandoned.Phase != kernel.ChangeAbandoned {
		t.Fatalf("change after refusal = %+v, found=%v, %v", abandoned, found, err)
	}
}

// A Change from before managed worktrees, never adopted, settles as it is:
// retained with no head, and no Git is needed to say so.
func TestSettleRunRetainsAGitFreeChangeWithoutAHead(t *testing.T) {
	fixture := newRecoveryFixtureWithRole(t, 0x82, kernel.RoleWorker)
	ctx := context.Background()
	changeState, path := fixture.settlementWorktree(t)
	if _, err := fixture.store.RecordChangeWorktree(ctx, changeState.ID, changeState.Revision, *changeState.HeadCommit); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	// As the v13 migration leaves a Change: base recorded, no head.
	execSupervisorSQL(t, fixture.storePath, `UPDATE changes SET head_commit = NULL WHERE id = ?`, changeState.ID.Bytes())
	fixture.daemon.RememberSupervisorAccount(fixture.changeParent, "", "/private/no-git-for-a-git-free-change")
	fixture.failBeforeRuntime(t)
	settled, err := fixture.daemon.settleRun(context.Background(), fixture.changeParent, fixture.run.ID)
	if err != nil || settled.Phase != kernel.RunTerminal || settled.Terminal == nil || settled.Terminal.Code() != kernel.FailureSpawn {
		t.Fatalf("Git-free settlement = %+v, %v", settled, err)
	}
	if retained, found, err := fixture.store.Change(ctx, changeState.ID); err != nil || !found || retained.Phase != kernel.ChangeRetained || retained.HeadCommit != nil {
		t.Fatalf("retained Git-free Change = %+v, found=%v, %v", retained, found, err)
	}
}

// A worker's commits are what is published. The head recorded when the
// change became available is the base, so a run the daemon settles after
// its own finalize did not (a restart, a scheduler pass) must take the
// branch as the worker left it, not refuse it for differing from the base.
func TestRecoverySettlesThePublishedTreeAsTheWorkerLeftIt(t *testing.T) {
	fixture := newRecoveryFixtureWithRole(t, 0x84, kernel.RoleWorker)
	ctx := context.Background()
	fixture.stageRuntime(t)
	fixture.beginRunnerStart(t)
	fixture.activateRunner(t)
	fixture.writeMarker(t, runner.OuterActivationMarkerName)
	changeState, path := fixture.settlementWorktree(t)
	providerIdentity, err := processResourceIdentity(runner.Identity{PID: 99996, PGID: 99996, Birth: runner.Birth{Seconds: 1700, Microseconds: 3}})
	if err != nil {
		t.Fatal(err)
	}
	states := fixture.resourceStates(t)
	process, group := states[kernel.ResourceProviderProcess], states[kernel.ResourceProviderGroup]
	if _, _, err := fixture.store.ActivateProviderResources(ctx, fixture.run.ID, process.ID, process.Revision, group.ID, group.Revision, providerIdentity, mustKernelTime(t, 450)); err != nil {
		t.Fatal(err)
	}
	session, found, err := fixture.store.TerminalSessionForRun(ctx, fixture.run.ID)
	if err != nil || !found {
		t.Fatalf("session: found=%v err=%v", found, err)
	}
	if fixture.run, err = fixture.store.ActivateRun(ctx, fixture.run.ID, session.ID, fixture.currentRun(t).Revision, session.Revision, mustKernelTime(t, 460)); err != nil {
		t.Fatal(err)
	}
	// The provider adds a file, commits it and reports success; then it and
	// the runner are gone, and the runner's result says the provider converged.
	if err := os.WriteFile(filepath.Join(path, "made.txt"), []byte("made\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	settlementGit(t, change.TrustedGitExecutable, path, "add", "made.txt")
	settlementGit(t, change.TrustedGitExecutable, path, "commit", "-q", "-m", "made a file")
	head := settlementGit(t, change.TrustedGitExecutable, path, "rev-parse", "HEAD")
	success, err := kernel.NewSuccessProposal("made a file")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.ProposeAttemptOutcome(ctx, fixture.keys.AttemptDigest, success, mustKernelTime(t, 500)); err != nil {
		t.Fatal(err)
	}
	fixture.writeArtifact(t, []byte(fmt.Sprintf(`{"version":1,"attempt_id":%q,"kind":"inner_converged","proof":%q,"process":{"pid":99996,"pgid":99996,"birth":{"seconds":1700,"microseconds":3}},"exit":{"code":0}}`, fixture.run.ID.String(), hex.EncodeToString(fixture.proof[:]))))
	disposition := fixture.sweep(t)
	if disposition.Action != RecoveredResultConsumed || disposition.Err != nil {
		t.Fatalf("disposition = %+v", disposition)
	}
	settled := fixture.currentRun(t)
	if settled.Phase != kernel.RunTerminal || settled.Terminal == nil || settled.Terminal.Kind() != kernel.OutcomeSucceeded {
		t.Fatalf("recovered run = %+v", settled)
	}
	retained, found, err := fixture.store.Change(ctx, changeState.ID)
	if err != nil || !found || retained.Phase != kernel.ChangeRetained || retained.HeadCommit == nil || hex.EncodeToString(retained.HeadCommit.Bytes()) != head {
		t.Fatalf("retained change = %+v, found=%v, %v (want head %s)", retained, found, err, head)
	}
	if hex.EncodeToString(retained.Selection.Commit().Bytes()) == head {
		t.Fatal("settlement moved the base")
	}
}

// The same recovery with the worktree gone ends the run as a visible source
// failure instead of leaving it finalizing for good.
func TestRecoveryMissingWorktreeFailsTheRunVisibly(t *testing.T) {
	fixture := newRecoveryFixtureWithRole(t, 0x94, kernel.RoleWorker)
	ctx := context.Background()
	fixture.stageRuntime(t)
	fixture.beginRunnerStart(t)
	fixture.activateRunner(t)
	fixture.writeMarker(t, runner.OuterActivationMarkerName)
	changeState, path := fixture.settlementWorktree(t)
	providerIdentity, err := processResourceIdentity(runner.Identity{PID: 99995, PGID: 99995, Birth: runner.Birth{Seconds: 1700, Microseconds: 4}})
	if err != nil {
		t.Fatal(err)
	}
	states := fixture.resourceStates(t)
	process, group := states[kernel.ResourceProviderProcess], states[kernel.ResourceProviderGroup]
	if _, _, err := fixture.store.ActivateProviderResources(ctx, fixture.run.ID, process.ID, process.Revision, group.ID, group.Revision, providerIdentity, mustKernelTime(t, 450)); err != nil {
		t.Fatal(err)
	}
	session, found, err := fixture.store.TerminalSessionForRun(ctx, fixture.run.ID)
	if err != nil || !found {
		t.Fatalf("session: found=%v err=%v", found, err)
	}
	if fixture.run, err = fixture.store.ActivateRun(ctx, fixture.run.ID, session.ID, fixture.currentRun(t).Revision, session.Revision, mustKernelTime(t, 460)); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	success, err := kernel.NewSuccessProposal("removed my worktree")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.ProposeAttemptOutcome(ctx, fixture.keys.AttemptDigest, success, mustKernelTime(t, 500)); err != nil {
		t.Fatal(err)
	}
	fixture.writeArtifact(t, []byte(fmt.Sprintf(`{"version":1,"attempt_id":%q,"kind":"inner_converged","proof":%q,"process":{"pid":99995,"pgid":99995,"birth":{"seconds":1700,"microseconds":4}},"exit":{"code":0}}`, fixture.run.ID.String(), hex.EncodeToString(fixture.proof[:]))))
	disposition := fixture.sweep(t)
	if disposition.Action != RecoveredResultConsumed || disposition.Err != nil {
		t.Fatalf("disposition = %+v", disposition)
	}
	settled := fixture.currentRun(t)
	if settled.Phase != kernel.RunTerminal || settled.Terminal == nil || settled.Terminal.Kind() != kernel.OutcomeFailed || settled.Terminal.Code() != kernel.FailureSource ||
		!strings.Contains(settled.Terminal.Detail(), "worktree is gone") {
		t.Fatalf("recovered run = %+v", settled.Terminal)
	}
	abandoned, found, err := fixture.store.Change(ctx, changeState.ID)
	if err != nil || !found || abandoned.Phase != kernel.ChangeAbandoned {
		t.Fatalf("change after refusal = %+v, found=%v, %v", abandoned, found, err)
	}
}

func TestScheduledCompletionSettlesReturnedReleasedRun(t *testing.T) {
	fixture := newRecoveryFixtureWithRole(t, 0x88, kernel.RoleWorker)
	failed := fixture.failBeforeRuntime(t)
	if err := fixture.daemon.validateScheduledCompletion(fixture.changeParent, nil, failed); err != nil {
		t.Fatalf("released completion = %v", err)
	}
	settled := fixture.currentRun(t)
	if settled.Phase != kernel.RunTerminal || settled.Terminal == nil || settled.Terminal.Code() != kernel.FailureSpawn {
		t.Fatalf("released completion stayed nonterminal: %+v", settled)
	}
}

// TestScheduledCompletionSurfacesUnsettledRun pins the non-fatal refusal:
// residue that still cannot settle remains visible while scheduling continues.
func TestScheduledCompletionSurfacesUnsettledRun(t *testing.T) {
	fixture := newRecoveryFixtureWithRole(t, 0x90, kernel.RoleWorker)
	fixture.stageRuntime(t)
	fixture.beginRunnerStart(t)
	fixture.activateRunner(t)
	ctx := context.Background()
	providerIdentity, err := processResourceIdentity(runner.Identity{PID: 99996, PGID: 99996, Birth: runner.Birth{Seconds: 1700, Microseconds: 3}})
	if err != nil {
		t.Fatal(err)
	}
	states := fixture.resourceStates(t)
	process, group := states[kernel.ResourceProviderProcess], states[kernel.ResourceProviderGroup]
	if _, _, err := fixture.store.ActivateProviderResources(ctx, fixture.run.ID, process.ID, process.Revision, group.ID, group.Revision, providerIdentity, mustKernelTime(t, 450)); err != nil {
		t.Fatal(err)
	}
	// The run fails with its fully active footprint: finalizing with
	// releasing residue nothing can settle — the exact shape a dead
	// controller leaves behind.
	run := fixture.currentRun(t)
	failure, err := kernel.NewFailureProposal(kernel.FailureProtocol, "settlement test residue")
	if err != nil {
		t.Fatal(err)
	}
	failed, err := fixture.store.FailRun(context.Background(), run.ID, run.Revision, failure, mustKernelTime(t, 500))
	if err != nil {
		t.Fatal(err)
	}
	fixture.run = failed
	reported := kernel.RunID{}
	var reportedErr error
	err = fixture.daemon.validateScheduledCompletion(fixture.changeParent, func(id kernel.RunID, cause error) {
		reported, reportedErr = id, cause
	}, failed)
	if err != nil {
		t.Fatalf("unsettled completion was fatal: %v", err)
	}
	if reported != failed.ID || !errors.Is(reportedErr, kernel.ErrConflict) {
		t.Fatalf("unsettled report = %v, %v", reported, reportedErr)
	}
	after := fixture.currentRun(t)
	if after.Phase != kernel.RunFinalizing {
		t.Fatalf("surfaced run mutated to %v", after.Phase)
	}
}

func TestSettlementWaitsForWriterUsingLifecycleContext(t *testing.T) {
	for _, role := range []kernel.AgentRole{kernel.RoleOrchestrator, kernel.RoleWorker} {
		t.Run(role.String(), func(t *testing.T) {
			fixture := newRecoveryFixtureWithRole(t, 0xb0, role)
			fixture.failBeforeRuntime(t)
			lock, err := sql.Open("sqlite3", "file:"+fixture.storePath)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			lock.SetMaxOpenConns(1)
			if _, err := lock.Exec("BEGIN IMMEDIATE"); err != nil {
				t.Fatal(err)
			}
			defer lock.Exec("ROLLBACK")
			entered := make(chan struct{}, 1)
			fixture.daemon.now = func() time.Time {
				select {
				case entered <- struct{}{}:
				default:
				}
				return time.UnixMilli(9000)
			}
			done := make(chan error, 1)
			go func() {
				_, err := fixture.daemon.settleRun(fixture.daemon.cleanupCtx, fixture.changeParent, fixture.run.ID)
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("settlement did not reach writer")
			}
			select {
			case err := <-done:
				t.Fatalf("settlement abandoned before release: %v", err)
			case <-time.After(liveAttemptStoreTimeout + 300*time.Millisecond):
			}
			if _, err := lock.Exec("ROLLBACK"); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("settlement did not finish")
			}
			if current := fixture.currentRun(t); current.Phase != kernel.RunTerminal {
				t.Fatalf("settlement=%+v", current)
			}
		})
	}
}
