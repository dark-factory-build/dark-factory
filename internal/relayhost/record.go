package relayhost

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// RecordType is the one-byte envelope discriminator. The set is closed:
// adding a type is a relay contract change on both sides at once.
type RecordType uint8

const (
	// RecordOpen is relay to host only and names a new controller session.
	RecordOpen RecordType = 0x01
	// RecordText carries one application text frame verbatim.
	RecordText RecordType = 0x02
	// RecordBinary carries one application binary frame verbatim.
	RecordBinary RecordType = 0x03
	// RecordClose ends one session in either direction.
	RecordClose RecordType = 0x04
	// RecordRevoke is host to relay only and always uses connection 0.
	RecordRevoke RecordType = 0x05
)

const (
	// MaxHostMessageBytes bounds one whole binary message on the host socket.
	MaxHostMessageBytes = 4 << 20
	// maxRecordPayloadBytes bounds one record payload: the 1 MiB snapshot
	// bound plus the envelope slack the relay contract allows.
	maxRecordPayloadBytes = (1 << 20) + 64
	// recordHeaderBytes is type(u8) connection(u32 BE) length(u32 BE).
	recordHeaderBytes = 9
)

var (
	// ErrRecordTruncated means a message ended inside a record.
	ErrRecordTruncated = errors.New("relayhost: truncated relay record")
	// ErrRecordOversized means a message or a record payload exceeded its bound.
	ErrRecordOversized = errors.New("relayhost: oversized relay record")
	// ErrRecordType means the type byte is outside the closed set.
	ErrRecordType = errors.New("relayhost: unknown relay record type")
)

// Record is one framed unit inside a host message.
type Record struct {
	Type       RecordType
	Connection uint32
	Payload    []byte
}

func (kind RecordType) known() bool {
	return kind >= RecordOpen && kind <= RecordRevoke
}

// AppendRecord appends the wire encoding of one record to dst.
func AppendRecord(dst []byte, record Record) []byte {
	dst = append(dst, byte(record.Type))
	dst = binary.BigEndian.AppendUint32(dst, record.Connection)
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(record.Payload)))
	return append(dst, record.Payload...)
}

// DecodeRecords splits one binary message into its records. Payloads alias
// message, so the caller must not mutate it. Bounds are exact and fail
// closed: a message this side cannot fully account for is a broken relay,
// never a partially applied batch.
func DecodeRecords(message []byte) ([]Record, error) {
	if len(message) > MaxHostMessageBytes {
		return nil, fmt.Errorf("%w: message is %d bytes", ErrRecordOversized, len(message))
	}
	if len(message) == 0 {
		return nil, fmt.Errorf("%w: message carries no record", ErrRecordTruncated)
	}
	var records []Record
	for offset := 0; offset < len(message); {
		if len(message)-offset < recordHeaderBytes {
			return nil, fmt.Errorf("%w: header at offset %d", ErrRecordTruncated, offset)
		}
		kind := RecordType(message[offset])
		if !kind.known() {
			return nil, fmt.Errorf("%w: 0x%02x at offset %d", ErrRecordType, message[offset], offset)
		}
		connection := binary.BigEndian.Uint32(message[offset+1 : offset+5])
		length := binary.BigEndian.Uint32(message[offset+5 : offset+recordHeaderBytes])
		if length > maxRecordPayloadBytes {
			return nil, fmt.Errorf("%w: payload is %d bytes", ErrRecordOversized, length)
		}
		start := offset + recordHeaderBytes
		if uint64(len(message)-start) < uint64(length) {
			return nil, fmt.Errorf("%w: payload at offset %d", ErrRecordTruncated, offset)
		}
		records = append(records, Record{Type: kind, Connection: connection, Payload: message[start : start+int(length)]})
		offset = start + int(length)
	}
	return records, nil
}
