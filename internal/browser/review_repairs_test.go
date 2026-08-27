package browser

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
)

func authProof(t *testing.T, connection *websocket.Conn) {
	t.Helper()
	proof, err := browserprotocol.EncodeAuthProve("auth", browserprotocol.AuthProve{
		ClientID: testID, Signature: strings.Repeat("01", browserprotocol.SignatureSize),
	})
	if err != nil {
		t.Fatal(err)
	}
	writeClientFrame(t, connection, proof)
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become true")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRevocationClosesInflightAuthenticationBeforeReturn(t *testing.T) {
	backend := newFakeBackend()
	backend.authWait = true
	server := startServer(t, backend)
	connection, _ := dialServer(t, server, testOrigin)
	_ = readServerFrame(t, connection)
	authProof(t, connection)
	waitFor(t, func() bool {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		return backend.authCalls == 1
	})
	clientID := backend.authentication.Principal.ClientID
	done := make(chan error, 1)
	go func() { done <- server.CloseClient(clientID) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("revocation did not cancel and join in-flight authentication")
	}
	server.mu.Lock()
	lifecycle := server.clientLifecycle[clientID]
	registered, inflight, revoking := 0, 0, 0
	if lifecycle != nil {
		registered = len(lifecycle.connections)
		inflight = len(lifecycle.authenticating)
		revoking = lifecycle.revoking
	}
	server.mu.Unlock()
	if registered != 0 || inflight != 0 || revoking != 0 {
		t.Fatalf("revocation did not quiesce registered=%d inflight=%d revoking=%d", registered, inflight, revoking)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, err := connection.Read(ctx); err == nil {
		t.Fatal("revoked in-flight authentication received a result")
	}
}

func TestAuthResultMustMatchRequestedClient(t *testing.T) {
	backend := newFakeBackend()
	backend.authentication.Principal.ClientID[0] ^= 0xff
	server := startServer(t, backend)
	connection, _ := dialServer(t, server, testOrigin)
	_ = readServerFrame(t, connection)
	authProof(t, connection)
	assertError(t, readServerFrame(t, connection), browserprotocol.ErrorUnauthorized)
	server.mu.Lock()
	lifecycle := server.clientLifecycle[backend.authentication.Principal.ClientID]
	registered := 0
	if lifecycle != nil {
		registered = len(lifecycle.connections)
	}
	server.mu.Unlock()
	if registered != 0 {
		t.Fatalf("mismatched backend principal registered %d connections", registered)
	}
}

func TestPairResultCannotRegisterAfterExactClientRevocation(t *testing.T) {
	backend := newFakeBackend()
	backend.pairStarted = make(chan struct{})
	release := make(chan struct{})
	backend.pairRelease = release
	server := startServer(t, backend)
	connection, _ := dialServer(t, server, testOrigin)
	_ = readServerFrame(t, connection)
	publicKey := append([]byte{4}, make([]byte, browserprotocol.PublicKeySize-1)...)
	pair, err := browserprotocol.EncodePairProve("pair", browserprotocol.PairProve{
		Challenge:     strings.Repeat("04", browserprotocol.ChallengeSize),
		PublicKeySEC1: hex.EncodeToString(publicKey),
		Signature:     strings.Repeat("05", browserprotocol.SignatureSize),
	})
	if err != nil {
		t.Fatal(err)
	}
	writeClientFrame(t, connection, pair)
	select {
	case <-backend.pairStarted:
	case <-time.After(time.Second):
		t.Fatal("pair backend did not start")
	}
	if err := server.CloseClient(backend.authentication.Principal.ClientID); err != nil {
		t.Fatal(err)
	}
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, payload, err := connection.Read(ctx); err == nil {
		t.Fatalf("revoked pairing received a result: %s", payload)
	}
}

func TestRevocationFenceIsTemporaryAndBounded(t *testing.T) {
	backend := newFakeBackend()
	server := startServer(t, backend)
	for value := 1; value <= 4096; value++ {
		var clientID [browserprotocol.ClientIDSize]byte
		clientID[0] = byte(value)
		clientID[1] = byte(value >> 8)
		if err := server.CloseClient(clientID); err != nil {
			t.Fatal(err)
		}
	}
	server.mu.Lock()
	lifecycles, pairingBlocked := len(server.clientLifecycle), server.pairingBlocked
	server.mu.Unlock()
	if lifecycles != 0 || pairingBlocked != 0 {
		t.Fatalf("quiescent revocation state lifecycles=%d pairing_blocked=%d", lifecycles, pairingBlocked)
	}

	// Transport fencing is not durable authorization. Once CloseClient has
	// quiesced, a later proof reaches Backend, which must enforce revocation.
	connection, _ := dialServer(t, server, testOrigin)
	authenticate(t, connection)
	backend.mu.Lock()
	authCalls := backend.authCalls
	backend.mu.Unlock()
	if authCalls != 1 {
		t.Fatalf("future authentication did not reach durable authority: calls=%d", authCalls)
	}
}

func TestOperationAuthorizationIsReloadedByBackend(t *testing.T) {
	backend := newFakeBackend()
	server := startServer(t, backend)
	connection, _ := dialServer(t, server, testOrigin)
	authenticate(t, connection)
	backend.mu.Lock()
	backend.detailErr = ErrUnauthorized
	backend.mu.Unlock()
	request, _ := browserprotocol.EncodeHumanRequestDetailGet("detail-revoked", browserprotocol.HumanRequestDetailGet{RequestID: requestID, ExpectedRevision: 1})
	writeClientFrame(t, connection, request)
	assertError(t, readServerFrame(t, connection), browserprotocol.ErrorUnauthorized)
	backend.mu.Lock()
	calls := backend.detailCalls
	backend.mu.Unlock()
	if calls != 1 {
		t.Fatalf("backend authorization calls=%d want 1", calls)
	}
}

func TestSubscriptionChronologyRestartsInsteadOfPublishingGap(t *testing.T) {
	tests := []struct {
		name   string
		update StateUpdate
		reason browserprotocol.RestartReason
	}{
		{"gap", StateUpdate{Event: eventPointer(browserprotocol.HiddenAdvanceEvent(browserprotocol.HiddenAdvance{Sequence: 9, Head: 9}))}, browserprotocol.RestartGap},
		{"pruned", StateUpdate{Event: eventPointer(browserprotocol.HiddenAdvanceEvent(browserprotocol.HiddenAdvance{Sequence: 8, Head: 8})), Floor: 8}, browserprotocol.RestartPruned},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := newFakeBackend()
			server := startServer(t, backend)
			connection, _ := dialServer(t, server, testOrigin)
			authenticate(t, connection)
			subscribe, _ := browserprotocol.EncodeStateSubscribe("chronology", browserprotocol.StateSubscribe{After: 7})
			writeClientFrame(t, connection, subscribe)
			waitFor(t, func() bool {
				backend.mu.Lock()
				defer backend.mu.Unlock()
				return backend.subCalls == 1
			})
			backend.sub.updates <- test.update
			frame := readServerFrame(t, connection)
			if frame.Type != browserprotocol.TypeStateRestart || frame.ID != "chronology" {
				t.Fatalf("gap published instead of restart: %+v", frame)
			}
			if got := frame.Body.(browserprotocol.StateRestart).Reason; got != test.reason {
				t.Fatalf("reason=%s want %s", got, test.reason)
			}
			if backend.sub.closed.Load() != 1 {
				t.Fatal("chronology restart did not join subscription")
			}
		})
	}
}

func eventPointer(value browserprotocol.StateEvent) *browserprotocol.StateEvent { return &value }

func TestHiddenChronologyAdvancesAndExplicitDependencyRestartCloses(t *testing.T) {
	backend := newFakeBackend()
	server := startServer(t, backend)
	connection, _ := dialServer(t, server, testOrigin)
	authenticate(t, connection)
	subscribe, _ := browserprotocol.EncodeStateSubscribe("hidden", browserprotocol.StateSubscribe{After: 7})
	writeClientFrame(t, connection, subscribe)
	waitFor(t, func() bool {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		return backend.subCalls == 1
	})
	event := browserprotocol.HiddenAdvanceEvent(browserprotocol.HiddenAdvance{Sequence: 8, Head: 8})
	backend.sub.updates <- StateUpdate{Event: &event}
	if frame := readServerFrame(t, connection); frame.Type != browserprotocol.TypeStateEvent || frame.ID != "hidden" {
		t.Fatalf("hidden advance=%+v", frame)
	}
	restart := browserprotocol.StateRestart{Head: 9, Floor: 0, Reason: browserprotocol.RestartHiddenDependency}
	backend.sub.updates <- StateUpdate{Restart: &restart}
	frame := readServerFrame(t, connection)
	if frame.Type != browserprotocol.TypeStateRestart || frame.Body.(browserprotocol.StateRestart).Reason != browserprotocol.RestartHiddenDependency {
		t.Fatalf("hidden dependency restart=%+v", frame)
	}
}

func TestBackendResponseCorrelationFailsClosed(t *testing.T) {
	t.Run("entity id", func(t *testing.T) {
		backend := newFakeBackend()
		backend.entity.ID = requestID
		backend.entity.Item = browserprotocol.ProjectStateItem(browserprotocol.ProjectItem{ID: requestID, Name: "Wrong", Revision: 1})
		server := startServer(t, backend)
		connection, _ := dialServer(t, server, testOrigin)
		authenticate(t, connection)
		request, _ := browserprotocol.EncodeStateEntityGet("entity-mismatch", browserprotocol.StateEntityGet{Kind: browserprotocol.StateProject, ID: projectID})
		writeClientFrame(t, connection, request)
		assertError(t, readServerFrame(t, connection), browserprotocol.ErrorInternal)
	})
	t.Run("detail id and revision", func(t *testing.T) {
		for _, mutate := range []func(*browserprotocol.HumanRequestDetail){
			func(value *browserprotocol.HumanRequestDetail) { value.RequestID = projectID },
			func(value *browserprotocol.HumanRequestDetail) { value.Revision = 2 },
		} {
			backend := newFakeBackend()
			mutate(&backend.detail)
			server := startServer(t, backend)
			connection, _ := dialServer(t, server, testOrigin)
			authenticate(t, connection)
			request, _ := browserprotocol.EncodeHumanRequestDetailGet("detail-mismatch", browserprotocol.HumanRequestDetailGet{RequestID: requestID, ExpectedRevision: 1})
			writeClientFrame(t, connection, request)
			assertError(t, readServerFrame(t, connection), browserprotocol.ErrorInternal)
		}
	})
	t.Run("page kind", func(t *testing.T) {
		backend := newFakeBackend()
		next := Cursor{Head: 7, Kind: browserprotocol.StateAgent}
		backend.page = StatePage{Head: 7, Kind: browserprotocol.StateProject, Items: browserprotocol.ProjectItems(nil), NextCursor: &next}
		server := startServer(t, backend)
		connection, _ := dialServer(t, server, testOrigin)
		authenticate(t, connection)
		request, _ := browserprotocol.EncodeStateGet("page-kind", browserprotocol.StateGet{})
		writeClientFrame(t, connection, request)
		assertError(t, readServerFrame(t, connection), browserprotocol.ErrorInternal)
	})
	t.Run("page continuation head", func(t *testing.T) {
		backend := newFakeBackend()
		backend.page.NextCursor.Head = 8
		server := startServer(t, backend)
		connection, _ := dialServer(t, server, testOrigin)
		authenticate(t, connection)
		request, _ := browserprotocol.EncodeStateGet("page-head", browserprotocol.StateGet{})
		writeClientFrame(t, connection, request)
		assertError(t, readServerFrame(t, connection), browserprotocol.ErrorInternal)
	})
	t.Run("page pinned cursor head", func(t *testing.T) {
		backend := newFakeBackend()
		server := startServer(t, backend)
		connection, _ := dialServer(t, server, testOrigin)
		authenticate(t, connection)
		first, _ := browserprotocol.EncodeStateGet("page-first", browserprotocol.StateGet{})
		writeClientFrame(t, connection, first)
		snapshot := readServerFrame(t, connection).Body.(browserprotocol.StateSnapshot)
		backend.mu.Lock()
		next := Cursor{Head: 8, Kind: browserprotocol.StateAgent}
		backend.page = StatePage{Head: 8, Kind: browserprotocol.StateProject, Items: browserprotocol.ProjectItems(nil), NextCursor: &next}
		backend.mu.Unlock()
		request, _ := browserprotocol.EncodeStateGet("page-pinned", browserprotocol.StateGet{Cursor: snapshot.NextCursor})
		writeClientFrame(t, connection, request)
		assertError(t, readServerFrame(t, connection), browserprotocol.ErrorInternal)
	})
}

func TestBackendSuccessAfterOperationDeadlineIsDiscarded(t *testing.T) {
	backend := newFakeBackend()
	backend.stateWait = true
	server := startServer(t, backend)
	connection, _ := dialServer(t, server, testOrigin)
	authenticate(t, connection)
	request, _ := browserprotocol.EncodeStateGet("late-state", browserprotocol.StateGet{})
	writeClientFrame(t, connection, request)
	frame := readServerFrame(t, connection)
	assertError(t, frame, browserprotocol.ErrorInternal)
}

func TestLateSubscriptionIsCancelledAndNeverInstalled(t *testing.T) {
	backend := newFakeBackend()
	backend.subWait = true
	server := startServer(t, backend)
	connection, _ := dialServer(t, server, testOrigin)
	authenticate(t, connection)
	request, _ := browserprotocol.EncodeStateSubscribe("late-sub", browserprotocol.StateSubscribe{})
	writeClientFrame(t, connection, request)
	assertError(t, readServerFrame(t, connection), browserprotocol.ErrorInternal)
	if backend.sub.closed.Load() != 1 {
		t.Fatalf("late subscription cleanup count=%d", backend.sub.closed.Load())
	}
}

func TestRejectedSubscriptionsAreCancelledAndJoined(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*fakeBackend)
		code  browserprotocol.ErrorCode
	}{
		{
			name: "backend result plus error",
			setup: func(backend *fakeBackend) {
				backend.subErr = ErrRateLimited
			},
			code: browserprotocol.ErrorRateLimited,
		},
		{
			name: "nil updates",
			setup: func(backend *fakeBackend) {
				backend.sub.updates = nil
			},
			code: browserprotocol.ErrorInternal,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := newFakeBackend()
			test.setup(backend)
			server := startServer(t, backend)
			connection, _ := dialServer(t, server, testOrigin)
			authenticate(t, connection)
			request, _ := browserprotocol.EncodeStateSubscribe("rejected", browserprotocol.StateSubscribe{})
			writeClientFrame(t, connection, request)
			assertError(t, readServerFrame(t, connection), test.code)
			if got := backend.sub.closed.Load(); got != 1 {
				t.Fatalf("rejected subscription cleanup count=%d", got)
			}
		})
	}
}

func TestRejectedSubscriptionCleanupUncertaintyIsObservable(t *testing.T) {
	backend := newFakeBackend()
	backend.sub.updates = nil
	backend.sub.neverDone = true
	server, err := Listen(Config{Address: "127.0.0.1:0", AllowedOrigins: []string{testOrigin}, Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	connection, _ := dialServer(t, server, testOrigin)
	authenticate(t, connection)
	request, _ := browserprotocol.EncodeStateSubscribe("unresolved-rejected", browserprotocol.StateSubscribe{})
	writeClientFrame(t, connection, request)
	assertError(t, readServerFrame(t, connection), browserprotocol.ErrorInternal)
	_ = connection.CloseNow()
	waitFor(t, func() bool {
		server.mu.Lock()
		defer server.mu.Unlock()
		return len(server.connections) == 0
	})
	if err := server.Close(); !errors.Is(err, ErrSubscriptionUnresolved) {
		t.Fatalf("close error=%v", err)
	}
}

func TestUnresolvedSubscriptionPoisonsEveryBoundedConnection(t *testing.T) {
	backend := newFakeBackend()
	backend.subFactory = func() StateSubscription {
		subscription := newFakeSubscription()
		subscription.updates = nil
		subscription.neverDone = true
		return subscription
	}
	server, err := Listen(Config{Address: "127.0.0.1:0", AllowedOrigins: []string{testOrigin}, Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	connections := make([]*websocket.Conn, 0, maxConnections)
	for range maxConnections {
		connection, _ := dialServer(t, server, testOrigin)
		authenticate(t, connection)
		connections = append(connections, connection)
	}
	first, _ := browserprotocol.EncodeStateSubscribe("poison", browserprotocol.StateSubscribe{})
	second, _ := browserprotocol.EncodeStateSubscribe("must-not-run", browserprotocol.StateSubscribe{})
	for _, connection := range connections {
		writeClientFrame(t, connection, first)
		writeClientFrame(t, connection, second)
	}
	for _, connection := range connections {
		assertError(t, readServerFrame(t, connection), browserprotocol.ErrorInternal)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, payload, readErr := connection.Read(ctx)
		cancel()
		if readErr == nil {
			t.Fatalf("poisoned connection remained usable: %s", payload)
		}
	}
	backend.mu.Lock()
	subCalls := backend.subCalls
	backend.mu.Unlock()
	if subCalls != maxConnections {
		t.Fatalf("SubscribeState calls=%d want exactly %d", subCalls, maxConnections)
	}
	if err := server.Close(); !errors.Is(err, ErrSubscriptionUnresolved) {
		t.Fatalf("close error=%v", err)
	}
}

type nilDoneSubscription struct {
	cancelled atomic.Int32
}

func (subscription *nilDoneSubscription) Updates() <-chan StateUpdate { return nil }
func (subscription *nilDoneSubscription) Cancel()                     { subscription.cancelled.Add(1) }
func (subscription *nilDoneSubscription) Done() <-chan struct{}       { return nil }
func (subscription *nilDoneSubscription) Err() error                  { return nil }

func TestStopSubscriptionCancelsBeforeNilDoneFailure(t *testing.T) {
	subscription := &nilDoneSubscription{}
	if err := stopSubscription(subscription); !errors.Is(err, ErrSubscriptionUnresolved) {
		t.Fatalf("cleanup error=%v", err)
	}
	if got := subscription.cancelled.Load(); got != 1 {
		t.Fatalf("Cancel calls=%d want 1", got)
	}
}

func TestSubscriptionHeadInitializationCannotUseZeroSentinel(t *testing.T) {
	current := &connection{subscriptionHead: browserprotocol.Decimal(browserprotocol.MaxSQLiteInteger)}
	if current.stateHeadRegressed(0) {
		t.Fatal("uninitialized stale numeric head constrained first event")
	}
	current.subscriptionHead = 0
	current.subscriptionHeadSet = true
	if current.stateHeadRegressed(0) {
		t.Fatal("initialized zero head was treated as absent")
	}
	current.subscriptionHead = 1
	if !current.stateHeadRegressed(0) {
		t.Fatal("explicitly initialized monotonic head check was bypassed")
	}

	current = &connection{
		subscriptionSequence: 0,
		subscriptionHead:     0,
		subscriptionHeadSet:  true,
	}
	impossible := browserprotocol.HiddenAdvanceEvent(browserprotocol.HiddenAdvance{Sequence: 1, Head: 0})
	if err := current.sendUpdate(StateUpdate{Event: &impossible}); err == nil {
		t.Fatal("impossible first chronology was accepted")
	}
	if !current.subscriptionHeadSet || current.subscriptionHead != 0 {
		t.Fatal("zero head lost explicit initialization state")
	}
}

func TestUncooperativeSubscriptionCleanupIsBoundedAndObservable(t *testing.T) {
	backend := newFakeBackend()
	backend.sub.neverDone = true
	server, err := Listen(Config{Address: "127.0.0.1:0", AllowedOrigins: []string{testOrigin}, Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	connection, _ := dialServer(t, server, testOrigin)
	authenticate(t, connection)
	subscribe, _ := browserprotocol.EncodeStateSubscribe("unresolved", browserprotocol.StateSubscribe{})
	writeClientFrame(t, connection, subscribe)
	waitFor(t, func() bool {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		return backend.subCalls == 1
	})
	started := time.Now()
	err = server.Close()
	if !errors.Is(err, ErrSubscriptionUnresolved) {
		t.Fatalf("close error=%v", err)
	}
	if elapsed := time.Since(started); elapsed > subscriptionCloseLimit+time.Second {
		t.Fatalf("uncooperative cleanup took %v", elapsed)
	}
}

type failingListener struct {
	err error
}

func (listener failingListener) Accept() (net.Conn, error) { return nil, listener.err }
func (failingListener) Close() error                       { return nil }
func (failingListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 43123}
}

func TestUnexpectedServeFailureIsObservable(t *testing.T) {
	sentinel := errors.New("serve-SENTINEL")
	server := start(newFakeBackend(), map[string]struct{}{testOrigin: {}}, failingListener{err: sentinel})
	select {
	case <-server.ServeDone():
	case <-time.After(time.Second):
		t.Fatal("Serve failure was not observable")
	}
	if !errors.Is(server.Err(), sentinel) {
		t.Fatalf("server Err=%v", server.Err())
	}
	if err := server.Close(); !errors.Is(err, sentinel) {
		t.Fatalf("Close error=%v", err)
	}
}
