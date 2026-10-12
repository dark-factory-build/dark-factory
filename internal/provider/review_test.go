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
			Filesystem struct{ AllowRead, AllowWrite []string }
		}
		Permissions struct{ Allow []string }
	}
	if err := json.Unmarshal([]byte(settings), &parsed); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/" + missing, "/" + filepath.Join(resolvedTarget, "missing")} {
		if !slices.Contains(parsed.Sandbox.Filesystem.AllowRead, path) || !slices.Contains(parsed.Sandbox.Filesystem.AllowWrite, path) || !slices.Contains(parsed.Permissions.Allow, "Read("+path+"/**)") {
			t.Fatalf("missing writable alias %s: %+v %v", path, parsed.Sandbox.Filesystem, parsed.Permissions.Allow)
		}
	}
}

func TestClaudeConfinementPreservesReadOnlyAlias(t *testing.T) {
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
	settings, err := claudeConfinement([]grant{{link, false}}, nil, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Permissions struct{ Allow []string }
		Sandbox     struct {
			Filesystem struct{ AllowRead, DenyWrite []string }
		}
	}
	if err := json.Unmarshal([]byte(settings), &parsed); err != nil {
		t.Fatal(err)
	}
	resolvedRule := "/" + resolvedTarget
	if !slices.Contains(parsed.Sandbox.Filesystem.AllowRead, resolvedRule) || !slices.Contains(parsed.Sandbox.Filesystem.DenyWrite, resolvedRule) || !slices.Contains(parsed.Permissions.Allow, "Read("+resolvedRule+"/**)") {
		t.Fatalf("missing resolved read-only alias %s: %+v %v", resolvedRule, parsed.Sandbox.Filesystem, parsed.Permissions.Allow)
	}
	for _, path := range []string{"/" + link, resolvedRule} {
		if !slices.Contains(parsed.Sandbox.Filesystem.DenyWrite, path) {
			t.Fatalf("missing read-only deny-write alias %s: %+v", path, parsed.Sandbox.Filesystem)
		}
	}
}

func TestClaudeConfinementCompactsReadOnlyAliasesWithResolvedReads(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 8; i++ {
		root = filepath.Join(root, "long-review-root")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var grants []grant
	var resolved []string
	for i := 0; i < 6; i++ {
		target := filepath.Join(root, strings.Repeat("target-", 12)+strconv.Itoa(i))
		link := filepath.Join(root, strings.Repeat("link-", 12)+strconv.Itoa(i))
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		resolvedTarget, err := filepath.EvalSymlinks(link)
		if err != nil {
			t.Fatal(err)
		}
		grants = append(grants, grant{link, false})
		resolved = append(resolved, "/"+resolvedTarget)
	}
	settings, err := claudeConfinement(grants, nil, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Sandbox struct {
			Filesystem struct{ AllowRead []string }
		}
		Permissions struct{ Allow []string }
	}
	if err := json.Unmarshal([]byte(settings), &parsed); err != nil {
		t.Fatal(err)
	}
	for _, path := range resolved {
		if !slices.Contains(parsed.Sandbox.Filesystem.AllowRead, path) || !slices.Contains(parsed.Permissions.Allow, "Read("+path+"/**)") {
			t.Fatalf("missing compacted resolved read grant %s (settings=%d): %+v %v", path, len(settings), parsed.Sandbox.Filesystem.AllowRead, parsed.Permissions.Allow)
		}
	}
}

// The prompt carries untrusted task, pull request, diff and dispute text, so
// a Claude reviewer's file tools get the worker's confinement: user data is
// denied, the checkout and toolchain are re-allowed, and nothing else is.
// Codex keeps its own read-only profile.
func TestRunReviewConfinesClaudeFileReads(t *testing.T) {
	bin, rootBase, account := t.TempDir(), t.TempDir(), t.TempDir()
	root := filepath.Join(rootBase, strings.Repeat("review-root-", 8))
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
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
	if err := os.Mkdir(filepath.Join(checkout, "web"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "web", "package.json"), []byte(`{"packageManager":"pnpm@`+pnpmVersion+`"}`), 0o600); err != nil {
		t.Fatal(err)
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
	hasWriteGrant := func(path string) bool {
		candidates := []string{"/" + path}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			if parent, parentErr := filepath.EvalSymlinks(filepath.Dir(path)); parentErr == nil {
				resolved = filepath.Join(parent, filepath.Base(path))
			}
		}
		if resolved != "" && resolved != path {
			candidates = append(candidates, "/"+resolved)
		}
		for _, candidate := range candidates {
			if slices.Contains(filesystem.AllowWrite, candidate) {
				return true
			}
		}
		return false
	}
	checkoutReads := []string{"/" + checkout}
	if resolved, err := filepath.EvalSymlinks(checkout); err == nil && resolved != checkout {
		checkoutReads = append(checkoutReads, "/"+resolved)
	}
	checkoutReadable := false
	for _, path := range checkoutReads {
		checkoutReadable = checkoutReadable || slices.Contains(filesystem.AllowRead, path)
	}
	if !slices.Contains(filesystem.DenyRead, "//Users") || !checkoutReadable {
		t.Fatalf("read boundary = %+v", filesystem)
	}
	for _, path := range []string{filepath.Join(root, ".review-home"), filepath.Join(root, ".review-tmp")} {
		if !hasWriteGrant(path) {
			t.Fatalf("missing write grant for %s: %+v", path, filesystem.AllowWrite)
		}
	}
	cargoHome := filepath.Join(operatorHome, ".cargo")
	hasDenyWrite := func(path string) bool {
		candidates := []string{"/" + path}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			if parent, parentErr := filepath.EvalSymlinks(filepath.Dir(path)); parentErr == nil {
				resolved = filepath.Join(parent, filepath.Base(path))
			}
		}
		if resolved != "" && resolved != path {
			candidates = append(candidates, "/"+resolved)
		}
		for _, candidate := range candidates {
			if slices.Contains(filesystem.DenyWrite, candidate) {
				return true
			}
		}
		return false
	}
	for _, path := range []string{checkout, "/opt/toolchain", goModuleCachePath(operatorHome), pnpmStorePath(operatorHome), filepath.Join(operatorHome, ".rustup"), filepath.Join(cargoHome, "registry"), filepath.Join(cargoHome, "git")} {
		if !hasDenyWrite(path) {
			t.Fatalf("missing deny-write grant for %s: %+v", path, filesystem.DenyWrite)
		}
	}
	defaultTemp := "/private/tmp/claude-" + strconv.Itoa(os.Getuid())
	if slices.Contains(filesystem.AllowRead, "/"+defaultTemp) {
		t.Fatalf("reviewer can read shared Claude temp: %+v", filesystem)
	}
	if !slices.Contains(filesystem.DenyWrite, "/"+defaultTemp) {
		t.Fatalf("missing deny-write grant for Claude default temp: %+v", filesystem.DenyWrite)
	}
	if got, err := os.ReadFile(filepath.Join(filepath.Dir(account), "review-tmp")); err != nil || string(got) != filepath.Join(root, ".review-tmp")+"\n"+cargoHome {
		t.Fatalf("CLAUDE_CODE_TMPDIR = %q, err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".review-home", ".cache", "corepack", "v1", "pnpm", pnpmVersion, "package.json")); err != nil {
		t.Fatalf("review Corepack cache was not prepared: %v", err)
	}
	readRulePresent := func(rule string) bool {
		if slices.Contains(allow, rule) || !strings.HasPrefix(rule, "Read(") {
			return slices.Contains(allow, rule)
		}
		path := strings.TrimSuffix(strings.TrimPrefix(rule, "Read("), "/**)")
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			if parent, parentErr := filepath.EvalSymlinks(filepath.Dir(path)); parentErr == nil {
				resolved = filepath.Join(parent, filepath.Base(path))
			}
		}
		return resolved != "" && slices.Contains(allow, "Read(/"+resolved+"/**)")
	}
	for _, rule := range []string{"Bash", "Read(/" + checkout + "/**)", "Read(//opt/toolchain/**)", "Read(//System/Library/OpenSSL/openssl.cnf/**)", "Read(/" + goModuleCachePath(operatorHome) + "/**)", "Read(/" + pnpmStorePath(operatorHome) + "/**)", "Read(/" + filepath.Join(operatorHome, ".rustup") + "/**)"} {
		if !readRulePresent(rule) {
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
