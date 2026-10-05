package relayhost

import (
	"bytes"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func VerifyHostToken(key ed25519.PublicKey, token string) (HostTokenPayload, error) {
	var payload HostTokenPayload
	if err := verify(key, hostDomain, token, &payload); err != nil {
		return HostTokenPayload{}, err
	}
	embedded, err := base64.RawURLEncoding.DecodeString(payload.Key)
	if err != nil || len(embedded) != ed25519.PublicKeySize || subtle.ConstantTimeCompare(embedded, key) != 1 {
		return HostTokenPayload{}, fmt.Errorf("%w: key does not match the signer", ErrToken)
	}
	if payload.Node == "" || payload.Node != NodeIDFromPublicKey(key) {
		return HostTokenPayload{}, fmt.Errorf("%w: node does not match the key", ErrToken)
	}
	if payload.Generation == 0 || payload.Sequence == 0 {
		return HostTokenPayload{}, fmt.Errorf("%w: generation and sequence start at one", ErrToken)
	}
	return payload, nil
}

func VerifyTicket(key ed25519.PublicKey, token string) (TicketPayload, error) {
	var payload TicketPayload
	if err := verify(key, ticketDomain, token, &payload); err != nil {
		return TicketPayload{}, err
	}
	if payload.Node == "" || payload.Node != NodeIDFromPublicKey(key) {
		return TicketPayload{}, fmt.Errorf("%w: node does not match the key", ErrToken)
	}
	if _, err := decodeFixed(payload.Controller, ControllerIDSize); err != nil {
		return TicketPayload{}, err
	}
	if _, err := decodeFixed(payload.Ticket, TicketIDSize); err != nil {
		return TicketPayload{}, err
	}
	switch payload.Purpose {
	case PurposePair:
		if payload.Device != "" {
			return TicketPayload{}, fmt.Errorf("%w: a pair ticket carries no device", ErrToken)
		}
	case PurposeControl:
		if _, err := decodeFixed(payload.Device, DeviceKeySize); err != nil {
			return TicketPayload{}, err
		}
	default:
		return TicketPayload{}, fmt.Errorf("%w: unknown purpose %q", ErrToken, payload.Purpose)
	}
	if payload.Expires <= 0 {
		return TicketPayload{}, fmt.Errorf("%w: missing expiry", ErrToken)
	}
	return payload, nil
}

func verify(key ed25519.PublicKey, domain, token string, out any) error {
	if len(key) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: verifier key is not an Ed25519 public key", ErrToken)
	}
	if token == "" || len(token) > maxTokenBytes {
		return fmt.Errorf("%w: token length", ErrToken)
	}
	separator := strings.IndexByte(token, '.')
	if separator <= 0 || separator == len(token)-1 || strings.IndexByte(token[separator+1:], '.') >= 0 {
		return fmt.Errorf("%w: token is not payload.signature", ErrToken)
	}
	text, encodedSignature := token[:separator], token[separator+1:]
	signature, err := base64.RawURLEncoding.DecodeString(encodedSignature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return fmt.Errorf("%w: signature encoding", ErrToken)
	}
	if !ed25519.Verify(key, append([]byte(domain), text...), signature) {
		return fmt.Errorf("%w: signature does not verify", ErrToken)
	}
	payload, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil {
		return fmt.Errorf("%w: payload encoding", ErrToken)
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("%w: payload is not the expected JSON object", ErrToken)
	}
	return nil
}

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

func TestControlTicketsRoundTripAndCarryTheirBoundMembers(t *testing.T) {
	identity := testIdentity(t)
	controller := [ControllerIDSize]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	var device [DeviceKeySize]byte
	device[0] = 4
	for index := 1; index < DeviceKeySize; index++ {
		device[index] = byte(index)
	}
	expires := time.Unix(1_800_100_000, 0)

	first, err := VerifyTicket(identity.PublicKey(), ControlTicket(identity, controller, device, expires))
	if err != nil {
		t.Fatalf("control ticket: %v", err)
	}
	if first.Purpose != PurposeControl || first.Expires != expires.Unix() {
		t.Fatalf("control payload = %+v", first)
	}
	if raw, err := base64.RawURLEncoding.DecodeString(first.Controller); err != nil || !bytes.Equal(raw, controller[:]) {
		t.Fatalf("control controller = %q, %v", first.Controller, err)
	}
	if raw, err := base64.RawURLEncoding.DecodeString(first.Device); err != nil || !bytes.Equal(raw, device[:]) {
		t.Fatalf("control device = %q, %v", first.Device, err)
	}
	second, err := VerifyTicket(identity.PublicKey(), ControlTicket(identity, controller, device, expires))
	if err != nil || second.Ticket == first.Ticket {
		t.Fatalf("two tickets shared one id: %v", err)
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
