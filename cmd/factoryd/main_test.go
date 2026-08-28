//go:build darwin

package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/daemon"
	"github.com/dark-factory-build/dark-factory/internal/install"
)

const testOrigin = "https://factoryd.test.invalid"

func TestVersionDoesNotOpenRuntime(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := run(context.Background(), []string{"--version"}, &stdout, &stderr); exit != 0 || stdout.String() != "factoryd development\n" || stderr.Len() != 0 {
		t.Fatalf("version exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
}

func TestBuildIdentityDoesNotOpenRuntime(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := run(context.Background(), []string{"--build-identity"}, &stdout, &stderr); exit != 0 || !strings.Contains(stdout.String(), `"release":false`) || stderr.Len() != 0 {
		t.Fatalf("build identity exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
}

func TestParseOwnsOneFreshHomeAndExactLoopbackBrowserPolicy(t *testing.T) {
	home := filepath.Join(t.TempDir(), "factory")
	tests := []struct {
		name string
		args []string
		ok   bool
	}{
		{name: "default", args: []string{"--home", home}, ok: true},
		{name: "development origin", args: []string{"--home", home, "--development-browser-origin", testOrigin}, ok: true},
		{name: "missing home", args: nil},
		{name: "relative home", args: []string{"--home", "relative"}},
		{name: "root home", args: []string{"--home", "/"}},
		{name: "duplicate home", args: []string{"--home", home, "--home", home}},
		{name: "nonloopback", args: []string{"--home", home, "--browser-address", "0.0.0.0:43123"}},
		{name: "localhost", args: []string{"--home", home, "--browser-address", "localhost:43123"}},
		{name: "browser address is fixed", args: []string{"--home", home, "--browser-address", "127.0.0.1:43124"}},
		{name: "wildcard origin", args: []string{"--home", home, "--development-browser-origin", "https://*.invalid"}},
		{name: "origin path", args: []string{"--home", home, "--development-browser-origin", testOrigin + "/path"}},
		{name: "duplicate origin", args: []string{"--home", home, "--development-browser-origin", testOrigin, "--development-browser-origin", testOrigin}},
		{name: "production origin is implicit", args: []string{"--home", home, "--development-browser-origin", defaultBrowserOrigin}},
		{name: "unknown", args: []string{"--home", home, "--socket", "/private/socket"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configuration, help, ok := parse(test.args)
			if help || ok != test.ok {
				t.Fatalf("parse = %+v, help=%v, ok=%v", configuration, help, ok)
			}
			if test.ok && (configuration.home != home || configuration.browserAddress != defaultBrowserAddress || len(configuration.browserOrigins) == 0) {
				t.Fatalf("valid configuration = %+v", configuration)
			}
		})
	}
	if _, help, ok := parse([]string{"--help"}); !help || !ok {
		t.Fatalf("help = %v, %v", help, ok)
	}
}

func TestProcessServesAPIAndBrowserThenReleasesExactHome(t *testing.T) {
	home := initializedHome(t)
	ctx, cancel := context.WithCancel(context.Background())
	owner, err := openProcess(ctx, testConfig(home))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- owner.wait(ctx) }()

	client := waitOperatorClient(t, home)
	callContext, callCancel := context.WithTimeout(context.Background(), 3*time.Second)
	health, err := client.Health(callContext)
	callCancel()
	if err != nil || !health.Ready {
		t.Fatalf("health = %+v, %v", health, err)
	}
	callContext, callCancel = context.WithTimeout(context.Background(), 3*time.Second)
	status, err := client.WebStatus(callContext)
	callCancel()
	if err != nil || !status.Ready || status.Address != owner.browser.Addr() || status.Path != "/browser/v1" || len(status.Origins) != 1 || status.Origins[0] != testOrigin {
		t.Fatalf("web status = %+v, %v", status, err)
	}

	address := owner.browser.Addr()
	idle, err := net.Dial("unix", install.LocalAPISocketPath(home))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("factoryd did not join cancelled API handler")
	}
	_ = idle.Close()
	assertReleased(t, home, address)
}

func TestProcessPortCollisionCleansLocalAPIAndHomeAuthority(t *testing.T) {
	home := initializedHome(t)
	blocker, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	configuration := testConfig(home)
	configuration.browserAddress = blocker.Addr().String()
	if owner, err := openProcess(context.Background(), configuration); err == nil || owner != nil {
		if owner != nil {
			_ = owner.close()
		}
		t.Fatalf("colliding browser start = %v, %v", owner, err)
	}
	if _, err := os.Lstat(install.LocalAPISocketPath(home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed startup retained socket: %v", err)
	}
	reopened, err := install.OpenOperationalHome(context.Background(), home)
	if err != nil {
		t.Fatalf("failed startup retained home lock: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSecondProcessCannotSplitHomeOwnershipOrDisruptFirst(t *testing.T) {
	home := initializedHome(t)
	ctx, cancel := context.WithCancel(context.Background())
	owner, err := openProcess(ctx, testConfig(home))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- owner.wait(ctx) }()
	client := waitOperatorClient(t, home)
	if second, err := openProcess(context.Background(), testConfig(home)); err == nil || second != nil {
		if second != nil {
			_ = second.close()
		}
		t.Fatalf("second process = %v, %v", second, err)
	}
	callContext, callCancel := context.WithTimeout(context.Background(), 3*time.Second)
	health, err := client.Health(callContext)
	callCancel()
	if err != nil || !health.Ready {
		t.Fatalf("first process after rejected split = %+v, %v", health, err)
	}
	address := owner.browser.Addr()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertReleased(t, home, address)
}

func TestOwnedListenerFailuresStopAndReleaseWholeProcess(t *testing.T) {
	tests := []struct {
		name string
		stop func(*process) error
	}{
		{name: "local API", stop: func(owner *process) error { return owner.listener.Close() }},
		{name: "browser", stop: func(owner *process) error { return owner.browser.Close() }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := initializedHome(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			owner, err := openProcess(ctx, testConfig(home))
			if err != nil {
				t.Fatal(err)
			}
			address := owner.browser.Addr()
			done := make(chan error, 1)
			go func() { done <- owner.wait(ctx) }()
			if err := test.stop(owner); err != nil {
				t.Fatalf("stop owner: %v", err)
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("unexpected owner stop reported success")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("factoryd did not converge after owned listener stopped")
			}
			assertReleased(t, home, address)
		})
	}
}

func TestRunRedactsStartupFailureAndReportsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := run(context.Background(), nil, &stdout, &stderr); exit != exitUsage || stdout.Len() != 0 || stderr.String() != usage {
		t.Fatalf("usage = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	home := filepath.Join(t.TempDir(), "missing")
	if exit := run(context.Background(), []string{"--home", home}, &stdout, &stderr); exit != exitFailure || stdout.Len() != 0 || stderr.String() != "factoryd: runtime unavailable\n" || strings.Contains(stderr.String(), home) {
		t.Fatalf("failure = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
	}
}

func TestStartupCancellationIsCleanAtEveryStartupPhase(t *testing.T) {
	for _, phase := range []string{"home", "store", "runtime parent", "supervisor spec", "daemon", "local API", "listener", "browser"} {
		t.Run(phase, func(t *testing.T) {
			home := initializedHome(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			startupPhaseHook = func(observed string) {
				if observed == phase {
					cancel()
				}
			}
			defer func() { startupPhaseHook = nil }()
			err := serve(ctx, testConfig(home))
			if err != nil {
				t.Fatalf("startup cancellation = %v", err)
			}
			reopened, err := install.OpenOperationalHome(context.Background(), home)
			if err != nil {
				t.Fatalf("home was not released after clean cancellation: %v", err)
			}
			if err := reopened.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCleanupFailureRetainsDependentAuthorityAndStableCloseResult(t *testing.T) {
	failure := errors.New("injected cleanup failure")
	for _, test := range []struct {
		name string
		set  func()
	}{
		{name: "daemon", set: func() {
			closeDaemon = func(value *daemon.Daemon) error {
				_ = value
				return failure
			}
		}},
		{name: "runtime parent", set: func() {
			calls := 0
			closeRuntimeParent = func(value *daemon.RuntimeParent) error {
				calls++
				if calls == 1 {
					_ = value.Close()
				}
				return failure
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			closeDaemon = func(value *daemon.Daemon) error { return value.Close() }
			closeRuntimeParent = func(value *daemon.RuntimeParent) error { return value.Close() }
			defer func() {
				closeDaemon = func(value *daemon.Daemon) error { return value.Close() }
				closeRuntimeParent = func(value *daemon.RuntimeParent) error { return value.Close() }
			}()
			home := initializedHome(t)
			owner, err := openProcess(context.Background(), testConfig(home))
			if err != nil {
				t.Fatal(err)
			}
			test.set()
			first := owner.close()
			if !errors.Is(first, failure) {
				t.Fatalf("cleanup = %v", first)
			}
			if reopened, openErr := install.OpenOperationalHome(context.Background(), home); openErr == nil {
				_ = reopened.Close()
				t.Fatal("failed cleanup released home authority")
			}
			if second := owner.close(); !errors.Is(second, failure) {
				t.Fatalf("duplicate cleanup = %v", second)
			}
			closeDaemon = func(value *daemon.Daemon) error { return value.Close() }
			closeRuntimeParent = func(value *daemon.RuntimeParent) error { return value.Close() }
			if err := owner.shutdown(); err != nil {
				t.Fatalf("test cleanup = %v", err)
			}
			if reopened, openErr := install.OpenOperationalHome(context.Background(), home); openErr != nil {
				t.Fatalf("cleanup retained home authority: %v", openErr)
			} else if err := reopened.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConcurrentCloseAndWaitReturnOneStableResult(t *testing.T) {
	home := initializedHome(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	owner, err := openProcess(ctx, testConfig(home))
	if err != nil {
		t.Fatal(err)
	}
	address := owner.browser.Addr()
	results := make(chan error, 3)
	go func() { results <- owner.wait(ctx) }()
	go func() { results <- owner.close() }()
	go func() { results <- owner.close() }()
	for index := 0; index < cap(results); index++ {
		if err := <-results; err != nil {
			t.Fatalf("lifecycle result = %v", err)
		}
	}
	if err := owner.wait(ctx); err != nil {
		t.Fatalf("repeated wait = %v", err)
	}
	assertReleased(t, home, address)
}

func initializedHome(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/private/tmp", "dark-factory-factoryd-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	home := filepath.Join(root, "factory")
	if _, err := install.Init(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	return home
}

func testConfig(home string) config {
	return config{
		home: home, browserAddress: "127.0.0.1:0", browserOrigins: []string{testOrigin},
		gitExecutable: defaultGitExecutable, toolPath: defaultToolPath, baseRevision: defaultBaseRevision,
		runnerExecutable: "/bin/sh", factoryctlExecutable: "/bin/sh",
	}
}

func waitOperatorClient(t *testing.T, home string) *api.OperatorClient {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	socket := install.LocalAPISocketPath(home)
	token := filepath.Join(home, "operator.token")
	for time.Now().Before(deadline) {
		client, err := api.NewOperatorClient(socket, token)
		if err == nil {
			return client
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("local API did not become available")
	return nil
}

func assertReleased(t *testing.T, home, browserAddress string) {
	t.Helper()
	if _, err := os.Lstat(install.LocalAPISocketPath(home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket remains: %v", err)
	}
	listener, err := net.Listen("tcp4", browserAddress)
	if err != nil {
		t.Fatalf("browser address remains: %v", err)
	}
	_ = listener.Close()
	reopened, err := install.OpenOperationalHome(context.Background(), home)
	if err != nil {
		t.Fatalf("home authority remains: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestParseSupervisorFlagsAndDefaults(t *testing.T) {
	home := filepath.Join(t.TempDir(), "factory")
	configuration, help, ok := parse([]string{"--home", home})
	if help || !ok || configuration.gitExecutable != defaultGitExecutable || configuration.toolPath != defaultToolPath || configuration.baseRevision != defaultBaseRevision || configuration.runnerExecutable != "" || configuration.factoryctlExecutable != "" {
		t.Fatalf("default supervisor configuration = %+v, help=%v, ok=%v", configuration, help, ok)
	}
	configuration, help, ok = parse([]string{"--home", home, "--git", "/opt/git/bin/git", "--tool-path", "/opt/tools:/usr/bin", "--base-revision", "refs/heads/main", "--runner", "/opt/df/factory-runner", "--factoryctl", "/opt/df/factoryctl"})
	if help || !ok || configuration.gitExecutable != "/opt/git/bin/git" || configuration.toolPath != "/opt/tools:/usr/bin" || configuration.baseRevision != "refs/heads/main" || configuration.runnerExecutable != "/opt/df/factory-runner" || configuration.factoryctlExecutable != "/opt/df/factoryctl" {
		t.Fatalf("explicit supervisor configuration = %+v, help=%v, ok=%v", configuration, help, ok)
	}
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "relative git", args: []string{"--home", home, "--git", "git"}},
		{name: "duplicate git", args: []string{"--home", home, "--git", "/usr/bin/git", "--git", "/usr/bin/git"}},
		{name: "relative tool path entry", args: []string{"--home", home, "--tool-path", "/usr/bin:relative"}},
		{name: "empty tool path", args: []string{"--home", home, "--tool-path", ""}},
		{name: "duplicate tool path", args: []string{"--home", home, "--tool-path", "/usr/bin", "--tool-path", "/usr/bin"}},
		{name: "empty base revision", args: []string{"--home", home, "--base-revision", ""}},
		{name: "option-shaped base revision", args: []string{"--home", home, "--base-revision", "--exec=evil"}},
		{name: "nul base revision", args: []string{"--home", home, "--base-revision", "HEAD\x00"}},
		{name: "duplicate base revision", args: []string{"--home", home, "--base-revision", "HEAD", "--base-revision", "HEAD"}},
		{name: "relative runner", args: []string{"--home", home, "--runner", "factory-runner"}},
		{name: "relative factoryctl", args: []string{"--home", home, "--factoryctl", "factoryctl"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if configuration, help, ok := parse(test.args); help || ok {
				t.Fatalf("invalid supervisor flag accepted: %+v", configuration)
			}
		})
	}
}

func copyExecutable(t *testing.T, destination string) {
	t.Helper()
	content, err := os.ReadFile("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, content, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestDeriveSupervisorSpecResolvesSymlinkedSelfToCommittedSiblings(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	copyExecutable(t, filepath.Join(base, "factoryd"))
	copyExecutable(t, filepath.Join(base, "factory-runner"))
	copyExecutable(t, filepath.Join(base, "factoryctl"))
	link := filepath.Join(base, "factoryd-link")
	if err := os.Symlink(filepath.Join(base, "factoryd"), link); err != nil {
		t.Fatal(err)
	}
	selfExecutable = func() (string, error) { return link, nil }
	defer func() { selfExecutable = os.Executable }()

	home := filepath.Join(base, "home")
	configuration := config{home: home, gitExecutable: defaultGitExecutable, toolPath: defaultToolPath, baseRevision: "refs/heads/main"}
	spec, err := deriveSupervisorSpec(configuration, nil)
	if err != nil {
		t.Fatal(err)
	}
	if spec.RunnerExecutable != filepath.Join(base, "factory-runner") || spec.FactoryctlExecutable != filepath.Join(base, "factoryctl") {
		t.Fatalf("sibling derivation = %+v", spec)
	}
	if spec.GitExecutable != defaultGitExecutable || spec.BaseRevision != "refs/heads/main" || spec.ToolPath != defaultToolPath {
		t.Fatalf("boot inputs = %+v", spec)
	}
	if spec.ChangeParent != filepath.Join(home, "changes") || spec.AttemptSocket != install.LocalAPISocketPath(home) {
		t.Fatalf("home-derived paths = %+v", spec)
	}
}

func TestDeriveSupervisorSpecRefusesMissingAndUnsafeExecutables(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	copyExecutable(t, filepath.Join(base, "factoryd"))
	copyExecutable(t, filepath.Join(base, "factoryctl"))
	selfExecutable = func() (string, error) { return filepath.Join(base, "factoryd"), nil }
	defer func() { selfExecutable = os.Executable }()
	home := filepath.Join(base, "home")
	valid := config{home: home, gitExecutable: defaultGitExecutable, toolPath: defaultToolPath, baseRevision: defaultBaseRevision}

	if _, err := deriveSupervisorSpec(valid, nil); err == nil {
		t.Fatal("missing sibling factory-runner was accepted")
	}
	copyExecutable(t, filepath.Join(base, "factory-runner"))
	if _, err := deriveSupervisorSpec(valid, nil); err != nil {
		t.Fatalf("complete siblings were refused: %v", err)
	}
	if err := os.Chmod(filepath.Join(base, "factory-runner"), 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := deriveSupervisorSpec(valid, nil); err == nil {
		t.Fatal("group and world writable runner was accepted")
	}
	if err := os.Chmod(filepath.Join(base, "factory-runner"), 0o755); err != nil {
		t.Fatal(err)
	}
	broken := valid
	broken.gitExecutable = filepath.Join(base, "missing-git")
	if _, err := deriveSupervisorSpec(broken, nil); err == nil {
		t.Fatal("missing git executable was accepted")
	}
	broken = valid
	broken.baseRevision = ""
	if _, err := deriveSupervisorSpec(broken, nil); err == nil {
		t.Fatal("empty base revision policy was accepted")
	}
}

func TestOpenProcessRefusesUnprovableSupervisorSpecAndReleasesHome(t *testing.T) {
	home := initializedHome(t)
	configuration := testConfig(home)
	configuration.gitExecutable = filepath.Join(home, "missing-git")
	if owner, err := openProcess(context.Background(), configuration); err == nil || owner != nil {
		t.Fatalf("unprovable supervisor specification started a process: %v, %v", owner, err)
	}
	reopened, err := install.OpenOperationalHome(context.Background(), home)
	if err != nil {
		t.Fatalf("home was not released after boot refusal: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBootGitTrustMatchesTheExactPerAttemptAuthority(t *testing.T) {
	if !change.TrustedDeveloperGitPath(defaultGitExecutable) {
		t.Fatalf("default git %q would be refused by the per-attempt trust predicate", defaultGitExecutable)
	}
	if _, err := os.Stat(defaultGitExecutable); err != nil {
		t.Skipf("CommandLineTools git is unavailable: %v", err)
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	selfExecutable = func() (string, error) { return filepath.Join(base, "factoryd"), nil }
	defer func() { selfExecutable = os.Executable }()
	copyExecutable(t, filepath.Join(base, "factoryd"))
	copyExecutable(t, filepath.Join(base, "factory-runner"))
	copyExecutable(t, filepath.Join(base, "factoryctl"))
	home := filepath.Join(base, "home")
	valid := config{home: home, gitExecutable: defaultGitExecutable, toolPath: defaultToolPath, baseRevision: defaultBaseRevision}
	if _, err := deriveSupervisorSpec(valid, nil); err != nil {
		t.Fatalf("toolchain default git was refused at boot: %v", err)
	}
	for _, untrusted := range []string{"/usr/bin/git", "/bin/sh"} {
		broken := valid
		broken.gitExecutable = untrusted
		_, err := deriveSupervisorSpec(broken, nil)
		if err == nil {
			t.Fatalf("untrusted git %q was accepted at boot", untrusted)
		}
		if untrusted == "/usr/bin/git" && !strings.Contains(err.Error(), "trusted Developer toolchain") {
			t.Fatalf("refusal does not name the trust requirement: %v", err)
		}
	}
}
