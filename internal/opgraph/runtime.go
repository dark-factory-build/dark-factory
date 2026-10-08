package opgraph

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Observation is provider-neutral runtime evidence: an adapter's aggregate
// for one selector set over one window, spelled with OpenTelemetry
// semantic-convention keys. Providers never reach the graph or the browser.
type Observation struct {
	Source      string            `json:"source"`      // adapter: factoryd, otlp, cloudflare, …
	Environment string            `json:"environment"` // local, production, …
	Kind        string            `json:"kind"`        // server | client | consumer | producer | internal
	Start       int64             `json:"start"`       // unix ms, inclusive
	End         int64             `json:"end"`         // unix ms, exclusive
	Attributes  map[string]string `json:"attributes"`
	Peer        map[string]string `json:"peer,omitempty"` // far end of client/producer work
	Count       uint64            `json:"count"`
	Errors      uint64            `json:"errors"`
	LatencyP95  float64           `json:"latency_p95_ms,omitempty"`
	Version     string            `json:"version,omitempty"`
}

// Coverage is a source saying it is reporting on a unit: which selector keys
// it can see there, as of when. Past AsOf+TTL the coverage is stale.
type Coverage struct {
	Source      string   `json:"source"`
	Environment string   `json:"environment"`
	Unit        string   `json:"unit"` // service.name or alias
	Keys        []string `json:"keys"` // selector keys this source reports for the unit
	Peers       bool     `json:"peers"`
	AsOf        int64    `json:"as_of"`
	TTL         int64    `json:"ttl"`
}

// Keys kept from any source: the selectors section 4 of the design names.
var selectorKeys = map[string]bool{
	"service.name": true, "http.request.method": true, "http.route": true, "url.path": true, "rpc.method": true,
	"messaging.destination.name": true, "messaging.system": true, "db.system.name": true, "db.namespace": true,
	"server.address": true, "server.port": true, "network.transport": true, "code.function.name": true,
	"code.file.path": true, "process.executable.name": true, "peer.service": true, "cicd.pipeline.task.name": true,
}

const (
	bucket          = int64(time.Minute / time.Millisecond)
	maxObservations = 8192
	maxCoverage     = 1024
	maxValue        = 256
)

// Runtime holds bounded per-minute aggregates in memory. Restarting the
// daemon drops them, which reads truthfully as stale or unobserved.
type Runtime struct {
	mu           sync.Mutex
	observations map[string]*Observation
	coverage     map[string]Coverage
	retain       int64
}

func NewRuntime(retain time.Duration) *Runtime {
	return &Runtime{observations: map[string]*Observation{}, coverage: map[string]Coverage{}, retain: retain.Milliseconds()}
}

// Record folds an observation into its source, selector set and minute.
// Unknown keys are dropped and values bounded, so arbitrary payload text
// never reaches the graph.
func (runtime *Runtime) Record(item Observation) {
	if runtime == nil {
		return
	}
	item.Attributes, item.Peer = clean(item.Attributes), clean(item.Peer)
	if item.End <= item.Start {
		item.End = item.Start + 1
	}
	item.Start -= item.Start % bucket
	if item.End-item.Start < bucket {
		item.End = item.Start + bucket
	}
	key := item.Source + "\x00" + item.Environment + "\x00" + item.Kind + "\x00" + attributeKey(item.Attributes) + "\x00" + attributeKey(item.Peer) + "\x00" + strconv.FormatInt(item.Start, 10)
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if held := runtime.observations[key]; held != nil {
		held.Count += item.Count
		held.Errors += item.Errors
		held.LatencyP95 = max(held.LatencyP95, item.LatencyP95)
		if item.Version != "" {
			held.Version = item.Version
		}
		return
	}
	if len(runtime.observations) >= maxObservations {
		runtime.evict(item.Start)
	}
	// Still full: the oldest minute goes, never the newest evidence.
	// ponytail: one flooding source can push others' history out; per-source quotas if that matters.
	for len(runtime.observations) >= maxObservations {
		oldest := int64(math.MaxInt64)
		for _, held := range runtime.observations {
			oldest = min(oldest, held.Start)
		}
		for key, held := range runtime.observations {
			if held.Start == oldest {
				delete(runtime.observations, key)
			}
		}
	}
	copied := item
	runtime.observations[key] = &copied
}

func (runtime *Runtime) Cover(coverage Coverage) {
	if runtime == nil {
		return
	}
	coverage.Unit = clip(coverage.Unit)
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	key := coverage.Source + "\x00" + coverage.Environment + "\x00" + coverage.Unit
	if _, held := runtime.coverage[key]; !held && len(runtime.coverage) >= maxCoverage {
		oldest := ""
		for candidate, item := range runtime.coverage {
			if oldest == "" || item.AsOf < runtime.coverage[oldest].AsOf {
				oldest = candidate
			}
		}
		delete(runtime.coverage, oldest)
	}
	// A source keeps the keys it has shown it can report while it stays current.
	if held, ok := runtime.coverage[key]; ok && held.AsOf+held.TTL >= coverage.AsOf {
		for _, item := range held.Keys {
			if !contains(coverage.Keys, item) {
				coverage.Keys = append(coverage.Keys, item)
			}
		}
		coverage.Peers = coverage.Peers || held.Peers
	}
	runtime.coverage[key] = coverage
}

// Snapshot returns retained observations and coverage, oldest first.
func (runtime *Runtime) Snapshot(now int64) ([]Observation, []Coverage) {
	if runtime == nil {
		return nil, nil
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.evict(now)
	observations := make([]Observation, 0, len(runtime.observations))
	for _, item := range runtime.observations {
		observations = append(observations, *item)
	}
	sort.Slice(observations, func(i, j int) bool { return observations[i].Start < observations[j].Start })
	coverage := make([]Coverage, 0, len(runtime.coverage))
	for _, item := range runtime.coverage {
		coverage = append(coverage, item)
	}
	sort.Slice(coverage, func(i, j int) bool { return coverage[i].Source+coverage[i].Unit < coverage[j].Source+coverage[j].Unit })
	return observations, coverage
}

func (runtime *Runtime) evict(now int64) {
	for key, item := range runtime.observations {
		if item.End < now-runtime.retain {
			delete(runtime.observations, key)
		}
	}
	// Expired coverage is kept long enough to read as stale, then forgotten.
	for key, item := range runtime.coverage {
		if item.AsOf+item.TTL < now-runtime.retain {
			delete(runtime.coverage, key)
		}
	}
}

func clean(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := map[string]string{}
	for key, value := range values {
		if selectorKeys[key] && value != "" {
			if len(value) > maxValue {
				value = strings.ToValidUTF8(value[:maxValue], "")
			}
			result[key] = value
		}
	}
	return result
}

func attributeKey(values map[string]string) string {
	var parts []string
	for key, value := range values {
		parts = append(parts, key+"="+value)
	}
	sort.Strings(parts)
	return strings.Join(parts, "\x01")
}
