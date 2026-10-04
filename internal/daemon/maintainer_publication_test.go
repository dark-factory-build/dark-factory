//go:build darwin || linux

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestRecordMaintainerPublicationPersistsTaskAndPullRequest(t *testing.T) {
	ctx := context.Background()
	fixture := newDispatchFixture(t)
	projectID := mustProjectID(t, testID(230))
	if _, err := fixture.store.CreateProject(ctx, kernel.NewProject{ID: projectID, Name: "publication", Root: "/publication"}, mustKernelTime(t, 2)); err != nil {
		t.Fatal(err)
	}
	incarnation, err := kernel.IncarnationIDFromBytes(mustIDBytes(t, testID(232)))
	if err != nil {
		t.Fatal(err)
	}
	task, err := fixture.store.EnqueueTask(ctx, kernel.NewTask{ID: mustTaskID(t, testID(231)), IncarnationID: incarnation, ProjectID: projectID, Title: "publish"}, mustKernelTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	head := strings.Repeat("a", 40)
	request := maintainerRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call", Params: json.RawMessage(`{"name":"create_pull_request","arguments":{"repository":"team/repo","title":"Ship it","head":"feature/ship","base":"main","body":"fixture body"}}`)}
	response := json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"isError":false,"structuredContent":{"number":7,"url":"https://github.com/team/repo/pull/7","head_sha":"` + head + `"}}}`)
	if err := fixture.daemon.recordMaintainerPublication(ctx, projectID, task.ID, request, response); err != nil {
		t.Fatal(err)
	}
	page, err := fixture.store.Production(ctx, projectID, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range page.Records {
		if record.Kind == "pull_request" && record.ID == "7" {
			if record.VisualID != "team/repo#7" || len(record.Tasks) != 1 || record.Tasks[0] != task.ID.String() {
				t.Fatalf("publication record = %+v", record)
			}
			return
		}
	}
	t.Fatal("publication record was not persisted")
}

func TestRecordMaintainerPublicationRejectsTopLevelJSONRPCError(t *testing.T) {
	request := maintainerRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"update_pull_request_body","arguments":{"repository":"team/repo","operation_id":"11111111-1111-4111-8111-111111111111","pull_number":7,"body":"corrected body"}}`),
	}
	response := json.RawMessage(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"operation refused"}}`)
	if err := (&Daemon{}).recordMaintainerPublication(context.Background(), kernel.ProjectID{}, kernel.TaskID{}, request, response); err == nil {
		t.Fatal("top-level JSON-RPC error was accepted as a successful publication response")
	}
}

func TestValidateMaintainerResponseRequiresMatchingResultEnvelope(t *testing.T) {
	request := maintainerRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`)}
	for _, test := range []struct {
		name     string
		response string
		valid    bool
	}{
		{name: "matching numeric result", response: `{"jsonrpc":"2.0","id":1,"result":{"isError":true}}`, valid: true},
		{name: "top-level error", response: `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"invalid params"}}`},
		{name: "result and null error", response: `{"jsonrpc":"2.0","id":1,"result":{"isError":false},"error":null}`},
		{name: "wrong id", response: `{"jsonrpc":"2.0","id":2,"result":{}}`},
		{name: "missing result", response: `{"jsonrpc":"2.0","id":1}`, valid: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateMaintainerResponse(request, json.RawMessage(test.response))
			if (err == nil) != test.valid {
				t.Fatalf("validation error=%v, want valid=%v", err, test.valid)
			}
		})
	}
}

func TestMaintainerIDsEqualAcceptsEquivalentJSONValues(t *testing.T) {
	for _, test := range []struct {
		name       string
		requestID  string
		responseID string
		wantEqual  bool
	}{
		{name: "numeric spelling", requestID: `1.0`, responseID: `1`, wantEqual: true},
		{name: "escaped string", requestID: `"a\u0062"`, responseID: `"ab"`, wantEqual: true},
		{name: "different number", requestID: `1`, responseID: `2`, wantEqual: false},
		{name: "different type", requestID: `1`, responseID: `"1"`, wantEqual: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := maintainerIDsEqual(json.RawMessage(test.requestID), json.RawMessage(test.responseID)); got != test.wantEqual {
				t.Fatalf("IDs equal=%v, want %v", got, test.wantEqual)
			}
		})
	}
}

func readProductionPull(t *testing.T, store *kernel.Store, project kernel.ProjectID, number uint64) kernel.ProductionPullRequest {
	t.Helper()
	page, err := store.Production(context.Background(), project, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range page.Records {
		if record.Kind == "pull_request" && record.ID == fmt.Sprint(number) {
			var pull kernel.ProductionPullRequest
			if err := json.Unmarshal(record.Document, &pull); err != nil {
				t.Fatal(err)
			}
			return pull
		}
	}
	t.Fatalf("pull request #%d missing", number)
	return kernel.ProductionPullRequest{}
}

func TestRecordMaintainerPublicationIgnoresUnrelatedResponsesAndEnforcesProject(t *testing.T) {
	ctx := context.Background()
	fixture := newDispatchFixture(t)
	projectID := mustProjectID(t, testID(233))
	foreignID := mustProjectID(t, testID(234))
	for _, id := range []kernel.ProjectID{projectID, foreignID} {
		if _, err := fixture.store.CreateProject(ctx, kernel.NewProject{ID: id, Name: "publication", Root: "/publication/" + id.String()}, mustKernelTime(t, 2)); err != nil {
			t.Fatal(err)
		}
	}
	incarnation, err := kernel.IncarnationIDFromBytes(mustIDBytes(t, testID(236)))
	if err != nil {
		t.Fatal(err)
	}
	task, err := fixture.store.EnqueueTask(ctx, kernel.NewTask{ID: mustTaskID(t, testID(235)), IncarnationID: incarnation, ProjectID: foreignID, Title: "foreign"}, mustKernelTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	request := maintainerRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call", Params: json.RawMessage(`{"name":"create_pull_request","arguments":{"repository":"team/repo","title":"Ship it","head":"feature/ship","base":"main","body":"fixture body"}}`)}
	failed := json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"isError":true,"structuredContent":{"number":7,"url":"https://github.com/team/repo/pull/7","head_sha":"` + strings.Repeat("b", 40) + `"}}}`)
	if err := fixture.daemon.recordMaintainerPublication(ctx, projectID, task.ID, request, failed); err != nil {
		t.Fatal(err)
	}
	other := maintainerRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call", Params: json.RawMessage(`{"name":"observe_operation","arguments":{}}`)}
	if err := fixture.daemon.recordMaintainerPublication(ctx, projectID, task.ID, other, failed); err != nil {
		t.Fatal(err)
	}
	valid := json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"isError":false,"structuredContent":{"number":8,"url":"https://github.com/team/repo/pull/8","head_sha":"` + strings.Repeat("c", 40) + `"}}}`)
	if err := fixture.daemon.recordMaintainerPublication(ctx, projectID, task.ID, request, valid); !errors.Is(err, kernel.ErrUnauthorized) {
		t.Fatalf("cross-project publication = %v", err)
	}
	page, err := fixture.store.Production(ctx, projectID, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 0 {
		t.Fatalf("unrelated publication records = %+v", page.Records)
	}
}
