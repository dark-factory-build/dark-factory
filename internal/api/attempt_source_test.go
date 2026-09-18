package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAttemptSourceReplyCarriesExactTargetReceipt(t *testing.T) {
	changeID := strings.Repeat("a", 32)
	receipt := RetainedChangeHandoff{
		ChangeID: changeID, BaseCommit: strings.Repeat("b", 40), HeadCommit: strings.Repeat("d", 40), Branch: "factory/" + changeID[:12], TaskID: strings.Repeat("c", 32),
		TaskWorkRevision: 3, ChangeRevision: 5,
		SourcePath: "/private/factory/changes/" + changeID, GitDirectory: "/private/project/.git", Dirty: true,
	}
	reply, err := NewAttemptSourceReply(receipt)
	if err != nil {
		t.Fatalf("valid source receipt rejected: %v", err)
	}
	if reply.attemptSource != receipt {
		t.Fatalf("source receipt changed: %+v", reply.attemptSource)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || !strings.Contains(string(encoded), `"task_work_revision":3`) || !strings.Contains(string(encoded), `"source_path"`) || !strings.Contains(string(encoded), `"head_commit"`) || !strings.Contains(string(encoded), `"git_directory"`) || !strings.Contains(string(encoded), `"dirty":true`) {
		t.Fatalf("source receipt JSON = %s, %v", encoded, err)
	}
	for name, mutate := range map[string]func(*RetainedChangeHandoff){
		"escaping source path":  func(h *RetainedChangeHandoff) { h.SourcePath = "/private/factory/changes/../other/" + changeID },
		"another Change's path": func(h *RetainedChangeHandoff) { h.SourcePath = "/private/factory/changes/" + strings.Repeat("e", 32) },
		"no source path":        func(h *RetainedChangeHandoff) { h.SourcePath = "" },
		"no git directory":      func(h *RetainedChangeHandoff) { h.GitDirectory = "" },
		"not a git directory":   func(h *RetainedChangeHandoff) { h.GitDirectory = "/private/project" },
		"no head":               func(h *RetainedChangeHandoff) { h.HeadCommit = "" },
		"short head":            func(h *RetainedChangeHandoff) { h.HeadCommit = strings.Repeat("d", 39) },
		"upper-case head":       func(h *RetainedChangeHandoff) { h.HeadCommit = strings.Repeat("D", 40) },
		"another branch":        func(h *RetainedChangeHandoff) { h.Branch = "factory/" + strings.Repeat("e", 12) },
		"no branch":             func(h *RetainedChangeHandoff) { h.Branch = "" },
	} {
		invalid := receipt
		mutate(&invalid)
		if _, err := NewAttemptSourceReply(invalid); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	// Overseer status carries the identities alone; the locations come only
	// from an explicit attempt source request.
	status := receipt
	status.SourcePath, status.GitDirectory, status.HeadCommit, status.Branch = "", "", "", ""
	if !validRetainedChangeHandoff(status) || validSourceHandoff(status) {
		t.Fatal("identity-only handoff validation")
	}
}
