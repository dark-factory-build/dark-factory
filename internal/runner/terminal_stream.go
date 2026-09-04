package runner

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
)

const (
	// terminalReplayCapacity is deliberately fixed. A live terminal has one
	// replay authority and its memory use must not depend on client behaviour.
	terminalReplayCapacity = 1 << 20
	terminalReplayChunk    = 8 << 10
	// Attach requests are serialized on the owner loop and this queue is a
	// bounded handoff to the single live ring. A peer that never grants credit
	// cannot consume unbounded owner memory.
	terminalReplayRequestCapacity = 32
	maxFrameDecoderFrames         = 64
)

var (
	errTerminalReplayReset  = errors.New("runner: terminal replay cursor is below the retained floor")
	errTerminalReplayFuture = errors.New("runner: terminal replay cursor is beyond the head")
	errTerminalReplaySize   = errors.New("runner: terminal replay head would overflow")
	errFrameDecoderPoisoned = errors.New("runner: frame decoder is poisoned")
)

// attemptFrameDecoder incrementally consumes the private length-prefixed JSON
// protocol. It is owned by one reader; Feed is intentionally synchronous.
// Header and body state survive an empty feed (the representation of EAGAIN).
type attemptFrameDecoder struct {
	limit      int
	header     [4]byte
	headerRead int
	body       []byte
	bodyRead   int
	poisoned   error
}

func newAttemptFrameDecoder(limit int) (*attemptFrameDecoder, error) {
	if limit <= 0 || limit > maxFrameBytes {
		return nil, fmt.Errorf("runner: invalid decoder limit %d", limit)
	}
	return &attemptFrameDecoder{limit: limit}, nil
}

// Feed returns every complete frame made available by p. A fatal framing or
// JSON error poisons the decoder: callers must close the capability rather
// than trying to resynchronize at an arbitrary byte boundary.
func (d *attemptFrameDecoder) Feed(p []byte) ([]attemptFrame, error) {
	if d == nil {
		return nil, ErrIdentity
	}
	if d.poisoned != nil {
		return nil, d.poisoned
	}
	frames := make([]attemptFrame, 0, 1)
	for len(p) != 0 {
		if d.headerRead != len(d.header) {
			n := copy(d.header[d.headerRead:], p)
			d.headerRead += n
			p = p[n:]
			if d.headerRead != len(d.header) {
				continue
			}
			size := binary.BigEndian.Uint32(d.header[:])
			if size == 0 || uint64(size) > uint64(d.limit) {
				return nil, d.fail(fmt.Errorf("runner: invalid frame size %d", size))
			}
			d.body = make([]byte, int(size))
			d.bodyRead = 0
		}

		n := copy(d.body[d.bodyRead:], p)
		d.bodyRead += n
		p = p[n:]
		if d.bodyRead != len(d.body) {
			continue
		}
		if len(frames) == maxFrameDecoderFrames {
			return nil, d.fail(fmt.Errorf("runner: too many frames in one feed"))
		}
		var frame attemptFrame
		if err := decodeFrameBody(d.body, &frame); err != nil {
			return nil, d.fail(err)
		}
		frames = append(frames, frame)
		d.headerRead = 0
		d.body = nil
		d.bodyRead = 0
	}
	return frames, nil
}

func (d *attemptFrameDecoder) fail(err error) error {
	d.poisoned = errors.Join(errFrameDecoderPoisoned, err)
	return d.poisoned
}

// terminalByteRing is the sole live scrollback copy. Sequence numbers are
// half-open byte ranges [floor, head). The backing array is never resized.
type terminalByteRing struct {
	bytes [terminalReplayCapacity]byte
	floor uint64
	head  uint64
}

func (r *terminalByteRing) Append(p []byte) error {
	if r == nil {
		return ErrIdentity
	}
	if uint64(len(p)) > math.MaxUint64-r.head {
		return errTerminalReplaySize
	}
	newHead := r.head + uint64(len(p))
	newFloor := uint64(0)
	if newHead > terminalReplayCapacity {
		newFloor = newHead - terminalReplayCapacity
	}
	// Calculate all failure cases before changing either cursor or byte data.
	if len(p) == 0 {
		r.floor, r.head = newFloor, newHead
		return nil
	}
	if len(p) >= terminalReplayCapacity {
		p = p[len(p)-terminalReplayCapacity:]
	}
	start := newHead - uint64(len(p))
	copy(r.bytes[start%terminalReplayCapacity:], p[:min(len(p), terminalReplayCapacity-int(start%terminalReplayCapacity))])
	if len(p) > terminalReplayCapacity-int(start%terminalReplayCapacity) {
		copy(r.bytes[:], p[terminalReplayCapacity-int(start%terminalReplayCapacity):])
	}
	r.floor, r.head = newFloor, newHead
	return nil
}

// Read returns one bounded chunk starting at cursor and the next cursor. A
// returned chunk is a snapshot for the caller's frame; it is not another
// retained ring. cursor==head is a successful empty read.
func (r *terminalByteRing) Read(cursor uint64) ([]byte, uint64, error) {
	if r == nil {
		return nil, 0, ErrIdentity
	}
	if cursor < r.floor {
		return nil, r.head, errTerminalReplayReset
	}
	if cursor > r.head {
		return nil, r.head, errTerminalReplayFuture
	}
	if cursor == r.head {
		return nil, cursor, nil
	}
	n := r.head - cursor
	if n > terminalReplayChunk {
		n = terminalReplayChunk
	}
	chunk := make([]byte, int(n))
	offset := int(cursor % terminalReplayCapacity)
	first := min(len(chunk), terminalReplayCapacity-offset)
	copy(chunk, r.bytes[offset:offset+first])
	if first != len(chunk) {
		copy(chunk[first:], r.bytes[:len(chunk)-first])
	}
	return chunk, cursor + n, nil
}

func (r *terminalByteRing) Floor() uint64 {
	return r.floor
}

func (r *terminalByteRing) Head() uint64 {
	return r.head
}

// decodeFrameBody is shared by blocking lifecycle reads and the incremental
// terminal reader, so strict unknown/trailing JSON handling cannot drift.
func decodeFrameBody(body []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		// The decoder reports a body holding no value at all as io.EOF, and one
		// that stops mid-value as io.ErrUnexpectedEOF. Both describe this
		// buffer, never the stream it arrived on, yet callers read exactly
		// those two errors from a frame read as proof the peer is gone. Neither
		// may escape wearing a transport error's name.
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return fmt.Errorf("runner: malformed frame body: %v", err)
		}
		return err
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); err != io.EOF {
		return errorsNewTrailing(err)
	}
	return nil
}
