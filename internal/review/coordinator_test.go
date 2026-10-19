package review

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type memoryStore struct{ values []Operation }

func (s *memoryStore) Create(_ context.Context, op Operation) error {
	s.values = append(s.values, op)
	return nil
}
func (s *memoryStore) Update(_ context.Context, op Operation) error {
	s.values = append(s.values, op)
	return nil
}
func (s *memoryStore) Blocked(context.Context, uint64, string) (bool, error) { return false, nil }
func (s *memoryStore) CreateRetry(_ context.Context, failed, retry Operation) error {
	for index := len(s.values) - 1; index >= 0; index-- {
		if s.values[index].ID == failed.ID {
			if !s.values[index].Retryable {
				return errors.New("retry already reserved")
			}
			break
		}
	}
	failed.Retryable = false
	s.values = append(s.values, failed, retry)
	return nil
}

type fakeBackend struct {
	reviews         int
	killed          bool
	submitErr       error
	event           string
	submitted       bool
	enqueues        int
	enqueueAttempts int
	enqueueErr      error
	observeErr      error
	pull            *Pull
}

func (b *fakeBackend) CloneReadOnly(context.Context, Request) (string, func(), error) {
	return "/review", func() {}, nil
}
func (b *fakeBackend) Review(context.Context, string, Request) (Verdict, error) {
	b.reviews++
	if b.killed {
		return Verdict{}, errors.New("provider killed at launch")
	}
	event := b.event
	if event == "" {
		event = "ALLOW"
	}
	return Verdict{Event: event, Body: "independent"}, nil
}
func (b *fakeBackend) Submit(context.Context, Operation, Verdict) error {
	b.submitted = true
	return b.submitErr
}
func (b *fakeBackend) Enqueue(context.Context, Operation) error {
	b.enqueueAttempts++
	if b.enqueueErr == nil {
		b.enqueues++
	}
	return b.enqueueErr
}
func (b *fakeBackend) Observe(context.Context, string) (Receipt, error) {
	return Receipt{State: "missing"}, nil
}
func (b *fakeBackend) StoredPull(context.Context, uint64, string) (Request, error) {
	return Request{}, nil
}

// ObservePull defaults to the pull request open at the operation's head,
// mergeable, unqueued and with every check passed.
func (b *fakeBackend) ObservePull(_ context.Context, op Operation) (Pull, error) {
	if b.observeErr != nil {
		return Pull{}, b.observeErr
	}
	if b.pull == nil {
		return Pull{Head: op.Request.Head, State: "open", Checks: []string{"required=success"}}, nil
	}
	return *b.pull, nil
}

type observedBackend struct {
	fakeBackend
	receipt Receipt
}

func (b *observedBackend) Observe(context.Context, string) (Receipt, error) {
	return b.receipt, nil
}

func reviewRequest() Request {
	return Request{Repository: "org/repo", PullNumber: 7, Head: "a" + "000000000000000000000000000000000000000", Base: "b" + "000000000000000000000000000000000000000", BaseRef: "main", Body: "body", Provider: "codex"}
}

// A review ends at its verdict: the pull request's reconciler enqueues it.
func TestStartPersistsBeforeProviderAndEndsAtTheVerdict(t *testing.T) {
	store, backend := &memoryStore{}, &fakeBackend{}
	c := Coordinator{Store: store, Backend: backend, Now: func() time.Time { return time.Unix(10, 0) }}
	op, err := c.Start(context.Background(), reviewRequest())
	if err != nil || op.State != "enqueued" || op.Enqueues != 0 || !backend.submitted || backend.enqueues != 0 {
		t.Fatalf("operation=%+v err=%v backend=%+v", op, err, backend)
	}
	if len(store.values) < 1 || store.values[0].State != "running" {
		t.Fatalf("prelaunch record=%+v", store.values)
	}
}

func TestCarryForwardSubmitsPriorAllowWithoutProviderReview(t *testing.T) {
	store, backend := &memoryStore{}, &fakeBackend{}
	c := Coordinator{Store: store, Backend: backend, Now: func() time.Time { return time.Unix(10, 0) }}
	prior := Operation{ID: "prior", Verdict: "allow", Detail: "independent", DiffID: "same-diff"}
	request := reviewRequest()
	op, err := c.CarryForward(context.Background(), request, prior)
	if err != nil || op.State != "enqueued" || op.DiffID != "same-diff" || backend.reviews != 0 || !backend.submitted {
		t.Fatalf("operation=%+v err=%v backend=%+v", op, err, backend)
	}
}

func TestStartRecordsProviderFailureForRetry(t *testing.T) {
	store, backend := &memoryStore{}, &fakeBackend{killed: true}
	c := Coordinator{Store: store, Backend: backend, Now: time.Now}
	op, err := c.Start(context.Background(), func() Request { request := reviewRequest(); request.Provider = "claude"; return request }())
	if err == nil || op.State != "failed" || !op.Retryable || len(store.values) < 2 || store.values[len(store.values)-1].State != "failed" {
		t.Fatalf("operation=%+v err=%v records=%+v", op, err, store.values)
	}
}

func TestSubmitFailureIsDurableButNotRetryable(t *testing.T) {
	store := &memoryStore{}
	backend := &fakeBackend{}
	backend.submitErr = errors.New("submit timeout after write")
	c := Coordinator{Store: store, Backend: backend, Now: time.Now}
	op, err := c.Start(context.Background(), reviewRequest())
	if err == nil || op.State != "failed" || op.Retryable {
		t.Fatalf("operation=%+v err=%v", op, err)
	}
	if _, err := c.Retry(context.Background(), op); err == nil {
		t.Fatal("post-submit ambiguity was retryable")
	}
}

func TestRetryRerunsTheSameExactHeadAfterProviderLaunchFailure(t *testing.T) {
	store, backend := &memoryStore{}, &fakeBackend{killed: true}
	c := Coordinator{Store: store, Backend: backend, Now: time.Now}
	first, err := c.Start(context.Background(), reviewRequest())
	if err == nil || first.State != "failed" {
		t.Fatalf("first operation=%+v err=%v", first, err)
	}
	backend.killed = false
	second, err := c.Retry(context.Background(), first)
	if err != nil || second.State != "enqueued" || second.Request.Head != first.Request.Head || !backend.submitted {
		t.Fatalf("retry operation=%+v err=%v backend=%+v", second, err, backend)
	}
	if _, err := c.Retry(context.Background(), first); err == nil {
		t.Fatal("original launch failure remained retryable after its retry was reserved")
	}
}

func TestRetryFailureIsExhaustedOnlyAfterTheSingleRetry(t *testing.T) {
	store, backend := &memoryStore{}, &fakeBackend{killed: true}
	c := Coordinator{Store: store, Backend: backend, Now: time.Now}
	first, err := c.Start(context.Background(), reviewRequest())
	if err == nil || !first.Retryable || strings.Contains(first.Detail, "retries exhausted") {
		t.Fatalf("first failure=%+v err=%v", first, err)
	}
	second, err := c.Retry(context.Background(), first)
	if err == nil || second.Retryable || !strings.Contains(second.Detail, "review retries exhausted") {
		t.Fatalf("exhausted retry=%+v err=%v", second, err)
	}
}

func TestRetryAllowsOnlyOneNewPreSubmitOperation(t *testing.T) {
	store, backend := &memoryStore{}, &fakeBackend{killed: true}
	c := Coordinator{Store: store, Backend: backend, Now: time.Now}
	first, err := c.Start(context.Background(), reviewRequest())
	if err == nil || first.State != "failed" || !first.Retryable {
		t.Fatalf("first operation=%+v err=%v", first, err)
	}
	second, err := c.Retry(context.Background(), first)
	if err == nil || second.State != "failed" || second.Retryable || second.RetryOf != first.ID {
		t.Fatalf("single retry operation=%+v err=%v", second, err)
	}
	if _, err := c.Retry(context.Background(), second); err == nil {
		t.Fatal("second pre-submit retry unexpectedly accepted")
	}
}

func TestSubmitResponseLossIsReconciledToTheVerdict(t *testing.T) {
	request := reviewRequest()
	backend := &observedBackend{fakeBackend: fakeBackend{submitErr: errors.New("response lost")}, receipt: Receipt{State: "completed", Kind: "submit_pull_request_review", Head: request.Head, Event: "ALLOW"}}
	store := &memoryStore{}
	c := Coordinator{Store: store, Backend: backend, Now: time.Now}
	op, err := c.Start(context.Background(), request)
	if err != nil || op.State != "enqueued" || !op.Submitted || backend.submitted == false {
		t.Fatalf("reconciled submit=%+v err=%v backend=%+v", op, err, backend)
	}
}

func TestRequestChangesResponseLossIsReconciledForRouting(t *testing.T) {
	request := reviewRequest()
	backend := &observedBackend{fakeBackend: fakeBackend{event: "REQUEST_CHANGES", submitErr: errors.New("response lost")}, receipt: Receipt{State: "completed", Kind: "submit_pull_request_review", Head: request.Head, Event: "REQUEST_CHANGES"}}
	store := &memoryStore{}
	c := Coordinator{Store: store, Backend: backend, Now: time.Now}
	op, err := c.Start(context.Background(), request)
	if err != nil || op.State != "completed" || !op.Submitted || op.Verdict != "request_changes" {
		t.Fatalf("reconciled request changes=%+v err=%v backend=%+v", op, err, backend)
	}
}

type pollingBackend struct {
	fakeBackend
	pulls []Pull
}

func (b *pollingBackend) ObservePull(_ context.Context, op Operation) (Pull, error) {
	pull := b.pulls[0]
	if len(b.pulls) > 1 {
		b.pulls = b.pulls[1:]
	}
	if pull.Head == "" {
		pull.Head, pull.State = op.Request.Head, "open"
	}
	return pull, nil
}

// #1582: review waits for the published head's required checks. A red head
// goes back to its author naming the failing tests, and spends no review.
func TestReviewWaitsForTheHeadsRequiredChecks(t *testing.T) {
	checksPoll = time.Millisecond
	defer func() { checksPoll = time.Minute }()
	conflicting, failed := false, Pull{Failing: []string{"required"}, Tests: append([]string{"--- FAIL: TestA (0.01s)"}, strings.Split(strings.Repeat("x,", 39)+"40", ",")...)}
	for _, test := range []struct {
		name    string
		pulls   []Pull
		age     time.Duration
		state   string
		reviews int
		detail  string
	}{
		{name: "red after running", pulls: []Pull{{}, {Pending: true}, failed}, state: "ejected", detail: "Not reviewed: its checks failed. Exact head " + reviewRequest().Head + " cannot merge. Failing checks: required. CI reproduced this failure on the exact head, so it is the premise: if your rerun passes, report what differs (environment, which tests you ran) and work from these lines; \"premise not reproduced\" is not a valid ending.\n- --- FAIL: TestA (0.01s)\n"},
		{name: "green after running", pulls: []Pull{{Pending: true}, {Checks: []string{"required=success"}}}, state: "enqueued", reviews: 1},
		{name: "no required check past the grace", pulls: []Pull{{}}, age: checksGrace, state: "enqueued", reviews: 1},
		{name: "still running past the grace", pulls: []Pull{{Pending: true}, {Checks: []string{"required=success"}}}, age: checksGrace, state: "enqueued", reviews: 1},
		{name: "conflict", pulls: []Pull{{Mergeable: &conflicting}}, state: "ejected", detail: "conflicts with main"},
		{name: "head moved", pulls: []Pull{{Head: strings.Repeat("c", 40), State: "open"}}, state: "superseded"},
	} {
		backend := &pollingBackend{pulls: test.pulls}
		c := Coordinator{Store: &memoryStore{}, Backend: backend, Now: time.Now}
		op, err := c.Resume(context.Background(), Operation{ID: "op", Request: reviewRequest(), State: "running", CreatedAt: time.Now().Add(-test.age)})
		if err != nil || op.State != test.state || backend.reviews != test.reviews || !strings.Contains(op.Detail, test.detail) {
			t.Fatalf("%s: operation=%+v err=%v reviews=%d", test.name, op, err, backend.reviews)
		}
		if strings.Contains(op.Detail, "\n- 40") {
			t.Fatalf("%s: more than 40 failure lines: %q", test.name, op.Detail)
		}
	}
}

// Each reconciler pass decides once, from one observation of the pull
// request and the operations at its head.
func TestDecideTakesOneActionPerObservation(t *testing.T) {
	head := reviewRequest().Head
	conflicting, group := false, &GroupRun{ID: 37527118828, URL: "https://github.com/o/r/actions/runs/37527118828", Conclusion: "failure", Jobs: []GroupJob{
		{Name: "checks", Conclusion: "failure", Annotations: []string{"--- FAIL: TestDaemonServesTaskOnlyToLiveAttempt (0.41s)"}},
	}}
	allow := Operation{ID: "allow", Request: reviewRequest(), State: "enqueued", Verdict: "allow", Submitted: true}
	block := Operation{ID: "block", Request: reviewRequest(), State: "completed", Verdict: "request_changes", Submitted: true, Detail: "fixture findings"}
	disputed := block
	disputed.ID, disputed.Request.Answer = "again", "the premise does not reproduce"
	with := func(op Operation, change func(*Operation)) Operation {
		change(&op)
		return op
	}
	for _, test := range []struct {
		name     string
		pull     Pull
		ops      []Operation
		answered bool
		action   Action
		note     string
	}{
		{name: "conflict", pull: Pull{Mergeable: &conflicting, Queued: true}, ops: []Operation{allow}, action: Rebase, note: "conflicts with main. Rebase this Change onto origin/main"},
		{name: "dirty", pull: Pull{MergeStateStatus: "DIRTY"}, ops: []Operation{allow}, action: Rebase},
		{name: "conflict after its rebase", pull: Pull{Mergeable: &conflicting}, ops: []Operation{with(allow, func(op *Operation) { op.Rebased = true })}, action: SendBack, note: "Exact head " + head + " conflicts with main"},
		{name: "conflict answered", pull: Pull{Mergeable: &conflicting}, ops: []Operation{with(allow, func(op *Operation) { op.Rebased = true })}, answered: true, action: Escalate, note: "answered the send-back of operation allow without a new head"},
		{name: "failing check before rebase", pull: Pull{Failing: []string{"ci / go", "ci / ui"}, Pending: true}, ops: []Operation{allow}, action: Rebase, note: "Failing checks: ci / go, ci / ui."},
		{name: "failing check after rebase", pull: Pull{Failing: []string{"ci / go", "ci / ui"}, Pending: true}, ops: []Operation{with(allow, func(op *Operation) { op.Rebased = true })}, action: SendBack, note: "Failing checks: ci / go, ci / ui."},
		{name: "checks running", pull: Pull{Pending: true}, ops: []Operation{allow}, action: Wait},
		{name: "draft", pull: Pull{Draft: true}, ops: []Operation{allow}, action: Wait},
		{name: "draft with failing checks", pull: Pull{Draft: true, Failing: []string{"ci / go"}}, ops: []Operation{allow}, action: Wait},
		{name: "draft block", pull: Pull{Draft: true}, ops: []Operation{block}, action: Wait},
		{name: "review failed", ops: []Operation{{ID: "op", Request: reviewRequest(), State: "failed", Retryable: true}}, action: Retry},
		{name: "retry failed", ops: []Operation{{ID: "op", Request: reviewRequest(), State: "failed", RetryOf: "first", Detail: "killed"}}, action: Escalate, note: "its review failed: killed"},
		{name: "failure escalated", ops: []Operation{{ID: "op", Request: reviewRequest(), State: "failed", Handled: true}}, action: Wait},
		{name: "no verdict", ops: []Operation{{ID: "op", Request: reviewRequest(), State: "ejected"}}, action: Review},
		{name: "block", ops: []Operation{block}, action: SendBack, note: "This is the review of record for exact head " + head + ". No other verdict on this head supersedes its findings: correct each one.\n\nfixture findings"},
		{name: "block disputed", ops: []Operation{block}, answered: true, action: Review},
		{name: "block upheld", ops: []Operation{block, disputed}, action: SendBack, note: "upheld that block:\n\nthe premise does not reproduce"},
		{name: "block upheld and disputed", ops: []Operation{block, disputed}, answered: true, action: Escalate, note: "without a new head"},
		{name: "queued", pull: Pull{Queued: true}, ops: []Operation{allow}, action: Wait},
		{name: "allowed and not queued", ops: []Operation{allow}, action: Enqueue},
		{name: "ejected once is re-queued", pull: Pull{Group: group}, ops: []Operation{with(allow, func(op *Operation) { op.Enqueues = 1 })}, action: Enqueue},
		{name: "ejected again names the group", pull: Pull{Group: group}, ops: []Operation{with(allow, func(op *Operation) { op.Enqueues = 2 })}, action: SendBack, note: "run 37527118828 failed"},
		{name: "ejected again with no group", ops: []Operation{with(allow, func(op *Operation) { op.Enqueues = 2 })}, action: SendBack, note: "no merge-group run that built it was readable"},
		{name: "failing until the pull changes", ops: []Operation{with(allow, func(op *Operation) { op.Failures = FailuresBeforeEscalation })}, action: Wait},
	} {
		test.pull.Head, test.pull.State = head, "open"
		action, note := Decide(test.pull, test.ops, test.answered)
		if action != test.action || !strings.Contains(note, test.note) {
			t.Fatalf("%s: action=%d note=%q, want %d with %q", test.name, action, note, test.action, test.note)
		}
	}
}
