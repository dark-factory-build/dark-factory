package relayhost

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func testIdentity(t *testing.T) Identity {
	t.Helper()
	identity, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestHostTokenRoundTripsThroughEd25519(t *testing.T) {
	identity := testIdentity(t)
	issued := time.Unix(1_800_000_000, 0)
	token := HostToken(identity, 7, issued)
	payload, err := VerifyHostToken(identity.PublicKey(), token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if payload.Node != identity.NodeID() || payload.Generation != identity.Generation() || payload.Sequence != 7 || payload.Issued != issued.Unix() {
		t.Fatalf("host payload = %+v", payload)
	}
	key, err := base64.RawURLEncoding.DecodeString(payload.Key)
	if err != nil || !bytes.Equal(key, identity.PublicKey()) {
		t.Fatalf("embedded key = %q, %v", payload.Key, err)
	}
	if strings.Contains(token, "=") || strings.Count(token, ".") != 1 {
		t.Fatalf("token %q is not unpadded payload.signature", token)
	}
}

func TestTicketsRoundTripAndCarryTheirBoundMembers(t *testing.T) {
	identity := testIdentity(t)
	controller := [ControllerIDSize]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	var device [DeviceKeySize]byte
	device[0] = 4
	for index := 1; index < DeviceKeySize; index++ {
		device[index] = byte(index)
	}
	expires := time.Unix(1_800_100_000, 0)

	pair, err := VerifyTicket(identity.PublicKey(), PairTicket(identity, controller, expires))
	if err != nil {
		t.Fatalf("pair ticket: %v", err)
	}
	if pair.Purpose != PurposePair || pair.Device != "" || pair.Expires != expires.Unix() {
		t.Fatalf("pair payload = %+v", pair)
	}
	if raw, err := DecodeFixedField(pair.Controller, ControllerIDSize); err != nil || !bytes.Equal(raw, controller[:]) {
		t.Fatalf("pair controller = %q, %v", pair.Controller, err)
	}

	control, err := VerifyTicket(identity.PublicKey(), ControlTicket(identity, controller, device, expires))
	if err != nil {
		t.Fatalf("control ticket: %v", err)
	}
	if control.Purpose != PurposeControl || control.Ticket == pair.Ticket {
		t.Fatalf("control payload = %+v (ticket ids must differ)", control)
	}
	if raw, err := DecodeFixedField(control.Device, DeviceKeySize); err != nil || !bytes.Equal(raw, device[:]) {
		t.Fatalf("control device = %q, %v", control.Device, err)
	}
}

func TestTamperedSignaturesAndPayloadsAreRefused(t *testing.T) {
	identity := testIdentity(t)
	token := HostToken(identity, 1, time.Unix(1_800_000_000, 0))
	separator := strings.IndexByte(token, '.')
	signature, err := base64.RawURLEncoding.DecodeString(token[separator+1:])
	if err != nil {
		t.Fatal(err)
	}
	signature[0] ^= 0x01
	tampered := token[:separator+1] + base64.RawURLEncoding.EncodeToString(signature)
	if _, err := VerifyHostToken(identity.PublicKey(), tampered); !errors.Is(err, ErrToken) {
		t.Fatalf("tampered signature = %v, want ErrToken", err)
	}

	payload, err := base64.RawURLEncoding.DecodeString(token[:separator])
	if err != nil {
		t.Fatal(err)
	}
	rewritten := bytes.Replace(payload, []byte(`"sequence":1`), []byte(`"sequence":9`), 1)
	if bytes.Equal(rewritten, payload) {
		t.Fatal("payload rewrite did not change the sequence")
	}
	swapped := base64.RawURLEncoding.EncodeToString(rewritten) + token[separator:]
	if _, err := VerifyHostToken(identity.PublicKey(), swapped); !errors.Is(err, ErrToken) {
		t.Fatalf("rewritten payload = %v, want ErrToken", err)
	}

	other := testIdentity(t)
	if _, err := VerifyHostToken(other.PublicKey(), token); !errors.Is(err, ErrToken) {
		t.Fatalf("foreign verifier = %v, want ErrToken", err)
	}
	if _, err := VerifyTicket(identity.PublicKey(), token); !errors.Is(err, ErrToken) {
		t.Fatalf("host token verified as a ticket = %v, want ErrToken", err)
	}
	if _, err := VerifyHostToken(identity.PublicKey(), "no-separator"); !errors.Is(err, ErrToken) {
		t.Fatalf("malformed token = %v, want ErrToken", err)
	}
	if _, err := VerifyHostToken(ed25519.PublicKey(nil), token); !errors.Is(err, ErrToken) {
		t.Fatalf("nil verifier = %v, want ErrToken", err)
	}
}
