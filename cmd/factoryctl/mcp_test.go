//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
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
	if len(lines) != 3 || !strings.Contains(lines[1], "attempt succeed") || !strings.Contains(lines[1], "attempt content create") || strings.Contains(lines[1], "factoryctl content create") || strings.Contains(lines[1], "project create") {
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
	for _, argv := range [][]string{{"status"}, {"dispatch", "off"}, {"service", "stop", "--home", "/private/tmp/factory"}, {"attempt", "mcp"}, {"attempt", "succeed", "--result-file", "/secret"}, {"attempt", "content", "create", "--project", "0123456789abcdef0123456789abcdef", "--kind", "procedure", "--title", "x", "--body-file", "/secret"}, {"content", "read", "--id", "0123456789abcdef0123456789abcdef", "--revision", "1"}, {"sh", "-c", "echo unsafe"}, {"--help"}} {
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

func TestAttemptMCPContentListUsesAttemptAuthority(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	t.Setenv("DARK_FACTORY_ATTEMPT_TOKEN_FILE", fixture.attemptPath)
	id := "0123456789abcdef0123456789abcdef"
	done := serveOne(fixture.listener, func(call api.Call) api.Reply {
		input, ok := call.ContentListInput()
		if !ok || input.ProjectID != id || call.Kind() != api.CallContentList {
			t.Errorf("unexpected content list call: %v", call)
		}
		return api.NewContentReply(api.ContentList{Items: []api.Content{}})
	})
	request := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"factory","arguments":{"argv":["attempt","content","list","--project","%s"]}}}`+"\n", id)
	var output bytes.Buffer
	if runAttemptMCP(context.Background(), strings.NewReader(request), &output, func(string) string { return fixture.socket }) != 0 {
		t.Fatal("MCP content list failed")
	}
	call := awaitServer(t, done)
	digest, ok := call.call.AttemptDigest()
	if call.err != nil || !ok || digest.Bytes() != sha256.Sum256(fixture.bearer[:]) {
		t.Fatalf("content list did not use attempt authentication: err=%v authenticated=%v", call.err, ok)
	}
	if strings.Contains(output.String(), `"isError":true`) {
		t.Fatalf("content list returned error: %s", output.String())
	}
}

func TestAttemptMCPCommandCatalogueParity(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	allowed := []struct {
		name string
		argv []string
	}{
		{"attempt task", []string{"attempt", "task"}},
		{"attempt terminal observe", []string{"attempt", "terminal", "observe", "--project", id, "--task", id, "--run", id}},
		{"attempt succeed", []string{"attempt", "succeed"}},
		{"attempt block", []string{"attempt", "block", "--detail", "blocked"}},
		{"attempt fail", []string{"attempt", "fail"}},
		{"attempt request-human", []string{"attempt", "request-human", "--idempotency-key", id, "--question", "question"}},
		{"attempt peer status", []string{"attempt", "peer", "status"}},
		{"attempt peer ask", []string{"attempt", "peer", "ask", "--task", id, "--idempotency-key", id, "--question", "question"}},
		{"attempt peer answer", []string{"attempt", "peer", "answer", "--question", id, "--revision", "1", "--idempotency-key", id, "--answer", "answer"}},
		{"attempt send-back", []string{"attempt", "send-back", "--task", id, "--note", "note"}},
		{"attempt content create", []string{"attempt", "content", "create", "--project", id, "--kind", "procedure", "--title", "title"}},
		{"attempt content revise", []string{"attempt", "content", "revise", "--project", id, "--id", id, "--revision", "1", "--kind", "procedure", "--title", "title"}},
		{"attempt content list", []string{"attempt", "content", "list", "--project", id}},
		{"attempt content read", []string{"attempt", "content", "read", "--id", id, "--revision", "1"}},
		{"attempt content body", []string{"attempt", "content", "body", "--id", id, "--revision", "1", "--offset", "0", "--limit", "1"}},
		{"attempt content evidence", []string{"attempt", "content", "evidence", "--project", id, "--id", id, "--revision", "1", "--tested-source", "check", "--result", "passed"}},
		{"attempt content evidence list", []string{"attempt", "content", "evidence-list", "--id", id, "--revision", "1"}},
		{"attempt content attach", []string{"attempt", "content", "attach", "--project", id, "--task", id, "--id", id, "--revision", "1"}},
		{"attempt content attachments", []string{"attempt", "content", "attachments", "--task", id, "--revision", "1"}},
		{"overseer status", []string{"overseer", "status"}},
		{"overseer task add", []string{"overseer", "task", "add", "--agent", id, "--title", "title"}},
		{"overseer task update", []string{"overseer", "task", "update", "--task", id, "--revision", "1", "--title", "title"}},
		{"overseer task send-back", []string{"overseer", "task", "send-back", "--task", id, "--note", "note"}},
		{"overseer agent pause", []string{"overseer", "agent", "pause", "--agent", id, "--revision", "1"}},
		{"overseer agent resume", []string{"overseer", "agent", "resume", "--agent", id, "--revision", "1"}},
		{"overseer agent archive", []string{"overseer", "agent", "archive", "--agent", id, "--revision", "1"}},
		{"overseer agent restore", []string{"overseer", "agent", "restore", "--agent", id, "--revision", "1"}},
		{"overseer worker stop", []string{"overseer", "worker", "stop", "--operation-id", id, "--task", id, "--task-revision", "1", "--run", id, "--run-revision", "1"}},
		{"overseer worker replace", []string{"overseer", "worker", "replace", "--operation-id", id, "--task", id, "--task-revision", "1", "--run", id, "--run-revision", "1", "--successor-task", id, "--successor-incarnation", id, "--instruction", "instruction"}},
		{"overseer worker message", []string{"overseer", "worker", "message", "--operation-id", id, "--task", id, "--task-revision", "1", "--run", id, "--run-revision", "1", "--message", "message"}},
		{"overseer worker interrupt", []string{"overseer", "worker", "interrupt", "--operation-id", id, "--task", id, "--task-revision", "1", "--run", id, "--run-revision", "1"}},
		{"overseer human reply", []string{"overseer", "human", "reply", "--operation-id", id, "--request", id, "--revision", "1", "--reply", "reply"}},
	}
	for _, test := range allowed {
		t.Run(test.name, func(t *testing.T) {
			command, help, ok := parse(test.argv)
			if !ok || help || !allowedAttemptMCPCommand(command.kind) {
				t.Fatalf("advertised command is unsupported: %v", test.argv)
			}
		})
	}
}

func TestAttemptMCPCommandAllowlistExcludesOperatorCommands(t *testing.T) {
	for _, kind := range []commandKind{
		commandWebStatus, commandWebListClients, commandWebRevoke, commandRemoteStatus,
		commandInit, commandDoctor, commandServiceStatus, commandServiceInstall,
		commandServiceStart, commandServiceStop, commandServiceUninstall,
		commandProjectCreate, commandProjectLimits, commandAgentCreate, commandAgentIdlePolicy, commandTaskAdd,
		commandTaskSendBack, commandDispatch, commandCapacity, commandStatus,
		commandKind(0), commandKind(255),
	} {
		if allowedAttemptMCPCommand(kind) {
			t.Fatalf("operator or unknown command %d was exposed", kind)
		}
	}
}

// The command kind values are intentionally opaque: inserting or reordering an
// enum must not alter MCP authority. Keep this structural canary next to the
// catalogue so an ordinal range cannot return unnoticed.
func TestAttemptMCPCommandAllowlistDoesNotDependOnOrdinals(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate mcp test source")
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(filepath.Dir(testFile), "mcp.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var allowlist *ast.FuncDecl
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == "allowedAttemptMCPCommand" {
			allowlist = function
			break
		}
	}
	if allowlist == nil {
		t.Fatal("allowlist function missing")
	}
	usesOrdinalComparison := false
	ast.Inspect(allowlist.Body, func(node ast.Node) bool {
		comparison, ok := node.(*ast.BinaryExpr)
		if !ok || (comparison.Op != token.LSS && comparison.Op != token.LEQ && comparison.Op != token.GTR && comparison.Op != token.GEQ) {
			return true
		}
		for _, operand := range []ast.Expr{comparison.X, comparison.Y} {
			if identifier, ok := operand.(*ast.Ident); ok && identifier.Name == "kind" {
				usesOrdinalComparison = true
			}
		}
		return true
	})
	if usesOrdinalComparison {
		t.Fatal("MCP allowlist must name commands explicitly, not compare command kind ordinals")
	}
}

func TestTerminalObservationCLIAndMCPUseReadableSafeExactOutput(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	argv := []string{"attempt", "terminal", "observe", "--project", id, "--task", id, "--run", id, "--cursor", "10000000"}
	for _, mode := range []string{"cli", "mcp"} {
		for _, refused := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/refused=%v", mode, refused), func(t *testing.T) {
				fixture := newAPIFixture(t)
				defer fixture.close(t)
				t.Setenv("DARK_FACTORY_ATTEMPT_TOKEN_FILE", fixture.attemptPath)
				payload := []byte("building x.go\n\x1b[2J\u009b")
				done := serveOne(fixture.listener, func(call api.Call) api.Reply {
					input, ok := call.TerminalObserveInput()
					if !ok || input.ProjectID != id || input.TaskID != id || input.RunID != id || input.Cursor != 10000000 {
						t.Errorf("request identity changed: %+v", input)
					}
					if refused {
						reply, _ := api.NewErrorReply(api.RemoteForbidden)
						return reply
					}
					reply, err := api.NewTerminalObservationReply(api.TerminalObservation{ProjectID: id, TaskID: id, RunID: id, Cursor: input.Cursor, NextCursor: input.Cursor + uint64(len(payload)), Head: input.Cursor + uint64(len(payload)), Source: "stored", Payload: payload})
					if err != nil {
						t.Error(err)
					}
					return reply
				})
				var output, stderr bytes.Buffer
				if mode == "cli" {
					exit := run(context.Background(), argv, func(string) string { return fixture.socket }, &output, &stderr)
					if (exit != 0) != refused {
						t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
					}
				} else {
					request, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "factory", "arguments": map[string]any{"argv": argv}}})
					if runAttemptMCP(context.Background(), bytes.NewReader(request), &output, func(string) string { return fixture.socket }) != 0 {
						t.Fatal("MCP failed")
					}
					var response struct {
						Result struct {
							IsError bool
							Content []struct{ Text string }
						}
					}
					if err := json.Unmarshal(output.Bytes(), &response); err != nil || response.Result.IsError != refused || len(response.Result.Content) != 1 {
						t.Fatalf("MCP result=%s err=%v", output.String(), err)
					}
					output.Reset()
					output.WriteString(response.Result.Content[0].Text)
				}
				result := awaitServer(t, done)
				digest, ok := result.call.AttemptDigest()
				if result.err != nil || !ok || digest.Bytes() != sha256.Sum256(fixture.bearer[:]) {
					t.Fatal("observer credential changed")
				}
				if !refused {
					var displayed struct {
						Payload    string
						NextCursor uint64 `json:"next_cursor"`
					}
					if err := json.Unmarshal(output.Bytes(), &displayed); err != nil || displayed.Payload != string(payload) || displayed.NextCursor != 10000000+uint64(len(payload)) {
						t.Fatalf("display=%s err=%v", output.String(), err)
					}
					if strings.ContainsAny(output.String(), "\x1b\u009b") {
						t.Fatalf("terminal controls were emitted: %q", output.String())
					}
				}
			})
		}
	}
}
