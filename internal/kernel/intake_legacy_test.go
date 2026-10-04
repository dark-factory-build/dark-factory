package kernel

import (
	"bytes"
	"context"
	"testing"
)

func TestLegacyIntakeSuppressionReadsMigratedBaseline(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	defer store.Close()
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 210), Name: "legacy", Root: "/legacy-cutover"}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := IntakeSourceIDFromBytes(bytes.Repeat([]byte{213}, IDBytes))
	source, err := store.CreateIntakeSource(ctx, NewIntakeSource{ID: id, GitHubRepositoryID: 42, GitHubRepositoryName: "owner/source", ProjectID: project.ID, TargetRepositoryID: RepositoryID(project.ID), Policy: IntakePolicyManual, PollSeconds: 60, AdmissionLimit: 1}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := intakeSnapshotForTest()
	if _, found, err := store.LegacyIntakeSuppression(ctx, source, snapshot); err != nil || found {
		t.Fatalf("invented suppression: %v %v", found, err)
	}
	hash := snapshot.ContentHash()
	if _, err := store.writer.Exec(`INSERT INTO intake_legacy_migrations(source_id, github_repository_id, plan_hash, config_hash, journal_hash, created_at_ms) VALUES(?, 42, ?, ?, ?, 3)`, source.ID.Bytes(), bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32), bytes.Repeat([]byte{3}, 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.Exec(`INSERT INTO intake_legacy_suppressions(github_repository_id, issue_number, issue_node_id, project_id, repository_id, content_hash, migration_source_id, has_history) VALUES(?, ?, ?, ?, ?, ?, ?, 1)`, 42, int64(snapshot.IssueNumber), snapshot.NodeID, project.ID.Bytes(), source.TargetRepositoryID.Bytes(), hash[:], source.ID.Bytes()); err != nil {
		t.Fatal(err)
	}
	actual, found, err := store.LegacyIntakeSuppression(ctx, source, snapshot)
	if err != nil || !found || !actual.HasHistory || actual.ContentHash != hash || actual.TaskID != (TaskID{}) {
		t.Fatalf("baseline history: %+v %v %v", actual, found, err)
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
