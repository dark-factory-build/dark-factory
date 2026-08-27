package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

func humanQuestionClient(t *testing.T, store *Store, seed byte, capabilities BrowserCapabilityMask) BrowserClient {
	t.Helper()
	boot := browserTestBoot(t, seed)
	digest := mintBrowserChallenge(t, store, seed, boot, 1, 1000, capabilities)
	return pairBrowserClient(t, store, digest, boot, browserTestID(t, seed), browserKey(t), 2)
}

func humanKey(seed byte) [IDBytes]byte {
	var key [IDBytes]byte
	key[0] = seed
	key[IDBytes-1] = seed ^ 0xa5
	return key
}

func humanDeliveryID(t *testing.T, seed byte) HumanRequestDeliveryID {
	t.Helper()
	value := bytes.Repeat([]byte{seed}, IDBytes)
	value[IDBytes-1] = seed ^ 0x5a
	id, err := HumanRequestDeliveryIDFromBytes(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestHumanQuestionCreationProjectionDetailAndIdempotency(t *testing.T) {
	ctx := context.Background()
	store, run, _ := runningOrchestratorRun(t)
	defer store.Close()
	before, err := store.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	client := humanQuestionClient(t, store, 201, BrowserCapabilityObserve|BrowserCapabilityPrivateHumanRequestDetail|BrowserCapabilityHumanActions)
	input := NewHumanQuestion{IdempotencyKey: humanKey(1), QuestionText: "QUESTION_PRIVATE_SENTINEL"}
	request, err := store.CreateHumanQuestionForAttempt(ctx, run.CredentialDigest, input, mustTime(t, 400))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.CreateHumanQuestionForAttempt(ctx, run.CredentialDigest, input, mustTime(t, 401))
	if err != nil || replay.ID != request.ID || replay.Revision != request.Revision {
		t.Fatalf("idempotent creation = %+v, %v", replay, err)
	}
	changed := input
	changed.QuestionText = "different"
	if _, err := store.CreateHumanQuestionForAttempt(ctx, run.CredentialDigest, changed, mustTime(t, 401)); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed idempotency key content = %v", err)
	}
	projection, found, err := store.HumanRequest(ctx, request.ID)
	if err != nil || !found {
		t.Fatalf("projection = %+v, %v, found=%v", projection, err, found)
	}
	batch, err := store.WatchAfter(ctx, before.Head)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, invalidation := range batch.Invalidations {
		if invalidation.EntityKind == EntityHumanRequest.String() && invalidation.EntityID == request.ID.String() && invalidation.Revision.Int64() == 1 {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("human request invalidation missing: %+v", batch.Invalidations)
	}
	if projection.Status != HumanRequestOpen || projection.Title == "" || projection.Summary == "" || strings.Contains(projection.Summary, input.QuestionText) {
		t.Fatalf("unsafe projection = %+v", projection)
	}
	encoded, err := json.Marshal(projection)
	if err != nil || bytes.Contains(encoded, []byte(input.QuestionText)) {
		t.Fatalf("public projection leaked question: %s, %v", encoded, err)
	}
	detail, err := store.HumanRequestDetail(ctx, client.ID, request.ID)
	if err != nil || detail.QuestionText != input.QuestionText || detail.Revision != request.Revision {
		t.Fatalf("detail = %+v, %v", detail, err)
	}
	if snapshot, err := store.Snapshot(ctx); err != nil || len(snapshot.HumanRequests) != 1 {
		t.Fatalf("snapshot = %+v, %v", snapshot, err)
	} else if encoded, marshalErr := json.Marshal(snapshot); marshalErr != nil || bytes.Contains(encoded, []byte(input.QuestionText)) {
		t.Fatalf("snapshot leaked question: %s, %v", encoded, marshalErr)
	}
	if _, err := store.HumanRequestDetail(ctx, humanQuestionClient(t, store, 202, BrowserCapabilityObserve).ID, request.ID); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("observation-only detail = %v", err)
	}
}

func TestHumanQuestionDeliveryRecoveryAndAckAreAtMostOnce(t *testing.T) {
	ctx := context.Background()
	store, run, _ := runningOrchestratorRun(t)
	defer store.Close()
	client := humanQuestionClient(t, store, 203, BrowserCapabilityObserve|BrowserCapabilityHumanActions)
	request, err := store.CreateHumanQuestionForAttempt(ctx, run.CredentialDigest, NewHumanQuestion{IdempotencyKey: humanKey(2), QuestionText: "question"}, mustTime(t, 400))
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := store.BeginHumanReply(ctx, client.ID, request.ID, request.Revision, humanDeliveryID(t, 4), "reply", mustTime(t, 401))
	if err != nil || delivery.Revision.Int64() != request.Revision.Int64()+1 || string(delivery.Reply) != "reply" {
		t.Fatalf("begin delivery = %+v, %v", delivery, err)
	}
	if err := store.AcknowledgeHumanReply(ctx, request.ID, delivery.DeliveryID, delivery.Revision, mustTime(t, 402)); err != nil {
		t.Fatalf("acknowledge = %v", err)
	}
	if err := store.AcknowledgeHumanReply(ctx, request.ID, delivery.DeliveryID, delivery.Revision, mustTime(t, 403)); err != nil {
		t.Fatalf("duplicate acknowledge = %v", err)
	}
	projection, _, err := store.HumanRequest(ctx, request.ID)
	if err != nil || projection.Status != HumanRequestResolved {
		t.Fatalf("resolved projection = %+v, %v", projection, err)
	}
	if _, err := store.BeginHumanReply(ctx, client.ID, request.ID, delivery.Revision, humanDeliveryID(t, 5), "retry", mustTime(t, 404)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("resolved reply = %v", err)
	}
}

func TestHumanQuestionReplyAuthorityAndDeliveryCollision(t *testing.T) {
	ctx := context.Background()
	store, run, _ := runningOrchestratorRun(t)
	defer store.Close()
	request, err := store.CreateHumanQuestionForAttempt(ctx, run.CredentialDigest, NewHumanQuestion{IdempotencyKey: humanKey(14), QuestionText: "question"}, mustTime(t, 400))
	if err != nil {
		t.Fatal(err)
	}
	observer := humanQuestionClient(t, store, 208, BrowserCapabilityObserve)
	if _, err := store.BeginHumanReply(ctx, observer.ID, request.ID, request.Revision, humanDeliveryID(t, 15), "reply", mustTime(t, 401)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("observation-only reply = %v", err)
	}
	client := humanQuestionClient(t, store, 209, BrowserCapabilityObserve|BrowserCapabilityHumanActions)
	deliveryID := humanDeliveryID(t, 16)
	delivery, err := store.BeginHumanReply(ctx, client.ID, request.ID, request.Revision, deliveryID, "reply", mustTime(t, 402))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginHumanReply(ctx, client.ID, request.ID, request.Revision, deliveryID, "reply", mustTime(t, 403)); !errors.Is(err, ErrConflict) {
		t.Fatalf("delivery collision = %v", err)
	}
	if err := store.AcknowledgeHumanReply(ctx, request.ID, deliveryID, delivery.Revision, mustTime(t, 404)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginHumanReply(ctx, client.ID, request.ID, delivery.Revision, humanDeliveryID(t, 17), "retry", mustTime(t, 405)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("resolved request reply = %v", err)
	}
}

func TestHumanQuestionRevokedClientCannotReply(t *testing.T) {
	ctx := context.Background()
	store, run, _ := runningOrchestratorRun(t)
	defer store.Close()
	client := humanQuestionClient(t, store, 210, BrowserCapabilityObserve|BrowserCapabilityHumanActions)
	request, err := store.CreateHumanQuestionForAttempt(ctx, run.CredentialDigest, NewHumanQuestion{IdempotencyKey: humanKey(18), QuestionText: "question"}, mustTime(t, 400))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RevokeBrowserClient(ctx, client.ID, client.Revision, mustTime(t, 401)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginHumanReply(ctx, client.ID, request.ID, request.Revision, humanDeliveryID(t, 19), "reply", mustTime(t, 402)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked client reply = %v", err)
	}
}

func TestHumanQuestionBoundAndRunUniquenessAreTransactional(t *testing.T) {
	ctx := context.Background()
	store, run, _ := runningOrchestratorRun(t)
	defer store.Close()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for seed := byte(20); seed < 22; seed++ {
		seed := seed
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.createHumanQuestionForAttempt(ctx, run.CredentialDigest, NewHumanQuestion{IdempotencyKey: humanKey(seed), QuestionText: "question"}, mustTime(t, 400), 1)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	var successes, busy int
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrBusy), errors.Is(err, ErrConflict):
			busy++
		default:
			t.Fatalf("concurrent bounded creation = %v", err)
		}
	}
	if successes != 1 || busy != 1 {
		t.Fatalf("concurrent bounded creation successes=%d busy=%d", successes, busy)
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.HumanRequests) != 1 {
		t.Fatalf("bounded snapshot requests=%d", len(snapshot.HumanRequests))
	}
}

func TestHumanQuestionInvalidationFailureRollsBackRequestAndRun(t *testing.T) {
	ctx := context.Background()
	store, run, keys := runningOrchestratorRun(t)
	defer store.Close()
	requestInput := NewHumanQuestion{IdempotencyKey: humanKey(22), QuestionText: "question"}
	if _, err := store.writer.Exec(`CREATE TRIGGER reject_human_request_invalidation BEFORE INSERT ON invalidations WHEN NEW.entity_kind = 'human_request' BEGIN SELECT RAISE(ABORT, 'forced human request invalidation failure'); END`); err != nil {
		t.Fatal(err)
	}
	beforeFactory, err := store.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateHumanQuestionForAttempt(ctx, run.CredentialDigest, requestInput, mustTime(t, 400)); err == nil {
		t.Fatal("human request creation accepted failed invalidation")
	}
	if _, err := store.writer.Exec(`DROP TRIGGER reject_human_request_invalidation`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.writer.QueryRow(`SELECT COUNT(*) FROM human_requests`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	afterFactory, err := store.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 || afterFactory.Head != beforeFactory.Head {
		t.Fatalf("failed creation left durable state: count=%d before=%d after=%d", count, beforeFactory.Head.Int64(), afterFactory.Head.Int64())
	}
	request, err := store.CreateHumanQuestionForAttempt(ctx, run.CredentialDigest, requestInput, mustTime(t, 401))
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := NewBlockedProposal("waiting")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.Exec(`CREATE TRIGGER reject_human_request_invalidation BEFORE INSERT ON invalidations WHEN NEW.entity_kind = 'human_request' BEGIN SELECT RAISE(ABORT, 'forced human request invalidation failure'); END`); err != nil {
		t.Fatal(err)
	}
	beforeRun, found, err := store.Run(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("run before failed finalizing = %+v, %v", beforeRun, err)
	}
	if _, err := store.ProposeAttemptOutcome(ctx, keys.AttemptDigest, proposal, mustTime(t, 402)); err == nil {
		t.Fatal("finalization accepted failed request invalidation")
	}
	if _, err := store.writer.Exec(`DROP TRIGGER reject_human_request_invalidation`); err != nil {
		t.Fatal(err)
	}
	afterRun, found, err := store.Run(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("run after failed finalizing = %+v, %v", afterRun, err)
	}
	if afterRun.Phase != RunRunning || afterRun.Revision != beforeRun.Revision {
		t.Fatalf("failed finalizing changed run: before=%+v after=%+v", beforeRun, afterRun)
	}
	current, _, err := store.HumanRequest(ctx, request.ID)
	if err != nil || current.Status != HumanRequestOpen {
		t.Fatalf("failed finalizing changed request: %+v, %v", current, err)
	}
}

func TestHumanQuestionCorruptChronologyFailsClosed(t *testing.T) {
	ctx := context.Background()
	store, run, _ := runningOrchestratorRun(t)
	path := storePath(t, store)
	defer store.Close()
	client := humanQuestionClient(t, store, 211, BrowserCapabilityObserve|BrowserCapabilityHumanActions)
	request, err := store.CreateHumanQuestionForAttempt(ctx, run.CredentialDigest, NewHumanQuestion{IdempotencyKey: humanKey(23), QuestionText: "question"}, mustTime(t, 400))
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := store.BeginHumanReply(ctx, client.ID, request.ID, request.Revision, humanDeliveryID(t, 24), "reply", mustTime(t, 401))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.Exec(`PRAGMA ignore_check_constraints = ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.Exec(`UPDATE human_requests SET delivery_started_at_ms = 399 WHERE id = ?`, request.ID.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.Exec(`PRAGMA ignore_check_constraints = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Snapshot(ctx); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("corrupt delivery chronology snapshot = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(ctx, path); !errors.Is(err, ErrCorruptState) {
		if reopened != nil {
			reopened.Close()
		}
		t.Fatalf("reopen corrupt delivery chronology = %v", err)
	}
	_ = delivery
}

func TestHumanQuestionUncertainDeliveryIsNotReplayedAndRestartRecovery(t *testing.T) {
	ctx := context.Background()
	store, run, _ := runningOrchestratorRun(t)
	path := storePath(t, store)
	client := humanQuestionClient(t, store, 204, BrowserCapabilityObserve|BrowserCapabilityHumanActions)
	request, err := store.CreateHumanQuestionForAttempt(ctx, run.CredentialDigest, NewHumanQuestion{IdempotencyKey: humanKey(3), QuestionText: "question"}, mustTime(t, 400))
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := store.BeginHumanReply(ctx, client.ID, request.ID, request.Revision, humanDeliveryID(t, 6), "reply", mustTime(t, 401))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if count, err := reopened.RecoverHumanDeliveries(ctx, mustTime(t, 402)); err != nil || count != 1 {
		t.Fatalf("recovery = count %d, err %v", count, err)
	}
	if count, err := reopened.RecoverHumanDeliveries(ctx, mustTime(t, 403)); err != nil || count != 0 {
		t.Fatalf("idempotent recovery = count %d, err %v", count, err)
	}
	projection, _, err := reopened.HumanRequest(ctx, request.ID)
	if err != nil || projection.Status != HumanRequestDeliveryUnknown {
		t.Fatalf("unknown projection = %+v, %v", projection, err)
	}
	if err := reopened.AcknowledgeHumanReply(ctx, request.ID, delivery.DeliveryID, delivery.Revision, mustTime(t, 404)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("unknown acknowledgement = %v", err)
	}
}

func TestHumanQuestionFinalizingStalesOpenAndMakesDeliveryUnknown(t *testing.T) {
	ctx := context.Background()
	store, run, _ := runningOrchestratorRun(t)
	defer store.Close()
	_ = humanQuestionClient(t, store, 205, BrowserCapabilityObserve|BrowserCapabilityHumanActions)
	open, err := store.CreateHumanQuestionForAttempt(ctx, run.CredentialDigest, NewHumanQuestion{IdempotencyKey: humanKey(9), QuestionText: "open"}, mustTime(t, 400))
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := NewBlockedProposal("waiting")
	if err != nil {
		t.Fatal(err)
	}
	finalizing, err := store.ProposeAttemptOutcome(ctx, run.CredentialDigest, proposal, mustTime(t, 401))
	if err != nil || finalizing.Phase != RunFinalizing {
		t.Fatalf("finalizing = %+v, %v", finalizing, err)
	}
	projection, _, err := store.HumanRequest(ctx, open.ID)
	if err != nil || projection.Status != HumanRequestStale {
		t.Fatalf("staled open request = %+v, %v", projection, err)
	}

	// A second run proves that an already accepted external delivery is never
	// represented as a delivered answer merely because finalization started.
	store2, run2, _ := runningOrchestratorRun(t)
	defer store2.Close()
	client2 := humanQuestionClient(t, store2, 206, BrowserCapabilityObserve|BrowserCapabilityHumanActions)
	request, err := store2.CreateHumanQuestionForAttempt(ctx, run2.CredentialDigest, NewHumanQuestion{IdempotencyKey: humanKey(10), QuestionText: "pending"}, mustTime(t, 400))
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := store2.BeginHumanReply(ctx, client2.ID, request.ID, request.Revision, humanDeliveryID(t, 11), "reply", mustTime(t, 401))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store2.ProposeAttemptOutcome(ctx, run2.CredentialDigest, proposal, mustTime(t, 402)); err != nil {
		t.Fatal(err)
	}
	projection, _, err = store2.HumanRequest(ctx, request.ID)
	if err != nil || projection.Status != HumanRequestDeliveryUnknown {
		t.Fatalf("uncertain delivery after finalizing = %+v, %v", projection, err)
	}
	if err := store2.AcknowledgeHumanReply(ctx, request.ID, delivery.DeliveryID, delivery.Revision, mustTime(t, 403)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("ack after finalizing = %v", err)
	}
}

func TestHumanQuestionTerminalizationStalesResidualDeliveryExactlyOnce(t *testing.T) {
	ctx := context.Background()
	store, run, keys := runningOrchestratorRun(t)
	defer store.Close()
	client := humanQuestionClient(t, store, 207, BrowserCapabilityObserve|BrowserCapabilityHumanActions|BrowserCapabilityPrivateHumanRequestDetail)
	request, err := store.CreateHumanQuestionForAttempt(ctx, run.CredentialDigest, NewHumanQuestion{IdempotencyKey: humanKey(12), QuestionText: "terminal question"}, mustTime(t, 400))
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := store.BeginHumanReply(ctx, client.ID, request.ID, request.Revision, humanDeliveryID(t, 13), "reply", mustTime(t, 401))
	if err != nil {
		t.Fatal(err)
	}
	finalizing, err := store.ProposeAttemptOutcome(ctx, keys.AttemptDigest, func() Proposal { p, _ := NewBlockedProposal("terminal"); return p }(), mustTime(t, 402))
	if err != nil || finalizing.Phase != RunFinalizing {
		t.Fatalf("finalizing = %+v, %v", finalizing, err)
	}
	unknown, _, err := store.HumanRequest(ctx, request.ID)
	if err != nil || unknown.Status != HumanRequestDeliveryUnknown {
		t.Fatalf("delivery after finalizing = %+v, %v", unknown, err)
	}
	observeMissingProcessExits(t, store, run.ID, 403)
	releaseAllRunResources(t, store, run.ID, 410)
	closed := closeTerminalSessionAtCurrent(t, store, run.ID, 415)
	if _, err := store.writer.Exec(`CREATE TRIGGER reject_terminal_human_invalidation BEFORE INSERT ON invalidations WHEN NEW.entity_kind = 'human_request' BEGIN SELECT RAISE(ABORT, 'forced terminal human request invalidation failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinalizeRun(ctx, run.ID, closed.Revision, mustTime(t, 420)); err == nil {
		t.Fatal("terminalization accepted failed request invalidation")
	}
	if _, err := store.writer.Exec(`DROP TRIGGER reject_terminal_human_invalidation`); err != nil {
		t.Fatal(err)
	}
	unchanged, _, err := store.Run(ctx, run.ID)
	if err != nil || unchanged.Phase != RunFinalizing || unchanged.Revision != closed.Revision {
		t.Fatalf("failed terminalization changed run: %+v, %v", unchanged, err)
	}
	terminal, err := store.FinalizeRun(ctx, run.ID, closed.Revision, mustTime(t, 421))
	if err != nil || terminal.Phase != RunTerminal {
		t.Fatalf("terminal = %+v, %v", terminal, err)
	}
	stale, _, err := store.HumanRequest(ctx, request.ID)
	if err != nil || stale.Status != HumanRequestStale {
		t.Fatalf("terminal request = %+v, %v", stale, err)
	}
	if _, err := store.HumanRequestDetail(ctx, client.ID, request.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("closed detail = %v", err)
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.HumanRequests) != 0 {
		t.Fatalf("stale request remains in snapshot: %+v", snapshot.HumanRequests)
	}
	before, err := store.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinalizeRun(ctx, run.ID, terminal.Revision, mustTime(t, 422)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("duplicate terminalization = %v", err)
	}
	after, err := store.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.Head != before.Head {
		t.Fatalf("duplicate terminalization advanced invalidations: %d -> %d", before.Head.Int64(), after.Head.Int64())
	}
	if delivery.RunID != request.RunID {
		t.Fatal("delivery lost exact run binding")
	}
}

func TestHumanQuestionCreationRequiresExactRunningAttempt(t *testing.T) {
	ctx := context.Background()
	store, run, keys := runningOrchestratorRun(t)
	defer store.Close()
	input := NewHumanQuestion{IdempotencyKey: humanKey(7), QuestionText: "question"}
	for _, digest := range []AttemptDigest{keys.AttemptDigest, func() AttemptDigest {
		value, _ := AttemptDigestFromBytes(bytes.Repeat([]byte{0xfe}, DigestBytes))
		return value
	}()} {
		if digest == keys.AttemptDigest {
			continue
		}
		if _, err := store.CreateHumanQuestionForAttempt(ctx, digest, input, mustTime(t, 400)); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("forged attempt = %v", err)
		}
	}
	if _, err := store.CreateHumanQuestionForAttempt(ctx, run.CredentialDigest, input, mustTime(t, run.UpdatedAt.Int64()-1)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale timestamp = %v", err)
	}
	if _, err := store.CreateHumanQuestionForAttempt(ctx, run.CredentialDigest, NewHumanQuestion{IdempotencyKey: humanKey(8), QuestionText: strings.Repeat("x", MaxHumanRequestQuestionBytes+1)}, mustTime(t, 400)); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("oversized question = %v", err)
	}
}
