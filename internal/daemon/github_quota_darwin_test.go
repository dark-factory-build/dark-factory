package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

// Each Maintainer request's owner-token GitHub calls, as the broker reports
// them, are recorded per operation; and while the owner's quota is under a
// tenth before its reset, the polls that spent it (#1510) wait: pull request
// refresh, the merge stage's observation and GitHub issue intake.
func TestLowGitHubQuotaHoldsBackPolling(t *testing.T) {
	parent, err := os.MkdirTemp("/private/tmp", "df-quota-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	ctx, path := context.Background(), filepath.Join(parent, "home")
	if _, err := install.Init(ctx, path); err != nil {
		t.Fatal(err)
	}
	home, err := install.OpenOperationalHome(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.Repeat("cd", 32)
	digest := sha256.Sum256([]byte(secret))
	id := hex.EncodeToString(digest[:])
	if err := home.WriteMaintainerCredential([]byte(`{"id":"` + id + `","credential":"` + secret + `"}`)); err != nil {
		t.Fatal(err)
	}
	host, err := maintainer.OpenHost(home)
	if err != nil {
		t.Fatal(err)
	}
	remaining, reset := "4999", time.Now().Add(time.Hour)
	daemon := &Daemon{now: time.Now, github: host}
	host.Instrument(func(*http.Client) *http.Client {
		return daemon.observed(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			header := http.Header{"X-Ratelimit-Remaining": {remaining}, "X-Ratelimit-Limit": {"5000"}, "X-Ratelimit-Reset": {strconv.FormatInt(reset.Unix(), 10)}, "X-Github-Requests": {"3"}}
			body := `{"connection_id":"` + id + `","state":"connected","github_user":{"id":123,"login":"operator"},"repositories":[]}`
			return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
		})})
	})
	if daemon.githubQuotaLow() {
		t.Fatal("low before the broker reported any quota")
	}
	for _, remaining = range []string{"4999", "499"} {
		if _, err := host.Status(ctx); err != nil {
			t.Fatal(err)
		}
		low := remaining == "499"
		daemon.productionRefreshAt = nil
		if daemon.githubQuotaLow() != low || daemon.productionRefreshAllowed(kernel.ProjectID{}, time.Now()) == low {
			t.Fatalf("remaining %s: low=%v", remaining, daemon.githubQuotaLow())
		}
	}
	var github uint64
	observations, _ := daemon.runtimeStore().Snapshot(time.Now().UnixMilli())
	for _, item := range observations {
		if item.Peer["server.address"] == "api.github.com" && item.Attributes["rpc.method"] == "get connection" {
			github += item.Count
		}
	}
	if github != 6 {
		t.Fatalf("GitHub calls recorded for status = %d in %+v", github, observations)
	}
	daemon.intakeIssues = func(context.Context, string, uint64, uint32, string, uint64) (maintainer.IssuePage, error) {
		t.Fatal("a GitHub intake source was polled on a low quota")
		return maintainer.IssuePage{}, nil
	}
	daemon.pollIntakeSource(ctx, kernel.IntakeSource{GitHubRepositoryName: "team/repo", PollSeconds: 120})
	if daemon.intakePolls != nil {
		t.Fatalf("intake poll state = %+v", daemon.intakePolls)
	}
	// The reset ends the hold.
	daemon.now = func() time.Time { return reset }
	if daemon.githubQuotaLow() {
		t.Fatal("still low after the reset")
	}
}
