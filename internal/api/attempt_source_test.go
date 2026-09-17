package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAttemptSourceReplyCarriesExactTargetReceipt(t *testing.T) {
	changeID := strings.Repeat("a", 32)
	receipt := RetainedChangeHandoff{
		ChangeID: changeID, BaseCommit: strings.Repeat("b", 40), TaskID: strings.Repeat("c", 32),
		TaskWorkRevision: 3, ChangeRevision: 5,
		SourcePath: "/private/runtime/retained-source/" + changeID,
	}
	reply, err := NewAttemptSourceReply(receipt)
	if err != nil {
		t.Fatalf("valid source receipt rejected: %v", err)
	}
	if reply.attemptSource != receipt {
		t.Fatalf("source receipt changed: %+v", reply.attemptSource)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || !strings.Contains(string(encoded), `"task_work_revision":3`) || !strings.Contains(string(encoded), `"source_path"`) {
		t.Fatalf("source receipt JSON = %s, %v", encoded, err)
	}
	for _, badPath := range []string{
		"/private/runtime/retained-source/../other/" + changeID,
		"/private/factory/changes/" + changeID,
	} {
		invalid := receipt
		invalid.SourcePath = badPath
		if _, err := NewAttemptSourceReply(invalid); err == nil {
			t.Fatalf("unsafe source path accepted: %q", badPath)
		}
	}
}
