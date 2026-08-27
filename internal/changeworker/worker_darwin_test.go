//go:build darwin

package changeworker_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/changeworker"
	"github.com/dark-factory-build/dark-factory/internal/daemon"
	"github.com/dark-factory-build/dark-factory/internal/runner"
	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 2 {
		var err error
		switch os.Args[1] {
		case "--exec-gate":
			err = runner.RunExecGate()
		case "--attempt-runner":
			err = runner.RunAttemptRunner()
		case "--change-worker-shell":
			err = changeworker.RunShell(context.Background())
		default:
			os.Exit(m.Run())
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(70)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestRegisteredShellWorkerCompletesExactFourReleaseSequence(t *testing.T) {
	fixture := newWorkerFixture(t)
	inner := fixture.start(t)

	if err := fixture.controller.Release(runner.StagePreparation); !errors.Is(err, runner.ErrState) {
		t.Fatalf("out-of-order release: %v", err)
	}
	selectionEvent := fixture.release(t, runner.StageSelection, runner.StageSelection)
	selection, err := changeworker.DecodeSelectionReport(selectionEvent.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if !selection.Repository.Equal(fixture.repositoryIdentity) || selection.EntryCount != 1 || selection.BlobBytes == 0 {
		t.Fatalf("selection=%+v", selection)
	}
	preparationEvent := fixture.release(t, runner.StagePreparation, runner.StagePreparation)
	preparation, err := changeworker.DecodePreparationReport(preparationEvent.Payload)
	if err != nil {
		t.Fatal(err)
	}
	populationEvent := fixture.release(t, runner.StagePopulation, runner.StagePopulation)
	population, err := changeworker.DecodePopulationReport(populationEvent.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if !population.Identity.Equal(preparation.Stage) || !population.Commitment.Equal(selection.Commitment) || population.EntryCount != selection.EntryCount || population.BlobBytes != selection.BlobBytes {
		t.Fatal("checkpoint facts diverged")
	}
	observation := runner.ObserveProcess(inner)
	if observation.Presence != runner.Present || len(observation.Members) != 0 {
		t.Fatalf("Git descendant survived population report: %+v", observation)
	}
	if _, err := os.Stat(fixture.witness); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("provider effect before provider release")
	}
	if err := fixture.controller.Release(runner.StageProvider); err != nil {
		t.Fatal(err)
	}
	record := fixture.finish(t)
	if record.Terminal.Process != inner || record.Terminal.Exit.Code != 0 || record.Terminal.Exit.Signal != 0 {
		t.Fatalf("terminal=%+v inner=%+v", record.Terminal, inner)
	}
	if body, err := os.ReadFile(fixture.witness); err != nil || string(body) != "x" {
		t.Fatalf("startup witness=%q err=%v", body, err)
	}
	changePath := filepath.Join(fixture.changeParent, fixture.finalName)
	if body, err := os.ReadFile(fixture.cwdWitness); err != nil || strings.TrimSpace(string(body)) != changePath {
		t.Fatalf("cwd=%q err=%v", body, err)
	}
	environment, err := os.ReadFile(fixture.envWitness)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"SSH_AUTH_SOCK=", "GITHUB_TOKEN=", "DARK_FACTORY_OPERATOR_TOKEN", fixture.repositoryRoot} {
		if strings.Contains(string(environment), forbidden) {
			t.Fatalf("ambient/source authority leaked in environment")
		}
	}
	if diagnostic := fixture.output(); strings.Contains(diagnostic, fixture.repositoryRoot) || strings.Contains(diagnostic, "printf x") {
		t.Fatalf("private source/input leaked: %q", diagnostic)
	}
}

func TestControllerEOFAfterSelectionLeavesNoLaterEffect(t *testing.T) {
	fixture := newWorkerFixture(t)
	fixture.start(t)
	fixture.release(t, runner.StageSelection, runner.StageSelection)
	if err := fixture.controller.Close(); err != nil {
		t.Fatal(err)
	}
	exit, err := fixture.child.FinishAfterExit(8 * time.Second)
	if err != nil {
		t.Fatalf("outer did not join after controller EOF: %v diag=%q", err, fixture.output())
	}
	if exit.Code == 0 && exit.Signal == 0 {
		t.Fatalf("controller EOF accepted: %+v", exit)
	}
	for _, path := range []string{filepath.Join(fixture.changeParent, fixture.finalName), filepath.Join(fixture.changeParent, ".stage"), fixture.witness} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("later effect %q after EOF: %v", filepath.Base(path), err)
		}
	}
}

func TestCompleteWorkerLeavesExactFDAndGoroutineCensus(t *testing.T) {
	beforeFD := fdCensus(t)
	beforeGoroutines := runtime.NumGoroutine()
	fixture := newWorkerFixture(t)
	fixture.start(t)
	fixture.release(t, runner.StageSelection, runner.StageSelection)
	fixture.release(t, runner.StagePreparation, runner.StagePreparation)
	fixture.release(t, runner.StagePopulation, runner.StagePopulation)
	if err := fixture.controller.Release(runner.StageProvider); err != nil {
		t.Fatal(err)
	}
	fixture.finish(t)
	fixture.close()
	if after := fdCensus(t); !reflect.DeepEqual(after, beforeFD) {
		t.Fatalf("FD census changed: before=%v after=%v", beforeFD, after)
	}
	if after := runtime.NumGoroutine(); after != beforeGoroutines {
		t.Fatalf("goroutine census changed: before=%d after=%d", beforeGoroutines, after)
	}
}

func TestInitialRuntimeChildValidationPrecedesSelectionEffects(t *testing.T) {
	for _, name := range []string{"config", "home", "tmp", "token"} {
		t.Run(name, func(t *testing.T) {
			before := fdCensus(t)
			fixture := newWorkerFixture(t)
			fixture.start(t)
			binding, err := fixture.runtime.Binding()
			if err != nil {
				t.Fatal(err)
			}
			var path string
			switch name {
			case "config":
				path, err = binding.WorkerConfigPath()
			case "home":
				path, err = binding.ProviderHome()
			case "tmp":
				path, err = binding.ProviderTemp()
			case "token":
				path, err = binding.AttemptTokenPath()
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := fixture.controller.Release(runner.StageSelection); err != nil {
				t.Fatal(err)
			}
			event, err := fixture.controller.Next(8 * time.Second)
			if !errors.Is(err, io.EOF) || event.Kind != "" || event.Stage != "" || event.Identity.Valid() || len(event.Payload) != 0 || event.Terminal != nil {
				t.Fatalf("event=%+v err=%v diag=%q", event, err, fixture.output())
			}
			if exit, err := fixture.child.FinishAfterExit(8 * time.Second); err != nil || exit.Code == 0 && exit.Signal == 0 {
				t.Fatalf("outer=%+v err=%v", exit, err)
			}
			for _, effect := range []string{filepath.Join(fixture.changeParent, fixture.finalName), filepath.Join(fixture.changeParent, ".stage"), fixture.witness} {
				if _, err := os.Lstat(effect); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("pre-validation effect %q: %v", effect, err)
				}
			}
			fixture.close()
			if after := fdCensus(t); !reflect.DeepEqual(after, before) {
				t.Fatalf("%s FD census changed: before=%v after=%v", name, before, after)
			}
		})
	}
}

func TestFinalReinspectionRejectsLateGitMetadata(t *testing.T) {
	fixture := newWorkerFixture(t)
	fixture.start(t)
	fixture.release(t, runner.StageSelection, runner.StageSelection)
	fixture.release(t, runner.StagePreparation, runner.StagePreparation)
	fixture.release(t, runner.StagePopulation, runner.StagePopulation)
	published := filepath.Join(fixture.changeParent, fixture.finalName)
	if err := os.Mkdir(filepath.Join(published, ".GiT"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := fixture.controller.Release(runner.StageProvider); err != nil {
		t.Fatal(err)
	}
	record := fixture.finish(t, false)
	if diagnostic := fixture.output(); !strings.Contains(diagnostic, "invalid Change input: .git path components are forbidden") {
		t.Fatalf("missing exact late-metadata rejection: %q", diagnostic)
	}
	if record.Terminal.Exit.Code == 0 && record.Terminal.Exit.Signal == 0 {
		t.Fatalf("late forbidden metadata reached provider: %+v", record.Terminal)
	}
	if _, err := os.Stat(fixture.witness); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("provider witness exists after failed final scan")
	}
}

func TestFinalReinspectionRejectsSameSizeContentMutation(t *testing.T) {
	fixture := newWorkerFixture(t)
	fixture.start(t)
	fixture.release(t, runner.StageSelection, runner.StageSelection)
	fixture.release(t, runner.StagePreparation, runner.StagePreparation)
	fixture.release(t, runner.StagePopulation, runner.StagePopulation)
	payload := filepath.Join(fixture.changeParent, fixture.finalName, "payload.txt")
	if err := os.WriteFile(payload, []byte("wrong bytes!\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fixture.controller.Release(runner.StageProvider); err != nil {
		t.Fatal(err)
	}
	record := fixture.finish(t, false)
	if diagnostic := fixture.output(); !strings.Contains(diagnostic, "invalid Change input: published Change facts changed") {
		t.Fatalf("missing exact content rejection: %q", diagnostic)
	}
	if record.Terminal.Exit.Code == 0 && record.Terminal.Exit.Signal == 0 {
		t.Fatalf("mutated content reached provider: %+v", record.Terminal)
	}
	if _, err := os.Stat(fixture.witness); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("provider witness exists after changed content")
	}
}

func TestFinalRuntimeRecheckRejectsFixedChildReplacement(t *testing.T) {
	fixture := newWorkerFixture(t)
	fixture.start(t)
	fixture.release(t, runner.StageSelection, runner.StageSelection)
	fixture.release(t, runner.StagePreparation, runner.StagePreparation)
	fixture.release(t, runner.StagePopulation, runner.StagePopulation)
	binding, err := fixture.runtime.Binding()
	if err != nil {
		t.Fatal(err)
	}
	home, err := binding.ProviderHome()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(home, home+".retained"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := fixture.controller.Release(runner.StageProvider); err != nil {
		t.Fatal(err)
	}
	record := fixture.finish(t, false)
	if diagnostic := fixture.output(); !strings.Contains(diagnostic, "runtime authority verification: Change worker failed") {
		t.Fatalf("missing exact runtime rejection: %q", diagnostic)
	}
	if record.Terminal.Exit.Code == 0 && record.Terminal.Exit.Signal == 0 {
		t.Fatalf("replacement HOME reached provider: %+v", record.Terminal)
	}
	if _, err := os.Stat(fixture.witness); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("provider witness exists after HOME replacement")
	}
}

type workerFixture struct {
	root, repositoryRoot, changeParent, finalName string
	witness, cwdWitness, envWitness               string
	repositoryIdentity                            change.RepositoryIdentity
	runtime                                       *daemon.Runtime
	parent                                        *daemon.RuntimeParent
	dir, lifetime                                 *os.File
	lease                                         *runner.GateLease
	controller                                    *runner.AttemptController
	child                                         *runner.OwnedChild
	diagnostic                                    *os.File
	closed                                        bool
}

func newWorkerFixture(t *testing.T) *workerFixture {
	t.Helper()
	root := workerSecureTempDir(t)
	git := nativeGit(t)
	repository := filepath.Join(root, "repository")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, git, "init", repository)
	runGit(t, git, "-C", repository, "config", "user.email", "test@example.invalid")
	runGit(t, git, "-C", repository, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(repository, "payload.txt"), []byte("exact source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, git, "-C", repository, "add", "payload.txt")
	runGit(t, git, "-C", repository, "commit", "-m", "base")
	stat, err := os.Stat(repository)
	if err != nil {
		t.Fatal(err)
	}
	sys := stat.Sys().(*syscall.Stat_t)
	repositoryID, err := change.NewRepositoryIdentity(uint64(sys.Dev), sys.Ino)
	if err != nil {
		t.Fatal(err)
	}
	changeParent := filepath.Join(root, "changes")
	if err := os.Mkdir(changeParent, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimeParentPath := filepath.Join(root, "runtimes")
	if err := os.Mkdir(runtimeParentPath, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimeParentDir, err := os.Open(runtimeParentPath)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := daemon.CreateRuntimeParent(runtimeParentDir)
	_ = runtimeParentDir.Close()
	if err != nil {
		t.Fatal(err)
	}
	runtimeValue, err := daemon.CreateRuntime(parent, "attempt")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := runtimeValue.Binding()
	if err != nil {
		t.Fatal(err)
	}
	runtimePath, runtimeID, err := binding.Values()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeValue.PublishAttemptToken(context.Background(), [32]byte{1}); err != nil {
		t.Fatal(err)
	}
	witness, cwdWitness, envWitness := filepath.Join(root, "provider.witness"), filepath.Join(root, "provider.cwd"), filepath.Join(root, "provider.env")
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	// A PTY has no implicit EOF after the initial handoff. The fixture selects
	// the complete byte sequence explicitly, including exit, rather than
	// relying on the worker to mutate it with a hidden newline or close.
	program := fmt.Sprintf("set -eu\nfor n in 3 9; do test ! -e /dev/fd/$n; done\ntest -e /dev/fd/10\ngit rev-parse --is-inside-work-tree >/dev/null 2>&1 && exit 81 || :\nprintf x > %s\npwd > %s\nenv | sort > %s\nexit\n", quote(witness), quote(cwdWitness), quote(envWitness))
	config := changeworker.Config{RuntimePath: runtimePath, RuntimeIdentity: runtimeID, GitExecutable: git, RepositoryRoot: repository, RepositoryIdentity: repositoryID, Revision: "HEAD", ChangeParent: changeParent, FinalName: "published", StagingName: ".stage", AttemptSocket: "/private/tmp/dark-factory-worker-api.sock", InitialTerminalInput: []byte(program)}
	if _, err := runtimeValue.PublishWorkerConfig(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	dir, lifetime, err := runtimeValue.DuplicateRunnerFiles()
	if err != nil {
		t.Fatal(err)
	}
	lease, _, err := runner.CreateGateLease(dir, lifetime, runner.OuterActivationMarkerName)
	if err != nil {
		t.Fatal(err)
	}
	controller, childCapability, err := runner.NewAttemptController()
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home, err := binding.ProviderHome()
	if err != nil {
		t.Fatal(err)
	}
	wrapper, err := runner.PrepareExecSpec(runner.ExecSpec{Target: executable, Args: []string{"--change-worker-shell"}, Env: []string{"PATH=/usr/bin:/bin", "LANG=C"}, Cwd: home})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Configure(runner.AttemptSpec{AttemptID: "attempt", Wrapper: wrapper, MarkerName: runner.InnerActivationMarkerName, TerminalName: runner.TerminalSpoolName}); err != nil {
		t.Fatal(err)
	}
	diagnostic, err := os.OpenFile(filepath.Join(root, "diagnostic"), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	outer, err := runner.PrepareExecSpec(runner.ExecSpec{Target: executable, Args: []string{"--attempt-runner"}, Env: []string{"PATH=/usr/bin:/bin", "LANG=C"}, Cwd: home, Stdout: diagnostic, Stderr: diagnostic, Control: childCapability})
	if err != nil {
		t.Fatal(err)
	}
	child, err := runner.StartBlocked(lease, executable, outer, true)
	_ = childCapability.Close()
	if err != nil {
		t.Fatal(err)
	}
	f := &workerFixture{root: root, repositoryRoot: repository, changeParent: changeParent, finalName: "published", witness: witness, cwdWitness: cwdWitness, envWitness: envWitness, repositoryIdentity: repositoryID, runtime: runtimeValue, parent: parent, dir: dir, lifetime: lifetime, lease: lease, controller: controller, child: child, diagnostic: diagnostic}
	t.Cleanup(f.close)
	return f
}

func (f *workerFixture) close() {
	if f == nil || f.closed {
		return
	}
	f.closed = true
	_ = f.controller.Close()
	_ = f.child.Close()
	_ = f.lease.Close()
	_ = f.lifetime.Close()
	_ = f.dir.Close()
	_ = f.runtime.Close()
	_ = f.parent.Close()
	_ = f.diagnostic.Close()
}

func (f *workerFixture) start(t *testing.T) runner.Identity {
	t.Helper()
	if _, err := f.child.Activate(); err != nil {
		t.Fatal(err)
	}
	event, err := f.controller.Next(6 * time.Second)
	if err != nil || event.Kind != runner.AttemptInnerReady {
		t.Fatalf("ready=%+v err=%v diag=%q", event, err, f.output())
	}
	return event.Identity
}
func (f *workerFixture) release(t *testing.T, release, report runner.AttemptStage) runner.AttemptEvent {
	t.Helper()
	if err := f.controller.Release(release); err != nil {
		t.Fatal(err)
	}
	event, err := f.controller.Next(8 * time.Second)
	if err != nil || event.Kind != runner.AttemptCheckpoint || event.Stage != report {
		t.Fatalf("event=%+v err=%v diag=%q", event, err, f.output())
	}
	return event
}
func (f *workerFixture) finish(t *testing.T, expectedSuccess ...bool) *runner.TerminalRecord {
	t.Helper()
	wantSuccess := true
	if len(expectedSuccess) != 0 {
		wantSuccess = expectedSuccess[0]
	}
	var event runner.AttemptEvent
	var err error
	for {
		event, err = f.controller.Next(8 * time.Second)
		if err != nil || event.Kind != runner.AttemptTerminalFrame {
			break
		}
	}
	if err != nil || event.Kind != runner.AttemptTerminal || event.Terminal == nil {
		t.Fatalf("terminal=%+v err=%v diag=%q", event, err, f.output())
	}
	if err := f.controller.AcknowledgeTerminal(event.Terminal, true); err != nil {
		t.Fatal(err)
	}
	exit, err := f.child.FinishAfterExit(8 * time.Second)
	if err != nil || wantSuccess && exit.Code != 0 || !wantSuccess && exit.Code == 0 && exit.Signal == 0 {
		t.Fatalf("outer exit=%+v err=%v diag=%q", exit, err, f.output())
	}
	return event.Terminal
}
func (f *workerFixture) output() string {
	_ = f.diagnostic.Sync()
	_, _ = f.diagnostic.Seek(0, 0)
	body, _ := io.ReadAll(f.diagnostic)
	_, _ = f.diagnostic.Seek(0, 2)
	return string(body)
}

func nativeGit(t testing.TB) string {
	t.Helper()
	command := exec.Command("/usr/bin/xcrun", "--find", "git")
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + workerSecureTempDir(t), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	body, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(body))
}
func runGit(t testing.TB, git string, args ...string) {
	t.Helper()
	command := exec.Command(git, args...)
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + workerSecureTempDir(t), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"}
	if body, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git failed: %v (%s)", err, body)
	}
}

func workerSecureTempDir(t testing.TB) string {
	t.Helper()
	path, err := os.MkdirTemp("/private/tmp", "dark-factory-worker-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	return path
}

func fdCensus(t testing.TB) map[int][2]uint64 {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[int][2]uint64)
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		var stat unix.Stat_t
		if unix.Fstat(fd, &stat) == nil {
			result[fd] = [2]uint64{uint64(stat.Dev), stat.Ino}
		}
	}
	return result
}
