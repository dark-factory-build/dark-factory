//go:build darwin

package runner

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

func TestAttemptControllerNextReadyDoesNotConsumePartialFrame(t *testing.T) {
	controller, peer, err := NewAttemptController()
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	defer peer.Close()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrapper, err := PrepareExecSpec(ExecSpec{Target: executable, Args: []string{"--unused"}, Env: []string{"PATH=/usr/bin:/bin"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Configure(AttemptSpec{AttemptID: "readiness", Wrapper: wrapper, MarkerName: InnerActivationMarkerName, ResultName: AttemptResultSpoolName}); err != nil {
		t.Fatal(err)
	}
	if ready, err := controller.NextReady(10 * time.Millisecond); err != nil || ready {
		t.Fatalf("empty readiness = %v, %v", ready, err)
	}

	want := Identity{PID: 12001, PGID: 12001, Birth: Birth{Seconds: 100, Microseconds: 5}}
	body, err := json.Marshal(attemptFrame{Version: 1, Kind: "inner-ready", Identity: want})
	if err != nil {
		t.Fatal(err)
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(body)))
	if _, err := peer.Write(append(header[:], body[:len(body)/2]...)); err != nil {
		t.Fatal(err)
	}
	if ready, err := controller.NextReady(time.Second); err != nil || !ready {
		t.Fatalf("partial readiness = %v, %v", ready, err)
	}
	written := make(chan error, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		_, writeErr := peer.Write(body[len(body)/2:])
		written <- writeErr
	}()
	event, err := controller.Next(time.Second)
	if writeErr := <-written; writeErr != nil {
		t.Fatal(writeErr)
	}
	if err != nil || event.Kind != AttemptInnerReady || event.Identity != want {
		t.Fatalf("decoded event = %+v, %v", event, err)
	}
}

func TestAttemptControllerNextReadyTreatsTimeoutAndHUPAsNonConsumingState(t *testing.T) {
	controller, peer, err := NewAttemptController()
	if err != nil {
		t.Fatal(err)
	}
	if ready, err := controller.NextReady(0); err != nil || ready {
		t.Fatalf("nonblocking readiness = %v, %v", ready, err)
	}
	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}
	if ready, err := controller.NextReady(time.Second); err != nil || !ready {
		t.Fatalf("HUP readiness = %v, %v", ready, err)
	}
	if _, err := controller.Next(time.Second); !errors.Is(err, io.EOF) {
		t.Fatalf("HUP Next = %v", err)
	}
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
	if ready, err := controller.NextReady(time.Second); ready || !errors.Is(err, ErrState) {
		t.Fatalf("closed readiness = %v, %v", ready, err)
	}
	if ready, err := (&AttemptController{}).NextReady(-time.Second); ready || !errors.Is(err, ErrState) {
		t.Fatalf("invalid readiness = %v, %v", ready, err)
	}
}
