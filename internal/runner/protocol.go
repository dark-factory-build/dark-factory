package runner

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

type gateConfig struct {
	Version        int              `json:"version"`
	Target         launchCommitment `json:"target"`
	LeaseDirectory fileCommitment   `json:"lease_directory"`
	MarkerName     string           `json:"marker_name"`
	KeepLease      bool             `json:"keep_lease"`
}

type gateFrame struct {
	Kind     string        `json:"kind"`
	Identity Identity      `json:"identity,omitempty"`
	Marker   *FileIdentity `json:"marker,omitempty"`
	Error    string        `json:"error,omitempty"`
}

func writeFrame(w io.Writer, value any, limit int) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(body) == 0 || len(body) > limit {
		return fmt.Errorf("runner: frame size %d", len(body))
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(body)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

func readFrame(r io.Reader, dst any, limit int) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	n := int(binary.BigEndian.Uint32(header[:]))
	if n <= 0 || n > limit {
		return fmt.Errorf("runner: invalid frame size %d", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return err
	}
	dec := json.NewDecoder(newBytesReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); err != io.EOF {
		return errorsNewTrailing(err)
	}
	return nil
}

type byteReader struct{ b []byte }

func newBytesReader(b []byte) *byteReader { return &byteReader{b: b} }
func (r *byteReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.b)
	r.b = r.b[n:]
	return n, nil
}
func errorsNewTrailing(err error) error { return fmt.Errorf("runner: trailing frame data: %v", err) }
