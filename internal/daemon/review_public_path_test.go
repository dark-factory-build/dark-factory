package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
	"github.com/dark-factory-build/dark-factory/internal/review"
)

type publicReviewBackend struct {
	killed, submitAmbiguous, requestChanges bool
	queued                                  bool
	checks                                  []string
	enqueueRefusal                          string // the broker's refusal class, if any
	leaveUnqueued                           int
	reviews, submits, enqueues              int
	enqueueSignal                           atomic.Int32
	enqueuedBase, enqueuedSHA, enqueuedBody string
	journal                                 map[string]string
	observations                            map[string]review.Receipt
	pull                                    *review.Pull
	observeFailures                         int
	storedErr                               error
}

func (b *publicReviewBackend) StoredPull(_ context.Context, pull uint64, head string) (review.Request, error) {
	if b.storedErr != nil {
		return review.Request{}, b.storedErr
	}
	return review.Request{Repository: "team/repo", PullNumber: pull, Head: head, Base: strings.Repeat("f", 40), BaseRef: "main", Body: "fixture body\n", Provider: "codex"}, nil
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
	b.enqueueSignal.Add(1)
	b.enqueuedBase, b.enqueuedSHA, b.enqueuedBody = operation.Request.BaseRef, operation.Request.Base, operation.Request.Body
	if b.enqueueRefusal != "" {
		return maintainerRejection("refused: The request was refused: rejected before execution as " + b.enqueueRefusal + ".")
	}
	if b.leaveUnqueued > 0 {
		b.leaveUnqueued--
	} else {
		b.queued = true
	}
	return nil
}

// ObservePull defaults to the pull request open at the operation's head with
// every check passed, queued once it has been enqueued.
func (b *publicReviewBackend) ObservePull(_ context.Context, operation review.Operation) (review.Pull, error) {
	if b.pull != nil {
		return *b.pull, nil
	}
	checks := b.checks
	if checks == nil {
		checks = []string{"required=success"}
	}
	return review.Pull{Head: operation.Request.Head, State: "open", Queued: b.queued, Checks: checks}, nil
}

func (b *publicReviewBackend) Observe(_ context.Context, operationID string) (review.Receipt, error) {
	if b.observeFailures > 0 {
		b.observeFailures++
		return review.Receipt{}, fmt.Errorf("observe_operation 401 #%d", b.observeFailures-1)
	}
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
	input := api.IntakeInput{Action: "review_pr", ProjectID: project.String(), ReviewRequest: &api.ReviewRequest{Repository: "team/repo", PullNumber: 7, Head: strings.Repeat("a", 40), Base: strings.Repeat("b", 40), BaseRef: "main", Provider: "codex"}}
	if result := fixture.daemon.Intake(context.Background(), input); result.State != "ok" || result.ReviewOperation == "" {
		t.Fatalf("created review answered %+v", result)
	}
	op := waitForDurableReview(t, fixture.store, project, func(op review.Operation) bool { return op.State == "failed" })
	if op.State != "failed" || !op.Retryable || backend.reviews != 1 {
		t.Fatalf("failed operation=%+v backend=%+v", op, backend)
	}
	backend.killed = false
	retry := api.IntakeInput{Action: "review_pr", ProjectID: project.String(), ReviewRequest: &api.ReviewRequest{RetryOperation: op.ID}}
	result := fixture.daemon.Intake(context.Background(), retry)
	waitForDurableReview(t, fixture.store, project, func(op review.Operation) bool { return op.ID == result.ReviewOperation && op.State == "enqueued" })
	if result.State != "ok" || backend.reviews != 2 || backend.submits != 1 || len(backend.journal) != 1 {
		t.Fatalf("retry result=%+v operation=%+v backend=%+v", result, lastDurableReview(t, fixture.store, project), backend)
	}
	if result := fixture.daemon.Intake(context.Background(), retry); result.State == "ok" || backend.reviews != 2 || backend.submits != 1 {
		t.Fatalf("original failure was replayed after its one retry: result=%+v backend=%+v", result, backend)
	}
}

// A review_pr claim on a pull request no refresh has recorded is still its
// reconciler's: a failed one is retried once, unasked.
func TestAClaimedReviewOfAnUnrecordedPullIsRetried(t *testing.T) {
	fixture, project := reviewPublicFixture(t)
	customerMode(t, fixture)
	backend := &publicReviewBackend{killed: true}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	request := publishedReviewRequest()
	request.PullNumber = 44
	if _, err := reviewNow(context.Background(), fixture.daemon, project, request); err == nil {
		t.Fatal("a killed provider reported success")
	}
	backend.killed = false
	if _, err := fixture.daemon.advanceReviewOperations(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	retried := func() bool {
		open, err := fixture.store.OpenPulls(context.Background())
		for _, pull := range open {
			for _, document := range pull.Operations {
				if strings.Contains(string(document), `"state":"enqueued"`) && strings.Contains(string(document), `"retry_of":"`) {
					return err == nil
				}
			}
		}
		return false
	}
	for deadline := time.Now().Add(2 * time.Second); !retried() && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	if !retried() || backend.reviews != 2 {
		t.Fatalf("unrecorded pull's failed review was not retried: reviews=%d", backend.reviews)
	}
}

// reviewNow claims and runs a review in the foreground, as reviewPR's
// background launch does.
func reviewNow(ctx context.Context, daemon *Daemon, project kernel.ProjectID, request api.ReviewRequest) (string, error) {
	op, err := daemon.claimReview(ctx, project, request)
	if err != nil {
		return "", err
	}
	op, err = daemon.resumeReview(ctx, project, op)
	return op.ID, err
}

func waitForMergePipeline(t *testing.T, daemon *Daemon) {
	t.Helper()
	// The issue's verdict wake is production behavior; finish that merge pass
	// before this test changes the daemon or backend state it reads.
	deadline := time.Now().Add(2 * time.Second)
	for daemon.pipelineBusy.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if daemon.pipelineBusy.Load() {
		t.Fatal("merge pipeline did not finish")
	}
}

// #1300: a second review's ALLOW at a head factoryd already blocked was
// enqueued, and the queue's review job failed on the standing block.
func TestAllowAtABlockedHeadIsNotEnqueued(t *testing.T) {
	fixture, project := reviewPublicFixture(t)
	backend := &publicReviewBackend{requestChanges: true}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	request := api.ReviewRequest{Repository: "team/repo", PullNumber: 12, Head: strings.Repeat("a", 40), Base: strings.Repeat("b", 40), BaseRef: "main", Provider: "codex"}
	if _, err := reviewNow(context.Background(), fixture.daemon, project, request); err != nil {
		t.Fatal(err)
	}
	backend.requestChanges = false
	id, _ := reviewNow(context.Background(), fixture.daemon, project, request)
	var op review.Operation
	document, _, err := fixture.store.ReviewOperation(context.Background(), project, id)
	if err != nil || json.Unmarshal(document, &op) != nil || backend.enqueues != 0 || op.State != "failed" || !strings.Contains(op.Detail, "blocking verdict of record") {
		t.Fatalf("allow over a same-head block: operation=%+v enqueues=%d", op, backend.enqueues)
	}
	request.Head = strings.Repeat("c", 40) // a new head needs no correction
	id, err = reviewNow(context.Background(), fixture.daemon, project, request)
	if document, _, readErr := fixture.store.ReviewOperation(context.Background(), project, id); err != nil || readErr != nil || !strings.Contains(string(document), `"state":"enqueued"`) {
		t.Fatalf("allow at a new head: err=%v operation=%s", err, document)
	}
}

type blockedReviewBackend struct {
	*publicReviewBackend
	release chan struct{}
}

func (b blockedReviewBackend) Review(ctx context.Context, checkout string, request review.Request) (review.Verdict, error) {
	<-b.release
	return b.publicReviewBackend.Review(ctx, checkout, request)
}

// #1166: a created review answers with its operation while it is still
// running, rather than outliving the operator call and reporting failure.
func TestReviewIntakeAnswersWithTheCreatedOperationWhileItRuns(t *testing.T) {
	fixture, project := reviewPublicFixture(t)
	backend := blockedReviewBackend{publicReviewBackend: &publicReviewBackend{}, release: make(chan struct{})}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	input := api.IntakeInput{Action: "review_pr", ProjectID: project.String(), ReviewRequest: &api.ReviewRequest{Repository: "team/repo", PullNumber: 9, Head: strings.Repeat("a", 40), Base: strings.Repeat("b", 40), BaseRef: "main", Provider: "codex"}}
	result := fixture.daemon.Intake(context.Background(), input)
	op := lastDurableReview(t, fixture.store, project)
	close(backend.release)
	if result.State != "ok" || result.ReviewOperation != op.ID || op.State != "running" {
		t.Fatalf("intake answered %+v for operation %+v", result, op)
	}
	if op = waitForDurableReview(t, fixture.store, project, func(op review.Operation) bool { return op.State == "enqueued" }); op.State != "enqueued" {
		t.Fatalf("background review left operation %+v", op)
	}
}

func TestPublicReviewPathRefusesRetryAfterAmbiguousSubmit(t *testing.T) {
	fixture, project := reviewPublicFixture(t)
	backend := &publicReviewBackend{submitAmbiguous: true}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	input := api.IntakeInput{Action: "review_pr", ProjectID: project.String(), ReviewRequest: &api.ReviewRequest{Repository: "team/repo", PullNumber: 8, Head: strings.Repeat("c", 40), Base: strings.Repeat("d", 40), BaseRef: "main", Provider: "claude"}}
	if result := fixture.daemon.Intake(context.Background(), input); result.State != "ok" || result.ReviewOperation == "" {
		t.Fatalf("created review answered %+v", result)
	}
	op := waitForDurableReview(t, fixture.store, project, func(op review.Operation) bool { return op.State == "failed" })
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
	if recovered.State != "failed" || recovered.Retryable || recovered.Submitted {
		t.Fatalf("pre-submit operation became routable=%+v", recovered)
	}
}

// publishedTask publishes pull 12 at head eeee… from a task that is running;
// settle ends its run so a send-back can land.
func publishedTask(t *testing.T) (fixture *dispatchFixture, project kernel.ProjectID, task kernel.Task, settle func()) {
	t.Helper()
	fixture, project = reviewPublicFixture(t)
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
	task, err = fixture.store.EnqueueTask(ctx, kernel.NewTask{ID: mustTaskID(t, testID(254)), IncarnationID: incarnation, ProjectID: project, AssignedAgentID: agent.ID, Title: "publish"}, mustKernelTime(t, 1002))
	if err != nil {
		t.Fatal(err)
	}
	settle = admitPublished(t, fixture, 220, 1003)
	head := strings.Repeat("e", 40)
	pr := kernel.ProductionPullRequest{Number: 12, Title: "Ship it", URL: "https://github.com/team/repo/pull/12", Head: head, Branch: "feature/ship", Base: "main", State: "open", Review: kernel.ProductionReview{Head: head, State: "unknown"}}
	if err := fixture.store.RecordPublication(ctx, project, task.ID, "team/repo", pr, mustKernelTime(t, 1010)); err != nil {
		t.Fatal(err)
	}
	fixture.daemon.now = func() time.Time { return time.Unix(2000, 0) }
	return fixture, project, task, settle
}

// admitPublished admits the published task's next run, its identities from
// base and its times from at on; the settle it returns ends that run, so a
// send-back can land.
func admitPublished(t *testing.T, fixture *dispatchFixture, base byte, at int64) (settle func()) {
	t.Helper()
	ctx := context.Background()
	decode := func(value []byte, decode func([]byte) (any, error)) any {
		decoded, err := decode(value)
		if err != nil {
			t.Fatal(err)
		}
		return decoded
	}
	keys := kernel.AdmissionKeys{
		RunID:             decode(mustIDBytes(t, testID(base)), func(value []byte) (any, error) { return kernel.RunIDFromBytes(value) }).(kernel.RunID),
		TerminalSessionID: decode(mustIDBytes(t, testID(base+1)), func(value []byte) (any, error) { return kernel.TerminalSessionIDFromBytes(value) }).(kernel.TerminalSessionID),
		AttemptDigest:     decode(bytes.Repeat([]byte{base + 2}, kernel.DigestBytes), func(value []byte) (any, error) { return kernel.AttemptDigestFromBytes(value) }).(kernel.AttemptDigest),
		ResultProofDigest: decode(bytes.Repeat([]byte{base + 3}, kernel.DigestBytes), func(value []byte) (any, error) { return kernel.ResultProofDigestFromBytes(value) }).(kernel.ResultProofDigest),
		CandidateChangeID: decode(mustIDBytes(t, testID(base+4)), func(value []byte) (any, error) { return kernel.ChangeIDFromBytes(value) }).(kernel.ChangeID),
		Resources: kernel.AdmissionResourceIDs{
			RuntimeRoot:     decode(mustIDBytes(t, testID(base+5)), func(value []byte) (any, error) { return kernel.ResourceIDFromBytes(value) }).(kernel.ResourceID),
			RunnerProcess:   decode(mustIDBytes(t, testID(base+6)), func(value []byte) (any, error) { return kernel.ResourceIDFromBytes(value) }).(kernel.ResourceID),
			ProviderProcess: decode(mustIDBytes(t, testID(base+7)), func(value []byte) (any, error) { return kernel.ResourceIDFromBytes(value) }).(kernel.ResourceID),
			ProviderGroup:   decode(mustIDBytes(t, testID(base+8)), func(value []byte) (any, error) { return kernel.ResourceIDFromBytes(value) }).(kernel.ResourceID),
		},
		RuntimeRoot: fmt.Sprintf("/runtime/review-%d", base),
	}
	admission, err := fixture.store.AdmitNext(ctx, keys, mustKernelTime(t, at))
	if err != nil || !admission.Admitted() {
		t.Fatalf("admission=%+v err=%v", admission, err)
	}
	runtimeIdentity, err := kernel.NewPathResourceIdentity(1, int64(base))
	if err != nil {
		t.Fatal(err)
	}
	birth, err := kernel.BirthDigestFromBytes(bytes.Repeat([]byte{base + 2}, kernel.DigestBytes))
	if err != nil {
		t.Fatal(err)
	}
	processIdentity, err := kernel.NewProcessResourceIdentity(300, 301, birth)
	if err != nil {
		t.Fatal(err)
	}
	return func() {
		run := *admission.Run
		resourceRevision, err := kernel.NewRevision(1)
		if err != nil {
			t.Fatal(err)
		}
		runtimeResource, err := fixture.store.ActivateResource(ctx, run.ID, keys.Resources.RuntimeRoot, resourceRevision, runtimeIdentity, mustKernelTime(t, at+1))
		if err != nil {
			t.Fatal(err)
		}
		startedRun, startingRunner, err := fixture.store.BeginRunnerStart(ctx, run.ID, keys.Resources.RunnerProcess, run.Revision, resourceRevision, mustKernelTime(t, at+2))
		if err != nil {
			t.Fatal(err)
		}
		activatedRun, runnerResource, err := fixture.store.ActivateRunner(ctx, startedRun.ID, startingRunner.ID, startedRun.Revision, startingRunner.Revision, processIdentity, mustKernelTime(t, at+3))
		if err != nil {
			t.Fatal(err)
		}
		finalizing, err := fixture.store.RecordRecoveredPreSessionRunnerAbsence(ctx, activatedRun.ID, runnerResource.ID, activatedRun.Revision, runnerResource.Revision, processIdentity, mustKernelTime(t, at+4))
		if err != nil {
			t.Fatal(err)
		}
		releasingRuntimeRevision, err := kernel.NewRevision(runtimeResource.Revision.Int64() + 1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.ReleaseResource(ctx, finalizing.ID, runtimeResource.ID, releasingRuntimeRevision, runtimeIdentity, mustKernelTime(t, at+5)); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.FinalizeRun(ctx, finalizing.ID, finalizing.Revision, mustKernelTime(t, at+6)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRestartRoutesCompletedRequestChangesWithoutResubmitting(t *testing.T) {
	fixture, project, task, settle := publishedTask(t)
	ctx := context.Background()
	settle()
	head, base := strings.Repeat("e", 40), strings.Repeat("f", 40)
	backend := &publicReviewBackend{requestChanges: true}
	now := func() time.Time { return time.Unix(1011, 0) }
	coordinator := review.Coordinator{Store: durableReviewStore{store: fixture.store, project: project, repository: "team/repo", now: now}, Backend: backend, Now: now}
	op, err := coordinator.Start(ctx, review.Request{Repository: "team/repo", PullNumber: 12, Head: head, Base: base, BaseRef: "main", Body: "fixture body", Provider: "codex"})
	if err != nil || op.State != "completed" || !op.Submitted || backend.submits != 1 {
		t.Fatalf("completed request-changes operation=%+v err=%v backend=%+v", op, err, backend)
	}

	restarted, err := newDaemon(fixture.store, func() time.Time { return time.Unix(1012, 0) })
	if err != nil {
		t.Fatal(err)
	}
	customerMode(t, fixture)
	restarted.github, restarted.reviewBackend = fixture.daemon.github, func(string, uint64) review.Backend { return backend }
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
	if recovered.ID != op.ID || recovered.State != "completed" {
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
	page, err := store.Production(context.Background(), project, 0, 8, kernel.UnixMillis{})
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

func publishedReviewRequest() api.ReviewRequest {
	return api.ReviewRequest{Repository: "team/repo", PullNumber: 12, Head: strings.Repeat("e", 40), Base: strings.Repeat("f", 40), BaseRef: "main", Provider: "codex"}
}

func TestSendBackToARunningTaskLandsOnALaterTick(t *testing.T) {
	fixture, project, task, settle := publishedTask(t)
	customerMode(t, fixture)
	ctx := context.Background()
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return &publicReviewBackend{requestChanges: true} }
	if _, err := reviewNow(ctx, fixture.daemon, project, publishedReviewRequest()); err != nil {
		t.Fatal(err)
	}
	waitForMergePipeline(t, fixture.daemon)
	for _, settled := range []bool{false, true} {
		if settled {
			settle()
		}
		if _, err := fixture.daemon.advanceReviewOperations(ctx, false); err != nil {
			t.Fatal(err)
		}
		op := lastDurableReview(t, fixture.store, project)
		current, _, err := fixture.store.Task(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		routed := strings.Contains(kernel.TaskFeedback(current), "review-operation: "+op.ID+"\n")
		if routed != settled || (current.WorkRevision.Int64() == 2) != settled {
			t.Fatalf("settled=%v: operation=%+v task revision %d routed=%v", settled, op, current.WorkRevision.Int64(), routed)
		}
	}
}

// #1078: the task handed a head with a BLOCK and a later ALLOW that does not
// correct it receives the BLOCK findings as its correction input. (The kernel
// test TestPublishedReviewSendBackReachesTheWorkerNotThePublisher shows that
// task is the worker's when the overseer published the worker's Change.)
func TestSendBackCarriesTheBlockOfRecordDespiteALaterAllow(t *testing.T) {
	fixture, project, task, settle := publishedTask(t)
	settle()
	ctx := context.Background()
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return &publicReviewBackend{requestChanges: true} }
	block, err := reviewNow(ctx, fixture.daemon, project, publishedReviewRequest())
	if err != nil {
		t.Fatal(err)
	}
	head := strings.Repeat("e", 40)
	if err := fixture.store.RecordProductionReview(ctx, project, "team/repo", 12, kernel.ProductionReview{Head: head, State: "allow", Findings: "looks fine", OperationID: "later-allow"}, mustKernelTime(t, 2000)); err != nil {
		t.Fatal(err)
	}
	customerMode(t, fixture)
	if _, err := fixture.daemon.advanceReviewOperations(ctx, false); err != nil {
		t.Fatal(err)
	}
	current, _, err := fixture.store.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	feedback := kernel.TaskFeedback(current)
	for _, want := range []string{"review-operation: " + block + "\n", "This is the review of record for exact head " + head, "No other verdict on this head supersedes its findings", "fixture findings"} {
		if !strings.Contains(feedback, want) {
			t.Fatalf("worker feedback lacks %q:\n%s", want, feedback)
		}
	}
	if review := readProductionPull(t, fixture.store, project, 12).Review; review.State != "block" || review.OperationID != block {
		t.Fatalf("production review of record = %+v, want the block %s", review, block)
	}
}

// #1706: a task that answers a block without a new head gets one more
// review of that head, which reads its answer. A block it upholds goes back
// with both positions; a second answer is the overseer's, once.
func TestADisputedBlockIsReviewedOnceMoreThenEscalatedOnce(t *testing.T) {
	fixture, project, task, settle := publishedTask(t)
	settle()
	customerMode(t, fixture)
	ctx := context.Background()
	backend := &publicReviewBackend{requestChanges: true}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	var offset atomic.Int64
	now := fixture.daemon.now
	fixture.daemon.now = func() time.Time { return now().Add(time.Duration(offset.Load())) }
	current := func() kernel.Task {
		current, _, err := fixture.store.Task(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		return current
	}
	// Each tick is a later reconciler pass; the review it launches wakes one
	// more, which this waits out.
	tick := func(revision int64) {
		offset.Add(int64(time.Minute))
		waitForMergePipeline(t, fixture.daemon)
		if _, err := fixture.daemon.advanceReviewOperations(ctx, false); err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(2 * time.Second); current().WorkRevision.Int64() != revision && time.Now().Before(deadline); {
			time.Sleep(5 * time.Millisecond)
		}
		waitForMergePipeline(t, fixture.daemon)
	}
	if _, err := reviewNow(ctx, fixture.daemon, project, publishedReviewRequest()); err != nil {
		t.Fatal(err)
	}
	tick(2)
	if sent := current(); sent.WorkRevision.Int64() != 2 || !strings.Contains(kernel.TaskFeedback(sent), "fixture findings") || backend.reviews != 1 {
		t.Fatalf("block send-back: task revision %d, reviews %d", sent.WorkRevision.Int64(), backend.reviews)
	}
	admitPublished(t, fixture, 230, fixture.daemon.now().UnixMilli()+1)() // answered, no new head
	answer := current().Result
	tick(3)
	upheld := current()
	if backend.reviews != 2 || upheld.WorkRevision.Int64() != 3 || !strings.Contains(kernel.TaskFeedback(upheld), "fixture findings") || answer != "" && !strings.Contains(kernel.TaskFeedback(upheld), answer) {
		t.Fatalf("disputed block: reviews %d, task revision %d, feedback %q", backend.reviews, upheld.WorkRevision.Int64(), kernel.TaskFeedback(upheld))
	}
	admitPublished(t, fixture, 240, fixture.daemon.now().UnixMilli()+1)() // answered again
	for range 2 {
		tick(3)
	}
	open, err := fixture.store.OpenPulls(ctx)
	if err != nil || len(open) != 1 {
		t.Fatalf("open pulls = %+v, %v", open, err)
	}
	escalated := 0
	for _, document := range open[0].Operations {
		if strings.Contains(string(document), "answered the send-back of operation") {
			escalated++
		}
	}
	if escalated != 1 || backend.reviews != 2 || current().WorkRevision.Int64() != 3 {
		t.Fatalf("second answer: escalated %d times, reviews %d, task revision %d", escalated, backend.reviews, current().WorkRevision.Int64())
	}
}

func TestMergeQueueEjectionSendsBackOnceAndAMergeCloses(t *testing.T) {
	conflicting := false
	for _, test := range []struct {
		pull  review.Pull
		state string
		note  string
	}{
		{pull: review.Pull{State: "open", Failing: []string{"ci / go", "ci / ui"}}, state: "enqueued", note: "Failing checks: ci / go, ci / ui."},
		// #1376: a conflicting pull request whose Change factoryd cannot
		// rebase goes back for rebase once.
		{pull: review.Pull{State: "open", Mergeable: &conflicting}, state: "enqueued", note: "conflicts with main. Rebase this Change onto origin/main"},
		{pull: review.Pull{State: "merged"}, state: "merged"},
	} {
		fixture, project, task, settle := publishedTask(t)
		settle()
		customerMode(t, fixture)
		ctx := context.Background()
		backend := &publicReviewBackend{}
		fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
		if _, err := reviewNow(ctx, fixture.daemon, project, publishedReviewRequest()); err != nil {
			t.Fatalf("enqueue err=%v backend=%+v", err, backend)
		}
		waitForMergePipeline(t, fixture.daemon)
		if backend.enqueues != 1 {
			t.Fatalf("initial enqueue count=%d", backend.enqueues)
		}
		test.pull.Head = publishedReviewRequest().Head
		backend.pull = &test.pull
		for range 2 {
			if _, err := fixture.daemon.advanceReviewOperations(ctx, false); err != nil {
				t.Fatal(err)
			}
		}
		op := lastDurableReview(t, fixture.store, project)
		current, _, err := fixture.store.Task(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		sentBack := current.WorkRevision.Int64() == 2 && test.note != "" && strings.Contains(kernel.TaskFeedback(current), test.note)
		if op.State != test.state || sentBack != (test.note != "") || current.WorkRevision.Int64() > 2 || backend.enqueues != 1 {
			t.Fatalf("%s: operation=%+v task revision %d feedback %q", test.state, op, current.WorkRevision.Int64(), kernel.TaskFeedback(current))
		}
	}
}

func TestReviewVerdictWakesMergePipelinePastRefreshGate(t *testing.T) {
	fixture, project, _, settle := publishedTask(t)
	settle()
	customerMode(t, fixture)
	ctx := context.Background()
	backend := &publicReviewBackend{}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	fixture.daemon.pipelineAt.Store(time.Now().Add(productionRefreshInterval).UnixNano())

	if _, err := reviewNow(ctx, fixture.daemon, project, publishedReviewRequest()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for backend.enqueueSignal.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if backend.enqueueSignal.Load() != 1 {
		t.Fatalf("verdict wake enqueues=%d, want 1", backend.enqueueSignal.Load())
	}
}

// An ejection with no failing check on the head is re-queued once by
// factoryd; a second ejection goes back to the worker naming the merge
// group's failures.
func TestMergeQueueEjectionWithNoHeadFailureRequeuesOnceThenSendsBack(t *testing.T) {
	fixture, project, task, settle := publishedTask(t)
	settle()
	customerMode(t, fixture)
	ctx := context.Background()
	backend := &publicReviewBackend{}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	if _, err := reviewNow(ctx, fixture.daemon, project, publishedReviewRequest()); err != nil {
		t.Fatalf("enqueue err=%v backend=%+v", err, backend)
	}
	waitForMergePipeline(t, fixture.daemon)
	if backend.enqueues != 1 {
		t.Fatalf("initial enqueue count=%d", backend.enqueues)
	}
	backend.pull = &review.Pull{Head: publishedReviewRequest().Head, State: "open", Group: &review.GroupRun{ID: 37516424704, Conclusion: "failure", Jobs: []review.GroupJob{{Name: "checks", Conclusion: "failure", Annotations: []string{"not ok 3 - board and shelves open peer views of one Library workspace"}}}}}
	if _, err := fixture.daemon.advanceReviewOperations(ctx, false); err != nil {
		t.Fatal(err)
	}
	if op := lastDurableReview(t, fixture.store, project); op.State != "enqueued" || op.Enqueues != 2 || backend.enqueues != 2 {
		t.Fatalf("re-queue operation=%+v enqueues=%d", op, backend.enqueues)
	}
	for range 3 {
		if _, err := fixture.daemon.advanceReviewOperations(ctx, false); err != nil {
			t.Fatal(err)
		}
	}
	op := lastDurableReview(t, fixture.store, project)
	current, _, err := fixture.store.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if op.State != "enqueued" || backend.enqueues != 2 || current.WorkRevision.Int64() != 2 || !strings.Contains(kernel.TaskFeedback(current), "board and shelves open peer views") {
		t.Fatalf("second ejection operation=%+v enqueues=%d task revision %d", op, backend.enqueues, current.WorkRevision.Int64())
	}
}

// An enqueue the App keeps refusing as UNPROCESSABLE is retried a bounded
// number of passes, then becomes one escalated overseer item and waits for
// the pull request to change.
func TestRefusedEnqueueBecomesAnOverseerItem(t *testing.T) {
	fixture, project, _, settle := publishedTask(t)
	settle()
	customerMode(t, fixture)
	ctx := context.Background()
	backend := &publicReviewBackend{enqueueRefusal: "UNPROCESSABLE"}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	if _, err := reviewNow(ctx, fixture.daemon, project, publishedReviewRequest()); err != nil {
		t.Fatal(err)
	}
	waitForMergePipeline(t, fixture.daemon)
	for range review.FailuresBeforeEscalation {
		if _, err := fixture.daemon.advanceReviewOperations(ctx, false); err != nil {
			t.Fatal(err)
		}
	}
	op := lastDurableReview(t, fixture.store, project)
	if op.State != "enqueued" || op.Handled || backend.enqueues != review.FailuresBeforeEscalation || op.Failures != review.FailuresBeforeEscalation || backend.reviews != 1 || !strings.Contains(op.Escalation, "could not enqueue it") || !strings.Contains(op.Escalation, "UNPROCESSABLE") {
		t.Fatalf("refused enqueue operation = %+v enqueues=%d reviews=%d", op, backend.enqueues, backend.reviews)
	}
	overseer, err := fixture.store.CreateAgent(ctx, kernel.NewAgent{ID: mustAgentID(t, testID(234)), ProjectID: project, Name: "overseer", Role: kernel.RoleOrchestrator, Provider: kernel.ProviderCodex, ToolBudgetLimit: 2}, mustKernelTime(t, 1001))
	if err != nil {
		t.Fatal(err)
	}
	policy, after, instruction := kernel.IdleStandingInstruction, uint32(1), "Supervise."
	if _, err := fixture.store.UpdateAgent(ctx, overseer.ID, overseer.Revision, kernel.AgentPatch{IdlePolicy: &policy, IdleAfterSeconds: &after, IdleInstruction: &instruction}, mustKernelTime(t, 1002)); err != nil {
		t.Fatal(err)
	}
	wakes, err := fixture.store.EnqueueOverseerWakeups(ctx, mustKernelTime(t, fixture.daemon.now().Add(time.Minute).UnixMilli()))
	if err != nil || len(wakes) != 1 || !strings.Contains(wakes[0].Body, "Escalated: factoryd cannot advance team/repo#12") {
		t.Fatalf("escalation wake = %+v, %v", wakes, err)
	}
}

// An enqueue refused over an unresolved review thread is not retried: its
// task is sent back once with the thread (#1857).
func TestUnresolvedConversationRefusalSendsTheThreadBack(t *testing.T) {
	fixture, project, task, settle := publishedTask(t)
	settle()
	customerMode(t, fixture)
	ctx := context.Background()
	backend := &publicReviewBackend{enqueueRefusal: "UNPROCESSABLE: Pull request A conversation must be resolved before this pull request can be merged.\nUnresolved review threads:\n- cmd/factoryctl/main.go:719 by github-advanced-security: Incorrect conversion between integer types\n- web/x.ts:3 by baziyer: Handle RATE_LIMITED here"}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	if _, err := reviewNow(ctx, fixture.daemon, project, publishedReviewRequest()); err != nil {
		t.Fatal(err)
	}
	waitForMergePipeline(t, fixture.daemon)
	for range 3 {
		if _, err := fixture.daemon.advanceReviewOperations(ctx, false); err != nil {
			t.Fatal(err)
		}
	}
	current, _, err := fixture.store.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	op := lastDurableReview(t, fixture.store, project)
	if backend.enqueues != 1 || op.Failures != 0 || op.Escalation != "" || current.WorkRevision.Int64() != 2 || !strings.Contains(kernel.TaskFeedback(current), "cmd/factoryctl/main.go:719 by github-advanced-security: Incorrect conversion") {
		t.Fatalf("conversation refusal operation=%+v enqueues=%d task revision %d feedback %q", op, backend.enqueues, current.WorkRevision.Int64(), kernel.TaskFeedback(current))
	}
}

func TestCodeownersRefusalEscalatesAndRecoversAfterApproval(t *testing.T) {
	fixture, project, _, settle := publishedTask(t)
	settle()
	customerMode(t, fixture)
	backend := &publicReviewBackend{enqueueRefusal: "UNPROCESSABLE: Waiting on code owner review from baziyer"}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	if _, err := reviewNow(context.Background(), fixture.daemon, project, publishedReviewRequest()); err != nil {
		t.Fatal(err)
	}
	waitForMergePipeline(t, fixture.daemon)
	op := lastDurableReview(t, fixture.store, project)
	if !op.OwnerApproval || backend.enqueues != 1 || !strings.Contains(op.Escalation, "#12") || !strings.Contains(op.Escalation, "(attempt 1): ") || !strings.Contains(op.Escalation, "Waiting on code owner review") {
		t.Fatalf("owner approval escalation = %+v enqueues=%d", op, backend.enqueues)
	}
	// #1698: the refused head waits for its owner instead of re-enqueueing every pass.
	backend.enqueueRefusal = ""
	if _, err := fixture.daemon.advanceReviewOperations(context.Background(), false); err != nil || backend.enqueues != 1 {
		t.Fatalf("unapproved head re-enqueued: enqueues=%d err=%v", backend.enqueues, err)
	}
	backend.pull = &review.Pull{Head: op.Request.Head, State: "open", Review: "APPROVED", Checks: []string{"required=success"}}
	if _, err := fixture.daemon.advanceReviewOperations(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if backend.enqueues != 2 || lastDurableReview(t, fixture.store, project).OwnerApproval {
		t.Fatalf("owner approval did not recover after retry: enqueues=%d", backend.enqueues)
	}
}

// customerMode puts the daemon on the factoryd Maintainer path with an offline
// customer connection, so no fixture can reach GitHub.
func customerMode(t *testing.T, fixture *dispatchFixture) {
	t.Helper()
	if err := fixture.home.WriteMaintainerCredential([]byte(`{"disabled":true}`)); err != nil {
		t.Fatal(err)
	}
	host, err := maintainer.OpenHost(fixture.home)
	if err != nil || !host.CustomerMode() {
		t.Fatalf("customer host: %v", err)
	}
	fixture.daemon.github = host
}

// The merge stage is dormant on an unconnected home: neither the tick nor
// startup routes a review_pr result or observes an enqueued head. On the
// factoryd Maintainer path both advance.
func TestMergeStageRunsOnlyOnTheCustomerPath(t *testing.T) {
	for _, customer := range []bool{false, true} {
		fixture, project, task, settle := publishedTask(t)
		ctx := context.Background()
		fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return &publicReviewBackend{requestChanges: true} }
		if _, err := reviewNow(ctx, fixture.daemon, project, publishedReviewRequest()); err != nil {
			t.Fatal(err)
		}
		waitForMergePipeline(t, fixture.daemon)
		settle()
		if customer {
			customerMode(t, fixture)
		}
		fixture.daemon.advanceMergePipeline(ctx)
		if current, _, err := fixture.store.Task(ctx, task.ID); err != nil || (current.WorkRevision.Int64() == 2) != customer {
			t.Fatalf("customer=%v: tick left task revision %d (err %v)", customer, current.WorkRevision.Int64(), err)
		}

		fixture, project, _, settle = publishedTask(t)
		settle()
		backend := &publicReviewBackend{}
		fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
		if _, err := reviewNow(ctx, fixture.daemon, project, publishedReviewRequest()); err != nil {
			t.Fatal(err)
		}
		waitForMergePipeline(t, fixture.daemon)
		backend.pull = &review.Pull{Head: publishedReviewRequest().Head, State: "merged"}
		if customer {
			customerMode(t, fixture)
		}
		if _, err := fixture.daemon.RecoverReviewOperations(ctx); err != nil {
			t.Fatal(err)
		}
		if state := lastDurableReview(t, fixture.store, project).State; (state == "merged") != customer {
			t.Fatalf("customer=%v: startup left operation %s", customer, state)
		}
	}
}

// heldBackend holds the next call of its held method open until release, as
// a slow GitHub read holds a reconcile or a slow provider holds a review.
type heldBackend struct {
	*publicReviewBackend
	held             string
	armed            atomic.Bool
	entered, release chan struct{}
}

func newHeldBackend(held string) *heldBackend {
	b := &heldBackend{publicReviewBackend: &publicReviewBackend{}, held: held, entered: make(chan struct{}), release: make(chan struct{})}
	b.armed.Store(true)
	return b
}

func (b *heldBackend) hold(method string) {
	if method == b.held && b.armed.CompareAndSwap(true, false) {
		close(b.entered)
		<-b.release
	}
}

func (b *heldBackend) ObservePull(ctx context.Context, operation review.Operation) (review.Pull, error) {
	b.hold("ObservePull")
	return b.publicReviewBackend.ObservePull(ctx, operation)
}

func (b *heldBackend) Review(ctx context.Context, clone string, request review.Request) (review.Verdict, error) {
	b.hold("Review")
	return b.publicReviewBackend.Review(ctx, clone, request)
}

func waitHeld(t *testing.T, b *heldBackend) {
	t.Helper()
	select {
	case <-b.entered:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s was never called", b.held)
	}
}

// Startup recovery runs after factoryd's listener opens (#1830), so a review
// the API accepted can be running when recovery reads it. Recovery takes over
// only reviews the previous daemon left, so this one's provider runs once.
func TestStartupRecoveryDoesNotRelaunchAReviewThisDaemonAccepted(t *testing.T) {
	fixture, project := reviewPublicFixture(t)
	ctx := context.Background()
	// This daemon booted at its fixture clock, before it accepted the review.
	fixture.daemon.bootedAt = fixture.daemon.now()
	backend := newHeldBackend("Review")
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	input := api.IntakeInput{Action: "review_pr", ProjectID: project.String(), ReviewRequest: &api.ReviewRequest{Repository: "team/repo", PullNumber: 7, Head: strings.Repeat("a", 40), Base: strings.Repeat("b", 40), BaseRef: "main", Provider: "codex"}}
	if result := fixture.daemon.Intake(ctx, input); result.State != "ok" || result.ReviewOperation == "" {
		t.Fatalf("review_pr answered %+v", result)
	}
	waitHeld(t, backend)
	count, err := fixture.daemon.RecoverReviewOperations(ctx)
	close(backend.release)
	if err != nil || count != 0 {
		t.Fatalf("startup recovery took over a review this daemon accepted: count=%d err=%v", count, err)
	}
	op := waitForDurableReview(t, fixture.store, project, func(op review.Operation) bool { return op.State == "enqueued" })
	if backend.reviews != 1 || backend.submits != 1 {
		t.Fatalf("operation=%+v backend=%+v", op, backend.publicReviewBackend)
	}
}

// factoryd's listener opens before startup review recovery (#1830), so a
// review_pr sent through the local API is accepted and reviewed while
// recovery's reconcile of another open pull request is still blocked on
// GitHub, and each review's provider runs once.
func TestAPIAnswersWhileStartupReviewReconcileIsBlocked(t *testing.T) {
	fixture, project, _, settle := publishedTask(t)
	settle()
	ctx := context.Background()
	backend := newHeldBackend("ObservePull")
	backend.armed.Store(false)
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	if _, err := reviewNow(ctx, fixture.daemon, project, publishedReviewRequest()); err != nil {
		t.Fatal(err)
	}
	waitForMergePipeline(t, fixture.daemon)
	customerMode(t, fixture)
	backend.armed.Store(true)
	recovered := make(chan error, 1)
	go func() {
		_, err := fixture.daemon.RecoverReviewOperations(ctx)
		recovered <- err
	}()
	waitHeld(t, backend)

	client, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	done := fixture.serve(t)
	input := api.IntakeInput{Action: "review_pr", ProjectID: project.String(), ReviewRequest: &api.ReviewRequest{Repository: "team/repo", PullNumber: 7, Head: strings.Repeat("a", 40), Base: strings.Repeat("b", 40), BaseRef: "main", Provider: "codex"}}
	result, err := client.Intake(ctx, input)
	if err == nil {
		waitDispatch(t, done)
	}
	reviewed := false
	for deadline := time.Now().Add(5 * time.Second); err == nil && !reviewed && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		var op review.Operation
		document, found, readErr := fixture.store.ReviewOperation(ctx, project, result.ReviewOperation)
		reviewed = readErr == nil && found && json.Unmarshal(document, &op) == nil && op.Submitted
	}
	close(backend.release)
	if err != nil || result.State != "ok" || !reviewed {
		t.Fatalf("review_pr while review reconcile is blocked = %+v, %v (reviewed=%v)", result, err, reviewed)
	}
	if err := <-recovered; err != nil {
		t.Fatal(err)
	}
	waitForMergePipeline(t, fixture.daemon)
	if backend.reviews != 2 || backend.submits != 2 {
		t.Fatalf("a review's provider ran twice: backend=%+v", backend.publicReviewBackend)
	}
}

// A failed review never stalls its pull request silently: the tick retries a
// retryable failure once, and escalates a failure nothing will retry once,
// including one whose retry cannot start.
func TestFailedReviewRetriesOnceThenEscalatesOnce(t *testing.T) {
	fixture, project, task, settle := publishedTask(t)
	settle()
	customerMode(t, fixture)
	ctx := context.Background()
	backend := &publicReviewBackend{killed: true}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	if _, err := reviewNow(ctx, fixture.daemon, project, publishedReviewRequest()); err == nil {
		t.Fatal("a killed provider reported success")
	}
	var offset atomic.Int64
	now := fixture.daemon.now
	fixture.daemon.now = func() time.Time { return now().Add(time.Duration(offset.Load())) }
	ops := func() (failed, handled int) {
		page, err := fixture.store.Production(ctx, project, 0, 8, kernel.UnixMillis{})
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range page.Records {
			var op review.Operation
			if record.Kind == "reviewer" && json.Unmarshal(record.Document, &op) == nil && op.State == "failed" {
				failed++
				if op.Handled {
					handled++
				}
			}
		}
		return failed, handled
	}
	tick := func() {
		offset.Add(int64(2 * reviewStuckAfter))
		if _, err := fixture.daemon.advanceReviewOperations(ctx, false); err != nil {
			t.Fatal(err)
		}
	}
	// The failed review's own wake and these ticks retry it once, then
	// escalate the failed retry once.
	deadline := time.Now().Add(2 * time.Second)
	for failed, handled := ops(); (failed != 2 || handled != 2) && time.Now().Before(deadline); failed, handled = ops() {
		tick()
		waitForMergePipeline(t, fixture.daemon)
		time.Sleep(5 * time.Millisecond)
	}
	for range 2 {
		tick()
	}
	page, err := fixture.store.Production(ctx, project, 0, 8, kernel.UnixMillis{})
	if err != nil {
		t.Fatal(err)
	}
	escalated := 0
	for _, record := range page.Records {
		var op review.Operation
		if record.Kind == "reviewer" && json.Unmarshal(record.Document, &op) == nil && strings.Contains(op.Escalation, "its review failed") {
			escalated++
		}
	}
	if escalated != 1 {
		t.Fatalf("exhausted retry escalated %d times, want once", escalated)
	}
	if failed, handled := ops(); failed != 2 || handled != 2 || backend.reviews != 2 {
		t.Fatalf("after escalation: failed=%d handled=%d reviews=%d", failed, handled, backend.reviews)
	}
	if acted, err := fixture.daemon.advanceReviewOperations(ctx, false); err != nil || acted != 0 {
		t.Fatalf("handled failures are still acted on: %d %v", acted, err)
	}

	// A repository the project does not bind cannot be reviewed again: its
	// reconciler escalates once it has failed for the bounded passes.
	head := strings.Repeat("e", 40)
	if err := fixture.store.RecordPublication(ctx, project, task.ID, "team/unbound", kernel.ProductionPullRequest{Number: 12, Title: "Ship it", URL: "https://github.com/team/unbound/pull/12", Head: head, Branch: "feature/ship", Base: "main", State: "open", Review: kernel.ProductionReview{Head: head, State: "unknown"}}, mustKernelTime(t, 1011)); err != nil {
		t.Fatal(err)
	}
	request := publishedReviewRequest()
	unbound := review.Operation{ID: "unbound-failure", Request: review.Request{Repository: "team/unbound", PullNumber: 12, Head: head, Base: request.Base, BaseRef: "main", Body: "fixture body", Provider: "codex"}, State: "failed", Retryable: true, Detail: "provider killed at launch", CreatedAt: fixture.daemon.now(), UpdatedAt: fixture.daemon.now()}
	if err := (durableReviewStore{store: fixture.store, project: project, repository: "team/unbound", now: fixture.daemon.now}).Create(ctx, unbound); err != nil {
		t.Fatal(err)
	}
	for range review.FailuresBeforeEscalation {
		tick()
	}
	if document, _, err := fixture.store.ReviewOperation(ctx, project, unbound.ID); err != nil || !strings.Contains(string(document), "could not observe the pull request") || backend.reviews != 2 {
		t.Fatalf("unbound failure left unescalated: %s %v reviews=%d", document, err, backend.reviews)
	}
}

// A verdict write whose resume keeps failing is escalated to the overseer
// once it has not advanced for 30 minutes, and is still resumed afterwards.
func TestStuckSubmitEscalatesOnceAndKeepsResuming(t *testing.T) {
	fixture, project, _, settle := publishedTask(t)
	settle()
	customerMode(t, fixture)
	ctx := context.Background()
	backend := &publicReviewBackend{observeFailures: 1}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	var offset atomic.Int64
	now := fixture.daemon.now
	fixture.daemon.now = func() time.Time { return now().Add(time.Duration(offset.Load())) }
	request := publishedReviewRequest()
	stuck := review.Operation{ID: "stuck-submit", Verdict: "allow", Request: review.Request{Repository: request.Repository, PullNumber: request.PullNumber, Head: request.Head, Base: request.Base, BaseRef: request.BaseRef, Body: "fixture body", Provider: request.Provider}, State: "submitting", CreatedAt: fixture.daemon.now(), UpdatedAt: fixture.daemon.now()}
	if err := (durableReviewStore{store: fixture.store, project: project, repository: "team/repo", now: fixture.daemon.now}).Create(ctx, stuck); err != nil {
		t.Fatal(err)
	}
	tick := func(after time.Duration) review.Operation {
		offset.Add(int64(after))
		if _, err := fixture.daemon.advanceReviewOperations(ctx, false); err != nil {
			t.Fatal(err)
		}
		return lastDurableReview(t, fixture.store, project)
	}
	if op := tick(3 * time.Minute); op.Escalation != "" || backend.observeFailures != 2 {
		t.Fatalf("a young stuck write escalated: %+v (observes %d)", op, backend.observeFailures-1)
	}
	if op := tick(30 * time.Minute); op.State != "submitting" || !strings.Contains(op.Escalation, "submitting write has not advanced for 30 minutes: observe_operation 401 #2") {
		t.Fatalf("stuck write was not escalated: %+v", op)
	}
	if op := tick(3 * time.Minute); op.State != "submitting" || !strings.Contains(op.Escalation, "401 #2") || backend.observeFailures != 4 {
		t.Fatalf("stuck write re-escalated or stopped resuming: %+v (observes %d)", op, backend.observeFailures-1)
	}
}

// A head with no review whose review cannot be claimed is retried within the
// bound like any other action, then escalated once.
func TestAnUnclaimableHeadReviewEscalatesOnce(t *testing.T) {
	fixture, project, _, settle := publishedTask(t)
	settle()
	customerMode(t, fixture)
	ctx := context.Background()
	backend := &publicReviewBackend{storedErr: errors.New("list_pull_requests unavailable")}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	for range review.FailuresBeforeEscalation + 2 {
		if _, err := fixture.daemon.advanceReviewOperations(ctx, false); err != nil {
			t.Fatal(err)
		}
	}
	op := lastDurableReview(t, fixture.store, project)
	if op.State != "unreviewed" || op.Failures != review.FailuresBeforeEscalation || backend.reviews != 0 || !strings.Contains(op.Escalation, "could not review it (attempt 6): list_pull_requests unavailable") {
		t.Fatalf("unclaimable head = %+v reviews=%d", op, backend.reviews)
	}
}

// An enqueue GitHub refuses in a way a later attempt may pass (RATE_LIMITED,
// #1510) with every required check passed is resent each tick without
// escalating within its bound; it is queued when GitHub accepts it, and ends
// when its pull request closes.
func TestRefusedEnqueueResendsEachTickAndEndsFromThePull(t *testing.T) {
	fixture, project, _, settle := publishedTask(t)
	settle()
	customerMode(t, fixture)
	ctx := context.Background()
	backend := &publicReviewBackend{enqueueRefusal: "RATE_LIMITED", checks: []string{"checks=success", "review=skipped"}}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	if _, err := reviewNow(ctx, fixture.daemon, project, publishedReviewRequest()); err != nil {
		t.Fatal(err)
	}
	waitForMergePipeline(t, fixture.daemon)
	// reviewNow's verdict wake makes the first attempt; explicit ticks below
	// verify the retries.
	if backend.enqueues != 1 {
		t.Fatalf("initial enqueue count=%d", backend.enqueues)
	}
	tick := func() review.Operation {
		if _, err := fixture.daemon.advanceReviewOperations(ctx, false); err != nil {
			t.Fatal(err)
		}
		return lastDurableReview(t, fixture.store, project)
	}
	if op := tick(); op.State != "enqueued" || op.Escalation != "" || op.Failures != 2 || backend.enqueues != 2 || backend.reviews != 1 {
		t.Fatalf("refused again: %+v (enqueues %d)", op, backend.enqueues)
	}
	backend.enqueueRefusal = ""
	if op := tick(); op.State != "enqueued" || op.Failures != 0 || op.Enqueues != 1 || backend.enqueues != 3 || backend.reviews != 1 {
		t.Fatalf("accepted: %+v (enqueues %d)", op, backend.enqueues)
	}
	backend.pull = &review.Pull{Head: publishedReviewRequest().Head, State: "closed"}
	if op := tick(); op.State != "closed" || backend.enqueues != 3 {
		t.Fatalf("closed pull: %+v (enqueues %d)", op, backend.enqueues)
	}
}

// A pull request no factory task published (a host or human author's) gets
// one shared repair task for its REQUEST_CHANGES (#1518), not an escalation.
func TestRequestChangesOnAPullNoTaskPublishedGetsOneRepairTask(t *testing.T) {
	fixture, project, _, settle := publishedTask(t)
	settle()
	customerMode(t, fixture)
	ctx := context.Background()
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return &publicReviewBackend{requestChanges: true} }
	request := publishedReviewRequest()
	request.PullNumber = 99
	id, err := reviewNow(ctx, fixture.daemon, project, request)
	if err != nil {
		t.Fatal(err)
	}
	waitForMergePipeline(t, fixture.daemon)
	for range 2 {
		if _, err := fixture.daemon.advanceReviewOperations(ctx, false); err != nil {
			t.Fatal(err)
		}
	}
	open, err := fixture.store.OpenPulls(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, pull := range open {
		if pull.Pull.Number != 99 {
			continue
		}
		repair, _, err := fixture.store.Task(ctx, pull.Task)
		if err != nil || repair.WorkRevision.Int64() != 1 || !strings.Contains(kernel.TaskFeedback(repair), "review-operation: "+id+"\n") {
			t.Fatalf("repair task = %+v, %v", repair, err)
		}
		if document, _, _ := fixture.store.ReviewOperation(ctx, project, id); strings.Contains(string(document), "escalation") {
			t.Fatalf("host pull escalated: %s", document)
		}
		return
	}
	t.Fatalf("host pull #99 not open: %+v", open)
}

// The caller supplies no body: the review binds the body GitHub stores,
// trailing newline included.
func TestReviewBindsTheStoredBody(t *testing.T) {
	fixture, project := reviewPublicFixture(t)
	backend := &publicReviewBackend{}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	if _, err := reviewNow(context.Background(), fixture.daemon, project, publishedReviewRequest()); err != nil || lastDurableReview(t, fixture.store, project).Request.Body != "fixture body\n" {
		t.Fatalf("err=%v reviewed body %q", err, lastDurableReview(t, fixture.store, project).Request.Body)
	}
}

// Only the checks the base branch's rules require gate the merge: an optional
// check that fails (CodeQL), is cancelled or never finishes decides nothing,
// though a failed shard's failure lines name the failing tests, redacted.
func TestOnlyRequiredChecksGateTheMerge(t *testing.T) {
	var pull review.Pull
	err := requiredChecks(json.RawMessage(`{"checks":[
		{"name":"checks","conclusion":"success","required":true},
		{"name":"review","conclusion":"skipped","required":true},
		{"name":"CodeQL","conclusion":"failure","required":false},
		{"name":"stale lint","conclusion":"cancelled","required":false},
		{"name":"preview","conclusion":null,"required":false}]}`), &pull)
	if err != nil || len(pull.Failing) != 0 || pull.Pending || strings.Join(pull.Checks, ",") != "checks=success,review=skipped" {
		t.Fatalf("optional checks decided: %+v err=%v", pull, err)
	}
	pull = review.Pull{}
	err = requiredChecks(json.RawMessage(`{"checks":[
		{"name":"checks (source)","conclusion":"failure","required":false,"annotations":["--- FAIL: TestX (0.01s)","x_test.go:3: token=ghs_live"]},
		{"name":"checks","conclusion":"failure","required":true},
		{"name":"ui","conclusion":null,"required":true}]}`), &pull)
	if err != nil || strings.Join(pull.Failing, ",") != "checks" || !pull.Pending || strings.Join(pull.Checks, ",") != "checks=failure,ui=pending" || strings.Join(pull.Tests, ",") != "--- FAIL: TestX (0.01s),x_test.go:3: **************" {
		t.Fatalf("required checks ignored: %+v err=%v", pull, err)
	}
}

func TestRequiredCheckAppearanceChangesObservation(t *testing.T) {
	var missing, passing review.Pull
	if err := requiredChecks(json.RawMessage(`{"checks":[]}`), &missing); err != nil || missing.Pending || len(missing.Failing) != 0 || len(missing.Checks) != 0 {
		t.Fatalf("missing check = %+v err=%v", missing, err)
	}
	if err := requiredChecks(json.RawMessage(`{"checks":[{"name":"checks","status":"completed","conclusion":"success","required":true}]}`), &passing); err != nil || passing.Pending || len(passing.Failing) != 0 || strings.Join(passing.Checks, ",") != "checks=success" {
		t.Fatalf("passing check = %+v err=%v", passing, err)
	}
	if strings.Join(missing.Checks, ",") == strings.Join(passing.Checks, ",") {
		t.Fatal("missing and passing required checks had the same observation")
	}
}
