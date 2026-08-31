//go:build darwin

package daemon

import (
	"context"
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
