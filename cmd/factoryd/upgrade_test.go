//go:build darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/buildinfo"
	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

var trialTarget = strings.Repeat("7", 40)

type trialSeams struct {
	exits     chan int
	rollbacks chan string
	verify    error
}

// trialHome is an initialized home whose service directory holds marker,
// with every process-ending effect of the trial boot recorded instead.
func trialHome(t *testing.T, marker install.UpgradeMarker) (string, *trialSeams) {
	t.Helper()
	home := initializedHome(t)
	if err := os.Mkdir(install.ServiceDirectoryPath(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := install.WriteUpgradeMarker(home, marker); err != nil {
		t.Fatal(err)
	}
	seams := &trialSeams{exits: make(chan int, 4), rollbacks: make(chan string, 4)}
	source, exit, rollback, verify, limit, promote := selfSource, trialExit, rollbackService, verifyRelease, trialLimit, promoteAfter
	t.Cleanup(func() {
		selfSource, trialExit, rollbackService, verifyRelease, trialLimit, promoteAfter = source, exit, rollback, verify, limit, promote
	})
	selfSource = func() string { return trialTarget }
	trialExit = func(code int) { seams.exits <- code }
	rollbackService = func(_ context.Context, _ string, restore bool, reason string) error {
		seams.rollbacks <- fmt.Sprintf("restore=%t %s", restore, reason)
		return nil
	}
	verifyRelease = func(context.Context, string, buildinfo.Identity) error { return seams.verify }
	trialLimit, promoteAfter = time.Minute, 20*time.Millisecond
	return home, seams
}

func readMarker(t *testing.T, home string) (install.UpgradeMarker, bool) {
	t.Helper()
	marker, present, err := install.ReadUpgradeMarker(home)
	if err != nil {
		t.Fatal(err)
	}
	return marker, present
}

// boot runs factoryd once as launchd would and returns its exit status.
func boot(t *testing.T, home string) int {
	t.Helper()
	return run(context.Background(), []string{"--home", home}, io.Discard, io.Discard)
}

func TestTrialBuildPromotesOnceItStaysUpAndVerifies(t *testing.T) {
	home, seams := trialHome(t, install.UpgradeMarker{Target: trialTarget, UserVersion: kernel.SchemaVersion, State: install.UpgradeTrial})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, testConfig(home)) }()
	deadline := time.Now().Add(5 * time.Second)
	for _, present := readMarker(t, home); present; _, present = readMarker(t, home) {
		if time.Now().After(deadline) {
			t.Fatal("the trial build was never promoted")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil || len(seams.exits) != 0 || len(seams.rollbacks) != 0 {
		t.Fatalf("promoted build = %v, exits %d, rollbacks %d", err, len(seams.exits), len(seams.rollbacks))
	}
}

func TestTrialBuildThatCannotForgetItsMarkerRecordsNothingAndRollsBack(t *testing.T) {
	home, seams := trialHome(t, install.UpgradeMarker{Target: trialTarget, UserVersion: kernel.SchemaVersion, State: install.UpgradeTrial})
	restore := removeUpgrade
	t.Cleanup(func() { removeUpgrade = restore })
	removals := make(chan struct{}, 4)
	removeUpgrade = func(string) error { removals <- struct{}{}; return errors.New("read-only") }
	trialLimit = 300 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, testConfig(home)) }()
	<-removals
	// Not promoted: the trial limit still ends the build.
	if code := <-seams.exits; code != exitRestart {
		t.Fatalf("exit = %d", code)
	}
	cancel()
	<-done
	if marker, present := readMarker(t, home); !present || marker.Boots != 1 {
		t.Fatalf("marker = %+v, %t", marker, present)
	}
	if exit := boot(t, home); exit != exitRestart || <-seams.rollbacks != "restore=false the new build exited before it was promoted" {
		t.Fatalf("next boot exit = %d", exit)
	}
}

func TestTrialBuildThatCrashesAtBootRollsBackOnItsSecondBoot(t *testing.T) {
	home, seams := trialHome(t, install.UpgradeMarker{Target: trialTarget, UserVersion: kernel.SchemaVersion, State: install.UpgradeTrial})
	startupPhaseHook = func(phase string) {
		if phase == "store" {
			panic("crash at boot")
		}
	}
	func() {
		defer func() { _ = recover() }()
		_ = serve(context.Background(), testConfig(home))
	}()
	startupPhaseHook = nil
	if marker, _ := readMarker(t, home); marker.Boots != 1 || len(seams.rollbacks) != 0 {
		t.Fatalf("first boot marker = %+v", marker)
	}
	if exit := boot(t, home); exit != exitRestart {
		t.Fatalf("second boot exit = %d", exit)
	}
	if got := <-seams.rollbacks; got != "restore=false the new build exited before it was promoted" {
		t.Fatalf("rollback = %q", got)
	}
}

func TestTrialBuildFailingVerificationAfterItIsUpRollsBack(t *testing.T) {
	home, seams := trialHome(t, install.UpgradeMarker{Target: trialTarget, UserVersion: kernel.SchemaVersion, State: install.UpgradeTrial})
	seams.verify = errors.New("factory-runner reports another build")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, testConfig(home)) }()
	if code := <-seams.exits; code != exitRestart {
		t.Fatalf("exit = %d", code)
	}
	cancel()
	<-done
	marker, _ := readMarker(t, home)
	if marker.Boots != 1 || !strings.Contains(marker.Reason, "factory-runner reports another build") {
		t.Fatalf("marker = %+v", marker)
	}
	if exit := boot(t, home); exit != exitRestart || !strings.Contains(<-seams.rollbacks, "verification failed") {
		t.Fatalf("next boot exit = %d", exit)
	}
}

func TestHungTrialBuildIsStoppedByTheTrialLimit(t *testing.T) {
	home, seams := trialHome(t, install.UpgradeMarker{Target: trialTarget, UserVersion: kernel.SchemaVersion, State: install.UpgradeTrial})
	trialLimit = 20 * time.Millisecond
	hung := make(chan struct{})
	startupPhaseHook = func(phase string) {
		if phase == "store" {
			<-hung
		}
	}
	defer func() { startupPhaseHook = nil }()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, testConfig(home)) }()
	if code := <-seams.exits; code != exitRestart {
		t.Fatalf("exit = %d", code)
	}
	cancel()
	close(hung)
	<-done
	if marker, _ := readMarker(t, home); marker.Boots != 1 {
		t.Fatalf("marker = %+v", marker)
	}
}

func TestRollbackRestoresTheDatabaseOnlyWhenTheSchemaMoved(t *testing.T) {
	for _, test := range []struct {
		userVersion int
		want        string
	}{{kernel.SchemaVersion - 1, "restore=true"}, {kernel.SchemaVersion, "restore=false"}} {
		home, seams := trialHome(t, install.UpgradeMarker{Target: trialTarget, UserVersion: test.userVersion, Boots: 1, State: install.UpgradeTrial})
		if exit := boot(t, home); exit != exitRestart || !strings.HasPrefix(<-seams.rollbacks, test.want+" ") {
			t.Fatalf("user_version %d: exit %d", test.userVersion, exit)
		}
	}
}

func TestOldBuildRecordsTheRollbackThenRestartsOnlyForANewRelease(t *testing.T) {
	home, _ := trialHome(t, install.UpgradeMarker{Target: trialTarget, UserVersion: kernel.SchemaVersion, State: install.UpgradeRolledBack, Reason: "crashed"})
	selfSource = func() string { return strings.Repeat("6", 40) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, testConfig(home)) }()
	waitOperatorClient(t, home)
	if _, present := readMarker(t, home); present {
		t.Fatal("the old build kept the rolled back marker")
	}
	// A release swaps the binaries, leaves a trial marker and shuts down.
	if err := install.WriteUpgradeMarker(home, install.UpgradeMarker{Target: trialTarget, State: install.UpgradeTrial}); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; !errors.Is(err, errRestart) {
		t.Fatalf("shutdown after a swap = %v", err)
	}
}

func TestTrialBuildStoppedBeforePromotionIsNotRestarted(t *testing.T) {
	home, _ := trialHome(t, install.UpgradeMarker{Target: trialTarget, UserVersion: kernel.SchemaVersion, State: install.UpgradeTrial})
	promoteAfter = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, testConfig(home)) }()
	waitOperatorClient(t, home)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("stopped trial build = %v", err)
	}
}
