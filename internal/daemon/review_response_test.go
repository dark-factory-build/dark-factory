package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
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

// Only a rejection the same request meets again is permanent (#1558): input
// the broker found invalid, or a tree GitHub cannot return whole. A refusal
// whose precondition can come to hold, UNPROCESSABLE included (#1531: a
// CODEOWNERS approval arrived later), is not.
func TestOnlyARejectionTheSameRequestMeetsAgainIsPermanent(t *testing.T) {
	for text, permanent := range map[string]bool{
		"invalid_input: Operation input is invalid.":                                                   true,
		"refused: The request was refused: the commit's tree is too large for GitHub to return whole.": true,
		"refused: The request was refused: rejected before execution as UNPROCESSABLE.":                false,
		"refused: The request was refused: rejected before execution as FORBIDDEN.":                    false,
		"refused: The request was refused: rejected before execution as UNPROCESSABLE+RATE_LIMITED.":   false,
		"refused: The request was refused: the queue read found no merge queue on the base branch.":    false,
		"conflict: The exact-head, operation, or observed-object binding did not match.":               false,
		"indeterminate: The operation outcome is indeterminate and was not repeated.":                  false,
		"unavailable: Maintainer authority is unavailable.":                                            false,
	} {
		response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"isError": true, "content": []any{map[string]string{"type": "text", "text": text}}}})
		_, err := reviewResponseStructuredContent(maintainerRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`)}, response)
		if err == nil || errors.Is(fmt.Errorf("enqueue: %w", err), review.ErrPermanent) != permanent || publicationFailureRetryable(err) == permanent {
			t.Errorf("%q: err=%v", text, err)
		}
	}
}
