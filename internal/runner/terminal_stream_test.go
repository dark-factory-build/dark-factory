package runner

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"testing"
)

func encodedAttemptFrames(t *testing.T, frames ...attemptFrame) []byte {
	t.Helper()
	var wire bytes.Buffer
	for _, frame := range frames {
		if err := writeFrame(&wire, frame, maxFrameBytes); err != nil {
			t.Fatal(err)
		}
	}
	return wire.Bytes()
}

func TestAttemptFrameDecoderRetainsEveryFragmentAndEmptyFeed(t *testing.T) {
	want := []attemptFrame{
		{Version: commandVersion, Kind: "terminal", Payload: []byte("first")},
		{Version: commandVersion, Kind: "terminal-frame", Sequence: 7, Payload: []byte("second")},
	}
	wire := encodedAttemptFrames(t, want...)
	decoder, err := newAttemptFrameDecoder(maxFrameBytes)
	if err != nil {
		t.Fatal(err)
	}
	var got []attemptFrame
	for i := range wire {
		frames, err := decoder.Feed(wire[i : i+1])
		if err != nil {
			t.Fatalf("byte %d: %v", i, err)
		}
		got = append(got, frames...)
		if frames, err := decoder.Feed(nil); err != nil || len(frames) != 0 {
			t.Fatalf("empty feed = frames %d err %v", len(frames), err)
		}
	}
	if len(got) != len(want) || !bytes.Equal(got[0].Payload, want[0].Payload) || !bytes.Equal(got[1].Payload, want[1].Payload) || got[1].Sequence != 7 {
		t.Fatalf("frames = %+v, want %+v", got, want)
	}
}

func TestAttemptFrameDecoderParsesCoalescedFrames(t *testing.T) {
	want := []attemptFrame{{Version: commandVersion, Kind: "one"}, {Version: commandVersion, Kind: "two"}, {Version: commandVersion, Kind: "three"}}
	decoder, err := newAttemptFrameDecoder(maxFrameBytes)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decoder.Feed(encodedAttemptFrames(t, want...))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("decoded %d frames, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Kind != want[i].Kind {
			t.Errorf("frame %d kind = %q, want %q", i, got[i].Kind, want[i].Kind)
		}
	}
}

func TestAttemptFrameDecoderRejectsLengthBeforeAllocationAndPoisons(t *testing.T) {
	for _, size := range []uint32{0, maxFrameBytes + 1} {
		decoder, err := newAttemptFrameDecoder(maxFrameBytes)
		if err != nil {
			t.Fatal(err)
		}
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], size)
		if _, err := decoder.Feed(header[:]); err == nil {
			t.Fatalf("size %d accepted", size)
		}
		if decoder.body != nil || decoder.bodyRead != 0 {
			t.Fatalf("size %d allocated body after rejection", size)
		}
		if _, err := decoder.Feed(nil); !errors.Is(err, errFrameDecoderPoisoned) {
			t.Fatalf("size %d did not remain poisoned: %v", size, err)
		}
	}
}

func TestAttemptFrameDecoderSharesStrictJSONBodyValidation(t *testing.T) {
	cases := [][]byte{
		[]byte(`{"version":1,"kind":"ok","unknown":true}`),
		[]byte(`{"version":1,"kind":"ok"}{"version":1,"kind":"again"}`),
		[]byte(`{"version":1,"kind":`),
	}
	for _, body := range cases {
		decoder, err := newAttemptFrameDecoder(maxFrameBytes)
		if err != nil {
			t.Fatal(err)
		}
		wire := make([]byte, 4+len(body))
		binary.BigEndian.PutUint32(wire[:4], uint32(len(body)))
		copy(wire[4:], body)
		if _, err := decoder.Feed(wire); err == nil {
			t.Fatalf("invalid body accepted: %s", body)
		}
	}
}

func TestAttemptFrameDecoderBoundsCompletedFramesPerFeed(t *testing.T) {
	frames := make([]attemptFrame, maxFrameDecoderFrames+1)
	for i := range frames {
		frames[i] = attemptFrame{Version: commandVersion, Kind: "frame"}
	}
	decoder, err := newAttemptFrameDecoder(maxFrameBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.Feed(encodedAttemptFrames(t, frames...)); err == nil {
		t.Fatal("frame flood accepted")
	}
	if _, err := decoder.Feed(nil); !errors.Is(err, errFrameDecoderPoisoned) {
		t.Fatalf("flood decoder did not remain poisoned: %v", err)
	}
}

func TestTerminalByteRingInitialAndCursorBoundaries(t *testing.T) {
	var ring terminalByteRing
	if ring.Floor() != 0 || ring.Head() != 0 {
		t.Fatalf("initial cursors = %d/%d", ring.Floor(), ring.Head())
	}
	chunk, next, err := ring.Read(0)
	if err != nil || len(chunk) != 0 || next != 0 {
		t.Fatalf("empty read = %q/%d/%v", chunk, next, err)
	}
	if _, _, err := ring.Read(1); !errors.Is(err, errTerminalReplayFuture) {
		t.Fatalf("future cursor = %v", err)
	}
	if err := ring.Append([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	chunk, next, err = ring.Read(0)
	if err != nil || string(chunk) != "hello" || next != 5 {
		t.Fatalf("exact read = %q/%d/%v", chunk, next, err)
	}
	if _, _, err := ring.Read(6); !errors.Is(err, errTerminalReplayFuture) {
		t.Fatalf("future cursor after append = %v", err)
	}
}

func TestTerminalByteRingWrapAndFloor(t *testing.T) {
	seed := bytes.Repeat([]byte{'s'}, terminalReplayCapacity-2)
	var ring terminalByteRing
	if err := ring.Append(seed); err != nil {
		t.Fatal(err)
	}
	if err := ring.Append([]byte("abcdef")); err != nil {
		t.Fatal(err)
	}
	if ring.Floor() != 4 || ring.Head() != terminalReplayCapacity+4 {
		t.Fatalf("cursors = %d/%d", ring.Floor(), ring.Head())
	}
	var got []byte
	cursor := ring.Floor()
	for cursor < ring.Head() {
		chunk, next, err := ring.Read(cursor)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, chunk...)
		cursor = next
	}
	if len(got) != terminalReplayCapacity || string(got[len(got)-4:]) != "cdef" {
		t.Fatalf("wrapped tail length/suffix = %d/%q", len(got), got[len(got)-4:])
	}
	if _, _, err := ring.Read(3); !errors.Is(err, errTerminalReplayReset) {
		t.Fatalf("below floor = %v", err)
	}
}

func TestTerminalByteRingChunksAndOversizedAppendRetainsTail(t *testing.T) {
	payload := bytes.Repeat([]byte{'x'}, terminalReplayCapacity+123)
	var ring terminalByteRing
	if err := ring.Append(payload); err != nil {
		t.Fatal(err)
	}
	if ring.Floor() != uint64(len(payload))-terminalReplayCapacity || ring.Head() != uint64(len(payload)) {
		t.Fatalf("oversized cursors = %d/%d", ring.Floor(), ring.Head())
	}
	var got []byte
	cursor := ring.Floor()
	for cursor < ring.Head() {
		chunk, next, err := ring.Read(cursor)
		if err != nil {
			t.Fatal(err)
		}
		if len(chunk) == 0 || len(chunk) > terminalReplayChunk || next <= cursor {
			t.Fatalf("bad chunk at %d: len=%d next=%d", cursor, len(chunk), next)
		}
		got = append(got, chunk...)
		cursor = next
	}
	want := payload[len(payload)-terminalReplayCapacity:]
	if !bytes.Equal(got, want) {
		t.Fatalf("tail mismatch: got %d bytes", len(got))
	}
}

func TestTerminalByteRingOverflowIsAtomic(t *testing.T) {
	var ring terminalByteRing
	ring.floor = math.MaxUint64 - 1
	ring.head = math.MaxUint64 - 1
	ring.bytes[0] = 7
	if err := ring.Append([]byte{1, 2}); !errors.Is(err, errTerminalReplaySize) {
		t.Fatalf("overflow = %v", err)
	}
	if ring.floor != math.MaxUint64-1 || ring.head != math.MaxUint64-1 || ring.bytes[0] != 7 {
		t.Fatal("overflow mutated ring")
	}
}
