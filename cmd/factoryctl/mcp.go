package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
)

// The provider's stdio tool runs outside its command sandbox. It exposes only
// existing attempt-authenticated commands, never a shell or operator command.
// The daemon still checks the exact attempt and overseer role on every call.
func runAttemptMCP(ctx context.Context, input io.Reader, output io.Writer, getenv func(string) string) int {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		var request struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || request.JSONRPC != "2.0" {
			return exitFailure
		}
		if len(request.ID) == 0 {
			continue
		}
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		switch request.Method {
		case "initialize":
			response["result"] = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "factory-attempt", "version": "1"}}
		case "ping":
			response["result"] = map[string]any{}
		case "tools/list":
			var help []string
			for _, line := range strings.Split(usage, "\n") {
				if strings.HasPrefix(line, "  factoryctl attempt ") || strings.HasPrefix(line, "  factoryctl overseer ") {
					help = append(help, strings.TrimSpace(line))
				}
			}
			response["result"] = map[string]any{"tools": []any{map[string]any{
				"name": "factory", "description": "Run an attempt-scoped factoryctl command. Pass argv without factoryctl; no shell expansion. Commands:\n" + strings.Join(help, "\n"),
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"argv": map[string]any{"type": "array", "items": map[string]string{"type": "string"}, "minItems": 2, "maxItems": 64}}, "required": []string{"argv"}, "additionalProperties": false},
			}}}
		case "tools/call":
			var params struct {
				Name      string `json:"name"`
				Arguments struct {
					Argv []string `json:"argv"`
				} `json:"arguments"`
			}
			var stdout, stderr bytes.Buffer
			exit := exitUsage
			if json.Unmarshal(request.Params, &params) == nil && params.Name == "factory" && len(params.Arguments.Argv) >= 2 && len(params.Arguments.Argv) <= 64 {
				command, help, ok := parse(params.Arguments.Argv)
				if ok && !help && (command.kind >= commandSucceed && command.kind <= commandAttemptTask || command.kind >= commandOverseerStatus && command.kind <= commandOverseerReplyHuman) {
					exit = run(ctx, params.Arguments.Argv, getenv, &stdout, &stderr)
				}
			}
			if exit == exitUsage {
				stderr.WriteString("unsupported attempt command; use the tool's documented argv")
			}
			response["result"] = map[string]any{"content": []any{map[string]string{"type": "text", "text": stdout.String() + stderr.String()}}, "isError": exit != 0}
		default:
			response["error"] = map[string]any{"code": -32601, "message": "method not found"}
		}
		if encoder.Encode(response) != nil {
			return exitFailure
		}
	}
	if scanner.Err() != nil {
		return exitFailure
	}
	return 0
}
