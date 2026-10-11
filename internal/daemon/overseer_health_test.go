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

// Repeated Maintainer faults, an answer outside its contract or a 503, wake
// the overseer with the logged signature, whichever refresh read met them:
// checks and deployments a best-effort refresh skips past count too. One or
// two, or other errors, do not.
func TestRepeatedMaintainerFaultsWakeTheOverseer(t *testing.T) {
	fixture := newIntakePollFixture(t, 120)
	ctx, fault, unavailable := context.Background(), fixture.daemon.noteMaintainerFault, false
	call := func(_ context.Context, request json.RawMessage, _ map[string]uint64) (json.RawMessage, error) {
		content := `{"pull_requests":[{"number":7,"head_sha":"` + strings.Repeat("a", 40) + `","state":"open"}]}`
		switch {
		case unavailable:
			return nil, fmt.Errorf("%w: the Maintainer answered 503", maintainer.ErrUnavailable)
		case strings.Contains(string(request), "observe_pull_request_checks"):
			content = `{"checks":"malformed"}`
		case strings.Contains(string(request), "list_deployments"):
			content = `{"deployments":"malformed"}`
		}
		return json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":` + content + `}}`), nil
	}
	fault(errors.New("review: no such pull request"))
	if observation, err := pullRequestObservation(ctx, call, "o/r", 1, nil, nil, fault); err != nil || observation.Unavailable != "checks" {
		t.Fatalf("best-effort refresh = %+v, %v", observation, err)
	}
	if _, _, err := recordDeployments(ctx, call, "o/r", 1, nil, nil, nil, 0, fault); err == nil {
		t.Fatal("malformed deployments read")
	}
	if health := fixture.daemon.overseerHealth(); len(health) != 0 {
		t.Fatalf("two faults held: %+v", health)
	}
	unavailable = true
	if _, err := pullRequestObservation(ctx, call, "o/r", 1, nil, nil, fault); err == nil {
		t.Fatal("unavailable refresh read")
	}
	if body := healthWake(t, fixture); !strings.Contains(body, "\nHealth since 1970-01-01T00:16:40Z: 3 Maintainer faults, last at 1970-01-01T00:16:40Z: ") ||
		!strings.Contains(body, "the Maintainer answered 503 [health:maintainer]") {
		t.Fatalf("wake = %q", body)
	}
	fixture.now = fixture.now.Add(time.Hour)
	if health := fixture.daemon.overseerHealth(); len(health) != 0 {
		t.Fatalf("an old streak still held: %+v", health)
	}
}
