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
	var documented []string
	for _, line := range strings.Split(usage, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "factoryctl content ") {
			line = strings.Replace(line, "factoryctl content ", "factoryctl attempt content ", 1)
			line = strings.Replace(line, "|--body-file PATH", "", 1)
		}
		if strings.HasPrefix(line, "factoryctl outcome ") {
			line = strings.Replace(line, "factoryctl outcome ", "factoryctl attempt outcome ", 1)
			line = strings.Replace(line, "--document JSON|--document-file PATH", "--document JSON", 1)
		}
		if strings.HasPrefix(line, "factoryctl attempt ") || strings.HasPrefix(line, "factoryctl overseer ") {
			documented = append(documented, line)
		}
	}
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
			response["result"] = map[string]any{"tools": []any{map[string]any{
				// alwaysLoad keeps Claude Code from deferring this one tool behind ToolSearch.
				"name": "factory", "_meta": map[string]any{"anthropic/alwaysLoad": true}, "description": "Run an attempt-scoped factoryctl command. Pass argv without factoryctl; no shell expansion. Commands:\n" + strings.Join(documented, "\n"),
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
			exit, diagnosed := exitUsage, false
			if json.Unmarshal(request.Params, &params) == nil && params.Name == "factory" && len(params.Arguments.Argv) >= 2 && len(params.Arguments.Argv) <= 64 && (params.Arguments.Argv[0] == "attempt" || params.Arguments.Argv[0] == "overseer") {
				argv := params.Arguments.Argv
				command, help, ok := parse(argv)
				if ok && !help && (command.attemptRead || allowedAttemptMCPCommand(command.kind)) && !(command.kind >= commandContentCreate && command.kind <= commandContentAttachments && command.bodyFile != "") && !(command.kind >= commandOutcomeWrite && command.kind <= commandOutcomeList && command.documentFile != "") {
					exit = run(ctx, argv, getenv, &stdout, &stderr)
				} else if !ok && argv[1] != "turn-complete" {
					// A malformed documented route keeps the CLI's usage line, unless a
					// file flag sits where its parser reads flags: content's flag package
					// (-name, --name=value, up to "--", "-" or a positional), outcome's pairs.
					fileBacked := false
					for i := 3; i < len(argv) && (argv[1] == "outcome" || argv[1] == "content" && len(argv[i]) > 1 && argv[i][0] == '-' && argv[i] != "--"); i += 2 {
						name, inline := argv[i], false
						if argv[1] == "content" {
							name, _, inline = strings.Cut("--"+strings.TrimPrefix(strings.TrimPrefix(name, "-"), "-"), "=")
						}
						fileBacked = fileBacked || name == map[string]string{"content": "--body-file", "outcome": "--document-file"}[argv[1]]
						if inline {
							i--
						}
					}
					// The route the CLI names must be documented; the tool documents an
					// attempt read as a "| route" alternative.
					var match bytes.Buffer
					usageFailureIn(strings.ReplaceAll(strings.Join(documented, "\n"), " | ", "\nfactoryctl attempt "), &match, argv)
					usageFailure(&stderr, argv)
					head, _, found := strings.Cut(stderr.String(), ": invalid arguments\n")
					if diagnosed = !fileBacked && found && strings.HasPrefix(match.String(), head+": invalid arguments\n"); !diagnosed {
						stderr.Reset()
					}
				}
			}
			if exit == exitUsage && !diagnosed {
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

func allowedAttemptMCPCommand(kind commandKind) bool {
	switch kind {
	case commandSucceed, commandBlock, commandFail, commandRequestHuman,
		commandPeerStatus, commandPeerAsk, commandPeerAnswer, commandSendBack,
		commandAttemptTask, commandAttemptSource, commandTerminalObserve, commandOverseerStatus, commandOverseerTaskAdd,
		commandOverseerTaskUpdate, commandOverseerTaskSendBack,
		commandOverseerAgentUpdate, commandOverseerStopWorker,
		commandOverseerReplaceWorker, commandOverseerMessageWorker,
		commandOverseerInterruptWorker, commandOverseerReplyHuman:
		return true
	case commandContentCreate, commandContentRevise, commandContentDeprecate,
		commandContentList, commandContentRead, commandContentBody,
		commandContentAttach, commandContentAttachments:
		return true
	case commandOutcomeWrite, commandOutcomeRead, commandOutcomeList:
		return true
	}
	return false
}
