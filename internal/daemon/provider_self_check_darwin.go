//go:build darwin

package daemon

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

const (
	providerSelfCheckFile   = ".dark-factory-provider-self-check"
	providerSelfCheckCommit = "dark-factory provider self-check"
)

// ProviderSelfCheckResult is the one bounded result a later adoption caller
// needs. RunIsolated owns the provider, Change, runner, PTY and MCP lifecycle;
// this layer only maps its terminal result and verifies the resulting commit.
type ProviderSelfCheckResult struct {
	Passed bool
	Detail string
	Commit string
}

// RunProviderSelfCheck exercises one native candidate without touching the
// daemon scheduler or provider-adoption state. RunIsolated selects the short
// private temporary path and removes its throwaway home and Change on return.
func RunProviderSelfCheck(ctx context.Context, spec SupervisorSpec, repository string, provider kernel.Provider) ProviderSelfCheckResult {
	if provider != kernel.ProviderClaudeCode && provider != kernel.ProviderCodex {
		return ProviderSelfCheckResult{Detail: "provider self-check requires a native provider"}
	}
	task := `Run this provider self-check exactly. Read payload.txt from the checkout and verify it contains exact source. Create exactly one file named ` + providerSelfCheckFile + ` containing exactly "provider self-check\n". Stage and commit only that file with commit message ` + providerSelfCheckCommit + ` and git -c commit.gpgSign=false. Then use the configured factory_attempt MCP tool to call attempt succeed with result "provider-self-check". Do not call factoryctl from the shell, do not modify any other file, and do not claim success if any step fails.`
	run, err := RunIsolated(ctx, spec, repository, provider, task)
	if err != nil {
		return ProviderSelfCheckResult{Detail: boundedSelfCheckDetail(err.Error())}
	}
	if run.Phase != kernel.RunTerminal || run.Terminal == nil || run.Terminal.Kind() != kernel.OutcomeSucceeded || run.Terminal.Result() != "provider-self-check" {
		return ProviderSelfCheckResult{Detail: selfCheckTerminalDetail(run)}
	}
	commit, err := verifyProviderSelfCheckCommit(ctx, spec.GitExecutable, repository, spec.BaseRevision)
	if err != nil {
		return ProviderSelfCheckResult{Detail: boundedSelfCheckDetail(err.Error())}
	}
	return ProviderSelfCheckResult{Passed: true, Detail: "provider self-check passed", Commit: commit}
}

func verifyProviderSelfCheckCommit(ctx context.Context, git, repository, base string) (string, error) {
	if git == "" || repository == "" || base == "" {
		return "", errors.New("provider self-check: invalid repository contract")
	}
	objects, err := selfCheckGitOutput(ctx, git, "-C", repository, "fsck", "--no-reflogs", "--unreachable", "--no-progress")
	if err != nil {
		return "", fmt.Errorf("provider self-check object lookup: %w", err)
	}
	var commit string
	for _, line := range strings.Split(strings.TrimSpace(objects), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "unreachable" && fields[1] == "commit" {
			subject, showErr := selfCheckGitOutput(ctx, git, "-C", repository, "show", "-s", "--format=%s", fields[2])
			if showErr == nil && strings.TrimSpace(subject) == providerSelfCheckCommit {
				if commit != "" {
					return "", errors.New("provider self-check: multiple self-check commits")
				}
				commit = fields[2]
			}
		}
	}
	if commit == "" {
		return "", errors.New("provider self-check: bounded commit not found")
	}
	names, err := selfCheckGitOutput(ctx, git, "-C", repository, "diff-tree", "--no-commit-id", "--name-only", "-r", commit)
	if err != nil {
		return "", fmt.Errorf("provider self-check: commit file lookup: %w", err)
	}
	if strings.TrimSpace(names) != providerSelfCheckFile {
		return "", errors.New("provider self-check: commit changed more than the bounded file")
	}
	body, err := selfCheckGitOutput(ctx, git, "-C", repository, "show", commit+":"+providerSelfCheckFile)
	if err != nil {
		return "", fmt.Errorf("provider self-check: bounded file lookup: %w", err)
	}
	if body != "provider self-check\n" {
		return "", errors.New("provider self-check: bounded file content mismatch")
	}
	baseCommit, err := selfCheckGitOutput(ctx, git, "-C", repository, "rev-parse", "--verify", base+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("provider self-check: base lookup: %w", err)
	}
	if strings.TrimSpace(baseCommit) == commit {
		return "", errors.New("provider self-check: commit did not advance the base")
	}
	return commit, nil
}

func selfCheckGitOutput(ctx context.Context, git string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, git, args...)
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func selfCheckTerminalDetail(run kernel.Run) string {
	if run.Terminal == nil {
		return "provider self-check: isolated run did not reach a terminal result"
	}
	if run.Terminal.Kind() == kernel.OutcomeFailed {
		return boundedSelfCheckDetail(run.Terminal.Code().String() + ": " + run.Terminal.Detail())
	}
	return boundedSelfCheckDetail("provider self-check: unexpected terminal outcome " + run.Terminal.Kind().String())
}

func boundedSelfCheckDetail(detail string) string {
	if len(detail) > 4096 {
		return detail[:4096]
	}
	return detail
}
