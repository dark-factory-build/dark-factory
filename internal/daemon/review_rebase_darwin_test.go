package daemon

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
	"github.com/dark-factory-build/dark-factory/internal/review"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

// #1520, #1637: factoryd rebases a published Change on a conflict itself,
// without a worker turn, and only once: a Change already on main has nothing
// to move.
func TestConflictRepairRebasesAnAppPublishedPullsChangeOnce(t *testing.T) {
	fixture := newRecoveryFixtureWithRole(t, 0x9c, kernel.RoleWorker)
	ctx := context.Background()
	fixture.stageRuntime(t)
	fixture.beginRunnerStart(t)
	fixture.activateRunner(t)
	fixture.writeMarker(t, runner.OuterActivationMarkerName)
	changeState, path := fixture.settlementWorktree(t)
	git := change.TrustedGitExecutable
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
	if err := os.WriteFile(filepath.Join(path, "made.txt"), []byte("made\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	settlementGit(t, git, path, "add", "made.txt")
	settlementGit(t, git, path, "commit", "-q", "-m", "made a file")
	local := strings.TrimSpace(settlementGit(t, git, path, "rev-parse", "HEAD"))
	success, err := kernel.NewSuccessProposal("made a file")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.ProposeAttemptOutcome(ctx, fixture.keys.AttemptDigest, success, mustKernelTime(t, 500)); err != nil {
		t.Fatal(err)
	}
	fixture.writeArtifact(t, []byte(fmt.Sprintf(`{"version":1,"attempt_id":%q,"kind":"inner_converged","proof":%q,"process":{"pid":99995,"pgid":99995,"birth":{"seconds":1700,"microseconds":4}},"exit":{"code":0}}`, fixture.run.ID.String(), hex.EncodeToString(fixture.proof[:]))))
	if disposition := fixture.sweep(t); disposition.Action != RecoveredResultConsumed || disposition.Err != nil {
		t.Fatalf("disposition = %+v", disposition)
	}

	// main moves on without touching the Change's file.
	repository := filepath.Join(filepath.Dir(filepath.Dir(fixture.parentPath)), "repo")
	if err := os.WriteFile(filepath.Join(repository, "main.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	settlementGit(t, git, repository, "add", "main.txt")
	settlementGit(t, git, repository, "commit", "-q", "-m", "main moved")
	origin := filepath.Join(filepath.Dir(repository), "origin.git")
	settlementGit(t, git, repository, "clone", "-q", "--bare", repository, origin)
	settlementGit(t, git, origin, "update-ref", "refs/heads/main", "HEAD") // the published base branch
	settlementGit(t, git, repository, "remote", "add", "origin", origin)
	settlementGit(t, git, repository, "config", "protocol.file.allow", "always")
	source, err := inspectRegisteredRepository(ctx, repository, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	source.PublicationRepository = "team/repo"
	if err := fixture.store.BindRepositorySource(ctx, kernel.RepositoryID(changeState.ProjectID), source); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.InitializeRepositoryBase(ctx, "HEAD"); err != nil {
		t.Fatal(err)
	}

	// factoryd published the Change's exact head to #12.
	pr := kernel.ProductionPullRequest{Number: 12, Title: "Ship it", URL: "https://github.com/team/repo/pull/12", Head: local, Branch: "factory/" + changeState.ID.String()[:12], Base: "main", State: "open", Review: kernel.ProductionReview{Head: local, State: "unknown"}}
	if err := fixture.store.RecordPublication(ctx, changeState.ProjectID, changeState.TaskID, "team/repo", pr, mustKernelTime(t, 510)); err != nil {
		t.Fatal(err)
	}
	task, _, err := fixture.store.Task(ctx, changeState.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.daemon.now = func() time.Time { return time.UnixMilli(10_000) }
	open := kernel.OpenPull{Project: changeState.ProjectID, Repository: "team/repo", Pull: pr, Task: changeState.TaskID}
	retainedHead := func() string {
		state, _, err := fixture.store.Change(ctx, changeState.ID)
		if err != nil || state.HeadCommit == nil {
			t.Fatalf("change=%+v err=%v", state, err)
		}
		return hex.EncodeToString(state.HeadCommit.Bytes())
	}

	if moved, err := fixture.daemon.rebaseChange(ctx, open); err != nil || !moved {
		t.Fatalf("published conflict rebased=%v err=%v", moved, err)
	}
	head := retainedHead()
	main := strings.TrimSpace(settlementGit(t, git, repository, "rev-parse", "HEAD"))
	if head == local || strings.TrimSpace(settlementGit(t, git, path, "rev-parse", "HEAD")) != head || strings.TrimSpace(settlementGit(t, git, path, "rev-parse", "HEAD~1")) != main {
		t.Fatalf("rebased head %s, want a new head on main %s", head, main)
	}
	if after, _, err := fixture.store.Task(ctx, task.ID); err != nil || after.WorkRevision != task.WorkRevision || after.Status != task.Status {
		t.Fatalf("task after repair=%+v err=%v, want unchanged %+v", after, err, task)
	}
	if moved, err := fixture.daemon.rebaseChange(ctx, open); err != nil || moved || retainedHead() != head {
		t.Fatalf("a Change already on main rebased=%v err=%v head=%s, want %s", moved, err, retainedHead(), head)
	}

	// #1800: a pull request still at the old head lacks the settled head. A
	// retryable publish failure holds the pass's publication back, not its
	// reconciler's.
	settledState, _, err := fixture.store.Change(ctx, changeState.ID)
	if err != nil {
		t.Fatal(err)
	}
	failure := kernel.PublishFailureID(changeState.ID, settledState.Revision)
	if err := fixture.store.RecordReviewOperation(ctx, changeState.ProjectID, "team/repo", failure, map[string]any{"id": failure, "state": "publish_failed", "retryable": true, "handled": true, "request": map[string]any{}}, mustKernelTime(t, 10_000)); err != nil {
		t.Fatal(err)
	}
	if fixture.daemon.publishSettledChanges(ctx, kernel.TaskID{}) {
		t.Fatal("a pass published inside its retry window")
	}
	for _, c := range []struct{ pull, want string }{{head, ""}, {strings.ToUpper(head), ""}, {local, head}} {
		if settled, err := fixture.daemon.unpublishedHead(ctx, open, review.Request{Head: c.pull}); err != nil || settled != c.want {
			t.Fatalf("pull request at %s: unpublished head %q err=%v, want %q", c.pull, settled, err, c.want)
		}
	}

	// Its reconciler, while the stale head's checks are still pending,
	// publishes the settled head before judging that head: a refusal leaves
	// it unescalated for the next pass, which pushes the settled head.
	if err := fixture.store.BindRepositoryGitHubID(ctx, kernel.RepositoryID(changeState.ProjectID), 42); err != nil {
		t.Fatal(err)
	}
	if err := fixture.home.WriteMaintainerCredential([]byte(`{"disabled":true}`)); err != nil {
		t.Fatal(err)
	}
	if fixture.daemon.github, err = maintainer.OpenHost(fixture.home); err != nil || !fixture.daemon.customerMaintainer() {
		t.Fatalf("customer host: %v", err)
	}
	stale := local
	bare := filepath.Join(t.TempDir(), "github.git")
	settlementGit(t, git, repository, "clone", "-q", "--bare", "--shared", repository, bare)
	app := &fakePublishMaintainer{t: t, bare: bare, main: main, tip: stale, journal: map[string]json.RawMessage{}, refuse: "made.txt", externalRefuse: true}
	backend := &publishingReviewBackend{checkoutReviewBackend{dir: path}, app}
	backend.pull = &review.Pull{Head: stale, State: "open", Pending: true, MergeStateStatus: "DIRTY"}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	op := review.Operation{ID: "op-1800", Request: review.Request{Repository: "team/repo", PullNumber: 12, Head: stale, Base: main, BaseRef: "main"}, State: "completed", Rebased: true, CreatedAt: time.UnixMilli(10_000), UpdatedAt: time.UnixMilli(10_000)}
	document, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	stalePull := open
	stalePull.Pull.Head, stalePull.Operations = stale, []json.RawMessage{document}
	var log strings.Builder
	fixture.daemon.log = &log
	pass := func() {
		t.Helper()
		if _, err := fixture.daemon.reconcilePull(ctx, stalePull, false); err != nil {
			t.Fatal(err)
		}
		if after, _, err := fixture.store.Task(ctx, task.ID); err != nil || after.Revision != task.Revision || after.WorkRevision != task.WorkRevision {
			t.Fatalf("stale head sent back task: %+v %v", after, err)
		}
		if document, found, err := fixture.store.ReviewOperation(ctx, changeState.ProjectID, op.ID); err != nil || found && strings.Contains(string(document), "escalation") {
			t.Fatalf("stale head record = %s %v", document, err)
		} else if found {
			var recorded review.Operation
			if err := json.Unmarshal(document, &recorded); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(recorded.LastAction, "send it back") {
				t.Fatalf("stale head attempted send-back: %s", recorded.LastAction)
			}
		}
	}
	attempt := "factoryd: publish change " + changeState.ID.String() + " at " + head + " for task " + task.ID.String() + ": "
	pass()
	if len(app.writes) != 1 || !strings.Contains(log.String(), attempt) || !strings.Contains(log.String(), "UNPROCESSABLE") {
		t.Fatalf("refused pass: writes %d, log %q", len(app.writes), log.String())
	}
	app.refuse = ""
	pass()
	var push map[string]any
	for _, write := range app.writes {
		if write["name"] == "publish_pack" {
			push = write
		}
	}
	if push == nil || push["arguments"].(map[string]any)["expected_head_sha"] != stale || app.tip != head ||
		!strings.Contains(log.String(), "factoryd: published change "+changeState.ID.String()+" at "+head) {
		t.Fatalf("published pass: push %+v at %s, log %q", push, app.tip, log.String())
	}
	if found, err := fixture.store.PublishableChanges(ctx, mustKernelTime(t, 1<<40)); err != nil || len(found) != 0 {
		t.Fatalf("published correction still unpublished: %+v %v", found, err)
	}
}

// The failed-check recovery must remain actionable after the reconciler's
// bounded rebase failures, while the pull observation stays unchanged.
func TestFailedCheckRecoveryAfterRepeatedReconcileFailures(t *testing.T) {
	fixture := newRecoveryFixtureWithRole(t, 0xad, kernel.RoleWorker)
	ctx := context.Background()
	fixture.stageRuntime(t)
	fixture.beginRunnerStart(t)
	fixture.activateRunner(t)
	fixture.writeMarker(t, runner.OuterActivationMarkerName)
	changeState, path := fixture.settlementWorktree(t)
	g := change.TrustedGitExecutable
	identity, err := processResourceIdentity(runner.Identity{PID: 99996, PGID: 99996, Birth: runner.Birth{Seconds: 1701, Microseconds: 4}})
	if err != nil {
		t.Fatal(err)
	}
	states := fixture.resourceStates(t)
	process, group := states[kernel.ResourceProviderProcess], states[kernel.ResourceProviderGroup]
	if _, _, err := fixture.store.ActivateProviderResources(ctx, fixture.run.ID, process.ID, process.Revision, group.ID, group.Revision, identity, mustKernelTime(t, 450)); err != nil {
		t.Fatal(err)
	}
	session, found, err := fixture.store.TerminalSessionForRun(ctx, fixture.run.ID)
	if err != nil || !found {
		t.Fatalf("session: found=%v err=%v", found, err)
	}
	if fixture.run, err = fixture.store.ActivateRun(ctx, fixture.run.ID, session.ID, fixture.currentRun(t).Revision, session.Revision, mustKernelTime(t, 460)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "made.txt"), []byte("made\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	settlementGit(t, g, path, "add", "made.txt")
	settlementGit(t, g, path, "commit", "-q", "-m", "made a file")
	local := strings.TrimSpace(settlementGit(t, g, path, "rev-parse", "HEAD"))
	success, err := kernel.NewSuccessProposal("made a file")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.ProposeAttemptOutcome(ctx, fixture.keys.AttemptDigest, success, mustKernelTime(t, 500)); err != nil {
		t.Fatal(err)
	}
	fixture.writeArtifact(t, []byte(fmt.Sprintf(`{"version":1,"attempt_id":%q,"kind":"inner_converged","proof":%q,"process":{"pid":99996,"pgid":99996,"birth":{"seconds":1701,"microseconds":4}},"exit":{"code":0}}`, fixture.run.ID.String(), hex.EncodeToString(fixture.proof[:]))))
	if disposition := fixture.sweep(t); disposition.Action != RecoveredResultConsumed || disposition.Err != nil {
		t.Fatalf("disposition = %+v", disposition)
	}
	if err := fixture.daemon.ContinueUnsettledRun(ctx, fixture.parent, fixture.changeParent, fixture.run.ID); err != nil {
		t.Fatal(err)
	}
	repository := filepath.Join(filepath.Dir(filepath.Dir(fixture.parentPath)), "repo")
	origin := filepath.Join(filepath.Dir(repository), "origin.git")
	settlementGit(t, g, repository, "update-ref", "refs/heads/main", local)
	settlementGit(t, g, repository, "clone", "-q", "--bare", repository, origin)
	settlementGit(t, g, origin, "update-ref", "refs/heads/main", local)
	settlementGit(t, g, repository, "remote", "add", "origin", origin)
	settlementGit(t, g, repository, "config", "protocol.file.allow", "always")
	source, err := inspectRegisteredRepository(ctx, repository, "main")
	if err != nil {
		t.Fatal(err)
	}
	source.PublicationRepository = "team/repo"
	if err := fixture.store.BindRepositorySource(ctx, kernel.RepositoryID(changeState.ProjectID), source); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.InitializeRepositoryBase(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	pr := kernel.ProductionPullRequest{Number: 12, Title: "Ship it", URL: "https://github.com/team/repo/pull/12", Head: local, Branch: "factory/" + changeState.ID.String()[:12], Base: "main", State: "open", Review: kernel.ProductionReview{Head: local, State: "unknown"}}
	if err := fixture.store.RecordPublication(ctx, changeState.ProjectID, changeState.TaskID, "team/repo", pr, mustKernelTime(t, 510)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.BindRepositoryGitHubID(ctx, kernel.RepositoryID(changeState.ProjectID), 42); err != nil {
		t.Fatal(err)
	}
	if err := fixture.home.WriteMaintainerCredential([]byte(`{"disabled":true}`)); err != nil {
		t.Fatal(err)
	}
	if fixture.daemon.github, err = maintainer.OpenHost(fixture.home); err != nil || !fixture.daemon.customerMaintainer() {
		t.Fatalf("customer host: %v", err)
	}
	backend := &checkoutReviewBackend{publicReviewBackend: publicReviewBackend{}, dir: path}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	request := api.ReviewRequest{Repository: "team/repo", PullNumber: 12, Head: local, Base: strings.Repeat("f", 40), BaseRef: "main", Provider: "codex"}
	if _, err := reviewNow(ctx, fixture.daemon, changeState.ProjectID, request); err != nil {
		t.Fatal(err)
	}
	waitForMergePipeline(t, fixture.daemon)
	backend.pull = &review.Pull{Head: local, State: "open", Failing: []string{"ci / required"}, Tests: []string{"--- FAIL: TestCorrection (0.01s)"}}
	parent := fixture.daemon.changeParent.Load()
	if parent == nil {
		t.Fatal("missing configured Change parent")
	}
	fixture.daemon.changeParent.Store(nil)
	for range review.FailuresBeforeEscalation {
		if _, err := fixture.daemon.advanceReviewOperations(ctx, false); err != nil {
			t.Fatal(err)
		}
	}
	before, _, err := fixture.store.Task(ctx, changeState.TaskID)
	if err != nil || before.WorkRevision.Int64() != 1 {
		t.Fatalf("failed actions changed worker task: %+v err=%v", before, err)
	}
	fixture.daemon.changeParent.Store(parent)
	var after kernel.Task
	for pass := 0; pass < review.FailuresBeforeEscalation; pass++ {
		if _, err := fixture.daemon.advanceReviewOperations(ctx, false); err != nil {
			t.Fatal(err)
		}
		var found bool
		after, found, err = fixture.store.Task(ctx, changeState.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if found && after.WorkRevision.Int64() == 2 && after.Status == kernel.TaskQueued {
			break
		}
	}
	if after.WorkRevision.Int64() != 2 {
		t.Fatalf("recovery did not advance task once: work revision=%d task=%+v", after.WorkRevision.Int64(), after)
	}
	if after.Status != kernel.TaskQueued {
		t.Fatalf("recovery task is not queued/actionable: status=%s task=%+v", after.Status, after)
	}
	op := lastDurableReview(t, fixture.store, changeState.ProjectID)
	feedback, marker := kernel.TaskFeedback(after), "review-operation: "+op.ID+"\n"
	if strings.Count(feedback, marker) != 1 {
		t.Fatalf("recovery feedback marker count=%d, want one: feedback=%q operation=%+v", strings.Count(feedback, marker), feedback, op)
	}
	if !strings.Contains(feedback, "Failing checks: ci / required") {
		t.Fatalf("recovery feedback lost failed-check text: %q", feedback)
	}
	if !strings.Contains(feedback, "--- FAIL: TestCorrection") {
		t.Fatalf("recovery feedback lost failed-test text: %q", feedback)
	}
}

// publishingReviewBackend is also the Maintainer factoryd publishes through.
type publishingReviewBackend struct {
	checkoutReviewBackend
	*fakePublishMaintainer
}

// checkoutReviewBackend checks every pull request out at dir.
type checkoutReviewBackend struct {
	publicReviewBackend
	dir string
}

func (b *checkoutReviewBackend) CloneReadOnly(context.Context, review.Request) (string, func(), error) {
	return b.dir, func() {}, nil
}
