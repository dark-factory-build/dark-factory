//go:build darwin

package runner

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestBlockedPTYIsInertUntilActivationAndProviderGetsExactTTY(t *testing.T) {
	f := newFixture(t)
	effect := filepath.Join(f.root, "provider.effect")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	spec, err := PrepareExecSpec(ExecSpec{Target: executable, Args: []string{"--pty-provider", f.root}, Env: []string{"PATH=/usr/bin:/bin", "LANG=C", "TERM=xterm"}, Cwd: f.cwd})
	if err != nil {
		t.Fatal(err)
	}
	gate, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child, err := StartBlockedPTY(f.lease, gate, spec, false)
	if err != nil {
		t.Fatal(err)
	}
	f.child = child
	if _, err := child.WritePTY([]byte("before\n")); !errors.Is(err, ErrState) {
		t.Fatalf("pre-activation PTY write error=%v, want ErrState", err)
	}
	if _, err := os.Stat(effect); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("provider side effect before activation: %v", err)
	}
	want := child.Identity()
	if !want.Valid() || want.PID != want.PGID {
		t.Fatalf("invalid blocked PTY identity: %+v", want)
	}
	if _, err := child.Activate(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for {
		if _, statErr := os.Stat(effect); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			got, finishErr := child.FinishAfterExit(time.Second)
			t.Fatalf("provider side effect absent, finish=%+v err=%v", got, finishErr)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := child.Identity(); got != want {
		t.Fatalf("identity changed across activation: before=%+v after=%+v", want, got)
	}
	if _, err := child.WritePTY([]byte("hello\n")); err != nil {
		t.Fatalf("PTY input after activation: %v", err)
	}
	var output strings.Builder
	buf := make([]byte, 128)
	for !strings.Contains(output.String(), "RESPONSE:hello") {
		n, err := child.ReadPTY(buf)
		if err != nil {
			t.Fatalf("PTY output: %v (partial=%q)", err, output.String())
		}
		output.Write(buf[:n])
	}
	if !strings.Contains(output.String(), "RESPONSE:hello") {
		t.Fatalf("provider response missing: %q", output.String())
	}
	if _, err := child.FinishAfterExit(4 * time.Second); err != nil {
		t.Fatalf("finish provider: %v", err)
	}
	if n, err := child.ReadPTY(make([]byte, 16)); !errors.Is(err, io.EOF) || n != 0 {
		t.Fatalf("post-exit PTY did not reach EOF: n=%d err=%v", n, err)
	}
	if _, err := child.WritePTY([]byte("after\n")); !errors.Is(err, ErrState) {
		t.Fatalf("post-exit PTY write error=%v, want ErrState", err)
	}
}

func TestPTYReadDeadlineDoesNotAffectOwnedProvider(t *testing.T) {
	f := newFixture(t)
	spec, err := PrepareExecSpec(ExecSpec{Target: "/bin/sh", Args: []string{"-c", "sleep 2"}, Env: []string{"PATH=/usr/bin:/bin", "LANG=C", "TERM=xterm"}, Cwd: f.cwd})
	if err != nil {
		t.Fatal(err)
	}
	gate, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child, err := StartBlockedPTY(f.lease, gate, spec, false)
	if err != nil {
		t.Fatal(err)
	}
	f.child = child
	if _, err := child.Activate(); err != nil {
		t.Fatal(err)
	}
	if _, err := child.ReadPTY(make([]byte, 32)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("quiet PTY read error=%v, want deadline", err)
	}
	if got := ObserveProcess(child.Identity()); got.Presence != Present {
		t.Fatalf("observer read changed provider lifecycle: %+v", got)
	}
	if _, err := child.Terminate(2 * time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestPTYResizeUsesOnlyLiveOwnedMasterAndExactBounds(t *testing.T) {
	f := newFixture(t)
	gate, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	spec, err := PrepareExecSpec(ExecSpec{Target: "/bin/sh", Args: []string{"-c", "sleep 2"}, Env: []string{"PATH=/usr/bin:/bin", "LANG=C", "TERM=xterm"}, Cwd: f.cwd})
	if err != nil {
		t.Fatal(err)
	}
	child, err := StartBlockedPTY(f.lease, gate, spec, false)
	if err != nil {
		t.Fatal(err)
	}
	f.child = child
	for _, invalid := range [][2]int{{0, 24}, {-1, 24}, {80, 0}, {80, -1}, {maxPTYDimension + 1, 24}, {80, maxPTYDimension + 1}, {1 << 20, 24}, {80, 1 << 20}, {int(^uint(0) >> 1), 24}} {
		if err := child.ResizePTY(invalid[0], invalid[1]); !errors.Is(err, ErrState) {
			t.Fatalf("ResizePTY(%d,%d) error=%v, want ErrState", invalid[0], invalid[1], err)
		}
	}
	if err := child.ResizePTY(80, 24); !errors.Is(err, ErrState) {
		t.Fatalf("pre-activation resize error=%v, want ErrState", err)
	}
	if _, err := child.Activate(); err != nil {
		t.Fatal(err)
	}
	for _, size := range [][2]int{{80, 24}, {132, 43}, {132, 43}} {
		if err := child.ResizePTY(size[0], size[1]); err != nil {
			t.Fatalf("ResizePTY(%d,%d): %v", size[0], size[1], err)
		}
		got, err := unix.IoctlGetWinsize(int(child.ptyMaster.Fd()), unix.TIOCGWINSZ)
		if err != nil {
			t.Fatalf("read PTY size after ResizePTY(%d,%d): %v", size[0], size[1], err)
		}
		if got.Col != uint16(size[0]) || got.Row != uint16(size[1]) {
			t.Fatalf("PTY size=%dx%d, want %dx%d", got.Col, got.Row, size[0], size[1])
		}
	}
	if _, err := child.Terminate(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	if err := child.ResizePTY(80, 24); !errors.Is(err, ErrState) {
		t.Fatalf("post-reap resize error=%v, want ErrState", err)
	}
}

func TestPTYResizeRejectsNonPTYAndClosedMaster(t *testing.T) {
	f := newFixture(t)
	child := f.start("/bin/sh", []string{"-c", "sleep 2"}, nil, nil)
	if _, err := child.Activate(); err != nil {
		t.Fatal(err)
	}
	if err := child.ResizePTY(80, 24); !errors.Is(err, ErrState) {
		t.Fatalf("non-PTY resize error=%v, want ErrState", err)
	}
	if _, err := child.Terminate(2 * time.Second); err != nil {
		t.Fatal(err)
	}

	ptyFixture := newFixture(t)
	gate, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	spec, err := PrepareExecSpec(ExecSpec{Target: "/bin/sh", Args: []string{"-c", "sleep 2"}, Env: []string{"PATH=/usr/bin:/bin", "LANG=C", "TERM=xterm"}, Cwd: ptyFixture.cwd})
	if err != nil {
		t.Fatal(err)
	}
	ptyChild, err := StartBlockedPTY(ptyFixture.lease, gate, spec, false)
	if err != nil {
		t.Fatal(err)
	}
	ptyFixture.child = ptyChild
	if _, err := ptyChild.Activate(); err != nil {
		t.Fatal(err)
	}
	if err := ptyChild.ptyMaster.Close(); err != nil {
		t.Fatal(err)
	}
	if ptyChild.state != stateActivated || ptyChild.exitObserved {
		t.Fatalf("closed-master fixture stopped before resize: state=%v exitObserved=%v", ptyChild.state, ptyChild.exitObserved)
	}
	if err := ptyChild.ResizePTY(80, 24); !errors.Is(err, unix.EBADF) {
		t.Fatalf("active closed-master resize error=%v, want EBADF", err)
	}
	// The owner no longer has a usable master, but still owns the live child;
	// discard the closed descriptor before ordinary lifecycle cleanup.
	ptyChild.ptyMaster = nil
	if _, err := ptyChild.FinishAfterExit(2 * time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestPTYAttemptsHaveDistinctOwnedMasterAndProcess(t *testing.T) {
	one, two := newFixture(t), newFixture(t)
	gate, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	start := func(f *fixture) *OwnedChild {
		f.t.Helper()
		spec, err := PrepareExecSpec(ExecSpec{Target: "/bin/sh", Args: []string{"-c", "sleep 2"}, Env: []string{"PATH=/usr/bin:/bin", "LANG=C", "TERM=xterm"}, Cwd: f.cwd})
		if err != nil {
			f.t.Fatal(err)
		}
		child, err := StartBlockedPTY(f.lease, gate, spec, false)
		if err != nil {
			f.t.Fatal(err)
		}
		f.child = child
		return child
	}
	a, b := start(one), start(two)
	if a.Identity() == b.Identity() {
		t.Fatalf("PTY attempts reused process identity: %+v", a.Identity())
	}
	if a.ptyMaster.Fd() == b.ptyMaster.Fd() {
		t.Fatal("PTY attempts reused master descriptor")
	}
	if _, err := a.Activate(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Activate(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Terminate(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Terminate(2 * time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestBlockedPTYAbortClosesMasterWithoutProviderEffect(t *testing.T) {
	f := newFixture(t)
	effect := filepath.Join(f.root, "provider.effect")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	spec, err := PrepareExecSpec(ExecSpec{Target: executable, Args: []string{"--pty-provider", f.root}, Env: []string{"PATH=/usr/bin:/bin", "LANG=C", "TERM=xterm"}, Cwd: f.cwd})
	if err != nil {
		t.Fatal(err)
	}
	gate, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child, err := StartBlockedPTY(f.lease, gate, spec, false)
	if err != nil {
		t.Fatal(err)
	}
	f.child = child
	masterFD := int(child.ptyMaster.Fd())
	if err := child.Abort(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(effect); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("aborted PTY provider executed: %v", err)
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(masterFD, &stat); !errors.Is(err, unix.EBADF) {
		t.Fatalf("aborted PTY master fd %d remains open: %v", masterFD, err)
	}
}

func TestPTYSlaveCloseFailureCleansExactStartedChild(t *testing.T) {
	f := newFixture(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	spec, err := PrepareExecSpec(ExecSpec{Target: executable, Args: []string{"--pty-provider", f.root}, Env: []string{"PATH=/usr/bin:/bin", "LANG=C", "TERM=xterm"}, Cwd: f.cwd})
	if err != nil {
		t.Fatal(err)
	}
	gate, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var started *OwnedChild
	oldAfterStart, oldSlaveClose := testPTYAfterStart, testPTYSlaveClose
	t.Cleanup(func() {
		testPTYAfterStart = oldAfterStart
		testPTYSlaveClose = oldSlaveClose
	})
	testPTYAfterStart = func(child *OwnedChild) { started = child }
	closeCalls := 0
	testPTYSlaveClose = func(file *os.File) error {
		closeCalls++
		if closeCalls == 1 {
			return errors.New("injected slave close failure")
		}
		return file.Close()
	}
	child, err := StartBlockedPTY(f.lease, gate, spec, false)
	if child != nil || err == nil {
		t.Fatalf("slave-close failure returned child=%v err=%v", child, err)
	}
	if started == nil {
		t.Fatal("post-start owner hook was not reached")
	}
	waitExactAbsence(t, started.Identity())
	if started.state != stateWaited {
		t.Fatalf("started child was not reaped: state=%v", started.state)
	}
	if started.ptyMaster != nil {
		t.Fatal("started child retained PTY master after failed slave close")
	}
}

func TestStartedChildBindUncertaintyRetainsExactOwner(t *testing.T) {
	f := newFixture(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	spec, err := PrepareExecSpec(ExecSpec{Target: executable, Args: []string{"--pty-provider", f.root}, Env: []string{"PATH=/usr/bin:/bin", "LANG=C", "TERM=xterm"}, Cwd: f.cwd})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareBlockedPTY(f.lease, executable, spec, false)
	if err != nil {
		t.Fatal(err)
	}
	started, err := prepared.Start()
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("injected hard-cleanup uncertainty")
	oldSlaveClose := testPTYSlaveClose
	t.Cleanup(func() { testPTYSlaveClose = oldSlaveClose })
	testPTYSlaveClose = func(*os.File) error { return errors.New("injected bind failure") }
	started.child.testHardCleanup = func() error { return want }
	if child, err := started.Bind(); child != nil || !errors.Is(err, want) {
		t.Fatalf("Bind child=%v err=%v", child, err)
	}
	if started.child == nil {
		t.Fatal("Bind uncertainty discarded the exact started owner")
	}
	identity := started.child.Identity()
	if got := ObserveProcess(identity); got.Presence != Present {
		t.Fatalf("retained started owner=%+v", got)
	}
	testPTYSlaveClose = oldSlaveClose
	started.child.testHardCleanup = nil
	if err := started.Close(); err != nil {
		t.Fatal(err)
	}
	if started.child != nil {
		t.Fatal("converged started owner remained retained")
	}
	waitExactAbsence(t, identity)
}

func TestLaunchAttemptDistinguishesStartFailureAndPreRegistrationConvergence(t *testing.T) {
	for _, test := range []struct {
		name         string
		mutateStart  bool
		wantStarted  bool
		wantInjected bool
	}{
		{name: "exact Start error", mutateStart: true},
		{name: "successful Start then Bind uncertainty", wantStarted: true, wantInjected: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			if err := os.WriteFile(filepath.Join(f.root, OuterActivationMarkerName), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			spec, err := PrepareExecSpec(ExecSpec{Target: executable, Args: []string{"--pty-provider", f.root}, Env: []string{"PATH=/usr/bin:/bin", "LANG=C", "TERM=xterm"}, Cwd: f.cwd})
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := PrepareBlockedPTY(f.lease, executable, spec, false)
			if err != nil {
				t.Fatal(err)
			}
			started := false
			injected := errors.New("injected pre-registration cleanup uncertainty")
			oldAfterStart, oldSlaveClose := testPTYAfterStart, testPTYSlaveClose
			t.Cleanup(func() {
				testPTYAfterStart = oldAfterStart
				testPTYSlaveClose = oldSlaveClose
			})
			if test.mutateStart {
				prepared.cmd.Path = filepath.Join(f.root, "missing-gate")
			} else {
				hardCleanupCalls := 0
				testPTYAfterStart = func(child *OwnedChild) {
					started = true
					child.testHardCleanup = func() error {
						hardCleanupCalls++
						if hardCleanupCalls == 1 {
							return injected
						}
						return nil
					}
				}
				closeCalls := 0
				testPTYSlaveClose = func(file *os.File) error {
					closeCalls++
					if closeCalls == 1 {
						return errors.New("injected Bind failure")
					}
					return file.Close()
				}
			}
			child, record, launchErr := prepared.LaunchAttempt(f.dir, "pre-registration")
			if child != nil || record == nil || launchErr == nil {
				t.Fatalf("LaunchAttempt child=%v record=%+v err=%v", child, record, launchErr)
			}
			if started != test.wantStarted {
				t.Fatalf("Start history=%t want=%t", started, test.wantStarted)
			}
			if errors.Is(launchErr, injected) != test.wantInjected {
				t.Fatalf("cleanup mutation err=%v", launchErr)
			}
			result := record.Result()
			if result.Kind() != AttemptResultInnerUnregisteredConverged {
				t.Fatalf("result kind=%s", result.Kind())
			}
			if _, ok := result.Process(); ok {
				t.Fatal("unregistered result carried process authority")
			}
			if _, err := AuthenticateAttemptResult(f.dir, "pre-registration", ptrNotice(record.Notice())); err != nil {
				t.Fatal(err)
			}
		})
	}
}
