//go:build darwin

package runner

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

// Callers read io.EOF out of readFrame as proof that the peer closed the
// stream, and act on it irreversibly. A complete frame whose body carries no
// JSON value must therefore never be able to produce that answer, or a live
// peer sending one malformed frame is indistinguishable from a dead one.
func TestReadFrameValuelessBodyIsNotStreamEOF(t *testing.T) {
	for _, body := range []string{"   ", "\n\n", "\t"} {
		var buf bytes.Buffer
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], uint32(len(body)))
		buf.Write(header[:])
		buf.WriteString(body)
		var frame attemptFrame
		err := readFrame(&buf, &frame, maxFrameBytes)
		if err == nil {
			t.Fatalf("valueless body %q was accepted as a frame", body)
		}
		if errors.Is(err, io.EOF) {
			t.Fatalf("complete frame with body %q reported stream EOF: %v", body, err)
		}
	}
}

// The transport EOF this relies on still has to survive: an actually closed
// stream, and only that, answers io.EOF.
func TestReadFrameClosedStreamIsStreamEOF(t *testing.T) {
	var frame attemptFrame
	if err := readFrame(bytes.NewReader(nil), &frame, maxFrameBytes); !errors.Is(err, io.EOF) {
		t.Fatalf("closed stream = %v, want EOF", err)
	}
	var partial attemptFrame
	if err := readFrame(bytes.NewReader([]byte{0, 0, 0}), &partial, maxFrameBytes); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("partial header = %v, want unexpected EOF", err)
	}
}
