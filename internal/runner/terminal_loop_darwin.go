//go:build darwin

package runner

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// runReleasedProvider is the single owner loop for a released PTY provider.
// It deliberately has no goroutines: the outer attempt runner owns the PTY,
// child group, two capability sockets and every terminal cursor.
func runReleasedProvider(child *OwnedChild, daemon, worker *os.File, reads *attemptReadSet) (bool, error) {
	if child == nil || daemon == nil || worker == nil || reads == nil || child.ptyMaster == nil {
		return false, ErrState
	}
	loop := terminalOwner{child: child, daemon: daemon, worker: worker, reads: reads, daemonOpen: true, workerOpen: true}
	if err := loop.awaitInitialInput(); err != nil {
		return loop.daemonOpen, err
	}
	if err := reads.removeWorker(); err != nil {
		return true, err
	}
	loop.workerOpen = false
	if err := reads.registerPTY(); err != nil {
		return true, err
	}
	loop.ptyOpen = true
	if err := loop.writeInitialInput(); err != nil {
		return true, err
	}
	// This is the bounded handoff barrier. The daemon may not send terminal
	// commands until the owner has registered the PTY and performed the one
	// initial write exactly once.
	if err := loop.send(TerminalFrame{Kind: TerminalReady}); err != nil {
		return false, err
	}
	return loop.serve()
}

type terminalOwner struct {
	child      *OwnedChild
	daemon     *os.File
	worker     *os.File
	reads      *attemptReadSet
	daemonOpen bool
	workerOpen bool
	ptyOpen    bool

	daemonDecoder  *attemptFrameDecoder
	generation     uint64
	inputActive    bool
	nextInput      uint64
	initialInput   []byte
	initialWritten bool
	initialMode    bool
	initialBefore  unix.Termios
	initialRaw     unix.Termios
	ptyEOF         bool
	ptyDrained     bool
	stopRequested  bool

	ring             terminalByteRing
	credit           uint64
	sent             uint64
	observerAttached bool
	replay           []terminalReplay
}

type terminalReplay struct {
	correlation uint64
	cursor      uint64
	head        uint64
}

func (o *terminalOwner) awaitInitialInput() error {
	for {
		frame, source, err := nextAttemptFrame(o.child, o.daemon, o.worker, o.daemonOpen, o.workerOpen, 0)
		if source == sourceChild && err == nil {
			return ErrState
		}
		if source == sourceDaemon {
			if errors.Is(err, io.EOF) {
				return errors.New("runner: daemon closed before provider input registration")
			}
			if err != nil {
				return err
			}
			// No browser terminal operation is accepted until provider input is
			// registered and the worker has handed off its control fd.
			if frame.Kind == "terminate" && validBareAttemptFrame(frame) {
				return errors.New("runner: daemon terminated before provider exec")
			}
			return ErrState
		}
		if source != sourceWorker {
			return protocolError("provider input registration", source, err)
		}
		if err == nil && frame.Version == 1 && frame.Kind == "provider-exec-error" && noLegacyFields(frame) && noTerminalFields(frame) && len(frame.Payload) > 0 && len(frame.Payload) <= maxAttemptReportBytes {
			return fmt.Errorf("runner: provider exec: %s", frame.Payload)
		}
		if errors.Is(err, io.EOF) {
			return errors.New("runner: worker closed before provider input registration")
		}
		if err != nil {
			return err
		}
		if frame.Version != 1 || frame.Kind != "provider-input" || !noLegacyFields(frame) || !noTerminalFields(frame) || ValidateProviderInput(frame.Payload) != nil {
			return ErrState
		}
		o.initialInput = append([]byte(nil), frame.Payload...)
		if err := o.prepareInitialInputMode(); err != nil {
			return err
		}
		if err := o.writeWorkerFrame(attemptFrame{Version: 1, Kind: "provider-input-registered"}); err != nil {
			return err
		}
		for {
			frame, source, err := nextAttemptFrame(o.child, o.daemon, o.worker, o.daemonOpen, o.workerOpen, 0)
			if source == sourceWorker && errors.Is(err, io.EOF) {
				return nil
			}
			if source == sourceWorker && err == nil && frame.Version == 1 && frame.Kind == "provider-exec-error" && noLegacyFields(frame) && noTerminalFields(frame) && len(frame.Payload) > 0 && len(frame.Payload) <= maxAttemptReportBytes {
				return fmt.Errorf("runner: provider exec: %s", frame.Payload)
			}
			if source == sourceWorker && err == nil && validCurrentExecCheck(frame) {
				if err := o.writeDaemonFrame(frame); err != nil {
					return err
				}
				ack, ackSource, ackErr := nextAttemptFrame(o.child, o.daemon, o.worker, o.daemonOpen, o.workerOpen, 0)
				if ackErr != nil || ackSource != sourceDaemon || !validCurrentExecCheckAck(ack) {
					return protocolError("current exec check ack", ackSource, ackErr)
				}
				if err := o.writeWorkerFrame(ack); err != nil {
					return err
				}
				continue
			}
			if source == sourceDaemon {
				if errors.Is(err, io.EOF) {
					return errors.New("runner: daemon closed before provider exec")
				}
				if err != nil {
					return err
				}
				if frame.Kind == "terminate" && validBareAttemptFrame(frame) {
					return errors.New("runner: daemon terminated before provider exec")
				}
			}
			if source == sourceChild && err == nil {
				return ErrState
			}
			if err != nil {
				return err
			}
			return ErrState
		}
	}
}

func (o *terminalOwner) writeInitialInput() error {
	if o == nil || o.initialWritten || o.child == nil || o.child.ptyMaster == nil || !o.initialMode {
		return ErrState
	}
	o.initialWritten = true // reservation happens before the effect; never retry.
	if len(o.initialInput) == 0 {
		return o.restoreInitialInputMode()
	}
	if o.child.state != stateActivated || o.child.exitObserved {
		return ErrState
	}
	deadline := time.Now().Add(attemptControlTimeout)
	written := 0
	for written < len(o.initialInput) {
		if err := o.drainInitialOutput(); err != nil {
			return fmt.Errorf("runner: initial terminal output: %w", errors.Join(err, ErrUnresolved))
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("runner: initial terminal input: %w", errors.Join(os.ErrDeadlineExceeded, ErrUnresolved))
		}
		end := min(written+terminalReplayChunk, len(o.initialInput))
		n, err := unix.Write(int(o.child.ptyMaster.Fd()), o.initialInput[written:end])
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				continue
			}
			milliseconds := int(min(remaining, 10*time.Millisecond) / time.Millisecond)
			if milliseconds < 1 {
				milliseconds = 1
			}
			fds := []unix.PollFd{{Fd: int32(o.child.ptyMaster.Fd()), Events: unix.POLLIN | unix.POLLOUT}}
			if _, pollErr := unix.Poll(fds, milliseconds); pollErr != nil && !errors.Is(pollErr, unix.EINTR) {
				return fmt.Errorf("runner: initial terminal input: %w", errors.Join(pollErr, ErrUnresolved))
			}
			if fds[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
				return fmt.Errorf("runner: initial terminal input: %w", ErrUnresolved)
			}
			continue
		}
		if err != nil || n <= 0 {
			if err == nil {
				err = io.ErrNoProgress
			}
			return fmt.Errorf("runner: initial terminal input: %w", errors.Join(err, ErrUnresolved))
		}
		written += n
	}
	if err := o.restoreInitialInputMode(); err != nil {
		return fmt.Errorf("runner: initial terminal mode: %w", errors.Join(err, ErrUnresolved))
	}
	return nil
}

// prepareInitialInputMode runs before the wrapper receives its input
// registration acknowledgement and therefore before provider exec. The raw
// handoff is required for exact bounded input: a canonical PTY can truncate a
// line before the one provider terminator, and echo can fill the output queue
// while the same synchronous owner is writing. Shell is the only enabled V1
// provider; a future native adapter must separately prove any terminal-mode
// changes it performs during startup.
func (o *terminalOwner) prepareInitialInputMode() error {
	if o == nil || o.initialMode || o.child == nil || o.child.ptyMaster == nil {
		return ErrState
	}
	before, err := unix.IoctlGetTermios(int(o.child.ptyMaster.Fd()), unix.TIOCGETA)
	if err != nil {
		return err
	}
	raw := *before
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXOFF | unix.IXANY
	raw.Oflag &^= unix.OPOST
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(int(o.child.ptyMaster.Fd()), unix.TIOCSETA, &raw); err != nil {
		return err
	}
	got, err := unix.IoctlGetTermios(int(o.child.ptyMaster.Fd()), unix.TIOCGETA)
	if err != nil || *got != raw {
		return errors.Join(err, ErrIdentity)
	}
	o.initialBefore, o.initialRaw, o.initialMode = *before, raw, true
	return nil
}

func (o *terminalOwner) restoreInitialInputMode() error {
	if o == nil || !o.initialMode || o.child == nil || o.child.ptyMaster == nil {
		return ErrState
	}
	current, err := unix.IoctlGetTermios(int(o.child.ptyMaster.Fd()), unix.TIOCGETA)
	if err != nil {
		return err
	}
	// A provider that deliberately changed the terminal after exec owns its
	// new mode. Do not overwrite that change with the pre-exec snapshot.
	if *current != o.initialRaw {
		o.initialMode = false
		return nil
	}
	if err := unix.IoctlSetTermios(int(o.child.ptyMaster.Fd()), unix.TIOCSETA, &o.initialBefore); err != nil {
		return err
	}
	o.initialMode = false
	return nil
}

func (o *terminalOwner) drainInitialOutput() error {
	buf := make([]byte, terminalReplayChunk)
	n, err := unix.Read(int(o.child.ptyMaster.Fd()), buf)
	if n > 0 {
		if appendErr := o.ring.Append(buf[:n]); appendErr != nil {
			return appendErr
		}
		return nil
	}
	if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
		return nil
	}
	if errors.Is(err, unix.EIO) || errors.Is(err, io.EOF) {
		return io.EOF
	}
	if err == nil {
		return io.ErrNoProgress
	}
	return err
}

func (o *terminalOwner) serve() (bool, error) {
	decoder, err := newAttemptFrameDecoder(maxFrameBytes)
	if err != nil {
		return o.daemonOpen, err
	}
	o.daemonDecoder = decoder
	for {
		ev, err := o.nextEvent()
		if err != nil {
			return o.daemonOpen, err
		}
		switch ev.source {
		case sourceChild:
			// First converge the exact process group and perform the sole Wait;
			// only then is PTY tail output drained. PTY EOF is emitted exclusively
			// from an actual EOF/EIO read, never from child exit.
			if _, err := o.child.FinishAfterExit(8 * time.Second); err != nil {
				return o.daemonOpen, err
			}
			if err := o.drainPTY(); err != nil {
				return o.daemonOpen, err
			}
			return o.daemonOpen, nil
		case sourcePTY:
			if err := o.consumePTY(ev.bytes, ev.err); err != nil {
				return o.daemonOpen, err
			}
		case sourceDaemon:
			if errors.Is(ev.err, io.EOF) {
				if err := o.daemonLost(); err != nil {
					return false, err
				}
				return false, errors.New("runner: daemon control closed")
			}
			if ev.err != nil {
				return o.daemonOpen, ev.err
			}
			frames, err := o.daemonDecoder.Feed(ev.bytes)
			if err != nil {
				return o.daemonOpen, err
			}
			for _, frame := range frames {
				if err := o.command(frame); err != nil {
					return o.daemonOpen, err
				}
				if o.stopRequested {
					return o.daemonOpen, nil
				}
			}
		}
	}
}

type terminalReady struct {
	source attemptSource
	bytes  []byte
	err    error
}

const sourcePTY attemptSource = sourceChild + 1

func (o *terminalOwner) nextEvent() (terminalReady, error) {
	events := make([]unix.Kevent_t, 1)
	for {
		n, err := unix.Kevent(o.child.kq, nil, events, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return terminalReady{}, err
		}
		if n != 1 {
			return terminalReady{}, ErrIdentity
		}
		ev := events[0]
		if ev.Filter == unix.EVFILT_PROC && ev.Ident == uint64(o.child.identity.PID) && ev.Fflags&unix.NOTE_EXIT != 0 {
			o.child.exitObserved = true
			o.child.state = stateExited
			return terminalReady{source: sourceChild}, nil
		}
		if ev.Filter != unix.EVFILT_READ || ev.Flags&unix.EV_ERROR != 0 {
			return terminalReady{}, ErrIdentity
		}
		var file *os.File
		var source attemptSource
		switch {
		case o.daemonOpen && ev.Ident == uint64(o.daemon.Fd()):
			file, source = o.daemon, sourceDaemon
		case o.ptyOpen && ev.Ident == uint64(o.child.ptyMaster.Fd()):
			file, source = o.child.ptyMaster, sourcePTY
		default:
			return terminalReady{}, ErrIdentity
		}
		buf := make([]byte, 16<<10)
		n, readErr := unix.Read(int(file.Fd()), buf)
		if n > 0 {
			return terminalReady{source: source, bytes: buf[:n], err: nil}, nil
		}
		if source == sourceDaemon && errors.Is(readErr, unix.EAGAIN) {
			continue
		}
		if source == sourcePTY && errors.Is(readErr, unix.EAGAIN) {
			continue
		}
		if source == sourcePTY && errors.Is(readErr, unix.EIO) {
			return terminalReady{source: sourcePTY, err: io.EOF}, nil
		}
		if readErr == nil {
			return terminalReady{source: source, err: io.EOF}, nil
		}
		return terminalReady{source: source, err: readErr}, nil
	}
}

func (o *terminalOwner) command(raw attemptFrame) error {
	if raw.Version == 1 && raw.Kind == "terminate" && validBareAttemptFrame(raw) {
		return o.stop()
	}
	command, err := terminalCommandFromFrame(raw)
	if err != nil {
		return err
	}
	switch command.Kind {
	case TerminalGenerationInstall:
		return o.install(command)
	case TerminalGenerationRevoke:
		return o.revoke(command)
	case TerminalAttach:
		return o.attach(command)
	case TerminalCredit:
		if o.credit+uint64(command.Credit) > maxTerminalCredit {
			return ErrState
		}
		o.credit += uint64(command.Credit)
		return o.flush()
	case TerminalInput:
		return o.input(command)
	case TerminalResize:
		return o.resize(command)
	case TerminalHumanReply:
		return o.humanReply(command)
	default:
		return ErrState
	}
}

// stop is the typed owner transition used by daemon cancellation/finalizing.
// Competing kqueue filters are retired before group convergence so no stale
// PTY or process event can race the exact kill/wait owner.
func (o *terminalOwner) stop() error {
	o.stopRequested = true
	o.inputActive = false
	var cleanupErr error
	if o.reads != nil {
		cleanupErr = errors.Join(cleanupErr, retireReadableFilter(o.reads.removeWorker))
		cleanupErr = errors.Join(cleanupErr, retireReadableFilter(o.reads.removePTY))
	}
	if o.child == nil || o.child.state != stateActivated {
		return errors.Join(cleanupErr, ErrState)
	}
	if _, err := o.child.Terminate(defaultStopTimeout); err != nil {
		return errors.Join(cleanupErr, err)
	}
	return errors.Join(cleanupErr, o.drainPTY())
}

func (o *terminalOwner) install(c TerminalCommand) error {
	status := TerminalResultOK
	if c.Generation <= o.generation || o.inputActive {
		if c.Generation == o.generation && o.inputActive {
			return o.send(TerminalFrame{Kind: TerminalGenerationResult, Correlation: c.Correlation, Generation: o.generation, Status: status})
		}
		status = TerminalResultRejected
		return o.send(TerminalFrame{Kind: TerminalGenerationResult, Correlation: c.Correlation, Generation: c.Generation, Status: status})
	}
	o.generation, o.inputActive, o.nextInput = c.Generation, true, 1
	return o.send(TerminalFrame{Kind: TerminalGenerationResult, Correlation: c.Correlation, Generation: c.Generation, Status: status})
}

func (o *terminalOwner) revoke(c TerminalCommand) error {
	status := TerminalResultOK
	if c.Generation < o.generation {
		status = TerminalResultRejected
	} else if c.Generation == o.generation && !o.inputActive {
		status = TerminalResultOK
	} else if c.Generation == o.generation && o.inputActive {
		o.inputActive = false
	} else {
		o.generation, o.inputActive = c.Generation, false
	}
	return o.send(TerminalFrame{Kind: TerminalGenerationResult, Correlation: c.Correlation, Generation: c.Generation, Status: status})
}

func (o *terminalOwner) input(c TerminalCommand) error {
	status := TerminalResultOK
	count := uint32(0)
	if !o.inputActive || c.Generation != o.generation || c.Sequence != o.nextInput {
		status = TerminalResultRejected
	} else {
		count, status = o.writeTerminalPayload(c.Payload)
		if status != TerminalResultOK {
			o.inputActive = false
		} else {
			o.nextInput++
		}
	}
	return o.send(TerminalFrame{Kind: TerminalInputResult, Correlation: c.Correlation, Generation: c.Generation, Sequence: c.Sequence, Count: count, Status: status})
}

// humanReply is a daemon-authorized one-shot write for an exact durable
// HumanRequest. It intentionally bypasses browser generation/sequence checks,
// but shares the sole owner-only PTY write primitive and its fail-closed
// result mapping with terminal input. The payload is written byte-for-byte;
// this path never appends a newline or retries a partial write.
func (o *terminalOwner) humanReply(c TerminalCommand) error {
	count, status := o.writeTerminalPayload(c.Payload)
	return o.send(TerminalFrame{Kind: TerminalHumanReplyResult, Correlation: c.Correlation, Count: count, Status: status})
}

func (o *terminalOwner) writeTerminalPayload(payload []byte) (uint32, TerminalResultStatus) {
	if o == nil || o.stopRequested || o.ptyEOF || !o.ptyOpen || o.child == nil {
		return 0, TerminalResultRejected
	}
	n, err := o.child.writePTYOwned(payload)
	count, status := terminalPayloadResult(n, len(payload), err)
	if status == TerminalResultOK {
		return count, status
	}
	// A partial or uncertain write is an irreversible operation boundary. Do
	// not retry or write a suffix; the caller receives the exact count and must
	// decide how to recover. Browser input separately loses its generation
	// authority in input, while daemon-authorized HumanRequest delivery remains
	// a distinct deliberate operation.
	return count, status
}

func terminalPayloadResult(written, total int, err error) (uint32, TerminalResultStatus) {
	if written == total && err == nil {
		return uint32(written), TerminalResultOK
	}
	if written > 0 {
		return uint32(written), TerminalResultPartial
	}
	return 0, TerminalResultUncertain
}

func (o *terminalOwner) resize(c TerminalCommand) error {
	status := TerminalResultOK
	if !o.inputActive || c.Generation != o.generation {
		status = TerminalResultRejected
	} else if err := o.child.resizePTYOwned(int(c.Cols), int(c.Rows)); err != nil {
		status = TerminalResultUncertain
	}
	return o.send(TerminalFrame{Kind: TerminalResizeResult, Correlation: c.Correlation, Generation: c.Generation, Rows: c.Rows, Cols: c.Cols, Status: status})
}

func (o *terminalOwner) attach(c TerminalCommand) error {
	if c.Sequence < o.ring.Floor() {
		return o.send(TerminalFrame{Kind: TerminalReset, Correlation: c.Correlation, Floor: o.ring.Floor(), Head: o.ring.Head()})
	}
	if c.Sequence > o.ring.Head() {
		return o.send(TerminalFrame{Kind: TerminalAttached, Correlation: c.Correlation, Sequence: c.Sequence, Floor: o.ring.Floor(), Head: o.ring.Head(), Status: TerminalResultRejected})
	}
	head := o.ring.Head()
	if len(o.replay) >= terminalReplayRequestCapacity {
		return errors.Join(ErrUnresolved, errors.New("runner: terminal replay request queue is full"))
	}
	if err := o.send(TerminalFrame{Kind: TerminalAttached, Correlation: c.Correlation, Sequence: c.Sequence, Floor: o.ring.Floor(), Head: head, Status: TerminalResultOK}); err != nil {
		return err
	}
	// The first observer owns the live cursor. Its historical bytes are routed
	// by correlation, so advancing the live cursor here prevents replay from
	// being emitted a second time as uncorrelated live output. Later observers
	// never mutate that cursor.
	if !o.observerAttached {
		o.observerAttached = true
		o.sent = head
	}
	o.replay = append(o.replay, terminalReplay{correlation: c.Correlation, cursor: c.Sequence, head: head})
	return o.flush()
}

func (o *terminalOwner) consumePTY(data []byte, readErr error) error {
	if len(data) > 0 {
		if o.ptyEOF {
			return ErrState
		}
		if appendErr := o.ring.Append(data); appendErr != nil {
			return appendErr
		}
		if flushErr := o.flush(); flushErr != nil {
			return flushErr
		}
	}
	if errors.Is(readErr, unix.EIO) || errors.Is(readErr, io.EOF) {
		var retireErr error
		if o.reads != nil && o.ptyOpen {
			retireErr = retireReadableFilter(o.reads.removePTY)
			if retireErr == nil {
				o.ptyOpen = false
			}
		}
		if retireErr != nil {
			return retireErr
		}
		o.ptyDrained = true
		return o.emitPTYEOF()
	}
	if errors.Is(readErr, unix.EAGAIN) || errors.Is(readErr, unix.EWOULDBLOCK) || readErr == nil {
		return nil
	}
	return readErr
}

func (o *terminalOwner) drainPTY() error {
	deadline := time.Now().Add(2 * time.Second)
	for o.ptyOpen {
		buf := make([]byte, terminalReplayChunk)
		n, err := unix.Read(int(o.child.ptyMaster.Fd()), buf)
		if n > 0 {
			if err := o.ring.Append(buf[:n]); err != nil {
				return err
			}
			if err := o.flush(); err != nil {
				return err
			}
			continue
		}
		if errors.Is(err, unix.EIO) || errors.Is(err, io.EOF) {
			var retireErr error
			if o.reads != nil && o.ptyOpen {
				retireErr = retireReadableFilter(o.reads.removePTY)
				if retireErr == nil {
					o.ptyOpen = false
				}
			}
			if retireErr != nil {
				return retireErr
			}
			o.ptyDrained = true
			return o.emitPTYEOF()
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			if time.Now().After(deadline) {
				return ErrUnresolved
			}
			time.Sleep(time.Millisecond)
			continue
		}
		return err
	}
	return nil
}

func (o *terminalOwner) flush() error {
	if o.credit == 0 || !o.daemonOpen {
		return nil
	}
	for len(o.replay) > 0 && o.credit != 0 {
		replay := &o.replay[0]
		if replay.cursor < o.ring.Floor() {
			if err := o.send(TerminalFrame{Kind: TerminalReset, Correlation: replay.correlation, Floor: o.ring.Floor(), Head: o.ring.Head()}); err != nil {
				return err
			}
			o.replay = o.replay[1:]
			continue
		}
		if replay.cursor > replay.head {
			return ErrIdentity
		}
		if replay.cursor == replay.head {
			o.replay = o.replay[1:]
			continue
		}
		chunk, next, err := o.ring.Read(replay.cursor)
		if err != nil {
			return err
		}
		if next > replay.head {
			chunk = chunk[:replay.head-replay.cursor]
			next = replay.head
		}
		if uint64(len(chunk)) > o.credit {
			chunk = chunk[:o.credit]
			next = replay.cursor + uint64(len(chunk))
		}
		if len(chunk) == 0 {
			return ErrState
		}
		if err := o.send(TerminalFrame{Kind: TerminalOutput, Correlation: replay.correlation, Start: replay.cursor, End: next, Payload: chunk}); err != nil {
			return err
		}
		replay.cursor = next
		o.credit -= uint64(len(chunk))
		if replay.cursor == replay.head {
			o.replay = o.replay[1:]
		}
	}
	// PTY EOF stops only future uncorrelated live output. Correlated replay
	// above remains valid for reconnecting observers after the PTY closes.
	if o.ptyEOF {
		return nil
	}
	if o.credit == 0 {
		return nil
	}
	if o.sent < o.ring.Floor() {
		if err := o.send(TerminalFrame{Kind: TerminalReset, Floor: o.ring.Floor(), Head: o.ring.Head()}); err != nil {
			return err
		}
		o.sent = o.ring.Floor()
	}
	for o.sent < o.ring.Head() && o.credit != 0 {
		chunk, next, err := o.ring.Read(o.sent)
		if err != nil {
			return err
		}
		if uint64(len(chunk)) > o.credit {
			chunk = chunk[:o.credit]
			next = o.sent + uint64(len(chunk))
		}
		if err := o.send(TerminalFrame{Kind: TerminalOutput, Start: o.sent, End: next, Payload: chunk}); err != nil {
			return err
		}
		o.sent = next
		o.credit -= uint64(len(chunk))
	}
	return nil
}

func (o *terminalOwner) send(frame TerminalFrame) error {
	if o == nil || !o.daemonOpen || o.daemon == nil {
		return io.EOF
	}
	if err := writeControlFrame(o.daemon, terminalEventFrame(frame), maxFrameBytes); err != nil {
		return o.poisonDaemon(err)
	}
	return nil
}

func (o *terminalOwner) writeDaemonFrame(frame attemptFrame) error {
	if o == nil || !o.daemonOpen || o.daemon == nil {
		return io.EOF
	}
	if err := writeControlFrame(o.daemon, frame, maxFrameBytes); err != nil {
		return o.poisonDaemon(err)
	}
	return nil
}

func (o *terminalOwner) writeWorkerFrame(frame attemptFrame) error {
	if o == nil || !o.workerOpen || o.worker == nil {
		return io.EOF
	}
	if err := writeControlFrame(o.worker, frame, maxFrameBytes); err != nil {
		o.workerOpen = false
		var closeErr error
		if o.reads != nil {
			closeErr = retireReadableFilter(o.reads.removeWorker)
		}
		closeErr = errors.Join(closeErr, o.worker.Close())
		o.worker = nil
		return errors.Join(err, closeErr)
	}
	return nil
}

func (o *terminalOwner) poisonDaemon(cause error) error {
	if o == nil {
		return cause
	}
	o.daemonOpen = false
	var cleanupErr error
	if o.reads != nil {
		cleanupErr = errors.Join(cleanupErr, retireReadableFilter(o.reads.removeDaemon))
	}
	if o.daemon != nil {
		cleanupErr = errors.Join(cleanupErr, o.daemon.Close())
		o.daemon = nil
	}
	return errors.Join(cause, cleanupErr)
}

func (o *terminalOwner) emitPTYEOF() error {
	if o == nil || !o.ptyDrained {
		return ErrState
	}
	if o.ptyEOF {
		return nil
	}
	o.ptyEOF = true
	o.inputActive = false
	if !o.daemonOpen {
		return nil
	}
	return o.send(TerminalFrame{Kind: TerminalPTYEOF})
}

func (o *terminalOwner) daemonLost() error {
	o.daemonOpen = false
	var cleanupErr error
	if o.reads != nil {
		cleanupErr = errors.Join(cleanupErr, retireReadableFilter(o.reads.removeDaemon))
		cleanupErr = errors.Join(cleanupErr, retireReadableFilter(o.reads.removeWorker))
		cleanupErr = errors.Join(cleanupErr, retireReadableFilter(o.reads.removePTY))
	}
	if o.daemon != nil {
		cleanupErr = errors.Join(cleanupErr, o.daemon.Close())
		o.daemon = nil
	}
	o.inputActive = false
	if o.child != nil && o.child.state == stateActivated {
		_, err := o.child.Terminate(defaultStopTimeout)
		if err != nil {
			return errors.Join(cleanupErr, err)
		}
		return errors.Join(cleanupErr, o.drainPTY())
	}
	return cleanupErr
}

func validBareAttemptFrame(frame attemptFrame) bool {
	return frame.Version == 1 && frame.Stage == "" && frame.Identity == (Identity{}) && len(frame.Payload) == 0 && frame.Terminal == nil && frame.FileIdentity == nil && frame.Digest == "" && !frame.StoreCommitted && noTerminalFields(frame)
}

func validCurrentExecCheck(frame attemptFrame) bool {
	return frame.Version == 1 && frame.Kind == "current-exec-check" && noLegacyFields(frame) && noTerminalFields(frame) && len(frame.Payload) == 0
}
