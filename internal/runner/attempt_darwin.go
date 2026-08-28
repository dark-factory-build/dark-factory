//go:build darwin

package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type attemptControllerState uint8

const (
	controllerNew attemptControllerState = iota + 1
	controllerConfigured
	controllerInnerReady
	controllerSelectionReleased
	controllerSelectionReported
	controllerPreparationReleased
	controllerPreparationReported
	controllerPopulationReleased
	controllerPopulationReported
	controllerProviderReleased
	controllerTerminal
	controllerAcknowledged
	controllerPoisoned
)

// AttemptController is the daemon side of one fixed attempt-runner protocol.
// Its peer is the sole control capability accepted by --attempt-runner.
type AttemptController struct {
	file          *os.File
	state         attemptControllerState
	attemptID     string
	inner         Identity
	terminalReady bool
	last          *TerminalRecord
}

// writeFrame is the controller's only authoritative write path. A failed
// framed write may have left a peer with an indistinguishable prefix, so the
// capability is poisoned immediately and can never append a retry to it.
func (c *AttemptController) writeFrame(value any, limit int) error {
	if c == nil || c.file == nil {
		return ErrState
	}
	err := writeControlFrame(c.file, value, limit)
	if err == nil {
		return nil
	}
	closeErr := c.file.Close()
	c.file = nil
	c.state = controllerPoisoned
	return errors.Join(err, closeErr)
}

func NewAttemptController() (*AttemptController, *os.File, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, nil, err
	}
	if err := unix.SetNonblock(fds[0], true); err != nil {
		unix.Close(fds[0])
		unix.Close(fds[1])
		return nil, nil, err
	}
	if err := unix.SetNonblock(fds[1], true); err != nil {
		unix.Close(fds[0])
		unix.Close(fds[1])
		return nil, nil, err
	}
	unix.CloseOnExec(fds[0])
	unix.CloseOnExec(fds[1])
	parent := os.NewFile(uintptr(fds[0]), "attempt-daemon-control")
	child := os.NewFile(uintptr(fds[1]), "attempt-runner-control")
	if _, err := commitControl(parent); err != nil {
		parent.Close()
		child.Close()
		return nil, nil, err
	}
	if _, err := commitControl(child); err != nil {
		parent.Close()
		child.Close()
		return nil, nil, err
	}
	return &AttemptController{file: parent, state: controllerNew}, child, nil
}

func (c *AttemptController) Configure(spec AttemptSpec) error {
	if c == nil || c.file == nil || c.state != controllerNew || spec.Wrapper == nil {
		return ErrState
	}
	if err := validateAttemptName(spec.AttemptID, 256); err != nil {
		return err
	}
	if spec.MarkerName != InnerActivationMarkerName || spec.TerminalName != TerminalSpoolName {
		return ErrIdentity
	}
	if spec.Wrapper.control != nil || spec.Wrapper.controlID != nil || len(spec.Wrapper.stdin) != 0 || spec.Wrapper.stdout != nil || spec.Wrapper.stderr != nil || spec.Wrapper.testFinal != nil || spec.Wrapper.testCurrentFinal {
		return fmt.Errorf("runner: wrapper launch contains unsupported capabilities")
	}
	cfg := attemptConfig{Version: 1, AttemptID: spec.AttemptID, Wrapper: spec.Wrapper.commit, MarkerName: spec.MarkerName, TerminalName: spec.TerminalName}
	if err := c.writeFrame(cfg, maxConfigBytes); err != nil {
		return err
	}
	c.state = controllerConfigured
	c.attemptID = spec.AttemptID
	return nil
}

func (c *AttemptController) Next(timeout time.Duration) (AttemptEvent, error) {
	if c == nil || c.file == nil {
		return AttemptEvent{}, ErrState
	}
	if timeout <= 0 {
		timeout = attemptControlTimeout
	}
	if err := c.file.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return AttemptEvent{}, err
	}
	defer c.file.SetReadDeadline(time.Time{})
	var frame attemptFrame
	if err := readFrame(c.file, &frame, maxConfigBytes); err != nil {
		return AttemptEvent{}, err
	}
	if frame.Version != 1 {
		return AttemptEvent{}, ErrIdentity
	}
	switch c.state {
	case controllerConfigured:
		if frame.Kind != "inner-ready" || !frame.Identity.Valid() || frame.Identity.PID != frame.Identity.PGID || frame.Stage != "" || len(frame.Payload) != 0 || frame.Terminal != nil || frame.FileIdentity != nil || frame.Digest != "" || frame.StoreCommitted || !noTerminalFields(frame) {
			return AttemptEvent{}, ErrState
		}
		c.state = controllerInnerReady
		c.inner = frame.Identity
		return AttemptEvent{Kind: AttemptInnerReady, Identity: frame.Identity}, nil
	case controllerSelectionReleased:
		return c.acceptCheckpoint(frame, StageSelection, controllerSelectionReported)
	case controllerPreparationReleased:
		return c.acceptCheckpoint(frame, StagePreparation, controllerPreparationReported)
	case controllerPopulationReleased:
		return c.acceptCheckpoint(frame, StagePopulation, controllerPopulationReported)
	case controllerProviderReleased:
		if isTerminalEventKind(frame.Kind) {
			if frame.Version != commandVersion || !validTerminalEnvelope(frame, false) {
				return AttemptEvent{}, ErrState
			}
			event, err := terminalEventFromFrame(frame)
			if err != nil {
				return AttemptEvent{}, err
			}
			if event.Kind == TerminalReady {
				if c.terminalReady {
					return AttemptEvent{}, ErrState
				}
				c.terminalReady = true
			}
			return AttemptEvent{Kind: AttemptTerminalFrame, Frame: &event}, nil
		}
		if frame.Kind == "current-exec-check" && noLegacyFields(frame) && noTerminalFields(frame) && len(frame.Payload) == 0 {
			return AttemptEvent{Kind: AttemptCheckpoint, Stage: StageProvider}, nil
		}
		if frame.Kind != "terminal" || frame.Stage != "" || frame.Identity != (Identity{}) || len(frame.Payload) != 0 || frame.Terminal == nil || frame.FileIdentity == nil || len(frame.Digest) != 64 || frame.StoreCommitted || !noTerminalFields(frame) {
			return AttemptEvent{}, ErrState
		}
		record := &TerminalRecord{Terminal: *frame.Terminal, Identity: *frame.FileIdentity, Digest: frame.Digest}
		if err := validateTerminal(record.Terminal); err != nil || record.Terminal.AttemptID != c.attemptID || record.Terminal.Process != c.inner || record.Identity.Device == 0 || record.Identity.Inode == 0 {
			return AttemptEvent{}, ErrIdentity
		}
		c.last = record
		c.state = controllerTerminal
		return AttemptEvent{Kind: AttemptTerminal, Terminal: record, Identity: record.Terminal.Process}, nil
	default:
		return AttemptEvent{}, ErrState
	}
}

func isTerminalEventKind(kind string) bool {
	switch TerminalEventKind(kind) {
	case TerminalGenerationResult, TerminalInputResult, TerminalResizeResult, TerminalHumanReplyResult, TerminalAttached, TerminalOutput, TerminalReset, TerminalReady, TerminalPTYEOF:
		return true
	default:
		return false
	}
}

// NextReady waits for the exact control socket to become readable or close
// without consuming any frame bytes. A timeout is an ordinary false result;
// callers invoke Next once with its full frame timeout only after readiness.
func (c *AttemptController) NextReady(timeout time.Duration) (bool, error) {
	if c == nil || c.file == nil || timeout < 0 {
		return false, ErrState
	}
	milliseconds := int(timeout / time.Millisecond)
	if timeout > 0 && milliseconds == 0 {
		milliseconds = 1
	}
	fds := []unix.PollFd{{Fd: int32(c.file.Fd()), Events: unix.POLLIN}}
	for {
		count, err := unix.Poll(fds, milliseconds)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return false, err
		}
		if count == 0 {
			return false, nil
		}
		revents := fds[0].Revents
		if revents&unix.POLLNVAL != 0 || revents&^(unix.POLLIN|unix.POLLHUP|unix.POLLERR) != 0 {
			return false, ErrIdentity
		}
		if revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR) != 0 {
			return true, nil
		}
		return false, ErrIdentity
	}
}

func (c *AttemptController) acceptCheckpoint(frame attemptFrame, stage AttemptStage, next attemptControllerState) (AttemptEvent, error) {
	if frame.Kind != "checkpoint" || frame.Stage != stage || frame.Identity != (Identity{}) || len(frame.Payload) > maxAttemptReportBytes || frame.Terminal != nil || frame.FileIdentity != nil || frame.Digest != "" || frame.StoreCommitted || !noTerminalFields(frame) {
		return AttemptEvent{}, ErrState
	}
	c.state = next
	return AttemptEvent{Kind: AttemptCheckpoint, Stage: stage, Payload: append([]byte{}, frame.Payload...)}, nil
}

func (c *AttemptController) Release(stage AttemptStage) error {
	if c == nil || c.file == nil {
		return ErrState
	}
	var want attemptControllerState
	var next attemptControllerState
	switch stage {
	case StageSelection:
		want, next = controllerInnerReady, controllerSelectionReleased
	case StagePreparation:
		want, next = controllerSelectionReported, controllerPreparationReleased
	case StagePopulation:
		want, next = controllerPreparationReported, controllerPopulationReleased
	case StageProvider:
		want, next = controllerPopulationReported, controllerProviderReleased
	default:
		return ErrState
	}
	if c.state != want {
		return ErrState
	}
	if err := c.writeFrame(attemptFrame{Version: 1, Kind: "release", Stage: stage}, maxFrameBytes); err != nil {
		return err
	}
	c.state = next
	return nil
}

// SendTerminalCommand validates and writes one complete terminal command to
// the already-running outer runner. The daemon supervisor owns the controller
// call and serializes it with its other lifecycle operations; this method does
// not add a concurrent RPC abstraction or a background writer.
func (c *AttemptController) SendTerminalCommand(command TerminalCommand) error {
	if c == nil || c.file == nil || c.state != controllerProviderReleased || !c.terminalReady {
		return ErrState
	}
	if err := command.validate(); err != nil {
		return err
	}
	return c.writeFrame(terminalCommandFrame(command), maxFrameBytes)
}

func (c *AttemptController) Terminate() error {
	if c == nil || c.file == nil || c.state < controllerInnerReady || c.state >= controllerTerminal {
		return ErrState
	}
	return c.writeFrame(attemptFrame{Version: 1, Kind: "terminate"}, maxFrameBytes)
}

func (c *AttemptController) AcknowledgeTerminal(want *TerminalRecord, storeCommitted bool) error {
	if c == nil || c.file == nil || c.state != controllerTerminal || !storeCommitted || want == nil || c.last == nil {
		return ErrState
	}
	if want.Digest != c.last.Digest || want.Identity != c.last.Identity || want.Terminal != c.last.Terminal {
		return ErrIdentity
	}
	if err := c.writeFrame(attemptFrame{Version: 1, Kind: "terminal-ack", Terminal: &want.Terminal, FileIdentity: &want.Identity, Digest: want.Digest, StoreCommitted: true}, maxFrameBytes); err != nil {
		return err
	}
	c.state = controllerAcknowledged
	return nil
}

// acknowledgeCurrentExecCheck exists only for the deterministic package test
// of the documented same-UID pathname race. Production controllers never see
// this frame because production LaunchSpecs cannot enable the seam.
func (c *AttemptController) acknowledgeCurrentExecCheck() error {
	if c == nil || c.file == nil || c.state != controllerProviderReleased {
		return ErrState
	}
	return c.writeFrame(attemptFrame{Version: 1, Kind: "current-exec-check-ack"}, maxFrameBytes)
}

func (c *AttemptController) Close() error {
	if c == nil || c.file == nil {
		return nil
	}
	err := c.file.Close()
	c.file = nil
	return err
}

type workerState uint8

const (
	workerSelection workerState = iota + 1
	workerSelectionReported
	workerPreparation
	workerPreparationReported
	workerPopulation
	workerPopulationReported
	workerProvider
	workerExec
)

// WorkerControl is the source wrapper's one inherited private capability. It
// exposes the fixed checkpoint sequence and the sole same-process provider
// exec; it does not expose arbitrary frames or descriptors.
type WorkerControl struct {
	file                    *os.File
	dir                     *os.File
	dirID                   fileCommitment
	dirIssued               bool
	lifetime                *os.File
	lifetimeID              descriptorCommitment
	identity                Identity
	state                   workerState
	providerInputRegistered bool
	providerErrorReported   bool
}

func OpenWorkerControl() (*WorkerControl, error) {
	control := os.NewFile(3, "attempt-worker-control")
	dir := os.NewFile(9, "attempt-private-dir")
	lifetime := os.NewFile(10, "runtime-lifetime")
	if control == nil || dir == nil || lifetime == nil {
		return nil, ErrIdentity
	}
	keep := false
	defer func() {
		if !keep {
			control.Close()
			dir.Close()
			lifetime.Close()
		}
	}()
	if _, err := commitControl(control); err != nil {
		return nil, fmt.Errorf("runner: worker control: %w", err)
	}
	dirID, err := validatePrivateDirectory(dir)
	if err != nil {
		return nil, fmt.Errorf("runner: worker private directory: %w", err)
	}
	lifetimeID, err := commitRuntimeLifetime(dir, lifetime)
	if err != nil {
		return nil, fmt.Errorf("runner: worker runtime lifetime: %w", err)
	}
	// These capabilities belong only to the wrapper. Its bounded Git children
	// and eventual provider must never gain accidental directory authority.
	// Only the empty regular lifetime file is deliberately retained for exec.
	unix.CloseOnExec(3)
	unix.CloseOnExec(9)
	if _, err := unix.FcntlInt(lifetime.Fd(), unix.F_SETFD, 0); err != nil {
		return nil, err
	}
	id, err := readIdentity(os.Getpid())
	if err != nil || id.PID != id.PGID {
		return nil, ErrIdentity
	}
	keep = true
	return &WorkerControl{file: control, dir: dir, dirID: dirID, lifetime: lifetime, lifetimeID: lifetimeID, identity: id, state: workerSelection}, nil
}

func (w *WorkerControl) Identity() Identity {
	if w == nil {
		return Identity{}
	}
	return w.identity
}

// DuplicateRuntimeDirectory returns the worker's one caller-owned CLOEXEC
// duplicate of its already-validated fd9 runtime directory. A successful or
// effectful attempt is one-shot; the caller owns and must close the result.
func (w *WorkerControl) DuplicateRuntimeDirectory(ctx context.Context) (_ *os.File, resultErr error) {
	if ctx == nil || w == nil || w.file == nil || w.dir == nil || w.lifetime == nil || w.state != workerSelection || w.dirIssued {
		return nil, ErrState
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w.dirIssued = true
	got, err := validatePrivateDirectory(w.dir)
	if err != nil || got != w.dirID {
		return nil, fmt.Errorf("runner: worker runtime directory: %w", errors.Join(ErrIdentity, err))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd, err := unix.FcntlInt(w.dir.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	duplicate := os.NewFile(uintptr(fd), "worker-runtime-directory")
	keep := false
	defer func() {
		if !keep {
			resultErr = errors.Join(resultErr, duplicate.Close())
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	duplicateID, err := validatePrivateDirectory(duplicate)
	if err != nil || duplicateID != w.dirID {
		return nil, fmt.Errorf("runner: duplicate runtime directory: %w", errors.Join(ErrIdentity, err))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	originalID, err := validatePrivateDirectory(w.dir)
	if err != nil || originalID != w.dirID {
		return nil, fmt.Errorf("runner: recheck runtime directory: %w", errors.Join(ErrIdentity, err))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	keep = true
	return duplicate, nil
}

func (w *WorkerControl) ReportSelection(payload []byte) error {
	return w.report(StageSelection, workerSelection, workerSelectionReported, payload)
}

func (w *WorkerControl) AwaitPreparation() error {
	return w.await(StagePreparation, workerSelectionReported, workerPreparation)
}

func (w *WorkerControl) ReportPreparation(payload []byte) error {
	return w.report(StagePreparation, workerPreparation, workerPreparationReported, payload)
}

func (w *WorkerControl) AwaitPopulation() error {
	return w.await(StagePopulation, workerPreparationReported, workerPopulation)
}

func (w *WorkerControl) ReportPopulation(payload []byte) error {
	return w.report(StagePopulation, workerPopulation, workerPopulationReported, payload)
}

func (w *WorkerControl) AwaitProvider() error {
	return w.await(StageProvider, workerPopulationReported, workerProvider)
}

// RegisterProviderInput transfers the one-shot input selected during
// admission to the live outer runner. The runner ACK is the durable handoff
// point: the worker never falls back to anonymous stdin or retries this
// transfer.
func (w *WorkerControl) RegisterProviderInput(input []byte) error {
	if w == nil || w.file == nil || w.state != workerProvider || w.providerInputRegistered || ValidateProviderInput(input) != nil {
		return ErrState
	}
	if err := writeControlFrame(w.file, attemptFrame{Version: 1, Kind: "provider-input", Payload: append([]byte(nil), input...)}, maxProviderFrameBytes); err != nil {
		return err
	}
	if err := w.file.SetReadDeadline(time.Now().Add(attemptControlTimeout)); err != nil {
		return err
	}
	defer w.file.SetReadDeadline(time.Time{})
	var ack attemptFrame
	if err := readFrame(w.file, &ack, maxFrameBytes); err != nil {
		return err
	}
	if ack.Version != 1 || ack.Kind != "provider-input-registered" || !noLegacyFields(ack) || !noTerminalFields(ack) || len(ack.Payload) != 0 {
		return ErrState
	}
	w.providerInputRegistered = true
	return nil
}

// ReportProviderError is the one bounded pre-exec failure report. It lets the
// outer owner distinguish a deliberate final source/authority rejection from
// an unexplained capability close, without introducing a general worker RPC.
func (w *WorkerControl) ReportProviderError(cause error) error {
	if w == nil || w.file == nil || w.state != workerProvider || w.providerErrorReported || cause == nil {
		return ErrState
	}
	message := []byte(cause.Error())
	if len(message) == 0 || len(message) > maxAttemptReportBytes {
		return ErrState
	}
	if err := writeControlFrame(w.file, attemptFrame{Version: 1, Kind: "provider-exec-error", Payload: message}, maxFrameBytes); err != nil {
		return err
	}
	w.providerErrorReported = true
	return nil
}

func (w *WorkerControl) report(stage AttemptStage, before, after workerState, payload []byte) error {
	if w == nil || w.file == nil || w.state != before || len(payload) > maxAttemptReportBytes {
		return ErrState
	}
	if err := writeControlFrame(w.file, attemptFrame{Version: 1, Kind: "checkpoint", Stage: stage, Payload: append([]byte{}, payload...)}, maxConfigBytes); err != nil {
		return err
	}
	w.state = after
	return nil
}

func (w *WorkerControl) await(stage AttemptStage, before, after workerState) error {
	if w == nil || w.file == nil || w.state != before {
		return ErrState
	}
	if err := w.file.SetReadDeadline(time.Now().Add(attemptControlTimeout)); err != nil {
		return err
	}
	defer w.file.SetReadDeadline(time.Time{})
	var frame attemptFrame
	if err := readFrame(w.file, &frame, maxFrameBytes); err != nil {
		return err
	}
	if frame.Version != 1 || frame.Kind != "release" || frame.Stage != stage || frame.Identity != (Identity{}) || len(frame.Payload) != 0 || frame.Terminal != nil || frame.FileIdentity != nil || frame.Digest != "" || frame.StoreCommitted || !noTerminalFields(frame) {
		return ErrState
	}
	w.state = after
	return nil
}

// ExecProvider takes ownership of cwd on every call. The registered worker
// supplies this exact descriptor only after its final source scan; runner does
// not reopen or interpret the Change pathname. On failure the descriptor is
// closed by the worker owner after its bounded diagnostic; on success it is
// CLOEXEC and disappears at provider exec.
func (w *WorkerControl) ExecProvider(spec *LaunchSpec, cwd *os.File) error {
	if w == nil || w.file == nil || w.dir == nil || w.lifetime == nil || w.state != workerProvider || !w.providerInputRegistered || spec == nil || cwd == nil || len(spec.stdin) != 0 || spec.control != nil || spec.controlID != nil || spec.stdout != nil || spec.stderr != nil {
		if cwd != nil {
			return errors.Join(ErrState, cwd.Close())
		}
		return ErrState
	}
	w.state = workerExec
	err := execPreparedCurrent(spec, cwd, w)
	if err != nil && w.file != nil {
		message := []byte(err.Error())
		if len(message) > maxAttemptReportBytes {
			message = message[:maxAttemptReportBytes]
		}
		// A failed pre-exec check must be observable by the outer owner. EOF
		// alone is ambiguous with successful unix.Exec, so publish one bounded
		// diagnostic before the worker exits; this is private control evidence,
		// not public provider output.
		_ = writeControlFrame(w.file, attemptFrame{Version: 1, Kind: "provider-exec-error", Payload: message}, maxFrameBytes)
		w.providerErrorReported = true
	}
	return err
}

func (w *WorkerControl) Close() error {
	if w == nil {
		return nil
	}
	var err error
	if w.file != nil {
		err = errors.Join(err, w.file.Close())
		w.file = nil
	}
	if w.dir != nil {
		err = errors.Join(err, w.dir.Close())
		w.dir = nil
	}
	if w.lifetime != nil {
		err = errors.Join(err, w.lifetime.Close())
		w.lifetime = nil
	}
	return err
}

func execPreparedCurrent(spec *LaunchSpec, cwd *os.File, worker *WorkerControl) (resultErr error) {
	defer func() {
		if cwd != nil {
			resultErr = errors.Join(resultErr, cwd.Close())
		}
	}()
	unix.CloseOnExec(int(cwd.Fd()))
	if err := verifyCurrentDirectory(cwd, spec.commit.Cwd); err != nil {
		return fmt.Errorf("runner: current cwd: %w", err)
	}
	target, err := verifyCommit(spec.commit.Executable, true)
	if err != nil {
		return fmt.Errorf("runner: current executable: %w", err)
	}
	defer target.Close()
	if err := sameNamedIdentity(spec.commit.Executable.Path, spec.commit.Executable.FileIdentity); err != nil {
		return err
	}
	if spec.testCurrentFinal {
		if err := writeControlFrame(worker.file, attemptFrame{Version: 1, Kind: "current-exec-check"}, maxFrameBytes); err != nil {
			return err
		}
		var ack attemptFrame
		if err := readFrame(worker.file, &ack, maxFrameBytes); err != nil || !validCurrentExecCheckAck(ack) {
			return fmt.Errorf("runner: current exec test seam: %w", errors.Join(err, ErrState))
		}
	}
	if got, err := commitRuntimeLifetime(worker.dir, worker.lifetime); err != nil || got != worker.lifetimeID {
		return fmt.Errorf("runner: current runtime lifetime: %w", errors.Join(ErrIdentity, err))
	}
	if err := verifyCurrentDirectory(cwd, spec.commit.Cwd); err != nil {
		return fmt.Errorf("runner: final current cwd: %w", err)
	}
	if err := unix.Fchdir(int(cwd.Fd())); err != nil {
		return err
	}
	closing := cwd
	cwd = nil
	if err := closing.Close(); err != nil {
		return err
	}
	if err := worker.dir.Close(); err != nil {
		return err
	}
	worker.dir = nil
	// The worker capability remains open only as a CLOEXEC diagnostic path for
	// a failed final exec. A successful provider never inherits it.
	unix.CloseOnExec(3)
	_ = unix.Close(9)
	if _, err := unix.FcntlInt(worker.lifetime.Fd(), unix.F_SETFD, 0); err != nil {
		return fmt.Errorf("runner: retain current runtime lifetime: %w", err)
	}
	if err := unix.Exec(spec.commit.Executable.Path, spec.commit.Argv, spec.commit.Env); err != nil {
		return fmt.Errorf("runner: current exec: %w", err)
	}
	return nil
}

func verifyCurrentDirectory(cwd *os.File, want fileCommitment) error {
	if cwd == nil {
		return ErrIdentity
	}
	// commitOpen uses want.Path only as the immutable commitment label. Every
	// authority fact comes from cwd; this does not open or stat the pathname.
	got, err := commitOpen(cwd, want.Path, false)
	if err != nil {
		return err
	}
	if got.UID != uint32(os.Geteuid()) || got.Mode&uint32(unix.S_IFMT) != uint32(unix.S_IFDIR) ||
		got.Mode&uint32(unix.S_IWGRP|unix.S_IWOTH|unix.S_ISUID|unix.S_ISGID) != 0 {
		return ErrIdentity
	}
	if got != want {
		return ErrIdentity
	}
	return nil
}

func RunAttemptRunner() error {
	control := os.NewFile(3, "attempt-daemon-control")
	dir := os.NewFile(9, "attempt-private-dir")
	lifetime := os.NewFile(10, "runtime-lifetime")
	if control == nil || dir == nil || lifetime == nil {
		return fmt.Errorf("runner: missing attempt capability")
	}
	defer control.Close()
	defer dir.Close()
	defer lifetime.Close()
	if _, err := commitControl(control); err != nil {
		return fmt.Errorf("runner: daemon control: %w", err)
	}
	if _, err := validatePrivateDirectory(dir); err != nil {
		return fmt.Errorf("runner: private directory: %w", err)
	}
	if _, err := commitRuntimeLifetime(dir, lifetime); err != nil {
		return fmt.Errorf("runner: runtime lifetime: %w", err)
	}
	// The attempt runner passes only deliberate duplicates through StartBlocked;
	// neither daemon control nor its retained directory may leak into the inner
	// gate through ordinary fork/exec inheritance.
	unix.CloseOnExec(3)
	unix.CloseOnExec(9)
	if _, err := unix.FcntlInt(lifetime.Fd(), unix.F_SETFD, 0); err != nil {
		return err
	}
	if err := control.SetReadDeadline(time.Now().Add(attemptControlTimeout)); err != nil {
		return err
	}
	var cfg attemptConfig
	if err := readFrame(control, &cfg, maxConfigBytes); err != nil {
		return err
	}
	_ = control.SetReadDeadline(time.Time{})
	if err := validateAttemptConfig(cfg); err != nil {
		return err
	}
	return runAttempt(control, dir, lifetime, cfg)
}

func validateAttemptConfig(cfg attemptConfig) error {
	if cfg.Version != 1 || validateAttemptName(cfg.AttemptID, 256) != nil || cfg.MarkerName != InnerActivationMarkerName || cfg.TerminalName != TerminalSpoolName {
		return ErrIdentity
	}
	if cfg.Wrapper.Executable.Path == "" || cfg.Wrapper.Cwd.Path == "" {
		return ErrIdentity
	}
	if err := validateArgv(cfg.Wrapper.Argv, cfg.Wrapper.Executable.Path); err != nil {
		return ErrIdentity
	}
	if len(cfg.Wrapper.Env) > 128 {
		return ErrIdentity
	}
	if err := validateEnvironment(cfg.Wrapper.Env); err != nil {
		return ErrIdentity
	}
	return nil
}

func validateAttemptName(value string, limit int) error {
	if value == "" || len(value) > limit {
		return ErrIdentity
	}
	return nil
}

func validateBasename(value string) error {
	if value == "" || len(value) > 255 || filepath.Base(value) != value || value == "." || value == ".." {
		return ErrIdentity
	}
	return nil
}

func runAttempt(daemon, dir, lifetime *os.File, cfg attemptConfig) (result error) {
	lease, _, err := CreateGateLease(dir, lifetime, cfg.MarkerName)
	if err != nil {
		return err
	}
	defer lease.Close()
	workerParent, workerChild, err := newControlPair("attempt-runner-worker", "attempt-worker-runner")
	if err != nil {
		return err
	}
	defer workerParent.Close()
	defer workerChild.Close()
	controlID, err := commitControl(workerChild)
	if err != nil {
		return err
	}
	// The inner worker and its eventual provider share the runner-owned PTY;
	// no separate stdout/stderr capability is allowed in this topology.
	wrapper := &LaunchSpec{commit: cfg.Wrapper, control: workerChild, controlID: &controlID}
	gate, err := os.Executable()
	if err != nil {
		return err
	}
	child, err := StartBlockedPTY(lease, gate, wrapper, true)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, child.Close()) }()
	_ = workerChild.Close()
	workerChild = nil
	reads := attemptReadSet{kq: child.kq, daemonFD: int(daemon.Fd()), workerFD: int(workerParent.Fd())}
	if err := reads.registerDaemon(); err != nil {
		return finishAttemptFailure(child, dir, cfg, &reads, err)
	}
	if err := reads.registerWorker(); err != nil {
		return finishAttemptFailure(child, dir, cfg, &reads, err)
	}
	if err := writeControlFrame(daemon, attemptFrame{Version: 1, Kind: "inner-ready", Identity: child.Identity()}, maxFrameBytes); err != nil {
		return finishAttemptFailure(child, dir, cfg, &reads, err)
	}
	frame, source, err := nextAttemptFrame(child, daemon, workerParent, true, true, 0)
	if err != nil || source != sourceDaemon || !validReleaseFrame(frame, StageSelection) {
		return finishAttemptFailure(child, dir, cfg, &reads, protocolError("selection release", source, err))
	}
	if _, err := child.Activate(); err != nil {
		return finishAttemptFailure(child, dir, cfg, &reads, err)
	}
	sequence := []struct {
		report  AttemptStage
		release AttemptStage
	}{
		{StageSelection, StagePreparation},
		{StagePreparation, StagePopulation},
		{StagePopulation, StageProvider},
	}
	for _, step := range sequence {
		frame, source, err = nextAttemptFrame(child, daemon, workerParent, true, true, 0)
		if err != nil || source != sourceWorker || !validCheckpointFrame(frame, step.report) {
			return finishAttemptFailure(child, dir, cfg, &reads, protocolError(string(step.report)+" report", source, err))
		}
		if err := writeControlFrame(daemon, frame, maxConfigBytes); err != nil {
			return finishAttemptFailure(child, dir, cfg, &reads, err)
		}
		frame, source, err = nextAttemptFrame(child, daemon, workerParent, true, true, 0)
		if err != nil || source != sourceDaemon || !validReleaseFrame(frame, step.release) {
			return finishAttemptFailure(child, dir, cfg, &reads, protocolError(string(step.release)+" release", source, err))
		}
		if err := writeControlFrame(workerParent, frame, maxFrameBytes); err != nil {
			return finishAttemptFailure(child, dir, cfg, &reads, err)
		}
	}
	reads.ptyFD = int(child.ptyMaster.Fd())
	daemonOpen, err := runReleasedProvider(child, daemon, workerParent, &reads)
	return finishAttemptWithExit(child, dir, cfg, &reads, daemon, daemonOpen, err)
}

func newControlPair(parentName, childName string) (*os.File, *os.File, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, nil, err
	}
	if err := unix.SetNonblock(fds[0], true); err != nil {
		unix.Close(fds[0])
		unix.Close(fds[1])
		return nil, nil, err
	}
	if err := unix.SetNonblock(fds[1], true); err != nil {
		unix.Close(fds[0])
		unix.Close(fds[1])
		return nil, nil, err
	}
	unix.CloseOnExec(fds[0])
	unix.CloseOnExec(fds[1])
	return os.NewFile(uintptr(fds[0]), parentName), os.NewFile(uintptr(fds[1]), childName), nil
}

type attemptSource uint8

const (
	sourceDaemon attemptSource = iota + 1
	sourceWorker
	sourceChild
)

func registerRead(kq, fd int) error {
	change := unix.Kevent_t{Ident: uint64(fd), Filter: unix.EVFILT_READ, Flags: unix.EV_ADD | unix.EV_ENABLE | unix.EV_RECEIPT}
	receipt := make([]unix.Kevent_t, 1)
	n, err := unix.Kevent(kq, []unix.Kevent_t{change}, receipt, nil)
	if err != nil {
		return err
	}
	if n != 1 || receipt[0].Flags&unix.EV_ERROR == 0 || receipt[0].Data != 0 {
		if n == 1 && receipt[0].Data != 0 {
			return unix.Errno(receipt[0].Data)
		}
		return ErrIdentity
	}
	return nil
}

func unregisterRead(kq, fd int) error {
	change := unix.Kevent_t{Ident: uint64(fd), Filter: unix.EVFILT_READ, Flags: unix.EV_DELETE | unix.EV_RECEIPT}
	receipt := make([]unix.Kevent_t, 1)
	n, err := unix.Kevent(kq, []unix.Kevent_t{change}, receipt, nil)
	if err != nil {
		return err
	}
	if n != 1 || receipt[0].Flags&unix.EV_ERROR == 0 || receipt[0].Data != 0 {
		if n == 1 && receipt[0].Data != 0 {
			return unix.Errno(receipt[0].Data)
		}
		return ErrIdentity
	}
	return nil
}

type attemptReadSet struct {
	kq               int
	daemonFD         int
	workerFD         int
	ptyFD            int
	daemonRegistered bool
	workerRegistered bool
	ptyRegistered    bool
	testUnregister   func(int) error // package-test-only; production is nil
}

func (reads *attemptReadSet) registerDaemon() error {
	if reads == nil || reads.daemonRegistered {
		return ErrState
	}
	if err := registerRead(reads.kq, reads.daemonFD); err != nil {
		return err
	}
	reads.daemonRegistered = true
	return nil
}

func (reads *attemptReadSet) registerWorker() error {
	if reads == nil || reads.workerRegistered {
		return ErrState
	}
	if err := registerRead(reads.kq, reads.workerFD); err != nil {
		return err
	}
	reads.workerRegistered = true
	return nil
}

func (reads *attemptReadSet) registerPTY() error {
	if reads == nil || reads.ptyRegistered || reads.ptyFD < 0 {
		return ErrState
	}
	if err := registerRead(reads.kq, reads.ptyFD); err != nil {
		return err
	}
	reads.ptyRegistered = true
	return nil
}

func (reads *attemptReadSet) removeDaemon() error {
	if reads == nil || !reads.daemonRegistered {
		return nil
	}
	if err := reads.unregister(reads.daemonFD); err != nil {
		return err
	}
	reads.daemonRegistered = false
	return nil
}

func (reads *attemptReadSet) removeWorker() error {
	if reads == nil || !reads.workerRegistered {
		return nil
	}
	if err := reads.unregister(reads.workerFD); err != nil {
		return err
	}
	reads.workerRegistered = false
	return nil
}

func (reads *attemptReadSet) removePTY() error {
	if reads == nil || !reads.ptyRegistered {
		return nil
	}
	if err := reads.unregister(reads.ptyFD); err != nil {
		return err
	}
	reads.ptyRegistered = false
	return nil
}

func (reads *attemptReadSet) unregister(fd int) error {
	if reads.testUnregister != nil {
		if err := reads.testUnregister(fd); err != nil {
			return err
		}
	}
	return unregisterRead(reads.kq, fd)
}

func retireReadableFilter(remove func() error) error {
	if remove == nil {
		return ErrState
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	var lastErr error
	for {
		if err := remove(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return errors.Join(ErrUnresolved, lastErr)
		}
		if remaining > 25*time.Millisecond {
			remaining = 25 * time.Millisecond
		}
		time.Sleep(remaining)
	}
}

func (reads *attemptReadSet) processOnly() error {
	if reads == nil {
		return nil
	}
	return errors.Join(
		retireReadableFilter(reads.removeDaemon),
		retireReadableFilter(reads.removeWorker),
		retireReadableFilter(reads.removePTY),
	)
}

func nextAttemptFrame(child *OwnedChild, daemon, worker *os.File, daemonOpen, workerOpen bool, timeout time.Duration) (attemptFrame, attemptSource, error) {
	for {
		events := make([]unix.Kevent_t, 1)
		var ts *unix.Timespec
		var value unix.Timespec
		if timeout > 0 {
			value = unix.NsecToTimespec(timeout.Nanoseconds())
			ts = &value
		}
		n, err := unix.Kevent(child.kq, nil, events, ts)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return attemptFrame{}, 0, err
		}
		if n == 0 {
			return attemptFrame{}, 0, os.ErrDeadlineExceeded
		}
		ev := events[0]
		if ev.Filter == unix.EVFILT_PROC && ev.Ident == uint64(child.identity.PID) && ev.Fflags&unix.NOTE_EXIT != 0 {
			child.exitObserved = true
			child.state = stateExited
			return attemptFrame{}, sourceChild, nil
		}
		if ev.Filter != unix.EVFILT_READ || ev.Flags&unix.EV_ERROR != 0 {
			return attemptFrame{}, 0, ErrIdentity
		}
		var file *os.File
		var source attemptSource
		switch {
		case daemonOpen && ev.Ident == uint64(daemon.Fd()):
			file, source = daemon, sourceDaemon
		case workerOpen && ev.Ident == uint64(worker.Fd()):
			file, source = worker, sourceWorker
		default:
			return attemptFrame{}, 0, ErrIdentity
		}
		if err := file.SetReadDeadline(time.Now().Add(attemptControlTimeout)); err != nil {
			return attemptFrame{}, source, err
		}
		var frame attemptFrame
		err = readFrame(file, &frame, maxConfigBytes)
		_ = file.SetReadDeadline(time.Time{})
		return frame, source, err
	}
}

func protocolError(want string, got attemptSource, err error) error {
	if err == nil {
		err = ErrState
	}
	return fmt.Errorf("runner: expected %s (source %d): %w", want, got, err)
}

func validReleaseFrame(frame attemptFrame, stage AttemptStage) bool {
	return frame.Version == 1 && frame.Kind == "release" && frame.Stage == stage && frame.Identity == (Identity{}) && len(frame.Payload) == 0 && frame.Terminal == nil && frame.FileIdentity == nil && frame.Digest == "" && !frame.StoreCommitted && noTerminalFields(frame)
}

func validCheckpointFrame(frame attemptFrame, stage AttemptStage) bool {
	return frame.Version == 1 && frame.Kind == "checkpoint" && frame.Stage == stage && frame.Identity == (Identity{}) && len(frame.Payload) <= maxAttemptReportBytes && frame.Terminal == nil && frame.FileIdentity == nil && frame.Digest == "" && !frame.StoreCommitted && noTerminalFields(frame)
}

func finishAttemptFailure(child *OwnedChild, dir *os.File, cfg attemptConfig, reads *attemptReadSet, cause error) error {
	return finishAttemptWithExit(child, dir, cfg, reads, nil, false, cause)
}

func finishAttemptWithExit(child *OwnedChild, dir *os.File, cfg attemptConfig, reads *attemptReadSet, daemon *os.File, daemonOpen bool, cause error) error {
	var filterErr error
	if reads != nil {
		filterErr = reads.processOnly()
	}
	cause = errors.Join(cause, filterErr)
	exit, cleanupErr := waitForAttemptChild(child)
	if cleanupErr != nil {
		return errors.Join(cause, cleanupErr)
	}
	// A terminal spool is evidence that the complete owner cleanup path was
	// observed. Protocol or kill errors marked unresolved must never be turned
	// into durable terminal evidence merely because Wait eventually returned.
	if errors.Is(cause, ErrUnresolved) {
		return cause
	}
	if cause != nil {
		exit.Aborted = true
	}
	return errors.Join(cause, publishAttemptTerminal(child, dir, cfg, exit, daemon, daemonOpen, cause))
}

// waitForAttemptChild is intentionally synchronous and has no terminal error
// path after a child exists. Cleanup uncertainty retains the sole live owner and
// retries with a bounded delay until exact group convergence and the sole Wait
// succeed. It may therefore remain here indefinitely rather than orphaning the
// child or publishing a false terminal observation.
func waitForAttemptChild(child *OwnedChild) (Exit, error) {
	if child == nil {
		return Exit{}, ErrState
	}
	var cleanupErr error
	for {
		if exit, err := child.waitedExit(); err == nil {
			return exit, cleanupErr
		}
		// Failure paths may arrive after the owner loop has retired its read
		// filters. Observe the exact child exit before attempting a group signal;
		// otherwise a dead gate can make every retry look like an authority
		// failure and prevent deterministic reaping.
		if child.state == stateActivated && !child.exitObserved {
			if err := child.refreshExit(); err != nil {
				if !interruptedCleanup(err) {
					cleanupErr = errors.Join(cleanupErr, err)
				}
			}
		}
		switch child.state {
		case stateBlocked:
			if err := child.Abort(); err != nil {
				cleanupErr = errors.Join(cleanupErr, err)
			}
		case stateActivated:
			// A signal can race a natural exit (Darwin may report EPERM after
			// census but before kill). Retry while the exact owner is live; only
			// uncertainty observed after exit is retained as a publication block.
			_, _ = child.Terminate(defaultStopTimeout)
		case stateExited:
			if child.activated {
				_, err := child.FinishAfterExit(attemptControlTimeout)
				if err != nil && child.state != stateWaited && !interruptedCleanup(err) {
					cleanupErr = errors.Join(cleanupErr, err)
				}
			} else {
				_, err := child.finishInertAfterExit()
				if err != nil && child.state != stateWaited && !interruptedCleanup(err) {
					cleanupErr = errors.Join(cleanupErr, err)
				}
			}
		case stateWaited:
			exit, err := child.waitedExit()
			return exit, errors.Join(cleanupErr, err)
		}
		if exit, err := child.waitedExit(); err == nil {
			return exit, cleanupErr
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func interruptedCleanup(err error) bool {
	return errors.Is(err, unix.EINTR) || strings.Contains(err.Error(), "interrupted system call")
}

func publishAttemptTerminal(child *OwnedChild, dir *os.File, cfg attemptConfig, exit Exit, daemon *os.File, daemonOpen bool, cause error) error {
	if _, err := child.waitedExit(); err != nil {
		return err
	}
	message := ""
	if cause != nil {
		message = cause.Error()
		if len(message) > 8192 {
			message = message[:8192]
		}
	}
	record, err := PublishTerminal(dir, cfg.TerminalName, Terminal{AttemptID: cfg.AttemptID, Process: child.Identity(), Exit: exit, Message: message})
	if err != nil {
		return err
	}
	if !daemonOpen || daemon == nil {
		return nil
	}
	frame := attemptFrame{Version: 1, Kind: "terminal", Terminal: &record.Terminal, FileIdentity: &record.Identity, Digest: record.Digest}
	if err := writeControlFrame(daemon, frame, maxConfigBytes); err != nil {
		return nil // durable spool is the recovery contract after control loss
	}
	if err := daemon.SetReadDeadline(time.Now().Add(attemptControlTimeout)); err != nil {
		return err
	}
	defer daemon.SetReadDeadline(time.Time{})
	for {
		var ack attemptFrame
		if err := readFrame(daemon, &ack, maxConfigBytes); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, os.ErrDeadlineExceeded) {
				return nil
			}
			return err
		}
		// A terminate sent just before natural provider exit can remain queued
		// while the owner publishes its terminal evidence. It is already
		// satisfied by the exact child wait above; consume this one idempotent
		// stale command and continue waiting for the authenticated acknowledgement.
		if ack.Kind == "terminate" && validBareAttemptFrame(ack) {
			continue
		}
		if !validTerminalAck(ack, record) {
			return ErrIdentity
		}
		return AcknowledgeTerminal(dir, cfg.TerminalName, record, true)
	}
}

func writeControlFrame(file *os.File, value any, limit int) error {
	if file == nil {
		return ErrIdentity
	}
	if err := file.SetWriteDeadline(time.Now().Add(attemptControlTimeout)); err != nil {
		return err
	}
	defer file.SetWriteDeadline(time.Time{})
	return writeFrame(file, value, limit)
}
