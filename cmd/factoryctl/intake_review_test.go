//go:build darwin

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/api"
)

func TestIntakeReviewAdapterRestrictsExactPublication(t *testing.T) {
	fixed := api.IntakeReviewInput{Tool: "submit_pull_request_review", PullNumber: 7, HeadSHA: strings.Repeat("a", 40), OperationID: "12345678-1234-1234-1234-123456789abc"}
	args := map[string]json.RawMessage{"repository": json.RawMessage(`"team/target"`), "pull_number": json.RawMessage(`7`), "head_sha": json.RawMessage(`"` + fixed.HeadSHA + `"`), "operation_id": json.RawMessage(`"` + fixed.OperationID + `"`), "event": json.RawMessage(`"ALLOW"`), "body": json.RawMessage(`"reviewed"`)}
	if _, ok := boundReviewCall(fixed.Tool, args, "team/target", fixed); !ok {
		t.Fatal("exact review denied")
	}
	for key, bad := range map[string]string{"repository": `"team/other"`, "pull_number": `8`, "head_sha": `"` + strings.Repeat("b", 40) + `"`, "operation_id": `"87654321-1234-1234-1234-123456789abc"`, "Repository": `"team/target"`} {
		old, had := args[key]
		args[key] = json.RawMessage(bad)
		if _, ok := boundReviewCall(fixed.Tool, args, "team/target", fixed); ok {
			t.Fatalf("wrong %s accepted", key)
		}
		if had {
			args[key] = old
		} else {
			delete(args, key)
		}
	}
	if _, ok := boundReviewCall("enqueue_pull_request", args, "team/target", fixed); ok {
		t.Fatal("reviewer acquired merge authority")
	}
}

func TestIntakeReviewAdapterSocketPreservesOpaqueMCP(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	fixed := api.IntakeReviewInput{Tool: "submit_pull_request_review", PullNumber: 7, HeadSHA: strings.Repeat("a", 40), OperationID: "12345678-1234-1234-1234-123456789abc"}
	bound := intakeReviewContext{Home: filepath.Join(fixture.directory, "home"), Repository: "team/target", Request: api.IntakeInput{Action: "review", SourceID: strings.Repeat("ab", 16), ProjectID: strings.Repeat("bc", 16), IssueNumber: 9, Configuration: &api.IntakeConfiguration{Repository: "team/source", TargetRepositoryID: strings.Repeat("cd", 16)}, Legacy: &api.LegacyIntakeInput{PlanHash: strings.Repeat("a", 64), ConfigHash: strings.Repeat("b", 64), JournalHash: strings.Repeat("c", 64)}, Review: &fixed}}
	data, _ := json.Marshal(bound)
	path := filepath.Join(fixture.directory, "review.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	done := serveOne(fixture.listener, func(call api.Call) api.Reply {
		request, ok := call.IntakeInput()
		if !ok || request.Review.OperationID != fixed.OperationID || request.Review.Event != "ALLOW" || request.IssueNumber != 9 || request.Configuration.Repository != "team/source" {
			t.Errorf("wrong review request: %+v", request)
		}
		return api.NewContentReply(api.IntakeResult{State: "ok", Review: &api.IntakeReviewResult{Repository: "team/target", RepositoryID: 42, Response: `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"verdict":"allow"},"isError":false}}`}})
	})
	input := `{"jsonrpc":"2.0","id":99,"method":"tools/call","params":{"name":"submit_pull_request_review","arguments":{"repository":"team/target","pull_number":7,"head_sha":"` + fixed.HeadSHA + `","operation_id":"` + fixed.OperationID + `","event":"ALLOW","body":"reviewed"}}}` + "\n"
	var output bytes.Buffer
	if exit := runIntakeReviewMCP(t.Context(), path, strings.NewReader(input), &output, func(string) string { return "" }); exit != 0 {
		t.Fatalf("adapter exit%d: %s", exit, output.String())
	}
	if result := awaitServer(t, done); result.err != nil {
		t.Fatal(result.err)
	}
	var reply struct {
		ID     int `json:"id"`
		Result struct {
			Content struct {
				Verdict string `json:"verdict"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	if json.Unmarshal(output.Bytes(), &reply) != nil || reply.ID != 99 || reply.Result.Content.Verdict != "allow" {
		t.Fatalf("MCP response: %s", output.String())
	}
}
