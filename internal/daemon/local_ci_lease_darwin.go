package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// Resolve the registered repository once on the host. Workers receive only the
// owned lease subtree; refs, objects, hooks and configuration remain outside it.
func prepareLocalCILeaseDirectory(ctx context.Context, gitExecutable, repository string) (string, error) {
	command := exec.CommandContext(ctx, gitExecutable, "-C", repository, "rev-parse", "--path-format=absolute", "--git-common-dir")
	command.Env = []string{"HOME=/var/empty", "PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0"}
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("resolve local CI lease repository: %w", err)
	}
	common, err := filepath.EvalSymlinks(strings.TrimSpace(string(output)))
	if err != nil {
		return "", fmt.Errorf("invalid local CI lease repository: %w", err)
	}
	if !filepath.IsAbs(common) {
		return "", errors.New("local CI lease repository is not absolute")
	}
	if _, err := os.Lstat(filepath.Join(common, ".dark-factory-local-ci")); !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("drain and clean the legacy local CI lease before enabling the dedicated lease directory")
	}
	// The old helper rejects a symlink lock object. Install this barrier with
	// no replacement, atomically excluding an old helper's competing mkdir.
	legacyLock := filepath.Join(common, ".dark-factory-local-ci.lock")
	const barrier = "dark-factory-local-ci/.dark-factory-local-ci.lock"
	if target, err := os.Readlink(legacyLock); err != nil || target != barrier {
		if err := os.Symlink(barrier, legacyLock); err != nil {
			return "", fmt.Errorf("legacy local CI lease has not drained: %w", err)
		}
	}
	directory := filepath.Join(common, "dark-factory-local-ci")
	if err := os.Mkdir(directory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != 0700 || !ok || stat.Uid != uint32(os.Geteuid()) {
		return "", errors.New("local CI lease directory is not owned and protected")
	}
	return directory, nil
}
