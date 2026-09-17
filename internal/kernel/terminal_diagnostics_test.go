package kernel

import (
	"bytes"
	"context"
	"testing"
)

func TestTerminalDiagnosticsSurviveRestartAndRemainBounded(t *testing.T) {
	ctx := context.Background()
	store, run, keys := runningOrchestratorRun(t)
	path := storePath(t, store)
	proposal, err := NewFailureProposal(FailureInternal, "provider exited before an attempt outcome")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ProposeAttemptOutcome(ctx, keys.AttemptDigest, proposal, mustTime(t, 40)); err != nil {
		t.Fatal(err)
	}
	payload := []byte("first\nprivate /Users/operator/.secret\nlast\n")
	if err := store.SaveTerminalDiagnostics(ctx, TerminalDiagnostics{RunID: run.ID, Floor: 4, Head: 4 + uint64(len(payload)), Payload: payload, CapturedAt: mustTime(t, 41)}); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.TerminalDiagnostics(ctx, run.ID)
	if err != nil || !found || got.Floor != 4 || got.Head != 4+uint64(len(payload)) || !bytes.Equal(got.Payload, payload) {
		t.Fatalf("diagnostics = %+v found=%v err=%v", got, found, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, found, err = reopened.TerminalDiagnostics(ctx, run.ID)
	if err != nil || !found || !bytes.Equal(got.Payload, payload) {
		t.Fatalf("reopened diagnostics = %+v found=%v err=%v", got, found, err)
	}
}
