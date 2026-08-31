//go:build darwin

package runner

import (
	"encoding/binary"
	"errors"
	"io"
	"testing"
	"time"
)

// A read that returns zero bytes proves the sole peer is gone: nothing further
// can arrive and no later write can be delivered. The controller must spend
// itself on that read, so a caller that writes afterwards is told its
// capability is finished rather than being handed an EPIPE describing a
// message no peer could ever have received.
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

// A peer that died between header and body is just as gone, so spending is
// still right there. What must not happen is spending on a *short* read that
// left the stream intact. This passes on the parent commit too: it is a guard
// against widening the predicate beyond zero-byte reads, not a regression test.
func TestAttemptControllerShortHeaderDoesNotSpendCapability(t *testing.T) {
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

// A header with no body behind it is a zero-byte body read, so it is EOF and
// does spend. Pinned so the boundary between the two cases above is explicit.
func TestAttemptControllerHeaderWithoutBodyIsStreamEOF(t *testing.T) {
	controller, peer, err := NewAttemptController()
	if err != nil {
		t.Fatal(err)
	}
	controller.state = controllerInnerReady
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], 16)
	if _, err := peer.Write(header[:]); err != nil {
		t.Fatal(err)
	}
	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Next(time.Second); !errors.Is(err, io.EOF) {
		t.Fatalf("header without body = %v, want EOF", err)
	}
	if controller.state != controllerSpent {
		t.Fatalf("header without body left state=%d", controller.state)
	}
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
}
