package browserprotocol

import (
	"encoding/binary"
	"fmt"
)

const (
	TerminalHeaderSize           = 40
	MaxTerminalPayload           = 64 << 10
	TerminalProtocolVersion byte = 1
)

type TerminalOpcode byte

const (
	TerminalInputOpcode  TerminalOpcode = 1
	TerminalOutputOpcode TerminalOpcode = 2
)

// TerminalFrame is the complete payload of one v1 binary WebSocket frame.
// Sequence is an input command sequence for input frames and the retained
// output byte-range start for output frames. The length header supplies the
// exclusive output end.
type TerminalFrame struct {
	Opcode          TerminalOpcode
	SessionID       [16]byte
	Sequence        uint64
	LeaseGeneration uint64
	Payload         []byte
}

func EncodeTerminalInput(sessionID [16]byte, sequence, generation uint64, payload []byte) ([]byte, error) {
	return encodeTerminalFrame(TerminalFrame{Opcode: TerminalInputOpcode, SessionID: sessionID, Sequence: sequence, LeaseGeneration: generation, Payload: payload})
}

func EncodeTerminalOutput(sessionID [16]byte, sequence uint64, payload []byte) ([]byte, error) {
	return encodeTerminalFrame(TerminalFrame{Opcode: TerminalOutputOpcode, SessionID: sessionID, Sequence: sequence, Payload: payload})
}

func DecodeTerminalFrame(data []byte) (TerminalFrame, error) {
	if len(data) < TerminalHeaderSize || len(data) > TerminalHeaderSize+MaxTerminalPayload {
		return TerminalFrame{}, fmt.Errorf("%w: invalid binary frame length", ErrMalformed)
	}
	if data[0] != 'D' || data[1] != 'F' || data[2] != TerminalProtocolVersion {
		return TerminalFrame{}, fmt.Errorf("%w: binary magic or version", ErrMalformed)
	}
	opcode := data[3]
	if TerminalOpcode(opcode) != TerminalInputOpcode && TerminalOpcode(opcode) != TerminalOutputOpcode {
		return TerminalFrame{}, fmt.Errorf("%w: binary opcode", ErrMalformed)
	}
	var sessionID [16]byte
	copy(sessionID[:], data[4:20])
	if zeroID(sessionID) {
		return TerminalFrame{}, fmt.Errorf("%w: zero session id", ErrMalformed)
	}
	sequence := binary.BigEndian.Uint64(data[20:28])
	generation := binary.BigEndian.Uint64(data[28:36])
	payloadLength := binary.BigEndian.Uint32(data[36:40])
	if payloadLength > MaxTerminalPayload || uint64(TerminalHeaderSize)+uint64(payloadLength) != uint64(len(data)) {
		return TerminalFrame{}, fmt.Errorf("%w: binary payload length", ErrMalformed)
	}
	if payloadLength == 0 || (TerminalOpcode(opcode) == TerminalInputOpcode && (sequence == 0 || generation == 0)) || (TerminalOpcode(opcode) == TerminalOutputOpcode && generation != 0) {
		return TerminalFrame{}, fmt.Errorf("%w: binary sequence, generation or payload", ErrMalformed)
	}
	payload := make([]byte, payloadLength)
	copy(payload, data[TerminalHeaderSize:])
	return TerminalFrame{Opcode: TerminalOpcode(opcode), SessionID: sessionID, Sequence: sequence, LeaseGeneration: generation, Payload: payload}, nil
}

func encodeTerminalFrame(frame TerminalFrame) ([]byte, error) {
	if frame.Opcode != TerminalInputOpcode && frame.Opcode != TerminalOutputOpcode {
		return nil, fmt.Errorf("%w: binary opcode", ErrMalformed)
	}
	if zeroID(frame.SessionID) {
		return nil, fmt.Errorf("%w: zero session id", ErrMalformed)
	}
	if len(frame.Payload) == 0 || len(frame.Payload) > MaxTerminalPayload {
		return nil, fmt.Errorf("%w: binary payload length", ErrMalformed)
	}
	if frame.Opcode == TerminalInputOpcode && (frame.Sequence == 0 || frame.LeaseGeneration == 0) {
		return nil, fmt.Errorf("%w: input requires positive sequence and generation", ErrMalformed)
	}
	if frame.Opcode == TerminalOutputOpcode && frame.LeaseGeneration != 0 {
		return nil, fmt.Errorf("%w: output generation must be zero", ErrMalformed)
	}
	result := make([]byte, TerminalHeaderSize+len(frame.Payload))
	result[0], result[1], result[2], result[3] = 'D', 'F', TerminalProtocolVersion, byte(frame.Opcode)
	copy(result[4:20], frame.SessionID[:])
	binary.BigEndian.PutUint64(result[20:28], frame.Sequence)
	binary.BigEndian.PutUint64(result[28:36], frame.LeaseGeneration)
	binary.BigEndian.PutUint32(result[36:40], uint32(len(frame.Payload)))
	copy(result[TerminalHeaderSize:], frame.Payload)
	return result, nil
}

func zeroID(value [16]byte) bool {
	for _, b := range value {
		if b != 0 {
			return false
		}
	}
	return true
}
