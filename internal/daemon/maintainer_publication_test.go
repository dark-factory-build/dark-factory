//go:build darwin || linux

package daemon

import (
	"context"
	"encoding/json"
	"errors"
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
	request := maintainerRequest{Method: "tools/call", Params: json.RawMessage(`{"name":"create_pull_request","arguments":{"repository":"team/repo","title":"Ship it","head":"feature/ship","base":"main"}}`)}
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

func TestRecordMaintainerPublicationPersistsReviewCoveredHead(t *testing.T) {
	ctx := context.Background()
	fixture := newDispatchFixture(t)
	projectID := mustProjectID(t, testID(237))
	if _, err := fixture.store.CreateProject(ctx, kernel.NewProject{ID: projectID, Name: "review", Root: "/review"}, mustKernelTime(t, 2)); err != nil {
		t.Fatal(err)
	}
	incarnation, err := kernel.IncarnationIDFromBytes(mustIDBytes(t, testID(239)))
	if err != nil {
		t.Fatal(err)
	}
	task, err := fixture.store.EnqueueTask(ctx, kernel.NewTask{ID: mustTaskID(t, testID(238)), IncarnationID: incarnation, ProjectID: projectID, Title: "publish"}, mustKernelTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	head := strings.Repeat("a", 40)
	create := maintainerRequest{Method: "tools/call", Params: json.RawMessage(`{"name":"create_pull_request","arguments":{"repository":"team/repo","title":"Ship it","head":"feature/ship","base":"main"}}`)}
	response := json.RawMessage(`{"result":{"isError":false,"structuredContent":{"number":7,"url":"https://github.com/team/repo/pull/7","head_sha":"` + head + `"}}}`)
	if err := fixture.daemon.recordMaintainerPublication(ctx, projectID, task.ID, create, response); err != nil {
		t.Fatal(err)
	}
	covered := strings.Repeat("b", 40)
	review := maintainerRequest{Method: "tools/call", Params: json.RawMessage(`{"name":"submit_pull_request_review","arguments":{"repository":"team/repo","pull_number":7,"head_sha":"` + covered + `","event":"ALLOW","body":"independent"}}`)}
	failedReview := json.RawMessage(`{"result":{"isError":true,"structuredContent":{}}}`)
	if err := fixture.daemon.recordMaintainerPublication(ctx, projectID, task.ID, review, failedReview); err != nil {
		t.Fatal(err)
	}
	page, err := fixture.store.Production(ctx, projectID, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range page.Records {
		if record.Kind != "pull_request" {
			continue
		}
		var pull kernel.ProductionPullRequest
		if err := json.Unmarshal(record.Document, &pull); err != nil {
			t.Fatal(err)
		}
		if pull.Review.State != "unknown" {
			t.Fatalf("failed review projected = %+v", pull.Review)
		}
	}
	successReview := json.RawMessage(`{"result":{"isError":false,"structuredContent":{"head_sha":"` + covered + `","verdict":"allow"}}}`)
	if err := fixture.daemon.recordMaintainerPublication(ctx, projectID, task.ID, review, successReview); err != nil {
		t.Fatal(err)
	}
	page, err = fixture.store.Production(ctx, projectID, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range page.Records {
		if record.Kind != "pull_request" {
			continue
		}
		var pull kernel.ProductionPullRequest
		if err := json.Unmarshal(record.Document, &pull); err != nil {
			t.Fatal(err)
		}
		if pull.Review.Head != covered || pull.Review.State != "allow" {
			t.Fatalf("review = %+v", pull.Review)
		}
		return
	}
	t.Fatal("pull request record was not persisted")
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
	request := maintainerRequest{Method: "tools/call", Params: json.RawMessage(`{"name":"create_pull_request","arguments":{"repository":"team/repo","title":"Ship it","head":"feature/ship","base":"main"}}`)}
	failed := json.RawMessage(`{"result":{"isError":true,"structuredContent":{"number":7,"url":"https://github.com/team/repo/pull/7","head_sha":"` + strings.Repeat("b", 40) + `"}}}`)
	if err := fixture.daemon.recordMaintainerPublication(ctx, projectID, task.ID, request, failed); err != nil {
		t.Fatal(err)
	}
	other := maintainerRequest{Method: "tools/call", Params: json.RawMessage(`{"name":"observe_operation","arguments":{}}`)}
	if err := fixture.daemon.recordMaintainerPublication(ctx, projectID, task.ID, other, failed); err != nil {
		t.Fatal(err)
	}
	valid := json.RawMessage(`{"result":{"isError":false,"structuredContent":{"number":8,"url":"https://github.com/team/repo/pull/8","head_sha":"` + strings.Repeat("c", 40) + `"}}}`)
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
