//go:build darwin || linux

package daemon

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
)

func TestIntakeAcceptedContentSurvivesLargeBacklogAndMetadataChanges(t *testing.T) {
	fixture := newDispatchFixture(t)
	ctx := context.Background()
	project := mustProjectID(t, testID(180))
	agent := mustAgentID(t, testID(181))
	if _, err := fixture.store.CreateProject(ctx, kernel.NewProject{ID: project, Name: "test", Root: "/intake-test"}, mustKernelTime(t, 101)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.CreateAgent(ctx, kernel.NewAgent{ID: agent, ProjectID: project, Name: "overseer", Role: kernel.RoleOrchestrator, Provider: kernel.ProviderShell, ToolBudgetLimit: 10}, mustKernelTime(t, 102)); err != nil {
		t.Fatal(err)
	}
	raw, _ := hex.DecodeString(testID(182))
	id, _ := kernel.IntakeSourceIDFromBytes(raw)
	source, err := fixture.store.CreateIntakeSource(ctx, kernel.NewIntakeSource{ID: id, ProjectID: project, TargetRepositoryID: kernel.RepositoryID(project), OverseerAgentID: agent, GitHubRepositoryID: 42, GitHubRepositoryName: "team/issues", Policy: kernel.IntakePolicyManual, PollSeconds: 60, AdmissionLimit: 1}, mustKernelTime(t, 103))
	if err != nil {
		t.Fatal(err)
	}
	source, err = fixture.store.SetIntakeSourceEnabled(ctx, id, source.Revision, true, mustKernelTime(t, 104))
	if err != nil {
		t.Fatal(err)
	}
	issue := maintainer.Issue{ID: 81, NodeID: "I_fixture", Number: 99999, Title: "Reviewed title", Body: "Exact instructions", State: "open", URL: "https://github.com/team/issues/issues/99999"}
	issue.Author.Login, issue.Author.Type = "outsider", "User"
	exactReads := 0
	fixture.daemon.intakeIssues = func(_ context.Context, repository string, repositoryID uint64, page uint32, label string, number uint64) (maintainer.IssuePage, error) {
		if repository != "team/issues" || repositoryID != 42 {
			t.Fatal("wrong source")
		}
		if number != 0 {
			exactReads++
			return maintainer.IssuePage{RepositoryID: 42, Issues: []maintainer.Issue{issue}}, nil
		}
		next := page + 1
		// Discovery is still walking an older backlog; it cannot starve acceptance.
		return maintainer.IssuePage{RepositoryID: 42, Issues: []maintainer.Issue{}, NextPage: &next}, nil
	}
	tick := api.IntakeInput{Action: "tick", SourceID: id.String(), Page: 1}
	if got := fixture.daemon.Intake(ctx, tick); got.State != "ok" || len(got.ImportedTasks) != 0 || got.NextPage == nil {
		t.Fatalf("unaccepted backlog: %+v", got)
	}
	snapshot := intakeSnapshot(source, issue)
	hash := snapshot.ContentHash()
	accept := api.IntakeInput{Action: "accept", SourceID: id.String(), ExpectedRevision: uint64(source.Revision.Int64()), IssueNumber: issue.Number, ContentHash: hex.EncodeToString(hash[:])}
	receipt := fixture.daemon.Intake(ctx, accept)
	if receipt.State != "accepted" {
		t.Fatalf("accept: %+v", receipt)
	}
	first := fixture.daemon.Intake(ctx, tick)
	if first.State != "ok" || len(first.ImportedTasks) != 1 || first.ImportedTasks[0] != receipt.TaskID || exactReads < 2 {
		t.Fatalf("priority import: %+v", first)
	}
	issue.UpdatedAt = "2026-09-18T12:00:00Z"
	issue.Labels = []string{"unrelated"}
	if got := fixture.daemon.Intake(ctx, tick); got.State != "ok" || len(got.ImportedTasks) != 0 {
		t.Fatalf("metadata repeated work: %+v", got)
	}
	issue.Body = "unreviewed replacement"
	if got := fixture.daemon.Intake(ctx, accept); got.State != "content_changed" {
		t.Fatalf("stale acceptance: %+v", got)
	}
	acceptedID, _ := browserID(receipt.AcceptanceID, kernel.IntakeAcceptanceIDFromBytes)
	accepted, found, err := fixture.store.IntakeAcceptance(ctx, acceptedID)
	if err != nil || !found || accepted.Snapshot.Body != "Exact instructions" {
		t.Fatal("approved snapshot replaced")
	}
	if got := fixture.daemon.Intake(ctx, api.IntakeInput{Action: "withdraw", AcceptanceID: receipt.AcceptanceID}); got.State != "withdrawn" {
		t.Fatalf("withdraw: %+v", got)
	}
	task, found, err := fixture.store.Task(ctx, accepted.TaskID)
	if err != nil || !found || task.Status != kernel.TaskCancelled || task.Body != "Exact instructions" {
		t.Fatal("withdraw did not preserve and cancel queued work")
	}
	issue.Body = "Exact instructions"
	read := fixture.daemon.intakeIssues
	fixture.daemon.intakeIssues = func(ctx context.Context, repository string, id uint64, page uint32, label string, number uint64) (maintainer.IssuePage, error) {
		if _, err := fixture.store.SetIntakeSourceEnabled(ctx, source.ID, source.Revision, false, mustKernelTime(t, 1000)); err != nil {
			t.Fatal(err)
		}
		return read(ctx, repository, id, page, label, number)
	}
	if got := fixture.daemon.Intake(ctx, accept); got.State != "conflict" {
		t.Fatalf("source changed during remote acceptance read: %+v", got)
	}
}
