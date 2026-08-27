//go:build darwin

package runner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestTerminateConsumesQueuedNaturalExitBeforeSignal(t *testing.T) {
	f := newFixture(t)
	child := f.start("/bin/sh", []string{"-c", "exit 23"}, nil, outputFile(t, filepath.Join(f.root, "out")))
	if _, err := child.Activate(); err != nil {
		t.Fatal(err)
	}
	installOwnedChildSafetyCleanup(t, child)
	waitOwnedGroupWithoutLiveMembers(t, child.Identity())
	var signals atomic.Int32
	child.testSignal = func(unix.Signal) error {
		signals.Add(1)
		return errors.New("unexpected signal")
	}

	exit, err := child.Terminate(time.Second)
	if err != nil || exit.Code != 23 || exit.Signal != 0 {
		t.Fatalf("queued natural exit=%+v err=%v", exit, err)
	}
	if got := signals.Load(); got != 0 {
		t.Fatalf("queued NOTE_EXIT reached signal path %d times", got)
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
	child := f.start("/bin/sh", []string{"-c", fmt.Sprintf("trap '' TERM; printf ready > %q; while :; do sleep 1; done", ready)}, nil, outputFile(t, filepath.Join(f.root, "out")))
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
	if err := unix.Kill(-child.Identity().PGID, 0); !errors.Is(err, unix.ESRCH) {
		t.Fatalf("owned group remains after Wait: %v", err)
	}
}
