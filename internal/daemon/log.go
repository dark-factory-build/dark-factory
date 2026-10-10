package daemon

import (
	"fmt"
	"io"
	"path"
	"sort"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
)

// LogFactoryd is the one stderr boundary for factoryd diagnostics. Callers
// supply the existing message, including its factoryd: prefix, unchanged.
func LogFactoryd(destination io.Writer, format string, args ...any) {
	logFactorydAt(destination, time.Now(), format, args...)
}

func logFactorydAt(destination io.Writer, now time.Time, format string, args ...any) {
	_, _ = fmt.Fprintf(destination, "%s %s", now.UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

const maxCallsReported = 8

// factorydHealth projects factoryd's own observations (the runtime store the
// plant reads) over the last runtimeWindow.
func (daemon *Daemon) factorydHealth() *api.FactorydHealth {
	now := daemon.now().UnixMilli()
	observations, _ := daemon.runtimeStore().Snapshot(now)
	byName := map[string]*api.FactorydCalls{}
	for _, item := range observations {
		// Server calls are the local API's (browser frames are observed untimed).
		if item.Source != "factoryd" || item.Kind != "internal" && (item.Kind != "server" || item.Attributes["network.transport"] != "unix") || item.End <= now-runtimeWindow.Milliseconds() {
			continue
		}
		name := path.Base(item.Attributes["code.function.name"] + item.Attributes["rpc.method"])
		if name == "." {
			continue
		}
		calls := byName[name]
		if calls == nil {
			calls = &api.FactorydCalls{Name: name}
			byName[name] = calls
		}
		calls.Count += item.Count
		calls.Failed += item.Errors
		calls.MaxMs = max(calls.MaxMs, uint64(item.LatencyP95))
	}
	health := &api.FactorydHealth{WindowMs: uint64(runtimeWindow.Milliseconds()), Calls: []api.FactorydCalls{}}
	for _, calls := range byName {
		health.Calls = append(health.Calls, *calls)
	}
	sort.Slice(health.Calls, func(i, j int) bool {
		if health.Calls[i].MaxMs != health.Calls[j].MaxMs {
			return health.Calls[i].MaxMs > health.Calls[j].MaxMs
		}
		return health.Calls[i].Name < health.Calls[j].Name
	})
	health.Calls = health.Calls[:min(len(health.Calls), maxCallsReported)]
	return health
}
