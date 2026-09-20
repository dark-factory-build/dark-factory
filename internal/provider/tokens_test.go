package provider

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestRunTokensCountsOnlyThisRunsWorkInItsDirectory(t *testing.T) {
	since := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []kernel.Provider{kernel.ProviderClaudeCode, kernel.ProviderCodex} {
		_, runtime, _ := nativeFixture(t, kind)
		cwd := "/work/change"
		write(filepath.Join(claudeConfigHome(runtime), "projects", escapeClaudeProjectPath(cwd), "a.jsonl"),
			`{"timestamp":"2026-09-20T11:00:00Z","message":{"id":"old","usage":{"input_tokens":900,"output_tokens":99}}}
{"timestamp":"2026-09-20T12:01:00Z","message":{"id":"m1","usage":{"input_tokens":1,"output_tokens":2,"cache_creation_input_tokens":3,"cache_read_input_tokens":4}}}
{"timestamp":"2026-09-20T12:01:01Z","message":{"id":"m1","usage":{"input_tokens":1,"output_tokens":12,"cache_creation_input_tokens":3,"cache_read_input_tokens":4}}}
not json
{"timestamp":"2026-09-20T12:02:00Z","message":{"id":"m2","usage":{"input_tokens":30,"output_tokens":0}}}
`)
		write(filepath.Join(codexConfigHome(runtime), "sessions", "2026", "09", "20", "rollout-x.jsonl"),
			`{"timestamp":"2026-09-20T11:00:00Z","type":"session_meta","payload":{"id":"s","cwd":"/work/earlier"}}
{"timestamp":"2026-09-20T11:30:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":1000}}}}
{"timestamp":"2026-09-20T12:01:00Z","type":"turn_context","payload":{"cwd":"/work/change"}}
{"timestamp":"2026-09-20T12:02:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":1040}}}}
{"timestamp":"2026-09-20T12:02:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":1040}}}}
{"timestamp":"2026-09-20T12:03:00Z","type":"event_msg","payload":{"type":"token_count","info":null}}
{"timestamp":"2026-09-20T12:04:00Z","type":"turn_context","payload":{"cwd":"/work/other"}}
{"timestamp":"2026-09-20T12:05:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":9000}}}}
`)
		want := map[kernel.Provider]uint64{kernel.ProviderClaudeCode: 50, kernel.ProviderCodex: 40}[kind]
		if got := RunTokens(kind, runtime.accountHome, runtime.accountConfig, cwd, since); got != want {
			t.Fatalf("%s tokens = %d, want %d", kind, got, want)
		}
		if got := RunTokens(kind, runtime.accountHome, runtime.accountConfig, "/work/nowhere", since); got != 0 {
			t.Fatalf("%s counted %d tokens for another directory", kind, got)
		}
	}
	if got := addTokens(^uint64(0)-1, 5); got != ^uint64(0) {
		t.Fatalf("token sum wrapped to %d", got)
	}
}
