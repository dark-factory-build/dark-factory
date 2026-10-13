package provider

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

var providerTerminalCSI = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)

// reviewLanes holds a one-slot lane per account home: the review using it.
var reviewLanes sync.Map

// ReviewResult is the provider-owned outcome of one read-only review launch.
// The daemon remains responsible for interpreting the provider's verdict.
type ReviewResult struct {
	Stdout  string
	Stderr  string
	Limited bool
}

// ReviewEnvironment is the same filtered, disposable environment used by the
// worker path. In particular, provider credentials and host Git credentials do
// not cross the daemon/provider boundary.
func ReviewEnvironment(root string) []string {
	home := filepath.Join(root, ".review-home")
	tmp := filepath.Join(root, ".review-tmp")
	_ = os.MkdirAll(home, 0700)
	_ = os.MkdirAll(tmp, 0700)
	allowed := map[string]bool{"PATH": true, "LANG": true, "LC_ALL": true, "LC_CTYPE": true, "TERM": true, "USER": true, "LOGNAME": true, "SHELL": true}
	result := make([]string, 0, 12)
	for _, value := range os.Environ() {
		if allowed[strings.SplitN(value, "=", 2)[0]] {
			result = append(result, value)
		}
	}
	return append(result,
		"HOME="+home, "TMPDIR="+tmp,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=/usr/bin/false", "SSH_ASKPASS=/usr/bin/false",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=",
	)
}

// RunReview launches the installed provider with the review's read-only
// policy. Provider selection, account selection, environment construction,
// process-group lifetime, and usage-limit handling live here with ordinary
// provider launches; callers do not assemble provider command lines. The
// Reviewers read the toolchain roots and trusted caches, offline, so they can
// run focused Go, Rust, or web tests; builds write only disposable roots.
func RunReview(ctx context.Context, kind kernel.Provider, toolPath, toolchainRoots, operatorHome string, accountHomes []string, checkout, prompt string, readable []string) (ReviewResult, error) {
	toolName := "codex"
	if kind == kernel.ProviderClaudeCode {
		toolName = "claude"
	}
	tool, err := WalkToolPath(toolPath, toolName)
	if err != nil {
		return ReviewResult{}, err
	}
	root := filepath.Dir(checkout)
	home, tmp := filepath.Join(root, ".review-home"), filepath.Join(root, ".review-tmp")
	golang := []string{"PATH=" + toolPath, "GOENV=off", "GOCACHE=" + filepath.Join(home, "go-build"), "GOPATH=" + filepath.Join(home, "go"), "GOFLAGS=-mod=readonly", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "CGO_ENABLED=0", "CARGO_HOME=" + filepath.Join(home, ".cargo"), "COREPACK_HOME=" + filepath.Join(home, ".cache", "corepack"), "COREPACK_ENABLE_NETWORK=0", "npm_config_cache=" + filepath.Join(home, ".cache", "npm"), "XDG_CACHE_HOME=" + filepath.Join(home, ".cache"), "CLAUDE_CODE_TMPDIR=" + tmp}
	readable = append(readable, filepath.SplitList(toolchainRoots)...)
	readable = append(readable, "/System/Library/OpenSSL/openssl.cnf")
	if operatorHome != "" {
		if _, err := os.Stat(filepath.Join(checkout, "web", "package.json")); err == nil {
			if err := prepareCorepackPnpm(operatorHome, home); err != nil {
				return ReviewResult{}, err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return ReviewResult{}, err
		}
		cargoHome := filepath.Join(operatorHome, ".cargo")
		readable = append(readable, goModuleCachePath(operatorHome), pnpmStorePath(operatorHome), filepath.Join(operatorHome, ".rustup"), filepath.Join(cargoHome, "registry"), filepath.Join(cargoHome, "git"))
		golang = append(golang, "GOMODCACHE="+goModuleCachePath(operatorHome), "RUSTUP_HOME="+filepath.Join(operatorHome, ".rustup"), "CARGO_HOME="+cargoHome, "CARGO_NET_OFFLINE=true", "CARGO_TARGET_DIR="+tmp, "pnpm_config_store_dir="+pnpmStorePath(operatorHome))
	}
	// Claude's tools get the same grants:
	// untrusted task, pull request, diff and dispute text reach the prompt.
	var settings string
	if kind == kernel.ProviderClaudeCode {
		if settings, err = ClaudeReview([]string{home, tmp}, readable...); err != nil {
			return ReviewResult{}, err
		}
	}
	for _, account := range accountHomes {
		environment := append(append(ReviewEnvironment(root), AccountEnvironment(kind, filepath.Dir(account), account)...), golang...)
		var argv []string
		if kind == kernel.ProviderClaudeCode {
			argv = []string{"-p", prompt, "--permission-mode", "dontAsk", "--safe-mode", "--restricted", "--setting-sources", "", "--strict-mcp-config", "--settings", settings, "--tools", "Read,Grep,Glob,Bash"}
		} else {
			argv = []string{"exec", "--disable", "computer_use", "--disable", "browser_use", "--disable", "plugins", "--ephemeral", "--ignore-user-config", "--strict-config", "-c", "approval_policy={ granular={sandbox_approval=false,rules=false,mcp_elicitations=false,request_permissions=false,skill_approval=false}}", "--ignore-rules", "--skip-git-repo-check"}
			argv = append(argv, CodexReview([]string{home, tmp}, readable...)...)
			argv = append(argv, prompt)
		}
		command := exec.CommandContext(ctx, tool, argv...)
		command.Dir, command.Env = checkout, environment
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
		command.WaitDelay = 5 * time.Second
		var stdout, stderr strings.Builder
		command.Stdout, command.Stderr = &stdout, &stderr
		// One review per account at a time (#1893): reviews started together
		// on one login fail with exit status 1, one at a time they pass. The
		// wait counts toward the caller's deadline. ponytail: a queue longer
		// than that deadline fails its tail retryably; order the queue in
		// factoryd if that shows.
		lane, _ := reviewLanes.LoadOrStore(account, make(chan struct{}, 1))
		select {
		case lane.(chan struct{}) <- struct{}{}:
		case <-ctx.Done():
			return ReviewResult{}, ctx.Err()
		}
		err := command.Run()
		<-lane.(chan struct{})
		result := ReviewResult{Stdout: stdout.String(), Stderr: stderr.String()}
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if err != nil && kind == kernel.ProviderCodex && ReviewUsageLimited(result.Stdout+result.Stderr) {
			result.Limited = true
			continue
		}
		if err != nil {
			return result, err
		}
		return result, nil
	}
	return ReviewResult{Limited: true}, errors.New("provider_limited")
}

// ReviewUsageLimited mirrors the worker's marker-aware Codex detector. The
// glyph is part of the provider error cell: quoted task or tool text without
// it is ordinary reviewer output, not account exhaustion.
func ReviewUsageLimited(output string) bool {
	text := providerTerminalCSI.ReplaceAllString(output, "")
	return strings.Contains(text, "■ You've hit your usage limit") || strings.Contains(text, "⚠ Selected model is at capacity")
}
