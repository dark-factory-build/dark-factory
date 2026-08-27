package kernel

import (
	"context"
	"errors"
	"testing"
)

func TestHumanRequestProjectionTerminalAvailabilityFollowsExactLifecycle(t *testing.T) {
	ctx := context.Background()
	store, run, keys := runningOrchestratorRun(t)
	defer store.Close()
	client := humanQuestionClient(t, store, 180, BrowserCapabilityObserve|BrowserCapabilityHumanActions)
	request, err := store.CreateHumanQuestionForAttempt(ctx, run.CredentialDigest, NewHumanQuestion{IdempotencyKey: humanKey(180), QuestionText: "private question"}, mustTime(t, 400))
	if err != nil {
		t.Fatal(err)
	}
	assertHumanRequestProjectionSources(t, store, request.ID, HumanRequestOpen, true)

	delivery, err := store.BeginHumanReply(ctx, client.ID, request.ID, request.Revision, humanDeliveryID(t, 181), "private reply", mustTime(t, 401))
	if err != nil {
		t.Fatal(err)
	}
	assertHumanRequestProjectionSources(t, store, request.ID, HumanRequestDelivering, true)
	if err := store.MarkHumanDeliveryUnknown(ctx, request.ID, delivery.DeliveryID, delivery.Revision, mustTime(t, 402)); err != nil {
		t.Fatal(err)
	}
	assertHumanRequestProjectionSources(t, store, request.ID, HumanRequestDeliveryUnknown, true)

	proposal, err := NewBlockedProposal("waiting")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ProposeAttemptOutcome(ctx, keys.AttemptDigest, proposal, mustTime(t, 403)); err != nil {
		t.Fatal(err)
	}
	assertHumanRequestProjectionSources(t, store, request.ID, HumanRequestDeliveryUnknown, false)
}

func TestHumanRequestProjectionResolvedAndStaleNeverOpenTerminal(t *testing.T) {
	ctx := context.Background()

	resolvedStore, resolvedRun, _ := runningOrchestratorRun(t)
	defer resolvedStore.Close()
	client := humanQuestionClient(t, resolvedStore, 182, BrowserCapabilityObserve|BrowserCapabilityHumanActions)
	resolved, err := resolvedStore.CreateHumanQuestionForAttempt(ctx, resolvedRun.CredentialDigest, NewHumanQuestion{IdempotencyKey: humanKey(182), QuestionText: "resolved"}, mustTime(t, 400))
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := resolvedStore.BeginHumanReply(ctx, client.ID, resolved.ID, resolved.Revision, humanDeliveryID(t, 183), "reply", mustTime(t, 401))
	if err != nil {
		t.Fatal(err)
	}
	if err := resolvedStore.AcknowledgeHumanReply(ctx, resolved.ID, delivery.DeliveryID, delivery.Revision, mustTime(t, 402)); err != nil {
		t.Fatal(err)
	}
	assertHumanRequestDirectProjection(t, resolvedStore, resolved.ID, HumanRequestResolved, false)

	staleStore, staleRun, staleKeys := runningOrchestratorRun(t)
	defer staleStore.Close()
	stale, err := staleStore.CreateHumanQuestionForAttempt(ctx, staleRun.CredentialDigest, NewHumanQuestion{IdempotencyKey: humanKey(184), QuestionText: "stale"}, mustTime(t, 400))
	if err != nil {
		t.Fatal(err)
	}
	proposal, _ := NewBlockedProposal("done")
	if _, err := staleStore.ProposeAttemptOutcome(ctx, staleKeys.AttemptDigest, proposal, mustTime(t, 401)); err != nil {
		t.Fatal(err)
	}
	assertHumanRequestDirectProjection(t, staleStore, stale.ID, HumanRequestStale, false)
}

func TestHumanRequestProjectionKnownNonAttachableTerminalStatesAreFalse(t *testing.T) {
	for _, test := range []struct {
		name string
		sql  string
	}{
		{name: "declared", sql: `UPDATE terminal_sessions SET state = 'declared', unresolved_reason = NULL, activated_at_ms = NULL, closed_at_ms = NULL, updated_at_ms = declared_at_ms WHERE run_id = ?`},
		{name: "unresolved", sql: `UPDATE terminal_sessions SET state = 'unresolved', unresolved_reason = 'owner uncertain', closed_at_ms = NULL WHERE run_id = ?`},
		{name: "closed", sql: `UPDATE terminal_sessions SET state = 'closed', unresolved_reason = NULL, closed_at_ms = updated_at_ms WHERE run_id = ?`},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, run, _ := runningOrchestratorRun(t)
			defer store.Close()
			request, err := store.CreateHumanQuestionForAttempt(context.Background(), run.CredentialDigest, NewHumanQuestion{IdempotencyKey: humanKey(185), QuestionText: "private"}, mustTime(t, 400))
			if err != nil {
				t.Fatal(err)
			}
			corruptSQL(t, store, test.sql, run.ID.Bytes())
			assertHumanRequestDirectProjection(t, store, request.ID, HumanRequestOpen, false)
		})
	}
}

func TestHumanRequestProjectionMissingOrMalformedDurableControlsFailClosed(t *testing.T) {
	for _, test := range []struct {
		name string
		sql  string
	}{
		{name: "missing terminal session", sql: `DELETE FROM terminal_sessions WHERE run_id = ?`},
		{name: "unknown terminal state", sql: `UPDATE terminal_sessions SET state = 'unknown' WHERE run_id = ?`},
		{name: "malformed terminal chronology", sql: `UPDATE terminal_sessions SET activated_at_ms = NULL WHERE run_id = ?`},
		{name: "unknown run phase", sql: `UPDATE runs SET phase = 'unknown' WHERE id = ?`},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, run, _ := runningOrchestratorRun(t)
			defer store.Close()
			request, err := store.CreateHumanQuestionForAttempt(context.Background(), run.CredentialDigest, NewHumanQuestion{IdempotencyKey: humanKey(186), QuestionText: "private"}, mustTime(t, 400))
			if err != nil {
				t.Fatal(err)
			}
			corruptSQL(t, store, test.sql, run.ID.Bytes())
			if projection, found, err := store.HumanRequest(context.Background(), request.ID); !errors.Is(err, ErrCorruptState) || found || projection != (HumanRequestProjection{}) {
				t.Fatalf("projection = %+v, found=%v, err=%v", projection, found, err)
			}
		})
	}
}

func TestHumanRequestProjectionReadPinsRunAndTerminalState(t *testing.T) {
	ctx := context.Background()
	store, run, keys := runningOrchestratorRun(t)
	defer store.Close()
	request, err := store.CreateHumanQuestionForAttempt(ctx, run.CredentialDigest, NewHumanQuestion{IdempotencyKey: humanKey(187), QuestionText: "private"}, mustTime(t, 400))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.beginRead(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	before, found, err := humanRequestProjectionByID(ctx, tx.connection, request.ID)
	if err != nil || !found || !before.CanOpenTerminal {
		t.Fatalf("before = %+v, found=%v, err=%v", before, found, err)
	}
	proposal, _ := NewBlockedProposal("waiting")
	if _, err := store.ProposeAttemptOutcome(ctx, keys.AttemptDigest, proposal, mustTime(t, 401)); err != nil {
		t.Fatal(err)
	}
	pinned, found, err := humanRequestProjectionByID(ctx, tx.connection, request.ID)
	if err != nil || !found || !pinned.CanOpenTerminal || pinned.Status != HumanRequestOpen {
		t.Fatalf("pinned = %+v, found=%v, err=%v", pinned, found, err)
	}
	if err := tx.Close(); err != nil {
		t.Fatal(err)
	}
	current, found, err := store.HumanRequest(ctx, request.ID)
	if err != nil || !found || current.CanOpenTerminal || current.Status != HumanRequestStale {
		t.Fatalf("current = %+v, found=%v, err=%v", current, found, err)
	}
}

func assertHumanRequestDirectProjection(t *testing.T, store *Store, id HumanRequestID, status HumanRequestStatus, canOpenTerminal bool) {
	t.Helper()
	projection, found, err := store.HumanRequest(context.Background(), id)
	if err != nil || !found || projection.Status != status || projection.CanOpenTerminal != canOpenTerminal {
		t.Fatalf("projection = %+v, found=%v, err=%v, want status=%s can_open_terminal=%v", projection, found, err, status.String(), canOpenTerminal)
	}
}

func assertHumanRequestProjectionSources(t *testing.T, store *Store, id HumanRequestID, status HumanRequestStatus, canOpenTerminal bool) {
	t.Helper()
	ctx := context.Background()
	assertHumanRequestDirectProjection(t, store, id, status, canOpenTerminal)

	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var fromSnapshot *HumanRequestProjection
	for index := range snapshot.HumanRequests {
		if snapshot.HumanRequests[index].ID == id {
			fromSnapshot = &snapshot.HumanRequests[index]
			break
		}
	}
	if fromSnapshot == nil || fromSnapshot.Status != status || fromSnapshot.CanOpenTerminal != canOpenTerminal {
		t.Fatalf("snapshot projection = %+v, want status=%s can_open_terminal=%v", fromSnapshot, status.String(), canOpenTerminal)
	}

	state, err := store.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.ReadPublicStatePage(ctx, &PublicStateCursor{Head: state.Head, Kind: PublicStateHumanRequest})
	if err != nil {
		t.Fatal(err)
	}
	var fromPage *HumanRequestProjection
	for _, item := range page.Items {
		projection, ok := item.HumanRequest()
		if ok && projection.ID == id {
			fromPage = &projection
			break
		}
	}
	if fromPage == nil || fromPage.Status != status || fromPage.CanOpenTerminal != canOpenTerminal {
		t.Fatalf("page projection = %+v, want status=%s can_open_terminal=%v", fromPage, status.String(), canOpenTerminal)
	}

	entity, err := store.ReadPublicStateEntity(ctx, PublicStateHumanRequest, mustPublicStateID(t, id.Bytes()))
	if err != nil || entity.Item == nil {
		t.Fatalf("entity = %+v, err=%v", entity, err)
	}
	fromEntity, ok := entity.Item.HumanRequest()
	if !ok || fromEntity.Status != status || fromEntity.CanOpenTerminal != canOpenTerminal {
		t.Fatalf("entity projection = %+v, ok=%v, want status=%s can_open_terminal=%v", fromEntity, ok, status.String(), canOpenTerminal)
	}
}
