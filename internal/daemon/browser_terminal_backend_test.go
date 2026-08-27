package daemon

import (
	"bytes"
	"context"
	"crypto/elliptic"
	"encoding/hex"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func pairExactCapabilities(t *testing.T, fixture *adapterFixture) browserprotocol.PairResult {
	t.Helper()
	connection := adapterDial(t, fixture.server)
	hello := adapterRead(t, connection).Body.(browserprotocol.Hello)
	challenge := bytes.Repeat([]byte{0x43}, browserprotocol.ChallengeSize)
	publicKey := elliptic.Marshal(elliptic.P256(), fixture.key.PublicKey.X, fixture.key.PublicKey.Y)
	transcript, err := browserprotocol.BuildPairTranscript(browserprotocol.PairTranscript{
		DaemonID: adapterHex(t, hello.DaemonID, browserprotocol.DaemonIDSize), BootID: adapterHex(t, hello.BootID, browserprotocol.BootIDSize),
		ConnectionNonce: adapterHex(t, hello.ConnectionNonce, browserprotocol.NonceSize), Challenge: challenge, PublicKeySEC1: publicKey,
		ValidatedHost: fixture.server.Addr(), ValidatedOrigin: adapterOrigin,
	})
	if err != nil {
		t.Fatal(err)
	}
	proof, err := browserprotocol.EncodePairProve("pair-exact", browserprotocol.PairProve{
		Challenge: hex.EncodeToString(challenge), PublicKeySEC1: hex.EncodeToString(publicKey), Signature: hex.EncodeToString(adapterSign(t, fixture.key, transcript)),
	})
	if err != nil {
		t.Fatal(err)
	}
	adapterWrite(t, connection, proof)
	frame := adapterRead(t, connection)
	if frame.Type != browserprotocol.TypePairResult || frame.ID != "pair-exact" {
		t.Fatalf("pair result = %+v", frame)
	}
	result := frame.Body.(browserprotocol.PairResult)
	clientID, err := kernel.BrowserClientIDFromBytes(adapterHex(t, result.ClientID, browserprotocol.ClientIDSize))
	if err != nil {
		t.Fatal(err)
	}
	client, found, err := fixture.store.BrowserClient(context.Background(), clientID)
	if err != nil || !found {
		t.Fatalf("paired client = %+v, found=%v, err=%v", client, found, err)
	}
	fixture.client = client
	return result
}

func TestProjectBrowserAuthenticationProjectsExactDurableEffectCapabilities(t *testing.T) {
	rawID := make([]byte, kernel.IDBytes)
	for index := range rawID {
		rawID[index] = byte(index + 1)
	}
	clientID, err := kernel.BrowserClientIDFromBytes(rawID)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		mask kernel.BrowserCapabilityMask
		wire browserprotocol.Capabilities
	}{
		{name: "observe", mask: kernel.BrowserCapabilityObserve, wire: browserprotocol.CapabilityObserve},
		{name: "human actions", mask: kernel.BrowserCapabilityObserve | kernel.BrowserCapabilityHumanActions, wire: browserprotocol.CapabilityObserve | browserprotocol.CapabilityHumanActions},
		{name: "terminal input", mask: kernel.BrowserCapabilityObserve | kernel.BrowserCapabilityTerminalInput, wire: browserprotocol.CapabilityObserve | browserprotocol.CapabilityTerminalInput},
		{name: "all", mask: kernel.BrowserCapabilityKnownMask, wire: browserprotocol.CapabilityObserve | browserprotocol.CapabilityPrivateHumanRequestDetail | browserprotocol.CapabilityHumanActions | browserprotocol.CapabilityTerminalInput},
	} {
		t.Run(test.name, func(t *testing.T) {
			authentication, err := projectBrowserAuthentication(kernel.BrowserClient{ID: clientID, CapabilityMask: test.mask})
			if err != nil {
				t.Fatal(err)
			}
			if authentication.Capabilities != test.wire {
				t.Fatalf("wire capabilities=%d want %d", authentication.Capabilities, test.wire)
			}
		})
	}
	for _, mask := range []kernel.BrowserCapabilityMask{0, kernel.BrowserCapabilityHumanActions, kernel.BrowserCapabilityObserve | (1 << 4)} {
		if _, err := projectBrowserAuthentication(kernel.BrowserClient{ID: clientID, CapabilityMask: mask}); err == nil {
			t.Fatalf("accepted invalid durable capability mask %d", mask)
		}
	}
}

func TestBrowserAdapterAdvertisesDurableEffectCapabilities(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve|kernel.BrowserCapabilityPrivateHumanRequestDetail|kernel.BrowserCapabilityHumanActions|kernel.BrowserCapabilityTerminalInput)
	result := pairExactCapabilities(t, fixture)
	want := browserprotocol.CapabilityObserve | browserprotocol.CapabilityPrivateHumanRequestDetail | browserprotocol.CapabilityHumanActions | browserprotocol.CapabilityTerminalInput
	if result.Capabilities != want {
		t.Fatalf("paired capabilities=%d want %d", result.Capabilities, want)
	}

	// The connection identity is minted only after this daemon-authorized
	// result; it is not part of the durable browser client or wire result.
	var principal browser.Principal
	copy(principal.ClientID[:], fixture.client.ID.Bytes())
	if principal.ConnectionID != (browser.ConnectionID{}) {
		t.Fatal("durable pair unexpectedly carried a connection identity")
	}
}
