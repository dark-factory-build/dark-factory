package daemon

import (
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
)

// LogFactoryd is the one stderr boundary for factoryd diagnostics. Callers
// supply the existing message, including its factoryd: prefix, unchanged.
func LogFactoryd(destination io.Writer, format string, args ...any) {
	logFactorydAt(destination, time.Now(), format, args...)
}

func logFactorydAt(destination io.Writer, now time.Time, format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	factorydLog.note(now, message)
	_, _ = fmt.Fprintf(destination, "%s %s", now.UTC().Format(time.RFC3339), message)
}

const (
	maxLogSignatures = 16
	maxCallsReported = 8
	maxSignatureLen  = 240
)

// factorydLog is per process, like stderr: one factoryd runs per process.
var factorydLog = &logSignatures{held: map[string]*api.FactorydLog{}}

// Identities and hashes vary per line; the signature is what recurs.
var (
	logIdentity = regexp.MustCompile(`(?i)\b[0-9a-f]*[0-9][0-9a-f]*(?:-[0-9a-f]+)*\b`)
	logNumber   = regexp.MustCompile(`[0-9]+`)
	logRedacted = regexp.MustCompile(`\*{3,}`)
)

// logSignatures keeps the most recent distinct log lines, redacted the way
// terminal observation is and with identities and numbers normalised out.
type logSignatures struct {
	mu   sync.Mutex
	held map[string]*api.FactorydLog
}

func logSignature(message string) string {
	text := logRedacted.ReplaceAllString(string(redactTerminalText([]byte(strings.TrimSpace(message)))), "***")
	text = logNumber.ReplaceAllString(logIdentity.ReplaceAllStringFunc(text, func(token string) string {
		if len(token) >= 8 {
			return "<id>"
		}
		return token
	}), "N")
	if len(text) > maxSignatureLen {
		text = strings.ToValidUTF8(text[:maxSignatureLen], "")
	}
	return text
}

func (logs *logSignatures) note(now time.Time, message string) {
	signature, at := logSignature(message), now.UnixMilli()
	logs.mu.Lock()
	defer logs.mu.Unlock()
	if held := logs.held[signature]; held != nil {
		held.Count++
		held.LastMs = at
		return
	}
	if len(logs.held) >= maxLogSignatures {
		oldest := ""
		for key, held := range logs.held {
			if oldest == "" || held.LastMs < logs.held[oldest].LastMs {
				oldest = key
			}
		}
		delete(logs.held, oldest)
	}
	logs.held[signature] = &api.FactorydLog{Signature: signature, Count: 1, FirstMs: at, LastMs: at}
}

func (logs *logSignatures) recent() []api.FactorydLog {
	logs.mu.Lock()
	defer logs.mu.Unlock()
	result := make([]api.FactorydLog, 0, len(logs.held))
	for _, held := range logs.held {
		result = append(result, *held)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].LastMs > result[j].LastMs })
	return result
}

// factorydHealth projects factoryd's own observations (the runtime store the
// plant reads) over the last runtimeWindow, plus its recent log signatures.
func (daemon *Daemon) factorydHealth() *api.FactorydHealth {
	now := daemon.now().UnixMilli()
	observations, _ := daemon.runtimeStore().Snapshot(now)
	byName := map[string]*api.FactorydCalls{}
	for _, item := range observations {
		if item.Source != "factoryd" || item.Kind != "internal" && item.Kind != "server" || item.End <= now-runtimeWindow.Milliseconds() {
			continue
		}
		name := path.Base(item.Attributes["code.function.name"] + item.Attributes["rpc.method"] + item.Attributes["http.route"])
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
	health := &api.FactorydHealth{WindowMs: uint64(runtimeWindow.Milliseconds()), Calls: []api.FactorydCalls{}, Log: factorydLog.recent()}
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
