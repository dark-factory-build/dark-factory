package relayhost

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const testDeadline = 5 * time.Second

// fakeRelay is a minimal stand-in for the relay Worker's host side. It checks
// the subprotocol and the host token with crypto/ed25519 and speaks the exact
// record envelope, so this package is proven against the contract rather than
// against a mirror of its own encoder.
type fakeRelay struct {
	t      *testing.T
	server *httptest.Server
	key    ed25519.PublicKey
	nodeID string

	accepted chan *hostSocket

	mu           sync.Mutex
	presented    []HostTokenPayload
	presentedRaw []string
	lastAccepted HostTokenPayload
	forbid       bool
	selectToken  bool
	autoPong     bool
	paused       bool
	gate         chan struct{}
	sockets      []*hostSocket
}

type hostSocket struct {
	relay   *fakeRelay
	conn    *websocket.Conn
	payload HostTokenPayload
	records chan Record
	texts   chan string
	closed  chan struct{}
}

func newFakeRelay(t *testing.T, key ed25519.PublicKey) *fakeRelay {
	t.Helper()
	relay := &fakeRelay{t: t, key: key, nodeID: NodeIDFromPublicKey(key), accepted: make(chan *hostSocket, 16), autoPong: true, gate: make(chan struct{})}
	relay.server = httptest.NewServer(http.HandlerFunc(relay.handle))
	t.Cleanup(relay.server.Close)
	return relay
}

func (relay *fakeRelay) origin() string {
	return "ws://" + strings.TrimPrefix(relay.server.URL, "http://")
}

func (relay *fakeRelay) handle(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/host/"+relay.nodeID {
		http.Error(writer, "not found", http.StatusNotFound)
		return
	}
	if request.Header.Get("Origin") != "" {
		http.Error(writer, "forbidden", http.StatusForbidden)
		return
	}
	offered := strings.Split(request.Header.Get("Sec-WebSocket-Protocol"), ",")
	for index := range offered {
		offered[index] = strings.TrimSpace(offered[index])
	}
	if len(offered) != 2 || offered[0] != relaySubprotocol {
		http.Error(writer, "forbidden", http.StatusForbidden)
		return
	}
	payload, err := VerifyHostToken(relay.key, offered[1])
	relay.mu.Lock()
	relay.presented = append(relay.presented, payload)
	relay.presentedRaw = append(relay.presentedRaw, offered[1])
	forbid, selectToken := relay.forbid, relay.selectToken
	stale := payload.Generation < relay.lastAccepted.Generation ||
		payload.Generation == relay.lastAccepted.Generation && payload.Sequence <= relay.lastAccepted.Sequence
	if err == nil && !forbid && !stale {
		relay.lastAccepted = payload
	}
	relay.mu.Unlock()
	if err != nil || forbid || stale {
		http.Error(writer, "forbidden", http.StatusForbidden)
		return
	}
	selected := relaySubprotocol
	if selectToken {
		selected = offered[1]
	}
	connection, err := websocket.Accept(writer, request, &websocket.AcceptOptions{Subprotocols: []string{selected}, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	connection.SetReadLimit(MaxHostMessageBytes)
	socket := &hostSocket{relay: relay, conn: connection, payload: payload, records: make(chan Record, 1024), texts: make(chan string, 64), closed: make(chan struct{})}
	relay.mu.Lock()
	relay.sockets = append(relay.sockets, socket)
	relay.mu.Unlock()
	relay.accepted <- socket
	socket.read()
}

func (socket *hostSocket) read() {
	defer close(socket.closed)
	ctx := context.Background()
	for {
		socket.relay.mu.Lock()
		paused, gate := socket.relay.paused, socket.relay.gate
		socket.relay.mu.Unlock()
		if paused {
			<-gate
		}
		kind, message, err := socket.conn.Read(ctx)
		if err != nil {
			return
		}
		if kind == websocket.MessageText {
			select {
			case socket.texts <- string(message):
			default:
			}
			socket.relay.mu.Lock()
			auto := socket.relay.autoPong
			socket.relay.mu.Unlock()
			if auto && string(message) == "ping" {
				_ = socket.conn.Write(ctx, websocket.MessageText, []byte("pong"))
			}
			continue
		}
		records, err := DecodeRecords(message)
		if err != nil {
			return
		}
		for _, record := range records {
			copied := Record{Type: record.Type, Connection: record.Connection, Payload: append([]byte(nil), record.Payload...)}
			select {
			case socket.records <- copied:
			default:
			}
		}
	}
}

func (relay *fakeRelay) accept(t *testing.T) *hostSocket {
	t.Helper()
	select {
	case socket := <-relay.accepted:
		return socket
	case <-time.After(testDeadline):
		t.Fatal("relay never accepted a host connection")
		return nil
	}
}

func (relay *fakeRelay) pauseReads() {
	relay.mu.Lock()
	relay.paused = true
	relay.gate = make(chan struct{})
	relay.mu.Unlock()
}

func (relay *fakeRelay) resumeReads() {
	relay.mu.Lock()
	relay.paused = false
	gate := relay.gate
	relay.mu.Unlock()
	close(gate)
}

func (relay *fakeRelay) tokens() []string {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	return append([]string(nil), relay.presentedRaw...)
}

func (socket *hostSocket) send(t *testing.T, records ...Record) {
	t.Helper()
	var message []byte
	for _, record := range records {
		message = AppendRecord(message, record)
	}
	socket.sendRaw(t, message)
}

func (socket *hostSocket) sendRaw(t *testing.T, message []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testDeadline)
	defer cancel()
	if err := socket.conn.Write(ctx, websocket.MessageBinary, message); err != nil {
		t.Fatalf("relay write: %v", err)
	}
}

func (socket *hostSocket) open(t *testing.T, connection uint32, controller [ControllerIDSize]byte, origin string) {
	t.Helper()
	payload, err := json.Marshal(openPayload{Controller: base64.RawURLEncoding.EncodeToString(controller[:]), Purpose: PurposeControl, Origin: origin})
	if err != nil {
		t.Fatal(err)
	}
	socket.send(t, Record{Type: RecordOpen, Connection: connection, Payload: payload})
}

func (socket *hostSocket) closeConnection(t *testing.T, connection uint32, code int) {
	t.Helper()
	payload, err := json.Marshal(closePayload{Code: code})
	if err != nil {
		t.Fatal(err)
	}
	socket.send(t, Record{Type: RecordClose, Connection: connection, Payload: payload})
}

// expect waits for the next record matching kind and connection, discarding
// records for other connections so an unrelated session cannot mask a
// missing one.
func (socket *hostSocket) expect(t *testing.T, kind RecordType, connection uint32) Record {
	t.Helper()
	deadline := time.After(testDeadline)
	for {
		select {
		case record := <-socket.records:
			if record.Type == kind && record.Connection == connection {
				return record
			}
		case <-deadline:
			t.Fatalf("relay never received a 0x%02x record for connection %d", byte(kind), connection)
			return Record{}
		}
	}
}

func (socket *hostSocket) drop(t *testing.T) {
	t.Helper()
	if err := socket.conn.Close(websocket.StatusGoingAway, ""); err != nil {
		t.Logf("relay drop: %v", err)
	}
}
