//go:build darwin

package daemon

import (
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/runner"
)

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
	// A late frame for the retired observer must be harmless. In particular it
	// must not close the already-closed queue a second time.
	attempt.routeLive(attachment, runner.TerminalFrame{Kind: runner.TerminalOutput, Start: 1, End: 2, Payload: []byte{'y'}})
	if len(attempt.retired) != 1 {
		t.Fatalf("retired subscriber correlation count = %d", len(attempt.retired))
	}
}
