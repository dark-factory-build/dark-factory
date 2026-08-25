package kernel

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestEntityCreationReconciliationAndRelationships(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	projectSpec := NewProject{ID: projectID(t, 1), Name: "project", Root: filepath.Join(t.TempDir(), "root")}
	project, err := store.CreateProject(ctx, projectSpec, mustTime(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	agentSpec := NewAgent{
		ID: agentID(t, 2), ProjectID: project.ID, Name: "worker", Role: RoleWorker,
		Provider: ProviderCodex, ExecutionMode: ExecutionWorkspaceWrite, Model: "private-model",
		ReasoningEffort: "high", ToolBudgetLimit: 987654321,
	}
	agent, err := store.CreateAgent(ctx, agentSpec, mustTime(t, 11))
	if err != nil {
		t.Fatal(err)
	}
	taskSpec := NewTask{
		ID: taskID(t, 3), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 4),
		Title: "task", Body: "private body", Priority: 9,
	}
	task, err := store.EnqueueTask(ctx, taskSpec, mustTime(t, 12))
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != TaskQueued || task.WorkRevision.Int64() != 1 || task.Revision.Int64() != 1 {
		t.Fatalf("unexpected task: %+v", task)
	}
	before, err := store.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(ctx, projectSpec, mustTime(t, 10)); err != nil {
		t.Fatalf("exact project replay: %v", err)
	}
	if _, err := store.CreateAgent(ctx, agentSpec, mustTime(t, 11)); err != nil {
		t.Fatalf("exact agent replay: %v", err)
	}
	if _, err := store.EnqueueTask(ctx, taskSpec, mustTime(t, 12)); err != nil {
		t.Fatalf("exact task replay: %v", err)
	}
	after, err := store.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before.Head != after.Head || after.Head.Int64() != 3 {
		t.Fatalf("replay emitted invalidations: before=%+v after=%+v", before, after)
	}
	conflict := projectSpec
	conflict.Name = "different"
	if _, err := store.CreateProject(ctx, conflict, mustTime(t, 10)); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting project replay error = %v", err)
	}
	if _, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 5), Name: "other", Root: projectSpec.Root}, mustTime(t, 10)); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate root error = %v", err)
	}
	invalidShell := agentSpec
	invalidShell.ID = agentID(t, 6)
	invalidShell.Provider = ProviderShell
	invalidShell.ExecutionMode = ExecutionWorkspaceWrite
	if _, err := store.CreateAgent(ctx, invalidShell, mustTime(t, 13)); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("invalid shell controls error = %v", err)
	}
	missingProject := agentSpec
	missingProject.ID = agentID(t, 7)
	missingProject.ProjectID = projectID(t, 8)
	if _, err := store.CreateAgent(ctx, missingProject, mustTime(t, 13)); err == nil {
		t.Fatal("agent with missing project succeeded")
	}
	wrongProjectTask := taskSpec
	wrongProjectTask.ID = taskID(t, 9)
	wrongProjectTask.ProjectID = projectID(t, 8)
	wrongProjectTask.IncarnationID = incarnationID(t, 9)
	if _, err := store.EnqueueTask(ctx, wrongProjectTask, mustTime(t, 14)); err == nil {
		t.Fatal("task with mismatched agent/project succeeded")
	}
	if got, found, err := store.Project(ctx, project.ID); err != nil || !found || got != project {
		t.Fatalf("Project read found=%v got=%+v err=%v", found, got, err)
	}
	if got, found, err := store.Agent(ctx, agent.ID); err != nil || !found || got != agent {
		t.Fatalf("Agent read found=%v got=%+v err=%v", found, got, err)
	}
	if got, found, err := store.Task(ctx, task.ID); err != nil || !found || got.ID != task.ID {
		t.Fatalf("Task read found=%v got=%+v err=%v", found, got, err)
	}
}

func TestStateAndInvalidationRollbackTogether(t *testing.T) {
	t.Run("state failure has no invalidation", func(t *testing.T) {
		store, _ := newTestStore(t)
		defer store.Close()
		spec := NewAgent{ID: agentID(t, 1), ProjectID: projectID(t, 2), Name: "orphan", Role: RoleWorker, Provider: ProviderCodex, ExecutionMode: ExecutionWorkspaceWrite, ToolBudgetLimit: 1}
		if _, err := store.CreateAgent(context.Background(), spec, mustTime(t, 2)); err == nil {
			t.Fatal("orphan agent succeeded")
		}
		state, err := store.Factory(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if state.Head.Int64() != 0 {
			t.Fatalf("failed state mutation emitted invalidation head %d", state.Head.Int64())
		}
	})

	t.Run("event failure rolls back state", func(t *testing.T) {
		store, _ := newTestStore(t)
		defer store.Close()
		ctx := context.Background()
		id := projectID(t, 3)
		connection, err := store.writer.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := connection.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
			t.Fatal(err)
		}
		if _, err := connection.ExecContext(ctx, `INSERT INTO invalidations(sequence, occurred_at_ms, entity_kind, entity_id, revision, deleted) VALUES(1, 1, 'project', ?, 1, 0)`, id.Bytes()); err != nil {
			t.Fatal(err)
		}
		if _, err := connection.ExecContext(ctx, `UPDATE factory SET next_invalidation_sequence = 2`); err != nil {
			t.Fatal(err)
		}
		if _, err := connection.ExecContext(ctx, `COMMIT`); err != nil {
			t.Fatal(err)
		}
		connection.Close()
		_, err = store.CreateProject(ctx, NewProject{ID: id, Name: "rollback", Root: filepath.Join(t.TempDir(), "root")}, mustTime(t, 2))
		if err == nil {
			t.Fatal("event uniqueness failure did not fail mutation")
		}
		if _, found, readErr := store.Project(ctx, id); readErr != nil || found {
			t.Fatalf("state survived failed invalidation found=%v err=%v", found, readErr)
		}
		state, readErr := store.Factory(ctx)
		if readErr != nil || state.Head.Int64() != 1 {
			t.Fatalf("event metadata changed: %+v %v", state, readErr)
		}
	})
}

func TestDispatchCapacityRevisionGuards(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	revision := mustRevision(t, 1)
	state, err := store.SetDispatch(ctx, revision, true, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	if !state.DispatchEnabled || state.Revision.Int64() != 2 || state.Head.Int64() != 1 {
		t.Fatalf("dispatch state = %+v", state)
	}
	replay, err := store.SetDispatch(ctx, revision, true, mustTime(t, 2))
	if err != nil || replay.Revision != state.Revision || replay.Head != state.Head {
		t.Fatalf("dispatch replay = %+v, %v", replay, err)
	}
	if _, err := store.SetDispatch(ctx, revision, false, mustTime(t, 3)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale dispatch error = %v", err)
	}
	state, err = store.SetCapacity(ctx, state.Revision, 7, mustTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	if state.Capacity != 7 || state.Revision.Int64() != 3 || state.Head.Int64() != 2 {
		t.Fatalf("capacity state = %+v", state)
	}
	if _, err := store.SetCapacity(ctx, state.Revision, 0, mustTime(t, 4)); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("zero capacity error = %v", err)
	}
	if _, err := store.SetCapacity(ctx, state.Revision, MaxFactoryCapacity+1, mustTime(t, 4)); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("oversized capacity error = %v", err)
	}
	batch, err := store.WatchAfter(ctx, mustSequence(t, 0))
	if err != nil || len(batch.Invalidations) != 2 || batch.Invalidations[0].Revision.Int64() != 2 || batch.Invalidations[1].Revision.Int64() != 3 {
		t.Fatalf("factory invalidations = %+v, %v", batch, err)
	}
}

func TestInvalidationRetentionBatchAndGapSemantics(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	state, err := store.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < EventRetentionLimit+1; index++ {
		state, err = store.SetDispatch(ctx, state.Revision, !state.DispatchEnabled, mustTime(t, int64(index+2)))
		if err != nil {
			t.Fatalf("mutation %d: %v", index, err)
		}
	}
	if state.Head.Int64() != EventRetentionLimit+1 || state.Floor.Int64() != 2 {
		t.Fatalf("retention metadata = %+v", state)
	}
	var count int
	if err := store.readers.QueryRow(`SELECT COUNT(*) FROM invalidations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != EventRetentionLimit {
		t.Fatalf("retained %d invalidations, want %d", count, EventRetentionLimit)
	}
	if _, err := store.WatchAfter(ctx, mustSequence(t, 0)); !isResync(err) {
		t.Fatalf("old cursor error = %v", err)
	}
	if _, err := store.WatchAfter(ctx, mustSequence(t, state.Head.Int64()+1)); !errors.Is(err, ErrFutureCursor) {
		t.Fatalf("future cursor error = %v", err)
	}
	batch, err := store.WatchAfter(ctx, mustSequence(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Invalidations) != WatchBatchLimit || batch.Invalidations[0].Sequence.Int64() != 2 || batch.Invalidations[WatchBatchLimit-1].Sequence.Int64() != 257 {
		t.Fatalf("batch boundaries = first/last/count %+v/%+v/%d", batch.Invalidations[0], batch.Invalidations[len(batch.Invalidations)-1], len(batch.Invalidations))
	}
	if _, err := store.writer.Exec(`DELETE FROM invalidations WHERE sequence = 100`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.WatchAfter(ctx, mustSequence(t, 98)); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("gap error = %v", err)
	}
}

func TestSnapshotWatchAgreementAndPrivateStateBoundary(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	privateRoot := filepath.Join(t.TempDir(), "ROOT_SENTINEL_21b23e")
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 1), Name: "public project", Root: privateRoot}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	privateModel := "MODEL_SENTINEL_748af8"
	agent, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 2), ProjectID: project.ID, Name: "public agent", Role: RoleWorker, Provider: ProviderCodex, ExecutionMode: ExecutionWorkspaceWrite, Model: privateModel, ReasoningEffort: "xhigh", ToolBudgetLimit: 9}, mustTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	privateBody := "BODY_SENTINEL_760adb"
	if _, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 3), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 4), Title: "public task", Body: privateBody}, mustTime(t, 4)); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		close(started)
		_, err := store.SetDispatch(ctx, mustRevision(t, 1), true, mustTime(t, 5))
		finished <- err
	}()
	<-started
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	batch, err := store.WatchAfter(ctx, snapshot.Head)
	if err != nil {
		t.Fatal(err)
	}
	switch snapshot.Factory.Revision.Int64() {
	case 1:
		if snapshot.Factory.DispatchEnabled || len(batch.Invalidations) != 1 {
			t.Fatalf("old snapshot did not pair with one event: %+v %+v", snapshot.Factory, batch)
		}
	case 2:
		if !snapshot.Factory.DispatchEnabled || len(batch.Invalidations) != 0 {
			t.Fatalf("new snapshot did not pair with empty tail: %+v %+v", snapshot.Factory, batch)
		}
	default:
		t.Fatalf("unexpected snapshot revision %d", snapshot.Factory.Revision.Int64())
	}
	for name, value := range map[string]any{"snapshot": snapshot, "watch": batch} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		for _, private := range []string{privateRoot, privateModel, privateBody, "xhigh", "987654321", "workspace_write", "codex", "incarnation"} {
			if strings.Contains(string(encoded), private) {
				t.Fatalf("%s exposed private sentinel %q: %s", name, private, encoded)
			}
		}
	}
}

func TestSnapshotRejectsCapPlusOne(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	tx, err := store.writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	statement, err := tx.Prepare(`INSERT INTO projects(id, name, root, revision, created_at_ms, updated_at_ms) VALUES(?, ?, ?, 1, 1, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= SnapshotEntityLimit+1; index++ {
		if _, err := statement.Exec(numberedProjectID(t, index).Bytes(), fmt.Sprintf("p%d", index), fmt.Sprintf("/p/%d", index)); err != nil {
			t.Fatalf("insert %d: %v", index, err)
		}
	}
	statement.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Snapshot(context.Background()); !errors.Is(err, ErrSnapshotTooLarge) {
		t.Fatalf("Snapshot error = %v", err)
	}
}

func TestCorruptControlsFailClosedOnReadAndReopen(t *testing.T) {
	tests := map[string]func(*testing.T, *Store){
		"boolean": func(t *testing.T, store *Store) {
			corruptSQL(t, store, `UPDATE factory SET dispatch_enabled = 2`)
			if _, err := store.Factory(context.Background()); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("Factory error = %v", err)
			}
		},
		"enum": func(t *testing.T, store *Store) {
			project, err := store.CreateProject(context.Background(), NewProject{ID: projectID(t, 1), Name: "p", Root: "/p"}, mustTime(t, 2))
			if err != nil {
				t.Fatal(err)
			}
			agent, err := store.CreateAgent(context.Background(), NewAgent{ID: agentID(t, 2), ProjectID: project.ID, Name: "a", Role: RoleWorker, Provider: ProviderCodex, ExecutionMode: ExecutionWorkspaceWrite, ToolBudgetLimit: 1}, mustTime(t, 3))
			if err != nil {
				t.Fatal(err)
			}
			corruptSQL(t, store, `UPDATE agents SET role = 'unknown'`)
			if _, _, err := store.Agent(context.Background(), agent.ID); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("Agent error = %v", err)
			}
		},
		"length": func(t *testing.T, store *Store) {
			project, err := store.CreateProject(context.Background(), NewProject{ID: projectID(t, 1), Name: "p", Root: "/p"}, mustTime(t, 2))
			if err != nil {
				t.Fatal(err)
			}
			corruptSQL(t, store, `UPDATE projects SET id = zeroblob(15)`)
			if _, _, err := store.Project(context.Background(), project.ID); err != nil {
				t.Fatalf("old Project lookup should be an unremarkable miss: %v", err)
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			store, path := newTestStore(t)
			mutate(t, store)
			store.Close()
			if _, err := Open(context.Background(), path); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("Open error = %v", err)
			}
		})
	}
}

func corruptSQL(t *testing.T, store *Store, statement string) {
	t.Helper()
	connection, err := store.writer.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(context.Background(), `PRAGMA ignore_check_constraints = ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(context.Background(), statement); err != nil {
		t.Fatal(err)
	}
}

func numberedProjectID(t *testing.T, value int) ProjectID {
	t.Helper()
	raw := make([]byte, IDBytes)
	raw[0] = 0xa5
	binary.BigEndian.PutUint64(raw[IDBytes-8:], uint64(value))
	result, err := ProjectIDFromBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustRevision(t *testing.T, value int64) Revision {
	t.Helper()
	result, err := NewRevision(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustSequence(t *testing.T, value int64) EventSequence {
	t.Helper()
	result, err := NewEventSequence(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func isResync(err error) bool {
	var target *ResyncRequiredError
	return errors.As(err, &target)
}
