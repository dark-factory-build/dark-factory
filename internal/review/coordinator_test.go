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
	reviews    int
	killed     bool
	submitErr  error
	event      string
	submitted  bool
	enqueues   int
	enqueueErr error
	observeErr error
	pull       *Pull
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
		return Pull{Head: op.Request.Head, State: "open"}, nil
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

func TestStartPersistsBeforeProviderAndEnqueuesExactHead(t *testing.T) {
	store, backend := &memoryStore{}, &fakeBackend{}
	c := Coordinator{Store: store, Backend: backend, Now: func() time.Time { return time.Unix(10, 0) }}
	op, err := c.Start(context.Background(), reviewRequest())
	if err != nil || op.State != "enqueued" || op.Enqueues != 1 || !backend.submitted || backend.enqueues != 1 {
		t.Fatalf("operation=%+v err=%v backend=%+v", op, err, backend)
	}
	if len(store.values) < 1 || store.values[0].State != "running" {
		t.Fatalf("prelaunch record=%+v", store.values)
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
	if err != nil || second.State != "enqueued" || second.Request.Head != first.Request.Head || backend.enqueues != 1 {
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

func TestSubmitResponseLossIsReconciledBeforeEnqueue(t *testing.T) {
	request := reviewRequest()
	backend := &observedBackend{fakeBackend: fakeBackend{submitErr: errors.New("response lost")}, receipt: Receipt{State: "completed", Kind: "submit_pull_request_review", Head: request.Head, Event: "ALLOW"}}
	store := &memoryStore{}
	c := Coordinator{Store: store, Backend: backend, Now: time.Now}
	op, err := c.Start(context.Background(), request)
	if err != nil || op.State != "enqueued" || !op.Submitted || backend.submitted == false || backend.enqueues != 1 {
		t.Fatalf("reconciled submit=%+v err=%v backend=%+v", op, err, backend)
	}
}

func TestRequestChangesResponseLossIsReconciledForRouting(t *testing.T) {
	request := reviewRequest()
	backend := &observedBackend{fakeBackend: fakeBackend{event: "REQUEST_CHANGES", submitErr: errors.New("response lost")}, receipt: Receipt{State: "completed", Kind: "submit_pull_request_review", Head: request.Head, Event: "REQUEST_CHANGES"}}
	store := &memoryStore{}
	c := Coordinator{Store: store, Backend: backend, Now: time.Now}
	op, err := c.Start(context.Background(), request)
	if err != nil || op.State != "completed" || !op.Submitted || !op.RoutePending {
		t.Fatalf("reconciled request changes=%+v err=%v backend=%+v", op, err, backend)
	}
}

// Each pass decides from one observation of the pull request.
func TestAdvanceDecidesFromOnePullObservation(t *testing.T) {
	head := reviewRequest().Head
	conflicting, group := false, &GroupRun{ID: 37527118828, URL: "https://github.com/o/r/actions/runs/37527118828", Conclusion: "failure", Jobs: []GroupJob{
		{Name: "checks", Conclusion: "failure", Annotations: []string{"--- FAIL: TestDaemonServesTaskOnlyToLiveAttempt (0.41s)"}},
	}}
	open := func(change func(*Pull)) *Pull {
		pull := &Pull{Head: head, State: "open"}
		change(pull)
		return pull
	}
	for _, test := range []struct {
		name       string
		enqueues   int
		failures   int
		escalation string
		escalated  bool // an escalation is recorded afterwards
		pull       *Pull
		enqueueErr error
		state      string
		calls      int // enqueue calls
		writes     int
		detail     string
	}{
		{name: "head moved", pull: &Pull{Head: strings.Repeat("c", 40), State: "open"}, state: "superseded", writes: 1},
		{name: "merged", pull: &Pull{Head: head, State: "merged"}, state: "merged", writes: 1},
		{name: "closed", pull: &Pull{Head: head, State: "closed"}, state: "closed", writes: 1},
		{name: "conflict", pull: open(func(p *Pull) { p.Mergeable = &conflicting; p.Queued = true }), state: "ejected", writes: 1, detail: "conflicts with main. Rebase this Change onto origin/main"},
		{name: "failing check at head", enqueues: 1, pull: open(func(p *Pull) { p.Failing = []string{"ci / go", "ci / ui"}; p.Pending = true }), state: "ejected", writes: 1, detail: "Failing checks: ci / go, ci / ui."},
		{name: "queued", enqueues: 1, pull: open(func(p *Pull) { p.Queued = true }), state: "enqueued"},
		{name: "queued clears an escalation", enqueues: 1, failures: FailuresBeforeEscalation, escalation: "stalled", pull: open(func(p *Pull) { p.Queued = true }), state: "enqueued", writes: 1},
		{name: "checks running", pull: open(func(p *Pull) { p.Pending = true }), state: "enqueued"},
		{name: "allowed and not queued", state: "enqueued", calls: 1, writes: 1},
		{name: "ejected once is re-queued", enqueues: 1, pull: open(func(p *Pull) { p.Group = group }), state: "enqueued", calls: 1, writes: 1},
		{name: "ejected again names the group", enqueues: 2, pull: open(func(p *Pull) { p.Group = group }), state: "ejected", writes: 1, detail: "run 37527118828 failed"},
		{name: "ejected again with no group", enqueues: 2, state: "ejected", writes: 1, detail: "no merge-group run that built it was readable"},
		{name: "a refused enqueue waits", enqueueErr: errors.New("refused"), state: "enqueued", writes: 1},
		{name: "a persisting refusal escalates", failures: FailuresBeforeEscalation - 1, enqueueErr: errors.New("refused"), state: "enqueued", writes: 1, escalated: true},
		{name: "an escalated refusal is not escalated again", failures: FailuresBeforeEscalation, escalation: "stalled", enqueueErr: errors.New("refused"), state: "enqueued", writes: 1, escalated: true},
	} {
		store, backend := &memoryStore{}, &fakeBackend{pull: test.pull, enqueueErr: test.enqueueErr}
		c := Coordinator{Store: store, Backend: backend, Now: func() time.Time { return time.Unix(20, 0) }}
		before := Operation{ID: "op", Request: reviewRequest(), State: "enqueued", Verdict: "allow", Submitted: true, Enqueues: test.enqueues, Failures: test.failures, Escalation: test.escalation}
		op, err := c.Advance(context.Background(), before)
		if (err != nil) != (test.enqueueErr != nil) || op.State != test.state || op.RoutePending != (test.state == "ejected") || backend.enqueues != test.calls || len(store.values) != test.writes || !strings.Contains(op.Detail, test.detail) {
			t.Fatalf("%s: operation=%+v err=%v enqueues=%d writes=%d", test.name, op, err, backend.enqueues, len(store.values))
		}
		if test.state == "ejected" && !strings.Contains(op.Detail, head) {
			t.Fatalf("%s: send-back %q does not name the head", test.name, op.Detail)
		}
		if (op.Escalation != "") != test.escalated || (test.escalation != "" && test.escalated && op.Escalation != test.escalation) {
			t.Fatalf("%s: escalation=%q", test.name, op.Escalation)
		}
		if test.calls == 1 && op.Enqueues != test.enqueues+1 {
			t.Fatalf("%s: enqueues=%d", test.name, op.Enqueues)
		}
	}
}

// #1376: a conflicting pull request whose enqueue GitHub refuses went back
// to "enqueuing" every five minutes for hours. It is now sent back once,
// without an enqueue, and the sent-back operation is never advanced again.
func TestConflictingPullIsSentBackOnceInsteadOfLooping(t *testing.T) {
	conflicting := false
	store := &memoryStore{}
	backend := &fakeBackend{pull: &Pull{Head: reviewRequest().Head, State: "open", Mergeable: &conflicting}, enqueueErr: errors.New("rejected before execution as UNPROCESSABLE")}
	c := Coordinator{Store: store, Backend: backend, Now: func() time.Time { return time.Unix(20, 0) }}
	op, err := c.Advance(context.Background(), Operation{ID: "op", Request: reviewRequest(), State: "enqueued", Verdict: "allow", Submitted: true})
	if err != nil || op.State != "ejected" || !op.RoutePending || op.Escalation != "" || len(store.values) != 1 {
		t.Fatalf("operation=%+v err=%v writes=%d", op, err, len(store.values))
	}
	if _, err := c.Advance(context.Background(), op); err == nil || len(store.values) != 1 || backend.enqueues != 0 {
		t.Fatalf("a sent-back operation advanced again: err=%v writes=%d", err, len(store.values))
	}
}

// An enqueue whose outcome is unknown leaves nothing sticky behind: the next
// pass reads the pull request again and goes on from what it shows.
func TestIndeterminateEnqueueIsSettledByTheNextObservation(t *testing.T) {
	store := &memoryStore{}
	backend := &fakeBackend{enqueueErr: errors.New("the outcome is indeterminate")}
	c := Coordinator{Store: store, Backend: backend, Now: func() time.Time { return time.Unix(20, 0) }}
	op, err := c.Advance(context.Background(), Operation{ID: "op", Request: reviewRequest(), State: "enqueued", Verdict: "allow", Submitted: true})
	if err == nil || op.State != "enqueued" || op.Escalation != "" || op.Failures != 1 {
		t.Fatalf("indeterminate: operation=%+v err=%v", op, err)
	}
	// It landed after all: the queue shows it, and the count clears.
	backend.pull = &Pull{Head: op.Request.Head, State: "open", Queued: true}
	if landed, err := c.Advance(context.Background(), op); err != nil || landed.State != "enqueued" || landed.Failures != 0 {
		t.Fatalf("landed: operation=%+v err=%v", landed, err)
	}
	// It did not land: the next pass simply ensures it is queued.
	backend.pull, backend.enqueueErr = nil, nil
	if queued, err := c.Advance(context.Background(), op); err != nil || queued.State != "enqueued" || queued.Failures != 0 || queued.Enqueues != 1 {
		t.Fatalf("retried: operation=%+v err=%v", queued, err)
	}
}

// A refusal is escalated only once it has persisted for
// FailuresBeforeEscalation consecutive passes, and only once.
func TestPersistingRefusalEscalatesOnceAfterTheGracePasses(t *testing.T) {
	store := &memoryStore{}
	c := Coordinator{Store: store, Backend: &fakeBackend{enqueueErr: errors.New("rejected before execution as UNPROCESSABLE")}, Now: func() time.Time { return time.Unix(20, 0) }}
	op := Operation{ID: "op", Request: reviewRequest(), State: "enqueued", Verdict: "allow", Submitted: true}
	for pass := 1; pass <= FailuresBeforeEscalation+2; pass++ {
		next, err := c.Advance(context.Background(), op)
		if err == nil || next.State != "enqueued" || next.Failures != pass || (next.Escalation != "") != (pass >= FailuresBeforeEscalation) {
			t.Fatalf("pass %d: operation=%+v err=%v", pass, next, err)
		}
		if pass > FailuresBeforeEscalation && next.Escalation != op.Escalation {
			t.Fatalf("pass %d re-escalated: %q", pass, next.Escalation)
		}
		op = next
	}
	if !strings.Contains(op.Escalation, "UNPROCESSABLE") {
		t.Fatalf("escalation = %q", op.Escalation)
	}
}

// A pass that cannot observe the pull request (a rules read refused, a base
// retargeted) counts toward the same escalation, and any pass that observes,
// waiting included, starts the count again: no failure stalls silently.
func TestPersistentObservationFailureEscalatesOnce(t *testing.T) {
	store := &memoryStore{}
	backend := &fakeBackend{observeErr: errors.New("observe_pull_request_merge: conflict")}
	c := Coordinator{Store: store, Backend: backend, Now: func() time.Time { return time.Unix(20, 0) }}
	op := Operation{ID: "op", Request: reviewRequest(), State: "enqueued", Verdict: "allow", Submitted: true, Enqueues: 1}
	for pass := 1; pass <= FailuresBeforeEscalation+1; pass++ {
		next, err := c.Advance(context.Background(), op)
		if err == nil || next.State != "enqueued" || next.Failures != pass || (next.Escalation != "") != (pass >= FailuresBeforeEscalation) || (pass > FailuresBeforeEscalation && next.Escalation != op.Escalation) {
			t.Fatalf("pass %d: operation=%+v err=%v", pass, next, err)
		}
		op = next
	}
	if !strings.Contains(op.Escalation, "failed 6 passes in a row: observe_pull_request_merge: conflict") || len(store.values) != FailuresBeforeEscalation+1 {
		t.Fatalf("escalation=%q writes=%d", op.Escalation, len(store.values))
	}
	// Observing again, even only to wait on a running check, resets both.
	backend.observeErr, backend.pull = nil, &Pull{Head: op.Request.Head, State: "open", Pending: true}
	if waited, err := c.Advance(context.Background(), op); err != nil || waited.Failures != 0 || waited.Escalation != "" || len(store.values) != FailuresBeforeEscalation+2 {
		t.Fatalf("waiting pass: operation=%+v err=%v writes=%d", waited, err, len(store.values))
	}
}
