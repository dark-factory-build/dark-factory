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

func TestLogSignaturesNormaliseRedactAndBound(t *testing.T) {
	logs := &logSignatures{held: map[string]*api.FactorydLog{}}
	at := time.UnixMilli(1_000)
	logs.note(at, "factoryd: intake source 0a1b2c3d4e5f60718293a4b5c6d7e8f9: open /Users/someone/.factory/db token=hunter2\n")
	logs.note(at.Add(time.Second), "factoryd: intake source 9f8e7d6c5b4a39281706f5e4d3c2b1a0: open /Users/other/x token=abc\n")
	got := logs.recent()
	if len(got) != 1 || got[0].Count != 2 || got[0].FirstMs != 1_000 || got[0].LastMs != 2_000 {
		t.Fatalf("signatures = %+v", got)
	}
	if want := "factoryd: intake source <id>: open *** ***"; got[0].Signature != want {
		t.Fatalf("signature = %q, want %q", got[0].Signature, want)
	}
	for i := range maxLogSignatures + 4 {
		logs.note(at.Add(time.Duration(i+2)*time.Second), "factoryd: line "+strings.Repeat("x", i+1))
	}
	if got := logs.recent(); len(got) != maxLogSignatures || got[0].Signature != "factoryd: line "+strings.Repeat("x", maxLogSignatures+4) {
		t.Fatalf("bounded signatures = %d, newest %q", len(got), got[0].Signature)
	}
}

func TestFactorydHealthProjectsOwnTimingsSlowestFirst(t *testing.T) {
	daemon := &Daemon{now: func() time.Time { return time.UnixMilli(20 * 60_000) }}
	daemon.observe("internal", map[string]string{"code.function.name": schedulerFunction}, nil, false, 22*time.Second)
	daemon.observe("internal", map[string]string{"code.function.name": schedulerFunction}, nil, true, time.Second)
	daemon.observe("server", map[string]string{"network.transport": "unix", "rpc.method": "content.create"}, nil, true, 3*time.Second)
	daemon.observe("client", map[string]string{"http.request.method": "GET"}, map[string]string{"server.address": "api.github.com"}, false, time.Minute)
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
