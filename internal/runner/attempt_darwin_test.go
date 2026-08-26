//go:build darwin

package runner

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
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
	providerCwd := filepath.Join(root, "work")
	var provider ExecSpec
	switch mode {
	case "shell", "term", "leader":
		script := fmt.Sprintf("test -z \"${HOME+x}\" || exit 90; test -z \"${DARK_FACTORY_ATTEMPT_TOKEN+x}\" || exit 91; for n in 3 4 5 6 7 8 9 11; do test ! -e /dev/fd/$n || exit 92; done; test -f /dev/fd/10 || exit 93; test ! -s /dev/fd/10 || exit 94; cat /dev/fd/10/change-worker.config >/dev/null 2>&1 && exit 95; cd /dev/fd/10 >/dev/null 2>&1 && exit 96; printf '%%s' $$ > %q; while test ! -f %q; do sleep 0.01; done; printf x >> %q; cat > %q", filepath.Join(root, "provider.pid"), filepath.Join(root, "continue"), filepath.Join(root, "provider.effect"), filepath.Join(root, "provider.stdin"))
		if mode == "term" {
			script = fmt.Sprintf("trap '' TERM; sleep 30 & printf '%%s' $! > %q; printf '%%s' $$ > %q; while :; do sleep 1; done", filepath.Join(root, "descendant.pid"), filepath.Join(root, "provider.pid"))
		}
		if mode == "leader" {
			script = fmt.Sprintf("sleep 30 & printf '%%s' $! > %q; printf '%%s' $$ > %q; exit 0", filepath.Join(root, "descendant.pid"), filepath.Join(root, "provider.pid"))
		}
		provider = ExecSpec{Target: "/bin/sh", Args: []string{"-c", script}, Env: []string{"PATH=/usr/bin:/bin", "LANG=C"}, Cwd: providerCwd, Stdin: []byte("one-startup")}
	case "binary", "seam", "lifetime", "lease-seam", "cwd", "cwd-seam", "cwd-unrelated", "cwd-file", "cwd-closed", "cwd-reused", "cwd-mode":
		if len(args) != 3 {
			return errors.New("attempt worker: missing binary target")
		}
		providerArgs := []string{"--attempt-provider", filepath.Join(root, "provider.effect")}
		if mode == "lifetime" {
			providerArgs = []string{"--lifetime-provider", root}
		}
		if strings.HasPrefix(mode, "cwd") {
			providerCwd = filepath.Join(root, "change", "work")
		}
		if mode == "cwd" || mode == "cwd-seam" {
			providerArgs = []string{"--cwd-provider", root}
		}
		provider = ExecSpec{Target: args[2], Args: providerArgs, Env: []string{"PATH=/usr/bin:/bin", "LANG=C"}, Cwd: providerCwd}
	default:
		return fmt.Errorf("attempt worker: invalid mode %q", mode)
	}
	prepared, err := PrepareExecSpec(provider)
	if err != nil {
		return err
	}
	if mode == "seam" || mode == "lease-seam" || mode == "cwd-seam" {
		prepared.testCurrentFinal = true
	}
	cwdPath := providerCwd
	if mode == "cwd-unrelated" {
		cwdPath = filepath.Join(root, "unrelated")
	}
	if mode == "cwd-file" {
		cwdPath = filepath.Join(root, "cwd-file")
	}
	cwd, err := os.Open(cwdPath)
	if err != nil {
		return err
	}
	if mode == "cwd-closed" {
		if err := cwd.Close(); err != nil {
			return err
		}
	}
	if mode == "cwd-reused" {
		raw := int(cwd.Fd())
		if err := unix.Close(raw); err != nil {
			return err
		}
		replacement, err := unix.Open(filepath.Join(root, "unrelated"), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
		if err != nil {
			return err
		}
		if replacement != raw {
			if err := unix.Dup2(replacement, raw); err != nil {
				unix.Close(replacement)
				return err
			}
			if err := unix.Close(replacement); err != nil {
				return err
			}
		}
	}
	if mode == "cwd-mode" {
		if err := os.Chmod(providerCwd, 0o755); err != nil {
			cwd.Close()
			return err
		}
	}
	if err := control.ReportPopulation([]byte("populated")); err != nil {
		cwd.Close()
		return err
	}
	if err := control.AwaitProvider(); err != nil {
		cwd.Close()
		return err
	}
	return control.ExecProvider(prepared, cwd)
}

func runRetirementProviderHelper(args []string) error {
	if len(args) != 3 {
		return errors.New("retirement provider: missing witnesses or delay")
	}
	delayMillis, err := strconv.Atoi(args[2])
	if err != nil || delayMillis < 1 || delayMillis > 5000 {
		return errors.New("retirement provider: invalid delay")
	}
	signal.Ignore(unix.SIGTERM)
	write := func(path string) error {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		return file.Close()
	}
	if err := write(args[0]); err != nil {
		return err
	}
	time.Sleep(time.Duration(delayMillis) * time.Millisecond)
	return write(args[1])
}

type attemptFixture struct {
	t          *testing.T
	root       string
	dir        *os.File
	lifetime   *os.File
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
	if err := os.MkdirAll(filepath.Join(root, "change", "work"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "unrelated"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cwd-file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	dir, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	lifetime := createTestRuntimeLifetime(t, dir)
	lease, _, err := CreateGateLease(dir, lifetime, OuterActivationMarkerName)
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
	attemptSpec := AttemptSpec{AttemptID: "attempt-1", Wrapper: wrapper, MarkerName: InnerActivationMarkerName, TerminalName: TerminalSpoolName}
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
	f := &attemptFixture{t: t, root: root, dir: dir, lifetime: lifetime, lease: lease, controller: controller, spec: attemptSpec, outer: outer, diagnostic: diagnostic}
	t.Cleanup(func() {
		_ = controller.Close()
		if f.inner.Valid() {
			if got, identityErr := readIdentity(f.inner.PID); identityErr == nil && got == f.inner {
				_ = unix.Kill(-f.inner.PGID, unix.SIGKILL)
			}
		}
		_ = outer.Close()
		_ = lease.Close()
		_ = lifetime.Close()
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

func TestRuntimeLifetimeRemainsHeldAcrossOuterAndInnerOwnership(t *testing.T) {
	f := newAttemptFixture(t, "shell", "")
	f.activateOuter()
	f.advanceToProvider()
	if err := f.controller.Release(StageProvider); err != nil {
		t.Fatal(err)
	}
	waitFile(t, filepath.Join(f.root, "provider.pid"))
	if err := f.lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.dir.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.lifetime.Close(); err != nil {
		t.Fatal(err)
	}
	probe, err := unix.Open(filepath.Join(f.root, RuntimeLifetimeLeaseName), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(probe)
	if err := unix.Flock(probe, unix.LOCK_EX|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) {
		t.Fatalf("attempt runner lost inherited lifetime lease: %v", err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "continue"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	event, err := f.controller.Next(6 * time.Second)
	if err != nil || event.Kind != AttemptTerminal || event.Terminal == nil {
		t.Fatalf("terminal=%+v err=%v output=%q", event, err, f.output())
	}
	if err := f.controller.AcknowledgeTerminal(event.Terminal, true); err != nil {
		t.Fatal(err)
	}
	if exit, err := f.outer.FinishAfterExit(6 * time.Second); err != nil || exit.Code != 0 {
		t.Fatalf("outer exit=%+v err=%v", exit, err)
	}
	if err := unix.Flock(probe, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("attempt runner retained lease after exit: %v", err)
	}
	if err := unix.Flock(probe, unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
}

func TestProviderRetainsLeastPrivilegeLifetimeAfterOuterSIGKILL(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := newAttemptFixture(t, "lifetime", executable)
	inner := f.activateOuter()
	f.advanceToProvider()
	if err := f.controller.Release(StageProvider); err != nil {
		t.Fatal(err)
	}
	waitFile(t, filepath.Join(f.root, "provider.ready"))
	if got, err := readIdentity(inner.PID); err != nil || got != inner {
		t.Fatalf("provider identity=%+v err=%v want=%+v", got, err, inner)
	}
	if err := f.lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.dir.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.lifetime.Close(); err != nil {
		t.Fatal(err)
	}
	probe, err := unix.Open(filepath.Join(f.root, RuntimeLifetimeLeaseName), unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(probe)
	if err := unix.Flock(probe, unix.LOCK_EX|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) {
		t.Fatalf("provider did not hold lifetime before outer death: %v", err)
	}
	if err := f.outer.cmd.Process.Signal(unix.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if exit, err := f.outer.FinishAfterExit(6 * time.Second); err != nil || exit.Signal != int(unix.SIGKILL) {
		t.Fatalf("outer SIGKILL exit=%+v err=%v", exit, err)
	}
	if got, err := readIdentity(inner.PID); err != nil || got != inner {
		t.Fatalf("provider did not survive outer death: got=%+v err=%v", got, err)
	}
	if err := unix.Flock(probe, unix.LOCK_EX|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) {
		t.Fatalf("recovery observed available while provider lived: %v", err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "continue"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(6 * time.Second)
	for {
		err := unix.Flock(probe, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) || time.Now().After(deadline) {
			t.Fatalf("lifetime did not become available after provider exit: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := unix.Flock(probe, unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	waitExactAbsence(t, inner)
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

func TestAttemptRunnerReapsInertInnerExitBeforeSelection(t *testing.T) {
	f := newAttemptFixture(t, "shell", "")
	inner := f.activateOuter()
	if err := unix.Kill(-inner.PGID, unix.SIGKILL); err != nil {
		t.Fatal(err)
	}
	exit, err := f.outer.FinishAfterExit(4 * time.Second)
	if err != nil || exit.Code == 0 {
		t.Fatalf("outer exit=%+v err=%v output=%q", exit, err, f.output())
	}
	record, err := LoadTerminal(f.dir, "terminal.json")
	if err != nil || record.Terminal.Process != inner || !record.Terminal.Exit.Aborted || record.Terminal.Exit.Signal != int(unix.SIGKILL) || record.Terminal.Message == "" {
		t.Fatalf("inert-exit terminal=%+v err=%v", record, err)
	}
	for _, witness := range []string{"selection", "preparation", "population", "provider.effect"} {
		if _, err := os.Stat(filepath.Join(f.root, witness)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inert inner exit left %s witness: %v", witness, err)
		}
	}
	waitExactAbsence(t, inner)
	if err := unix.Kill(-inner.PGID, 0); !errors.Is(err, unix.ESRCH) {
		t.Fatalf("inert inner group remains: %v", err)
	}
}

func TestAttemptCleanupReapsObservedInertExit(t *testing.T) {
	f := newFixture(t)
	effect := filepath.Join(f.root, "effect")
	child := f.start("/bin/sh", []string{"-c", "printf bad > " + effect}, nil, nil)
	daemon, daemonPeer, err := newControlPair("test-daemon", "test-daemon-peer")
	if err != nil {
		t.Fatal(err)
	}
	worker, workerPeer, err := newControlPair("test-worker", "test-worker-peer")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []*os.File{daemon, daemonPeer, worker, workerPeer} {
		file := file
		t.Cleanup(func() { _ = file.Close() })
	}
	reads := &attemptReadSet{kq: child.kq, daemonFD: int(daemon.Fd()), workerFD: int(worker.Fd())}
	if err := reads.registerDaemon(); err != nil {
		t.Fatal(err)
	}
	if err := reads.registerWorker(); err != nil {
		t.Fatal(err)
	}
	identity := child.Identity()
	if err := unix.Kill(-identity.PGID, unix.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_, source, err := nextAttemptFrame(child, daemon, worker, true, true, time.Second)
	if err != nil || source != sourceChild || child.state != stateExited || !child.exitObserved {
		t.Fatalf("observed inert exit source=%d err=%v state=%d observed=%t", source, err, child.state, child.exitObserved)
	}
	cfg := attemptConfig{AttemptID: "attempt-inert-exit", TerminalName: "terminal.json"}
	done := make(chan error, 1)
	go func() {
		done <- finishAttemptWithExit(child, f.dir, cfg, reads, nil, false, nil)
	}()
	select {
	case finishErr := <-done:
		if finishErr != nil {
			t.Fatal(finishErr)
		}
	case <-time.After(2 * time.Second):
		reads.processOnly()
		_, _ = child.finishInertAfterExit()
		select {
		case <-done:
		case <-time.After(4 * time.Second):
		}
		t.Fatal("observed inert exit was not reaped")
	}
	if child.state != stateWaited || reads.daemonRegistered || reads.workerRegistered {
		t.Fatalf("inert exit did not converge: state=%d reads=%+v", child.state, reads)
	}
	record, err := LoadTerminal(f.dir, cfg.TerminalName)
	if err != nil || record.Terminal.Process != identity || !record.Terminal.Exit.Aborted || record.Terminal.Exit.Signal != int(unix.SIGKILL) {
		t.Fatalf("observed inert terminal=%+v err=%v", record, err)
	}
	if _, err := os.Stat(effect); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("observed inert exit executed target: %v", err)
	}
	waitExactAbsence(t, identity)
}

func TestAttemptCleanupRetiresProtocolReadinessBeforeProcessWait(t *testing.T) {
	for _, mode := range []string{"eof", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			effect := filepath.Join(f.root, "effect")
			child := f.start("/bin/sh", []string{"-c", "printf bad > " + effect}, nil, nil)
			daemon, daemonPeer, err := newControlPair("test-daemon", "test-daemon-peer")
			if err != nil {
				t.Fatal(err)
			}
			worker, workerPeer, err := newControlPair("test-worker", "test-worker-peer")
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range []*os.File{daemon, daemonPeer, worker, workerPeer} {
				file := file
				t.Cleanup(func() { _ = file.Close() })
			}
			reads := &attemptReadSet{kq: child.kq, daemonFD: int(daemon.Fd()), workerFD: int(worker.Fd())}
			if err := reads.registerDaemon(); err != nil {
				t.Fatal(err)
			}
			if err := reads.registerWorker(); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "eof":
				if err := daemonPeer.Close(); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				if _, err := daemonPeer.Write(make([]byte, 64)); err != nil {
					t.Fatal(err)
				}
			}
			_, source, cause := nextAttemptFrame(child, daemon, worker, true, true, time.Second)
			if cause == nil || source != sourceDaemon || child.state != stateBlocked || child.exitObserved {
				t.Fatalf("delayed process setup source=%d cause=%v state=%d observed=%t", source, cause, child.state, child.exitObserved)
			}
			cfg := attemptConfig{AttemptID: "attempt-" + mode, TerminalName: "terminal.json"}
			done := make(chan error, 1)
			started := time.Now()
			go func() {
				done <- finishAttemptWithExit(child, f.dir, cfg, reads, nil, false, cause)
			}()
			select {
			case finishErr := <-done:
				if finishErr == nil {
					t.Fatal("protocol failure lost its cause")
				}
			case <-time.After(2 * time.Second):
				// Independent safety transition keeps a deliberately broken
				// mutation from leaking the real fixture after this failure.
				reads.processOnly()
				_, _ = child.finishInertAfterExit()
				select {
				case <-done:
				case <-time.After(4 * time.Second):
				}
				t.Fatal("protocol readiness starved the process exit")
			}
			if time.Since(started) >= 2*time.Second || reads.daemonRegistered || reads.workerRegistered || child.state != stateWaited {
				t.Fatalf("process-only transition did not converge: elapsed=%v reads=%+v state=%d", time.Since(started), reads, child.state)
			}
			record, err := LoadTerminal(f.dir, cfg.TerminalName)
			if err != nil || record.Terminal.Process != child.Identity() || !record.Terminal.Exit.Aborted {
				t.Fatalf("protocol-failure terminal=%+v err=%v", record, err)
			}
			if _, err := os.Stat(effect); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("inert protocol failure executed target: %v", err)
			}
			waitExactAbsence(t, child.Identity())
		})
	}
}

func TestAttemptReadSetRemovalIsRetryableAndIdempotent(t *testing.T) {
	kq, err := unix.Kqueue()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(kq) })
	daemon, daemonPeer, err := newControlPair("test-daemon", "test-daemon-peer")
	if err != nil {
		t.Fatal(err)
	}
	worker, workerPeer, err := newControlPair("test-worker", "test-worker-peer")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []*os.File{daemon, daemonPeer, worker, workerPeer} {
		file := file
		t.Cleanup(func() { _ = file.Close() })
	}
	reads := &attemptReadSet{kq: kq, daemonFD: int(daemon.Fd()), workerFD: int(worker.Fd())}
	if err := reads.registerDaemon(); err != nil {
		t.Fatal(err)
	}
	if err := reads.registerWorker(); err != nil {
		t.Fatal(err)
	}
	reads.kq = -1
	daemonErr := reads.removeDaemon()
	workerErr := reads.removeWorker()
	if daemonErr == nil || workerErr == nil || !reads.daemonRegistered || !reads.workerRegistered {
		t.Fatalf("failed removal lost registration authority: daemon=%v worker=%v reads=%+v", daemonErr, workerErr, reads)
	}
	reads.kq = kq
	reads.processOnly()
	if reads.daemonRegistered || reads.workerRegistered {
		t.Fatalf("retried removal reads=%+v", reads)
	}
	reads.processOnly()
	if _, err := daemonPeer.Write([]byte("still-open")); err != nil {
		t.Fatal(err)
	}
	if _, err := workerPeer.Write([]byte("still-open")); err != nil {
		t.Fatal(err)
	}
	events := make([]unix.Kevent_t, 1)
	timeout := unix.NsecToTimespec((10 * time.Millisecond).Nanoseconds())
	if n, err := unix.Kevent(kq, nil, events, &timeout); err != nil || n != 0 {
		t.Fatalf("removed read filter remained ready: n=%d events=%+v err=%v", n, events, err)
	}
}

type releasedRetirementFixture struct {
	fixture            *fixture
	child              *OwnedChild
	daemon, daemonPeer *os.File
	worker, workerPeer *os.File
	reads              *attemptReadSet
	completed          string
}

func newReleasedRetirementFixture(t *testing.T, delay time.Duration) *releasedRetirementFixture {
	t.Helper()
	f := newFixture(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	started := filepath.Join(f.root, "provider-started")
	completed := filepath.Join(f.root, "provider-completed")
	child := f.start(executable, []string{"--attempt-retirement-provider", started, completed, strconv.Itoa(int(delay / time.Millisecond))}, nil, nil)
	daemon, daemonPeer, err := newControlPair("test-daemon", "test-daemon-peer")
	if err != nil {
		t.Fatal(err)
	}
	worker, workerPeer, err := newControlPair("test-worker", "test-worker-peer")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []*os.File{daemon, daemonPeer, worker, workerPeer} {
		file := file
		t.Cleanup(func() { _ = file.Close() })
	}
	reads := &attemptReadSet{kq: child.kq, daemonFD: int(daemon.Fd()), workerFD: int(worker.Fd())}
	if err := reads.registerDaemon(); err != nil {
		t.Fatal(err)
	}
	if err := reads.registerWorker(); err != nil {
		t.Fatal(err)
	}
	if _, err := child.Activate(); err != nil {
		t.Fatal(err)
	}
	waitFile(t, started)
	return &releasedRetirementFixture{fixture: f, child: child, daemon: daemon, daemonPeer: daemonPeer, worker: worker, workerPeer: workerPeer, reads: reads, completed: completed}
}

func (f *releasedRetirementFixture) closePeer(t *testing.T, source attemptSource) int {
	t.Helper()
	switch source {
	case sourceDaemon:
		if err := f.daemonPeer.Close(); err != nil {
			t.Fatal(err)
		}
		return int(f.daemon.Fd())
	case sourceWorker:
		if err := f.workerPeer.Close(); err != nil {
			t.Fatal(err)
		}
		return int(f.worker.Fd())
	default:
		t.Fatal("invalid retirement source")
		return -1
	}
}

func (f *releasedRetirementFixture) finish(t *testing.T, cfg attemptConfig) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		daemonOpen, cause := finishReleasedProvider(f.child, f.daemon, f.worker, f.reads)
		done <- finishAttemptWithExit(f.child, f.fixture.dir, cfg, f.reads, nil, daemonOpen, cause)
	}()
	return done
}

func TestReleasedProviderRetriesReadableFilterRetirement(t *testing.T) {
	for _, test := range []struct {
		name   string
		source attemptSource
	}{
		{name: "daemon-eof", source: sourceDaemon},
		{name: "worker-eof", source: sourceWorker},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newReleasedRetirementFixture(t, 1200*time.Millisecond)
			targetFD := f.closePeer(t, test.source)
			var calls atomic.Int32
			f.reads.testUnregister = func(fd int) error {
				if fd == targetFD && calls.Add(1) <= 2 {
					return fmt.Errorf("%w: injected readable-filter retirement", ErrUnresolved)
				}
				return nil
			}
			cfg := attemptConfig{AttemptID: "attempt-" + test.name, TerminalName: "terminal.json"}
			done := f.finish(t, cfg)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("released provider did not finish after transient filter retirement uncertainty")
			}
			if got := calls.Load(); got != 3 {
				t.Fatalf("retirement calls=%d want=3", got)
			}
			if _, err := os.Stat(f.completed); err != nil {
				t.Fatalf("authorized provider did not complete naturally: %v", err)
			}
			record, err := LoadTerminal(f.fixture.dir, cfg.TerminalName)
			if err != nil || record.Terminal.Process != f.child.Identity() || record.Terminal.Exit.Code != 0 || record.Terminal.Exit.Signal != 0 {
				t.Fatalf("natural terminal=%+v err=%v", record, err)
			}
			waitExactAbsence(t, f.child.Identity())
		})
	}
}

func TestReleasedProviderPermanentFilterUncertaintyPreservesExecution(t *testing.T) {
	f := newReleasedRetirementFixture(t, 100*time.Millisecond)
	targetFD := f.closePeer(t, sourceDaemon)
	entered := make(chan struct{}, 16)
	var restored atomic.Bool
	f.reads.testUnregister = func(fd int) error {
		if fd != targetFD {
			return nil
		}
		select {
		case entered <- struct{}{}:
		default:
		}
		if !restored.Load() {
			return fmt.Errorf("%w: permanent readable-filter retirement", ErrUnresolved)
		}
		return nil
	}
	cfg := attemptConfig{AttemptID: "attempt-permanent-retirement", TerminalName: "terminal.json"}
	done := f.finish(t, cfg)
	finished := false
	t.Cleanup(func() {
		if finished {
			return
		}
		restored.Store(true)
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Error("retirement owner did not join after restoration")
		}
	})
	for range 3 {
		select {
		case <-entered:
		case err := <-done:
			finished = true
			t.Fatalf("retirement uncertainty escaped to cleanup: %v", err)
		case <-time.After(4 * time.Second):
			t.Fatal("retirement owner stopped retrying")
		}
	}
	waitFile(t, f.completed)
	if _, err := os.Stat(filepath.Join(f.fixture.root, cfg.TerminalName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("permanent filter uncertainty published terminal: %v", err)
	}
	if got := ObserveProcess(f.child.Identity()); got.Presence != Present {
		t.Fatalf("retirement uncertainty lost exact unreaped provider: %+v", got)
	}
	select {
	case err := <-done:
		finished = true
		t.Fatalf("permanent retirement uncertainty returned: %v", err)
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
		t.Fatal("retirement did not converge after restoration")
	}
	record, err := LoadTerminal(f.fixture.dir, cfg.TerminalName)
	if err != nil || record.Terminal.Process != f.child.Identity() || record.Terminal.Exit.Code != 0 || record.Terminal.Exit.Signal != 0 {
		t.Fatalf("restored natural terminal=%+v err=%v", record, err)
	}
	waitExactAbsence(t, f.child.Identity())
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

func TestCurrentExecUsesTransferredCwdAfterParentPathReplacement(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := newAttemptFixture(t, "cwd", executable)
	inner := f.activateOuter()
	f.advanceToProvider()
	originalPath := filepath.Join(f.root, "change", "work")
	originalInfo, err := os.Lstat(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	original := originalInfo.Sys().(*syscall.Stat_t)
	if err := os.Rename(filepath.Join(f.root, "change"), filepath.Join(f.root, "change.old")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(originalPath, 0o700); err != nil {
		t.Fatal(err)
	}
	replacementInfo, err := os.Lstat(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	replacement := replacementInfo.Sys().(*syscall.Stat_t)
	if original.Dev == replacement.Dev && original.Ino == replacement.Ino {
		t.Fatal("replacement reused original cwd identity")
	}
	if err := f.controller.Release(StageProvider); err != nil {
		t.Fatal(err)
	}
	record := f.finishAndAck()
	if record.Terminal.Exit.Code != 0 || record.Terminal.Process != inner {
		t.Fatalf("descriptor cwd terminal=%+v", record.Terminal)
	}
	want := fmt.Sprintf("%d:%d", original.Dev, original.Ino)
	if got, err := os.ReadFile(filepath.Join(f.root, "cwd.identity")); err != nil || string(got) != want {
		t.Fatalf("provider cwd identity=%q err=%v want=%q", got, err, want)
	}
	if got, err := os.ReadFile(filepath.Join(f.root, "provider.effect")); err != nil || string(got) != "provider" {
		t.Fatalf("provider witness=%q err=%v", got, err)
	}
}

func TestCurrentExecRejectsInvalidTransferredCwd(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"cwd-unrelated", "cwd-file", "cwd-closed", "cwd-reused", "cwd-mode"} {
		t.Run(mode, func(t *testing.T) {
			f := newAttemptFixture(t, mode, executable)
			f.activateOuter()
			f.advanceToProvider()
			if err := f.controller.Release(StageProvider); err != nil {
				t.Fatal(err)
			}
			record := f.finishAndAck()
			if record.Terminal.Exit.Code == 0 {
				t.Fatalf("invalid cwd reported success: %+v", record.Terminal)
			}
			if _, err := os.Lstat(filepath.Join(f.root, "provider.effect")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid cwd provider effect: %v", err)
			}
		})
	}
}

func TestCurrentExecRechecksTransferredCwdAtFinalSeam(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := newAttemptFixture(t, "cwd-seam", executable)
	f.activateOuter()
	f.advanceToProvider()
	if err := f.controller.Release(StageProvider); err != nil {
		t.Fatal(err)
	}
	event, err := f.controller.Next(4 * time.Second)
	if err != nil || event.Kind != AttemptCheckpoint || event.Stage != StageProvider {
		t.Fatalf("final-check event=%+v err=%v", event, err)
	}
	if err := os.Chmod(filepath.Join(f.root, "change", "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := f.controller.acknowledgeCurrentExecCheck(); err != nil {
		t.Fatal(err)
	}
	record := f.finishAndAck()
	if record.Terminal.Exit.Code == 0 {
		t.Fatalf("mutated cwd reported success: %+v", record.Terminal)
	}
	if _, err := os.Lstat(filepath.Join(f.root, "provider.effect")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mutated cwd provider effect: %v", err)
	}
}

func TestExecProviderTakesCwdOwnershipOnRejectedCall(t *testing.T) {
	cwd, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fd := int(cwd.Fd())
	var worker *WorkerControl
	if err := worker.ExecProvider(nil, cwd); !errors.Is(err, ErrState) {
		t.Fatalf("rejected transfer=%v", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); !errors.Is(err, unix.EBADF) {
		t.Fatalf("rejected cwd remained open: %v", err)
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

func TestCurrentExecRejectsReplacedRuntimeLifetime(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := newAttemptFixture(t, "lease-seam", executable)
	f.activateOuter()
	f.advanceToProvider()
	if err := f.controller.Release(StageProvider); err != nil {
		t.Fatal(err)
	}
	event, err := f.controller.Next(4 * time.Second)
	if err != nil || event.Kind != AttemptCheckpoint || event.Stage != StageProvider {
		t.Fatalf("final-check event=%+v err=%v", event, err)
	}
	lifetimePath := filepath.Join(f.root, RuntimeLifetimeLeaseName)
	if err := os.Rename(lifetimePath, lifetimePath+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lifetimePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.controller.acknowledgeCurrentExecCheck(); err != nil {
		t.Fatal(err)
	}
	record := f.finishAndAck()
	if record.Terminal.Exit.Code == 0 {
		t.Fatalf("replaced lifetime reported success: %+v", record.Terminal)
	}
	if _, err := os.Stat(filepath.Join(f.root, "provider.effect")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("provider ran with replaced lifetime: %v", err)
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
	finishStart := strings.Index(string(attemptSource), "func finishAttemptWithExit(")
	waitStart := strings.Index(string(attemptSource), "func waitForAttemptChild(")
	if finishStart < 0 || waitStart <= finishStart {
		t.Fatal("attempt cleanup functions missing")
	}
	finishBody := string(attemptSource)[finishStart:waitStart]
	if strings.Index(finishBody, "reads.processOnly()") < 0 || strings.Index(finishBody, "reads.processOnly()") > strings.Index(finishBody, "waitForAttemptChild(child)") {
		t.Fatal("process cleanup begins before protocol read filters are retired")
	}
	waitBody := string(attemptSource)[waitStart:]
	for _, lifecycleCase := range []string{"case stateBlocked:", "case stateActivated:", "case stateExited:", "case stateWaited:"} {
		if !strings.Contains(waitBody, lifecycleCase) {
			t.Fatalf("attempt cleanup omits %s", lifecycleCase)
		}
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
		done <- finishAttemptWithExit(child, f.dir, cfg, nil, nil, false, nil)
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
		done <- finishAttemptWithExit(child, f.dir, cfg, nil, nil, false, nil)
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
