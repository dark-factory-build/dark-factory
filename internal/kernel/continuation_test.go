package kernel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestContinuationTaskFitsCodexProviderLimit(t *testing.T) {
	t.Parallel()
	context := ContinuationContext{ResolutionDetail: strings.Repeat("答", 4096)}
	if ContinuationTaskFits(ProviderCodex, strings.Repeat("x", 8192), []ContinuationContext{context}) {
		t.Fatal("exact-limit Codex task was admitted without room for causal context")
	}
	if !ContinuationTaskCanUseFetchFallback(ProviderCodex, []ContinuationContext{context}) {
		t.Fatal("oversized Codex continuation lost its bounded fetch fallback")
	}
}

func TestQuestionYieldCannotBeStrandedByImmediateResolution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, keys := runningWorkerRun(t)
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
	if read := requestForTest(t, store, request.ID); read.Continuation != ContinuationWaiting || read.Revision != request.Revision {
		t.Fatalf("atomic question/yield continuation: %+v", read)
	}
}

func TestOrchestratorHumanQuestionYieldsAndRevokesBearer(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	for _, size := range []int{4097, MaxHumanRequestReplyBytes} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			ctx := context.Background()
			store, _, keys, path := runningWorkerRunWithPath(t)
			request, err := store.CreateHumanQuestionAndYieldForAttempt(ctx, keys.AttemptDigest, NewHumanQuestion{IdempotencyKey: humanKey(byte(size % 251)), QuestionText: "full reply"}, mustTime(t, 40))
			if err != nil {
				t.Fatal(err)
			}
			request = requestForTest(t, store, request.ID)
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
			if err := resolveHumanContinuationOnConnection(ctx, tx, request, deliveryID, reply, mustTime(t, 50)); err != nil {
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
			if persisted := requestForTest(t, reopened, request.ID); persisted.ContinuationReply != reply {
				t.Fatalf("persisted %d-byte reply: len=%d", size, len(persisted.ContinuationReply))
			}
		})
	}
}

func requestForTest(t *testing.T, store *Store, id HumanRequestID) HumanRequest {
	t.Helper()
	readTx, err := store.beginRead(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer readTx.Close()
	request, found, err := humanRequestByID(context.Background(), readTx.connection, id)
	if err != nil || !found {
		t.Fatalf("human request: %+v found=%v err=%v", request, found, err)
	}
	return request
}

func TestCreateHumanQuestionAndYieldIsAtomic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, keys := runningWorkerRun(t)
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
	if continuation := requestForTest(t, store, request.ID).Continuation; continuation != ContinuationWaiting {
		t.Fatalf("atomic continuation = %q", continuation)
	}
}

func TestResolvedContinuationPromotesThenReentersProviderAdmission(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	proposal, _ := NewBlockedProposal("waiting for continuation")
	store, run := finalizingReleasedRun(t, RoleOrchestrator, proposal)
	defer store.Close()
	request := humanKey(226)
	// Production queues continuations only through a human reply; insert the
	// resolved request directly to exercise promotion and admission alone.
	at := mustTime(t, 83)
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.connection.ExecContext(ctx, `INSERT INTO human_requests(id, run_id, idempotency_key, kind, reason_code, question_text, status, delivery_id, delivery_started_at_ms, resolution_kind, closed_at_ms, revision, created_at_ms, updated_at_ms, continuation, continuation_reply) VALUES(?1, ?2, ?1, 'question', 'provider_question', 'continue?', 'resolved', ?1, ?3, 'reply', ?3, 1, ?3, ?3, 'queued', 'continue')`, request[:], run.ID.Bytes(), at.Int64()); err != nil {
		t.Fatal(tx.Rollback(err))
	}
	if err := appendInvalidations(ctx, tx.connection, at, []pendingInvalidation{{kind: EntityHumanRequest, id: request[:], revision: 1}}); err != nil {
		t.Fatal(tx.Rollback(err))
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx.Close()
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
	if context.ContextDigest != sha256.Sum256([]byte(fmt.Sprintf("%s:%d", run.TaskID, run.AdmittedTaskWorkRevision.Int64()))) ||
		!bytes.Equal(context.RequestID.Bytes(), request[:]) || context.ResolutionDetail != "continue" {
		t.Fatalf("fresh admission continuation context = %+v", context)
	}
	if resolved := requestForTest(t, store, context.RequestID); resolved.Continuation != ContinuationResolved {
		t.Fatalf("resolved continuation after admission: %+v", resolved)
	}
}

func TestReusedHumanQuestionYieldsAtomically(t *testing.T) {
	t.Parallel()
	for _, reuse := range []bool{false, true} {
		t.Run(fmt.Sprint(reuse), func(t *testing.T) {
			ctx := context.Background()
			store, _, keys := runningWorkerRun(t)
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
			if continuation := requestForTest(t, store, request.ID).Continuation; continuation != ContinuationWaiting {
				t.Fatalf("reused question continuation = %q", continuation)
			}
		})
	}
}
