//go:build darwin

package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

func TestLiveAttemptAttachBeforeReadyIsTypedRetryable(t *testing.T) {
	runID, sessionID := liveTestIDs(t, 11101)
	attempt := newLiveAttempt(nil, runID, sessionID, nil)
	attachment := &TerminalAttachment{queue: make(chan TerminalEvent, terminalSubscriberCap)}
	revision, err := kernel.NewRevision(1)
	if err != nil {
		t.Fatal(err)
	}
	// The supervisor can commit session activation before this owner consumes
	// the runner's ready frame. An attach in that window is early, not wrong:
	// it must classify as retryable busyness on the wire, never internal.
	if err := attempt.handleAttach(attachment, sessionID, revision, revision, 0); !errors.Is(err, ErrTerminalNotReady) {
		t.Fatalf("attach before ready = %v", err)
	}
	if mapped := mapBrowserError(ErrTerminalNotReady); !errors.Is(mapped, browser.ErrRateLimited) {
		t.Fatalf("not-ready wire mapping = %v", mapped)
	}
	// After the result is seen the attach window is over: the durable world
	// moved past the pinned target, so the client re-resolves via the typed
	// stale arm instead of retrying in place.
	for _, readySeen := range []bool{false, true} {
		attempt.readySeen = readySeen
		attempt.resultSeen = true
		if err := attempt.handleAttach(attachment, sessionID, revision, revision, 0); !errors.Is(err, kernel.ErrConflict) {
			t.Fatalf("attach after result ready=%t = %v", readySeen, err)
		}
		if mapped := mapBrowserError(kernel.ErrConflict); !errors.Is(mapped, browser.ErrStale) {
			t.Fatalf("post-result ready=%t wire mapping = %v", readySeen, mapped)
		}
	}
	if len(attempt.subs) != 0 || len(attempt.correlations) != 0 {
		t.Fatal("refused attaches changed subscribers")
	}
}

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

func TestLiveAttemptDeliversResultNoticeAndBroadcastsCommittedExit(t *testing.T) {
	for _, test := range []struct {
		name              string
		event             TerminalEvent
		terminated        bool
		wantCode, wantSig int
		wantAborted       bool
	}{
		{name: "success", event: TerminalEvent{Kind: TerminalEventExit}},
		{name: "failure", event: TerminalEvent{Kind: TerminalEventExit, ExitCode: 7}, wantCode: 7},
		{name: "signal-after-termination", event: TerminalEvent{Kind: TerminalEventExit, ExitSignal: 15}, terminated: true, wantSig: 15, wantAborted: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runID, sessionID := liveTestIDs(t, 11000)
			attempt := newLiveAttempt(nil, runID, sessionID, nil)
			attachment := &TerminalAttachment{queue: make(chan TerminalEvent, terminalSubscriberCap)}
			attempt.subs[attachment] = struct{}{}
			attempt.terminationDelivered = test.terminated
			notice := &runner.AttemptResultNotice{Identity: runner.FileIdentity{Device: 3, Inode: 9}, Digest: "aa"}
			stop, err := attempt.handleRunnerEvent(runner.AttemptEvent{Kind: runner.AttemptResultReady, Result: notice})
			if err != nil || stop {
				t.Fatalf("result event = stop %v err %v", stop, err)
			}
			if !attempt.resultSeen || attempt.resultNotice != notice {
				t.Fatal("result notice was not retained")
			}
			// The shape-only notice must not reach observers as an exit; only the
			// store-committed exit sent by the supervisor is broadcast.
			if len(attachment.queue) != 0 {
				t.Fatalf("notice broadcast to observers: %d queued", len(attachment.queue))
			}
			if result := <-attempt.result; result.err != nil || result.notice != notice {
				t.Fatalf("result delivery = %+v", result)
			}
			command := liveAttemptCommand{kind: liveCommandFinishExit, exit: &test.event, result: make(chan error, 1)}
			stop, err = attempt.handleRunningCommand(command)
			if err != nil || !stop {
				t.Fatalf("finish exit = stop %v err %v", stop, err)
			}
			if commandErr := <-command.result; commandErr != nil {
				t.Fatal(commandErr)
			}
			event, err := attachment.Next(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if event.Kind != TerminalEventExit || event.ExitCode != test.wantCode || event.ExitSignal != test.wantSig || event.Aborted != test.wantAborted {
				t.Fatalf("browser exit = %+v", event)
			}
		})
	}
}

func TestLiveAttemptRejectsExitBroadcastWithoutCommittedResult(t *testing.T) {
	runID, sessionID := liveTestIDs(t, 11001)
	attempt := newLiveAttempt(nil, runID, sessionID, nil)
	attachment := &TerminalAttachment{queue: make(chan TerminalEvent, terminalSubscriberCap)}
	attempt.subs[attachment] = struct{}{}
	if stop, err := attempt.handleRunnerEvent(runner.AttemptEvent{Kind: runner.AttemptResultReady}); stop || !errors.Is(err, runner.ErrState) {
		t.Fatalf("nil notice = stop %v err %v", stop, err)
	}
	if attempt.resultSeen || attempt.resultNotice != nil || len(attempt.result) != 0 {
		t.Fatal("nil notice changed state")
	}
	exit := TerminalEvent{Kind: TerminalEventExit}
	command := liveAttemptCommand{kind: liveCommandFinishExit, exit: &exit, result: make(chan error, 1)}
	if stop, err := attempt.handleRunningCommand(command); stop || err != nil {
		t.Fatalf("finish exit before result = stop %v err %v", stop, err)
	}
	if commandErr := <-command.result; !errors.Is(commandErr, runner.ErrIdentity) {
		t.Fatalf("finish exit before result = %v", commandErr)
	}
	attempt.resultSeen = true
	wrongKind := TerminalEvent{Kind: TerminalEventOutput}
	command = liveAttemptCommand{kind: liveCommandFinishExit, exit: &wrongKind, result: make(chan error, 1)}
	if stop, err := attempt.handleRunningCommand(command); stop || err != nil {
		t.Fatalf("finish exit with wrong kind = stop %v err %v", stop, err)
	}
	if commandErr := <-command.result; !errors.Is(commandErr, runner.ErrIdentity) {
		t.Fatalf("finish exit with wrong kind = %v", commandErr)
	}
	command = liveAttemptCommand{kind: liveCommandFinishExit, result: make(chan error, 1)}
	if stop, err := attempt.handleRunningCommand(command); stop || err != nil {
		t.Fatalf("finish exit with nil event = stop %v err %v", stop, err)
	}
	if commandErr := <-command.result; !errors.Is(commandErr, runner.ErrIdentity) {
		t.Fatalf("finish exit with nil event = %v", commandErr)
	}
	if len(attachment.queue) != 0 {
		t.Fatal("rejected commands reached observers")
	}
}

func TestLiveAttemptRejectsWrongSessionBeforeRunnerAttach(t *testing.T) {
	runID, sessionID := liveTestIDs(t, 10009)
	_, wrongSession := liveTestIDs(t, 10010)
	attempt := newLiveAttempt(nil, runID, sessionID, nil)
	attempt.readySeen = true
	attachment := &TerminalAttachment{queue: make(chan TerminalEvent, terminalSubscriberCap)}
	revision, err := kernel.NewRevision(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := attempt.handleAttach(attachment, wrongSession, revision, revision, 0); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("wrong session attach = %v", err)
	}
	if len(attempt.subs) != 0 || len(attempt.correlations) != 0 {
		t.Fatalf("wrong session changed subscribers: subs=%d correlations=%d", len(attempt.subs), len(attempt.correlations))
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
	if !attempt.controller.Closed() {
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

func TestLiveAttemptEffectTypingSeparatesNotReadyFromPublishedResult(t *testing.T) {
	runID, sessionID := liveTestIDs(t, 11102)
	controller, peer := readyTerminalEffectController(t)
	attempt := newLiveAttempt(nil, runID, sessionID, controller)
	t.Cleanup(func() {
		_ = controller.Close()
		_ = peer.Close()
	})
	effect := terminalEffect{kind: terminalEffectCheck}

	// Before the runner's ready frame the window opens by itself, so the wire
	// must type the refusal retryable — the same condition attach reports.
	result, err := attempt.handleTerminalEffect(effect)
	if err != nil || !errors.Is(result.err, ErrTerminalNotReady) {
		t.Fatalf("pre-ready effect = %+v err=%v", result, err)
	}
	if mapped := mapBrowserError(result.err); !errors.Is(mapped, browser.ErrRateLimited) {
		t.Fatalf("pre-ready wire mapping = %v", mapped)
	}

	// Once the result is published this effect can never succeed. Typing it
	// retryable would invite a client to spin on a permanent condition, so it
	// must reach the wire as stale and keep its terminal fence regardless of
	// whether the runner's ready frame was consumed first.
	for _, readySeen := range []bool{false, true} {
		attempt.readySeen = readySeen
		attempt.resultSeen = true
		result, err = attempt.handleTerminalEffect(effect)
		if err != nil || !errors.Is(result.err, kernel.ErrConflict) {
			t.Fatalf("post-result ready=%t effect = %+v err=%v", readySeen, result, err)
		}
		if !result.terminalFence || result.status != runner.TerminalResultRejected {
			t.Fatalf("post-result ready=%t effect lost its fence or status: %+v", readySeen, result)
		}
		mapped := mapBrowserError(result.err)
		if !errors.Is(mapped, browser.ErrStale) || errors.Is(mapped, browser.ErrRateLimited) {
			t.Fatalf("post-result ready=%t wire mapping = %v", readySeen, mapped)
		}
	}
}

func TestLiveAttemptPreReleaseEffectCompletesThroughOwnerMailbox(t *testing.T) {
	runID, sessionID := liveTestIDs(t, 11103)
	attempt := newLiveAttempt(nil, runID, sessionID, nil)
	ownerContext, cancelOwner := context.WithCancel(context.Background())
	ownerClosed := false
	t.Cleanup(func() {
		cancelOwner()
		if ownerClosed {
			return
		}
		select {
		case <-attempt.done:
		case <-time.After(100 * time.Millisecond):
		}
	})
	startLiveAttempt(attempt, ownerContext)

	done := make(chan terminalEffectResult, 1)
	go func() {
		done <- attempt.submitEffect(context.Background(), terminalEffect{kind: terminalEffectCheck})
	}()
	select {
	case result := <-done:
		if result.status != runner.TerminalResultRejected || !errors.Is(result.err, ErrTerminalNotReady) {
			t.Fatalf("pre-release owner effect = %+v", result)
		}
		_ = attempt.close()
		ownerClosed = true
	case <-time.After(time.Second):
		t.Fatal("pre-release owner effect remained blocked in the mailbox")
	}
}
