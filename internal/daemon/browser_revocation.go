package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// ErrBrowserClientCleanup means revocation committed durably, but one or more
// transports could not prove that every matching connection joined. Callers
// must report the cleanup uncertainty without pretending revocation rolled
// back; the returned BrowserClient contains the committed durable state.
var ErrBrowserClientCleanup = errors.New("daemon: revoked browser client cleanup unresolved")

// RevokeBrowserClient is the sole daemon-owned revocation lane. The exact
// client gate spans the durable transaction and transport join, so an
// authenticate or effect either completes before revocation or reloads the
// committed revoked client afterward. SQLite, not the gate, authorizes the
// transition.
func (daemon *Daemon) RevokeBrowserClient(ctx context.Context, id kernel.BrowserClientID, expected kernel.Revision) (kernel.BrowserClient, error) {
	if daemon == nil || daemon.store == nil || daemon.now == nil || daemon.browserClientGates == nil {
		return kernel.BrowserClient{}, fmt.Errorf("%w: invalid browser daemon", kernel.ErrInvalidValue)
	}
	release, err := daemon.browserClientGates.acquire(ctx, id)
	if err != nil {
		return kernel.BrowserClient{}, err
	}
	defer release()
	return daemon.revokeBrowserClientHeld(ctx, id, expected)
}

// revokeBrowserClientHeld requires the exact client gate. Keeping the durable
// mutation separate from gate acquisition makes both causal gate orders
// testable without adding a generic operation framework.
func (daemon *Daemon) revokeBrowserClientHeld(ctx context.Context, id kernel.BrowserClientID, expected kernel.Revision) (kernel.BrowserClient, error) {
	at, err := kernel.NewUnixMillis(daemon.now().UnixMilli())
	if err != nil {
		return kernel.BrowserClient{}, err
	}
	client, err := daemon.store.RevokeBrowserClient(ctx, id, expected, at)
	if err != nil {
		return kernel.BrowserClient{}, err
	}

	// Take the transport snapshot only after commit. A concurrently added
	// transport shares this client gate, while one already removed has joined
	// all of its connections before unregistering itself.
	daemon.browserMu.Lock()
	runtimes := make([]*BrowserRuntime, 0, len(daemon.browsers))
	for runtime := range daemon.browsers {
		runtimes = append(runtimes, runtime)
	}
	daemon.browserMu.Unlock()
	var cleanupErrors []error
	for _, runtime := range runtimes {
		cleanupErrors = append(cleanupErrors, runtime.closeClient(id))
	}
	if cleanupErr := errors.Join(cleanupErrors...); cleanupErr != nil {
		return client, errors.Join(ErrBrowserClientCleanup, cleanupErr)
	}
	return client, nil
}
