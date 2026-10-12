package provider

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestReviewUsageLimitedRequiresProviderMarkers(t *testing.T) {
	for _, test := range []struct {
		name, output string
		want         bool
	}{
		{"quoted limit", "the task says: You've hit your usage limit", false},
		{"usage limit", "■ You've hit your usage limit", true},
		{"styled usage limit", "\x1b[31m■\x1b[0m You've hit your usage limit", true},
		{"model capacity", "⚠ Selected model is at capacity", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := ReviewUsageLimited(test.output); got != test.want {
				t.Fatalf("ReviewUsageLimited(%q) = %v, want %v", test.output, got, test.want)
			}
		})
	}
}

// The prompt carries untrusted task, pull request, diff and dispute text, so
// a Claude reviewer's file tools get the worker's confinement: user data is
// denied, the checkout and toolchain are re-allowed, and nothing else is.
// Codex keeps its own read-only profile.
func TestRunReviewConfinesClaudeFileReads(t *testing.T) {
	bin, root, account := t.TempDir(), t.TempDir(), t.TempDir()
	checkout, scratch := filepath.Join(root, "checkout"), filepath.Join(root, "scratch")
	for _, directory := range []string{checkout, scratch} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, tool := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(bin, tool), []byte("#!/bin/sh\nprintf '%s\\0' \"$@\"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	toolPath := bin + string(os.PathListSeparator) + "/usr/bin:/bin"
	review := func(kind kernel.Provider) []string {
		result, err := RunReview(context.Background(), kind, toolPath, "/opt/toolchain", "", []string{account}, checkout, "untrusted prompt", []string{checkout})
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSuffix(result.Stdout, "\x00"), "\x00")
	}
	argv := review(kernel.ProviderClaudeCode)
	if mode := slices.Index(argv, "--permission-mode"); mode < 0 || argv[mode+1] != "dontAsk" || slices.Contains(argv, "--allowedTools") {
		t.Fatalf("Claude review allows reads no rule grants: %q", argv)
	}
	at := slices.Index(argv, "--settings")
	if at < 0 {
		t.Fatalf("Claude review has no settings: %q", argv)
	}
	var settings struct {
		Permissions struct{ Allow []string }
		Sandbox     struct {
			Filesystem struct{ AllowWrite, DenyRead, AllowRead []string }
		}
	}
	if err := json.Unmarshal([]byte(argv[at+1]), &settings); err != nil {
		t.Fatal(err)
	}
	filesystem, allow := settings.Sandbox.Filesystem, settings.Permissions.Allow
	if !slices.Contains(filesystem.DenyRead, "//Users") || !slices.Contains(filesystem.AllowRead, "/"+checkout) || len(filesystem.AllowWrite) != 0 {
		t.Fatalf("read boundary = %+v", filesystem)
	}
	for _, rule := range []string{"Read(/" + checkout + "/**)", "Read(//opt/toolchain/**)"} {
		if !slices.Contains(allow, rule) {
			t.Fatalf("allow rules lack %s: %q", rule, allow)
		}
	}
	for _, rule := range allow {
		if !strings.HasPrefix(rule, "Read(") || strings.Contains(rule, scratch) || strings.Contains(rule, account) {
			t.Fatalf("allow rule %q widens the review past its grants: %q", rule, allow)
		}
	}
	argv = review(kernel.ProviderCodex)
	if slices.Contains(argv, "--settings") || !slices.Contains(argv, `default_permissions="dark-factory-review"`) {
		t.Fatalf("Codex review changed: %q", argv)
	}
}
