package daemon

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
)

// The operator and scheduler paths carry only the exact durable accepted
// revision into a task body: a trusted author's later edit, a replayed older
// revision, and a withdrawn receipt all fail to create or amend work.
func TestIntakeTickMaterializesOnlyExactAcceptedRevision(t *testing.T) {
	fixture := newDispatchFixture(t)
	ctx := context.Background()
	clock := int64(1000)
	fixture.daemon.now = func() time.Time { clock++; return time.UnixMilli(clock) }
	project := mustProjectID(t, testID(186))
	if _, err := fixture.store.CreateProject(ctx, kernel.NewProject{ID: project, Name: "exact", Root: "/intake-exact"}, mustKernelTime(t, 101)); err != nil {
		t.Fatal(err)
	}
	raw, _ := hex.DecodeString(testID(187))
	id, _ := kernel.IntakeSourceIDFromBytes(raw)
	source, err := fixture.store.CreateIntakeSource(ctx, kernel.NewIntakeSource{ID: id, ProjectID: project, TargetRepositoryID: kernel.RepositoryID(project), GitHubRepositoryID: 42, GitHubRepositoryName: "team/issues", Policy: kernel.IntakePolicyTrustedAuthors, TrustedGitHubLogins: []string{"maintainer"}, PollSeconds: 60, AdmissionLimit: 5}, mustKernelTime(t, 102))
	if err != nil {
		t.Fatal(err)
	}
	source, err = fixture.store.SetIntakeSourceEnabled(ctx, id, source.Revision, true, mustKernelTime(t, 103))
	if err != nil {
		t.Fatal(err)
	}
	issue := maintainer.Issue{ID: 90, NodeID: "I_exact", Number: 3, Title: "Reviewed", Body: "first accepted body", State: "open", URL: "https://github.com/team/issues/issues/3"}
	issue.Author.Login, issue.Author.Type = "maintainer", "User"
	fixture.daemon.intakeIssues = func(context.Context, string, uint64, uint32, string, uint64) (maintainer.IssuePage, error) {
		return maintainer.IssuePage{RepositoryID: 42, Issues: []maintainer.Issue{issue}}, nil
	}
	tick := api.IntakeInput{Action: "tick", SourceID: id.String(), Page: 1}
	accept := func() api.IntakeResult {
		hash := intakeSnapshot(source, issue).ContentHash()
		return fixture.daemon.Intake(ctx, api.IntakeInput{Action: "accept", SourceID: id.String(), ExpectedRevision: uint64(source.Revision.Int64()), IssueNumber: issue.Number, ContentHash: hex.EncodeToString(hash[:])})
	}
	taskBody := func(taskID string) string {
		decoded, err := decodeID(taskID, kernel.TaskIDFromBytes)
		if err != nil {
			t.Fatal(err)
		}
		task, found, err := fixture.store.Task(ctx, decoded)
		if err != nil || !found {
			t.Fatalf("task %s: found=%v err=%v", taskID, found, err)
		}
		return task.Body
	}
	requireNoImport := func(step, reason string) {
		t.Helper()
		got := fixture.daemon.Intake(ctx, tick)
		if got.State != "ok" || len(got.ImportedTasks) != 0 || len(got.Candidates) != 1 || got.Candidates[0].Reason != reason {
			t.Fatalf("%s tick: %+v", step, got)
		}
	}

	first := fixture.daemon.Intake(ctx, tick)
	if first.State != "ok" || len(first.ImportedTasks) != 1 {
		t.Fatalf("trusted import: %+v", first)
	}
	firstTask := first.ImportedTasks[0]
	if !strings.HasPrefix(taskBody(firstTask), "first accepted body\n\nSource: ") {
		t.Fatalf("first task body = %q", taskBody(firstTask))
	}

	// A trusted author's edit after acceptance needs a fresh review and leaves
	// the queued task's body as accepted.
	issue.Body = "edited after acceptance"
	requireNoImport("edited", string(kernel.IntakeContentChanged))
	if !strings.HasPrefix(taskBody(firstTask), "first accepted body\n\nSource: ") {
		t.Fatalf("edit reached queued task: %q", taskBody(firstTask))
	}
	second := accept()
	if second.State != "accepted" || second.TaskID == firstTask {
		t.Fatalf("accept edit: %+v", second)
	}
	if got := fixture.daemon.Intake(ctx, tick); got.State != "ok" || len(got.ImportedTasks) != 1 || got.ImportedTasks[0] != second.TaskID {
		t.Fatalf("accepted edit import: %+v", got)
	}

	// Reverting the issue to the older accepted hash is a stale replay.
	issue.Body = "first accepted body"
	if got := accept(); got.State != "conflict" {
		t.Fatalf("stale replay accepted: %+v", got)
	}
	requireNoImport("stale", string(kernel.IntakeContentChanged))
	if !strings.HasPrefix(taskBody(firstTask), "first accepted body\n\nSource: ") || !strings.HasPrefix(taskBody(second.TaskID), "edited after acceptance\n\nSource: ") {
		t.Fatal("stale replay amended accepted work")
	}

	// A withdrawn receipt cannot materialize again, even with matching content.
	issue.Body = "edited after acceptance"
	if got := fixture.daemon.Intake(ctx, api.IntakeInput{Action: "withdraw", AcceptanceID: second.AcceptanceID}); got.State != "withdrawn" {
		t.Fatalf("withdraw: %+v", got)
	}
	if got := accept(); got.State != "withdrawn" || got.AcceptanceID != second.AcceptanceID {
		t.Fatalf("re-accept withdrawn: %+v", got)
	}
	if got := fixture.daemon.Intake(ctx, api.IntakeInput{Action: "import", AcceptanceID: second.AcceptanceID}); got.State != "conflict" {
		t.Fatalf("withdrawn import: %+v", got)
	}
	requireNoImport("withdrawn", string(kernel.IntakeWithdrawn))
	decoded, _ := decodeID(second.TaskID, kernel.TaskIDFromBytes)
	if task, _, err := fixture.store.Task(ctx, decoded); err != nil || task.Status != kernel.TaskCancelled {
		t.Fatalf("withdrawn task = %+v, %v", task, err)
	}
}
