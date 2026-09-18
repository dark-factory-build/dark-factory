package kernel

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestContinuationTaskTextCapsCodexAtProviderLimit(t *testing.T) {
	context := ContinuationContext{ConditionKind: ConditionHumanRequest, ConditionRevision: mustRevision(t, 1), ResolutionDetail: strings.Repeat("答", 4096)}
	text := ContinuationTaskText(ProviderCodex, strings.Repeat("x", 7168), []ContinuationContext{context})
	if ContinuationTaskFits(ProviderCodex, strings.Repeat("x", 8192), []ContinuationContext{context}) {
		t.Fatal("exact-limit Codex task was admitted without room for causal context")
	}
	if !ContinuationTaskCanUseFetchFallback(ProviderCodex, []ContinuationContext{context}) {
		t.Fatal("oversized Codex continuation lost its bounded fetch fallback")
	}
	if len(text) > 8192 || !utf8.ValidString(text) {
		t.Fatalf("bounded continuation task length=%d valid=%v", len(text), utf8.ValidString(text))
	}
}

func TestQuestionYieldCannotBeStrandedByImmediateResolution(t *testing.T) {
	ctx := context.Background()
	store, run, keys := runningWorkerRun(t)
	defer store.Close()

	request, err := store.CreateHumanQuestionAndYieldForAttempt(ctx, keys.AttemptDigest, NewHumanQuestion{
		IdempotencyKey: humanKey(233), QuestionText: "answer immediately",
	}, mustTime(t, 40))
	if err != nil {
		t.Fatalf("create and yield question: %v", err)
	}
	if _, err := store.AuthenticateAttempt(ctx, keys.AttemptDigest); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old attempt after yield: %v", err)
	}
	// The reply is delivered as soon as the atomic request/yield call returns.
	// The request and its waiting condition must already be durable together;
	// there is no observable interval in which a reply can strand the wake edge.
	var condition ContinuationConditionID
	copy(condition[:], request.ID.Bytes())
	continuation := continuationForRequest(t, store, run, condition)
	read, found, err := store.Continuation(ctx, continuation)
	if err != nil || !found || read.State != ContinuationWaiting || read.ConditionRevision != request.Revision {
		t.Fatalf("atomic question/yield continuation: %+v found=%v err=%v", read, found, err)
	}
}

func TestOrchestratorHumanQuestionYieldsAndRevokesBearer(t *testing.T) {
	ctx := context.Background()
	store, run, keys := runningOrchestratorRun(t)
	defer store.Close()
	request, err := store.CreateHumanQuestionAndYieldForAttempt(ctx, keys.AttemptDigest, NewHumanQuestion{
		IdempotencyKey: humanKey(234), QuestionText: "choose the deployment target",
	}, mustTime(t, 40))
	if err != nil {
		t.Fatalf("orchestrator question yield: %v", err)
	}
	if _, err := store.AuthenticateAttempt(ctx, keys.AttemptDigest); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old overseer bearer after yield: %v", err)
	}
	terminal, found, err := store.Run(ctx, run.ID)
	if err != nil || !found || terminal.Phase != RunFinalizing {
		t.Fatalf("yielded overseer run = %+v found=%v err=%v", terminal, found, err)
	}
	projection, found, err := store.HumanRequest(ctx, request.ID)
	if err != nil || !found || !projection.CanReply {
		t.Fatalf("yielded overseer request = %+v found=%v err=%v", projection, found, err)
	}
}

func TestYieldedHumanReplyPersistsFullSchemaBound(t *testing.T) {
	for _, size := range []int{4097, MaxHumanRequestReplyBytes} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			ctx := context.Background()
			store, run, keys, path := runningWorkerRunWithPath(t)
			request, err := store.CreateHumanQuestionAndYieldForAttempt(ctx, keys.AttemptDigest, NewHumanQuestion{IdempotencyKey: humanKey(byte(size % 251)), QuestionText: "full reply"}, mustTime(t, 40))
			if err != nil {
				t.Fatal(err)
			}
			var condition ContinuationConditionID
			copy(condition[:], request.ID.Bytes())
			continuationID := continuationForRequest(t, store, run, condition)
			reply := strings.Repeat("r", size)
			deliveryKey := humanKey(byte(size%251 + 1))
			deliveryID, err := HumanRequestDeliveryIDFromBytes(deliveryKey[:])
			if err != nil {
				t.Fatal(err)
			}
			tx, err := store.beginValidatedWrite(ctx)
			if err != nil {
				t.Fatal(err)
			}
			continuation, found, err := continuationByID(ctx, tx.connection, continuationID)
			if err != nil || !found {
				t.Fatalf("read yielded continuation: found=%v err=%v", found, err)
			}
			if err := resolveHumanContinuationOnConnection(ctx, tx, request, continuation, deliveryID, reply, mustTime(t, 50)); err != nil {
				t.Fatalf("resolve %d-byte reply: %v", size, err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			tx.Close()
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			persisted, found, err := reopened.Continuation(ctx, continuationID)
			if err != nil || !found || persisted.ResolutionDetail != reply {
				t.Fatalf("persisted %d-byte reply: len=%d found=%v err=%v", size, len(persisted.ResolutionDetail), found, err)
			}
		})
	}
}

func continuationForRequest(t *testing.T, store *Store, run Run, condition ContinuationConditionID) ContinuationID {
	t.Helper()
	readTx, err := store.beginRead(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer readTx.Close()
	continuation, found, err := continuationByCondition(context.Background(), readTx.connection, run.TaskID, run.TaskIncarnationID, run.AdmittedTaskWorkRevision, ConditionHumanRequest, condition)
	if err != nil || !found {
		t.Fatalf("continuation for request: %+v found=%v err=%v", continuation, found, err)
	}
	return continuation.ID
}

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
	if yielded.ConditionID != condition || yielded.ConditionRevision != mustRevision(t, 1) || yielded.ContextDigest == ([DigestBytes]byte{}) {
		t.Fatalf("yielded causal context = %+v", yielded)
	}
	if _, err := store.AuthenticateAttempt(ctx, keys.AttemptDigest); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old attempt credential after yield = %v", err)
	}

	// This is the event edge immediately following yield: the old bearer is
	// already revoked, but the durable causal context still accepts exactly one
	// wake. The atomic question test above covers the event-before-yield window.
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

func TestCreateHumanQuestionAndYieldIsAtomic(t *testing.T) {
	ctx := context.Background()
	store, run, keys := runningWorkerRun(t)
	defer store.Close()

	request, err := store.CreateHumanQuestionAndYieldForAttempt(ctx, keys.AttemptDigest, NewHumanQuestion{
		IdempotencyKey: humanKey(223), QuestionText: "atomic question",
	}, mustTime(t, 40))
	if err != nil {
		t.Fatalf("create and yield: %v", err)
	}
	if request.Status != HumanRequestOpen {
		t.Fatalf("request status = %s, want open", request.Status)
	}
	if _, err := store.AuthenticateAttempt(ctx, keys.AttemptDigest); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old attempt credential after atomic yield = %v", err)
	}
	readTx, err := store.beginRead(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer readTx.Close()
	var condition ContinuationConditionID
	copy(condition[:], request.ID.Bytes())
	continuation, found, err := continuationByCondition(ctx, readTx.connection, run.TaskID, run.TaskIncarnationID, run.AdmittedTaskWorkRevision, ConditionHumanRequest, condition)
	if err != nil || !found || continuation.State != ContinuationWaiting {
		t.Fatalf("atomic continuation = %+v found=%v err=%v", continuation, found, err)
	}
}

func TestResolvedContinuationPromotesThenReentersProviderAdmission(t *testing.T) {
	ctx := context.Background()
	proposal, _ := NewBlockedProposal("waiting for continuation")
	store, run := finalizingReleasedRun(t, RoleOrchestrator, VerificationNone, proposal)
	defer store.Close()
	condition := ContinuationConditionID{}
	conditionBytes := humanKey(226)
	copy(condition[:], conditionBytes[:])
	spec := NewContinuation{
		ID: continuationIDForTest(t, 227), ProjectID: run.ProjectID, TaskID: run.TaskID,
		TaskIncarnationID: run.TaskIncarnationID, WorkRevision: run.AdmittedTaskWorkRevision,
		ContextDigest: sha256.Sum256([]byte("admission-continuation")), ConditionKind: ConditionHumanRequest,
		ConditionID: condition, ConditionRevision: mustRevision(t, 1),
	}
	waiting, err := store.CreateContinuation(ctx, spec, mustTime(t, 82))
	if err != nil {
		t.Fatalf("create continuation: %v", err)
	}
	queued, err := store.ResolveContinuationForEvent(ctx, waiting.ID, waiting.Revision, ConditionHumanRequest, condition, waiting.ConditionRevision, "continue", mustTime(t, 83))
	if err != nil || queued.State != ContinuationQueued {
		t.Fatalf("queue continuation: %+v, %v", queued, err)
	}
	terminal, err := finalizeTestRun(t, store, run, 90)
	if err != nil || terminal.Phase != RunTerminal {
		t.Fatalf("terminal origin run: %+v, %v", terminal, err)
	}
	promoted, err := store.PromoteQueuedContinuations(ctx, mustTime(t, 100))
	if err != nil || len(promoted) != 1 || promoted[0].ID != run.TaskID || promoted[0].Status != TaskQueued {
		t.Fatalf("promoted continuation: %+v, %v", promoted, err)
	}
	oversizedBody := strings.Repeat("x", 8192)
	if _, err := store.UpdateTask(ctx, promoted[0].ID, promoted[0].Revision, TaskPatch{Body: &oversizedBody}, mustTime(t, 100)); err != nil {
		t.Fatalf("oversized resumed Codex task remains admissible for fetch fallback: %v", err)
	}
	admission, err := store.AdmitNext(ctx, admissionKeys(t, 228, nil), mustTime(t, 101))
	if err != nil || !admission.Admitted() || admission.Run.TaskID != run.TaskID || admission.Run.Provider != ProviderCodex {
		t.Fatalf("fresh provider admission after promotion: %+v, %v", admission, err)
	}
	if len(admission.Run.ContinuationContexts) != 1 {
		t.Fatalf("fresh admission continuation contexts = %+v", admission.Run.ContinuationContexts)
	}
	context := admission.Run.ContinuationContexts[0]
	if context.ContextDigest != spec.ContextDigest || context.ConditionKind != ConditionHumanRequest ||
		context.ConditionID != condition || context.ConditionRevision != waiting.ConditionRevision ||
		context.ResolutionDetail != "continue" {
		t.Fatalf("fresh admission continuation context = %+v", context)
	}
	resolved, found, err := store.Continuation(ctx, waiting.ID)
	if err != nil || !found || resolved.State != ContinuationResolved {
		t.Fatalf("resolved continuation after admission: %+v found=%v err=%v", resolved, found, err)
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

func TestReusedHumanQuestionYieldsAtomically(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		t.Run(fmt.Sprint(reuse), func(t *testing.T) {
			ctx := context.Background()
			store, run, keys := runningWorkerRun(t)
			defer store.Close()
			input := NewHumanQuestion{IdempotencyKey: humanKey(234), QuestionText: "existing question"}
			original, err := store.CreateHumanQuestionForAttempt(ctx, keys.AttemptDigest, input, mustTime(t, 40))
			if err != nil {
				t.Fatal(err)
			}
			if reuse {
				input.IdempotencyKey = humanKey(235)
				input.ReuseExisting = true
				input.QuestionText = "completion callback"
			}
			request, err := store.CreateHumanQuestionAndYieldForAttempt(ctx, keys.AttemptDigest, input, mustTime(t, 41))
			if err != nil || request.ID != original.ID {
				t.Fatalf("reuse: %+v, %v", request, err)
			}
			if _, err := store.AuthenticateAttempt(ctx, keys.AttemptDigest); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("old authority: %v", err)
			}
			var condition ContinuationConditionID
			copy(condition[:], request.ID.Bytes())
			continuationForRequest(t, store, run, condition)
		})
	}
}
