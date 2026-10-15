package kernel

import (
	"bytes"
	"context"
	"testing"
)

func TestProviderAdoptionFailureKeepsNeedsYouThroughRunFailure(t *testing.T) {
	store, run, keys := runningWorkerRun(t)
	defer store.Close()
	active := bytes.Repeat([]byte{5}, DigestBytes)
	candidate := bytes.Repeat([]byte{6}, DigestBytes)
	if _, err := store.ObserveProviderCandidate(context.Background(), ProviderCodex, "/old/codex", active, candidate, "/new/codex", mustTime(t, 30)); err != nil {
		t.Fatal(err)
	}
	_, request, err := store.CompleteProviderAdoptionForAttempt(context.Background(), keys.AttemptDigest, ProviderCodex, candidate, "attempt MCP unavailable", mustTime(t, 31))
	if err != nil {
		t.Fatalf("failed adoption question: %v", err)
	}
	failure, err := NewFailureProposal(FailureProviderExit, "provider adoption self-check failed")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FailRunProviderExit(context.Background(), run.ID, run.Revision, failure, mustTime(t, 32)); err != nil {
		t.Fatalf("fail run: %v", err)
	}
	read := requestForTest(t, store, request.ID)
	if read.Status != HumanRequestOpen || read.Continuation != ContinuationWaiting {
		t.Fatalf("adoption question after run failure = %+v", read)
	}
	projection, found, err := store.HumanRequest(context.Background(), request.ID)
	if err != nil || !found || !projection.CanReply {
		t.Fatalf("adoption question projection = %+v found=%v err=%v", projection, found, err)
	}
}

func TestProviderAdoptionKeepsFailedCandidateAcrossRestart(t *testing.T) {
	store, path := newTestStore(t)
	active := bytes.Repeat([]byte{1}, DigestBytes)
	candidate := bytes.Repeat([]byte{2}, DigestBytes)
	got, err := store.ObserveProviderCandidate(context.Background(), ProviderCodex, "/old/codex", active, candidate, "/new/codex", mustTime(t, 10))
	if err != nil || got.CandidateState != "pending" || !bytes.Equal(got.ActiveCommitment, active) {
		t.Fatalf("observe = %+v, %v", got, err)
	}
	got, err = store.CompleteProviderAdoption(context.Background(), ProviderCodex, candidate, false, "MCP self-check failed", mustTime(t, 11))
	if err != nil || got.CandidateState != "failed" || got.ActivePath != "/old/codex" || got.Diagnostic != "MCP self-check failed" {
		t.Fatalf("failure = %+v, %v", got, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, found, err := reopened.ProviderAdoption(context.Background(), ProviderCodex)
	if err != nil || !found || got.CandidateState != "failed" || got.ActivePath != "/old/codex" || got.Diagnostic != "MCP self-check failed" {
		t.Fatalf("reopened = %+v found=%v err=%v", got, found, err)
	}
	got, err = reopened.ObserveProviderCandidate(context.Background(), ProviderCodex, "/old/codex", active, candidate, "/new/codex", mustTime(t, 12))
	if err != nil || got.CandidateState != "failed" || got.Revision.Int64() != 2 {
		t.Fatalf("reobserve failed candidate = %+v, %v", got, err)
	}
}

func TestProviderAdoptionPromotesOnlyTheCheckedCandidate(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	active := bytes.Repeat([]byte{3}, DigestBytes)
	candidate := bytes.Repeat([]byte{4}, DigestBytes)
	if _, err := store.ObserveProviderCandidate(context.Background(), ProviderClaudeCode, "/old/claude", active, candidate, "/new/claude", mustTime(t, 20)); err != nil {
		t.Fatal(err)
	}
	got, err := store.CompleteProviderAdoption(context.Background(), ProviderClaudeCode, candidate, true, "", mustTime(t, 21))
	if err != nil || got.CandidateState != "none" || got.ActivePath != "/new/claude" || !bytes.Equal(got.ActiveCommitment, candidate) {
		t.Fatalf("promotion = %+v, %v", got, err)
	}
}
