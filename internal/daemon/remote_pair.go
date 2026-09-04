package daemon

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"strconv"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/relayhost"
)

const (
	// remoteCapabilities is the remote grant. It is deliberately the web mask
	// without terminal input: a remote controller observes, reads private
	// HumanRequest detail and answers it, and never types into a provider PTY.
	remoteCapabilities = kernel.BrowserCapabilityObserve | kernel.BrowserCapabilityPrivateHumanRequestDetail | kernel.BrowserCapabilityHumanActions
	// remoteLinkFragment opens the PWA route that consumes an invitation. The
	// members that follow are plain query pairs so the browser can read them
	// with URLSearchParams and this side with url.ParseQuery: every byte of
	// JSON or base64 wrapper is a byte of QR code nobody can scan.
	remoteLinkFragment = "/remote#df_remote"
)

// RemotePair mints one remote pairing invitation: a browser pairing challenge
// bound to the production origin, plus a single-use relay ticket signed by the
// node key. The challenge and the ticket are secrets that exist only in this
// reply; the daemon never logs them and keeps no copy it could reveal later.
func (daemon *Daemon) RemotePair(ctx context.Context) (api.RemoteInvitation, error) {
	// The same gate OpenBrowser uses. A challenge must not be minted against a
	// transport that browser shutdown has already begun to invalidate.
	daemon.browserLifecycleMu.Lock()
	defer daemon.browserLifecycleMu.Unlock()
	runtime, valid := daemon.webRuntime()
	if !valid || !browserRuntimeReady(runtime) {
		return api.RemoteInvitation{}, fmt.Errorf("%w: browser transport unavailable", kernel.ErrBusy)
	}
	relay, err := daemon.relayRuntime()
	if err != nil {
		return api.RemoteInvitation{}, err
	}
	if !runtimeAllowsProductionOrigin(runtime) {
		return api.RemoteInvitation{}, fmt.Errorf("%w: production browser origin is not configured", kernel.ErrInvalidValue)
	}
	state, err := daemon.store.Factory(ctx)
	if err != nil {
		return api.RemoteInvitation{}, err
	}
	at, err := daemon.timestamp()
	if err != nil {
		return api.RemoteInvitation{}, err
	}
	if at.Int64() > math.MaxInt64-int64(webChallengeTTL/time.Millisecond) {
		return api.RemoteInvitation{}, fmt.Errorf("%w: browser challenge clock exhausted", kernel.ErrInvalidValue)
	}
	expires, err := kernel.NewUnixMillis(at.Int64() + int64(webChallengeTTL/time.Millisecond))
	if err != nil {
		return api.RemoteInvitation{}, err
	}
	var challenge [browserprotocol.ChallengeSize]byte
	var controller [relayhost.ControllerIDSize]byte
	runtime.backend.randomMu.Lock()
	_, challengeErr := io.ReadFull(runtime.backend.random, challenge[:])
	_, controllerErr := io.ReadFull(runtime.backend.random, controller[:])
	runtime.backend.randomMu.Unlock()
	if challengeErr != nil || controllerErr != nil || allZero(challenge[:]) || allZero(controller[:]) {
		return api.RemoteInvitation{}, fmt.Errorf("%w: remote pairing secret generation failed", kernel.ErrBusy)
	}
	expiry := time.UnixMilli(expires.Int64())
	ticket := relayhost.PairTicket(relay.identity, controller, expiry)
	if ticket == "" {
		return api.RemoteInvitation{}, fmt.Errorf("%w: relay pairing ticket could not be minted", kernel.ErrBusy)
	}
	digest := kernel.HashBrowserChallenge(challenge[:])
	// A commit-uncertain mint is a plain failure here. There is no exact
	// cleanup identity to hand back, and an unopened challenge simply expires.
	if _, err := daemon.store.CreateBrowserPairingChallenge(ctx, digest, runtime.backend.boot, webProductionOrigin, remoteCapabilities, at, expires); err != nil {
		return api.RemoteInvitation{}, err
	}
	// Every value is already URL-safe, so the fragment is built literally
	// rather than through Values.Encode, which would escape the relay origin's
	// own separators for nothing. The node public key is deliberately absent:
	// only the relay checks it, and the daemon id is the fingerprint a browser
	// pins. A default relay origin or browser address is omitted rather than
	// transmitted, because every byte here is a QR module the operator has to
	// scan.
	link := webProductionOrigin + remoteLinkFragment +
		"&node=" + relay.identity.NodeID() +
		"&daemon=" + state.DaemonID.String() +
		"&challenge=" + hex.EncodeToString(challenge[:]) +
		"&ticket=" + ticket +
		"&expires=" + strconv.FormatInt(expiry.Unix(), 10)
	if relay.relayOrigin != relayhost.DefaultRelayOrigin {
		link += "&relay=" + relay.relayOrigin
	}
	if relay.browserAddress != DefaultBrowserAddress {
		link += "&host=" + relay.browserAddress
	}
	return api.RemoteInvitation{
		Link:            link,
		NodeID:          relay.identity.NodeID(),
		Expires:         expiry.Unix(),
		ChallengeDigest: hex.EncodeToString(digest.Bytes()),
	}, nil
}

// RemoteStatus is the bounded operator view of the relay connector.
func (daemon *Daemon) RemoteStatus() (api.RemoteStatus, error) {
	relay, err := daemon.relayRuntime()
	if err != nil {
		return api.RemoteStatus{}, err
	}
	status := relay.Status()
	return api.RemoteStatus{
		NodeID:      status.NodeID,
		RelayOrigin: relay.relayOrigin,
		Connected:   status.Connected,
		Sessions:    status.Sessions,
	}, nil
}

// relayRuntime returns the registered relay connector. A factory started
// without a relay origin has no remote surface at all, which is a refusal
// rather than an empty projection: there is no node to invite anyone to.
func (daemon *Daemon) relayRuntime() (*RelayRuntime, error) {
	if daemon == nil || daemon.store == nil {
		return nil, fmt.Errorf("%w: invalid daemon", kernel.ErrInvalidValue)
	}
	daemon.browserMu.Lock()
	relay := daemon.relay
	daemon.browserMu.Unlock()
	if relay == nil || relay.connector == nil || relay.identity.NodeID() == "" || relay.relayOrigin == "" || relay.browserAddress == "" {
		return nil, fmt.Errorf("%w: the relay is not enabled", kernel.ErrNotFound)
	}
	return relay, nil
}

func runtimeAllowsProductionOrigin(runtime *BrowserRuntime) bool {
	for _, origin := range runtime.origins {
		if origin == webProductionOrigin {
			return true
		}
	}
	return false
}
