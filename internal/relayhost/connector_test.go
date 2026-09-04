package relayhost

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type harness struct {
	identity  Identity
	relay     *fakeRelay
	loopback  *fakeLoopback
	connector *Connector
	device    [DeviceKeySize]byte
}

func newHarness(t *testing.T, adjust func(*Config)) *harness {
	return newPreparedHarness(t, nil, adjust)
}

// newPreparedHarness configures the fake relay before the connector's first
// dial, so a test about refused dials cannot race a successful one.
func newPreparedHarness(t *testing.T, prepare func(*fakeRelay), adjust func(*Config)) *harness {
	t.Helper()
	identity := testIdentity(t)
	relay := newFakeRelay(t, identity.PublicKey())
	if prepare != nil {
		prepare(relay)
	}
	loopback := newFakeLoopback(t)
	fixture := &harness{identity: identity, relay: relay, loopback: loopback}
	fixture.device[0] = 4
	for index := 1; index < DeviceKeySize; index++ {
		fixture.device[index] = byte(index * 3)
	}
	config := Config{
		RelayOrigin: relay.origin(),
		Identity:    identity,
		BrowserURL:  loopback.url(),
		DeviceKey: func(context.Context, [ControllerIDSize]byte) ([DeviceKeySize]byte, bool, error) {
			return fixture.device, true, nil
		},
		TicketLifetime: time.Hour,
		BaseBackoff:    20 * time.Millisecond,
		MaxBackoff:     80 * time.Millisecond,
		// Keepalive is disabled unless a test is about keepalive, so an
		// unrelated timing assertion cannot be rescued or broken by a ping.
		PingInterval: time.Hour,
		PongTimeout:  time.Hour,
	}
	if adjust != nil {
		adjust(&config)
	}
	connector, err := Dial(context.Background(), config)
	if err != nil {
		t.Fatalf("dial connector: %v", err)
	}
	t.Cleanup(func() {
		if err := connector.Close(); err != nil {
			t.Errorf("close connector: %v", err)
		}
	})
	fixture.connector = connector
	return fixture
}

func controllerID(seed uint32) [ControllerIDSize]byte {
	var controller [ControllerIDSize]byte
	binary.BigEndian.PutUint32(controller[:4], seed)
	controller[15] = 1
	return controller
}

// openSession opens one relayed controller and consumes the daemon's opening
// HELLO, so a later assertion cannot accidentally match it.
func (fixture *harness) openSession(t *testing.T, host *hostSocket, connection uint32) *loopbackSession {
	t.Helper()
	host.open(t, connection, controllerID(connection), loopbackOrigin)
	session := fixture.loopback.accept(t)
	hello := host.expect(t, RecordText, connection)
	if !bytes.Contains(hello.Payload, []byte("HELLO")) {
		t.Fatalf("first relayed frame = %q, want the daemon HELLO", hello.Payload)
	}
	return session
}

func TestConnectorOpensALoopbackSessionWithTheExactValidatedOrigin(t *testing.T) {
	fixture := newHarness(t, nil)
	host := fixture.relay.accept(t)
	session := fixture.openSession(t, host, 5)
	if session.observedHost != fixture.loopback.address() {
		t.Fatalf("loopback Host = %q, want %q", session.observedHost, fixture.loopback.address())
	}
	if refusals := fixture.loopback.refusals(); refusals != 0 {
		t.Fatalf("loopback refused %d handshakes for a correct origin", refusals)
	}
	if status := fixture.connector.Status(); !status.Connected || status.Sessions != 1 || status.NodeID != fixture.identity.NodeID() {
		t.Fatalf("status = %+v", status)
	}
}

func TestConnectorClosesASessionTheLoopbackListenerRefuses(t *testing.T) {
	fixture := newHarness(t, nil)
	host := fixture.relay.accept(t)
	host.open(t, 6, controllerID(6), "https://impostor.example")
	record := host.expect(t, RecordClose, 6)
	var payload closePayload
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Code != relayCloseUnavailable {
		t.Fatalf("close code = %d, want %d", payload.Code, relayCloseUnavailable)
	}
	if refusals := fixture.loopback.refusals(); refusals != 1 {
		t.Fatalf("loopback refused %d handshakes, want exactly the impostor origin", refusals)
	}
}

func TestConnectorPreservesFrameKindInBothDirections(t *testing.T) {
	fixture := newHarness(t, nil)
	host := fixture.relay.accept(t)
	session := fixture.openSession(t, host, 7)

	host.send(t, Record{Type: RecordText, Connection: 7, Payload: []byte(`{"v":1,"type":"STATE_GET"}`)})
	if frame := session.expect(t); frame.kind != websocket.MessageText || string(frame.payload) != `{"v":1,"type":"STATE_GET"}` {
		t.Fatalf("text to loopback = kind %d, %q", frame.kind, frame.payload)
	}
	host.send(t, Record{Type: RecordBinary, Connection: 7, Payload: []byte{0x01, 0x02, 0x03}})
	if frame := session.expect(t); frame.kind != websocket.MessageBinary || !bytes.Equal(frame.payload, []byte{0x01, 0x02, 0x03}) {
		t.Fatalf("binary to loopback = kind %d, %q", frame.kind, frame.payload)
	}

	session.write(t, websocket.MessageBinary, []byte{0x0a, 0x0b})
	if record := host.expect(t, RecordBinary, 7); !bytes.Equal(record.Payload, []byte{0x0a, 0x0b}) {
		t.Fatalf("binary to relay = %q", record.Payload)
	}
	session.write(t, websocket.MessageText, []byte(`{"v":1,"type":"STATE_CHANGED"}`))
	if record := host.expect(t, RecordText, 7); string(record.Payload) != `{"v":1,"type":"STATE_CHANGED"}` {
		t.Fatalf("text to relay = %q", record.Payload)
	}
}

func TestConnectorDeliversOneMessageOfThreeRecordsInOrderToTwoSessions(t *testing.T) {
	fixture := newHarness(t, nil)
	host := fixture.relay.accept(t)
	first := fixture.openSession(t, host, 11)
	second := fixture.openSession(t, host, 12)

	host.send(t,
		Record{Type: RecordText, Connection: 11, Payload: []byte("first-a")},
		Record{Type: RecordText, Connection: 12, Payload: []byte("second-a")},
		Record{Type: RecordText, Connection: 11, Payload: []byte("first-b")},
	)
	if frame := first.expect(t); string(frame.payload) != "first-a" {
		t.Fatalf("session 11 frame 1 = %q", frame.payload)
	}
	if frame := first.expect(t); string(frame.payload) != "first-b" {
		t.Fatalf("session 11 frame 2 = %q", frame.payload)
	}
	if frame := second.expect(t); string(frame.payload) != "second-a" {
		t.Fatalf("session 12 frame 1 = %q", frame.payload)
	}
}

func TestConnectorCarriesCloseCodesInBothDirections(t *testing.T) {
	fixture := newHarness(t, nil)
	host := fixture.relay.accept(t)

	// Relay to loopback: the session ends, and a frame already in flight for
	// it is dropped alone rather than ending every other session.
	relayClosed := fixture.openSession(t, host, 21)
	survivor := fixture.openSession(t, host, 24)
	host.closeConnection(t, 21, 4002)
	if status := relayClosed.waitClosed(t); status != websocket.StatusNormalClosure {
		t.Fatalf("loopback close status = %d, want a normal closure", status)
	}
	host.send(t, Record{Type: RecordText, Connection: 21, Payload: []byte("late")})
	survivor.write(t, websocket.MessageText, []byte("unaffected"))
	if record := host.expect(t, RecordText, 24); string(record.Payload) != "unaffected" {
		t.Fatalf("a late frame for a closed session ended another session: %q", record.Payload)
	}

	// Loopback to relay: a code inside the carried range travels verbatim.
	carried := fixture.openSession(t, host, 22)
	if err := carried.conn.Close(4444, "daemon reason"); err != nil {
		t.Fatal(err)
	}
	if code := expectCloseCode(t, host, 22); code != 4444 {
		t.Fatalf("relay close code = %d, want 4444", code)
	}

	// Loopback to relay: anything else becomes 4005 rather than inventing a
	// controller-visible code.
	normal := fixture.openSession(t, host, 23)
	if err := normal.conn.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatal(err)
	}
	if code := expectCloseCode(t, host, 23); code != relayCloseUnavailable {
		t.Fatalf("relay close code = %d, want %d", code, relayCloseUnavailable)
	}
}

func expectCloseCode(t *testing.T, host *hostSocket, connection uint32) int {
	t.Helper()
	record := host.expect(t, RecordClose, connection)
	var payload closePayload
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		t.Fatalf("close payload %q: %v", record.Payload, err)
	}
	return payload.Code
}

func TestRelayLossClosesEverySessionAndTheReconnectPresentsTheNextSequence(t *testing.T) {
	fixture := newHarness(t, nil)
	host := fixture.relay.accept(t)
	first := fixture.openSession(t, host, 31)
	second := fixture.openSession(t, host, 32)
	firstSequence := host.payload.Sequence

	host.drop(t)

	if status := first.waitClosed(t); status != websocket.StatusGoingAway {
		t.Fatalf("first loopback close status = %d, want 1001", status)
	}
	if status := second.waitClosed(t); status != websocket.StatusGoingAway {
		t.Fatalf("second loopback close status = %d, want 1001", status)
	}

	next := fixture.relay.accept(t)
	if next.payload.Sequence != firstSequence+1 {
		t.Fatalf("reconnect sequence = %d, want %d", next.payload.Sequence, firstSequence+1)
	}
	if next.payload.Generation != host.payload.Generation {
		t.Fatalf("reconnect generation = %d, want the same boot %d", next.payload.Generation, host.payload.Generation)
	}
	tokens := fixture.relay.tokens()
	if len(tokens) < 2 || tokens[len(tokens)-1] == tokens[len(tokens)-2] {
		t.Fatalf("reconnect replayed a host token (%d presented)", len(tokens))
	}

	// Nothing is replayed: the reconnected controller gets a brand-new daemon
	// session with its own HELLO.
	fixture.openSession(t, next, 31)
	select {
	case record := <-next.records:
		t.Fatalf("reconnect replayed a record: %+v", record)
	default:
	}
}

func TestOneSlowSessionIsDroppedWithoutDisturbingTheOthers(t *testing.T) {
	fixture := newHarness(t, nil)
	host := fixture.relay.accept(t)
	slow := fixture.openSession(t, host, 41)
	steady := fixture.openSession(t, host, 42)

	// While the relay stops reading, the single writer blocks and the slow
	// session's own bound is the only thing that can end.
	fixture.relay.pauseReads()
	flood := make(chan struct{})
	go func() {
		defer close(flood)
		payload := bytes.Repeat([]byte("x"), 64<<10)
		for index := 0; index < 600; index++ {
			ctx, cancel := context.WithTimeout(context.Background(), testDeadline)
			err := slow.conn.Write(ctx, websocket.MessageBinary, payload)
			cancel()
			if err != nil {
				return
			}
		}
	}()
	slow.waitClosed(t)
	if !steady.stillOpen(t) {
		t.Fatal("the steady session was closed by the slow session's backlog")
	}
	fixture.relay.resumeReads()
	<-flood

	if code := expectCloseCode(t, host, 41); code != relayCloseSlow {
		t.Fatalf("slow session close code = %d, want %d", code, relayCloseSlow)
	}
	steady.write(t, websocket.MessageText, []byte("still serving"))
	if record := host.expect(t, RecordText, 42); string(record.Payload) != "still serving" {
		t.Fatalf("steady session frame = %q", record.Payload)
	}
	select {
	case <-steady.closed:
		t.Fatal("the steady session closed after the slow one was dropped")
	default:
	}
}

func cannedResult(t *testing.T, kind string, clientID [ControllerIDSize]byte) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"v":    1,
		"type": kind,
		"id":   "request-1",
		"body": map[string]any{"client_id": hex.EncodeToString(clientID[:]), "capabilities": 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestAResultFrameIsForwardedVerbatimAndFollowedByItsOwnTicketFrame(t *testing.T) {
	fixture := newHarness(t, nil)
	clientID := controllerID(0xabcd)
	fixture.loopback.mu.Lock()
	fixture.loopback.pairBody = cannedResult(t, "PAIR_RESULT", clientID)
	fixture.loopback.authBody = cannedResult(t, "AUTH_RESULT", clientID)
	canned := map[string][]byte{"PAIR_RESULT": fixture.loopback.pairBody, "AUTH_RESULT": fixture.loopback.authBody}
	fixture.loopback.mu.Unlock()

	host := fixture.relay.accept(t)
	session := fixture.openSession(t, host, 51)

	seen := make(map[string]struct{})
	for _, probe := range []struct{ request, kind string }{
		{request: `{"v":1,"type":"PAIR_PROVE","id":"request-1","body":{}}`, kind: "PAIR_RESULT"},
		{request: `{"v":1,"type":"AUTH_PROVE","id":"request-1","body":{}}`, kind: "AUTH_RESULT"},
	} {
		host.send(t, Record{Type: RecordText, Connection: 51, Payload: []byte(probe.request)})
		if result := host.expect(t, RecordText, 51); !bytes.Equal(result.Payload, canned[probe.kind]) {
			t.Fatalf("%s was rewritten in flight:\n got %q\nwant %q", probe.kind, result.Payload, canned[probe.kind])
		}
		payload := verifyTicketFrame(t, fixture, host.expect(t, RecordText, 51).Payload)
		if _, duplicate := seen[payload.Ticket]; duplicate {
			t.Fatalf("%s reused a ticket id", probe.kind)
		}
		seen[payload.Ticket] = struct{}{}
		if raw, err := base64.RawURLEncoding.DecodeString(payload.Controller); err != nil || !bytes.Equal(raw, clientID[:]) {
			t.Fatalf("%s ticket names controller %q", probe.kind, payload.Controller)
		}
		if raw, err := base64.RawURLEncoding.DecodeString(payload.Device); err != nil || !bytes.Equal(raw, fixture.device[:]) {
			t.Fatalf("%s ticket names device %q", probe.kind, payload.Device)
		}
	}

	// Any other frame travels alone.
	other := []byte(`{"v":1,"type":"STATE_CHANGED","body":{"head":"1"}}`)
	session.write(t, websocket.MessageText, other)
	if record := host.expect(t, RecordText, 51); !bytes.Equal(record.Payload, other) {
		t.Fatalf("unrelated frame was rewritten: %q", record.Payload)
	}
	session.write(t, websocket.MessageText, []byte(`{"v":1,"type":"STATE_CHANGED","body":{"head":"2"}}`))
	if record := host.expect(t, RecordText, 51); bytes.Contains(record.Payload, []byte("RELAY_TICKET")) {
		t.Fatalf("an unrelated frame produced a ticket frame: %q", record.Payload)
	}
}

func verifyTicketFrame(t *testing.T, fixture *harness, payload []byte) TicketPayload {
	t.Helper()
	var frame struct {
		Type   string `json:"type"`
		Ticket string `json:"ticket"`
	}
	if err := json.Unmarshal(payload, &frame); err != nil || frame.Type != "RELAY_TICKET" {
		t.Fatalf("ticket frame = %q, %v", payload, err)
	}
	if want := `{"type":"RELAY_TICKET","ticket":"` + frame.Ticket + `"}`; string(payload) != want {
		t.Fatalf("ticket frame = %q, want exactly %q", payload, want)
	}
	verified, err := VerifyTicket(fixture.identity.PublicKey(), frame.Ticket)
	if err != nil {
		t.Fatalf("ticket does not verify against the node key: %v", err)
	}
	return verified
}

func TestNoTicketFrameFollowsWhenTheDeviceKeyIsGone(t *testing.T) {
	clientID := controllerID(0xbeef)
	fixture := newHarness(t, func(config *Config) {
		config.DeviceKey = func(context.Context, [ControllerIDSize]byte) ([DeviceKeySize]byte, bool, error) {
			return [DeviceKeySize]byte{}, false, nil
		}
	})
	canned := cannedResult(t, "AUTH_RESULT", clientID)
	fixture.loopback.mu.Lock()
	fixture.loopback.authBody = canned
	fixture.loopback.mu.Unlock()
	host := fixture.relay.accept(t)
	session := fixture.openSession(t, host, 52)
	host.send(t, Record{Type: RecordText, Connection: 52, Payload: []byte(`{"v":1,"type":"AUTH_PROVE","id":"request-1","body":{}}`)})
	if record := host.expect(t, RecordText, 52); !bytes.Equal(record.Payload, canned) {
		t.Fatalf("revoked client frame = %q, want the original bytes", record.Payload)
	}
	// The next frame is the following application frame, not a ticket.
	session.write(t, websocket.MessageText, []byte("sentinel"))
	if record := host.expect(t, RecordText, 52); string(record.Payload) != "sentinel" {
		t.Fatalf("a ticket frame followed a result with no device key: %q", record.Payload)
	}
}

func TestOpensBeyondTheLoopbackSlotCountAreRefusedWithoutASession(t *testing.T) {
	fixture := newHarness(t, nil)
	host := fixture.relay.accept(t)
	records := make([]Record, 0, maxSessions+1)
	for index := 0; index <= maxSessions; index++ {
		controller := controllerID(uint32(200 + index))
		payload, err := json.Marshal(openPayload{Controller: base64.RawURLEncoding.EncodeToString(controller[:]), Purpose: PurposeControl, Origin: loopbackOrigin})
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, Record{Type: RecordOpen, Connection: uint32(200 + index), Payload: payload})
	}
	host.send(t, records...)

	if code := expectCloseCode(t, host, uint32(200+maxSessions)); code != relayCloseSlow {
		t.Fatalf("overflow OPEN close code = %d, want %d", code, relayCloseSlow)
	}
	for index := 0; index < maxSessions; index++ {
		fixture.loopback.accept(t)
	}
	if sessions := fixture.connector.Status().Sessions; sessions != maxSessions {
		t.Fatalf("sessions = %d, want the loopback slot count %d", sessions, maxSessions)
	}
	select {
	case session := <-fixture.loopback.opened:
		t.Fatalf("the refused OPEN still dialled the loopback listener: %q", session.observedHost)
	default:
	}
}

func TestAnUnknownRecordTypeDropsTheRelayConnection(t *testing.T) {
	fixture := newHarness(t, nil)
	host := fixture.relay.accept(t)
	session := fixture.openSession(t, host, 61)
	host.sendRaw(t, []byte{0x09, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00})
	session.waitClosed(t)
	next := fixture.relay.accept(t)
	if next.payload.Sequence != host.payload.Sequence+1 {
		t.Fatalf("sequence after a protocol violation = %d, want %d", next.payload.Sequence, host.payload.Sequence+1)
	}
	if status := fixture.connector.Status(); !errors.Is(status.LastError, ErrRecordType) {
		t.Fatalf("status error = %v, want ErrRecordType", status.LastError)
	}
}

func TestATextFrameOtherThanPongDropsTheRelayConnection(t *testing.T) {
	fixture := newHarness(t, nil)
	host := fixture.relay.accept(t)
	ctx, cancel := context.WithTimeout(context.Background(), testDeadline)
	defer cancel()
	if err := host.conn.Write(ctx, websocket.MessageText, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	next := fixture.relay.accept(t)
	if next.payload.Sequence != host.payload.Sequence+1 {
		t.Fatalf("sequence after an unexpected text frame = %d, want %d", next.payload.Sequence, host.payload.Sequence+1)
	}
	if status := fixture.connector.Status(); !errors.Is(status.LastError, ErrRelayProtocol) {
		t.Fatalf("status error = %v, want ErrRelayProtocol", status.LastError)
	}
}

func TestAnUnansweredKeepaliveDropsTheRelayConnection(t *testing.T) {
	fixture := newPreparedHarness(t, func(relay *fakeRelay) { relay.autoPong = false }, func(config *Config) {
		config.PingInterval = 60 * time.Millisecond
		config.PongTimeout = 30 * time.Millisecond
	})
	host := fixture.relay.accept(t)
	select {
	case text := <-host.texts:
		if text != "ping" {
			t.Fatalf("keepalive text = %q, want ping", text)
		}
	case <-time.After(testDeadline):
		t.Fatal("no keepalive was sent")
	}
	next := fixture.relay.accept(t)
	if next.payload.Sequence != host.payload.Sequence+1 {
		t.Fatalf("sequence after a keepalive timeout = %d, want %d", next.payload.Sequence, host.payload.Sequence+1)
	}
}

// An answered keepalive must not drop the connection, so the test above is
// caused by the missing pong rather than by the interval alone.
func TestAnAnsweredKeepaliveHoldsTheRelayConnection(t *testing.T) {
	fixture := newHarness(t, func(config *Config) {
		config.PingInterval = 60 * time.Millisecond
		config.PongTimeout = 30 * time.Millisecond
	})
	host := fixture.relay.accept(t)
	for index := 0; index < 4; index++ {
		select {
		case text := <-host.texts:
			if text != "ping" {
				t.Fatalf("keepalive text = %q", text)
			}
		case <-host.closed:
			t.Fatal("an answered keepalive dropped the connection")
		case <-time.After(testDeadline):
			t.Fatal("keepalive stopped")
		}
	}
	if attempts := len(fixture.relay.tokens()); attempts != 1 {
		t.Fatalf("%d dials while keepalives were answered, want 1", attempts)
	}
}

func TestRevokeSendsOneRecordOnlyWhileConnected(t *testing.T) {
	fixture := newHarness(t, nil)
	host := fixture.relay.accept(t)
	fixture.openSession(t, host, 71)
	clientID := controllerID(0x1234)
	if !fixture.connector.Revoke(clientID) {
		t.Fatal("Revoke did not reach the live relay connection")
	}
	record := host.expect(t, RecordRevoke, 0)
	var payload revokePayload
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		t.Fatalf("revoke payload %q: %v", record.Payload, err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload.Controller)
	if err != nil || !bytes.Equal(raw, clientID[:]) {
		t.Fatalf("revoke names controller %q, %v", payload.Controller, err)
	}
	if err := fixture.connector.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if fixture.connector.Revoke(clientID) {
		t.Fatal("Revoke claimed to reach a closed connector")
	}
}

func TestAForbiddenHostIsRetriedNoFasterThanHalfTheCeiling(t *testing.T) {
	fixture := newPreparedHarness(t, func(relay *fakeRelay) { relay.forbid = true }, func(config *Config) {
		config.BaseBackoff = 5 * time.Millisecond
		config.MaxBackoff = 400 * time.Millisecond
	})

	deadline := time.Now().Add(600 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	attempts := len(fixture.relay.tokens())
	// Unfloored exponential backoff from 5ms would fit roughly eight
	// attempts into this window; the 200ms floor fits at most four.
	if attempts < 2 {
		t.Fatalf("a forbidden host stopped retrying after %d attempts", attempts)
	}
	if attempts > 4 {
		t.Fatalf("a forbidden host was retried %d times in 600ms, faster than half the ceiling", attempts)
	}
	status := fixture.connector.Status()
	if status.Connected || status.LastError == nil || !strings.Contains(status.LastError.Error(), "403") {
		t.Fatalf("status = %+v", status)
	}
}

func TestAWrongAcceptedSubprotocolIsRefused(t *testing.T) {
	fixture := newPreparedHarness(t, func(relay *fakeRelay) { relay.selectToken = true }, nil)
	deadline := time.After(testDeadline)
	for {
		status := fixture.connector.Status()
		if errors.Is(status.LastError, ErrRelayProtocol) && !status.Connected && len(fixture.relay.tokens()) >= 2 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("wrong subprotocol was not refused and retried: %+v after %d dials", status, len(fixture.relay.tokens()))
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestDialRefusesAnInvalidConfiguration(t *testing.T) {
	identity := testIdentity(t)
	for _, testCase := range []struct {
		name   string
		config Config
	}{
		{name: "relay origin with a path", config: Config{RelayOrigin: "wss://relay.example/host", Identity: identity, BrowserURL: "ws://127.0.0.1:1/browser/v2"}},
		{name: "relay origin over https", config: Config{RelayOrigin: "https://relay.example", Identity: identity, BrowserURL: "ws://127.0.0.1:1/browser/v2"}},
		{name: "relay origin with a query", config: Config{RelayOrigin: "wss://relay.example?a=b", Identity: identity, BrowserURL: "ws://127.0.0.1:1/browser/v2"}},
		{name: "unloaded identity", config: Config{RelayOrigin: "wss://relay.example", BrowserURL: "ws://127.0.0.1:1/browser/v2"}},
		{name: "browser URL without a path", config: Config{RelayOrigin: "wss://relay.example", Identity: identity, BrowserURL: "ws://127.0.0.1:1"}},
	} {
		if _, err := Dial(context.Background(), testCase.config); !errors.Is(err, ErrConfig) {
			t.Fatalf("%s = %v, want ErrConfig", testCase.name, err)
		}
	}
}

func TestCloseIsIdempotentAndJoinsEverything(t *testing.T) {
	fixture := newHarness(t, nil)
	host := fixture.relay.accept(t)
	session := fixture.openSession(t, host, 81)
	if err := fixture.connector.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := fixture.connector.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	session.waitClosed(t)
	if status := fixture.connector.Status(); status.Connected || status.Sessions != 0 {
		t.Fatalf("status after close = %+v", status)
	}
}
