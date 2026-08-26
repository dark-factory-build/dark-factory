//go:build darwin

package runner

import (
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
)

// AttemptController is the daemon side of one fixed attempt-runner protocol.
// Its peer is the sole control capability accepted by --attempt-runner.
type AttemptController struct {
	file      *os.File
	state     attemptControllerState
	attemptID string
	inner     Identity
	last      *TerminalRecord
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
	if err := validateBasename(spec.MarkerName); err != nil {
		return err
	}
	if err := validateBasename(spec.TerminalName); err != nil || spec.MarkerName == spec.TerminalName {
		return ErrIdentity
	}
	if spec.Wrapper.control != nil || spec.Wrapper.controlID != nil || len(spec.Wrapper.stdin) != 0 || spec.Wrapper.stdout != nil || spec.Wrapper.stderr != nil || spec.Wrapper.testFinal != nil || spec.Wrapper.testCurrentFinal {
		return fmt.Errorf("runner: wrapper launch contains unsupported capabilities")
	}
	cfg := attemptConfig{Version: 1, AttemptID: spec.AttemptID, Wrapper: spec.Wrapper.commit, MarkerName: spec.MarkerName, TerminalName: spec.TerminalName}
	if err := writeControlFrame(c.file, cfg, maxConfigBytes); err != nil {
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
		if frame.Kind != "inner-ready" || !frame.Identity.Valid() || frame.Identity.PID != frame.Identity.PGID || frame.Stage != "" || len(frame.Payload) != 0 || frame.Terminal != nil || frame.FileIdentity != nil || frame.Digest != "" || frame.StoreCommitted {
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
		if frame.Kind == "current-exec-check" {
			return AttemptEvent{Kind: AttemptCheckpoint, Stage: StageProvider}, nil
		}
		if frame.Kind != "terminal" || frame.Stage != "" || frame.Identity != (Identity{}) || len(frame.Payload) != 0 || frame.Terminal == nil || frame.FileIdentity == nil || len(frame.Digest) != 64 || frame.StoreCommitted {
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

func (c *AttemptController) acceptCheckpoint(frame attemptFrame, stage AttemptStage, next attemptControllerState) (AttemptEvent, error) {
	if frame.Kind != "checkpoint" || frame.Stage != stage || frame.Identity != (Identity{}) || len(frame.Payload) > maxAttemptReportBytes || frame.Terminal != nil || frame.FileIdentity != nil || frame.Digest != "" || frame.StoreCommitted {
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
	if err := writeControlFrame(c.file, attemptFrame{Version: 1, Kind: "release", Stage: stage}, maxFrameBytes); err != nil {
		return err
	}
	c.state = next
	return nil
}

func (c *AttemptController) Terminate() error {
	if c == nil || c.file == nil || c.state < controllerInnerReady || c.state >= controllerTerminal {
		return ErrState
	}
	return writeControlFrame(c.file, attemptFrame{Version: 1, Kind: "terminate"}, maxFrameBytes)
}

func (c *AttemptController) AcknowledgeTerminal(want *TerminalRecord, storeCommitted bool) error {
	if c == nil || c.file == nil || c.state != controllerTerminal || !storeCommitted || want == nil || c.last == nil {
		return ErrState
	}
	if want.Digest != c.last.Digest || want.Identity != c.last.Identity || want.Terminal != c.last.Terminal {
		return ErrIdentity
	}
	if err := writeControlFrame(c.file, attemptFrame{Version: 1, Kind: "terminal-ack", Terminal: &want.Terminal, FileIdentity: &want.Identity, Digest: want.Digest, StoreCommitted: true}, maxFrameBytes); err != nil {
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
	return writeControlFrame(c.file, attemptFrame{Version: 1, Kind: "current-exec-check-ack"}, maxFrameBytes)
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
	file     *os.File
	dir      *os.File
	identity Identity
	state    workerState
}

func OpenWorkerControl() (*WorkerControl, error) {
	control := os.NewFile(3, "attempt-worker-control")
	dir := os.NewFile(9, "attempt-private-dir")
	if control == nil || dir == nil {
		return nil, ErrIdentity
	}
	if _, err := commitControl(control); err != nil {
		return nil, fmt.Errorf("runner: worker control: %w", err)
	}
	if _, err := validatePrivateDirectory(dir); err != nil {
		return nil, fmt.Errorf("runner: worker private directory: %w", err)
	}
	// These capabilities belong only to the wrapper. Its bounded Git children
	// and eventual provider must never gain accidental duplicate ownership.
	unix.CloseOnExec(3)
	unix.CloseOnExec(9)
	id, err := readIdentity(os.Getpid())
	if err != nil || id.PID != id.PGID {
		return nil, ErrIdentity
	}
	return &WorkerControl{file: control, dir: dir, identity: id, state: workerSelection}, nil
}

func (w *WorkerControl) Identity() Identity {
	if w == nil {
		return Identity{}
	}
	return w.identity
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
	if frame.Version != 1 || frame.Kind != "release" || frame.Stage != stage || frame.Identity != (Identity{}) || len(frame.Payload) != 0 || frame.Terminal != nil || frame.FileIdentity != nil || frame.Digest != "" || frame.StoreCommitted {
		return ErrState
	}
	w.state = after
	return nil
}

func (w *WorkerControl) ExecProvider(spec *LaunchSpec) error {
	if w == nil || w.file == nil || w.dir == nil || w.state != workerProvider || spec == nil || spec.control != nil || spec.controlID != nil || spec.stdout != nil || spec.stderr != nil {
		return ErrState
	}
	w.state = workerExec
	return execPreparedCurrent(spec, w)
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
	return err
}

func execPreparedCurrent(spec *LaunchSpec, worker *WorkerControl) error {
	target, err := verifyCommit(spec.commit.Executable, true)
	if err != nil {
		return fmt.Errorf("runner: current executable: %w", err)
	}
	defer target.Close()
	cwd, err := verifyCommit(spec.commit.Cwd, false)
	if err != nil {
		return fmt.Errorf("runner: current cwd: %w", err)
	}
	defer cwd.Close()
	if err := sameNamedIdentity(spec.commit.Executable.Path, spec.commit.Executable.FileIdentity); err != nil {
		return err
	}
	if spec.testCurrentFinal {
		if err := writeControlFrame(worker.file, attemptFrame{Version: 1, Kind: "current-exec-check"}, maxFrameBytes); err != nil {
			return err
		}
		var ack attemptFrame
		if err := readFrame(worker.file, &ack, maxFrameBytes); err != nil || ack.Version != 1 || ack.Kind != "current-exec-check-ack" {
			return fmt.Errorf("runner: current exec test seam: %w", errors.Join(err, ErrState))
		}
	}
	stdin, err := anonymousFile(worker.dir, "provider-stdin", spec.stdin)
	if err != nil {
		return err
	}
	defer stdin.Close()
	if err := unix.Fchdir(int(cwd.Fd())); err != nil {
		return err
	}
	if err := unix.Dup2(int(stdin.Fd()), 0); err != nil {
		return err
	}
	if err := worker.file.Close(); err != nil {
		return err
	}
	worker.file = nil
	if err := worker.dir.Close(); err != nil {
		return err
	}
	worker.dir = nil
	_ = unix.Close(3)
	_ = unix.Close(9)
	if err := unix.Exec(spec.commit.Executable.Path, spec.commit.Argv, spec.commit.Env); err != nil {
		return fmt.Errorf("runner: current exec: %w", err)
	}
	return nil
}

func RunAttemptRunner() error {
	control := os.NewFile(3, "attempt-daemon-control")
	dir := os.NewFile(9, "attempt-private-dir")
	if control == nil || dir == nil {
		return fmt.Errorf("runner: missing attempt capability")
	}
	defer control.Close()
	defer dir.Close()
	if _, err := commitControl(control); err != nil {
		return fmt.Errorf("runner: daemon control: %w", err)
	}
	if _, err := validatePrivateDirectory(dir); err != nil {
		return fmt.Errorf("runner: private directory: %w", err)
	}
	// The attempt runner passes only deliberate duplicates through StartBlocked;
	// neither daemon control nor its retained directory may leak into the inner
	// gate through ordinary fork/exec inheritance.
	unix.CloseOnExec(3)
	unix.CloseOnExec(9)
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
	return runAttempt(control, dir, cfg)
}

func validateAttemptConfig(cfg attemptConfig) error {
	if cfg.Version != 1 || validateAttemptName(cfg.AttemptID, 256) != nil || validateBasename(cfg.MarkerName) != nil || validateBasename(cfg.TerminalName) != nil || cfg.MarkerName == cfg.TerminalName {
		return ErrIdentity
	}
	if cfg.Wrapper.Executable.Path == "" || cfg.Wrapper.Cwd.Path == "" || len(cfg.Wrapper.Argv) == 0 || cfg.Wrapper.Argv[0] != cfg.Wrapper.Executable.Path {
		return ErrIdentity
	}
	if len(cfg.Wrapper.Argv) > 129 || len(cfg.Wrapper.Env) > 128 {
		return ErrIdentity
	}
	for _, value := range append(append([]string{}, cfg.Wrapper.Argv...), cfg.Wrapper.Env...) {
		if len(value) > 8192 || strings.IndexByte(value, 0) >= 0 {
			return ErrIdentity
		}
	}
	for _, value := range cfg.Wrapper.Env {
		parts := strings.SplitN(value, "=", 2)
		if len(parts) != 2 || !allowedEnv(parts[0]) {
			return ErrIdentity
		}
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

func runAttempt(daemon, dir *os.File, cfg attemptConfig) (result error) {
	lease, _, err := CreateGateLease(dir, cfg.MarkerName)
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
	wrapper := &LaunchSpec{commit: cfg.Wrapper, stdout: os.Stdout, stderr: os.Stderr, control: workerChild, controlID: &controlID}
	gate, err := os.Executable()
	if err != nil {
		return err
	}
	child, err := StartBlocked(lease, gate, wrapper, true)
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
	daemonOpen, err := finishReleasedProvider(child, daemon, workerParent, &reads)
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
	daemonRegistered bool
	workerRegistered bool
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

func (reads *attemptReadSet) unregister(fd int) error {
	if reads.testUnregister != nil {
		if err := reads.testUnregister(fd); err != nil {
			return err
		}
	}
	return unregisterRead(reads.kq, fd)
}

func retireReadableFilter(remove func() error) {
	for {
		if err := remove(); err == nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (reads *attemptReadSet) processOnly() {
	if reads == nil {
		return
	}
	retireReadableFilter(reads.removeDaemon)
	retireReadableFilter(reads.removeWorker)
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
	return frame.Version == 1 && frame.Kind == "release" && frame.Stage == stage && frame.Identity == (Identity{}) && len(frame.Payload) == 0 && frame.Terminal == nil && frame.FileIdentity == nil && frame.Digest == "" && !frame.StoreCommitted
}

func validCheckpointFrame(frame attemptFrame, stage AttemptStage) bool {
	return frame.Version == 1 && frame.Kind == "checkpoint" && frame.Stage == stage && frame.Identity == (Identity{}) && len(frame.Payload) <= maxAttemptReportBytes && frame.Terminal == nil && frame.FileIdentity == nil && frame.Digest == "" && !frame.StoreCommitted
}

func finishAttemptFailure(child *OwnedChild, dir *os.File, cfg attemptConfig, reads *attemptReadSet, cause error) error {
	return finishAttemptWithExit(child, dir, cfg, reads, nil, false, cause)
}

func finishReleasedProvider(child *OwnedChild, daemon, worker *os.File, reads *attemptReadSet) (bool, error) {
	daemonOpen := true
	workerOpen := true
	for {
		frame, source, err := nextAttemptFrame(child, daemon, worker, daemonOpen, workerOpen, 0)
		if source == sourceChild && err == nil {
			return daemonOpen, nil
		}
		if source == sourceDaemon {
			if errors.Is(err, io.EOF) {
				retireReadableFilter(reads.removeDaemon)
				daemonOpen = false
				continue
			}
			if err != nil {
				return daemonOpen, err
			}
			if frame.Version != 1 || frame.Kind != "terminate" {
				return daemonOpen, ErrState
			}
			return daemonOpen, nil
		}
		if source == sourceWorker {
			if errors.Is(err, io.EOF) {
				retireReadableFilter(reads.removeWorker)
				workerOpen = false
				continue
			}
			if err != nil {
				return daemonOpen, err
			}
			if frame.Version != 1 || frame.Kind != "current-exec-check" || !daemonOpen {
				return daemonOpen, ErrState
			}
			if err := writeControlFrame(daemon, frame, maxFrameBytes); err != nil {
				return false, err
			}
			ack, ackSource, err := nextAttemptFrame(child, daemon, worker, true, true, 0)
			if err != nil || ackSource != sourceDaemon || ack.Version != 1 || ack.Kind != "current-exec-check-ack" {
				return false, protocolError("current exec check ack", ackSource, err)
			}
			if err := writeControlFrame(worker, ack, maxFrameBytes); err != nil {
				return daemonOpen, err
			}
			continue
		}
		return daemonOpen, err
	}
}

func finishAttemptWithExit(child *OwnedChild, dir *os.File, cfg attemptConfig, reads *attemptReadSet, daemon *os.File, daemonOpen bool, cause error) error {
	if reads != nil {
		reads.processOnly()
	}
	exit, cleanupErr := waitForAttemptChild(child)
	if cleanupErr != nil {
		return errors.Join(cause, cleanupErr)
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
	for {
		if exit, err := child.waitedExit(); err == nil {
			return exit, nil
		}
		switch child.state {
		case stateBlocked:
			_ = child.Abort()
		case stateActivated:
			_, _ = child.Terminate(defaultStopTimeout)
		case stateExited:
			if child.activated {
				_, _ = child.FinishAfterExit(attemptControlTimeout)
			} else {
				_, _ = child.finishInertAfterExit()
			}
		case stateWaited:
			return child.waitedExit()
		}
		if exit, err := child.waitedExit(); err == nil {
			return exit, nil
		}
		time.Sleep(25 * time.Millisecond)
	}
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
	var ack attemptFrame
	if err := readFrame(daemon, &ack, maxConfigBytes); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, os.ErrDeadlineExceeded) {
			return nil
		}
		return err
	}
	if ack.Version != 1 || ack.Kind != "terminal-ack" || ack.Stage != "" || ack.Identity != (Identity{}) || len(ack.Payload) != 0 || !ack.StoreCommitted || ack.Terminal == nil || ack.FileIdentity == nil || ack.Digest != record.Digest || *ack.FileIdentity != record.Identity || *ack.Terminal != record.Terminal {
		return ErrIdentity
	}
	return AcknowledgeTerminal(dir, cfg.TerminalName, record, true)
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
