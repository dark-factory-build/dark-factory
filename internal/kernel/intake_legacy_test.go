package kernel

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestLegacyIntakeMigrationIsAtomicPausedAndNeverAdoptsHistoricalTask(t *testing.T) {
	ctx := context.Background()
	store, path := newTestStore(t)
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 210), Name: "legacy", Root: "/legacy-cutover"}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 211), ProjectID: project.ID, Name: "legacy", Role: RoleOrchestrator, Provider: ProviderShell, ToolBudgetLimit: 10}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 212), IncarnationID: incarnationID(t, 212), ProjectID: project.ID, AssignedAgentID: agent.ID, Title: "historical source", Body: "legacy instructions"}, mustTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := IntakeSourceIDFromBytes(bytes.Repeat([]byte{213}, IDBytes))
	spec := NewIntakeSource{ID: id, GitHubRepositoryID: 42, GitHubRepositoryName: "owner/source", ProjectID: project.ID, TargetRepositoryID: RepositoryID(project.ID), OverseerAgentID: agent.ID, PriorityDefault: -1, PriorityByLabel: map[string]int64{"urgent": 5, "later": -2}, Policy: IntakePolicyTrustedAuthors, TrustedGitHubLogins: []string{"owner"}, PollSeconds: 60, AdmissionLimit: 25}
	snapshot := intakeSnapshotForTest()
	record := LegacyIntakeRecord{HasHistory: true, IssueNumber: snapshot.IssueNumber, NodeID: snapshot.NodeID, ContentHash: snapshot.ContentHash(), TaskID: task.ID, IncarnationID: task.IncarnationID, TaskRevision: task.Revision}
	receipt := LegacyIntakeReceipt{RepositoryID: 42, PlanHash: [32]byte{1}, ConfigHash: [32]byte{2}, JournalHash: [32]byte{3}}
	if _, err := store.CommitLegacyIntakeMigration(ctx, spec, receipt, []LegacyIntakeRecord{record}, mustTime(t, 4)); !errors.Is(err, ErrConflict) {
		t.Fatalf("active legacy work cut over: %v", err)
	}
	if _, found, err := store.IntakeSource(ctx, id); err != nil || found {
		t.Fatalf("failed cutover left source: %v %v", found, err)
	}
	task, err = store.UpdateTask(ctx, task.ID, task.Revision, TaskPatch{Cancel: true}, mustTime(t, 5))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitLegacyIntakeMigration(ctx, spec, receipt, []LegacyIntakeRecord{record}, mustTime(t, 6)); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale task revision cut over: %v", err)
	}
	record.TaskRevision = task.Revision
	worker, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 215), ProjectID: project.ID, Name: "unrecorded legacy worker", Role: RoleWorker, Provider: ProviderShell, ToolBudgetLimit: 10}, mustTime(t, 6))
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 216), IncarnationID: incarnationID(t, 216), ProjectID: project.ID, AssignedAgentID: worker.ID, Title: "legacy descendant has no lineage"}, mustTime(t, 6))
	if err != nil {
		t.Fatal(err)
	}
	if settled, err := store.LegacyIntakeProjectSettled(ctx, project.ID); err != nil || settled {
		t.Fatalf("unrecorded active work ignored: %v %v", settled, err)
	}
	if _, err := store.CommitLegacyIntakeMigration(ctx, spec, receipt, []LegacyIntakeRecord{record}, mustTime(t, 7)); !errors.Is(err, ErrConflict) {
		t.Fatalf("active project cut over: %v", err)
	}
	if _, err := store.UpdateTask(ctx, child.ID, child.Revision, TaskPatch{Cancel: true}, mustTime(t, 7)); err != nil {
		t.Fatal(err)
	}
	unproved := record
	unproved.TaskID, unproved.IncarnationID, unproved.TaskRevision = TaskID{}, IncarnationID{}, Revision{}
	hash := snapshot.ContentHash()
	unproved.HistoricalContentHash = &hash
	if _, err := store.CommitLegacyIntakeMigration(ctx, spec, receipt, []LegacyIntakeRecord{unproved}, mustTime(t, 7)); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("unproved historical bytes stored: %v", err)
	}
	otherRepository, err := store.AddProjectRepository(ctx, NewProjectRepository{ID: repositoryID(t, 219), ProjectID: project.ID, Name: "other", Root: "/legacy-other", BaseRef: "release"}, mustTime(t, 7))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetProjectRepositoryDefault(ctx, otherRepository.ID, otherRepository.Revision, mustTime(t, 7)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitLegacyIntakeMigration(ctx, spec, receipt, []LegacyIntakeRecord{record}, mustTime(t, 7)); !errors.Is(err, ErrConflict) {
		t.Fatalf("default changed after preview but committed: %v", err)
	}
	if _, found, err := store.LegacyIntakeMigration(ctx, spec.ID); found || err != nil {
		t.Fatalf("failed transaction left receipt: %v %v", found, err)
	}
	primary, _, err := store.ProjectRepository(ctx, spec.TargetRepositoryID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetProjectRepositoryDefault(ctx, primary.ID, primary.Revision, mustTime(t, 7)); err != nil {
		t.Fatal(err)
	}
	source, err := store.CommitLegacyIntakeMigration(ctx, spec, receipt, []LegacyIntakeRecord{record}, mustTime(t, 7))
	if err != nil || source.Enabled {
		t.Fatalf("atomic source: %+v %v", source, err)
	}
	if _, found, err := store.IntakeAcceptanceForTask(ctx, task.ID); err != nil || found {
		t.Fatalf("historical task acquired acceptance: %v %v", found, err)
	}
	if _, found, err := store.LatestIntakeAcceptance(ctx, snapshot, project.ID, source.TargetRepositoryID); err != nil || found {
		t.Fatalf("baseline became approval: %v %v", found, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if replay, err := store.CommitLegacyIntakeMigration(ctx, spec, receipt, []LegacyIntakeRecord{record}, mustTime(t, 8)); err != nil || replay.ID != source.ID || replay.Enabled {
		t.Fatalf("lost-response replay: %+v %v", replay, err)
	}
	reloaded, _, err := store.IntakeSource(ctx, source.ID)
	if err != nil || IntakePriority(reloaded, []string{"later"}) != -2 || IntakePriority(reloaded, []string{"later", "urgent"}) != 5 || IntakePriority(reloaded, nil) != -1 {
		t.Fatalf("priority rules lost on restart: %+v %v", reloaded, err)
	}
	actual, found, err := store.LegacyIntakeSuppression(ctx, source, snapshot)
	if err != nil || !found || actual.TaskID != task.ID || actual.IncarnationID != task.IncarnationID || actual.ContentHash != snapshot.ContentHash() {
		t.Fatalf("baseline history: %+v %v %v", actual, found, err)
	}
	changed := receipt
	changed.PlanHash[0]++
	if _, err := store.CommitLegacyIntakeMigration(ctx, spec, changed, []LegacyIntakeRecord{record}, mustTime(t, 9)); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay: %v", err)
	}
	other, _ := IntakeSourceIDFromBytes(bytes.Repeat([]byte{214}, IDBytes))
	spec.ID = other
	spec.LabelFilter = "other-filter"
	if _, err := store.CommitLegacyIntakeMigration(ctx, spec, receipt, make([]LegacyIntakeRecord, 201), mustTime(t, 10)); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("partial overflow: %v", err)
	}
	if _, err := store.CommitLegacyIntakeMigration(ctx, spec, receipt, []LegacyIntakeRecord{record}, mustTime(t, 10)); err != nil {
		t.Fatal(err)
	}
	ordinaryID, _ := IntakeSourceIDFromBytes(bytes.Repeat([]byte{217}, IDBytes))
	spec.ID, spec.LabelFilter = ordinaryID, "ordinary"
	if _, err := store.CreateIntakeSource(ctx, spec, mustTime(t, 11)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitLegacyIntakeMigration(ctx, spec, receipt, nil, mustTime(t, 12)); !errors.Is(err, ErrConflict) {
		t.Fatalf("preexisting ordinary source was adopted: %v", err)
	}
	var count int
	if err := store.writer.QueryRow(`SELECT COUNT(*) FROM intake_legacy_suppressions`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("overlapping migration duplicated canonical receipt: %d %v", count, err)
	}
}

func TestV26MigrationAddsEmptyLegacySuppressionWithoutChangingExistingIDs(t *testing.T) {
	ctx := context.Background()
	store, path := newTestStore(t)
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 219), Name: "v26", Root: "/v26"}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	downgradeIntakeToV27(t, store)
	for _, statement := range []string{"DROP TABLE intake_source_priorities", "DROP TABLE intake_legacy_suppressions", "DROP TABLE intake_legacy_migrations", "PRAGMA user_version = 26"} {
		if _, err := store.writer.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if value, found, err := store.Project(ctx, project.ID); err != nil || !found || value.ID != project.ID {
		t.Fatalf("migrated project: %+v %v %v", value, found, err)
	}
	var count int
	if err := store.writer.QueryRow(`SELECT COUNT(*) FROM intake_legacy_suppressions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invented baseline: %d %v", count, err)
	}
}

func TestImportedIntakeIssueUsesExplicitReacceptanceOrder(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	defer store.Close()
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 230), Name: "review-order", Root: "/review-order"}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := IntakeSourceIDFromBytes(bytes.Repeat([]byte{231}, IDBytes))
	source, err := store.CreateIntakeSource(ctx, NewIntakeSource{ID: id, GitHubRepositoryID: 42, GitHubRepositoryName: "owner/repository", ProjectID: project.ID, TargetRepositoryID: RepositoryID(project.ID), Policy: IntakePolicyManual, PollSeconds: 60, AdmissionLimit: 1}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	source, err = store.SetIntakeSourceEnabled(ctx, id, source.Revision, true, mustTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	first := intakeSnapshotForTest()
	a, err := store.AcceptIntakeSnapshot(ctx, id, first, mustTime(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportIntakeAcceptance(ctx, a.ID, mustTime(t, 5), source); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Body = "Second explicitly approved content"
	b, err := store.AcceptIntakeSnapshot(ctx, id, second, mustTime(t, 6))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportIntakeAcceptance(ctx, b.ID, mustTime(t, 7), source); err != nil {
		t.Fatal(err)
	}
	if _, err := store.WithdrawIntakeAcceptance(ctx, b.ID, mustTime(t, 8)); err != nil {
		t.Fatal(err)
	}
	if restored, err := store.AcceptIntakeSnapshot(ctx, id, first, mustTime(t, 9)); err != nil || restored.ID != a.ID {
		t.Fatalf("restore: %+v %v", restored, err)
	}
	retained, found, err := store.ImportedIntakeIssue(ctx, 42, first.IssueNumber, first.NodeID, project.ID, RepositoryID(project.ID))
	if err != nil || !found || retained.ID != a.ID || retained.WithdrawnAt != nil {
		t.Fatalf("companion ignored latest explicit review: %+v %v %v", retained, found, err)
	}
}
