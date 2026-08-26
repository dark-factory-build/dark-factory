//go:build darwin

package runner

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

type GateLease struct {
	dir         *os.File
	dirIdentity fileCommitment
	lifetime    *os.File
	lifetimeID  descriptorCommitment
	basename    string
	marker      *FileIdentity
	closed      bool
}

func CreateGateLease(dir, lifetime *os.File, basename string) (*GateLease, FileIdentity, error) {
	if dir == nil || lifetime == nil || basename != OuterActivationMarkerName && basename != InnerActivationMarkerName {
		return nil, FileIdentity{}, fmt.Errorf("runner: invalid marker name")
	}
	dup, err := unix.FcntlInt(dir.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, FileIdentity{}, err
	}
	f := os.NewFile(uintptr(dup), "gate-lease-dir")
	c, err := validatePrivateDirectory(f)
	if err != nil {
		f.Close()
		return nil, FileIdentity{}, err
	}
	lifetimeDup, err := unix.FcntlInt(lifetime.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		f.Close()
		return nil, FileIdentity{}, err
	}
	lifetimeFile := os.NewFile(uintptr(lifetimeDup), "runtime-lifetime")
	lifetimeID, err := commitRuntimeLifetime(f, lifetimeFile)
	if err != nil {
		f.Close()
		lifetimeFile.Close()
		return nil, FileIdentity{}, err
	}
	var s unix.Stat_t
	err = unix.Fstatat(int(f.Fd()), basename, &s, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		f.Close()
		lifetimeFile.Close()
		return nil, FileIdentity{}, os.ErrExist
	}
	if !errors.Is(err, unix.ENOENT) {
		f.Close()
		lifetimeFile.Close()
		return nil, FileIdentity{}, err
	}
	return &GateLease{dir: f, dirIdentity: c, lifetime: lifetimeFile, lifetimeID: lifetimeID, basename: basename}, c.FileIdentity, nil
}

func commitRuntimeLifetime(dir, lifetime *os.File) (descriptorCommitment, error) {
	if dir == nil || lifetime == nil {
		return descriptorCommitment{}, ErrIdentity
	}
	var opened, named unix.Stat_t
	if err := unix.Fstat(int(lifetime.Fd()), &opened); err != nil {
		return descriptorCommitment{}, err
	}
	if err := unix.Fstatat(int(dir.Fd()), RuntimeLifetimeLeaseName, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return descriptorCommitment{}, err
	}
	valid := func(stat unix.Stat_t) bool {
		return stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Mode&0o7777 == 0o600 && stat.Uid == uint32(os.Geteuid()) && stat.Nlink == 1 && stat.Size == 0 && stat.Dev != 0 && stat.Ino != 0
	}
	if !valid(opened) || !valid(named) || opened.Dev != named.Dev || opened.Ino != named.Ino {
		return descriptorCommitment{}, ErrIdentity
	}
	if err := unix.Flock(int(lifetime.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return descriptorCommitment{}, err
	}
	var recheck, namedRecheck unix.Stat_t
	if err := unix.Fstat(int(lifetime.Fd()), &recheck); err != nil || unix.Fstatat(int(dir.Fd()), RuntimeLifetimeLeaseName, &namedRecheck, unix.AT_SYMLINK_NOFOLLOW) != nil || !valid(recheck) || !valid(namedRecheck) || recheck.Dev != opened.Dev || recheck.Ino != opened.Ino || namedRecheck.Dev != opened.Dev || namedRecheck.Ino != opened.Ino {
		return descriptorCommitment{}, errors.Join(ErrIdentity, err)
	}
	return descriptorCommitment{FileIdentity: FileIdentity{Device: uint64(opened.Dev), Inode: opened.Ino}, UID: opened.Uid, GID: opened.Gid, Mode: uint32(opened.Mode)}, nil
}

func fdPath(f *os.File) (string, error) {
	b := make([]byte, 1024)
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), unix.F_GETPATH, uintptr(unsafePointer(&b[0])))
	if errno != 0 {
		return "", errno
	}
	n := 0
	for n < len(b) && b[n] != 0 {
		n++
	}
	if n == 0 {
		return "", ErrIdentity
	}
	return string(b[:n]), nil
}

// Kept in the Darwin file: F_GETPATH has no typed Go wrapper.
func unsafePointer(p *byte) uintptr { return uintptr(unsafe.Pointer(p)) }

func (l *GateLease) Close() error {
	if l == nil || l.closed {
		return nil
	}
	l.closed = true
	return errors.Join(l.dir.Close(), l.lifetime.Close())
}

type OwnedChild struct {
	cmd            *exec.Cmd
	identity       Identity
	exit           Exit
	kq             int
	activation     *os.File
	status         *os.File
	lease          *GateLease
	state          state
	activated      bool
	exitObserved   bool
	exitRegistered bool
	keepDirectory  bool
	// testConvergence injects a package-test-only failure immediately before
	// activated group convergence. Production children always leave it nil.
	testConvergence func() error
}

func (c *OwnedChild) Identity() Identity {
	if c == nil {
		return Identity{}
	}
	return c.identity
}

func StartBlocked(lease *GateLease, gateExecutable string, spec *LaunchSpec, keepDirectoryAcrossExec bool) (child *OwnedChild, err error) {
	if lease == nil || lease.closed || lease.marker != nil || lease.lifetime == nil || spec == nil {
		return nil, ErrState
	}
	gate, err := canonical(gateExecutable)
	if err != nil {
		return nil, err
	}
	if _, err := commitExecutable(gate); err != nil {
		return nil, fmt.Errorf("runner: gate executable: %w", err)
	}
	config, err := anonymousFile(lease.dir, "config", nil)
	if err != nil {
		return nil, err
	}
	defer config.Close()
	if err := writeFrame(config, gateConfig{Version: 1, Target: spec.commit, LeaseDirectory: lease.dirIdentity, Lifetime: lease.lifetimeID, MarkerName: lease.basename, KeepDirectory: keepDirectoryAcrossExec, Control: spec.controlID, TestFinalCheck: spec.testFinal != nil}, maxConfigBytes); err != nil {
		return nil, err
	}
	if _, err := config.Seek(0, 0); err != nil {
		return nil, err
	}
	stdin, err := anonymousFile(lease.dir, "stdin", spec.stdin)
	if err != nil {
		return nil, err
	}
	defer stdin.Close()
	leashR, leashW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer leashR.Close()
	statusR, statusW, err := os.Pipe()
	if err != nil {
		leashW.Close()
		return nil, err
	}
	defer statusW.Close()
	stdout := spec.stdout
	stderr := spec.stderr
	var closeOut, closeErr *os.File
	if stdout == nil {
		closeOut, err = os.OpenFile("/dev/null", os.O_WRONLY, 0)
		if err != nil {
			return nil, err
		}
		stdout = closeOut
		defer closeOut.Close()
	}
	if stderr == nil {
		closeErr, err = os.OpenFile("/dev/null", os.O_WRONLY, 0)
		if err != nil {
			return nil, err
		}
		stderr = closeErr
		defer closeErr.Close()
	}
	leaseDup, err := unix.FcntlInt(lease.dir.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	leaseFile := os.NewFile(uintptr(leaseDup), "lease-dir")
	defer leaseFile.Close()
	lifetimeDup, err := unix.FcntlInt(lease.lifetime.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	lifetimeFile := os.NewFile(uintptr(lifetimeDup), "runtime-lifetime")
	defer lifetimeFile.Close()
	kq, err := unix.Kqueue()
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(kq)
	cmd := exec.Command(gate, "--exec-gate")
	cmd.Env = []string{}
	cmd.Stderr = stderr
	cmd.ExtraFiles = []*os.File{config, leashR, statusW, stdin, stdout, stderr, leaseFile, lifetimeFile}
	if spec.controlID != nil {
		cmd.ExtraFiles = append(cmd.ExtraFiles, spec.control)
	}
	if spec.testFinal != nil {
		cmd.ExtraFiles = append(cmd.ExtraFiles, spec.testFinal)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		unix.Close(kq)
		leashW.Close()
		statusR.Close()
		return nil, err
	}
	c := &OwnedChild{cmd: cmd, identity: Identity{PID: cmd.Process.Pid, PGID: cmd.Process.Pid}, kq: kq, activation: leashW, status: statusR, lease: lease, state: stateBlocked, keepDirectory: keepDirectoryAcrossExec}
	defer func() {
		if err != nil {
			if cleanupErr := c.hardCleanup(); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("runner: failed launch cleanup: %w", cleanupErr))
			}
		}
	}()
	_ = leashR.Close()
	_ = statusW.Close()
	first, e := readIdentity(c.identity.PID)
	if e != nil {
		err = fmt.Errorf("runner: first identity: %w", e)
		return nil, err
	}
	if first.PGID != first.PID {
		err = ErrIdentity
		return nil, err
	}
	c.identity = first
	if e = registerExit(kq, c.identity.PID); e != nil {
		err = fmt.Errorf("runner: register exit: %w", e)
		return nil, err
	}
	c.exitRegistered = true
	if e = statusR.SetReadDeadline(time.Now().Add(4 * time.Second)); e != nil {
		err = fmt.Errorf("runner: ready deadline: %w", e)
		return nil, err
	}
	var ready gateFrame
	if e = readFrame(statusR, &ready, maxFrameBytes); e != nil {
		err = fmt.Errorf("runner: ready frame: %w", e)
		return nil, err
	}
	second, e := readIdentity(c.identity.PID)
	if e != nil {
		err = fmt.Errorf("runner: second identity: %w", e)
		return nil, err
	}
	if ready.Kind != "ready" || !ready.Identity.Valid() || ready.Identity != first || second != first {
		err = ErrIdentity
		return nil, err
	}
	c.identity = first
	return c, nil
}

func anonymousFile(dir *os.File, prefix string, body []byte) (*os.File, error) {
	return anonymousFileWithHook(dir, prefix, body, nil)
}

func anonymousFileWithHook(dir *os.File, prefix string, body []byte, afterOpen func(string)) (*os.File, error) {
	name := ""
	switch prefix {
	case "config":
		name = GateConfigScratchName
	case "stdin":
		name = GateStdinScratchName
	case "provider-stdin":
		name = ProviderStdinScratchName
	default:
		return nil, ErrIdentity
	}
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	if afterOpen != nil {
		afterOpen(name)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		f.Close()
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o7777 != 0o600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		_ = unlinkExactScratch(dir, name, stat)
		f.Close()
		return nil, ErrIdentity
	}
	// Fixed create-only names make a crash residue enumerable. The open inode
	// is unlinked immediately; successful launches leave no named scratch.
	if err := unix.Unlinkat(int(dir.Fd()), name, 0); err != nil {
		f.Close()
		return nil, err
	}
	if len(body) > 0 {
		if _, err := f.Write(body); err != nil {
			f.Close()
			return nil, err
		}
	}
	if _, err := f.Seek(0, 0); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func (c *OwnedChild) Activate() (FileIdentity, error) {
	if c == nil || c.state != stateBlocked || c.lease.closed {
		return FileIdentity{}, ErrState
	}
	fd, err := unix.Openat(int(c.lease.dir.Fd()), c.lease.basename, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return FileIdentity{}, err
	}
	f := os.NewFile(uintptr(fd), "activation-marker")
	id, err := statIdentity(f)
	if err == nil {
		err = unix.Fsync(fd)
	}
	if err == nil {
		err = unix.Fsync(int(c.lease.dir.Fd()))
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return FileIdentity{}, err
	}
	c.lease.marker = &id
	// The durable marker is the activation linearization point. From here on,
	// a lost leash acknowledgement is still an activated child, never inert.
	c.activated = true
	c.state = stateActivated
	if err := writeFrame(c.activation, gateFrame{Kind: "activate", Marker: &id}, maxFrameBytes); err != nil {
		return id, err
	}
	if err := c.activation.Close(); err != nil {
		return id, err
	}
	c.activation = nil
	return id, nil
}

func (c *OwnedChild) Abort() error {
	if c == nil || c.state != stateBlocked {
		return ErrState
	}
	var err error
	if c.activation != nil {
		err = c.activation.Close()
		c.activation = nil
	}
	if !c.exitObserved {
		if _, werr := c.waitForExit(4 * time.Second); werr != nil {
			return werr
		}
	}
	_, finishErr := c.finishInertAfterExit()
	return errors.Join(err, finishErr)
}

func (c *OwnedChild) waitForExit(timeout time.Duration) (unix.Kevent_t, error) {
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return unix.Kevent_t{}, os.ErrDeadlineExceeded
		}
		events := make([]unix.Kevent_t, 1)
		ts := unix.NsecToTimespec(remaining.Nanoseconds())
		n, err := unix.Kevent(c.kq, nil, events, &ts)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return unix.Kevent_t{}, err
		}
		if n == 0 {
			return unix.Kevent_t{}, os.ErrDeadlineExceeded
		}
		ev := events[0]
		if ev.Ident != uint64(c.identity.PID) || ev.Filter != unix.EVFILT_PROC || ev.Fflags&unix.NOTE_EXIT == 0 {
			return unix.Kevent_t{}, ErrIdentity
		}
		c.exitObserved = true
		c.state = stateExited
		return ev, nil
	}
}

func (c *OwnedChild) FinishAfterExit(timeout time.Duration) (Exit, error) {
	if c == nil || !c.activated || c.state == stateWaited {
		return Exit{}, ErrState
	}
	if !c.exitObserved {
		if _, err := c.waitForExit(timeout); err != nil {
			return Exit{}, err
		}
	}
	waitErr := c.waitActivatedOnce()
	if c.state != stateWaited {
		return Exit{}, waitErr
	}
	_ = c.status.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	var frame gateFrame
	if ferr := readFrame(c.status, &frame, maxFrameBytes); ferr == nil && frame.Kind == "launch-error" {
		c.exit.LaunchErr = frame.Error
	}
	return c.exit, nil
}

func exitFromWait(ps *os.ProcessState, err error) Exit {
	e := Exit{Code: -1}
	if ps == nil {
		e.LaunchErr = fmt.Sprint(err)
		return e
	}
	ws, ok := ps.Sys().(syscall.WaitStatus)
	if !ok {
		e.LaunchErr = "unknown wait status"
		return e
	}
	if ws.Exited() {
		e.Code = ws.ExitStatus()
	}
	if ws.Signaled() {
		e.Signal = int(ws.Signal())
	}
	return e
}

func (c *OwnedChild) waitOnce() error {
	if c.state == stateWaited {
		return ErrState
	}
	err := c.cmd.Wait()
	c.state = stateWaited
	c.exit = exitFromWait(c.cmd.ProcessState, err)
	return err
}

func (c *OwnedChild) waitInertOnce() error {
	if c.activated {
		return ErrState
	}
	return c.waitOnce()
}

func (c *OwnedChild) finishInertAfterExit() (Exit, error) {
	if c == nil || c.activated || !c.exitObserved || c.state != stateExited {
		return Exit{}, ErrState
	}
	waitErr := c.waitInertOnce()
	if c.state != stateWaited {
		return Exit{}, waitErr
	}
	c.exit.Aborted = true
	return c.exit, nil
}

func (c *OwnedChild) waitActivatedOnce() error {
	if !c.activated || !c.exitObserved {
		return ErrState
	}
	if c.testConvergence != nil {
		if err := c.testConvergence(); err != nil {
			return err
		}
	}
	if err := killRemainingGroup(c.identity); err != nil {
		return err
	}
	return c.waitOnce()
}

func (c *OwnedChild) Terminate(timeout time.Duration) (Exit, error) {
	if c == nil || !c.activated || c.state == stateWaited || !c.identity.Valid() {
		return Exit{}, ErrState
	}
	if timeout <= 0 {
		timeout = defaultStopTimeout
	}
	if err := signalOwnedGroup(c.identity, unix.SIGTERM); err != nil {
		return Exit{}, fmt.Errorf("%w: TERM: %v", ErrUnresolved, err)
	}
	escalate := false
	if !c.exitObserved {
		_, err := c.waitForExit(timeout)
		if err != nil {
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				return Exit{}, err
			}
			escalate = true
		}
	}
	if !escalate {
		if err := waitForOwnedGroupQuiescence(c.identity, timeout); err != nil {
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				return Exit{}, err
			}
			escalate = true
		}
	}
	if escalate {
		if err := signalOwnedGroup(c.identity, unix.SIGKILL); err != nil {
			return Exit{}, fmt.Errorf("%w: KILL: %v", ErrUnresolved, err)
		}
		if !c.exitObserved {
			if _, err := c.waitForExit(4 * time.Second); err != nil {
				return Exit{}, err
			}
		}
	}
	waitErr := c.waitActivatedOnce()
	if c.state != stateWaited {
		return Exit{}, waitErr
	}
	return c.exit, nil
}

func (c *OwnedChild) waitedExit() (Exit, error) {
	if c == nil || c.state != stateWaited {
		return Exit{}, ErrState
	}
	return c.exit, nil
}

func waitForOwnedGroupQuiescence(leader Identity, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		census, err := censusOwnedGroup(leader)
		if err != nil {
			return err
		}
		if !census.hasLiveMember {
			return nil
		}
		if time.Now().After(deadline) {
			return os.ErrDeadlineExceeded
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func killRemainingGroup(leader Identity) error {
	deadline := time.Now().Add(4 * time.Second)
	for {
		census, err := censusOwnedGroup(leader)
		if err != nil {
			return err
		}
		if census.onlyLeader {
			return nil
		}
		if census.hasLiveMember {
			if err := signalOwnedGroup(leader, unix.SIGKILL); err != nil {
				return err
			}
		}
		if time.Now().After(deadline) {
			return ErrUnresolved
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func signalOwnedGroup(leader Identity, signal unix.Signal) error {
	census, err := censusOwnedGroup(leader)
	if err != nil {
		return err
	}
	if !census.hasLiveMember {
		return nil
	}
	signalErr := unix.Kill(-leader.PGID, signal)
	if signalErr == nil {
		return nil
	}
	noLiveMembers := false
	if errors.Is(signalErr, unix.ESRCH) {
		census, err = censusOwnedGroup(leader)
		if err != nil {
			return err
		}
		noLiveMembers = !census.hasLiveMember
	}
	return classifyGroupSignal(signalErr, noLiveMembers)
}

func classifyGroupSignal(signalErr error, exactCensusHasNoLiveMembers bool) error {
	if signalErr == nil || errors.Is(signalErr, unix.ESRCH) && exactCensusHasNoLiveMembers {
		return nil
	}
	return fmt.Errorf("%w: group signal: %v", ErrUnresolved, signalErr)
}

type ownedGroupCensus struct {
	onlyLeader    bool
	hasLiveMember bool
}

const darwinZombieState = 5

func censusOwnedGroup(leader Identity) (ownedGroupCensus, error) {
	observed, err := readIdentity(leader.PID)
	if err != nil || observed != leader {
		return ownedGroupCensus{}, fmt.Errorf("%w: direct leader identity changed during group census", ErrUnresolved)
	}
	members, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", leader.PGID)
	if err != nil {
		return ownedGroupCensus{}, fmt.Errorf("%w: group census: %v", ErrUnresolved, err)
	}
	foundLeader := false
	hasDescendant := false
	hasLiveMember := false
	for _, member := range members {
		identity := Identity{PID: int(member.Proc.P_pid), PGID: int(member.Eproc.Pgid), Birth: Birth{Seconds: member.Proc.P_starttime.Sec, Microseconds: member.Proc.P_starttime.Usec}}
		if identity.PID == leader.PID {
			if identity != leader {
				return ownedGroupCensus{}, fmt.Errorf("%w: leader changed during group census", ErrUnresolved)
			}
			foundLeader = true
			if member.Proc.P_stat != darwinZombieState {
				hasLiveMember = true
			}
			continue
		}
		hasDescendant = true
		if member.Proc.P_stat != darwinZombieState {
			hasLiveMember = true
		}
	}
	if !foundLeader {
		return ownedGroupCensus{}, fmt.Errorf("%w: exact leader missing from group census", ErrUnresolved)
	}
	return ownedGroupCensus{onlyLeader: !hasDescendant, hasLiveMember: hasLiveMember}, nil
}

func (c *OwnedChild) Close() error {
	if c == nil {
		return nil
	}
	if c.state != stateWaited {
		if c.activated {
			if _, err := c.Terminate(defaultStopTimeout); err != nil {
				return err
			}
		} else if c.state == stateBlocked {
			if err := c.Abort(); err != nil {
				return err
			}
		} else if c.state == stateExited {
			if err := c.waitInertOnce(); err != nil {
				return err
			}
		} else {
			return ErrState
		}
	}
	if c.activation != nil {
		_ = c.activation.Close()
	}
	if c.status != nil {
		_ = c.status.Close()
	}
	if c.kq >= 0 {
		_ = unix.Close(c.kq)
		c.kq = -1
	}
	return nil
}
func (c *OwnedChild) hardCleanup() error {
	if c == nil || c.cmd == nil {
		return nil
	}
	var cleanupErr error
	if err := c.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		cleanupErr = errors.Join(cleanupErr, err)
	}
	if c.exitRegistered {
		if _, err := c.waitForExit(4 * time.Second); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	if c.state != stateWaited {
		if err := c.waitInertOnce(); err != nil && c.state != stateWaited {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	if c.activation != nil {
		_ = c.activation.Close()
	}
	if c.status != nil {
		_ = c.status.Close()
	}
	if c.kq >= 0 {
		_ = unix.Close(c.kq)
		c.kq = -1
	}
	return cleanupErr
}

func registerExit(kq, pid int) error {
	change := unix.Kevent_t{Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ENABLE | unix.EV_ONESHOT | unix.EV_RECEIPT, Fflags: unix.NOTE_EXIT}
	events := make([]unix.Kevent_t, 1)
	n, err := unix.Kevent(kq, []unix.Kevent_t{change}, events, nil)
	if err != nil {
		return err
	}
	if n != 1 || events[0].Flags&unix.EV_ERROR == 0 {
		return ErrIdentity
	}
	if events[0].Data != 0 {
		return unix.Errno(events[0].Data)
	}
	return nil
}

func readIdentity(pid int) (Identity, error) {
	if pid <= 1 {
		return Identity{}, ErrIdentity
	}
	p, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return Identity{}, err
	}
	id := Identity{PID: pid, PGID: int(p.Eproc.Pgid), Birth: Birth{Seconds: p.Proc.P_starttime.Sec, Microseconds: p.Proc.P_starttime.Usec}}
	if !id.Valid() || int(p.Proc.P_pid) != pid {
		return Identity{}, ErrIdentity
	}
	return id, nil
}

func ObserveProcess(want Identity) Observation {
	if !want.Valid() {
		return Observation{Presence: Unknown, Err: ErrIdentity}
	}
	got, err := readIdentity(want.PID)
	if err == nil {
		if got == want {
			return Observation{Presence: Present}
		}
		return Observation{Presence: Reused}
	}
	pzero := unix.Kill(want.PID, 0)
	gzero := unix.Kill(-want.PGID, 0)
	return classifyUnavailable(err, pzero, gzero)
}

func classifyUnavailable(infoErr, processProbe, groupProbe error) Observation {
	if (errors.Is(infoErr, unix.EIO) || errors.Is(infoErr, unix.ESRCH) || errors.Is(infoErr, unix.ENOENT)) && errors.Is(processProbe, unix.ESRCH) && errors.Is(groupProbe, unix.ESRCH) {
		return Observation{Presence: Absent}
	}
	return Observation{Presence: Unknown, Err: fmt.Errorf("%w: info=%v pid=%v group=%v", ErrUnresolved, infoErr, processProbe, groupProbe)}
}

func ObserveProcessGroup(want Identity) Observation {
	base := ObserveProcess(want)
	if base.Presence != Present {
		return base
	}
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", want.PGID)
	if err != nil {
		return Observation{Presence: Unknown, Err: err}
	}
	members := make([]Identity, 0, len(procs))
	for _, p := range procs {
		id := Identity{PID: int(p.Proc.P_pid), PGID: int(p.Eproc.Pgid), Birth: Birth{Seconds: p.Proc.P_starttime.Sec, Microseconds: p.Proc.P_starttime.Usec}}
		if !id.Valid() {
			return Observation{Presence: Unknown, Err: ErrIdentity}
		}
		members = append(members, id)
	}
	return Observation{Presence: Present, Members: members}
}

func RunExecGate() error {
	config := os.NewFile(3, "gate-config")
	leash := os.NewFile(4, "gate-leash")
	status := os.NewFile(5, "gate-status")
	stdin := os.NewFile(6, "target-stdin")
	stdout := os.NewFile(7, "target-stdout")
	stderr := os.NewFile(8, "target-stderr")
	leaseDir := os.NewFile(9, "lease-dir")
	lifetime := os.NewFile(10, "runtime-lifetime")
	for _, f := range []*os.File{config, leash, status, stdin, stdout, stderr, leaseDir, lifetime} {
		if f == nil {
			return fmt.Errorf("runner: missing inherited capability")
		}
	}
	var cfg gateConfig
	if err := readFrame(io.LimitReader(config, maxConfigBytes+4), &cfg, maxConfigBytes); err != nil {
		return err
	}
	_ = config.Close()
	if cfg.Version != 1 || cfg.MarkerName == "" || filepath.Base(cfg.MarkerName) != cfg.MarkerName {
		return fmt.Errorf("runner: invalid gate config")
	}
	if got, err := commitOpen(leaseDir, cfg.LeaseDirectory.Path, false); err != nil || got.FileIdentity != cfg.LeaseDirectory.FileIdentity || got.UID != cfg.LeaseDirectory.UID || got.GID != cfg.LeaseDirectory.GID || got.Mode != cfg.LeaseDirectory.Mode {
		return fmt.Errorf("runner: lease directory: %w", ErrIdentity)
	}
	if got, err := commitRuntimeLifetime(leaseDir, lifetime); err != nil || got != cfg.Lifetime {
		return fmt.Errorf("runner: runtime lifetime: %w", errors.Join(ErrIdentity, err))
	}
	var control *os.File
	if cfg.Control != nil {
		control = os.NewFile(11, "target-control")
		if control == nil || verifyControl(control, *cfg.Control) != nil {
			return fmt.Errorf("runner: control capability: %w", ErrIdentity)
		}
	}
	target, err := verifyCommit(cfg.Target.Executable, true)
	if err != nil {
		return fmt.Errorf("runner: prepared executable: %w", err)
	}
	_ = target.Close()
	cwd, err := verifyCommit(cfg.Target.Cwd, false)
	if err != nil {
		return fmt.Errorf("runner: prepared cwd: %w", err)
	}
	defer cwd.Close()
	id, err := readIdentity(os.Getpid())
	if err != nil {
		return err
	}
	if err := writeFrame(status, gateFrame{Kind: "ready", Identity: id}, maxFrameBytes); err != nil {
		return err
	}
	var activation gateFrame
	if err := readFrame(leash, &activation, maxFrameBytes); err != nil {
		if errors.Is(err, io.EOF) {
			_ = writeFrame(status, gateFrame{Kind: "aborted"}, maxFrameBytes)
			return nil
		}
		return err
	}
	_ = leash.Close()
	if activation.Kind != "activate" || activation.Marker == nil {
		return fmt.Errorf("runner: invalid activation")
	}
	var marker unix.Stat_t
	if err := unix.Fstatat(int(leaseDir.Fd()), cfg.MarkerName, &marker, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if marker.Mode&unix.S_IFMT != unix.S_IFREG || marker.Mode&0o777 != 0o600 || uint64(marker.Dev) != activation.Marker.Device || marker.Ino != activation.Marker.Inode {
		return ErrIdentity
	}
	target, err = verifyCommit(cfg.Target.Executable, true)
	if err != nil {
		_ = writeFrame(status, gateFrame{Kind: "launch-error", Error: err.Error()}, maxFrameBytes)
		return err
	}
	if err := sameNamedIdentity(cfg.Target.Executable.Path, cfg.Target.Executable.FileIdentity); err != nil {
		target.Close()
		_ = writeFrame(status, gateFrame{Kind: "launch-error", Error: err.Error()}, maxFrameBytes)
		return err
	}
	if cfg.TestFinalCheck {
		// This package-test-only barrier makes the documented cooperative
		// same-UID pathname race measurable; it is not a production defense.
		seamFD := uintptr(11)
		if control != nil {
			seamFD = 12
		}
		seam := os.NewFile(seamFD, "test-final-check")
		if seam == nil {
			return ErrIdentity
		}
		if _, err := seam.Write([]byte{'R'}); err != nil {
			return err
		}
		var ack [1]byte
		if _, err := io.ReadFull(seam, ack[:]); err != nil || ack[0] != 'X' {
			return fmt.Errorf("runner: test final-check seam: %v", err)
		}
		_ = seam.Close()
	}
	_ = target.Close()
	if err := unix.Fchdir(int(cwd.Fd())); err != nil {
		return err
	}
	_ = cwd.Close()
	if !cfg.KeepDirectory {
		_ = leaseDir.Close()
	}
	if err := unix.Dup2(int(stdin.Fd()), 0); err != nil {
		return err
	}
	if err := unix.Dup2(int(stdout.Fd()), 1); err != nil {
		return err
	}
	if err := unix.Dup2(int(stderr.Fd()), 2); err != nil {
		return err
	}
	if control != nil {
		if err := unix.Dup2(int(control.Fd()), 3); err != nil {
			return err
		}
	}
	for _, fd := range []int{4, 6, 7, 8, 11, 12} {
		_ = unix.Close(fd)
	}
	if control == nil {
		_ = unix.Close(3)
	}
	unix.CloseOnExec(5)
	if !cfg.KeepDirectory {
		_ = unix.Close(9)
	}
	if _, err := unix.FcntlInt(lifetime.Fd(), unix.F_SETFD, 0); err != nil {
		return fmt.Errorf("runner: retain runtime lifetime: %w", err)
	}
	if err := unix.Exec(cfg.Target.Executable.Path, cfg.Target.Argv, cfg.Target.Env); err != nil {
		_ = writeFrame(status, gateFrame{Kind: "launch-error", Error: err.Error()}, maxFrameBytes)
		return err
	}
	return nil
}
