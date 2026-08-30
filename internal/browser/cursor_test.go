package browser

import (
	"encoding/base64"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
)

func TestCursorRoundTripAndClosedEncoding(t *testing.T) {
	t.Parallel()
	var after [16]byte
	for index := range after {
		after[index] = byte(index + 1)
	}
	cases := []Cursor{
		{Head: 0, Kind: browserprotocol.StateFactory},
		{Head: 7, Kind: browserprotocol.StateProject},
		{Head: browserprotocol.Decimal(browserprotocol.MaxSQLiteInteger), Kind: browserprotocol.StateHumanRequest, AfterID: after, HasAfter: true},
	}
	for _, candidate := range cases {
		encoded, err := encodeCursor(candidate)
		if err != nil {
			t.Fatalf("encode %+v: %v", candidate, err)
		}
		decoded, err := decodeCursor(encoded)
		if err != nil {
			t.Fatalf("decode %+v: %v", candidate, err)
		}
		if decoded != candidate {
			t.Fatalf("round trip mismatch: got %+v want %+v", decoded, candidate)
		}
	}
}

func TestCursorRejectsMalformedUnknownAndZeroIdentity(t *testing.T) {
	t.Parallel()
	valid, err := encodeCursor(Cursor{Head: 9, Kind: browserprotocol.StateTask})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(valid)
	if err != nil {
		t.Fatal(err)
	}
	mutate := func(change func([]byte)) string {
		copyRaw := append([]byte(nil), raw...)
		change(copyRaw)
		return base64.RawURLEncoding.EncodeToString(copyRaw)
	}
	cases := []string{
		"",
		"=not-base64=",
		base64.URLEncoding.EncodeToString(raw),
		mutate(func(value []byte) { value[0] = 2 }),
		mutate(func(value []byte) { value[9] = 0 }),
		mutate(func(value []byte) { value[10] = 2 }),
		base64.RawURLEncoding.EncodeToString(append(append([]byte(nil), raw...), make([]byte, 16)...)),
	}
	withZeroIdentity := append([]byte(nil), raw...)
	withZeroIdentity[9] = 2
	withZeroIdentity[10] = 1
	withZeroIdentity = append(withZeroIdentity, make([]byte, 16)...)
	cases = append(cases, base64.RawURLEncoding.EncodeToString(withZeroIdentity))
	for _, candidate := range cases {
		if _, err := decodeCursor(candidate); err == nil {
			t.Fatalf("accepted malformed cursor %q", candidate)
		}
	}
	if _, err := encodeCursor(Cursor{Kind: browserprotocol.StateFactory, HasAfter: true, AfterID: [16]byte{1}}); err == nil {
		t.Fatal("factory cursor accepted after identity")
	}
	if _, err := encodeCursor(Cursor{Kind: browserprotocol.StateProject, HasAfter: true}); err == nil {
		t.Fatal("dynamic cursor accepted zero identity")
	}
	if _, err := encodeCursor(Cursor{Kind: browserprotocol.StateProject, AfterID: [16]byte{1}}); err == nil {
		t.Fatal("cursor accepted hidden identity")
	}
	overflow := append([]byte(nil), raw...)
	for index := 1; index < 9; index++ {
		overflow[index] = 0xff
	}
	if _, err := decodeCursor(base64.RawURLEncoding.EncodeToString(overflow)); err == nil {
		t.Fatal("cursor accepted chronology above SQLite integer")
	}
}
