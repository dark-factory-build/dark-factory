package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
)

type intakeReviewContext struct {
	Home       string          `json:"home"`
	Repository string          `json:"repository"`
	Request    api.IntakeInput `json:"request"`
}

// The model sees three exact-review tools. This adapter alone locates the
// ordinary private operator credential; no credential is exported to the model.
func runIntakeReviewMCP(ctx context.Context, path string, input io.Reader, output io.Writer, getenv func(string) string) int {
	if getenv("DARK_FACTORY_ATTEMPT_TOKEN_FILE") != "" {
		return exitFailure
	}
	data, err := privateControllerFile(path, 256<<10, true)
	var bound intakeReviewContext
	if err != nil || json.Unmarshal(data, &bound) != nil || !filepath.IsAbs(bound.Home) || filepath.Clean(bound.Home) != bound.Home || bound.Repository == "" || bound.Request.Review == nil {
		return exitFailure
	}
	fixed := *bound.Request.Review
	probe := bound.Request
	copied := fixed
	copied.Body = "validate context"
	copied.Event = "ALLOW"
	copied.Tool = "submit_pull_request_review"
	probe.Review = &copied
	if !api.ValidIntakeInput(probe) {
		return exitFailure
	}
	client, err := api.NewOperatorClient(filepath.Join(bound.Home, "runtimes", "factory.sock"), filepath.Join(bound.Home, "operator.token"))
	if err != nil {
		return exitFailure
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 256<<10)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		var rpc struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  struct {
				Name      string                     `json:"name"`
				Arguments map[string]json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &rpc) != nil || rpc.JSONRPC != "2.0" {
			return exitFailure
		}
		if len(rpc.ID) == 0 {
			continue
		}
		response := map[string]any{"jsonrpc": "2.0", "id": rpc.ID}
		switch rpc.Method {
		case "initialize":
			response["result"] = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "dark-factory-review", "version": "1"}}
		case "ping":
			response["result"] = map[string]any{}
		case "tools/list":
			response["result"] = intakeReviewTools()
		case "tools/call":
			review, ok := boundReviewCall(rpc.Params.Name, rpc.Params.Arguments, bound.Repository, fixed)
			if !ok {
				response["error"] = map[string]any{"code": -32602, "message": "Review tool must match its fixed repository, PR, head and operation"}
				break
			}
			request := bound.Request
			request.Review = &review
			if review.Tool != "submit_pull_request_review" {
				request.IssueNumber = 0
			}
			callContext, cancel := context.WithTimeout(ctx, 120*time.Second)
			result, err := client.Intake(callContext, request)
			cancel()
			if err != nil || result.State != "ok" || result.Review == nil || json.Unmarshal([]byte(result.Review.Response), &response) != nil {
				response = map[string]any{"jsonrpc": "2.0", "error": map[string]any{"code": -32000, "message": "Customer review authority unavailable; reconnect or refresh access"}}
			}
			response["id"] = rpc.ID
		default:
			response["error"] = map[string]any{"code": -32601, "message": "Method not found"}
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

func boundReviewCall(tool string, args map[string]json.RawMessage, repository string, fixed api.IntakeReviewInput) (api.IntakeReviewInput, bool) {
	var name string
	if json.Unmarshal(args["repository"], &name) != nil || !strings.EqualFold(name, repository) {
		return api.IntakeReviewInput{}, false
	}
	allowed := map[string]bool{"repository": true}
	value := api.IntakeReviewInput{Tool: tool}
	switch tool {
	case "maintainer_status":
	case "observe_operation":
		allowed["operation_id"] = true
		value.OperationID = fixed.OperationID
	case "submit_pull_request_review":
		for _, key := range []string{"operation_id", "pull_number", "head_sha", "corrects_review_operation_id", "event", "body"} {
			allowed[key] = true
		}
		value = fixed
		value.Tool = tool
	default:
		return value, false
	}
	for key := range args {
		if !allowed[key] {
			return value, false
		}
	}
	if tool != "maintainer_status" {
		var id string
		if json.Unmarshal(args["operation_id"], &id) != nil || id != fixed.OperationID {
			return value, false
		}
	}
	if tool == "submit_pull_request_review" {
		var head, corrects string
		var pull uint64
		if json.Unmarshal(args["head_sha"], &head) != nil || head != fixed.HeadSHA || json.Unmarshal(args["pull_number"], &pull) != nil || pull != fixed.PullNumber {
			return value, false
		}
		if raw, exists := args["corrects_review_operation_id"]; exists {
			if json.Unmarshal(raw, &corrects) != nil {
				return value, false
			}
		}
		if corrects != fixed.CorrectsReviewOperationID || json.Unmarshal(args["body"], &value.Body) != nil || json.Unmarshal(args["event"], &value.Event) != nil {
			return value, false
		}
	}
	return value, true
}

func intakeReviewTools() map[string]any {
	tools := []any{}
	for _, name := range []string{"maintainer_status", "observe_operation", "submit_pull_request_review"} {
		properties := map[string]any{"repository": map[string]any{"type": "string"}}
		required := []string{"repository"}
		if name != "maintainer_status" {
			properties["operation_id"] = map[string]any{"type": "string"}
			required = append(required, "operation_id")
		}
		if name == "submit_pull_request_review" {
			for _, key := range []string{"head_sha", "event", "body", "corrects_review_operation_id"} {
				properties[key] = map[string]any{"type": "string"}
			}
			properties["pull_number"] = map[string]any{"type": "integer"}
			required = append(required, "pull_number", "head_sha", "event", "body")
		}
		tools = append(tools, map[string]any{"name": name, "description": "Exact host-bound independent review", "inputSchema": map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}})
	}
	return map[string]any{"tools": tools}
}
