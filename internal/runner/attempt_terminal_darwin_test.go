//go:build darwin

package runner

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAttemptControllerTerminalCommandsRequireProviderRelease(t *testing.T) {
	controller, peer, err := NewAttemptController()
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	defer peer.Close()

	command := TerminalCommand{Kind: TerminalCredit, Correlation: 1, Credit: 4096}
	if err := controller.SendTerminalCommand(command); !errors.Is(err, ErrState) {
		t.Fatalf("pre-release command = %v", err)
	}
	controller.state = controllerProviderReleased
	if err := controller.SendTerminalCommand(command); err != nil {
		t.Fatal(err)
	}
	var got attemptFrame
	if err := readFrame(peer, &got, maxFrameBytes); err != nil {
		t.Fatal(err)
	}
	if got.Kind != string(TerminalCredit) || got.Correlation != command.Correlation || got.Credit != command.Credit {
		t.Fatalf("command frame = %+v", got)
	}
}

func TestAttemptControllerTerminalEventsInterleaveWithoutLifecycleMutation(t *testing.T) {
	controller, peer, err := NewAttemptController()
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	defer peer.Close()
	controller.state = controllerProviderReleased
	controller.attemptID = "attempt-terminal"
	controller.inner = Identity{PID: 22, PGID: 22, Birth: Birth{Seconds: 3, Microseconds: 4}}

	output := TerminalFrame{Kind: TerminalOutput, Generation: 1, Start: 0, End: 5, Payload: []byte("hello")}
	if err := writeControlFrame(peer, terminalEventFrame(output), maxFrameBytes); err != nil {
		t.Fatal(err)
	}
	event, err := controller.Next(time.Second)
	if err != nil || event.Kind != AttemptTerminalFrame || event.Frame == nil || event.Frame.Kind != TerminalOutput || string(event.Frame.Payload) != "hello" {
		t.Fatalf("output event = %+v, %v", event, err)
	}
	if controller.state != controllerProviderReleased {
		t.Fatalf("terminal stream changed lifecycle state to %d", controller.state)
	}

	if err := controller.SendTerminalCommand(TerminalCommand{Kind: TerminalGenerationRevoke, Correlation: 2, Generation: 1}); err != nil {
		t.Fatal(err)
	}
	var command attemptFrame
	if err := readFrame(peer, &command, maxFrameBytes); err != nil {
		t.Fatal(err)
	}
	if command.Kind != string(TerminalGenerationRevoke) {
		t.Fatalf("command after output = %+v", command)
	}

	identity := FileIdentity{Device: 1, Inode: 2}
	terminal := Terminal{AttemptID: controller.attemptID, Process: controller.inner, Exit: Exit{Code: 0}}
	if err := writeControlFrame(peer, attemptFrame{Version: 1, Kind: "terminal", Terminal: &terminal, FileIdentity: &identity, Digest: strings.Repeat("0", 64)}, maxFrameBytes); err != nil {
		t.Fatal(err)
	}
	event, err = controller.Next(time.Second)
	if err != nil || event.Kind != AttemptTerminal || event.Terminal == nil {
		t.Fatalf("final event = %+v, %v", event, err)
	}
}

func TestAttemptControllerRejectsMalformedTerminalEvents(t *testing.T) {
	bad := []attemptFrame{
		{Version: 1, Kind: string(TerminalOutput), Generation: 1, Start: 0, End: 2, Payload: []byte("x")},
		{Version: 1, Kind: string(TerminalReset), Correlation: 1, Generation: 1, Floor: 3, Head: 2},
		{Version: 1, Kind: string(TerminalPTYEOF), Generation: 1, Payload: []byte("bytes")},
	}
	for _, frame := range bad {
		controller, peer, err := NewAttemptController()
		if err != nil {
			t.Fatal(err)
		}
		controller.state = controllerProviderReleased
		if err := writeControlFrame(peer, frame, maxFrameBytes); err != nil {
			t.Fatal(err)
		}
		if _, err := controller.Next(time.Second); !errors.Is(err, ErrState) {
			t.Fatalf("accepted malformed frame %+v: %v", frame, err)
		}
		peer.Close()
		controller.Close()
	}
}
