package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAttemptTaskAssignmentReceiptIsAdditiveAndTerminalSafe(t *testing.T) {
	value := AttemptTask{
		Task:                   "inspect\u007f\u0085",
		TaskID:                 strings.Repeat("1", 32),
		IncarnationID:          strings.Repeat("2", 32),
		WorkRevision:           3,
		ChangeID:               strings.Repeat("3", 32),
		AdmittedChangeRevision: 4,
		ChangeRevision:         5,
		BaseCommit:             strings.Repeat("a", 40),
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal = %v", err)
	}
	text := string(encoded)
	for _, field := range []string{`"task"`, `"task_id"`, `"incarnation_id"`, `"work_revision"`, `"change_id"`, `"admitted_change_revision"`, `"change_revision"`, `"base_commit"`} {
		if !strings.Contains(text, field) {
			t.Fatalf("assignment receipt omitted %s: %s", field, text)
		}
	}
	if strings.Contains(text, "\x7f") || strings.Contains(text, "\x85") {
		t.Fatalf("unsafe task text in JSON: %q", text)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded["task"] != "inspect\x7f\u0085" {
		t.Fatalf("decoded assignment = %#v, err=%v", decoded, err)
	}
}

func TestAttemptTaskAssignmentReceiptRequiresCompleteWorkerIdentity(t *testing.T) {
	value := AttemptTask{Task: "task", TaskID: strings.Repeat("1", 32)}
	if _, err := NewAttemptTaskReply(value); err != ErrInvalidInput {
		t.Fatalf("partial assignment error = %v", err)
	}
	if _, err := NewAttemptTaskReply(AttemptTask{Task: "orchestrator"}); err != nil {
		t.Fatalf("orchestrator assignment error = %v", err)
	}
}
