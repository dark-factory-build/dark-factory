package browserprotocol

import (
	"strings"
	"testing"
)

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
