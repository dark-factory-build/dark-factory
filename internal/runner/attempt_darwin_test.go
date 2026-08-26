//go:build darwin

package runner

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func runAttemptWorkerHelper(args []string) error {
	if len(args) < 2 {
		return errors.New("attempt worker: missing mode/root")
	}
	mode, root := args[0], args[1]
	control, err := OpenWorkerControl()
	if err != nil {
		return err
	}
	defer control.Close()
	write := func(name, value string) error {
		f, err := os.OpenFile(filepath.Join(root, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(f, value); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	}
	if err := write("selection", fmt.Sprintf("%d", control.Identity().PID)); err != nil {
		return err
	}
	if err := control.ReportSelection([]byte("selected")); err != nil {
		return err
	}
	if err := control.AwaitPreparation(); err != nil {
		return err
	}
	if err := write("preparation", "prepared"); err != nil {
		return err
	}
	if err := control.ReportPreparation([]byte("prepared")); err != nil {
		return err
	}
	if err := control.AwaitPopulation(); err != nil {
		return err
	}
	if err := write("population", "populated"); err != nil {
		return err
	}
	var provider ExecSpec
	switch mode {
	case "shell", "term", "leader":
		script := fmt.Sprintf("test -z \"${HOME+x}\" || exit 90; test -z \"${DARK_FACTORY_ATTEMPT_TOKEN+x}\" || exit 91; for n in 3 4 5 6 7 8 9 10 11; do test ! -e /dev/fd/$n || exit 92; done; printf '%%s' $$ > %q; while test ! -f %q; do sleep 0.01; done; printf x >> %q; cat > %q", filepath.Join(root, "provider.pid"), filepath.Join(root, "continue"), filepath.Join(root, "provider.effect"), filepath.Join(root, "provider.stdin"))
		if mode == "term" {
			script = fmt.Sprintf("trap '' TERM; sleep 30 & printf '%%s' $! > %q; printf '%%s' $$ > %q; while :; do sleep 1; done", filepath.Join(root, "descendant.pid"), filepath.Join(root, "provider.pid"))
		}
		if mode == "leader" {
			script = fmt.Sprintf("sleep 30 & printf '%%s' $! > %q; printf '%%s' $$ > %q; exit 0", filepath.Join(root, "descendant.pid"), filepath.Join(root, "provider.pid"))
		}
		provider = ExecSpec{Target: "/bin/sh", Args: []string{"-c", script}, Env: []string{"PATH=/usr/bin:/bin", "LANG=C"}, Cwd: filepath.Join(root, "work"), Stdin: []byte("one-startup")}
	case "binary", "seam":
		if len(args) != 3 {
			return errors.New("attempt worker: missing binary target")
		}
		provider = ExecSpec{Target: args[2], Args: []string{"--attempt-provider", filepath.Join(root, "provider.effect")}, Env: []string{"PATH=/usr/bin:/bin", "LANG=C"}, Cwd: filepath.Join(root, "work")}
	default:
		return fmt.Errorf("attempt worker: invalid mode %q", mode)
	}
	prepared, err := PrepareExecSpec(provider)
	if err != nil {
		return err
	}
	if mode == "seam" {
		prepared.testCurrentFinal = true
	}
	if err := control.ReportPopulation([]byte("populated")); err != nil {
		return err
	}
	if err := control.AwaitProvider(); err != nil {
		return err
	}
	return control.ExecProvider(prepared)
}

type attemptFixture struct {
	t          *testing.T
	root       string
	dir        *os.File
	lease      *GateLease
	controller *AttemptController
	spec       AttemptSpec
	childCap   *os.File
	outer      *OwnedChild
	inner      Identity
	diagnostic *os.File
}

func newAttemptFixture(t *testing.T, mode string, target string) *attemptFixture {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "work"), 0o700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	lease, _, err := CreateGateLease(dir, "outer.activate")
	if err != nil {
		t.Fatal(err)
	}
	controller, childCap, err := NewAttemptController()
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	workerArgs := []string{"--attempt-worker", mode, root}
	if target != "" {
		workerArgs = append(workerArgs, target)
	}
	wrapper, err := PrepareExecSpec(ExecSpec{Target: executable, Args: workerArgs, Env: []string{"PATH=/usr/bin:/bin", "LANG=C"}, Cwd: filepath.Join(root, "work")})
	if err != nil {
		t.Fatal(err)
	}
	attemptSpec := AttemptSpec{AttemptID: "attempt-1", Wrapper: wrapper, MarkerName: "inner.activate", TerminalName: "terminal.json"}
	diagnostic := outputFile(t, filepath.Join(root, "runner.output"))
	outerSpec, err := PrepareExecSpec(ExecSpec{Target: executable, Args: []string{"--attempt-runner"}, Env: []string{"PATH=/usr/bin:/bin", "LANG=C"}, Cwd: filepath.Join(root, "work"), Stdout: diagnostic, Stderr: diagnostic, Control: childCap})
	if err != nil {
		t.Fatal(err)
	}
	outer, err := StartBlocked(lease, executable, outerSpec, true)
	if err != nil {
		t.Fatalf("outer StartBlocked: %v", err)
	}
	_ = childCap.Close()
	f := &attemptFixture{t: t, root: root, dir: dir, lease: lease, controller: controller, spec: attemptSpec, outer: outer, diagnostic: diagnostic}
	t.Cleanup(func() {
		_ = controller.Close()
		if f.inner.Valid() {
			if got, identityErr := readIdentity(f.inner.PID); identityErr == nil && got == f.inner {
				_ = unix.Kill(-f.inner.PGID, unix.SIGKILL)
			}
		}
		_ = outer.Close()
		_ = lease.Close()
		_ = dir.Close()
	})
	return f
}

func (f *attemptFixture) activateOuter() Identity {
	f.t.Helper()
	if _, err := os.Stat(filepath.Join(f.root, "selection")); !errors.Is(err, os.ErrNotExist) {
		f.t.Fatal("wrapper effect before outer activation")
	}
	if _, err := f.outer.Activate(); err != nil {
		f.t.Fatal(err)
	}
	if err := f.controller.Configure(f.spec); err != nil {
		f.t.Fatal(err)
	}
	event, err := f.controller.Next(4 * time.Second)
	if err != nil || event.Kind != AttemptInnerReady || !event.Identity.Valid() {
		f.t.Fatalf("inner ready=%+v err=%v output=%q", event, err, f.output())
	}
	f.inner = event.Identity
	if _, err := os.Stat(filepath.Join(f.root, "selection")); !errors.Is(err, os.ErrNotExist) {
		f.t.Fatal("wrapper effect before selection release")
	}
	return event.Identity
}

func (f *attemptFixture) advanceToProvider() {
	f.t.Helper()
	steps := []struct {
		release AttemptStage
		report  AttemptStage
		witness string
	}{
		{StageSelection, StageSelection, "selection"},
		{StagePreparation, StagePreparation, "preparation"},
		{StagePopulation, StagePopulation, "population"},
	}
	for _, step := range steps {
		if err := f.controller.Release(step.release); err != nil {
			f.t.Fatalf("release %s: %v", step.release, err)
		}
		event, err := f.controller.Next(4 * time.Second)
		if err != nil || event.Kind != AttemptCheckpoint || event.Stage != step.report {
			f.t.Fatalf("checkpoint %s event=%+v err=%v output=%q", step.report, event, err, f.output())
		}
		if _, err := os.Stat(filepath.Join(f.root, step.witness)); err != nil {
			f.t.Fatalf("missing %s witness: %v", step.witness, err)
		}
	}
}

func (f *attemptFixture) finishAndAck() *TerminalRecord {
	f.t.Helper()
	event, err := f.controller.Next(6 * time.Second)
	if err != nil || event.Kind != AttemptTerminal || event.Terminal == nil {
		f.t.Fatalf("terminal event=%+v err=%v output=%q", event, err, f.output())
	}
	record := event.Terminal
	loaded, err := LoadTerminal(f.dir, "terminal.json")
	if err != nil || loaded.Digest != record.Digest || loaded.Identity != record.Identity {
		f.t.Fatalf("durable terminal loaded=%+v event=%+v err=%v", loaded, record, err)
	}
	if got := ObserveProcess(f.inner); got.Presence != Absent {
		f.t.Fatalf("terminal published before sole Wait: %+v", got)
	}
	if err := f.controller.AcknowledgeTerminal(record, false); !errors.Is(err, ErrState) {
		f.t.Fatalf("terminal acknowledged before Store commit: %v", err)
	}
	if retained, err := LoadTerminal(f.dir, "terminal.json"); err != nil || retained.Digest != record.Digest {
		f.t.Fatalf("uncommitted ack removed spool: record=%+v err=%v", retained, err)
	}
	if err := f.controller.AcknowledgeTerminal(record, true); err != nil {
		f.t.Fatal(err)
	}
	exit, err := f.outer.FinishAfterExit(6 * time.Second)
	if err != nil || exit.Code != 0 {
		f.t.Fatalf("outer exit=%+v err=%v output=%q", exit, err, f.output())
	}
	if _, err := os.Stat(filepath.Join(f.root, "terminal.json")); !errors.Is(err, os.ErrNotExist) {
		f.t.Fatalf("terminal remains after exact ack: %v", err)
	}
	return record
}

func (f *attemptFixture) output() string {
	_ = f.diagnostic.Sync()
	_, _ = f.diagnostic.Seek(0, 0)
	body, _ := io.ReadAll(f.diagnostic)
	_, _ = f.diagnostic.Seek(0, 2)
	return string(body)
}

func TestAttemptRunnerOrdersOuterWrapperAndShellProvider(t *testing.T) {
	f := newAttemptFixture(t, "shell", "")
	outer := f.outer.Identity()
	inner := f.activateOuter()
	if inner == outer || inner.PID == outer.PID || inner.PGID == outer.PGID {
		t.Fatalf("ownership boundaries collapsed outer=%+v inner=%+v", outer, inner)
	}
	if err := f.controller.Release(StagePreparation); !errors.Is(err, ErrState) {
		t.Fatalf("out-of-order release accepted: %v", err)
	}
	f.advanceToProvider()
	if err := f.controller.Release(StagePopulation); !errors.Is(err, ErrState) {
		t.Fatalf("replayed release accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.root, "provider.effect")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("provider effect before provider release")
	}
	if err := f.controller.Release(StageProvider); err != nil {
		t.Fatal(err)
	}
	waitFile(t, filepath.Join(f.root, "provider.pid"))
	body, err := os.ReadFile(filepath.Join(f.root, "provider.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(body))
	if err != nil || pid != inner.PID {
		t.Fatalf("provider pid=%q err=%v want=%d", body, err, inner.PID)
	}
	if got, err := readIdentity(pid); err != nil || got != inner {
		t.Fatalf("provider identity=%+v err=%v want=%+v", got, err, inner)
	}
	if err := os.WriteFile(filepath.Join(f.root, "continue"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	record := f.finishAndAck()
	if record.Terminal.Process != inner || record.Terminal.Exit.Code != 0 || record.Terminal.Exit.Signal != 0 {
		t.Fatalf("terminal=%+v", record.Terminal)
	}
	if body, err := os.ReadFile(filepath.Join(f.root, "provider.effect")); err != nil || string(body) != "x" {
		t.Fatalf("effect=%q err=%v", body, err)
	}
	if body, err := os.ReadFile(filepath.Join(f.root, "provider.stdin")); err != nil || string(body) != "one-startup" {
		t.Fatalf("stdin=%q err=%v", body, err)
	}
}

func TestAttemptRunnerDaemonEOFCuts(t *testing.T) {
	t.Run("before-provider-release", func(t *testing.T) {
		f := newAttemptFixture(t, "shell", "")
		inner := f.activateOuter()
		if err := f.controller.Close(); err != nil {
			t.Fatal(err)
		}
		exit, err := f.outer.FinishAfterExit(6 * time.Second)
		if err != nil || exit.Code == 0 {
			t.Fatalf("outer exit=%+v err=%v output=%q", exit, err, f.output())
		}
		waitExactAbsence(t, inner)
		if _, err := os.Stat(filepath.Join(f.root, "selection")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("wrapper ran after daemon EOF before selection release")
		}
		record, err := LoadTerminal(f.dir, "terminal.json")
		if err != nil || record.Terminal.Process != inner || !record.Terminal.Exit.Aborted {
			t.Fatalf("terminal=%+v err=%v", record, err)
		}
	})

	t.Run("after-provider-release", func(t *testing.T) {
		f := newAttemptFixture(t, "shell", "")
		inner := f.activateOuter()
		f.advanceToProvider()
		if err := f.controller.Release(StageProvider); err != nil {
			t.Fatal(err)
		}
		if err := f.controller.Close(); err != nil {
			t.Fatal(err)
		}
		waitFile(t, filepath.Join(f.root, "provider.pid"))
		if err := os.WriteFile(filepath.Join(f.root, "continue"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		exit, err := f.outer.FinishAfterExit(6 * time.Second)
		if err != nil || exit.Code != 0 {
			t.Fatalf("outer exit=%+v err=%v output=%q", exit, err, f.output())
		}
		record, err := LoadTerminal(f.dir, "terminal.json")
		if err != nil || record.Terminal.Process != inner || record.Terminal.Exit.Code != 0 {
			t.Fatalf("replay terminal=%+v err=%v", record, err)
		}
		if err := AcknowledgeTerminal(f.dir, "terminal.json", record, true); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("active-wrapper-before-provider-release", func(t *testing.T) {
		f := newAttemptFixture(t, "shell", "")
		inner := f.activateOuter()
		f.advanceToProvider()
		if err := f.controller.Close(); err != nil {
			t.Fatal(err)
		}
		exit, err := f.outer.FinishAfterExit(6 * time.Second)
		if err != nil || exit.Code == 0 {
			t.Fatalf("outer exit=%+v err=%v output=%q", exit, err, f.output())
		}
		waitExactAbsence(t, inner)
		if _, err := os.Stat(filepath.Join(f.root, "provider.effect")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("provider ran after daemon EOF before provider release: %v", err)
		}
		record, err := LoadTerminal(f.dir, "terminal.json")
		if err != nil || record.Terminal.Process != inner {
			t.Fatalf("terminal=%+v err=%v", record, err)
		}
	})
}

func TestAttemptRunnerTerminatesOwnedProviderGroup(t *testing.T) {
	for _, mode := range []string{"term", "leader"} {
		t.Run(mode, func(t *testing.T) {
			f := newAttemptFixture(t, mode, "")
			inner := f.activateOuter()
			f.advanceToProvider()
			if err := f.controller.Release(StageProvider); err != nil {
				t.Fatal(err)
			}
			waitFile(t, filepath.Join(f.root, "descendant.pid"))
			if mode == "term" {
				if err := f.controller.Terminate(); err != nil {
					t.Fatal(err)
				}
			}
			record := f.finishAndAck()
			if record.Terminal.Process != inner {
				t.Fatalf("terminal identity=%+v", record.Terminal.Process)
			}
			if err := unix.Kill(-inner.PGID, 0); !errors.Is(err, unix.ESRCH) {
				t.Fatalf("provider group remains: %v", err)
			}
		})
	}
}

func TestCurrentExecRejectsCommittedTargetChanges(t *testing.T) {
	mutations := []string{"replace", "mode", "bytes", "symlink", "remove"}
	for _, mutation := range mutations {
		t.Run(mutation, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "provider")
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			copyExecutable(t, executable, target)
			f := newAttemptFixture(t, "binary", target)
			f.activateOuter()
			f.advanceToProvider()
			switch mutation {
			case "replace":
				replacement := filepath.Join(root, "replacement")
				copyExecutable(t, executable, replacement)
				if err := os.Rename(replacement, target); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(target, 0o600); err != nil {
					t.Fatal(err)
				}
			case "bytes":
				file, err := os.OpenFile(target, os.O_WRONLY|os.O_APPEND, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.Write([]byte("changed")); err != nil {
					t.Fatal(err)
				}
				_ = file.Close()
			case "remove":
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(executable, target); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.controller.Release(StageProvider); err != nil {
				t.Fatal(err)
			}
			record := f.finishAndAck()
			if record.Terminal.Exit.Code == 0 {
				t.Fatalf("mutated provider reported success: %+v", record.Terminal)
			}
			if _, err := os.Stat(filepath.Join(f.root, "provider.effect")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("mutated provider effect exists: %v", err)
			}
		})
	}
}

func TestCurrentExecDocumentsCooperativeSameUIDRace(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "provider")
	copyExecutable(t, "/usr/bin/false", target)
	f := newAttemptFixture(t, "seam", target)
	f.activateOuter()
	f.advanceToProvider()
	if err := f.controller.Release(StageProvider); err != nil {
		t.Fatal(err)
	}
	event, err := f.controller.Next(4 * time.Second)
	if err != nil || event.Kind != AttemptCheckpoint || event.Stage != StageProvider {
		t.Fatalf("final-check event=%+v err=%v", event, err)
	}
	replacement := filepath.Join(root, "replacement")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	copyExecutable(t, executable, replacement)
	if err := os.Rename(replacement, target); err != nil {
		t.Fatal(err)
	}
	if err := f.controller.acknowledgeCurrentExecCheck(); err != nil {
		t.Fatal(err)
	}
	record := f.finishAndAck()
	if record.Terminal.Exit.Code != 0 {
		t.Fatalf("replacement did not win documented race: %+v", record.Terminal)
	}
	if body, err := os.ReadFile(filepath.Join(f.root, "provider.effect")); err != nil || string(body) != "provider" {
		t.Fatalf("replacement witness=%q err=%v", body, err)
	}
}

func TestAttemptProtocolSurfaceIsClosed(t *testing.T) {
	controller, child, err := NewAttemptController()
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	defer child.Close()
	if err := controller.Release(StageSelection); !errors.Is(err, ErrState) {
		t.Fatalf("release before configuration=%v", err)
	}
	if err := controller.AcknowledgeTerminal(nil, true); !errors.Is(err, ErrState) {
		t.Fatalf("ack without terminal=%v", err)
	}
	if err := controller.AcknowledgeTerminal(&TerminalRecord{}, false); !errors.Is(err, ErrState) {
		t.Fatalf("ack before Store=%v", err)
	}
	source, err := os.ReadFile("process_darwin.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(source), ".Process.Signal(") {
		t.Fatal("numeric per-process signal surface introduced")
	}
	attemptSource, err := os.ReadFile("attempt_darwin.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{".Wait(", "unix.Kill("} {
		if strings.Contains(string(attemptSource), forbidden) {
			t.Fatalf("attempt runner bypasses OwnedChild with %q", forbidden)
		}
	}
	if got := strings.Count(string(attemptSource), "publishAttemptTerminal("); got != 2 {
		t.Fatalf("terminal publication must have one guarded call site, got %d occurrences", got)
	}
	if !strings.Contains(string(attemptSource), "if _, err := child.waitedExit(); err != nil") {
		t.Fatal("terminal publication lost waited-child proof")
	}
	typesSource, err := os.ReadFile("types.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(typesSource), "ExtraFiles ") {
		t.Fatal("generic arbitrary inherited-FD surface introduced")
	}
}

func TestPublishAttemptTerminalRequiresWaitedChild(t *testing.T) {
	f := newFixture(t)
	child := f.start("/bin/sh", []string{"-c", "exit 0"}, nil, nil)
	cfg := attemptConfig{AttemptID: "attempt-guard", TerminalName: "terminal.json"}
	if err := publishAttemptTerminal(child, f.dir, cfg, Exit{Code: 0}, nil, false, nil); !errors.Is(err, ErrState) {
		t.Fatalf("unwaited terminal publication=%v", err)
	}
	if _, err := os.Stat(filepath.Join(f.root, cfg.TerminalName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unwaited terminal left an effect: %v", err)
	}
	if err := child.Abort(); err != nil && child.state != stateWaited {
		t.Fatalf("abort guarded child: %v", err)
	}
}

func TestAttemptCleanupUncertaintyRetainsOwnerBeforeTerminal(t *testing.T) {
	f := newFixture(t)
	child := f.start("/bin/sh", []string{"-c", "exit 0"}, nil, nil)
	if _, err := child.Activate(); err != nil {
		t.Fatal(err)
	}
	cfg := attemptConfig{AttemptID: "attempt-retry", TerminalName: "terminal.json"}
	identity := child.Identity()
	entered := make(chan int, 4)
	release := make(chan struct{})
	released := false
	t.Cleanup(func() {
		if !released {
			close(release)
		}
	})
	var calls atomic.Int32
	child.testConvergence = func() error {
		call := int(calls.Add(1))
		entered <- call
		if call <= 2 {
			<-release
			return fmt.Errorf("%w: injected convergence uncertainty", ErrUnresolved)
		}
		return nil
	}
	done := make(chan error, 1)
	go func() {
		done <- finishAttemptWithExit(child, f.dir, cfg, nil, false, nil)
	}()
	select {
	case call := <-entered:
		if call != 1 {
			t.Fatalf("first convergence call=%d", call)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("cleanup did not reach injected uncertainty")
	}
	if _, err := os.Stat(filepath.Join(f.root, cfg.TerminalName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("uncertain cleanup published terminal: %v", err)
	}
	if got := ObserveProcess(identity); got.Presence != Present {
		t.Fatalf("uncertain cleanup lost exact unreaped owner: %+v", got)
	}
	select {
	case err := <-done:
		t.Fatalf("uncertain cleanup returned early: %v", err)
	default:
	}
	close(release)
	released = true
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("cleanup did not converge after uncertainty cleared")
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("convergence calls=%d want=3", got)
	}
	if child.state != stateWaited {
		t.Fatalf("terminal returned without sole Wait: state=%d", child.state)
	}
	record, err := LoadTerminal(f.dir, cfg.TerminalName)
	if err != nil || record.Terminal.Process != identity {
		t.Fatalf("terminal after convergence=%+v err=%v", record, err)
	}
	waitExactAbsence(t, identity)
	if err := unix.Kill(-identity.PGID, 0); !errors.Is(err, unix.ESRCH) {
		t.Fatalf("owned group remains after terminal: %v", err)
	}
}

func TestAttemptPermanentCleanupUncertaintyPublishesNothing(t *testing.T) {
	f := newFixture(t)
	child := f.start("/bin/sh", []string{"-c", "exit 0"}, nil, nil)
	if _, err := child.Activate(); err != nil {
		t.Fatal(err)
	}
	cfg := attemptConfig{AttemptID: "attempt-unresolved", TerminalName: "terminal.json"}
	identity := child.Identity()
	entered := make(chan struct{}, 16)
	var restored atomic.Bool
	child.testConvergence = func() error {
		entered <- struct{}{}
		if !restored.Load() {
			return fmt.Errorf("%w: permanent injected convergence uncertainty", ErrUnresolved)
		}
		return nil
	}
	done := make(chan error, 1)
	finished := false
	t.Cleanup(func() {
		if finished {
			return
		}
		restored.Store(true)
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Error("cleanup owner did not join after safety restoration")
		}
	})
	go func() {
		done <- finishAttemptWithExit(child, f.dir, cfg, nil, false, nil)
	}()
	for range 3 {
		select {
		case <-entered:
		case err := <-done:
			finished = true
			t.Fatalf("cleanup owner abandoned permanent uncertainty: %v", err)
		case <-time.After(4 * time.Second):
			t.Fatal("cleanup stopped retrying permanent uncertainty")
		}
	}
	if _, err := os.Stat(filepath.Join(f.root, cfg.TerminalName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("permanent uncertainty published terminal: %v", err)
	}
	if got := ObserveProcess(identity); got.Presence != Present {
		t.Fatalf("permanent uncertainty lost exact unreaped owner: %+v", got)
	}
	select {
	case err := <-done:
		t.Fatalf("permanent uncertainty returned early: %v", err)
	default:
	}
	restored.Store(true)
	select {
	case err := <-done:
		finished = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("cleanup did not converge after safety restoration")
	}
	if child.state != stateWaited {
		t.Fatalf("restored cleanup returned before Wait: state=%d", child.state)
	}
	if _, err := LoadTerminal(f.dir, cfg.TerminalName); err != nil {
		t.Fatalf("restored cleanup did not publish terminal: %v", err)
	}
	waitExactAbsence(t, identity)
}

func TestAttemptControllerRejectsForeignTerminalAuthority(t *testing.T) {
	want := Identity{PID: 22, PGID: 22, Birth: Birth{Seconds: 3, Microseconds: 4}}
	for _, terminal := range []Terminal{
		{AttemptID: "other", Process: want, Exit: Exit{Code: 0}},
		{AttemptID: "attempt-1", Process: Identity{PID: 23, PGID: 23, Birth: Birth{Seconds: 3, Microseconds: 4}}, Exit: Exit{Code: 0}},
	} {
		controller, peer, err := NewAttemptController()
		if err != nil {
			t.Fatal(err)
		}
		controller.state = controllerProviderReleased
		controller.attemptID = "attempt-1"
		controller.inner = want
		identity := FileIdentity{Device: 1, Inode: 2}
		if err := writeControlFrame(peer, attemptFrame{Version: 1, Kind: "terminal", Terminal: &terminal, FileIdentity: &identity, Digest: strings.Repeat("0", 64)}, maxConfigBytes); err != nil {
			t.Fatal(err)
		}
		if _, err := controller.Next(time.Second); !errors.Is(err, ErrIdentity) {
			t.Fatalf("foreign terminal accepted: terminal=%+v err=%v", terminal, err)
		}
		_ = peer.Close()
		_ = controller.Close()
	}
}

func TestDirectAttemptRunnerWithoutCapabilitiesFailsBeforeEffect(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := execCommand(executable, "--attempt-runner")
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "runner:") {
		t.Fatalf("direct attempt runner err=%v output=%q", err, output)
	}
}

func copyExecutable(t *testing.T, from, to string) {
	t.Helper()
	source, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(target, source); err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
}

// Kept behind this seam so the direct-invocation test cannot accidentally
// acquire any of the fixture's inherited descriptors.
func execCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Env = []string{}
	return cmd
}
