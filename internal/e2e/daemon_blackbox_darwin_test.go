//go:build darwin

package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

const blackBoxGit = "/Library/Developer/CommandLineTools/usr/bin/git"

// happyPathBody is the shell provider task: it must declare its own outcome,
// exactly as a real provider session does.
const happyPathBody = `set -eu
printf 'built by the factory\n'
"$DARK_FACTORY_FACTORYCTL" attempt succeed --result 'built by the factory'
`

// syncBuffer collects a live child's combined output while the test reads it.
type syncBuffer struct {
	mu    sync.Mutex
	value strings.Builder
}

func (buffer *syncBuffer) Write(payload []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.value.Write(payload)
}

func (buffer *syncBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.value.String()
}

type blackBoxFixture struct {
	root       string
	home       string
	repo       string
	factoryd   string
	factoryctl string
}

// TestBlackBoxDaemonLifecycle drives the real installed-shape binaries: a
// factoryctl-initialized temporary home, a real factoryd process, operator
// subcommands over the real socket, one shell task to a succeeded terminal
// record, and the SIGKILL crash cuts the recovery matrix proves black-box.
func TestBlackBoxDaemonLifecycle(t *testing.T) {
	if os.Getenv("DARK_FACTORY_DAEMON_E2E") != "1" {
		t.Skip("run through scripts/go-daemon-e2e.sh")
	}
	fixture := newBlackBoxFixture(t)

	// Boot A: create the operator surface with dispatch off, then SIGKILL
	// with the first task enqueued and never admitted (crash cut a).
	daemonA, outputA := fixture.startFactoryd(t)
	client := fixture.waitClient(t)
	projectID := fixture.operatorID(t, fixture.runFactoryctl(t, 0, "project", "create", "--name", "black-box", "--root", fixture.repo))
	agentID := fixture.operatorID(t, fixture.runFactoryctl(t, 0, "agent", "create", "--project", projectID, "--name", "builder", "--tool-budget", "4"))
	firstTask := fixture.operatorID(t, fixture.runFactoryctl(t, 0, "task", "add", "--project", projectID, "--agent", agentID, "--title", "prove the happy path", "--body", happyPathBody))
	if status := fixture.taskStatus(t, client, firstTask); status != "queued" {
		t.Fatalf("task before dispatch = %q", status)
	}
	fixture.sigkill(t, daemonA)
	_ = outputA

	// Boot B: the queued task survived the kill; dispatch drives it through
	// a real attempt to the succeeded terminal record.
	daemonB, outputB := fixture.startFactoryd(t)
	client = fixture.waitClient(t)
	if sweep := outputB.String(); strings.Contains(sweep, "recovered run") {
		t.Fatalf("boot after pre-admission kill recovered a run: %q", sweep)
	}
	fixture.runFactoryctl(t, 0, "dispatch", "on")
	fixture.awaitTaskStatus(t, client, firstTask, "succeeded", 90*time.Second)
	changes, err := os.ReadDir(install.ChangesPath(fixture.home))
	if err != nil || len(changes) == 0 {
		t.Fatalf("published changes after success = %v, %v", changes, err)
	}

	// Crash cut b: SIGKILL factoryd while the second task's attempt is live.
	// The orphaned attempt loses the factory API, so its worker fails and
	// the runner publishes a failure result nobody consumes.
	secondTask := fixture.operatorID(t, fixture.runFactoryctl(t, 0, "task", "add", "--project", projectID, "--agent", agentID, "--title", "prove the crash cut", "--body", "sleep 6"))
	fixture.awaitTaskStatus(t, client, secondTask, "running", 30*time.Second)
	// The running status flips at admission; the kill must land while the
	// provider is genuinely live, which the durable inner activation marker
	// proves black-box.
	fixture.awaitRuntimeFile(t, "inner.activate", 30*time.Second)
	fixture.sigkill(t, daemonB)
	fixture.awaitOrphanArtifact(t, 60*time.Second)

	// Boot C: the sweep consumes the published result before any listener
	// opens and settles the abandoned run to a terminal failed task.
	daemonC, outputC := fixture.startFactoryd(t)
	client = fixture.waitClient(t)
	fixture.awaitTaskStatus(t, client, secondTask, "failed", 30*time.Second)
	if sweep := outputC.String(); !strings.Contains(sweep, "result-consumed") {
		t.Fatalf("boot after mid-attempt kill did not consume the result: %q", sweep)
	}

	// A second factoryd against the live home refuses without disturbing it.
	refusedOutput := &strings.Builder{}
	refused := exec.Command(fixture.factoryd, "--home", fixture.home)
	refused.Stdout, refused.Stderr = refusedOutput, refusedOutput
	if err := refused.Start(); err != nil {
		t.Fatal(err)
	}
	if err := awaitProcessExit(refused, 15*time.Second); err != nil {
		t.Fatalf("second factoryd did not exit: %v (output %q)", err, refusedOutput.String())
	}
	if refused.ProcessState.ExitCode() == 0 {
		t.Fatalf("second factoryd claimed the live home: %q", refusedOutput.String())
	}
	if status := fixture.taskStatus(t, client, firstTask); status != "succeeded" {
		t.Fatalf("first task after refused double boot = %q", status)
	}

	// Teardown census: SIGTERM converges, the socket and home release, and
	// no runtime child or factoryd process survives.
	if err := daemonC.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := awaitProcessExit(daemonC, 30*time.Second); err != nil {
		t.Fatalf("factoryd did not converge on SIGTERM: %v (output %q)", err, outputC.String())
	}
	if daemonC.ProcessState.ExitCode() != 0 {
		t.Fatalf("factoryd SIGTERM exit = %d (output %q)", daemonC.ProcessState.ExitCode(), outputC.String())
	}
	if _, err := os.Lstat(install.LocalAPISocketPath(fixture.home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket remains after shutdown: %v", err)
	}
	entries, err := os.ReadDir(install.RuntimesPath(fixture.home))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			t.Fatalf("runtime child %q survived the lifecycle", entry.Name())
		}
	}
	reopened, err := install.OpenOperationalHome(context.Background(), fixture.home)
	if err != nil {
		t.Fatalf("home was not released: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func newBlackBoxFixture(t *testing.T) *blackBoxFixture {
	t.Helper()
	if !change.TrustedDeveloperGitPath(blackBoxGit) {
		t.Fatalf("git path %q is not the trusted toolchain shape", blackBoxGit)
	}
	if _, err := os.Stat(blackBoxGit); err != nil {
		t.Fatalf("the black-box gate requires the CommandLineTools git: %v", err)
	}
	factoryd := requiredExecutable(t, "DARK_FACTORY_E2E_FACTORYD")
	factoryctl := requiredExecutable(t, "DARK_FACTORY_E2E_FACTORYCTL")
	requiredExecutable(t, "DARK_FACTORY_E2E_RUNNER")
	root, err := os.MkdirTemp("/private/tmp", "dark-factory-daemon-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture := &blackBoxFixture{root: root, home: filepath.Join(root, "factory"), repo: filepath.Join(root, "repo"), factoryd: factoryd, factoryctl: factoryctl}
	if err := os.Mkdir(fixture.repo, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{
		{"init", "--quiet", "--initial-branch=main"},
		{"-c", "user.name=black-box", "-c", "user.email=black-box@invalid", "commit", "--quiet", "--allow-empty", "--message", "seed"},
	} {
		command := exec.Command(blackBoxGit, arguments...)
		command.Dir = fixture.repo
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", arguments, err, output)
		}
	}
	fixture.runFactoryctl(t, 0, "init", "--home", fixture.home)
	return fixture
}

func (fixture *blackBoxFixture) startFactoryd(t *testing.T) (*exec.Cmd, *syncBuffer) {
	t.Helper()
	output := &syncBuffer{}
	command := exec.Command(fixture.factoryd, "--home", fixture.home)
	command.Stdout, command.Stderr = output, output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	return command, output
}

func (fixture *blackBoxFixture) waitClient(t *testing.T) *api.OperatorClient {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	socket := install.LocalAPISocketPath(fixture.home)
	token := filepath.Join(fixture.home, "operator.token")
	for time.Now().Before(deadline) {
		client, err := api.NewOperatorClient(socket, token)
		if err == nil {
			callContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			health, healthErr := client.Health(callContext)
			cancel()
			if healthErr == nil && health.Ready {
				return client
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("factoryd local API did not become ready")
	return nil
}

func (fixture *blackBoxFixture) runFactoryctl(t *testing.T, wantExit int, arguments ...string) string {
	t.Helper()
	command := exec.Command(fixture.factoryctl, arguments...)
	command.Env = []string{
		"DARK_FACTORY_SOCKET=" + install.LocalAPISocketPath(fixture.home),
		"DARK_FACTORY_OPERATOR_TOKEN_FILE=" + filepath.Join(fixture.home, "operator.token"),
	}
	output, err := command.CombinedOutput()
	exit := 0
	if err != nil {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) {
			t.Fatalf("factoryctl %v: %v (%s)", arguments, err, output)
		}
		exit = exitError.ExitCode()
	}
	if exit != wantExit {
		t.Fatalf("factoryctl %v exit = %d, want %d (%s)", arguments, exit, wantExit, output)
	}
	return string(output)
}

func (fixture *blackBoxFixture) operatorID(t *testing.T, output string) string {
	t.Helper()
	var payload struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil || len(payload.ID) != 32 {
		t.Fatalf("operator output %q: %v", output, err)
	}
	return payload.ID
}

func (fixture *blackBoxFixture) taskStatus(t *testing.T, client *api.OperatorClient, taskID string) string {
	t.Helper()
	callContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	snapshot, err := client.Snapshot(callContext)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range snapshot.Tasks {
		if task.ID == taskID {
			return task.Status
		}
	}
	t.Fatalf("task %s is not in the snapshot", taskID)
	return ""
}

func (fixture *blackBoxFixture) awaitTaskStatus(t *testing.T, client *api.OperatorClient, taskID, want string, patience time.Duration) {
	t.Helper()
	deadline := time.Now().Add(patience)
	status := ""
	for time.Now().Before(deadline) {
		status = fixture.taskStatus(t, client, taskID)
		if status == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("task %s status = %q, want %q", taskID, status, want)
}

// awaitOrphanArtifact waits for the orphaned attempt tree to converge after
// factoryd was killed: the runner publishes its result artifact with nobody
// left to consume it.
func (fixture *blackBoxFixture) awaitOrphanArtifact(t *testing.T, patience time.Duration) {
	t.Helper()
	if !fixture.awaitRuntimeFilePresence(t, runner.AttemptResultSpoolName, patience) {
		t.Fatal("the orphaned attempt never published its result")
	}
}

func (fixture *blackBoxFixture) awaitRuntimeFile(t *testing.T, name string, patience time.Duration) {
	t.Helper()
	if !fixture.awaitRuntimeFilePresence(t, name, patience) {
		t.Fatalf("runtime file %q never appeared", name)
	}
}

func (fixture *blackBoxFixture) awaitRuntimeFilePresence(t *testing.T, name string, patience time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(patience)
	runtimes := install.RuntimesPath(fixture.home)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(runtimes)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if _, err := os.Lstat(filepath.Join(runtimes, entry.Name(), name)); err == nil {
				return true
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func (fixture *blackBoxFixture) sigkill(t *testing.T, command *exec.Cmd) {
	t.Helper()
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := awaitProcessExit(command, 10*time.Second); err != nil {
		t.Fatal(err)
	}
}

func awaitProcessExit(command *exec.Cmd, patience time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		var exitError *exec.ExitError
		if err == nil || errors.As(err, &exitError) {
			return nil
		}
		return err
	case <-time.After(patience):
		_ = command.Process.Kill()
		return errors.New("process did not exit in time")
	}
}
