//go:build darwin

package runner

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestFinishAfterExitAcceptsDeadGroupPeer(t *testing.T) {
	f := newFixture(t)
	child, release := startControlledNaturalExit(t, f, 23)
	installOwnedChildSafetyCleanup(t, child)

	peer := exec.Command("/usr/bin/true")
	peer.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: child.Identity().PGID}
	if err := peer.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = peer.Process.Wait() })
	waitForZombieProcess(t, peer.Process.Pid, child.Identity().PGID)
	if _, err := release.Write([]byte("exit\n")); err != nil {
		t.Fatal(err)
	}
	if err := waitOwnedGroupWithoutLiveMembersBounded(child.Identity(), time.Second); err != nil {
		t.Fatal(err)
	}
	census, err := censusOwnedGroup(child.Identity())
	if err != nil || census.hasLiveMember || census.onlyLeader {
		t.Fatalf("dead-peer census=%+v err=%v", census, err)
	}

	started := time.Now()
	exit, err := child.FinishAfterExit(time.Second)
	if err != nil || exit.Code != 23 || exit.Signal != 0 {
		t.Fatalf("dead peer blocked exact wait: exit=%+v err=%v", exit, err)
	}
	if elapsed := time.Since(started); elapsed < groupSignalSettle {
		t.Fatalf("dead peer converged without a stable census: %s", elapsed)
	}
	assertWaitedAndAbsent(t, child)
}

func waitForZombieProcess(t *testing.T, pid, pgid int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		process, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
		if err == nil && int(process.Eproc.Pgid) == pgid && process.Proc.P_stat == darwinZombieState {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("process %d did not become a zombie in group %d", pid, pgid)
}

func TestTerminateConvergesQueuedNaturalExit(t *testing.T) {
	f := newFixture(t)
	child := f.start("/bin/sh", []string{"-c", "exit 23"}, nil, outputFile(t, filepath.Join(f.root, "out")))
	if _, err := child.Activate(); err != nil {
		t.Fatal(err)
	}
	installOwnedChildSafetyCleanup(t, child)
	waitOwnedGroupWithoutLiveMembers(t, child.Identity())

	exit, err := child.Terminate(time.Second)
	if err != nil || exit.Code != 23 || exit.Signal != 0 {
		t.Fatalf("queued natural exit=%+v err=%v", exit, err)
	}
	assertWaitedAndAbsent(t, child)
	if _, err := child.Terminate(time.Second); !errors.Is(err, ErrState) {
		t.Fatalf("second termination performed work: %v", err)
	}
}

func TestTerminateDischargesEPERMOnlyAfterExactNaturalExit(t *testing.T) {
	f := newFixture(t)
	child, release := startControlledNaturalExit(t, f, 23)
	installOwnedChildSafetyCleanup(t, child)
	var calls atomic.Int32
	var setupErr error
	child.testSignal = func(signal unix.Signal) error {
		calls.Add(1)
		if signal != unix.SIGTERM {
			return fmt.Errorf("unexpected signal %d", signal)
		}
		var signalErr error
		signalErr, setupErr = naturalExitThenEPERM(child, release)
		return signalErr
	}

	exit, err := child.Terminate(time.Second)
	if setupErr != nil {
		t.Fatalf("race setup: %v", setupErr)
	}
	if err != nil || exit.Code != 23 || exit.Signal != 0 {
		t.Fatalf("exact exit did not discharge EPERM: exit=%+v err=%v", exit, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("group signal calls=%d want=1", got)
	}
	assertWaitedAndAbsent(t, child)
}

func TestTerminateRetainsLiveOwnerAfterEPERMWithoutExit(t *testing.T) {
	f := newFixture(t)
	ready := filepath.Join(f.root, "ready")
	// Bounded by the fixture root (t.TempDir removes it on every exit path,
	// panic included) rather than by cleanup code that a failing path could
	// skip. It stays alive across the EPERM termination it witnesses.
	child := f.start("/bin/sh", []string{"-c", fmt.Sprintf("trap '' TERM; printf ready > %q; while test -d %q; do sleep 0.1; done", ready, f.root)}, nil, outputFile(t, filepath.Join(f.root, "out")))
	if _, err := child.Activate(); err != nil {
		t.Fatal(err)
	}
	installOwnedChildSafetyCleanup(t, child)
	waitFile(t, ready)
	child.testSignal = func(signal unix.Signal) error {
		if signal != unix.SIGTERM {
			return fmt.Errorf("unexpected signal %d", signal)
		}
		return classifyGroupSignal(unix.EPERM, false)
	}

	if _, err := child.Terminate(25 * time.Millisecond); !errors.Is(err, ErrUnresolved) || !strings.Contains(fmt.Sprint(err), unix.EPERM.Error()) {
		t.Fatalf("live EPERM termination=%v", err)
	}
	if child.state != stateActivated || child.exitObserved || child.cmd.ProcessState != nil {
		t.Fatalf("live EPERM lost ownership: state=%d observed=%t process_state=%v", child.state, child.exitObserved, child.cmd.ProcessState)
	}
	if observation := ObserveProcess(child.Identity()); observation.Presence != Present {
		t.Fatalf("live EPERM child presence=%+v", observation)
	}

	child.testSignal = nil
	exit, err := child.Terminate(50 * time.Millisecond)
	if err != nil || exit.Signal != int(unix.SIGKILL) {
		t.Fatalf("scoped safety cleanup exit=%+v err=%v", exit, err)
	}
	assertWaitedAndAbsent(t, child)
}

func TestTerminatePreservesEPERMAfterExitWhenConvergenceFails(t *testing.T) {
	f := newFixture(t)
	child, release := startControlledNaturalExit(t, f, 23)
	installOwnedChildSafetyCleanup(t, child)
	identity := child.Identity()
	var setupErr error
	child.testSignal = func(signal unix.Signal) error {
		if signal != unix.SIGTERM {
			return fmt.Errorf("unexpected signal %d", signal)
		}
		var signalErr error
		signalErr, setupErr = naturalExitThenEPERM(child, release)
		return signalErr
	}
	child.testConvergence = func() error {
		return fmt.Errorf("%w: injected group convergence failure", ErrUnresolved)
	}

	if _, err := child.Terminate(time.Second); !errors.Is(err, ErrUnresolved) || !strings.Contains(fmt.Sprint(err), unix.EPERM.Error()) {
		t.Fatalf("convergence failure lost EPERM evidence: %v", err)
	}
	if setupErr != nil {
		t.Fatalf("race setup: %v", setupErr)
	}
	if child.state != stateExited || !child.exitObserved || child.cmd.ProcessState != nil {
		t.Fatalf("failed convergence invented Wait: state=%d observed=%t process_state=%v", child.state, child.exitObserved, child.cmd.ProcessState)
	}
	if observation := ObserveProcess(identity); observation.Presence != Present {
		t.Fatalf("failed convergence lost exact unreaped child: %+v", observation)
	}

	child.testSignal = nil
	child.testConvergence = nil
	exit, err := child.FinishAfterExit(time.Second)
	if err != nil || exit.Code != 23 || exit.Signal != 0 {
		t.Fatalf("restored convergence exit=%+v err=%v", exit, err)
	}
	assertWaitedAndAbsent(t, child)
}

func TestTypedTerminalTerminateIsNotStarvedByUnreadDaemonReadiness(t *testing.T) {
	f := newFixture(t)
	ready := filepath.Join(f.root, "terminal-owner-ready")
	child := startLivePTYChild(t, f, ready)
	installOwnedChildSafetyCleanup(t, child)
	unread := newUnreadTerminalOwner(t, child)
	started := time.Now()
	err := unread.owner.command(attemptFrame{Version: 1, Kind: "terminate"})
	elapsed := time.Since(started)
	unread.cancelRelease()
	if err != nil {
		t.Fatalf("typed terminal termination: %v", err)
	}
	if elapsed >= 600*time.Millisecond {
		t.Fatalf("typed terminal termination starved behind unread daemon readiness for %s", elapsed)
	}
	if !unread.owner.stopRequested {
		t.Fatal("terminal owner did not commit its typed stop transition")
	}
	exit, err := child.waitedExit()
	if err != nil || exit.Signal != int(unix.SIGTERM) {
		t.Fatalf("terminal evidence=%+v err=%v", exit, err)
	}
	if err := unread.reads.removeDaemon(); err != nil {
		t.Fatal(err)
	}
	assertWaitedAndAbsent(t, child)
}

func TestTypedTerminalTerminateDrainsPTYUntilExit(t *testing.T) {
	for _, tc := range []struct {
		name        string
		breakDaemon bool
	}{{name: "connected-daemon"}, {name: "failed-daemon-send", breakDaemon: true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			ready := filepath.Join(f.root, "pty-exit-pressure-ready")
			child, wantBytes := startPTYExitPressureChild(t, f, ready)
			installOwnedChildSafetyCleanup(t, child)
			// Closing the test-owned master first lets the ordinary exact-child
			// safety cleanup reap even if the pre-fix path stalls in terminal exit.
			t.Cleanup(func() {
				if child.state != stateWaited {
					_ = child.closePTY()
				}
			})
			reads := &attemptReadSet{kq: child.kq, daemonFD: -1, workerFD: -1, ptyFD: int(child.ptyMaster.Fd())}
			if err := reads.registerPTY(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reads.processOnly() })
			owner := terminalOwner{child: child, reads: reads, ptyOpen: true, ring: &terminalByteRing{}}
			if tc.breakDaemon {
				daemon, peer, err := newControlPair("failed-daemon", "failed-daemon-peer")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = daemon.Close() })
				if err := peer.Close(); err != nil {
					t.Fatal(err)
				}
				owner.daemon, owner.daemonOpen, owner.credit = daemon, true, 1
			}
			err := owner.command(attemptFrame{Version: 1, Kind: "terminate"})
			if tc.breakDaemon {
				if !errors.Is(err, unix.EPIPE) || owner.daemonOpen || owner.daemon != nil {
					t.Fatalf("failed daemon send was not retained: err=%v open=%t daemon=%v", err, owner.daemonOpen, owner.daemon)
				}
			} else if err != nil {
				t.Fatalf("typed terminal termination under PTY pressure: %v", err)
			}
			if !owner.stopRequested || !owner.ptyDrained || !owner.ptyEOF || !child.ptyDrained {
				t.Fatalf("terminal drain evidence stop=%t owner_drained=%t eof=%t child_drained=%t", owner.stopRequested, owner.ptyDrained, owner.ptyEOF, child.ptyDrained)
			}
			exit, err := child.waitedExit()
			if err != nil || exit.Code != 0 || exit.Signal != 0 {
				t.Fatalf("terminal exit=%+v err=%v", exit, err)
			}
			if owner.ring.Head() != uint64(wantBytes) {
				t.Fatalf("PTY output head=%d want=%d", owner.ring.Head(), wantBytes)
			}
			got := make([]byte, 0, min(wantBytes, terminalReplayCapacity))
			for cursor := owner.ring.Floor(); cursor < owner.ring.Head(); {
				chunk, next, err := owner.ring.Read(cursor)
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, chunk...)
				cursor = next
			}
			want := bytes.Repeat([]byte{'x'}, len(got))
			if !bytes.Equal(got, want) {
				t.Fatalf("retained PTY tail bytes=%d want=%d", len(got), len(want))
			}
			assertWaitedAndAbsent(t, child)
		})
	}
}

func TestTypedTerminalResizeIsNotStarvedByUnreadDaemonReadiness(t *testing.T) {
	f := newFixture(t)
	ready := filepath.Join(f.root, "terminal-resize-ready")
	child := startLivePTYChild(t, f, ready)
	installOwnedChildSafetyCleanup(t, child)
	for _, dimensions := range [][2]int{{0, 24}, {80, 0}, {maxPTYDimension + 1, 24}, {80, maxPTYDimension + 1}} {
		if err := child.resizePTYOwned(dimensions[0], dimensions[1]); !errors.Is(err, ErrState) {
			t.Fatalf("owner resize %dx%d error=%v, want ErrState", dimensions[0], dimensions[1], err)
		}
	}

	unread := newUnreadTerminalOwner(t, child)
	unread.owner.inputActive = true
	unread.owner.generation = 7
	want := TerminalCommand{Kind: TerminalResize, Correlation: 31, Generation: 7, Rows: 43, Cols: 132}
	started := time.Now()
	err := unread.owner.command(terminalCommandFrame(want))
	elapsed := time.Since(started)
	unread.cancelRelease()
	if err != nil {
		t.Fatalf("typed terminal resize: %v", err)
	}
	if elapsed >= 600*time.Millisecond {
		t.Fatalf("typed terminal resize starved behind unread daemon readiness for %s", elapsed)
	}
	if err := unread.peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var raw attemptFrame
	if err := readFrame(unread.peer, &raw, maxFrameBytes); err != nil {
		t.Fatal(err)
	}
	got, err := terminalEventFromFrame(raw)
	if err != nil || got.Kind != TerminalResizeResult || got.Correlation != want.Correlation || got.Generation != want.Generation || got.Rows != want.Rows || got.Cols != want.Cols || got.Status != TerminalResultOK {
		t.Fatalf("resize result=%+v err=%v", got, err)
	}
	size, err := unix.IoctlGetWinsize(int(child.ptyMaster.Fd()), unix.TIOCGWINSZ)
	if err != nil || size.Row != want.Rows || size.Col != want.Cols {
		t.Fatalf("owned PTY size=%+v err=%v", size, err)
	}
	if err := unread.owner.command(attemptFrame{Version: 1, Kind: "terminate"}); err != nil {
		t.Fatalf("owner stopped responding after resize: %v", err)
	}
	if err := unread.reads.removeDaemon(); err != nil {
		t.Fatal(err)
	}
	if err := child.resizePTYOwned(80, 24); !errors.Is(err, ErrState) {
		t.Fatalf("post-Wait owner resize error=%v, want ErrState", err)
	}
	assertWaitedAndAbsent(t, child)
}

type unreadTerminalOwner struct {
	owner         terminalOwner
	reads         *attemptReadSet
	peer          *os.File
	cancelRelease func()
}

func newUnreadTerminalOwner(t *testing.T, child *OwnedChild) *unreadTerminalOwner {
	t.Helper()
	daemon, peer, err := newControlPair("unread-daemon", "unread-daemon-peer")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = daemon.Close()
		_ = peer.Close()
	})
	reads := &attemptReadSet{kq: child.kq, daemonFD: int(daemon.Fd()), workerFD: -1, ptyFD: int(child.ptyMaster.Fd())}
	if err := reads.registerDaemon(); err != nil {
		t.Fatal(err)
	}
	if err := reads.registerPTY(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reads.processOnly() })
	if err := writeControlFrame(peer, terminalCommandFrame(TerminalCommand{Kind: TerminalCredit, Credit: 1}), maxFrameBytes); err != nil {
		t.Fatal(err)
	}
	assertLevelTriggeredReadiness(t, child.kq, int(daemon.Fd()))
	return &unreadTerminalOwner{
		owner:         terminalOwner{child: child, daemon: daemon, reads: reads, daemonOpen: true, ptyOpen: true, ring: &terminalByteRing{}},
		reads:         reads,
		peer:          peer,
		cancelRelease: releaseUnreadReadinessAfter(t, daemon, 1200*time.Millisecond),
	}
}

func startLivePTYChild(t *testing.T, f *fixture, ready string) *OwnedChild {
	t.Helper()
	gate, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := fmt.Sprintf("printf ready > %q; exec /bin/sleep 30", ready)
	spec, err := PrepareExecSpec(ExecSpec{Target: "/bin/sh", Args: []string{"-c", command}, Env: []string{"PATH=/usr/bin:/bin", "LANG=C", "TERM=xterm"}, Cwd: f.cwd})
	if err != nil {
		t.Fatal(err)
	}
	child, err := StartBlockedPTY(f.lease, gate, spec, false)
	if err != nil {
		t.Fatal(err)
	}
	f.child = child
	if _, err := child.Activate(); err != nil {
		t.Fatal(err)
	}
	waitFile(t, ready)
	return child
}

func startPTYExitPressureChild(t *testing.T, f *fixture, ready string) (*OwnedChild, int) {
	t.Helper()
	gate, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	spec, err := PrepareExecSpec(ExecSpec{Target: gate, Args: []string{"--pty-exit-pressure", ready}, Env: []string{"PATH=/usr/bin:/bin", "LANG=C", "TERM=xterm"}, Cwd: f.cwd})
	if err != nil {
		t.Fatal(err)
	}
	child, err := StartBlockedPTY(f.lease, gate, spec, false)
	if err != nil {
		t.Fatal(err)
	}
	f.child = child
	if _, err := child.Activate(); err != nil {
		t.Fatal(err)
	}
	waitFile(t, ready)
	body, err := os.ReadFile(ready)
	if err != nil {
		t.Fatal(err)
	}
	written, err := strconv.Atoi(string(body))
	if err != nil || written <= 0 {
		t.Fatalf("PTY pressure witness=%q err=%v", body, err)
	}
	return child, written
}

func assertLevelTriggeredReadiness(t *testing.T, kq, fd int) {
	t.Helper()
	events := make([]unix.Kevent_t, 1)
	zero := unix.Timespec{}
	n, err := unix.Kevent(kq, nil, events, &zero)
	if err != nil || n != 1 || events[0].Filter != unix.EVFILT_READ || events[0].Ident != uint64(fd) {
		t.Fatalf("daemon readiness event=%+v count=%d err=%v", events[0], n, err)
	}
}

func releaseUnreadReadinessAfter(t *testing.T, daemon *os.File, delay time.Duration) func() {
	t.Helper()
	cancel := make(chan struct{})
	done := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(done)
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
			_, _ = unix.Read(int(daemon.Fd()), make([]byte, maxFrameBytes))
		case <-cancel:
		}
	}()
	stop := func() {
		once.Do(func() { close(cancel) })
		<-done
	}
	t.Cleanup(stop)
	return stop
}

func startControlledNaturalExit(t *testing.T, f *fixture, code int) (*OwnedChild, *os.File) {
	t.Helper()
	fifo := filepath.Join(f.root, "natural-exit.fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	release, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = release.Close() })
	ready := filepath.Join(f.root, "natural-exit.ready")
	command := fmt.Sprintf("printf ready > %q; IFS= read -r _ < %q; exit %d", ready, fifo, code)
	child := f.start("/bin/sh", []string{"-c", command}, nil, outputFile(t, filepath.Join(f.root, "out")))
	if _, err := child.Activate(); err != nil {
		t.Fatal(err)
	}
	waitFile(t, ready)
	return child, release
}

func naturalExitThenEPERM(child *OwnedChild, release *os.File) (error, error) {
	census, err := censusOwnedGroup(child.Identity())
	if err != nil {
		return nil, fmt.Errorf("pre-signal census: %w", err)
	}
	if !census.hasLiveMember {
		return nil, errors.New("pre-signal census found no live member")
	}
	if _, err := release.Write([]byte("exit\n")); err != nil {
		return nil, err
	}
	if err := waitOwnedGroupWithoutLiveMembersBounded(child.Identity(), time.Second); err != nil {
		return nil, err
	}
	return classifyGroupSignal(unix.EPERM, false), nil
}

func installOwnedChildSafetyCleanup(t *testing.T, child *OwnedChild) {
	t.Helper()
	t.Cleanup(func() {
		child.testSignal = nil
		child.testConvergence = nil
		if child.state == stateWaited {
			return
		}
		if _, err := child.Terminate(100 * time.Millisecond); err == nil {
			return
		}
		if !child.exitObserved {
			_ = child.cmd.Process.Kill()
			_, _ = child.waitForExit(4 * time.Second)
		}
		if child.exitObserved && child.state != stateWaited {
			_ = child.waitActivatedOnce()
		}
		if child.state != stateWaited {
			t.Errorf("hard safety cleanup retained child state %d", child.state)
		}
	})
}

func waitOwnedGroupWithoutLiveMembers(t *testing.T, identity Identity) {
	t.Helper()
	if err := waitOwnedGroupWithoutLiveMembersBounded(identity, 4*time.Second); err != nil {
		t.Fatal(err)
	}
}

func waitOwnedGroupWithoutLiveMembersBounded(identity Identity, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		census, err := censusOwnedGroup(identity)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if !census.hasLiveMember {
			return nil
		}
		if time.Now().After(deadline) {
			return os.ErrDeadlineExceeded
		}
		time.Sleep(time.Millisecond)
	}
}

func assertWaitedAndAbsent(t *testing.T, child *OwnedChild) {
	t.Helper()
	if child.state != stateWaited || child.cmd.ProcessState == nil {
		t.Fatalf("child not solely waited: state=%d process_state=%v", child.state, child.cmd.ProcessState)
	}
	waitExactAbsence(t, child.Identity())
}
