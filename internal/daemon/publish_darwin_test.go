package daemon

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
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
	main                    string
	tip                     string // the pull request branch's head
	journal                 map[string]json.RawMessage
	planned                 map[string]bool  // ids a write stopped before GitHub
	indeterminate           map[string]bool  // ids whose first response was unavailable
	writes                  []map[string]any // every write sent
	refuse                  string           // a path publish_commit refuses
	externalRefuse          bool             // the Maintainer refuses before the App can apply it
	unavailable             bool             // the Maintainer authority is temporarily unavailable
	indeterminateAgain      bool             // the fresh operation is also indeterminate
	landCreateIndeterminate bool             // create_pull_request lands before losing its response
	headRef                 string
	bodyIndeterminate       bool
	landBodyIndeterminate   bool
	bodyWrites              int
	extraPull               bool
	dirty                   bool
	queued                  bool
	closed                  bool
	landSecondCommit        bool
	landFirstCommit         bool
	landedCommit            string
	commitWrites            int
	loseNext                bool   // the next write lands but its response is lost
	body                    string // the open pull request's stored body
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
		if f.indeterminate[id] {
			return json.Marshal(map[string]any{"operation_id": id, "state": "indeterminate"})
		}
		if result, ok := f.journal[id]; ok {
			return json.Marshal(map[string]any{"operation_id": id, "state": "completed", "result": result})
		}
		if f.planned[id] {
			return json.Marshal(map[string]any{"operation_id": id, "state": "planned"})
		}
		return json.Marshal(map[string]any{"operation_id": id, "state": "missing"})
	case "list_pull_requests":
		number := arguments["pull_number"]
		if number == nil {
			number = uint64(31)
		}
		state, mergeable, mergeState := "open", true, "clean"
		if f.closed {
			state, mergeable, mergeState = "closed", true, "unknown"
		} else if f.dirty {
			mergeable, mergeState = false, "dirty"
		}
		pull := map[string]any{"number": number, "url": "https://github.com/team/repo/pull/31", "head_sha": f.tip, "head_ref": f.headRef, "base_sha": f.main, "base_ref": "main", "state": state, "mergeable": mergeable, "merge_state_status": mergeState, "body": f.body}
		pulls := []any{pull}
		if f.extraPull {
			pulls = []any{map[string]any{"number": uint64(29), "url": "https://github.com/team/repo/pull/29", "head_sha": f.tip, "head_ref": "factory/other", "base_sha": f.main, "base_ref": "main", "state": "open", "mergeable": true, "merge_state_status": "clean"}, pull}
		}
		return json.Marshal(map[string]any{"pull_requests": pulls})
	case "observe_pull_request_merge":
		mergeState := "INACTIVE"
		if f.queued {
			mergeState = "ACTIVE_QUEUE"
		}
		return json.Marshal(map[string]any{"pull_number": arguments["pull_number"], "head_sha": f.tip, "base": "main", "state": mergeState, "queue_state": nil, "merge_commit_sha": nil, "merge_group": nil})
	}
	if _, replay := f.journal[id]; replay || f.planned[id] {
		return nil, fmt.Errorf("conflict: operation %s replayed", id)
	}
	priorWrites := len(f.writes)
	f.writes = append(f.writes, map[string]any{"name": name, "arguments": arguments})
	landCreate := f.landCreateIndeterminate && name == "create_pull_request"
	if name == "publish_commit" {
		f.commitWrites++
	}
	if name == "update_pull_request_body" && f.bodyIndeterminate {
		f.bodyWrites++
	}
	landCommit := name == "publish_commit" && (f.landFirstCommit && f.commitWrites == 1 || f.landSecondCommit && f.commitWrites == 2)
	landBody := name == "update_pull_request_body" && f.landBodyIndeterminate && f.bodyWrites == 2
	if f.unavailable || landCreate || (f.indeterminateAgain && priorWrites > 0) || (name == "update_pull_request_body" && f.bodyIndeterminate) || landCommit || landBody {
		f.unavailable = false
		f.indeterminate[id] = true
		if !landCreate && !landCommit && !landBody {
			return nil, maintainerRejection("unavailable: Maintainer authority is unavailable.")
		}
	}
	var result json.RawMessage
	switch name {
	case "publish_commit":
		if f.tip != "" && arguments["expected_head_sha"] != f.tip {
			return nil, fmt.Errorf("conflict: branch is at %s, not %v", f.tip, arguments["expected_head_sha"])
		}
		for _, entry := range arguments["changes"].([]map[string]any) {
			if f.refuse != "" && strings.HasPrefix(entry["path"].(string), f.refuse) {
				if f.externalRefuse {
					return nil, maintainerRejection("refused: The request was refused: rejected before execution as UNPROCESSABLE.")
				}
				return nil, fmt.Errorf("refused: %s cannot be written", f.refuse)
			}
		}
		if landCommit && f.landedCommit != "" {
			f.tip = f.landedCommit
		} else {
			f.tip = fmt.Sprintf("%x", sha1.Sum([]byte(id)))
		}
		result, _ = json.Marshal(map[string]any{"branch": arguments["branch"], "commit_sha": f.tip, "parent_sha": arguments["expected_head_sha"]})
	case "create_pull_request":
		result, _ = json.Marshal(map[string]any{"number": 31, "url": "https://github.com/team/repo/pull/31", "head_sha": arguments["head_sha"], "base_sha": arguments["base_sha"]})
	case "update_pull_request_body":
		f.body = arguments["body"].(string)
		result, _ = json.Marshal(map[string]any{"number": arguments["pull_number"], "head_sha": f.tip})
	default:
		return nil, fmt.Errorf("unexpected tool %s", name)
	}
	f.journal[id] = result
	if landCreate || landCommit || landBody {
		f.landCreateIndeterminate = false
		f.landSecondCommit = false
		f.landFirstCommit = false
		return nil, maintainerRejection("unavailable: Maintainer authority is unavailable.")
	}
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
	checkout := func(context.Context, string, string) (string, func(), error) {
		clone := filepath.Join(t.TempDir(), "clone")
		supervisorGit(t, git, "clone", "-q", "--shared", registered, clone)
		return filepath.Join(clone, ".git"), func() {}, nil
	}
	return fixture, candidate, filepath.Join(root, ".git"), &fakePublishMaintainer{main: base, headRef: "factory/" + changeID.String()[:12], journal: map[string]json.RawMessage{}, indeterminate: map[string]bool{}}, checkout
}

// First publication end to end, with the first commit's response lost: the
// retry replays nothing the journal completed, chains 52 entries over two
// commits, opens the pull request from the accepted issue with a redacted
// body, records it under the worker task and starts factoryd's review.
func TestFactorydPublishesASettledIntakeChange(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 51, false)
	ctx := context.Background()
	app.loseNext = true
	if err := fixture.daemon.publishChange(ctx, c, "team/repo", source, app.call, checkout); err == nil {
		t.Fatal("a lost response reported publication")
	}
	if len(app.writes) != 1 {
		t.Fatalf("writes before the retry = %d", len(app.writes))
	}
	if err := fixture.daemon.publishChange(ctx, c, "team/repo", source, app.call, checkout); err != nil {
		t.Fatal(err)
	}
	first, second, pull := app.writes[0]["arguments"].(map[string]any), app.writes[1]["arguments"].(map[string]any), app.writes[2]["arguments"].(map[string]any)
	if len(app.writes) != 3 || first["operation_id"] != uuid5("dark-factory:"+c.Change.String()+":publish-"+c.Head[:8]+"-1") || second["operation_id"] != uuid5("dark-factory:"+c.Change.String()+":publish-"+c.Head[:8]+"-2") || pull["operation_id"] != uuid5("dark-factory:"+c.Change.String()+":pr-"+c.Head[:8]) {
		t.Fatalf("writes = %+v", app.writes)
	}
	firstCommit := fmt.Sprintf("%x", sha1.Sum([]byte(first["operation_id"].(string))))
	secondCommit := fmt.Sprintf("%x", sha1.Sum([]byte(second["operation_id"].(string))))
	if first["expected_head_sha"] != c.Base || len(first["changes"].([]map[string]any)) != 50 || second["expected_head_sha"] != firstCommit || len(second["changes"].([]map[string]any)) != 2 || first["message"] != "Fix the thing" || first["branch"] != "factory/"+c.Change.String()[:12] {
		t.Fatalf("commit chain: first=%v second=%v", first["expected_head_sha"], second["expected_head_sha"])
	}
	body := pull["body"].(string)
	if pull["head_sha"] != secondCommit || pull["issue_number"] != uint64(9) || pull["source_repository"] != nil || pull["close_on_merge"] != true || pull["base_sha"] != c.Base ||
		strings.Contains(body, "example.com") || strings.Contains(body, "/Users/") || !strings.Contains(body, "Implemented it") || !strings.Contains(body, secondCommit) || !strings.Contains(body, "+51 -1 across 52 files") || !strings.HasSuffix(body, "The merge queue runs the gate.") {
		t.Fatalf("pull request = %+v", pull)
	}
	if pr := readProductionPull(t, fixture.store, c.Task.ProjectID, 31); pr.Head != secondCommit || pr.Branch != "factory/"+c.Change.String()[:12] {
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
	if op := waitForDurableReview(t, fixture.store, c.Task.ProjectID, func(op review.Operation) bool { return op.State == "enqueued" }); op.State != "enqueued" || op.Request.PullNumber != 31 || op.Request.Head != secondCommit {
		t.Fatalf("review = %+v", op)
	}
}

// A head that changes after commits reached the Change's branch but before
// its pull request (a retried task) goes on that branch head under new
// operation ids, carrying only what differs from it.
func TestFirstPublicationOfAChangedHeadBuildsOnItsBranch(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 2, false)
	ctx := context.Background()
	root, git := filepath.Dir(source), change.TrustedGitExecutable
	stale := c.Head
	app.tip = stale // the branch carries the earlier head's tree; no pull request
	if err := os.WriteFile(filepath.Join(root, "f00.txt"), []byte("retried\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	supervisorGit(t, git, "-C", root, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-q", "-am", "retry")
	c.Head = strings.TrimSpace(supervisorGitOutput(t, git, "-C", root, "rev-parse", "HEAD"))
	withTip := func(ctx context.Context, ref, head string) (string, func(), error) {
		gitDir, cleanup, err := checkout(ctx, ref, head)
		if err == nil && ref == "refs/heads/factory/"+c.Change.String()[:12] && head == stale {
			_, err = gitOutput(ctx, gitDir, "-c", "protocol.file.allow=always", "fetch", "--quiet", source, stale)
		}
		return gitDir, cleanup, err
	}
	if err := fixture.daemon.publishChange(ctx, c, "team/repo", source, app.call, withTip); err != nil {
		t.Fatal(err)
	}
	if len(app.writes) != 2 {
		t.Fatalf("writes = %+v", app.writes)
	}
	commit, pull := app.writes[0]["arguments"].(map[string]any), app.writes[1]["arguments"].(map[string]any)
	changes := commit["changes"].([]map[string]any)
	if commit["operation_id"] != uuid5("dark-factory:"+c.Change.String()+":publish-"+c.Head[:8]+"-1") || commit["expected_head_sha"] != stale || len(changes) != 1 || changes[0]["path"] != "f00.txt" ||
		pull["operation_id"] != uuid5("dark-factory:"+c.Change.String()+":pr-"+c.Head[:8]) || pull["head_sha"] != app.tip {
		t.Fatalf("commit = %+v pull = %+v", commit, pull)
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

// A branch an earlier attempt already brought to the head's tree, with no
// pull request, gets its pull request at that branch head and no commit.
func TestFirstPublicationOfAnAlreadyPublishedTreeOpensItsPullRequest(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 1, false)
	ctx := context.Background()
	backend := &publicReviewBackend{}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	app.tip = c.Head // the earlier attempt's commit carries the same tree
	withTip := func(ctx context.Context, ref, head string) (string, func(), error) {
		gitDir, cleanup, err := checkout(ctx, ref, head)
		if err == nil {
			_, err = gitOutput(ctx, gitDir, "-c", "protocol.file.allow=always", "fetch", "--quiet", source, head)
		}
		return gitDir, cleanup, err
	}
	if err := fixture.daemon.publishChange(ctx, c, "team/repo", source, app.call, withTip); err != nil {
		t.Fatal(err)
	}
	if len(app.writes) != 1 || app.writes[0]["name"] != "create_pull_request" || app.writes[0]["arguments"].(map[string]any)["head_sha"] != c.Head {
		t.Fatalf("writes = %+v", app.writes)
	}
	if open, err := fixture.store.OpenPulls(ctx); err != nil || len(open) != 1 || len(open[0].Operations) != 1 {
		t.Fatalf("publication did not claim one review: open=%+v err=%v", open, err)
	}
}

// An empty diff from the main the worker integrated says nothing about a
// stale branch tip: no pull request opens there.
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

// A correction after a send-back goes on the open pull request's branch head
// as one commit carrying only what the worker changed since, with its first
// commit's response lost: the retry publishes nothing twice, replaces the
// body naming the new head under its closing line, and a further pass writes
// nothing.
func TestFactorydPublishesACorrectionOnItsPullRequest(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 2, false)
	ctx := context.Background()
	root, git := filepath.Dir(source), change.TrustedGitExecutable
	app.tip = c.Head // the first publication's tree, as the branch carries it
	if err := os.WriteFile(filepath.Join(root, "f00.txt"), []byte("corrected\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	supervisorGit(t, git, "-C", root, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-q", "-am", "correct")
	c.Head, c.Pull = strings.TrimSpace(supervisorGitOutput(t, git, "-C", root, "rev-parse", "HEAD")), 31
	tip := app.tip
	withTip := func(ctx context.Context, ref, head string) (string, func(), error) {
		gitDir, cleanup, err := checkout(ctx, ref, head)
		if err == nil {
			_, err = gitOutput(ctx, gitDir, "-c", "protocol.file.allow=always", "fetch", "--quiet", source, tip)
		}
		return gitDir, cleanup, err
	}
	app.loseNext = true
	if err := fixture.daemon.publishChange(ctx, c, "team/repo", source, app.call, withTip); err == nil {
		t.Fatal("a lost response reported publication")
	}
	for range 2 {
		if err := fixture.daemon.publishChange(ctx, c, "team/repo", source, app.call, withTip); err != nil {
			t.Fatal(err)
		}
	}
	if len(app.writes) != 2 || app.writes[0]["name"] != "publish_commit" || app.writes[1]["name"] != "update_pull_request_body" {
		t.Fatalf("writes = %+v", app.writes)
	}
	commit, body := app.writes[0]["arguments"].(map[string]any), app.writes[1]["arguments"].(map[string]any)
	changes := commit["changes"].([]map[string]any)
	if commit["operation_id"] != uuid5("dark-factory:"+c.Change.String()+":publish-"+c.Head[:8]+"-1") || commit["expected_head_sha"] != tip || commit["merge_parent_sha"] != nil ||
		commit["branch"] != "factory/"+c.Change.String()[:12] || len(changes) != 1 || changes[0]["path"] != "f00.txt" {
		t.Fatalf("correction commit = %+v", commit)
	}
	text := body["body"].(string)
	if body["operation_id"] != uuid5("dark-factory:"+c.Change.String()+":body-"+app.tip[:8]+"-3") || body["pull_number"] != uint64(31) ||
		!strings.Contains(text, "at "+app.tip+": +2 -1 across 3 files from "+c.Base) || !strings.HasSuffix(text, "\n\nCloses #9") {
		t.Fatalf("corrected body = %+v", body)
	}
}

// A correction may merge main twice after the worker's path was first
// published.  The current main content must survive while the correction's
// own path is applied, including a deliberate merge resolution.
func TestFactorydPublishesCorrectionFromThePRTip(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 1, false)
	ctx := context.Background()
	root, git := filepath.Dir(source), change.TrustedGitExecutable
	registered := strings.TrimSpace(supervisorGitOutput(t, git, "-C", root, "remote", "get-url", "origin"))

	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("worker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	supervisorGit(t, git, "-C", root, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "add", "a.txt")
	supervisorGit(t, git, "-C", root, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-q", "-m", "worker")
	worker := strings.TrimSpace(supervisorGitOutput(t, git, "-C", root, "rev-parse", "HEAD"))

	if err := os.WriteFile(filepath.Join(registered, "a.txt"), []byte("main one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	supervisorGit(t, git, "-C", registered, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "add", "a.txt")
	supervisorGit(t, git, "-C", registered, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-q", "-m", "main one")
	supervisorGit(t, git, "-C", root, "fetch", "-q", "origin", "main")
	supervisorGit(t, git, "-C", root, "merge", "-q", "--no-ff", "-s", "ours", "FETCH_HEAD", "-m", "import main")
	if err := os.WriteFile(filepath.Join(root, "fix.txt"), []byte("correction\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	supervisorGit(t, git, "-C", root, "add", "fix.txt")
	supervisorGit(t, git, "-C", root, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-q", "--amend", "--no-edit")
	c.Head = strings.TrimSpace(supervisorGitOutput(t, git, "-C", root, "rev-parse", "HEAD"))

	if err := os.WriteFile(filepath.Join(registered, "a.txt"), []byte("main two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	supervisorGit(t, git, "-C", registered, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-q", "-am", "main two")
	app.main = strings.TrimSpace(supervisorGitOutput(t, git, "-C", registered, "rev-parse", "HEAD"))
	app.tip, c.Pull = worker, 31
	withTip := func(ctx context.Context, ref, head string) (string, func(), error) {
		gitDir, cleanup, err := checkout(ctx, ref, head)
		if err == nil {
			_, err = gitOutput(ctx, gitDir, "-c", "protocol.file.allow=always", "fetch", "--quiet", source, worker)
		}
		return gitDir, cleanup, err
	}

	if err := fixture.daemon.publishChange(ctx, c, "team/repo", source, app.call, withTip); err != nil {
		t.Fatal(err)
	}
	if len(app.writes) != 2 || app.writes[0]["name"] != "publish_commit" || app.writes[1]["name"] != "update_pull_request_body" {
		t.Fatalf("writes = %+v", app.writes)
	}
	changes := app.writes[0]["arguments"].(map[string]any)["changes"].([]map[string]any)
	var content []byte
	var err error
	if len(changes) == 1 {
		content, err = base64.StdEncoding.DecodeString(changes[0]["content_base64"].(string))
	}
	if err != nil || len(changes) != 1 || changes[0]["path"] != "fix.txt" || string(content) != "correction\n" {
		t.Fatalf("correction changes = %+v", changes)
	}
}

// A worker answering a verdict with evidence and no file change (#1706)
// moves no head: its answer replaces the body, instead of publication
// refusing "nothing to publish". The reconciler reviews the head once more.
func TestAnEvidenceOnlyCorrectionReplacesOnlyTheBody(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 1, false)
	ctx := context.Background()
	app.tip, c.Pull = c.Head, 31 // the branch already carries the worker's tree
	c.Task.Result = "Premise not reproduced: the cited line already guards it."
	withTip := func(ctx context.Context, ref, head string) (string, func(), error) {
		gitDir, cleanup, err := checkout(ctx, ref, head)
		if err == nil {
			_, err = gitOutput(ctx, gitDir, "-c", "protocol.file.allow=always", "fetch", "--quiet", source, head)
		}
		return gitDir, cleanup, err
	}
	if err := fixture.daemon.publishChange(ctx, c, "team/repo", source, app.call, withTip); err != nil {
		t.Fatal(err)
	}
	if len(app.writes) != 1 || app.writes[0]["name"] != "update_pull_request_body" || !strings.Contains(app.writes[0]["arguments"].(map[string]any)["body"].(string), "Premise not reproduced") {
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
	if first, second := app.writes[0]["arguments"].(map[string]any)["operation_id"], app.writes[1]["arguments"].(map[string]any)["operation_id"]; first == second {
		t.Fatalf("fresh operation ids = %v, %v", first, second)
	}
}

func TestIndeterminatePublicationEscalatesOnceAfterFreshOperation(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 1, false)
	app.unavailable, app.indeterminateAgain = true, true
	if err := fixture.daemon.publishChange(context.Background(), c, "team/repo", source, app.call, checkout); err != nil {
		t.Fatal(err)
	}
	document, found, err := fixture.store.ReviewOperation(context.Background(), c.Task.ProjectID, kernel.PublishFailureID(c.Change, c.Revision))
	var op review.Operation
	if err != nil || !found || json.Unmarshal(document, &op) != nil || op.Escalation == "" {
		t.Fatalf("indeterminate failure = %s %v %v", document, found, err)
	}
	if err := fixture.daemon.publishChange(context.Background(), c, "team/repo", source, app.call, checkout); err != nil {
		t.Fatal(err)
	}
	if len(app.writes) != 2 {
		t.Fatalf("indeterminate writes = %d, want one original and one fresh", len(app.writes))
	}
	if err := fixture.daemon.publishChange(context.Background(), c, "team/repo", source, app.call, checkout); err != nil {
		t.Fatal(err)
	}
	document, _, err = fixture.store.ReviewOperation(context.Background(), c.Task.ProjectID, kernel.PublishFailureID(c.Change, c.Revision))
	if err != nil || strings.Count(string(document), "factoryd cannot publish change") != 1 || !strings.Contains(string(document), "observed-missing") {
		t.Fatalf("repeat escalation = %s %v", document, err)
	}
}

func TestIndeterminateLandedFirstCommitIsRecorded(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 1, false)
	app.tip, app.landFirstCommit, app.landedCommit = c.Base, true, c.Head
	withTip := func(ctx context.Context, ref, head string) (string, func(), error) {
		gitDir, cleanup, err := checkout(ctx, ref, head)
		if err == nil {
			_, err = gitOutput(ctx, gitDir, "-c", "protocol.file.allow=always", "fetch", "--quiet", source, head)
		}
		return gitDir, cleanup, err
	}
	for range 2 {
		if err := fixture.daemon.publishChange(context.Background(), c, "team/repo", source, app.call, withTip); err != nil {
			t.Fatal(err)
		}
	}
	if len(app.writes) != 2 || app.writes[0]["name"] != "publish_commit" || app.writes[1]["name"] != "create_pull_request" {
		t.Fatalf("landed first commit writes = %+v", app.writes)
	}
}

func TestIndeterminateCreateThatLandedIsRecorded(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 1, false)
	app.landCreateIndeterminate = true
	app.extraPull = true
	if err := fixture.daemon.publishChange(context.Background(), c, "team/repo", source, app.call, checkout); err != nil {
		t.Fatal(err)
	}
	if err := fixture.daemon.publishChange(context.Background(), c, "team/repo", source, app.call, checkout); err != nil {
		t.Fatal(err)
	}
	if len(app.writes) != 2 || app.writes[1]["name"] != "create_pull_request" {
		t.Fatalf("landed create writes = %+v", app.writes)
	}
	if open, err := fixture.store.OpenPulls(context.Background()); err != nil || len(open) != 1 {
		t.Fatalf("landed create was not recorded: %+v %v", open, err)
	}
}

func TestIndeterminateBodyRetryIsObservedAndNotReplayed(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 2, false)
	ctx := context.Background()
	app.tip, c.Pull = c.Head, 31
	root, git := filepath.Dir(source), change.TrustedGitExecutable
	if err := os.WriteFile(filepath.Join(root, "f00.txt"), []byte("corrected\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	supervisorGit(t, git, "-C", root, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-q", "-am", "correct")
	c.Head = strings.TrimSpace(supervisorGitOutput(t, git, "-C", root, "rev-parse", "HEAD"))
	app.bodyIndeterminate = true
	for range 3 {
		if err := fixture.daemon.publishChange(ctx, c, "team/repo", source, app.call, checkout); err != nil {
			t.Fatal(err)
		}
	}
	if app.bodyWrites != 2 || len(app.writes) != 3 {
		t.Fatalf("body retry writes = %d / %+v", app.bodyWrites, app.writes)
	}
}

func TestIndeterminateLandedBodyUpdateIsRecorded(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 1, false)
	app.tip, c.Pull, app.bodyIndeterminate, app.landBodyIndeterminate = c.Head, 31, true, true
	for range 3 {
		if err := fixture.daemon.publishChange(context.Background(), c, "team/repo", source, app.call, checkout); err != nil {
			t.Fatal(err)
		}
	}
	if app.bodyWrites != 2 || len(app.writes) != 2 {
		t.Fatalf("landed body writes = %d / %+v", app.bodyWrites, app.writes)
	}
}

func TestIndeterminateLandedCorrectionCommitBatchIsRecorded(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 101, false)
	app.tip, c.Pull, app.landSecondCommit = c.Base, 31, true
	if err := fixture.daemon.publishChange(context.Background(), c, "team/repo", source, app.call, checkout); err != nil {
		t.Fatal(err)
	}
	if err := fixture.daemon.publishChange(context.Background(), c, "team/repo", source, app.call, checkout); err != nil {
		t.Fatal(err)
	}
	if app.commitWrites != 3 || len(app.writes) != 4 {
		t.Fatalf("landed correction commits = %d / %+v", app.commitWrites, app.writes)
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
	c.Task.Result = "Partial: items 1 and 5. Fixes #9 once item 2 lands.\n" + strings.Repeat("report line\n", 3000) + "Refs #9" // beyond the 24000-byte body
	if err := fixture.daemon.publishChange(context.Background(), c, "team/repo", source, app.call, checkout); err != nil {
		t.Fatal(err)
	}
	if pull := app.writes[len(app.writes)-1]["arguments"].(map[string]any); pull["close_on_merge"] != false || pull["issue_number"] != uint64(9) || strings.Contains(pull["body"].(string), "Fixes #9") {
		t.Fatalf("pull request = %+v", pull)
	}
}

// A correction keeps a referencing pull request referencing, and one that
// leaves the Change changing no file (#1796) stops closing the issue.
func TestACorrectionNeverTurnsAnUnfinishedIntakeIntoAClose(t *testing.T) {
	for name, revert := range map[string]bool{"partial pull request": false, "zero-file Change": true} {
		t.Run(name, func(t *testing.T) {
			fixture, c, source, app, checkout := publishFixture(t, 1, false)
			root, git := filepath.Dir(source), change.TrustedGitExecutable
			tip := c.Head
			app.tip, c.Pull = tip, 31
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
			withTip := func(ctx context.Context, ref, head string) (string, func(), error) {
				gitDir, cleanup, err := checkout(ctx, ref, head)
				if err == nil {
					_, err = gitOutput(ctx, gitDir, "-c", "protocol.file.allow=always", "fetch", "--quiet", source, tip)
				}
				return gitDir, cleanup, err
			}
			if err := fixture.daemon.publishChange(context.Background(), c, "team/repo", source, app.call, withTip); err != nil {
				t.Fatal(err)
			}
			body := app.writes[len(app.writes)-1]
			if text, _ := body["arguments"].(map[string]any)["body"].(string); body["name"] != "update_pull_request_body" || !strings.HasPrefix(text, "Answered the review.\n\nRefs #9\n\nPublished by factoryd") ||
				!strings.HasSuffix(text, "The merge queue runs the gate.\n\nRefs #9") || strings.Contains(strings.ToLower(text), "closes") {
				t.Fatalf("writes = %+v", app.writes)
			}
		})
	}
}

func TestClosesIssueOnlyForACompletingChange(t *testing.T) {
	for _, tc := range []struct {
		text  string
		files int
		want  bool
	}{{"Done.", 2, true}, {"Done.", 0, false}, {"refs #9, partial", 2, false}, {"Refs #90 is separate.", 2, true}, {"Body\n\nRefs #9\n", 1, false}} {
		if got := closesIssue(tc.text, 9, tc.files); got != tc.want {
			t.Errorf("closesIssue(%q, 9, %d) = %v", tc.text, tc.files, got)
		}
	}
}

func TestReferencingDisarmsEveryClosingKeywordForTheIssue(t *testing.T) {
	in := "Closes #9\nfixed: team/repo#9, Resolves https://github.com/team/repo/issues/9 and closes #90."
	if got, want := referencing(in, 9), "Refs #9\nRefs team/repo#9, Refs https://github.com/team/repo/issues/9 and closes #90."; got != want {
		t.Fatalf("referencing = %q, want %q", got, want)
	}
}
