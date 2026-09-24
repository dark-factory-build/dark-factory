package review

import (
	"context"
	"errors"
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

type fakeBackend struct {
	killed              bool
	submitErr           error
	event               string
	submitted, enqueued bool
}

func (b *fakeBackend) CloneReadOnly(context.Context, Request) (string, func(), error) {
	return "/review", func() {}, nil
}
func (b *fakeBackend) Review(context.Context, string, Request) (Verdict, error) {
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
	if err != nil || second.State != "enqueued" || second.Request.Head != first.Request.Head || !backend.enqueued {
		t.Fatalf("retry operation=%+v err=%v backend=%+v", second, err, backend)
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
	if err == nil || second.State != "failed" || !second.Retryable || second.RetryOf != first.ID {
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
