//go:build darwin || linux

package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/review"
)

// gateFixtureRepo commits a fake gate at a base and then at a head commit.
func gateFixtureRepo(t *testing.T, baseGate, headGate string) (string, string, string) {
	t.Helper()
	repo := t.TempDir()
	git := func(args ...string) string {
		command := exec.Command("/usr/bin/git", append([]string{"-C", repo, "-c", "user.name=fixture", "-c", "user.email=fixture@invalid"}, args...)...)
		command.Env = reviewEnvironment(t.TempDir())
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "--quiet")
	commit := func(script string) string {
		if err := os.MkdirAll(filepath.Join(repo, "scripts"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, gateCommand), []byte("#!/bin/sh\n"+script+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
		git("add", "--all")
		git("commit", "--quiet", "--allow-empty", "-m", "fixture")
		return git("rev-parse", "HEAD")
	}
	base := commit(baseGate)
	return repo, base, commit(headGate)
}

type fixtureGateBackend struct {
	publicReviewBackend
	daemon   *Daemon
	checkout string
}

func (b *fixtureGateBackend) CloneReadOnly(context.Context, review.Request) (string, func(), error) {
	return b.checkout, func() {}, nil
}

func (b *fixtureGateBackend) Gate(ctx context.Context, checkout string, op review.Operation, commit string) (review.GateRun, error) {
	return b.daemon.runGate(ctx, checkout, op.ID, commit, len(op.Gates)+1)
}

var gateHome string

func startGateFixture(t *testing.T, baseGate, headGate string) (review.Operation, *fixtureGateBackend, error) {
	t.Helper()
	gateHome = t.TempDir()
	fixture, project := reviewPublicFixture(t)
	fixture.daemon.ConfigureGate(gateHome, "/usr/bin:/bin")
	repo, base, head := gateFixtureRepo(t, baseGate, headGate)
	backend := &fixtureGateBackend{daemon: fixture.daemon, checkout: repo}
	now := func() time.Time { return time.Unix(20, 0) }
	coordinator := review.Coordinator{Store: durableReviewStore{store: fixture.store, project: project, repository: "team/repo", now: now}, Backend: backend, Now: now}
	op, err := coordinator.Start(context.Background(), review.Request{Repository: "team/repo", PullNumber: 21, Head: head, Base: base, BaseRef: "main", Body: "fixture body", Provider: "codex"})
	return op, backend, err
}

func TestGateSendsBackHeadFailureAbsentAtBase(t *testing.T) {
	t.Setenv("GH_TOKEN", "secret")
	op, backend, err := startGateFixture(t, "exit 0", "echo token=${GH_TOKEN-unset}\necho '--- FAIL: TestBroken (0.01s)'\necho '    --- FAIL: TestBroken/case'\nexit 1")
	if err != nil || op.State != "completed" || op.Verdict != "request_changes" || !op.RoutePending || backend.reviews != 0 || len(op.Gates) != 3 || op.Gates[2].ExitCode != 0 {
		t.Fatalf("operation=%+v err=%v", op, err)
	}
	// The note reaches the worker and may be published, so it never names
	// a host path: the absolute log path stays in the operation document.
	userHome, _ := os.UserHomeDir()
	if !strings.Contains(op.Detail, "tests=TestBroken, TestBroken/case") || strings.Contains(op.Detail, " /") || strings.Contains(op.Detail, gateHome) || (userHome != "" && strings.Contains(op.Detail, userHome)) || !filepath.IsAbs(op.Gates[0].Log) {
		t.Fatalf("note=%q", op.Detail)
	}
	log, err := os.ReadFile(op.Gates[0].Log)
	if err != nil || !strings.Contains(string(log), "token=unset") {
		t.Fatalf("gate log=%q err=%v", log, err)
	}
}

func TestGateWrapperExitIsRetryableHostBlocker(t *testing.T) {
	op, backend, err := startGateFixture(t, "exit 0", "exit 125")
	if err == nil || op.State != "failed" || !op.Retryable || op.Verdict != "" || op.RoutePending || backend.reviews != 0 || len(op.Gates) != 0 {
		t.Fatalf("operation=%+v err=%v", op, err)
	}
}

func TestGateTimeoutKillsProcessGroup(t *testing.T) {
	previous := gateTimeout
	gateTimeout = 2 * time.Second
	t.Cleanup(func() { gateTimeout = previous })
	pidFile := filepath.Join(t.TempDir(), "child")
	repo, _, head := gateFixtureRepo(t, "exit 0", "/bin/sleep 60 &\necho $! > "+pidFile+"\nexec /bin/sleep 60")
	daemon := &Daemon{}
	daemon.ConfigureGate(t.TempDir(), "/usr/bin:/bin")
	started := time.Now()
	run, err := daemon.runGate(context.Background(), repo, "op", head, 1)
	// Under timeout + WaitDelay: the timeout killed the group, not just the leader.
	if err != nil || run.ExitCode != 124 || time.Since(started) > 6*time.Second {
		t.Fatalf("run=%+v err=%v after %s", run, err, time.Since(started))
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || syscall.Kill(pid, 0) != syscall.ESRCH {
		t.Fatalf("background gate child %d survived: %v", pid, err)
	}
}

func TestGatePassLeavingProcessesRunningFails(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child")
	repo, _, head := gateFixtureRepo(t, "exit 0", "/bin/sleep 60 >/dev/null 2>&1 &\necho $! > "+pidFile+"\nexit 0")
	daemon := &Daemon{}
	daemon.ConfigureGate(t.TempDir(), "/usr/bin:/bin")
	run, err := daemon.runGate(context.Background(), repo, "op", head, 1)
	if err != nil || run.ExitCode == 0 || !slices.Contains(run.Failed, "gate left processes running") {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err != nil || syscall.Kill(pid, 0) != syscall.ESRCH {
		t.Fatalf("leaked gate child %d survived: %v", pid, err)
	}
}

func TestRestartDuringGatingKillsStaleGroupAndRegates(t *testing.T) {
	fixture, project := reviewPublicFixture(t)
	repo, base, head := gateFixtureRepo(t, "exit 0", "exit 0")
	op, err := review.Prepare(review.Request{Repository: "team/repo", PullNumber: 22, Head: head, Base: base, BaseRef: "main", Body: "fixture body", Provider: "codex"}, func() time.Time { return time.Unix(30, 0) })
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.RecordReviewOperation(context.Background(), project, "team/repo", op.ID, op, mustKernelTime(t, 30)); err != nil {
		t.Fatal(err)
	}
	// The stopped daemon's gate is still running in its own group.
	home := t.TempDir()
	stale := exec.Command("/bin/sleep", "60")
	stale.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := stale.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- stale.Wait() }()
	directory := filepath.Join(install.RuntimesPath(home), "gates", op.ID)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "1.pgid"), []byte(strconv.Itoa(stale.Process.Pid)), 0600); err != nil {
		t.Fatal(err)
	}

	restarted, err := newDaemon(fixture.store, func() time.Time { return time.Unix(31, 0) })
	if err != nil {
		t.Fatal(err)
	}
	backend := &fixtureGateBackend{daemon: restarted, checkout: repo}
	restarted.reviewBackend = func(string, uint64) review.Backend { return backend }
	restarted.ConfigureGate(home, "/usr/bin:/bin")
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		_ = stale.Process.Kill()
		t.Fatal("stale gate process group survived restart")
	}
	if count, err := restarted.RecoverReviewOperations(context.Background()); err != nil || count != 1 {
		t.Fatalf("recovery count=%d err=%v", count, err)
	}
	deadline := time.Now().Add(20 * time.Second)
	recovered := lastDurableReview(t, fixture.store, project)
	for recovered.State != "enqueued" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		recovered = lastDurableReview(t, fixture.store, project)
	}
	if recovered.ID != op.ID || recovered.State != "enqueued" || len(recovered.Gates) != 1 || recovered.Gates[0].ExitCode != 0 || recovered.Gates[0].Commit != head {
		t.Fatalf("recovered operation=%+v", recovered)
	}
}
