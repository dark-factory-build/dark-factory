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

// startGroupLeader starts a TERM-immune group leader, so the real SIGTERM
// issued by the production path cannot itself converge the group and mask
// what the test pins. Only an explicit SIGKILL converges it.
func startGroupLeader(t *testing.T) Identity {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", `trap "" TERM; sleep 30`)
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
	defer func() { testGroupSignalResult = nil }()
	// The SIGKILL below genuinely converges the real group; only the errno
	// the kernel is taken to have reported is injected.
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
		return unix.EPERM
	}
	if err := signalOwnedGroup(leader, unix.SIGTERM); err != nil {
		t.Fatalf("EPERM with converged census = %v", err)
	}

	// A live member behind the same errno stays fail-closed unresolved: the
	// census enumerates every group member regardless of whether we may
	// signal it, so it is a strict superset of the kernel's own walk.
	live := startGroupLeader(t)
	testGroupSignalResult = func(error) error { return unix.EPERM }
	if err := signalOwnedGroup(live, unix.SIGTERM); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("EPERM with live members = %v", err)
	}
}
