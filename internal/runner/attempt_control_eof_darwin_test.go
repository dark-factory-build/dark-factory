//go:build darwin

package runner

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

func framedHeader(size uint32) []byte {
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], size)
	return header[:]
}

// A read that stops because the stream ended is conclusive: the peer is gone,
// nothing more can arrive, and no later write can be delivered. The controller
// must spend itself there, so a caller that writes afterwards is told its
// capability is finished rather than being handed an EPIPE describing a message
// no peer could ever have received.
func TestAttemptControllerStreamEndSpendsCapability(t *testing.T) {
	for _, streamEnd := range []struct {
		name string
		send []byte
	}{
		{"closed with nothing sent", nil},
		{"torn header", []byte{0, 0, 0}},
		{"header with no body", framedHeader(16)},
		{"header with a torn body", append(framedHeader(16), 'a')},
	} {
		t.Run(streamEnd.name, func(t *testing.T) {
			controller, peer, err := NewAttemptController()
			if err != nil {
				t.Fatal(err)
			}
			controller.state = controllerInnerReady
			if len(streamEnd.send) != 0 {
				if _, err := peer.Write(streamEnd.send); err != nil {
					t.Fatal(err)
				}
			}
			if err := peer.Close(); err != nil {
				t.Fatal(err)
			}
			_, readErr := controller.Next(time.Second)
			if !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
				t.Fatalf("read after peer exit = %v, want a stream end", readErr)
			}
			if controller.file != nil || controller.state != controllerSpent {
				t.Fatalf("controller survived a stream end: file=%v state=%d", controller.file, controller.state)
			}
			if err := controller.Terminate(); !errors.Is(err, ErrState) {
				t.Fatalf("terminate after a stream end = %v, want ErrState", err)
			}
			if err := controller.Release(StageSelection); !errors.Is(err, ErrState) {
				t.Fatalf("release after a stream end = %v, want ErrState", err)
			}
			if err := controller.Close(); err != nil {
				t.Fatalf("close after a stream end = %v", err)
			}
		})
	}
}

// A complete frame whose body is malformed came from a peer that is still
// there. The JSON decoder answers such a body with io.EOF or io.ErrUnexpectedEOF
// under its own name, so without renaming them one bad frame would destroy a
// live capability and report the peer as gone.
func TestAttemptControllerMalformedBodyDoesNotSpendCapability(t *testing.T) {
	for _, body := range []string{"   ", `{"version":`} {
		t.Run(body, func(t *testing.T) {
			controller, peer, err := NewAttemptController()
			if err != nil {
				t.Fatal(err)
			}
			controller.state = controllerInnerReady
			t.Cleanup(func() { closeAll(t, controller, peer) })
			if _, err := peer.Write(append(framedHeader(uint32(len(body))), body...)); err != nil {
				t.Fatal(err)
			}
			_, readErr := controller.Next(time.Second)
			if readErr == nil {
				t.Fatalf("malformed body %q was accepted", body)
			}
			if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
				t.Fatalf("malformed body %q impersonated a closed stream: %v", body, readErr)
			}
			if controller.file == nil || controller.state == controllerSpent {
				t.Fatalf("malformed body %q spent a live capability", body)
			}
		})
	}
}

func closeAll(t *testing.T, controller *AttemptController, peer *os.File) {
	t.Helper()
	if err := controller.Close(); err != nil {
		t.Error(err)
	}
	if err := peer.Close(); err != nil {
		t.Error(err)
	}
}
