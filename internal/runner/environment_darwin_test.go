//go:build darwin

package runner

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var shellEnvironmentNames = []string{
	"DARK_FACTORY_SOCKET",
	"DARK_FACTORY_ATTEMPT_TOKEN_FILE",
	"HOME",
	"TMPDIR",
	"PATH",
	"LANG",
	"LC_ALL",
	"TERM",
	"NO_COLOR",
	"GIT_CEILING_DIRECTORIES",
	"GIT_DISCOVERY_ACROSS_FILESYSTEM",
	"GIT_CONFIG_NOSYSTEM",
	"GIT_CONFIG_GLOBAL",
	"GIT_TERMINAL_PROMPT",
	"GIT_ASKPASS",
	"GIT_SSH_COMMAND",
	"GH_CONFIG_DIR",
}

func TestPrepareExecSpecAcceptsOnlyClosedShellEnvironmentNames(t *testing.T) {
	for _, name := range append(append([]string{}, shellEnvironmentNames...), "LC_CTYPE", "SHELL", "USER", "LOGNAME") {
		if !allowedEnv(name) {
			t.Errorf("required environment name %q rejected", name)
		}
	}

	forbidden := []string{
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "CODEX_API_KEY",
		"SSH_AUTH_SOCK", "GIT_SSH", "GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0",
		"GIT_DIR", "GIT_WORK_TREE", "GH_TOKEN", "GITHUB_TOKEN",
		"DYLD_INSERT_LIBRARIES", "DYLD_LIBRARY_PATH", "LD_PRELOAD", "LD_LIBRARY_PATH",
		"DARK_FACTORY_HOME", "DARK_FACTORY_RUN_ID", "DARK_FACTORY_TASK_ID", "ARBITRARY",
	}
	missing := filepath.Join(t.TempDir(), "must-not-be-observed")
	for _, name := range forbidden {
		t.Run(name, func(t *testing.T) {
			if allowedEnv(name) {
				t.Fatalf("forbidden environment name %q allowed", name)
			}
			_, err := PrepareExecSpec(ExecSpec{Target: missing, Cwd: missing, Env: []string{name + "=private"}})
			if err == nil || !strings.Contains(err.Error(), "environment key") {
				t.Fatalf("forbidden environment was not rejected before path commitment: %v", err)
			}
		})
	}
}

func TestPrepareExecSpecRejectsDuplicateAndMalformedEnvironmentBeforePaths(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "must-not-be-observed")
	tests := []struct {
		name string
		env  []string
		want string
	}{
		{name: "same duplicate", env: []string{"PATH=/usr/bin", "PATH=/usr/bin"}, want: "duplicate environment"},
		{name: "different-value duplicate", env: []string{"PATH=/usr/bin", "PATH=/bin"}, want: "duplicate environment"},
		{name: "case-distinct remains distinct and unallowed", env: []string{"PATH=/usr/bin", "path=/bin"}, want: "environment key"},
		{name: "missing separator", env: []string{"PATH"}, want: "invalid environment"},
		{name: "empty name", env: []string{"=value"}, want: "invalid environment"},
		{name: "empty value", env: []string{"PATH="}, want: "invalid environment"},
		{name: "name NUL", env: []string{"PA\x00TH=value"}, want: "invalid environment"},
		{name: "value NUL", env: []string{"PATH=va\x00lue"}, want: "invalid environment"},
		{name: "invalid UTF-8 name", env: []string{"LA" + string([]byte{0xff}) + "G=value"}, want: "invalid environment"},
		{name: "invalid UTF-8 value", env: []string{"LANG=" + string([]byte{0xff})}, want: "invalid environment"},
		{name: "oversized value", env: []string{"PATH=" + strings.Repeat("x", 8193)}, want: "invalid environment"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := PrepareExecSpec(ExecSpec{Target: missing, Cwd: missing, Env: test.env})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("invalid environment was not rejected before path commitment: %v", err)
			}
		})
	}
}

func TestPrepareExecSpecRejectsInvalidUTF8BeforeControlCommitment(t *testing.T) {
	control, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Close() })
	_, err = PrepareExecSpec(ExecSpec{
		Target: "/bin/sh", Cwd: t.TempDir(), Env: []string{"LANG=" + string([]byte{0xff})}, Control: control,
	})
	if err == nil || !strings.Contains(err.Error(), "invalid environment") {
		t.Fatalf("invalid UTF-8 was not rejected before control commitment: %v", err)
	}
}

func TestAttemptConfigRejectsInvalidEnvironment(t *testing.T) {
	root := t.TempDir()
	prepared, err := PrepareExecSpec(ExecSpec{Target: "/bin/sh", Cwd: root, Env: []string{"PATH=/usr/bin:/bin"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		env  []string
	}{
		{name: "duplicate", env: []string{"PATH=/usr/bin", "PATH=/bin"}},
		{name: "invalid UTF-8 name", env: []string{"LA" + string([]byte{0xff}) + "G=value"}},
		{name: "invalid UTF-8 value", env: []string{"LANG=" + string([]byte{0xff})}},
	} {
		t.Run(test.name, func(t *testing.T) {
			commitment := prepared.commit
			commitment.Env = test.env
			cfg := attemptConfig{Version: 1, AttemptID: "attempt", MarkerName: "marker", TerminalName: "terminal", Wrapper: commitment}
			if err := validateAttemptConfig(cfg); err != ErrIdentity {
				t.Fatalf("invalid recovered environment = %v, want ErrIdentity", err)
			}
		})
	}
}

func TestPreparedEnvironmentOrderAndBytesReachRealChildExactly(t *testing.T) {
	f := newFixture(t)
	output := outputFile(t, filepath.Join(f.root, "environment.out"))
	environment := []string{
		"DARK_FACTORY_SOCKET=/private/runtime/api.sock",
		"DARK_FACTORY_ATTEMPT_TOKEN_FILE=/private/runtime/attempt.token",
		"HOME=/private/provider-home",
		"TMPDIR=/private/provider-tmp",
		"PATH=/usr/bin:/bin",
		"LANG=工場.UTF-8",
		"LC_ALL=C",
		"TERM=dumb",
		"NO_COLOR=1",
		"GIT_CEILING_DIRECTORIES=/private/changes",
		"GIT_DISCOVERY_ACROSS_FILESYSTEM=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/usr/bin/false",
		"GIT_SSH_COMMAND=/usr/bin/false -F /dev/null",
		"GH_CONFIG_DIR=/dev/null",
	}
	want := append([]string{}, environment...)
	prepared, err := PrepareExecSpec(ExecSpec{
		Target: "/usr/bin/env", Cwd: f.cwd, Env: environment, Stdout: output, Stderr: output,
	})
	if err != nil {
		t.Fatal(err)
	}
	environment[0] = "DARK_FACTORY_SOCKET=/mutated"
	if !slices.Equal(prepared.commit.Env, want) {
		t.Fatalf("prepared environment changed with caller slice: %q", prepared.commit.Env)
	}
	child := f.startPrepared(prepared, false)
	if _, err := child.Activate(); err != nil {
		t.Fatal(err)
	}
	exit, err := child.FinishAfterExit(4 * time.Second)
	if err != nil || exit.Code != 0 || exit.Signal != 0 || exit.LaunchErr != "" {
		t.Fatalf("environment child exit=%+v err=%v", exit, err)
	}
	if err := output.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(output)
	if err != nil {
		t.Fatal(err)
	}
	if got, expected := string(body), strings.Join(want, "\n")+"\n"; got != expected {
		t.Fatalf("child environment bytes/order differ\ngot:  %q\nwant: %q", got, expected)
	}
	if got := ObserveProcess(child.Identity()); got.Presence != Absent {
		t.Fatalf("environment child remains after Wait: %+v", got)
	}
}
