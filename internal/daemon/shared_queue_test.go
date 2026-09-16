//go:build darwin || linux

package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// The console submits shared work from an agent's pane with mode "any": the
// task is queued for any eligible worker in that agent's project and the pane
// agent's revision still fences the write.
func TestBrowserTaskEnqueueAnyWorkerQueuesUnclaimedWork(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve|kernel.BrowserCapabilityHumanActions)
	ctx := context.Background()
	projectID, _ := kernel.ProjectIDFromBytes(adapterID(t, 90))
	agentID, _ := kernel.AgentIDFromBytes(adapterID(t, 91))
	taskID, _ := kernel.TaskIDFromBytes(adapterID(t, 92))
	incarnationID, _ := kernel.IncarnationIDFromBytes(adapterID(t, 93))
	project, err := fixture.store.CreateProject(ctx, kernel.NewProject{ID: projectID, Name: "shared", Root: "/private/shared"}, adapterTime(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := fixture.store.CreateAgent(ctx, kernel.NewAgent{ID: agentID, ProjectID: project.ID, Name: "pane worker", Role: kernel.RoleWorker, Provider: kernel.ProviderCodex, ToolBudgetLimit: 2}, adapterTime(t, 11))
	if err != nil {
		t.Fatal(err)
	}
	connection := fixture.pair(t)
	request := browserprotocol.TaskEnqueue{TaskID: taskID.String(), IncarnationID: incarnationID.String(), AgentID: agent.ID.String(), ExpectedAgentRevision: browserprotocol.Decimal(agent.Revision.Int64()), Instruction: "Whoever is free: fix the flaky test", Mode: "any"}
	payload, err := browserprotocol.EncodeTaskEnqueue("enqueue-any", request)
	if err != nil {
		t.Fatal(err)
	}
	adapterWrite(t, connection, payload)
	frame := adapterRead(t, connection)
	if frame.Type != browserprotocol.TypeTaskEnqueueResult || frame.ID != "enqueue-any" {
		t.Fatalf("enqueue response = %+v", frame)
	}
	result := frame.Body.(browserprotocol.TaskEnqueueResult)
	if result.TaskID != taskID.String() || result.AgentRevision != request.ExpectedAgentRevision {
		t.Fatalf("enqueue result = %+v", result)
	}
	stored, found, err := fixture.store.Task(ctx, taskID)
	if err != nil || !found {
		t.Fatalf("durable task = %+v, found=%v, err=%v", stored, found, err)
	}
	if stored.ProjectID != project.ID || stored.AssignedAgentID != (kernel.AgentID{}) || stored.Status != kernel.TaskQueued {
		t.Fatalf("shared task = %+v", stored)
	}
	// The wire snapshot serves unclaimed work in the additive shared_tasks
	// member, never as a task item without an agent, so a console built before
	// the shared queue keeps decoding snapshots.
	public, err := fixture.store.ReadPublicSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := projectPublicSnapshot(public, func(string, string) (string, string, string) { return "", "", "" })
	if err != nil {
		t.Fatal(err)
	}
	if len(wire.SharedTasks) != 1 || wire.SharedTasks[0].ID != taskID.String() || wire.SharedTasks[0].AssignedAgentID != "" || wire.SharedTasks[0].Status != "queued" {
		t.Fatalf("shared tasks = %+v", wire.SharedTasks)
	}
	for _, item := range wire.Tasks {
		if item.ID == taskID.String() {
			t.Fatalf("unclaimed task served as a task item: %+v", item)
		}
	}
	if _, err := browserprotocol.EncodeStateSnapshot("state", wire); err != nil {
		t.Fatalf("snapshot with shared work refused: %v", err)
	}
}

// The operator API queues shared work with an empty assigned agent; until a
// worker claims it there is no run to send back.
func TestOperatorEnqueueAnyWorkerTaskIsUnclaimed(t *testing.T) {
	fixture := newDispatchFixture(t)
	ctx := context.Background()
	operator, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	call := func(name string, invoke func() error) {
		t.Helper()
		done := fixture.serve(t)
		if err := invoke(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		waitDispatch(t, done)
	}
	call("create project", func() error {
		_, err := operator.CreateProject(ctx, api.CreateProjectInput{ID: testID(1), Name: "project", Root: "/private/tmp/shared-project"})
		return err
	})
	call("create worker", func() error {
		_, err := operator.CreateAgent(ctx, api.CreateAgentInput{ID: testID(2), ProjectID: testID(1), Name: "worker", Role: "worker", Provider: "codex", ToolBudgetLimit: 10})
		return err
	})
	call("enqueue shared task", func() error {
		_, err := operator.EnqueueTask(ctx, api.EnqueueTaskInput{ID: testID(3), ProjectID: testID(1), AssignedAgentID: "", IncarnationID: testID(4), Title: "shared", Body: "any worker"})
		return err
	})
	assertSchedulerWake(t, fixture.daemon)
	stored, found, err := fixture.store.Task(ctx, mustTaskID(t, testID(3)))
	if err != nil || !found || stored.AssignedAgentID != (kernel.AgentID{}) || stored.Status != kernel.TaskQueued {
		t.Fatalf("shared task = %+v, found=%v, err=%v", stored, found, err)
	}
	done := fixture.serve(t)
	snapshot, err := operator.Snapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot with unclaimed task: %v", err)
	}
	waitDispatch(t, done)
	seen := false
	for _, task := range snapshot.Tasks {
		if task.ID == testID(3) {
			seen = task.AssignedAgentID == ""
		}
	}
	if !seen {
		t.Fatalf("unclaimed task not served: %+v", snapshot.Tasks)
	}
	var remote *api.RemoteError
	done = fixture.serve(t)
	if _, err := operator.SendBackTask(ctx, api.SendBackInput{TaskID: testID(3), Note: "nobody ran it"}); !errors.As(err, &remote) || remote.Code() != api.RemoteConflict {
		t.Fatalf("send-back of unclaimed task = %v", err)
	}
	waitDispatch(t, done)
}
