//go:build darwin

package runner

import (
	"errors"
	"io"
	"testing"
	"time"
)

// A framed read that ends exactly on a frame boundary proves the sole peer is
// gone: nothing further can arrive and no later write can be delivered. The
// controller must therefore spend itself on that read, so a caller that writes
// afterwards is told its capability is finished rather than being handed an
// EPIPE that describes a message no peer could ever have received.
func TestAttemptControllerCleanEOFSpendsCapability(t *testing.T) {
	controller, peer, err := NewAttemptController()
	if err != nil {
		t.Fatal(err)
	}
	controller.state = controllerInnerReady
	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Next(time.Second); !errors.Is(err, io.EOF) {
		t.Fatalf("read after peer exit = %v, want EOF", err)
	}
	if controller.file != nil || controller.state != controllerSpent {
		t.Fatalf("controller survived a conclusive EOF: file=%v state=%d", controller.file, controller.state)
	}
	if err := controller.Terminate(); !errors.Is(err, ErrState) {
		t.Fatalf("terminate after observed EOF = %v, want ErrState", err)
	}
	if err := controller.Release(StageSelection); !errors.Is(err, ErrState) {
		t.Fatalf("release after observed EOF = %v, want ErrState", err)
	}
	if err := controller.Close(); err != nil {
		t.Fatalf("close after observed EOF = %v", err)
	}
}

// A partial frame is not a clean boundary: the peer may have died mid-write and
// the caller must still see that distinct failure rather than a lifecycle state.
func TestAttemptControllerPartialFrameIsNotACleanEOF(t *testing.T) {
	controller, peer, err := NewAttemptController()
	if err != nil {
		t.Fatal(err)
	}
	controller.state = controllerInnerReady
	if _, err := peer.Write([]byte{0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Next(time.Second); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated header = %v, want unexpected EOF", err)
	}
	if controller.file == nil {
		t.Fatal("truncated header spent the capability")
	}
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
}
