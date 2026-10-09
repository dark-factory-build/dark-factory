package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/install"
)

func TestParseServiceStatusIsOneExplicitCommand(t *testing.T) {
	home := "/private/tmp/factory"
	command, help, ok := parse([]string{"service", "status", "--home", home})
	if !ok || help || !reflect.DeepEqual(command, attemptCommand{kind: commandServiceStatus, home: home}) {
		t.Fatalf("parse = %+v, help=%t, ok=%t", command, help, ok)
	}
	for verb, kind := range map[string]commandKind{
		"install": commandServiceInstall, "start": commandServiceStart,
		"stop": commandServiceStop, "uninstall": commandServiceUninstall,
	} {
		command, help, ok := parse([]string{"service", verb, "--home", home, "--label", "com.dark-factory.e2e.x", "--plist-dir", "/private/tmp/plists"})
		want := attemptCommand{kind: kind, home: home, label: "com.dark-factory.e2e.x", plistDir: "/private/tmp/plists"}
		if !ok || help || !reflect.DeepEqual(command, want) {
			t.Fatalf("parse service %s = %+v, help=%t, ok=%t", verb, command, help, ok)
		}
	}
	for _, args := range [][]string{
		{"service"},
		{"service", "status"},
		{"service", "install"},
		{"service", "reload", "--home", home},
		{"service", "status", "--home", "relative"},
		{"service", "status", "--home", "/"},
		{"service", "status", "--home", home, "extra"},
		{"service", "install", "--home", home, "--home", home},
		{"service", "install", "--home", home, "--label", ""},
		{"service", "install", "--home", home, "--plist-dir", "relative"},
		{"service", "status", "--home=" + home},
		{"service", "status", "--home=" + home, "--"},
		{"service", "status", "-home", home},
		{"service", "status", "--home", home, "--"},
		{"service_status", "--home", home},
	} {
		if _, _, ok := parse(args); ok {
			t.Fatalf("invalid service syntax accepted: %q", args)
		}
	}
	for _, args := range [][]string{{"service", "--help"}, {"service", "status", "--help"}} {
		if _, help, ok := parse(args); !ok || !help {
			t.Fatalf("service help rejected: %q", args)
		}
	}
}

func TestParseServiceInstallConfigurationIsInstallOnlyAndExact(t *testing.T) {
	home := "/private/tmp/factory"
	const origin = "wss://relay&.example"
	const address = "127.0.0.1:0"
	command, help, ok := parse([]string{"service", "install", "--home", home, "--relay-origin", origin, "--development-browser-address", address})
	if !ok || help || !reflect.DeepEqual(command, attemptCommand{kind: commandServiceInstall, home: home, relayOrigin: origin, browserAddress: address}) {
		t.Fatalf("parse = %+v, help=%t, ok=%t", command, help, ok)
	}
	if config := serviceConfigFor(command); config.RelayOrigin != origin || config.DevelopmentBrowserAddress != address {
		t.Fatalf("service config = %+v", config)
	}
	// Omitting the flag installs exactly as before.
	command, _, ok = parse([]string{"service", "install", "--home", home})
	if !ok || serviceConfigFor(command).RelayOrigin != "" {
		t.Fatalf("bare install carried a relay origin: %+v", command)
	}
	for _, args := range [][]string{
		// Only install renders a plist; the other verbs read the receipt.
		{"service", "status", "--home", home, "--relay-origin", origin},
		{"service", "uninstall", "--home", home, "--relay-origin", origin},
		{"service", "status", "--home", home, "--development-browser-address", address},
		{"service", "uninstall", "--home", home, "--development-browser-address", address},
		// The connector's own grammar bounds the flag.
		{"service", "install", "--home", home, "--relay-origin", ""},
		{"service", "install", "--home", home, "--relay-origin", "https://relay.darkfactory.build"},
		{"service", "install", "--home", home, "--relay-origin", "wss://relay.darkfactory.build/host"},
		{"service", "install", "--home", home, "--relay-origin", "wss://relay.darkfactory.build?x=1"},
		{"service", "install", "--home", home, "--relay-origin", "wss://user@relay.darkfactory.build"},
		{"service", "install", "--home", home, "--relay-origin", "wss://"},
		{"service", "install", "--home", home, "--development-browser-address", "localhost:43124"},
		{"service", "install", "--home", home, "--development-browser-address", "127.0.0.1:65536"},
		{"service", "install", "--home", home, "--development-browser-address", ""},
		{"service", "install", "--home", home, "--relay-origin", strings.Repeat("w", install.MaxRelayOriginBytes)},
		// Repeating any service flag is a syntax error, this one included.
		{"service", "install", "--home", home, "--relay-origin", origin, "--relay-origin", origin},
	} {
		if _, _, ok := parse(args); ok {
			t.Fatalf("invalid relay origin syntax accepted: %q", args)
		}
	}
}

// TestInstallPairsTheBrowserExactlyOnce proves the outcomes of the
// post-install pairing: a service this command started mints one link over the
// operator API with the home's own token and opens it once, a repeat install
// that found the service already there opens nothing, and a factory that never
// answers is reported as not opened, bounded by cancellation.
func TestInstallPairsTheBrowserExactlyOnce(t *testing.T) {
	for _, test := range []struct {
		name     string
		existing install.ServiceState
		state    install.ServiceState
		opens    bool
	}{
		{name: "fresh install", existing: install.ServiceAbsent, state: install.ServiceRunning, opens: true},
		{name: "unknown prior state", state: install.ServiceRunning, opens: true},
		{name: "already running", existing: install.ServiceRunning, state: install.ServiceRunning},
		{name: "already installed", existing: install.ServiceInstalled, state: install.ServiceRunning},
		{name: "installed but not started", existing: install.ServiceAbsent, state: install.ServiceInstalled},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := installStartedService(test.existing, test.state); got != test.opens {
				t.Fatalf("installStartedService(%q, %q) = %t", test.existing, test.state, got)
			}
		})
	}

	const link = "https://app.darkfactory.build/#df_pair=00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	done := serveOne(fixture.listener, func(call api.Call) api.Reply {
		if call.Kind() != api.CallWebPair {
			return mustWebErrorReply(t, api.RemoteInvalidRequest)
		}
		return api.NewContentReply(api.WebPair{Link: link})
	})
	opened := []string{}
	if !openPairedBrowser(context.Background(), filepath.Join(fixture.directory, "home"), func(_ context.Context, value string) error {
		opened = append(opened, value)
		return nil
	}) || len(opened) != 1 || opened[0] != link {
		t.Fatalf("open = %q", opened)
	}
	if result := awaitServer(t, done); result.err != nil {
		t.Fatal(result.err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if openPairedBrowser(ctx, filepath.Join(t.TempDir(), "missing"), func(context.Context, string) error {
		t.Fatal("opened without a link")
		return nil
	}) || time.Since(start) > 5*time.Second {
		t.Fatalf("a cancelled pairing reported success or took %s", time.Since(start))
	}
}

func TestServiceStatusCLIUsesExactReadOnlyInspectorAndBoundedOutput(t *testing.T) {
	home := "/private/tmp/factory-private-sentinel"
	calls := 0
	inspector := func(ctx context.Context, gotHome string) (install.ServiceStatus, error) {
		calls++
		if ctx == nil || gotHome != home {
			t.Fatalf("inspector = ctx %v, home %q", ctx, gotHome)
		}
		return install.ServiceStatus{State: install.ServiceAbsent}, nil
	}
	lookups := []string{}
	var stdout, stderr bytes.Buffer
	exit := runWithDependencies(context.Background(), []string{"service", "status", "--home", home}, func(name string) string {
		lookups = append(lookups, name)
		return "/private/tmp/user-private-sentinel"
	}, &stdout, &stderr, nil, inspector)
	if exit != 0 || calls != 1 || stdout.String() != "{\"state\":\"absent\"}\n" || stderr.Len() != 0 {
		t.Fatalf("status = exit %d calls %d stdout %q stderr %q", exit, calls, stdout.String(), stderr.String())
	}
	if len(lookups) != 0 {
		t.Fatalf("environment lookups = %q", lookups)
	}
	if strings.Contains(stdout.String()+stderr.String(), "private-sentinel") || strings.Contains(stdout.String()+stderr.String(), "credential") {
		t.Fatal("service status output leaked private input")
	}
}

func TestServiceStatusCLIMapsFailuresWithoutPrivateDiagnostics(t *testing.T) {
	private := "private-platform-diagnostic"
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "ambiguous", err: errors.Join(install.ErrServiceAmbiguous, errors.New(private)), want: "factoryctl: the service operation is ambiguous; inspect the home and launchd state\n"},
		{name: "launchctl", err: errors.Join(install.ErrServiceLaunchctl, errors.New(private)), want: "factoryctl: the service operation is ambiguous; inspect the home and launchd state\n"},
		{name: "home", err: errors.Join(install.ErrInvalidHome, errors.New(private)), want: "factoryctl: service operations require an exact Go home\n"},
		{name: "foreign", err: errors.Join(install.ErrServiceForeign, errors.New(private)), want: "factoryctl: a service artifact is not this installation's property; refusing\n"},
		{name: "residue", err: errors.Join(install.ErrServiceResidue, errors.New(private)), want: "factoryctl: service residue found; run factoryctl service uninstall first\n"},
		{name: "canceled", err: context.Canceled, want: "factoryctl: service operation canceled\n"},
		{name: "deadline", err: context.DeadlineExceeded, want: "factoryctl: service operation timed out\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exit := runWithDependencies(context.Background(), []string{"service", "status", "--home", "/private/tmp/factory"}, func(string) string {
				return "/private/tmp/user"
			}, &stdout, &stderr, nil, func(context.Context, string) (install.ServiceStatus, error) {
				return install.ServiceStatus{State: install.ServiceAmbiguous, PID: 731}, test.err
			})
			if exit != exitFailure || stdout.Len() != 0 || stderr.String() != test.want || strings.Contains(stderr.String(), private) || strings.Contains(stderr.String(), "731") {
				t.Fatalf("failure = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
			}
		})
	}
}

func TestServiceStatusCLIRefusesMissingHomeOrNonAbsentProjection(t *testing.T) {
	for _, test := range []struct {
		name      string
		userHome  string
		inspector serviceInspector
	}{
		{name: "alternate HOME ignored", userHome: "/private/tmp/user", inspector: func(context.Context, string) (install.ServiceStatus, error) {
			return install.ServiceStatus{State: install.ServiceAbsent}, nil
		}},
		{name: "ambiguous success", userHome: "/private/tmp/user", inspector: func(context.Context, string) (install.ServiceStatus, error) {
			return install.ServiceStatus{State: install.ServiceAmbiguous, PID: 731}, nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exit := runWithDependencies(context.Background(), []string{"service", "status", "--home", "/private/tmp/factory"}, func(string) string {
				return test.userHome
			}, &stdout, &stderr, nil, test.inspector)
			if test.name == "alternate HOME ignored" {
				if exit != 0 || stdout.String() != "{\"state\":\"absent\"}\n" || stderr.Len() != 0 {
					t.Fatalf("alternate HOME affected status: exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
				}
			} else if exit != exitFailure || stdout.Len() != 0 || stderr.Len() == 0 {
				t.Fatalf("refusal = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
			}
		})
	}
}

func TestServiceToolchainFlagsAreInstallOnly(t *testing.T) {
	flags := []string{"--tool-path", "/opt/software/node/bin:/usr/bin:/bin", "--toolchain-read-roots", "/opt/software/node"}
	command, help, ok := parse(append([]string{"service", "install", "--home", "/private/factory/home"}, flags...))
	if !ok || help {
		t.Fatal("toolchain install flags refused")
	}
	config := serviceConfigFor(command)
	if config.ToolPath != flags[1] || config.ToolchainReadRoots != flags[3] {
		t.Fatalf("lost toolchain configuration: %+v", config)
	}
	for _, verb := range []string{"status", "uninstall"} {
		if _, _, ok := parse(append([]string{"service", verb, "--home", "/private/factory/home"}, flags...)); ok {
			t.Fatalf("%s accepted install authority", verb)
		}
	}
}
