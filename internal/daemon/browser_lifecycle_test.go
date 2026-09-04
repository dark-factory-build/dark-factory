//go:build darwin || linux

package daemon

import (
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestBrowserRuntimeDoneAndErrForwardServerOwner(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve)

	done := fixture.runtime.Done()
	if done != fixture.server.ServeDone() {
		t.Fatal("runtime Done did not return the server owner signal")
	}
	select {
	case <-done:
		t.Fatal("runtime Done closed before the server owner stopped")
	default:
	}
	if err := fixture.runtime.Err(); err != nil {
		t.Fatalf("runtime Err before close = %v, want nil", err)
	}

	if err := fixture.runtime.Close(); err != nil {
		t.Fatalf("runtime close = %v", err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runtime Done did not close after the server owner stopped")
	}
	if err := fixture.runtime.Err(); err != nil {
		t.Fatalf("runtime Err after normal close = %v, want nil", err)
	}
}

func TestBrowserRuntimeDoneAndErrInvalidReceiverAreAlreadyComplete(t *testing.T) {
	var nilRuntime *BrowserRuntime
	assertBrowserRuntimeAlreadyDone(t, nilRuntime)
	if err := nilRuntime.Err(); err != nil {
		t.Fatalf("nil runtime Err = %v, want nil", err)
	}

	assertBrowserRuntimeAlreadyDone(t, &BrowserRuntime{})
	if err := (&BrowserRuntime{}).Err(); err != nil {
		t.Fatalf("invalid runtime Err = %v, want nil", err)
	}
}

func assertBrowserRuntimeAlreadyDone(t *testing.T, runtime *BrowserRuntime) {
	t.Helper()
	select {
	case <-runtime.Done():
	default:
		t.Fatal("invalid runtime Done blocked indefinitely")
	}
}
