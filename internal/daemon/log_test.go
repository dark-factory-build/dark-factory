package daemon

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
)

func TestLogFactorydPrefixesWithUTCRFC3339(t *testing.T) {
	var output bytes.Buffer
	when := time.Date(2026, 10, 5, 15, 6, 7, 0, time.FixedZone("BST", 60*60))
	logFactorydAt(&output, when, "factoryd: refresh timed out\n")

	got := strings.TrimSuffix(output.String(), "\n")
	parts := strings.SplitN(got, " ", 2)
	if len(parts) != 2 {
		t.Fatalf("timestamped line = %q", got)
	}
	parsed, err := time.Parse(time.RFC3339, parts[0])
	if err != nil {
		t.Fatalf("timestamp = %q: %v", parts[0], err)
	}
	if !parsed.Equal(when.UTC()) || !strings.HasSuffix(parts[0], "Z") {
		t.Fatalf("timestamp = %q, want UTC RFC 3339 for %s", parts[0], when.UTC())
	}
	if got != "2026-10-05T14:06:07Z factoryd: refresh timed out" {
		t.Fatalf("line = %q", got)
	}
}

func TestFactorydHealthProjectsOwnTimingsSlowestFirst(t *testing.T) {
	daemon := &Daemon{now: func() time.Time { return time.UnixMilli(20 * 60_000) }}
	daemon.observe("internal", map[string]string{"code.function.name": schedulerFunction}, nil, false, 22*time.Second)
	daemon.observe("internal", map[string]string{"code.function.name": schedulerFunction}, nil, true, time.Second)
	daemon.observe("server", map[string]string{"network.transport": "unix", "rpc.method": "content.create"}, nil, true, 3*time.Second)
	daemon.observe("client", map[string]string{"http.request.method": "GET"}, map[string]string{"server.address": "api.github.com"}, false, time.Minute)
	daemon.observe("server", map[string]string{"url.path": "/browser", "rpc.method": "STATE_GET"}, nil, false, 0)
	health := daemon.factorydHealth()
	want := []api.FactorydCalls{{Name: "daemon.Daemon.RunScheduler", Count: 2, Failed: 1, MaxMs: 22_000}, {Name: "content.create", Count: 1, Failed: 1, MaxMs: 3_000}}
	if health.WindowMs != uint64(runtimeWindow.Milliseconds()) || !reflect.DeepEqual(health.Calls, want) {
		t.Fatalf("health calls = %+v", health.Calls)
	}
	// Outside the window, nothing is reported.
	daemon.now = func() time.Time { return time.UnixMilli(40 * 60_000) }
	if calls := daemon.factorydHealth().Calls; len(calls) != 0 {
		t.Fatalf("stale calls = %+v", calls)
	}
}
