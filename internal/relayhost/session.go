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
	// injection point. A pairing or authentication result is tiny; a
	// snapshot must never pay for the scan.
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

// relayCloseStatus maps a relay CLOSE record onto the loopback socket. The
// daemon does not interpret controller close codes, so anything outside the
// verbatim range becomes a normal closure rather than an invented failure.
func relayCloseStatus(code int) websocket.StatusCode {
	if code >= minPassthroughCloseCode && code <= maxPassthroughCloseCode {
		return websocket.StatusCode(code)
	}
	return websocket.StatusNormalClosure
}

// injectTicket adds a relay control ticket to a pairing or authentication
// result so the controller can reconnect through the relay without another
// terminal-side pairing. Every other frame, and every frame whose client id
// has no usable device key, is forwarded byte for byte. Frames are never
// logged and never decoded with a protocol decoder: this side must not become
// a second interpreter of daemon messages.
func (connector *Connector) injectTicket(ctx context.Context, frame []byte) []byte {
	if connector.config.DeviceKey == nil || !carriesResult(frame) {
		return frame
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(frame, &envelope); err != nil {
		return frame
	}
	var kind string
	if err := json.Unmarshal(envelope["type"], &kind); err != nil || kind != "PAIR_RESULT" && kind != "AUTH_RESULT" {
		return frame
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(envelope["body"], &body); err != nil {
		return frame
	}
	var encoded string
	if err := json.Unmarshal(body["client_id"], &encoded); err != nil || len(encoded) != 2*ControllerIDSize {
		return frame
	}
	raw, err := hex.DecodeString(encoded)
	if err != nil || len(raw) != ControllerIDSize {
		return frame
	}
	var clientID [ControllerIDSize]byte
	copy(clientID[:], raw)
	deviceKey, ok, err := connector.config.DeviceKey(ctx, clientID)
	if err != nil || !ok {
		return frame
	}
	ticket := ControlTicket(connector.config.Identity, clientID, deviceKey, connector.now().Add(connector.config.TicketLifetime))
	if ticket == "" {
		return frame
	}
	encodedTicket, err := json.Marshal(ticket)
	if err != nil {
		return frame
	}
	body["relay_ticket"] = encodedTicket
	encodedBody, err := json.Marshal(body)
	if err != nil {
		return frame
	}
	envelope["body"] = encodedBody
	rewritten, err := json.Marshal(envelope)
	if err != nil {
		return frame
	}
	return rewritten
}

// carriesResult is the cheap pre-filter: almost every frame is neither a
// pairing nor an authentication result, and those must not pay for a decode.
func carriesResult(frame []byte) bool {
	if len(frame) > maxResultFrameBytes {
		return false
	}
	return bytes.Contains(frame, []byte(`"PAIR_RESULT"`)) || bytes.Contains(frame, []byte(`"AUTH_RESULT"`))
}
