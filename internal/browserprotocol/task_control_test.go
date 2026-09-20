package browserprotocol

import (
	"strings"
	"testing"
)

func TestTaskEnqueueControlBoundsAndDirection(t *testing.T) {
	request := TaskEnqueue{TaskID: strings.Repeat("01", 16), IncarnationID: strings.Repeat("02", 16), AgentID: strings.Repeat("03", 16), ExpectedAgentRevision: 7, Instruction: "Run the focused smoke test."}
	wire, err := EncodeTaskEnqueue("enqueue-1", request)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := DecodeClientControl(wire)
	if err != nil || frame.Type != TypeTaskEnqueue || frame.Body.(TaskEnqueue).Instruction != request.Instruction {
		t.Fatalf("request round-trip = %+v, err=%v", frame, err)
	}
	if _, err := DecodeServerControl(wire); err != ErrMalformed {
		t.Fatalf("client request crossed server decoder: %v", err)
	}
	resultWire, err := EncodeTaskEnqueueResult("enqueue-1", TaskEnqueueResult{TaskID: request.TaskID, Revision: 1, AgentRevision: request.ExpectedAgentRevision})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeServerControl(resultWire); err != nil {
		t.Fatal(err)
	}
	for _, instruction := range []string{"", strings.Repeat("x", MaxTaskInstructionBytes+1)} {
		request.Instruction = instruction
		if _, err := EncodeTaskEnqueue("enqueue-2", request); err == nil {
			t.Fatalf("invalid instruction accepted: %q", instruction)
		}
	}
	request.Instruction = "\u0085"
	if _, err := EncodeTaskEnqueue("enqueue-3", request); err != nil {
		t.Fatalf("non-ASCII whitespace drifted from the TypeScript contract: %v", err)
	}
	// "any" queues the instruction for any eligible worker in the pane
	// agent's project; other modes stay refused.
	request.Instruction, request.Mode = "shared", "any"
	if wire, err := EncodeTaskEnqueue("enqueue-4", request); err != nil {
		t.Fatalf("any-worker mode refused: %v", err)
	} else if frame, err := DecodeClientControl(wire); err != nil || frame.Body.(TaskEnqueue).Mode != "any" {
		t.Fatalf("any-worker round-trip = %+v, %v", frame, err)
	}
	request.RepositoryID = strings.Repeat("04", 16)
	if wire, err := EncodeTaskEnqueue("enqueue-repository", request); err != nil {
		t.Fatal(err)
	} else if frame, err := DecodeClientControl(wire); err != nil || frame.Body.(TaskEnqueue).RepositoryID != request.RepositoryID {
		t.Fatalf("repository selection did not round-trip: %+v %v", frame, err)
	}
	request.Mode = "everyone"
	if _, err := EncodeTaskEnqueue("enqueue-5", request); err == nil {
		t.Fatal("unknown mode accepted")
	}
}

// Unclaimed shared work travels in the additive shared_tasks member, so a
// console from before the shared queue (every task item names an agent)
// ignores it. A task item without an agent is still malformed.
func TestSharedTasksCarryOnlyUnclaimedQueuedWork(t *testing.T) {
	item := TaskItem{ID: strings.Repeat("01", 16), ProjectID: strings.Repeat("02", 16), AssignedAgentID: "", Title: "shared", Status: "queued", Priority: 0, Revision: 1}
	snapshot := StateSnapshot{Head: 1, Factory: FactoryItem{Capacity: 1, Revision: 1}, SharedTasks: []TaskItem{item}}
	if err := validateStateSnapshot(snapshot); err != nil {
		t.Fatalf("unclaimed queued task refused: %v", err)
	}
	if err := validateStateSnapshot(StateSnapshot{Head: 1, Factory: snapshot.Factory, Tasks: []TaskItem{item}}); err == nil {
		t.Fatal("task item without an agent accepted")
	}
	claimed := item
	claimed.AssignedAgentID = strings.Repeat("03", 16)
	if err := validateStateSnapshot(StateSnapshot{Head: 1, Factory: snapshot.Factory, Tasks: []TaskItem{claimed}, SharedTasks: []TaskItem{item}}); err == nil {
		t.Fatal("one task served as both claimed and shared")
	}
	for _, bad := range []TaskItem{{ID: item.ID, ProjectID: item.ProjectID, AssignedAgentID: claimed.AssignedAgentID, Title: "shared", Status: "queued", Priority: 0, Revision: 1}, {ID: item.ID, ProjectID: item.ProjectID, Title: "shared", Status: "running", Priority: 0, Revision: 1}} {
		if err := validateStateSnapshot(StateSnapshot{Head: 1, Factory: snapshot.Factory, SharedTasks: []TaskItem{bad}}); err == nil {
			t.Fatalf("shared task item accepted: %+v", bad)
		}
	}
}

func TestAgentControlRejectsAmbiguousObjectives(t *testing.T) {
	request := AgentControl{OperationID: strings.Repeat("01", 16), TaskID: strings.Repeat("02", 16), RunID: strings.Repeat("03", 16), ExpectedTaskRevision: 1, ExpectedRunRevision: 2, Action: "interrupt"}
	for _, action := range []string{"interrupt", "stop", "message", "replace"} {
		request.Action = action
		request.Instruction = ""
		request.SuccessorTaskID = ""
		request.SuccessorIncarnationID = ""
		if action == "message" || action == "replace" {
			request.Instruction = "A new direction"
		}
		if action == "replace" {
			request.SuccessorTaskID = strings.Repeat("04", 16)
			request.SuccessorIncarnationID = strings.Repeat("05", 16)
		}
		wire, err := EncodeAgentControl("control", request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeClientControl(wire); err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeServerControl(wire); err == nil {
			t.Fatal("crossed direction")
		}
	}
	request.SuccessorTaskID = request.TaskID
	if _, err := EncodeAgentControl("control", request); err == nil {
		t.Fatal("replacement reused task identity")
	}
	request.Action = "interrupt"
	if _, err := EncodeAgentControl("control", request); err == nil {
		t.Fatal("interrupt accepted objective payload")
	}
}

func TestTaskListBoundsCursorAndDirection(t *testing.T) {
	agent := strings.Repeat("03", 16)
	before := Decimal(123)
	request := TaskListGet{AgentID: agent, BeforeUpdatedAt: &before, BeforeTaskID: strings.Repeat("01", 16)}
	wire, err := EncodeTaskListGet("list", request)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := DecodeClientControl(wire)
	if err != nil || frame.Body.(TaskListGet).BeforeTaskID != request.BeforeTaskID {
		t.Fatalf("cursor roundtrip: %+v %v", frame, err)
	}
	if _, err := DecodeServerControl(wire); err == nil {
		t.Fatal("client request accepted as server frame")
	}
	request.BeforeUpdatedAt = nil
	if _, err := EncodeTaskListGet("list", request); err == nil {
		t.Fatal("partial cursor accepted")
	}
	task := TaskItem{ID: strings.Repeat("01", 16), ProjectID: strings.Repeat("02", 16), AssignedAgentID: agent, Title: "completed", Status: "cancelled", Revision: 1, UpdatedAtMillis: 123}
	result := TaskList{AgentID: agent, Head: 1, Total: 1, Tasks: []TaskItem{task}}
	wire, err = EncodeTaskList("list", result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeServerControl(wire); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"queued", "running"} {
		result.Tasks[0].Status = status
		if _, err := EncodeTaskList("list", result); err == nil {
			t.Fatal("active task in completed history")
		}
	}
	result.Tasks[0] = task
	result.Tasks[0].AssignedAgentID = strings.Repeat("04", 16)
	if _, err := EncodeTaskList("list", result); err == nil {
		t.Fatal("foreign agent task in history")
	}
	result.Tasks = []TaskItem{task, task}
	result.Total = 2
	if _, err := EncodeTaskList("list", result); err == nil {
		t.Fatal("duplicate task in history")
	}
	result.Tasks = []TaskItem{task}
	result.HasMore = true
	if _, err := EncodeTaskList("list", result); err == nil {
		t.Fatal("short page claims another page")
	}
}

func TestTaskAttachmentRejectsMalformedBinaryAndNullIndex(t *testing.T) {
	good := `{"type":"TASK_ATTACHMENT","id":"upload","body":{"index":0,"offset":"0","size":"3","name":"x.png","data":"YWJj"}}`
	if _, err := DecodeClientControl([]byte(good)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(good, `"index":0`, `"index":null`, 1),
		strings.Replace(good, `"data":"YWJj"`, `"data":[97,98,99]`, 1),
		strings.Replace(good, `"data":"YWJj"`, `"data":null`, 1),
		strings.Replace(good, `"data":"YWJj"`, `"data":"!!!="`, 1),
		strings.Replace(good, `"size":"3"`, `"size":"2"`, 1),
	} {
		if _, err := DecodeClientControl([]byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
