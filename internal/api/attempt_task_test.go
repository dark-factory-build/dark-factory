package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAttemptTaskCarriesOnlyAValidRetainedHandoff(t *testing.T) {
	changeID := strings.Repeat("a", 32)
	handoff := RetainedChangeHandoff{
		ChangeID: changeID, BaseCommit: strings.Repeat("b", 40), TaskID: strings.Repeat("c", 32),
		TaskWorkRevision: 7, ChangeRevision: 9, SourcePath: "/private/runtime/retained-source/" + changeID,
	}
	task := AttemptTask{Task: "review the retained Change", Handoffs: []RetainedChangeHandoff{handoff}}
	if _, err := NewAttemptTaskReply(task); err != nil {
		t.Fatalf("valid handoff rejected: %v", err)
	}
	encoded, err := json.Marshal(task)
	if err != nil || !strings.Contains(string(encoded), `"retained_change_handoffs":[{`) || !strings.Contains(string(encoded), `"task_work_revision":7`) {
		t.Fatalf("task receipt = %s, %v", encoded, err)
	}
	for _, mutate := range []func(*RetainedChangeHandoff){
		func(value *RetainedChangeHandoff) { value.SourcePath = "/private/factory/changes" },
		func(value *RetainedChangeHandoff) { value.TaskID = strings.Repeat("D", 32) },
	} {
		invalid := handoff
		mutate(&invalid)
		if _, err := NewAttemptTaskReply(AttemptTask{Task: task.Task, Handoffs: []RetainedChangeHandoff{invalid}}); err == nil {
			t.Fatal("broad or malformed retained handoff was accepted")
		}
	}
}
