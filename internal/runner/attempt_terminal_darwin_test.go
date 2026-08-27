//go:build darwin

package runner

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func saturateControllerSendBuffer(t *testing.T, controller *AttemptController) {
	t.Helper()
	if err := unix.SetsockoptInt(int(controller.file.Fd()), unix.SOL_SOCKET, unix.SO_SNDBUF, 1024); err != nil {
		t.Fatal(err)
	}
	filler := make([]byte, 4096)
	for {
		_, err := unix.Write(int(controller.file.Fd()), filler)
		if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestAttemptControllerWriteFailurePoisonsCapability(t *testing.T) {
	controller, peer, err := NewAttemptController()
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	controller.state = controllerProviderReleased
	controller.terminalReady = true
	saturateControllerSendBuffer(t, controller)
	started := time.Now()
	err = controller.SendTerminalCommand(TerminalCommand{Kind: TerminalCredit, Credit: 1})
	if !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("saturated write = %v, want EAGAIN or deadline", err)
	}
	if elapsed := time.Since(started); elapsed > attemptControlTimeout+time.Second {
		t.Fatalf("EAGAIN write took %s; nonblocking failure must be bounded", elapsed)
	}
	if controller.file != nil || controller.state != controllerPoisoned {
		t.Fatalf("failed writer remained usable: file=%v state=%d", controller.file, controller.state)
	}
	if err := controller.SendTerminalCommand(TerminalCommand{Kind: TerminalCredit, Credit: 1}); !errors.Is(err, ErrState) {
		t.Fatalf("retry after poison = %v", err)
	}
	if err := controller.Terminate(); !errors.Is(err, ErrState) {
		t.Fatalf("terminate after poison = %v", err)
	}
	var frame attemptFrame
	if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := readFrame(peer, &frame, maxFrameBytes); err == nil {
		t.Fatal("poisoned controller appended a decodable frame")
	}
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAttemptControllerTerminalCommandsRequireProviderRelease(t *testing.T) {
	controller, peer, err := NewAttemptController()
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	defer peer.Close()

	command := TerminalCommand{Kind: TerminalCredit, Credit: 4096}
	if err := controller.SendTerminalCommand(command); !errors.Is(err, ErrState) {
		t.Fatalf("pre-release command = %v", err)
	}
	controller.state = controllerProviderReleased
	if err := controller.SendTerminalCommand(command); !errors.Is(err, ErrState) {
		t.Fatalf("pre-ready command = %v", err)
	}
	controller.terminalReady = true
	if err := controller.SendTerminalCommand(command); err != nil {
		t.Fatal(err)
	}
	var got attemptFrame
	if err := readFrame(peer, &got, maxFrameBytes); err != nil {
		t.Fatal(err)
	}
	if got.Kind != string(TerminalCredit) || got.Correlation != 0 || got.Credit != command.Credit {
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
	controller.terminalReady = true
	controller.attemptID = "attempt-terminal"
	controller.inner = Identity{PID: 22, PGID: 22, Birth: Birth{Seconds: 3, Microseconds: 4}}

	output := TerminalFrame{Kind: TerminalOutput, Start: 0, End: 5, Payload: []byte("hello")}
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

func TestAttemptControllerAcceptsAttachResultsWithoutLifecycleMutation(t *testing.T) {
	controller, peer, err := NewAttemptController()
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	defer peer.Close()
	controller.state = controllerProviderReleased
	controller.terminalReady = true

	frames := []TerminalFrame{
		{Kind: TerminalAttached, Correlation: 1, Sequence: 8, Floor: 2, Head: 8, Status: TerminalResultOK},
		{Kind: TerminalAttached, Correlation: 2, Sequence: 9, Floor: 2, Head: 8, Status: TerminalResultRejected},
	}
	for _, want := range frames {
		if err := writeControlFrame(peer, terminalEventFrame(want), maxFrameBytes); err != nil {
			t.Fatal(err)
		}
		got, err := controller.Next(time.Second)
		if err != nil || got.Kind != AttemptTerminalFrame || got.Frame == nil || !reflect.DeepEqual(*got.Frame, want) {
			t.Fatalf("attach event = %+v, err=%v, want %+v", got, err, want)
		}
		if controller.state != controllerProviderReleased {
			t.Fatalf("attach result changed lifecycle state to %d", controller.state)
		}
	}
}

func TestEveryDeclaredTerminalEventKindIsHandled(t *testing.T) {
	// Keep this table beside the closed union. Adding a TerminalEventKind
	// requires adding its constructor here and its case to isTerminalEventKind;
	// otherwise the controller can silently reject a valid event.
	declared := []TerminalEventKind{
		TerminalGenerationResult,
		TerminalInputResult,
		TerminalResizeResult,
		TerminalAttached,
		TerminalOutput,
		TerminalReset,
		TerminalReady,
		TerminalPTYEOF,
	}
	for _, kind := range declared {
		if !isTerminalEventKind(string(kind)) {
			t.Fatalf("declared terminal event kind %q is not accepted by controller", kind)
		}
	}
}

func TestAttemptControllerRejectsMalformedTerminalEvents(t *testing.T) {
	bad := []attemptFrame{
		{Version: 1, Kind: string(TerminalOutput), Start: 0, End: 2, Payload: []byte("x")},
		{Version: 1, Kind: string(TerminalAttached), Correlation: 1, Sequence: 9, Floor: 2, Head: 8, Status: "ok"},
		{Version: 1, Kind: string(TerminalAttached), Correlation: 1, Sequence: 8, Floor: 2, Head: 8, Status: "partial"},
		{Version: 1, Kind: string(TerminalReset), Correlation: 1, Generation: 1, Floor: 3, Head: 2},
		{Version: 1, Kind: string(TerminalPTYEOF), Payload: []byte("bytes")},
	}
	for _, frame := range bad {
		controller, peer, err := NewAttemptController()
		if err != nil {
			t.Fatal(err)
		}
		controller.state = controllerProviderReleased
		controller.terminalReady = true
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
