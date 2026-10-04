package daemon

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"golang.org/x/sys/unix"
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

func TestOverseerSnapshotLivenessUsesRealConnectionDeliveryAndRearm(t *testing.T) {
	fixture := newDispatchFixture(t)
	// Drive only the liveness clock; the supervisor clock stays real time,
	// far from this synthetic timeline, so reading it would change the report.
	projectID, err := parseProjectID(testID(241))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.CreateProject(context.Background(), kernel.NewProject{ID: projectID, Name: "liveness", Root: "/private/tmp", VerificationPolicy: kernel.VerificationNone}, mustKernelTime(t, 100)); err != nil {
		t.Fatal(err)
	}
	active := prepareActiveAttempt(t, fixture, 241)
	ctx := context.Background()
	started := time.UnixMilli(10_000)
	quiet := started.Add(stalledRunLivenessThreshold)
	session, found, err := fixture.store.TerminalSessionForRun(ctx, active.run.ID)
	if err != nil || !found {
		t.Fatalf("terminal session: found=%v err=%v", found, err)
	}
	attempt := newLiveAttempt(fixture.daemon, active.run.ID, session.ID, nil)
	attempt.attemptDigest = active.run.CredentialDigest
	attempt.markStarted(started)
	if err := fixture.daemon.registerLiveAttempt(attempt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fixture.daemon.unregisterLiveAttempt(active.run.ID, attempt) })

	fixture.daemon.livenessClock = func() time.Time { return quiet }
	done := fixture.serve(t)
	first, err := active.client.OverseerSnapshot(ctx)
	waitDispatch(t, done)
	if err != nil || len(first.LivenessReports) != 1 {
		t.Fatalf("first real snapshot reports=%d err=%v", len(first.LivenessReports), err)
	}
	done = fixture.serve(t)
	second, err := active.client.OverseerSnapshot(ctx)
	waitDispatch(t, done)
	if err != nil || len(second.LivenessReports) != 0 {
		t.Fatalf("deduplicated real snapshot reports=%d err=%v", len(second.LivenessReports), err)
	}

	activity := quiet.Add(time.Millisecond)
	fixture.daemon.livenessClock = func() time.Time { return activity }
	done = fixture.serve(t)
	if _, err := active.client.Task(ctx); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	fixture.daemon.livenessClock = func() time.Time { return activity.Add(stalledRunLivenessThreshold) }
	done = fixture.serve(t)
	rearmed, err := active.client.OverseerSnapshot(ctx)
	waitDispatch(t, done)
	if err != nil || len(rearmed.LivenessReports) != 1 {
		t.Fatalf("API-activity re-armed reports=%d err=%v", len(rearmed.LivenessReports), err)
	}

	failedActivity := activity.Add(stalledRunLivenessThreshold + time.Millisecond)
	fixture.daemon.livenessClock = func() time.Time { return failedActivity }
	done = fixture.serve(t)
	if _, err := active.client.Task(ctx); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	failedAt := failedActivity.Add(stalledRunLivenessThreshold)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	fixture.daemon.livenessClock = func() time.Time {
		once.Do(func() { close(entered); <-release })
		return failedAt
	}
	done = fixture.serve(t)
	connection, err := net.Dial("unix", fixture.socket)
	if err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(struct {
		Method string `json:"method"`
		Params any    `json:"params"`
	}{Method: "overseer_snapshot", Params: struct{}{}})
	if err != nil {
		t.Fatal(err)
	}
	payload := append([]byte{2}, active.bearer...)
	payload = append(payload, request...)
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(payload)))
	copy(frame[4:], payload)
	if _, err := connection.Write(frame); err != nil {
		t.Fatal(err)
	}
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		t.Fatal("attempt connection is not unix")
	}
	if err := unixConnection.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("snapshot did not reach liveness projection")
	}
	raw, err := unixConnection.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Control(func(fd uintptr) {
		if setErr := unix.SetsockoptLinger(int(fd), unix.SOL_SOCKET, unix.SO_LINGER, &unix.Linger{Onoff: 1, Linger: 0}); setErr != nil {
			t.Errorf("set reset linger: %v", setErr)
		}
	}); err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	close(release)
	select {
	case handlerErr := <-done:
		if handlerErr == nil {
			t.Fatal("failed snapshot response unexpectedly succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("failed snapshot handler did not finish")
	}

	fixture.daemon.livenessClock = func() time.Time { return failedAt }
	done = fixture.serve(t)
	retried, err := active.client.OverseerSnapshot(ctx)
	waitDispatch(t, done)
	if err != nil || len(retried.LivenessReports) != 1 {
		t.Fatalf("failed-delivery re-arm reports=%d err=%v", len(retried.LivenessReports), err)
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
