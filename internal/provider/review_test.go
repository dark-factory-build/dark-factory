package provider

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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

func TestClaudeConfinementResolvesMissingWritableChildAlias(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	link := filepath.Join(root, "link")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(link, "missing")
	settings, err := claudeConfinement([]grant{{missing, true}}, nil, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Sandbox struct {
			Filesystem struct{ AllowWrite []string }
		}
	}
	if err := json.Unmarshal([]byte(settings), &parsed); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/" + missing, "/" + filepath.Join(resolvedTarget, "missing")} {
		if !slices.Contains(parsed.Sandbox.Filesystem.AllowWrite, path) {
			t.Fatalf("missing writable alias %s: %v", path, parsed.Sandbox.Filesystem.AllowWrite)
		}
	}
}

// The prompt carries untrusted task, pull request, diff and dispute text, so
// a Claude reviewer's file tools get the worker's confinement: user data is
// denied, the checkout and toolchain are re-allowed, and nothing else is.
// Codex keeps its own read-only profile.
func TestRunReviewConfinesClaudeFileReads(t *testing.T) {
	bin, root, account := t.TempDir(), t.TempDir(), t.TempDir()
	operatorHome := t.TempDir()
	corepack := filepath.Join(operatorHome, "Library", "Caches", "dark-factory", "local-ci", "trusted", "corepack", "v1", "pnpm", pnpmVersion)
	if err := os.MkdirAll(corepack, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corepack, "package.json"), []byte(`{"name":"pnpm","version":"`+pnpmVersion+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	checkout, scratch := filepath.Join(root, "checkout"), filepath.Join(root, "scratch")
	for _, directory := range []string{checkout, scratch} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, tool := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(bin, tool), []byte("#!/bin/sh\nprintf '%s\\n%s' \"$CLAUDE_CODE_TMPDIR\" \"$CARGO_HOME\" > \"$HOME/review-tmp\"\nprintf '%s\\0' \"$@\"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	toolPath := bin + string(os.PathListSeparator) + "/usr/bin:/bin"
	review := func(kind kernel.Provider) []string {
		result, err := RunReview(context.Background(), kind, toolPath, "/opt/toolchain", operatorHome, []string{account}, checkout, "untrusted prompt", []string{checkout})
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSuffix(result.Stdout, "\x00"), "\x00")
	}
	argv := review(kernel.ProviderClaudeCode)
	if mode := slices.Index(argv, "--permission-mode"); mode < 0 || argv[mode+1] != "dontAsk" || slices.Contains(argv, "--allowedTools") || !slices.Contains(argv, "Read,Grep,Glob,Bash") {
		t.Fatalf("Claude review allows reads no rule grants: %q", argv)
	}
	at := slices.Index(argv, "--settings")
	if at < 0 {
		t.Fatalf("Claude review has no settings: %q", argv)
	}
	var settings struct {
		Permissions struct{ Allow []string }
		Sandbox     struct {
			Filesystem struct{ AllowWrite, DenyWrite, DenyRead, AllowRead []string }
		}
	}
	if err := json.Unmarshal([]byte(argv[at+1]), &settings); err != nil {
		t.Fatal(err)
	}
	filesystem, allow := settings.Sandbox.Filesystem, settings.Permissions.Allow
	wantWrites := []string{"/" + filepath.Join(root, ".review-home"), "/" + filepath.Join(root, ".review-tmp")}
	for _, path := range []string{filepath.Join(root, ".review-home"), filepath.Join(root, ".review-tmp")} {
		if resolved, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
			resolved = filepath.Join(resolved, filepath.Base(path))
			if resolved != path {
				wantWrites = append(wantWrites, "/"+resolved)
			}
		}
	}
	if !slices.Contains(filesystem.DenyRead, "//Users") || !slices.Contains(filesystem.AllowRead, "/"+checkout) || len(filesystem.AllowWrite) != len(wantWrites) {
		t.Fatalf("read boundary = %+v", filesystem)
	}
	for _, path := range wantWrites {
		if !slices.Contains(filesystem.AllowWrite, path) {
			t.Fatalf("missing write grant for %s: %+v", path, filesystem.AllowWrite)
		}
	}
	cargoHome := filepath.Join(operatorHome, ".cargo")
	for _, path := range []string{checkout, "/opt/toolchain", goModuleCachePath(operatorHome), pnpmStorePath(operatorHome), filepath.Join(operatorHome, ".rustup"), filepath.Join(cargoHome, "registry"), filepath.Join(cargoHome, "git")} {
		if !slices.Contains(filesystem.DenyWrite, "/"+path) {
			t.Fatalf("missing deny-write grant for %s: %+v", path, filesystem.DenyWrite)
		}
	}
	defaultTemp := "/private/tmp/claude-" + strconv.Itoa(os.Getuid())
	if !slices.Contains(filesystem.DenyWrite, "/"+defaultTemp) {
		t.Fatalf("missing deny-write grant for Claude default temp: %+v", filesystem.DenyWrite)
	}
	if got, err := os.ReadFile(filepath.Join(filepath.Dir(account), "review-tmp")); err != nil || string(got) != filepath.Join(root, ".review-tmp")+"\n"+cargoHome {
		t.Fatalf("CLAUDE_CODE_TMPDIR = %q, err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".review-home", ".cache", "corepack", "v1", "pnpm", pnpmVersion, "package.json")); err != nil {
		t.Fatalf("review Corepack cache was not prepared: %v", err)
	}
	for _, rule := range []string{"Bash", "Read(/" + checkout + "/**)", "Read(//opt/toolchain/**)", "Read(//System/Library/OpenSSL/openssl.cnf/**)", "Read(/" + goModuleCachePath(operatorHome) + "/**)", "Read(/" + pnpmStorePath(operatorHome) + "/**)", "Read(/" + filepath.Join(operatorHome, ".rustup") + "/**)"} {
		if !slices.Contains(allow, rule) {
			t.Fatalf("allow rules lack %s: %q", rule, allow)
		}
	}
	for _, rule := range allow {
		if (strings.HasPrefix(rule, "Write(") || strings.HasPrefix(rule, "Edit(")) && !strings.Contains(rule, filepath.Join(root, ".review-home")) && !strings.Contains(rule, filepath.Join(root, ".review-tmp")) {
			t.Fatalf("allow rule %q widens the review past its grants: %q", rule, allow)
		}
		if strings.HasPrefix(rule, "Read(") && (strings.Contains(rule, scratch) || strings.Contains(rule, account)) {
			t.Fatalf("allow rule %q widens the review past its grants: %q", rule, allow)
		}
	}
	argv = review(kernel.ProviderCodex)
	if slices.Contains(argv, "--settings") || !slices.Contains(argv, `default_permissions="dark-factory-review"`) {
		t.Fatalf("Codex review changed: %q", argv)
	}
}
