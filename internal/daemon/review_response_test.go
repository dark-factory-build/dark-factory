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

// The broker's typed UNPROCESSABLE refusal (control-plane mcp.rs
// operation_error) is terminal for the head; a rate limit or any other
// failure is not (#1510).
func TestOnlyAnUnprocessableEnqueueRefusalIsTerminal(t *testing.T) {
	for text, terminal := range map[string]bool{
		"refused: The request was refused: rejected before execution as UNPROCESSABLE.":                                                        true,
		"refused: The request was refused: rejected before execution as UNPROCESSABLE: Pull request head no longer matches the expected head.": true,
		"refused: The request was refused: rejected before execution as UNPROCESSABLE+RATE_LIMITED.":                                           false,
		"refused: The request was refused: rejected before execution as RATE_LIMITED.":                                                         false,
		"refused: The request was refused: the queue read found no merge queue on the base branch.":                                            false,
		"indeterminate: The operation outcome is indeterminate and was not repeated.":                                                          false,
	} {
		response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"isError": true, "content": []any{map[string]string{"type": "text", "text": text}}}})
		_, err := reviewResponseStructuredContent(maintainerRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`)}, response)
		// GitHub's own reason rides into the text the overseer is shown (#1614).
		if err == nil || errors.Is(err, review.ErrRefused) != terminal || !strings.Contains(err.Error(), text) {
			t.Errorf("%q: err=%v", text, err)
		}
	}
}

func TestCodeownersEnqueueRefusalIsOwnerApproval(t *testing.T) {
	for _, why := range []string{"required CODEOWNERS approval is missing.", "rejected before execution as UNPROCESSABLE: Waiting on code owner review from baziyer."} {
		if err := maintainerRejection("refused: The request was refused: " + why); !errors.Is(err, review.ErrOwnerApproval) {
			t.Fatalf("err=%v", err)
		}
	}
}

func TestCodeOwnedNamesOnlyOwnedChangedPaths(t *testing.T) {
	codeowners := "# merge authority\n/.github/workflows/ @o\n/docs/development/WORKFLOW.md @o\n*.rs @o\n/control-plane/src/free.rs\n"
	changed := []string{".github/workflows/ci.yml", "docs/development/WORKFLOW.md", "docs/WORKFLOW.md", "internal/review/coordinator.go", "control-plane/src/lib.rs", "control-plane/src/free.rs", ".github/workflows", ""}
	if got := strings.Join(codeOwned(codeowners, changed), ","); got != ".github/workflows/ci.yml,docs/development/WORKFLOW.md,control-plane/src/lib.rs" {
		t.Fatalf("owned = %s", got)
	}
}

// Only a rejection the same request meets again is permanent (#1558): input
// the broker found invalid, or a tree GitHub cannot return whole. Publication
// retries every other one, and so does the merge stage.
func TestOnlyARejectionTheSameRequestMeetsAgainIsPermanent(t *testing.T) {
	for text, permanent := range map[string]bool{
		"invalid_input: Operation input is invalid.":                                                   true,
		"refused: The request was refused: the commit's tree is too large for GitHub to return whole.": true,
		"refused: The request was refused: rejected before execution as UNPROCESSABLE.":                false,
		"refused: The request was refused: rejected before execution as FORBIDDEN.":                    false,
		"conflict: The exact-head, operation, or observed-object binding did not match.":               false,
		"unavailable: Maintainer authority is unavailable.":                                            false,
	} {
		response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"isError": true, "content": []any{map[string]string{"type": "text", "text": text}}}})
		_, err := reviewResponseStructuredContent(maintainerRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`)}, response)
		if err == nil || errors.Is(fmt.Errorf("enqueue: %w", err), review.ErrPermanent) != permanent || publicationFailureRetryable(err) == permanent {
			t.Errorf("%q: err=%v", text, err)
		}
	}
}
