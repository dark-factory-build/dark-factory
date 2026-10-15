package kernel

import (
	"context"
	"errors"
	"testing"
)

func TestProviderAdoptionPromotesCandidateAndSurvivesRestart(t *testing.T) {
	store, path := newTestStore(t)
	ctx := context.Background()
	pending, err := store.RecordProviderCandidate(ctx, ProviderCodex, "codex-v1", mustTime(t, 10))
	if err != nil || pending.ActiveCommitment != "" || pending.CandidateCommitment != "codex-v1" || pending.NeedsYou {
		t.Fatalf("pending adoption = %+v, %v", pending, err)
	}
	active, err := store.CommitProviderCandidate(ctx, ProviderCodex, pending.Revision, ProviderSelfCheck{Started: true, ReadCheckout: true, Committed: true, AttemptAPI: true}, mustTime(t, 11))
	if err != nil || active.ActiveCommitment != "codex-v1" || active.CandidateCommitment != "" {
		t.Fatalf("active adoption = %+v, %v", active, err)
	}
	replayed, err := store.CommitProviderCandidate(ctx, ProviderCodex, pending.Revision, ProviderSelfCheck{Started: true, ReadCheckout: true, Committed: true, AttemptAPI: true}, mustTime(t, 12))
	if err != nil || replayed.Revision != active.Revision || replayed.ActiveCommitment != active.ActiveCommitment {
		t.Fatalf("promotion replay = %+v, %v", replayed, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, found, err := reopened.ProviderAdoption(ctx, ProviderCodex)
	if err != nil || !found || got.ActiveCommitment != "codex-v1" || got.CandidateCommitment != "" {
		t.Fatalf("restarted adoption = %+v found=%v err=%v", got, found, err)
	}
}

func TestProviderAdoptionFailureRetainsPriorVersionAndRaisesOneNeedYou(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	prior, err := store.RecordProviderCandidate(ctx, ProviderClaudeCode, "claude-v1", mustTime(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	prior, err = store.CommitProviderCandidate(ctx, ProviderClaudeCode, prior.Revision, ProviderSelfCheck{Started: true, ReadCheckout: true, Committed: true, AttemptAPI: true}, mustTime(t, 11))
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := store.RecordProviderCandidate(ctx, ProviderClaudeCode, "claude-v2", mustTime(t, 12))
	if err != nil {
		t.Fatal(err)
	}
	failed, err := store.FailProviderCandidate(ctx, ProviderClaudeCode, candidate.Revision, "self-check: attempt API unavailable", mustTime(t, 13))
	if err != nil || failed.ActiveCommitment != "claude-v1" || failed.CandidateState != ProviderAdoptionFailed || !failed.NeedsYou || failed.FailureDetail == "" {
		t.Fatalf("failed adoption = %+v, %v", failed, err)
	}
	replay, err := store.RecordProviderCandidate(ctx, ProviderClaudeCode, "claude-v2", mustTime(t, 14))
	if err != nil || replay.Revision != failed.Revision || replay.FailureDetail != failed.FailureDetail {
		t.Fatalf("failed candidate replay = %+v, %v", replay, err)
	}
	if _, err := store.CommitProviderCandidate(ctx, ProviderClaudeCode, failed.Revision, ProviderSelfCheck{Started: true, ReadCheckout: true, Committed: true, AttemptAPI: true}, mustTime(t, 15)); !errors.Is(err, ErrConflict) {
		t.Fatalf("failed candidate promotion = %v", err)
	}
}

func TestProviderAdoptionRequiresEverySelfCheckEdge(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	pending, err := store.RecordProviderCandidate(ctx, ProviderCodex, "codex-v1", mustTime(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitProviderCandidate(ctx, ProviderCodex, pending.Revision, ProviderSelfCheck{Started: true, ReadCheckout: true, Committed: true}, mustTime(t, 11)); !errors.Is(err, ErrConflict) {
		t.Fatalf("incomplete self-check promotion = %v", err)
	}
	got, found, err := store.ProviderAdoption(ctx, ProviderCodex)
	if err != nil || !found || got.ActiveCommitment != "" || got.CandidateCommitment != "codex-v1" {
		t.Fatalf("incomplete self-check changed state = %+v found=%v err=%v", got, found, err)
	}
}
