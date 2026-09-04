//go:build darwin || linux

package daemon

import (
	"context"
	"encoding/hex"
	"errors"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestBrowserRevocationWaitsForPriorAuthenticationAuthorityLaneAndJoinsSocket(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve)
	connection := fixture.pair(t)
	clientID, release, authorized, err := fixture.backend.authorize(context.Background(), rawBrowserClient(fixture.client.ID), kernel.BrowserCapabilityObserve)
	if err != nil || clientID != fixture.client.ID || authorized.ID != fixture.client.ID {
		t.Fatalf("prior authentication authority = %+v, %v", authorized, err)
	}
	done := make(chan struct {
		client kernel.BrowserClient
		err    error
	}, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		client, revokeErr := fixture.daemon.RevokeBrowserClient(context.Background(), fixture.client.ID, fixture.client.Revision)
		done <- struct {
			client kernel.BrowserClient
			err    error
		}{client, revokeErr}
	}()
	<-started
	adapterWaitGateUsers(t, fixture.daemon.browserClientGates, fixture.client.ID, 2)
	select {
	case result := <-done:
		t.Fatalf("revocation crossed prior authority gate: %+v, %v", result.client, result.err)
	default:
	}
	release()
	result := <-done
	if result.err != nil || result.client.RevokedAt == nil {
		t.Fatalf("revocation = %+v, %v", result.client, result.err)
	}
	adapterAssertSocketClosed(t, connection)

	stored, found, err := fixture.store.BrowserClient(context.Background(), fixture.client.ID)
	if err != nil || !found || stored.RevokedAt == nil || stored.Revision != result.client.Revision {
		t.Fatalf("durable revocation = %+v, found=%v, err=%v", stored, found, err)
	}
}

func TestBrowserAuthenticationQueuedAfterRevocationReloadsDurableAuthority(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve)
	paired := fixture.pair(t)
	release, err := fixture.daemon.browserClientGates.acquire(context.Background(), fixture.client.ID)
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := fixture.daemon.revokeBrowserClientHeld(context.Background(), fixture.client.ID, fixture.client.Revision)
	if err != nil || revoked.RevokedAt == nil {
		t.Fatalf("held revocation = %+v, %v", revoked, err)
	}
	adapterAssertSocketClosed(t, paired)

	connection := adapterDial(t, fixture.server)
	hello := adapterRead(t, connection).Body.(browserprotocol.Hello)
	adapterWrite(t, connection, adapterAuthProof(t, fixture, hello, "after-revoke"))
	adapterWaitGateUsers(t, fixture.daemon.browserClientGates, fixture.client.ID, 2)
	release()
	frame := adapterRead(t, connection)
	if frame.Type != browserprotocol.TypeError || frame.ID != "after-revoke" || frame.Body.(browserprotocol.Error).Code != browserprotocol.ErrorUnauthorized {
		t.Fatalf("post-revoke authentication = %+v", frame)
	}
}

func TestBrowserRevocationIsRevisionGuardedIdempotentAndClearsLease(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve|kernel.BrowserCapabilityTerminalInput)
	fixture.pair(t)
	run := adapterRunningRun(t, fixture.store, 160)
	session, found, err := fixture.store.TerminalSessionForRun(context.Background(), run.ID)
	if err != nil || !found {
		t.Fatalf("terminal session = %+v, found=%v, err=%v", session, found, err)
	}
	lease, err := fixture.store.AcquireTerminalLease(context.Background(), run.ID, session.ID, fixture.client.ID, run.Revision, session.Revision, adapterTime(t, 2_000))
	if err != nil || lease.Generation == 0 {
		t.Fatalf("terminal lease = %+v, %v", lease, err)
	}

	revoked, err := fixture.daemon.RevokeBrowserClient(context.Background(), fixture.client.ID, fixture.client.Revision)
	if err != nil || revoked.RevokedAt == nil {
		t.Fatalf("revoke = %+v, %v", revoked, err)
	}
	cleared, found, err := fixture.store.TerminalSession(context.Background(), session.ID)
	if err != nil || !found || cleared.LeaseClientID != nil || cleared.LeaseExpiresAt != nil || cleared.LeaseGeneration != lease.Generation+1 {
		t.Fatalf("cleared lease = %+v, found=%v, err=%v", cleared, found, err)
	}

	replay, err := fixture.daemon.RevokeBrowserClient(context.Background(), fixture.client.ID, revoked.Revision)
	if err != nil || replay.Revision != revoked.Revision || replay.RevokedAt == nil {
		t.Fatalf("idempotent revoke = %+v, %v", replay, err)
	}
	if _, err := fixture.daemon.RevokeBrowserClient(context.Background(), fixture.client.ID, fixture.client.Revision); !errors.Is(err, kernel.ErrRevisionConflict) {
		t.Fatalf("stale revoke = %v", err)
	}
	if err := fixture.runtime.closeClient(fixture.client.ID); err != nil {
		t.Fatalf("repeated transport close = %v", err)
	}
}

func TestBrowserRevocationLinearizesTerminalTargetResolution(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve)
	connection := fixture.pair(t)
	run := adapterRunningRun(t, fixture.store, 170)
	agent, found, err := fixture.store.Agent(context.Background(), run.AgentID)
	if err != nil || !found {
		t.Fatalf("agent = %+v, found=%v, err=%v", agent, found, err)
	}
	state, err := fixture.store.Factory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	request := browserprotocol.TerminalTargetGet{AgentID: agent.ID.String(), ExpectedAgentRevision: decimalRevision(agent.Revision), ExpectedHead: decimalSequence(state.Head)}
	hold, err := fixture.daemon.browserClientGates.acquire(context.Background(), fixture.client.ID)
	if err != nil {
		t.Fatal(err)
	}
	targetDone := make(chan struct {
		result browserprotocol.TerminalTarget
		err    error
	}, 1)
	go func() {
		result, targetErr := fixture.backend.TerminalTarget(context.Background(), rawBrowserClient(fixture.client.ID), request)
		targetDone <- struct {
			result browserprotocol.TerminalTarget
			err    error
		}{result, targetErr}
	}()
	adapterWaitGateUsers(t, fixture.daemon.browserClientGates, fixture.client.ID, 2)
	revokeDone := make(chan struct {
		client kernel.BrowserClient
		err    error
	}, 1)
	go func() {
		client, revokeErr := fixture.daemon.RevokeBrowserClient(context.Background(), fixture.client.ID, fixture.client.Revision)
		revokeDone <- struct {
			client kernel.BrowserClient
			err    error
		}{client, revokeErr}
	}()
	adapterWaitGateUsers(t, fixture.daemon.browserClientGates, fixture.client.ID, 3)
	hold()
	target := <-targetDone
	revoked := <-revokeDone
	if revoked.err != nil || revoked.client.RevokedAt == nil {
		t.Fatalf("revoke = %+v, %v", revoked.client, revoked.err)
	}
	if target.err != nil && !errors.Is(target.err, browser.ErrUnauthorized) {
		t.Fatalf("target race error = %v", target.err)
	}
	if target.err == nil {
		if target.result.AgentID != request.AgentID || target.result.AgentRevision != request.ExpectedAgentRevision || target.result.Head != request.ExpectedHead || target.result.Target == nil || target.result.Target.RunID != run.ID.String() {
			t.Fatalf("target race result = %+v", target.result)
		}
	}
	if _, err := fixture.backend.TerminalTarget(context.Background(), rawBrowserClient(fixture.client.ID), request); !errors.Is(err, browser.ErrUnauthorized) {
		t.Fatalf("post-revocation target = %v, want unauthorized", err)
	}
	adapterAssertSocketClosed(t, connection)
}

func TestBrowserRevocationReportsCleanupFailureAfterDurableCommit(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve)
	fixture.pair(t)
	backend := newUnresolvedDaemonBackend(fixture.client.ID)
	server, err := browser.Listen(browser.Config{Address: "127.0.0.1:0", AllowedOrigins: []string{adapterOrigin}, Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &BrowserRuntime{daemon: fixture.daemon, server: server}
	fixture.daemon.browserMu.Lock()
	fixture.daemon.browsers[runtime] = struct{}{}
	fixture.daemon.browserMu.Unlock()
	t.Cleanup(func() {
		fixture.daemon.browserMu.Lock()
		delete(fixture.daemon.browsers, runtime)
		fixture.daemon.browserMu.Unlock()
		_ = server.Close()
	})

	connection := adapterDial(t, server)
	_ = adapterRead(t, connection)
	auth, _ := browserprotocol.EncodeAuthProve("cleanup-auth", browserprotocol.AuthProve{
		ClientID: fixture.client.ID.String(), Signature: strings.Repeat("01", browserprotocol.SignatureSize),
	})
	adapterWrite(t, connection, auth)
	if frame := adapterRead(t, connection); frame.Type != browserprotocol.TypeAuthResult {
		t.Fatalf("cleanup fixture auth = %+v", frame)
	}
	subscribe, _ := browserprotocol.EncodeStateWatch("unresolved", browserprotocol.StateWatch{})
	adapterWrite(t, connection, subscribe)
	<-backend.started

	committed, err := fixture.daemon.RevokeBrowserClient(context.Background(), fixture.client.ID, fixture.client.Revision)
	if !errors.Is(err, ErrBrowserClientCleanup) || !errors.Is(err, browser.ErrSubscriptionUnresolved) || committed.RevokedAt == nil {
		t.Fatalf("cleanup result = %+v, %v", committed, err)
	}
	stored, found, readErr := fixture.store.BrowserClient(context.Background(), fixture.client.ID)
	if readErr != nil || !found || stored.RevokedAt == nil || stored.Revision != committed.Revision {
		t.Fatalf("durable cleanup-failure revocation = %+v, found=%v, err=%v", stored, found, readErr)
	}
	adapterAssertSocketClosed(t, connection)
}

func adapterAuthProof(t *testing.T, fixture *adapterFixture, hello browserprotocol.Hello, id string) []byte {
	t.Helper()
	transcript, err := browserprotocol.BuildAuthTranscript(browserprotocol.AuthTranscript{
		DaemonID: adapterHex(t, hello.DaemonID, browserprotocol.DaemonIDSize), BootID: adapterHex(t, hello.BootID, browserprotocol.BootIDSize),
		ConnectionNonce: adapterHex(t, hello.ConnectionNonce, browserprotocol.NonceSize), ClientID: fixture.client.ID.Bytes(),
		ValidatedHost: fixture.server.Addr(), ValidatedOrigin: adapterOrigin,
	})
	if err != nil {
		t.Fatal(err)
	}
	proof, err := browserprotocol.EncodeAuthProve(id, browserprotocol.AuthProve{ClientID: fixture.client.ID.String(), Signature: hex.EncodeToString(adapterSign(t, fixture.key, transcript))})
	if err != nil {
		t.Fatal(err)
	}
	return proof
}

func adapterWaitGateUsers(t *testing.T, gates *browserClientGates, id kernel.BrowserClientID, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		gates.mu.Lock()
		gate := gates.gates[id]
		got := 0
		if gate != nil {
			got = gate.users
		}
		gates.mu.Unlock()
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("client gate users=%d want %d", got, want)
		}
		runtime.Gosched()
	}
}

func adapterAssertSocketClosed(t *testing.T, connection *websocket.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, err := connection.Read(ctx); err == nil {
		t.Fatal("revoked browser socket remained open")
	}
}

type unresolvedDaemonBackend struct {
	identity browser.Identity
	auth     browser.Authentication
	started  chan struct{}
	calls    atomic.Int32
}

func newUnresolvedDaemonBackend(id kernel.BrowserClientID) *unresolvedDaemonBackend {
	backend := &unresolvedDaemonBackend{started: make(chan struct{})}
	backend.identity.DaemonID[0] = 1
	backend.identity.BootID[0] = 2
	copy(backend.auth.Principal.ClientID[:], id.Bytes())
	backend.auth.Capabilities = browserprotocol.CapabilityObserve
	return backend
}

func (backend *unresolvedDaemonBackend) Identity(context.Context) (browser.Identity, error) {
	return backend.identity, nil
}
func (backend *unresolvedDaemonBackend) Pair(context.Context, browser.PairRequest) (browser.Authentication, error) {
	return browser.Authentication{}, browser.ErrUnauthorized
}
func (backend *unresolvedDaemonBackend) Authenticate(context.Context, browser.AuthRequest) (browser.Authentication, error) {
	return backend.auth, nil
}
func (backend *unresolvedDaemonBackend) StateSnapshot(context.Context, [browserprotocol.ClientIDSize]byte) (browserprotocol.StateSnapshot, error) {
	return browserprotocol.StateSnapshot{}, browser.ErrNotFound
}
func (backend *unresolvedDaemonBackend) HumanRequestDetail(context.Context, [browserprotocol.ClientIDSize]byte, browserprotocol.HumanRequestDetailGet) (browserprotocol.HumanRequestDetail, error) {
	return browserprotocol.HumanRequestDetail{}, browser.ErrUnauthorized
}
func (backend *unresolvedDaemonBackend) TerminalTarget(context.Context, [browserprotocol.ClientIDSize]byte, browserprotocol.TerminalTargetGet) (browserprotocol.TerminalTarget, error) {
	return browserprotocol.TerminalTarget{}, browser.ErrUnauthorized
}
func (backend *unresolvedDaemonBackend) WatchState(context.Context, [browserprotocol.ClientIDSize]byte, browserprotocol.Decimal) (browser.StateSubscription, error) {
	if backend.calls.Add(1) == 1 {
		close(backend.started)
	}
	return unresolvedDaemonSubscription{}, nil
}

type unresolvedDaemonSubscription struct{}

func (unresolvedDaemonSubscription) Updates() <-chan browser.StateUpdate {
	return make(chan browser.StateUpdate)
}
func (unresolvedDaemonSubscription) Cancel()               {}
func (unresolvedDaemonSubscription) Done() <-chan struct{} { return nil }
func (unresolvedDaemonSubscription) Err() error            { return nil }
