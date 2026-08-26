package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
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

func TestHumanQuestionUncertainDeliveryIsNotReplayedAndRestartRecovery(t *testing.T) {
	ctx := context.Background()
	store, run, _ := runningOrchestratorRun(t)
	defer store.Close()
	client := humanQuestionClient(t, store, 204, BrowserCapabilityObserve|BrowserCapabilityHumanActions)
	request, err := store.CreateHumanQuestionForAttempt(ctx, run.CredentialDigest, NewHumanQuestion{IdempotencyKey: humanKey(3), QuestionText: "question"}, mustTime(t, 400))
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := store.BeginHumanReply(ctx, client.ID, request.ID, request.Revision, humanDeliveryID(t, 6), "reply", mustTime(t, 401))
	if err != nil {
		t.Fatal(err)
	}
	if count, err := store.RecoverHumanDeliveries(ctx, mustTime(t, 402)); err != nil || count != 1 {
		t.Fatalf("recovery = count %d, err %v", count, err)
	}
	if count, err := store.RecoverHumanDeliveries(ctx, mustTime(t, 403)); err != nil || count != 0 {
		t.Fatalf("idempotent recovery = count %d, err %v", count, err)
	}
	projection, _, err := store.HumanRequest(ctx, request.ID)
	if err != nil || projection.Status != HumanRequestDeliveryUnknown {
		t.Fatalf("unknown projection = %+v, %v", projection, err)
	}
	if err := store.AcknowledgeHumanReply(ctx, request.ID, delivery.DeliveryID, delivery.Revision, mustTime(t, 404)); !errors.Is(err, ErrRevisionConflict) {
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
