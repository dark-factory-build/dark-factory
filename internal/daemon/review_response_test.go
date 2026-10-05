package daemon

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMaintainerRejectionCarriesTheBrokerReason(t *testing.T) {
	response := json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"isError":true,"content":[{"type":"text","text":"pull request is not mergeable"}]}}`)
	_, err := reviewResponseStructuredContent(maintainerRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`)}, response)
	if err == nil || !strings.Contains(err.Error(), "rejected operation: pull request is not mergeable") {
		t.Fatalf("err=%v", err)
	}
}
