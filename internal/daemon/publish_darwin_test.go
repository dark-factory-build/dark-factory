package daemon

import (
	"context"
	"crypto/sha1"
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
	main     string
	tip      string // the pull request branch's head
	journal  map[string]json.RawMessage
	writes   []map[string]any // every write sent
	refuse   string           // a path publish_commit refuses
	loseNext bool             // the next write lands but its response is lost
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
		return json.Marshal(map[string]any{"operation_id": id, "state": "missing"})
	}
	if _, replay := f.journal[id]; replay {
		return nil, fmt.Errorf("%w: conflict: operation %s replayed", review.ErrRejected, id)
	}
	f.writes = append(f.writes, map[string]any{"name": name, "arguments": arguments})
	var result json.RawMessage
	switch name {
	case "publish_commit":
		if f.tip != "" && arguments["expected_head_sha"] != f.tip {
			return nil, fmt.Errorf("%w: conflict: branch is at %s, not %v", review.ErrRejected, f.tip, arguments["expected_head_sha"])
		}
		for _, entry := range arguments["changes"].([]map[string]any) {
			if f.refuse != "" && strings.HasPrefix(entry["path"].(string), f.refuse) {
				return nil, fmt.Errorf("%w: refused: %s cannot be written", review.ErrRejected, f.refuse)
			}
		}
		f.tip = fmt.Sprintf("%x", sha1.Sum([]byte(id)))
		result, _ = json.Marshal(map[string]any{"branch": arguments["branch"], "commit_sha": f.tip, "parent_sha": arguments["expected_head_sha"]})
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
	checkout := func(context.Context, string, string) (string, func(), error) {
		clone := filepath.Join(t.TempDir(), "clone")
		supervisorGit(t, git, "clone", "-q", "--shared", registered, clone)
		return filepath.Join(clone, ".git"), func() {}, nil
	}
	return fixture, candidate, filepath.Join(root, ".git"), &fakePublishMaintainer{main: base, journal: map[string]json.RawMessage{}}, checkout
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

// A branch an earlier attempt already brought to the head's tree, with no
// pull request, gets its pull request at that branch head and no commit.
func TestFirstPublicationOfAnAlreadyPublishedTreeOpensItsPullRequest(t *testing.T) {
	fixture, c, source, app, checkout := publishFixture(t, 1, false)
	ctx := context.Background()
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
	if body["operation_id"] != uuid5("dark-factory:"+c.Change.String()+":body-"+app.tip[:8]) || body["pull_number"] != uint64(31) ||
		!strings.Contains(text, "at "+app.tip+": +2 -1 across 3 files from "+c.Base) || !strings.HasSuffix(text, "\n\nCloses #9") {
		t.Fatalf("corrected body = %+v", body)
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
	if err != nil || !found || json.Unmarshal(document, &op) != nil || op.State != "publish_failed" || !op.Handled ||
		!strings.HasPrefix(op.Escalation, "factoryd cannot publish change "+c.Change.String()+" for task "+c.Task.ID.String()+": ") || !strings.Contains(op.Escalation, "refused: .github/workflows") {
		t.Fatalf("failure record = %s %v %v", document, found, err)
	}
	if pending, err := fixture.store.InFlightReviewOperations(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("in flight = %+v %v", pending, err)
	}
	if len(app.writes) != 1 {
		t.Fatalf("writes = %d", len(app.writes))
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
	for range 2 {
		if err := fixture.daemon.publishSettledChange(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	page, err := fixture.store.Production(ctx, c.Task.ProjectID, 0, 8, kernel.UnixMillis{})
	if err != nil {
		t.Fatal(err)
	}
	escalations := 0
	for _, record := range page.Records {
		var op review.Operation
		if record.Kind == "reviewer" && json.Unmarshal(record.Document, &op) == nil && op.Escalation != "" {
			escalations++
			if op.ID != kernel.PublishFailureID(c.Change, c.Revision) || op.Escalation != "factoryd cannot publish change "+c.Change.String()+" for task "+c.Task.ID.String()+": repository disabled for new work" {
				t.Fatalf("escalation = %+v", op)
			}
		}
		if record.Kind == "pull_request" {
			t.Fatalf("published into a disabled repository: %s", record.Document)
		}
	}
	if escalations != 1 {
		t.Fatalf("escalations = %d, want 1", escalations)
	}
}
