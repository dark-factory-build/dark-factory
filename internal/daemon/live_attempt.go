package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

const (
	maxLiveAttempts       = 1024
	liveAttemptMailboxCap = 64
	terminalSubscriberCap = 64
	terminalPendingCap    = 64
	terminalPayloadCap    = 64 << 10
	liveAttemptCredit     = 1 << 20
)

var (
	ErrTerminalNotReady = errors.New("daemon: terminal is not ready")
	ErrTerminalClosed   = errors.New("daemon: terminal attachment is closed")
	ErrTerminalSlow     = errors.New("daemon: terminal attachment was too slow")
	ErrTerminalReset    = errors.New("daemon: terminal attachment requires reset")
)

// TerminalEvent is the daemon-owned, browser-facing terminal projection. It
// intentionally contains no runner frames, process identities or descriptors.
type TerminalEvent struct {
	Kind       TerminalEventKind
	Accepted   bool
	Sequence   uint64
	Start      uint64
	End        uint64
	Floor      uint64
	Head       uint64
	ExitCode   int
	ExitSignal int
	Aborted    bool
	Payload    []byte
}

type TerminalEventKind uint8

const (
	TerminalEventAttached TerminalEventKind = iota + 1
	TerminalEventOutput
	TerminalEventReset
	TerminalEventPTYEOF
	TerminalEventExit
)

// TerminalAttachment is one read-only observer of one exact live terminal.
// The owner loop is the only writer or closer of queue. Close synchronously
// submits a detach command and waits until the owner has removed the observer.
type TerminalAttachment struct {
	owner   *liveAttempt
	runID   kernel.RunID
	session kernel.TerminalSessionID
	queue   chan TerminalEvent

	mu          sync.Mutex
	closed      bool
	finished    bool
	closeErr    error
	closeDone   chan struct{}
	closeResult error

	// The fields below are owned by the attempt loop. They are not read by
	// callers; the mutex above protects only Close/Next lifecycle state.
	correlation uint64
	expected    uint64
	replayHead  uint64
	replaying   bool
	pending     []TerminalEvent
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
func (daemon *Daemon) AttachTerminal(ctx context.Context, runID kernel.RunID, sessionID kernel.TerminalSessionID, sequence uint64) (*TerminalAttachment, error) {
	if daemon == nil || daemon.store == nil || ctx == nil || runID == (kernel.RunID{}) || sessionID == (kernel.TerminalSessionID{}) {
		return nil, fmt.Errorf("%w: invalid terminal attachment", kernel.ErrInvalidValue)
	}
	session, found, err := daemon.store.TerminalSessionForRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if !found || session.ID != sessionID || session.State != kernel.TerminalSessionActive {
		return nil, kernel.ErrConflict
	}
	run, found, err := daemon.store.Run(ctx, runID)
	if err != nil {
		return nil, err
	}
	if !found || run.Phase != kernel.RunRunning {
		return nil, kernel.ErrConflict
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
	return attempt.attach(ctx, sequence)
}

type liveAttemptCommandKind uint8

const (
	liveCommandReleaseProvider liveAttemptCommandKind = iota + 1
	liveCommandAttach
	liveCommandDetach
	liveCommandAcknowledge
	liveCommandShutdown
)

type liveAttemptCommand struct {
	kind       liveAttemptCommandKind
	attachment *TerminalAttachment
	sequence   uint64
	terminal   *runner.TerminalRecord
	result     chan error
}

type liveAttemptResult struct {
	event runner.AttemptEvent
	err   error
}

// liveAttempt is intentionally concrete. All mutable fields below belong to
// its one owner goroutine after construction; the registry mutex protects
// only the daemon's map membership.
type liveAttempt struct {
	daemon     *Daemon
	runID      kernel.RunID
	sessionID  kernel.TerminalSessionID
	controller *runner.AttemptController

	commands chan liveAttemptCommand
	wake     chan struct{}
	done     chan struct{}
	terminal chan liveAttemptResult

	subs            map[*TerminalAttachment]struct{}
	correlations    map[uint64]*TerminalAttachment
	retired         map[uint64]struct{}
	retiredOrder    []uint64
	lastCorrelation uint64

	readySeen         bool
	releaseSent       bool
	terminationSent   bool
	terminalSeen      bool
	acknowledged      bool
	terminalEvent     *runner.AttemptEvent
	creditOutstanding uint64
}

func newLiveAttempt(daemon *Daemon, runID kernel.RunID, sessionID kernel.TerminalSessionID, controller *runner.AttemptController) *liveAttempt {
	return &liveAttempt{
		daemon: daemon, runID: runID, sessionID: sessionID, controller: controller,
		commands: make(chan liveAttemptCommand, liveAttemptMailboxCap),
		wake:     make(chan struct{}, 1), done: make(chan struct{}), terminal: make(chan liveAttemptResult, 1),
		subs: make(map[*TerminalAttachment]struct{}), correlations: make(map[uint64]*TerminalAttachment), retired: make(map[uint64]struct{}),
	}
}

func (attempt *liveAttempt) retireCorrelation(correlation uint64) {
	if attempt == nil || correlation == 0 {
		return
	}
	if _, exists := attempt.retired[correlation]; exists {
		return
	}
	if len(attempt.retiredOrder) >= terminalSubscriberCap {
		oldest := attempt.retiredOrder[0]
		attempt.retiredOrder = attempt.retiredOrder[1:]
		delete(attempt.retired, oldest)
	}
	attempt.retired[correlation] = struct{}{}
	attempt.retiredOrder = append(attempt.retiredOrder, correlation)
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

// closeLiveAttempts is the daemon shutdown seam. It first closes admission to
// the in-memory owner registry, then synchronously asks each owner to converge
// and joins it. The Store remains the authority for recovery after an
// abnormal daemon death; this method only covers normal in-process shutdown.
func (daemon *Daemon) closeLiveAttempts() error {
	if daemon == nil {
		return nil
	}
	daemon.attemptMu.Lock()
	if daemon.closing {
		daemon.attemptMu.Unlock()
		return nil
	}
	daemon.closing = true
	attempts := make([]*liveAttempt, 0, len(daemon.attempts))
	for _, attempt := range daemon.attempts {
		attempts = append(attempts, attempt)
	}
	daemon.attemptMu.Unlock()
	var result error
	for _, attempt := range attempts {
		result = errors.Join(result, attempt.close())
	}
	for _, attempt := range attempts {
		result = errors.Join(result, attempt.join())
	}
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

func (attempt *liveAttempt) attach(ctx context.Context, sequence uint64) (*TerminalAttachment, error) {
	if attempt == nil || ctx == nil {
		return nil, ErrTerminalClosed
	}
	attachment := &TerminalAttachment{owner: attempt, runID: attempt.runID, session: attempt.sessionID, queue: make(chan TerminalEvent, terminalSubscriberCap)}
	command := liveAttemptCommand{kind: liveCommandAttach, attachment: attachment, sequence: sequence, result: make(chan error, 1)}
	if err := attempt.submit(ctx, command); err != nil {
		return nil, err
	}
	return attachment, nil
}

func (attempt *liveAttempt) detach(ctx context.Context, attachment *TerminalAttachment) error {
	return attempt.submit(ctx, liveAttemptCommand{kind: liveCommandDetach, attachment: attachment})
}

func (attempt *liveAttempt) acknowledge(ctx context.Context, terminal *runner.TerminalRecord) error {
	return attempt.submit(ctx, liveAttemptCommand{kind: liveCommandAcknowledge, terminal: terminal})
}

func (attempt *liveAttempt) close() error {
	if attempt == nil {
		return nil
	}
	select {
	case <-attempt.done:
		return nil
	default:
	}
	err := attempt.submit(context.Background(), liveAttemptCommand{kind: liveCommandShutdown})
	<-attempt.done
	return err
}

func (attempt *liveAttempt) join() error {
	if attempt == nil {
		return nil
	}
	<-attempt.done
	return nil
}

func (attempt *liveAttempt) waitTerminal() liveAttemptResult {
	if attempt == nil {
		return liveAttemptResult{err: ErrTerminalClosed}
	}
	return <-attempt.terminal
}
