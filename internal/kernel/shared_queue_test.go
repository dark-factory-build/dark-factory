package kernel

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

// A shared task names no agent. Any unarchived, unpaused worker in its
// project may claim it at admission; the claim writes that worker into the
// task inside the admission transaction and stays through corrections.

func newSharedQueueStore(t *testing.T, capacity uint16) (*Store, string, Project, Agent, Agent) {
	t.Helper()
	path, err := canonicalTestDatabasePath(filepath.Join(t.TempDir(), "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store, err := createTestStore(ctx, path, FactoryConfig{DispatchEnabled: true, Capacity: capacity}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 1), Name: "p", Root: "/project"}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 2), ProjectID: project.ID, Name: "first", Role: RoleWorker, Provider: ProviderCodex, ToolBudgetLimit: 5}, mustTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 3), ProjectID: project.ID, Name: "second", Role: RoleWorker, Provider: ProviderCodex, ToolBudgetLimit: 5}, mustTime(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	return store, path, project, first, second
}

func sharedTask(t *testing.T, store *Store, project Project, seed byte, priority int64, at int64) Task {
	t.Helper()
	task, err := store.EnqueueTask(context.Background(), NewTask{ID: taskID(t, seed), ProjectID: project.ID, IncarnationID: incarnationID(t, seed+1), Title: "shared", Priority: priority}, mustTime(t, at))
	if err != nil {
		t.Fatal(err)
	}
	if !task.AssignedAgentID.zero() || task.Status != TaskQueued {
		t.Fatalf("shared task = %+v", task)
	}
	return task
}

func pauseAgent(t *testing.T, store *Store, agent Agent, paused bool, at int64) Agent {
	t.Helper()
	updated, err := store.UpdateAgent(context.Background(), agent.ID, agent.Revision, AgentPatch{Paused: &paused}, mustTime(t, at))
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

// settleWorkerRunForTest drives an admitted worker run through activation,
// a successful proposal, resource release and Change retention to terminal.
func settleWorkerRunForTest(t *testing.T, store *Store, run Run, keys AdmissionKeys, at int64) Run {
	t.Helper()
	ctx := context.Background()
	format, _ := NewObjectFormat("sha1")
	commit, _ := NewCommitID(format, bytes.Repeat([]byte{1}, 20))
	repository, _ := NewFileIdentity(61, 62)
	selection, _ := NewChangeSelection(format, commit, repository)
	change, found, err := store.Change(ctx, *run.ChangeID)
	if err != nil || !found {
		t.Fatalf("change = %+v, found=%v, err=%v", change, found, err)
	}
	prepared, err := store.RecordChangePrepared(ctx, change.ID, change.Revision, selection, mustTime(t, at))
	if err != nil {
		t.Fatalf("prepare change: %v", err)
	}
	if _, err := store.MarkChangeAvailable(ctx, change.ID, prepared.Revision, selection.commit, mustTime(t, at+1)); err != nil {
		t.Fatalf("mark change available: %v", err)
	}
	_, activated := activateAllResources(t, store, run, keys, at+2)
	session := terminalSessionForRunTest(t, store, run.ID)
	running, err := store.ActivateRun(ctx, run.ID, session.ID, activated.Revision, session.Revision, mustTime(t, at+10))
	if err != nil {
		t.Fatalf("activate run: %v", err)
	}
	proposal, _ := NewSuccessProposal("done")
	if _, err := store.ProposeAttemptOutcome(ctx, keys.AttemptDigest, proposal, mustTime(t, at+11)); err != nil {
		t.Fatalf("propose outcome: %v", err)
	}
	observeMissingProcessExits(t, store, running.ID, at+12)
	for index, resource := range resourcesForRunTest(t, store, running.ID) {
		if resource.State == ResourceReleased {
			continue
		}
		if _, err := store.ReleaseResource(ctx, running.ID, resource.ID, resource.Revision, resource.Identity, mustTime(t, at+15+int64(index))); err != nil {
			t.Fatalf("release resource: %v", err)
		}
	}
	closeTerminalSessionAtCurrent(t, store, running.ID, at+20)
	finalizing, found, err := store.Run(ctx, running.ID)
	if err != nil || !found {
		t.Fatalf("finalizing run = %+v, found=%v, err=%v", finalizing, found, err)
	}
	terminal, err := finalizeTestRun(t, store, finalizing, at+21)
	if err != nil {
		t.Fatalf("finalize run: %v", err)
	}
	return terminal
}

func TestSharedTaskIsClaimedByExactlyOneWorkerAcrossStores(t *testing.T) {
	store, path, project, first, second := newSharedQueueStore(t, 2)
	task := sharedTask(t, store, project, 10, 0, 5)
	other, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	defer store.Close()
	start := make(chan struct{})
	results := make(chan AdmissionResult, 2)
	var wait sync.WaitGroup
	for index, candidate := range []*Store{store, other} {
		wait.Add(1)
		go func(index int, candidate *Store) {
			defer wait.Done()
			<-start
			result, err := candidate.AdmitNext(context.Background(), admissionKeys(t, byte(40+index*30), nil), mustTime(t, 20))
			if err != nil {
				t.Errorf("concurrent claim: %v", err)
			}
			results <- result
		}(index, candidate)
	}
	close(start)
	wait.Wait()
	close(results)
	var winner *Run
	for result := range results {
		if result.Admitted() {
			if winner != nil {
				t.Fatal("two workers claimed one shared task")
			}
			winner = result.Run
		}
	}
	if winner == nil || winner.TaskID != task.ID || winner.AgentID != first.ID && winner.AgentID != second.ID {
		t.Fatalf("winner = %+v", winner)
	}
	fresh, _, _ := store.Task(context.Background(), task.ID)
	var runs int
	if err := store.readers.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if fresh.Status != TaskRunning || fresh.AssignedAgentID != winner.AgentID || runs != 1 {
		t.Fatalf("claimed task=%+v runs=%d", fresh, runs)
	}
}

func TestWorkerPrefersItsSpecificTaskBeforeSharedWork(t *testing.T) {
	ctx := context.Background()
	store, _, project, first, second := newSharedQueueStore(t, 4)
	defer store.Close()
	specific, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 10), ProjectID: project.ID, AssignedAgentID: first.ID, IncarnationID: incarnationID(t, 11), Title: "specific", Priority: 0}, mustTime(t, 5))
	if err != nil {
		t.Fatal(err)
	}
	shared := sharedTask(t, store, project, 12, 5, 6)
	second = pauseAgent(t, store, second, true, 7)
	// Alone, the first worker takes its own task even though the shared one
	// outranks it; the shared task waits unclaimed.
	result, err := store.AdmitNext(ctx, admissionKeys(t, 40, nil), mustTime(t, 10))
	if err != nil || !result.Admitted() || result.Run.TaskID != specific.ID || result.Run.AgentID != first.ID {
		t.Fatalf("specific-first admission = %+v, %v", result, err)
	}
	waiting, _, _ := store.Task(ctx, shared.ID)
	if waiting.Status != TaskQueued || !waiting.AssignedAgentID.zero() {
		t.Fatalf("shared task while first is busy = %+v", waiting)
	}
	if result, err := store.AdmitNext(ctx, admissionKeys(t, 70, nil), mustTime(t, 11)); err != nil || result.Admitted() || result.Reason != NoAdmissionNoEligibleWork {
		t.Fatalf("no free worker = %+v, %v", result, err)
	}
	// A resumed second worker with nothing of its own claims the shared task.
	pauseAgent(t, store, second, false, 12)
	result, err = store.AdmitNext(ctx, admissionKeys(t, 82, nil), mustTime(t, 13))
	if err != nil || !result.Admitted() || result.Run.TaskID != shared.ID || result.Run.AgentID != second.ID {
		t.Fatalf("shared claim = %+v, %v", result, err)
	}
	claimed, _, _ := store.Task(ctx, shared.ID)
	if claimed.AssignedAgentID != second.ID || claimed.Status != TaskRunning {
		t.Fatalf("claimed shared task = %+v", claimed)
	}
}

func TestSharedTaskOutranksSpecificWorkAcrossWorkers(t *testing.T) {
	ctx := context.Background()
	store, _, project, first, second := newSharedQueueStore(t, 4)
	defer store.Close()
	if _, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 10), ProjectID: project.ID, AssignedAgentID: first.ID, IncarnationID: incarnationID(t, 11), Title: "specific", Priority: 0}, mustTime(t, 5)); err != nil {
		t.Fatal(err)
	}
	shared := sharedTask(t, store, project, 12, 5, 6)
	// The canonical global order still wins across agents: the second worker's
	// best candidate is the higher-priority shared task.
	result, err := store.AdmitNext(ctx, admissionKeys(t, 40, nil), mustTime(t, 10))
	if err != nil || !result.Admitted() || result.Run.TaskID != shared.ID || result.Run.AgentID != second.ID {
		t.Fatalf("global admission = %+v, %v", result, err)
	}
	result, err = store.AdmitNext(ctx, admissionKeys(t, 70, nil), mustTime(t, 11))
	if err != nil || !result.Admitted() || result.Run.AgentID != first.ID {
		t.Fatalf("specific admission = %+v, %v", result, err)
	}
}

// sentBackSharedTask claims a shared task for the first worker, settles that
// run and sends the task back: it is queued again at work revision 2.
func sentBackSharedTask(t *testing.T) (*Store, Task, Agent, Agent) {
	t.Helper()
	ctx := context.Background()
	store, _, project, first, second := newSharedQueueStore(t, 4)
	second = pauseAgent(t, store, second, true, 5)
	task := sharedTask(t, store, project, 10, 0, 6)
	keys := admissionKeys(t, 40, nil)
	result, err := store.AdmitNext(ctx, keys, mustTime(t, 10))
	if err != nil || !result.Admitted() || result.Run.AgentID != first.ID {
		store.Close()
		t.Fatalf("claim = %+v, %v", result, err)
	}
	settleWorkerRunForTest(t, store, *result.Run, keys, 20)
	settled, _, _ := store.Task(ctx, task.ID)
	if settled.Status != TaskSucceeded || settled.AssignedAgentID != first.ID {
		store.Close()
		t.Fatalf("settled task = %+v", settled)
	}
	returned, err := store.SendBackTask(ctx, task.ID, settled.Revision, "fix the test", mustTime(t, 50))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if returned.Status != TaskQueued || returned.WorkRevision.Int64() != 2 || returned.AssignedAgentID != first.ID {
		store.Close()
		t.Fatalf("sent-back task = %+v", returned)
	}
	return store, returned, first, second
}

func TestClaimedSharedTaskStaysWithItsWorkerThroughCorrections(t *testing.T) {
	ctx := context.Background()
	store, task, first, second := sentBackSharedTask(t)
	defer store.Close()
	// The correction is the first worker's alone: an idle second worker does
	// not claim it, and the first worker gets it back with its retained Change.
	first = pauseAgent(t, store, first, true, 60)
	pauseAgent(t, store, second, false, 61)
	if result, err := store.AdmitNext(ctx, admissionKeys(t, 70, nil), mustTime(t, 62)); err != nil || result.Admitted() || result.Reason != NoAdmissionNoEligibleWork {
		t.Fatalf("second worker took a sent-back task = %+v, %v", result, err)
	}
	pauseAgent(t, store, first, false, 63)
	result, err := store.AdmitNext(ctx, admissionKeys(t, 82, nil), mustTime(t, 64))
	if err != nil || !result.Admitted() || result.Run.TaskID != task.ID || result.Run.AgentID != first.ID || result.Run.AdmittedTaskWorkRevision.Int64() != 2 || result.Run.AdmittedChangeRevision == nil || result.Run.AdmittedChangeRevision.Int64() == 1 {
		t.Fatalf("correction admission = %+v, %v", result, err)
	}
}

func TestExplicitReassignmentMovesASentBackSharedTask(t *testing.T) {
	ctx := context.Background()
	store, task, first, second := sentBackSharedTask(t)
	defer store.Close()
	pauseAgent(t, store, second, false, 60)
	moved, err := store.UpdateTask(ctx, task.ID, task.Revision, TaskPatch{AssignedAgentID: &second.ID}, mustTime(t, 61))
	if err != nil || moved.AssignedAgentID != second.ID || moved.WorkRevision.Int64() != 2 {
		t.Fatalf("reassigned task = %+v, %v", moved, err)
	}
	result, err := store.AdmitNext(ctx, admissionKeys(t, 70, nil), mustTime(t, 62))
	if err != nil || !result.Admitted() || result.Run.TaskID != task.ID || result.Run.AgentID != second.ID || result.Run.AgentID == first.ID {
		t.Fatalf("reassigned admission = %+v, %v", result, err)
	}
}

func TestSharedTaskWaitsForAnEligibleWorker(t *testing.T) {
	ctx := context.Background()
	store, _, project, first, second := newSharedQueueStore(t, 4)
	defer store.Close()
	overseer, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 4), ProjectID: project.ID, Name: "overseer", Role: RoleOrchestrator, Provider: ProviderCodex, ToolBudgetLimit: 5}, mustTime(t, 5))
	if err != nil {
		t.Fatal(err)
	}
	elsewhere, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 9), Name: "other", Root: "/other"}, mustTime(t, 6))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 5), ProjectID: elsewhere.ID, Name: "stranger", Role: RoleWorker, Provider: ProviderCodex, ToolBudgetLimit: 5}, mustTime(t, 7)); err != nil {
		t.Fatal(err)
	}
	pauseAgent(t, store, first, true, 8)
	archived := true
	if _, err := store.UpdateAgent(ctx, second.ID, second.Revision, AgentPatch{Archived: &archived}, mustTime(t, 9)); err != nil {
		t.Fatal(err)
	}
	task := sharedTask(t, store, project, 10, 9, 10)
	// A paused worker, an archived worker, the overseer and another project's
	// worker are not eligible: the task stays queued and unclaimed.
	if result, err := store.AdmitNext(ctx, admissionKeys(t, 40, nil), mustTime(t, 11)); err != nil || result.Admitted() || result.Reason != NoAdmissionNoEligibleWork {
		t.Fatalf("ineligible admission = %+v, %v", result, err)
	}
	waiting, _, _ := store.Task(ctx, task.ID)
	if waiting.Status != TaskQueued || !waiting.AssignedAgentID.zero() {
		t.Fatalf("waiting task = %+v", waiting)
	}
	_ = overseer
	// The console can still edit and cancel it; a cancelled unclaimed task
	// leaves the public view without claiming a completion slot.
	priority := int64(3)
	edited, err := store.UpdateTask(ctx, task.ID, waiting.Revision, TaskPatch{Priority: &priority}, mustTime(t, 12))
	if err != nil || !edited.AssignedAgentID.zero() || edited.Priority != 3 {
		t.Fatalf("edited task = %+v, %v", edited, err)
	}
	if _, err := store.SendBackTask(ctx, task.ID, edited.Revision, "note", mustTime(t, 13)); !errors.Is(err, ErrConflict) {
		t.Fatalf("send-back of unclaimed task = %v", err)
	}
	cancelled, err := store.UpdateTask(ctx, task.ID, edited.Revision, TaskPatch{Cancel: true}, mustTime(t, 14))
	if err != nil || cancelled.Status != TaskCancelled || !cancelled.AssignedAgentID.zero() {
		t.Fatalf("cancelled task = %+v, %v", cancelled, err)
	}
	snapshot, err := store.ReadPublicSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, summary := range snapshot.Tasks {
		if summary.ID == task.ID {
			t.Fatalf("cancelled unclaimed task in public view: %+v", summary)
		}
	}
}

func TestSharedQueueSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	store, path, project, first, second := newSharedQueueStore(t, 4)
	if _, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 10), ProjectID: project.ID, AssignedAgentID: first.ID, IncarnationID: incarnationID(t, 11), Title: "specific", Priority: 9}, mustTime(t, 5)); err != nil {
		t.Fatal(err)
	}
	shared := sharedTask(t, store, project, 12, 0, 6)
	if result, err := store.AdmitNext(ctx, admissionKeys(t, 40, nil), mustTime(t, 10)); err != nil || !result.Admitted() || result.Run.AgentID != first.ID {
		t.Fatalf("first admission = %+v, %v", result, err)
	}
	public, err := store.ReadPublicSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, summary := range public.Tasks {
		if summary.ID == shared.ID {
			seen = summary.AssignedAgentID.zero() && summary.Status == "queued"
		}
	}
	if !seen {
		t.Fatalf("unclaimed shared task not public: %+v", public.Tasks)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// The durable validator accepts the unclaimed queued task and the claimed
	// running one, and the next probe claims the shared task.
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen with shared work: %v", err)
	}
	defer reopened.Close()
	if _, err := reopened.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := reopened.AdmitNext(ctx, admissionKeys(t, 70, nil), mustTime(t, 20))
	if err != nil || !result.Admitted() || result.Run.TaskID != shared.ID || result.Run.AgentID != second.ID {
		t.Fatalf("claim after reopen = %+v, %v", result, err)
	}
}

func TestMigratedHomeKeepsAssignmentsAndAcceptsSharedTasks(t *testing.T) {
	ctx := context.Background()
	path, _ := newLegacyDatabase(t, false, v12UserVersion)
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open v12 home: %v", err)
	}
	defer store.Close()
	var unclaimed int
	if err := store.readers.QueryRow(`SELECT COUNT(*) FROM tasks WHERE assigned_agent_id IS NULL`).Scan(&unclaimed); err != nil {
		t.Fatal(err)
	}
	if unclaimed != 0 {
		t.Fatalf("migration unassigned %d existing tasks", unclaimed)
	}
	task, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 200), ProjectID: projectID(t, 1), IncarnationID: incarnationID(t, 201), Title: "shared after upgrade"}, mustTime(t, 100))
	if err != nil || !task.AssignedAgentID.zero() {
		t.Fatalf("shared task on migrated home = %+v, %v", task, err)
	}
	if _, err := store.ReadPublicSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
}
