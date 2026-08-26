package kernel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestAdmitNextSelectsCanonicalCurrentQueueInsideTransaction(t *testing.T) {
	t.Run("priority then creation time", func(t *testing.T) {
		store, _, project, agent := newAdmissionStore(t, RoleOrchestrator, 4)
		defer store.Close()
		ctx := context.Background()
		stale, _ := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 20), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 21), Title: "stale", Priority: 1}, mustTime(t, 10))
		high, _ := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 22), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 23), Title: "high", Priority: 2}, mustTime(t, 20))
		result, err := store.AdmitNext(ctx, agent.ID, admissionKeys(t, 30, nil), mustTime(t, 30))
		if err != nil || !result.Admitted() || result.Run.TaskID != high.ID || result.Run.TaskID == stale.ID {
			t.Fatalf("admission = %+v, %v", result, err)
		}
	})
	t.Run("binary identifier tie break", func(t *testing.T) {
		store, _, project, agent := newAdmissionStore(t, RoleOrchestrator, 4)
		defer store.Close()
		ctx := context.Background()
		highID := taskID(t, 42)
		lowID := taskID(t, 41)
		_, _ = store.EnqueueTask(ctx, NewTask{ID: highID, ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 43), Title: "higher bytes", Priority: 7}, mustTime(t, 10))
		low, _ := store.EnqueueTask(ctx, NewTask{ID: lowID, ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 44), Title: "lower bytes", Priority: 7}, mustTime(t, 10))
		result, err := store.AdmitNext(ctx, agent.ID, admissionKeys(t, 50, nil), mustTime(t, 20))
		if err != nil || !result.Admitted() || result.Run.TaskID != low.ID {
			t.Fatalf("binary-order admission = %+v, %v", result, err)
		}
	})
}

func TestAdmissionCreatesExactDeclaredTerminalSession(t *testing.T) {
	store, _, _, agent := newAdmissionStore(t, RoleOrchestrator, 4)
	defer store.Close()
	if _, err := store.EnqueueTask(context.Background(), NewTask{ID: taskID(t, 221), ProjectID: agent.ProjectID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 222), Title: "session"}, mustTime(t, 5)); err != nil {
		t.Fatal(err)
	}
	keys := admissionKeys(t, 220, nil)
	result, err := store.AdmitNext(context.Background(), agent.ID, keys, mustTime(t, 10))
	if err != nil || !result.Admitted() {
		t.Fatalf("admission = %+v, %v", result, err)
	}
	session, found, err := store.TerminalSession(context.Background(), keys.TerminalSessionID)
	if err != nil || !found || session.RunID != result.Run.ID || session.State != TerminalSessionDeclared || session.Revision.Int64() != 1 {
		t.Fatalf("terminal session = %+v, found=%v, err=%v", session, found, err)
	}
	var count int
	if err := store.writer.QueryRow(`SELECT COUNT(*) FROM terminal_sessions WHERE run_id = ?`, result.Run.ID.Bytes()).Scan(&count); err != nil || count != 1 {
		t.Fatalf("terminal session count = %d, err=%v", count, err)
	}
}

func TestAdmissionGatesHaveZeroFootprint(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *Store, Agent)
		want   NoAdmissionReason
	}{
		{name: "disabled", mutate: func(t *testing.T, store *Store, _ Agent) {
			state, err := store.Factory(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.SetDispatch(context.Background(), state.Revision, false, mustTime(t, 10)); err != nil {
				t.Fatal(err)
			}
		}, want: NoAdmissionDispatchDisabled},
		{name: "paused", mutate: func(t *testing.T, store *Store, agent Agent) {
			if _, err := store.writer.Exec(`UPDATE agents SET paused = 1, revision = revision + 1 WHERE id = ?`, agent.ID.Bytes()); err != nil {
				t.Fatal(err)
			}
		}, want: NoAdmissionAgentPaused},
		{name: "budget", mutate: func(t *testing.T, store *Store, agent Agent) {
			if _, err := store.writer.Exec(`UPDATE agents SET tool_calls_used = tool_budget_limit, revision = revision + 1 WHERE id = ?`, agent.ID.Bytes()); err != nil {
				t.Fatal(err)
			}
		}, want: NoAdmissionBudgetExhausted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, _, project, agent := newAdmissionStore(t, RoleOrchestrator, 2)
			defer store.Close()
			task, err := store.EnqueueTask(context.Background(), NewTask{ID: taskID(t, 60), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 61), Title: "queued"}, mustTime(t, 5))
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, store, agent)
			before := admissionFootprint(t, store)
			result, err := store.AdmitNext(context.Background(), agent.ID, admissionKeys(t, 62, nil), mustTime(t, 20))
			if err != nil || result.Admitted() || result.Reason != test.want {
				t.Fatalf("result = %+v, %v", result, err)
			}
			after := admissionFootprint(t, store)
			if before != after {
				t.Fatalf("gate footprint before=%+v after=%+v", before, after)
			}
			fresh, _, _ := store.Task(context.Background(), task.ID)
			if fresh.Status != TaskQueued {
				t.Fatalf("task = %+v", fresh)
			}
		})
	}
}

func TestAdmissionCreatesExactWorkerFootprintAndReconciles(t *testing.T) {
	store, _, project, agent := newAdmissionStore(t, RoleWorker, 2)
	defer store.Close()
	task, err := store.EnqueueTask(context.Background(), NewTask{ID: taskID(t, 70), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 71), Title: "worker"}, mustTime(t, 5))
	if err != nil {
		t.Fatal(err)
	}
	reservation := &ChangeReservation{ID: changeID(t, 72), SourceRoot: "/changes/published", StagingRoot: "/changes/staged"}
	keys := admissionKeys(t, 73, reservation)
	result, err := store.AdmitNext(context.Background(), agent.ID, keys, mustTime(t, 10))
	if err != nil || !result.Admitted() {
		t.Fatalf("admit = %+v, %v", result, err)
	}
	if result.Run.Phase != RunAdmitted || result.Run.ChangeID == nil || *result.Run.ChangeID != reservation.ID || !bytes.Equal(result.Run.CredentialDigest.Bytes(), keys.AttemptDigest.Bytes()) {
		t.Fatalf("run binding = %+v", result.Run)
	}
	freshTask, _, _ := store.Task(context.Background(), task.ID)
	change, found, err := store.Change(context.Background(), reservation.ID)
	if err != nil || !found || change.Phase != ChangeReserved || freshTask.Status != TaskRunning {
		t.Fatalf("task/change = %+v %+v %v", freshTask, change, err)
	}
	resources := resourcesForRunTest(t, store, result.Run.ID)
	if len(resources) != 4 {
		t.Fatalf("resources = %+v", resources)
	}
	for _, resource := range resources {
		if resource.State != ResourceDeclared || !resource.Identity.Empty() {
			t.Fatalf("declared resource = %+v", resource)
		}
	}
	reconciled, err := store.ReconcileAdmission(context.Background(), keys)
	if err != nil || !reconciled.Admitted() || reconciled.Run.ID != result.Run.ID {
		t.Fatalf("reconcile = %+v, %v", reconciled, err)
	}
	retried, err := store.AdmitNext(context.Background(), agent.ID, keys, mustTime(t, 99))
	if err != nil || !retried.Admitted() || retried.Run.AdmittedAt != result.Run.AdmittedAt || retried.Run.Revision != result.Run.Revision {
		t.Fatalf("retry = %+v, %v", retried, err)
	}
	conflict := keys
	conflict.RuntimeRoot = "/different-runtime"
	if _, err := store.ReconcileAdmission(context.Background(), conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting reconciliation = %v", err)
	}
	footprint := admissionFootprint(t, store)
	if footprint.runs != 1 || footprint.resources != 4 || footprint.changes != 1 {
		t.Fatalf("footprint = %+v", footprint)
	}
}

func TestAdmissionRejectsNonCanonicalOwnershipLocators(t *testing.T) {
	store, _, project, agent := newAdmissionStore(t, RoleWorker, 2)
	defer store.Close()
	_, err := store.EnqueueTask(context.Background(), NewTask{ID: taskID(t, 75), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 76), Title: "locator"}, mustTime(t, 5))
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*AdmissionKeys){
		"aliased Change roots": func(keys *AdmissionKeys) {
			keys.Change = &ChangeReservation{ID: changeID(t, 77), SourceRoot: "/changes/same", StagingRoot: "/changes/same/."}
		},
		"unclean runtime": func(keys *AdmissionKeys) { keys.RuntimeRoot = "/runtime/run/." },
		"root runtime":    func(keys *AdmissionKeys) { keys.RuntimeRoot = "/" },
	} {
		t.Run(name, func(t *testing.T) {
			keys := admissionKeys(t, 78, &ChangeReservation{ID: changeID(t, 79), SourceRoot: "/changes/source", StagingRoot: "/changes/stage"})
			mutate(&keys)
			before := admissionFootprint(t, store)
			if _, err := store.AdmitNext(context.Background(), agent.ID, keys, mustTime(t, 10)); !errors.Is(err, ErrInvalidValue) {
				t.Fatalf("admission error = %v", err)
			}
			if after := admissionFootprint(t, store); after != before {
				t.Fatalf("invalid locator footprint before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestIndependentStoresCannotAdmitSameAgentOrTask(t *testing.T) {
	store, path, project, agent := newAdmissionStore(t, RoleOrchestrator, 4)
	task, err := store.EnqueueTask(context.Background(), NewTask{ID: taskID(t, 80), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 81), Title: "race"}, mustTime(t, 5))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	defer store.Close()
	start := make(chan struct{})
	results := make(chan AdmissionResult, 2)
	errorsSeen := make(chan error, 2)
	var wait sync.WaitGroup
	for index, candidate := range []*Store{store, second} {
		wait.Add(1)
		go func(index int, candidate *Store) {
			defer wait.Done()
			<-start
			result, err := candidate.AdmitNext(context.Background(), agent.ID, admissionKeys(t, byte(90+index*10), nil), mustTime(t, 20))
			results <- result
			errorsSeen <- err
		}(index, candidate)
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsSeen)
	winners := 0
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent admission: %v", err)
		}
	}
	for result := range results {
		if result.Admitted() {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("winners = %d", winners)
	}
	fresh, _, _ := store.Task(context.Background(), task.ID)
	var runs int
	if err := store.readers.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if fresh.Status != TaskRunning || runs != 1 {
		t.Fatalf("durable race result task=%+v runs=%d", fresh, runs)
	}
}

func TestAdmissionTaskGuardFailureRollsBackEntireFootprint(t *testing.T) {
	store, _, project, agent := newAdmissionStore(t, RoleWorker, 2)
	defer store.Close()
	task, err := store.EnqueueTask(context.Background(), NewTask{ID: taskID(t, 105), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 106), Title: "guarded"}, mustTime(t, 5))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.Exec(`CREATE TRIGGER suppress_admission_task_update BEFORE UPDATE ON tasks WHEN OLD.id = X'69696969696969696969696969696969' BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	before := admissionFootprint(t, store)
	reservation := &ChangeReservation{ID: changeID(t, 107), SourceRoot: "/changes/guarded-published", StagingRoot: "/changes/guarded-staged"}
	result, err := store.AdmitNext(context.Background(), agent.ID, admissionKeys(t, 108, reservation), mustTime(t, 10))
	if !errors.Is(err, ErrRevisionConflict) || result.Admitted() {
		t.Fatalf("guarded admission = %+v, %v", result, err)
	}
	after := admissionFootprint(t, store)
	if before != after {
		t.Fatalf("guard failure footprint before=%+v after=%+v", before, after)
	}
	fresh, found, err := store.Task(context.Background(), task.ID)
	if err != nil || !found || fresh.Status != TaskQueued {
		t.Fatalf("task after guarded rollback = %+v found=%v err=%v", fresh, found, err)
	}
}

func TestAdmissionRequiresEveryDeclaredResourceInsert(t *testing.T) {
	for _, kind := range []ResourceKind{ResourceRuntimeRoot, ResourceRunnerProcess, ResourceProviderProcess, ResourceProviderGroup} {
		t.Run(kind.String(), func(t *testing.T) {
			store, _, project, agent := newAdmissionStore(t, RoleWorker, 2)
			defer store.Close()
			task, err := store.EnqueueTask(context.Background(), NewTask{
				ID: taskID(t, 109), ProjectID: project.ID, AssignedAgentID: agent.ID,
				IncarnationID: incarnationID(t, 110), Title: "resource insert guard",
			}, mustTime(t, 5))
			if err != nil {
				t.Fatal(err)
			}
			trigger := fmt.Sprintf(`CREATE TRIGGER suppress_resource_insert BEFORE INSERT ON resources WHEN NEW.kind = '%s' BEGIN SELECT RAISE(IGNORE); END`, kind.String())
			if _, err := store.writer.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			before := admissionFootprint(t, store)
			reservation := &ChangeReservation{ID: changeID(t, 111), SourceRoot: "/changes/resource-guard", StagingRoot: "/changes/resource-guard-stage"}
			result, err := store.AdmitNext(context.Background(), agent.ID, admissionKeys(t, 112, reservation), mustTime(t, 10))
			if !errors.Is(err, ErrRevisionConflict) || result.Admitted() {
				t.Fatalf("suppressed %s admission = %+v, %v", kind.String(), result, err)
			}
			if after := admissionFootprint(t, store); after != before {
				t.Fatalf("suppressed %s left footprint: before=%+v after=%+v", kind.String(), before, after)
			}
			fresh, found, err := store.Task(context.Background(), task.ID)
			if err != nil || !found || fresh.Status != TaskQueued {
				t.Fatalf("suppressed %s task = %+v found=%v err=%v", kind.String(), fresh, found, err)
			}
		})
	}
}

type admissionCounts struct{ runs, resources, changes, invalidations int }

func admissionFootprint(t *testing.T, store *Store) admissionCounts {
	t.Helper()
	var result admissionCounts
	for table, target := range map[string]*int{"runs": &result.runs, "resources": &result.resources, "changes": &result.changes, "invalidations": &result.invalidations} {
		if err := store.readers.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func newAdmissionStore(t *testing.T, role AgentRole, capacity uint16) (*Store, string, Project, Agent) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kernel.db")
	store, err := Create(context.Background(), path, FactoryConfig{DispatchEnabled: true, Capacity: capacity}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(context.Background(), NewProject{ID: projectID(t, 1), Name: "p", Root: "/project"}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := store.CreateAgent(context.Background(), NewAgent{ID: agentID(t, 2), ProjectID: project.ID, Name: "a", Role: role, Provider: ProviderCodex, ExecutionMode: ExecutionWorkspaceWrite, ToolBudgetLimit: 5}, mustTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	return store, path, project, agent
}

func admissionKeys(t *testing.T, seed byte, change *ChangeReservation) AdmissionKeys {
	t.Helper()
	digest, err := AttemptDigestFromBytes(bytes.Repeat([]byte{seed}, DigestBytes))
	if err != nil {
		t.Fatal(err)
	}
	return AdmissionKeys{
		RunID: runID(t, seed), TerminalSessionID: terminalSessionID(t, seed+20), AttemptDigest: digest, Change: change, RuntimeRoot: "/runtime/" + string([]byte{'a' + seed%20}),
		Resources: AdmissionResourceIDs{RuntimeRoot: resourceID(t, seed+1), RunnerProcess: resourceID(t, seed+2), ProviderProcess: resourceID(t, seed+3), ProviderGroup: resourceID(t, seed+4)},
	}
}

func resourcesForRunTest(t *testing.T, store *Store, runID RunID) []Resource {
	t.Helper()
	connection, err := store.readerConnection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	resources, err := resourcesForRun(context.Background(), connection, runID)
	if err != nil {
		t.Fatal(err)
	}
	return resources
}

func terminalSessionForRunTest(t testing.TB, store *Store, runID RunID) TerminalSession {
	t.Helper()
	session, found, err := store.TerminalSessionForRun(context.Background(), runID)
	if err != nil || !found {
		t.Fatalf("terminal session = %+v, found=%v, err=%v", session, found, err)
	}
	return session
}
