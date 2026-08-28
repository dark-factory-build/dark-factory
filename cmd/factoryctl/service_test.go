//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"errors"
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
	for _, args := range [][]string{
		{"service"},
		{"service", "install", "--home", home},
		{"service", "start", "--home", home},
		{"service", "stop", "--home", home},
		{"service", "status"},
		{"service", "status", "--home", "relative"},
		{"service", "status", "--home", "/"},
		{"service", "status", "--home", home, "extra"},
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
		{name: "ambiguous", err: errors.Join(install.ErrServiceAmbiguous, errors.New(private)), want: "factoryctl: service status is ambiguous\n"},
		{name: "launchctl", err: errors.Join(install.ErrServiceLaunchctl, errors.New(private)), want: "factoryctl: service status is ambiguous\n"},
		{name: "home", err: errors.Join(install.ErrInvalidHome, errors.New(private)), want: "factoryctl: service status requires an exact fresh Go home\n"},
		{name: "unsupported", err: install.ErrUnsupported, want: "factoryctl: service status is unsupported on this platform\n"},
		{name: "canceled", err: context.Canceled, want: "factoryctl: service status canceled\n"},
		{name: "deadline", err: context.DeadlineExceeded, want: "factoryctl: service status timed out\n"},
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
