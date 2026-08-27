//go:build darwin

package daemon

import (
	"context"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

func TestLiveAttemptGlobalResetAdvancesEveryObserverCursor(t *testing.T) {
	runID, sessionID := liveTestIDs(t, 10006)
	attempt := newLiveAttempt(nil, runID, sessionID, nil)
	first := &TerminalAttachment{queue: make(chan TerminalEvent, terminalSubscriberCap), correlation: 1, expected: 3, replayHead: 10, replaying: true, pending: []TerminalEvent{{Kind: TerminalEventOutput}}}
	second := &TerminalAttachment{queue: make(chan TerminalEvent, terminalSubscriberCap), correlation: 2, expected: 4, replayHead: 10, replaying: true}
	attempt.subs[first] = struct{}{}
	attempt.subs[second] = struct{}{}
	attempt.correlations[1] = first
	attempt.correlations[2] = second
	attempt.lastCorrelation = 2
	if err := attempt.routeFrame(runner.TerminalFrame{Kind: runner.TerminalReset, Floor: 8, Head: 11}); err != nil {
		t.Fatal(err)
	}
	for name, subscriber := range map[string]*TerminalAttachment{"first": first, "second": second} {
		if subscriber.replaying || subscriber.expected != 11 || subscriber.replayHead != 11 || len(subscriber.pending) != 0 || subscriber.correlation != 0 {
			t.Fatalf("%s reset state = %+v", name, subscriber)
		}
		if _, present := attempt.subs[subscriber]; !present {
			t.Fatalf("%s observer was detached by global reset", name)
		}
		event, ok := <-subscriber.queue
		if !ok || event.Kind != TerminalEventReset || event.Floor != 8 || event.Head != 11 {
			t.Fatalf("%s reset event = %+v, present=%v", name, event, ok)
		}
	}
}

func TestLiveAttemptOwnerClosesControllerBeforeDoneOnReadError(t *testing.T) {
	daemon := &Daemon{attempts: make(map[kernel.RunID]*liveAttempt)}
	runID, sessionID := liveTestIDs(t, 10007)
	controller, peer, err := runner.NewAttemptController()
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}
	attempt := newLiveAttempt(daemon, runID, sessionID, controller)
	attempt.releaseSent = true
	if err := daemon.registerLiveAttempt(attempt); err != nil {
		t.Fatal(err)
	}
	startLiveAttempt(attempt, context.Background())
	if err := attempt.join(); err == nil {
		t.Fatal("owner read error was hidden")
	}
	if !attempt.controllerClosed {
		t.Fatal("owner published done before controller closure")
	}
}

func TestLiveAttemptSlowSubscriberIsDroppedExactlyOnce(t *testing.T) {
	runID, sessionID := liveTestIDs(t, 10004)
	attempt := newLiveAttempt(nil, runID, sessionID, nil)
	attachment := &TerminalAttachment{queue: make(chan TerminalEvent, terminalSubscriberCap), correlation: 1, expected: 0}
	for range terminalSubscriberCap {
		attachment.queue <- TerminalEvent{Kind: TerminalEventOutput}
	}
	attempt.subs[attachment] = struct{}{}
	attempt.routeLive(attachment, runner.TerminalFrame{Kind: runner.TerminalOutput, Start: 0, End: 1, Payload: []byte{'x'}})
	if _, present := attempt.subs[attachment]; present || !attachment.finished {
		t.Fatalf("slow subscriber remained registered: present=%v finished=%v", present, attachment.finished)
	}
	// A stale delivery path must be harmless after the queue has been closed;
	// the owner removes the subscriber before any later correlated frame can
	// route to it.
	attempt.routeLive(attachment, runner.TerminalFrame{Kind: runner.TerminalOutput, Start: 1, End: 2, Payload: []byte{'y'}})
	// A late frame for an already allocated correlation is harmless. A frame
	// from a correlation the owner never allocated is a protocol violation.
	attempt.lastCorrelation = 3
	attempt.creditOutstanding = liveAttemptCredit
	if err := attempt.routeFrame(runner.TerminalFrame{Kind: runner.TerminalOutput, Correlation: 2, Payload: []byte{'y'}}); err != nil {
		t.Fatalf("late retired correlation error = %v", err)
	}
	if err := attempt.routeFrame(runner.TerminalFrame{Kind: runner.TerminalOutput, Correlation: 4, Payload: []byte{'z'}}); err == nil {
		t.Fatal("unallocated correlation was accepted")
	}
}

func TestLiveAttemptReplayPendingBytesAreBounded(t *testing.T) {
	runID, sessionID := liveTestIDs(t, 10008)
	attempt := newLiveAttempt(nil, runID, sessionID, nil)
	attachment := &TerminalAttachment{
		queue:      make(chan TerminalEvent, terminalSubscriberCap),
		replaying:  true,
		replayHead: 0,
		expected:   0,
	}
	attempt.subs[attachment] = struct{}{}
	for i := 0; i < terminalPendingBytesCap/terminalPayloadCap; i++ {
		attempt.routeLive(attachment, runner.TerminalFrame{
			Kind: runner.TerminalOutput, Start: 0, End: 1,
			Payload: make([]byte, terminalPayloadCap),
		})
	}
	if _, present := attempt.subs[attachment]; !present {
		t.Fatal("subscriber was dropped before reaching pending byte bound")
	}
	if got := attachment.pendingBytes; got != terminalPendingBytesCap {
		t.Fatalf("pending bytes = %d, want %d", got, terminalPendingBytesCap)
	}
	attempt.routeLive(attachment, runner.TerminalFrame{
		Kind: runner.TerminalOutput, Start: 0, End: 1,
		Payload: make([]byte, terminalPayloadCap),
	})
	if _, present := attempt.subs[attachment]; present || !attachment.finished {
		t.Fatal("subscriber survived pending byte overflow")
	}
}
