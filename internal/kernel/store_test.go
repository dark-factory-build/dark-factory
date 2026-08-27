package kernel

import (
	"bytes"
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
	projectReplay, err := store.CreateProject(ctx, projectSpec, mustTime(t, 110))
	if err != nil {
		t.Fatalf("exact project replay: %v", err)
	}
	if projectReplay.CreatedAt != project.CreatedAt || projectReplay.UpdatedAt != project.UpdatedAt {
		t.Fatalf("project replay replaced durable times: original=%+v replay=%+v", project, projectReplay)
	}
	agentReplay, err := store.CreateAgent(ctx, agentSpec, mustTime(t, 111))
	if err != nil {
		t.Fatalf("exact agent replay: %v", err)
	}
	if agentReplay.CreatedAt != agent.CreatedAt || agentReplay.UpdatedAt != agent.UpdatedAt {
		t.Fatalf("agent replay replaced durable times: original=%+v replay=%+v", agent, agentReplay)
	}
	taskReplay, err := store.EnqueueTask(ctx, taskSpec, mustTime(t, 112))
	if err != nil {
		t.Fatalf("exact task replay: %v", err)
	}
	if taskReplay.CreatedAt != task.CreatedAt || taskReplay.UpdatedAt != task.UpdatedAt {
		t.Fatalf("task replay replaced durable times: original=%+v replay=%+v", task, taskReplay)
	}
	after, err := store.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before.Head != after.Head || after.Head.Int64() != 3 {
		t.Fatalf("replay emitted invalidations: before=%+v after=%+v", before, after)
	}
	for table, want := range map[string]int{"projects": 1, "agents": 1, "tasks": 1, "invalidations": 3} {
		var count int
		if err := store.readers.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("%s row count after replay = %d, want %d", table, count, want)
		}
	}
	conflict := projectSpec
	conflict.Name = "different"
	if _, err := store.CreateProject(ctx, conflict, mustTime(t, 10)); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting project replay error = %v", err)
	}
	agentConflict := agentSpec
	agentConflict.Model = "changed-model"
	if _, err := store.CreateAgent(ctx, agentConflict, mustTime(t, 111)); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting agent replay error = %v", err)
	}
	taskConflict := taskSpec
	taskConflict.Body = "changed body"
	if _, err := store.EnqueueTask(ctx, taskConflict, mustTime(t, 112)); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting task replay error = %v", err)
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
	if _, err := store.SetDispatch(ctx, Revision{}, false, mustTime(t, 4)); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("zero revision error = %v", err)
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
	if _, err := store.WatchAfter(ctx, mustSequence(t, 98)); !isWatchRestart(err, WatchRestartGap, state) || !errors.Is(err, ErrCorruptState) {
		t.Fatalf("gap error = %v", err)
	}
	if _, err := store.WatchAfter(ctx, state.Head); !isWatchRestart(err, WatchRestartGap, state) || !errors.Is(err, ErrCorruptState) {
		t.Fatalf("gap behind cursor error = %v", err)
	}
}

func TestWatchAfterRestartClassificationsAreFiniteAndPrivate(t *testing.T) {
	if WatchRestartGap.String() != "gap" || WatchRestartHiddenDependency.String() != "hidden_dependency" || WatchRestartReason(99).String() != "" {
		t.Fatal("watch restart reason strings are not closed")
	}
	t.Run("early end is gap", func(t *testing.T) {
		store, _ := newTestStore(t)
		defer store.Close()
		state, err := store.SetDispatch(context.Background(), mustRevision(t, 1), true, mustTime(t, 2))
		if err != nil {
			t.Fatal(err)
		}
		corruptSQL(t, store, `DELETE FROM invalidations WHERE sequence = 1`)
		if _, err := store.WatchAfter(context.Background(), mustSequence(t, 0)); !isWatchRestart(err, WatchRestartGap, state) || !errors.Is(err, ErrCorruptState) {
			t.Fatalf("early-end error = %v", err)
		}
	})

	t.Run("empty retained log only represents initial state", func(t *testing.T) {
		store, _ := newTestStore(t)
		defer store.Close()
		batch, err := store.WatchAfter(context.Background(), mustSequence(t, 0))
		if err != nil || batch.Head.Int64() != 0 || batch.Floor.Int64() != 1 || len(batch.Invalidations) != 0 {
			t.Fatalf("initial empty log = %+v, %v", batch, err)
		}
	})

	for _, metadata := range []struct {
		name  string
		next  int64
		floor int64
	}{
		{name: "one missing event", next: 2, floor: 2},
		{name: "noninitial missing history", next: 4, floor: 4},
	} {
		t.Run(metadata.name, func(t *testing.T) {
			store, _ := newTestStore(t)
			defer store.Close()
			corruptSQL(t, store, `UPDATE factory SET next_invalidation_sequence = ?, invalidation_floor = ?`, metadata.next, metadata.floor)
			state, err := store.Factory(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for _, after := range []int64{0, state.Head.Int64(), state.Floor.Int64()} {
				if _, err := store.WatchAfter(context.Background(), mustSequence(t, after)); !isWatchRestart(err, WatchRestartGap, state) || !errors.Is(err, ErrCorruptState) {
					t.Fatalf("after=%d empty advanced log error = %v", after, err)
				}
			}
		})
	}

	t.Run("unknown kind is hidden dependency", func(t *testing.T) {
		store, _ := newTestStore(t)
		defer store.Close()
		state, err := store.SetDispatch(context.Background(), mustRevision(t, 1), true, mustTime(t, 2))
		if err != nil {
			t.Fatal(err)
		}
		const secret = "PRIVATE_UNKNOWN_KIND_SENTINEL"
		corruptSQL(t, store, `UPDATE invalidations SET entity_kind = ? WHERE sequence = 1`, secret)
		_, err = store.WatchAfter(context.Background(), mustSequence(t, 0))
		if !isWatchRestart(err, WatchRestartHiddenDependency, state) || !errors.Is(err, ErrCorruptState) {
			t.Fatalf("unknown-kind error = %v", err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("unknown-kind error exposed durable value: %v", err)
		}
	})
}

func TestWatchAfterMalformedKnownRowsRemainCorruption(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate string
	}{
		{name: "revision", mutate: `UPDATE invalidations SET revision = 0 WHERE sequence = 1`},
		{name: "identity", mutate: `UPDATE invalidations SET entity_id = zeroblob(15) WHERE sequence = 1`},
		{name: "deleted", mutate: `UPDATE invalidations SET deleted = 2 WHERE sequence = 1`},
		{name: "time", mutate: `UPDATE invalidations SET occurred_at_ms = -1 WHERE sequence = 1`},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _ := newTestStore(t)
			defer store.Close()
			if _, err := store.SetDispatch(context.Background(), mustRevision(t, 1), true, mustTime(t, 2)); err != nil {
				t.Fatal(err)
			}
			corruptSQL(t, store, test.mutate)
			_, err := store.WatchAfter(context.Background(), mustSequence(t, 0))
			var restart *WatchRestartError
			if !errors.Is(err, ErrCorruptState) || errors.As(err, &restart) {
				t.Fatalf("malformed row error = %v", err)
			}
		})
	}
}

func TestWatchAfterRetainsExactChangeAndRunChronology(t *testing.T) {
	store, _, _ := admittedWorkerRun(t)
	defer store.Close()
	batch, err := store.WatchAfter(context.Background(), mustSequence(t, 0))
	if err != nil {
		t.Fatal(err)
	}
	var change, run bool
	for index, event := range batch.Invalidations {
		if event.Sequence.Int64() != int64(index+1) {
			t.Fatalf("event %d sequence = %d", index, event.Sequence.Int64())
		}
		change = change || event.EntityKind == EntityChange.String()
		run = run || event.EntityKind == EntityRun.String()
	}
	if !change || !run || batch.Head.Int64() != int64(len(batch.Invalidations)) {
		t.Fatalf("change/run chronology = %+v", batch)
	}
}

func TestFactoryMutationRequiresExactlyOneRow(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	if _, err := store.writer.Exec(`CREATE TRIGGER suppress_factory_update BEFORE UPDATE OF dispatch_enabled ON factory BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetDispatch(context.Background(), mustRevision(t, 1), true, mustTime(t, 2)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("suppressed update error = %v", err)
	}
	state, err := store.Factory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.DispatchEnabled || state.Revision.Int64() != 1 || state.Head.Int64() != 0 {
		t.Fatalf("suppressed update left footprint: %+v", state)
	}
}

func TestReadTransactionPinsSnapshotBeforeConcurrentWrite(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	tx, err := store.beginRead(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	state, err := factoryState(ctx, tx.connection)
	if err != nil {
		t.Fatal(err)
	}
	spec := NewProject{ID: projectID(t, 11), Name: "concurrent", Root: filepath.Join(t.TempDir(), "concurrent")}
	if _, err := store.CreateProject(ctx, spec, mustTime(t, 2)); err != nil {
		t.Fatal(err)
	}
	if _, found, err := projectByID(ctx, tx.connection, spec.ID); err != nil || found {
		t.Fatalf("pinned read observed concurrent write found=%v err=%v", found, err)
	}
	batch, err := store.WatchAfter(ctx, state.Head)
	if err != nil || len(batch.Invalidations) != 1 {
		t.Fatalf("watch after pinned head = %+v, %v", batch, err)
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
	statement, err := tx.Prepare(`INSERT INTO projects(id, name, root, verification_policy, revision, created_at_ms, updated_at_ms) VALUES(?, ?, ?, 'none', 1, 1, 1)`)
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
		"run outcome detail": func(t *testing.T, store *Store) {
			seedDurableAuthority(t, store)
			corruptSQL(t, store, `UPDATE runs SET proposal_detail = ''`)
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

func TestOpenRefusesHiddenCorruptionWithoutFilesystemMutation(t *testing.T) {
	tests := map[string]string{
		"agent provider":     `UPDATE agents SET provider = 'unknown'`,
		"agent mode":         `UPDATE agents SET execution_mode = 'unknown'`,
		"task incarnation":   `UPDATE tasks SET incarnation_id = zeroblob(15)`,
		"task status":        `UPDATE tasks SET status = 'unknown'`,
		"task private state": `UPDATE tasks SET blocked_reason = 'hidden'`,
		"run phase":          `UPDATE runs SET phase = 'terminal'`,
		"run outcome":        `UPDATE runs SET proposal_detail = 'hidden'`,
		"resource identity":  `UPDATE resources SET state = 'active'`,
		"invalidation":       `UPDATE invalidations SET deleted = 2 WHERE sequence = 1`,
	}
	for name, statement := range tests {
		t.Run(name, func(t *testing.T) {
			store, path := newTestStore(t)
			seedDurableAuthority(t, store)
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			corruptDatabaseFile(t, path, statement)
			before := captureDatabaseEvidence(t, path)
			if _, err := Open(context.Background(), path); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("Open error = %v", err)
			}
			assertDatabaseEvidenceUnchanged(t, path, before)
		})
	}

	t.Run("non-WAL journal", func(t *testing.T) {
		store, path := newTestStore(t)
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		raw := openRaw(t, path)
		var mode string
		if err := raw.QueryRow(`PRAGMA journal_mode = DELETE`).Scan(&mode); err != nil {
			t.Fatal(err)
		}
		if err := raw.Close(); err != nil {
			t.Fatal(err)
		}
		if mode != "delete" {
			t.Fatalf("journal mode = %q", mode)
		}
		before := captureDatabaseEvidence(t, path)
		if _, err := Open(context.Background(), path); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("Open error = %v", err)
		}
		assertDatabaseEvidenceUnchanged(t, path, before)
	})
}

func TestSnapshotAndWatchRejectHiddenControlsInPinnedSnapshot(t *testing.T) {
	tests := map[string]struct {
		corrupt string
		repair  string
		args    func(*testing.T) []any
		secret  string
	}{
		"provider": {corrupt: `UPDATE agents SET provider = 'PROVIDER_SECRET_78c2'`, repair: `UPDATE agents SET provider = 'codex'`, secret: "PROVIDER_SECRET_78c2"},
		"mode":     {corrupt: `UPDATE agents SET execution_mode = 'MODE_SECRET_91f4'`, repair: `UPDATE agents SET execution_mode = 'workspace_write'`, secret: "MODE_SECRET_91f4"},
		"task incarnation": {
			corrupt: `UPDATE tasks SET incarnation_id = zeroblob(15)`,
			repair:  `UPDATE tasks SET incarnation_id = ?`,
			args: func(t *testing.T) []any {
				return []any{incarnationID(t, 4).Bytes()}
			},
		},
		"task private": {corrupt: `UPDATE tasks SET blocked_reason = 'TASK_SECRET_6a31'`, repair: `UPDATE tasks SET blocked_reason = NULL`, secret: "TASK_SECRET_6a31"},
		"run phase":    {corrupt: `UPDATE runs SET phase = 'RUN_SECRET_a229'`, repair: `UPDATE runs SET phase = 'admitted'`, secret: "RUN_SECRET_a229"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			store, _ := newTestStore(t)
			defer store.Close()
			seedDurableAuthority(t, store)
			state, err := store.Factory(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			corruptSQL(t, store, test.corrupt)
			if _, err := store.Snapshot(context.Background()); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("Snapshot error = %v", err)
			} else if test.secret != "" && strings.Contains(err.Error(), test.secret) {
				t.Fatalf("Snapshot exposed hidden control: %v", err)
			}
			if _, err := store.WatchAfter(context.Background(), state.Head); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("WatchAfter error = %v", err)
			} else if test.secret != "" && strings.Contains(err.Error(), test.secret) {
				t.Fatalf("WatchAfter exposed hidden control: %v", err)
			}
			var args []any
			if test.args != nil {
				args = test.args(t)
			}
			corruptSQL(t, store, test.repair, args...)
			if _, err := store.Snapshot(context.Background()); err != nil {
				t.Fatalf("Snapshot after repair: %v", err)
			}
			if batch, err := store.WatchAfter(context.Background(), state.Head); err != nil || len(batch.Invalidations) != 0 {
				t.Fatalf("WatchAfter after repair = %+v, %v", batch, err)
			}
		})
	}
}

func TestConcurrentOpenAndValidWriterNeverMixSnapshots(t *testing.T) {
	writer, path := newTestStore(t)
	defer writer.Close()
	ctx := context.Background()
	start := make(chan struct{})
	writerResult := make(chan error, 1)
	go func() {
		<-start
		state, err := writer.Factory(ctx)
		for index := 0; err == nil && index < 2000; index++ {
			state, err = writer.SetDispatch(ctx, state.Revision, !state.DispatchEnabled, UnixMillis{value: int64(index + 2)})
		}
		writerResult <- err
	}()
	close(start)
	for index := 0; index < 300; index++ {
		opened, err := Open(ctx, path)
		if err != nil {
			t.Fatalf("Open %d returned a false failure: %v", index, err)
		}
		if err := opened.Close(); err != nil {
			t.Fatalf("Close %d: %v", index, err)
		}
	}
	if err := <-writerResult; err != nil {
		t.Fatalf("writer: %v", err)
	}
	state, err := writer.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision.Int64() != 2001 || state.Head.Int64() != 2000 || state.Floor.Int64() != 1 {
		t.Fatalf("final factory state = %+v", state)
	}
	if _, err := writer.Snapshot(ctx); err != nil {
		t.Fatalf("final Snapshot: %v", err)
	}
	if _, err := writer.WatchAfter(ctx, mustSequence(t, 0)); err != nil {
		t.Fatalf("final WatchAfter: %v", err)
	}
}

func TestChangeCommitmentSchemaUsesFrozenBounds(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 1), Name: "p", Root: "/p"}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 2), ProjectID: project.ID, Name: "a", Role: RoleWorker, Provider: ProviderCodex, ExecutionMode: ExecutionWorkspaceWrite, ToolBudgetLimit: 1}, mustTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 3), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 4), Title: "t"}, mustTime(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	var entryColumns, legacyColumns int
	if err := store.readers.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('changes') WHERE name = 'entry_count'`).Scan(&entryColumns); err != nil {
		t.Fatal(err)
	}
	if err := store.readers.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('changes') WHERE name = 'file_count'`).Scan(&legacyColumns); err != nil {
		t.Fatal(err)
	}
	if entryColumns != 1 || legacyColumns != 0 {
		t.Fatalf("change count columns entry=%d legacy=%d", entryColumns, legacyColumns)
	}
	change := changeID(t, 5)
	if _, err := store.writer.Exec(`INSERT INTO changes(
	            id, project_id, task_id, task_incarnation_id, phase, source_root, staging_root,
	            object_format, selected_commit, repository_root, repository_dev, repository_inode, selected_at_ms,
	            stage_dev, stage_inode, prepared_at_ms,
	            tree_digest, entry_count, total_bytes, source_dev, source_inode, available_at_ms,
	            revision, created_at_ms, updated_at_ms
	        ) VALUES(?, ?, ?, ?, 'available', '/source', '/staging', 'sha1', ?, '/repository', 0, 1, 5, 0, 2, 6, ?, ?, ?, 0, 2, 7, 4, 4, 7)`,
		change.Bytes(), project.ID.Bytes(), task.ID.Bytes(), task.IncarnationID.Bytes(), bytes.Repeat([]byte{0x11}, 20), bytes.Repeat([]byte{0x22}, DigestBytes), MaxChangeTreeEntries, MaxChangeTreeBlobBytes); err != nil {
		t.Fatalf("insert exact cap: %v", err)
	}
	if _, err := store.writer.Exec(`UPDATE changes SET entry_count = ? WHERE id = ?`, MaxChangeTreeEntries+1, change.Bytes()); err == nil {
		t.Fatal("entry cap plus one succeeded")
	}
	if _, err := store.writer.Exec(`UPDATE changes SET total_bytes = ? WHERE id = ?`, MaxChangeTreeBlobBytes+1, change.Bytes()); err == nil {
		t.Fatal("aggregate byte cap plus one succeeded")
	}
	var entries, totalBytes int64
	if err := store.readers.QueryRow(`SELECT entry_count, total_bytes FROM changes WHERE id = ?`, change.Bytes()).Scan(&entries, &totalBytes); err != nil {
		t.Fatal(err)
	}
	if entries != MaxChangeTreeEntries || totalBytes != MaxChangeTreeBlobBytes {
		t.Fatalf("failed cap mutations changed commitment: entries=%d bytes=%d", entries, totalBytes)
	}
}

func seedDurableAuthority(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 1), Name: "p", Root: "/p"}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 2), ProjectID: project.ID, Name: "a", Role: RoleOrchestrator, Provider: ProviderCodex, ExecutionMode: ExecutionWorkspaceWrite, ToolBudgetLimit: 1}, mustTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 3), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 4), Title: "t", Body: "private body"}, mustTime(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.Exec(`UPDATE tasks SET status = 'running', revision = revision + 1, updated_at_ms = 5 WHERE id = ?`, task.ID.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.Exec(`INSERT INTO runs(
            id, project_id, agent_id, task_id, task_incarnation_id, admitted_task_work_revision,
	            change_id, role, provider, execution_mode, model, reasoning_effort, verification_policy, phase,
            proposal_kind, proposal_code, proposal_detail, proposal_result,
            terminal_kind, terminal_code, terminal_detail, terminal_result,
            credential_digest, credential_revoked_at_ms,
	            runner_exit_kind, runner_exit_sequence, runner_exit_code, runner_exit_signal, runner_exit_at_ms,
            revision, admitted_at_ms, running_at_ms, finalizing_at_ms, terminal_at_ms, updated_at_ms
	        ) VALUES(?, ?, ?, ?, ?, 1, NULL, 'orchestrator', 'codex', 'workspace_write', NULL, NULL, 'none', 'admitted',
	            NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, ?, NULL, NULL, NULL, NULL, NULL, NULL, 1, 5, NULL, NULL, NULL, 5)`,
		runID(t, 5).Bytes(), project.ID.Bytes(), agent.ID.Bytes(), task.ID.Bytes(), task.IncarnationID.Bytes(), bytes.Repeat([]byte{0x5a}, DigestBytes)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.Exec(`INSERT INTO terminal_sessions(
			id, run_id, state, unresolved_reason, revision, declared_at_ms,
			activated_at_ms, closed_at_ms, updated_at_ms
		) VALUES(?, ?, 'declared', NULL, 1, 5, NULL, NULL, 5)`, terminalSessionID(t, 15).Bytes(), runID(t, 5).Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.Exec(`INSERT INTO resources(
            id, run_id, kind, state, path, path_dev, path_inode, pid, pgid, birth_digest,
            unresolved_reason, revision, declared_at_ms, updated_at_ms, released_at_ms
        ) VALUES(?, ?, 'runtime_root', 'declared', '/runtime', NULL, NULL, NULL, NULL, NULL, NULL, 1, 5, 5, NULL)`,
		resourceID(t, 6).Bytes(), runID(t, 5).Bytes()); err != nil {
		t.Fatal(err)
	}
	for index, kind := range []string{"runner_process", "provider_process", "provider_group"} {
		if _, err := store.writer.Exec(`INSERT INTO resources(
			id, run_id, kind, state, path, path_dev, path_inode, pid, pgid, birth_digest,
			unresolved_reason, revision, declared_at_ms, updated_at_ms, released_at_ms
		) VALUES(?, ?, ?, 'declared', NULL, NULL, NULL, NULL, NULL, NULL, NULL, 1, 5, 5, NULL)`,
			resourceID(t, byte(7+index)).Bytes(), runID(t, 5).Bytes(), kind); err != nil {
			t.Fatal(err)
		}
	}
}

func corruptSQL(t *testing.T, store *Store, statement string, args ...any) {
	t.Helper()
	connection, err := store.writer.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer discardConnection(connection)
	if _, err := connection.ExecContext(context.Background(), `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(context.Background(), `PRAGMA ignore_check_constraints = ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(context.Background(), statement, args...); err != nil {
		t.Fatal(err)
	}
}

func corruptDatabaseFile(t *testing.T, path, statement string, args ...any) {
	t.Helper()
	raw := openRaw(t, path)
	connection, err := raw.Conn(context.Background())
	if err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(context.Background(), `PRAGMA foreign_keys = OFF`); err != nil {
		connection.Close()
		raw.Close()
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(context.Background(), `PRAGMA ignore_check_constraints = ON`); err != nil {
		connection.Close()
		raw.Close()
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(context.Background(), statement, args...); err != nil {
		connection.Close()
		raw.Close()
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
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

func isWatchRestart(err error, reason WatchRestartReason, state FactoryState) bool {
	var target *WatchRestartError
	return errors.As(err, &target) && target.Reason == reason && target.Head == state.Head && target.Floor == state.Floor
}
