package browser

import (
	"context"
	"sync"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
)

type taskDispatchBackend struct {
	*fakeBackend
	mu     sync.Mutex
	client [browserprotocol.ClientIDSize]byte
	err    error

	push browserprotocol.PushSubscribe
}

func newTaskDispatchBackend() *taskDispatchBackend {
	base := newFakeBackend()
	base.authentication.Capabilities |= browserprotocol.CapabilityHumanActions
	return &taskDispatchBackend{fakeBackend: base}
}

func (backend *taskDispatchBackend) TelemetryIngest(context.Context, [browserprotocol.ClientIDSize]byte, browserprotocol.TelemetryIngest) (browserprotocol.TelemetryIngestResult, error) {
	return browserprotocol.TelemetryIngestResult{}, ErrUnauthorized
}

func (backend *taskDispatchBackend) SubscribePush(_ context.Context, client [browserprotocol.ClientIDSize]byte, subscription browserprotocol.PushSubscribe) error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.client = client
	backend.push = subscription
	return backend.err
}

func startTaskServer(t *testing.T, backend Backend) *Server {
	t.Helper()
	server, err := Listen(Config{Address: "127.0.0.1:0", AllowedOrigins: []string{testOrigin, devOrigin}, Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return server
}

func TestPushSubscribeDispatchesAndCorrelatesItsResult(t *testing.T) {
	subscription := browserprotocol.PushSubscribe{Endpoint: "https://web.push.apple.com/QGdfl/abc", PublicKey: "BGsX0fLhLEJH-Lzm5WOkQPJ3A32BLeszoPShOUXYmMKWT-NC4v4af5uO5-tKfA-eFivOM1drMV7Oy7ZAaDe_UfU", PrivateKey: "MIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQgAQ"}
	payload, err := testEncodePushSubscribe("push", subscription)
	if err != nil {
		t.Fatal(err)
	}
	backend := newTaskDispatchBackend()
	server := startTaskServer(t, backend)
	connection, _ := dialServer(t, server, testOrigin)
	authenticate(t, connection)
	writeClientFrame(t, connection, payload)
	frame := readServerFrame(t, connection)
	if frame.Type != browserprotocol.TypePushSubscribeResult || frame.ID != "push" {
		t.Fatalf("push result = %+v", frame)
	}
	backend.mu.Lock()
	got := backend.push
	backend.mu.Unlock()
	if got != subscription {
		t.Fatalf("stored subscription = %+v", got)
	}
}
