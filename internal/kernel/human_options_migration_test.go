package kernel

import (
	"context"
	"testing"
)

func TestV8PendingQuestionSurvivesOptionsMigration(t *testing.T) {
	ctx := context.Background()
	store, run, _ := runningOrchestratorRun(t)
	question, err := store.CreateHumanQuestionForAttempt(ctx, run.CredentialDigest, NewHumanQuestion{IdempotencyKey: humanKey(73), QuestionText: "Keep this pending question"}, mustTime(t, 400))
	if err != nil {
		t.Fatal(err)
	}
	path := storePath(t, store)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	pool, connection := openRawDatabase(t, path, false)
	if err := setForeignKeys(ctx, connection, false); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{"DROP INDEX continuations_admission_queue", "DROP INDEX continuations_one_waiting_per_condition", "DROP TABLE continuations"} {
		if _, err := connection.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := rebuildTable(ctx, connection, expectedSchemaOf(v8SchemaStatements()), "invalidations", testInvalidationColumns, "invalidations_entity_revision_unique", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := rebuildTable(ctx, connection, expectedSchemaOf(v8SchemaStatements()), "agents", v7AgentColumns, "agents_id_project_unique", "", ""); err != nil {
		t.Fatal(err)
	}
	columns := "id, run_id, idempotency_key, kind, reason_code, question_text, status, delivery_id, delivery_started_at_ms, resolution_kind, closed_at_ms, revision, created_at_ms, updated_at_ms"
	if err := rebuildTable(ctx, connection, expectedSchemaOf(v8SchemaStatements()), "human_requests", columns, "human_requests_one_unresolved_per_run", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := rebuildTable(ctx, connection, expectedSchemaOf(v8SchemaStatements()), "tasks", testTaskColumnsV5, "tasks_id_project_incarnation_unique", "tasks_incarnation_unique", "tasks_canonical_queue", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := rebuildTable(ctx, connection, expectedSchemaOf(v8SchemaStatements()), "changes", testChangeColumns, "changes_id_project_task_incarnation_unique", "changes_task_incarnation_unique", "tree_digest, entry_count, total_bytes, tree_dev, tree_inode",
		"CASE WHEN prepared_at_ms IS NULL THEN NULL ELSE zeroblob(32) END, CASE WHEN prepared_at_ms IS NULL THEN NULL ELSE 1 END, CASE WHEN prepared_at_ms IS NULL THEN NULL ELSE 1 END, CASE WHEN prepared_at_ms IS NULL THEN NULL ELSE 0 END, CASE WHEN prepared_at_ms IS NULL THEN NULL ELSE 2 END"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{"DROP TABLE mission_task_bindings", "DROP TABLE run_tokens", "DROP TABLE project_tokens", "DROP TABLE attachment_retention", "DROP TABLE task_attachments", "DROP TABLE intake_acceptance_reviews", "DROP TABLE intake_source_priorities", "DROP TABLE intake_legacy_suppressions", "DROP TABLE intake_legacy_migrations", "DROP TABLE intake_task_bindings", "DROP TABLE intake_source_trusted_logins", "DROP TABLE intake_acceptances", "DROP TABLE intake_sources", "DROP TABLE repository_source_identities", "DROP TABLE content_repository_bindings", "DROP TABLE task_repository_bindings", "DROP TABLE project_repositories", "DROP TABLE terminal_diagnostics", "DROP TABLE project_outcome_revisions", "DROP TABLE task_content_references", "DROP TABLE project_content_evidence", "DROP TABLE project_content_revisions", "DROP INDEX task_prerequisites_upstream", "DROP TABLE task_prerequisites", "DROP TABLE task_conflict_paths"} {
		if _, err := connection.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := connection.ExecContext(ctx, "PRAGMA user_version = 8"); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatal(err)
	}
	connection.Close()
	pool.Close()
	migrated, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	reader, err := migrated.readerConnection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, found, err := humanRequestByID(ctx, reader, question.ID)
	if err != nil || !found || got.QuestionText != question.QuestionText || got.Status != HumanRequestOpen || got.Revision != question.Revision || len(got.Options) != 0 {
		t.Fatalf("pending question changed: %+v found=%v err=%v", got, found, err)
	}
}
