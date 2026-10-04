//go:build darwin

package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/buildinfo"
	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// TestBlackBoxServiceLifecycle proves the managed launchd installation with
// the real binaries, the real launchctl, and one disposable unique label in
// the user's gui domain. Every file lives under a temporary root; the
// operator's home, ~/.dark-factory, and the production label are never
// touched. Cleanup boots the label out and removes the root even on failure.
func TestBlackBoxServiceLifecycle(t *testing.T) {
	if os.Getenv("DARK_FACTORY_SERVICE_E2E") != "1" {
		t.Skip("service E2E runs only under scripts/go-service-e2e.sh")
	}
	fixture := newBlackBoxFixture(t)
	label := os.Getenv("DARK_FACTORY_E2E_SERVICE_LABEL")
	if !strings.HasPrefix(label, "com.dark-factory.e2e.") {
		t.Fatalf("the harness must mint the disposable label; got %q", label)
	}
	plistDir := filepath.Join(fixture.root, "plists")
	if err := os.Mkdir(plistDir, 0o700); err != nil {
		t.Fatal(err)
	}
	serviceArgs := func(verb string) []string {
		args := []string{"service", verb, "--home", fixture.home, "--label", label, "--plist-dir", plistDir}
		if verb == "install" {
			args = append(args, "--development-browser-address", "127.0.0.1:0")
		}
		return args
	}
	t.Cleanup(func() {
		// Guaranteed teardown: the disposable label must not survive the test
		// process regardless of which assertion failed.
		_ = exec.Command("/bin/launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Geteuid(), label)).Run()
		awaitNoHomeProcesses(t, fixture.home, 20*time.Second)
	})

	// Install: launchd starts factoryd from the sibling service directory.
	output := fixture.runFactoryctl(t, 0, serviceArgs("install")...)
	state := serviceState(t, output)
	if state.State != "running" || state.PID <= 1 {
		t.Fatalf("install state = %+v (%s)", state, output)
	}
	// A fresh install names the pair page and reports whether it opened it,
	// inside the JSON: the whole result is one parseable document. runFactoryctl
	// gives the child no PATH, so `open` is not findable and no browser can be
	// launched — which is also what keeps this test from throwing a window onto
	// the operator's screen.
	if state.BrowserOpened {
		t.Fatalf("install pairing report = %+v (%s)", state, output)
	}
	stderr, err := os.Lstat(filepath.Join(install.ServiceDirectoryPath(fixture.home), "factoryd.stderr.log"))
	if err != nil {
		t.Fatalf("service stderr = %v", err)
	}
	stat, ok := stderr.Sys().(*syscall.Stat_t)
	if !ok || stderr.Mode().Type() != 0 || stderr.Mode().Perm() != 0o600 || stat.Uid != uint32(os.Geteuid()) {
		t.Fatalf("service stderr metadata = mode %v uid %v", stderr.Mode(), stat)
	}
	client := fixture.waitClient(t, serviceStartupOutput(filepath.Join(install.ServiceDirectoryPath(fixture.home), "factoryd.stderr.log")))
	web, err := client.WebStatus(context.Background())
	if err != nil || !web.Ready || web.Address == "127.0.0.1:43123" {
		t.Fatalf("disposable browser status = %+v, %v", web, err)
	}
	pairAddress := "http://" + web.Address + "/pair"
	connection, err := net.DialTimeout("tcp", web.Address, time.Second)
	if err != nil {
		t.Fatalf("disposable pair listener %q: %v", pairAddress, err)
	}
	_ = connection.Close()

	// The managed daemon serves a real task end to end.
	project := fixture.operatorID(t, fixture.runFactoryctl(t, 0, "project", "create", "--name", "Managed", "--root", fixture.repo))
	agent := fixture.operatorID(t, fixture.runFactoryctl(t, 0, "agent", "create", "--project", project, "--name", "Service Smith", "--provider", "shell", "--tool-budget", "4"))
	task := fixture.operatorID(t, fixture.runFactoryctl(t, 0, "task", "add", "--project", project, "--agent", agent, "--title", "Managed service run", "--body", happyPathBody))
	fixture.runFactoryctl(t, 0, "dispatch", "on")
	fixture.awaitTaskStatus(t, client, task, "succeeded", 60*time.Second)

	// Status proves running through the receipt.
	state = serviceState(t, fixture.runFactoryctl(t, 0, serviceArgs("status")...))
	if state.State != "running" || state.PID <= 1 {
		t.Fatalf("running status = %+v", state)
	}

	// KeepAlive SuccessfulExit=false: launchd restarts a daemon that dies
	// unsuccessfully, after its default 10s throttle, with a new pid.
	if err := syscall.Kill(state.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	killed := state.PID
	awaitSocketGone(t, install.LocalAPISocketPath(fixture.home), 5*time.Second)
	// While launchd waits out the throttle ("spawn scheduled") the service is
	// installed with no pid, not ambiguous, so stop and uninstall still work.
	for deadline := time.Now().Add(5 * time.Second); state.State != "installed" || state.PID != 0; time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("killed daemon status = %+v", state)
		}
		state = serviceState(t, fixture.runFactoryctl(t, 0, serviceArgs("status")...))
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(250 * time.Millisecond) {
		if connection, err := net.DialTimeout("unix", install.LocalAPISocketPath(fixture.home), time.Second); err == nil {
			_ = connection.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("launchd did not restart the killed daemon")
		}
	}
	client = fixture.waitClient(t, serviceStartupOutput(filepath.Join(install.ServiceDirectoryPath(fixture.home), "factoryd.stderr.log")))
	if state = serviceState(t, fixture.runFactoryctl(t, 0, serviceArgs("status")...)); state.State != "running" || state.PID <= 1 || state.PID == killed {
		t.Fatalf("restarted status = %+v, killed pid %d", state, killed)
	}

	// Stop unloads the job, the daemon exits, and the socket dies.
	state = serviceState(t, fixture.runFactoryctl(t, 0, serviceArgs("stop")...))
	if state.State != "installed" || state.PID != 0 {
		t.Fatalf("stop state = %+v", state)
	}
	// An intentional stop stays stopped beyond the restart throttle.
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
		if state = serviceState(t, fixture.runFactoryctl(t, 0, serviceArgs("status")...)); state.State != "installed" || state.PID != 0 {
			t.Fatalf("stopped daemon came back: %+v", state)
		}
	}
	awaitSocketGone(t, install.LocalAPISocketPath(fixture.home), 20*time.Second)
	// launchd absence and a closed listener do not prove that factoryd has
	// joined its scheduler and released the home. Do not bootstrap a second
	// owner until the first process has actually left.
	awaitNoHomeProcesses(t, fixture.home, 20*time.Second)
	state = serviceState(t, fixture.runFactoryctl(t, 0, serviceArgs("status")...))
	if state.State != "installed" {
		t.Fatalf("stopped status = %+v", state)
	}

	// Start serves again: a real restart cycle through launchd.
	state = serviceState(t, fixture.runFactoryctl(t, 0, serviceArgs("start")...))
	if state.State != "running" && state.State != "installed" {
		t.Fatalf("start state = %+v", state)
	}
	client = fixture.waitClient(t, serviceStartupOutput(filepath.Join(install.ServiceDirectoryPath(fixture.home), "factoryd.stderr.log")))
	second := fixture.operatorID(t, fixture.runFactoryctl(t, 0, "task", "add", "--project", project, "--agent", agent, "--title", "Managed restart run", "--body", happyPathBody))
	fixture.awaitTaskStatus(t, client, second, "succeeded", 60*time.Second)

	// A release swaps in release binaries; the SIGTERMed daemon exits 75 and
	// launchd starts them on trial, and they promote once up and verified.
	released := buildReleaseBinaries(t, strings.Repeat("1", 40))
	if err := install.ServiceUpgrade(context.Background(), fixture.home, released.directory, released.identity, kernel.SchemaVersion); err != nil {
		t.Fatal(err)
	}
	restartService(t, fixture, serviceArgs, syscall.SIGTERM)
	awaitServiceBuild(t, fixture, released.identity.Source())
	awaitUpgradeSettled(t, fixture.home)
	// A trial build that dies before promotion is rolled back on its next
	// boot, and the previous build serves again.
	trial := buildReleaseBinaries(t, strings.Repeat("2", 40))
	if err := install.ServiceUpgrade(context.Background(), fixture.home, trial.directory, trial.identity, kernel.SchemaVersion); err != nil {
		t.Fatal(err)
	}
	restartService(t, fixture, serviceArgs, syscall.SIGTERM)
	awaitServiceBuild(t, fixture, trial.identity.Source())
	restartService(t, fixture, serviceArgs, syscall.SIGKILL)
	awaitServiceBuild(t, fixture, released.identity.Source())
	awaitUpgradeSettled(t, fixture.home)
	if state = serviceState(t, fixture.runFactoryctl(t, 0, serviceArgs("status")...)); state.State != "running" {
		t.Fatalf("rolled back status = %+v", state)
	}

	// Uninstall removes the job and every artifact; absence is provable.
	state = serviceState(t, fixture.runFactoryctl(t, 0, serviceArgs("uninstall")...))
	if state.State != "absent" {
		t.Fatalf("uninstall state = %+v", state)
	}
	awaitSocketGone(t, install.LocalAPISocketPath(fixture.home), 20*time.Second)
	if _, err := os.Lstat(install.ServiceDirectoryPath(fixture.home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("service directory survived uninstall")
	}
	if _, err := os.Lstat(filepath.Join(plistDir, label+".plist")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("plist survived uninstall")
	}
	print := exec.Command("/bin/launchctl", "print", fmt.Sprintf("gui/%d/%s", os.Geteuid(), label))
	if err := print.Run(); err == nil {
		t.Fatal("launchd job survived uninstall")
	} else {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 113 {
			t.Fatalf("launchctl print after uninstall = %v", err)
		}
	}
	state = serviceState(t, fixture.runFactoryctl(t, 0, serviceArgs("status")...))
	if state.State != "absent" {
		t.Fatalf("final status = %+v", state)
	}
	awaitNoHomeProcesses(t, fixture.home, 20*time.Second)
}

type releaseBinaries struct {
	directory string
	identity  buildinfo.Identity
}

// buildReleaseBinaries builds this tree's three binaries as a release of
// source, the way factoryd builds one.
func buildReleaseBinaries(t *testing.T, source string) releaseBinaries {
	t.Helper()
	identity, ok := buildinfo.Expected("0.0.0", source, runtime.GOOS+"/"+runtime.GOARCH)
	if !ok {
		t.Fatal("invalid release identity")
	}
	directory := t.TempDir()
	for _, name := range []string{"factoryd", "factoryctl", "factory-runner"} {
		output := filepath.Join(directory, name)
		command := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags", "-s -w -X github.com/dark-factory-build/dark-factory/internal/buildinfo.receipt="+identity.Receipt(), "-o", output, "../../cmd/"+name)
		command.Env = append(os.Environ(), "CGO_ENABLED=0")
		if log, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", name, err, log)
		}
		if err := os.Chmod(output, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return releaseBinaries{directory: directory, identity: identity}
}

// restartService signals the running daemon and waits for launchd to start
// another one.
func restartService(t *testing.T, fixture *blackBoxFixture, serviceArgs func(string) []string, signal syscall.Signal) {
	t.Helper()
	before := serviceState(t, fixture.runFactoryctl(t, 0, serviceArgs("status")...))
	if before.State != "running" {
		t.Fatalf("status before restart = %+v", before)
	}
	if err := syscall.Kill(before.PID, signal); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(250 * time.Millisecond) {
		// Status may be refused while launchd is between processes.
		var state serviceStateOutput
		output, _ := exec.Command(fixture.factoryctl, serviceArgs("status")...).Output()
		if json.Unmarshal(output, &state) == nil && state.State == "running" && state.PID != before.PID {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("launchd did not restart pid %d: %+v", before.PID, state)
		}
	}
}

func awaitServiceBuild(t *testing.T, fixture *blackBoxFixture, source string) {
	t.Helper()
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(250 * time.Millisecond) {
		if client, err := api.NewOperatorClient(install.LocalAPISocketPath(fixture.home), filepath.Join(fixture.home, "operator.token")); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			web, err := client.WebStatus(ctx)
			cancel()
			if err == nil && web.Build.Source == source {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the service never served build %s", source)
		}
	}
}

// awaitUpgradeSettled waits for promotion (60s after the trial build is up)
// or for the old build to record a rollback: either removes the marker.
func awaitUpgradeSettled(t *testing.T, home string) {
	t.Helper()
	for deadline := time.Now().Add(120 * time.Second); ; time.Sleep(time.Second) {
		if _, present, err := install.ReadUpgradeMarker(home); err == nil && !present {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the upgrade never settled")
		}
	}
}

func serviceStartupOutput(path string) func() string {
	return func() string {
		output, err := os.ReadFile(path)
		if err != nil {
			return ""
		}
		return string(output)
	}
}

type serviceStateOutput struct {
	State         string `json:"state"`
	PID           int    `json:"pid"`
	PairPage      string `json:"pair_page"`
	BrowserOpened bool   `json:"browser_opened"`
}

func serviceState(t *testing.T, output string) serviceStateOutput {
	t.Helper()
	var state serviceStateOutput
	if err := json.Unmarshal([]byte(output), &state); err != nil {
		t.Fatalf("service output %q: %v", output, err)
	}
	return state
}

func awaitSocketGone(t *testing.T, socket string, patience time.Duration) {
	t.Helper()
	deadline := time.Now().Add(patience)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("unix", socket, time.Second)
		if err != nil {
			return
		}
		_ = connection.Close()
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the daemon socket kept accepting after stop")
}

// awaitNoHomeProcesses is the process census: nothing referencing the
// temporary home may survive teardown.
func awaitNoHomeProcesses(t *testing.T, home string, patience time.Duration) {
	t.Helper()
	deadline := time.Now().Add(patience)
	for {
		command := exec.Command("/usr/bin/pgrep", "-f", home)
		output, err := command.Output()
		if err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == 1 {
				return
			}
			t.Fatalf("pgrep: %v", err)
		}
		if time.Now().After(deadline) {
			survivors := strings.ReplaceAll(strings.TrimSpace(string(output)), "\n", ",")
			t.Fatalf("processes referencing the home survived: pids %s", survivors)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
