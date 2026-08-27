//go:build darwin

package daemon

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

const liveAttemptPoll = 100 * time.Millisecond

func startLiveAttempt(attempt *liveAttempt, ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	go attempt.run(ctx)
}

func (attempt *liveAttempt) run(ctx context.Context) {
	err := attempt.loop(ctx)
	// The owner must retain controller authority until it has synchronously
	// requested convergence and closed the capability. A read/protocol/Store
	// error is not evidence that the child disappeared.
	cleanupErr := attempt.shutdownController()
	err = errors.Join(err, cleanupErr)
	if !attempt.terminalSeen {
		if err == nil {
			err = ErrTerminalClosed
		}
	}
	attempt.finalErr = err
	if !attempt.terminalSeen {
		attempt.terminal <- liveAttemptResult{err: err}
	}
	attempt.finishSubscribers(err)
	close(attempt.done)
	if attempt.daemon != nil {
		attempt.daemon.unregisterLiveAttempt(attempt.runID, attempt)
	}
}

func (attempt *liveAttempt) loop(ctx context.Context) error {
	// The owner is normally started before releaseSent becomes true. Keeping
	// this derived from the owner state also makes a restarted/test owner
	// converge immediately when release has already been durably sent.
	released := attempt.releaseSent
	for {
		if !released {
			select {
			case command := <-attempt.commands:
				stop, err := attempt.handleBeforeRelease(ctx, command)
				if err != nil {
					return err
				}
				if stop {
					return nil
				}
				if attempt.releaseSent {
					released = true
				}
			case <-ctx.Done():
				return ctx.Err()
			case <-attempt.wake:
			}
			continue
		}

		if stop, err := attempt.processLifecycle(ctx); err != nil {
			return err
		} else if stop {
			return nil
		}
		select {
		case command := <-attempt.commands:
			stop, err := attempt.handleRunningCommand(command)
			if err != nil {
				return err
			}
			if stop {
				return nil
			}
			continue
		default:
		}
		select {
		case <-attempt.wake:
			continue
		default:
		}
		ready, err := attempt.controller.NextReady(liveAttemptPoll)
		if err != nil {
			return err
		}
		if !ready {
			continue
		}
		event, err := attempt.controller.Next(8 * time.Second)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("daemon: attempt controller closed")
			}
			return err
		}
		if stop, err := attempt.handleRunnerEvent(event); err != nil {
			return err
		} else if stop {
			return nil
		}
	}
}

func (attempt *liveAttempt) handleBeforeRelease(ctx context.Context, command liveAttemptCommand) (bool, error) {
	switch command.kind {
	case liveCommandReleaseProvider:
		if ctx.Err() != nil {
			command.result <- ctx.Err()
			return false, ctx.Err()
		}
		if attempt.daemon == nil || attempt.daemon.store == nil {
			err := kernel.ErrCorruptState
			command.result <- err
			return false, err
		}
		// Durable finalization and the irreversible provider release share one
		// linearization gate. Re-read both run and session state while holding
		// it; a stale supervisor observation can never release a run that has
		// already entered finalizing.
		attempt.daemon.operationMu.Lock()
		storeCtx, cancel := context.WithTimeout(context.Background(), liveAttemptStoreTimeout)
		run, found, err := attempt.daemon.store.Run(storeCtx, attempt.runID)
		cancel()
		if err == nil && (!found || run.Phase != kernel.RunRunning) {
			err = kernel.ErrConflict
		}
		if err == nil {
			storeCtx, cancel = context.WithTimeout(context.Background(), liveAttemptStoreTimeout)
			session, sessionFound, sessionErr := attempt.daemon.store.TerminalSessionForRun(storeCtx, attempt.runID)
			cancel()
			if sessionErr != nil {
				err = sessionErr
			} else if !sessionFound || session.ID != attempt.sessionID || session.State != kernel.TerminalSessionActive {
				err = kernel.ErrConflict
			}
		}
		if err == nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		if err == nil {
			err = attempt.controller.Release(runner.StageProvider)
		}
		attempt.daemon.operationMu.Unlock()
		if err != nil {
			command.result <- err
			return false, err
		}
		attempt.releaseSent = true
		command.result <- nil
		return false, nil
	case liveCommandShutdown:
		err := attempt.shutdownController()
		command.result <- err
		return true, err
	default:
		err := ErrTerminalNotReady
		command.result <- err
		return false, nil
	}
}

func (attempt *liveAttempt) processLifecycle(ctx context.Context) (bool, error) {
	if attempt.terminationSent || attempt.terminalSeen {
		return false, nil
	}
	if ctx.Err() != nil {
		return false, attempt.terminateController()
	}
	if attempt.daemon == nil || attempt.daemon.store == nil {
		return false, nil
	}
	storeCtx, cancel := context.WithTimeout(context.Background(), liveAttemptStoreTimeout)
	run, found, err := attempt.daemon.store.Run(storeCtx, attempt.runID)
	cancel()
	if err != nil {
		return false, err
	}
	if !found {
		return false, kernel.ErrCorruptState
	}
	if run.Phase == kernel.RunFinalizing {
		return false, attempt.terminateController()
	}
	return false, nil
}

func (attempt *liveAttempt) terminateController() error {
	if attempt.terminationSent {
		return nil
	}
	err := attempt.controller.Terminate()
	if errors.Is(err, runner.ErrState) {
		return nil
	}
	if err != nil {
		return err
	}
	attempt.terminationSent = true
	return nil
}

func (attempt *liveAttempt) handleRunningCommand(command liveAttemptCommand) (bool, error) {
	switch command.kind {
	case liveCommandAttach:
		err := attempt.handleAttach(command.attachment, command.session, command.sequence)
		command.result <- err
		// Validation and durable-state conflicts belong to this request, not to
		// the provider. A controller write failure poisons the controller itself;
		// the next owner read observes that and the run cleanup below converges it.
		return false, nil
	case liveCommandDetach:
		err := attempt.handleDetach(command.attachment)
		command.result <- err
		return false, nil
	case liveCommandAcknowledge:
		if !attempt.terminalSeen || attempt.terminalEvent == nil || command.terminal == nil || *command.terminal != *attempt.terminalEvent.Terminal {
			err := runner.ErrIdentity
			command.result <- err
			return false, nil
		}
		err := attempt.controller.AcknowledgeTerminal(command.terminal, true)
		command.result <- err
		if err != nil {
			return false, err
		}
		return true, nil
	case liveCommandShutdown:
		err := attempt.shutdownController()
		command.result <- err
		return true, err
	default:
		command.result <- runner.ErrState
		return false, nil
	}
}

func (attempt *liveAttempt) shutdownController() error {
	if attempt == nil || attempt.controller == nil {
		return nil
	}
	var result error
	if !attempt.terminalSeen {
		result = attempt.terminateController()
	}
	closeErr := attempt.controller.Close()
	if closeErr == nil || errors.Is(closeErr, runner.ErrState) {
		attempt.controllerClosed = true
	} else {
		result = errors.Join(result, closeErr)
	}
	return result
}

func (attempt *liveAttempt) handleRunnerEvent(event runner.AttemptEvent) (bool, error) {
	switch event.Kind {
	case runner.AttemptTerminalFrame:
		if event.Frame == nil {
			return false, runner.ErrState
		}
		return false, attempt.routeFrame(*event.Frame)
	case runner.AttemptTerminal:
		if event.Terminal == nil {
			return false, runner.ErrState
		}
		attempt.terminalSeen = true
		attempt.terminalEvent = &event
		attempt.broadcast(TerminalEvent{Kind: TerminalEventExit, ExitCode: event.Terminal.Terminal.Exit.Code, ExitSignal: event.Terminal.Terminal.Exit.Signal, Aborted: event.Terminal.Terminal.Exit.Aborted})
		attempt.closeSubscribers(ErrTerminalClosed)
		attempt.terminal <- liveAttemptResult{event: event}
		return false, nil
	default:
		return false, runner.ErrState
	}
}

func (attempt *liveAttempt) routeFrame(frame runner.TerminalFrame) error {
	switch frame.Kind {
	case runner.TerminalReady:
		if attempt.readySeen {
			return runner.ErrState
		}
		attempt.readySeen = true
		return nil
	case runner.TerminalAttached:
		return attempt.routeAttached(frame)
	case runner.TerminalOutput:
		if frame.Correlation == 0 {
			for subscriber := range attempt.subs {
				attempt.routeLive(subscriber, frame)
			}
		} else if subscriber := attempt.correlations[frame.Correlation]; subscriber != nil {
			attempt.routeReplay(subscriber, frame)
		} else if frame.Correlation > attempt.lastCorrelation {
			return runner.ErrState
		}
		return attempt.replenishCredit(uint64(len(frame.Payload)))
	case runner.TerminalReset:
		if frame.Correlation == 0 {
			for subscriber := range attempt.subs {
				attempt.resetSubscriber(subscriber, frame)
			}
			return nil
		}
		if subscriber := attempt.correlations[frame.Correlation]; subscriber != nil {
			attempt.finishReplay(subscriber, toTerminalEvent(frame), frame.Head, false)
		} else if frame.Correlation > attempt.lastCorrelation {
			return runner.ErrState
		}
		return nil
	case runner.TerminalPTYEOF:
		attempt.broadcast(TerminalEvent{Kind: TerminalEventPTYEOF})
		return nil
	default:
		return runner.ErrState
	}
}

func (attempt *liveAttempt) routeAttached(frame runner.TerminalFrame) error {
	subscriber := attempt.correlations[frame.Correlation]
	if subscriber == nil {
		if frame.Correlation <= attempt.lastCorrelation {
			return nil
		}
		return runner.ErrState
	}
	event := toTerminalEvent(frame)
	if !subscriber.enqueue(event) {
		attempt.dropSubscriber(subscriber, ErrTerminalSlow)
		return nil
	}
	if frame.Status == runner.TerminalResultRejected {
		attempt.dropSubscriber(subscriber, ErrTerminalReset)
		return nil
	}
	subscriber.replayHead = frame.Head
	if frame.Sequence == frame.Head {
		attempt.finishReplay(subscriber, TerminalEvent{}, frame.Head, true)
	}
	return nil
}

func (attempt *liveAttempt) routeReplay(subscriber *TerminalAttachment, frame runner.TerminalFrame) {
	if subscriber == nil {
		return
	}
	if !subscriber.replaying {
		if !attempt.acceptOutput(subscriber, toTerminalEvent(frame)) {
			attempt.dropSubscriber(subscriber, ErrTerminalReset)
		}
		return
	}
	if frame.End > subscriber.replayHead || frame.Start < subscriber.expected {
		if frame.End <= subscriber.expected {
			return
		}
		attempt.dropSubscriber(subscriber, ErrTerminalReset)
		return
	}
	if !attempt.acceptOutput(subscriber, toTerminalEvent(frame)) {
		attempt.dropSubscriber(subscriber, ErrTerminalReset)
		return
	}
	if frame.End == subscriber.replayHead {
		attempt.finishReplay(subscriber, TerminalEvent{}, frame.End, true)
	}
}

func (attempt *liveAttempt) routeLive(subscriber *TerminalAttachment, frame runner.TerminalFrame) {
	if subscriber == nil {
		return
	}
	if _, present := attempt.subs[subscriber]; !present {
		return
	}
	if subscriber.replaying {
		if frame.End <= subscriber.replayHead {
			return
		}
		event := toTerminalEvent(frame)
		if frame.Start < subscriber.replayHead || len(subscriber.pending) >= terminalPendingCap || subscriber.pendingBytes+len(event.Payload) > terminalPendingBytesCap {
			attempt.dropSubscriber(subscriber, ErrTerminalReset)
			return
		}
		subscriber.pending = append(subscriber.pending, event)
		subscriber.pendingBytes += len(event.Payload)
		return
	}
	if !attempt.acceptOutput(subscriber, toTerminalEvent(frame)) {
		attempt.dropSubscriber(subscriber, ErrTerminalReset)
	}
}

func (attempt *liveAttempt) finishReplay(subscriber *TerminalAttachment, event TerminalEvent, expected uint64, accepted bool) {
	subscriber.expected = expected
	subscriber.replaying = false
	if event.Kind != 0 {
		if !subscriber.enqueue(event) {
			attempt.dropSubscriber(subscriber, ErrTerminalSlow)
			return
		}
	}
	if !accepted {
		attempt.dropSubscriber(subscriber, ErrTerminalReset)
		return
	}
	for _, pending := range subscriber.pending {
		subscriber.pendingBytes -= len(pending.Payload)
		if !attempt.acceptOutput(subscriber, pending) {
			attempt.dropSubscriber(subscriber, ErrTerminalReset)
			return
		}
	}
	subscriber.pending = nil
	subscriber.pendingBytes = 0
}

func (attempt *liveAttempt) acceptOutput(subscriber *TerminalAttachment, event TerminalEvent) bool {
	if event.Start < subscriber.expected {
		return event.End <= subscriber.expected
	}
	if event.Start > subscriber.expected {
		return false
	}
	if !subscriber.enqueue(event) {
		return false
	}
	subscriber.expected = event.End
	return true
}

func (attempt *liveAttempt) handleAttach(attachment *TerminalAttachment, sessionID kernel.TerminalSessionID, sequence uint64) error {
	// The operation gate is acquired below before reading durable phase/session
	// state. An attach processed before terminalSeen/finalizing wins; after
	// either boundary it is refused, never delivered across that boundary.
	if !attempt.readySeen || attempt.terminalSeen {
		return ErrTerminalNotReady
	}
	if sessionID == (kernel.TerminalSessionID{}) || sessionID != attempt.sessionID {
		return kernel.ErrConflict
	}
	if attachment == nil || len(attempt.correlations) >= terminalSubscriberCap {
		return kernel.ErrBusy
	}
	if attempt.daemon == nil || attempt.daemon.store == nil {
		return kernel.ErrCorruptState
	}
	attempt.daemon.operationMu.Lock()
	defer attempt.daemon.operationMu.Unlock()
	attempt.daemon.attemptMu.Lock()
	closing := attempt.daemon.closing
	attempt.daemon.attemptMu.Unlock()
	if closing {
		return ErrTerminalClosed
	}
	storeCtx, cancel := context.WithTimeout(context.Background(), liveAttemptStoreTimeout)
	session, found, err := attempt.daemon.store.TerminalSessionForRun(storeCtx, attempt.runID)
	cancel()
	if err != nil {
		return err
	}
	if !found || session.ID != attempt.sessionID || session.State != kernel.TerminalSessionActive {
		return kernel.ErrConflict
	}
	storeCtx, cancel = context.WithTimeout(context.Background(), liveAttemptStoreTimeout)
	run, found, err := attempt.daemon.store.Run(storeCtx, attempt.runID)
	cancel()
	if err != nil {
		return err
	}
	if !found {
		return kernel.ErrCorruptState
	}
	if run.Phase != kernel.RunRunning {
		return kernel.ErrConflict
	}
	correlation, err := attempt.nextCorrelation()
	if err != nil {
		return err
	}
	attachment.correlation = correlation
	attachment.expected = sequence
	attachment.replaying = true
	attempt.subs[attachment] = struct{}{}
	attempt.correlations[correlation] = attachment
	if err := attempt.controller.SendTerminalCommand(runner.TerminalCommand{Kind: runner.TerminalAttach, Correlation: correlation, Sequence: sequence}); err != nil {
		attempt.removeSubscriber(attachment)
		return err
	}
	if attempt.creditOutstanding == 0 {
		if err := attempt.addCredit(liveAttemptCredit); err != nil {
			attempt.removeSubscriber(attachment)
			return err
		}
	}
	return nil
}

func (attempt *liveAttempt) handleDetach(attachment *TerminalAttachment) error {
	if attachment == nil {
		return nil
	}
	attempt.removeSubscriber(attachment)
	return nil
}

func (attempt *liveAttempt) nextCorrelation() (uint64, error) {
	if attempt.lastCorrelation == ^uint64(0)>>1 {
		return 0, runner.ErrIdentity
	}
	attempt.lastCorrelation++
	return attempt.lastCorrelation, nil
}

func (attempt *liveAttempt) addCredit(credit uint64) error {
	if credit == 0 || credit > liveAttemptCredit || attempt.creditOutstanding+credit > liveAttemptCredit {
		return runner.ErrState
	}
	if err := attempt.controller.SendTerminalCommand(runner.TerminalCommand{Kind: runner.TerminalCredit, Credit: uint32(credit)}); err != nil {
		return err
	}
	attempt.creditOutstanding += credit
	return nil
}

func (attempt *liveAttempt) replenishCredit(consumed uint64) error {
	if consumed > attempt.creditOutstanding {
		return runner.ErrIdentity
	}
	attempt.creditOutstanding -= consumed
	if attempt.creditOutstanding < liveAttemptCredit/2 {
		return attempt.addCredit(liveAttemptCredit - attempt.creditOutstanding)
	}
	return nil
}

func (attempt *liveAttempt) routeEventTo(subscriber *TerminalAttachment, event TerminalEvent) {
	if !subscriber.enqueue(event) {
		attempt.dropSubscriber(subscriber, ErrTerminalSlow)
	}
}

func (attempt *liveAttempt) resetSubscriber(subscriber *TerminalAttachment, frame runner.TerminalFrame) {
	// A global reset means the runner's live cursor can no longer cover the
	// observer's previous position. Retire any correlated replay, move the
	// live cursor to the advertised head, and let the browser reattach from
	// that exact point. This keeps the reset scoped to each observer without
	// replaying stale bytes.
	if subscriber == nil {
		return
	}
	if subscriber.correlation != 0 {
		delete(attempt.correlations, subscriber.correlation)
		subscriber.correlation = 0
	}
	subscriber.pending = nil
	subscriber.pendingBytes = 0
	subscriber.replaying = false
	subscriber.expected = frame.Head
	subscriber.replayHead = frame.Head
	attempt.routeEventTo(subscriber, toTerminalEvent(frame))
}

func (attempt *liveAttempt) broadcast(event TerminalEvent) {
	for subscriber := range attempt.subs {
		attempt.routeEventTo(subscriber, event)
	}
}

func (attempt *liveAttempt) removeSubscriber(subscriber *TerminalAttachment) {
	if subscriber == nil {
		return
	}
	if _, present := attempt.subs[subscriber]; !present {
		return
	}
	delete(attempt.subs, subscriber)
	if subscriber.correlation != 0 {
		delete(attempt.correlations, subscriber.correlation)
	}
	subscriber.finish(ErrTerminalClosed)
}

func (attempt *liveAttempt) dropSubscriber(subscriber *TerminalAttachment, err error) {
	if subscriber == nil {
		return
	}
	if _, present := attempt.subs[subscriber]; !present {
		return
	}
	delete(attempt.subs, subscriber)
	if subscriber.correlation != 0 {
		delete(attempt.correlations, subscriber.correlation)
	}
	subscriber.finish(err)
}

func (attempt *liveAttempt) closeSubscribers(err error) {
	for subscriber := range attempt.subs {
		attempt.dropSubscriber(subscriber, err)
	}
}

func (attempt *liveAttempt) finishSubscribers(err error) {
	if err == nil {
		err = ErrTerminalClosed
	}
	for subscriber := range attempt.subs {
		attempt.dropSubscriber(subscriber, err)
	}
}

func toTerminalEvent(frame runner.TerminalFrame) TerminalEvent {
	return TerminalEvent{Kind: terminalEventKind(frame.Kind), Accepted: frame.Status == runner.TerminalResultOK, Sequence: frame.Sequence, Start: frame.Start, End: frame.End, Floor: frame.Floor, Head: frame.Head, Payload: append([]byte(nil), frame.Payload...)}
}

func terminalEventKind(kind runner.TerminalEventKind) TerminalEventKind {
	switch kind {
	case runner.TerminalAttached:
		return TerminalEventAttached
	case runner.TerminalOutput:
		return TerminalEventOutput
	case runner.TerminalReset:
		return TerminalEventReset
	case runner.TerminalPTYEOF:
		return TerminalEventPTYEOF
	default:
		return 0
	}
}
