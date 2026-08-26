package runner

import (
	"bytes"
	"errors"
	"io"
	"syscall"
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

type errnoWriter struct {
	partial int
	err     error
}

func (w errnoWriter) Write(p []byte) (int, error) {
	if w.partial > len(p) {
		w.partial = len(p)
	}
	return w.partial, w.err
}

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

func TestWriteFullySurfacesWouldBlockBeforeAndAfterProgress(t *testing.T) {
	if err := writeFully(errnoWriter{err: syscall.EAGAIN}, []byte("header")); !errors.Is(err, syscall.EAGAIN) {
		t.Fatalf("would-block before progress = %v", err)
	}
	if err := writeFully(errnoWriter{partial: 1, err: syscall.EAGAIN}, []byte("body")); !errors.Is(err, syscall.EAGAIN) {
		t.Fatalf("would-block after progress = %v", err)
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
		{Kind: TerminalResize, Correlation: 1, Generation: 1, Rows: 4097, Cols: 80},
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

func TestTerminalResizeUsesPTYOwnerBounds(t *testing.T) {
	valid := TerminalCommand{Kind: TerminalResize, Correlation: 1, Generation: 1, Rows: maxPTYDimension, Cols: maxPTYDimension}
	if err := valid.validate(); err != nil {
		t.Fatalf("owner-bound resize rejected: %v", err)
	}
	for _, version := range []int{0, commandVersion - 1, commandVersion + 1, 99} {
		frame := terminalCommandFrame(TerminalCommand{Kind: TerminalCredit, Correlation: 1, Credit: 1})
		frame.Version = version
		if _, err := terminalCommandFromFrame(frame); !errors.Is(err, ErrIdentity) {
			t.Fatalf("command version %d accepted: %v", version, err)
		}
		event := terminalEventFrame(TerminalFrame{Kind: TerminalPTYEOF, Generation: 1})
		event.Version = version
		if _, err := terminalEventFromFrame(event); !errors.Is(err, ErrIdentity) {
			t.Fatalf("event version %d accepted: %v", version, err)
		}
	}
}

func TestTerminalConversionsRejectLifecycleAndCrossUnionFields(t *testing.T) {
	commandBase := terminalCommandFrame(TerminalCommand{Kind: TerminalCredit, Correlation: 1, Credit: 1})
	commandContaminations := []struct {
		name   string
		mutate func(*attemptFrame)
	}{
		{"stage", func(f *attemptFrame) { f.Stage = StageProvider }},
		{"identity", func(f *attemptFrame) { f.Identity = Identity{PID: 2, PGID: 2, Birth: Birth{Seconds: 1}} }},
		{"terminal", func(f *attemptFrame) { f.Terminal = &Terminal{} }},
		{"file_identity", func(f *attemptFrame) { f.FileIdentity = &FileIdentity{Device: 1, Inode: 1} }},
		{"digest", func(f *attemptFrame) { f.Digest = "digest" }},
		{"store_committed", func(f *attemptFrame) { f.StoreCommitted = true }},
		{"generation", func(f *attemptFrame) { f.Generation = 1 }},
		{"sequence", func(f *attemptFrame) { f.Sequence = 1 }},
		{"start", func(f *attemptFrame) { f.Start = 1 }},
		{"end", func(f *attemptFrame) { f.End = 1 }},
		{"floor", func(f *attemptFrame) { f.Floor = 1 }},
		{"head", func(f *attemptFrame) { f.Head = 1 }},
		{"count", func(f *attemptFrame) { f.Count = 1 }},
		{"rows", func(f *attemptFrame) { f.Rows = 1 }},
		{"cols", func(f *attemptFrame) { f.Cols = 1 }},
		{"status", func(f *attemptFrame) { f.Status = "ok" }},
		{"payload", func(f *attemptFrame) { f.Payload = []byte("unexpected") }},
	}
	for _, test := range commandContaminations {
		t.Run("command/"+test.name, func(t *testing.T) {
			frame := commandBase
			test.mutate(&frame)
			if _, err := terminalCommandFromFrame(frame); err == nil {
				t.Fatalf("accepted contaminated command frame %+v", frame)
			}
		})
	}

	eventBase := terminalEventFrame(TerminalFrame{Kind: TerminalPTYEOF, Generation: 1})
	eventContaminations := []struct {
		name   string
		mutate func(*attemptFrame)
	}{
		{"stage", func(f *attemptFrame) { f.Stage = StageProvider }},
		{"identity", func(f *attemptFrame) { f.Identity = Identity{PID: 2, PGID: 2, Birth: Birth{Seconds: 1}} }},
		{"terminal", func(f *attemptFrame) { f.Terminal = &Terminal{} }},
		{"file_identity", func(f *attemptFrame) { f.FileIdentity = &FileIdentity{Device: 1, Inode: 1} }},
		{"digest", func(f *attemptFrame) { f.Digest = "digest" }},
		{"store_committed", func(f *attemptFrame) { f.StoreCommitted = true }},
		{"correlation", func(f *attemptFrame) { f.Correlation = 1 }},
		{"sequence", func(f *attemptFrame) { f.Sequence = 1 }},
		{"start", func(f *attemptFrame) { f.Start = 1 }},
		{"end", func(f *attemptFrame) { f.End = 1 }},
		{"floor", func(f *attemptFrame) { f.Floor = 1 }},
		{"head", func(f *attemptFrame) { f.Head = 1 }},
		{"count", func(f *attemptFrame) { f.Count = 1 }},
		{"rows", func(f *attemptFrame) { f.Rows = 1 }},
		{"cols", func(f *attemptFrame) { f.Cols = 1 }},
		{"credit", func(f *attemptFrame) { f.Credit = 1 }},
		{"status", func(f *attemptFrame) { f.Status = "ok" }},
		{"payload", func(f *attemptFrame) { f.Payload = []byte("unexpected") }},
	}
	for _, test := range eventContaminations {
		t.Run("event/"+test.name, func(t *testing.T) {
			frame := eventBase
			test.mutate(&frame)
			if _, err := terminalEventFromFrame(frame); err == nil {
				t.Fatalf("accepted contaminated event frame %+v", frame)
			}
		})
	}
}

func TestTerminalAcknowledgementsRejectEveryTerminalField(t *testing.T) {
	record := &TerminalRecord{Terminal: Terminal{AttemptID: "attempt", Process: Identity{PID: 2, PGID: 2, Birth: Birth{Seconds: 1}}}, Identity: FileIdentity{Device: 1, Inode: 2}, Digest: "digest"}
	current := attemptFrame{Version: commandVersion, Kind: "current-exec-check-ack"}
	terminal := attemptFrame{Version: commandVersion, Kind: "terminal-ack", Terminal: &record.Terminal, FileIdentity: &record.Identity, Digest: record.Digest, StoreCommitted: true}
	mutations := []struct {
		name   string
		mutate func(*attemptFrame)
	}{
		{"stage", func(f *attemptFrame) { f.Stage = StageProvider }},
		{"identity", func(f *attemptFrame) { f.Identity = Identity{PID: 2, PGID: 2, Birth: Birth{Seconds: 1}} }},
		{"terminal", func(f *attemptFrame) { f.Terminal = &Terminal{} }},
		{"file_identity", func(f *attemptFrame) { f.FileIdentity = &FileIdentity{Device: 1, Inode: 1} }},
		{"digest", func(f *attemptFrame) { f.Digest = "other" }},
		{"store_committed", func(f *attemptFrame) { f.StoreCommitted = !f.StoreCommitted }},
		{"correlation", func(f *attemptFrame) { f.Correlation = 1 }},
		{"generation", func(f *attemptFrame) { f.Generation = 1 }},
		{"sequence", func(f *attemptFrame) { f.Sequence = 1 }},
		{"start", func(f *attemptFrame) { f.Start = 1 }},
		{"end", func(f *attemptFrame) { f.End = 1 }},
		{"floor", func(f *attemptFrame) { f.Floor = 1 }},
		{"head", func(f *attemptFrame) { f.Head = 1 }},
		{"count", func(f *attemptFrame) { f.Count = 1 }},
		{"rows", func(f *attemptFrame) { f.Rows = 1 }},
		{"cols", func(f *attemptFrame) { f.Cols = 1 }},
		{"credit", func(f *attemptFrame) { f.Credit = 1 }},
		{"status", func(f *attemptFrame) { f.Status = "ok" }},
	}
	for _, test := range mutations {
		t.Run("current/"+test.name, func(t *testing.T) {
			frame := current
			test.mutate(&frame)
			if validCurrentExecCheckAck(frame) {
				t.Fatalf("accepted contaminated current-exec ACK %+v", frame)
			}
		})
		t.Run("terminal/"+test.name, func(t *testing.T) {
			frame := terminal
			test.mutate(&frame)
			if validTerminalAck(frame, record) {
				t.Fatalf("accepted contaminated terminal ACK %+v", frame)
			}
		})
	}
	if !validCurrentExecCheckAck(current) || !validTerminalAck(terminal, record) {
		t.Fatal("valid ACK baseline rejected")
	}
}
