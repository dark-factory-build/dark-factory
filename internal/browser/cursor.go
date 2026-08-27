package browser

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"

	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
)

const cursorVersion byte = 1

func encodeCursor(cursor Cursor) (string, error) {
	if uint64(cursor.Head) > browserprotocol.MaxSQLiteInteger {
		return "", fmt.Errorf("invalid cursor head")
	}
	kind, err := encodeKind(cursor.Kind)
	if err != nil {
		return "", err
	}
	if cursor.Kind == browserprotocol.StateFactory && cursor.HasAfter {
		return "", fmt.Errorf("invalid factory cursor")
	}
	if !cursor.HasAfter && !zero16(cursor.AfterID) {
		return "", fmt.Errorf("invalid hidden cursor identity")
	}
	size := 11
	if cursor.HasAfter {
		if zero16(cursor.AfterID) {
			return "", fmt.Errorf("invalid zero cursor identity")
		}
		size += len(cursor.AfterID)
	}
	raw := make([]byte, size)
	raw[0] = cursorVersion
	binary.BigEndian.PutUint64(raw[1:9], uint64(cursor.Head))
	raw[9] = kind
	if cursor.HasAfter {
		raw[10] = 1
		copy(raw[11:], cursor.AfterID[:])
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeCursor(encoded string) (Cursor, error) {
	if encoded == "" || len(encoded) > browserprotocol.MaxCursorBytes {
		return Cursor{}, fmt.Errorf("invalid cursor length")
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || len(raw) != 11 && len(raw) != 27 || raw[0] != cursorVersion {
		return Cursor{}, fmt.Errorf("invalid cursor encoding")
	}
	kind, err := decodeKind(raw[9])
	if err != nil {
		return Cursor{}, err
	}
	cursor := Cursor{Head: browserprotocol.Decimal(binary.BigEndian.Uint64(raw[1:9])), Kind: kind}
	if uint64(cursor.Head) > browserprotocol.MaxSQLiteInteger {
		return Cursor{}, fmt.Errorf("invalid cursor head")
	}
	switch raw[10] {
	case 0:
		if len(raw) != 11 {
			return Cursor{}, fmt.Errorf("invalid cursor identity flag")
		}
	case 1:
		if len(raw) != 27 || kind == browserprotocol.StateFactory {
			return Cursor{}, fmt.Errorf("invalid cursor identity")
		}
		copy(cursor.AfterID[:], raw[11:])
		if zero16(cursor.AfterID) {
			return Cursor{}, fmt.Errorf("invalid zero cursor identity")
		}
		cursor.HasAfter = true
	default:
		return Cursor{}, fmt.Errorf("invalid cursor identity flag")
	}
	return cursor, nil
}

func encodeKind(kind browserprotocol.StateKind) (byte, error) {
	switch kind {
	case browserprotocol.StateFactory:
		return 1, nil
	case browserprotocol.StateProject:
		return 2, nil
	case browserprotocol.StateAgent:
		return 3, nil
	case browserprotocol.StateTask:
		return 4, nil
	case browserprotocol.StateHumanRequest:
		return 5, nil
	default:
		return 0, fmt.Errorf("invalid state kind")
	}
}

func decodeKind(value byte) (browserprotocol.StateKind, error) {
	switch value {
	case 1:
		return browserprotocol.StateFactory, nil
	case 2:
		return browserprotocol.StateProject, nil
	case 3:
		return browserprotocol.StateAgent, nil
	case 4:
		return browserprotocol.StateTask, nil
	case 5:
		return browserprotocol.StateHumanRequest, nil
	default:
		return "", fmt.Errorf("invalid state kind")
	}
}

func zero16(value [16]byte) bool {
	for _, item := range value {
		if item != 0 {
			return false
		}
	}
	return true
}
