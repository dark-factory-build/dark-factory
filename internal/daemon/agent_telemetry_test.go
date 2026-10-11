package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// Agent telemetry is counted only for a run factoryd launched, named by its
// resource; it reaches the console with the live run's paths, and a run that
// recorded nothing shows nothing rather than zeros.
func TestAgentTelemetryCountsOnlyTheLiveRunsItNames(t *testing.T) {
	ctx := context.Background()
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve)
	fixture.pair(t)
	run := adapterRunningRoleRun(t, fixture.store, 0x70, kernel.RoleWorker)
	fixture.daemon.RememberSupervisorAccount(t.TempDir(), "", change.TrustedGitExecutable)
	session, found, err := fixture.store.TerminalSessionForRun(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("session: %v %v", found, err)
	}
	owner := newLiveAttempt(fixture.daemon, run.ID, session.ID, nil)
	owner.agentID, owner.changeID, owner.pathsSince = run.AgentID, *run.ChangeID, *run.RunningAt
	if err := fixture.daemon.registerLiveAttempt(owner); err != nil {
		t.Fatal(err)
	}
	registered := true
	t.Cleanup(func() {
		if registered {
			fixture.daemon.unregisterLiveAttempt(run.ID, owner)
		}
	})
	at := time.UnixMilli(1_000_000)
	fixture.daemon.now = func() time.Time { return at }
	request := browserprotocol.RunPathsGet{AgentID: run.AgentID.String()}
	read := func() *browserprotocol.RunTelemetry {
		t.Helper()
		answer, err := fixture.backend.RunPaths(ctx, rawBrowserClient(fixture.client.ID), request)
		if err != nil || answer.RunID != run.ID.String() {
			t.Fatalf("run paths %+v, %v", answer, err)
		}
		return answer.Telemetry
	}
	if got := read(); got != nil {
		t.Fatalf("a run with no telemetry answered %+v", got)
	}

	resource := func(id string) string {
		return `{"resource":{"attributes":[{"key":"dark_factory.run.id","value":{"stringValue":"` + id + `"}}]},`
	}
	event := func(name, extra string) string {
		return `{"attributes":[{"key":"event.name","value":{"stringValue":"` + name + `"}}` + extra + `]}`
	}
	unknown := strings.Repeat("ab", 16)
	logs := `{"resourceLogs":[` +
		resource(run.ID.String()) + `"scopeLogs":[{"logRecords":[` +
		event("tool_result", `,{"key":"tool_input","value":{"stringValue":"secret"}}`) + `,` +
		event("codex.api_request", "") + `,` +
		event("user_prompt", `,{"key":"prompt","value":{"stringValue":"secret"}}`) + `]}]},` +
		resource(unknown) + `"scopeLogs":[{"logRecords":[` + event("tool_result", "") + `]}]},` +
		`{"resource":{},"scopeLogs":[{"logRecords":[` + event("tool_result", "") + `]}]}]}`
	tokens := func(kind, value string) string {
		return `{"name":"claude_code.token.usage","sum":{"aggregationTemporality":1,"dataPoints":[{"asInt":"` + value + `","attributes":[{"key":"type","value":{"stringValue":"` + kind + `"}}]}]}}`
	}
	metrics := func(cost string) string {
		return `{"resourceMetrics":[` + resource(run.ID.String()) + `"scopeMetrics":[{"metrics":[` +
			tokens("input", "6000") + `,` + tokens("output", "200") + `,` + tokens("cacheRead", "90000") + `,` +
			`{"name":"codex.turn.cost_microusd","histogram":{"aggregationTemporality":2,"dataPoints":[{"startTimeUnixNano":"5","count":"1","sum":` + cost + `}]}}]}]}]}`
	}
	for _, export := range []struct{ path, body string }{
		{browser.LogsPath, logs},
		{browser.MetricsPath, metrics("100000")},
		// A cumulative total repeats what was already counted.
		{browser.MetricsPath, metrics("310000")},
	} {
		if err := fixture.backend.ReceiveAgentTelemetry(export.path, []byte(export.body), false); err != nil {
			t.Fatalf("%s: %v", export.path, err)
		}
	}
	at = at.Add(30 * time.Second)
	got := read()
	quiet := uint64(30)
	want := browserprotocol.RunTelemetry{TokensIn: 12000, TokensOut: 400, CostMicroUSD: 310000, ToolCalls: 1, APIRequests: 1, QuietSeconds: &quiet}
	if got == nil || got.QuietSeconds == nil || *got.QuietSeconds != quiet || got.TokensIn != want.TokensIn || got.TokensOut != want.TokensOut ||
		got.CostMicroUSD != want.CostMicroUSD || got.ToolCalls != want.ToolCalls || got.APIRequests != want.APIRequests {
		t.Fatalf("telemetry %+v, want %+v", got, want)
	}
	if len(fixture.daemon.telemetry) != 1 {
		t.Fatalf("an unlaunched run was counted: %d runs", len(fixture.daemon.telemetry))
	}

	// An ended run still takes its CLI's final flush, then ages out.
	fixture.daemon.unregisterLiveAttempt(run.ID, owner)
	registered = false
	if err := fixture.backend.ReceiveAgentTelemetry(browser.LogsPath, []byte(logs), false); err != nil {
		t.Fatal(err)
	}
	if view := fixture.daemon.runTelemetryView(run.ID); view == nil || view.ToolCalls != 2 {
		t.Fatalf("final flush after the run ended: %+v", view)
	}
	at = at.Add(runtimeWindow + time.Second)
	if err := fixture.backend.ReceiveAgentTelemetry(browser.LogsPath, []byte(`{}`), false); err != nil {
		t.Fatal(err)
	}
	if view := fixture.daemon.runTelemetryView(run.ID); view != nil {
		t.Fatalf("an ended run outlived the window: %+v", view)
	}
	if err := fixture.backend.ReceiveAgentTelemetry(browser.LogsPath, []byte(logs), false); err != nil || fixture.daemon.runTelemetryView(run.ID) != nil {
		t.Fatalf("an ended run's telemetry was taken again: %v", err)
	}
	if err := fixture.backend.ReceiveAgentTelemetry(browser.MetricsPath, []byte("not json"), false); err == nil {
		t.Fatal("a malformed export was accepted")
	}
}
