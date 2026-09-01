//go:build darwin

package daemon

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/runner"
	"golang.org/x/sys/unix"
)

// When the outer runner dies before it finishes the protocol, the supervisor's
// control read reports only EOF -- the same string whether the runner exited
// cleanly, failed to exec, or was killed. The owner waits that exact child
// during cleanup and therefore already holds the status that tells those apart,
// so a failing run must carry it. Without this, an intermittent runner death is
// one indistinguishable line in a CI log and cannot be diagnosed from a single
// sighting, which is exactly what happened in #425.
func TestSupervisorFailedRunNamesOuterRunnerExit(t *testing.T) {
	fixture := newSupervisorFixture(t, providerExitAfterSuccessProgram(t, 7))
	fixture.spec.activateOuter = func(child *runner.OwnedChild) (runner.FileIdentity, error) {
		marker, err := child.Activate()
		if err != nil {
			return marker, err
		}
		// Kill the released outer runner. Whether it dies before or after it
		// registers inner-ready decides which control operation notices first,
		// and the run must name the exit either way. The daemon still owns the
		// only wait, so the status stays its to observe.
		if err := unix.Kill(child.Identity().PID, unix.SIGKILL); err != nil {
			t.Fatalf("kill outer runner: %v", err)
		}
		return marker, nil
	}
	_, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err == nil {
		t.Fatal("a killed outer runner produced no error")
	}
	text := err.Error()
	if !strings.Contains(text, "daemon: outer runner exit") {
		t.Fatalf("run error does not name the outer runner exit:\n%v", text)
	}
	if !strings.Contains(text, "signal=9") {
		t.Fatalf("run error does not name the killing signal:\n%v", text)
	}
	t.Logf("reported failure:\n%v", text)
}

// The owner's own reap in close() is the path every post-activation failure
// takes once the checkpoint stages have run, and it is where an intermittent
// runner death during the protocol lands. Kill the outer runner after its
// stages complete: the daemon's terminate can no longer be delivered, so the
// exit is the runner's own and the run must name it.
func TestSupervisorLateRunnerDeathNamesExitFromOwnerClose(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
	var outer runner.Identity
	fixture.spec.activateOuter = func(child *runner.OwnedChild) (runner.FileIdentity, error) {
		marker, err := child.Activate()
		outer = child.Identity()
		return marker, err
	}
	fixture.spec.beforeProviderRelease = func() {
		if err := unix.Kill(outer.PID, unix.SIGKILL); err != nil {
			t.Errorf("kill outer runner: %v", err)
		}
	}
	_, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err == nil {
		t.Fatal("a dead outer runner produced no error")
	}
	if !strings.Contains(err.Error(), "daemon: outer runner exit") {
		t.Fatalf("run error does not name the outer runner exit:\n%v", err)
	}
	if !strings.Contains(err.Error(), "signal=9") {
		t.Fatalf("run error does not name the killing signal:\n%v", err)
	}
	t.Logf("reported failure:\n%v", err)
}

// A run that already carries its own reason must not gain an exit line. The
// runner is alive and converged by the daemon here, so its exit is a
// consequence of the daemon's own action rather than evidence about the
// failure, and reporting it in the same voice would read as an independent
// cause — the exact failure mode this evidence exists to remove.
func TestSupervisorSelfExplainingFailureCarriesNoExitEvidence(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
	injected := errors.New("injected pre-release state failure")
	fixture.spec.beforeProviderStateCheck = func() error { return injected }
	_, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if !errors.Is(err, injected) {
		t.Fatalf("RunNext = %v, want the injected failure", err)
	}
	if strings.Contains(err.Error(), "daemon: outer runner exit") {
		t.Fatalf("a self-explaining failure gained an exit line:\n%v", err)
	}
	if _, statErr := os.Stat(fixture.witness); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("provider ran despite the pre-release failure: %v", statErr)
	}
}
