package daemon

import (
	"bytes"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestStalledRunLivenessRequiresBothQuietSignals(t *testing.T) {
	started := time.UnixMilli(1_000)
	threshold := 2 * time.Minute
	now := started.Add(threshold)
	if !stalledRunLiveness(now, started, time.Time{}, time.Time{}, threshold) {
		t.Fatal("threshold did not report a silent run")
	}
	if stalledRunLiveness(now.Add(-time.Millisecond), started, time.Time{}, time.Time{}, threshold) {
		t.Fatal("reported before the explicit threshold")
	}
	if stalledRunLiveness(now, started, now.Add(-time.Second), time.Time{}, threshold) {
		t.Fatal("terminal output growth did not clear the report")
	}
	if stalledRunLiveness(now, started, time.Time{}, now.Add(-time.Second), threshold) {
		t.Fatal("attempt API activity did not clear the report")
	}
}

func TestAddOverseerLivenessReportsActionableContext(t *testing.T) {
	runID, err := kernel.RunIDFromBytes(bytes.Repeat([]byte{1}, kernel.IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := kernel.TaskIDFromBytes(bytes.Repeat([]byte{2}, kernel.IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	started := time.UnixMilli(10_000)
	now := started.Add(stalledRunLivenessThreshold)
	attempt := &liveAttempt{runID: runID}
	attempt.markStarted(started)
	daemon := &Daemon{attempts: map[kernel.RunID]*liveAttempt{runID: attempt}}
	projected := api.OverseerSnapshot{}
	deliveries := daemon.addOverseerLiveness(&projected, []kernel.OverseerRunSummary{{ID: runID, TaskID: taskID, Provider: kernel.ProviderClaudeCode}}, now)
	if len(projected.LivenessReports) != 1 {
		t.Fatalf("liveness reports=%d, want 1", len(projected.LivenessReports))
	}
	report := projected.LivenessReports[0]
	if report.RunID != runID.String() || report.TaskID != taskID.String() || report.Provider != kernel.ProviderClaudeCode.String() || !report.Stalled || report.StartedAtMs != uint64(started.UnixMilli()) || report.ThresholdMs != uint64(stalledRunLivenessThreshold.Milliseconds()) {
		t.Fatalf("liveness report=%+v", report)
	}
	if report.Detail == "" {
		t.Fatal("liveness report omitted actionable detail")
	}
	if len(deliveries) != 1 {
		t.Fatalf("liveness deliveries=%d, want 1", len(deliveries))
	}
	deliveries[0].finish(false)
	projected = api.OverseerSnapshot{}
	deliveries = daemon.addOverseerLiveness(&projected, []kernel.OverseerRunSummary{{ID: runID, TaskID: taskID, Provider: kernel.ProviderClaudeCode}}, now)
	if len(projected.LivenessReports) != 1 || len(deliveries) != 1 {
		t.Fatalf("failed-delivery retry reports=%d deliveries=%d, want one each", len(projected.LivenessReports), len(deliveries))
	}
	deliveries[0].finish(true)
	projected = api.OverseerSnapshot{}
	if deliveries = daemon.addOverseerLiveness(&projected, []kernel.OverseerRunSummary{{ID: runID, TaskID: taskID, Provider: kernel.ProviderClaudeCode}}, now); len(projected.LivenessReports) != 0 || len(deliveries) != 0 {
		t.Fatalf("acknowledged quiet snapshot reports=%d deliveries=%d, want zero", len(projected.LivenessReports), len(deliveries))
	}
	attempt.markTerminalOutput(now, 1)
	projected = api.OverseerSnapshot{}
	daemon.addOverseerLiveness(&projected, []kernel.OverseerRunSummary{{ID: runID, TaskID: taskID, Provider: kernel.ProviderClaudeCode}}, now)
	if len(projected.LivenessReports) != 0 {
		t.Fatalf("active snapshot reports=%d, want 0", len(projected.LivenessReports))
	}
	attempt.markAttemptAPICall(now)
	later := now.Add(stalledRunLivenessThreshold)
	projected = api.OverseerSnapshot{}
	daemon.addOverseerLiveness(&projected, []kernel.OverseerRunSummary{{ID: runID, TaskID: taskID, Provider: kernel.ProviderClaudeCode}}, later)
	if len(projected.LivenessReports) != 1 {
		t.Fatalf("re-armed snapshot reports=%d, want 1", len(projected.LivenessReports))
	}
}
