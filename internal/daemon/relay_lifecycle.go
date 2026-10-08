package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/relayhost"
)

// RelayRuntime owns the outbound relay connector. The connector reaches the
// daemon only through the loopback browser listener it already owns, as an
// ordinary WebSocket client: there is no second authentication path, no
// second snapshot reader, and no relay-shaped authority. A relayed session is
// exactly a browser session whose bytes arrived over a different pipe.
type RelayRuntime struct {
	daemon    *Daemon
	connector *relayhost.Connector
	// identity, relayOrigin and browserAddress are the facts an invitation
	// needs that the connector does not project: the node key that signs a
	// pairing ticket, the origin a paired browser dials, and the loopback
	// address the relay itself pipes sessions into.
	identity       relayhost.Identity
	relayOrigin    string
	browserAddress string
	// ingestPath holds the SHA-256 of the telemetry ingest secret; ingestMu
	// keeps that file and the connector's digest in step.
	ingestPath string
	ingestMu   sync.Mutex

	stopFeed context.CancelFunc
	feedDone chan struct{}

	closeOnce sync.Once
	closeErr  error
}

// DefaultBrowserAddress is the loopback endpoint factoryd listens on unless
// the operator names another. A remote invitation omits the address when it is
// this one, which is most of the time and several QR modules.
const DefaultBrowserAddress = "127.0.0.1:43123"

// ingestKeyFileName is the home file holding the ingest secret's digest.
const ingestKeyFileName = "ingest.sha256"

// DialRelay starts the outbound relay connector for one already-listening
// browser runtime. It returns before the relay is reachable; an unreachable
// relay must never hold up the daemon, and the connector reports what it
// observes through Status.
func (daemon *Daemon) DialRelay(ctx context.Context, relayOrigin, home string, browserAddress string) (*RelayRuntime, error) {
	if daemon == nil || daemon.store == nil {
		return nil, fmt.Errorf("%w: invalid relay daemon", kernel.ErrInvalidValue)
	}
	if browserAddress == "" {
		return nil, fmt.Errorf("%w: relay requires a listening browser address", kernel.ErrInvalidValue)
	}
	identity, err := relayhost.LoadOrCreate(home)
	if err != nil {
		return nil, err
	}
	daemon.browserMu.Lock()
	closing := daemon.browserClosing
	existing := daemon.relay
	daemon.browserMu.Unlock()
	if closing {
		return nil, browser.ErrUnauthorized
	}
	if existing != nil {
		return nil, fmt.Errorf("%w: a relay connector is already registered", kernel.ErrInvalidValue)
	}
	connector, err := relayhost.Dial(ctx, relayhost.Config{
		RelayOrigin: relayOrigin,
		Identity:    identity,
		BrowserURL:  "ws://" + browserAddress + browser.Path,
		DeviceKey:   daemon.relayDeviceKey,
		IngestKey:   readIngestKey(filepath.Join(home, ingestKeyFileName)),
	})
	if err != nil {
		return nil, err
	}
	feedContext, stopFeed := context.WithCancel(ctx)
	runtime := &RelayRuntime{daemon: daemon, connector: connector, identity: identity, relayOrigin: relayOrigin, browserAddress: browserAddress, ingestPath: filepath.Join(home, ingestKeyFileName), stopFeed: stopFeed, feedDone: make(chan struct{})}
	daemon.browserMu.Lock()
	if daemon.browserClosing || daemon.relay != nil {
		daemon.browserMu.Unlock()
		stopFeed()
		_ = connector.Close()
		return nil, browser.ErrUnauthorized
	}
	daemon.relay = runtime
	// Alert subscriptions live beside this relay's node key, so they are
	// bound to the one home that owns the relay.
	daemon.push = newPushStore(filepath.Join(home, install.RelayDirectoryName))
	daemon.browserMu.Unlock()
	if project := daemon.observeConfig().PublicProject; project != "" {
		LogFactoryd(daemon.log, "factoryd: publishing project %s as public world %s\n", project, identity.PublicID())
	}
	go runtime.feed(feedContext)
	return runtime, nil
}

// feed publishes the opted-in public world over this relay connection.
func (runtime *RelayRuntime) feed(ctx context.Context) {
	defer close(runtime.feedDone)
	// A run's first publish waits a full interval, so it always lands past the
	// relay's 30 s spacing from whatever an earlier run last sent: a restart
	// that opts out is never refused.
	state := publicFeed{at: runtime.daemon.now()}
	ticker := time.NewTicker(publicFeedInterval / 4)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runtime.daemon.publishPublicWorld(ctx, &state, runtime.connector.Publish, runtime.connector.Connection)
		}
	}
}

// readIngestKey returns the stored digest. Anything but exactly 32 readable
// bytes counts as revoked.
func readIngestKey(path string) []byte {
	digest, err := os.ReadFile(path)
	if err != nil || len(digest) != sha256.Size {
		return nil
	}
	return digest
}

// ingest applies one TELEMETRY_INGEST action. A mint returns the new secret,
// which nothing keeps: only its SHA-256 is stored and handed to the relay.
func (runtime *RelayRuntime) ingest(action string) (url, secret string, active bool, err error) {
	runtime.ingestMu.Lock()
	defer runtime.ingestMu.Unlock()
	switch action {
	case "mint":
		var raw [32]byte
		_, _ = rand.Read(raw[:])
		secret = base64.RawURLEncoding.EncodeToString(raw[:])
		// The relay hashes the bearer string exactly as presented.
		digest := sha256.Sum256([]byte(secret))
		temporary := runtime.ingestPath + ".tmp"
		if err := os.WriteFile(temporary, digest[:], 0o600); err != nil {
			return "", "", false, err
		}
		if err := os.Rename(temporary, runtime.ingestPath); err != nil {
			return "", "", false, err
		}
		runtime.connector.SetIngestKey(digest[:])
	case "revoke":
		// Revoke in memory first: a file that will not go still stops the
		// secret on this connection.
		runtime.connector.SetIngestKey(nil)
		if err := os.Remove(runtime.ingestPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", "", false, err
		}
	}
	// ws→http and wss→https: the relay serves ingest on its own origin.
	url = "http" + strings.TrimPrefix(runtime.relayOrigin, "ws") + "/ingest/" + runtime.identity.NodeID() + "/v1/traces"
	return url, secret, readIngestKey(runtime.ingestPath) != nil, nil
}

// Status reports what the connector currently observes.
func (runtime *RelayRuntime) Status() relayhost.Status {
	if runtime == nil {
		return relayhost.Status{}
	}
	return runtime.connector.Status()
}

func (runtime *RelayRuntime) Close() error {
	if runtime == nil {
		return nil
	}
	runtime.closeOnce.Do(func() {
		runtime.stopFeed()
		<-runtime.feedDone
		runtime.closeErr = runtime.connector.Close()
		if runtime.daemon != nil {
			runtime.daemon.browserMu.Lock()
			if runtime.daemon.relay == runtime {
				runtime.daemon.relay = nil
			}
			runtime.daemon.browserMu.Unlock()
		}
	})
	return runtime.closeErr
}

// relayDeviceKey resolves one client id to its durable device public key. A
// missing or revoked client reports ok=false, which suppresses relay ticket
// minting for that frame rather than failing the session: the durable grant,
// not the relay, decides what a client may do.
func (daemon *Daemon) relayDeviceKey(ctx context.Context, raw [relayhost.ControllerIDSize]byte) ([relayhost.DeviceKeySize]byte, bool, error) {
	var key [relayhost.DeviceKeySize]byte
	if daemon == nil || daemon.store == nil {
		return key, false, nil
	}
	id, err := kernel.BrowserClientIDFromBytes(raw[:])
	if err != nil {
		return key, false, nil
	}
	client, found, err := daemon.store.BrowserClient(ctx, id)
	if err != nil {
		return key, false, err
	}
	if !found || client.RevokedAt != nil || len(client.PublicKey) != relayhost.DeviceKeySize {
		return key, false, nil
	}
	copy(key[:], client.PublicKey)
	return key, true, nil
}

// revokeRelayClient asks the relay to close and refuse every socket of one
// revoked client. It cannot fail the revocation: that already committed
// durably, the loopback transports have already joined, and a controller that
// reconnects presents authority the daemon no longer honours.
func (daemon *Daemon) revokeRelayClient(id kernel.BrowserClientID) {
	if daemon == nil {
		return
	}
	daemon.browserMu.Lock()
	runtime := daemon.relay
	daemon.browserMu.Unlock()
	if runtime == nil || runtime.connector == nil {
		return
	}
	var raw [relayhost.ControllerIDSize]byte
	copy(raw[:], id.Bytes())
	runtime.connector.Revoke(raw)
}
