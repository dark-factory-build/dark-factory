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

type publicReviewBackend struct {
	killed, submitAmbiguous, requestChanges bool
	reviews, submits, enqueues              int
	enqueuedBase, enqueuedSHA               string
	journal                                 map[string]string
	observations                            map[string]review.Receipt
}

func (b *publicReviewBackend) CloneReadOnly(context.Context, review.Request) (string, func(), error) {
	return "/fixture-review", func() {}, nil
}
func (b *publicReviewBackend) Review(context.Context, string, review.Request) (review.Verdict, error) {
	b.reviews++
	if b.killed {
		return review.Verdict{}, errors.New("provider killed at launch")
	}
	if b.requestChanges {
		return review.Verdict{Event: "REQUEST_CHANGES", Body: "fixture findings"}, nil
	}
	return review.Verdict{Event: "ALLOW", Body: "fixture allow"}, nil
}
func (b *publicReviewBackend) Submit(_ context.Context, operation review.Operation, _ review.Verdict) error {
	b.submits++
	if err := b.record("submit_pull_request_review", operation.ID); err != nil {
		return err
	}
	if b.submitAmbiguous {
		return errors.New("submit timeout after Maintainer write")
	}
	return nil
}
func (b *publicReviewBackend) Enqueue(_ context.Context, operation review.Operation) error {
	b.enqueues++
	b.enqueuedBase, b.enqueuedSHA = operation.Request.BaseRef, operation.Request.Base
	return b.record("enqueue_pull_request", operation.EnqueueID)
}

func (b *publicReviewBackend) Observe(_ context.Context, operationID string) (review.Receipt, error) {
	if receipt, ok := b.observations[operationID]; ok {
		return receipt, nil
	}
	return review.Receipt{State: "missing"}, nil
}

func (b *publicReviewBackend) record(kind, id string) error {
	if b.journal == nil {
		b.journal = map[string]string{}
	}
	// The Maintainer journal binds one UUID to one operation kind.
	if previous, exists := b.journal[id]; exists && previous != kind {
		return errors.New("journal rejected operation UUID reuse")
	}
	b.journal[id] = kind
	return nil
}

func TestPublicReviewPathPersistsKilledProviderFailureAndRetries(t *testing.T) {
	fixture, project := reviewPublicFixture(t)
	backend := &publicReviewBackend{killed: true}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	input := api.IntakeInput{Action: "review_pr", ProjectID: project.String(), ReviewRequest: &api.ReviewRequest{Repository: "team/repo", PullNumber: 7, Head: strings.Repeat("a", 40), Base: strings.Repeat("b", 40), BaseRef: "main", Body: "fixture", Provider: "codex"}}
	if result := fixture.daemon.Intake(context.Background(), input); result.State == "ok" {
		t.Fatal("killed provider unexpectedly succeeded")
	}
	op := lastDurableReview(t, fixture.store, project)
	if op.State != "failed" || !op.Retryable || backend.reviews != 1 {
		t.Fatalf("failed operation=%+v backend=%+v", op, backend)
	}
	backend.killed = false
	retry := api.IntakeInput{Action: "review_pr", ProjectID: project.String(), ReviewRequest: &api.ReviewRequest{RetryOperation: op.ID}}
	result := fixture.daemon.Intake(context.Background(), retry)
	if result.State != "ok" || backend.reviews != 2 || backend.submits != 1 || backend.enqueues != 1 || len(backend.journal) != 2 {
		t.Fatalf("retry result=%+v operation=%+v backend=%+v", result, lastDurableReview(t, fixture.store, project), backend)
	}
}

func TestPublicReviewPathRefusesRetryAfterAmbiguousSubmit(t *testing.T) {
	fixture, project := reviewPublicFixture(t)
	backend := &publicReviewBackend{submitAmbiguous: true}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	input := api.IntakeInput{Action: "review_pr", ProjectID: project.String(), ReviewRequest: &api.ReviewRequest{Repository: "team/repo", PullNumber: 8, Head: strings.Repeat("c", 40), Base: strings.Repeat("d", 40), BaseRef: "main", Body: "fixture", Provider: "claude"}}
	if result := fixture.daemon.Intake(context.Background(), input); result.State == "ok" {
		t.Fatal("ambiguous submit unexpectedly succeeded")
	}
	op := lastDurableReview(t, fixture.store, project)
	if op.State != "failed" || op.Retryable {
		t.Fatalf("ambiguous operation=%+v", op)
	}
	before := backend.reviews
	result := fixture.daemon.Intake(context.Background(), api.IntakeInput{Action: "review_pr", ProjectID: project.String(), ReviewRequest: &api.ReviewRequest{RetryOperation: op.ID}})
	if result.State == "ok" || backend.reviews != before || backend.enqueues != 0 {
		t.Fatalf("ambiguous retry was reissued: result=%+v backend=%+v", result, backend)
	}
}

func TestRestartDoesNotRouteRequestChangesBeforeSubmit(t *testing.T) {
	fixture, project := reviewPublicFixture(t)
	request := review.Request{Repository: "team/repo", PullNumber: 13, Head: strings.Repeat("a", 40), Base: strings.Repeat("b", 40), BaseRef: "main", Body: "fixture body", Provider: "codex"}
	operation := review.Operation{ID: "request-changes-before-submit", Request: request, State: "running", Verdict: "request_changes", Detail: "not submitted", CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(1, 0)}
	if err := fixture.store.RecordReviewOperation(context.Background(), project, request.Repository, operation.ID, operation, mustKernelTime(t, 1001)); err != nil {
		t.Fatal(err)
	}
	restarted, err := newDaemon(fixture.store, func() time.Time { return time.Unix(2, 0) })
	if err != nil {
		t.Fatal(err)
	}
	if count, err := restarted.RecoverReviewOperations(context.Background()); err != nil || count != 1 {
		t.Fatalf("pre-submit restart recovery count=%d err=%v", count, err)
	}
	recovered := lastDurableReview(t, fixture.store, project)
	if recovered.State != "failed" || recovered.RoutePending || recovered.Submitted {
		t.Fatalf("pre-submit operation became routable=%+v", recovered)
	}
}

func TestRestartRoutesCompletedRequestChangesWithoutResubmitting(t *testing.T) {
	fixture, project := reviewPublicFixture(t)
	ctx := context.Background()
	factory, err := fixture.store.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.SetDispatch(ctx, factory.Revision, true, mustKernelTime(t, 1000)); err != nil {
		t.Fatal(err)
	}
	agent, err := fixture.store.CreateAgent(ctx, kernel.NewAgent{ID: mustAgentID(t, testID(252)), ProjectID: project, Name: "publisher", Role: kernel.RoleOrchestrator, Provider: kernel.ProviderCodex, ToolBudgetLimit: 2}, mustKernelTime(t, 1001))
	if err != nil {
		t.Fatal(err)
	}
	incarnation, err := kernel.IncarnationIDFromBytes(mustIDBytes(t, testID(253)))
	if err != nil {
		t.Fatal(err)
	}
	task, err := fixture.store.EnqueueTask(ctx, kernel.NewTask{ID: mustTaskID(t, testID(254)), IncarnationID: incarnation, ProjectID: project, AssignedAgentID: agent.ID, Title: "publish"}, mustKernelTime(t, 1002))
	if err != nil {
		t.Fatal(err)
	}
	decode := func(value []byte, decode func([]byte) (any, error)) any {
		decoded, err := decode(value)
		if err != nil {
			t.Fatal(err)
		}
		return decoded
	}
	keys := kernel.AdmissionKeys{
		RunID:             decode(mustIDBytes(t, testID(220)), func(value []byte) (any, error) { return kernel.RunIDFromBytes(value) }).(kernel.RunID),
		TerminalSessionID: decode(mustIDBytes(t, testID(221)), func(value []byte) (any, error) { return kernel.TerminalSessionIDFromBytes(value) }).(kernel.TerminalSessionID),
		AttemptDigest:     decode([]byte(strings.Repeat("a", kernel.DigestBytes)), func(value []byte) (any, error) { return kernel.AttemptDigestFromBytes(value) }).(kernel.AttemptDigest),
		ResultProofDigest: decode([]byte(strings.Repeat("b", kernel.DigestBytes)), func(value []byte) (any, error) { return kernel.ResultProofDigestFromBytes(value) }).(kernel.ResultProofDigest),
		CandidateChangeID: decode(mustIDBytes(t, testID(224)), func(value []byte) (any, error) { return kernel.ChangeIDFromBytes(value) }).(kernel.ChangeID),
		Resources: kernel.AdmissionResourceIDs{
			RuntimeRoot:     decode(mustIDBytes(t, testID(225)), func(value []byte) (any, error) { return kernel.ResourceIDFromBytes(value) }).(kernel.ResourceID),
			RunnerProcess:   decode(mustIDBytes(t, testID(226)), func(value []byte) (any, error) { return kernel.ResourceIDFromBytes(value) }).(kernel.ResourceID),
			ProviderProcess: decode(mustIDBytes(t, testID(227)), func(value []byte) (any, error) { return kernel.ResourceIDFromBytes(value) }).(kernel.ResourceID),
			ProviderGroup:   decode(mustIDBytes(t, testID(228)), func(value []byte) (any, error) { return kernel.ResourceIDFromBytes(value) }).(kernel.ResourceID),
		},
		RuntimeRoot: "/runtime/review",
	}
	admission, err := fixture.store.AdmitNext(ctx, keys, mustKernelTime(t, 1003))
	if err != nil || !admission.Admitted() {
		t.Fatalf("admission=%+v err=%v", admission, err)
	}
	runtimeIdentity, err := kernel.NewPathResourceIdentity(1, 2)
	if err != nil {
		t.Fatal(err)
	}
	birth, err := kernel.BirthDigestFromBytes([]byte(strings.Repeat("a", kernel.DigestBytes)))
	if err != nil {
		t.Fatal(err)
	}
	processIdentity, err := kernel.NewProcessResourceIdentity(300, 301, birth)
	if err != nil {
		t.Fatal(err)
	}
	run := *admission.Run
	resourceRevision, err := kernel.NewRevision(1)
	if err != nil {
		t.Fatal(err)
	}
	runtimeResource, err := fixture.store.ActivateResource(ctx, run.ID, keys.Resources.RuntimeRoot, resourceRevision, runtimeIdentity, mustKernelTime(t, 1004))
	if err != nil {
		t.Fatal(err)
	}
	startedRun, startingRunner, err := fixture.store.BeginRunnerStart(ctx, run.ID, keys.Resources.RunnerProcess, run.Revision, resourceRevision, mustKernelTime(t, 1005))
	if err != nil {
		t.Fatal(err)
	}
	activatedRun, runnerResource, err := fixture.store.ActivateRunner(ctx, startedRun.ID, startingRunner.ID, startedRun.Revision, startingRunner.Revision, processIdentity, mustKernelTime(t, 1006))
	if err != nil {
		t.Fatal(err)
	}
	finalizing, err := fixture.store.RecordRecoveredPreSessionRunnerAbsence(ctx, activatedRun.ID, runnerResource.ID, activatedRun.Revision, runnerResource.Revision, processIdentity, mustKernelTime(t, 1007))
	if err != nil {
		t.Fatal(err)
	}
	releasingRuntimeRevision, err := kernel.NewRevision(runtimeResource.Revision.Int64() + 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.ReleaseResource(ctx, finalizing.ID, runtimeResource.ID, releasingRuntimeRevision, runtimeIdentity, mustKernelTime(t, 1008)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.FinalizeRun(ctx, finalizing.ID, finalizing.Revision, mustKernelTime(t, 1009)); err != nil {
		t.Fatal(err)
	}
	head, base := strings.Repeat("e", 40), strings.Repeat("f", 40)
	pr := kernel.ProductionPullRequest{Number: 12, Title: "Ship it", URL: "https://github.com/team/repo/pull/12", Head: head, Branch: "feature/ship", Base: "main", State: "open", Review: kernel.ProductionReview{Head: head, State: "unknown"}}
	if err := fixture.store.RecordPublication(ctx, project, task.ID, "team/repo", pr, mustKernelTime(t, 1010)); err != nil {
		t.Fatal(err)
	}
	backend := &publicReviewBackend{requestChanges: true}
	now := func() time.Time { return time.Unix(1011, 0) }
	coordinator := review.Coordinator{Store: durableReviewStore{store: fixture.store, project: project, repository: "team/repo", now: now}, Backend: backend, Now: now}
	op, err := coordinator.Start(ctx, review.Request{Repository: "team/repo", PullNumber: 12, Head: head, Base: base, BaseRef: "main", Body: "fixture body", Provider: "codex"})
	if err != nil || op.State != "completed" || !op.Submitted || !op.RoutePending || backend.submits != 1 {
		t.Fatalf("completed request-changes operation=%+v err=%v backend=%+v", op, err, backend)
	}

	restarted, err := newDaemon(fixture.store, func() time.Time { return time.Unix(1012, 0) })
	if err != nil {
		t.Fatal(err)
	}
	if count, err := restarted.RecoverReviewOperations(ctx); err != nil || count != 1 {
		t.Fatalf("restart routing count=%d err=%v", count, err)
	}
	taskAfter, found, err := fixture.store.Task(ctx, task.ID)
	if err != nil || !found || !strings.Contains(kernel.TaskFeedback(taskAfter), "review-operation: "+op.ID) {
		t.Fatalf("routed task found=%v err=%v task=%+v", found, err, taskAfter)
	}
	if backend.submits != 1 || backend.reviews != 1 {
		t.Fatalf("restart resubmitted provider review: backend=%+v", backend)
	}
	if count, err := restarted.RecoverReviewOperations(ctx); err != nil || count != 0 {
		t.Fatalf("replayed routing count=%d err=%v", count, err)
	}
	recovered := lastDurableReview(t, fixture.store, project)
	if recovered.ID != op.ID || recovered.State != "completed" || recovered.RoutePending {
		t.Fatalf("routed operation=%+v", recovered)
	}
}

func reviewPublicFixture(t *testing.T) (*dispatchFixture, kernel.ProjectID) {
	t.Helper()
	fixture := newDispatchFixture(t)
	project := mustProjectID(t, testID(250))
	ctx := context.Background()
	if _, err := fixture.store.CreateProject(ctx, kernel.NewProject{ID: project, Name: "review-public", Root: "/review-public"}, mustKernelTime(t, 2)); err != nil {
		t.Fatal(err)
	}
	repository, err := kernel.RepositoryIDFromBytes(mustIDBytes(t, testID(251)))
	if err != nil {
		t.Fatal(err)
	}
	identity := kernel.RepositorySourceIdentity{RootDevice: 1, RootInode: 2, GitDevice: 1, GitInode: 3, OriginDigest: [32]byte{1}, PublicationRepository: "team/repo"}
	if _, err := fixture.store.AddProjectRepository(ctx, kernel.NewProjectRepository{ID: repository, ProjectID: project, Name: "publication", Root: "/review-public-repository", BaseRef: "main", SourceIdentity: &identity}, mustKernelTime(t, 3)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.BindRepositoryGitHubID(ctx, repository, 42); err != nil {
		t.Fatal(err)
	}
	return fixture, project
}

func lastDurableReview(t *testing.T, store *kernel.Store, project kernel.ProjectID) review.Operation {
	t.Helper()
	page, err := store.Production(context.Background(), project, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range page.Records {
		if record.Kind == "reviewer" {
			var op review.Operation
			if err := json.Unmarshal(record.Document, &op); err != nil {
				t.Fatal(err)
			}
			return op
		}
	}
	t.Fatal("no durable review operation")
	return review.Operation{}
}

func waitForDurableReview(t *testing.T, store *kernel.Store, project kernel.ProjectID, ready func(review.Operation) bool) review.Operation {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		op := lastDurableReview(t, store, project)
		if ready(op) {
			return op
		}
		time.Sleep(5 * time.Millisecond)
	}
	return lastDurableReview(t, store, project)
}
