package daemon

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/review"
)

func TestMaintainerRejectionCarriesTheBrokerReason(t *testing.T) {
	response := json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"isError":true,"content":[{"type":"text","text":"pull request is not mergeable"}]}}`)
	_, err := reviewResponseStructuredContent(maintainerRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`)}, response)
	if err == nil || !strings.Contains(err.Error(), "rejected operation: pull request is not mergeable") {
		t.Fatalf("err=%v", err)
	}
}

// The broker's typed UNPROCESSABLE refusal (control-plane mcp.rs
// operation_error) is terminal for the head; a rate limit or any other
// failure is not (#1510).
func TestOnlyAnUnprocessableEnqueueRefusalIsTerminal(t *testing.T) {
	for text, terminal := range map[string]bool{
		"refused: The request was refused: rejected before execution as UNPROCESSABLE.":              true,
		"refused: The request was refused: rejected before execution as UNPROCESSABLE+RATE_LIMITED.": false,
		"refused: The request was refused: rejected before execution as RATE_LIMITED.":               false,
		"refused: The request was refused: the queue read found no merge queue on the base branch.":  false,
		"indeterminate: The operation outcome is indeterminate and was not repeated.":                false,
	} {
		response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"isError": true, "content": []any{map[string]string{"type": "text", "text": text}}}})
		_, err := reviewResponseStructuredContent(maintainerRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`)}, response)
		if err = enqueueRefused(err); err == nil || errors.Is(err, review.ErrRefused) != terminal {
			t.Errorf("%q: err=%v", text, err)
		}
	}
	if enqueueRefused(nil) != nil {
		t.Fatal("success became a refusal")
	}
}

func TestCodeownersEnqueueRefusalIsOwnerApproval(t *testing.T) {
	err := errors.New("review: Maintainer rejected operation: refused: The request was refused: required CODEOWNERS approval is missing.")
	if !errors.Is(enqueueRefused(err), review.ErrOwnerApproval) {
		t.Fatalf("err=%v", enqueueRefused(err))
	}
}
