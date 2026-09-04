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
	// maxSessionInboundBytes bounds the same queue by size. Records alone
	// would let a relay pin 64 MiB per session; sessions are capped, but the
	// product is still far more memory than a stalled session may hold.
	maxSessionInboundBytes = 2 << 20
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

	// queued is the session's unwritten outbound budget, and draining says
	// the relay removed this session from the connector map. Both are owned
	// by the connector, not by mu: queued by outboundQueue.mu and draining by
	// Connector.mu.
	queued   int
	draining bool

	inboundBytes int

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
	current.mu.Lock()
	defer current.mu.Unlock()
	if current.inboundBytes+len(record.Payload) > maxSessionInboundBytes {
		return false
	}
	select {
	case current.inbound <- record:
		current.inboundBytes += len(record.Payload)
		return true
	default:
		return false
	}
}

// setLoopback publishes the dialed socket, or reports false when the relay
// already ended this session. shutdown takes the same mutex, so a CLOSE that
// lands while the dial is in flight either finds the socket and closes it or
// loses here - and then the dialer owns the close. Without that handoff the
// socket would stay open, uncounted, until the relay connection dropped.
func (current *session) setLoopback(connection *websocket.Conn) bool {
	current.mu.Lock()
	defer current.mu.Unlock()
	if current.relayClosed {
		return false
	}
	current.loopback = connection
	return true
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
			current.mu.Lock()
			current.inboundBytes -= len(record.Payload)
			current.mu.Unlock()
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
// authentication result, or nil. The result itself is forwarded byte for
// byte and the ticket travels as its own frame, so the relay path never
// depends on how either protocol decoder treats members it does not know.
// The client's relay socket wrapper consumes this frame instead of
// forwarding it. Frames are never logged and never decoded with a protocol
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
