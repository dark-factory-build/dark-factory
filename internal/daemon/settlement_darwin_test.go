//go:build darwin

package daemon

import (
	"bytes"
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/change"
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

func TestSettleRunAllowsAFullRetainedTreeScan(t *testing.T) {
	fixture := newRecoveryFixtureWithRole(t, 0x68, kernel.RoleWorker)
	ctx := context.Background()
	changeState, found, err := fixture.store.Change(ctx, *fixture.run.ChangeID)
	if err != nil || !found {
		t.Fatalf("change: found=%v err=%v", found, err)
	}
	format, err := change.NewObjectFormat("sha1")
	if err != nil {
		t.Fatal(err)
	}
	base, err := change.NewObjectID(format, bytes.Repeat([]byte{0x55}, 20))
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("right\n")
	sum := sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(content))), content...))
	oid, err := change.NewObjectID(format, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	entry, err := change.NewEntry([]byte("nested/a"), "100644", uint64(len(content)), oid)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := change.NewManifest(format, base, []change.Entry{entry})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := change.Prepare(ctx, fixture.changeParent, changeState.ID.String(), changeState.ID.String()+".stage")
	if err != nil {
		t.Fatal(err)
	}
	published, err := prepared.PopulateAndPublish(ctx, manifest, func(context.Context, change.ObjectID) ([]byte, error) { return bytes.Clone(content), nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(published.Path()); err != nil {
		t.Fatalf("published tree: %v", err)
	}
	facts := published.Facts()
	large := filepath.Join(published.Path(), "generated.bin")
	largeFile, err := os.OpenFile(large, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := largeFile.Truncate(1 << 30); err != nil {
		_ = largeFile.Close()
		t.Fatal(err)
	}
	if err := largeFile.Close(); err != nil {
		t.Fatal(err)
	}
	tree, err := kernelStageIdentity(prepared.Identity())
	if err != nil {
		t.Fatal(err)
	}
	kernelFormat, err := kernel.NewObjectFormat("sha1")
	if err != nil {
		t.Fatal(err)
	}
	commit, err := kernel.NewCommitID(kernelFormat, base.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	digest, err := kernel.TreeDigestFromBytes(facts.Commitment().Bytes())
	if err != nil {
		t.Fatal(err)
	}
	repository, err := kernel.NewFileIdentity(7, 8)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := kernel.NewChangeSelection(kernelFormat, commit, digest, uint32(facts.EntryCount()), facts.BlobBytes(), repository)
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := fixture.store.RecordChangePrepared(ctx, changeState.ID, changeState.Revision, selection, tree, mustKernelTime(t, 300))
	if err != nil {
		t.Fatal(err)
	}
	availability, err := kernelAvailability(facts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.MarkChangeAvailable(ctx, changeState.ID, recorded.Revision, availability, mustKernelTime(t, 310)); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.failBeforeRuntime(t)
	// Hold the retained inspection while an independent durable update advances
	// the factory clock. The settlement timestamp must be read after that
	// inspection, or the kernel rejects its stale finalization.
	var clock atomic.Int64
	clock.Store(500)
	fixture.daemon.now = func() time.Time { return time.UnixMilli(clock.Load()) }
	inspectionHeld := make(chan struct{})
	continueInspection := make(chan struct{})
	fixture.daemon.settleRetained = func(ctx context.Context, parent string, state kernel.Change) (kernel.ChangeSettlement, error) {
		close(inspectionHeld)
		<-continueInspection
		return retainedSettlement(ctx, parent, state)
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
		t.Fatalf("large retained settlement = %+v, %v", settled, err)
	}
}

func TestSuccessfulWorkerOutcomeRefusesDirtySourceUntilCorrection(t *testing.T) {
	fixture := newRecoveryFixtureWithRole(t, 0x6e, kernel.RoleWorker)
	ctx := context.Background()
	_, path := fixture.settlementWorktree(t)
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
	if err := fixture.daemon.validateSuccessSource(ctx, live, success); !errors.Is(err, errDirtyWorkerChange) || !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("dirty success refusal = %v", err)
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
		"Store.Project": func(fixture *recoveryFixture, _ string) {
			fixture.daemon.successSourceProject = func(context.Context, kernel.ProjectID) (kernel.Project, bool, error) {
				return kernel.Project{}, false, errors.New("injected Project read failure")
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

// TestSettleRunRefusesUnverifiablePublishedChange pins the retained arm's
// refusal: a durably published change whose tree cannot be re-read settles
// nothing, and the run stays finalizing and discoverable.
func TestSettleRunRefusesUnverifiablePublishedChange(t *testing.T) {
	fixture := newRecoveryFixtureWithRole(t, 0x80, kernel.RoleWorker)
	ctx := context.Background()
	run := fixture.currentRun(t)
	if run.ChangeID == nil {
		t.Fatal("worker run without candidate change")
	}
	changeState, found, err := fixture.store.Change(ctx, *run.ChangeID)
	if err != nil || !found {
		t.Fatalf("change: found=%v err=%v", found, err)
	}
	format, err := kernel.NewObjectFormat("sha1")
	if err != nil {
		t.Fatal(err)
	}
	commit, err := kernel.NewCommitID(format, bytes.Repeat([]byte{0x81}, 20))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := kernel.TreeDigestFromBytes(bytes.Repeat([]byte{0x82}, kernel.DigestBytes))
	if err != nil {
		t.Fatal(err)
	}
	repository, err := kernel.NewFileIdentity(7, 8)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := kernel.NewChangeSelection(format, commit, digest, 1, 64, repository)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := kernel.NewFileIdentity(9, 10)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := fixture.store.RecordChangePrepared(ctx, changeState.ID, changeState.Revision, selection, tree, mustKernelTime(t, 300))
	if err != nil {
		t.Fatal(err)
	}
	availability, err := kernel.NewChangeAvailability(digest, 1, 64, tree)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.MarkChangeAvailable(ctx, changeState.ID, prepared.Revision, availability, mustKernelTime(t, 310)); err != nil {
		t.Fatal(err)
	}
	before := fixture.failBeforeRuntime(t)
	settled, err := fixture.daemon.settleRun(context.Background(), fixture.changeParent, fixture.run.ID)
	if !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("unverifiable published change settlement = %+v, %v", settled, err)
	}
	after := fixture.currentRun(t)
	if after.Phase != kernel.RunFinalizing || after.Revision != before.Revision {
		t.Fatalf("refused settlement mutated the run: %+v -> %+v", before, after)
	}
}

// A worker's edits are what is published. The selection recorded when the
// change became available is the tree before the worker ran, so a run the
// daemon settles after its own finalize did not (a restart, a scheduler
// pass) must take the tree as the worker left it, not refuse it for
// differing from that selection.
func TestRecoverySettlesThePublishedTreeAsTheWorkerLeftIt(t *testing.T) {
	fixture := newRecoveryFixtureWithRole(t, 0x84, kernel.RoleWorker)
	ctx := context.Background()
	fixture.stageRuntime(t)
	fixture.beginRunnerStart(t)
	fixture.activateRunner(t)
	fixture.writeMarker(t, runner.OuterActivationMarkerName)
	run := fixture.currentRun(t)
	if run.ChangeID == nil {
		t.Fatal("worker run without candidate change")
	}
	changeState, found, err := fixture.store.Change(ctx, *run.ChangeID)
	if err != nil || !found {
		t.Fatalf("change: found=%v err=%v", found, err)
	}
	// One real published tree with one file, as the daemon makes it.
	format, err := change.NewObjectFormat("sha1")
	if err != nil {
		t.Fatal(err)
	}
	base, err := change.NewObjectID(format, bytes.Repeat([]byte{0x55}, 20))
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("right\n")
	sum := sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(content))), content...))
	oid, err := change.NewObjectID(format, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	entry, err := change.NewEntry([]byte("nested/a"), "100644", uint64(len(content)), oid)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := change.NewManifest(format, base, []change.Entry{entry})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := change.Prepare(ctx, fixture.changeParent, changeState.ID.String(), changeState.ID.String()+".stage")
	if err != nil {
		t.Fatal(err)
	}
	published, err := prepared.PopulateAndPublish(ctx, manifest, func(context.Context, change.ObjectID) ([]byte, error) { return bytes.Clone(content), nil })
	if err != nil {
		t.Fatal(err)
	}
	facts := published.Facts()
	tree, err := kernelStageIdentity(prepared.Identity())
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
	kernelFormat, err := kernel.NewObjectFormat("sha1")
	if err != nil {
		t.Fatal(err)
	}
	commit, err := kernel.NewCommitID(kernelFormat, base.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	digest, err := kernel.TreeDigestFromBytes(facts.Commitment().Bytes())
	if err != nil {
		t.Fatal(err)
	}
	repository, err := kernel.NewFileIdentity(7, 8)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := kernel.NewChangeSelection(kernelFormat, commit, digest, uint32(facts.EntryCount()), facts.BlobBytes(), repository)
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := fixture.store.RecordChangePrepared(ctx, changeState.ID, changeState.Revision, selection, tree, mustKernelTime(t, 300))
	if err != nil {
		t.Fatal(err)
	}
	availability, err := kernelAvailability(facts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.MarkChangeAvailable(ctx, changeState.ID, recorded.Revision, availability, mustKernelTime(t, 310)); err != nil {
		t.Fatal(err)
	}
	// The provider runs, adds a file and reports success; then it and the
	// runner are gone, and the runner's result says the provider converged.
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
	made := filepath.Join(fixture.changeParent, changeState.ID.String(), "nested", "b")
	if err := os.WriteFile(made, []byte("made\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(made, 0o644); err != nil {
		t.Fatal(err)
	}
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
	if err != nil || !found || retained.Phase != kernel.ChangeRetained || retained.Selection == nil {
		t.Fatalf("retained change phase=%v selection=%v, found=%v, %v", retained.Phase, retained.Selection != nil, found, err)
	}
	if retained.Selection.EntryCount() != uint32(facts.EntryCount())+1 || retained.Selection.TotalBytes() != facts.BlobBytes()+5 {
		t.Fatalf("retained selection entries=%d bytes=%d, want %d and %d", retained.Selection.EntryCount(), retained.Selection.TotalBytes(), facts.EntryCount()+1, facts.BlobBytes()+5)
	}
}

// The same recovery with a tree the inspection refuses ends the run as a
// visible source failure instead of leaving it finalizing for good.
func TestRecoveryRefusedPublicationFailsTheRunVisibly(t *testing.T) {
	fixture := newRecoveryFixtureWithRole(t, 0x94, kernel.RoleWorker)
	ctx := context.Background()
	fixture.stageRuntime(t)
	fixture.beginRunnerStart(t)
	fixture.activateRunner(t)
	fixture.writeMarker(t, runner.OuterActivationMarkerName)
	run := fixture.currentRun(t)
	changeState, found, err := fixture.store.Change(ctx, *run.ChangeID)
	if err != nil || !found {
		t.Fatalf("change: found=%v err=%v", found, err)
	}
	format, err := change.NewObjectFormat("sha1")
	if err != nil {
		t.Fatal(err)
	}
	base, err := change.NewObjectID(format, bytes.Repeat([]byte{0x55}, 20))
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("right\n")
	sum := sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(content))), content...))
	oid, err := change.NewObjectID(format, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	entry, err := change.NewEntry([]byte("nested/a"), "100644", uint64(len(content)), oid)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := change.NewManifest(format, base, []change.Entry{entry})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := change.Prepare(ctx, fixture.changeParent, changeState.ID.String(), changeState.ID.String()+".stage")
	if err != nil {
		t.Fatal(err)
	}
	published, err := prepared.PopulateAndPublish(ctx, manifest, func(context.Context, change.ObjectID) ([]byte, error) { return bytes.Clone(content), nil })
	if err != nil {
		t.Fatal(err)
	}
	facts := published.Facts()
	tree, err := kernelStageIdentity(prepared.Identity())
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
	kernelFormat, err := kernel.NewObjectFormat("sha1")
	if err != nil {
		t.Fatal(err)
	}
	commit, err := kernel.NewCommitID(kernelFormat, base.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	digest, err := kernel.TreeDigestFromBytes(facts.Commitment().Bytes())
	if err != nil {
		t.Fatal(err)
	}
	repository, err := kernel.NewFileIdentity(7, 8)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := kernel.NewChangeSelection(kernelFormat, commit, digest, uint32(facts.EntryCount()), facts.BlobBytes(), repository)
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := fixture.store.RecordChangePrepared(ctx, changeState.ID, changeState.Revision, selection, tree, mustKernelTime(t, 300))
	if err != nil {
		t.Fatal(err)
	}
	availability, err := kernelAvailability(facts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.MarkChangeAvailable(ctx, changeState.ID, recorded.Revision, availability, mustKernelTime(t, 310)); err != nil {
		t.Fatal(err)
	}
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
	// The worker leaves an empty directory, which no git tree can hold.
	if err := os.Mkdir(filepath.Join(fixture.changeParent, changeState.ID.String(), "left-empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	success, err := kernel.NewSuccessProposal("made a directory")
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
		!strings.Contains(settled.Terminal.Detail(), "empty or unselected prepared directory") {
		t.Fatalf("recovered run = %+v", settled.Terminal)
	}
	abandoned, found, err := fixture.store.Change(ctx, changeState.ID)
	if err != nil || !found || abandoned.Phase != kernel.ChangeAbandoned {
		t.Fatalf("change after refusal = %+v, found=%v, %v", abandoned, found, err)
	}
	aside := changeState.ID.String() + ".refused-" + fixture.run.ID.String()[:8]
	if _, err := os.Stat(filepath.Join(fixture.changeParent, aside, "left-empty")); err != nil || !strings.Contains(settled.Terminal.Detail(), "changes/"+aside) {
		t.Fatalf("refused tree aside: %v, detail %q", err, settled.Terminal.Detail())
	}
	if _, err := os.Lstat(filepath.Join(fixture.changeParent, changeState.ID.String())); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the Change's own name is still taken: %v", err)
	}
}

// A refusal whose move was made but whose settlement did not commit is
// settled on the next pass from the moved tree's presence alone.
func TestSettleRunFinishesARefusalMovedEarlier(t *testing.T) {
	fixture := newRecoveryFixtureWithRole(t, 0x98, kernel.RoleWorker)
	ctx := context.Background()
	run := fixture.currentRun(t)
	changeState, found, err := fixture.store.Change(ctx, *run.ChangeID)
	if err != nil || !found {
		t.Fatalf("change: found=%v err=%v", found, err)
	}
	format, err := kernel.NewObjectFormat("sha1")
	if err != nil {
		t.Fatal(err)
	}
	commit, err := kernel.NewCommitID(format, bytes.Repeat([]byte{0x99}, 20))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := kernel.TreeDigestFromBytes(bytes.Repeat([]byte{0x9a}, kernel.DigestBytes))
	if err != nil {
		t.Fatal(err)
	}
	repository, err := kernel.NewFileIdentity(7, 8)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := kernel.NewChangeSelection(format, commit, digest, 1, 64, repository)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := kernel.NewFileIdentity(9, 10)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := fixture.store.RecordChangePrepared(ctx, changeState.ID, changeState.Revision, selection, tree, mustKernelTime(t, 300))
	if err != nil {
		t.Fatal(err)
	}
	availability, err := kernel.NewChangeAvailability(digest, 1, 64, tree)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.MarkChangeAvailable(ctx, changeState.ID, prepared.Revision, availability, mustKernelTime(t, 310)); err != nil {
		t.Fatal(err)
	}
	before := fixture.failBeforeRuntime(t)
	// Nothing at the Change's own name and nothing moved aside: not settleable.
	if settled, err := fixture.daemon.settleRun(context.Background(), fixture.changeParent, fixture.run.ID); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("missing tree with nothing aside = %+v, %v", settled, err)
	}
	if after := fixture.currentRun(t); after.Phase != kernel.RunFinalizing || after.Revision != before.Revision {
		t.Fatalf("refused settlement mutated the run: %+v -> %+v", before, after)
	}
	// A durable settlement refusal keeps the exact-run continuation alive,
	// but cancellation must release it without inventing a terminal outcome.
	retryContext, cancelRetry := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancelRetry()
	if err := fixture.daemon.ContinueUnsettledRun(retryContext, fixture.parent, fixture.changeParent, fixture.run.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("conflicting continuation = %v", err)
	}
	if after := fixture.currentRun(t); after.Phase != kernel.RunFinalizing || after.Revision != before.Revision {
		t.Fatalf("cancelled continuation changed durable outcome: %+v", after)
	}
	aside := filepath.Join(fixture.changeParent, changeState.ID.String()+".refused-"+fixture.run.ID.String()[:8])
	if err := os.Mkdir(aside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := fixture.daemon.ContinueUnsettledRun(ctx, fixture.parent, fixture.changeParent, fixture.run.ID); err != nil {
		t.Fatalf("continuation after removing refusal: %v", err)
	}
	settled, err := fixture.daemon.settleRun(context.Background(), fixture.changeParent, fixture.run.ID)
	if err != nil || settled.Phase != kernel.RunTerminal || settled.Terminal == nil || settled.Terminal.Code() != kernel.FailureSource ||
		!strings.Contains(settled.Terminal.Detail(), "earlier pass") || !strings.Contains(settled.Terminal.Detail(), filepath.Base(aside)) {
		t.Fatalf("settlement of an earlier refusal = %+v, %v", settled, err)
	}
	if abandoned, found, err := fixture.store.Change(ctx, changeState.ID); err != nil || !found || abandoned.Phase != kernel.ChangeAbandoned {
		t.Fatalf("change after the earlier refusal = %+v, found=%v, %v", abandoned, found, err)
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
