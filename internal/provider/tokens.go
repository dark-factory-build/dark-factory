package provider

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

type codexTokenUsage struct {
	Tokens      uint64 `json:"total_tokens"`
	Input       uint64 `json:"input_tokens"`
	CachedInput uint64 `json:"cached_input_tokens"`
	Output      uint64 `json:"output_tokens"`
}

// RunTokens is the billable token usage of one run, read from the provider's
// own session log: uncached input plus output (and Claude cache creation)
// for work in cwd since the run began. Cached input reads are excluded.
// A log that is absent or unreadable counts nothing; it never fails a run.
func RunTokens(kind kernel.Provider, accountHome, accountConfig, cwd string, since time.Time) uint64 {
	runtime := RuntimePaths{accountHome: accountHome, accountConfig: accountConfig}
	var total uint64
	switch kind {
	case kernel.ProviderClaudeCode:
		transcripts, _ := filepath.Glob(filepath.Join(claudeConfigHome(runtime), "projects", escapeClaudeProjectPath(cwd), "*.jsonl"))
		for _, transcript := range transcripts {
			total = addTokens(total, claudeTranscriptTokens(transcript, since))
		}
	case kernel.ProviderCodex:
		// ponytail: every rollout written since the run began is read in full;
		// index by session id if settling ever waits on this.
		rollouts, _ := filepath.Glob(filepath.Join(codexConfigHome(runtime), "sessions", "*", "*", "*", "rollout-*.jsonl"))
		for _, rollout := range rollouts {
			if info, err := os.Stat(rollout); err == nil && !info.ModTime().Before(since) {
				total = addTokens(total, codexRolloutTokens(rollout, cwd, since))
			}
		}
	}
	return total
}

// addTokens saturates: a log is provider-written input, and a wrapped sum
// would read as no spend at all.
func addTokens(a, b uint64) uint64 {
	if a+b < a {
		return ^uint64(0)
	}
	return a + b
}

func sessionLines(path string, line func([]byte)) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(nil, nativeSessionRotateBytes)
	for scanner.Scan() {
		line(scanner.Bytes())
	}
}

// One API response spans several transcript lines that repeat its usage, so
// the last line per message id is the one counted.
func claudeTranscriptTokens(path string, since time.Time) uint64 {
	messages := map[string]uint64{}
	sessionLines(path, func(line []byte) {
		var entry struct {
			Timestamp time.Time
			Message   struct {
				ID    string
				Usage *struct {
					Input         uint64 `json:"input_tokens"`
					Output        uint64 `json:"output_tokens"`
					CacheCreation uint64 `json:"cache_creation_input_tokens"`
				}
			}
		}
		if json.Unmarshal(line, &entry) == nil && entry.Message.Usage != nil && !entry.Timestamp.Before(since) {
			usage := entry.Message.Usage
			messages[entry.Message.ID] = addTokens(addTokens(usage.Input, usage.Output), usage.CacheCreation)
		}
	})
	var total uint64
	for _, tokens := range messages {
		total = addTokens(total, tokens)
	}
	return total
}

// A rollout carries a running session total. A resumed session keeps its file
// but records each turn's cwd, so growth counts only while the session is
// working in this run's directory.
func codexRolloutTokens(path, cwd string, since time.Time) uint64 {
	var total, last uint64
	current := ""
	sessionLines(path, func(line []byte) {
		var entry struct {
			Timestamp time.Time
			Payload   struct {
				Type string
				Cwd  string
				Info *struct {
					Total codexTokenUsage `json:"total_token_usage"`
				}
			}
		}
		if json.Unmarshal(line, &entry) != nil {
			return
		}
		if entry.Payload.Cwd != "" {
			current = entry.Payload.Cwd
		}
		if entry.Payload.Type != "token_count" || entry.Payload.Info == nil {
			return
		}
		now := entry.Payload.Info.Total.billable()
		if now > last && current == cwd && !entry.Timestamp.Before(since) {
			total = addTokens(total, now-last)
		}
		last = now
	})
	return total
}

func (usage codexTokenUsage) billable() uint64 {
	// Older rollouts only contain total_tokens; retain their accounting while
	// newer rollouts use Codex's uncached-input-plus-output definition.
	if usage.Input == 0 && usage.CachedInput == 0 && usage.Output == 0 {
		return usage.Tokens
	}
	input := usage.Input
	if usage.CachedInput < input {
		input -= usage.CachedInput
	} else {
		input = 0
	}
	return addTokens(input, usage.Output)
}
