package daemon

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
	"github.com/dark-factory-build/dark-factory/internal/review"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

// #1520, #1637: the App publishes its own commit of a Change, so a conflict
// names a pull request head the retained Change never has. factoryd rebases
// that Change itself, without a worker turn, and only once: a Change already
// on main has nothing to move.
func TestConflictRepairRebasesAnAppPublishedPullsChangeOnce(t *testing.T) {
	fixture, changeState, path, repository, local, pr := appPublishedChange(t)
	ctx, git := context.Background(), change.TrustedGitExecutable
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

	// #1800: the App's commit of the rebased head is no new work; a pull
	// request still at the old tree lacks the settled head. A retryable
	// publish failure holds the pass's publication back, not its reconciler's.
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
	for _, c := range []struct{ tree, want string }{{head, ""}, {local, head}} {
		app := strings.TrimSpace(settlementGit(t, git, path, "commit-tree", c.tree+"^{tree}", "-p", main, "-m", "app"))
		backend := &checkoutReviewBackend{dir: path}
		if settled, err := fixture.daemon.unpublishedHead(ctx, backend, open, review.Request{Head: app}); err != nil || settled != c.want {
			t.Fatalf("pull request at the tree of %s: unpublished head %q err=%v, want %q", c.tree, settled, err, c.want)
		}
	}

	// Its reconciler, while the stale head's checks are still pending,
	// publishes the settled head before judging that head: a refusal leaves
	// it unescalated for the next pass, which publishes the settled tree.
	if err := fixture.store.BindRepositoryGitHubID(ctx, kernel.RepositoryID(changeState.ProjectID), 42); err != nil {
		t.Fatal(err)
	}
	if err := fixture.home.WriteMaintainerCredential([]byte(`{"disabled":true}`)); err != nil {
		t.Fatal(err)
	}
	if fixture.daemon.github, err = maintainer.OpenHost(fixture.home); err != nil || !fixture.daemon.customerMaintainer() {
		t.Fatalf("customer host: %v", err)
	}
	stale := strings.TrimSpace(settlementGit(t, git, path, "commit-tree", local+"^{tree}", "-p", main, "-m", "app"))
	app := &fakePublishMaintainer{main: main, journal: map[string]json.RawMessage{}, refuse: "made.txt", externalRefuse: true}
	backend := &publishingReviewBackend{checkoutReviewBackend{dir: path}, app}
	backend.pull = &review.Pull{Head: stale, State: "open", Pending: true}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	op := review.Operation{ID: "op-1800", Request: review.Request{Repository: "team/repo", PullNumber: 12, Head: stale, Base: main, BaseRef: "main"}, State: "completed", CreatedAt: time.UnixMilli(100), UpdatedAt: time.UnixMilli(100)}
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
		if _, err := fixture.daemon.reconcilePull(ctx, stalePull, false, false); err != nil {
			t.Fatal(err)
		}
		if document, found, err := fixture.store.ReviewOperation(ctx, changeState.ProjectID, op.ID); err != nil || found && strings.Contains(string(document), "escalation") {
			t.Fatalf("stale head record = %s %v", document, err)
		}
	}
	attempt := "factoryd: publish change " + changeState.ID.String() + " at " + head + " for task " + task.ID.String() + ": "
	pass()
	if len(app.writes) != 1 || !strings.Contains(log.String(), attempt) || !strings.Contains(log.String(), "UNPROCESSABLE") {
		t.Fatalf("refused pass: writes %d, log %q", len(app.writes), log.String())
	}
	app.refuse = ""
	pass()
	commit := app.writes[len(app.writes)-1]["arguments"].(map[string]any)
	made := false
	for _, entry := range commit["changes"].([]map[string]any) {
		made = made || entry["path"] == "made.txt" && entry["content_base64"] == base64.StdEncoding.EncodeToString([]byte("made\n"))
	}
	if !made || !strings.Contains(log.String(), "factoryd: published change "+changeState.ID.String()+" at "+head) {
		t.Fatalf("published pass: commit %+v, log %q", commit, log.String())
	}
	if found, err := fixture.store.PublishableChanges(ctx, mustKernelTime(t, 1<<40)); err != nil || len(found) != 0 {
		t.Fatalf("published correction still unpublished: %+v %v", found, err)
	}
}

// #1784: a correction whose head lacks the main its pull request already
// carries would revert that main. publication writes nothing and sends the
// task back to rebase.
func TestACorrectionLackingItsPullRequestBaseGoesBackToRebase(t *testing.T) {
	fixture, changeState, _, repository, _, pr := appPublishedChange(t)
	ctx, git := context.Background(), change.TrustedGitExecutable
	fixture.daemon.now = func() time.Time { return time.UnixMilli(10_000) }
	open := kernel.OpenPull{Project: changeState.ProjectID, Repository: "team/repo", Pull: pr, Task: changeState.TaskID}
	// The correction: the Change, rebased onto main after its publication.
	if moved, err := fixture.daemon.rebaseChange(ctx, open); err != nil || !moved {
		t.Fatalf("rebased=%v err=%v", moved, err)
	}
	// main moves again, and the pull request's branch takes it in.
	if err := os.WriteFile(filepath.Join(repository, "later.txt"), []byte("later\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	settlementGit(t, git, repository, "add", "later.txt")
	settlementGit(t, git, repository, "commit", "-q", "-m", "main moved again")
	settlementGit(t, git, repository, "push", "-q", "origin", "HEAD:refs/heads/main", "+HEAD:refs/heads/"+pr.Branch)
	main := strings.TrimSpace(settlementGit(t, git, repository, "rev-parse", "HEAD"))

	task, _, err := fixture.store.Task(ctx, changeState.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	found, err := fixture.store.PublishableChanges(ctx, mustKernelTime(t, 10_000))
	if err != nil || len(found) != 1 || found[0].Task.ID != task.ID || found[0].Pull != pr.Number || task.Status != kernel.TaskSucceeded {
		t.Fatalf("publishable correction = %+v, task %+v, err %v", found, task, err)
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
	app := &fakePublishMaintainer{main: main, tip: main, journal: map[string]json.RawMessage{}}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return &publishingReviewBackend{fakePublishMaintainer: app} }
	if !fixture.daemon.publishSettledChanges(ctx, task.ID) {
		t.Fatal("the correction was not tried")
	}
	sent, _, err := fixture.store.Task(ctx, task.ID)
	if feedback := kernel.TaskFeedback(sent); err != nil || len(app.writes) != 0 || sent.Status != kernel.TaskQueued || sent.WorkRevision.Int64() != task.WorkRevision.Int64()+1 ||
		!strings.Contains(feedback, "Rebase the Change onto origin/main") || !strings.Contains(feedback, main) {
		t.Fatalf("writes %+v, sent back task %+v, err %v", app.writes, sent, err)
	}
}

// appPublishedChange is a succeeded worker whose retained Change, one commit
// on the registered repository's main, factoryd published to #12 (pr), the
// App's commit of it being that pull request's head; main has since moved on.
// It returns the Change's worktree path, the registered repository (whose
// origin is a bare clone), and the Change's settled head local.
func appPublishedChange(t *testing.T) (*recoveryFixture, kernel.Change, string, string, string, kernel.ProductionPullRequest) {
	t.Helper()
	fixture := newRecoveryFixtureFor(t, 0x9c, kernel.RoleWorker, kernel.ProviderCodex) // a shell task takes no send-back note
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

	// factoryd published the Change to #12, whose head is the App's commit.
	pr := kernel.ProductionPullRequest{Number: 12, Title: "Ship it", URL: "https://github.com/team/repo/pull/12", Head: local, Branch: "factory/" + changeState.ID.String()[:12], Base: "main", State: "open", Review: kernel.ProductionReview{Head: local, State: "unknown"}}
	if err := fixture.store.RecordPublication(ctx, changeState.ProjectID, changeState.TaskID, "team/repo", pr, mustKernelTime(t, 510)); err != nil {
		t.Fatal(err)
	}
	published := strings.Repeat("d", 40)
	pr.Head, pr.Review.Head = published, published
	if err := fixture.store.RecordProductionObservation(ctx, changeState.ProjectID, kernel.ProductionObservation{Repository: "team/repo", ObservedAt: 520, PullRequests: []kernel.ProductionPullRequest{pr}}, mustKernelTime(t, 520)); err != nil {
		t.Fatal(err)
	}
	return fixture, changeState, path, repository, local, pr
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
