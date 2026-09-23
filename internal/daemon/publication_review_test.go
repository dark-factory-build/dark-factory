//go:build darwin || linux

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/review"
)

type failReviewUpdateStore struct {
	review.Store
	failAt, updates int
}

func (s *failReviewUpdateStore) Update(ctx context.Context, operation review.Operation) error {
	s.updates++
	if s.updates == s.failAt {
		return errors.New("simulated restart before durable review update")
	}
	return s.Store.Update(ctx, operation)
}

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
	request := maintainerRequest{Method: "tools/call", Params: json.RawMessage(`{"name":"create_pull_request","arguments":{"repository":"team/repo","title":"Ship it","head":"feature/ship","base":"main","body":"fixture body"}}`)}
	response := json.RawMessage(`{"result":{"isError":false,"structuredContent":{"number":7,"url":"https://github.com/team/repo/pull/7","head_sha":"` + head + `","base_sha":"` + base + `","base_ref":"main"}}}`)
	if err := fixture.daemon.recordMaintainerPublication(context.Background(), projectID, task.ID, request, response); err != nil {
		t.Fatal(err)
	}
	if got.Repository != "team/repo" || got.PullNumber != 7 || got.Head != head || got.Base != base || got.BaseRef != "main" || got.Body != "fixture body" || got.Provider != "codex" {
		t.Fatalf("review request=%+v", got)
	}
}

func TestPublishedFixtureEnqueuesWithBaseRef(t *testing.T) {
	fixture, projectID := reviewPublicFixture(t)
	incarnation, err := kernel.IncarnationIDFromBytes(mustIDBytes(t, testID(252)))
	if err != nil {
		t.Fatal(err)
	}
	task, err := fixture.store.EnqueueTask(context.Background(), kernel.NewTask{ID: mustTaskID(t, testID(253)), IncarnationID: incarnation, ProjectID: projectID, Title: "publish"}, mustKernelTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	backend := &publicReviewBackend{}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	request := maintainerRequest{Method: "tools/call", Params: json.RawMessage(`{"name":"create_pull_request","arguments":{"repository":"team/repo","title":"Ship it","head":"feature/ship","base":"main","body":"fixture body"}}`)}
	response := json.RawMessage(`{"result":{"isError":false,"structuredContent":{"number":7,"url":"https://github.com/team/repo/pull/7","head_sha":"` + head + `","base_sha":"` + base + `"}}}`)
	if err := fixture.daemon.recordMaintainerPublication(context.Background(), projectID, task.ID, request, response); err != nil {
		t.Fatal(err)
	}
	op := waitForDurableReview(t, fixture.store, projectID, func(op review.Operation) bool { return op.State == "enqueued" })
	if backend.reviews != 1 || backend.submits != 1 || backend.enqueues != 1 || backend.enqueuedBase != "main" || backend.enqueuedSHA != base || op.Request.Base != base || op.Request.BaseRef != "main" {
		t.Fatalf("publication review enqueue=%+v operation=%+v", backend, op)
	}
}

func TestPublishedReviewOperationIsDurableBeforeProviderFailure(t *testing.T) {
	fixture, projectID := reviewPublicFixture(t)
	incarnation, err := kernel.IncarnationIDFromBytes(mustIDBytes(t, testID(243)))
	if err != nil {
		t.Fatal(err)
	}
	task, err := fixture.store.EnqueueTask(context.Background(), kernel.NewTask{ID: mustTaskID(t, testID(244)), IncarnationID: incarnation, ProjectID: projectID, Title: "publish"}, mustKernelTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	backend := &publicReviewBackend{killed: true}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	head := strings.Repeat("a", 40)
	response := json.RawMessage(`{"result":{"isError":false,"structuredContent":{"number":7,"url":"https://github.com/team/repo/pull/7","head_sha":"` + head + `","base_sha":"` + strings.Repeat("b", 40) + `"}}}`)
	request := maintainerRequest{Method: "tools/call", Params: json.RawMessage(`{"name":"create_pull_request","arguments":{"repository":"team/repo","title":"Ship it","head":"feature/ship","base":"main","body":"fixture body"}}`)}
	if err := fixture.daemon.recordMaintainerPublication(context.Background(), projectID, task.ID, request, response); err != nil {
		t.Fatalf("publication acknowledgement failed: %v", err)
	}
	op := waitForDurableReview(t, fixture.store, projectID, func(op review.Operation) bool { return op.State == "failed" })
	if op.State != "failed" || backend.reviews != 1 {
		t.Fatalf("publication review was not durably started: operation=%+v backend=%+v", op, backend)
	}
}

func TestPublicationReviewClaimSurvivesInterruptionBeforeCoordinatorResume(t *testing.T) {
	fixture, projectID := reviewPublicFixture(t)
	incarnation, err := kernel.IncarnationIDFromBytes(mustIDBytes(t, testID(245)))
	if err != nil {
		t.Fatal(err)
	}
	task, err := fixture.store.EnqueueTask(context.Background(), kernel.NewTask{ID: mustTaskID(t, testID(246)), IncarnationID: incarnation, ProjectID: projectID, Title: "publish"}, mustKernelTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	prepared, err := review.Prepare(review.Request{Repository: "team/repo", PullNumber: 9, Head: head, Base: base, BaseRef: "main", Body: "fixture body", Provider: "codex"}, func() time.Time { return time.Unix(3, 0) })
	if err != nil {
		t.Fatal(err)
	}
	pr := kernel.ProductionPullRequest{Number: 9, Title: "Ship it", URL: "https://github.com/team/repo/pull/9", Head: head, Branch: "feature/ship", Base: "main", State: "open", Review: kernel.ProductionReview{Head: head, State: "unknown"}}
	if err := fixture.store.RecordPublicationWithReviewOperation(context.Background(), projectID, task.ID, "team/repo", pr, prepared.ID, prepared, mustKernelTime(t, 4)); err != nil {
		t.Fatal(err)
	}
	op := lastDurableReview(t, fixture.store, projectID)
	if op.ID != prepared.ID || op.State != "running" || op.Request.Head != head {
		t.Fatalf("interrupted publication claim = %+v", op)
	}
}

func TestRestartReconcilesPublicationReviewAndRetriesWithoutDuplicateSubmit(t *testing.T) {
	fixture, projectID := reviewPublicFixture(t)
	incarnation, err := kernel.IncarnationIDFromBytes(mustIDBytes(t, testID(247)))
	if err != nil {
		t.Fatal(err)
	}
	task, err := fixture.store.EnqueueTask(context.Background(), kernel.NewTask{ID: mustTaskID(t, testID(248)), IncarnationID: incarnation, ProjectID: projectID, Title: "publish"}, mustKernelTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	prepared, err := review.Prepare(review.Request{Repository: "team/repo", PullNumber: 10, Head: head, Base: base, BaseRef: "main", Body: "fixture body", Provider: "codex"}, func() time.Time { return time.Unix(3, 0) })
	if err != nil {
		t.Fatal(err)
	}
	pr := kernel.ProductionPullRequest{Number: 10, Title: "Ship it", URL: "https://github.com/team/repo/pull/10", Head: head, Branch: "feature/ship", Base: "main", State: "open", Review: kernel.ProductionReview{Head: head, State: "unknown"}}
	if err := fixture.store.RecordPublicationWithReviewOperation(context.Background(), projectID, task.ID, "team/repo", pr, prepared.ID, prepared, mustKernelTime(t, 4)); err != nil {
		t.Fatal(err)
	}

	restarted, err := newDaemon(fixture.store, func() time.Time { return time.Unix(5, 0) })
	if err != nil {
		t.Fatal(err)
	}
	backend := &publicReviewBackend{}
	restarted.reviewBackend = func(string, uint64) review.Backend { return backend }
	if count, err := restarted.RecoverReviewOperations(context.Background()); err != nil || count != 1 {
		t.Fatalf("startup reconciliation count=%d err=%v", count, err)
	}
	if count, err := restarted.RecoverReviewOperations(context.Background()); err != nil || count != 0 {
		t.Fatalf("second reconciliation count=%d err=%v", count, err)
	}
	recovered := lastDurableReview(t, fixture.store, projectID)
	if recovered.ID != prepared.ID || recovered.State != "failed" || !recovered.Retryable {
		t.Fatalf("recovered operation=%+v", recovered)
	}
	result := restarted.Intake(context.Background(), api.IntakeInput{Action: "review_pr", ProjectID: projectID.String(), ReviewRequest: &api.ReviewRequest{RetryOperation: prepared.ID}})
	if result.State != "ok" || backend.reviews != 1 || backend.submits != 1 || backend.enqueues != 1 || len(backend.journal) != 2 {
		t.Fatalf("retry result=%+v operation=%+v backend=%+v", result, lastDurableReview(t, fixture.store, projectID), backend)
	}
}

func TestRestartRefusesRetryAfterUncertainExternalReviewWrites(t *testing.T) {
	for _, test := range []struct {
		name         string
		failAt       int
		wantSubmits  int
		wantEnqueues int
	}{
		{name: "submit", failAt: 2, wantSubmits: 1},
		{name: "enqueue", failAt: 3, wantSubmits: 1, wantEnqueues: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, projectID := reviewPublicFixture(t)
			backend := &publicReviewBackend{}
			now := func() time.Time { return time.Unix(6, 0) }
			store := durableReviewStore{store: fixture.store, project: projectID, repository: "team/repo", now: now}
			coordinator := review.Coordinator{Store: &failReviewUpdateStore{Store: store, failAt: test.failAt}, Backend: backend, Now: now}
			head, base := strings.Repeat("c", 40), strings.Repeat("d", 40)
			_, err := coordinator.Start(context.Background(), review.Request{Repository: "team/repo", PullNumber: 11, Head: head, Base: base, BaseRef: "main", Body: "fixture body", Provider: "codex"})
			if err == nil {
				t.Fatal("simulated restart boundary unexpectedly completed")
			}
			before := lastDurableReview(t, fixture.store, projectID)
			hasEnqueueReceipt := before.EnqueueID != ""
			if before.State != "running" || before.Verdict != "allow" || before.Request.Head != head || hasEnqueueReceipt != (test.wantEnqueues == 1) || backend.submits != test.wantSubmits || backend.enqueues != test.wantEnqueues {
				t.Fatalf("boundary operation=%+v backend=%+v", before, backend)
			}

			restarted, err := newDaemon(fixture.store, func() time.Time { return time.Unix(7, 0) })
			if err != nil {
				t.Fatal(err)
			}
			if count, err := restarted.RecoverReviewOperations(context.Background()); err != nil || count != 1 {
				t.Fatalf("reconciliation count=%d err=%v", count, err)
			}
			recovered := lastDurableReview(t, fixture.store, projectID)
			if recovered.State != "failed" || recovered.Retryable || recovered.Request.Head != head || recovered.ID != before.ID {
				t.Fatalf("uncertain operation=%+v", recovered)
			}
			restarted.reviewBackend = func(string, uint64) review.Backend { return backend }
			result := restarted.Intake(context.Background(), api.IntakeInput{Action: "review_pr", ProjectID: projectID.String(), ReviewRequest: &api.ReviewRequest{RetryOperation: recovered.ID}})
			if result.State == "ok" || backend.submits != test.wantSubmits || backend.enqueues != test.wantEnqueues {
				t.Fatalf("uncertain retry result=%+v operation=%+v backend=%+v", result, recovered, backend)
			}
		})
	}
}
