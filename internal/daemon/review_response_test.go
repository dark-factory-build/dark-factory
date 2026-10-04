//go:build darwin || linux

package daemon

import (
	"encoding/json"
	"testing"
)

func TestReviewResponseStructuredContentValidatesEnvelope(t *testing.T) {
	request := maintainerRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`)}
	response, err := reviewResponseStructuredContent(request, json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"isError":false,"structuredContent":{"head_sha":"abc"}}}`))
	if err != nil || string(response) != `{"head_sha":"abc"}` {
		t.Fatalf("successful response = %s, error = %v", response, err)
	}

	for _, test := range []struct {
		name     string
		response string
	}{
		{name: "tool rejection", response: `{"jsonrpc":"2.0","id":1,"result":{"isError":true,"structuredContent":{}}}`},
		{name: "top level error", response: `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"refused"}}`},
		{name: "wrong id", response: `{"jsonrpc":"2.0","id":2,"result":{"isError":false}}`},
		{name: "wrong version", response: `{"jsonrpc":"1.0","id":1,"result":{"isError":false}}`},
		{name: "missing result", response: `{"jsonrpc":"2.0","id":1}`},
		{name: "null result", response: `{"jsonrpc":"2.0","id":1,"result":null}`},
		{name: "malformed JSON", response: `{"jsonrpc":"2.0","id":1,"result":{"isError":false}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := reviewResponseStructuredContent(request, json.RawMessage(test.response)); err == nil {
				t.Fatal("invalid review response was accepted")
			}
		})
	}
}
