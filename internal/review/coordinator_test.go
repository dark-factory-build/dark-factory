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
	gates               []GateRun // scripted gate results, in order; none = pass
	gateErr             error
	reviews             int
	killed              bool
	submitErr           error
	event               string
	submitted, enqueued bool
	merge               Merge
}

func (b *fakeBackend) CloneReadOnly(context.Context, Request) (string, func(), error) {
	return "/review", func() {}, nil
}
func (b *fakeBackend) Gate(_ context.Context, _ string, _ Operation, commit string) (GateRun, error) {
	if b.gateErr != nil {
		return GateRun{}, b.gateErr
	}
	if len(b.gates) == 0 {
		return GateRun{Commit: commit}, nil
	}
	run := b.gates[0]
	b.gates = b.gates[1:]
	run.Commit = commit
	return run, nil
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
func (b *fakeBackend) Enqueue(context.Context, Operation) error { b.enqueued = true; return nil }
func (b *fakeBackend) ObserveMerge(context.Context, Operation) (Merge, error) {
	return b.merge, nil
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
	if err != nil || op.State != "enqueued" || op.EnqueueID == "" || op.EnqueueID == op.ID || !backend.submitted || !backend.enqueued {
		t.Fatalf("operation=%+v err=%v backend=%+v", op, err, backend)
	}
	if len(store.values) < 1 || store.values[0].State != "gating" {
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
	if err != nil || second.State != "enqueued" || second.Request.Head != first.Request.Head || !backend.enqueued {
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
	if err != nil || op.State != "enqueued" || !op.Submitted || backend.submitted == false || !backend.enqueued {
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

func TestGateFlakeAtHeadIsRerunAndReviewProceeds(t *testing.T) {
	backend := &fakeBackend{gates: []GateRun{{ExitCode: 1, Failed: []string{"TestFlaky"}}}}
	c := Coordinator{Store: &memoryStore{}, Backend: backend, Now: time.Now}
	op, err := c.Start(context.Background(), reviewRequest())
	if err != nil || op.State != "enqueued" || backend.reviews != 1 || len(op.Gates) != 2 || op.Gates[0].ExitCode == 0 || op.Gates[1].ExitCode != 0 || op.Gates[1].Commit != op.Request.Head {
		t.Fatalf("operation=%+v err=%v", op, err)
	}
}

func TestGateFailureAbsentAtBaseIsSentBackWithTestNames(t *testing.T) {
	failed := GateRun{ExitCode: 1, Failed: []string{"TestBroken", "TestOther"}}
	backend := &fakeBackend{gates: []GateRun{failed, failed, {ExitCode: 1, Failed: []string{"TestBroken"}}}}
	c := Coordinator{Store: &memoryStore{}, Backend: backend, Now: time.Now}
	op, err := c.Start(context.Background(), reviewRequest())
	if err != nil || op.State != "completed" || op.Verdict != "request_changes" || !op.RoutePending || op.Submitted || backend.reviews != 0 || backend.submitted {
		t.Fatalf("operation=%+v err=%v", op, err)
	}
	if len(op.Gates) != 3 || op.Gates[2].Commit != op.Request.Base || !strings.Contains(op.Detail, "tests=TestOther.") || !strings.Contains(op.Detail, op.Request.Head) {
		t.Fatalf("gates=%+v note=%q", op.Gates, op.Detail)
	}
}

func TestGateFailureAlsoAtBaseProceedsToReview(t *testing.T) {
	failed := GateRun{ExitCode: 1, Failed: []string{"TestInherited"}}
	backend := &fakeBackend{gates: []GateRun{failed, failed, {ExitCode: 1, Failed: []string{"TestInherited", "TestElse"}}}}
	c := Coordinator{Store: &memoryStore{}, Backend: backend, Now: time.Now}
	op, err := c.Start(context.Background(), reviewRequest())
	if err != nil || op.State != "enqueued" || backend.reviews != 1 || len(op.Gates) != 3 {
		t.Fatalf("operation=%+v err=%v", op, err)
	}
}

func TestGateHostBlockerStaysRetryableWithoutSendBack(t *testing.T) {
	backend := &fakeBackend{gateErr: errors.New("gate exit 125, nothing ran")}
	c := Coordinator{Store: &memoryStore{}, Backend: backend, Now: time.Now}
	op, err := c.Start(context.Background(), reviewRequest())
	if err == nil || op.State != "failed" || !op.Retryable || op.Verdict != "" || op.RoutePending || backend.reviews != 0 {
		t.Fatalf("operation=%+v err=%v", op, err)
	}
}

func TestGateSendsBackUnlessEveryNamedHeadFailureFailsAtBase(t *testing.T) {
	for name, runs := range map[string][]GateRun{
		"base passed":     {{ExitCode: 1, Failed: []string{"TestA"}}, {ExitCode: 1, Failed: []string{"TestA"}}, {ExitCode: 0, Failed: []string{"TestA"}}},
		"no test names":   {{ExitCode: 2}, {ExitCode: 2}},
		"one run unnamed": {{ExitCode: 1, Failed: []string{"TestA"}}, {ExitCode: 2}},
	} {
		backend := &fakeBackend{gates: runs}
		op, err := Coordinator{Store: &memoryStore{}, Backend: backend, Now: time.Now}.Start(context.Background(), reviewRequest())
		if err != nil || op.State != "completed" || op.Verdict != "request_changes" || backend.reviews != 0 {
			t.Fatalf("%s: operation=%+v err=%v", name, op, err)
		}
	}
}

func TestGateDisjointHeadFailuresAreAFlakeWithoutBaseRun(t *testing.T) {
	backend := &fakeBackend{gates: []GateRun{{ExitCode: 1, Failed: []string{"TestA"}}, {ExitCode: 1, Failed: []string{"TestB"}}}}
	op, err := Coordinator{Store: &memoryStore{}, Backend: backend, Now: time.Now}.Start(context.Background(), reviewRequest())
	if err != nil || op.State != "enqueued" || backend.reviews != 1 || len(op.Gates) != 2 {
		t.Fatalf("operation=%+v err=%v", op, err)
	}
}

func TestGateSendsBackReproducingFailureTheBaseDoesNotShare(t *testing.T) {
	backend := &fakeBackend{gates: []GateRun{{ExitCode: 1, Failed: []string{"TestA", "TestB"}}, {ExitCode: 1, Failed: []string{"TestA"}}, {ExitCode: 1, Failed: []string{"TestB"}}}}
	op, err := Coordinator{Store: &memoryStore{}, Backend: backend, Now: time.Now}.Start(context.Background(), reviewRequest())
	if err != nil || op.State != "completed" || op.Verdict != "request_changes" || backend.reviews != 0 || len(op.Gates) != 3 || !strings.Contains(op.Detail, "tests=TestA.") {
		t.Fatalf("operation=%+v err=%v", op, err)
	}
}

func TestGateReproducingFailureAlsoAtBaseProceeds(t *testing.T) {
	failed := GateRun{ExitCode: 1, Failed: []string{"TestA"}}
	backend := &fakeBackend{gates: []GateRun{failed, failed, failed}}
	op, err := Coordinator{Store: &memoryStore{}, Backend: backend, Now: time.Now}.Start(context.Background(), reviewRequest())
	if err != nil || op.State != "enqueued" || backend.reviews != 1 || len(op.Gates) != 3 {
		t.Fatalf("operation=%+v err=%v", op, err)
	}
}

func TestObserveMergeMarksMergedAndEjectsOnceWithFailingChecks(t *testing.T) {
	enqueued := Operation{ID: "op", EnqueueID: "enqueue", Request: reviewRequest(), State: "enqueued", Verdict: "allow", Submitted: true}
	for _, test := range []struct {
		merge Merge
		state string
	}{
		{merge: Merge{State: "ACTIVE_QUEUE", Open: true}, state: "enqueued"},
		{merge: Merge{State: "MERGED_AFTER_ENQUEUE_ATTEMPT"}, state: "merged"},
		{merge: Merge{State: "NOT_QUEUED"}, state: "closed"},
		{merge: Merge{State: "NOT_QUEUED", Open: true, Failing: []string{"ci / go", "ci / ui"}}, state: "ejected"},
	} {
		store := &memoryStore{}
		c := Coordinator{Store: store, Backend: &fakeBackend{merge: test.merge}, Now: func() time.Time { return time.Unix(20, 0) }}
		op, err := c.ObserveMerge(context.Background(), enqueued)
		if err != nil || op.State != test.state || op.RoutePending != (test.state == "ejected") || len(store.values) != map[bool]int{true: 0, false: 1}[test.state == "enqueued"] {
			t.Fatalf("%+v: operation=%+v err=%v records=%d", test.merge, op, err, len(store.values))
		}
		if test.state == "ejected" {
			if !strings.Contains(op.Detail, "ci / go, ci / ui") || !strings.Contains(op.Detail, enqueued.Request.Head) {
				t.Fatalf("ejection note = %q", op.Detail)
			}
			// The ejected head is no longer enqueued, so a later tick cannot eject it twice.
			if _, err := c.ObserveMerge(context.Background(), op); err == nil {
				t.Fatal("an ejected operation was observed again")
			}
		}
	}
}

// A planned enqueue was claimed by the broker but never run: resuming
// resends the same operation id and the operation becomes enqueued.
func TestResumeResendsAPlannedEnqueue(t *testing.T) {
	store := &memoryStore{}
	backend := &observedBackend{receipt: Receipt{State: "planned"}}
	c := Coordinator{Store: store, Backend: backend, Now: func() time.Time { return time.Unix(10, 0) }}
	op, err := Prepare(reviewRequest(), c.Now)
	if err != nil {
		t.Fatal(err)
	}
	op.State, op.Verdict, op.Submitted, op.EnqueueID = "enqueuing", "allow", true, "enqueue-1"
	got, err := c.Resume(context.Background(), op)
	if err != nil || got.State != "enqueued" || !backend.enqueued {
		t.Fatalf("operation=%+v err=%v enqueued=%v", got, err, backend.enqueued)
	}
}
