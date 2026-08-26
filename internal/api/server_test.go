//go:build darwin || linux

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type serverReceiveResult struct {
	call Call
	err  error
}

func newAPITestListener(t testing.TB, bearer credential) (*Listener, string) {
	t.Helper()
	directory := privateTestDirectory(t)
	tokenPath := filepath.Join(directory, "operator.token")
	writeTestToken(t, tokenPath, bearer)
	socketPath := filepath.Join(directory, "api.sock")
	listener, err := Listen(socketPath, tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener, socketPath
}

func startServerReceive(listener *Listener, reply func(Call) Reply) <-chan serverReceiveResult {
	done := make(chan serverReceiveResult, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			done <- serverReceiveResult{err: err}
			return
		}
		defer connection.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		call, err := connection.Receive(ctx)
		if err == nil && reply != nil {
			err = connection.Respond(reply(call))
		}
		done <- serverReceiveResult{call: call, err: err}
	}()
	return done
}

func rawRequest(generation, domain byte, bearer credential, body []byte) []byte {
	payload := make([]byte, requestPrelude+len(body))
	payload[0], payload[1] = generation, domain
	copy(payload[2:requestPrelude], bearer[:])
	copy(payload[requestPrelude:], body)
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	return append(header[:], payload...)
}

func exchangeRaw(socketPath string, request []byte, closeWrite bool) ([]byte, error) {
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(time.Second))
	if _, err := connection.Write(request); err != nil {
		return nil, err
	}
	if closeWrite {
		if err := connection.CloseWrite(); err != nil {
			return nil, err
		}
	}
	response, err := readTestFrame(connection)
	if err != nil {
		return nil, err
	}
	if err := requireEOF(connection); err != nil {
		return nil, err
	}
	return response, nil
}

func decodeTestResponse(t testing.TB, payload []byte, domain byte, output any) error {
	t.Helper()
	if len(payload) < responsePrelude || payload[0] != protocolGeneration || payload[1] != domain {
		t.Fatalf("response prelude = %v", payload)
	}
	return decodeResponse(payload[responsePrelude:], output)
}

func replyForCall(call Call) Reply {
	switch call.Kind() {
	case CallHealth:
		return NewHealthReply(HealthStatus{Ready: true})
	case CallSnapshot:
		reply, _ := NewSnapshotReply(DashboardSnapshot{
			Head: 1, Factory: FactorySummary{Capacity: 1, Revision: 1},
			Projects: []ProjectSummary{}, Agents: []AgentSummary{}, Tasks: []TaskSummary{},
		})
		return reply
	default:
		reply, _ := NewMutationReply(MutationResult{Head: 0, Revision: 1})
		return reply
	}
}

func TestListenerCreatesExactOwnerSocketAndGuardsRemoval(t *testing.T) {
	bearer := testCredential('L')
	directory := privateTestDirectory(t)
	tokenPath := filepath.Join(directory, "operator.token")
	writeTestToken(t, tokenPath, bearer)
	socketPath := filepath.Join(directory, "api.sock")

	listener, err := Listen(socketPath, tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	identity, ok := identityOf(info)
	if !ok || !validSocketInfo(info) || identity.uid != uint32(os.Geteuid()) {
		t.Fatalf("listener socket metadata = %#v", info)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed listener socket remains: %v", err)
	}

	preexisting := []byte("preserve")
	if err := os.WriteFile(socketPath, preexisting, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := Listen(socketPath, tokenPath); got != nil || !errors.Is(err, ErrInvalidListener) {
		t.Fatalf("preexisting target listener = %+v, %v", got, err)
	}
	if contents, err := os.ReadFile(socketPath); err != nil || !bytes.Equal(contents, preexisting) {
		t.Fatalf("preexisting target changed: %q, %v", contents, err)
	}
	if err := os.Remove(socketPath); err != nil {
		t.Fatal(err)
	}

	listener, err = Listen(socketPath, tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(directory, "owned-moved.sock")
	if err := os.Rename(socketPath, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(socketPath, preexisting, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); !errors.Is(err, ErrInvalidListener) {
		t.Fatalf("replacement close = %v", err)
	}
	if contents, err := os.ReadFile(socketPath); err != nil || !bytes.Equal(contents, preexisting) {
		t.Fatalf("replacement target changed: %q, %v", contents, err)
	}
	if _, err := os.Lstat(moved); err != nil {
		t.Fatalf("owned moved socket was guessed away: %v", err)
	}
}

func TestServerDecodesClosedNineMethodMatrix(t *testing.T) {
	operatorBearer := testCredential('O')
	attemptBearer := testCredential('A')
	tests := []struct {
		name   string
		domain byte
		bearer credential
		body   string
		kind   CallKind
		check  func(*testing.T, Call)
	}{
		{name: "health", domain: operatorDomain, bearer: operatorBearer, body: `{"method":"health","params":{}}`, kind: CallHealth},
		{name: "snapshot", domain: operatorDomain, bearer: operatorBearer, body: `{"method":"snapshot","params":{}}`, kind: CallSnapshot},
		{name: "create project", domain: operatorDomain, bearer: operatorBearer, body: `{"method":"create_project","params":{"id":"` + id('1') + `","name":"project","root":"/private/sentinel-root"}}`, kind: CallCreateProject, check: func(t *testing.T, call Call) {
			input, ok := call.CreateProjectInput()
			if !ok || input.Root != "/private/sentinel-root" {
				t.Fatalf("project input = %+v, %t", input, ok)
			}
		}},
		{name: "create project paired surrogate", domain: operatorDomain, bearer: operatorBearer, body: `{"method":"create_project","params":{"id":"` + id('1') + `","name":"\ud83d\ude00","root":"/private/project"}}`, kind: CallCreateProject, check: func(t *testing.T, call Call) {
			input, ok := call.CreateProjectInput()
			if !ok || input.Name != "😀" {
				t.Fatalf("paired project input = %+v, %t", input, ok)
			}
		}},
		{name: "create project literal replacement", domain: operatorDomain, bearer: operatorBearer, body: `{"method":"create_project","params":{"id":"` + id('1') + `","name":"�","root":"/private/project"}}`, kind: CallCreateProject, check: func(t *testing.T, call Call) {
			input, ok := call.CreateProjectInput()
			if !ok || input.Name != "�" {
				t.Fatalf("literal replacement project input = %+v, %t", input, ok)
			}
		}},
		{name: "create shell agent", domain: operatorDomain, bearer: operatorBearer, body: `{"method":"create_shell_agent","params":{"id":"` + id('2') + `","project_id":"` + id('1') + `","name":"agent","role":"worker","tool_budget_limit":20}}`, kind: CallCreateShellAgent, check: func(t *testing.T, call Call) {
			input, ok := call.CreateShellAgentInput()
			if !ok || input.ToolBudgetLimit != 20 {
				t.Fatalf("agent input = %+v, %t", input, ok)
			}
		}},
		{name: "enqueue task", domain: operatorDomain, bearer: operatorBearer, body: `{"method":"enqueue_task","params":{"id":"` + id('3') + `","project_id":"` + id('1') + `","assigned_agent_id":"` + id('2') + `","incarnation_id":"` + id('4') + `","title":"task","body":"private-body-sentinel","priority":7}}`, kind: CallEnqueueTask, check: func(t *testing.T, call Call) {
			input, ok := call.EnqueueTaskInput()
			if !ok || input.Body != "private-body-sentinel" {
				t.Fatalf("task input = %+v, %t", input, ok)
			}
		}},
		{name: "set dispatch", domain: operatorDomain, bearer: operatorBearer, body: `{"method":"set_dispatch","params":{"expected_revision":3,"enabled":true}}`, kind: CallSetDispatch, check: func(t *testing.T, call Call) {
			revision, enabled, ok := call.Dispatch()
			if !ok || revision != 3 || !enabled {
				t.Fatalf("dispatch = %d, %t, %t", revision, enabled, ok)
			}
		}},
		{name: "succeed", domain: attemptDomain, bearer: attemptBearer, body: `{"method":"succeed","params":{"result":"private-result-sentinel"}}`, kind: CallSucceed, check: func(t *testing.T, call Call) {
			result, ok := call.Result()
			if !ok || result != "private-result-sentinel" {
				t.Fatalf("result = %q, %t", result, ok)
			}
		}},
		{name: "block", domain: attemptDomain, bearer: attemptBearer, body: `{"method":"block","params":{"detail":"blocked"}}`, kind: CallBlock},
		{name: "fail empty detail", domain: attemptDomain, bearer: attemptBearer, body: `{"method":"fail","params":{"detail":""}}`, kind: CallFail, check: func(t *testing.T, call Call) {
			detail, ok := call.Detail()
			if !ok || detail != "" {
				t.Fatalf("failure detail = %q, %t", detail, ok)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			listener, socketPath := newAPITestListener(t, operatorBearer)
			done := startServerReceive(listener, replyForCall)
			response, err := exchangeRaw(socketPath, rawRequest(protocolGeneration, test.domain, test.bearer, []byte(test.body)), true)
			if err != nil {
				t.Fatal(err)
			}
			var output any
			switch test.kind {
			case CallHealth:
				output = &HealthStatus{}
			case CallSnapshot:
				output = &DashboardSnapshot{}
			default:
				output = &MutationResult{}
			}
			if err := decodeTestResponse(t, response, test.domain, output); err != nil {
				t.Fatal(err)
			}
			result := <-done
			if result.err != nil || result.call.Kind() != test.kind {
				t.Fatalf("receive = %v, %v", result.call.Kind(), result.err)
			}
			if test.check != nil {
				test.check(t, result.call)
			}
			if test.domain == attemptDomain {
				digest, ok := result.call.AttemptDigest()
				want := sha256.Sum256(attemptBearer[:])
				if !ok || digest.Bytes() != want {
					t.Fatalf("attempt digest = %x, %t", digest.Bytes(), ok)
				}
			} else if _, ok := result.call.AttemptDigest(); ok {
				t.Fatal("operator call exposed attempt digest")
			}
			formatted := fmt.Sprintf("%v %+v %#v", result.call, result.call, result.call)
			encoded, err := json.Marshal(result.call)
			if err != nil {
				t.Fatal(err)
			}
			for _, private := range []string{string(operatorBearer[:]), string(attemptBearer[:]), "private-body-sentinel", "private-result-sentinel", "/private/sentinel-root"} {
				if strings.Contains(formatted+string(encoded), private) {
					t.Fatalf("public call formatting exposed private value: %s %s", formatted, encoded)
				}
			}
		})
	}
}

func TestServerRejectsDomainFallbackAndInvalidRequests(t *testing.T) {
	operatorBearer := testCredential('O')
	attemptBearer := testCredential('A')
	tests := []struct {
		name       string
		generation byte
		domain     byte
		bearer     credential
		body       []byte
		code       RemoteErrorCode
	}{
		{name: "wrong generation", generation: 2, domain: operatorDomain, bearer: operatorBearer, body: []byte(`{"method":"health","params":{}}`), code: RemoteUnsupportedProtocol},
		{name: "operator bearer does not authorize attempt domain", generation: 1, domain: attemptDomain, bearer: operatorBearer, body: []byte(`{"method":"health","params":{}}`), code: RemoteForbidden},
		{name: "attempt bearer does not authorize operator", generation: 1, domain: operatorDomain, bearer: attemptBearer, body: []byte(`{"method":"health","params":{}}`), code: RemoteUnauthorized},
		{name: "operator domain cannot invoke attempt", generation: 1, domain: operatorDomain, bearer: operatorBearer, body: []byte(`{"method":"fail","params":{"detail":"x"}}`), code: RemoteForbidden},
		{name: "unknown method", generation: 1, domain: operatorDomain, bearer: operatorBearer, body: []byte(`{"method":"delete_all","params":{}}`), code: RemoteInvalidRequest},
		{name: "null params", generation: 1, domain: operatorDomain, bearer: operatorBearer, body: []byte(`{"method":"health","params":null}`), code: RemoteInvalidRequest},
		{name: "array params", generation: 1, domain: operatorDomain, bearer: operatorBearer, body: []byte(`{"method":"health","params":[]}`), code: RemoteInvalidRequest},
		{name: "scalar params", generation: 1, domain: operatorDomain, bearer: operatorBearer, body: []byte(`{"method":"health","params":1}`), code: RemoteInvalidRequest},
		{name: "duplicate method", generation: 1, domain: operatorDomain, bearer: operatorBearer, body: []byte(`{"method":"health","method":"snapshot","params":{}}`), code: RemoteInvalidRequest},
		{name: "casefold method", generation: 1, domain: operatorDomain, bearer: operatorBearer, body: []byte(`{"method":"health","Method":"snapshot","params":{}}`), code: RemoteInvalidRequest},
		{name: "nested duplicate", generation: 1, domain: operatorDomain, bearer: operatorBearer, body: []byte(`{"method":"set_dispatch","params":{"expected_revision":1,"expected_revision":2,"enabled":true}}`), code: RemoteInvalidRequest},
		{name: "nested noncanonical", generation: 1, domain: operatorDomain, bearer: operatorBearer, body: []byte(`{"method":"set_dispatch","params":{"Expected_revision":1,"enabled":true}}`), code: RemoteInvalidRequest},
		{name: "unknown nested", generation: 1, domain: operatorDomain, bearer: operatorBearer, body: []byte(`{"method":"set_dispatch","params":{"expected_revision":1,"enabled":true,"private":"sentinel"}}`), code: RemoteInvalidRequest},
		{name: "attempt cannot supply id", generation: 1, domain: attemptDomain, bearer: attemptBearer, body: []byte(`{"method":"fail","params":{"detail":"x","id":"` + id('1') + `"}}`), code: RemoteInvalidRequest},
		{name: "attempt cannot supply failure code", generation: 1, domain: attemptDomain, bearer: attemptBearer, body: []byte(`{"method":"fail","params":{"detail":"x","code":"internal"}}`), code: RemoteInvalidRequest},
		{name: "empty block detail", generation: 1, domain: attemptDomain, bearer: attemptBearer, body: []byte(`{"method":"block","params":{"detail":""}}`), code: RemoteInvalidRequest},
		{name: "oversized failure detail", generation: 1, domain: attemptDomain, bearer: attemptBearer, body: []byte(`{"method":"fail","params":{"detail":"` + strings.Repeat("x", 4097) + `"}}`), code: RemoteInvalidRequest},
		{name: "invalid UTF-8", generation: 1, domain: operatorDomain, bearer: operatorBearer, body: []byte("{\"method\":\"health\",\"params\":{},\"x\":\"\xff\"}"), code: RemoteInvalidRequest},
		{name: "lone high surrogate", generation: 1, domain: operatorDomain, bearer: operatorBearer, body: []byte(`{"method":"create_project","params":{"id":"` + id('1') + `","name":"\ud800","root":"/private/project"}}`), code: RemoteInvalidRequest},
		{name: "lone low surrogate", generation: 1, domain: operatorDomain, bearer: operatorBearer, body: []byte(`{"method":"create_project","params":{"id":"` + id('1') + `","name":"\udc00","root":"/private/project"}}`), code: RemoteInvalidRequest},
		{name: "reversed surrogate pair", generation: 1, domain: operatorDomain, bearer: operatorBearer, body: []byte(`{"method":"create_project","params":{"id":"` + id('1') + `","name":"\udc00\ud800","root":"/private/project"}}`), code: RemoteInvalidRequest},
		{name: "mismatched surrogate pair", generation: 1, domain: operatorDomain, bearer: operatorBearer, body: []byte(`{"method":"create_project","params":{"id":"` + id('1') + `","name":"\ud800\u0041","root":"/private/project"}}`), code: RemoteInvalidRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			listener, socketPath := newAPITestListener(t, operatorBearer)
			done := startServerReceive(listener, nil)
			response, err := exchangeRaw(socketPath, rawRequest(test.generation, test.domain, test.bearer, test.body), true)
			if err != nil {
				t.Fatal(err)
			}
			var output HealthStatus
			responseDomain := test.domain
			if err := decodeTestResponse(t, response, responseDomain, &output); err == nil {
				t.Fatal("invalid request succeeded")
			} else {
				var remote *RemoteError
				if !errors.As(err, &remote) || remote.Code() != test.code {
					t.Fatalf("remote error = %v, want %s", err, test.code)
				}
			}
			if result := <-done; !errors.Is(result.err, ErrProtocol) || result.call.Kind() != 0 {
				t.Fatalf("invalid request dispatched: %+v, %v", result.call, result.err)
			}
		})
	}
}

func TestServerRequiresRequestEOFBeforedispatch(t *testing.T) {
	bearer := testCredential('E')
	listener, socketPath := newAPITestListener(t, bearer)
	done := startServerReceive(listener, replyForCall)
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	request := rawRequest(protocolGeneration, operatorDomain, bearer, []byte(`{"method":"health","params":{}}`))
	if _, err := connection.Write(request); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		t.Fatalf("request dispatched before EOF: %+v", result)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := connection.Write(request); err != nil {
		t.Fatal(err)
	}
	if err := connection.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if _, err := readTestFrame(connection); err != nil {
		t.Fatal(err)
	}
	if result := <-done; !errors.Is(result.err, ErrProtocol) || result.call.Kind() != 0 {
		t.Fatalf("second frame dispatched: %+v, %v", result.call, result.err)
	}
}

func TestServerFramingDeadlinePeerAndResponseBounds(t *testing.T) {
	bearer := testCredential('F')
	t.Run("frame bounds and truncation", func(t *testing.T) {
		requests := [][]byte{
			{0, 0, 0, 0},
			{0, 0x10, 0, 1},
			{0, 0},
			{0, 0, 0, 10, 1, operatorDomain},
		}
		for index, request := range requests {
			t.Run(fmt.Sprint(index), func(t *testing.T) {
				listener, socketPath := newAPITestListener(t, bearer)
				done := startServerReceive(listener, nil)
				connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socketPath, Net: "unix"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := connection.Write(request); err != nil {
					t.Fatal(err)
				}
				if err := connection.CloseWrite(); err != nil {
					t.Fatal(err)
				}
				_ = connection.Close()
				if result := <-done; result.err == nil || result.call.Kind() != 0 {
					t.Fatalf("bad frame dispatched: %+v, %v", result.call, result.err)
				}
			})
		}
	})

	t.Run("blocked read deadline", func(t *testing.T) {
		listener, socketPath := newAPITestListener(t, bearer)
		done := make(chan error, 1)
		go func() {
			connection, err := listener.Accept()
			if err != nil {
				done <- err
				return
			}
			defer connection.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			_, err = connection.Receive(ctx)
			done <- err
		}()
		connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socketPath, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Close()
		started := time.Now()
		if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocked read = %v", err)
		}
		if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
			t.Fatalf("blocked read took %v", elapsed)
		}
	})

	t.Run("complete frame without EOF never dispatches", func(t *testing.T) {
		listener, socketPath := newAPITestListener(t, bearer)
		done := make(chan serverReceiveResult, 1)
		go func() {
			connection, err := listener.Accept()
			if err != nil {
				done <- serverReceiveResult{err: err}
				return
			}
			defer connection.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			call, err := connection.Receive(ctx)
			done <- serverReceiveResult{call: call, err: err}
		}()
		connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socketPath, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Close()
		request := rawRequest(1, operatorDomain, bearer, []byte(`{"method":"health","params":{}}`))
		if _, err := connection.Write(request); err != nil {
			t.Fatal(err)
		}
		result := <-done
		if !errors.Is(result.err, context.DeadlineExceeded) || result.call.Kind() != 0 {
			t.Fatalf("missing EOF dispatched: %+v, %v", result.call, result.err)
		}
	})

	t.Run("peer prerequisite seam", func(t *testing.T) {
		listener, socketPath := newAPITestListener(t, bearer)
		dialed := make(chan *net.UnixConn, 1)
		go func() {
			connection, _ := net.DialUnix("unix", nil, &net.UnixAddr{Name: socketPath, Net: "unix"})
			dialed <- connection
		}()
		connection, err := listener.accept(func(net.Conn) error { return ErrInvalidListener })
		if connection != nil || !errors.Is(err, ErrInvalidListener) {
			t.Fatalf("foreign peer = %+v, %v", connection, err)
		}
		if client := <-dialed; client != nil {
			_ = client.Close()
		}
	})

	t.Run("oversized snapshot becomes fixed too large", func(t *testing.T) {
		listener, socketPath := newAPITestListener(t, bearer)
		tasks := make([]TaskSummary, maxSnapshotEntries)
		for index := range tasks {
			tasks[index] = TaskSummary{ID: id('1'), ProjectID: id('2'), AssignedAgentID: id('3'), Title: strings.Repeat("x", 1024), Status: "queued", Revision: 1}
		}
		snapshot := DashboardSnapshot{Head: 1, Factory: FactorySummary{Capacity: 1, Revision: 1}, Projects: []ProjectSummary{}, Agents: []AgentSummary{}, Tasks: tasks}
		reply, err := NewSnapshotReply(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		done := startServerReceive(listener, func(Call) Reply { return reply })
		response, err := exchangeRaw(socketPath, rawRequest(1, operatorDomain, bearer, []byte(`{"method":"snapshot","params":{}}`)), true)
		if err != nil {
			t.Fatal(err)
		}
		var output DashboardSnapshot
		err = decodeTestResponse(t, response, operatorDomain, &output)
		var remote *RemoteError
		if !errors.As(err, &remote) || remote.Code() != RemoteTooLarge {
			t.Fatalf("oversized response = %v", err)
		}
		if result := <-done; result.err != nil {
			t.Fatal(result.err)
		}
	})
}

func TestServerReceiveCancellationCutsJoinWatcher(t *testing.T) {
	bearer := testCredential('K')
	var partialPayloadHeader [4]byte
	binary.BigEndian.PutUint32(partialPayloadHeader[:], 128)
	cuts := []struct {
		name  string
		write []byte
	}{
		{name: "partial header", write: []byte{0, 0}},
		{name: "partial payload", write: append(partialPayloadHeader[:], 1, operatorDomain, 'x')},
		{name: "missing request EOF", write: rawRequest(1, operatorDomain, bearer, []byte(`{"method":"health","params":{}}`))},
	}
	for _, cut := range cuts {
		t.Run(cut.name, func(t *testing.T) {
			baselineGoroutines := runtime.NumGoroutine()
			baselineFDs := countTestFDs(t)
			for range 20 {
				listener, socketPath := newAPITestListener(t, bearer)
				ctx, cancel := context.WithCancel(context.Background())
				accepted := make(chan struct{})
				done := make(chan serverReceiveResult, 1)
				go func() {
					connection, err := listener.Accept()
					if err != nil {
						done <- serverReceiveResult{err: err}
						return
					}
					close(accepted)
					call, receiveErr := connection.Receive(ctx)
					firstClose := connection.Close()
					secondClose := connection.Close()
					if firstClose != nil {
						receiveErr = firstClose
					}
					if secondClose != nil {
						receiveErr = secondClose
					}
					done <- serverReceiveResult{call: call, err: receiveErr}
				}()
				client, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socketPath, Net: "unix"})
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				<-accepted
				if _, err := client.Write(cut.write); err != nil {
					cancel()
					client.Close()
					t.Fatal(err)
				}
				time.Sleep(5 * time.Millisecond)
				started := time.Now()
				cancel()
				select {
				case result := <-done:
					if !errors.Is(result.err, context.Canceled) || result.call.Kind() != 0 {
						t.Fatalf("cancelled receive dispatched: %+v, %v", result.call, result.err)
					}
				case <-time.After(500 * time.Millisecond):
					t.Fatal("cancelled receive retained its connection")
				}
				if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
					t.Fatalf("cancelled receive took %v", elapsed)
				}
				if err := client.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
					t.Fatal(err)
				}
				if count, err := client.Read(make([]byte, 1)); count != 0 || err == nil {
					t.Fatalf("server connection remained open: count=%d error=%v", count, err)
				}
				if err := client.Close(); err != nil {
					t.Fatal(err)
				}
				if err := listener.Close(); err != nil {
					t.Fatal(err)
				}
			}
			deadline := time.Now().Add(500 * time.Millisecond)
			for runtime.NumGoroutine() != baselineGoroutines && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			if after := runtime.NumGoroutine(); after != baselineGoroutines {
				t.Fatalf("cancelled receives changed goroutine census: before=%d after=%d", baselineGoroutines, after)
			}
			if after := countTestFDs(t); after != baselineFDs {
				t.Fatalf("cancelled receives changed FD census: before=%d after=%d", baselineFDs, after)
			}
		})
	}
}

func TestServerRechecksOperatorTokenAndKeepsPublicValuesPrivate(t *testing.T) {
	bearer := testCredential('S')
	listener, socketPath := newAPITestListener(t, bearer)
	replacement := filepath.Join(filepath.Dir(listener.tokenPath), "replacement.token")
	writeTestToken(t, replacement, bearer)
	if err := os.Rename(replacement, listener.tokenPath); err != nil {
		t.Fatal(err)
	}
	done := startServerReceive(listener, nil)
	response, err := exchangeRaw(socketPath, rawRequest(1, operatorDomain, bearer, []byte(`{"method":"health","params":{}}`)), true)
	if err != nil {
		t.Fatal(err)
	}
	var status HealthStatus
	err = decodeTestResponse(t, response, operatorDomain, &status)
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Code() != RemoteUnauthorized {
		t.Fatalf("replaced token = %v", err)
	}
	if result := <-done; !errors.Is(result.err, ErrProtocol) {
		t.Fatalf("replaced token dispatched: %+v, %v", result.call, result.err)
	}
	values := []any{listener, *listener, listener.token.bearer, AttemptDigest{value: sha256.Sum256(bearer[:])}, Call{text: "private-result-sentinel"}, Reply{code: RemoteInternal}}
	for _, value := range values {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		formatted := fmt.Sprintf("%v %+v %#v %s", value, value, value, encoded)
		for _, private := range []string{string(bearer[:]), socketPath, listener.tokenPath, "private-result-sentinel"} {
			if strings.Contains(formatted, private) {
				t.Fatalf("public formatting exposed private value: %s", formatted)
			}
		}
	}
}

func TestServerConnectionsLeaveExactResourceCensus(t *testing.T) {
	bearer := testCredential('C')
	baselineGoroutines := runtime.NumGoroutine()
	baselineFDs := countTestFDs(t)
	for range 20 {
		listener, socketPath := newAPITestListener(t, bearer)
		done := startServerReceive(listener, replyForCall)
		response, err := exchangeRaw(socketPath, rawRequest(1, operatorDomain, bearer, []byte(`{"method":"health","params":{}}`)), true)
		if err != nil {
			t.Fatal(err)
		}
		var status HealthStatus
		if err := decodeTestResponse(t, response, operatorDomain, &status); err != nil {
			t.Fatal(err)
		}
		if result := <-done; result.err != nil {
			t.Fatal(result.err)
		}
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("socket retained: %v", err)
		}
	}
	time.Sleep(20 * time.Millisecond)
	if delta := runtime.NumGoroutine() - baselineGoroutines; delta > 1 {
		t.Fatalf("server calls retained %d goroutines", delta)
	}
	if after := countTestFDs(t); after != baselineFDs {
		t.Fatalf("server calls changed FD census: before=%d after=%d", baselineFDs, after)
	}
}

func TestReplyConstructorsAndConnectionOrderAreClosed(t *testing.T) {
	if _, err := NewMutationReply(MutationResult{Head: 0, Revision: 1}); err != nil {
		t.Fatalf("zero canonical head rejected: %v", err)
	}
	if _, err := NewMutationReply(MutationResult{Head: 1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("zero revision = %v", err)
	}
	if _, err := NewErrorReply("private-sentinel"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("arbitrary error = %v", err)
	}
	connection := &Connection{}
	if _, err := connection.Receive(context.Background()); !errors.Is(err, ErrProtocol) {
		t.Fatalf("nil receive = %v", err)
	}
	if err := connection.Respond(NewHealthReply(HealthStatus{Ready: true})); !errors.Is(err, ErrProtocol) {
		t.Fatalf("response before receive = %v", err)
	}
}

func TestRawRequestHelperProducesOneExactFrame(t *testing.T) {
	bearer := testCredential('H')
	request := rawRequest(1, operatorDomain, bearer, []byte(`{"method":"health","params":{}}`))
	reader := bytes.NewReader(request)
	payload, err := readTestFrame(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload[2:requestPrelude], bearer[:]) {
		t.Fatal("test request bearer differs")
	}
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("test request has trailing bytes: %v", err)
	}
}
