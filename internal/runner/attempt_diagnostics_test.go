package runner

import (
	"bytes"
	"errors"
	"os"
	"testing"
)

func TestAttemptDiagnosticsKeepCauseAndRetainedOutputOnDisk(t *testing.T) {
	dir, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if _, found, err := ReadAttemptDiagnostics(dir); found || err != nil {
		t.Fatalf("absent diagnostics found=%v err=%v", found, err)
	}
	ring := &terminalByteRing{}
	if err := ring.Append([]byte("worker stderr line\n")); err != nil {
		t.Fatal(err)
	}
	if err := writeAttemptDiagnostics(dir, errors.New("runner: provider exec: Change worktree moved"), ring); err != nil {
		t.Fatal(err)
	}
	got, found, err := ReadAttemptDiagnostics(dir)
	if err != nil || !found || got.Cause != "runner: provider exec: Change worktree moved" || got.Floor != 0 || got.Head != ring.Head() || !bytes.Equal(got.Output, []byte("worker stderr line\n")) {
		t.Fatalf("diagnostics = %+v found=%v err=%v", got, found, err)
	}
	if err := writeAttemptDiagnostics(dir, errors.New("again"), ring); err == nil {
		t.Fatal("diagnostics were replaced")
	}
}

// A worker can exit before the runner reads its refusal; the refusal is then
// already queued on the worker socket and must still name the cause.
func TestQueuedWorkerRefusalSurvivesExitObservedFirst(t *testing.T) {
	own, peer, err := newControlPair("refusal-runner", "refusal-worker")
	if err != nil {
		t.Fatal(err)
	}
	defer own.Close()
	owner := &terminalOwner{worker: own, workerOpen: true}
	if refusal := owner.queuedWorkerRefusal(); refusal != nil {
		t.Fatalf("empty socket refusal = %v", refusal)
	}
	if err := writeControlFrame(peer, attemptFrame{Version: 1, Kind: "provider-exec-error", Payload: []byte("Change worktree moved")}, maxFrameBytes); err != nil {
		t.Fatal(err)
	}
	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}
	if refusal := owner.queuedWorkerRefusal(); refusal == nil || refusal.Error() != "runner: provider exec: Change worktree moved" {
		t.Fatalf("queued refusal = %v", refusal)
	}
}
