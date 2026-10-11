package daemon

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/review"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

// #1520, #1637: the App publishes its own commit of a Change, so a conflict
// names a pull request head the retained Change never has. factoryd rebases
// that Change itself, without a worker turn; a conflict observed at a head the
// pull request has since left rebases nothing.
func TestConflictRepairRebasesAnAppPublishedPullOnlyAtItsCurrentHead(t *testing.T) {
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
	settlementGit(t, git, repository, "remote", "add", "origin", origin)
	settlementGit(t, git, repository, "config", "protocol.file.allow", "always")
	source, err := inspectRegisteredRepository(ctx, repository, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
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
	published, newer := strings.Repeat("d", 40), strings.Repeat("c", 40)
	pr.Head, pr.Review.Head = published, published
	if err := fixture.store.RecordProductionObservation(ctx, changeState.ProjectID, kernel.ProductionObservation{Repository: "team/repo", ObservedAt: 520, PullRequests: []kernel.ProductionPullRequest{pr}}, mustKernelTime(t, 520)); err != nil {
		t.Fatal(err)
	}
	task, _, err := fixture.store.Task(ctx, changeState.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.daemon.now = func() time.Time { return time.UnixMilli(10_000) }
	conflict := func(head string) review.Operation {
		return review.Operation{State: "ejected", RoutePending: true, Detail: "Exact head " + head + " conflicts with main.", Request: review.Request{Repository: "team/repo", PullNumber: 12, Head: head}}
	}
	retainedHead := func() string {
		state, _, err := fixture.store.Change(ctx, changeState.ID)
		if err != nil || state.HeadCommit == nil {
			t.Fatalf("change=%+v err=%v", state, err)
		}
		return hex.EncodeToString(state.HeadCommit.Bytes())
	}

	if rebased, err := fixture.daemon.tryAutoRebase(ctx, changeState.ProjectID, "team/repo", conflict(newer)); err != nil || rebased || retainedHead() != local || strings.TrimSpace(settlementGit(t, git, path, "rev-parse", "HEAD")) != local {
		t.Fatalf("stale conflict rebased=%v err=%v head=%s, want %s untouched", rebased, err, retainedHead(), local)
	}
	if rebased, err := fixture.daemon.tryAutoRebase(ctx, changeState.ProjectID, "team/repo", conflict(published)); err != nil || !rebased {
		t.Fatalf("published conflict rebased=%v err=%v", rebased, err)
	}
	head := retainedHead()
	main := strings.TrimSpace(settlementGit(t, git, repository, "rev-parse", "HEAD"))
	if head == local || strings.TrimSpace(settlementGit(t, git, path, "rev-parse", "HEAD")) != head || strings.TrimSpace(settlementGit(t, git, path, "rev-parse", "HEAD~1")) != main {
		t.Fatalf("rebased head %s, want a new head on main %s", head, main)
	}
	if after, _, err := fixture.store.Task(ctx, task.ID); err != nil || after.WorkRevision != task.WorkRevision || after.Status != task.Status {
		t.Fatalf("task after repair=%+v err=%v, want unchanged %+v", after, err, task)
	}
}
