package daemon

import (
	"math"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/opgraph"
)

// runTelemetry is what one run's agent CLI recorded about itself: counts and
// cost only. cumulative holds the last value of each cumulative series, keyed
// by a digest, so a repeated total is counted once.
type runTelemetry struct {
	tokensIn, tokensOut, costUSD float64
	toolCalls, apiRequests       uint64
	lastActivity, updated        time.Time
	cumulative                   map[string]float64
}

// maxTelemetrySeries bounds one run's cumulative series; a CLI emits a few
// dozen (token type by model). Later series are ignored, not miscounted.
const maxTelemetrySeries = 256

// ReceiveAgentTelemetry counts a local OTLP metrics or logs export against
// the runs factoryd launched. A resource naming any other run is dropped.
func (backend *browserBackend) ReceiveAgentTelemetry(path string, body []byte, protobuf bool) error {
	if backend.owner == nil {
		return browser.ErrNotFound
	}
	decode := opgraph.DecodeAgentLogs
	if path == browser.MetricsPath {
		decode = opgraph.DecodeAgentMetrics
	}
	points, err := decode(body, protobuf)
	if err != nil {
		return err
	}
	backend.owner.recordAgentTelemetry(points)
	return nil
}

func (daemon *Daemon) recordAgentTelemetry(points []opgraph.AgentPoint) {
	now := daemon.now()
	live := map[kernel.RunID]bool{}
	daemon.attemptMu.Lock()
	for id, attempt := range daemon.attempts {
		select {
		case <-attempt.done:
		default:
			live[id] = true
		}
	}
	daemon.attemptMu.Unlock()
	daemon.telemetryMu.Lock()
	defer daemon.telemetryMu.Unlock()
	// An ended run keeps its counts for the runtime window, so the final flush
	// an exiting CLI sends still lands; after that it is gone.
	for id, entry := range daemon.telemetry {
		if !live[id] && now.Sub(entry.updated) > runtimeWindow {
			delete(daemon.telemetry, id)
		}
	}
	for _, point := range points {
		id, err := decodeID(point.Run, kernel.RunIDFromBytes)
		if err != nil || math.IsNaN(point.Value) || math.IsInf(point.Value, 0) {
			continue
		}
		entry := daemon.telemetry[id]
		if entry == nil {
			if !live[id] {
				continue
			}
			if daemon.telemetry == nil {
				daemon.telemetry = make(map[kernel.RunID]*runTelemetry)
			}
			entry = &runTelemetry{cumulative: map[string]float64{}}
			daemon.telemetry[id] = entry
		}
		value := point.Value
		if point.Series != "" {
			previous, seen := entry.cumulative[point.Series]
			if !seen && len(entry.cumulative) >= maxTelemetrySeries {
				continue
			}
			entry.cumulative[point.Series] = value
			// A total below the last one is a restarted series.
			if seen && value >= previous {
				value -= previous
			}
		}
		entry.updated = now
		switch point.Kind {
		case opgraph.TokensIn:
			entry.tokensIn += value
		case opgraph.TokensOut:
			entry.tokensOut += value
		case opgraph.CostUSD:
			entry.costUSD += value
		case opgraph.ToolCall:
			entry.toolCalls++
			entry.lastActivity = now
		case opgraph.APIRequest:
			entry.apiRequests++
			entry.lastActivity = now
		}
	}
}

// runTelemetryView is one run's counts as the console reads them, or nil
// when its agent recorded nothing: no telemetry is never shown as zero.
func (daemon *Daemon) runTelemetryView(id kernel.RunID) *browserprotocol.RunTelemetry {
	daemon.telemetryMu.Lock()
	defer daemon.telemetryMu.Unlock()
	entry := daemon.telemetry[id]
	if entry == nil {
		return nil
	}
	const limit = uint64(browserprotocol.MaxJSONInteger)
	count := func(value float64) uint64 { return uint64(min(math.Round(value), float64(limit))) }
	view := &browserprotocol.RunTelemetry{
		TokensIn: count(entry.tokensIn), TokensOut: count(entry.tokensOut), CostMicroUSD: count(entry.costUSD * 1e6),
		ToolCalls: min(entry.toolCalls, limit), APIRequests: min(entry.apiRequests, limit),
	}
	if !entry.lastActivity.IsZero() {
		quiet := uint64(max(daemon.now().Sub(entry.lastActivity), 0) / time.Second)
		view.QuietSeconds = &quiet
	}
	return view
}
