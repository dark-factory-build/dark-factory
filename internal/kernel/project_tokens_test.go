package kernel

import (
	"context"
	"reflect"
	"testing"
)

func TestProjectTokenCeilingStopsAdmissionUntilTheAllowanceIsRaised(t *testing.T) {
	store, _, project, agent := newAdmissionStore(t, RoleOrchestrator, 1)
	defer store.Close()
	ctx := context.Background()
	limit := func(tokens uint64, at int64) {
		t.Helper()
		var err error
		if project, err = store.SetProjectLimitsWithTokens(ctx, project.ID, project.Revision, 0, 0, &tokens, mustTime(t, at)); err != nil {
			t.Fatal(err)
		}
	}
	admit := func(seed byte, at int64) (AdmissionResult, Project) {
		t.Helper()
		result, err := store.AdmitNext(ctx, admissionKeys(t, seed, nil), mustTime(t, at))
		if err != nil {
			t.Fatal(err)
		}
		current, _, err := store.Project(ctx, project.ID)
		if err != nil {
			t.Fatal(err)
		}
		return result, current
	}
	limit(100, 3)
	for id := byte(201); id <= 203; id += 2 {
		if _, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, id), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, id+1), Title: "work"}, mustTime(t, 4)); err != nil {
			t.Fatal(err)
		}
	}
	first, current := admit(210, 5)
	if !first.Admitted() {
		t.Fatalf("admission under the ceiling = %+v", first)
	}
	project = current
	// The run's row is the receipt: a retried write counts once.
	for range 2 {
		if err := store.AddRunTokens(ctx, first.Run.ID, 100); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := store.ProjectTokens(ctx, project.ID); err != nil || got != (ProjectTokens{TokenLimit: 100, TokensUsed: 100}) {
		t.Fatalf("tokens = %+v, %v", got, err)
	}
	// A stale revision changes neither the run limits nor the token ceiling.
	stale, tokens := project.Revision, uint64(999)
	if _, err := store.SetProjectLimitsWithTokens(ctx, project.ID, Revision{value: stale.Int64() - 1}, 5, 5, &tokens, mustTime(t, 6)); err == nil {
		t.Fatal("stale limits edit was accepted")
	}
	if got, _ := store.ProjectTokens(ctx, project.ID); got.TokenLimit != 100 {
		t.Fatalf("stale edit moved the ceiling: %+v", got)
	}
	// A new allowance is additional to what is already spent, and zero
	// removes the ceiling without losing history.
	limit(50, 7)
	if got, _ := store.ProjectTokens(ctx, project.ID); got.TokenLimit != 150 {
		t.Fatalf("raised limit = %+v", got)
	}
	limit(0, 8)
	if got, _ := store.ProjectTokens(ctx, project.ID); got != (ProjectTokens{TokensUsed: 100}) {
		t.Fatalf("disabled ceiling lost history: %+v", got)
	}
}

func TestProjectAtItsTokenCeilingAdmitsNothing(t *testing.T) {
	store, _, project, agent := newAdmissionStore(t, RoleOrchestrator, 1)
	defer store.Close()
	ctx := context.Background()
	if _, err := store.writer.ExecContext(ctx, `INSERT INTO project_tokens(project_id, token_limit, tokens_used) VALUES(?, 10, 10)`, project.ID.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 201), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 202), Title: "one"}, mustTime(t, 4)); err != nil {
		t.Fatal(err)
	}
	if spent, err := store.AdmitNext(ctx, admissionKeys(t, 203, nil), mustTime(t, 5)); err != nil || spent.Admitted() {
		t.Fatalf("admission at the ceiling = %+v, %v", spent, err)
	}
}

func TestV29MigrationPreservesHomeAndAddsEmptyTokenLedger(t *testing.T) {
	ctx := context.Background()
	store, path, project, _ := newAdmissionStore(t, RoleWorker, 2)
	connection, err := store.writer.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotSchemaRows(t, ctx, connection, v29SchemaStatements(), false)
	connection.Close()
	if _, err := store.writer.ExecContext(ctx, `DROP TABLE run_tokens; DROP TABLE project_tokens; PRAGMA user_version = 29`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err = Open(ctx, path); err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if connection, err = store.writer.Conn(ctx); err != nil {
		t.Fatal(err)
	}
	after := snapshotSchemaRows(t, ctx, connection, v29SchemaStatements(), false)
	connection.Close()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("migration changed existing data")
	}
	if got, err := store.ProjectTokens(ctx, project.ID); err != nil || got != (ProjectTokens{}) {
		t.Fatalf("migrated ledger = %+v, %v", got, err)
	}
}
