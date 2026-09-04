//go:build linux

package daemon

import (
	"context"
	"errors"
	"testing"
	"unsafe"

	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestTerminalEffectsExplicitlyRejectUnsupportedPlatform(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve|kernel.BrowserCapabilityTerminalInput)
	fixture.pair(t)
	run := adapterRunningRun(t, fixture.store, 170)
	session, found, err := fixture.store.TerminalSessionForRun(context.Background(), run.ID)
	if err != nil || !found {
		t.Fatalf("terminal session = %+v, found=%v, err=%v", session, found, err)
	}
	var principal browser.Principal
	copy(principal.ClientID[:], fixture.client.ID.Bytes())
	if unsafe.Sizeof(principal.ConnectionID) != kernel.IDBytes {
		t.Fatal("unexpected browser connection identity size")
	}
	raw := (*[kernel.IDBytes]byte)(unsafe.Pointer(&principal.ConnectionID))
	raw[0] = 1
	if _, err := fixture.daemon.terminalLeaseAcquire(context.Background(), principal, run.ID, session.ID, run.Revision, session.Revision); !errors.Is(err, ErrTerminalEffectsUnsupported) {
		t.Fatalf("unsupported terminal acquisition = %v", err)
	}
	unchanged, found, err := fixture.store.TerminalSession(context.Background(), session.ID)
	if err != nil || !found || unchanged.LeaseClientID != nil {
		t.Fatalf("unsupported effect changed durable state = %+v, found=%v, err=%v", unchanged, found, err)
	}
}
