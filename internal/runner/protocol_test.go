package runner

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

type shortWriter struct {
	buf bytes.Buffer
	max int
}

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) > w.max {
		p = p[:w.max]
	}
	return w.buf.Write(p)
}

type noProgressWriter struct{}

func (noProgressWriter) Write([]byte) (int, error) { return 0, nil }

func TestWriteFrameCompletesShortHeaderAndBody(t *testing.T) {
	var wire shortWriter
	wire.max = 1
	want := gateFrame{Kind: "ready", Error: "short writes are not a frame boundary"}
	if err := writeFrame(&wire, want, maxFrameBytes); err != nil {
		t.Fatal(err)
	}
	var got gateFrame
	if err := readFrame(bytes.NewReader(wire.buf.Bytes()), &got, maxFrameBytes); err != nil {
		t.Fatal(err)
	}
	if got.Kind != want.Kind || got.Error != want.Error {
		t.Fatalf("frame = %+v, want %+v", got, want)
	}
}

func TestWriteFrameRejectsZeroProgress(t *testing.T) {
	if err := writeFrame(noProgressWriter{}, gateFrame{Kind: "ready"}, maxFrameBytes); !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("zero-progress write = %v, want io.ErrNoProgress", err)
	}
}

func TestTerminalCommandAndEventFramesRoundTrip(t *testing.T) {
	commands := []TerminalCommand{
		{Kind: TerminalGenerationInstall, Correlation: 1, Generation: 2},
		{Kind: TerminalGenerationRevoke, Correlation: 2, Generation: 2},
		{Kind: TerminalAttach, Correlation: 3, Sequence: 4},
		{Kind: TerminalCredit, Correlation: 4, Credit: 4096},
		{Kind: TerminalInput, Correlation: 5, Generation: 2, Sequence: 1, Payload: []byte("input")},
		{Kind: TerminalResize, Correlation: 6, Generation: 2, Rows: 24, Cols: 80},
	}
	for _, want := range commands {
		if err := want.validate(); err != nil {
			t.Fatalf("command %q rejected: %v", want.Kind, err)
		}
		var wire bytes.Buffer
		if err := writeFrame(&wire, terminalCommandFrame(want), maxFrameBytes); err != nil {
			t.Fatal(err)
		}
		var raw attemptFrame
		if err := readFrame(&wire, &raw, maxFrameBytes); err != nil {
			t.Fatal(err)
		}
		got, err := terminalCommandFromFrame(raw)
		if err != nil || got.Kind != want.Kind || got.Correlation != want.Correlation || got.Generation != want.Generation || got.Sequence != want.Sequence || got.Credit != want.Credit || got.Rows != want.Rows || got.Cols != want.Cols || !bytes.Equal(got.Payload, want.Payload) {
			t.Fatalf("command = %+v, err=%v, want %+v", got, err, want)
		}
	}

	events := []TerminalFrame{
		{Kind: TerminalGenerationResult, Correlation: 7, Generation: 2, Status: TerminalResultOK},
		{Kind: TerminalInputResult, Correlation: 8, Generation: 2, Sequence: 1, Status: TerminalResultPartial, Count: 3},
		{Kind: TerminalResizeResult, Correlation: 9, Generation: 2, Rows: 24, Cols: 80, Status: TerminalResultOK},
		{Kind: TerminalOutput, Generation: 2, Start: 10, End: 15, Payload: []byte("hello")},
		{Kind: TerminalReset, Correlation: 10, Generation: 2, Floor: 10, Head: 15},
		{Kind: TerminalPTYEOF, Generation: 2},
	}
	for _, want := range events {
		if err := want.validate(); err != nil {
			t.Fatalf("event %q rejected: %v", want.Kind, err)
		}
		var wire bytes.Buffer
		if err := writeFrame(&wire, terminalEventFrame(want), maxFrameBytes); err != nil {
			t.Fatal(err)
		}
		var raw attemptFrame
		if err := readFrame(&wire, &raw, maxFrameBytes); err != nil {
			t.Fatal(err)
		}
		got, err := terminalEventFromFrame(raw)
		if err != nil || got.Kind != want.Kind || got.Correlation != want.Correlation || got.Generation != want.Generation || got.Sequence != want.Sequence || got.Start != want.Start || got.End != want.End || got.Floor != want.Floor || got.Head != want.Head || got.Count != want.Count || got.Rows != want.Rows || got.Cols != want.Cols || got.Status != want.Status || !bytes.Equal(got.Payload, want.Payload) {
			t.Fatalf("event = %+v, err=%v, want %+v", got, err, want)
		}
	}
}

func TestTerminalWireValidationRejectsAmbiguousFrames(t *testing.T) {
	badCommands := []TerminalCommand{
		{Kind: TerminalInput, Correlation: 1, Generation: 1, Sequence: 1},
		{Kind: TerminalResize, Correlation: 1, Generation: 1, Rows: 0, Cols: 80},
		{Kind: TerminalCredit, Correlation: 1, Credit: maxTerminalCredit + 1},
		{Kind: TerminalInput, Correlation: 1, Generation: 1, Sequence: 1, Payload: bytes.Repeat([]byte{'x'}, maxTerminalFramePayload+1)},
		{Kind: "terminal-unknown", Correlation: 1},
	}
	for _, command := range badCommands {
		if err := command.validate(); err == nil {
			t.Fatalf("accepted invalid command %+v", command)
		}
	}
	badEvents := []TerminalFrame{
		{Kind: TerminalOutput, Generation: 1, Start: 2, End: 4, Payload: []byte("x")},
		{Kind: TerminalReset, Correlation: 1, Generation: 1, Floor: 3, Head: 2},
		{Kind: TerminalGenerationResult, Correlation: 1, Generation: 1, Status: "unknown"},
		{Kind: TerminalPTYEOF, Generation: 1, Payload: []byte("unexpected")},
		{Kind: "terminal-unknown", Generation: 1},
	}
	for _, event := range badEvents {
		if err := event.validate(); err == nil {
			t.Fatalf("accepted invalid event %+v", event)
		}
	}
}
