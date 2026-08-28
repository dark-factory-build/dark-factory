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
	testPTYSlaveClose = func(file *os.File) error {
		_ = file.Close()
		return errors.New("injected slave close failure")
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
	testPTYSlaveClose = func(file *os.File) error {
		_ = file.Close()
		return errors.New("injected bind failure")
	}
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
				testPTYSlaveClose = func(file *os.File) error {
					_ = file.Close()
					return errors.New("injected Bind failure")
				}
			}
			child, record, launchErr := prepared.LaunchAttempt(f.dir, "pre-registration", testResultProof())
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

// startedPTYChildForCleanup starts a real blocked PTY child and registers its
// exact exit filter the way Bind does, so cleanup tests exercise the
// exitRegistered arm without a full Bind.
func startedPTYChildForCleanup(t *testing.T) *StartedChild {
	t.Helper()
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
	t.Cleanup(func() {
		if started.child != nil {
			started.child.testHardCleanup = nil
			_ = convergeStartedChild(started)
		}
	})
	if err := registerExit(started.child.kq, started.child.identity.PID); err != nil {
		t.Fatal(err)
	}
	started.child.exitRegistered = true
	return started
}

// TestHardCleanupConvergesAfterObservedExit proves cleanup never re-waits a
// consumed one-shot exit observation: after waitForExit has already observed
// the exit, hardCleanup must succeed immediately and repeat as a no-op.
func TestHardCleanupConvergesAfterObservedExit(t *testing.T) {
	started := startedPTYChildForCleanup(t)
	child := started.child
	if err := child.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if _, err := child.waitForExit(4 * time.Second); err != nil {
		t.Fatal(err)
	}
	if err := child.hardCleanup(); err != nil {
		t.Fatalf("hard cleanup after observed exit = %v", err)
	}
	if err := child.hardCleanup(); err != nil {
		t.Fatalf("repeated hard cleanup = %v", err)
	}
	if child.state != stateWaited || child.kq != -1 {
		t.Fatalf("cleanup postcondition state=%v kq=%d", child.state, child.kq)
	}
	if err := started.Close(); err != nil || started.child != nil {
		t.Fatalf("converged close = %v child=%v", err, started.child)
	}
}

// TestHardCleanupRetriesConvergeAfterLostExitEvent proves a failed first pass
// keeps retry viable: a lost one-shot exit event costs one bounded timeout,
// after which the reaped state converges instead of retrying a closed kqueue.
func TestHardCleanupRetriesConvergeAfterLostExitEvent(t *testing.T) {
	started := startedPTYChildForCleanup(t)
	child := started.child
	if err := child.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	events := make([]unix.Kevent_t, 1)
	ts := unix.NsecToTimespec((4 * time.Second).Nanoseconds())
	if n, err := unix.Kevent(child.kq, nil, events, &ts); err != nil || n != 1 {
		t.Fatalf("draining exit event = %d, %v", n, err)
	}
	if err := child.hardCleanup(); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("first pass with lost event = %v", err)
	}
	if child.state != stateWaited {
		t.Fatalf("first pass did not reap: state=%v", child.state)
	}
	if child.kq < 0 {
		t.Fatal("failed pass closed the kqueue and doomed retry")
	}
	if err := child.hardCleanup(); err != nil {
		t.Fatalf("retry after lost event = %v", err)
	}
	if err := started.Close(); err != nil || started.child != nil {
		t.Fatalf("converged close = %v child=%v", err, started.child)
	}
}

// TestStartedChildCloseConvergesAfterPermanentSlaveCloseFailure models the
// real os.File contract: a failed Close still invalidates the descriptor, so
// the slave must converge on the first attempt with the error reported once.
func TestStartedChildCloseConvergesAfterPermanentSlaveCloseFailure(t *testing.T) {
	started := startedPTYChildForCleanup(t)
	injected := errors.New("injected permanent slave close failure")
	oldSlaveClose := testPTYSlaveClose
	t.Cleanup(func() { testPTYSlaveClose = oldSlaveClose })
	closeCalls := 0
	testPTYSlaveClose = func(file *os.File) error {
		closeCalls++
		if closeCalls == 1 {
			_ = file.Close()
			return injected
		}
		return file.Close()
	}
	err := started.Close()
	if !errors.Is(err, injected) {
		t.Fatalf("first close did not report the slave failure: %v", err)
	}
	if started.child != nil {
		if retryErr := started.Close(); retryErr != nil || started.child != nil {
			t.Fatalf("close after slave convergence = %v child=%v", retryErr, started.child)
		}
	}
	if closeCalls != 1 {
		t.Fatalf("slave close attempts = %d, the descriptor was already invalid", closeCalls)
	}
}

// TestLaunchAttemptReachesUnresolvedUnderPermanentCleanupFailure proves the
// designed permanent-uncertainty outcome is live: cleanup that fails on every
// attempt must surface ErrUnresolved without publishing convergence evidence,
// not spin forever.
func TestLaunchAttemptReachesUnresolvedUnderPermanentCleanupFailure(t *testing.T) {
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
	injected := errors.New("injected permanent cleanup failure")
	var startedChild *OwnedChild
	oldAfterStart, oldSlaveClose := testPTYAfterStart, testPTYSlaveClose
	t.Cleanup(func() {
		testPTYAfterStart = oldAfterStart
		testPTYSlaveClose = oldSlaveClose
		if startedChild != nil {
			startedChild.testHardCleanup = nil
			_ = startedChild.hardCleanup()
		}
	})
	testPTYAfterStart = func(child *OwnedChild) {
		startedChild = child
		child.testHardCleanup = func() error { return injected }
	}
	testPTYSlaveClose = func(file *os.File) error {
		_ = file.Close()
		return errors.New("injected Bind failure")
	}
	type launchResult struct {
		child  *OwnedChild
		record *AttemptResultRecord
		err    error
	}
	done := make(chan launchResult, 1)
	go func() {
		child, record, launchErr := prepared.LaunchAttempt(f.dir, "permanent-uncertainty", testResultProof())
		done <- launchResult{child: child, record: record, err: launchErr}
	}()
	select {
	case got := <-done:
		if got.child != nil || got.record != nil || !errors.Is(got.err, ErrUnresolved) || !errors.Is(got.err, injected) {
			t.Fatalf("LaunchAttempt child=%v record=%v err=%v", got.child, got.record, got.err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("LaunchAttempt never surfaced permanent cleanup uncertainty")
	}
	if _, err := os.Stat(filepath.Join(f.root, AttemptResultSpoolName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("permanent uncertainty fabricated convergence evidence: %v", err)
	}
}
