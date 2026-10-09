//go:build darwin

package main

import (
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

	"github.com/dark-factory-build/dark-factory/internal/install"
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
	staged, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(currentProgram))
	for _, step := range []error{
		os.MkdirAll(filepath.Join(bin, "current"), 0o700), os.MkdirAll(filepath.Join(bin, "previous"), 0o700),
		os.WriteFile(filepath.Join(bin, "current", "factoryd"), []byte(currentProgram), 0o700),
		os.WriteFile(filepath.Join(bin, "previous", "factoryd"), staged, 0o700),
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
		t.Fatalf("marker = %+v, %t", marker, present)
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

func TestUnreadableMarkerIsDiscardedNotCrashLooped(t *testing.T) {
	home := stagedHome(t, "serve", "x")
	if err := os.WriteFile(filepath.Join(install.ServiceDirectoryPath(home), "upgrade"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	bootUntilSettled(t, home)
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
