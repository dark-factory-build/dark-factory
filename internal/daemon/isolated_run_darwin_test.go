//go:build darwin

package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
	"golang.org/x/sys/unix"
)

// RunIsolated drives the production supervisor, runner, Change worker, PTY
// owner and attempt API end to end with fake providers, and leaves no home,
// process or descriptor behind whatever the outcome.
func TestRunIsolatedReturnsOneTypedResultAndCleansUp(t *testing.T) {
	root, err := os.MkdirTemp(runner.ShortTempDir(), "dark-factory-isolated-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	git := supervisorNativeGit(t)
	repository := filepath.Join(root, "repository")
	supervisorGit(t, git, "init", repository)
	supervisorGit(t, git, "-C", repository, "config", "user.email", "test@example.invalid")
	supervisorGit(t, git, "-C", repository, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(repository, "payload.txt"), []byte("exact source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	supervisorGit(t, git, "-C", repository, "add", "payload.txt")
	supervisorGit(t, git, "-C", repository, "commit", "-m", "base")
	base := strings.TrimSpace(supervisorGitOutput(t, git, "-C", repository, "rev-parse", "HEAD"))
	executable := supervisorTestExecutable(t)
	factoryctl, err := filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join(root, "tools")
	if err := os.Mkdir(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	copySupervisorExecutable(t, executable, filepath.Join(tools, "claude"))
	toolPath := filepath.Join(runtime.GOROOT(), "bin") + ":/usr/bin:/bin"
	helper := quoteShell(executable)
	childReceipt, continueReceipt := filepath.Join(root, "child.pid"), filepath.Join(root, "continue")
	if err := os.WriteFile(continueReceipt, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name     string
		provider kernel.Provider
		toolPath string
		task     string
		kind     kernel.OutcomeKind
		result   string
		code     kernel.FailureCode
	}{
		// The provider reads its checkout, then leaves a TERM-immune
		// descendant behind its typed success for cleanup to reap.
		{name: "success", provider: kernel.ProviderShell, toolPath: toolPath, kind: kernel.OutcomeSucceeded, result: "typed-success",
			task: "set -eu\ngrep -qx 'exact source' payload.txt\nDARK_FACTORY_CHILD_RECEIPT=" + quoteShell(childReceipt) + " DARK_FACTORY_CONTINUE_RECEIPT=" + quoteShell(continueReceipt) + " " + helper + " --supervisor-descendant-provider\n"},
		{name: "native provider", provider: kernel.ProviderClaudeCode, toolPath: tools + ":" + toolPath, kind: kernel.OutcomeSucceeded, result: "2",
			task: strings.Repeat("Codify the operator scripts and the deploy order. ", 2)},
		// Far beyond a 64 KiB pipe: an undrained terminal would block the
		// provider before it could report.
		{name: "output larger than pipe capacity", provider: kernel.ProviderShell, toolPath: toolPath, kind: kernel.OutcomeSucceeded, result: "drained",
			task: "set -eu\nyes 0123456789abcdef | head -n 65536\n" + helper + " --supervisor-attempt-succeed drained\n"},
		{name: "launch failure", provider: kernel.ProviderCodex, toolPath: toolPath, kind: kernel.OutcomeFailed, code: kernel.FailureProviderExit, task: "unused"},
		// A forged bearer cannot bootstrap the attempt API, so the provider
		// exits without an outcome.
		{name: "attempt API bootstrap failure", provider: kernel.ProviderShell, toolPath: toolPath, kind: kernel.OutcomeFailed, code: kernel.FailureProviderExit,
			task: "set -eu\numask 077\nprintf %032d 0 > \"$TMPDIR/forged\"\nDARK_FACTORY_ATTEMPT_TOKEN_FILE=\"$TMPDIR/forged\" " + helper + " --supervisor-attempt-succeed forged\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_ = os.Remove(childReceipt)
			baselineFDs := supervisorFDCount(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			run, err := RunIsolated(ctx, SupervisorSpec{
				GitExecutable: git, BaseRevision: base, RunnerExecutable: executable, FactoryctlExecutable: factoryctl,
				ToolPath: test.toolPath, AccountHome: root,
			}, repository, test.provider, test.task)
			if err != nil {
				t.Fatalf("RunIsolated: %v", err)
			}
			if run.Phase != kernel.RunTerminal || run.Terminal == nil || run.Terminal.Kind() != test.kind || run.Provider != test.provider {
				t.Fatalf("isolated run = %+v", run)
			}
			if test.kind == kernel.OutcomeSucceeded && run.Terminal.Result() != test.result || test.kind == kernel.OutcomeFailed && run.Terminal.Code() != test.code {
				t.Fatalf("isolated outcome = %+v", run.Terminal)
			}
			if leftovers, _ := filepath.Glob(filepath.Join(runner.ShortTempDir(), "df-run-*")); len(leftovers) != 0 {
				t.Fatalf("isolated home remains: %v", leftovers)
			}
			if body, err := os.ReadFile(childReceipt); err == nil {
				pid, err := strconv.Atoi(string(body))
				if err != nil {
					t.Fatal(err)
				}
				for deadline := time.Now().Add(4 * time.Second); !errors.Is(unix.Kill(pid, 0), unix.ESRCH); time.Sleep(10 * time.Millisecond) {
					if time.Now().After(deadline) {
						t.Fatalf("provider descendant %d remains", pid)
					}
				}
			} else if test.name == "success" {
				t.Fatalf("provider descendant receipt: %v", err)
			}
			for deadline := time.Now().Add(4 * time.Second); supervisorFDCount(t) != baselineFDs; time.Sleep(10 * time.Millisecond) {
				if time.Now().After(deadline) {
					t.Fatalf("descriptors %d -> %d", baselineFDs, supervisorFDCount(t))
				}
			}
		})
	}
}
