//go:build darwin || linux

package daemon

import (
	"context"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestProductionBrowserPairingUsesExactlyImplementedCapabilities(t *testing.T) {
	want := kernel.BrowserCapabilityObserve | kernel.BrowserCapabilityPrivateHumanRequestDetail | kernel.BrowserCapabilityHumanActions | kernel.BrowserCapabilityTerminalInput
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
	if status.State != "ready" || !status.Ready || status.Address == "" || status.Path != "/browser" || len(status.Origins) != 1 || status.Origins[0] != adapterOrigin || status.ActiveClients != 0 || status.RevokedClients != 0 || status.ActiveChallenges != 1 {
		t.Fatalf("initial web status = %+v", status)
	}
	launch, err := fixture.daemon.OpenBrowser(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if launch.Outcome != api.WebLaunchReady {
		t.Fatalf("launch outcome = %q, want %q", launch.Outcome, api.WebLaunchReady)
	}
	parsed, err := url.Parse(launch.LaunchURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "app.darkfactory.build" || parsed.Path != "/" || parsed.RawQuery != "" || !strings.HasPrefix(parsed.Fragment, "df_pair=") {
		t.Fatalf("launch URL = %q, err=%v", launch.LaunchURL, err)
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
}

func TestWebOpenAbandonmentReclaimsChallengeCapacity(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve|kernel.BrowserCapabilityPrivateHumanRequestDetail)
	ctx := context.Background()
	for index := 0; index < 33; index++ {
		launch, err := fixture.daemon.OpenBrowser(ctx)
		if err != nil {
			t.Fatalf("open %d: %v", index, err)
		}
		_, err = fixture.daemon.AbandonBrowserOpen(ctx, api.WebAbandonOpenInput{ChallengeDigest: launch.ChallengeDigest})
		if err != nil {
			t.Fatalf("abandon %d = %v", index, err)
		}
		status, err := fixture.daemon.WebStatus(ctx)
		if err != nil || status.ActiveChallenges != 1 {
			t.Fatalf("challenge count after abandon %d = %+v, %v", index, status, err)
		}
	}
	if _, err := fixture.daemon.OpenBrowser(ctx); err != nil {
		t.Fatalf("open after repeated abandonment: %v", err)
	}
}
