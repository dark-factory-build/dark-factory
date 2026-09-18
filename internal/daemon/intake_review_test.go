//go:build darwin || linux

package daemon

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
)

func TestIntakeReviewUsesFrozenTargetAndRefusesWithdrawnLineage(t *testing.T) {
	fixture := newDispatchFixture(t)
	ctx := t.Context()
	project, agent := mustProjectID(t, testID(180)), mustAgentID(t, testID(181))
	if _, err := fixture.store.CreateProject(ctx, kernel.NewProject{ID: project, Name: "review", Root: "/review"}, mustKernelTime(t, 101)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.CreateAgent(ctx, kernel.NewAgent{ID: agent, ProjectID: project, Name: "overseer", Role: kernel.RoleOrchestrator, Provider: kernel.ProviderShell, ToolBudgetLimit: 10}, mustKernelTime(t, 102)); err != nil {
		t.Fatal(err)
	}
	target, _ := kernel.RepositoryIDFromBytes([]byte("review-target-id"))
	proof := kernel.RepositorySourceIdentity{RootDevice: 1, RootInode: 2, GitDevice: 1, GitInode: 3, OriginDigest: [32]byte{1}, PublicationRepository: "delivery/target"}
	if _, err := fixture.store.AddProjectRepository(ctx, kernel.NewProjectRepository{ID: target, ProjectID: project, Name: "publication", Root: "/review-target", BaseRef: "release", SourceIdentity: &proof}, mustKernelTime(t, 103)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.BindRepositoryGitHubID(ctx, target, 42); err != nil {
		t.Fatal(err)
	}
	repository, _, err := fixture.store.ProjectRepository(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.SetProjectRepositoryDefault(ctx, target, repository.Revision, mustKernelTime(t, 103)); err != nil {
		t.Fatal(err)
	}
	raw, _ := hex.DecodeString(testID(182))
	sourceID, _ := kernel.IntakeSourceIDFromBytes(raw)
	receipt := kernel.LegacyIntakeReceipt{RepositoryID: 41, PlanHash: [32]byte{1}, ConfigHash: [32]byte{2}, JournalHash: [32]byte{3}}
	source, err := fixture.store.CommitLegacyIntakeMigration(ctx, kernel.NewIntakeSource{ID: sourceID, ProjectID: project, TargetRepositoryID: target, OverseerAgentID: agent, GitHubRepositoryID: 41, GitHubRepositoryName: "feed/source", Policy: kernel.IntakePolicyManual, PollSeconds: 60, AdmissionLimit: 1}, receipt, nil, mustKernelTime(t, 104))
	if err != nil {
		t.Fatal(err)
	}
	source, err = fixture.store.SetIntakeSourceEnabled(ctx, source.ID, source.Revision, true, mustKernelTime(t, 105))
	if err != nil {
		t.Fatal(err)
	}
	issue := maintainer.Issue{ID: 1, Number: 9, NodeID: "I_source", Title: "Accepted", Body: "Reviewed", State: "open"}
	issue.Author.Login, issue.Author.Type = "human", "User"
	accepted, err := fixture.store.AcceptIntakeSnapshot(ctx, source.ID, intakeSnapshot(source, issue), mustKernelTime(t, 106))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.store.ImportIntakeAcceptance(ctx, accepted.ID, mustKernelTime(t, 107)); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.store.WithdrawIntakeAcceptance(ctx, accepted.ID, mustKernelTime(t, 108)); err != nil {
		t.Fatal(err)
	}
	primary, _, err := fixture.store.ProjectRepository(ctx, kernel.RepositoryID(project))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.SetProjectRepositoryDefault(ctx, primary.ID, primary.Revision, mustKernelTime(t, 109)); err != nil {
		t.Fatal(err)
	}
	fixture.daemon.github = &maintainer.Host{} // No credential: no fixture can reach GitHub.
	fixture.daemon.intakeIssues = func(_ context.Context, repository string, id uint64, _ uint32, _ string, number uint64) (maintainer.IssuePage, error) {
		if repository != "feed/source" || id != 41 || number != 9 {
			t.Fatalf("wrong source %s/%d/%d", repository, id, number)
		}
		return maintainer.IssuePage{RepositoryID: 41, Issues: []maintainer.Issue{issue}}, nil
	}
	input := api.IntakeInput{Action: "review", SourceID: source.ID.String(), ProjectID: project.String(), IssueNumber: 9, Configuration: &api.IntakeConfiguration{Repository: "feed/source", TargetRepositoryID: target.String()}, Legacy: &api.LegacyIntakeInput{PlanHash: hex.EncodeToString(receipt.PlanHash[:]), ConfigHash: hex.EncodeToString(receipt.ConfigHash[:]), JournalHash: hex.EncodeToString(receipt.JournalHash[:])}, Review: &api.IntakeReviewInput{Tool: "submit_pull_request_review", PullNumber: 7, HeadSHA: strings.Repeat("a", 40), OperationID: "12345678-1234-1234-1234-123456789abc", Event: "ALLOW", Body: "reviewed"}}
	if result := fixture.daemon.Intake(ctx, input); result.State != "withdrawn" {
		t.Fatalf("withdrawn review = %+v", result)
	}
	input.Review = &api.IntakeReviewInput{Tool: "enqueue_pull_request", PullNumber: 7, HeadSHA: strings.Repeat("a", 40), OperationID: "12345678-1234-1234-1234-123456789abc", Base: "release", ReviewedBodyDigest: "sha256:" + strings.Repeat("b", 64)}
	if result := fixture.daemon.Intake(ctx, input); result.State != "withdrawn" {
		t.Fatalf("withdrawn enqueue = %+v", result)
	}
	input.Configuration.TargetRepositoryID = kernel.RepositoryID(project).String()
	if result := fixture.daemon.Intake(ctx, input); result.State != "repository_unbound" {
		t.Fatalf("default substituted = %+v", result)
	}
	input.ProjectID = testID(190)
	if result := fixture.daemon.Intake(ctx, input); result.State != "denied" {
		t.Fatalf("foreign project = %+v", result)
	}
}
