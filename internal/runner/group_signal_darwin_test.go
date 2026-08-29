//go:build darwin

package runner

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// startGroupLeader starts a real process that leads its own group.
func startGroupLeader(t *testing.T) Identity {
	t.Helper()
	cmd := exec.Command("/bin/sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = unix.Kill(-cmd.Process.Pid, unix.SIGKILL)
		_, _ = cmd.Process.Wait()
	})
	leader, err := readIdentity(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if leader.PGID != cmd.Process.Pid {
		t.Fatalf("leader did not own its group: %+v", leader)
	}
	return leader
}

func TestSignalOwnedGroupForgivesEPERMOnlyWithConvergedCensus(t *testing.T) {
	// Darwin reports EPERM from the owned-group kill once our unreaped
	// zombie leader is the only member left. The exact leader-anchored
	// census, not the errno, decides whether that means converged.
	leader := startGroupLeader(t)
	defer func() {
		testGroupSignalResult = nil
		testGroupCensus = nil
	}()
	// The SIGKILL below genuinely converges the real group; only the errno
	// the kernel is taken to have reported is injected.
	var observed error
	testGroupSignalResult = func(error) error {
		_ = unix.Kill(-leader.PGID, unix.SIGKILL)
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			census, err := censusOwnedGroup(leader)
			if err == nil && !census.hasLiveMember {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		// Ask the kernel what it genuinely reports for this exact state —
		// our own unreaped zombie leader alone in its group — rather than
		// asserting a remembered errno. The seam controls only when the
		// question is asked; the answer is the kernel's.
		observed = unix.Kill(-leader.PGID, unix.Signal(0))
		return observed
	}
	if err := signalOwnedGroup(leader, unix.SIGTERM); err != nil {
		t.Fatalf("converged zombie-leader group = %v (kernel reported %v)", err, observed)
	}
	if !errors.Is(observed, unix.EPERM) {
		t.Fatalf("kernel vocabulary changed: zombie-leader group signal = %v, want EPERM", observed)
	}

	// A live member behind the same errno stays fail-closed unresolved: the
	// census enumerates every group member regardless of whether we may
	// signal it, so it is a strict superset of the kernel's own walk. Signal
	// 0 exercises the identical production path — the same negated-PGID
	// unix.Kill — while leaving the group deterministically alive, so the
	// refusal is pinned by the census, not by signal timing.
	live := startGroupLeader(t)
	censusCalls := 0
	testGroupCensus = func(identity Identity) (ownedGroupCensus, error) {
		censusCalls++
		if censusCalls == 2 {
			return ownedGroupCensus{}, unix.EINTR
		}
		return censusOwnedGroup(identity)
	}
	testGroupSignalResult = func(error) error { return unix.EPERM }
	started := time.Now()
	err := signalOwnedGroup(live, unix.Signal(0))
	if !errors.Is(err, ErrUnresolved) {
		t.Fatalf("EPERM with live members = %v", err)
	}
	if censusCalls < 3 || time.Since(started) < 200*time.Millisecond {
		t.Fatalf("live census did not retry EINTR through settle window: calls=%d elapsed=%s", censusCalls, time.Since(started))
	}
	if census, censusErr := censusOwnedGroup(live); censusErr != nil || !census.hasLiveMember {
		t.Fatalf("live fixture did not stay alive: census=%+v err=%v", census, censusErr)
	}
}

func TestSignalOwnedGroupCensusFailureStaysFailClosed(t *testing.T) {
	leader := startGroupLeader(t)
	t.Cleanup(func() {
		testGroupSignalResult = nil
		testGroupCensus = nil
	})
	injected := errors.New("injected census failure")
	censusCalls := 0
	testGroupCensus = func(identity Identity) (ownedGroupCensus, error) {
		censusCalls++
		if censusCalls == 2 {
			return ownedGroupCensus{}, errors.Join(ErrUnresolved, injected)
		}
		return censusOwnedGroup(identity)
	}
	testGroupSignalResult = func(error) error { return unix.EPERM }
	err := signalOwnedGroup(leader, unix.Signal(0))
	if !errors.Is(err, ErrUnresolved) || !errors.Is(err, injected) {
		t.Fatalf("non-EINTR census error was not preserved: %v", err)
	}
}
