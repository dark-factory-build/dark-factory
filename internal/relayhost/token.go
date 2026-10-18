package relayhost

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	// The signed bytes are a fixed domain prefix followed by the exact
	// base64url payload text, so verification never re-serialises JSON.
	hostDomain   = "dark-factory-relay/host\n"
	ticketDomain = "dark-factory-relay/ticket\n"
	// consoleDomain prefixes the signed console bundle.
	consoleDomain = "dark-factory-console\n"

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

// SignConsole frames a gzipped console bundle the way every page shell
// verifies it: the 32-byte node public key, its Ed25519 signature over
// consoleDomain and the bundle, then the bundle. A shell accepts it only for
// the key it pinned at pairing, or whose node or public id it already holds.
func SignConsole(identity Identity, bundle []byte) []byte {
	if !identity.valid() || len(bundle) == 0 {
		return nil
	}
	signature := ed25519.Sign(identity.private, append([]byte(consoleDomain), bundle...))
	return append(append(identity.PublicKey(), signature...), bundle...)
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
