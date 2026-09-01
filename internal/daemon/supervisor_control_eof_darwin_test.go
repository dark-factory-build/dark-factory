//go:build darwin

package daemon

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/runner"
)

// innerReadyControlPair drives a real controller to the inner-ready state over
// the exact wire protocol and hands back the runner's still-open peer.
func innerReadyControlPair(t *testing.T, attemptID string) (*runner.AttemptController, *os.File) {
	t.Helper()
	controller, peer, err := runner.NewAttemptController()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = controller.Close()
		_ = peer.Close()
	})
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrapper, err := runner.PrepareExecSpec(runner.ExecSpec{Target: executable, Args: []string{"--unused"}, Env: []string{"PATH=/usr/bin:/bin"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Configure(runner.AttemptSpec{
		AttemptID: attemptID, Wrapper: wrapper,
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
func TestSupervisorCheckpointEOFReportsNoManufacturedBrokenPipe(t *testing.T) {
	controller, peer := innerReadyControlPair(t, "supervisor-control-eof")
	// The peer runs off the test goroutine, so it reports through a channel
	// rather than calling t.Fatal, which may only be called on the goroutine
	// running the test.
	drained := make(chan error, 1)
	go func() {
		// Consuming the release frame proves the supervisor's write landed
		// while the runner was still alive, so the exit that follows is
		// orderly rather than a write into an already-dead peer.
		var header [4]byte
		if _, err := io.ReadFull(peer, header[:]); err != nil {
			drained <- err
			return
		}
		body := make([]byte, binary.BigEndian.Uint32(header[:]))
		if _, err := io.ReadFull(peer, body); err != nil {
			drained <- err
			return
		}
		drained <- peer.Close()
	}()
	_, stageErr := releaseCheckpoint(controller, runner.StageSelection)
	if err := <-drained; err != nil {
		t.Fatalf("runner peer: %v", err)
	}
	if !errors.Is(stageErr, io.EOF) || !errors.Is(stageErr, runner.ErrState) {
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
}
