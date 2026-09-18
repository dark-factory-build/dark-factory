//go:build darwin || linux

package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
)

func TestLegacyCutoverLivePlanHistorySuppressionAndPriority(t *testing.T) {
	fixture := newDispatchFixture(t)
	ctx := context.Background()
	project, agent := mustProjectID(t, testID(180)), mustAgentID(t, testID(181))
	if _, err := fixture.store.CreateProject(ctx, kernel.NewProject{ID: project, Name: "legacy", Root: "/legacy"}, mustKernelTime(t, 101)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.CreateAgent(ctx, kernel.NewAgent{ID: agent, ProjectID: project, Name: "overseer", Role: kernel.RoleOrchestrator, Provider: kernel.ProviderShell, ToolBudgetLimit: 10}, mustKernelTime(t, 102)); err != nil {
		t.Fatal(err)
	}
	target, _ := kernel.RepositoryIDFromBytes([]byte("destination-repo"))
	repository, err := fixture.store.AddProjectRepository(ctx, kernel.NewProjectRepository{ID: target, ProjectID: project, Name: "actual-default", Root: "/legacy-other", BaseRef: "release"}, mustKernelTime(t, 102))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.SetProjectRepositoryDefault(ctx, repository.ID, repository.Revision, mustKernelTime(t, 102)); err != nil {
		t.Fatal(err)
	}
	issue := maintainer.Issue{ID: 81, Number: 1, NodeID: "I_legacy", Title: "Original", Body: "Reviewed", State: "open", Labels: []string{"ready", "urgent"}}
	issue.Author.Login, issue.Author.Type = "owner", "User"
	fingerprint := fmt.Sprintf("%064x", 1)
	taskDigest := sha256.Sum256([]byte("source\x00FACTORY_SOURCE team/issues#1\x00" + fingerprint))
	taskID, _ := kernel.TaskIDFromBytes(taskDigest[:16])
	incarnationDigest := sha256.Sum256([]byte("incarnation\x00" + taskID.String()))
	incarnation, _ := kernel.IncarnationIDFromBytes(incarnationDigest[:16])
	old, err := fixture.store.EnqueueTask(ctx, kernel.NewTask{ID: taskID, IncarnationID: incarnation, ProjectID: project, AssignedAgentID: agent, Title: "Legacy", Body: issue.Body}, mustKernelTime(t, 103))
	if err != nil {
		t.Fatal(err)
	}
	sourceID := testID(182)
	hash := sha256.Sum256([]byte(issue.Title + "\x00" + issue.Body))
	input := api.IntakeInput{Action: "legacy_preview", SourceID: sourceID, ProjectID: project.String(), Configuration: &api.IntakeConfiguration{Repository: "team/issues", OverseerAgentID: agent.String(), Label: "ready", Policy: "trusted_authors", TrustedAuthors: []string{"owner"}, PollSeconds: 60, AdmissionLimit: 25, PriorityByLabel: map[string]int64{"urgent": 8, "later": -2}}, Legacy: &api.LegacyIntakeInput{ConfigHash: fmt.Sprintf("%064x", 2), JournalHash: fmt.Sprintf("%064x", 3), History: []api.LegacyIntakeHistory{{Number: 1, Kind: "processed", TaskID: taskID.String(), IncarnationID: incarnation.String(), Fingerprint: fingerprint, HistoricalContentHash: hex.EncodeToString(hash[:])}}}}
	status := maintainer.Status{State: "connected", Repositories: []maintainer.Delegation{{Repository: "team/issues", RepositoryID: 42}}}
	issueCount := 1
	identity := uint64(42)
	fixture.daemon.intakeIssues = func(_ context.Context, repository string, id uint64, page uint32, label string, number uint64) (maintainer.IssuePage, error) {
		if repository != "team/issues" || id != 42 {
			t.Fatalf("wrong live source %s %d", repository, id)
		}
		result := []maintainer.Issue{issue}
		if number > 1 {
			result[0].Number = number
			result[0].NodeID = fmt.Sprint("I_", number)
		}
		if number == 0 {
			for i := 2; i <= issueCount; i++ {
				other := issue
				other.Number = uint64(i)
				other.NodeID = fmt.Sprint("I_", i)
				result = append(result, other)
			}
		}
		return maintainer.IssuePage{RepositoryID: identity, Issues: result}, nil
	}
	preview := func() api.IntakeResult {
		return fixture.daemon.legacyIntakeWithStatus(ctx, input, mustKernelTime(t, 110), status)
	}
	if result := preview(); result.State != "legacy_blocked" {
		t.Fatalf("active legacy accepted: %+v", result)
	}
	old, err = fixture.store.UpdateTask(ctx, old.ID, old.Revision, kernel.TaskPatch{Cancel: true}, mustKernelTime(t, 104))
	if err != nil {
		t.Fatal(err)
	}
	first := preview()
	if first.State != "legacy_preview" || first.Legacy.Issues[0].TaskID != old.ID.String() || first.Legacy.TargetRepositoryID != target.String() {
		t.Fatalf("terminal preview: %+v", first)
	}
	primary, _, err := fixture.store.ProjectRepository(ctx, kernel.RepositoryID(project))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.SetProjectRepositoryDefault(ctx, primary.ID, primary.Revision, mustKernelTime(t, 105)); err != nil {
		t.Fatal(err)
	}
	if result := preview(); result.State != "legacy_blocked" {
		t.Fatalf("mixed retained destination not refused: %+v", result)
	}
	repository, _, err = fixture.store.ProjectRepository(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.SetProjectRepositoryDefault(ctx, target, repository.Revision, mustKernelTime(t, 106)); err != nil {
		t.Fatal(err)
	}
	issue.Labels = append(issue.Labels, "unrelated")
	if result := preview(); result.Legacy.PlanHash != first.Legacy.PlanHash {
		t.Fatal("unrelated metadata invalidates migration")
	}
	issue.Author.Login = "outsider"
	if result := preview(); result.Legacy.PlanHash == first.Legacy.PlanHash {
		t.Fatal("eligibility change ignored")
	}
	issue.Author.Login = "owner"
	identity = 43
	if result := preview(); result.State != "denied" {
		t.Fatalf("replacement identity accepted: %+v", result)
	}
	identity = 42
	issueCount = 201
	if result := preview(); result.State != "overflow" {
		t.Fatalf("partial overflow: %+v", result)
	}
	issueCount = 3
	missingDigest := sha256.Sum256([]byte("source\x00FACTORY_SOURCE team/issues#3\x00" + fingerprint))
	missingTask, _ := kernel.TaskIDFromBytes(missingDigest[:16])
	missingInc := sha256.Sum256([]byte("incarnation\x00" + missingTask.String()))
	input.Legacy.History = append(input.Legacy.History, api.LegacyIntakeHistory{Number: 3, Kind: "processed", TaskID: missingTask.String(), IncarnationID: hex.EncodeToString(missingInc[:16]), Fingerprint: fingerprint})
	first = preview()
	unchangedPolicyHash := first.Legacy.PlanHash
	input.Legacy.ManualAppAuthors = []string{"app/factory"}
	first = preview()
	if first.Legacy.PlanHash == unchangedPolicyHash || !first.Legacy.RequiresPolicyAcknowledgement {
		t.Fatal("policy narrowing missing from reviewed hash")
	}
	input.Action = "legacy_commit"
	input.Legacy.PlanHash = first.Legacy.PlanHash
	if result := preview(); result.State != "policy_acknowledgement_required" {
		t.Fatalf("unacknowledged cutover: %+v", result)
	}
	input.Legacy.AcknowledgePolicyNarrowing = true
	input.Action = "legacy_commit"
	input.Legacy.PlanHash = first.Legacy.PlanHash
	result := preview()
	if result.State != "legacy_committed" {
		t.Fatalf("commit: %+v", result)
	}
	if result := preview(); result.State != "legacy_committed" {
		t.Fatalf("lost response: %+v", result)
	}
	status.Repositories[0].Repository = "team/renamed"
	if result := preview(); result.State != "legacy_committed" {
		t.Fatalf("same numeric ID rename broke receipt replay: %+v", result)
	}
	status.Repositories[0].RepositoryID = 43
	if result := preview(); result.State != "denied" {
		t.Fatalf("replacement numeric ID replayed: %+v", result)
	}
	status.Repositories[0].Repository, status.Repositories[0].RepositoryID = "team/issues", 42
	input.Action = "legacy_preview"
	input.SourceID = testID(183)
	input.Legacy.PlanHash = ""
	if result := preview(); result.State != "legacy_blocked" {
		t.Fatalf("existing source collision was not caught before cutover: %+v", result)
	}
	input.SourceID = sourceID
	raw, _ := hex.DecodeString(sourceID)
	id, _ := kernel.IntakeSourceIDFromBytes(raw)
	source, found, err := fixture.store.IntakeSource(ctx, id)
	if err != nil || !found || source.Enabled {
		t.Fatalf("source not paused: %+v %v", source, err)
	}
	source, err = fixture.store.SetIntakeSourceEnabled(ctx, id, source.Revision, true, mustKernelTime(t, 120))
	if err != nil {
		t.Fatal(err)
	}
	tick := fixture.daemon.previewIntake(ctx, source, 1, true, "")
	if tick.State != "ok" || len(tick.ImportedTasks) != 0 || tick.Candidates[0].Reason != "legacy_existing_work" || tick.Candidates[1].Reason != "legacy_suppressed" || tick.Candidates[2].Reason != "legacy_history_unresolved" {
		t.Fatalf("baseline imported: %+v", tick)
	}
	accept := api.IntakeInput{Action: "accept", SourceID: sourceID, ExpectedRevision: uint64(source.Revision.Int64()), IssueNumber: 1, ContentHash: hex.EncodeToString(hash[:])}
	if reply := fixture.daemon.Intake(ctx, accept); reply.State != "legacy_existing_work" || reply.TaskID != old.ID.String() {
		t.Fatalf("duplicate legacy task: %+v", reply)
	}
	accept.IssueNumber = 3
	if reply := fixture.daemon.Intake(ctx, accept); reply.State != "legacy_history_unresolved" {
		t.Fatalf("unknown processed history silently duplicated: %+v", reply)
	}
	accept.IssueNumber = 2
	baselineOnly := fixture.daemon.Intake(ctx, accept)
	if baselineOnly.State != "accepted" {
		t.Fatalf("unprocessed baseline cannot be explicitly reviewed: %+v", baselineOnly)
	}
	lineage := input
	lineage.Action, lineage.IssueNumber = "legacy_lineage", 2
	configCopy, legacyCopy := *input.Configuration, *input.Legacy
	configCopy.TargetRepositoryID = target.String()
	legacyCopy.PlanHash = first.Legacy.PlanHash
	lineage.Configuration, lineage.Legacy = &configCopy, &legacyCopy
	if reply := fixture.daemon.Intake(ctx, lineage); reply.State != "not_found" {
		t.Fatalf("unimported acceptance counted as managed work: %+v", reply)
	}
	if reply := fixture.daemon.Intake(ctx, api.IntakeInput{Action: "import", AcceptanceID: baselineOnly.AcceptanceID}); reply.State != "imported" {
		t.Fatalf("baseline explicit import: %+v", reply)
	}
	accept.IssueNumber = 1
	// A reviewed title/body edit can create new work, while priority-only labels
	// update that same queued task without a new acceptance or incarnation.
	issue.Title = "New reviewed content"
	hash = sha256.Sum256([]byte(issue.Title + "\x00" + issue.Body))
	accept.ContentHash = hex.EncodeToString(hash[:])
	accepted := fixture.daemon.Intake(ctx, accept)
	if accepted.State != "accepted" {
		t.Fatalf("edited explicit acceptance: %+v", accepted)
	}
	imported := fixture.daemon.Intake(ctx, api.IntakeInput{Action: "import", AcceptanceID: accepted.AcceptanceID})
	if imported.State != "imported" {
		t.Fatalf("import: %+v", imported)
	}
	newID, _ := browserID(imported.TaskID, kernel.TaskIDFromBytes)
	task, _, err := fixture.store.Task(ctx, newID)
	if err != nil || task.Priority != 8 {
		t.Fatalf("initial priority: %+v %v", task, err)
	}
	issue.Labels = []string{"ready", "later"}
	issueCount = 1
	tick = fixture.daemon.previewIntake(ctx, source, 1, true, "")
	after, _, err := fixture.store.Task(ctx, newID)
	if err != nil || after.ID != task.ID || after.IncarnationID != task.IncarnationID || after.WorkRevision != task.WorkRevision || after.Priority != -2 || len(tick.ImportedTasks) != 0 || tick.Candidates[0].AcceptanceID != accepted.AcceptanceID {
		t.Fatalf("priority created work: %+v %+v %v", after, tick, err)
	}
	lineage.IssueNumber = 1
	if reply := fixture.daemon.Intake(ctx, lineage); reply.State != "imported" || reply.TaskID != imported.TaskID {
		t.Fatalf("managed companion lost imported human work: %+v", reply)
	}
	lineage.Configuration.TargetRepositoryID = project.String()
	if reply := fixture.daemon.Intake(ctx, lineage); reply.State != "not_found" {
		t.Fatalf("foreign destination borrowed lineage: %+v", reply)
	}
	lineage.Configuration.TargetRepositoryID = target.String()
	lineage.ProjectID = testID(184)
	if reply := fixture.daemon.Intake(ctx, lineage); reply.State != "not_found" {
		t.Fatalf("foreign project borrowed lineage: %+v", reply)
	}
	lineage.ProjectID = project.String()
	identity = 43
	if reply := fixture.daemon.Intake(ctx, lineage); reply.State != "denied" {
		t.Fatalf("replacement repository borrowed lineage: %+v", reply)
	}
	identity = 42
	originalNode := issue.NodeID
	issue.NodeID = "I_replacement"
	if reply := fixture.daemon.Intake(ctx, lineage); reply.State != "not_found" {
		t.Fatalf("replacement issue borrowed lineage: %+v", reply)
	}
	issue.NodeID = originalNode
	changed := kernel.NewIntakeSource{ID: source.ID, ProjectID: source.ProjectID, OverseerAgentID: source.OverseerAgentID, Policy: source.Policy, TrustedGitHubLogins: source.TrustedGitHubLogins, PollSeconds: source.PollSeconds, AdmissionLimit: source.AdmissionLimit, LabelFilter: source.LabelFilter}
	changed.GitHubRepositoryID, changed.GitHubRepositoryName, changed.TargetRepositoryID = 99, "team/other", kernel.RepositoryID(project)
	if _, err := fixture.store.UpdateIntakeSource(ctx, source.ID, source.Revision, changed, true, mustKernelTime(t, 1000)); err != nil {
		t.Fatal(err)
	}
	if reply := fixture.daemon.Intake(ctx, lineage); reply.State != "imported" || reply.TaskID != imported.TaskID {
		t.Fatalf("mutable source retargeted retained review: %+v", reply)
	}
	acceptedID, _ := browserID(accepted.AcceptanceID, kernel.IntakeAcceptanceIDFromBytes)
	if _, err := fixture.store.WithdrawIntakeAcceptance(ctx, acceptedID, mustKernelTime(t, 1001)); err != nil {
		t.Fatal(err)
	}
	if reply := fixture.daemon.Intake(ctx, lineage); reply.State != "withdrawn" {
		t.Fatalf("withdrawn acceptance retained review authority: %+v", reply)
	}

}

func TestLegacyMigrationUsesAuthenticatedIntakeReplyContract(t *testing.T) {
	fixture := newDispatchFixture(t)
	client, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"legacy_preview", "legacy_commit"} {
		input := api.IntakeInput{Action: action, SourceID: testID(181), ProjectID: testID(182), Configuration: &api.IntakeConfiguration{}, Legacy: &api.LegacyIntakeInput{ConfigHash: fmt.Sprintf("%064x", 1), JournalHash: fmt.Sprintf("%064x", 2), PlanHash: fmt.Sprintf("%064x", 3)}}
		done := fixture.serve(t)
		result, err := client.Intake(context.Background(), input)
		waitDispatch(t, done)
		if err != nil || result.State != "unavailable" {
			t.Fatalf("%s socket reply: %+v %v", action, result, err)
		}
	}
}
