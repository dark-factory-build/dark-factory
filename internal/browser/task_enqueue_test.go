package browser

import (
	"context"
	"encoding/hex"
	"sync"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
)

type taskDispatchBackend struct {
	*fakeBackend
	mu      sync.Mutex
	request browserprotocol.TaskEnqueue
	client  [browserprotocol.ClientIDSize]byte
	result  browserprotocol.TaskEnqueueResult
	err     error
	calls   int
}

func newTaskDispatchBackend() *taskDispatchBackend {
	base := newFakeBackend()
	base.authentication.Capabilities |= browserprotocol.CapabilityHumanActions
	return &taskDispatchBackend{fakeBackend: base}
}

func (backend *taskDispatchBackend) EnqueueTask(_ context.Context, client [browserprotocol.ClientIDSize]byte, request browserprotocol.TaskEnqueue) (browserprotocol.TaskEnqueueResult, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.calls++
	backend.client = client
	backend.request = request
	return backend.result, backend.err
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

func TestTaskEnqueueDispatchesAndCorrelatesExactResult(t *testing.T) {
	request := browserprotocol.TaskEnqueue{
		TaskID:                "404142434445464748494a4b4c4d4e4f",
		IncarnationID:         "505152535455565758595a5b5c5d5e5f",
		AgentID:               "606162636465666768696a6b6c6d6e6f",
		ExpectedAgentRevision: 7,
		Instruction:           "inspect the next failure",
	}
	backend := newTaskDispatchBackend()
	backend.result = browserprotocol.TaskEnqueueResult{TaskID: request.TaskID, Revision: 1, AgentRevision: request.ExpectedAgentRevision}
	server := startTaskServer(t, backend)
	connection, _ := dialServer(t, server, testOrigin)
	authenticate(t, connection)
	payload, err := browserprotocol.EncodeTaskEnqueue("enqueue", request)
	if err != nil {
		t.Fatal(err)
	}
	writeClientFrame(t, connection, payload)
	frame := readServerFrame(t, connection)
	if frame.Type != browserprotocol.TypeTaskEnqueueResult || frame.ID != "enqueue" || frame.Body.(browserprotocol.TaskEnqueueResult) != backend.result {
		t.Fatalf("enqueue result = %+v", frame)
	}
	backend.mu.Lock()
	calls, gotRequest, gotClient := backend.calls, backend.request, backend.client
	backend.mu.Unlock()
	wantClient, err := hex.DecodeString(testID)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || gotRequest != request || string(gotClient[:]) != string(wantClient) {
		t.Fatalf("dispatch calls=%d request=%+v client=%x", calls, gotRequest, gotClient)
	}
}

func TestTaskEnqueueFailsClosedWithoutBackendAndOnMismatchedResult(t *testing.T) {
	request := browserprotocol.TaskEnqueue{
		TaskID:                "404142434445464748494a4b4c4d4e4f",
		IncarnationID:         "505152535455565758595a5b5c5d5e5f",
		AgentID:               "606162636465666768696a6b6c6d6e6f",
		ExpectedAgentRevision: 7,
		Instruction:           "inspect the next failure",
	}
	payload, err := browserprotocol.EncodeTaskEnqueue("enqueue", request)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("missing optional backend", func(t *testing.T) {
		server := startTaskServer(t, newFakeBackend())
		connection, _ := dialServer(t, server, testOrigin)
		authenticate(t, connection)
		writeClientFrame(t, connection, payload)
		assertError(t, readServerFrame(t, connection), browserprotocol.ErrorUnauthorized)
	})
	t.Run("mismatched backend result", func(t *testing.T) {
		backend := newTaskDispatchBackend()
		backend.result = browserprotocol.TaskEnqueueResult{TaskID: request.TaskID, Revision: 1, AgentRevision: request.ExpectedAgentRevision + 1}
		server := startTaskServer(t, backend)
		connection, _ := dialServer(t, server, testOrigin)
		authenticate(t, connection)
		writeClientFrame(t, connection, payload)
		assertError(t, readServerFrame(t, connection), browserprotocol.ErrorInternal)
	})
	t.Run("backend stale", func(t *testing.T) {
		backend := newTaskDispatchBackend()
		backend.err = ErrStale
		server := startTaskServer(t, backend)
		connection, _ := dialServer(t, server, testOrigin)
		authenticate(t, connection)
		writeClientFrame(t, connection, payload)
		assertError(t, readServerFrame(t, connection), browserprotocol.ErrorStale)
	})
}
