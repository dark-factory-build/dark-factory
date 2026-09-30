//go:build darwin

package runner

import (
	"os/exec"
	"strings"
	"testing"
)

// TestControlPairIsNotInheritedByAConcurrentFork proves the attempt control
// pair cannot reach a process forked while it is being created. Darwin has no
// SOCK_CLOEXEC, so before newControlSocketPair held syscall.ForkLock roughly a
// third of the forks in this loop inherited one run's control descriptors.
func TestControlPairIsNotInheritedByAConcurrentFork(t *testing.T) {
	quiet := childDescriptors(t)
	stop := make(chan struct{})
	pairs := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				pairs <- nil
				return
			default:
			}
			parent, child, err := newControlPair("fork-race-parent", "fork-race-child")
			if err != nil {
				pairs <- err
				return
			}
			if err := parent.Close(); err != nil {
				pairs <- err
				return
			}
			if err := child.Close(); err != nil {
				pairs <- err
				return
			}
		}
	}()
	for fork := 0; fork < 300; fork++ {
		if got := childDescriptors(t); got != quiet {
			close(stop)
			<-pairs
			t.Fatalf("fork %d child descriptors = %v, want %v", fork, got, quiet)
		}
	}
	close(stop)
	if err := <-pairs; err != nil {
		t.Fatalf("control pair: %v", err)
	}
}

// childDescriptors lists what one ordinary forked child inherits. The exact
// set belongs to os/exec, so the test compares against its own quiet sample
// rather than a constant.
func childDescriptors(t *testing.T) string {
	t.Helper()
	output, err := exec.Command("/bin/ls", "/dev/fd").Output()
	if err != nil {
		t.Fatalf("child descriptor census: %v", err)
	}
	return strings.Join(strings.Fields(string(output)), " ")
}
