package daemon

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

func TestAttemptTaskReturnsExactProviderLimitWithCausalContext(t *testing.T) {
	condition := kernel.ContinuationConditionID{}
	copy(condition[:], bytes.Repeat([]byte{0x42}, len(condition)))
	revision, err := kernel.NewRevision(7)
	if err != nil {
		t.Fatal(err)
	}
	context := kernel.ContinuationContext{
		ConditionKind:     kernel.ConditionHumanRequest,
		ConditionID:       condition,
		ConditionRevision: revision,
		ContextDigest:     [kernel.DigestBytes]byte{0x24},
		ResolutionDetail:  strings.Repeat("r", kernel.MaxHumanRequestReplyBytes),
	}
	for _, test := range []struct {
		name     string
		provider kernel.Provider
		taskSize int
	}{
		{name: "codex", provider: kernel.ProviderCodex, taskSize: runner.MaxCodexTaskBytes},
		{name: "claude", provider: kernel.ProviderClaudeCode, taskSize: runner.MaxProviderTaskBytes},
	} {
		t.Run(test.name, func(t *testing.T) {
			task := bytes.Repeat([]byte{'x'}, test.taskSize)
			framed, err := attemptTaskWithContinuationContext(test.provider, task, []kernel.ContinuationContext{context})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.HasPrefix(framed, task) || !bytes.Contains(framed, []byte("condition=human_request")) || !bytes.Contains(framed, []byte("resolution="+context.ResolutionDetail)) {
				t.Fatalf("attempt task lost causal envelope: len=%d", len(framed))
			}
			if len(framed) > kernel.MaxContinuationTaskBytes {
				t.Fatalf("attempt task exceeds API bound: %d", len(framed))
			}
			if _, err := api.NewAttemptTaskReply(api.AttemptTask{Task: string(framed)}); err != nil {
				t.Fatalf("attempt-task API rejected bounded causal envelope: %v", err)
			}
		})
	}
}
