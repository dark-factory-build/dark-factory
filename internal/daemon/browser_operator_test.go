package daemon

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/relayhost"
)

func TestProductionBrowserPairingUsesExactlyImplementedCapabilities(t *testing.T) {
	want := kernel.BrowserCapabilityObserve | kernel.BrowserCapabilityPrivateHumanRequestDetail | kernel.BrowserCapabilityHumanActions | kernel.BrowserCapabilityTerminalInput | kernel.BrowserCapabilityAdministration
	if webCapabilities != want || webCapabilities&^kernel.BrowserCapabilityKnownMask != 0 {
		t.Fatalf("production browser capabilities = %#x, want exactly %#x", webCapabilities, want)
	}
}

func TestWebOperatorOpenStatusListAndRevoke(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve|kernel.BrowserCapabilityPrivateHumanRequestDetail)
	ctx := context.Background()
	status, err := fixture.daemon.WebStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "ready" || !status.Ready || status.Address == "" || status.Path != "/browser" || len(status.Origins) != 1 || status.Origins[0] != adapterOrigin || status.ActiveClients != 0 || status.RevokedClients != 0 || status.ActiveChallenges != 1 || status.Build.Target == "" || status.Build.Source == "" {
		t.Fatalf("initial web status = %+v", status)
	}
	link, err := fixture.daemon.OpenBrowser(ctx)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(link)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "app.darkfactory.build" || parsed.Path != "/" || parsed.RawQuery != "" || !strings.HasPrefix(parsed.Fragment, "df_pair=") {
		t.Fatalf("launch URL = %q, err=%v", link, err)
	}
	rawChallenge, err := hex.DecodeString(strings.TrimPrefix(parsed.Fragment, "df_pair="))
	if err != nil || len(rawChallenge) != 32 {
		t.Fatalf("launch challenge = %q, err=%v", parsed.Fragment, err)
	}
	status, err = fixture.daemon.WebStatus(ctx)
	if err != nil || status.ActiveChallenges != 2 {
		t.Fatalf("post-open status = %+v, %v", status, err)
	}
	page, err := fixture.daemon.WebListClients(ctx, "")
	if err != nil || len(page.Clients) != 0 || page.NextAfter != nil {
		t.Fatalf("empty client page = %+v, %v", page, err)
	}

	fixture.pair(t)
	page, err = fixture.daemon.WebListClients(ctx, "")
	if err != nil || len(page.Clients) != 1 || page.Clients[0].ID != fixture.client.ID.String() || page.Clients[0].CapabilityMask != uint8(fixture.client.CapabilityMask) || page.Clients[0].Revision != uint64(fixture.client.Revision.Int64()) {
		t.Fatalf("paired client page = %+v, %v", page, err)
	}
	if page.Clients[0].RevokedAtMs != nil {
		t.Fatal("fresh client listed as revoked")
	}
	if _, err := fixture.daemon.WebRevokeClient(ctx, api.WebClientRevocationInput{ID: fixture.client.ID.String(), ExpectedRevision: uint64(fixture.client.Revision.Int64())}); err != nil {
		t.Fatal(err)
	}
	page, err = fixture.daemon.WebListClients(ctx, "")
	if err != nil || len(page.Clients) != 1 || page.Clients[0].RevokedAtMs == nil || page.Clients[0].Revision != uint64(fixture.client.Revision.Int64()+1) {
		t.Fatalf("revoked client page = %+v, %v", page, err)
	}
	// Revocation invalidates every live challenge; the operator can still pair
	// afresh with no client left.
	if _, err := fixture.daemon.OpenBrowser(ctx); err != nil {
		t.Fatalf("pair after revoking every client = %v", err)
	}
	if status, err = fixture.daemon.WebStatus(ctx); err != nil || status.ActiveChallenges != 1 {
		t.Fatalf("post-revoke status = %+v, %v", status, err)
	}
}

func TestWebStatusKeepsTheRelayBoundLoopbackListener(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve)
	other, err := fixture.daemon.ListenBrowser("127.0.0.1:0", []string{adapterOrigin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	dialRelayFixture(t, fixture)

	status, err := fixture.daemon.WebStatus(context.Background())
	if err != nil || !status.Ready || status.Address != fixture.server.Addr() {
		t.Fatalf("relay-bound web status = %+v, %v", status, err)
	}
	if _, err := fixture.daemon.OpenBrowser(context.Background()); err != nil {
		t.Fatalf("relay-bound browser opening = %v", err)
	}
}

// TestOnlyTheOperatorAPIMintsABrowserPairing reproduces the forged requests a
// sandboxed worker could send to the loopback listener -- the Fetch Metadata,
// Origin, Referer and Host a browser would carry -- and proves none of them
// mints a challenge, while the operator-domain web_pair call does.
func TestOnlyTheOperatorAPIMintsABrowserPairing(t *testing.T) {
	fixture := newDispatchFixture(t)
	runtime, err := fixture.daemon.ListenBrowser("127.0.0.1:0", []string{webProductionOrigin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fixture.daemon.Close() })
	ctx := context.Background()
	address := runtime.server.Addr()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, site := range []string{"none", "same-origin"} {
			request, err := http.NewRequest(method, "http://"+address+"/pair", strings.NewReader(""))
			if err != nil {
				t.Fatal(err)
			}
			for key, value := range map[string]string{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document", "Sec-Fetch-Site": site, "Sec-Fetch-User": "?1", "Origin": webProductionOrigin, "Referer": webProductionOrigin + "/"} {
				request.Header.Set(key, value)
			}
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusNotFound || response.Header.Get("Location") != "" {
				t.Fatalf("forged %s /pair (%s) = %d location %q", method, site, response.StatusCode, response.Header.Get("Location"))
			}
		}
	}
	if status, err := fixture.daemon.WebStatus(ctx); err != nil || status.ActiveChallenges != 0 {
		t.Fatalf("forged requests minted: %+v, %v", status, err)
	}

	operator, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	done := fixture.serve(t)
	link, err := operator.WebPair(ctx)
	waitDispatch(t, done)
	challenge, found := strings.CutPrefix(link, webProductionOrigin+"/#df_pair=")
	if raw, decodeErr := hex.DecodeString(challenge); err != nil || !found || decodeErr != nil || len(raw) != 32 {
		t.Fatalf("web_pair = %v (link well-formed: %t)", err, found && decodeErr == nil)
	}
	if status, err := fixture.daemon.WebStatus(ctx); err != nil || status.ActiveChallenges != 1 {
		t.Fatalf("web_pair status = %+v, %v", status, err)
	}
}

// The local pairing link pins the key of the console this daemon signs: the
// home's identity, which the served bundle carries. The hosted origin then
// redeems the link's challenge (the hosted console is the one trusted origin).
func TestLocalPairingPinsTheSignedConsoleKeyAndRedeemsFromTheHostedOrigin(t *testing.T) {
	bundle := []byte("console bundle")
	previous := consoleBundle
	consoleBundle = func() []byte { return bundle }
	t.Cleanup(func() { consoleBundle = previous })
	clock := time.UnixMilli(2_000)
	store, err := createTestStore(context.Background(), filepath.Join(t.TempDir(), "kernel.sqlite"), kernel.FactoryConfig{DispatchEnabled: true, Capacity: 8}, adapterTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	daemon, err := newDaemon(store, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	daemon.home = t.TempDir()
	runtime, err := daemon.ListenBrowser("127.0.0.1:0", []string{webProductionOrigin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = daemon.Close() })
	ctx := context.Background()

	link, err := daemon.OpenBrowser(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fragment, found := strings.CutPrefix(link, webProductionOrigin+"/#df_pair=")
	pair, key, hasKey := strings.Cut(fragment, "&df_key=")
	challenge, decodeErr := hex.DecodeString(pair)
	if !found || !hasKey || decodeErr != nil || len(challenge) != browserprotocol.ChallengeSize {
		t.Fatalf("pairing link = %q", link)
	}
	pinned, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := relayhost.LoadOrCreate(daemon.home)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pinned, identity.PublicKey()) {
		t.Fatal("df_key is not the home's console signing key")
	}

	request, err := http.NewRequest(http.MethodGet, "http://"+runtime.server.Addr()+browser.ConsolePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", webProductionOrigin)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	served, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || len(served) <= ed25519.PublicKeySize+ed25519.SignatureSize || !bytes.Equal(served[:ed25519.PublicKeySize], pinned) {
		t.Fatalf("served console %d bytes, status %d, err %v", len(served), response.StatusCode, err)
	}

	frame, _ := provePairWithChallenge(t, &adapterFixture{server: runtime.server}, challenge)
	if frame.Type != browserprotocol.TypePairResult || frame.ID != "pair" {
		t.Fatalf("redeeming the link from the hosted origin = %+v", frame)
	}
}
