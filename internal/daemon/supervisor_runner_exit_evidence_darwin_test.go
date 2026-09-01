//go:build darwin

package daemon

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
	"golang.org/x/sys/unix"
)

// When the outer runner dies before it finishes the protocol, the supervisor's
// control read reports only EOF -- the same string whether the runner exited
// cleanly, failed to exec, or was killed. The owner waits that exact child
// during cleanup and therefore already holds the status that tells those apart,
// so a failing run must carry it. Without this, an intermittent runner death is
// one indistinguishable line and cannot be diagnosed from a single sighting,
// which is what happened in #425 -- a package-test failure, where RunNext's
// error is the report. Note the scheduler drops this error for an admitted
// attempt, so under factoryd the cause still reaches no operator; see #435.
func TestSupervisorFailedRunNamesOuterRunnerExit(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
	fixture.spec.activateOuter = func(child *runner.OwnedChild) (runner.FileIdentity, error) {
		marker, err := child.Activate()
		if err != nil {
			return marker, err
		}
		// Kill the outer runner at activation, before it can register
		// inner-ready, so the failure converges through convergeActivatedRunner.
		// The daemon still owns the only wait, so the status stays its to
		// observe. The two later tests cover the other two record sites.
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

// The runner dying after provider release is the shape #425 recorded, and the
// daemon notices it by a read rather than a write: the live owner's control
// read ends the stream. That path waits the child in the supervisor's own
// result tail rather than in owner.close, and it is where the exit was being
// waited and thrown away.
func TestSupervisorRunnerDeathAfterReleaseNamesExit(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, true, false))
	var outer runner.Identity
	fixture.spec.activateOuter = func(child *runner.OwnedChild) (runner.FileIdentity, error) {
		marker, err := child.Activate()
		outer = child.Identity()
		return marker, err
	}
	fixture.spec.afterProviderRelease = func() error {
		return unix.Kill(outer.PID, unix.SIGKILL)
	}
	_, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err == nil {
		t.Fatal("a killed outer runner produced no error")
	}
	if !strings.Contains(err.Error(), "daemon: outer runner exit") {
		t.Fatalf("run error does not name the outer runner exit:\n%v", err)
	}
	if !strings.Contains(err.Error(), "signal=9") {
		t.Fatalf("run error does not name the killing signal:\n%v", err)
	}
	t.Logf("reported failure:\n%v", err)
}

// The proposal detail is the only durable free-form field a failed run carries.
// It used to store a constant while the cause was discarded, which is why a
// failed run in the database says nothing about why it failed.
func TestSupervisorFailedRunPersistsItsCause(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
	injected := errors.New("injected pre-release state failure")
	fixture.spec.beforeProviderStateCheck = func() error { return injected }
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if !errors.Is(err, injected) {
		t.Fatalf("RunNext = %v, want the injected failure", err)
	}
	stored, found, readErr := fixture.store.Run(context.Background(), run.ID)
	if readErr != nil || !found {
		t.Fatalf("read back run: found=%v err=%v", found, readErr)
	}
	if stored.Proposal == nil {
		t.Fatal("a failed run carries no proposal")
	}
	if !strings.Contains(stored.Proposal.Detail(), injected.Error()) {
		t.Fatalf("durable failure detail does not name the cause: %q", stored.Proposal.Detail())
	}
}

// A cause longer than the field must be truncated on a rune boundary so the
// stored text stays valid UTF-8.
func TestFailureDetailTruncatesWholeRunes(t *testing.T) {
	long := errors.New(strings.Repeat("é", maxFailureDetailBytes))
	detail := failureDetail(long)
	if len(detail) > maxFailureDetailBytes {
		t.Fatalf("detail is %d bytes, over the %d-byte field", len(detail), maxFailureDetailBytes)
	}
	if !utf8.ValidString(detail) {
		t.Fatal("truncation split a rune")
	}
	if _, err := kernel.NewFailureProposal(kernel.FailureInternal, detail); err != nil {
		t.Fatalf("kernel rejected the truncated detail: %v", err)
	}
	if failureDetail(nil) == "" {
		t.Fatal("a nil cause produced an empty detail")
	}
}
