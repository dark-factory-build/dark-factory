//go:build darwin

package provider

import (
	"bytes"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

func providerFixture(t *testing.T) (Installation, RuntimePaths) {
	t.Helper()
	executable, err := runner.CommitExecutableLocator(shellPath)
	if err != nil {
		t.Fatal(err)
	}
	installation, err := NewShellInstallation(executable)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	runtime, err := NewRuntimePaths(
		root+"/home", root+"/tmp", "/private/tmp/df-provider-test.sock", root+"/attempt.token",
		"/usr/local/bin/factoryctl", root+"/changes", "/opt/homebrew/bin:/usr/bin:/bin",
	)
	if err != nil {
		t.Fatal(err)
	}
	return installation, runtime
}

func requestFor(t *testing.T, kind kernel.Provider, installation Installation, runtime RuntimePaths, model, effort string) Request {
	t.Helper()
	request, err := NewRequest(kind, installation, model, effort, runtime)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestBuildShellReturnsExactImmutableLaunch(t *testing.T) {
	installation, runtime := providerFixture(t)
	t.Setenv("ANTHROPIC_API_KEY", "AMBIENT_SENTINEL")
	t.Setenv("HTTP_PROXY", "AMBIENT_SENTINEL")
	launch, err := Build(requestFor(t, kernel.ProviderShell, installation, runtime, "", ""))
	if err != nil {
		t.Fatal(err)
	}
	if launch.Executable().Path() != shellPath || launch.Executable() != installation.executable {
		t.Fatal("launch did not retain the exact Shell executable commitment")
	}
	wantArgv := []string{"/bin/sh", "-s"}
	if got := launch.Argv(); !slices.Equal(got, wantArgv) {
		t.Fatalf("argv=%q, want %q", got, wantArgv)
	}
	wantEnvironment := []string{
		"DARK_FACTORY_SOCKET=" + runtime.socket,
		"DARK_FACTORY_ATTEMPT_TOKEN_FILE=" + runtime.token,
		"DARK_FACTORY_FACTORYCTL=" + runtime.factoryctl,
		"HOME=" + runtime.home,
		"TMPDIR=" + runtime.temp,
		"PATH=" + runtime.toolPath,
		"LANG=C", "LC_ALL=C", "TERM=xterm-256color", "SHELL=/bin/sh",
		"GIT_CEILING_DIRECTORIES=" + runtime.gitCeiling,
		"GIT_DISCOVERY_ACROSS_FILESYSTEM=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=/usr/bin/false", "GIT_SSH_COMMAND=/usr/bin/false", "GH_CONFIG_DIR=/dev/null",
	}
	if got := launch.Environment(); !slices.Equal(got, wantEnvironment) {
		t.Fatalf("environment=\n%q\nwant=\n%q", got, wantEnvironment)
	}
	for _, entry := range launch.Environment() {
		if strings.Contains(entry, "AMBIENT_SENTINEL") || strings.HasPrefix(entry, "ANTHROPIC_API_KEY=") || strings.HasPrefix(entry, "HTTP_PROXY=") {
			t.Fatalf("ambient environment reached launch: %q", entry)
		}
	}

	argv := launch.Argv()
	environment := launch.Environment()
	argv[0] = "/bin/false"
	environment[0] = "DARK_FACTORY_SOCKET=/ambient"
	if !slices.Equal(launch.Argv(), wantArgv) || !slices.Equal(launch.Environment(), wantEnvironment) {
		t.Fatal("Launch accessors exposed mutable authoritative slices")
	}
	if _, err := runner.PrepareCommittedExecSpec(launch.Executable(), launch.Argv(), launch.Environment(), t.TempDir()); err != nil {
		t.Fatalf("runner refused exact provider launch: %v", err)
	}
}

func TestBuildRejectsProviderInstallationMismatch(t *testing.T) {
	installation, runtime := providerFixture(t)
	installation.provider = kernel.ProviderCodex
	request := requestFor(t, kernel.ProviderShell, installation, runtime, "", "")
	if _, err := Build(request); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatched provider error=%v, want ErrInvalid", err)
	}
}

func TestBuildShellRejectsModelAndReasoningEffortIndependently(t *testing.T) {
	installation, runtime := providerFixture(t)
	for _, test := range []struct{ name, model, effort string }{
		{name: "model", model: "model-sentinel"},
		{name: "effort", effort: "high"},
		{name: "both", model: "model-sentinel", effort: "high"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewRequest(kernel.ProviderShell, installation, test.model, test.effort, runtime); !errors.Is(err, ErrInvalid) {
				t.Fatalf("NewRequest error=%v, want ErrInvalid", err)
			}
		})
	}
}

func TestNewRequestRejectsUnknownDurableReasoningEffort(t *testing.T) {
	installation, runtime := providerFixture(t)
	if _, err := NewRequest(kernel.ProviderCodex, installation, "", "speculative", runtime); !errors.Is(err, ErrInvalid) {
		t.Fatalf("NewRequest error=%v, want ErrInvalid", err)
	}
}

func TestNewRequestRejectsInvalidDurableModelText(t *testing.T) {
	installation, runtime := providerFixture(t)
	for _, model := range []string{string([]byte{0xff}), "model\x00suffix"} {
		if _, err := NewRequest(kernel.ProviderCodex, installation, model, "", runtime); !errors.Is(err, ErrInvalid) {
			t.Fatalf("model %x error=%v, want ErrInvalid", []byte(model), err)
		}
	}
}

func TestBuildFailsClosedForUnknownAndUnimplementedProviders(t *testing.T) {
	installation, runtime := providerFixture(t)
	if _, err := NewRequest(kernel.Provider(255), installation, "", "", runtime); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown provider error=%v, want ErrInvalid", err)
	}
	for _, kind := range []kernel.Provider{kernel.ProviderClaudeCode, kernel.ProviderCodex} {
		t.Run(kind.String(), func(t *testing.T) {
			request := requestFor(t, kind, Installation{}, runtime, "", "")
			if _, err := Build(request); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("Build error=%v, want ErrUnavailable", err)
			}
			if _, err := InitialInput(kind, []byte("task")); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("InitialInput error=%v, want ErrUnavailable", err)
			}
		})
	}
}

func TestNewRuntimePathsRejectsMissingAndMalformedSealedValues(t *testing.T) {
	root := t.TempDir()
	valid := []string{root + "/home", root + "/tmp", "/private/tmp/df-provider-path-test.sock", root + "/token", "/usr/local/bin/factoryctl", root + "/changes", "/usr/bin:/bin"}
	for index := range valid {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			values := append([]string(nil), valid...)
			values[index] = ""
			if _, err := NewRuntimePaths(values[0], values[1], values[2], values[3], values[4], values[5], values[6]); !errors.Is(err, ErrInvalid) {
				t.Fatalf("missing index %d error=%v, want ErrInvalid", index, err)
			}
		})
	}
	for _, toolPath := range []string{"relative:/bin", "/usr/bin::/bin", "/usr/bin:/usr/bin"} {
		if _, err := NewRuntimePaths(valid[0], valid[1], valid[2], valid[3], valid[4], valid[5], toolPath); !errors.Is(err, ErrInvalid) {
			t.Fatalf("tool path %q error=%v, want ErrInvalid", toolPath, err)
		}
	}
	first := "/" + strings.Repeat("a", maxPathBytes-1)
	exact := first + ":/" + strings.Repeat("b", runner.MaxEnvironmentEntryBytes-len("PATH=")-len(first)-2)
	if len(exact) != runner.MaxEnvironmentEntryBytes-len("PATH=") {
		t.Fatalf("exact ToolPath length = %d", len(exact))
	}
	if err := ValidateToolPath(exact); err != nil {
		t.Fatalf("exact ToolPath rejected: %v", err)
	}
	if err := ValidateToolPath(exact + "b"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ToolPath wider than PATH environment error=%v, want ErrInvalid", err)
	}
	invalid := append([]string(nil), valid...)
	invalid[0] = "relative/home"
	if _, err := NewRuntimePaths(invalid[0], invalid[1], invalid[2], invalid[3], invalid[4], invalid[5], invalid[6]); !errors.Is(err, ErrInvalid) {
		t.Fatalf("relative home error=%v, want ErrInvalid", err)
	}
	invalid = append([]string(nil), valid...)
	invalid[2] = "/" + strings.Repeat("s", maxSocketBytes)
	if _, err := NewRuntimePaths(invalid[0], invalid[1], invalid[2], invalid[3], invalid[4], invalid[5], invalid[6]); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized socket error=%v, want ErrInvalid", err)
	}
	invalid = append([]string(nil), valid...)
	invalid[5] = valid[5] + ":/private/other-ceiling"
	if _, err := NewRuntimePaths(invalid[0], invalid[1], invalid[2], invalid[3], invalid[4], invalid[5], invalid[6]); !errors.Is(err, ErrInvalid) {
		t.Fatalf("multi-ceiling git path error=%v, want ErrInvalid", err)
	}
}

func TestInitialInputCapturesOneExactTaskBeforeEvaluation(t *testing.T) {
	for _, test := range []struct {
		name string
		task []byte
	}{
		{name: "append", task: []byte("echo TASK_SENTINEL")},
		{name: "retained", task: []byte("echo TASK_SENTINEL\n")},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := bytes.Clone(test.task)
			input, err := InitialInput(kernel.ProviderShell, test.task)
			if err != nil {
				t.Fatal(err)
			}
			test.task[0] = 'X'
			if bytes.Count(input, want) != 1 || bytes.Count(input, []byte("TASK_SENTINEL")) != 1 || !bytes.HasPrefix(input, []byte("umask 077\nset -C\nif /bin/cat")) || !bytes.HasSuffix(input, []byte("\nthen\n  set +C\n  . \"$TMPDIR/.dark-factory-input.sh\"\n  exit\nfi\nexit 70\n")) {
				t.Fatalf("input=%q, want one exact captured task", input)
			}
		})
	}
}

func TestInitialInputSelectsDelimiterAbsentFromTaskLines(t *testing.T) {
	task := []byte("printf before\n" + shellDelimiterPrefix + "0\nprintf after\n")
	input, err := InitialInput(kernel.ProviderShell, task)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(input, []byte("<<'"+shellDelimiterPrefix+"1'\n")) || bytes.Count(input, task) != 1 {
		t.Fatalf("collision-safe framed input = %q", input)
	}
}

func TestInitialInputMaximumDelimiterCollisionsFitRunnerFrame(t *testing.T) {
	var task bytes.Buffer
	for counter := 0; ; counter++ {
		line := shellDelimiterPrefix + strconv.Itoa(counter) + "\n"
		if task.Len()+len(line) > maxInitialInputBytes {
			task.Write(bytes.Repeat([]byte{'x'}, maxInitialInputBytes-task.Len()))
			break
		}
		task.WriteString(line)
	}
	input, err := InitialInput(kernel.ProviderShell, task.Bytes())
	if err != nil {
		t.Fatalf("maximum collision task rejected: %v", err)
	}
	if len(input) > runner.MaxProviderInputBytes {
		t.Fatalf("maximum collision frame=%d, runner limit=%d", len(input), runner.MaxProviderInputBytes)
	}
}

func TestInitialInputRejectsInvalidTaskBytes(t *testing.T) {
	invalid := [][]byte{
		nil,
		{},
		[]byte("bad\x00task"),
		{0xff},
		bytes.Repeat([]byte{'x'}, maxInitialInputBytes+1),
	}
	for _, task := range invalid {
		if _, err := InitialInput(kernel.ProviderShell, task); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid task len=%d error=%v, want ErrInvalid", len(task), err)
		}
	}
	if _, err := InitialInput(kernel.Provider(255), []byte("task")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown provider error=%v, want ErrInvalid", err)
	}
}

func TestInitialInputRejectsEmptyShellTask(t *testing.T) {
	for _, task := range [][]byte{nil, {}} {
		if _, err := InitialInput(kernel.ProviderShell, task); !errors.Is(err, ErrInvalid) {
			t.Fatalf("empty shell task error=%v, want ErrInvalid", err)
		}
	}
}

func TestInitialInputAcceptsMaximumTaskBodyWithinRunnerFrame(t *testing.T) {
	task := bytes.Repeat([]byte{'x'}, maxInitialInputBytes)
	input, err := InitialInput(kernel.ProviderShell, task)
	if err != nil {
		t.Fatalf("maximum task body rejected: %v", err)
	}
	if len(input) <= len(task) || len(input) > runner.MaxProviderInputBytes || input[len(input)-1] != '\n' || bytes.Count(input, task) != 1 {
		t.Fatalf("captured maximum input length or bytes changed: len=%d last=%q", len(input), input[len(input)-1])
	}
	if err := runner.ValidateProviderInput(input); err != nil {
		t.Fatalf("normalized maximum input fails runner boundary: %v", err)
	}
	if _, err := InitialInput(kernel.ProviderShell, bytes.Repeat([]byte{'x'}, maxInitialInputBytes+1)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("one-byte-over task body error=%v, want ErrInvalid", err)
	}
}

func TestLaunchFormattingDoesNotRevealRuntimeOrExecutable(t *testing.T) {
	installation, runtime := providerFixture(t)
	launch, err := Build(requestFor(t, kernel.ProviderShell, installation, runtime, "", ""))
	if err != nil {
		t.Fatal(err)
	}
	for _, formatted := range []string{launch.String(), launch.GoString(), installation.String(), runtime.String()} {
		for _, secret := range []string{runtime.home, runtime.socket, shellPath} {
			if strings.Contains(formatted, secret) {
				t.Fatalf("formatting exposed %q in %q", secret, formatted)
			}
		}
	}
}
