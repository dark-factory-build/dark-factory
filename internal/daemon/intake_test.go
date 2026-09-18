//go:build darwin || linux

package daemon

import (
	"context"
	"encoding/hex"
	"fmt"
	"sort"
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

func TestIntakeAdmissionLimitIncludesAcceptedDiscoveryCandidates(t *testing.T) {
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
	issues := []maintainer.Issue{
		{ID: 81, NodeID: "I_first", Number: 1, Title: "First", Body: "Reviewed first", State: "open", URL: "https://github.com/team/issues/issues/1"},
		{ID: 82, NodeID: "I_second", Number: 2, Title: "Second", Body: "Reviewed second", State: "open", URL: "https://github.com/team/issues/issues/2"},
	}
	accepted := make([]kernel.IntakeAcceptance, 0, 2)
	for i := range issues {
		issues[i].Author.Login, issues[i].Author.Type = "reviewer", "User"
		receipt, err := fixture.store.AcceptIntakeSnapshot(ctx, id, intakeSnapshot(source, issues[i]), mustKernelTime(t, int64(105+i)), source.Revision)
		if err != nil {
			t.Fatal(err)
		}
		accepted = append(accepted, receipt)
	}
	fixture.daemon.intakeIssues = func(_ context.Context, _ string, _ uint64, _ uint32, _ string, number uint64) (maintainer.IssuePage, error) {
		if number != 0 {
			return maintainer.IssuePage{RepositoryID: 42, Issues: []maintainer.Issue{issues[number-1]}}, nil
		}
		return maintainer.IssuePage{RepositoryID: 42, Issues: issues}, nil
	}
	tick := api.IntakeInput{Action: "tick", SourceID: id.String(), Page: 1}
	sort.Slice(accepted, func(i, j int) bool { return accepted[i].ID.String() < accepted[j].ID.String() })
	for i := range accepted {
		result := fixture.daemon.Intake(ctx, tick)
		if result.State != "ok" || len(result.ImportedTasks) != 1 || result.ImportedTasks[0] != accepted[i].TaskID.String() {
			t.Fatalf("tick %d: %+v", i, result)
		}
		if i == 0 {
			if _, found, err := fixture.store.Task(ctx, accepted[1].TaskID); err != nil || found {
				t.Fatalf("discovery exceeded admission cap: found=%v err=%v", found, err)
			}
		}
	}
	if result := fixture.daemon.Intake(ctx, tick); result.State != "ok" || len(result.ImportedTasks) != 0 {
		t.Fatalf("replay counted as new work: %+v", result)
	}
}

func TestIntakePendingCursorPassesOverTwoHundredStaleReceipts(t *testing.T) {
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
	issues := map[uint64]maintainer.Issue{}
	accepted := []kernel.IntakeAcceptance{}
	for number := uint64(1); number <= 204; number++ {
		issue := maintainer.Issue{ID: int64(number), NodeID: fmt.Sprintf("I_%d", number), Number: number, Title: "Reviewed", Body: "Approved instructions", State: "open", URL: fmt.Sprintf("https://github.com/team/issues/issues/%d", number)}
		issue.Author.Login, issue.Author.Type = "human", "User"
		issues[number] = issue
		receipt, err := fixture.store.AcceptIntakeSnapshot(ctx, source.ID, intakeSnapshot(source, issue), mustKernelTime(t, int64(200+number)))
		if err != nil {
			t.Fatal(err)
		}
		accepted = append(accepted, receipt)
	}
	sort.Slice(accepted, func(i, j int) bool { return accepted[i].ID.String() < accepted[j].ID.String() })
	for _, receipt := range accepted[:202] {
		issue := issues[receipt.Snapshot.IssueNumber]
		issue.Body = "Unapproved replacement"
		issues[issue.Number] = issue
	}
	exactReads := 0
	fixture.daemon.intakeIssues = func(_ context.Context, _ string, _ uint64, page uint32, _ string, number uint64) (maintainer.IssuePage, error) {
		if number != 0 {
			exactReads++
			return maintainer.IssuePage{RepositoryID: 42, Issues: []maintainer.Issue{issues[number]}}, nil
		}
		next := page + 1 // Approved issues remain beyond the unaccepted discovery backlog.
		return maintainer.IssuePage{RepositoryID: 42, Issues: []maintainer.Issue{}, NextPage: &next}, nil
	}
	tick := api.IntakeInput{Action: "tick", SourceID: source.ID.String(), Page: 1}
	imports := map[string]int{}
	wrapped := false
	for index := 0; index < 20; index++ {
		before := exactReads
		result := fixture.daemon.Intake(ctx, tick)
		if result.State != "ok" || len(result.ImportedTasks) > 1 || exactReads-before > 25 {
			t.Fatalf("unbounded or failed tick: %+v, reads=%d", result, exactReads-before)
		}
		for _, task := range result.ImportedTasks {
			imports[task]++
		}
		if result.AcceptanceCursor == "" {
			wrapped = true
		}
		tick.AcceptanceCursor, tick.Page = result.AcceptanceCursor, *result.NextPage
	}
	if !wrapped || len(imports) != 2 {
		t.Fatalf("pending receipts starved: wrapped=%v imported=%v", wrapped, imports)
	}
	for _, receipt := range accepted[202:] {
		if imports[receipt.TaskID.String()] != 1 {
			t.Fatalf("approved receipt not imported exactly once: %v", imports)
		}
	}
	for _, receipt := range accepted[:202] {
		if _, found, err := fixture.store.Task(ctx, receipt.TaskID); err != nil || found {
			t.Fatalf("stale receipt imported: %v %v", found, err)
		}
	}
}

func TestIntakePendingCursorPassesUnavailableReceiptAndRetriesOnWrap(t *testing.T) {
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
	issues := map[uint64]maintainer.Issue{}
	accepted := []kernel.IntakeAcceptance{}
	for number := uint64(1); number <= 2; number++ {
		issue := maintainer.Issue{ID: int64(number), NodeID: fmt.Sprintf("I_%d", number), Number: number, Title: "Reviewed", Body: "Approved instructions", State: "open", URL: fmt.Sprintf("https://github.com/team/issues/issues/%d", number)}
		issue.Author.Login, issue.Author.Type = "human", "User"
		issues[number] = issue
		receipt, err := fixture.store.AcceptIntakeSnapshot(ctx, source.ID, intakeSnapshot(source, issue), mustKernelTime(t, int64(200+number)))
		if err != nil {
			t.Fatal(err)
		}
		accepted = append(accepted, receipt)
	}
	sort.Slice(accepted, func(i, j int) bool { return accepted[i].ID.String() < accepted[j].ID.String() })
	for _, receipt := range accepted[:1] {
		issue := issues[receipt.Snapshot.IssueNumber]
		issue.Body = "Unapproved replacement"
		issues[issue.Number] = issue
	}
	exactReads := 0
	fixture.daemon.intakeIssues = func(_ context.Context, _ string, _ uint64, page uint32, _ string, number uint64) (maintainer.IssuePage, error) {
		if number != 0 {
			exactReads++
			if number == accepted[0].Snapshot.IssueNumber {
				return maintainer.IssuePage{}, maintainer.ErrUnavailable
			}
			return maintainer.IssuePage{RepositoryID: 42, Issues: []maintainer.Issue{issues[number]}}, nil
		}
		next := page + 1 // Approved issues remain beyond the unaccepted discovery backlog.
		return maintainer.IssuePage{RepositoryID: 42, Issues: []maintainer.Issue{}, NextPage: &next}, nil
	}
	tick := api.IntakeInput{Action: "tick", SourceID: source.ID.String(), Page: 1}
	imports := map[string]int{}
	wrapped := false
	for index := 0; index < 4; index++ {
		before := exactReads
		result := fixture.daemon.Intake(ctx, tick)
		if (result.State != "ok" && result.State != "unavailable") || !result.AcceptanceProgress || len(result.ImportedTasks) > 1 || exactReads-before > 25 {
			t.Fatalf("unbounded or failed tick: %+v, reads=%d", result, exactReads-before)
		}
		for _, task := range result.ImportedTasks {
			imports[task]++
		}
		if result.AcceptanceCursor == "" {
			wrapped = true
		}
		tick.AcceptanceCursor = result.AcceptanceCursor
		if result.NextPage != nil {
			tick.Page = *result.NextPage
		}
	}
	if !wrapped || len(imports) != 1 {
		t.Fatalf("pending receipts starved: wrapped=%v imported=%v", wrapped, imports)
	}
	for _, receipt := range accepted[1:] {
		if imports[receipt.TaskID.String()] != 1 {
			t.Fatalf("approved receipt not imported exactly once: %v", imports)
		}
	}
	for _, receipt := range accepted[:1] {
		if _, found, err := fixture.store.Task(ctx, receipt.TaskID); err != nil || found {
			t.Fatalf("stale receipt imported: %v %v", found, err)
		}
	}
}
