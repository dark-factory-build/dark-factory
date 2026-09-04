package relayhost

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// The signed bytes are a fixed domain prefix followed by the exact
	// base64url payload text, so verification never re-serialises JSON.
	hostDomain   = "dark-factory-relay/host\n"
	ticketDomain = "dark-factory-relay/ticket\n"

	// PurposePair names a single-use pairing ticket. It carries no device
	// proof: the browser has no key yet, and the pairing challenge inside the
	// invitation is what actually authorizes anything.
	PurposePair = "pair"
	// PurposeControl names a ticket bound to one device public key.
	PurposeControl = "control"

	// ControllerIDSize is the daemon's client identity for one PWA install.
	ControllerIDSize = 16
	// TicketIDSize is the relay's single-use / deny-list key.
	TicketIDSize = 16
	// DeviceKeySize is the browser device public key as an uncompressed SEC1 point.
	DeviceKeySize = 65

	maxTokenBytes = 4096
)

// ErrToken is every credential refusal. The relay Worker makes the same
// checks; these helpers exist so this side can prove round trips against
// crypto/ed25519 rather than against a mock of itself.
var ErrToken = errors.New("relayhost: invalid relay token")

// HostTokenPayload is the outbound connection credential. Member order is the
// wire order; the relay reads the exact base64url text, never a re-encoding.
type HostTokenPayload struct {
	Node       string `json:"node"`
	Key        string `json:"key"`
	Generation uint64 `json:"generation"`
	Sequence   uint64 `json:"sequence"`
	Issued     int64  `json:"issued"`
}

// TicketPayload is a controller credential. Device is present only for
// PurposeControl.
type TicketPayload struct {
	Node       string `json:"node"`
	Controller string `json:"controller"`
	Device     string `json:"device,omitempty"`
	Purpose    string `json:"purpose"`
	Ticket     string `json:"ticket"`
	Expires    int64  `json:"expires"`
}

// HostToken mints the credential for one dial attempt. sequence counts dials
// within one boot; the relay requires (generation, sequence) to be strictly
// greater than the last pair it accepted.
func HostToken(identity Identity, sequence uint64, now time.Time) string {
	if !identity.valid() {
		return ""
	}
	return sign(identity, hostDomain, HostTokenPayload{
		Node:       identity.nodeID,
		Key:        base64.RawURLEncoding.EncodeToString(identity.PublicKey()),
		Generation: identity.generation,
		Sequence:   sequence,
		Issued:     now.Unix(),
	})
}

// PairTicket mints a single-use pairing credential for one controller id.
func PairTicket(identity Identity, controller [ControllerIDSize]byte, expires time.Time) string {
	if !identity.valid() {
		return ""
	}
	return sign(identity, ticketDomain, TicketPayload{
		Node:       identity.nodeID,
		Controller: base64.RawURLEncoding.EncodeToString(controller[:]),
		Purpose:    PurposePair,
		Ticket:     newTicketID(),
		Expires:    expires.Unix(),
	})
}

// ControlTicket mints a credential bound to one controller id and one device
// public key. The relay accepts it only alongside a proof signed by that key.
func ControlTicket(identity Identity, clientID [ControllerIDSize]byte, deviceSEC1 [DeviceKeySize]byte, expires time.Time) string {
	if !identity.valid() {
		return ""
	}
	return sign(identity, ticketDomain, TicketPayload{
		Node:       identity.nodeID,
		Controller: base64.RawURLEncoding.EncodeToString(clientID[:]),
		Device:     base64.RawURLEncoding.EncodeToString(deviceSEC1[:]),
		Purpose:    PurposeControl,
		Ticket:     newTicketID(),
		Expires:    expires.Unix(),
	})
}

// VerifyHostToken mirrors the relay's host admission: the signature must be
// the node key's, the embedded key must be that key, and the node id must be
// the one the key derives.
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

// VerifyTicket mirrors the relay's controller admission for the parts the
// node key authorizes. Expiry, single use and the deny list stay with the
// relay because only it observes redemption.
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

func sign[Payload any](identity Identity, domain string, payload Payload) string {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	text := base64.RawURLEncoding.EncodeToString(encoded)
	signature := ed25519.Sign(identity.private, append([]byte(domain), text...))
	return text + "." + base64.RawURLEncoding.EncodeToString(signature)
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

func decodeFixed(value string, size int) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) != size {
		return nil, fmt.Errorf("%w: member is not %d base64url bytes", ErrToken, size)
	}
	return raw, nil
}

func newTicketID() string {
	var value [TicketIDSize]byte
	if _, err := rand.Read(value[:]); err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(value[:])
}
