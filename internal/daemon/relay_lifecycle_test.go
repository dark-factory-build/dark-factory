//go:build darwin || linux

package daemon

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/relayhost"
)

const relayTestDeadline = 20 * time.Second

// relayServer is a minimal stand-in for the relay Worker's host side. It
// verifies the host token with the node public key and speaks the record
// envelope, then routes records to one channel per controller connection so
// several relayed controllers can be in flight at once.
type relayServer struct {
	server   *httptest.Server
	key      relayhost.Identity
	nodeID   string
	accepted chan *relayHost

	mu           sync.Mutex
	dials        int
	lastAccepted relayhost.HostTokenPayload
}

type relayHost struct {
	conn    *websocket.Conn
	payload relayhost.HostTokenPayload
	closed  chan struct{}
	revokes chan relayhost.Record

	mu     sync.Mutex
	routes map[uint32]chan relayhost.Record
}

func newRelayServer(t *testing.T, identity relayhost.Identity) *relayServer {
	t.Helper()
	relay := &relayServer{key: identity, nodeID: identity.NodeID(), accepted: make(chan *relayHost, 8)}
	relay.server = httptest.NewServer(http.HandlerFunc(relay.handle))
	t.Cleanup(relay.server.Close)
	return relay
}

func (relay *relayServer) origin() string {
	return "ws://" + strings.TrimPrefix(relay.server.URL, "http://")
}

func (relay *relayServer) handle(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/host/"+relay.nodeID || request.Header.Get("Origin") != "" {
		http.Error(writer, "forbidden", http.StatusForbidden)
		return
	}
	offered := strings.Split(request.Header.Get("Sec-WebSocket-Protocol"), ",")
	for index := range offered {
		offered[index] = strings.TrimSpace(offered[index])
	}
	if len(offered) != 2 || offered[0] != "dark-factory-relay" {
		http.Error(writer, "forbidden", http.StatusForbidden)
		return
	}
	payload, err := relayhost.VerifyHostToken(relay.key.PublicKey(), offered[1])
	relay.mu.Lock()
	relay.dials++
	stale := payload.Generation < relay.lastAccepted.Generation ||
		payload.Generation == relay.lastAccepted.Generation && payload.Sequence <= relay.lastAccepted.Sequence
	if err == nil && !stale {
		relay.lastAccepted = payload
	}
	relay.mu.Unlock()
	if err != nil || stale {
		http.Error(writer, "forbidden", http.StatusForbidden)
		return
	}
	connection, err := websocket.Accept(writer, request, &websocket.AcceptOptions{Subprotocols: []string{"dark-factory-relay"}, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	connection.SetReadLimit(relayhost.MaxHostMessageBytes)
	host := &relayHost{conn: connection, payload: payload, closed: make(chan struct{}), revokes: make(chan relayhost.Record, 16), routes: make(map[uint32]chan relayhost.Record)}
	relay.accepted <- host
	host.read()
}

func (host *relayHost) read() {
	defer close(host.closed)
	ctx := context.Background()
	for {
		kind, message, err := host.conn.Read(ctx)
		if err != nil {
			return
		}
		if kind == websocket.MessageText {
			if string(message) == "ping" {
				_ = host.conn.Write(ctx, websocket.MessageText, []byte("pong"))
			}
			continue
		}
		records, err := relayhost.DecodeRecords(message)
		if err != nil {
			return
		}
		for _, record := range records {
			copied := relayhost.Record{Type: record.Type, Connection: record.Connection, Payload: append([]byte(nil), record.Payload...)}
			if copied.Type == relayhost.RecordRevoke {
				select {
				case host.revokes <- copied:
				default:
				}
				continue
			}
			host.mu.Lock()
			route := host.routes[copied.Connection]
			host.mu.Unlock()
			if route == nil {
				continue
			}
			select {
			case route <- copied:
			default:
			}
		}
	}
}

func (relay *relayServer) accept(t *testing.T) *relayHost {
	t.Helper()
	select {
	case host := <-relay.accepted:
		return host
	case <-time.After(relayTestDeadline):
		t.Fatal("relay never accepted a host connection")
		return nil
	}
}

func (host *relayHost) write(t *testing.T, record relayhost.Record) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), relayTestDeadline)
	defer cancel()
	if err := host.conn.Write(ctx, websocket.MessageBinary, relayhost.AppendRecord(nil, record)); err != nil {
		t.Fatalf("relay write: %v", err)
	}
}

// relayController is one PWA installation reached through the relay.
type relayController struct {
	host       *relayHost
	connection uint32
	records    chan relayhost.Record
	key        *ecdsa.PrivateKey
	client     kernel.BrowserClient
	ticket     string
}

func (host *relayHost) open(t *testing.T, connection uint32, key *ecdsa.PrivateKey) *relayController {
	t.Helper()
	records := make(chan relayhost.Record, 64)
	host.mu.Lock()
	host.routes[connection] = records
	host.mu.Unlock()
	var controller [relayhost.ControllerIDSize]byte
	controller[0] = byte(connection)
	controller[15] = 1
	payload, err := json.Marshal(map[string]string{
		"controller": base64.RawURLEncoding.EncodeToString(controller[:]),
		"purpose":    "control",
		"origin":     adapterOrigin,
	})
	if err != nil {
		t.Fatal(err)
	}
	host.write(t, relayhost.Record{Type: relayhost.RecordOpen, Connection: connection, Payload: payload})
	return &relayController{host: host, connection: connection, records: records, key: key}
}

func (controller *relayController) send(t *testing.T, payload []byte) {
	t.Helper()
	controller.host.write(t, relayhost.Record{Type: relayhost.RecordText, Connection: controller.connection, Payload: payload})
}

func (controller *relayController) next(t *testing.T) relayhost.Record {
	t.Helper()
	select {
	case record := <-controller.records:
		return record
	case <-time.After(relayTestDeadline):
		t.Fatalf("relayed controller %d received nothing", controller.connection)
		return relayhost.Record{}
	}
}

// frame reads the next relayed application frame and decodes it with the real
// server decoder, after lifting out the relay's added ticket member.
func (controller *relayController) frame(t *testing.T) (browserprotocol.ControlFrame, string) {
	t.Helper()
	record := controller.next(t)
	if record.Type != relayhost.RecordText {
		t.Fatalf("relayed record type = 0x%02x, want TEXT", byte(record.Type))
	}
	cleaned, ticket := captureRelayTicketAsTheTransportDoes(t, record.Payload)
	frame, err := browserprotocol.DecodeServerControl(cleaned)
	if err != nil {
		t.Fatalf("decode relayed frame %q: %v", cleaned, err)
	}
	return frame, ticket
}

func (controller *relayController) quiet(t *testing.T) {
	t.Helper()
	select {
	case record := <-controller.records:
		t.Fatalf("relayed controller %d received an unexpected record: type 0x%02x %q", controller.connection, byte(record.Type), record.Payload)
	default:
	}
}

// captureRelayTicketAsTheTransportDoes performs, in the test, exactly what the
// PWA's relay socket wrapper performs in production: it takes the transport
// level relay_ticket member out of a PAIR_RESULT or AUTH_RESULT body before
// anything decodes the frame as a session message. See
// web/packages/client/src/remote/relay-socket.ts, whose #captureTicket lifts
// the member and forwards the rest untouched. The member is addressed to the
// relay transport and is never seen by a protocol decoder on either side, so
// neither decoder tolerates it - which the test below proves both ways.
func captureRelayTicketAsTheTransportDoes(t *testing.T, payload []byte) ([]byte, string) {
	t.Helper()
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("relayed frame %q: %v", payload, err)
	}
	body, present := envelope["body"]
	if !present {
		return payload, ""
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(body, &members); err != nil {
		return payload, ""
	}
	raw, present := members["relay_ticket"]
	if !present {
		return payload, ""
	}
	var ticket string
	if err := json.Unmarshal(raw, &ticket); err != nil {
		t.Fatalf("relay_ticket %q is not a string", raw)
	}
	delete(members, "relay_ticket")
	encodedBody, err := json.Marshal(members)
	if err != nil {
		t.Fatal(err)
	}
	envelope["body"] = encodedBody
	cleaned, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return cleaned, ticket
}

// hello consumes the daemon's opening HELLO for a relayed session.
func (controller *relayController) hello(t *testing.T) browserprotocol.Hello {
	t.Helper()
	frame, ticket := controller.frame(t)
	hello, ok := frame.Body.(browserprotocol.Hello)
	if !ok || ticket != "" {
		t.Fatalf("first relayed frame = %+v (ticket %q), want HELLO", frame, ticket)
	}
	return hello
}

func (controller *relayController) proveePair(t *testing.T, fixture *adapterFixture, hello browserprotocol.Hello, challenge []byte) {
	t.Helper()
	publicKey := elliptic.Marshal(elliptic.P256(), controller.key.PublicKey.X, controller.key.PublicKey.Y)
	transcript, err := browserprotocol.BuildPairTranscript(browserprotocol.PairTranscript{
		DaemonID: adapterHex(t, hello.DaemonID, browserprotocol.DaemonIDSize), BootID: adapterHex(t, hello.BootID, browserprotocol.BootIDSize),
		ConnectionNonce: adapterHex(t, hello.ConnectionNonce, browserprotocol.NonceSize), Challenge: challenge, PublicKeySEC1: publicKey,
		ValidatedHost: fixture.server.Addr(), ValidatedOrigin: adapterOrigin,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := browserprotocol.EncodePairProve("pair", browserprotocol.PairProve{
		Challenge: hex.EncodeToString(challenge), PublicKeySEC1: hex.EncodeToString(publicKey), Signature: hex.EncodeToString(adapterSign(t, controller.key, transcript)),
	})
	if err != nil {
		t.Fatal(err)
	}
	controller.send(t, payload)
}

func (controller *relayController) proveAuth(t *testing.T, fixture *adapterFixture, hello browserprotocol.Hello) {
	t.Helper()
	transcript, err := browserprotocol.BuildAuthTranscript(browserprotocol.AuthTranscript{
		DaemonID: adapterHex(t, hello.DaemonID, browserprotocol.DaemonIDSize), BootID: adapterHex(t, hello.BootID, browserprotocol.BootIDSize),
		ConnectionNonce: adapterHex(t, hello.ConnectionNonce, browserprotocol.NonceSize), ClientID: controller.client.ID.Bytes(),
		ValidatedHost: fixture.server.Addr(), ValidatedOrigin: adapterOrigin,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := browserprotocol.EncodeAuthProve("auth", browserprotocol.AuthProve{ClientID: controller.client.ID.String(), Signature: hex.EncodeToString(adapterSign(t, controller.key, transcript))})
	if err != nil {
		t.Fatal(err)
	}
	controller.send(t, payload)
}

func (controller *relayController) snapshot(t *testing.T, id string) browserprotocol.StateSnapshot {
	t.Helper()
	payload, err := browserprotocol.EncodeStateGet(id, browserprotocol.StateGet{})
	if err != nil {
		t.Fatal(err)
	}
	controller.send(t, payload)
	frame, _ := controller.frame(t)
	if frame.Type != browserprotocol.TypeStateSnapshot || frame.ID != id {
		t.Fatalf("relayed snapshot = %+v, want STATE_SNAPSHOT for %q", frame, id)
	}
	return frame.Body.(browserprotocol.StateSnapshot)
}

func relayChallenge(t *testing.T, fixture *adapterFixture, seed byte, capabilities kernel.BrowserCapabilityMask) []byte {
	t.Helper()
	challenge := bytes.Repeat([]byte{seed}, browserprotocol.ChallengeSize)
	if _, err := fixture.store.CreateBrowserPairingChallenge(context.Background(), kernel.HashBrowserChallenge(challenge), fixture.backend.boot, adapterOrigin, capabilities, adapterTime(t, 1_000), adapterTime(t, 3_000)); err != nil {
		t.Fatal(err)
	}
	return challenge
}

// relaySeedProject commits one public entity so a snapshot has a durable head
// and real content to carry, rather than an empty projection that would agree
// with itself no matter what the relay did.
func relaySeedProject(t *testing.T, fixture *adapterFixture, seed byte, name string) kernel.Project {
	t.Helper()
	id, err := kernel.ProjectIDFromBytes(adapterID(t, seed))
	if err != nil {
		t.Fatal(err)
	}
	project, err := fixture.store.CreateProject(context.Background(), kernel.NewProject{ID: id, Name: name, Root: "/PRIVATE_ROOT_SENTINEL"}, adapterTime(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	return project
}

func relayKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// dialRelayFixture wires the fixture's own loopback browser listener to a
// fake relay and returns the identity the relay verifies against.
func dialRelayFixture(t *testing.T, fixture *adapterFixture) (*relayServer, *RelayRuntime, relayhost.Identity) {
	t.Helper()
	home := t.TempDir()
	identity, err := relayhost.LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	relay := newRelayServer(t, identity)
	runtime, err := fixture.daemon.DialRelay(context.Background(), relay.origin(), home, fixture.server.Addr())
	if err != nil {
		t.Fatalf("dial relay: %v", err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Errorf("close relay runtime: %v", err)
		}
	})
	return relay, runtime, identity
}

// resolveClient reads the durable client the relayed pairing minted and
// checks the injected ticket names exactly that client and its device key.
func (controller *relayController) resolveClient(t *testing.T, fixture *adapterFixture, identity relayhost.Identity, result browserprotocol.PairResult, ticket string) {
	t.Helper()
	raw := adapterHex(t, result.ClientID, browserprotocol.ClientIDSize)
	id, err := kernel.BrowserClientIDFromBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	client, found, err := fixture.store.BrowserClient(context.Background(), id)
	if err != nil || !found {
		t.Fatalf("durable client = %+v found=%v err=%v", client, found, err)
	}
	controller.client = client
	controller.ticket = ticket
	payload, err := relayhost.VerifyTicket(identity.PublicKey(), ticket)
	if err != nil {
		t.Fatalf("relay ticket does not verify against the node key: %v", err)
	}
	if payload.Purpose != relayhost.PurposeControl {
		t.Fatalf("relay ticket purpose = %q", payload.Purpose)
	}
	if named, err := base64.RawURLEncoding.DecodeString(payload.Controller); err != nil || !bytes.Equal(named, raw) {
		t.Fatalf("relay ticket names controller %q, want %x", payload.Controller, raw)
	}
	if device, err := base64.RawURLEncoding.DecodeString(payload.Device); err != nil || !bytes.Equal(device, client.PublicKey) {
		t.Fatalf("relay ticket names a device key that is not the durable one")
	}
}

// The relay ticket rides on the pairing and authentication results because a
// separate frame would need the same interception in the client wrapper plus a
// message type on both sides. What keeps that free of protocol debt is that no
// decoder ever tolerates the member: the transport consumes it first.
func TestTheRelayTicketIsConsumedByTheTransportAndRefusedByTheSessionDecoder(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve)
	relay, _, _ := dialRelayFixture(t, fixture)
	host := relay.accept(t)
	challenge := relayChallenge(t, fixture, 0x49, kernel.BrowserCapabilityObserve)
	controller := host.open(t, 131, relayKey(t))
	controller.proveePair(t, fixture, controller.hello(t), challenge)

	carried := controller.next(t).Payload
	stripped, ticket := captureRelayTicketAsTheTransportDoes(t, carried)
	if ticket == "" {
		t.Fatalf("the daemon sent no relay ticket on %q", carried)
	}
	if _, err := browserprotocol.DecodeServerControl(carried); !errors.Is(err, browserprotocol.ErrMalformed) {
		t.Fatalf("the session decoder accepted a frame still carrying relay_ticket: %v", err)
	}
	frame, err := browserprotocol.DecodeServerControl(stripped)
	if err != nil {
		t.Fatalf("the transport stripped frame does not decode: %v", err)
	}
	if frame.Type != browserprotocol.TypePairResult {
		t.Fatalf("stripped frame = %+v, want PAIR_RESULT", frame)
	}
}

func TestRelayCarriesTwoConcurrentControllersToTheirOwnDaemonSessions(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve)
	project := relaySeedProject(t, fixture, 1, "relayed project")
	relay, _, identity := dialRelayFixture(t, fixture)
	host := relay.accept(t)

	firstChallenge := relayChallenge(t, fixture, 0x44, kernel.BrowserCapabilityObserve)
	secondChallenge := relayChallenge(t, fixture, 0x45, kernel.BrowserCapabilityObserve)
	first := host.open(t, 101, relayKey(t))
	second := host.open(t, 102, relayKey(t))

	firstHello, secondHello := first.hello(t), second.hello(t)
	if firstHello.ConnectionNonce == secondHello.ConnectionNonce {
		t.Fatal("two relayed sessions shared one connection nonce")
	}

	// Both pairings are in flight at the daemon at the same time.
	first.proveePair(t, fixture, firstHello, firstChallenge)
	second.proveePair(t, fixture, secondHello, secondChallenge)

	firstFrame, firstTicket := first.frame(t)
	secondFrame, secondTicket := second.frame(t)
	if firstFrame.Type != browserprotocol.TypePairResult || secondFrame.Type != browserprotocol.TypePairResult {
		t.Fatalf("pair results = %+v, %+v", firstFrame, secondFrame)
	}
	first.resolveClient(t, fixture, identity, firstFrame.Body.(browserprotocol.PairResult), firstTicket)
	second.resolveClient(t, fixture, identity, secondFrame.Body.(browserprotocol.PairResult), secondTicket)
	if first.client.ID == second.client.ID {
		t.Fatal("two relayed pairings minted one client identity")
	}
	if firstTicket == secondTicket {
		t.Fatal("two relayed pairings received one relay ticket")
	}

	firstSnapshot := first.snapshot(t, "state-first")
	secondSnapshot := second.snapshot(t, "state-second")
	if firstSnapshot.Head == 0 || firstSnapshot.Head != secondSnapshot.Head {
		t.Fatalf("relayed snapshots = %d and %d", firstSnapshot.Head, secondSnapshot.Head)
	}
	for _, snapshot := range []browserprotocol.StateSnapshot{firstSnapshot, secondSnapshot} {
		if len(snapshot.Projects) != 1 || snapshot.Projects[0].ID != project.ID.String() || snapshot.Projects[0].Name != "relayed project" {
			t.Fatalf("relayed snapshot projects = %+v", snapshot.Projects)
		}
	}
	first.quiet(t)
	second.quiet(t)
}

func TestRelayRevocationEndsOnlyTheRevokedControllerSessions(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve)
	project := relaySeedProject(t, fixture, 2, "surviving project")
	relay, _, identity := dialRelayFixture(t, fixture)
	host := relay.accept(t)

	revokedChallenge := relayChallenge(t, fixture, 0x46, kernel.BrowserCapabilityObserve)
	survivorChallenge := relayChallenge(t, fixture, 0x47, kernel.BrowserCapabilityObserve)
	revoked := host.open(t, 111, relayKey(t))
	survivor := host.open(t, 112, relayKey(t))

	revokedHello, survivorHello := revoked.hello(t), survivor.hello(t)
	revoked.proveePair(t, fixture, revokedHello, revokedChallenge)
	survivor.proveePair(t, fixture, survivorHello, survivorChallenge)
	revokedFrame, revokedTicket := revoked.frame(t)
	survivorFrame, survivorTicket := survivor.frame(t)
	revoked.resolveClient(t, fixture, identity, revokedFrame.Body.(browserprotocol.PairResult), revokedTicket)
	survivor.resolveClient(t, fixture, identity, survivorFrame.Body.(browserprotocol.PairResult), survivorTicket)
	revoked.snapshot(t, "before-revocation")
	survivor.snapshot(t, "before-revocation")

	client, err := fixture.daemon.RevokeBrowserClient(context.Background(), revoked.client.ID, revoked.client.Revision)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if client.RevokedAt == nil {
		t.Fatal("revocation did not commit durably")
	}

	// The revoked controller's relayed session ends, and the relay is told to
	// refuse that controller.
	closed := revoked.next(t)
	if closed.Type != relayhost.RecordClose {
		t.Fatalf("revoked controller record = type 0x%02x %q, want CLOSE", byte(closed.Type), closed.Payload)
	}
	select {
	case record := <-host.revokes:
		var payload struct {
			Controller string `json:"controller"`
		}
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			t.Fatalf("revoke payload %q: %v", record.Payload, err)
		}
		named, err := base64.RawURLEncoding.DecodeString(payload.Controller)
		if err != nil || !bytes.Equal(named, revoked.client.ID.Bytes()) {
			t.Fatalf("REVOKE names %q, want %s", payload.Controller, revoked.client.ID)
		}
	case <-time.After(relayTestDeadline):
		t.Fatal("revocation emitted no REVOKE record")
	}

	// The other controller keeps its session and its authority.
	survivor.quiet(t)
	snapshot := survivor.snapshot(t, "after-revocation")
	if snapshot.Head == 0 || len(snapshot.Projects) != 1 || snapshot.Projects[0].ID != project.ID.String() {
		t.Fatalf("the surviving controller lost its snapshot authority: %+v", snapshot)
	}
}

func TestRelayDropForcesAFreshSessionWithNoReplayedFrames(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve)
	relaySeedProject(t, fixture, 3, "persistent project")
	relay, _, identity := dialRelayFixture(t, fixture)
	host := relay.accept(t)

	challenge := relayChallenge(t, fixture, 0x48, kernel.BrowserCapabilityObserve)
	controller := host.open(t, 121, relayKey(t))
	hello := controller.hello(t)
	controller.proveePair(t, fixture, hello, challenge)
	frame, ticket := controller.frame(t)
	controller.resolveClient(t, fixture, identity, frame.Body.(browserprotocol.PairResult), ticket)
	before := controller.snapshot(t, "before-drop")

	if err := host.conn.Close(websocket.StatusGoingAway, ""); err != nil {
		t.Logf("relay drop: %v", err)
	}
	<-host.closed

	next := relay.accept(t)
	if next.payload.Sequence != host.payload.Sequence+1 {
		t.Fatalf("reconnect sequence = %d, want %d", next.payload.Sequence, host.payload.Sequence+1)
	}

	// The reconnected controller is a brand-new daemon session: it must run
	// HELLO and AUTH again and receives no frame from before the drop.
	resumed := next.open(t, 121, controller.key)
	resumed.client = controller.client
	resumedHello := resumed.hello(t)
	if resumedHello.ConnectionNonce == hello.ConnectionNonce {
		t.Fatal("the reconnected session reused the previous connection nonce")
	}
	resumed.quiet(t)
	resumed.proveAuth(t, fixture, resumedHello)
	authFrame, authTicket := resumed.frame(t)
	if authFrame.Type != browserprotocol.TypeAuthResult {
		t.Fatalf("reconnected auth result = %+v", authFrame)
	}
	if authTicket == "" || authTicket == ticket {
		t.Fatal("the reconnected session did not receive its own relay ticket")
	}
	after := resumed.snapshot(t, "after-drop")
	if after.Head == 0 || after.Head != before.Head {
		t.Fatalf("fresh snapshot head = %d, want the unchanged durable head %d", after.Head, before.Head)
	}
	resumed.quiet(t)
}
