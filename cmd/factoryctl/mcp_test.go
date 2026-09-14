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

func TestAttemptMCPUsesExistingAuthAndExactTask(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	t.Setenv("DARK_FACTORY_ATTEMPT_TOKEN_FILE", fixture.attemptPath)
	task := "exact task\nquoted \"text\"\u009b"
	done := serveOne(fixture.listener, func(api.Call) api.Reply {
		reply, err := api.NewAttemptTaskReply(api.AttemptTask{Task: task})
		if err != nil {
			t.Error(err)
		}
		return reply
	})
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize"}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"factory","arguments":{"argv":["attempt","task"]}}}
`
	var output bytes.Buffer
	if runAttemptMCP(context.Background(), strings.NewReader(input), &output, func(string) string { return fixture.socket }) != 0 {
		t.Fatal("MCP failed")
	}
	call := awaitServer(t, done)
	digest, ok := call.call.AttemptDigest()
	if call.err != nil || call.call.Kind() != api.CallAttemptTask || !ok || digest.Bytes() != sha256.Sum256(fixture.bearer[:]) {
		t.Fatal("MCP changed authentication or task identity")
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[1], "attempt succeed") || strings.Contains(lines[1], "project create") {
		t.Fatalf("unexpected discovery: %s", output.String())
	}
	var response struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(lines[2]), &response) != nil || response.Result.IsError || len(response.Result.Content) != 1 {
		t.Fatalf("unexpected call response: %s", lines[2])
	}
	var decoded api.AttemptTask
	if json.Unmarshal([]byte(response.Result.Content[0].Text), &decoded) != nil || decoded.Task != task {
		t.Fatal("task text was changed")
	}
}

func TestAttemptMCPRefusesOperatorShellRecursiveAndInvalidCommands(t *testing.T) {
	for _, argv := range [][]string{{"status"}, {"dispatch", "off"}, {"service", "stop", "--home", "/private/tmp/factory"}, {"attempt", "mcp"}, {"attempt", "succeed", "--result-file", "/secret"}, {"sh", "-c", "echo unsafe"}, {"--help"}} {
		request, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "factory", "arguments": map[string]any{"argv": argv}}})
		var output bytes.Buffer
		if runAttemptMCP(context.Background(), bytes.NewReader(request), &output, func(string) string { t.Fatal("forbidden command reached environment"); return "" }) != 0 || !strings.Contains(output.String(), `"isError":true`) {
			t.Fatalf("accepted %q: %s", argv, output.String())
		}
	}
	var output bytes.Buffer
	if runAttemptMCP(context.Background(), strings.NewReader(strings.Repeat("x", 1<<20)), &output, func(string) string { return "" }) != exitFailure || output.Len() != 0 {
		t.Fatal("oversized input accepted")
	}
}
