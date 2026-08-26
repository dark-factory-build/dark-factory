package kernel

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestActivateRunRequiresExactTerminalSessionAndRevision(t *testing.T) {
	store, run, keys := admittedOrchestratorRun(t)
	defer store.Close()
	activateAllResources(t, store, run, keys, 20)
	wrong, err := TerminalSessionIDFromBytes(bytes.Repeat([]byte{0xee}, IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateRun(context.Background(), run.ID, wrong, run.Revision, mustRevision(t, 1), mustTime(t, 30)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong terminal session activation = %v", err)
	}
	session := terminalSessionForRunTest(t, store, run.ID)
	active, err := store.ActivateRun(context.Background(), run.ID, session.ID, run.Revision, session.Revision, mustTime(t, 30))
	if err != nil || active.Phase != RunRunning {
		t.Fatalf("activation = %+v, err=%v", active, err)
	}
	session, found, err := store.TerminalSession(context.Background(), session.ID)
	if err != nil || !found || session.State != TerminalSessionActive || session.Revision.Int64() != 2 {
		t.Fatalf("active terminal session = %+v, found=%v, err=%v", session, found, err)
	}
}

func TestTerminalSessionActivationAndLiveCloseAreDurableTransitions(t *testing.T) {
	store, _, keys := runningOrchestratorRun(t)
	defer store.Close()
	proposal, err := NewSuccessProposal("done")
	if err != nil {
		t.Fatal(err)
	}
	finalizing, err := store.ProposeAttemptOutcome(context.Background(), keys.AttemptDigest, proposal, mustTime(t, 40))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CloseActiveTerminalSession(context.Background(), finalizing.ID, keys.TerminalSessionID, finalizing.Revision, terminalSessionForRunTest(t, store, finalizing.ID).Revision, mustTime(t, 40)); !errors.Is(err, ErrConflict) {
		t.Fatalf("close before provider evidence = %v", err)
	}
	observeMissingProcessExits(t, store, finalizing.ID, 45)
	releaseAllRunResources(t, store, finalizing.ID, 50)
	fresh, found, err := store.Run(context.Background(), finalizing.ID)
	if err != nil || !found {
		t.Fatalf("finalizing run = %+v, found=%v, err=%v", fresh, found, err)
	}
	session := terminalSessionForRunTest(t, store, fresh.ID)
	closed, err := store.CloseActiveTerminalSession(context.Background(), fresh.ID, session.ID, fresh.Revision, session.Revision, mustTime(t, 60))
	if err != nil || closed.State != TerminalSessionClosed || closed.Revision.Int64() != session.Revision.Int64()+1 {
		t.Fatalf("closed session = %+v, err=%v", closed, err)
	}
	before, _, err := store.Run(context.Background(), fresh.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeFactory, err := store.Factory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.CloseActiveTerminalSession(context.Background(), fresh.ID, session.ID, fresh.Revision, session.Revision, mustTime(t, 60))
	if err != nil || replay.ID != closed.ID || replay.Revision != closed.Revision || replay.State != closed.State {
		t.Fatalf("duplicate close replay = %+v, err=%v", replay, err)
	}
	after, _, err := store.Run(context.Background(), fresh.ID)
	if err != nil {
		t.Fatal(err)
	}
	afterFactory, err := store.Factory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision || afterFactory.Head != beforeFactory.Head {
		t.Fatalf("duplicate close advanced authority: run %d -> %d, head %d -> %d", before.Revision.Int64(), after.Revision.Int64(), beforeFactory.Head.Int64(), afterFactory.Head.Int64())
	}
	current, _, _ := store.Run(context.Background(), fresh.ID)
	terminal, err := store.FinalizeRun(context.Background(), fresh.ID, current.Revision, mustTime(t, 70))
	if err != nil || terminal.Phase != RunTerminal {
		t.Fatalf("terminal run = %+v, err=%v", terminal, err)
	}
	if _, err := store.CloseActiveTerminalSession(context.Background(), fresh.ID, session.ID, current.Revision, closed.Revision, mustTime(t, 71)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("duplicate close = %v", err)
	}
}

func TestTerminalSessionUnresolvedCannotTerminalize(t *testing.T) {
	store, run, keys := runningOrchestratorRun(t)
	defer store.Close()
	proposal, _ := NewFailureProposal(FailureInternal, "uncertain")
	finalizing, err := store.ProposeAttemptOutcome(context.Background(), keys.AttemptDigest, proposal, mustTime(t, 40))
	if err != nil {
		t.Fatal(err)
	}
	session := terminalSessionForRunTest(t, store, run.ID)
	unresolved, err := store.MarkTerminalSessionUnresolved(context.Background(), run.ID, session.ID, finalizing.Revision, session.Revision, "owner uncertain", mustTime(t, 41))
	if err != nil || unresolved.State != TerminalSessionUnresolved {
		t.Fatalf("unresolved = %+v, err=%v", unresolved, err)
	}
	reopened := terminalSessionForRunTest(t, store, run.ID)
	if reopened.State != TerminalSessionUnresolved || reopened.UnresolvedReason != "owner uncertain" {
		t.Fatalf("reopened unresolved = %+v", reopened)
	}
	current, found, err := store.Run(context.Background(), run.ID)
	if err != nil || !found {
		t.Fatalf("unresolved run reload = %+v, found=%v, err=%v", current, found, err)
	}
	if _, err := store.FinalizeRun(context.Background(), run.ID, current.Revision, mustTime(t, 50)); !errors.Is(err, ErrConflict) {
		t.Fatalf("unresolved terminalization = %v", err)
	}
}
