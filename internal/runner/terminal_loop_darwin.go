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
		return true, err
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
	ptyEOF         bool

	ring   terminalByteRing
	credit uint64
	sent   uint64
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
		if errors.Is(err, io.EOF) {
			return errors.New("runner: worker closed before provider input registration")
		}
		if err != nil {
			return err
		}
		if frame.Version != 1 || frame.Kind != "provider-input" || !noLegacyFields(frame) || !noTerminalFields(frame) || len(frame.Payload) > maxInputBytes {
			return ErrState
		}
		o.initialInput = append([]byte(nil), frame.Payload...)
		if err := writeControlFrame(o.worker, attemptFrame{Version: 1, Kind: "provider-input-registered"}, maxFrameBytes); err != nil {
			return err
		}
		for {
			frame, source, err := nextAttemptFrame(o.child, o.daemon, o.worker, o.daemonOpen, o.workerOpen, 0)
			if source == sourceWorker && errors.Is(err, io.EOF) {
				return nil
			}
			if source == sourceWorker && err == nil && validCurrentExecCheck(frame) {
				if err := writeControlFrame(o.daemon, frame, maxFrameBytes); err != nil {
					return err
				}
				ack, ackSource, ackErr := nextAttemptFrame(o.child, o.daemon, o.worker, o.daemonOpen, o.workerOpen, 0)
				if ackErr != nil || ackSource != sourceDaemon || !validCurrentExecCheckAck(ack) {
					return protocolError("current exec check ack", ackSource, ackErr)
				}
				if err := writeControlFrame(o.worker, ack, maxFrameBytes); err != nil {
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
	if o == nil || o.initialWritten || o.child == nil {
		return ErrState
	}
	o.initialWritten = true // reservation happens before the effect; never retry.
	if len(o.initialInput) == 0 {
		return nil
	}
	n, err := o.child.writePTYOwned(o.initialInput)
	if err != nil || n != len(o.initialInput) {
		return fmt.Errorf("runner: initial terminal input: %w", errors.Join(err, ErrUnresolved))
	}
	return nil
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
			if err := o.emitPTYEOF(); err != nil {
				return o.daemonOpen, err
			}
			// First converge the exact process group and perform the sole Wait;
			// only then is PTY tail output drained and spooled.
			if _, err := o.child.FinishAfterExit(8 * time.Second); err != nil {
				return o.daemonOpen, err
			}
			if err := o.drainPTY(); err != nil {
				return o.daemonOpen, err
			}
			return o.daemonOpen, nil
		case sourcePTY:
			if err := o.readPTY(); err != nil {
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
	default:
		return ErrState
	}
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
		n, err := o.child.writePTYOwned(c.Payload)
		count = uint32(n)
		if err != nil || n != len(c.Payload) {
			o.inputActive = false
			if n > 0 {
				status = TerminalResultPartial
			} else {
				status = TerminalResultUncertain
			}
		} else {
			o.nextInput++
		}
	}
	return o.send(TerminalFrame{Kind: TerminalInputResult, Correlation: c.Correlation, Generation: c.Generation, Sequence: c.Sequence, Count: count, Status: status})
}

func (o *terminalOwner) resize(c TerminalCommand) error {
	status := TerminalResultOK
	if !o.inputActive || c.Generation != o.generation {
		status = TerminalResultRejected
	} else if err := o.child.ResizePTY(int(c.Cols), int(c.Rows)); err != nil {
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
	if err := o.send(TerminalFrame{Kind: TerminalAttached, Correlation: c.Correlation, Sequence: c.Sequence, Floor: o.ring.Floor(), Head: head, Status: TerminalResultOK}); err != nil {
		return err
	}
	o.sent = c.Sequence
	return o.flush()
}

func (o *terminalOwner) readPTY() error {
	buf := make([]byte, terminalReplayChunk)
	n, err := unix.Read(int(o.child.ptyMaster.Fd()), buf)
	if n > 0 {
		if appendErr := o.ring.Append(buf[:n]); appendErr != nil {
			return appendErr
		}
		if flushErr := o.flush(); flushErr != nil {
			return flushErr
		}
	}
	if errors.Is(err, unix.EIO) || errors.Is(err, io.EOF) {
		if o.reads != nil && o.ptyOpen {
			retireReadableFilter(o.reads.removePTY)
			o.ptyOpen = false
		}
		return o.emitPTYEOF()
	}
	if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) || err == nil {
		return nil
	}
	return err
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
			if o.reads != nil && o.ptyOpen {
				retireReadableFilter(o.reads.removePTY)
				o.ptyOpen = false
			}
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
	if !o.daemonOpen {
		return io.EOF
	}
	return writeControlFrame(o.daemon, terminalEventFrame(frame), maxFrameBytes)
}

func (o *terminalOwner) emitPTYEOF() error {
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
	if o.reads != nil {
		retireReadableFilter(o.reads.removeDaemon)
		retireReadableFilter(o.reads.removeWorker)
		retireReadableFilter(o.reads.removePTY)
	}
	o.inputActive = false
	if o.child != nil && o.child.state == stateActivated {
		_, err := o.child.Terminate(defaultStopTimeout)
		if err != nil {
			return err
		}
		return o.drainPTY()
	}
	return nil
}

func validBareAttemptFrame(frame attemptFrame) bool {
	return frame.Version == 1 && frame.Stage == "" && frame.Identity == (Identity{}) && len(frame.Payload) == 0 && frame.Terminal == nil && frame.FileIdentity == nil && frame.Digest == "" && !frame.StoreCommitted && noTerminalFields(frame)
}

func validCurrentExecCheck(frame attemptFrame) bool {
	return frame.Version == 1 && frame.Kind == "current-exec-check" && noLegacyFields(frame) && noTerminalFields(frame) && len(frame.Payload) == 0
}
