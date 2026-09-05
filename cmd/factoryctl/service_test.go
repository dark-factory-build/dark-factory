//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/install"
)

func TestParseServiceStatusIsOneExplicitCommand(t *testing.T) {
	home := "/private/tmp/factory"
	command, help, ok := parse([]string{"service", "status", "--home", home})
	if !ok || help || command != (attemptCommand{kind: commandServiceStatus, home: home}) {
		t.Fatalf("parse = %+v, help=%t, ok=%t", command, help, ok)
	}
	for verb, kind := range map[string]commandKind{
		"install": commandServiceInstall, "start": commandServiceStart,
		"stop": commandServiceStop, "uninstall": commandServiceUninstall,
	} {
		command, help, ok := parse([]string{"service", verb, "--home", home, "--label", "com.dark-factory.e2e.x", "--plist-dir", "/private/tmp/plists"})
		want := attemptCommand{kind: kind, home: home, label: "com.dark-factory.e2e.x", plistDir: "/private/tmp/plists"}
		if !ok || help || command != want {
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

func TestParseServiceInstallRelayOriginIsInstallOnlyAndExact(t *testing.T) {
	home := "/private/tmp/factory"
	const origin = "wss://relay.darkfactory.build"
	command, help, ok := parse([]string{"service", "install", "--home", home, "--relay-origin", origin})
	if !ok || help || command != (attemptCommand{kind: commandServiceInstall, home: home, relayOrigin: origin}) {
		t.Fatalf("parse = %+v, help=%t, ok=%t", command, help, ok)
	}
	if config := serviceConfigFor(command); config.RelayOrigin != origin {
		t.Fatalf("service config relay origin = %q", config.RelayOrigin)
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
		// The connector's own grammar bounds the flag.
		{"service", "install", "--home", home, "--relay-origin", ""},
		{"service", "install", "--home", home, "--relay-origin", "https://relay.darkfactory.build"},
		{"service", "install", "--home", home, "--relay-origin", "wss://relay.darkfactory.build/host"},
		{"service", "install", "--home", home, "--relay-origin", "wss://relay.darkfactory.build?x=1"},
		{"service", "install", "--home", home, "--relay-origin", "wss://user@relay.darkfactory.build"},
		{"service", "install", "--home", home, "--relay-origin", "wss://"},
		{"service", "install", "--home", home, "--relay-origin", strings.Repeat("w", install.MaxRelayOriginBytes)},
		// Repeating any service flag is a syntax error, this one included.
		{"service", "install", "--home", home, "--relay-origin", origin, "--relay-origin", origin},
	} {
		if _, _, ok := parse(args); ok {
			t.Fatalf("invalid relay origin syntax accepted: %q", args)
		}
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
		{name: "unsupported", err: install.ErrUnsupported, want: "factoryctl: service operations are unsupported on this platform\n"},
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

func TestServiceCLIPrintsTheChangedRelayOriginRefusal(t *testing.T) {
	const origin = "wss://relay.darkfactory.build"
	var stdout, stderr bytes.Buffer
	exit := runWithDependencies(context.Background(), []string{"service", "status", "--home", "/private/tmp/factory"}, func(string) string {
		return "/private/tmp/user"
	}, &stdout, &stderr, nil, func(context.Context, string) (install.ServiceStatus, error) {
		return install.ServiceStatus{}, fmt.Errorf("%w %q; run factoryctl service uninstall first", install.ErrServiceRelayOrigin, origin)
	})
	if exit != exitFailure || stdout.Len() != 0 {
		t.Fatalf("refusal = exit %d, stdout %q", exit, stdout.String())
	}
	// Without both halves the operator cannot tell what is installed or how to
	// replace it, which is the whole point of refusing instead of no-opping.
	if !strings.Contains(stderr.String(), origin) || !strings.Contains(stderr.String(), "service uninstall") {
		t.Fatalf("refusal stderr = %q", stderr.String())
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
