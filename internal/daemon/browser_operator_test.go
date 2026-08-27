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

func TestWebOperatorOpenStatusListAndRevoke(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve|kernel.BrowserCapabilityPrivateHumanRequestDetail)
	ctx := context.Background()
	status, err := fixture.daemon.WebStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "ready" || !status.Ready || status.Address == "" || status.Path != "/browser/v1" || len(status.Origins) != 1 || status.Origins[0] != adapterOrigin || status.ActiveClients != 0 || status.RevokedClients != 0 || status.ActiveChallenges != 1 {
		t.Fatalf("initial web status = %+v", status)
	}
	launch, err := fixture.daemon.OpenBrowser(ctx)
	if err != nil {
		t.Fatal(err)
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
