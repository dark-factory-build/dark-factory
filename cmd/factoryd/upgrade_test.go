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
	"syscall"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
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
// settled, then stops.
func bootUntilSettled(t *testing.T, home string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, testConfig(home)) }()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, present, err := install.ReadUpgradeMarker(home); !present && err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("boot = %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the boot never settled the release")
		}
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
