//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/api"
)

func TestInstalledMaintainerBridgeUsesExactAttempt(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	t.Setenv("DARK_FACTORY_ATTEMPT_TOKEN_FILE", fixture.attemptPath)
	request := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"observe_operation","arguments":{"repository":"team/repo","operation_id":"original-uuid"}}}`
	reply := json.RawMessage(`{"jsonrpc":"2.0","id":7,"result":{"state":"completed"}}`)
	done := serveOne(fixture.listener, func(call api.Call) api.Reply {
		value, ok := call.MaintainerInput()
		if !ok || string(value.Request) != request {
			t.Error("bridge changed original receipt request")
		}
		return api.NewContentReply(api.MaintainerResult{State: "ok", Response: reply})
	})
	var output bytes.Buffer
	if runMaintainerMCP(context.Background(), strings.NewReader(request+"\n"), &output, func(string) string { return fixture.socket }) != 0 {
		t.Fatal("bridge failed")
	}
	call := awaitServer(t, done)
	digest, ok := call.call.AttemptDigest()
	if call.err != nil || !ok || digest.Bytes() != sha256.Sum256(fixture.bearer[:]) {
		t.Fatal("bridge changed attempt authority")
	}
	if strings.TrimSpace(output.String()) != string(reply) {
		t.Fatalf("bridge response=%s", output.String())
	}
}
