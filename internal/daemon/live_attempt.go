package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

const (
	maxLiveAttempts       = kernel.MaxFactoryCapacity + 1
	liveAttemptMailboxCap = 64
	// Sixteen observers covers several tabs/devices while keeping the fixed
	// per-run queue budget small and auditable.
	terminalSubscriberCap = 16
	// One credited replay can fill 128 terminal payload frames before the
	// browser's smaller ACK window resumes its attachment reader. Keep that
	// bounded replay plus its attach control event instead of treating it as a
	// slow observer.
	terminalSubscriberEventCap = liveAttemptCredit/terminalPayloadCap + 1
	terminalPendingCap         = 64
	terminalPayloadCap         = 8 << 10
	terminalPendingBytesCap    = 256 << 10
	liveAttemptCredit          = 1 << 20
	liveAttemptStoreTimeout    = 2 * time.Second
	liveAttemptEffectLimit     = 4 * time.Second
)

var (
	ErrTerminalNotReady = errors.New("daemon: terminal is not ready")
	ErrTerminalClosed   = errors.New("daemon: terminal attachment is closed")
	ErrTerminalSlow     = errors.New("daemon: terminal attachment was too slow")
	ErrTerminalReset    = errors.New("daemon: terminal attachment requires reset")
)

// TerminalEvent is the daemon-owned, browser-facing terminal projection. It
// intentionally contains no runner frames, process identities or descriptors.
type TerminalEvent = browser.TerminalEvent
type TerminalEventKind = browser.TerminalEventKind

const (
	TerminalEventAttached = browser.TerminalEventAttached
	TerminalEventOutput   = browser.TerminalEventOutput
	TerminalEventReset    = browser.TerminalEventReset
	TerminalEventPTYEOF   = browser.TerminalEventPTYEOF
	TerminalEventExit     = browser.TerminalEventExit
)

// TerminalAttachment is one read-only observer of one exact live terminal.
// The owner loop is the only writer or closer of queue. Close synchronously
// submits a detach command and waits until the owner has removed the observer.
type TerminalAttachment struct {
	owner *liveAttempt
	queue chan TerminalEvent

	mu          sync.Mutex
	closed      bool
	finished    bool
	closeErr    error
	closeDone   chan struct{}
	closeResult error

	// The fields below are owned by the attempt loop. They are not read by
	// callers; the mutex above protects only Close/Next lifecycle state.
	correlation  uint64
	expected     uint64
	replayHead   uint64
	replaying    bool
	pending      []TerminalEvent
	pendingBytes int
}

func (attachment *TerminalAttachment) Events() <-chan browser.TerminalEvent {
	if attachment == nil {
		return nil
	}
	return attachment.queue
}

func (attachment *TerminalAttachment) ResetRequired() bool {
	if attachment == nil {
		return false
	}
	attachment.mu.Lock()
	defer attachment.mu.Unlock()
	return errors.Is(attachment.closeErr, ErrTerminalSlow) || errors.Is(attachment.closeErr, ErrTerminalReset)
}

// Next waits for one bounded terminal event. Context cancellation only stops
// this observer; it never affects the provider or the live attempt owner.
func (attachment *TerminalAttachment) Next(ctx context.Context) (TerminalEvent, error) {
	if attachment == nil || ctx == nil {
		return TerminalEvent{}, ErrTerminalClosed
	}
	select {
	case event, ok := <-attachment.queue:
		if !ok {
			attachment.mu.Lock()
			err := attachment.closeErr
			attachment.mu.Unlock()
			if err == nil {
				err = ErrTerminalClosed
			}
			return TerminalEvent{}, err
		}
		return event, nil
	case <-ctx.Done():
		return TerminalEvent{}, ctx.Err()
	}
}

// Close removes this observer through the owning attempt loop. It is safe to
// call repeatedly and does not stop the provider.
func (attachment *TerminalAttachment) Close() error {
	if attachment == nil {
		return nil
	}
	attachment.mu.Lock()
	if attachment.finished {
		attachment.mu.Unlock()
		return nil
	}
	if attachment.closed {
		done := attachment.closeDone
		attachment.mu.Unlock()
		if done != nil {
			<-done
			attachment.mu.Lock()
			err := attachment.closeResult
			attachment.mu.Unlock()
			return err
		}
		return nil
	}
	attachment.closed = true
	if attachment.closeDone == nil {
		attachment.closeDone = make(chan struct{})
	}
	done := attachment.closeDone
	attachment.mu.Unlock()
	if attachment.owner == nil {
		attachment.mu.Lock()
		attachment.closeResult = nil
		close(done)
		attachment.mu.Unlock()
		return nil
	}
	err := attachment.owner.detach(context.Background(), attachment)
	attachment.mu.Lock()
	attachment.closeResult = err
	close(done)
	attachment.mu.Unlock()
	return err
}

func (attachment *TerminalAttachment) finish(err error) {
	if attachment == nil {
		return
	}
	attachment.mu.Lock()
	if attachment.finished {
		attachment.mu.Unlock()
		return
	}
	attachment.finished = true
	if attachment.closeErr == nil {
		attachment.closeErr = err
	}
	close(attachment.queue)
	attachment.mu.Unlock()
}

func (attachment *TerminalAttachment) enqueue(event TerminalEvent) bool {
	if attachment == nil || len(event.Payload) > terminalPayloadCap {
		return false
	}
	select {
	case attachment.queue <- event:
		return true
	default:
		return false
	}
}

// AttachTerminal validates the durable run/session relationship before it
// creates an in-memory observer. The runner and its replay ring remain hidden
// behind the attempt owner.
func (daemon *Daemon) AttachTerminal(ctx context.Context, runID kernel.RunID, sessionID kernel.TerminalSessionID, expectedRun, expectedSession kernel.Revision, sequence uint64) (*TerminalAttachment, error) {
	if daemon == nil || daemon.store == nil || ctx == nil || runID == (kernel.RunID{}) || sessionID == (kernel.TerminalSessionID{}) {
		return nil, fmt.Errorf("%w: invalid terminal attachment", kernel.ErrInvalidValue)
	}
	daemon.attemptMu.Lock()
	if daemon.closing {
		daemon.attemptMu.Unlock()
		return nil, ErrTerminalClosed
	}
	attempt := daemon.attempts[runID]
	daemon.attemptMu.Unlock()
	if attempt == nil {
		return nil, kernel.ErrNotFound
	}
	daemon.operationMu.Lock()
	defer daemon.operationMu.Unlock()
	return attempt.attach(ctx, sessionID, expectedRun, expectedSession, sequence)
}

type liveAttemptCommandKind uint8

const (
	liveCommandReleaseProvider liveAttemptCommandKind = iota + 1
	liveCommandAttach
	liveCommandDetach
	liveCommandEffect
	liveCommandFinishExit
	liveCommandShutdown
)

type liveAttemptCommand struct {
	kind                         liveAttemptCommandKind
	attachment                   *TerminalAttachment
	session                      kernel.TerminalSessionID
	expectedRun, expectedSession kernel.Revision
	sequence                     uint64
	exit                         *TerminalEvent
	result                       chan error
	effect                       *terminalEffect
	effectDone                   chan terminalEffectResult
}

type liveAttemptResult struct {
	notice            *runner.AttemptResultNotice
	err               error
	observersRetained bool
}

// liveAttempt is intentionally concrete. Except for the receipt fence named
// below, mutable fields belong to its one owner goroutine after construction;
// the registry mutex protects only the daemon's map membership.
type liveAttempt struct {
	daemon *Daemon
	runID  kernel.RunID
	// Immutable observation facts from the inspected source, installed before registration.
	agentID    kernel.AgentID
	changeID   kernel.ChangeID
	pathsSince kernel.UnixMillis
	sessionID  kernel.TerminalSessionID
	controller *runner.AttemptController
	// sourceOps counts explicit source requests in flight for this attempt,
	// so shutdown refuses new ones and waits for admitted ones to finish.
	sourceOpsMu        sync.Mutex
	sourceOpsDone      chan struct{}
	sourceCloseStarted chan struct{}
	sourceOps          int
	sourceClosing      bool
	attemptDigest      kernel.AttemptDigest

	commands chan liveAttemptCommand
	wake     chan struct{}
	done     chan struct{}
	result   chan liveAttemptResult

	// outcomeReceiptPending is set and cleared only under daemon.operationMu.
	// It spans durable finalization through the reporting client's validated
	// response acknowledgement, without holding that global gate during I/O.
	outcomeReceiptPending bool
	// outcomeRefusal is written and consumed under daemon.operationMu. It is a
	// typed refusal from this exact bearer, never a generic unauthorized hint.
	outcomeRefusal error
	// pendingOutcome retains the exact proposal whose live API call was refused
	// until the authenticated runner result gives the supervisor one final,
	// owner-bound chance to commit it. It is not terminal authority and is never
	// recovered or replayed after this owner is gone.
	pendingOutcome *kernel.Proposal

	subs            map[*TerminalAttachment]struct{}
	correlations    map[uint64]*TerminalAttachment
	lastCorrelation uint64

	readySeen            bool
	releaseSent          bool
	terminationSent      bool
	terminationDelivered bool
	resultReturned       bool
	shutdownRequested    bool
	resultNotice         *runner.AttemptResultNotice
	creditOutstanding    uint64
	finalErr             error
	binding              terminalBinding
	effectLimit          time.Duration
	// These exact seams are fixed before the owner starts. Production uses the
	// concrete Store renewal below; daemon tests replace it or pause one phase
	// to prove ambiguous Store and operation-gate schedules causally.
	renewLease               terminalLeaseRenewal
	beforeRenewCommit        func()
	beforeAttachEffect       func()
	beforeProviderStateCheck func() error
}

func newLiveAttempt(daemon *Daemon, runID kernel.RunID, sessionID kernel.TerminalSessionID, controller *runner.AttemptController) *liveAttempt {
	attempt := &liveAttempt{
		daemon: daemon, runID: runID, sessionID: sessionID, controller: controller,
		commands: make(chan liveAttemptCommand, liveAttemptMailboxCap),
		wake:     make(chan struct{}, 1), done: make(chan struct{}), result: make(chan liveAttemptResult, 1),
		subs: make(map[*TerminalAttachment]struct{}), correlations: make(map[uint64]*TerminalAttachment),
		sourceOpsDone: make(chan struct{}), sourceCloseStarted: make(chan struct{}),
		effectLimit: liveAttemptEffectLimit,
	}
	if daemon != nil && daemon.store != nil {
		attempt.renewLease = func(ctx context.Context, client kernel.BrowserClientID, generation uint64, expectedRun, expectedSession kernel.Revision, at kernel.UnixMillis) (kernel.TerminalLease, error) {
			return daemon.store.RenewTerminalLease(ctx, runID, sessionID, client, generation, expectedRun, expectedSession, at)
		}
	}
	return attempt
}

func (attempt *liveAttempt) beginSourceOperation() bool {
	attempt.sourceOpsMu.Lock()
	defer attempt.sourceOpsMu.Unlock()
	if attempt.sourceClosing {
		return false
	}
	if attempt.sourceOps == 0 {
		// A completed request may be followed by another one. Each non-empty
		// generation needs its own drain signal; a closed channel is never
		// reopened or reused.
		attempt.sourceOpsDone = make(chan struct{})
	}
	attempt.sourceOps++
	return true
}

func (attempt *liveAttempt) endSourceOperation() {
	attempt.sourceOpsMu.Lock()
	defer attempt.sourceOpsMu.Unlock()
	attempt.sourceOps--
	if attempt.sourceOps == 0 && attempt.sourceOpsDone != nil {
		close(attempt.sourceOpsDone)
	}
}

func (attempt *liveAttempt) closeSourceOperations() {
	attempt.sourceOpsMu.Lock()
	attempt.sourceClosing = true
	if attempt.sourceCloseStarted != nil {
		close(attempt.sourceCloseStarted)
		attempt.sourceCloseStarted = nil
	}
	done := attempt.sourceOpsDone
	zero := attempt.sourceOps == 0
	attempt.sourceOpsMu.Unlock()
	if !zero {
		<-done
	}
}

func (daemon *Daemon) registerLiveAttempt(attempt *liveAttempt) error {
	if daemon == nil || attempt == nil || attempt.runID == (kernel.RunID{}) || attempt.sessionID == (kernel.TerminalSessionID{}) {
		return fmt.Errorf("%w: invalid live attempt", kernel.ErrInvalidValue)
	}
	daemon.attemptMu.Lock()
	defer daemon.attemptMu.Unlock()
	if daemon.closing {
		return ErrTerminalClosed
	}
	if daemon.attempts == nil {
		daemon.attempts = make(map[kernel.RunID]*liveAttempt)
	}
	if _, exists := daemon.attempts[attempt.runID]; exists {
		return kernel.ErrConflict
	}
	if len(daemon.attempts) >= maxLiveAttempts {
		return kernel.ErrBusy
	}
	daemon.attempts[attempt.runID] = attempt
	return nil
}

func (daemon *Daemon) unregisterLiveAttempt(runID kernel.RunID, attempt *liveAttempt) {
	if daemon == nil {
		return
	}
	daemon.attemptMu.Lock()
	if daemon.attempts[runID] == attempt {
		delete(daemon.attempts, runID)
	}
	daemon.attemptMu.Unlock()
}

// liveAttemptForDigest finds the owner for an exact attempt bearer without
// asking SQLite to authenticate it. The caller has already validated the
// digest shape; this lookup is only a best-effort wake route for a refused
// outcome, and the owner still rereads durable state before acting.
func (daemon *Daemon) liveAttemptForDigest(digest kernel.AttemptDigest) *liveAttempt {
	if daemon == nil {
		return nil
	}
	daemon.attemptMu.Lock()
	defer daemon.attemptMu.Unlock()
	for _, attempt := range daemon.attempts {
		if attempt != nil && attempt.attemptDigest == digest {
			return attempt
		}
	}
	return nil
}

// closeLiveAttempts is the daemon shutdown seam. It first closes admission to
// the in-memory owner registry, then synchronously asks each owner to converge
// and joins it. The Store remains the authority for recovery after an
// abnormal daemon death; this method only covers normal in-process shutdown.
func (daemon *Daemon) closeLiveAttempts() error {
	if daemon != nil && daemon.cleanupCancel != nil {
		daemon.cleanupCancel()
	}
	if daemon == nil {
		return nil
	}
	daemon.attemptMu.Lock()
	if daemon.closing {
		done := daemon.closeDone
		daemon.attemptMu.Unlock()
		if done != nil {
			<-done
		}
		return daemon.closeErr
	}
	daemon.closing = true
	daemon.closeDone = make(chan struct{})
	done := daemon.closeDone
	attempts := make([]*liveAttempt, 0, len(daemon.attempts))
	for _, attempt := range daemon.attempts {
		attempts = append(attempts, attempt)
	}
	supervisors := make([]*supervisorRegistration, 0, len(daemon.supervisors))
	for registration := range daemon.supervisors {
		supervisors = append(supervisors, registration)
	}
	daemon.attemptMu.Unlock()
	for _, registration := range supervisors {
		registration.cancel()
	}
	var result error
	// RunNext owns the outer child and resource cleanup even before it can
	// register a live attempt. Cancellation lets that same outer owner finish
	// its terminal acknowledgement and resource cleanup; do not preempt a
	// terminal-seen owner by closing its controller before that acknowledgement.
	for _, registration := range supervisors {
		result = errors.Join(result, registration.wait())
	}
	// A live attempt can exist without a supervisor only in a lower-level test
	// or a failed handoff. Close those leftovers as a fail-safe after all
	// registered outer owners have joined. Registered owners close their live
	// attempt as part of their own synchronous cleanup, so these calls are
	// idempotent joins rather than a second controller owner.
	for _, attempt := range attempts {
		result = errors.Join(result, attempt.close())
	}
	for _, attempt := range attempts {
		result = errors.Join(result, attempt.join())
	}
	daemon.attemptMu.Lock()
	daemon.closeErr = result
	close(done)
	daemon.attemptMu.Unlock()
	return result
}

func (attempt *liveAttempt) notify() {
	if attempt == nil {
		return
	}
	select {
	case attempt.wake <- struct{}{}:
	default:
	}
}

func (attempt *liveAttempt) notifyOutcomeRefusal(err error) {
	if attempt == nil || err == nil || attempt.daemon == nil {
		return
	}
	attempt.outcomeRefusal = err
	attempt.notify()
}

func (attempt *liveAttempt) pendingOutcomeSnapshot() (kernel.Proposal, bool) {
	if attempt == nil || attempt.daemon == nil {
		return kernel.Proposal{}, false
	}
	attempt.daemon.operationMu.Lock()
	defer attempt.daemon.operationMu.Unlock()
	if attempt.pendingOutcome == nil {
		return kernel.Proposal{}, false
	}
	return *attempt.pendingOutcome, true
}

func (attempt *liveAttempt) submit(ctx context.Context, command liveAttemptCommand) error {
	if attempt == nil || ctx == nil {
		return ErrTerminalClosed
	}
	if command.result == nil {
		command.result = make(chan error, 1)
	}
	select {
	case <-attempt.done:
		return ErrTerminalClosed
	case <-ctx.Done():
		return ctx.Err()
	case attempt.commands <- command:
	}
	// Once accepted, the command is owned by the attempt. Do not abandon it
	// merely because the caller's context expires after the send. The done case
	// prevents a queued command from pinning a caller if the owner exits before
	// it can consume the mailbox entry.
	select {
	case err := <-command.result:
		return err
	case <-attempt.done:
		select {
		case err := <-command.result:
			return err
		default:
			return ErrTerminalClosed
		}
	}
}

func (attempt *liveAttempt) releaseProvider(ctx context.Context) error {
	return attempt.submit(ctx, liveAttemptCommand{kind: liveCommandReleaseProvider})
}

// submitEffect is separate from submit so the established error-only
// lifecycle path remains unchanged. Cancellation can prevent mailbox
// acceptance; once accepted, the owner completes the exact command and the
// caller observes its result even if the caller context is subsequently
// cancelled.
func (attempt *liveAttempt) submitEffect(ctx context.Context, effect terminalEffect) terminalEffectResult {
	if attempt == nil || ctx == nil {
		return uncertainTerminalEffect(ErrTerminalClosed)
	}
	done := make(chan terminalEffectResult, 1)
	command := liveAttemptCommand{kind: liveCommandEffect, effect: &effect, effectDone: done}
	select {
	case <-attempt.done:
		return uncertainTerminalEffect(ErrTerminalClosed)
	case <-ctx.Done():
		return terminalEffectResult{err: ctx.Err()}
	case attempt.commands <- command:
	}
	select {
	case result := <-done:
		return result
	case <-attempt.done:
		select {
		case result := <-done:
			return result
		default:
			return uncertainTerminalEffect(ErrTerminalClosed)
		}
	}
}

func (attempt *liveAttempt) attach(ctx context.Context, sessionID kernel.TerminalSessionID, expectedRun, expectedSession kernel.Revision, sequence uint64) (*TerminalAttachment, error) {
	if attempt == nil || ctx == nil {
		return nil, ErrTerminalClosed
	}
	attachment := &TerminalAttachment{owner: attempt, queue: make(chan TerminalEvent, terminalSubscriberEventCap)}
	command := liveAttemptCommand{kind: liveCommandAttach, attachment: attachment, session: sessionID, expectedRun: expectedRun, expectedSession: expectedSession, sequence: sequence, result: make(chan error, 1)}
	if err := attempt.submit(ctx, command); err != nil {
		return nil, err
	}
	return attachment, nil
}

func (attempt *liveAttempt) detach(ctx context.Context, attachment *TerminalAttachment) error {
	return attempt.submit(ctx, liveAttemptCommand{kind: liveCommandDetach, attachment: attachment})
}

// finishExit broadcasts the store-committed wire exit to every observer and
// stops the owner loop. The exit value comes from the authenticated result
// after ConsumeAttemptResult, never from browser or runner prose.
func (attempt *liveAttempt) finishExit(ctx context.Context, exit TerminalEvent) error {
	return attempt.submit(ctx, liveAttemptCommand{kind: liveCommandFinishExit, exit: &exit})
}

func (attempt *liveAttempt) close() error {
	if attempt == nil {
		return nil
	}
	select {
	case <-attempt.done:
		return attempt.join()
	default:
	}
	err := attempt.submit(context.Background(), liveAttemptCommand{kind: liveCommandShutdown})
	return errors.Join(err, attempt.join())
}

func (attempt *liveAttempt) join() error {
	if attempt == nil {
		return nil
	}
	<-attempt.done
	return attempt.finalErr
}

func (attempt *liveAttempt) waitResult() liveAttemptResult {
	if attempt == nil {
		return liveAttemptResult{err: ErrTerminalClosed}
	}
	return <-attempt.result
}
