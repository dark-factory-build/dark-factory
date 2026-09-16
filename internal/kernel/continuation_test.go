package kernel

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
)

func TestContinuationSpecRejectsUnknownConditionAndZeroTarget(t *testing.T) {
	spec := NewContinuation{ConditionKind: ConditionHumanRequest}
	if err := validateContinuationSpec(spec); err == nil {
		t.Fatal("zero continuation spec was accepted")
	}
	spec.ConditionKind = ContinuationCondition("unknown")
	if err := validateContinuationSpec(spec); err == nil {
		t.Fatal("unknown continuation condition was accepted")
	}
}

func TestContinuationConditionIDRoundTripsBytes(t *testing.T) {
	var want ContinuationConditionID
	want[0], want[15] = 1, 255
	got := ContinuationConditionID{}
	copy(got[:], want.Bytes())
	if got != want || got.zero() {
		t.Fatalf("condition id round trip = %x, want %x", got, want)
	}
}

func TestYieldedContinuationResolvesExactlyOnceAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	store, run, keys, path := runningWorkerRunWithPath(t)
	condition := ContinuationConditionID{}
	conditionBytes := humanKey(220)
	copy(condition[:], conditionBytes[:])

	yielded, err := store.YieldContinuationForAttempt(ctx, keys.AttemptDigest, ConditionHumanRequest, condition, mustRevision(t, 1), mustTime(t, 40))
	if err != nil {
		t.Fatalf("yield continuation: %v", err)
	}
	if yielded.State != ContinuationWaiting || yielded.WorkRevision != run.AdmittedTaskWorkRevision {
		t.Fatalf("yielded continuation = %+v", yielded)
	}
	if _, err := store.AuthenticateAttempt(ctx, keys.AttemptDigest); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old attempt credential after yield = %v", err)
	}

	woken, err := store.ResolveContinuationForEvent(ctx, yielded.ID, yielded.Revision, ConditionHumanRequest, condition, yielded.ConditionRevision, "human answered", mustTime(t, 41))
	if err != nil || woken.State != ContinuationQueued || woken.ResolutionDetail != "human answered" || woken.ResolvedAt == nil {
		t.Fatalf("resolved continuation = %+v, %v", woken, err)
	}
	replay, err := store.ResolveContinuationForEvent(ctx, yielded.ID, yielded.Revision, ConditionHumanRequest, condition, yielded.ConditionRevision, "human answered", mustTime(t, 42))
	if err != nil || replay.ID != woken.ID || replay.Revision != woken.Revision {
		t.Fatalf("duplicate wake = %+v, %v", replay, err)
	}
	if _, err := store.ResolveContinuationForEvent(ctx, yielded.ID, yielded.Revision, ConditionHumanRequest, condition, yielded.ConditionRevision, "different answer", mustTime(t, 42)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("conflicting wake = %v", err)
	}
	store.Close()
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()
	read, found, err := reopened.Continuation(ctx, yielded.ID)
	if err != nil || !found || read.State != ContinuationQueued || read.ResolutionDetail != "human answered" {
		t.Fatalf("restarted continuation = %+v found=%v err=%v", read, found, err)
	}
}

func TestContinuationCancellationWinsAndStaleEventCannotWake(t *testing.T) {
	ctx := context.Background()
	store, run, _ := runningWorkerRun(t)
	defer store.Close()
	condition := ContinuationConditionID{}
	conditionBytes := humanKey(221)
	copy(condition[:], conditionBytes[:])
	spec := NewContinuation{ID: continuationIDForTest(t, 222), ProjectID: run.ProjectID, TaskID: run.TaskID, TaskIncarnationID: run.TaskIncarnationID, WorkRevision: run.AdmittedTaskWorkRevision, ContextDigest: sha256.Sum256([]byte("cancel-context")), ConditionKind: ConditionHumanRequest, ConditionID: condition, ConditionRevision: mustRevision(t, 1)}
	created, err := store.CreateContinuation(ctx, spec, mustTime(t, 40))
	if err != nil {
		t.Fatalf("create continuation: %v", err)
	}
	cancelled, err := store.CancelContinuation(ctx, created.ID, created.Revision, "scope changed", mustTime(t, 41))
	if err != nil || cancelled.State != ContinuationCancelled || cancelled.ResolutionDetail != "scope changed" {
		t.Fatalf("cancelled continuation = %+v, %v", cancelled, err)
	}
	if _, err := store.ResolveContinuationForEvent(ctx, created.ID, created.Revision, ConditionHumanRequest, condition, created.ConditionRevision, "late answer", mustTime(t, 42)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("late event after cancellation = %v", err)
	}
}

func continuationIDForTest(t *testing.T, seed byte) ContinuationID {
	t.Helper()
	value := humanKey(seed)
	id, err := ContinuationIDFromBytes(value[:])
	if err != nil {
		t.Fatal(err)
	}
	return id
}
