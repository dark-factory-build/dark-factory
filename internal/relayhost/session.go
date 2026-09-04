package relayhost

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"sync"

	"github.com/coder/websocket"
)

const (
	// maxSessionInbound bounds records waiting to be written to one loopback
	// socket. The daemon reads promptly; a session that stops draining is
	// dropped alone rather than stalling the relay read loop.
	maxSessionInbound = 64
	// relayCloseSlow is the close code for a session this side cannot keep up
	// with in either direction.
	relayCloseSlow = 4003
	// relayCloseUnavailable is the close code for a session that has no
	// usable loopback socket, and the fallback for a close code the relay
	// contract does not carry.
	relayCloseUnavailable = 4005
	// minPassthroughCloseCode and maxPassthroughCloseCode bound the close
	// codes the envelope carries verbatim.
	minPassthroughCloseCode = 3000
	maxPassthroughCloseCode = 4999
	// maxResultFrameBytes bounds the frames worth scanning for a ticket
	// injection point: a pairing or authentication result is tiny.
	maxResultFrameBytes = 4 << 10
)

type openPayload struct {
	Controller string `json:"controller"`
	Purpose    string `json:"purpose"`
	Origin     string `json:"origin"`
}

type closePayload struct {
	Code   int    `json:"code"`
	Reason string `json:"reason"`
}

type revokePayload struct {
	Controller string `json:"controller"`
}

// session is one relay-opened controller mapped onto one ordinary loopback
// WebSocket client of the daemon's own browser listener.
type session struct {
	connection uint32
	inbound    chan Record

	// queued is the session's unwritten outbound budget. It is owned by
	// outboundQueue.mu, not by mu.
	queued int

	mu          sync.Mutex
	loopback    *websocket.Conn
	relayClosed bool
}

func newSession(connection uint32) *session {
	return &session{connection: connection, inbound: make(chan Record, maxSessionInbound)}
}

// deliver hands one relay record to the session writer without blocking the
// relay read loop. A refusal means this session is not draining.
func (current *session) deliver(record Record) bool {
	select {
	case current.inbound <- record:
		return true
	default:
		return false
	}
}

func (current *session) setLoopback(connection *websocket.Conn) {
	current.mu.Lock()
	current.loopback = connection
	current.mu.Unlock()
}

func (current *session) socket() *websocket.Conn {
	current.mu.Lock()
	defer current.mu.Unlock()
	return current.loopback
}

// markRelayClosed records that the relay ended this session, so the session
// goroutine does not echo a CLOSE record back for a close the relay already
// knows about.
func (current *session) markRelayClosed() {
	current.mu.Lock()
	current.relayClosed = true
	current.mu.Unlock()
}

func (current *session) relayInitiated() bool {
	current.mu.Lock()
	defer current.mu.Unlock()
	return current.relayClosed
}

// shutdown closes the loopback socket with an exact status. It is safe from
// any goroutine, including while the session goroutine is blocked reading.
func (current *session) shutdown(status websocket.StatusCode) {
	if connection := current.socket(); connection != nil {
		_ = connection.Close(status, "")
	}
}

// pump is the only writer of the loopback socket.
func (current *session) pump(ctx context.Context) {
	connection := current.socket()
	for {
		select {
		case <-ctx.Done():
			return
		case record := <-current.inbound:
			kind := websocket.MessageBinary
			if record.Type == RecordText {
				kind = websocket.MessageText
			}
			if err := connection.Write(ctx, kind, record.Payload); err != nil {
				return
			}
		}
	}
}

// loopbackCloseCode maps a loopback close onto the relay envelope: a code the
// contract carries verbatim passes through, anything else becomes 4005.
func loopbackCloseCode(err error) int {
	status := int(websocket.CloseStatus(err))
	if status >= minPassthroughCloseCode && status <= maxPassthroughCloseCode {
		return status
	}
	return relayCloseUnavailable
}

// ticketFrame returns the transport frame that follows a pairing or
// authentication result, or nil. The ticket travels as its own frame rather
// than as a member added to the result, so the daemon's bytes reach the
// session decoder untouched: neither protocol decoder tolerates an unknown
// member, and the client's relay socket wrapper consumes this frame instead
// of forwarding it. Frames are never logged and never decoded with a protocol
// decoder: this side must not become a second interpreter of daemon messages.
func (connector *Connector) ticketFrame(ctx context.Context, frame []byte) []byte {
	// Almost every frame is neither result, and a snapshot must not pay for
	// the scan.
	if connector.config.DeviceKey == nil || len(frame) > maxResultFrameBytes ||
		!bytes.Contains(frame, []byte(`"PAIR_RESULT"`)) && !bytes.Contains(frame, []byte(`"AUTH_RESULT"`)) {
		return nil
	}
	var result struct {
		Type string `json:"type"`
		Body struct {
			ClientID string `json:"client_id"`
		} `json:"body"`
	}
	if err := json.Unmarshal(frame, &result); err != nil || result.Type != "PAIR_RESULT" && result.Type != "AUTH_RESULT" {
		return nil
	}
	raw, err := hex.DecodeString(result.Body.ClientID)
	if err != nil || len(raw) != ControllerIDSize {
		return nil
	}
	var clientID [ControllerIDSize]byte
	copy(clientID[:], raw)
	deviceKey, ok, err := connector.config.DeviceKey(ctx, clientID)
	if err != nil || !ok {
		return nil
	}
	ticket := ControlTicket(connector.config.Identity, clientID, deviceKey, connector.now().Add(connector.config.TicketLifetime))
	if ticket == "" {
		return nil
	}
	payload, err := json.Marshal(struct {
		Type   string `json:"type"`
		Ticket string `json:"ticket"`
	}{Type: "RELAY_TICKET", Ticket: ticket})
	if err != nil {
		return nil
	}
	return payload
}
