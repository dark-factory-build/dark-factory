package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/review"
)

// gateCommand is the operator's full gate, run at the exact commit outside
// any worker sandbox, as the operator. It is factory configuration, not PR
// input, but it resolves inside the checked-out commit, so the commit's own
// copy runs, as the host gate does today (#1106 open question 2).
const gateCommand = "scripts/local-ci.sh"

// gateTimeout is a package-test seam.
var gateTimeout = 30 * time.Minute

const gateLogLimit = 16 << 20

var gateFailureLine = regexp.MustCompile(`(?m)^\s*(?:--- FAIL|FAIL|ERROR): (\S+)`)

// ConfigureGate sets the home and tool path the gate runs with, and kills any
// gate process group a stopped daemon left running. Call before review
// recovery re-gates.
func (daemon *Daemon) ConfigureGate(home, toolPath string) {
	daemon.gateHome, daemon.gateToolPath = home, toolPath
	files, _ := filepath.Glob(filepath.Join(install.RuntimesPath(home), "gates", "*", "*.pgid"))
	for _, file := range files {
		// ponytail: a group id recycled after a host reboot could be signalled;
		// record the leader's start time if that ever matters.
		if data, err := os.ReadFile(file); err == nil {
			if pgid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pgid > 1 {
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			}
		}
		_ = os.Remove(file)
	}
}

// runGate runs gateCommand once at commit in a detached worktree of the
// review checkout. An error means nothing conclusive ran: the operation stays
// retryable and nothing is sent back.
func (daemon *Daemon) runGate(ctx context.Context, checkout, operationID, commit string, n int) (review.GateRun, error) {
	if daemon.gateHome == "" || operationID == "" || filepath.Base(operationID) != operationID || strings.HasPrefix(operationID, ".") {
		return review.GateRun{}, errors.New("gate is not configured")
	}
	// ponytail: one gate at a time per factory; per-repository slots if gates queue.
	daemon.gateMu.Lock()
	defer daemon.gateMu.Unlock()
	directory := filepath.Join(install.RuntimesPath(daemon.gateHome), "gates", operationID)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return review.GateRun{}, err
	}
	root, err := os.MkdirTemp("", "dark-factory-gate-")
	if err != nil {
		return review.GateRun{}, err
	}
	defer os.RemoveAll(root)
	tree := filepath.Join(root, "tree")
	if err := runReviewGit(ctx, root, "-C", checkout, "worktree", "add", "--quiet", "--detach", tree, commit); err != nil {
		return review.GateRun{}, fmt.Errorf("gate worktree: %w", err)
	}
	defer runReviewGit(context.WithoutCancel(ctx), root, "-C", checkout, "worktree", "remove", "--force", tree)
	logPath := filepath.Join(directory, strconv.Itoa(n)+".log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return review.GateRun{}, err
	}
	defer log.Close()
	runCtx, cancel := context.WithTimeout(ctx, gateTimeout)
	defer cancel()
	command := exec.CommandContext(runCtx, filepath.Join(tree, gateCommand))
	command.Dir = tree
	command.Env = []string{"PATH=" + daemon.gateToolPath}
	for _, value := range os.Environ() {
		// Allowlisted, so no GitHub or provider token reaches the gate.
		if slices.Contains([]string{"HOME", "USER", "LOGNAME", "SHELL", "TMPDIR", "LANG", "LC_ALL", "LC_CTYPE", "TERM"}, strings.SplitN(value, "=", 2)[0]) {
			command.Env = append(command.Env, value)
		}
	}
	output := &cappedWriter{file: log, left: gateLogLimit}
	command.Stdout, command.Stderr = output, output
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
	command.WaitDelay = 5 * time.Second
	if err := command.Start(); err != nil {
		return review.GateRun{}, err
	}
	pgid := command.Process.Pid
	pgidPath := filepath.Join(directory, strconv.Itoa(n)+".pgid")
	_ = os.WriteFile(pgidPath, []byte(strconv.Itoa(pgid)), 0600)
	_ = command.Wait()
	// The gate passes only once its whole group is gone, so a leaked
	// descendant cannot hide behind a successful parent exit.
	leaked := runCtx.Err() == nil && syscall.Kill(-pgid, 0) == nil
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	for wait := 0; syscall.Kill(-pgid, 0) != syscall.ESRCH; wait++ {
		if wait == 100 {
			return review.GateRun{}, errors.New("gate process group survived SIGKILL")
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = os.Remove(pgidPath)
	if ctx.Err() != nil {
		return review.GateRun{}, ctx.Err()
	}
	code := command.ProcessState.ExitCode()
	if status, ok := command.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		code = 128 + int(status.Signal())
	}
	if runCtx.Err() != nil {
		code = 124
	}
	// The wrapper statuses go_gate_run_bounded reserves: nothing ran.
	if code == 64 || (code >= 125 && code <= 127) {
		return review.GateRun{}, fmt.Errorf("gate exit %d, nothing ran; log %s", code, logPath)
	}
	run := review.GateRun{Commit: commit, ExitCode: code, Log: logPath}
	if data, err := os.ReadFile(logPath); err == nil {
		for _, match := range gateFailureLine.FindAllStringSubmatch(string(data), -1) {
			if !slices.Contains(run.Failed, match[1]) && len(run.Failed) < 16 {
				run.Failed = append(run.Failed, match[1])
			}
		}
	}
	if leaked {
		run.Failed = append(run.Failed, "gate left processes running")
		if run.ExitCode == 0 {
			run.ExitCode = 1
		}
	}
	return run, nil
}

// cappedWriter keeps the first bytes of a gate log and drops the rest, so a
// runaway gate cannot fill the disk.
type cappedWriter struct {
	file *os.File
	left int
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if keep := min(len(p), w.left); keep > 0 {
		if _, err := w.file.Write(p[:keep]); err != nil {
			return 0, err
		}
		w.left -= keep
	}
	return len(p), nil
}

func runReviewGit(ctx context.Context, root string, args ...string) error {
	command := exec.CommandContext(ctx, "/usr/bin/git", args...)
	command.Env = reviewEnvironment(root)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
