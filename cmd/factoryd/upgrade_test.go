//go:build darwin

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"golang.org/x/sys/unix"
)

// The trial child is this test binary, re-executed as a staged build.
func TestMain(m *testing.M) {
	switch os.Getenv("FACTORYD_TEST_CHILD") {
	case "":
		os.Exit(m.Run())
	case "panic":
		panic("a build that panics before run")
	case "hang":
		time.Sleep(time.Hour)
	case "socket":
		// Boot opens files before it listens; the first takes the lowest free fd.
		if _, err := os.Open(os.Args[0]); err != nil || browserSocket == nil {
			os.Exit(3)
		}
		listener, err := net.FileListener(browserSocket)
		if err != nil {
			os.Exit(4)
		}
		_, _ = os.Stdout.WriteString(listener.Addr().String())
	default:
		main()
	}
}

var runningSource = strings.Repeat("6", 40)

const currentProgram = "#!/bin/sh\n"

// stagedHome is an initialized home with a release staged as bin/previous:
// this test binary, running as mode, under an upgrade marker naming target.
func stagedHome(t *testing.T, mode, target string) string {
	t.Helper()
	home := initializedHome(t)
	bin := filepath.Join(install.ServiceDirectoryPath(home), "bin")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(currentProgram))
	for _, step := range []error{
		os.MkdirAll(filepath.Join(bin, "current"), 0o700), os.MkdirAll(filepath.Join(bin, "previous"), 0o700),
		os.WriteFile(filepath.Join(bin, "current", "factoryd"), []byte(currentProgram), 0o700),
		// A link, not a copy: on macOS a copy of the running test binary
		// makes this tree refuse the test's writes while the child runs.
		os.Link(self, filepath.Join(bin, "previous", "factoryd")),
		os.WriteFile(filepath.Join(install.ServiceDirectoryPath(home), "receipt"), []byte(`{"program_digest":"`+hex.EncodeToString(digest[:])+`"}`), 0o600),
		install.WriteUpgradeMarker(home, install.UpgradeMarker{Target: target}),
	} {
		if step != nil {
			t.Fatal(step)
		}
	}
	t.Setenv("FACTORYD_TEST_CHILD", mode)
	source, limit, poll := selfSource, trialLimit, trialPoll
	t.Cleanup(func() { selfSource, trialLimit, trialPoll = source, limit, poll })
	selfSource = func() string { return runningSource }
	trialLimit, trialPoll = time.Minute, 50*time.Millisecond
	return home
}

// A trial child adopts its supervisor's console socket from fd 3 even after
// boot has opened other files, and never mistakes one of those for it.
func TestTrialChildAdoptsOnlyItsInheritedConsoleSocket(t *testing.T) {
	held, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	socket, err := held.(*net.TCPListener).File()
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	for _, inherited := range []*os.File{socket, nil} {
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "FACTORYD_TEST_CHILD=socket", trialEnv+"=1")
		if inherited != nil {
			child.ExtraFiles = []*os.File{inherited}
		}
		output, err := child.Output()
		if inherited != nil && (err != nil || string(output) != held.Addr().String()) {
			t.Fatalf("inherited socket = %q, %v", output, err)
		}
		if exit := (*exec.ExitError)(nil); inherited == nil && (!errors.As(err, &exit) || exit.ExitCode() != 3) {
			t.Fatalf("no inherited socket = %q, %v", output, err)
		}
	}
}

// installedReceipt makes home an installed service whose plist renders the
// console Sockets at address, as ServiceInstall would record it.
func installedReceipt(t *testing.T, home, address string) {
	t.Helper()
	digest := sha256.Sum256([]byte(currentProgram))
	receipt := `{"version":1,"label":"com.dark-factory.test.console","plist_path":"/private/tmp/com.dark-factory.test.console.plist","plist_digest":"` + strings.Repeat("a", 64) + `","program_digest":"` + hex.EncodeToString(digest[:]) + `","development_browser_address":"` + address + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(install.ServiceDirectoryPath(home), "receipt"), []byte(receipt), 0o600); err != nil {
		t.Fatal(err)
	}
	if !install.ServiceHoldsBrowser(home) {
		t.Fatal("the installed receipt does not hold the console address")
	}
}

// Across a release from a build launchd gave no console socket, the new
// build's trial proves boot without taking the installed console address
// (here held by another listener) and releasing it.
func TestTrialWithoutTheConsoleSocketNeverBindsTheInstalledAddress(t *testing.T) {
	other, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	home := stagedHome(t, "serve", "development")
	installedReceipt(t, home, other.Addr().String())
	args := []string{"--home", home, "--runner", "/bin/sh", "--factoryctl", "/bin/sh", "--development-browser-address", other.Addr().String()}
	if err := superviseTrial(home, args, install.UpgradeMarker{Target: "development"}); !errors.Is(err, errRestart) || !strings.HasSuffix(err.Error(), ": promoted development") {
		t.Fatalf("trial = %v", err)
	}
}

// An installed factoryd without the console socket never binds the address
// itself: launchd did not start this one, so it refuses before opening the
// home rather than serve the paired origin.
func TestBootWithoutTheConsoleSocketOutsideLaunchdRefuses(t *testing.T) {
	home := stagedHome(t, "serve", "development")
	installedReceipt(t, home, "127.0.0.1:43123")
	var stdout, stderr bytes.Buffer
	if exit := run(context.Background(), childArgs(home), &stdout, &stderr); exit != exitFailure || !strings.Contains(stderr.String(), "launchd job holds the console address") || strings.Contains(stderr.String(), "startup") {
		t.Fatalf("boot outside launchd = %d %q", exit, stderr.String())
	}
}

func childArgs(home string, extra ...string) []string {
	return append([]string{"--home", home, "--runner", "/bin/sh", "--factoryctl", "/bin/sh", "--development-browser-address", "127.0.0.1:0"}, extra...)
}

// requireNotPromoted: launchd still runs the old build, and the marker
// carries why for the old build's next boot to record.
func requireNotPromoted(t *testing.T, home string, err error, reason string) {
	t.Helper()
	if !errors.Is(err, errRestart) {
		t.Fatalf("trial = %v", err)
	}
	if current, _ := os.ReadFile(filepath.Join(install.ServiceDirectoryPath(home), "bin", "current", "factoryd")); string(current) != currentProgram {
		t.Fatal("an unproven build became current")
	}
	if marker, present, _ := install.ReadUpgradeMarker(home); !present || !strings.Contains(marker.Reason, reason) {
		t.Fatalf("marker = %+v, %t (trial %v)", marker, present, err)
	}
}

// #1390: the staged build decided its own rollback, so a build that panicked,
// refused its arguments or hung before it rolled back was restarted forever.
func TestStagedBuildThatPanicsIsNeverPromoted(t *testing.T) {
	home := stagedHome(t, "panic", "development")
	requireNotPromoted(t, home, superviseTrial(home, childArgs(home), install.UpgradeMarker{Target: "development"}), "exit status 2")
}

func TestStagedBuildThatRejectsItsArgumentsIsNeverPromoted(t *testing.T) {
	home := stagedHome(t, "serve", "development")
	args := childArgs(home, "--flag-only-the-old-build-knows", "x")
	requireNotPromoted(t, home, superviseTrial(home, args, install.UpgradeMarker{Target: "development"}), "exit status 64")
}

func TestStagedBuildThatHangsIsStoppedAndNeverPromoted(t *testing.T) {
	home := stagedHome(t, "hang", "development")
	trialLimit = time.Second
	requireNotPromoted(t, home, superviseTrial(home, childArgs(home), install.UpgradeMarker{Target: "development"}), "answered false")
}

func TestStagedBuildThatAnswersAsAnotherReleaseIsNeverPromoted(t *testing.T) {
	home := stagedHome(t, "serve", strings.Repeat("7", 40))
	trialLimit = 3 * time.Second
	requireNotPromoted(t, home, superviseTrial(home, childArgs(home), install.UpgradeMarker{Target: strings.Repeat("7", 40)}), "answered false")
}

func TestStagedBuildThatAnswersAndStopsCleanlyIsPromoted(t *testing.T) {
	home := stagedHome(t, "serve", "development")
	if err := superviseTrial(home, childArgs(home), install.UpgradeMarker{Target: "development"}); !errors.Is(err, errRestart) || !strings.HasSuffix(err.Error(), ": promoted development") {
		t.Fatalf("trial = %v", err)
	}
	if current, _ := os.ReadFile(filepath.Join(install.ServiceDirectoryPath(home), "bin", "current", "factoryd")); string(current) == currentProgram {
		t.Fatal("the proven build was not promoted")
	}
	// The child left the release for the build launchd starts next.
	if marker, present, _ := install.ReadUpgradeMarker(home); !present || marker.Reason != "" {
		t.Fatalf("marker = %+v, %t", marker, present)
	}
	selfSource = func() string { return "development" }
	bootUntilSettled(t, home)
}

// Under launchd the trial serves the canonical socket its supervisor was
// handed: without it the trial would find that socket live, refuse to bind
// and never answer. The trial leaves the socket in place for the next build.
// An attempt read and outcome queued before the trial are refused by it and
// replay until the build launchd starts next, inheriting the same socket,
// answers them: never a transport failure.
func TestStagedBuildServesTheActivatedSocketAndLeavesIt(t *testing.T) {
	home := stagedHome(t, "serve", "development")
	path := install.LocalAPISocketPath(home)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	activated, err := listener.File()
	if err != nil {
		t.Fatal(err)
	}
	defer activated.Close()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	original := activatedLocalAPI
	t.Cleanup(func() { activatedLocalAPI = original })
	activatedLocalAPI = func() *os.File { return activated }
	token := filepath.Join(filepath.Dir(home), "attempt.token")
	if err := os.WriteFile(token, bytes.Repeat([]byte{'Q'}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DARK_FACTORY_ATTEMPT_TOKEN_FILE", token)
	client, err := api.NewAttemptClientFromEnvironment(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	answers := make(chan error, 2)
	go func() { _, err := client.Task(ctx); answers <- err }()
	go func() { _, err := client.Succeed(ctx, "queued across the release"); answers <- err }()
	awaitQueued(t, activated, 2)
	if err := superviseTrial(home, childArgs(home), install.UpgradeMarker{Target: "development"}); !errors.Is(err, errRestart) || !strings.HasSuffix(err.Error(), ": promoted development") {
		t.Fatalf("trial = %v", err)
	}
	if after, err := os.Lstat(path); err != nil || !os.SameFile(before, after) {
		t.Fatalf("the trial changed the activated socket: %v, %v", after, err)
	}
	successor := exec.Command(filepath.Join(install.ServiceDirectoryPath(home), "bin", "current", "factoryd"), childArgs(home)...)
	successor.Env = append(os.Environ(), install.LocalAPIInheritEnv+"=4")
	successor.ExtraFiles, successor.Stderr = []*os.File{nil, activated}, os.Stderr
	if err := successor.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = successor.Process.Signal(syscall.SIGTERM)
		_ = successor.Wait()
	}()
	for range 2 {
		// The successor knows no such attempt: its refusal is its answer.
		var remote *api.RemoteError
		if err := <-answers; !errors.As(err, &remote) || remote.Code() != api.RemoteUnauthorized {
			t.Fatalf("queued attempt call across the release = %v", err)
		}
	}
}

// withRelease runs use against home's store, closed again after.
func withRelease(t *testing.T, home string, use func(context.Context, *kernel.Store) error) {
	t.Helper()
	ctx := context.Background()
	opened, err := install.OpenOperationalHome(ctx, home)
	if err != nil {
		t.Fatal(err)
	}
	store, err := opened.OpenStore(ctx)
	if err == nil {
		err = use(ctx, store)
		err = errors.Join(err, store.Close())
	}
	if err = errors.Join(err, opened.Close()); err != nil {
		t.Fatal(err)
	}
}

// A release restart's outage runs from when the staging generation stops
// serving, stamped while its socket is still bound, to when the settling one
// starts accepting, after its browser and relay are up.
func TestReleaseOutageStampsTheServingTransitions(t *testing.T) {
	home := stagedHome(t, "serve", "development")
	withRelease(t, home, func(ctx context.Context, store *kernel.Store) error {
		project, err := kernel.ProjectIDFromBytes(bytes.Repeat([]byte{0x33}, kernel.IDBytes))
		at, atErr := kernel.NewUnixMillis(time.Now().UnixMilli())
		if err = errors.Join(err, atErr); err == nil {
			_, err = store.CreateProject(ctx, kernel.NewProject{ID: project, Name: "self", Root: filepath.Join(filepath.Dir(home), "repo")}, at)
		}
		if err == nil {
			err = store.RecordDelivery(ctx, project, "dark-factory-build/dark-factory", kernel.ProductionDelivery{ID: "release:development", Kind: "runtime", Destination: "factoryd", Revision: strings.Repeat("e", 40), State: "running", Phase: "trial", PullRequests: []uint64{}, UpdatedAt: at.Int64()}, at)
		}
		return err
	})
	hook := startupPhaseHook
	t.Cleanup(func() { startupPhaseHook = hook })
	var bound atomic.Bool
	var browser atomic.Int64
	startupPhaseHook = func(phase string) {
		switch phase {
		case "withdrawn":
			_, err := os.Lstat(install.LocalAPISocketPath(home))
			bound.Store(err == nil)
		case "browser":
			browser.Store(time.Now().UnixMilli())
			time.Sleep(2 * time.Millisecond)
		}
	}
	owner, err := openProcess(context.Background(), testConfig(home))
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.close(); err != nil {
		t.Fatal(err)
	}
	if !bound.Load() {
		t.Fatal("the withdrawal was stamped after the socket was withdrawn")
	}
	selfSource = func() string { return "development" }
	bootUntilSettled(t, home)
	withRelease(t, home, func(ctx context.Context, store *kernel.Store) error {
		_, delivery, found, err := store.Delivery(ctx, "release:development")
		if err != nil || !found || delivery.State != "verified" || delivery.WithdrawnAt == 0 || delivery.ServedAt <= browser.Load() || delivery.ServedAt < delivery.WithdrawnAt {
			t.Errorf("release = %+v, %t, %v; browser bound at %d", delivery, found, err, browser.Load())
		}
		return nil
	})
}

// awaitQueued waits until n connections wait in listener's backlog: kqueue
// reports a listening socket's pending connections as its read data.
func awaitQueued(t *testing.T, listener *os.File, n int64) {
	t.Helper()
	queue, err := unix.Kqueue()
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(queue)
	changes := []unix.Kevent_t{{Ident: uint64(listener.Fd()), Filter: unix.EVFILT_READ, Flags: unix.EV_ADD}}
	events := make([]unix.Kevent_t, 1)
	for deadline := time.Now().Add(10 * time.Second); ; changes = nil {
		count, err := unix.Kevent(queue, changes, events, &unix.Timespec{Nsec: 50e6})
		if err == nil && count == 1 && events[0].Data >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d connections never queued: %v", n, err)
		}
	}
}

func TestPassedTrialRecoveryRestartsAndSettlesWithoutRepeatingPromotion(t *testing.T) {
	revision := strings.Repeat("d", 40)
	home := stagedHome(t, "serve", "development")
	ctx := context.Background()
	opened, err := install.OpenOperationalHome(ctx, home)
	if err != nil {
		t.Fatal(err)
	}
	store, err := opened.OpenStore(ctx)
	if err != nil {
		_ = opened.Close()
		t.Fatal(err)
	}
	project, projectErr := kernel.ProjectIDFromBytes(bytes.Repeat([]byte{0x32}, kernel.IDBytes))
	at, atErr := kernel.NewUnixMillis(time.Now().UnixMilli())
	if err = errors.Join(err, projectErr, atErr); err == nil {
		_, err = store.CreateProject(ctx, kernel.NewProject{ID: project, Name: "self", Root: filepath.Join(filepath.Dir(home), "repo")}, at)
	}
	if err == nil {
		err = store.RecordDelivery(ctx, project, "dark-factory-build/dark-factory", kernel.ProductionDelivery{ID: "release:development", Kind: "runtime", Destination: "factoryd", Revision: revision, State: "running", Phase: "trial", PullRequests: []uint64{}, UpdatedAt: at.Int64()}, at)
	}
	if err = errors.Join(err, store.Close(), opened.Close()); err != nil {
		t.Fatal(err)
	}
	promote, calls := promoteService, 0
	t.Cleanup(func() { promoteService = promote })
	promoteService = func(home string) error {
		calls++
		if calls == 1 {
			return errors.New("supervisor interrupted before promotion")
		}
		return install.PromoteService(home)
	}
	selfSource = func() string { return runningSource }
	settled := make(chan string, 1)
	settle := releaseSettledHook
	t.Cleanup(func() { releaseSettledHook = settle })
	releaseSettledHook = func(target, state string) { settled <- target + " " + state }
	if err := superviseTrial(home, childArgs(home), install.UpgradeMarker{Target: "development"}); !errors.Is(err, errRestart) {
		t.Fatalf("interrupted trial = %v", err)
	}
	if calls != 1 {
		t.Fatalf("promotion calls after trial = %d, want 1", calls)
	}
	if marker, present, err := install.ReadUpgradeMarker(home); err != nil || !present || !marker.TrialPassed {
		t.Fatalf("passed marker after interruption = %+v, %t, %v", marker, present, err)
	}
	if current, _ := os.ReadFile(filepath.Join(install.ServiceDirectoryPath(home), "bin", "current", "factoryd")); string(current) != currentProgram {
		t.Fatal("an interrupted trial changed current")
	}
	if err := serve(context.Background(), testConfig(home)); !errors.Is(err, errRestart) {
		t.Fatalf("recovery = %v", err)
	}
	if calls != 2 {
		t.Fatalf("promotion calls after recovery = %d, want 2", calls)
	}
	if marker, present, err := install.ReadUpgradeMarker(home); err != nil || !present || !marker.TrialPassed {
		t.Fatalf("passed marker after recovery = %+v, %t, %v", marker, present, err)
	}
	if current, _ := os.ReadFile(filepath.Join(install.ServiceDirectoryPath(home), "bin", "current", "factoryd")); string(current) == currentProgram {
		t.Fatal("recovery did not promote the passed trial")
	}
	selfSource = func() string { return "development" }
	bootCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- serve(bootCtx, testConfig(home)) }()
	select {
	case result := <-settled:
		if result != "development verified" {
			t.Fatalf("settlement = %q", result)
		}
	case err := <-done:
		t.Fatalf("settling boot = %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("release never reached verified")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("promotion calls after settlement = %d, want 2", calls)
	}
	opened, err = install.OpenOperationalHome(ctx, home)
	if err != nil {
		t.Fatal(err)
	}
	store, err = opened.OpenStore(ctx)
	if err != nil {
		_ = opened.Close()
		t.Fatal(err)
	}
	_, delivery, found, err := store.Delivery(ctx, "release:development")
	if err = errors.Join(err, store.Close(), opened.Close()); err != nil || !found || delivery.State != "verified" || delivery.Phase != "" {
		t.Fatalf("settled release = %+v, %t, %v", delivery, found, err)
	}
}

// A failed trial whose child migrated the store leaves a schema the old
// build refuses: the old build restores its backup and serves.
func TestOldBuildRestoresTheBackupOnlyOverAMigratedStore(t *testing.T) {
	for _, migrated := range []bool{true, false} {
		home := stagedHome(t, "serve", strings.Repeat("7", 40))
		database := filepath.Join(home, "factory.sqlite3")
		backup := []byte("not a database: restoring it would refuse the boot")
		if migrated {
			var err error
			if backup, err = os.ReadFile(database); err != nil {
				t.Fatal(err)
			}
			if output, err := exec.Command("/usr/bin/sqlite3", database, "PRAGMA user_version = 999999").CombinedOutput(); err != nil {
				t.Fatalf("migrate: %v %s", err, output)
			}
		}
		if err := os.WriteFile(install.UpgradeBackupPath(home), backup, 0o600); err != nil {
			t.Fatal(err)
		}
		// Unmigrated, the store keeps this build's writes since the backup.
		bootUntilSettled(t, home)
		if _, err := os.Lstat(install.UpgradeBackupPath(home)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("migrated=%t: backup = %v", migrated, err)
		}
	}
}

// A backup restores only a failed trial's migration: never without a marker
// naming another build, and never over a store refused for another reason.
func TestOldBuildNeverRestoresTheBackupOverAnyOtherRefusal(t *testing.T) {
	for name, damage := range map[string]string{"no marker": "PRAGMA user_version = 999999", "changed schema": "CREATE TABLE written_by_hand (x)"} {
		home := stagedHome(t, "serve", strings.Repeat("7", 40))
		if name == "no marker" {
			if err := os.Remove(filepath.Join(install.ServiceDirectoryPath(home), "upgrade")); err != nil {
				t.Fatal(err)
			}
		}
		database := filepath.Join(home, "factory.sqlite3")
		backup, err := os.ReadFile(database)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(install.UpgradeBackupPath(home), backup, 0o600); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command("/usr/bin/sqlite3", database, damage).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v %s", name, err, output)
		}
		// Bounded, so a build that restored and served fails here, not hangs.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = serve(ctx, testConfig(home))
		cancel()
		if !errors.Is(err, kernel.ErrForeignDatabase) {
			t.Fatalf("%s: serve = %v", name, err)
		}
		if _, err := os.Lstat(install.UpgradeBackupPath(home)); err != nil {
			t.Fatalf("%s: the backup replaced the store: %v", name, err)
		}
	}
}

// A staged build that cannot bind the browser fails its trial: every boot
// step that can refuse the process runs there.
func TestStagedBuildThatCannotBindTheBrowserIsNeverPromoted(t *testing.T) {
	home := stagedHome(t, "serve", "development")
	held, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	args := []string{"--home", home, "--runner", "/bin/sh", "--factoryctl", "/bin/sh", "--development-browser-address", held.Addr().String()}
	requireNotPromoted(t, home, superviseTrial(home, args, install.UpgradeMarker{Target: "development"}), "answered false")
}

// launchd does not end a trial child whose supervisor died, so the old
// build's boot stops the orphan holding the home before it opens it.
func TestOldBuildRestartedMidTrialStopsTheOrphanedChild(t *testing.T) {
	home := stagedHome(t, "serve", strings.Repeat("7", 40))
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(self, childArgs(home)...)
	child.Env = append(os.Environ(), trialEnv+"=1")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- child.Wait() }()
	t.Cleanup(func() { _ = child.Process.Kill() })
	if err := install.WriteUpgradeMarker(home, install.UpgradeMarker{Target: strings.Repeat("7", 40), Trial: child.Process.Pid, TrialStart: processStart(child.Process.Pid)}); err != nil {
		t.Fatal(err)
	}
	waitOperatorClient(t, home)
	bootUntilSettled(t, home)
	select {
	case <-exited:
	default:
		t.Fatal("the orphaned trial child still runs")
	}
}

// A supervisor killed mid-trial decided nothing: the next boot fails its
// release as interrupted, which the release lane retries once
// (internal/daemon's TestTrialInterruptedBeforePromotionIsRetriedOnceByTheLane),
// and promotes nothing.
func TestSupervisorKilledMidTrialLeavesARetryableRelease(t *testing.T) {
	sha := strings.Repeat("7", 40)
	home := stagedHome(t, "serve", sha)
	ctx := context.Background()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	supervisor := exec.Command(self, childArgs(home)...)
	supervisor.SysProcAttr, supervisor.Stderr = &syscall.SysProcAttr{Setpgid: true}, os.Stderr
	if err := supervisor.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-supervisor.Process.Pid, syscall.SIGKILL) })
	exited := make(chan error, 1)
	go func() { exited <- supervisor.Wait() }()
	markerUntil := func(done func(install.UpgradeMarker, bool) bool) install.UpgradeMarker {
		t.Helper()
		for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			if marker, present, err := install.ReadUpgradeMarker(home); err == nil && done(marker, present) {
				return marker
			}
			select {
			case err := <-exited:
				t.Fatalf("the supervisor exited early: %v", err)
			default:
			}
			if time.Now().After(deadline) {
				t.Fatal("the upgrade marker never reached the expected state")
			}
		}
	}
	// Its boot settling the stale marker shows its recovery has run; then a
	// release stages again and signals it, as release() does.
	markerUntil(func(_ install.UpgradeMarker, present bool) bool { return !present })
	if err := install.WriteUpgradeMarker(home, install.UpgradeMarker{Target: sha}); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	marker := markerUntil(func(marker install.UpgradeMarker, present bool) bool { return present && marker.Trial > 0 })
	if err := syscall.Kill(-supervisor.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	<-exited
	if marker = markerUntil(func(install.UpgradeMarker, bool) bool { return true }); marker.Reason != "" || marker.TrialPassed {
		t.Fatalf("a killed supervisor decided its trial: %+v", marker)
	}
	// The boot's own orphan stop is covered above; here the orphan is stopped
	// first so the release record of the dead process can be written.
	_ = syscall.Kill(-marker.Trial, syscall.SIGKILL)
	project, err := kernel.ProjectIDFromBytes(bytes.Repeat([]byte{0x33}, kernel.IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		opened, err := install.OpenOperationalHome(ctx, home)
		if err == nil {
			store, storeErr := opened.OpenStore(ctx)
			at, atErr := kernel.NewUnixMillis(time.Now().UnixMilli())
			if err = errors.Join(storeErr, atErr); err == nil {
				_, err = store.CreateProject(ctx, kernel.NewProject{ID: project, Name: "self", Root: filepath.Join(filepath.Dir(home), "repo")}, at)
				if err == nil {
					// What release() last wrote before its SIGTERM.
					err = store.RecordDelivery(ctx, project, "dark-factory-build/dark-factory", kernel.ProductionDelivery{ID: "release:" + sha, Kind: "runtime", Destination: "factoryd", Revision: sha, State: "running", Phase: "trial", PullRequests: []uint64{}, UpdatedAt: at.Int64()}, at)
				}
				err = errors.Join(err, store.Close())
			}
			if err = errors.Join(err, opened.Close()); err != nil {
				t.Fatal(err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the orphaned trial still holds the home: %v", err)
		}
	}
	bootUntilSettled(t, home)
	if current, _ := os.ReadFile(filepath.Join(install.ServiceDirectoryPath(home), "bin", "current", "factoryd")); string(current) != currentProgram {
		t.Fatal("an undecided trial was promoted")
	}
	opened, err := install.OpenOperationalHome(ctx, home)
	if err != nil {
		t.Fatal(err)
	}
	store, err := opened.OpenStore(ctx)
	if err != nil {
		_ = opened.Close()
		t.Fatal(err)
	}
	_, delivery, found, err := store.Delivery(ctx, "release:"+sha)
	if err = errors.Join(err, store.Close(), opened.Close()); err != nil || !found || delivery.State != "failed" || delivery.Phase != "trial" || delivery.Reason != "interrupted by a factoryd restart" {
		t.Fatalf("settled release = %+v, %t, %v", delivery, found, err)
	}
}

// A pid the trial child no longer owns belongs to someone else: the boot
// kills only the process that started when the child did.
func TestOldBuildNeverKillsAReusedTrialPid(t *testing.T) {
	home := stagedHome(t, "serve", strings.Repeat("7", 40))
	other := exec.Command("/bin/sleep", "30")
	other.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Process.Kill(); _ = other.Wait() })
	if err := install.WriteUpgradeMarker(home, install.UpgradeMarker{Target: strings.Repeat("7", 40), Trial: other.Process.Pid, TrialStart: processStart(other.Process.Pid) - 1}); err != nil {
		t.Fatal(err)
	}
	bootUntilSettled(t, home)
	if err := other.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("the boot killed an unrelated process: %v", err)
	}
}

// A trial child proves it boots and answers as the release, and acts on
// nothing: no recovery, scheduler (so no dispatch, intake, release or GitHub
// tick), browser or relay, every other call refused, and its release left
// for the supervisor.
func TestTrialChildAnswersStatusAndActsOnNothing(t *testing.T) {
	home := stagedHome(t, "serve", "development")
	t.Setenv(trialEnv, "1")
	var phases []string
	startupPhaseHook = func(phase string) { phases = append(phases, phase) }
	t.Cleanup(func() { startupPhaseHook = nil })
	// Bounded on its own: the child stops at trialLimit with no supervisor.
	trialLimit = 2 * time.Second
	done := make(chan error, 1)
	go func() { done <- serve(context.Background(), testConfig(home)) }()
	client := waitOperatorClient(t, home)
	if web, err := client.WebStatus(context.Background()); err != nil || web.Build.Source != "development" || web.State != "stopped" {
		t.Fatalf("web_status = %+v, %v", web, err)
	}
	if _, err := client.Snapshot(context.Background()); err == nil {
		t.Fatal("a trial build answered a call other than web_status")
	}
	if err := <-done; err != nil {
		t.Fatalf("serve = %v", err)
	}
	if got := strings.Join(phases, ","); got != "home,store,runtime parent,supervisor spec,maintainer,local API,listener,browser,trial" {
		t.Fatalf("trial phases = %s", got)
	}
	if _, present, _ := install.ReadUpgradeMarker(home); !present {
		t.Fatal("the trial child settled its own release")
	}
}

// A torn marker is discarded, not crash-looped, even over a store the trial
// child migrated: with a backup present it is a failed trial.
func TestUnreadableMarkerIsDiscardedNotCrashLooped(t *testing.T) {
	for _, migrated := range []bool{false, true} {
		home := stagedHome(t, "serve", "x")
		if migrated {
			database := filepath.Join(home, "factory.sqlite3")
			backup, err := os.ReadFile(database)
			if err == nil {
				err = os.WriteFile(install.UpgradeBackupPath(home), backup, 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if output, err := exec.Command("/usr/bin/sqlite3", database, "PRAGMA user_version = 999999").CombinedOutput(); err != nil {
				t.Fatalf("migrate: %v %s", err, output)
			}
		}
		if err := os.WriteFile(filepath.Join(install.ServiceDirectoryPath(home), "upgrade"), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		bootUntilSettled(t, home)
	}
}

// bootUntilSettled serves as launchd's next boot would until the release is
// settled, then stops. It waits for the record, not the marker: the marker
// goes first, and stopping then would cancel the record's write.
func bootUntilSettled(t *testing.T, home string) {
	t.Helper()
	settled, hook := make(chan struct{}, 1), releaseSettledHook
	releaseSettledHook = func(string, string) { settled <- struct{}{} }
	defer func() { releaseSettledHook = hook }()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, testConfig(home)) }()
	select {
	case <-settled:
	case err := <-done:
		t.Fatalf("boot = %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the boot never settled the release")
	}
	if _, present, err := install.ReadUpgradeMarker(home); present || err != nil {
		t.Fatalf("settled release left its marker: %t, %v", present, err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve = %v", err)
	}
}

// #1854: a boot with no upgrade marker fails the release its previous process
// left running and stops that release's orphaned build.
func TestBootFailsAReleaseItsPreviousProcessLeftRunning(t *testing.T) {
	home := initializedHome(t)
	orphan := exec.Command("/bin/sleep", "60")
	orphan.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := orphan.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = orphan.Process.Kill() })
	sha := strings.Repeat("7", 40)
	ctx := context.Background()
	opened, err := install.OpenOperationalHome(ctx, home)
	if err != nil {
		t.Fatal(err)
	}
	store, err := opened.OpenStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	project, err := kernel.ProjectIDFromBytes(bytes.Repeat([]byte{0x31}, kernel.IDBytes))
	at, atErr := kernel.NewUnixMillis(time.Now().UnixMilli())
	if err = errors.Join(err, atErr); err == nil {
		_, err = store.CreateProject(ctx, kernel.NewProject{ID: project, Name: "self", Root: filepath.Join(filepath.Dir(home), "repo")}, at)
	}
	if err == nil {
		err = store.RecordDelivery(ctx, project, "dark-factory-build/dark-factory", kernel.ProductionDelivery{ID: "release:" + sha, Kind: "runtime", Destination: "factoryd", Revision: sha, State: "running", Phase: "build", PullRequests: []uint64{}, UpdatedAt: at.Int64(), Group: orphan.Process.Pid}, at)
	}
	if err = errors.Join(err, store.Close(), opened.Close()); err != nil {
		t.Fatal(err)
	}
	serveCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- serve(serveCtx, testConfig(home)) }()
	client := waitOperatorClient(t, home)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		delivery, err := client.Release(ctx, api.ReleaseInput{SHA: sha})
		if err == nil && delivery.State == "failed" && delivery.Reason == "interrupted by a factoryd restart" && delivery.Group == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the boot never failed the interrupted release: %+v, %v", delivery, err)
		}
	}
	if err := orphan.Wait(); err == nil || !strings.Contains(err.Error(), "killed") {
		t.Fatalf("the interrupted release's build survived the boot: %v", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve = %v", err)
	}
}
