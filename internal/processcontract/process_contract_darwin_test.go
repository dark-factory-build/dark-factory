//go:build darwin

package processcontract

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const (
	helperEnv     = "DARK_FACTORY_PROCESS_CONTRACT_HELPER"
	helperModeEnv = "DARK_FACTORY_PROCESS_CONTRACT_MODE"
	ioTimeout     = 4 * time.Second
	exitTimeout   = 5 * time.Second
)

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		os.Exit(m.Run())
	}
	beforeFDs, err := fdSnapshot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "processcontract initial FD census: %v\n", err)
		os.Exit(1)
	}
	beforeGoroutines := runtime.NumGoroutine()
	code := m.Run()
	afterFDs, fdErr := fdSnapshot()
	afterGoroutines := runtime.NumGoroutine()
	if code == 0 && fdErr != nil {
		fmt.Fprintf(os.Stderr, "processcontract final FD census: %v\n", fdErr)
		code = 1
	}
	if code == 0 && !sameFDs(beforeFDs, afterFDs) {
		fmt.Fprintf(os.Stderr, "processcontract FD leak: before=%v after=%v\n", beforeFDs, afterFDs)
		code = 1
	}
	if code == 0 && afterGoroutines > beforeGoroutines {
		fmt.Fprintf(os.Stderr, "processcontract goroutine leak: before=%d after=%d\n", beforeGoroutines, afterGoroutines)
		code = 1
	}
	os.Exit(code)
}

type fdIdentity struct {
	Device uint64
	Inode  uint64
	Mode   uint32
}

func fdSnapshot() (map[int]fdIdentity, error) {
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		return nil, err
	}
	fds := make(map[int]fdIdentity)
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err == nil {
			fds[fd] = fdIdentity{Device: uint64(st.Dev), Inode: st.Ino, Mode: uint32(st.Mode)}
		}
	}
	return fds, nil
}

func sameFDs(a, b map[int]fdIdentity) bool {
	if len(a) != len(b) {
		return false
	}
	for fd, want := range a {
		if got, ok := b[fd]; !ok || got != want {
			return false
		}
	}
	return true
}

type birth struct {
	Sec  int64 `json:"sec"`
	Usec int32 `json:"usec"`
}

type identity struct {
	PID   int   `json:"pid"`
	PGID  int   `json:"pgid"`
	Birth birth `json:"birth"`
}

type markerIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

type helperFrame struct {
	Kind     string   `json:"kind"`
	Identity identity `json:"identity"`
}

func TestProcessContractHelper(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		return
	}
	mode := os.Getenv(helperModeEnv)
	switch mode {
	case "blocked":
		helperReadyThenBlock(3, 4, 0)
	case "exit-23":
		helperReadyThenBlock(3, 4, 23)
	case "exec-gate":
		helperExecGate()
	case "exec-target":
		writeFrameAndExit(5, helperFrame{Kind: "exec-target", Identity: mustSelfIdentity()})
	case "term-ignore":
		helperTermIgnore()
	case "leash-gate":
		helperLeashGate()
	default:
		fmt.Fprintf(os.Stderr, "unknown helper mode %q\n", mode)
		os.Exit(97)
	}
}

func helperReadyThenBlock(readyFD, releaseFD, code int) {
	ready := os.NewFile(uintptr(readyFD), "ready")
	release := os.NewFile(uintptr(releaseFD), "release")
	if ready == nil || release == nil {
		os.Exit(98)
	}
	if _, err := ready.Write([]byte{'R'}); err != nil {
		os.Exit(98)
	}
	_ = ready.Close()
	var b [1]byte
	if _, err := io.ReadFull(release, b[:]); err != nil || b[0] != 'X' {
		os.Exit(98)
	}
	_ = release.Close()
	os.Exit(code)
}

func helperExecGate() {
	activation := os.NewFile(3, "activation")
	ready := os.NewFile(4, "ready")
	if activation == nil || ready == nil {
		os.Exit(98)
	}
	if _, err := ready.Write([]byte{'R'}); err != nil {
		os.Exit(98)
	}
	_ = ready.Close()
	var b [1]byte
	if _, err := io.ReadFull(activation, b[:]); err != nil || b[0] != 'A' {
		os.Exit(98)
	}
	_ = activation.Close()
	env := helperEnvironment("exec-target")
	if err := syscall.Exec(os.Args[0], []string{os.Args[0], "-test.run=^TestProcessContractHelper$"}, env); err != nil {
		os.Exit(99)
	}
}

func helperTermIgnore() {
	ready := os.NewFile(3, "ready")
	ack := os.NewFile(4, "ack")
	if ready == nil || ack == nil {
		os.Exit(98)
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)
	if _, err := ready.Write([]byte{'R'}); err != nil {
		os.Exit(98)
	}
	_ = ready.Close()
	<-ch
	if _, err := ack.Write([]byte{'T'}); err != nil {
		os.Exit(98)
	}
	_ = ack.Close()
	select {}
}

func helperLeashGate() {
	leash := os.NewFile(3, "leash")
	ready := os.NewFile(4, "gate-ready")
	if leash == nil || ready == nil {
		os.Exit(98)
	}
	if _, err := ready.Write([]byte{'G'}); err != nil {
		os.Exit(98)
	}
	_ = ready.Close()
	deadline := time.AfterFunc(exitTimeout, func() { os.Exit(124) })
	var b [1]byte
	_, err := leash.Read(b[:])
	_ = deadline.Stop()
	_ = leash.Close()
	if !errors.Is(err, io.EOF) {
		os.Exit(98)
	}
	markerPath := os.Getenv("DF_MARKER_PATH")
	effectPath := os.Getenv("DF_EFFECT_PATH")
	if markerPath != "" {
		if _, err := os.Lstat(markerPath); err == nil {
			fd, openErr := unix.Open(effectPath, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
			if openErr != nil {
				os.Exit(98)
			}
			_ = unix.Close(fd)
		} else if !errors.Is(err, os.ErrNotExist) {
			os.Exit(98)
		}
	}
	os.Exit(0)
}

func helperEnvironment(mode string) []string {
	return []string{helperEnv + "=1", helperModeEnv + "=" + mode, "TMPDIR=" + os.TempDir()}
}

func mustSelfIdentity() identity {
	id, err := readIdentity(os.Getpid())
	if err != nil {
		os.Exit(98)
	}
	return id
}

func writeFrameAndExit(fd uintptr, f helperFrame) {
	file := os.NewFile(fd, "result")
	if file == nil {
		os.Exit(98)
	}
	writeFrame(file, f)
	_ = file.Close()
	os.Exit(0)
}

func writeFrame(w io.Writer, f helperFrame) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(f); err != nil {
		os.Exit(98)
	}
}

type ownedChild struct {
	cmd    *exec.Cmd
	pid    int
	pgid   int
	kq     int
	waited bool
}

func startHelper(t *testing.T, mode string, pgid int, files []*os.File, extraEnv ...string) *ownedChild {
	t.Helper()
	kq, err := unix.Kqueue()
	if err != nil {
		t.Fatal(err)
	}
	unix.CloseOnExec(kq)
	cmd := exec.Command(os.Args[0], "-test.run=^TestProcessContractHelper$")
	cmd.Env = append(helperEnvironment(mode), extraEnv...)
	cmd.ExtraFiles = files
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: pgid}
	if err := cmd.Start(); err != nil {
		_ = unix.Close(kq)
		t.Fatal(err)
	}
	c := &ownedChild{cmd: cmd, pid: cmd.Process.Pid, pgid: cmd.Process.Pid, kq: kq}
	if pgid != 0 {
		c.pgid = pgid
	}
	if err := registerExit(kq, c.pid); err != nil {
		_ = unix.Kill(c.pid, unix.SIGKILL)
		_, _ = cmd.Process.Wait()
		_ = unix.Close(kq)
		t.Fatalf("register NOTE_EXIT for %d: %v", c.pid, err)
	}
	t.Cleanup(func() {
		if !c.waited {
			_ = unix.Kill(c.pid, unix.SIGKILL)
			_, _ = waitExit(kq, c.pid, exitTimeout)
			_ = c.wait()
		}
		_ = unix.Close(kq)
	})
	return c
}

func (c *ownedChild) wait() error {
	if c.waited {
		return errors.New("child waited more than once")
	}
	c.waited = true
	return c.cmd.Wait()
}

func (c *ownedChild) killGroup(sig unix.Signal) error {
	if c.waited {
		return errors.New("group signal requires unreaped direct-child leader")
	}
	return unix.Kill(-c.pgid, sig)
}

var errExitTimeout = errors.New("NOTE_EXIT timeout")

type receiptError struct {
	Errno unix.Errno
}

func (e receiptError) Error() string {
	return fmt.Sprintf("EV_RECEIPT errno=%d (%v)", e.Errno, e.Errno)
}

func registerExit(kq, pid int) error {
	change := unix.Kevent_t{Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ENABLE | unix.EV_ONESHOT | unix.EV_RECEIPT, Fflags: unix.NOTE_EXIT}
	receipts := make([]unix.Kevent_t, 1)
	n, err := unix.Kevent(kq, []unix.Kevent_t{change}, receipts, nil)
	if err != nil {
		return err
	}
	if n != 1 || receipts[0].Flags&unix.EV_ERROR == 0 {
		return fmt.Errorf("bad EV_RECEIPT n=%d flags=%#x data=%d", n, receipts[0].Flags, receipts[0].Data)
	}
	if receipts[0].Data != 0 {
		return receiptError{Errno: unix.Errno(receipts[0].Data)}
	}
	return nil
}

func waitExit(kq, pid int, timeout time.Duration) (unix.Kevent_t, error) {
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return unix.Kevent_t{}, errExitTimeout
		}
		events := make([]unix.Kevent_t, 1)
		ts := unix.NsecToTimespec(remaining.Nanoseconds())
		n, err := unix.Kevent(kq, nil, events, &ts)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return unix.Kevent_t{}, err
		}
		if n == 0 {
			return unix.Kevent_t{}, errExitTimeout
		}
		ev := events[0]
		if ev.Ident != uint64(pid) || ev.Filter != unix.EVFILT_PROC || ev.Fflags&unix.NOTE_EXIT == 0 || ev.Flags&unix.EV_ERROR != 0 {
			return unix.Kevent_t{}, fmt.Errorf("unexpected NOTE_EXIT event: %+v", ev)
		}
		return ev, nil
	}
}

func readIdentity(pid int) (identity, error) {
	if pid <= 0 {
		return identity{}, errors.New("invalid pid")
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return identity{}, err
	}
	if int(kp.Proc.P_pid) != pid || kp.Eproc.Pgid <= 0 || kp.Proc.P_starttime.Sec <= 0 || kp.Proc.P_starttime.Usec < 0 || kp.Proc.P_starttime.Usec >= 1_000_000 {
		return identity{}, errors.New("malformed kinfo identity")
	}
	return identity{PID: pid, PGID: int(kp.Eproc.Pgid), Birth: birth{Sec: kp.Proc.P_starttime.Sec, Usec: kp.Proc.P_starttime.Usec}}, nil
}

func groupMembers(pgid int) ([]identity, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pgid)
	if err != nil {
		return nil, err
	}
	out := make([]identity, 0, len(procs))
	for _, kp := range procs {
		out = append(out, identity{PID: int(kp.Proc.P_pid), PGID: int(kp.Eproc.Pgid), Birth: birth{Sec: kp.Proc.P_starttime.Sec, Usec: kp.Proc.P_starttime.Usec}})
	}
	return out, nil
}

type classification string

const (
	present classification = "present"
	absent  classification = "absent"
	reused  classification = "reused"
	unknown classification = "unknown"
)

type observations struct {
	Stored           identity
	Current          *identity
	ProcessInfoError error
	ProcessZeroError error
	GroupZeroError   error
}

func classify(o observations) classification {
	if o.Stored.PID <= 0 || o.Stored.PGID <= 0 || o.Stored.Birth.Sec <= 0 || o.Stored.Birth.Usec < 0 || o.Stored.Birth.Usec >= 1_000_000 {
		return unknown
	}
	if o.Current != nil {
		if *o.Current == o.Stored {
			return present
		}
		return reused
	}
	if errors.Is(o.ProcessInfoError, unix.EPERM) || errors.Is(o.ProcessZeroError, unix.EPERM) || errors.Is(o.GroupZeroError, unix.EPERM) {
		return unknown
	}
	// Darwin's kern.proc.pid reports a reaped PID as EIO; that is only absence
	// evidence when both exact PID and exact negative-PGID probes say ESRCH.
	if (errors.Is(o.ProcessInfoError, unix.ESRCH) || errors.Is(o.ProcessInfoError, unix.ENOENT) || errors.Is(o.ProcessInfoError, unix.EIO)) && errors.Is(o.ProcessZeroError, unix.ESRCH) && errors.Is(o.GroupZeroError, unix.ESRCH) {
		return absent
	}
	return unknown
}

func createMarker(dir, name string) (markerIdentity, error) {
	dirfd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return markerIdentity{}, err
	}
	defer unix.Close(dirfd)
	fd, err := unix.Openat(dirfd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return markerIdentity{}, err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return markerIdentity{}, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0o777 != 0o600 || st.Nlink != 1 {
		return markerIdentity{}, fmt.Errorf("unsafe marker mode=%#o nlink=%d", st.Mode, st.Nlink)
	}
	if err := unix.Fsync(fd); err != nil {
		return markerIdentity{}, err
	}
	return markerIdentity{Device: uint64(st.Dev), Inode: st.Ino}, nil
}

func readByte(t *testing.T, f *os.File, want byte) {
	t.Helper()
	if err := f.SetReadDeadline(time.Now().Add(ioTimeout)); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err := io.ReadFull(f, b[:]); err != nil || b[0] != want {
		t.Fatalf("read helper byte want=%q got=%q err=%v", want, b[0], err)
	}
}

func readFrame(t *testing.T, f *os.File) helperFrame {
	t.Helper()
	if err := f.SetReadDeadline(time.Now().Add(ioTimeout)); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bufio.NewReader(f))
	dec.DisallowUnknownFields()
	var frame helperFrame
	if err := dec.Decode(&frame); err != nil {
		t.Fatal(err)
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatalf("helper frame had trailing data: %v", err)
	}
	if frame.Kind == "" {
		t.Fatal("empty helper frame kind")
	}
	return frame
}

func pipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	return r, w
}

func TestBirthAndPGIDStableAcrossExec(t *testing.T) {
	activationR, activationW := pipe(t)
	readyR, readyW := pipe(t)
	resultR, resultW := pipe(t)
	c := startHelper(t, "exec-gate", 0, []*os.File{activationR, readyW, resultW})
	_ = activationR.Close()
	_ = readyW.Close()
	_ = resultW.Close()
	readByte(t, readyR, 'R')
	before, err := readIdentity(c.pid)
	if err != nil {
		t.Fatal(err)
	}
	if before.PGID != c.pid {
		t.Fatalf("explicit group not established: %+v", before)
	}
	if before.Birth.Sec <= 0 || before.Birth.Usec < 0 || before.Birth.Usec >= 1_000_000 {
		t.Fatalf("birth fingerprint was not populated: %+v", before.Birth)
	}
	if _, err := activationW.Write([]byte{'A'}); err != nil {
		t.Fatal(err)
	}
	_ = activationW.Close()
	frame := readFrame(t, resultR)
	if frame.Kind != "exec-target" || frame.Identity != before {
		t.Fatalf("identity changed across exec: before=%+v frame=%+v", before, frame)
	}
	if _, err := waitExit(c.kq, c.pid, exitTimeout); err != nil {
		t.Fatal(err)
	}
	if after, err := readIdentity(c.pid); err != nil || after != before {
		t.Fatalf("unreaped exec target not exact: after=%+v err=%v", after, err)
	}
	if err := c.wait(); err != nil {
		t.Fatal(err)
	}
}

func TestUnreapedIdentityAndTimeout(t *testing.T) {
	readyR, readyW := pipe(t)
	releaseR, releaseW := pipe(t)
	c := startHelper(t, "exit-23", 0, []*os.File{readyW, releaseR})
	_ = readyW.Close()
	_ = releaseR.Close()
	readByte(t, readyR, 'R')
	want, err := readIdentity(c.pid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := waitExit(c.kq, c.pid, 20*time.Millisecond); !errors.Is(err, errExitTimeout) {
		t.Fatalf("wanted explicit timeout, got %v", err)
	}
	if got, err := readIdentity(c.pid); err != nil || got != want {
		t.Fatalf("timeout masqueraded as absence: got=%+v err=%v", got, err)
	}
	_, _ = releaseW.Write([]byte{'X'})
	_ = releaseW.Close()
	if _, err := waitExit(c.kq, c.pid, exitTimeout); err != nil {
		t.Fatal(err)
	}
	if got, err := readIdentity(c.pid); err != nil || got != want {
		t.Fatalf("zombie identity changed: got=%+v err=%v", got, err)
	}
	err = c.wait()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 23 {
		t.Fatalf("want exit 23, got %v", err)
	}
	_, infoErr := readIdentity(c.pid)
	pzero := unix.Kill(c.pid, 0)
	gzero := unix.Kill(-c.pgid, 0)
	if got := classify(observations{Stored: want, ProcessInfoError: infoErr, ProcessZeroError: pzero, GroupZeroError: gzero}); got != absent {
		t.Fatalf("post-Wait classification=%s info=%v pid0=%v group0=%v", got, infoErr, pzero, gzero)
	}
}

func TestKqueueLateRegistrationOnKnownZombie(t *testing.T) {
	readyR, readyW := pipe(t)
	releaseR, releaseW := pipe(t)
	// startHelper does not return until EV_RECEIPT has acknowledged the first
	// NOTE_EXIT registration. The blocked child cannot exit before release.
	c := startHelper(t, "blocked", 0, []*os.File{readyW, releaseR})
	_ = readyW.Close()
	_ = releaseR.Close()
	readByte(t, readyR, 'R')
	want, err := readIdentity(c.pid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := releaseW.Write([]byte{'X'}); err != nil {
		t.Fatal(err)
	}
	_ = releaseW.Close()
	if _, err := waitExit(c.kq, c.pid, exitTimeout); err != nil {
		t.Fatal(err)
	}
	if got, err := readIdentity(c.pid); err != nil || got != want {
		t.Fatalf("first NOTE_EXIT did not leave exact zombie: got=%+v err=%v", got, err)
	}

	lateKQ, err := unix.Kqueue()
	if err != nil {
		t.Fatal(err)
	}
	err = registerExit(lateKQ, c.pid)
	var receiptErr receiptError
	if !errors.As(err, &receiptErr) || receiptErr.Errno != unix.ESRCH {
		_ = unix.Close(lateKQ)
		t.Fatalf("known-zombie registration unexpectedly succeeded or changed error: %v", err)
	}
	if err := unix.Close(lateKQ); err != nil {
		t.Fatal(err)
	}
	if err := c.wait(); err != nil {
		t.Fatal(err)
	}
	_, infoErr := readIdentity(c.pid)
	pzero := unix.Kill(c.pid, 0)
	gzero := unix.Kill(-c.pgid, 0)
	if !errors.Is(pzero, unix.ESRCH) || !errors.Is(gzero, unix.ESRCH) {
		t.Fatalf("reaped child was not absent: info=%v pid0=%v group0=%v", infoErr, pzero, gzero)
	}
	if got := classify(observations{Stored: want, ProcessInfoError: infoErr, ProcessZeroError: pzero, GroupZeroError: gzero}); got != absent {
		t.Fatalf("post-Wait classification=%s info=%v pid0=%v group0=%v", got, infoErr, pzero, gzero)
	}
}

func TestLeaderExitBeforeDescendantAndGroupKill(t *testing.T) {
	lReadyR, lReadyW := pipe(t)
	lReleaseR, lReleaseW := pipe(t)
	leader := startHelper(t, "blocked", 0, []*os.File{lReadyW, lReleaseR})
	_ = lReadyW.Close()
	_ = lReleaseR.Close()
	readByte(t, lReadyR, 'R')
	dReadyR, dReadyW := pipe(t)
	dReleaseR, _ := pipe(t)
	desc := startHelper(t, "blocked", leader.pgid, []*os.File{dReadyW, dReleaseR})
	_ = dReadyW.Close()
	_ = dReleaseR.Close()
	readByte(t, dReadyR, 'R')
	descID, err := readIdentity(desc.pid)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = lReleaseW.Write([]byte{'X'})
	_ = lReleaseW.Close()
	if _, err := waitExit(leader.kq, leader.pid, exitTimeout); err != nil {
		t.Fatal(err)
	}
	members, err := groupMembers(leader.pgid)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, member := range members {
		if member == descID {
			found = true
		}
	}
	if !found {
		t.Fatalf("descendant absent from group: %+v", members)
	}
	if err := leader.killGroup(unix.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if _, err := waitExit(desc.kq, desc.pid, exitTimeout); err != nil {
		t.Fatal(err)
	}
	if err := desc.wait(); err == nil {
		t.Fatal("SIGKILL descendant exited successfully")
	}
	if err := leader.wait(); err != nil {
		t.Fatal(err)
	}
	if err := unix.Kill(-leader.pgid, 0); !errors.Is(err, unix.ESRCH) {
		t.Fatalf("group survives cleanup: %v", err)
	}
}

func TestTermIgnoringGroupEscalates(t *testing.T) {
	readyR, readyW := pipe(t)
	ackR, ackW := pipe(t)
	c := startHelper(t, "term-ignore", 0, []*os.File{readyW, ackW})
	_ = readyW.Close()
	_ = ackW.Close()
	readByte(t, readyR, 'R')
	want, err := readIdentity(c.pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.killGroup(unix.SIGTERM); err != nil {
		t.Fatal(err)
	}
	readByte(t, ackR, 'T')
	if got, err := readIdentity(c.pid); err != nil || got != want {
		t.Fatalf("TERM acknowledgement was not live evidence: %+v %v", got, err)
	}
	if err := c.killGroup(unix.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if _, err := waitExit(c.kq, c.pid, exitTimeout); err != nil {
		t.Fatal(err)
	}
	if err := c.wait(); err == nil {
		t.Fatal("SIGKILL helper exited successfully")
	}
	if err := unix.Kill(-c.pgid, 0); !errors.Is(err, unix.ESRCH) {
		t.Fatalf("group survives: %v", err)
	}
}

func TestCreateOnlyMarker(t *testing.T) {
	dir := t.TempDir()
	marker, err := createMarker(dir, "activate")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "activate")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := createMarker(dir, "activate"); !errors.Is(err, unix.EEXIST) {
		t.Fatalf("second marker create=%v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	bst, ast := before.Sys().(*syscall.Stat_t), after.Sys().(*syscall.Stat_t)
	if marker != (markerIdentity{Device: uint64(bst.Dev), Inode: bst.Ino}) || bst.Dev != ast.Dev || bst.Ino != ast.Ino || after.Size() != 0 {
		t.Fatal("marker replaced or truncated")
	}
	if err := os.Symlink("elsewhere", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := createMarker(dir, "link"); err == nil {
		t.Fatal("O_NOFOLLOW did not reject symlink")
	}
}

func TestParentDeathBeforeReleaseAndMarkerRace(t *testing.T) {
	t.Run("before-marker", func(t *testing.T) { runOwnerDeath(t, false) })
	t.Run("after-marker", func(t *testing.T) { runOwnerDeath(t, true) })
}

func runOwnerDeath(t *testing.T, create bool) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "activate")
	effect := filepath.Join(dir, "effect")
	leashR, leashW := pipe(t)
	gateReadyR, gateReadyW := pipe(t)
	gate := startHelper(t, "leash-gate", 0, []*os.File{leashR, gateReadyW}, "DF_MARKER_PATH="+marker, "DF_EFFECT_PATH="+effect)
	_ = leashR.Close()
	_ = gateReadyW.Close()
	ownerReadyR, ownerReadyW := pipe(t)
	ownerReleaseR, _ := pipe(t)
	// The owner holds the only leash writer. The test remains the independent
	// supervisor and therefore has sole Wait authority for both direct children.
	owner := startHelper(t, "blocked", 0, []*os.File{ownerReadyW, ownerReleaseR, leashW})
	_ = ownerReadyW.Close()
	_ = ownerReleaseR.Close()
	_ = leashW.Close()
	readByte(t, gateReadyR, 'G')
	readByte(t, ownerReadyR, 'R')
	if create {
		created, err := createMarker(dir, filepath.Base(marker))
		if err != nil {
			t.Fatal(err)
		}
		st, err := os.Stat(marker)
		if err != nil {
			t.Fatal(err)
		}
		sys := st.Sys().(*syscall.Stat_t)
		if created != (markerIdentity{Device: uint64(sys.Dev), Inode: sys.Ino}) {
			t.Fatal("recorded marker identity mismatch")
		}
	}
	if err := unix.Kill(owner.pid, unix.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if _, err := waitExit(owner.kq, owner.pid, exitTimeout); err != nil {
		t.Fatal(err)
	}
	if err := owner.wait(); err == nil {
		t.Fatal("killed owner exited successfully")
	}
	if _, err := waitExit(gate.kq, gate.pid, 3*time.Second); err != nil {
		t.Fatalf("gate %d did not abort promptly after owner death: %v", gate.pid, err)
	}
	if err := gate.wait(); err != nil {
		t.Fatalf("gate did not exit cleanly on leash EOF: %v", err)
	}
	if _, err := readIdentity(gate.pid); err == nil {
		t.Fatal("reaped gate remained present")
	}
	_, effectErr := os.Stat(effect)
	if create && effectErr != nil {
		t.Fatalf("committed marker did not yield effect: %v", effectErr)
	}
	if !create && !errors.Is(effectErr, os.ErrNotExist) {
		t.Fatalf("effect without marker: %v", effectErr)
	}
}

func TestKqueueWatcherWakeAndJoin(t *testing.T) {
	readyR, readyW := pipe(t)
	releaseR, _ := pipe(t)
	c := startHelper(t, "blocked", 0, []*os.File{readyW, releaseR})
	_ = readyW.Close()
	_ = releaseR.Close()
	readByte(t, readyR, 'R')
	kq, err := unix.Kqueue()
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(kq)
	changes := []unix.Kevent_t{
		{Ident: uint64(c.pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ENABLE, Fflags: unix.NOTE_EXIT},
		{Ident: 1, Filter: unix.EVFILT_USER, Flags: unix.EV_ADD | unix.EV_CLEAR},
	}
	if _, err := unix.Kevent(kq, changes, nil, nil); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		events := make([]unix.Kevent_t, 1)
		_, err := unix.Kevent(kq, nil, events, nil)
		done <- err
	}()
	trigger := unix.Kevent_t{Ident: 1, Filter: unix.EVFILT_USER, Fflags: unix.NOTE_TRIGGER}
	if _, err := unix.Kevent(kq, []unix.Kevent_t{trigger}, nil, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(ioTimeout):
		t.Fatal("watcher did not join")
	}
	if err := unix.Kill(c.pid, unix.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if _, err := waitExit(c.kq, c.pid, exitTimeout); err != nil {
		t.Fatal(err)
	}
	if err := c.wait(); !exitedBySignal(err, unix.SIGKILL) {
		t.Fatalf("want SIGKILL ProcessState from sole Wait, got %v", err)
	}
}

func exitedBySignal(err error, sig unix.Signal) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ProcessState == nil {
		return false
	}
	status, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus)
	return ok && status.Signaled() && status.Signal() == syscall.Signal(sig)
}

func TestObservationClassifier(t *testing.T) {
	want := identity{PID: 222, PGID: 222, Birth: birth{Sec: 10, Usec: 20}}
	other := want
	other.Birth.Usec++
	tests := []struct {
		name string
		in   observations
		want classification
	}{
		{"present", observations{Stored: want, Current: &want}, present},
		{"reused", observations{Stored: want, Current: &other}, reused},
		{"malformed-pid", observations{Stored: identity{}}, unknown},
		{"malformed-birth", observations{Stored: identity{PID: 2, PGID: 2, Birth: birth{Sec: 1, Usec: 1_000_000}}}, unknown},
		{"eperm", observations{Stored: want, ProcessInfoError: unix.EPERM, ProcessZeroError: unix.EPERM, GroupZeroError: unix.ESRCH}, unknown},
		{"partial-esrch", observations{Stored: want, ProcessInfoError: unix.ESRCH, ProcessZeroError: unix.ESRCH}, unknown},
		{"absent", observations{Stored: want, ProcessInfoError: unix.ESRCH, ProcessZeroError: unix.ESRCH, GroupZeroError: unix.ESRCH}, absent},
		{"darwin-sysctl-eio-absent", observations{Stored: want, ProcessInfoError: unix.EIO, ProcessZeroError: unix.ESRCH, GroupZeroError: unix.ESRCH}, absent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classify(tt.in); got != tt.want {
				t.Fatalf("got %s want %s", got, tt.want)
			}
		})
	}
}

func TestKnownOpenFDSelfTestAndLocalCensus(t *testing.T) {
	baseline := runtime.NumGoroutine()
	path := filepath.Join(t.TempDir(), "known-open")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	want := st.Sys().(*syscall.Stat_t)
	found := false
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		var got unix.Stat_t
		if unix.Fstat(fd, &got) == nil && got.Dev == want.Dev && got.Ino == want.Ino {
			found = true
		}
	}
	if !found {
		t.Fatal("known-open FD census did not detect fixture")
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if got := runtime.NumGoroutine(); got > baseline {
		t.Fatalf("goroutine census grew: before=%d after=%d", baseline, got)
	}
}

func TestHelperEnvironmentIsClosed(t *testing.T) {
	for _, value := range helperEnvironment("blocked") {
		if strings.HasPrefix(value, "HOME=") || strings.HasPrefix(value, "DARK_FACTORY_HOME=") {
			t.Fatalf("live path leaked: %s", value)
		}
	}
}
