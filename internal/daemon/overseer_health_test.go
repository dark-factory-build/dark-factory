package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
)

// healthWake makes the intake fixture's overseer standing and returns the
// wake factoryd's held health gives it once settled ("" for none).
func healthWake(t *testing.T, fixture *intakePollFixture) string {
	t.Helper()
	ctx := context.Background()
	overseer, _, err := fixture.store.Agent(ctx, mustAgentID(t, testID(191)))
	if err != nil {
		t.Fatal(err)
	}
	if overseer.Idle.Policy != kernel.IdleStandingInstruction {
		policy, after, instruction := kernel.IdleStandingInstruction, uint32(1), "Supervise."
		if _, err := fixture.store.UpdateAgent(ctx, overseer.ID, overseer.Revision, kernel.AgentPatch{IdlePolicy: &policy, IdleAfterSeconds: &after, IdleInstruction: &instruction}, mustKernelTime(t, 105)); err != nil {
			t.Fatal(err)
		}
	}
	wakes, err := fixture.store.EnqueueOverseerWakeups(ctx, mustKernelTime(t, fixture.now.Add(time.Minute).UnixMilli()), fixture.daemon.overseerHealth()...)
	if err != nil || len(wakes) > 1 {
		t.Fatalf("wakes = %+v, %v", wakes, err)
	}
	if len(wakes) == 0 {
		return ""
	}
	return wakes[0].Body
}

// The owner's GitHub quota under a tenth (500 of 5,000) wakes the overseer
// with the broker's x-ratelimit evidence and when it fell low.
func TestLowGitHubQuotaWakesTheOverseer(t *testing.T) {
	fixture := newIntakePollFixture(t, 120)
	secret := strings.Repeat("cd", 32)
	digest := sha256.Sum256([]byte(secret))
	id := hex.EncodeToString(digest[:])
	if err := fixture.home.WriteMaintainerCredential([]byte(`{"id":"` + id + `","credential":"` + secret + `"}`)); err != nil {
		t.Fatal(err)
	}
	host, err := maintainer.OpenHost(fixture.home)
	if err != nil {
		t.Fatal(err)
	}
	host.Instrument(func(*http.Client) *http.Client {
		return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			header := http.Header{"X-Ratelimit-Remaining": {"499"}, "X-Ratelimit-Limit": {"5000"}, "X-Ratelimit-Reset": {strconv.FormatInt(fixture.now.Add(time.Hour).Unix(), 10)}}
			body := `{"connection_id":"` + id + `","state":"connected","github_user":{"id":123,"login":"operator"},"repositories":[]}`
			return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
		})}
	})
	if _, err := host.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	fixture.daemon.github = host
	if body := healthWake(t, fixture); !strings.Contains(body, "\nHealth since 1970-01-01T00:16:40Z: GitHub quota 499/5000 remaining (Maintainer x-ratelimit-remaining) until 1970-01-01T01:16:40Z") ||
		!strings.Contains(body, "[health:github-quota]") {
		t.Fatalf("wake = %q", body)
	}
}

// A failing intake sync wakes its project's overseer with the sync fields
// factoryctl intake list shows, and recovering clears it.
func TestFailingIntakeSyncWakesTheOverseer(t *testing.T) {
	fixture := newIntakePollFixture(t, 120)
	fixture.daemon.intakeIssues = func(context.Context, string, uint64, uint32, string, uint64) (maintainer.IssuePage, error) {
		return maintainer.IssuePage{}, context.DeadlineExceeded
	}
	fixture.daemon.pollIntakeSource(context.Background(), fixture.source)
	want := fmt.Sprintf("\nHealth since 1970-01-01T00:16:40Z: intake source %s sync failing: factoryctl intake list sync.state=error sync.error=", fixture.source.ID)
	if body := healthWake(t, fixture); !strings.Contains(body, want) || !strings.Contains(body, "[health:intake:"+fixture.source.ID.String()+"]") {
		t.Fatalf("wake = %q", body)
	}
	fixture.attach(fixture.daemon)
	fixture.now = fixture.now.Add(time.Hour)
	fixture.daemon.pollIntakeSource(context.Background(), fixture.source)
	if health := fixture.daemon.overseerHealth(); len(health) != 0 {
		t.Fatalf("recovered sync still held: %+v", health)
	}
}

// Repeated Maintainer faults, a 503 or an answer outside its contract (here a
// malformed pull request page the refresh reads), wake the overseer with the
// logged signature; one or two, or other errors, do not.
func TestRepeatedMaintainerFaultsWakeTheOverseer(t *testing.T) {
	fixture := newIntakePollFixture(t, 120)
	fixture.daemon.noteMaintainerFault(errors.New("review: no such pull request"))
	fixture.daemon.noteMaintainerFault(fmt.Errorf("%w: the Maintainer answered 503", maintainer.ErrUnavailable))
	fixture.daemon.noteMaintainerFault(fmt.Errorf("%w: the Maintainer answered 503", maintainer.ErrUnavailable))
	if health := fixture.daemon.overseerHealth(); len(health) != 0 {
		t.Fatalf("two faults held: %+v", health)
	}
	malformed := func(context.Context, json.RawMessage, map[string]uint64) (json.RawMessage, error) {
		return json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"pull_requests":[{"number":7,"head_sha":"short"}]}}}`), nil
	}
	_, err := pullRequestObservation(context.Background(), malformed, "o/r", 1, nil, nil)
	fixture.daemon.noteMaintainerFault(err)
	if body := healthWake(t, fixture); !strings.Contains(body, "\nHealth since 1970-01-01T00:16:40Z: 3 Maintainer faults in factoryd.stderr.log, last at 1970-01-01T00:16:40Z: Maintainer returned an invalid pull request head [health:maintainer]") {
		t.Fatalf("wake = %q", body)
	}
	fixture.now = fixture.now.Add(time.Hour)
	if health := fixture.daemon.overseerHealth(); len(health) != 0 {
		t.Fatalf("an old streak still held: %+v", health)
	}
}
