//go:build darwin || linux

package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestPublishedFixtureTriggersExactHeadReview(t *testing.T) {
	fixture := newDispatchFixture(t)
	projectID := mustProjectID(t, testID(240))
	if _, err := fixture.store.CreateProject(context.Background(), kernel.NewProject{ID: projectID, Name: "publication-review", Root: "/publication-review"}, mustKernelTime(t, 2)); err != nil {
		t.Fatal(err)
	}
	incarnation, err := kernel.IncarnationIDFromBytes(mustIDBytes(t, testID(242)))
	if err != nil {
		t.Fatal(err)
	}
	task, err := fixture.store.EnqueueTask(context.Background(), kernel.NewTask{ID: mustTaskID(t, testID(241)), IncarnationID: incarnation, ProjectID: projectID, Title: "publish"}, mustKernelTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	head := strings.Repeat("a", 40)
	base := strings.Repeat("b", 40)
	var got api.ReviewRequest
	fixture.daemon.reviewPublished = func(_ context.Context, _ kernel.ProjectID, request api.ReviewRequest) (string, error) {
		got = request
		return "review-op", nil
	}
	request := maintainerRequest{Method: "tools/call", Params: json.RawMessage(`{"name":"create_pull_request","arguments":{"repository":"team/repo","title":"Ship it","head":"feature/ship","base":"main"}}`)}
	response := json.RawMessage(`{"result":{"isError":false,"structuredContent":{"number":7,"url":"https://github.com/team/repo/pull/7","head_sha":"` + head + `","base_sha":"` + base + `","body":"fixture body"}}}`)
	if err := fixture.daemon.recordMaintainerPublication(context.Background(), projectID, task.ID, request, response); err != nil {
		t.Fatal(err)
	}
	if got.Repository != "team/repo" || got.PullNumber != 7 || got.Head != head || got.Base != base || got.Body != "fixture body" || got.Provider != "codex" {
		t.Fatalf("review request=%+v", got)
	}
}
