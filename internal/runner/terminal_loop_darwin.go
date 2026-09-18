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
// It never returns with an unjoined goroutine: the outer attempt runner owns
// the PTY, child group, two capability sockets and every terminal cursor.
func runReleasedProvider(child *OwnedChild, daemon, worker *os.File, reads *attemptReadSet, stagePTY *ptyStageSink, retained *terminalByteRing, startup []byte) (bool, error) {
	return runReleasedProviderWithHandover(child, daemon, worker, reads, stagePTY, retained, startup, nil)
}

// HandoverTransport belongs to the runner loop. The endpoint sends only
// already-authenticated, fenced duplex connections through Replacements.
// After the loop returns, Current receives the final result notice and is
// closed by the caller after finalization.
type HandoverTransport struct {
	Replacements <-chan *os.File
	Current      *os.File
	Stop         func()
}

// handoverDetachedGrace bounds how long the owner loop waits, unattended, for
// a replacement control connection after quiescing or losing its daemon.
// Past it, the runner gives up and converges the provider itself, the same
// outcome protocol-1's daemonLost reaches immediately on daemon EOF.
const handoverDetachedGrace = 10 * time.Minute

// testHandoverDetachedGrace overrides handoverDetachedGrace in package tests
// only; production leaves it zero and uses the real ceiling.
var testHandoverDetachedGrace time.Duration

func handoverGrace() time.Duration {
	if testHandoverDetachedGrace > 0 {
		return testHandoverDetachedGrace
	}
	return handoverDetachedGrace
}

// The endpoint admits only a fenced replacement and passes its still-open
// duplex connection here. A nil channel retains protocol-1 close-and-drain.
func runReleasedProviderWithHandover(child *OwnedChild, daemon, worker *os.File, reads *attemptReadSet, stagePTY *ptyStageSink, retained *terminalByteRing, startup []byte, handover *HandoverTransport) (bool, error) {
	if child == nil || daemon == nil || worker == nil || reads == nil || child.ptyMaster == nil || retained == nil {
		return false, ErrState
	}
	// The PTY was registered and drained from inner activation, so pre-provider
	// worker output is already retained in exact order. The stage sink and this
	// loop share that one ring by pointer: any copy here would silently drop
	// every byte the worker writes between adoption and provider exec.
	loop := terminalOwner{child: child, daemon: daemon, worker: worker, reads: reads, daemonOpen: true, workerOpen: true, ring: retained, handover: handover}
	if handover != nil {
		handover.Current = daemon
	}
	if err := loop.awaitProviderExec(stagePTY); err != nil {
		return loop.daemonOpen, err
	}
	if err := reads.removeWorker(); err != nil {
		return true, err
	}
	loop.workerOpen = false
	if !reads.ptyRegistered {
		if err := reads.registerPTY(); err != nil {
			return true, err
		}
	}
	loop.ptyOpen = true
	// The worker's CLOEXEC capability has closed, proving provider exec, and the
	// PTY is now registered. Claude receives its frozen prompt once. Shell reads
	// its program from fd 11, and Codex reads its task through the attempt API;
	// neither has startup PTY bytes.
	// The prompt is typed only once the provider has taken the terminal out
	// of canonical mode, or at a ceiling: until then the line discipline
	// echoes every byte back as output and keeps at most a kilobyte of a
	// line, so a long prompt typed into a provider still starting would be
	// cut short and its echo would flood the daemon. A prompt ending in CR is
	// then submitted by that CR as a keystroke of its own, once the provider
	// has drawn its prompt: an interactive provider reads one chunk that
	// carries text and a newline as a paste, and a paste does not submit. The
	// text goes now; the CR follows from serve.
	if len(startup) > 0 {
		defer clear(startup)
		alive, err := loop.awaitRawMode(stagePTY, startupRawCeiling)
		if err != nil {
			return loop.daemonOpen, err
		}
		// A provider that hung up its terminal during the gate is owed no
		// prompt; serve reports its exit as it would any other.
		if alive {
			text := startup
			if text[len(text)-1] == '\r' {
				text = text[:len(text)-1]
				now := time.Now()
				loop.enterAfter, loop.enterBy = now.Add(startupEnterFloor), now.Add(startupEnterCeiling)
			}
			if len(text) > 0 {
				n, err := loop.child.writePTYOwned(text, attemptControlTimeout)
				count, status := terminalPayloadResult(n, len(text), err)
				if status != TerminalResultOK {
					stopErr := loop.stop()
					return loop.daemonOpen, errors.Join(fmt.Errorf("runner: provider startup input %s after %d bytes: %w", status, count, err), stopErr)
				}
			}
		}
	}
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

	daemonDecoder *attemptFrameDecoder
	generation    uint64
	inputActive   bool
	nextInput     uint64
	ptyEOF        bool
	ptyDrained    bool
	stopRequested bool

	ring             *terminalByteRing
	credit           uint64
	sent             uint64
	observerAttached bool
	replay           []terminalReplay
	handover         *HandoverTransport
	detached         bool
	detachedAt       time.Time

	// enterAfter and enterBy bound the one pending CR owed to the provider;
	// lastOutput is when the provider last wrote, so the CR follows a quiet
	// prompt rather than a banner still being drawn.
	enterAfter, enterBy, lastOutput time.Time
	humanReplyCorrelation           uint64
	humanReplyCount                 uint32
}

// ponytail: the provider's output is opaque to the runner, so its readiness
// is calibrated, not observed. The gate waits for canonical input to clear
// and gives up at startupRawCeiling; the submitting CR waits for the output
// to go quiet for startupEnterQuiet after startupEnterFloor and goes at
// startupEnterCeiling regardless. Recognise the provider's own prompt if
// these ever prove wrong for a CLI.
const (
	startupRawCeiling         = 2 * time.Second
	startupEnterFloor         = time.Second
	startupEnterQuiet         = 500 * time.Millisecond
	startupEnterCeiling       = 5 * time.Second
	startupEnterTick          = 100 * time.Millisecond
	terminalPayloadWriteLimit = 250 * time.Millisecond
	// DeferredSubmitBudget is the extra daemon effect budget for a deferred
	// Codex submit: its paste, ceiling/tick, and standalone CR write.
	DeferredSubmitBudget = startupEnterCeiling + 2*terminalPayloadWriteLimit + startupEnterTick
)

// awaitRawMode waits, up to the ceiling, for the provider to clear canonical
// input on its terminal, draining what it prints meanwhile so a provider that
// greets with more than the terminal's output buffer is not stuck before it
// can. It answers false once the provider has hung up its terminal, so a
// provider that dies while starting is reported as an exit, not as a failed
// prompt. The master reflects the slave's line discipline on Darwin.
func (o *terminalOwner) awaitRawMode(stagePTY *ptyStageSink, ceiling time.Duration) (bool, error) {
	deadline := time.Now().Add(ceiling)
	master := int(o.child.ptyMaster.Fd())
	for {
		before := o.ring.Head()
		if err := stagePTY.drain(); err != nil {
			return false, err
		}
		if o.ring.Head() != before {
			o.lastOutput = time.Now()
		}
		fds := []unix.PollFd{{Fd: int32(master), Events: unix.POLLIN}}
		if _, err := unix.Poll(fds, 0); err == nil && fds[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return false, nil
		}
		termios, err := unix.IoctlGetTermios(master, unix.TIOCGETA)
		if err != nil || termios.Lflag&unix.ICANON == 0 || !time.Now().Before(deadline) {
			return true, nil
		}
		time.Sleep(startupEnterTick)
	}
}

// submitPending writes the owed CR once the provider's output has been quiet
// for a spell after the floor (a provider that never wrote is quiet), or at
// the ceiling regardless. A provider that has exited or closed its terminal
// is owed nothing.
func (o *terminalOwner) submitPending() error {
	if o.enterBy.IsZero() {
		return nil
	}
	if o.child.exitObserved {
		return o.rejectHumanReply()
	}
	now := time.Now()
	quiet := o.lastOutput.IsZero() || now.Sub(o.lastOutput) >= startupEnterQuiet
	if now.Before(o.enterAfter) || now.Before(o.enterBy) && !quiet {
		return nil
	}
	o.enterAfter, o.enterBy = time.Time{}, time.Time{}
	_, status := o.writeTerminalPayload([]byte{'\r'})
	if o.humanReplyCorrelation != 0 {
		correlation, count := o.humanReplyCorrelation, o.humanReplyCount
		o.humanReplyCorrelation, o.humanReplyCount = 0, 0
		return o.send(TerminalFrame{Kind: TerminalHumanReplyResult, Correlation: correlation, Count: count, Status: status})
	}
	switch status {
	case TerminalResultOK, TerminalResultRejected:
		return nil
	default:
		return fmt.Errorf("runner: provider startup submit %s", status)
	}
}

func (o *terminalOwner) rejectHumanReply() error {
	if o.humanReplyCorrelation == 0 {
		o.enterAfter, o.enterBy = time.Time{}, time.Time{}
		return nil
	}
	correlation, count := o.humanReplyCorrelation, o.humanReplyCount
	o.humanReplyCorrelation, o.humanReplyCount = 0, 0
	o.enterAfter, o.enterBy = time.Time{}, time.Time{}
	return o.send(TerminalFrame{Kind: TerminalHumanReplyResult, Correlation: correlation, Count: count, Status: TerminalResultRejected})
}

type terminalReplay struct {
	correlation uint64
	cursor      uint64
	head        uint64
}

func (o *terminalOwner) awaitProviderExec(stagePTY *ptyStageSink) error {
	for {
		frame, source, err := nextAttemptFrame(o.child, o.daemon, o.worker, o.daemonOpen, o.workerOpen, stagePTY, 0)
		switch source {
		case sourceChild:
			if err == nil {
				return ErrState
			}
			return err
		case sourceDaemon:
			if errors.Is(err, io.EOF) {
				return errors.New("runner: daemon closed before provider exec")
			}
			if err != nil {
				return err
			}
			if frame.Kind == "terminate" && validBareAttemptFrame(frame) {
				return errors.New("runner: daemon terminated before provider exec")
			}
			return ErrState
		case sourceWorker:
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			if validProviderErrorFrame(frame) {
				return fmt.Errorf("runner: provider exec: %s", frame.Payload)
			}
			if !validCurrentExecCheck(frame) {
				return ErrState
			}
			if err := o.writeDaemonFrame(frame); err != nil {
				return err
			}
			ack, ackSource, ackErr := nextAttemptFrame(o.child, o.daemon, o.worker, o.daemonOpen, o.workerOpen, stagePTY, 0)
			if ackErr != nil || ackSource != sourceDaemon || !validCurrentExecCheckAck(ack) {
				return protocolError("current exec check ack", ackSource, ackErr)
			}
			if err := o.writeWorkerFrame(ack); err != nil {
				return err
			}
		default:
			return protocolError("provider exec", source, err)
		}
	}
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
		case sourceTick:
			if stopped, err := o.handoverStep(); stopped || err != nil {
				return o.daemonOpen, err
			}
			if o.detached {
				continue
			}
			if err := o.submitPending(); err != nil {
				return o.daemonOpen, err
			}
		case sourceChild:
			if err := o.rejectHumanReply(); err != nil {
				return o.daemonOpen, err
			}
			if o.handover != nil && o.handover.Stop != nil {
				o.handover.Stop()
			}
			// A takeover may already be authenticated and queued while the
			// provider exits. Consume it before finalization; there may be no
			// later PTY read or idle tick on which to fence and attach it.
			if stopped, err := o.handoverStep(); stopped || err != nil {
				return o.daemonOpen, err
			}
			// First converge the exact process group and perform the sole Wait;
			// only then is PTY tail output drained. PTY EOF is emitted exclusively
			// from an actual EOF/EIO read, never from child exit.
			if _, err := o.child.FinishAfterExit(8 * time.Second); err != nil {
				return o.daemonOpen, err
			}
			if err := o.drainPTY(); err != nil {
				return o.daemonOpen, err
			}
			if stopped, err := o.handoverStep(); stopped || err != nil {
				return o.daemonOpen, err
			}
			return o.daemonOpen, nil
		case sourcePTY:
			o.lastOutput = time.Now()
			if err := o.consumePTY(ev.bytes, ev.err); err != nil {
				return o.daemonOpen, err
			}
			if stopped, err := o.handoverStep(); stopped || err != nil {
				return o.daemonOpen, err
			}
			if !o.detached {
				if err := o.submitPending(); err != nil {
					return o.daemonOpen, err
				}
			}
		case sourceDaemon:
			if errors.Is(ev.err, io.EOF) {
				if err := o.daemonLost(); err != nil {
					return false, err
				}
				if o.detached {
					continue
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
				if o.detached {
					break // no command on the old stream can follow quiescence
				}
				if o.stopRequested {
					return o.daemonOpen, nil
				}
			}
		}
	}
}

// handoverStep takes a buffered replacement and enforces the detached grace.
// A buffered replacement is taken while attached too: the endpoint has
// already consumed its grant, so the old owner is over either way and must
// be fenced now, not whenever it happens to quiesce or die. The step runs on
// the idle tick and after every PTY read: the tick exists only when nothing
// is readable, and a provider that never stops writing (a TUI redrawing)
// keeps the PTY readable for as long as it runs, so a step that lived on the
// tick alone was never reached under exactly the output a live worker makes.
func (o *terminalOwner) handoverStep() (stopped bool, err error) {
	if o.handover == nil {
		return false, nil
	}
	if err := o.adoptReplacement(); err != nil {
		return false, err
	}
	if o.detached && time.Since(o.detachedAt) >= handoverGrace() {
		return true, o.stop()
	}
	return false, nil
}

type terminalReady struct {
	source attemptSource
	bytes  []byte
	err    error
}

const (
	sourcePTY  attemptSource = sourceChild + 1
	sourceTick attemptSource = sourceChild + 2
)

func (o *terminalOwner) nextEvent() (terminalReady, error) {
	events := make([]unix.Kevent_t, 1)
	for {
		// While a startup CR is owed the wait is bounded, so the quiet prompt
		// is noticed without any event arriving.
		var timeout *unix.Timespec
		if !o.enterBy.IsZero() || o.handover != nil {
			tick := unix.NsecToTimespec(int64(startupEnterTick))
			timeout = &tick
		}
		n, err := unix.Kevent(o.child.kq, nil, events, timeout)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return terminalReady{}, err
		}
		if n == 0 && timeout != nil {
			return terminalReady{source: sourceTick}, nil
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
	if o.detached {
		return ErrState
	}
	if raw.Version == 1 && raw.Kind == "terminate" && validBareAttemptFrame(raw) {
		return o.stop()
	}
	if raw.Version == 2 && raw.Kind == "handover-quiesce" && noLegacyFields(raw) && noTerminalFields(raw) && len(raw.Payload) == 0 {
		return o.quiesce()
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

func validHandoverCursorFrame(frame attemptFrame) bool {
	return frame.Version == 2 && frame.Stage == "" && frame.Identity == (Identity{}) && len(frame.Payload) == 0 && frame.FileIdentity == nil && frame.Digest == "" && frame.Floor <= frame.Head && frame.Correlation == 0 && frame.Generation == 0 && frame.Sequence == 0 && frame.Start == 0 && frame.End == 0 && frame.Count == 0 && frame.Rows == 0 && frame.Cols == 0 && frame.Credit == 0 && !frame.Submit && frame.Status == ""
}

// quiesce runs on the sole terminal owner loop, after every earlier command
// in the old stream. It leaves the child and PTY untouched. The old daemon is
// fenced at the runner before a replacement connection can be adopted.
func (o *terminalOwner) quiesce() error {
	if o == nil || o.detached || o.daemon == nil || o.daemonDecoder == nil {
		return ErrState
	}
	if o.handover == nil || o.humanReplyCorrelation != 0 || !o.enterBy.IsZero() || o.daemonDecoder.headerRead != 0 || len(o.daemonDecoder.body) != 0 {
		return o.writeDaemonFrame(attemptFrame{Version: 2, Kind: string(AttemptHandoverRejected), Floor: o.ring.Floor(), Head: o.ring.Head()})
	}
	if err := retireReadableFilter(o.reads.removeDaemon); err != nil {
		return err
	}
	_ = o.writeDaemonFrame(attemptFrame{Version: 2, Kind: string(AttemptHandoverQuiesced), Floor: o.ring.Floor(), Head: o.ring.Head()})
	o.fenceDaemon()
	return nil
}

// fenceDaemon ends the current owner capability on this loop. Its stream is
// closed and every piece of per-owner state is dropped, so no command it sent
// can still run, no pending CR is owed to its successor, and no correlation,
// credit, observer or replay is inherited. The caller retires the read filter
// first, and nothing but this loop ever calls it.
func (o *terminalOwner) fenceDaemon() {
	if o.daemon != nil {
		_ = o.daemon.Close()
		o.daemon = nil
	}
	o.daemonOpen, o.detached, o.detachedAt = false, true, time.Now()
	o.enterAfter, o.enterBy = time.Time{}, time.Time{}
	o.humanReplyCorrelation, o.humanReplyCount = 0, 0
	o.inputActive, o.credit, o.observerAttached = false, 0, false
	o.replay = nil
	if o.handover != nil {
		o.handover.Current = nil
	}
}

// adoptReplacement takes one already-authenticated replacement from the
// takeover endpoint. Fencing the old owner and answering the new one are one
// step on this loop, in that order, so a replacement is never told it owns the
// run while the stream it replaces can still issue a command: the endpoint
// consumed the grant but never answered it. A replacement that cannot be
// committed or registered is closed unanswered, which its client reads as a
// refusal and falls back to leaving the run to its live holder.
func (o *terminalOwner) adoptReplacement() error {
	if o == nil || o.handover == nil {
		return ErrState
	}
	var file *os.File
	select {
	case candidate, ok := <-o.handover.Replacements:
		if !ok || candidate == nil {
			return nil
		}
		file = candidate
	default:
		return nil
	}
	if !o.detached {
		if err := retireReadableFilter(o.reads.removeDaemon); err != nil {
			_ = file.Close()
			return err
		}
		_ = o.writeDaemonFrame(attemptFrame{Version: 2, Kind: string(AttemptHandoverQuiesced), Floor: o.ring.Floor(), Head: o.ring.Head()})
		o.fenceDaemon()
	}
	if _, err := commitControl(file); err != nil {
		_ = file.Close()
		return nil
	}
	o.reads.daemonFD = int(file.Fd())
	if err := o.reads.registerDaemon(); err != nil {
		_ = file.Close()
		return nil
	}
	o.daemon = file
	o.daemonOpen = true
	o.handover.Current = file
	o.daemonDecoder, _ = newAttemptFrameDecoder(maxFrameBytes)
	if err := writeTakeoverResponse(file, true, ""); err != nil {
		return o.poisonDaemon(nil) // a fresh grant can retry
	}
	if err := o.writeDaemonFrame(attemptFrame{Version: 2, Kind: "handover-attached", Floor: o.ring.Floor(), Head: o.ring.Head()}); err != nil {
		return nil // poisonDaemon closed this candidate; a fresh grant can retry
	}
	o.detached = false
	o.detachedAt = time.Time{}
	return nil
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
	return errors.Join(cleanupErr, o.terminateDrainingPTY())
}

// terminateDrainingPTY preserves the PTY tail while Darwin tears down the
// provider's controlling terminal. Termination owns kqueue process events;
// this owner concurrently drains only the PTY master, then joins termination
// before either ownership domain can escape this call.
func (o *terminalOwner) terminateDrainingPTY() error {
	if o == nil || o.child == nil || o.child.state != stateActivated {
		return ErrState
	}
	terminated := make(chan error, 1)
	go func() {
		_, err := o.child.Terminate(defaultStopTimeout)
		terminated <- err
	}()
	drainErr := o.drainPTY()
	return errors.Join(<-terminated, drainErr)
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
	if o.humanReplyCorrelation != 0 || !o.inputActive || c.Generation != o.generation || c.Sequence != o.nextInput {
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
// result mapping with terminal input. A requested submit is delayed like the
// startup submit so the provider receives the answer as a paste and CR as its
// own keystroke.
func (o *terminalOwner) humanReply(c TerminalCommand) error {
	if !o.enterBy.IsZero() || o.humanReplyCorrelation != 0 {
		return o.send(TerminalFrame{Kind: TerminalHumanReplyResult, Correlation: c.Correlation, Status: TerminalResultRejected})
	}
	count, status := o.writeTerminalPayload(c.Payload)
	if status == TerminalResultOK && c.Submit {
		now := time.Now()
		o.humanReplyCorrelation, o.humanReplyCount = c.Correlation, count
		o.enterAfter, o.enterBy = now.Add(startupEnterFloor), now.Add(startupEnterCeiling)
		return nil
	}
	return o.send(TerminalFrame{Kind: TerminalHumanReplyResult, Correlation: c.Correlation, Count: count, Status: status})
}

func (o *terminalOwner) writeTerminalPayload(payload []byte) (uint32, TerminalResultStatus) {
	if o == nil || o.stopRequested || o.ptyEOF || !o.ptyOpen || o.child == nil {
		return 0, TerminalResultRejected
	}
	n, err := o.child.writePTYOwned(payload, terminalPayloadWriteLimit)
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
	contextStart := o.ring.Floor()
	var contextBytes []byte
	if c.Sequence > o.ring.Floor() {
		if c.Sequence-o.ring.Floor() > maxTerminalLookbehind {
			contextStart = c.Sequence - maxTerminalLookbehind
		}
		var readErr error
		contextBytes, _, readErr = o.ring.Read(contextStart)
		if readErr != nil {
			return readErr
		}
		contextBytes = contextBytes[:int(c.Sequence-contextStart)]
	}
	if err := o.send(TerminalFrame{Kind: TerminalAttached, Correlation: c.Correlation, Sequence: c.Sequence, Floor: o.ring.Floor(), Head: head, Status: TerminalResultOK, ContextStart: contextStart, Context: contextBytes}); err != nil {
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
	var streamErr error
	for o.ptyOpen {
		buf := make([]byte, terminalReplayChunk)
		n, err := unix.Read(int(o.child.ptyMaster.Fd()), buf)
		if n > 0 {
			if err := o.ring.Append(buf[:n]); err != nil {
				return err
			}
			if streamErr == nil {
				streamErr = o.flush()
			}
			continue
		}
		if n == 0 && err == nil || errors.Is(err, unix.EIO) || errors.Is(err, io.EOF) {
			var retireErr error
			if o.reads != nil && o.ptyOpen {
				retireErr = retireReadableFilter(o.reads.removePTY)
				if retireErr == nil {
					o.ptyOpen = false
				}
			}
			if retireErr != nil {
				return errors.Join(streamErr, retireErr)
			}
			o.ptyDrained = true
			return errors.Join(streamErr, o.emitPTYEOF())
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			if time.Now().After(deadline) {
				return errors.Join(streamErr, ErrUnresolved)
			}
			time.Sleep(time.Millisecond)
			continue
		}
		return errors.Join(streamErr, err)
	}
	return streamErr
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
	if o.handover != nil {
		o.handover.Current = nil
	}
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
	if o.child != nil {
		o.child.ptyDrained = true
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
		if o.handover == nil {
			cleanupErr = errors.Join(cleanupErr, retireReadableFilter(o.reads.removeWorker))
			cleanupErr = errors.Join(cleanupErr, retireReadableFilter(o.reads.removePTY))
		}
	}
	if o.daemon != nil {
		cleanupErr = errors.Join(cleanupErr, o.daemon.Close())
		o.daemon = nil
	}
	o.inputActive = false
	if o.handover != nil && cleanupErr == nil {
		// A deferred submit may already have written text. No new owner may
		// blindly send its CR or reuse its correlation after an EOF.
		o.fenceDaemon()
		return nil
	}
	if o.child != nil && o.child.state == stateActivated {
		return errors.Join(cleanupErr, o.terminateDrainingPTY())
	}
	return cleanupErr
}

func validBareAttemptFrame(frame attemptFrame) bool {
	return frame.Version == 1 && frame.Stage == "" && frame.Identity == (Identity{}) && len(frame.Payload) == 0 && frame.FileIdentity == nil && frame.Digest == "" && noTerminalFields(frame)
}

func validCurrentExecCheck(frame attemptFrame) bool {
	return frame.Version == 1 && frame.Kind == "current-exec-check" && noLegacyFields(frame) && noTerminalFields(frame) && len(frame.Payload) == 0
}
