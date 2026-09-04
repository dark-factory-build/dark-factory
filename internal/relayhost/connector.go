package relayhost

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	relaySubprotocol = "dark-factory-relay"

	defaultTicketLifetime = 14 * 24 * time.Hour
	defaultBaseBackoff    = time.Second
	defaultMaxBackoff     = 30 * time.Second
	defaultPingInterval   = 25 * time.Second
	defaultPongTimeout    = 10 * time.Second

	// maxSessions is held strictly below the loopback listener's 32
	// connection slots so relayed sessions can never take the last one: 8
	// slots stay reserved for the operator's own local browser, which a
	// hostile or merely busy relay would otherwise starve with 503s.
	maxSessions = 24

	relayDialTimeout    = 15 * time.Second
	loopbackDialTimeout = 5 * time.Second
	relayWriteTimeout   = 10 * time.Second
)

// ErrRelayProtocol means the relay sent something this side cannot account
// for. It is never a session-level failure: the whole relay connection drops
// and reconnects, because a relay that framed one record wrongly cannot be
// trusted to have framed the others correctly.
var ErrRelayProtocol = errors.New("relayhost: relay protocol violation")

// ErrConfig is a refusal to start a connector at all.
var ErrConfig = errors.New("relayhost: invalid relay connector configuration")

// DialFunc is the WebSocket dial seam. Tests substitute it to reach fake
// servers and to prove dial failures; production uses websocket.Dial.
type DialFunc func(ctx context.Context, target string, options *websocket.DialOptions) (*websocket.Conn, *http.Response, error)

// Config describes one connector. Only RelayOrigin, Identity and BrowserURL
// are required; every other member has a production default.
type Config struct {
	// RelayOrigin is the relay's wss:// (or ws:// for development) origin
	// with no path or query.
	RelayOrigin string
	// Identity is the node identity this connector presents.
	Identity Identity
	// BrowserURL is the daemon's own loopback browser endpoint, for example
	// ws://127.0.0.1:43123/browser/v2.
	BrowserURL string
	// DeviceKey resolves one client id to its device public key. It reports
	// ok=false for a missing or revoked client, which suppresses ticket
	// injection for that frame rather than failing the session.
	DeviceKey func(ctx context.Context, clientID [ControllerIDSize]byte) ([DeviceKeySize]byte, bool, error)
	// TicketLifetime is how long an injected control ticket stays valid.
	TicketLifetime time.Duration

	// Dialer, Now, BaseBackoff, MaxBackoff, PingInterval and PongTimeout are
	// the test seams. The timing members are configurable because a test that
	// waited real reconnect and keepalive intervals could not assert them.
	Dialer       DialFunc
	Now          func() time.Time
	BaseBackoff  time.Duration
	MaxBackoff   time.Duration
	PingInterval time.Duration
	PongTimeout  time.Duration
}

// Status is the bounded view of one connector, read by tests and by
// factoryctl remote status once the pairing slice lands.
type Status struct {
	Connected bool
	NodeID    string
	LastError error
	Sessions  int
}

// Connector maintains one outbound relay connection and the loopback sessions
// it carries. Nothing is buffered across reconnects: a controller that comes
// back gets a brand-new daemon session with a fresh HELLO and snapshot, never
// replayed frames.
type Connector struct {
	config  Config
	hostURL string

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	closeOnce sync.Once

	mu        sync.Mutex
	connected bool
	failing   bool
	lastErr   error
	sequence  uint64
	sessions  map[uint32]*session
	queue     *outboundQueue

	sessionGroup sync.WaitGroup
}

// Dial validates the configuration and starts the reconnect loop. It returns
// before the first dial completes: a relay that is unreachable at boot must
// not hold up the daemon, and Status reports what the loop has observed.
func Dial(ctx context.Context, config Config) (*Connector, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: nil context", ErrConfig)
	}
	if err := validateRelayOrigin(config.RelayOrigin); err != nil {
		return nil, err
	}
	if !config.Identity.valid() {
		return nil, fmt.Errorf("%w: identity is not loaded", ErrConfig)
	}
	if err := validateBrowserURL(config.BrowserURL); err != nil {
		return nil, err
	}
	config = config.withDefaults()
	owned, cancel := context.WithCancel(ctx)
	connector := &Connector{
		config:   config,
		hostURL:  strings.TrimSuffix(config.RelayOrigin, "/") + "/host/" + config.Identity.NodeID(),
		ctx:      owned,
		cancel:   cancel,
		done:     make(chan struct{}),
		sessions: make(map[uint32]*session),
	}
	go connector.run()
	return connector, nil
}

func (config Config) withDefaults() Config {
	if config.Dialer == nil {
		config.Dialer = websocket.Dial
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.TicketLifetime <= 0 {
		config.TicketLifetime = defaultTicketLifetime
	}
	if config.BaseBackoff <= 0 {
		config.BaseBackoff = defaultBaseBackoff
	}
	if config.MaxBackoff < config.BaseBackoff {
		config.MaxBackoff = max(defaultMaxBackoff, config.BaseBackoff)
	}
	if config.PingInterval <= 0 {
		config.PingInterval = defaultPingInterval
	}
	if config.PongTimeout <= 0 {
		config.PongTimeout = defaultPongTimeout
	}
	return config
}

func validateRelayOrigin(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "wss" && parsed.Scheme != "ws" || parsed.Host == "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.String() != value {
		return fmt.Errorf("%w: relay origin must be an exact wss:// or ws:// origin", ErrConfig)
	}
	return nil
}

func validateBrowserURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "ws" && parsed.Scheme != "wss" || parsed.Host == "" || parsed.User != nil ||
		parsed.Path == "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.String() != value {
		return fmt.Errorf("%w: browser URL must be an exact loopback WebSocket URL", ErrConfig)
	}
	return nil
}

func (connector *Connector) now() time.Time { return connector.config.Now() }

// Status reports what the loop currently observes. LastError is the most
// recent bounded failure and is retained across a reconnect so a persistent
// refusal stays visible.
func (connector *Connector) Status() Status {
	if connector == nil {
		return Status{}
	}
	connector.mu.Lock()
	defer connector.mu.Unlock()
	return Status{Connected: connector.connected, NodeID: connector.config.Identity.NodeID(), LastError: connector.lastErr, Sessions: len(connector.sessions)}
}

// Revoke asks the relay to close and refuse every socket of one controller.
// It is best effort by design: the durable revocation has already committed,
// and a controller that reconnects presents authority the daemon no longer
// honours, so there is nothing for a caller to do about a lost record.
func (connector *Connector) Revoke(clientID [ControllerIDSize]byte) {
	if connector == nil {
		return
	}
	connector.mu.Lock()
	queue := connector.queue
	connector.mu.Unlock()
	payload, err := json.Marshal(revokePayload{Controller: base64.RawURLEncoding.EncodeToString(clientID[:])})
	if queue == nil || err != nil {
		return
	}
	queue.push(nil, Record{Type: RecordRevoke, Connection: 0, Payload: payload})
}

// Close stops the reconnect loop and joins every goroutine it owns.
func (connector *Connector) Close() error {
	if connector == nil {
		return nil
	}
	connector.closeOnce.Do(func() {
		connector.cancel()
		<-connector.done
	})
	return nil
}

func (connector *Connector) run() {
	defer close(connector.done)
	backoff := connector.config.BaseBackoff
	for connector.ctx.Err() == nil {
		connector.mu.Lock()
		connector.sequence++
		sequence := connector.sequence
		connector.mu.Unlock()

		started := connector.now()
		accepted, forbidden := connector.connect(sequence)
		if accepted && connector.now().Sub(started) >= connector.config.BaseBackoff {
			backoff = connector.config.BaseBackoff
		}
		delay := jitter(backoff)
		// A 403 is an older boot or a misconfiguration, not congestion:
		// retrying it quickly cannot succeed, so it never beats half the
		// ceiling. Status carries the reason for an operator.
		if forbidden && delay < connector.config.MaxBackoff/2 {
			delay = connector.config.MaxBackoff / 2
		}
		timer := time.NewTimer(delay)
		select {
		case <-connector.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff < connector.config.MaxBackoff {
			backoff = min(backoff*2, connector.config.MaxBackoff)
		}
	}
}

func jitter(backoff time.Duration) time.Duration {
	if backoff <= 0 {
		return 0
	}
	half := backoff / 2
	if half <= 0 {
		return backoff
	}
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

// connect performs one dial and, if it is accepted, serves that relay
// connection until it fails. It reports whether the connection was accepted
// and whether the relay refused it with HTTP 403.
func (connector *Connector) connect(sequence uint64) (accepted bool, forbidden bool) {
	connector.mu.Lock()
	connector.failing = false
	connector.mu.Unlock()
	token := HostToken(connector.config.Identity, sequence, connector.now())
	if token == "" {
		connector.fail(fmt.Errorf("%w: host token could not be minted", ErrConfig))
		return false, false
	}
	dialContext, cancelDial := context.WithTimeout(connector.ctx, relayDialTimeout)
	relay, response, err := connector.config.Dialer(dialContext, connector.hostURL, &websocket.DialOptions{
		Subprotocols:    []string{relaySubprotocol, token},
		CompressionMode: websocket.CompressionDisabled,
	})
	cancelDial()
	if err != nil {
		connector.fail(err)
		return false, response != nil && response.StatusCode == http.StatusForbidden
	}
	if relay.Subprotocol() != relaySubprotocol {
		_ = relay.Close(websocket.StatusProtocolError, "")
		connector.fail(fmt.Errorf("%w: accepted subprotocol %q", ErrRelayProtocol, relay.Subprotocol()))
		return false, false
	}
	relay.SetReadLimit(MaxHostMessageBytes)
	connector.serve(relay)
	return true, false
}

// serve owns one accepted relay connection: one writer, one reader, and the
// sessions opened on it. It returns only after every one of them has joined.
func (connector *Connector) serve(relay *websocket.Conn) {
	connectionContext, cancelConnection := context.WithCancel(connector.ctx)
	queue := newOutboundQueue()
	pongs := make(chan struct{}, 1)

	connector.mu.Lock()
	connector.connected = true
	connector.queue = queue
	connector.mu.Unlock()

	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		connector.write(connectionContext, relay, queue, pongs, cancelConnection)
	}()

	connector.read(connectionContext, relay, queue, pongs)

	// Relay loss ends every session at once. Each loopback socket is closed
	// with 1001 before the context is cancelled so the daemon observes a
	// deliberate going-away, not a torn connection.
	connector.mu.Lock()
	connector.connected = false
	connector.queue = nil
	sessions := make([]*session, 0, len(connector.sessions))
	for _, current := range connector.sessions {
		sessions = append(sessions, current)
	}
	connector.sessions = make(map[uint32]*session)
	connector.mu.Unlock()

	var shutdown sync.WaitGroup
	for _, current := range sessions {
		shutdown.Add(1)
		go func(current *session) {
			defer shutdown.Done()
			current.shutdown(websocket.StatusGoingAway)
		}(current)
	}
	shutdown.Wait()
	cancelConnection()
	connector.sessionGroup.Wait()
	queue.close()
	writer.Wait()
	_ = relay.CloseNow()
}

// read is the only reader of the relay connection.
func (connector *Connector) read(ctx context.Context, relay *websocket.Conn, queue *outboundQueue, pongs chan struct{}) {
	for {
		kind, message, err := relay.Read(ctx)
		if err != nil {
			connector.fail(err)
			return
		}
		if kind == websocket.MessageText {
			if string(message) != "pong" {
				connector.fail(fmt.Errorf("%w: unexpected text frame", ErrRelayProtocol))
				return
			}
			select {
			case pongs <- struct{}{}:
			default:
			}
			continue
		}
		records, err := DecodeRecords(message)
		if err != nil {
			connector.fail(err)
			return
		}
		for _, record := range records {
			if err := connector.apply(ctx, record, queue); err != nil {
				connector.fail(err)
				return
			}
		}
	}
}

// write is the only writer of the relay connection. It coalesces everything
// queued into as few messages as the bound allows and owns the keepalive.
func (connector *Connector) write(ctx context.Context, relay *websocket.Conn, queue *outboundQueue, pongs chan struct{}, fail context.CancelFunc) {
	ping := time.NewTicker(connector.config.PingInterval)
	defer ping.Stop()
	pong := time.NewTimer(connector.config.PongTimeout)
	pong.Stop()
	defer pong.Stop()
	// The deadline runs from the first unanswered keepalive, not from the
	// most recent one: re-arming it every interval would let a relay that
	// never answers hold the connection open forever.
	awaiting := false
	send := func(kind websocket.MessageType, payload []byte) bool {
		writeContext, cancel := context.WithTimeout(ctx, relayWriteTimeout)
		err := relay.Write(writeContext, kind, payload)
		cancel()
		if err != nil {
			connector.fail(err)
			fail()
			return false
		}
		return true
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-queue.signal:
			for _, message := range coalesce(queue.drain()) {
				if !send(websocket.MessageBinary, message) {
					return
				}
			}
		case <-ping.C:
			if !send(websocket.MessageText, []byte("ping")) {
				return
			}
			if !awaiting {
				awaiting = true
				pong.Reset(connector.config.PongTimeout)
			}
		case <-pongs:
			awaiting = false
			pong.Stop()
		case <-pong.C:
			connector.fail(fmt.Errorf("%w: keepalive was not answered", ErrRelayProtocol))
			fail()
			return
		}
	}
}

// apply routes one relay record. A returned error drops the whole relay
// connection; a session-level problem is handled by closing that session.
func (connector *Connector) apply(ctx context.Context, record Record, queue *outboundQueue) error {
	switch record.Type {
	case RecordOpen:
		return connector.open(ctx, record, queue)
	case RecordText, RecordBinary:
		if record.Connection == 0 {
			return fmt.Errorf("%w: frame on connection 0", ErrRelayProtocol)
		}
		current := connector.sessionFor(record.Connection)
		if current == nil {
			// A frame can already be in flight when a session ends, so an
			// unroutable frame is dropped alone. The violations that mean a
			// broken relay - an unknown type, a malformed payload, a reused
			// connection id - still end the whole connection below.
			return nil
		}
		if !current.deliver(record) {
			connector.dropSession(current, queue, relayCloseSlow, "session not draining")
		}
		return nil
	case RecordClose:
		return connector.closeSession(record)
	case RecordRevoke:
		return fmt.Errorf("%w: REVOKE is host to relay only", ErrRelayProtocol)
	default:
		return fmt.Errorf("%w: 0x%02x", ErrRecordType, byte(record.Type))
	}
}

func (connector *Connector) open(ctx context.Context, record Record, queue *outboundQueue) error {
	if record.Connection == 0 {
		return fmt.Errorf("%w: OPEN on connection 0", ErrRelayProtocol)
	}
	var payload openPayload
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		return fmt.Errorf("%w: OPEN payload is not an object", ErrRelayProtocol)
	}
	if _, err := decodeFixed(payload.Controller, ControllerIDSize); err != nil {
		return fmt.Errorf("%w: OPEN controller", ErrRelayProtocol)
	}
	if payload.Purpose != PurposePair && payload.Purpose != PurposeControl {
		return fmt.Errorf("%w: OPEN purpose %q", ErrRelayProtocol, payload.Purpose)
	}
	if !validOrigin(payload.Origin) {
		return fmt.Errorf("%w: OPEN origin", ErrRelayProtocol)
	}
	connector.mu.Lock()
	if _, duplicate := connector.sessions[record.Connection]; duplicate {
		connector.mu.Unlock()
		return fmt.Errorf("%w: OPEN reuses a live connection id", ErrRelayProtocol)
	}
	if len(connector.sessions) >= maxSessions {
		connector.mu.Unlock()
		queue.push(nil, closeRecord(record.Connection, relayCloseSlow, "session capacity"))
		return nil
	}
	current := newSession(record.Connection)
	connector.sessions[record.Connection] = current
	connector.mu.Unlock()

	connector.sessionGroup.Add(1)
	go func() {
		defer connector.sessionGroup.Done()
		connector.runSession(ctx, current, payload.Origin, queue)
	}()
	return nil
}

func (connector *Connector) closeSession(record Record) error {
	if record.Connection == 0 {
		return fmt.Errorf("%w: CLOSE on connection 0", ErrRelayProtocol)
	}
	// The code in a relay CLOSE is validated and then discarded by design:
	// the daemon has no use for a controller's close code, so the loopback
	// socket gets a normal closure either way.
	if len(record.Payload) > 0 {
		var payload closePayload
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			return fmt.Errorf("%w: CLOSE payload is not an object", ErrRelayProtocol)
		}
	}
	connector.mu.Lock()
	current := connector.sessions[record.Connection]
	delete(connector.sessions, record.Connection)
	connector.mu.Unlock()
	if current == nil {
		return nil
	}
	current.markRelayClosed()
	// Detached because the close handshake must not stall the relay read
	// loop; joined by serve through sessionGroup.
	connector.shutdownAsync(current, websocket.StatusNormalClosure)
	return nil
}

// shutdownAsync closes one loopback socket off the read loop while keeping it
// inside the group Close joins.
func (connector *Connector) shutdownAsync(current *session, status websocket.StatusCode) {
	connector.sessionGroup.Add(1)
	go func() {
		defer connector.sessionGroup.Done()
		current.shutdown(status)
	}()
}

// runSession dials the daemon's own loopback listener as an ordinary browser
// client and pipes frames both ways. The daemon therefore cannot tell this
// session from a local one.
func (connector *Connector) runSession(ctx context.Context, current *session, origin string, queue *outboundQueue) {
	if current.relayInitiated() {
		// The CLOSE was applied before this goroutine was scheduled - an
		// OPEN and its CLOSE in one message - so never dial at all.
		connector.finishSession(current, queue, 0, "")
		return
	}
	sessionContext, cancel := context.WithCancel(ctx)
	defer cancel()
	header := make(http.Header)
	header.Set("Origin", origin)
	dialContext, cancelDial := context.WithTimeout(sessionContext, loopbackDialTimeout)
	loopback, _, err := connector.config.Dialer(dialContext, connector.config.BrowserURL, &websocket.DialOptions{
		HTTPHeader:      header,
		CompressionMode: websocket.CompressionDisabled,
	})
	cancelDial()
	if err != nil {
		code := relayCloseUnavailable
		if current.relayInitiated() {
			code = 0
		}
		connector.finishSession(current, queue, code, "loopback unavailable")
		return
	}
	loopback.SetReadLimit(maxRecordPayloadBytes)
	if !current.setLoopback(loopback) {
		// The relay ended this session during the dial, so its shutdown found
		// no socket and this goroutine owns the close.
		_ = loopback.CloseNow()
		connector.finishSession(current, queue, 0, "")
		return
	}

	var pump sync.WaitGroup
	pump.Add(1)
	go func() {
		defer pump.Done()
		current.pump(sessionContext)
	}()

	code := relayCloseUnavailable
	for {
		kind, frame, readErr := loopback.Read(sessionContext)
		if readErr != nil {
			code = loopbackCloseCode(readErr)
			break
		}
		recordType := RecordBinary
		if kind == websocket.MessageText {
			recordType = RecordText
		}
		if !queue.push(current, Record{Type: recordType, Connection: current.connection, Payload: frame}) {
			code = relayCloseSlow
			break
		}
		// The daemon's bytes go first and unaltered; the relay ticket follows
		// as its own frame on the same session, in order.
		var ticket []byte
		if recordType == RecordText {
			ticket = connector.ticketFrame(sessionContext, frame)
		}
		if ticket != nil && !queue.push(current, Record{Type: RecordText, Connection: current.connection, Payload: ticket}) {
			code = relayCloseSlow
			break
		}
	}
	cancel()
	pump.Wait()
	_ = loopback.CloseNow()
	if current.relayInitiated() {
		// The relay ended this session; echoing a CLOSE back would name a
		// connection the relay has already forgotten.
		code = 0
	}
	connector.finishSession(current, queue, code, "")
}

// dropSession ends one session without touching the relay connection.
func (connector *Connector) dropSession(current *session, queue *outboundQueue, code int, reason string) {
	connector.forget(current)
	current.markRelayClosed()
	queue.push(nil, closeRecord(current.connection, code, reason))
	connector.shutdownAsync(current, websocket.StatusPolicyViolation)
}

func (connector *Connector) finishSession(current *session, queue *outboundQueue, code int, reason string) {
	connector.forget(current)
	if code == 0 {
		return
	}
	queue.push(nil, closeRecord(current.connection, code, reason))
}

func (connector *Connector) sessionFor(connection uint32) *session {
	connector.mu.Lock()
	defer connector.mu.Unlock()
	return connector.sessions[connection]
}

func (connector *Connector) forget(current *session) {
	connector.mu.Lock()
	if connector.sessions[current.connection] == current {
		delete(connector.sessions, current.connection)
	}
	connector.mu.Unlock()
}

// fail retains the first failure of the current dial. Later errors are the
// consequences of that one - a write onto a connection already being torn
// down - and would hide the cause. A failure caused by this connector's own
// Close is not a relay failure and is not retained.
func (connector *Connector) fail(err error) {
	if err == nil || connector.ctx.Err() != nil {
		return
	}
	connector.mu.Lock()
	if !connector.failing {
		connector.failing = true
		connector.lastErr = err
	}
	connector.mu.Unlock()
}

func closeRecord(connection uint32, code int, reason string) Record {
	payload, err := json.Marshal(closePayload{Code: code, Reason: reason})
	if err != nil {
		payload = nil
	}
	return Record{Type: RecordClose, Connection: connection, Payload: payload}
}

// validOrigin accepts exactly the shape the loopback browser listener will
// compare against its own configured origins.
func validOrigin(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != "" && parsed.User == nil &&
		parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == "" && parsed.String() == value && !strings.Contains(value, "*")
}
