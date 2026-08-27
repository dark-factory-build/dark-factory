package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// BrowserRuntime owns the loopback listener, every accepted WebSocket and
// every Store-watch subscription created through its backend. Close performs
// the only safe order: stop and join the transport first, then cancel and join
// any backend subscription left by an interrupted connection, then invalidate
// unredeemed challenges for this runtime boot. The caller may close the Store
// only after Close returns.
type BrowserRuntime struct {
	daemon  *Daemon
	server  *browser.Server
	backend *browserBackend
	origins []string

	closeOnce sync.Once
	closeErr  error
}

// ListenBrowser starts the state-only browser-v1 surface. Address is still
// validated by internal/browser and therefore accepts only exact IPv4
// loopback. Terminal and HumanRequest effects are not part of this slice.
func (daemon *Daemon) ListenBrowser(address string, allowedOrigins []string) (*BrowserRuntime, error) {
	if daemon == nil || daemon.store == nil {
		return nil, fmt.Errorf("%w: invalid browser daemon", kernel.ErrInvalidValue)
	}
	daemon.browserMu.Lock()
	defer daemon.browserMu.Unlock()
	if daemon.browserClosing {
		return nil, browser.ErrUnauthorized
	}
	backend, err := newProductionBrowserBackend(daemon)
	if err != nil {
		return nil, err
	}
	server, err := browser.Listen(browser.Config{Address: address, AllowedOrigins: allowedOrigins, Backend: backend})
	if err != nil {
		_ = backend.close()
		return nil, err
	}
	runtime := &BrowserRuntime{daemon: daemon, server: server, backend: backend, origins: append([]string(nil), allowedOrigins...)}
	if daemon.browsers == nil {
		daemon.browsers = make(map[*BrowserRuntime]struct{})
	}
	daemon.browsers[runtime] = struct{}{}
	return runtime, nil
}

func (runtime *BrowserRuntime) Addr() string {
	if runtime == nil || runtime.server == nil {
		return ""
	}
	return runtime.server.Addr()
}

func (runtime *BrowserRuntime) Close() error {
	if runtime == nil {
		return nil
	}
	if runtime.daemon != nil {
		// Remove the runtime under the same gate OpenBrowser uses. The gate is
		// released before server/backend callbacks so a transport cannot
		// deadlock by re-entering daemon lifecycle code.
		runtime.daemon.browserLifecycleMu.Lock()
		runtime.daemon.browserMu.Lock()
		delete(runtime.daemon.browsers, runtime)
		runtime.daemon.browserMu.Unlock()
		runtime.daemon.browserLifecycleMu.Unlock()
	}
	runtime.closeOnce.Do(func() {
		var closeErrors []error
		if runtime.server != nil {
			closeErrors = append(closeErrors, runtime.server.Close())
		}
		if runtime.backend != nil {
			closeErrors = append(closeErrors, runtime.backend.close())
		}
		if runtime.daemon != nil && runtime.daemon.store != nil && runtime.backend != nil {
			closeErrors = append(closeErrors, runtime.daemon.store.InvalidateBrowserPairingChallenges(context.Background(), runtime.backend.boot))
		}
		runtime.closeErr = errors.Join(closeErrors...)
	})
	return runtime.closeErr
}

func (daemon *Daemon) closeBrowsers() error {
	if daemon == nil {
		return nil
	}
	// This gate is the linearization point shared with OpenBrowser. It is held
	// only through the readiness/close decision, never across transport
	// callbacks that may re-enter daemon code.
	daemon.browserLifecycleMu.Lock()
	daemon.browserMu.Lock()
	daemon.browserClosing = true
	runtimes := make([]*BrowserRuntime, 0, len(daemon.browsers))
	for runtime := range daemon.browsers {
		runtimes = append(runtimes, runtime)
	}
	daemon.browserMu.Unlock()
	daemon.browserLifecycleMu.Unlock()
	var closeErrors []error
	for _, runtime := range runtimes {
		closeErrors = append(closeErrors, runtime.Close())
	}
	return errors.Join(closeErrors...)
}

// closeClient terminates all existing sockets for a client after a durable
// revocation has committed. The owner-only revocation lane must use the same
// backend client gate before calling this non-reentrant transport callback.
func (runtime *BrowserRuntime) closeClient(id kernel.BrowserClientID) error {
	if runtime == nil || runtime.server == nil {
		return nil
	}
	var raw [16]byte
	copy(raw[:], id.Bytes())
	return runtime.server.CloseClient(raw)
}
