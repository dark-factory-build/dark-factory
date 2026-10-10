package daemon

import (
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
	enqueueRefusal                          string // the broker's tool error text, if any
	reviews, submits, enqueues              int
	enqueuedBase, enqueuedSHA, enqueuedBody string
	journal                                 map[string]string
	observations                            map[string]review.Receipt
	pull                                    *review.Pull
	observeFailures                         int
}

func (b *publicReviewBackend) StoredPull(context.Context, uint64, string) (review.Request, error) {
	return review.Request{Body: "fixture body\n"}, nil
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
	b.enqueuedBase, b.enqueuedSHA, b.enqueuedBody = operation.Request.BaseRef, operation.Request.Base, operation.Request.Body
	if b.enqueueRefusal != "" {
		return maintainerRejection(b.enqueueRefusal)
	}
	b.queued = true
	return nil
}

// ObservePull defaults to the pull request open at the operation's head with
// every check passed, queued once it has been enqueued.
func (b *publicReviewBackend) ObservePull(_ context.Context, operation review.Operation) (review.Pull, error) {
	if b.pull != nil {
		return *b.pull, nil
	}
	return review.Pull{Head: operation.Request.Head, State: "open", Queued: b.queued}, nil
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
	if result.State != "ok" || backend.reviews != 2 || backend.submits != 1 || backend.enqueues != 1 || len(backend.journal) != 1 {
		t.Fatalf("retry result=%+v operation=%+v backend=%+v", result, lastDurableReview(t, fixture.store, project), backend)
	}
	if result := fixture.daemon.Intake(context.Background(), retry); result.State == "ok" || backend.reviews != 2 || backend.submits != 1 || backend.enqueues != 1 {
		t.Fatalf("original failure was replayed after its one retry: result=%+v backend=%+v", result, backend)
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
	if _, err := reviewNow(context.Background(), fixture.daemon, project, request); err != nil || backend.enqueues != 1 {
		t.Fatalf("allow at a new head: err=%v enqueues=%d", err, backend.enqueues)
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
	if recovered.State != "failed" || recovered.RoutePending || recovered.Submitted {
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
	head := strings.Repeat("e", 40)
	pr := kernel.ProductionPullRequest{Number: 12, Title: "Ship it", URL: "https://github.com/team/repo/pull/12", Head: head, Branch: "feature/ship", Base: "main", State: "open", Review: kernel.ProductionReview{Head: head, State: "unknown"}}
	if err := fixture.store.RecordPublication(ctx, project, task.ID, "team/repo", pr, mustKernelTime(t, 1010)); err != nil {
		t.Fatal(err)
	}
	settle = func() {
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
	}
	fixture.daemon.now = func() time.Time { return time.Unix(2000, 0) }
	return fixture, project, task, settle
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
	ctx := context.Background()
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return &publicReviewBackend{requestChanges: true} }
	if _, err := reviewNow(ctx, fixture.daemon, project, publishedReviewRequest()); err == nil {
		t.Fatal("a running task accepted the send-back")
	}
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
		if op.RoutePending == settled || routed != settled || (current.WorkRevision.Int64() == 2) != settled {
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

func TestMergeQueueEjectionSendsBackOnceAndAMergeCloses(t *testing.T) {
	conflicting := false
	for _, test := range []struct {
		pull  review.Pull
		state string
		note  string
	}{
		{pull: review.Pull{State: "open", Failing: []string{"ci / go", "ci / ui"}}, state: "ejected", note: "Failing checks: ci / go, ci / ui."},
		// #1376: a conflicting pull request goes back for rebase once.
		{pull: review.Pull{State: "open", Mergeable: &conflicting}, state: "ejected", note: "conflicts with main. Rebase this Change onto origin/main"},
		{pull: review.Pull{State: "merged"}, state: "merged"},
	} {
		fixture, project, task, settle := publishedTask(t)
		settle()
		customerMode(t, fixture)
		ctx := context.Background()
		backend := &publicReviewBackend{}
		fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
		if _, err := reviewNow(ctx, fixture.daemon, project, publishedReviewRequest()); err != nil || backend.enqueues != 1 {
			t.Fatalf("enqueue err=%v backend=%+v", err, backend)
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
		if op.State != test.state || op.RoutePending || sentBack != (test.state == "ejected") || current.WorkRevision.Int64() > 2 || backend.enqueues != 1 {
			t.Fatalf("%s: operation=%+v task revision %d feedback %q", test.state, op, current.WorkRevision.Int64(), kernel.TaskFeedback(current))
		}
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
	if _, err := reviewNow(ctx, fixture.daemon, project, publishedReviewRequest()); err != nil || backend.enqueues != 1 {
		t.Fatalf("enqueue err=%v backend=%+v", err, backend)
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
	if op.State != "ejected" || op.RoutePending || backend.enqueues != 2 || current.WorkRevision.Int64() != 2 || !strings.Contains(kernel.TaskFeedback(current), "board and shelves open peer views") {
		t.Fatalf("second ejection operation=%+v enqueues=%d task revision %d", op, backend.enqueues, current.WorkRevision.Int64())
	}
}

// An enqueue the Maintainer refuses permanently (invalid input: the same
// request meets the same answer) fails the head at once: never enqueued
// again, and escalated once as an item due to the project's overseer.
func TestRefusedEnqueueBecomesAnOverseerItem(t *testing.T) {
	fixture, project, _, settle := publishedTask(t)
	settle()
	customerMode(t, fixture)
	ctx := context.Background()
	backend := &publicReviewBackend{enqueueRefusal: "invalid_input: Operation input is invalid."}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	if _, err := reviewNow(ctx, fixture.daemon, project, publishedReviewRequest()); !errors.Is(err, review.ErrPermanent) {
		t.Fatalf("a refused enqueue reported %v", err)
	}
	now, offset := fixture.daemon.now, time.Duration(0)
	fixture.daemon.now = func() time.Time { return now().Add(offset) }
	for range 3 {
		offset += 2 * reviewStuckAfter
		if _, err := fixture.daemon.advanceReviewOperations(ctx, false); err != nil {
			t.Fatal(err)
		}
	}
	op := lastDurableReview(t, fixture.store, project)
	if op.State != "failed" || !op.Handled || op.Retryable || backend.enqueues != 1 || backend.reviews != 1 || !strings.Contains(op.Escalation, "its review failed: review: Maintainer rejected operation: invalid_input") {
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

// The merge stage is dormant on an unconnected home: the tick does not route a
// review_pr result, and startup routes it as before but does not observe an
// enqueued head. On the factoryd Maintainer path both advance.
func TestMergeStageRunsOnlyOnTheCustomerPath(t *testing.T) {
	for _, customer := range []bool{false, true} {
		fixture, project, task, settle := publishedTask(t)
		ctx := context.Background()
		fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return &publicReviewBackend{requestChanges: true} }
		if _, err := reviewNow(ctx, fixture.daemon, project, publishedReviewRequest()); err == nil {
			t.Fatal("a running task accepted the send-back")
		}
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
	tick()
	deadline := time.Now().Add(2 * time.Second)
	for failed, _ := ops(); failed != 2 && time.Now().Before(deadline); failed, _ = ops() {
		time.Sleep(5 * time.Millisecond)
	}
	if failed, handled := ops(); failed != 2 || handled != 1 || backend.reviews != 2 {
		t.Fatalf("after one tick: failed=%d handled=%d reviews=%d, want the one retry failed", failed, handled, backend.reviews)
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
	if pending, err := fixture.store.InFlightReviewOperations(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("handled failures are still in flight: %d %v", len(pending), err)
	}

	// A repository the project does not bind cannot start the retry.
	head := strings.Repeat("e", 40)
	if err := fixture.store.RecordPublication(ctx, project, task.ID, "team/unbound", kernel.ProductionPullRequest{Number: 12, Title: "Ship it", URL: "https://github.com/team/unbound/pull/12", Head: head, Branch: "feature/ship", Base: "main", State: "open", Review: kernel.ProductionReview{Head: head, State: "unknown"}}, mustKernelTime(t, 1011)); err != nil {
		t.Fatal(err)
	}
	request := publishedReviewRequest()
	unbound := review.Operation{ID: "unbound-failure", Request: review.Request{Repository: "team/unbound", PullNumber: 12, Head: head, Base: request.Base, BaseRef: "main", Body: "fixture body", Provider: "codex"}, State: "failed", Retryable: true, Detail: "provider killed at launch", CreatedAt: fixture.daemon.now(), UpdatedAt: fixture.daemon.now()}
	if err := (durableReviewStore{store: fixture.store, project: project, repository: "team/unbound", now: fixture.daemon.now}).Create(ctx, unbound); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		tick()
	}
	if document, _, err := fixture.store.ReviewOperation(ctx, project, unbound.ID); err != nil || !strings.Contains(string(document), `"handled":true`) || !strings.Contains(string(document), "its retry could not start") || backend.reviews != 2 {
		t.Fatalf("unbound failure left unhandled: %s %v reviews=%d", document, err, backend.reviews)
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

// #1531: GitHub refused an enqueue as UNPROCESSABLE until the owner's
// CODEOWNERS approval arrived. Such a refusal names a precondition that can
// come to hold, so it is resent each tick, escalated once when it has
// persisted for FailuresBeforeEscalation passes, and the same operation is
// queued when GitHub accepts it, then ends when its pull request closes.
func TestRefusedEnqueueResendsEachTickAndEndsFromThePull(t *testing.T) {
	fixture, project, _, settle := publishedTask(t)
	settle()
	customerMode(t, fixture)
	ctx := context.Background()
	backend := &publicReviewBackend{enqueueRefusal: "refused: The request was refused: rejected before execution as UNPROCESSABLE."}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	if _, err := reviewNow(ctx, fixture.daemon, project, publishedReviewRequest()); err == nil || errors.Is(err, review.ErrPermanent) || backend.enqueues != 1 {
		t.Fatalf("refused enqueue err=%v enqueues=%d", err, backend.enqueues)
	}
	tick := func() review.Operation {
		if _, err := fixture.daemon.advanceReviewOperations(ctx, false); err != nil {
			t.Fatal(err)
		}
		return lastDurableReview(t, fixture.store, project)
	}
	escalation := ""
	for pass := 2; pass <= review.FailuresBeforeEscalation+2; pass++ {
		op := tick()
		if op.State != "enqueued" || op.Failures != pass || backend.enqueues != pass || (op.Escalation != "") != (pass >= review.FailuresBeforeEscalation) || (escalation != "" && op.Escalation != escalation) {
			t.Fatalf("pass %d refused again: %+v (enqueues %d)", pass, op, backend.enqueues)
		}
		escalation = op.Escalation
	}
	if !strings.Contains(escalation, "UNPROCESSABLE") {
		t.Fatalf("escalation = %q", escalation)
	}
	backend.enqueueRefusal = ""
	if op := tick(); op.State != "enqueued" || op.Failures != 0 || op.Escalation != "" || op.Enqueues != 1 || backend.enqueues != review.FailuresBeforeEscalation+3 || backend.reviews != 1 {
		t.Fatalf("accepted: %+v (enqueues %d)", op, backend.enqueues)
	}
	backend.pull = &review.Pull{Head: publishedReviewRequest().Head, State: "closed"}
	if op := tick(); op.State != "closed" || backend.enqueues != review.FailuresBeforeEscalation+3 {
		t.Fatalf("closed pull: %+v (enqueues %d)", op, backend.enqueues)
	}
}

// A pull request no factory task published (a host or human author's) keeps
// its REQUEST_CHANGES on GitHub: it is neither sent back nor escalated.
func TestRequestChangesOnAPullNoTaskPublishedIsNotEscalated(t *testing.T) {
	fixture, project, _, settle := publishedTask(t)
	settle()
	customerMode(t, fixture)
	ctx := context.Background()
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return &publicReviewBackend{requestChanges: true} }
	request := publishedReviewRequest()
	request.PullNumber = 99
	if _, err := reviewNow(ctx, fixture.daemon, project, request); err != nil {
		t.Fatal(err)
	}
	if op := lastDurableReview(t, fixture.store, project); op.State != "completed" || op.RoutePending || op.Escalation != "" {
		t.Fatalf("host pull operation = %+v", op)
	}
}

// The caller supplies no body: the review binds the body GitHub stores,
// trailing newline included.
func TestReviewBindsTheStoredBody(t *testing.T) {
	fixture, project := reviewPublicFixture(t)
	backend := &publicReviewBackend{}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	if _, err := reviewNow(context.Background(), fixture.daemon, project, publishedReviewRequest()); err != nil || backend.enqueuedBody != "fixture body\n" {
		t.Fatalf("err=%v enqueued body %q", err, backend.enqueuedBody)
	}
}

// Only the checks the base branch's rules require gate the merge: an optional
// check that fails (CodeQL), is cancelled or never finishes decides nothing.
func TestOnlyRequiredChecksGateTheMerge(t *testing.T) {
	failing, pending, err := requiredChecks(json.RawMessage(`{"checks":[
		{"name":"checks","conclusion":"success","required":true},
		{"name":"review","conclusion":"skipped","required":true},
		{"name":"CodeQL","conclusion":"failure","required":false},
		{"name":"stale lint","conclusion":"cancelled","required":false},
		{"name":"preview","conclusion":null,"required":false}]}`))
	if err != nil || len(failing) != 0 || pending {
		t.Fatalf("optional checks decided: failing=%v pending=%v err=%v", failing, pending, err)
	}
	failing, pending, err = requiredChecks(json.RawMessage(`{"checks":[
		{"name":"checks","conclusion":"failure","required":true},
		{"name":"ui","conclusion":null,"required":true}]}`))
	if err != nil || strings.Join(failing, ",") != "checks" || !pending {
		t.Fatalf("required checks ignored: failing=%v pending=%v err=%v", failing, pending, err)
	}
}
