//go:build darwin || linux

package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

type subscriptionInterruptError struct{}

func (subscriptionInterruptError) Error() string { return "independent driver interruption" }

func TestBrowserStateSubscriptionOwnerCancellationIgnoresDriverInterrupt(t *testing.T) {
	subscription, backend := newBrowserStateSubscriptionLoopTest()
	started := make(chan struct{})
	go subscription.runReading(func() (kernel.EventSequence, error) {
		close(started)
		<-subscription.ctx.Done()
		return kernel.EventSequence{}, subscriptionInterruptError{}
	})

	<-started
	subscription.Cancel()
	waitBrowserStateSubscriptionLoopTest(t, subscription)
	if err := subscription.Err(); err != nil {
		t.Fatalf("owner-cancelled subscription error = %v", err)
	}
	assertBrowserStateSubscriptionUnregistered(t, backend, subscription)
}

func TestBrowserStateSubscriptionPreservesErrorClassifiedWhileOwnerLive(t *testing.T) {
	subscription, backend := newBrowserStateSubscriptionLoopTest()
	want := errors.New("independent Store failure")
	go subscription.runReading(func() (kernel.EventSequence, error) {
		return kernel.EventSequence{}, want
	})

	waitBrowserStateSubscriptionLoopTest(t, subscription)
	if err := subscription.Err(); !errors.Is(err, want) {
		t.Fatalf("live-context subscription error = %v, want %v", err, want)
	}
	subscription.Cancel()
	if err := subscription.Err(); !errors.Is(err, want) {
		t.Fatalf("later cancellation erased subscription error: %v", err)
	}
	assertBrowserStateSubscriptionUnregistered(t, backend, subscription)
}

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

func newBrowserStateSubscriptionLoopTest() (*browserStateWatch, *browserBackend) {
	ctx, cancel := context.WithCancel(context.Background())
	backend := &browserBackend{subs: make(map[*browserStateWatch]struct{})}
	subscription := &browserStateWatch{
		backend: backend,
		ctx:     ctx,
		cancel:  cancel,
		updates: make(chan browser.StateUpdate, browserStateWatchQueue),
		done:    make(chan struct{}),
	}
	backend.subs[subscription] = struct{}{}
	return subscription, backend
}

func waitBrowserStateSubscriptionLoopTest(t *testing.T, subscription *browserStateWatch) {
	t.Helper()
	select {
	case <-subscription.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("subscription did not join")
	}
}

func assertBrowserStateSubscriptionUnregistered(t *testing.T, backend *browserBackend, subscription *browserStateWatch) {
	t.Helper()
	backend.subMu.Lock()
	_, registered := backend.subs[subscription]
	backend.subMu.Unlock()
	if registered {
		t.Fatal("joined subscription remained registered")
	}
}
