package daemon

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func liveTestIDs(t *testing.T, value uint64) (kernel.RunID, kernel.TerminalSessionID) {
	t.Helper()
	rawRun := make([]byte, kernel.IDBytes)
	binary.BigEndian.PutUint64(rawRun[8:], value+1)
	run, err := kernel.RunIDFromBytes(rawRun)
	if err != nil {
		t.Fatal(err)
	}
	rawSession := make([]byte, kernel.IDBytes)
	binary.BigEndian.PutUint64(rawSession[8:], value+1)
	rawSession[0] = 1
	session, err := kernel.TerminalSessionIDFromBytes(rawSession)
	if err != nil {
		t.Fatal(err)
	}
	return run, session
}

func TestLiveAttemptRegistryIsBoundedAndRejectsDuplicateRuns(t *testing.T) {
	daemon := &Daemon{attempts: make(map[kernel.RunID]*liveAttempt)}
	attempts := make([]*liveAttempt, 0, maxLiveAttempts)
	for i := 0; i < maxLiveAttempts; i++ {
		runID, sessionID := liveTestIDs(t, uint64(i))
		attempt := newLiveAttempt(daemon, runID, sessionID, nil)
		if err := daemon.registerLiveAttempt(attempt); err != nil {
			t.Fatalf("register attempt %d: %v", i, err)
		}
		attempts = append(attempts, attempt)
	}
	duplicate := newLiveAttempt(daemon, attempts[0].runID, attempts[0].sessionID, nil)
	if err := daemon.registerLiveAttempt(duplicate); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("duplicate registration error = %v", err)
	}
	runID, sessionID := liveTestIDs(t, maxLiveAttempts+1)
	if err := daemon.registerLiveAttempt(newLiveAttempt(daemon, runID, sessionID, nil)); !errors.Is(err, kernel.ErrBusy) {
		t.Fatalf("over-capacity registration error = %v", err)
	}
	daemon.unregisterLiveAttempt(attempts[0].runID, attempts[0])
	if err := daemon.registerLiveAttempt(newLiveAttempt(daemon, runID, sessionID, nil)); err != nil {
		t.Fatalf("registration after removal: %v", err)
	}
}

func TestLiveAttemptSubmitDoesNotWaitForExitedOwner(t *testing.T) {
	runID, sessionID := liveTestIDs(t, 10000)
	attempt := newLiveAttempt(nil, runID, sessionID, nil)
	close(attempt.done)
	finished := make(chan error, 1)
	go func() {
		finished <- attempt.submit(context.Background(), liveAttemptCommand{kind: liveCommandAttach})
	}()
	select {
	case err := <-finished:
		if !errors.Is(err, ErrTerminalClosed) {
			t.Fatalf("exited owner error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("submit blocked after owner exit")
	}
}

func TestLiveAttemptSubmitReturnsWhenOwnerExitsAfterAcceptingCommand(t *testing.T) {
	runID, sessionID := liveTestIDs(t, 10003)
	attempt := newLiveAttempt(nil, runID, sessionID, nil)
	received := make(chan struct{})
	release := make(chan struct{})
	go func() {
		<-attempt.commands
		close(received)
		<-release
		close(attempt.done)
	}()
	result := make(chan error, 1)
	go func() {
		result <- attempt.submit(context.Background(), liveAttemptCommand{kind: liveCommandAttach})
	}()
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("owner did not accept command")
	}
	close(release)
	select {
	case err := <-result:
		if !errors.Is(err, ErrTerminalClosed) {
			t.Fatalf("accepted command after owner exit error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("accepted command blocked after owner exit")
	}
}

func TestLiveAttemptShutdownClosesRegistryToNewOwners(t *testing.T) {
	daemon := &Daemon{attempts: make(map[kernel.RunID]*liveAttempt)}
	if err := daemon.closeLiveAttempts(); err != nil {
		t.Fatalf("empty live-attempt shutdown: %v", err)
	}
	runID, sessionID := liveTestIDs(t, 10000)
	if err := daemon.registerLiveAttempt(newLiveAttempt(daemon, runID, sessionID, nil)); !errors.Is(err, ErrTerminalClosed) {
		t.Fatalf("registration after shutdown error = %v", err)
	}
}

func TestTerminalAttachmentCloseIsConcurrentAndIdempotent(t *testing.T) {
	runID, sessionID := liveTestIDs(t, 10001)
	attempt := newLiveAttempt(nil, runID, sessionID, nil)
	close(attempt.done)
	attachment := &TerminalAttachment{owner: attempt, runID: runID, session: sessionID, queue: make(chan TerminalEvent, 1)}
	results := make(chan error, 2)
	var group sync.WaitGroup
	group.Add(2)
	for range 2 {
		go func() {
			defer group.Done()
			results <- attachment.Close()
		}()
	}
	group.Wait()
	close(results)
	for err := range results {
		if !errors.Is(err, ErrTerminalClosed) {
			t.Fatalf("concurrent close error = %v", err)
		}
	}
	if err := attachment.Close(); !errors.Is(err, ErrTerminalClosed) {
		t.Fatalf("repeated close error = %v", err)
	}
}

func TestTerminalAttachmentFinishAndCorrelationTombstonesAreBounded(t *testing.T) {
	runID, sessionID := liveTestIDs(t, 10002)
	attempt := newLiveAttempt(nil, runID, sessionID, nil)
	for i := uint64(1); i <= terminalSubscriberCap*3; i++ {
		attempt.retireCorrelation(i)
	}
	if len(attempt.retired) > terminalSubscriberCap || len(attempt.retiredOrder) > terminalSubscriberCap {
		t.Fatalf("retired correlations grew beyond bound: map=%d order=%d", len(attempt.retired), len(attempt.retiredOrder))
	}
	attachment := &TerminalAttachment{queue: make(chan TerminalEvent, 1)}
	closeErr := errors.New("closed")
	attachment.finish(closeErr)
	attachment.finish(errors.New("double close"))
	if _, err := attachment.Next(context.Background()); !errors.Is(err, closeErr) {
		t.Fatalf("finished attachment error = %v", err)
	}
	if attachment.enqueue(TerminalEvent{Kind: TerminalEventOutput, Payload: make([]byte, terminalPayloadCap+1)}) {
		t.Fatal("oversized terminal payload was queued")
	}
}
