package relayhost

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const (
	loopbackPath   = "/browser/v2"
	loopbackOrigin = "https://app.darkfactory.build"
)

// fakeLoopback stands in for the daemon's own browser listener. It validates
// the exact Host and Origin the real listener validates, so a session opened
// through the relay is proven to arrive as an ordinary local client.
type fakeLoopback struct {
	server *httptest.Server
	origin string

	opened chan *loopbackSession

	mu       sync.Mutex
	rejected int
	pairBody []byte
	authBody []byte
}

type loopbackSession struct {
	conn         *websocket.Conn
	frames       chan loopbackFrame
	closed       chan struct{}
	closeStatus  websocket.StatusCode
	observedHost string
}

type loopbackFrame struct {
	kind    websocket.MessageType
	payload []byte
}

func newFakeLoopback(t *testing.T) *fakeLoopback {
	t.Helper()
	loopback := &fakeLoopback{origin: loopbackOrigin, opened: make(chan *loopbackSession, 16)}
	loopback.server = httptest.NewServer(http.HandlerFunc(loopback.handle))
	t.Cleanup(loopback.server.Close)
	return loopback
}

func (loopback *fakeLoopback) url() string {
	return "ws://" + strings.TrimPrefix(loopback.server.URL, "http://") + loopbackPath
}

func (loopback *fakeLoopback) address() string {
	return strings.TrimPrefix(loopback.server.URL, "http://")
}

func (loopback *fakeLoopback) handle(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != loopbackPath || request.Host != loopback.address() || request.Header.Get("Origin") != loopback.origin {
		loopback.mu.Lock()
		loopback.rejected++
		loopback.mu.Unlock()
		http.Error(writer, "forbidden", http.StatusForbidden)
		return
	}
	connection, err := websocket.Accept(writer, request, &websocket.AcceptOptions{OriginPatterns: []string{loopback.origin}, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	connection.SetReadLimit(MaxRecordPayloadBytes)
	session := &loopbackSession{conn: connection, frames: make(chan loopbackFrame, 1024), closed: make(chan struct{}), observedHost: request.Host}
	loopback.opened <- session
	ctx := context.Background()
	_ = connection.Write(ctx, websocket.MessageText, []byte(`{"v":1,"type":"HELLO","body":{"daemon_id":"aa","boot_id":"bb","connection_nonce":"cc"}}`))
	defer close(session.closed)
	for {
		kind, payload, err := connection.Read(ctx)
		if err != nil {
			session.closeStatus = websocket.CloseStatus(err)
			return
		}
		select {
		case session.frames <- loopbackFrame{kind: kind, payload: append([]byte(nil), payload...)}:
		default:
		}
		if kind == websocket.MessageText && bytes.Contains(payload, []byte("PAIR_PROVE")) {
			loopback.mu.Lock()
			body := loopback.pairBody
			loopback.mu.Unlock()
			if body != nil {
				_ = connection.Write(ctx, websocket.MessageText, body)
			}
			continue
		}
		if kind == websocket.MessageText && bytes.Contains(payload, []byte("AUTH_PROVE")) {
			loopback.mu.Lock()
			body := loopback.authBody
			loopback.mu.Unlock()
			if body != nil {
				_ = connection.Write(ctx, websocket.MessageText, body)
			}
			continue
		}
	}
}

func (loopback *fakeLoopback) accept(t *testing.T) *loopbackSession {
	t.Helper()
	select {
	case session := <-loopback.opened:
		return session
	case <-time.After(testDeadline):
		t.Fatal("loopback never accepted a session")
		return nil
	}
}

func (loopback *fakeLoopback) refusals() int {
	loopback.mu.Lock()
	defer loopback.mu.Unlock()
	return loopback.rejected
}

func (session *loopbackSession) expect(t *testing.T) loopbackFrame {
	t.Helper()
	select {
	case frame := <-session.frames:
		return frame
	case <-time.After(testDeadline):
		t.Fatal("loopback session never received a frame")
		return loopbackFrame{}
	}
}

func (session *loopbackSession) write(t *testing.T, kind websocket.MessageType, payload []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testDeadline)
	defer cancel()
	if err := session.conn.Write(ctx, kind, payload); err != nil {
		t.Fatalf("loopback write: %v", err)
	}
}

func (session *loopbackSession) waitClosed(t *testing.T) websocket.StatusCode {
	t.Helper()
	select {
	case <-session.closed:
		return session.closeStatus
	case <-time.After(testDeadline):
		t.Fatal("loopback session never closed")
		return 0
	}
}

func (session *loopbackSession) stillOpen(t *testing.T) bool {
	t.Helper()
	select {
	case <-session.closed:
		return false
	default:
		return true
	}
}
