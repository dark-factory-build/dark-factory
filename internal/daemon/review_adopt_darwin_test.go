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

	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/gitauthor"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
	"github.com/dark-factory-build/dark-factory/internal/review"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

// #2080: a trusted operator's correction commit on a factory-owned pull
// request branch is adopted onto the task's retained Change, exactly once and
// only when GitHub verifies every added commit as theirs; the reconciler then
// judges that head as any other. Anything less keeps the escalation.
func TestATrustedOperatorHeadIsAdoptedOntoItsChange(t *testing.T) {
	fixture := newRecoveryFixtureWithRole(t, 0x9e, kernel.RoleWorker)
	ctx := context.Background()
	fixture.stageRuntime(t)
	fixture.beginRunnerStart(t)
	fixture.activateRunner(t)
	fixture.writeMarker(t, runner.OuterActivationMarkerName)
	changeState, path := fixture.settlementWorktree(t)
	git, branch := change.TrustedGitExecutable, change.BranchName(changeState.ID.String())
	providerIdentity, err := processResourceIdentity(runner.Identity{PID: 99993, PGID: 99993, Birth: runner.Birth{Seconds: 1703, Microseconds: 4}})
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
	settled := strings.TrimSpace(settlementGit(t, git, path, "rev-parse", "HEAD"))
	success, err := kernel.NewSuccessProposal("made a file")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.ProposeAttemptOutcome(ctx, fixture.keys.AttemptDigest, success, mustKernelTime(t, 500)); err != nil {
		t.Fatal(err)
	}
	fixture.writeArtifact(t, []byte(fmt.Sprintf(`{"version":1,"attempt_id":%q,"kind":"inner_converged","proof":%q,"process":{"pid":99993,"pgid":99993,"birth":{"seconds":1703,"microseconds":4}},"exit":{"code":0}}`, fixture.run.ID.String(), hex.EncodeToString(fixture.proof[:]))))
	if disposition := fixture.sweep(t); disposition.Action != RecoveredResultConsumed || disposition.Err != nil {
		t.Fatalf("disposition = %+v", disposition)
	}

	// GitHub holds the published branch and its pull request #12; the
	// operator commits a fix on that branch there.
	repository := filepath.Join(filepath.Dir(filepath.Dir(fixture.parentPath)), "repo")
	origin := filepath.Join(filepath.Dir(repository), "origin.git")
	settlementGit(t, git, repository, "clone", "-q", "--bare", repository, origin)
	settlementGit(t, git, origin, "update-ref", "refs/heads/main", "HEAD")
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
	if err := fixture.store.BindRepositoryGitHubID(ctx, kernel.RepositoryID(changeState.ProjectID), 42); err != nil {
		t.Fatal(err)
	}
	operator := filepath.Join(t.TempDir(), "operator")
	settlementGit(t, git, filepath.Dir(operator), "clone", "-q", "--branch", branch, origin, operator)
	operatorCommit := func(name string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(operator, name), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		settlementGit(t, git, operator, "add", name)
		settlementGit(t, git, operator, "commit", "-q", "-m", name)
		return strings.TrimSpace(settlementGit(t, git, operator, "rev-parse", "HEAD"))
	}
	pullRef := func(head string) {
		settlementGit(t, git, operator, "push", "-q", "-f", "origin", head+":refs/pull/12/head")
	}
	operatorHead := operatorCommit("fix.txt")
	settlementGit(t, git, operator, "push", "-q", "origin", "HEAD:refs/heads/"+branch)
	pullRef(operatorHead)

	clock := time.Now().UnixMilli() + 60_000
	fixture.daemon.now = func() time.Time { return time.UnixMilli(clock) }
	pr := kernel.ProductionPullRequest{Number: 12, Title: "Ship it", URL: "https://github.com/team/repo/pull/12", Head: settled, HeadRepository: "team/repo", Branch: branch, Base: "main", State: "open", Review: kernel.ProductionReview{Head: settled, State: "unknown"}}
	if err := fixture.store.RecordPublication(ctx, changeState.ProjectID, changeState.TaskID, "team/repo", pr, mustKernelTime(t, clock-1000)); err != nil {
		t.Fatal(err)
	}
	if fixture.daemon.publishSettledChanges(ctx, kernel.TaskID{}) {
		t.Fatal("the settled head is published: nothing is left for adoption to decide")
	}
	raw, _ := hex.DecodeString(strings.Repeat("ab", 16))
	sourceID, _ := kernel.IntakeSourceIDFromBytes(raw)
	intake, err := fixture.store.CreateIntakeSource(ctx, kernel.NewIntakeSource{ID: sourceID, ProjectID: changeState.ProjectID, TargetRepositoryID: kernel.RepositoryID(changeState.ProjectID), GitHubRepositoryID: 43, GitHubRepositoryName: "team/issues", Policy: kernel.IntakePolicyTrustedAuthors, TrustedGitHubLogins: []string{"operator"}, PollSeconds: 60, AdmissionLimit: 5}, mustKernelTime(t, clock-900))
	if err == nil {
		_, err = fixture.store.SetIntakeSourceEnabled(ctx, intake.ID, intake.Revision, true, mustKernelTime(t, clock-800))
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.home.WriteMaintainerCredential([]byte(`{"disabled":true}`)); err != nil {
		t.Fatal(err)
	}
	if fixture.daemon.github, err = maintainer.OpenHost(fixture.home); err != nil || !fixture.daemon.customerMaintainer() {
		t.Fatalf("customer host: %v", err)
	}
	app := &fakePublishMaintainer{t: t, bare: origin, tip: operatorHead, journal: map[string]json.RawMessage{}}
	backend := &publishingReviewBackend{checkoutReviewBackend{dir: path}, app}
	backend.pull = &review.Pull{Head: operatorHead, State: "open", MergeStateStatus: "CLEAN"}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	var log strings.Builder
	fixture.daemon.log = &log
	task, _, err := fixture.store.Task(ctx, changeState.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	pr.Head = operatorHead // the refresh recorded the operator's head
	open := kernel.OpenPull{Project: changeState.ProjectID, Repository: "team/repo", Pull: pr, Task: changeState.TaskID}
	request := review.Request{Repository: "team/repo", PullNumber: 12, Head: operatorHead, Base: strings.TrimSpace(settlementGit(t, git, repository, "rev-parse", "HEAD")), BaseRef: "main", Body: "fixture body\n"}
	account := func(login string, id int64) map[string]any { return map[string]any{"login": login, "id": id} }
	commit := func(sha string, author, committer map[string]any, verified bool) map[string]any {
		return map[string]any{"sha": sha, "author": author, "committer": committer, "commit": map[string]any{"verification": map[string]any{"verified": verified, "reason": "valid"}}}
	}
	observation := func(head, status string, commits ...map[string]any) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"pull_number": 12, "head_sha": head, "base_sha": settled, "status": status, "commits": commits})
		return raw
	}
	trusted := observation(operatorHead, "ahead", commit(operatorHead, account("operator", 7), account("web-flow", 19864447), true))
	retainedHead := func() string {
		t.Helper()
		state, _, err := fixture.store.Change(ctx, changeState.ID)
		if err != nil || state.HeadCommit == nil {
			t.Fatalf("change=%+v err=%v", state, err)
		}
		return hex.EncodeToString(state.HeadCommit.Bytes())
	}
	unadopted := func(name, want string) {
		t.Helper()
		lacks, why, err := fixture.daemon.publishUnanswered(ctx, open, request)
		if err != nil || !lacks || !strings.Contains(why, "nor does it adopt that head: ") || !strings.Contains(why, want) {
			t.Fatalf("%s: lacks=%v why=%q err=%v, want a refusal naming %q", name, lacks, why, err, want)
		}
		if head := retainedHead(); head != settled || strings.TrimSpace(settlementGit(t, git, path, "rev-parse", "HEAD")) != settled {
			t.Fatalf("%s moved the Change to %s", name, head)
		}
	}

	// Refused: an untrusted account, GitHub's unverified attribution, a mixed
	// range, a range that is not a descendant, and a head the broker binds
	// elsewhere.
	app.commits = observation(operatorHead, "ahead", commit(operatorHead, account("stranger", 8), account("stranger", 8), true))
	unadopted("untrusted author", "commit "+operatorHead+" is not verified by GitHub as a trusted author's")
	app.commits = observation(operatorHead, "ahead", commit(operatorHead, account("operator", 7), account("operator", 7), false))
	unadopted("unverified commit", "commit "+operatorHead+" is not verified")
	app.commits = observation(operatorHead, "ahead", commit(operatorHead, nil, account("web-flow", 19864447), true))
	unadopted("unattributed author", "commit "+operatorHead+" is not verified")
	app.commits = observation(operatorHead, "ahead", commit(strings.Repeat("1", 40), account("operator", 7), account("operator", 7), true), commit(operatorHead, account("stranger", 8), account("stranger", 8), true))
	unadopted("mixed range", "commit "+operatorHead+" is not verified")
	app.commits = observation(operatorHead, "diverged", commit(operatorHead, account("operator", 7), account("operator", 7), true))
	unadopted("force-pushed head", "head "+operatorHead+" is diverged of the settled head")
	app.commits, app.commitsErr = nil, maintainerRejection("conflict: enqueue head binding did not match: expected "+operatorHead+", observed "+strings.Repeat("2", 40)+".")
	unadopted("stale head at the broker", "head binding did not match")
	app.commits, app.commitsErr = trusted, nil
	// Stale at GitHub itself: the pull request moved on after the broker
	// answered, so the ref no longer names the head.
	newer := operatorCommit("more.txt")
	pullRef(newer)
	unadopted("stale head at GitHub", "the pull request head is no longer "+operatorHead)
	// A head the broker calls ahead but which does not fast-forward the
	// worktree (a rewritten branch) moves nothing either.
	settlementGit(t, git, operator, "checkout", "-q", "--detach", request.Base)
	rewritten := operatorCommit("rewrite.txt")
	pullRef(rewritten)
	open.Pull.Head, request.Head = rewritten, rewritten
	app.commits = observation(rewritten, "ahead", commit(rewritten, account("operator", 7), account("web-flow", 19864447), true))
	unadopted("non-descendant head", rewritten+" does not fast-forward the Change worktree")
	open.Pull.Head, request.Head = operatorHead, operatorHead
	settlementGit(t, git, operator, "checkout", "-q", branch)
	settlementGit(t, git, operator, "reset", "-q", "--hard", operatorHead)
	pullRef(operatorHead)
	app.commits = observation(operatorHead, "ahead", commit(operatorHead, account("stranger", 8), account("stranger", 8), true))
	// The reconciler escalates the refusal once, with its reason, and neither
	// sends the task back nor enqueues.
	op := review.Operation{ID: "op-2080", Request: request, State: "completed", Verdict: "allow", Submitted: true, CreatedAt: time.UnixMilli(clock), UpdatedAt: time.UnixMilli(clock)}
	document, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	open.Operations = []json.RawMessage{document}
	if _, err := fixture.daemon.reconcilePull(ctx, open, false); err != nil {
		t.Fatal(err)
	}
	stored, found, err := fixture.store.ReviewOperation(ctx, changeState.ProjectID, op.ID)
	if err != nil || !found {
		t.Fatalf("operation: found=%v err=%v", found, err)
	}
	var recorded review.Operation
	if err := json.Unmarshal(stored, &recorded); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recorded.Escalation, "nor does it adopt that head: commit "+operatorHead) || !recorded.Handled || backend.enqueues != 0 {
		t.Fatalf("refused head: escalation %q handled=%v enqueues=%d", recorded.Escalation, recorded.Handled, backend.enqueues)
	}
	if after, _, err := fixture.store.Task(ctx, task.ID); err != nil || after.Revision != task.Revision || after.WorkRevision != task.WorkRevision || after.Status != kernel.TaskSucceeded {
		t.Fatalf("refused head changed its task: %+v %v", after, err)
	}

	// Adopted: one commit GitHub verifies as the trusted author's, committed
	// through GitHub's editor. The Change and its worktree are at that head,
	// nothing is left to publish, and nothing was enqueued by the adoption.
	app.commits, app.commitReads = trusted, 0
	if lacks, why, err := fixture.daemon.publishUnanswered(ctx, open, request); err != nil || lacks || why != "" {
		t.Fatalf("trusted head: lacks=%v why=%q err=%v", lacks, why, err)
	}
	if head := retainedHead(); head != operatorHead || strings.TrimSpace(settlementGit(t, git, path, "rev-parse", "HEAD")) != operatorHead || strings.TrimSpace(settlementGit(t, git, path, "rev-parse", "--abbrev-ref", "HEAD")) != branch {
		t.Fatalf("adopted Change at %s, want %s on %s", head, operatorHead, branch)
	}
	if found, err := fixture.store.PublishableChanges(ctx, mustKernelTime(t, 1<<40)); err != nil || len(found) != 0 {
		t.Fatalf("adopted head is unpublished: %+v %v", found, err)
	}
	if after, _, err := fixture.store.Task(ctx, task.ID); err != nil || after.Revision != task.Revision || after.WorkRevision != task.WorkRevision || after.Status != kernel.TaskSucceeded {
		t.Fatalf("adoption changed its task: %+v %v", after, err)
	}
	if backend.enqueues != 0 || app.commitReads != 1 || !strings.Contains(log.String(), "factoryd: adopted team/repo#12 head "+operatorHead+" onto change "+changeState.ID.String()) {
		t.Fatalf("adoption: enqueues=%d reads=%d log=%q", backend.enqueues, app.commitReads, log.String())
	}
	// Replayed, the same request adopts nothing again and reads nothing; a
	// newer head GitHub holds meanwhile is not adopted unseen.
	settlementGit(t, git, operator, "push", "-q", "origin", newer+":refs/heads/"+branch)
	pullRef(newer)
	for range 2 {
		if lacks, why, err := fixture.daemon.publishUnanswered(ctx, open, request); err != nil || lacks || why != "" || retainedHead() != operatorHead || app.commitReads != 1 {
			t.Fatalf("replay: lacks=%v why=%q err=%v head=%s reads=%d", lacks, why, err, retainedHead(), app.commitReads)
		}
	}
	// The reconciler then advances the adopted head on its review of record.
	op.ID = "op-2080-adopted"
	if document, err = json.Marshal(op); err != nil {
		t.Fatal(err)
	}
	open.Operations = []json.RawMessage{document}
	if _, err := fixture.daemon.reconcilePull(ctx, open, false); err != nil {
		t.Fatal(err)
	}
	var advanced review.Operation
	if stored, _, err = fixture.store.ReviewOperation(ctx, changeState.ProjectID, op.ID); err != nil || json.Unmarshal(stored, &advanced) != nil {
		t.Fatalf("operation %s: %v", stored, err)
	}
	if backend.enqueues != 1 || advanced.Escalation != "" || advanced.LastAction != "enqueue it" {
		t.Fatalf("adopted head: enqueues=%d operation=%+v", backend.enqueues, advanced)
	}
}

func TestTrustedGitHubAccountIsTheOperatorOrATrustedIntakeAuthor(t *testing.T) {
	repository, other := kernel.RepositoryID(mustProjectID(t, testID(190))), kernel.RepositoryID(mustProjectID(t, testID(191)))
	operator := gitauthor.Identity{ID: 7, Login: "operator"}
	trusting := kernel.IntakeSource{Enabled: true, TargetRepositoryID: repository, Policy: kernel.IntakePolicyTrustedAuthors, TrustedGitHubLogins: []string{"Owner"}}
	disabled, elsewhere, manual := trusting, trusting, trusting
	disabled.Enabled, elsewhere.TargetRepositoryID, manual.Policy = false, other, kernel.IntakePolicyManual
	for _, c := range []struct {
		name     string
		operator gitauthor.Identity
		sources  []kernel.IntakeSource
		account  *githubAccount
		want     bool
	}{
		{"the connection's user by id", operator, nil, &githubAccount{Login: "renamed", ID: 7}, true},
		{"the operator's login without its id", operator, nil, &githubAccount{Login: "operator", ID: 9}, false},
		{"no connection", gitauthor.Identity{}, nil, &githubAccount{Login: "anyone", ID: 0}, false},
		{"a trusted intake login, any case", gitauthor.Identity{}, []kernel.IntakeSource{trusting}, &githubAccount{Login: "owner", ID: 3}, true},
		{"a disabled source", gitauthor.Identity{}, []kernel.IntakeSource{disabled}, &githubAccount{Login: "owner", ID: 3}, false},
		{"another repository's source", gitauthor.Identity{}, []kernel.IntakeSource{elsewhere}, &githubAccount{Login: "owner", ID: 3}, false},
		{"a manual source", gitauthor.Identity{}, []kernel.IntakeSource{manual}, &githubAccount{Login: "owner", ID: 3}, false},
		{"no account", operator, []kernel.IntakeSource{trusting}, nil, false},
	} {
		if got := trustedGitHubAccount(c.operator, c.sources, repository, c.account); got != c.want {
			t.Errorf("%s: trusted=%v, want %v", c.name, got, c.want)
		}
	}
}
