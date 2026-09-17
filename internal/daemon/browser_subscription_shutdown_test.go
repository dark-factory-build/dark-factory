//go:build darwin || linux

package daemon

import (
	"context"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestBrowserRuntimeCloseJoinsDisconnectedActiveStateSubscription(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve)
	connection := fixture.pair(t)
	state, err := fixture.store.Factory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	subscribe, err := browserprotocol.EncodeStateWatch("shutdown-watch", browserprotocol.StateWatch{AfterHead: decimalSequence(state.Head)})
	if err != nil {
		t.Fatal(err)
	}
	adapterWrite(t, connection, subscribe)
	barrier, err := browserprotocol.EncodeStateGet("subscription-installed", browserprotocol.StateGet{})
	if err != nil {
		t.Fatal(err)
	}
	adapterWrite(t, connection, barrier)
	frame := adapterRead(t, connection)
	if frame.Type != browserprotocol.TypeStateSnapshot || frame.ID != "subscription-installed" {
		t.Fatalf("active subscription barrier = %+v", frame)
	}

	if err := connection.CloseNow(); err != nil {
		t.Fatalf("browser disconnect = %v", err)
	}
	if err := fixture.runtime.Close(); err != nil {
		t.Fatalf("runtime close after browser disconnect = %v", err)
	}
	select {
	case <-fixture.server.ServeDone():
	default:
		t.Fatal("runtime close returned before browser listener joined")
	}
	if err := fixture.daemon.Close(); err != nil {
		t.Fatalf("daemon close after browser disconnect = %v", err)
	}
	if _, err := fixture.store.Factory(context.Background()); err != nil {
		t.Fatalf("browser shutdown closed Store: %v", err)
	}
	fixture.backend.subMu.Lock()
	remaining := len(fixture.backend.subs)
	fixture.backend.subMu.Unlock()
	if remaining != 0 {
		t.Fatalf("browser shutdown retained %d subscriptions", remaining)
	}
}

func TestConnectedStateWatchMapsStoreCloseToRetryableLifecycleError(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve)
	paired := fixture.pair(t)
	_ = paired.CloseNow()
	connection := fixture.authenticate(t)
	state, err := fixture.store.Factory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	subscribe, err := browserprotocol.EncodeStateWatch("store-close-watch", browserprotocol.StateWatch{AfterHead: decimalSequence(state.Head)})
	if err != nil {
		t.Fatal(err)
	}
	adapterWrite(t, connection, subscribe)
	barrier, err := browserprotocol.EncodeStateGet("store-close-barrier", browserprotocol.StateGet{})
	if err != nil {
		t.Fatal(err)
	}
	adapterWrite(t, connection, barrier)
	if frame := adapterRead(t, connection); frame.Type != browserprotocol.TypeStateSnapshot || frame.ID != "store-close-barrier" {
		t.Fatalf("connected watch was not installed: %+v", frame)
	}

	// This is a real authenticated WebSocket and the production daemon adapter;
	// closing its isolated fixture Store reproduces the lifecycle failure that
	// previously escaped mapBrowserError as a finite internal frame.
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	frame := adapterRead(t, connection)
	verdict, ok := frame.Body.(browserprotocol.Error)
	if !ok || frame.Type != browserprotocol.TypeError || frame.ID != "store-close-watch" || verdict.Code != browserprotocol.ErrorRateLimited || !bool(verdict.Retryable) {
		t.Fatalf("closed-store watch = %+v, want retryable rate_limited", frame)
	}
	// The fixture Store is deliberately closed to exercise the connected path;
	// detach the runtime from its daemon so fixture cleanup does not attempt
	// challenge invalidation through the closed Store.
	fixture.runtime.daemon = nil
	fixture.daemon.browserMu.Lock()
	delete(fixture.daemon.browsers, fixture.runtime)
	fixture.daemon.browserMu.Unlock()
	_ = fixture.server.Close()
	_ = fixture.backend.close()
}

func TestBrowserStateWatchCoalescesWhenSubscriberIsSlow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watch := &browserStateWatch{ctx: ctx, updates: make(chan browser.StateUpdate, browserStateWatchQueue)}
	if !watch.send(browser.StateUpdate{Head: 1}) || !watch.send(browser.StateUpdate{Head: 2}) {
		t.Fatal("slow subscriber blocked or rejected latest update")
	}
	if got := <-watch.updates; got.Head != 2 {
		t.Fatalf("coalesced head = %d, want 2", got.Head)
	}
}
