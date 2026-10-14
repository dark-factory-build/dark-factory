package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
	"github.com/dark-factory-build/dark-factory/internal/review"
)

// fakePublishMaintainer is the App's journal: one result per operation id,
// a replay of a completed id answered from it.
type fakePublishMaintainer struct {
	t               *testing.T
	bare            string // GitHub's repository, for publish_pack
	pack            *packMaintainer
	main            string
	tip             string // the pull request branch's head
	journal         map[string]json.RawMessage
	planned         map[string]bool  // ids a write stopped before GitHub
	writes          []map[string]any // every write sent
	refuse          string           // a path whose push GitHub refuses
	externalRefuse  bool             // the Maintainer refuses before the App can apply it
	unavailable     bool             // the Maintainer authority is temporarily unavailable
	loseNext        bool             // the next write lands but its response is lost
	body            string           // the open pull request's stored body
	issueBody       string           // the current source issue body
	issueTitle      string           // the current source issue title
	issueRepository string           // repository used for the current source issue
}

func TestRepairPublicationUsesTaskTitle(t *testing.T) {
	want := "Repair team/repo#31"
	if got := publicationMessage(kernel.PublishableChange{Task: kernel.Task{Title: want}}); got != want {
		t.Fatalf("repair publication message = %q, want %q", got, want)
	}
}

func (f *fakePublishMaintainer) call(_ context.Context, name string, arguments map[string]any) (json.RawMessage, error) {
	id, _ := arguments["operation_id"].(string)
	switch name {
	case "observe_ref":
		if arguments["branch"] != "main" {
			return json.Marshal(map[string]any{"branch": arguments["branch"], "head_sha": f.tip})
		}
		return json.Marshal(map[string]any{"branch": "main", "head_sha": f.main})
	case "observe_operation":
		if result, ok := f.journal[id]; ok {
			return json.Marshal(map[string]any{"operation_id": id, "state": "completed", "result": result})
		}
		if f.planned[id] {
			return json.Marshal(map[string]any{"operation_id": id, "state": "planned"})
		}
		return json.Marshal(map[string]any{"operation_id": id, "state": "missing"})
	case "list_pull_requests":
		return json.Marshal(map[string]any{"pull_requests": []any{map[string]any{"number": arguments["pull_number"], "head_sha": f.tip, "body": f.body}}})
	case "observe_issue":
		f.issueRepository, _ = arguments["repository"].(string)
		return json.Marshal(map[string]any{"number": arguments["issue_number"], "title": f.issueTitle, "body": f.issueBody})
	}
	if _, replay := f.journal[id]; replay || f.planned[id] {
		return nil, fmt.Errorf("conflict: operation %s replayed", id)
	}
	f.writes = append(f.writes, map[string]any{"name": name, "arguments": arguments})
	if f.unavailable {
		return nil, maintainerRejection("unavailable: Maintainer authority is unavailable.")
	}
	var result json.RawMessage
	switch name {
	case "publish_pack":
		if f.refuse != "" {
			if f.externalRefuse {
				return nil, maintainerRejection("refused: The request was refused: rejected before execution as UNPROCESSABLE.")
			}
			return nil, fmt.Errorf("refused: %s cannot be written", f.refuse)
		}
		// Every chunk replays under one id, so the pack's own journal answers.
		if f.pack == nil || f.pack.operation != id {
			f.pack = &packMaintainer{t: f.t, bare: f.bare, operation: id}
		}
		f.pack.branch, f.pack.loseNext, f.loseNext = f.tip, f.loseNext, false
		result, err := f.pack.call(context.Background(), name, arguments)
		f.tip = f.pack.branch
		return result, err
	case "create_pull_request":
		result, _ = json.Marshal(map[string]any{"number": 31, "url": "https://github.com/team/repo/pull/31", "head_sha": arguments["head_sha"], "base_sha": arguments["base_sha"]})
	case "update_pull_request_body":
		result, _ = json.Marshal(map[string]any{"number": arguments["pull_number"], "head_sha": f.tip})
	default:
		return nil, fmt.Errorf("unexpected tool %s", name)
	}
	f.journal[id] = result
	if f.loseNext {
		f.loseNext = false
		return nil, maintainer.ErrUnavailable
	}
	return result, nil
}

// publishFixture is a repository whose main is the Change's base and whose
// worker head adds files, deletes one, and adds a workflow when asked; a
// worker task in the review fixture's project, and that Change as a candidate.
func publishFixture(t *testing.T, files int, workflow bool) (*dispatchFixture, kernel.PublishableChange, string, *fakePublishMaintainer, publishCheckout) {
	t.Helper()
	git := change.TrustedGitExecutable
	registered := t.TempDir()
	root := registered
	run := func(args ...string) string {
		return strings.TrimSpace(supervisorGitOutput(t, git, append([]string{"-C", root, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid"}, args...)...))
	}
	run("init", "-q", "-b", "main")
	for _, name := range []string{"keep.txt", "gone.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", ".")
	run("commit", "-q", "-m", "base")
	base := run("rev-parse", "HEAD")
	// As in production, the worker's commits exist only in the Change's own
	// Git directory, never in the registered repository the clone borrows.
	root = filepath.Join(t.TempDir(), "change")
	supervisorGit(t, git, "clone", "-q", registered, root)
	run("rm", "-q", "gone.txt")
	for i := range files {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%02d.txt", i)), []byte(fmt.Sprintf("line %d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if workflow {
		if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".github", "workflows", "ci.yml"), []byte("on: push\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", ".")
	run("commit", "-q", "-m", "work")
	head := run("rev-parse", "HEAD")

	fixture, project := reviewPublicFixture(t)
	ctx := context.Background()
	agent, err := fixture.store.CreateAgent(ctx, kernel.NewAgent{ID: mustAgentID(t, testID(240)), ProjectID: project, Name: "worker", Role: kernel.RoleWorker, Provider: kernel.ProviderCodex, ToolBudgetLimit: 2}, mustKernelTime(t, 1001))
	if err != nil {
		t.Fatal(err)
	}
	incarnation, err := kernel.IncarnationIDFromBytes(mustIDBytes(t, testID(241)))
	if err != nil {
		t.Fatal(err)
	}
	task, err := fixture.store.EnqueueTask(ctx, kernel.NewTask{ID: mustTaskID(t, testID(242)), IncarnationID: incarnation, ProjectID: project, AssignedAgentID: agent.ID, Title: "work"}, mustKernelTime(t, 1002))
	if err != nil {
		t.Fatal(err)
	}
	task.Result = "Implemented it; ran go test. Contact a.person@example.com, log at /Users/someone/run.log."
	changeID, err := kernel.ChangeIDFromBytes(mustIDBytes(t, testID(243)))
	if err != nil {
		t.Fatal(err)
	}
	revision, err := kernel.NewRevision(3)
	if err != nil {
		t.Fatal(err)
	}
	candidate := kernel.PublishableChange{Task: task, Change: changeID, Revision: revision, Base: base, Head: head,
		Accepted: kernel.IntakeAcceptance{SourceRepository: "team/repo", Snapshot: kernel.IntakeIssueSnapshot{GitHubRepositoryID: 42, IssueNumber: 9, Title: "Fix  the\nthing"}}}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return &publicReviewBackend{} }
	checkout := func(context.Context, string) (string, func(), error) {
		clone := filepath.Join(t.TempDir(), "clone")
		supervisorGit(t, git, "clone", "-q", "--shared", registered, clone)
		return filepath.Join(clone, ".git"), func() {}, nil
	}
	bare := filepath.Join(t.TempDir(), "github.git")
	supervisorGit(t, git, "clone", "-q", "--bare", "--shared", registered, bare)
	return fixture, candidate, filepath.Join(root, ".git"), &fakePublishMaintainer{t: t, bare: bare, main: base, journal: map[string]json.RawMessage{}, issueTitle: "Fix  the\nthing"}, checkout
}

// publishedExactly fails unless GitHub's branch is the worker's head and holds
// the worker's own commit bytes.
func publishedExactly(t *testing.T, app *fakePublishMaintainer, source, head string) {
	t.Helper()
	git := change.TrustedGitExecutable
	if app.tip != head {
		t.Fatalf("branch at %s, want the Change head %s", app.tip, head)
	}
	if got, want := supervisorGitOutput(t, git, "--git-dir", app.bare, "cat-file", "commit", head), supervisorGitOutput(t, git, "--git-dir", source, "cat-file", "commit", head); got != want {
		t.Fatalf("GitHub holds %q, the worker made %q", got, want)
	}
}

// First publication end to end, with the push's response lost: the retry
// finds the branch at the head and pushes nothing again, opens the pull
// request at the worker's exact head from the accepted issue with a redacted
// body, records it under the worker task and starts factoryd's review.
func TestFactorydPublishesASettledIntakeChange(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 51, false)
	ctx := context.Background()
	app.issueBody = "Current operator scope\n\nRelated: #7\nCloses feed/issues#9"
	c.Accepted.SourceRepository = "feed/issues"
	app.loseNext = true
	if err := fixture.daemon.publishChange(ctx, c, "team/repo", source, app.call, checkout); err == nil {
		t.Fatal("a lost response reported publication")
	}
	if err := fixture.daemon.publishChange(ctx, c, "team/repo", source, app.call, checkout); err != nil {
		t.Fatal(err)
	}
	if len(app.writes) != 2 || app.writes[0]["name"] != "publish_pack" || app.writes[1]["name"] != "create_pull_request" {
		t.Fatalf("writes = %+v", app.writes)
	}
	if app.issueRepository != c.Accepted.SourceRepository {
		t.Fatalf("observed issue repository = %q, want %q", app.issueRepository, c.Accepted.SourceRepository)
	}
	push, pull := app.writes[0]["arguments"].(map[string]any), app.writes[1]["arguments"].(map[string]any)
	if push["operation_id"] != uuid5("dark-factory:"+c.Change.String()+":pack-"+c.Head[:8]) || push["expected_head_sha"] != nil || push["head_sha"] != c.Head || push["branch"] != "factory/"+c.Change.String()[:12] ||
		pull["operation_id"] != uuid5("dark-factory:"+c.Change.String()+":pr-"+c.Head[:8]) || pull["title"] != "Fix the thing" {
		t.Fatalf("push = %+v pull = %+v", push, pull)
	}
	publishedExactly(t, app, source, c.Head)
	body := pull["body"].(string)
	if pull["head_sha"] != c.Head || pull["issue_number"] != uint64(9) || pull["source_repository"] != "feed/issues" || pull["close_on_merge"] != false || pull["base_sha"] != c.Base ||
		strings.Contains(body, "example.com") || strings.Contains(body, "/Users/") || !strings.Contains(body, "Current operator scope") || !strings.Contains(body, "Related: feed/issues#7") || !strings.Contains(body, "Refs feed/issues#9") || strings.Contains(body, "Closes feed/issues#9") || strings.Contains(body, "Implemented it") || !strings.Contains(body, c.Head) || !strings.Contains(body, "+51 -1 across 52 files") || !strings.HasSuffix(body, "The merge queue runs the gate.") {
		t.Fatalf("pull request = %+v", pull)
	}
	if pr := readProductionPull(t, fixture.store, c.Task.ProjectID, 31); pr.Head != c.Head || pr.Branch != "factory/"+c.Change.String()[:12] {
		t.Fatalf("recorded pull = %+v", pr)
	}
	page, err := fixture.store.Production(ctx, c.Task.ProjectID, 0, 8, kernel.UnixMillis{})
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range page.Records {
		if record.Kind == "pull_request" && (len(record.Tasks) != 1 || record.Tasks[0] != c.Task.ID.String()) {
			t.Fatalf("publication tasks = %v, want the worker %s", record.Tasks, c.Task.ID)
		}
	}
	if op := waitForDurableReview(t, fixture.store, c.Task.ProjectID, func(op review.Operation) bool { return op.State == "enqueued" }); op.State != "enqueued" || op.Request.PullNumber != 31 || op.Request.Head != c.Head {
		t.Fatalf("review = %+v", op)
	}
}

func TestIntakePublicationSurfacesCurrentIssueObservationFailure(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 1, false)
	c.Accepted.SourceRepository = "feed/issues"
	call := func(ctx context.Context, name string, arguments map[string]any) (json.RawMessage, error) {
		if name == "observe_issue" {
			return nil, errors.New("issue observation unavailable")
		}
		return app.call(ctx, name, arguments)
	}
	if err := fixture.daemon.publishChange(context.Background(), c, "team/repo", source, call, checkout); err != nil {
		t.Fatal(err)
	}
	document, found, err := fixture.store.ReviewOperation(context.Background(), c.Task.ProjectID, kernel.PublishFailureID(c.Change, c.Revision))
	if err != nil || !found || !strings.Contains(string(document), "observe accepted issue #9: issue observation unavailable") {
		t.Fatalf("issue observation failure was not surfaced: found=%v err=%v document=%s", found, err, document)
	}
}

// A head that changes after an earlier head reached the Change's branch but
// before its pull request (a retried task) replaces that branch head, a range
// of two worker commits pushed exactly.
func TestFirstPublicationOfAChangedHeadReplacesItsBranch(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 2, false)
	ctx := context.Background()
	root, git := filepath.Dir(source), change.TrustedGitExecutable
	stale := c.Head
	app.tip = stale // no pull request yet
	if err := os.WriteFile(filepath.Join(root, "f00.txt"), []byte("retried\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	supervisorGit(t, git, "-C", root, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-q", "-am", "retry")
	c.Head = strings.TrimSpace(supervisorGitOutput(t, git, "-C", root, "rev-parse", "HEAD"))
	if err := fixture.daemon.publishChange(ctx, c, "team/repo", source, app.call, checkout); err != nil {
		t.Fatal(err)
	}
	if len(app.writes) != 2 {
		t.Fatalf("writes = %+v", app.writes)
	}
	push, pull := app.writes[0]["arguments"].(map[string]any), app.writes[1]["arguments"].(map[string]any)
	if push["operation_id"] != uuid5("dark-factory:"+c.Change.String()+":pack-"+c.Head[:8]) || push["expected_head_sha"] != stale || pull["head_sha"] != c.Head {
		t.Fatalf("push = %+v pull = %+v", push, pull)
	}
	publishedExactly(t, app, source, c.Head)
	app.tip = stale
	publishedExactly(t, app, source, stale) // the range's first commit, as the worker made it
}

// A branch moved after factoryd observed it is never overwritten: the push's
// expected head refuses it and no pull request opens.
func TestPublicationRefusesABranchThatMovedSinceItWasObserved(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 1, false)
	moved := c.Base
	app.tip = moved
	stale := func(ctx context.Context, name string, arguments map[string]any) (json.RawMessage, error) {
		if name == "observe_ref" && arguments["branch"] != "main" {
			return json.Marshal(map[string]any{"head_sha": ""}) // observed before it moved
		}
		return app.call(ctx, name, arguments)
	}
	if err := fixture.daemon.publishPull(context.Background(), c, "team/repo", source, stale, checkout); err == nil || !strings.Contains(err.Error(), "branch is at "+moved) {
		t.Fatalf("raced push = %v", err)
	}
	if app.tip != moved || len(app.writes) != 1 || app.writes[0]["name"] != "publish_pack" {
		t.Fatalf("branch %s, writes %+v", app.tip, app.writes)
	}
}

// A pull request create left planned (stopped before GitHub, bound to the
// main it named) is not replayed against a moved main: the create goes under
// an id bound to the current main.
func TestAPlannedPullRequestCreateMovesToTheCurrentMain(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 1, false)
	ctx := context.Background()
	app.planned = map[string]bool{uuid5("dark-factory:" + c.Change.String() + ":pr-" + c.Head[:8]): true}
	if err := fixture.daemon.publishChange(ctx, c, "team/repo", source, app.call, checkout); err != nil {
		t.Fatal(err)
	}
	pull := app.writes[len(app.writes)-1]["arguments"].(map[string]any)
	if pull["operation_id"] != uuid5("dark-factory:"+c.Change.String()+":pr-"+c.Head[:8]+"-"+app.main) || pull["base_sha"] != app.main {
		t.Fatalf("pull request = %+v", pull)
	}
}

// A branch an earlier attempt already brought to the head, with no pull
// request, gets its pull request at that head and no push.
func TestFirstPublicationOfAnAlreadyPushedHeadOpensItsPullRequest(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 1, false)
	ctx := context.Background()
	backend := &publicReviewBackend{}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	app.tip = c.Head
	if err := fixture.daemon.publishChange(ctx, c, "team/repo", source, app.call, checkout); err != nil {
		t.Fatal(err)
	}
	if len(app.writes) != 1 || app.writes[0]["name"] != "create_pull_request" || app.writes[0]["arguments"].(map[string]any)["head_sha"] != c.Head {
		t.Fatalf("writes = %+v", app.writes)
	}
	if open, err := fixture.store.OpenPulls(ctx); err != nil || len(open) != 1 || len(open[0].Operations) != 1 {
		t.Fatalf("publication did not claim one review: open=%+v err=%v", open, err)
	}
}

// A true empty diff from the main the worker integrated opens no pull
// request, whatever stale commit the branch holds.
func TestEmptyDiffFromIntegratedMainOpensNoPullRequestAtAStaleTip(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 1, false)
	ctx := context.Background()
	root, git := filepath.Dir(source), change.TrustedGitExecutable
	registered := strings.TrimSpace(supervisorGitOutput(t, git, "-C", root, "remote", "get-url", "origin"))
	supervisorGit(t, git, "-C", registered, "fetch", "-q", "--update-head-ok", root, "HEAD:main") // main now is the worker's head
	app.main, app.tip = c.Head, c.Base                                                            // the branch holds a stale commit
	if err := fixture.daemon.publishPull(ctx, c, "team/repo", source, app.call, checkout); err == nil || !strings.Contains(err.Error(), "nothing to publish") || len(app.writes) != 0 {
		t.Fatalf("err = %v, writes = %+v", err, app.writes)
	}
}

// A correction after a send-back replaces the open pull request's branch
// head with the worker's exact head, with the push's response lost: the retry
// pushes nothing twice, replaces the body naming the new head under its
// closing line, and a further pass writes nothing.
func TestFactorydPublishesACorrectionOnItsPullRequest(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 2, false)
	ctx := context.Background()
	root, git := filepath.Dir(source), change.TrustedGitExecutable
	tip := c.Head // the first publication
	app.tip = tip
	if err := os.WriteFile(filepath.Join(root, "f00.txt"), []byte("corrected\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	supervisorGit(t, git, "-C", root, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-q", "-am", "correct")
	c.Head, c.Pull = strings.TrimSpace(supervisorGitOutput(t, git, "-C", root, "rev-parse", "HEAD")), 31
	app.loseNext = true
	if err := fixture.daemon.publishChange(ctx, c, "team/repo", source, app.call, checkout); err == nil {
		t.Fatal("a lost response reported publication")
	}
	for range 2 {
		if err := fixture.daemon.publishChange(ctx, c, "team/repo", source, app.call, checkout); err != nil {
			t.Fatal(err)
		}
	}
	if len(app.writes) != 2 || app.writes[0]["name"] != "publish_pack" || app.writes[1]["name"] != "update_pull_request_body" {
		t.Fatalf("writes = %+v", app.writes)
	}
	push, body := app.writes[0]["arguments"].(map[string]any), app.writes[1]["arguments"].(map[string]any)
	if push["operation_id"] != uuid5("dark-factory:"+c.Change.String()+":pack-"+c.Head[:8]) || push["expected_head_sha"] != tip || push["branch"] != "factory/"+c.Change.String()[:12] {
		t.Fatalf("correction push = %+v", push)
	}
	publishedExactly(t, app, source, c.Head)
	text := body["body"].(string)
	if body["operation_id"] != uuid5("dark-factory:"+c.Change.String()+":body-"+c.Head[:8]+"-3") || body["pull_number"] != uint64(31) ||
		!strings.Contains(text, "at "+c.Head+": +2 -1 across 3 files from "+c.Base) || !strings.HasSuffix(text, "\n\nRefs #9") {
		t.Fatalf("corrected body = %+v", body)
	}
}

// #1819: a correction rebased onto a main that moved under its pull request,
// resolving a conflict, is pushed as it is, so the pull request's history has
// that main and stays mergeable; no commit is built on the old tip without it.
func TestACorrectionRebasedOntoAMovedMainLeavesItsPullRequestMergeable(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 1, false)
	ctx := context.Background()
	root, git := filepath.Dir(source), change.TrustedGitExecutable
	commit := func(dir string, args ...string) {
		supervisorGit(t, git, append([]string{"-C", dir, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid"}, args...)...)
	}
	registered := strings.TrimSpace(supervisorGitOutput(t, git, "-C", root, "remote", "get-url", "origin"))
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("worker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commit(root, "add", "a.txt")
	commit(root, "commit", "-q", "-m", "worker")
	app.tip, c.Pull = strings.TrimSpace(supervisorGitOutput(t, git, "-C", root, "rev-parse", "HEAD")), 31
	tip := app.tip
	// main moves with a conflicting a.txt; the worker rebases onto it.
	if err := os.WriteFile(filepath.Join(registered, "a.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commit(registered, "add", "a.txt")
	commit(registered, "commit", "-q", "-m", "main moved")
	app.main = strings.TrimSpace(supervisorGitOutput(t, git, "-C", registered, "rev-parse", "HEAD"))
	commit(root, "fetch", "-q", "origin", "main")
	if err := exec.Command(git, "-C", root, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "rebase", "-q", "FETCH_HEAD").Run(); err == nil {
		t.Fatal("the fixture's rebase did not conflict")
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("main\nworker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commit(root, "add", "a.txt")
	commit(root, "-c", "core.editor=true", "rebase", "--continue")
	c.Head = strings.TrimSpace(supervisorGitOutput(t, git, "-C", root, "rev-parse", "HEAD"))

	if err := fixture.daemon.publishChange(ctx, c, "team/repo", source, app.call, checkout); err != nil {
		t.Fatal(err)
	}
	if len(app.writes) != 2 || app.writes[0]["name"] != "publish_pack" || app.writes[0]["arguments"].(map[string]any)["expected_head_sha"] != tip || app.writes[1]["name"] != "update_pull_request_body" {
		t.Fatalf("writes = %+v", app.writes)
	}
	publishedExactly(t, app, source, c.Head)
	supervisorGit(t, git, "--git-dir", app.bare, "merge-base", "--is-ancestor", app.main, c.Head) // mergeable: main is in its history
}

// A worker answering a verdict with evidence and no file change (#1706)
// moves no head: its answer replaces the body, instead of publication
// refusing "nothing to publish". The reconciler reviews the head once more.
func TestAnEvidenceOnlyCorrectionReplacesOnlyTheBody(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 1, false)
	ctx := context.Background()
	app.tip, c.Pull = c.Head, 31 // the branch already is the worker's head
	app.issueBody = "Current operator scope"
	c.Task.Result = "Premise not reproduced: the cited line already guards it."
	if err := fixture.daemon.publishChange(ctx, c, "team/repo", source, app.call, checkout); err != nil {
		t.Fatal(err)
	}
	body := app.writes[0]["arguments"].(map[string]any)["body"].(string)
	if len(app.writes) != 1 || app.writes[0]["name"] != "update_pull_request_body" || !strings.Contains(body, "Premise not reproduced") || !strings.Contains(body, "Current operator scope") {
		t.Fatalf("writes = %+v", app.writes)
	}
}

// A path the App refuses is escalated once, as a handled record the overseer
// is woken for, and nothing about it stays in flight to be retried.
func TestRefusedPublicationEscalatesOnce(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 1, true)
	app.refuse = ".github/workflows"
	ctx := context.Background()
	if err := fixture.daemon.publishChange(ctx, c, "team/repo", source, app.call, checkout); err != nil {
		t.Fatal(err)
	}
	document, found, err := fixture.store.ReviewOperation(ctx, c.Task.ProjectID, kernel.PublishFailureID(c.Change, c.Revision))
	var op review.Operation
	if err != nil || !found || json.Unmarshal(document, &op) != nil || !strings.Contains(string(document), `"retryable":false`) || op.State != "publish_failed" || !op.Handled ||
		!strings.HasPrefix(op.Escalation, "factoryd cannot publish change "+c.Change.String()+" for task "+c.Task.ID.String()+": ") || !strings.Contains(op.Escalation, "refused: .github/workflows") {
		t.Fatalf("failure record = %s %v %v", document, found, err)
	}
	if open, err := fixture.store.OpenPulls(ctx); err != nil || len(open) != 0 {
		t.Fatalf("in flight = %+v %v", open, err)
	}
	if len(app.writes) != 1 {
		t.Fatalf("writes = %d", len(app.writes))
	}
	// A retry past the window that is refused again refreshes the record
	// without escalating a second time.
	if err := fixture.daemon.publishChange(ctx, c, "team/repo", source, app.call, checkout); err != nil {
		t.Fatal(err)
	}
	document, found, err = fixture.store.ReviewOperation(ctx, c.Task.ProjectID, kernel.PublishFailureID(c.Change, c.Revision))
	op = review.Operation{}
	if err != nil || !found || json.Unmarshal(document, &op) != nil || op.State != "publish_failed" || op.Escalation != "" || !strings.Contains(op.Detail, "refused: .github/workflows") {
		t.Fatalf("repeat failure record = %s %v %v", document, found, err)
	}
}

func TestExternalPublicationRefusalRetriesWhenItClears(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 1, false)
	app.refuse, app.externalRefuse = "f00.txt", true
	ctx := context.Background()
	var log strings.Builder
	fixture.daemon.log = &log
	if err := fixture.daemon.publishChange(ctx, c, "team/repo", source, app.call, checkout); err != nil {
		t.Fatal(err)
	}
	document, found, err := fixture.store.ReviewOperation(ctx, c.Task.ProjectID, kernel.PublishFailureID(c.Change, c.Revision))
	var op review.Operation
	if err != nil || !found || json.Unmarshal(document, &op) != nil || !op.Retryable {
		t.Fatalf("external refusal = %s %v %v", document, found, err)
	}
	// #1800: each attempt's own outcome is logged, a recorded refusal included.
	if attempt := "factoryd: publish change " + c.Change.String() + " at " + c.Head + " for task " + c.Task.ID.String() + ": "; !strings.Contains(log.String(), attempt) || !strings.Contains(log.String(), "UNPROCESSABLE") || strings.Contains(log.String(), "factoryd: published") {
		t.Fatalf("refused attempt logged %q", log.String())
	}
	app.refuse, app.externalRefuse = "", false
	if err := fixture.daemon.publishChange(ctx, c, "team/repo", source, app.call, checkout); err != nil {
		t.Fatal(err)
	}
	if len(app.writes) != 3 {
		t.Fatalf("writes after refusal cleared = %d, want 3", len(app.writes))
	}
	if !strings.Contains(log.String(), "factoryd: published change "+c.Change.String()+" at "+c.Head) {
		t.Fatalf("publication logged %q", log.String())
	}
}

func TestUnavailablePublicationRetriesWhenServiceRecovers(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 1, false)
	app.unavailable = true
	ctx := context.Background()
	if err := fixture.daemon.publishChange(ctx, c, "team/repo", source, app.call, checkout); err != nil {
		t.Fatal(err)
	}
	document, found, err := fixture.store.ReviewOperation(ctx, c.Task.ProjectID, kernel.PublishFailureID(c.Change, c.Revision))
	var op review.Operation
	if err != nil || !found || json.Unmarshal(document, &op) != nil || !op.Retryable {
		t.Fatalf("unavailable refusal = %s %v %v", document, found, err)
	}
	app.unavailable = false
	if err := fixture.daemon.publishChange(ctx, c, "team/repo", source, app.call, checkout); err != nil {
		t.Fatal(err)
	}
	if len(app.writes) != 3 {
		t.Fatalf("writes after service recovery = %d, want 3", len(app.writes))
	}
}

func TestPublicationRedactsEmailsAndUUID5MatchesTheRunbook(t *testing.T) {
	if got := string(redactTerminalText([]byte("mail a.b+c@mail.example.org now"))); strings.Contains(got, "@") || !strings.HasSuffix(got, " now") {
		t.Fatalf("redacted = %q", got)
	}
	cjk := strings.Repeat("修正", 50) // 300 bytes of three-byte runes
	if got := publicationTitle(cjk + "\nmore"); len(got) != 255 || !utf8.ValidString(got) || !strings.HasPrefix(cjk, got) {
		t.Fatalf("title = %d bytes %q", len(got), got)
	}
	// python3 -c "import uuid; print(uuid.uuid5(uuid.NAMESPACE_URL, 'dark-factory:00112233445566778899aabbccddeeff:publish-1'))"
	if got := uuid5("dark-factory:00112233445566778899aabbccddeeff:publish-1"); got != "b766e0bb-085c-51fb-8f53-11c3cdd7c979" {
		t.Fatalf("uuid5 = %s", got)
	}
}

// A repository disabled for new work after acceptance is neither published
// to nor reviewed: the Change is escalated once, before any Maintainer call
// (the fixture has no connection, so a call would fail the test).
func TestDisabledAcceptedRepositoryEscalatesOnce(t *testing.T) {
	fixture, c, _, _, _ := publishFixture(t, 1, false)
	ctx := context.Background()
	id, err := kernel.RepositoryIDFromBytes(mustIDBytes(t, testID(251)))
	if err != nil {
		t.Fatal(err)
	}
	repository, _, err := fixture.store.ProjectRepository(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.SetProjectRepositoryEnabled(ctx, id, repository.Revision, false, mustKernelTime(t, 1003)); err != nil {
		t.Fatal(err)
	}
	c.Accepted.RepositoryID = id
	// The first pass escalates; a repeat only refreshes the one record.
	for _, escalation := range []string{"factoryd cannot publish change " + c.Change.String() + " for task " + c.Task.ID.String() + ": repository disabled for new work", ""} {
		if err := fixture.daemon.publishSettledChange(ctx, c); err != nil {
			t.Fatal(err)
		}
		page, err := fixture.store.Production(ctx, c.Task.ProjectID, 0, 8, kernel.UnixMillis{})
		if err != nil {
			t.Fatal(err)
		}
		failures := 0
		for _, record := range page.Records {
			var op review.Operation
			if record.Kind == "reviewer" && json.Unmarshal(record.Document, &op) == nil && op.State == "publish_failed" {
				failures++
				if op.ID != kernel.PublishFailureID(c.Change, c.Revision) || !op.Retryable || op.Escalation != escalation || op.Detail != "repository disabled for new work" {
					t.Fatalf("failure record = %+v", op)
				}
			}
			if record.Kind == "pull_request" {
				t.Fatalf("published into a disabled repository: %s", record.Document)
			}
		}
		if failures != 1 {
			t.Fatalf("failure records = %d, want 1", failures)
		}
	}
}

// Only a Change completing its intake issue closes it on merge: one whose
// result says it is partial (Refs #N) publishes as a reference (#1795),
// even when that marker lies past what the body shows.
func TestAPartialIntakeChangeReferencesItsIssue(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 1, false)
	app.issueBody = "Current operator scope"
	c.Task.Result = "Partial: items 1 and 5. Fixes #9 once item 2 lands.\n" + strings.Repeat("report line\n", 3000) + "Refs #9" // beyond the 24000-byte body
	if err := fixture.daemon.publishChange(context.Background(), c, "team/repo", source, app.call, checkout); err != nil {
		t.Fatal(err)
	}
	if pull := app.writes[len(app.writes)-1]["arguments"].(map[string]any); pull["close_on_merge"] != false || pull["issue_number"] != uint64(9) || !strings.Contains(pull["body"].(string), "Current operator scope") || strings.Contains(pull["body"].(string), "Fixes #9") {
		t.Fatalf("pull request = %+v", pull)
	}
}

func TestLinearPublicationUsesExternalSourceWithoutIssueObservation(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 1, false)
	c.Accepted.SourceRepository = "linear/acme"
	c.Accepted.Snapshot.LinearTeamID = "ENG"
	c.Accepted.Snapshot.URL = "https://linear.app/acme/issue/ENG-7/intake"
	if err := fixture.daemon.publishChange(context.Background(), c, "team/repo", source, app.call, checkout); err != nil {
		t.Fatal(err)
	}
	if app.issueRepository != "" {
		t.Fatalf("Linear publication observed a GitHub issue in %q", app.issueRepository)
	}
	pull := app.writes[len(app.writes)-1]["arguments"].(map[string]any)
	if pull["issue_number"] != uint64(0) || pull["source_repository"] != nil || pull["external_source_url"] != c.Accepted.Snapshot.URL || strings.Contains(pull["body"].(string), "#0") {
		t.Fatalf("Linear pull request = %+v", pull)
	}
}

func TestCrossSourceCorrectionKeepsQualifiedReferenceFooter(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 1, false)
	c.Accepted.SourceRepository = "feed/issues"
	c.Pull = 31
	app.tip, app.body = c.Head, "Implemented it.\n\nRefs feed/issues#9"
	root, git := filepath.Dir(source), change.TrustedGitExecutable
	if err := os.WriteFile(filepath.Join(root, "f00.txt"), []byte("corrected\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	supervisorGit(t, git, "-C", root, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-q", "-am", "correct")
	c.Head = strings.TrimSpace(supervisorGitOutput(t, git, "-C", root, "rev-parse", "HEAD"))
	c.Task.Result = "Answered the review.\n\nCloses feed/issues#9"
	if err := fixture.daemon.publishChange(context.Background(), c, "team/repo", source, app.call, checkout); err != nil {
		t.Fatal(err)
	}
	body := app.writes[len(app.writes)-1]["arguments"].(map[string]any)["body"].(string)
	if strings.Contains(body, "Closes feed/issues#9") || !strings.Contains(body, "Refs feed/issues#9") || !strings.HasSuffix(body, "Refs feed/issues#9") {
		t.Fatalf("cross-source correction body = %q", body)
	}
}

func TestTitleOnlyIntakePublicationUsesObservedTitle(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 1, false)
	c.Task.Result = "worker result must not replace the title"
	if err := fixture.daemon.publishChange(context.Background(), c, "team/repo", source, app.call, checkout); err != nil {
		t.Fatal(err)
	}
	body := app.writes[len(app.writes)-1]["arguments"].(map[string]any)["body"].(string)
	if !strings.Contains(body, c.Accepted.Snapshot.Title) || strings.Contains(body, c.Task.Result) {
		t.Fatalf("title-only body = %q", body)
	}
}

// A correction keeps a referencing pull request referencing, and one that
// leaves the Change changing no file (#1796) stops closing the issue.
func TestACorrectionNeverTurnsAnUnfinishedIntakeIntoAClose(t *testing.T) {
	for name, revert := range map[string]bool{"partial pull request": false, "zero-file Change": true} {
		t.Run(name, func(t *testing.T) {
			fixture, c, source, app, checkout := publishFixture(t, 1, false)
			root, git := filepath.Dir(source), change.TrustedGitExecutable
			app.tip, c.Pull = c.Head, 31
			app.body = "Implemented items 1 and 5.\n\nRefs #9\n\n<!-- dark-factory:operation -->"
			if revert {
				app.body = "Implemented it.\n\nCloses #9"
				supervisorGit(t, git, "-C", root, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "revert", "--no-edit", "HEAD")
			} else if err := os.WriteFile(filepath.Join(root, "f00.txt"), []byte("corrected\n"), 0o644); err != nil {
				t.Fatal(err)
			} else {
				supervisorGit(t, git, "-C", root, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-q", "-am", "correct")
			}
			c.Head = strings.TrimSpace(supervisorGitOutput(t, git, "-C", root, "rev-parse", "HEAD"))
			c.Task.Result = "Answered the review.\n\nCloses #9"
			if err := fixture.daemon.publishChange(context.Background(), c, "team/repo", source, app.call, checkout); err != nil {
				t.Fatal(err)
			}
			body := app.writes[len(app.writes)-1]
			if text, _ := body["arguments"].(map[string]any)["body"].(string); body["name"] != "update_pull_request_body" || !strings.HasPrefix(text, "Fix  the\nthing\n\nCorrection result:\nAnswered the review.\n\nRefs #9\n\nPublished by factoryd") ||
				!strings.HasSuffix(text, "The merge queue runs the gate.\n\nRefs #9") || strings.Contains(strings.ToLower(text), "closes") {
				t.Fatalf("writes = %+v", app.writes)
			}
		})
	}
}

func TestReferencingDisarmsEveryClosingKeywordForTheIssue(t *testing.T) {
	in := "Closes #9\nfixed: team/repo#9, Resolves https://github.com/team/repo/issues/9 and closes #90."
	if got, want := referencing(in, 9), "Refs #9\nRefs team/repo#9, Refs https://github.com/team/repo/issues/9 and closes #90."; got != want {
		t.Fatalf("referencing = %q, want %q", got, want)
	}
}

// packMaintainer is publish_pack's side of the Maintainer and GitHub: it holds
// chunks in order, binds every one to the first one's fields, and on the last
// checks the digest, the branch precondition and the objects' connectivity in
// a bare repository standing in for GitHub.
type packMaintainer struct {
	t         *testing.T
	operation string
	bare      string // GitHub's repository
	branch    string // the factory branch's head, "" while absent
	fields    string
	chunks    []string
	pushes    int
	loseNext  bool            // the push lands but its response is lost
	result    json.RawMessage // the completed operation's journaled result
}

func (m *packMaintainer) call(_ context.Context, name string, arguments map[string]any) (json.RawMessage, error) {
	// The request factoryd's attempt client and API bound at 512 KiB.
	envelope, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": arguments}})
	if name != "publish_pack" || len(envelope) > 512<<10 {
		m.t.Fatalf("%s request of %d bytes", name, len(envelope))
	}
	identity := map[string]any{}
	for key, value := range arguments {
		identity[key] = value
	}
	chunk, index := identity["chunk_base64"].(string), identity["chunk_index"].(int)
	delete(identity, "chunk_base64")
	delete(identity, "chunk_index")
	fields, _ := json.Marshal(identity)
	if m.fields == "" {
		m.fields = string(fields)
	}
	// As the journal does, a completed operation answers only its own request.
	if m.result != nil && string(fields) == m.fields {
		return m.result, nil
	}
	switch {
	case string(fields) != m.fields || index > len(m.chunks) || index < len(m.chunks) && m.chunks[index] != chunk:
		return nil, maintainerRejection("conflict: chunk is not this operation's next or held chunk")
	case index == len(m.chunks):
		m.chunks = append(m.chunks, chunk)
	}
	size := identity["pack_bytes"].(int)
	if len(m.chunks)*packChunkBytes < size {
		return json.Marshal(map[string]any{"branch": identity["branch"], "received_chunks": len(m.chunks), "commit_sha": nil})
	}
	var pack []byte
	for _, chunk := range m.chunks {
		decoded, err := base64.StdEncoding.DecodeString(chunk)
		if err != nil {
			m.t.Fatal(err)
		}
		pack = append(pack, decoded...)
	}
	if sum := sha256.Sum256(pack); len(pack) != size || hex.EncodeToString(sum[:]) != identity["pack_sha256"] {
		return nil, maintainerRejection("conflict: assembled pack does not match its digest")
	}
	expected, _ := identity["expected_head_sha"].(string)
	if expected != m.branch {
		return nil, maintainerRejection("conflict: branch is at " + m.branch)
	}
	file := filepath.Join(m.bare, "objects", "pack", fmt.Sprintf("pack-%d.pack", m.pushes))
	if err := os.WriteFile(file, pack, 0o644); err != nil {
		m.t.Fatal(err)
	}
	head := identity["head_sha"].(string)
	supervisorGit(m.t, change.TrustedGitExecutable, "--git-dir", m.bare, "index-pack", file)
	supervisorGit(m.t, change.TrustedGitExecutable, "--git-dir", m.bare, "rev-list", "--objects", head)
	m.branch, m.pushes = head, m.pushes+1
	m.result, _ = json.Marshal(map[string]any{"branch": identity["branch"], "received_chunks": len(m.chunks), "commit_sha": head})
	if m.loseNext {
		m.loseNext = false
		return nil, maintainer.ErrUnavailable
	}
	return m.result, nil
}

// Exact publication end to end: two worker commits over 512 KiB travel in
// ordered chunks within the attempt envelope, a lost push response is replayed
// to the journaled result without a second push, GitHub ends up holding the
// worker's own commit bytes, a stale precondition is refused, and a head that
// changes review authority is never sent.
func TestPublishPackSendsExactObjectsInReplaySafeChunks(t *testing.T) {
	git := change.TrustedGitExecutable
	source := filepath.Join(t.TempDir(), "source")
	run := func(args ...string) string {
		return strings.TrimSpace(supervisorGitOutput(t, git, append([]string{"-C", source, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid"}, args...)...))
	}
	write := func(name string, content []byte) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(source, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	supervisorGit(t, git, "init", "-q", "-b", "main", source)
	write("keep.txt", []byte("base\n"))
	run("add", ".")
	run("commit", "-q", "-m", "base")
	base := run("rev-parse", "HEAD")
	bare := filepath.Join(t.TempDir(), "github.git")
	supervisorGit(t, git, "clone", "-q", "--bare", source, bare)
	noise := make([]byte, 700<<10) // incompressible, so the pack spans chunks
	_, _ = rand.NewChaCha8([32]byte{7}).Read(noise)
	write("noise.bin", noise)
	run("add", ".")
	run("commit", "-q", "-m", "first")
	write("keep.txt", []byte("worker\n"))
	run("commit", "-q", "-am", "second\n\nwith an exact body")
	head := run("rev-parse", "HEAD")
	gitDir := filepath.Join(source, ".git")
	ctx := context.Background()

	m := &packMaintainer{t: t, bare: bare, loseNext: true}
	operation := uuid5("dark-factory:pack-test")
	if err := publishPack(ctx, m.call, gitDir, "team/repo", operation, "factory/d2fe80c351a5", "", base, head); !errors.Is(err, maintainer.ErrUnavailable) {
		t.Fatalf("lost push response = %v", err)
	}
	// Git may repack between attempts; the replay must still be the same
	// pack, or its digest is another request and the journal refuses it.
	run("-c", "pack.compression=1", "repack", "-q", "-a", "-d", "-f", "--window=2", "--depth=1")
	if err := publishPack(ctx, m.call, gitDir, "team/repo", operation, "factory/d2fe80c351a5", "", base, head); err != nil {
		t.Fatal(err)
	}
	if m.pushes != 1 || len(m.chunks) < 3 || m.branch != head {
		t.Fatalf("pushes=%d chunks=%d branch=%s", m.pushes, len(m.chunks), m.branch)
	}
	for _, commit := range []string{head, head + "^"} {
		if got, want := supervisorGitOutput(t, git, "--git-dir", bare, "cat-file", "commit", commit), run("cat-file", "commit", commit)+"\n"; got != want {
			t.Fatalf("GitHub holds %q for %s, the worker made %q", got, commit, want)
		}
	}

	write("other.txt", []byte("race\n"))
	run("add", ".")
	run("commit", "-q", "-m", "raced")
	raced := run("rev-parse", "HEAD")
	m.result, m.chunks, m.fields = nil, nil, ""
	if err := publishPack(ctx, m.call, gitDir, "team/repo", uuid5("dark-factory:pack-race"), "factory/d2fe80c351a5", base, base, raced); err == nil || !strings.Contains(err.Error(), "branch is at "+head) {
		t.Fatalf("stale precondition = %v", err)
	}
	if m.branch != head || m.pushes != 1 {
		t.Fatalf("a refused push moved the branch to %s", m.branch)
	}

	// Each path publish_commit refuses, including a blob replacing the
	// .github tree, is refused before anything is sent.
	write(".github/workflows/ci.yml", []byte("on: push\n"))
	run("add", ".")
	run("commit", "-q", "-m", "workflow")
	workflow := run("rev-parse", "HEAD")
	for _, change := range []struct{ path, name string }{{".github/CODEOWNERS", ".github/CODEOWNERS"}, {"docs/CODEOWNERS", "docs/CODEOWNERS"}, {".github", ".github"}, {".GitHub", ".GitHub"}} {
		run("checkout", "-q", "--detach", workflow)
		if strings.EqualFold(change.path, ".github") {
			run("rm", "-rq", ".github")
		}
		write(change.path, []byte("* @someone\n"))
		run("add", ".")
		run("commit", "-q", "-m", "authority")
		m.result, m.chunks, m.fields = nil, nil, ""
		if err := publishPack(ctx, m.call, gitDir, "team/repo", uuid5("dark-factory:pack-authority"), "factory/d2fe80c351a5", head, workflow, run("rev-parse", "HEAD")); err == nil || !strings.HasSuffix(err.Error(), "review authority: "+change.name) || m.fields != "" {
			t.Fatalf("%s = %v, sent %q", change.path, err, m.fields)
		}
	}
	// A workflow is not review authority.
	if err := publishPack(ctx, m.call, gitDir, "team/repo", uuid5("dark-factory:pack-workflow"), "factory/d2fe80c351a5", head, head, workflow); err != nil {
		t.Fatal(err)
	}
}
