package daemon

import (
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dark-factory-build/dark-factory/internal/relayhost"
)

const (
	testHostDomain    = "dark-factory-relay/host\n"
	testTicketDomain  = "dark-factory-relay/ticket\n"
	testMaxTokenBytes = 4096
)

func testVerifyHostToken(key ed25519.PublicKey, token string) (relayhost.HostTokenPayload, error) {
	var payload relayhost.HostTokenPayload
	if err := testVerifyToken(key, testHostDomain, token, &payload); err != nil {
		return relayhost.HostTokenPayload{}, err
	}
	embedded, err := base64.RawURLEncoding.DecodeString(payload.Key)
	if err != nil || len(embedded) != ed25519.PublicKeySize || subtle.ConstantTimeCompare(embedded, key) != 1 {
		return relayhost.HostTokenPayload{}, fmt.Errorf("%w: key does not match the signer", relayhost.ErrToken)
	}
	if payload.Node == "" || payload.Node != relayhost.NodeIDFromPublicKey(key) {
		return relayhost.HostTokenPayload{}, fmt.Errorf("%w: node does not match the key", relayhost.ErrToken)
	}
	if payload.Generation == 0 || payload.Sequence == 0 {
		return relayhost.HostTokenPayload{}, fmt.Errorf("%w: generation and sequence start at one", relayhost.ErrToken)
	}
	return payload, nil
}

func testVerifyTicket(key ed25519.PublicKey, token string) (relayhost.TicketPayload, error) {
	var payload relayhost.TicketPayload
	if err := testVerifyToken(key, testTicketDomain, token, &payload); err != nil {
		return relayhost.TicketPayload{}, err
	}
	if payload.Node == "" || payload.Node != relayhost.NodeIDFromPublicKey(key) {
		return relayhost.TicketPayload{}, fmt.Errorf("%w: node does not match the key", relayhost.ErrToken)
	}
	if _, err := testDecodeFixed(payload.Controller, relayhost.ControllerIDSize); err != nil {
		return relayhost.TicketPayload{}, err
	}
	if _, err := testDecodeFixed(payload.Ticket, relayhost.TicketIDSize); err != nil {
		return relayhost.TicketPayload{}, err
	}
	switch payload.Purpose {
	case relayhost.PurposePair:
		if payload.Device != "" {
			return relayhost.TicketPayload{}, fmt.Errorf("%w: a pair ticket carries no device", relayhost.ErrToken)
		}
	case relayhost.PurposeControl:
		if _, err := testDecodeFixed(payload.Device, relayhost.DeviceKeySize); err != nil {
			return relayhost.TicketPayload{}, err
		}
	default:
		return relayhost.TicketPayload{}, fmt.Errorf("%w: unknown purpose %q", relayhost.ErrToken, payload.Purpose)
	}
	if payload.Expires <= 0 {
		return relayhost.TicketPayload{}, fmt.Errorf("%w: missing expiry", relayhost.ErrToken)
	}
	return payload, nil
}

func testVerifyToken(key ed25519.PublicKey, domain, token string, out any) error {
	if len(key) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: verifier key is not an Ed25519 public key", relayhost.ErrToken)
	}
	if token == "" || len(token) > testMaxTokenBytes {
		return fmt.Errorf("%w: token length", relayhost.ErrToken)
	}
	separator := strings.IndexByte(token, '.')
	if separator <= 0 || separator == len(token)-1 || strings.IndexByte(token[separator+1:], '.') >= 0 {
		return fmt.Errorf("%w: token is not payload.signature", relayhost.ErrToken)
	}
	text, encodedSignature := token[:separator], token[separator+1:]
	signature, err := base64.RawURLEncoding.DecodeString(encodedSignature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return fmt.Errorf("%w: signature encoding", relayhost.ErrToken)
	}
	if !ed25519.Verify(key, append([]byte(domain), text...), signature) {
		return fmt.Errorf("%w: signature does not verify", relayhost.ErrToken)
	}
	payload, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil {
		return fmt.Errorf("%w: payload encoding", relayhost.ErrToken)
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("%w: payload is not the expected JSON object", relayhost.ErrToken)
	}
	return nil
}

func testDecodeFixed(value string, size int) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) != size {
		return nil, fmt.Errorf("%w: member is not %d base64url bytes", relayhost.ErrToken, size)
	}
	return raw, nil
}
