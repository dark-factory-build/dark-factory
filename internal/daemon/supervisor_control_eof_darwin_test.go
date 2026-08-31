//go:build darwin

package daemon

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/runner"
)

// innerReadyControlPair drives a real controller to the inner-ready state over
// the exact wire protocol and hands back the runner's still-open peer.
func innerReadyControlPair(t *testing.T) (*runner.AttemptController, *os.File) {
	t.Helper()
	controller, peer, err := runner.NewAttemptController()
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrapper, err := runner.PrepareExecSpec(runner.ExecSpec{Target: executable, Args: []string{"--unused"}, Env: []string{"PATH=/usr/bin:/bin"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Configure(runner.AttemptSpec{
		AttemptID: "supervisor-control-eof", Wrapper: wrapper,
		MarkerName: runner.InnerActivationMarkerName, ResultName: runner.AttemptResultSpoolName, ResultProof: testResultProof(t),
	}); err != nil {
		t.Fatal(err)
	}
	_ = readTerminalEffectWire(t, peer)
	writeTerminalEffectWire(t, peer, terminalEffectWireFrame{
		Version: 1, Kind: "inner-ready",
		Identity: runner.Identity{PID: 41001, PGID: 41001, Birth: runner.Birth{Seconds: 17, Microseconds: 9}},
	})
	if event, err := controller.Next(time.Second); err != nil || event.Kind != runner.AttemptInnerReady {
		t.Fatalf("inner ready = %+v, %v", event, err)
	}
	return controller, peer
}

// A runner that takes a checkpoint release and then exits leaves the supervisor
// reading EOF on the control socket. The deferred owner close must not follow
// that read with a terminate frame nothing can receive: a manufactured
// "broken pipe" in the run's error describes no failure of its own and reads
// as evidence that a racing daemon write ended the attempt, which is backwards.
// The EOF is the whole failure and must be reported alone.
func TestSupervisorCheckpointEOFReportsNoManufacturedBrokenPipe(t *testing.T) {
	controller, peer := innerReadyControlPair(t)
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		// Reading the release frame proves the supervisor's write landed while
		// the runner was still alive, so the exit that follows is orderly.
		_ = readTerminalEffectWire(t, peer)
		_ = peer.Close()
	}()
	_, stageErr := releaseCheckpoint(controller, runner.StageSelection)
	<-closed
	if !errors.Is(stageErr, runner.ErrState) || !strings.Contains(stageErr.Error(), "EOF") {
		t.Fatalf("checkpoint after orderly runner exit = %v, want joined EOF and lifecycle state", stageErr)
	}
	owner := &supervisorAttemptOwner{controller: controller}
	closeErr := owner.close()
	if closeErr != nil {
		t.Fatalf("owner close after observed EOF = %v, want no further error", closeErr)
	}
	if owner.controller != nil {
		t.Fatal("owner retained a control capability whose peer is gone")
	}
	joined := errors.Join(stageErr, closeErr)
	if strings.Contains(joined.Error(), "broken pipe") {
		t.Fatalf("run error manufactured a write failure after a conclusive read:\n%v", joined)
	}
}
