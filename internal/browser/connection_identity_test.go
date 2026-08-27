package browser

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
)

func TestTransportMintsPrivateIdentityPerConnection(t *testing.T) {
	backend := newFakeBackend()
	server := startServer(t, backend)

	pairSocket, _ := dialServer(t, server, testOrigin)
	_ = readServerFrame(t, pairSocket)
	pairProof, err := browserprotocol.EncodePairProve("pair", browserprotocol.PairProve{
		Challenge:     strings.Repeat("04", browserprotocol.ChallengeSize),
		PublicKeySEC1: "04" + strings.Repeat("00", browserprotocol.PublicKeySize-1),
		Signature:     strings.Repeat("05", browserprotocol.SignatureSize),
	})
	if err != nil {
		t.Fatal(err)
	}
	writeClientFrame(t, pairSocket, pairProof)
	pairPayload, pairResult := readServerPayload(t, pairSocket)
	if pairResult.Type != browserprotocol.TypePairResult {
		t.Fatalf("pair result = %+v", pairResult)
	}
	pairConnection := soleClientConnection(t, server, backend.authentication.Principal.ClientID)
	pairID := pairConnection.principal.ConnectionID
	if pairID.zero() {
		t.Fatal("pair socket has zero connection identity")
	}

	authSocket, _ := dialServer(t, server, testOrigin)
	_ = readServerFrame(t, authSocket)
	authProof(t, authSocket)
	authPayload, authResult := readServerPayload(t, authSocket)
	if authResult.Type != browserprotocol.TypeAuthResult {
		t.Fatalf("auth result = %+v", authResult)
	}
	authConnection := otherClientConnection(t, server, backend.authentication.Principal.ClientID, pairConnection)
	authID := authConnection.principal.ConnectionID
	if authID.zero() {
		t.Fatal("auth socket has zero connection identity")
	}
	if authID == pairID {
		t.Fatal("pair and auth sockets shared a connection identity")
	}
	assertPrivateConnectionID(t, pairID, pairPayload)
	assertPrivateConnectionID(t, authID, authPayload)

	state, err := browserprotocol.EncodeStateGet("state", browserprotocol.StateGet{})
	if err != nil {
		t.Fatal(err)
	}
	writeClientFrame(t, authSocket, state)
	statePayload, stateResult := readServerPayload(t, authSocket)
	if stateResult.Type != browserprotocol.TypeStateSnapshot {
		t.Fatalf("state result = %+v", stateResult)
	}
	assertPrivateConnectionID(t, authID, statePayload)
	assertPrivateConnectionID(t, pairID, statePayload)

	if err := authSocket.CloseNow(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		server.mu.Lock()
		defer server.mu.Unlock()
		return !authConnection.authenticated && authConnection.principal == (Principal{})
	})
	reconnected, _ := dialServer(t, server, testOrigin)
	authenticate(t, reconnected)
	reconnectedOwner := otherClientConnection(t, server, backend.authentication.Principal.ClientID, pairConnection)
	if reconnectedOwner.principal.ConnectionID == authID {
		t.Fatal("reconnect reused the dead WebSocket generation")
	}
}

func TestBackendCannotChooseConnectionIdentity(t *testing.T) {
	for _, pair := range []bool{false, true} {
		name := "auth"
		if pair {
			name = "pair"
		}
		t.Run(name, func(t *testing.T) {
			backend := newFakeBackend()
			backend.authentication.Principal.ConnectionID.value[0] = 0xa5
			server := startServer(t, backend)
			socket, _ := dialServer(t, server, testOrigin)
			_ = readServerFrame(t, socket)
			if pair {
				proof, err := browserprotocol.EncodePairProve("pair", browserprotocol.PairProve{
					Challenge:     strings.Repeat("04", browserprotocol.ChallengeSize),
					PublicKeySEC1: "04" + strings.Repeat("00", browserprotocol.PublicKeySize-1),
					Signature:     strings.Repeat("05", browserprotocol.SignatureSize),
				})
				if err != nil {
					t.Fatal(err)
				}
				writeClientFrame(t, socket, proof)
			} else {
				authProof(t, socket)
			}
			frame := readServerFrame(t, socket)
			assertError(t, frame, browserprotocol.ErrorUnauthorized)
			if frame.Type == browserprotocol.TypePairResult || frame.Type == browserprotocol.TypeAuthResult {
				t.Fatalf("backend-selected connection identity produced success: %+v", frame)
			}
			waitFor(t, func() bool {
				server.mu.Lock()
				defer server.mu.Unlock()
				lifecycle := server.clientLifecycle[backend.authentication.Principal.ClientID]
				return lifecycle == nil || len(lifecycle.connections) == 0
			})
		})
	}
}

func TestCloseClientClearsEveryPrivatePrincipal(t *testing.T) {
	backend := newFakeBackend()
	server := startServer(t, backend)
	for range 2 {
		socket, _ := dialServer(t, server, testOrigin)
		authenticate(t, socket)
	}
	connections := clientConnections(t, server, backend.authentication.Principal.ClientID)
	if len(connections) != 2 {
		t.Fatalf("connections = %d, want 2", len(connections))
	}
	if connections[0].principal.ConnectionID == connections[1].principal.ConnectionID {
		t.Fatal("same-client browser tabs shared a connection identity")
	}
	if err := server.CloseClient(backend.authentication.Principal.ClientID); err != nil {
		t.Fatal(err)
	}
	for _, current := range connections {
		if current.authenticated || current.principal != (Principal{}) {
			t.Fatalf("closed connection retained principal: authenticated=%v principal=%+v", current.authenticated, current.principal)
		}
	}
}

func readServerPayload(t *testing.T, connection *websocket.Conn) ([]byte, browserprotocol.ControlFrame) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), backendCallLimit+2*time.Second)
	defer cancel()
	kind, payload, err := connection.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if kind != websocket.MessageText {
		t.Fatalf("message type = %v", kind)
	}
	frame, err := browserprotocol.DecodeServerControl(payload)
	if err != nil {
		t.Fatal(err)
	}
	return payload, frame
}

func assertPrivateConnectionID(t *testing.T, id ConnectionID, payload []byte) {
	t.Helper()
	if id.zero() || len(id.value) != browserprotocol.ClientIDSize {
		t.Fatalf("invalid private connection identity")
	}
	encoded := []byte(hex.EncodeToString(id.value[:]))
	if bytes.Contains(payload, id.value[:]) || bytes.Contains(payload, encoded) {
		t.Fatal("private connection identity reached a public frame")
	}
	if diagnostic := fmt.Sprintf("%v %+v %#v", id, id, id); bytes.Contains([]byte(diagnostic), id.value[:]) || bytes.Contains([]byte(diagnostic), encoded) {
		t.Fatal("private connection identity reached diagnostics")
	}
}

func clientConnections(t *testing.T, server *Server, clientID [browserprotocol.ClientIDSize]byte) []*connection {
	t.Helper()
	server.mu.Lock()
	defer server.mu.Unlock()
	lifecycle := server.clientLifecycle[clientID]
	if lifecycle == nil {
		return nil
	}
	result := make([]*connection, 0, len(lifecycle.connections))
	for current := range lifecycle.connections {
		result = append(result, current)
	}
	return result
}

func soleClientConnection(t *testing.T, server *Server, clientID [browserprotocol.ClientIDSize]byte) *connection {
	t.Helper()
	connections := clientConnections(t, server, clientID)
	if len(connections) != 1 {
		t.Fatalf("connections = %d, want 1", len(connections))
	}
	return connections[0]
}

func otherClientConnection(t *testing.T, server *Server, clientID [browserprotocol.ClientIDSize]byte, excluded *connection) *connection {
	t.Helper()
	connections := clientConnections(t, server, clientID)
	for _, current := range connections {
		if current != excluded {
			return current
		}
	}
	t.Fatalf("no second connection among %d", len(connections))
	return nil
}
