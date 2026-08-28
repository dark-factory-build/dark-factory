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
	"github.com/dark-factory-build/dark-factory/internal/install"
)

const testOrigin = "https://factoryd.test.invalid"

func TestParseOwnsOneFreshHomeAndExactLoopbackBrowserPolicy(t *testing.T) {
	home := filepath.Join(t.TempDir(), "factory")
	tests := []struct {
		name string
		args []string
		ok   bool
	}{
		{name: "default", args: []string{"--home", home}, ok: true},
		{name: "configured", args: []string{"--home", home, "--browser-address", "127.0.0.1:43124", "--development-browser-origin", testOrigin}, ok: true},
		{name: "missing home", args: nil},
		{name: "relative home", args: []string{"--home", "relative"}},
		{name: "root home", args: []string{"--home", "/"}},
		{name: "duplicate home", args: []string{"--home", home, "--home", home}},
		{name: "nonloopback", args: []string{"--home", home, "--browser-address", "0.0.0.0:43123"}},
		{name: "localhost", args: []string{"--home", home, "--browser-address", "localhost:43123"}},
		{name: "random production port", args: []string{"--home", home, "--browser-address", "127.0.0.1:0"}},
		{name: "duplicate address", args: []string{"--home", home, "--browser-address", defaultBrowserAddress, "--browser-address", "127.0.0.1:43124"}},
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
			if test.ok && (configuration.home != home || configuration.browserAddress == "" || len(configuration.browserOrigins) == 0) {
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
	idle, err := net.Dial("unix", filepath.Join(home, "runtimes", "factory.sock"))
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
	if _, err := os.Lstat(filepath.Join(home, "runtimes", "factory.sock")); !errors.Is(err, os.ErrNotExist) {
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
	if exit := run(context.Background(), []string{"--home", home, "--browser-address", "127.0.0.1:43124"}, &stdout, &stderr); exit != exitFailure || stdout.Len() != 0 || stderr.String() != "factoryd: runtime unavailable\n" || strings.Contains(stderr.String(), home) {
		t.Fatalf("failure = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
	}
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
	return config{home: home, browserAddress: "127.0.0.1:0", browserOrigins: []string{testOrigin}}
}

func waitOperatorClient(t *testing.T, home string) *api.OperatorClient {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	socket := filepath.Join(home, "runtimes", "factory.sock")
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
	if _, err := os.Lstat(filepath.Join(home, "runtimes", "factory.sock")); !errors.Is(err, os.ErrNotExist) {
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
