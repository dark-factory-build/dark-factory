package daemon

import (
	"context"
	"crypto/elliptic"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestRemoteStatusRefusesADisabledRelay(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve)
	status, statusErr := fixture.daemon.RemoteStatus()
	if !errors.Is(statusErr, kernel.ErrNotFound) || status != (api.RemoteStatus{}) {
		t.Fatalf("remote status without a relay = %+v, %v", status, statusErr)
	}
	if code := remoteErrorCode(statusErr); code != api.RemoteNotFound {
		t.Fatalf("remote status error code = %s", code)
	}
}

func TestRemoteStatusProjectsTheLiveConnector(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve)
	relay, _, identity := dialRelayFixture(t, fixture)
	relay.accept(t)

	deadline := time.Now().Add(relayTestDeadline)
	var status api.RemoteStatus
	for {
		var err error
		status, err = fixture.daemon.RemoteStatus()
		if err != nil {
			t.Fatal(err)
		}
		if status.Connected || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !status.Connected || status.NodeID != identity.NodeID() || status.RelayOrigin != relay.origin() || status.Sessions != 0 {
		t.Fatalf("remote status = %+v", status)
	}
}

// provePairWithChallenge redeems one specific pairing challenge over the
// fixture's own loopback transport with a fresh device key, and returns the
// frame the daemon answered with.
func provePairWithChallenge(t *testing.T, fixture *adapterFixture, challenge []byte) (browserprotocol.ControlFrame, []byte) {
	t.Helper()
	key := relayKey(t)
	connection := adapterDial(t, fixture.server)
	hello := adapterRead(t, connection).Body.(browserprotocol.Hello)
	publicKey := elliptic.Marshal(elliptic.P256(), key.PublicKey.X, key.PublicKey.Y)
	transcript, err := browserprotocol.BuildPairTranscript(browserprotocol.PairTranscript{
		DaemonID: adapterHex(t, hello.DaemonID, browserprotocol.DaemonIDSize), BootID: adapterHex(t, hello.BootID, browserprotocol.BootIDSize),
		ConnectionNonce: adapterHex(t, hello.ConnectionNonce, browserprotocol.NonceSize), Challenge: challenge, PublicKeySEC1: publicKey,
		ValidatedHost: fixture.server.Addr(), ValidatedOrigin: adapterOrigin,
	})
	if err != nil {
		t.Fatal(err)
	}
	proof, err := testEncodePairProve("pair", browserprotocol.PairProve{
		Challenge: hex.EncodeToString(challenge), PublicKeySEC1: hex.EncodeToString(publicKey), Signature: hex.EncodeToString(adapterSign(t, key, transcript)),
	})
	if err != nil {
		t.Fatal(err)
	}
	adapterWrite(t, connection, proof)
	return adapterRead(t, connection), publicKey
}

func TestRevokingAClientInvalidatesEveryLivePairingChallenge(t *testing.T) {
	fixture := newAdapterFixture(t, webCapabilities)
	fixture.pair(t)
	ctx := context.Background()

	link, err := fixture.daemon.OpenBrowser(ctx)
	if err != nil {
		t.Fatal(err)
	}
	encoded, found := strings.CutPrefix(link, webProductionOrigin+"/#df_pair=")
	encoded, _, _ = strings.Cut(encoded, "&")
	challenge, decodeErr := hex.DecodeString(encoded)
	if !found || decodeErr != nil || len(challenge) != browserprotocol.ChallengeSize {
		t.Fatalf("pairing link = %q", link)
	}
	before, err := fixture.daemon.WebStatus(ctx)
	if err != nil || before.ActiveChallenges != 1 {
		t.Fatalf("active challenges before revocation = %+v, %v", before, err)
	}

	if _, err := fixture.daemon.WebRevokeClient(ctx, api.WebClientRevocationInput{ID: fixture.client.ID.String(), ExpectedRevision: uint64(fixture.client.Revision.Int64())}); err != nil {
		t.Fatal(err)
	}

	after, err := fixture.daemon.WebStatus(ctx)
	if err != nil || after.ActiveChallenges != 0 {
		t.Fatalf("active challenges after revocation = %+v, %v", after, err)
	}
	// The link was already handed out; only the durable row stands between it
	// and a fresh client carrying the grant that was just revoked.
	frame, _ := provePairWithChallenge(t, fixture, challenge)
	if frame.Type != browserprotocol.TypeError {
		t.Fatalf("a challenge minted before revocation still paired: %+v", frame)
	}
}
