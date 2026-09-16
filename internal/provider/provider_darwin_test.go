//go:build darwin

package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

func runtimeFixture(t *testing.T, toolPath, accountHome string) RuntimePaths {
	t.Helper()
	root := t.TempDir()
	runtime, err := NewRuntimePaths(
		root+"/home", root+"/tmp", "/private/tmp/df-provider-test.sock", root+"/attempt.token",
		"/usr/local/bin/factoryctl", root+"/changes", toolPath, accountHome, "", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func providerFixture(t *testing.T) (Installation, RuntimePaths) {
	t.Helper()
	toolPath := "/opt/homebrew/bin:/usr/bin:/bin"
	installation, err := ResolveInstallation(kernel.ProviderShell, toolPath)
	if err != nil {
		t.Fatal(err)
	}
	return installation, runtimeFixture(t, toolPath, filepath.Join(t.TempDir(), "account"))
}

func nativeFixture(t *testing.T, kind kernel.Provider) (Installation, RuntimePaths, string) {
	t.Helper()
	missing := t.TempDir()
	tools := t.TempDir()
	var tool string
	if kind == kernel.ProviderClaudeCode {
		tool = claudeTool
	} else if kind == kernel.ProviderCodex {
		tool = codexTool
	} else {
		t.Fatalf("provider %s is not native", kind)
	}
	locator := filepath.Join(tools, tool)
	if err := os.Symlink("/usr/bin/true", locator); err != nil {
		t.Fatal(err)
	}
	toolPath := strings.Join([]string{missing, tools, "/usr/bin", "/bin"}, string(filepath.ListSeparator))
	installation, err := ResolveInstallation(kind, toolPath)
	if err != nil {
		t.Fatal(err)
	}
	accountHome := filepath.Join(t.TempDir(), "account")
	return installation, runtimeFixture(t, toolPath, accountHome), locator
}

func requestFor(t *testing.T, kind kernel.Provider, installation Installation, runtime RuntimePaths, model, effort string) Request {
	t.Helper()
	return roleRequestFor(t, kind, installation, runtime, model, effort, kernel.RoleWorker)
}

func roleRequestFor(t *testing.T, kind kernel.Provider, installation Installation, runtime RuntimePaths, model, effort string, role kernel.AgentRole) Request {
	t.Helper()
	request, err := NewRequest(kind, installation, model, effort, runtime, filepath.Join(t.TempDir(), "change"), role)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

// An orchestrator's Claude session is handed the Maintainer bridge as its one
// MCP server, resolved on the fixed tool path; a worker's is handed none, and
// an orchestrator without the bridge is not launched at all.
func TestBuildOrchestratorClaudeIsGivenTheMaintainerBridge(t *testing.T) {
	installation, runtime, locator := nativeFixture(t, kernel.ProviderClaudeCode)
	worker, err := Build(roleRequestFor(t, kernel.ProviderClaudeCode, installation, runtime, "", "", kernel.RoleWorker))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(worker.Argv(), []string{"/usr/bin/true", "--dangerously-skip-permissions", "--strict-mcp-config"}) {
		t.Fatalf("worker argv = %q, want strict MCP with no server", worker.Argv())
	}
	if _, err := Build(roleRequestFor(t, kernel.ProviderClaudeCode, installation, runtime, "", "", kernel.RoleOrchestrator)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("orchestrator without the bridge = %v, want ErrUnavailable", err)
	}
	// The installed bridge is a script, which the CLI commitment would refuse;
	// it needs only to be a regular executable nobody but its owner can write.
	bridge := filepath.Join(filepath.Dir(locator), maintainerBridge)
	if err := os.WriteFile(bridge, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolvedBridge, err := filepath.EvalSymlinks(bridge)
	if err != nil {
		t.Fatal(err)
	}
	launch, err := Build(roleRequestFor(t, kernel.ProviderClaudeCode, installation, runtime, "", "", kernel.RoleOrchestrator))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/bin/true", "--dangerously-skip-permissions", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{"maintainer":{"command":"` + resolvedBridge + `"}}}`}
	if !reflect.DeepEqual(launch.Argv(), want) {
		t.Fatalf("orchestrator argv = %q, want %q", launch.Argv(), want)
	}
	if _, err := runner.PrepareCommittedExecSpec(launch.Executable(), launch.Argv(), launch.Environment(), t.TempDir()); err != nil {
		t.Fatalf("runner rejected Claude overseer environment: %v", err)
	}
	if !slices.Contains(launch.Environment(), "DARK_FACTORY_MAINTAINER_BRIDGE="+resolvedBridge) {
		t.Fatal("Claude overseer lost its exact bridge environment")
	}
	if slices.Contains(worker.Environment(), "DARK_FACTORY_MAINTAINER_BRIDGE="+resolvedBridge) {
		t.Fatal("Claude worker inherited publication authority")
	}
	// A bridge that is present but unfit is refused by name, unlike a
	// missing one, so the operator learns which of the two it is.
	for name, mode := range map[string]os.FileMode{"not executable": 0o644, "group writable": 0o775} {
		if err := os.Chmod(bridge, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := Build(roleRequestFor(t, kernel.ProviderClaudeCode, installation, runtime, "", "", kernel.RoleOrchestrator)); !errors.Is(err, ErrUnavailable) || !errors.Is(err, errBridgeUnfit) {
			t.Fatalf("%s bridge = %v, want ErrUnavailable and errBridgeUnfit", name, err)
		}
	}
	if err := os.Remove(bridge); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(roleRequestFor(t, kernel.ProviderClaudeCode, installation, runtime, "", "", kernel.RoleOrchestrator)); !errors.Is(err, ErrUnavailable) || errors.Is(err, errBridgeUnfit) {
		t.Fatalf("missing bridge = %v, want ErrUnavailable alone", err)
	}
	if _, err := NewRequest(kernel.ProviderClaudeCode, installation, "", "", runtime, "/private/change", kernel.AgentRole(0)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("request without a role = %v, want ErrInvalid", err)
	}
}

func TestBuildShellReturnsExactImmutableLaunchAndTask(t *testing.T) {
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
	if launch.TaskDelivery() != TaskDeliveryFD11 {
		t.Fatalf("task delivery=%d, want fd 11", launch.TaskDelivery())
	}
	wantArgv := []string{"/bin/sh", runner.ProviderTaskPath}
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
	task := []byte("echo TASK_SENTINEL\n")
	wantTask := bytes.Clone(task)
	delivery, prepared, err := PrepareTask(kernel.ProviderShell, task)
	if err != nil {
		t.Fatal(err)
	}
	if delivery != launch.TaskDelivery() {
		t.Fatalf("prepared delivery=%d, want %d", delivery, launch.TaskDelivery())
	}
	task[0] = 'X'
	if !bytes.Equal(prepared, wantTask) {
		t.Fatalf("prepared task=%q, want exact program %q", prepared, wantTask)
	}
	if _, err := runner.PrepareCommittedExecSpec(launch.Executable(), launch.Argv(), launch.Environment(), t.TempDir()); err != nil {
		t.Fatalf("runner refused exact provider launch: %v", err)
	}
}

func TestResolveNativeInstallationCommitsResolvedDirectTargetOnce(t *testing.T) {
	for _, kind := range []kernel.Provider{kernel.ProviderClaudeCode, kernel.ProviderCodex} {
		t.Run(kind.String(), func(t *testing.T) {
			installation, runtime, locator := nativeFixture(t, kind)
			if installation.provider != kind || installation.executable.Path() != "/usr/bin/true" {
				t.Fatalf("installation provider=%s path=%q", installation.provider, installation.executable.Path())
			}
			if err := os.Remove(locator); err != nil {
				t.Fatal(err)
			}
			if _, err := Build(requestFor(t, kind, installation, runtime, "", "")); err != nil {
				t.Fatalf("resolved commitment depended on removed symlink: %v", err)
			}
		})
	}
}

func TestResolveInstallationFailsClosedAtInvalidExistingCandidate(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	if err := os.WriteFile(filepath.Join(first, codexTool), []byte("#!/bin/sh\nexit 0\n"), 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/usr/bin/true", filepath.Join(second, codexTool)); err != nil {
		t.Fatal(err)
	}
	toolPath := first + string(filepath.ListSeparator) + second
	if _, err := ResolveInstallation(kernel.ProviderCodex, toolPath); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("invalid first candidate error=%v, want ErrUnavailable", err)
	}
	if err := os.Remove(filepath.Join(first, codexTool)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(first, "missing-target"), filepath.Join(first, codexTool)); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveInstallation(kernel.ProviderCodex, toolPath); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("broken first symlink error=%v, want ErrUnavailable", err)
	}
	if _, err := ResolveInstallation(kernel.ProviderCodex, "relative:/bin"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid tool path error=%v, want ErrInvalid", err)
	}
	if _, err := ResolveInstallation(kernel.Provider(255), "/usr/bin:/bin"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown provider error=%v, want ErrInvalid", err)
	}
}

func TestBuildNativeReturnsExactArgvEnvironmentAndSafeStartupTask(t *testing.T) {
	tests := []struct {
		kind         kernel.Provider
		model        string
		effort       string
		wantDelivery TaskDelivery
		wantArgv     []string
	}{
		{
			kind: kernel.ProviderClaudeCode, model: "claude-model", effort: "max", wantDelivery: TaskDeliveryStartupTerminal,
			wantArgv: []string{"/usr/bin/true", "--dangerously-skip-permissions", "--model", "claude-model", "--effort", "max", "--strict-mcp-config"},
		},
		{
			kind: kernel.ProviderCodex, model: "codex-model", effort: "xhigh", wantDelivery: TaskDeliveryAttemptAPI,
			wantArgv: []string{"/usr/bin/true", "-c", "notify=[]", "--strict-config", "--no-alt-screen", "-c", "check_for_update_on_startup=false", "-c", "tool_output_token_limit=32768", "-c", "projects=<working-directory>", "--model", "codex-model", "-c", `model_reasoning_effort="xhigh"`, codexBootstrapPrompt},
		},
	}
	for _, test := range tests {
		t.Run(test.kind.String(), func(t *testing.T) {
			const privateTaskSentinel = "PRIVATE_TASK_SENTINEL"
			task := []byte(privateTaskSentinel + "\nline 1\n\"quoted\"\x1b café 😀\u007f\u0085")
			installation, runtime, _ := nativeFixture(t, test.kind)
			request := requestFor(t, test.kind, installation, runtime, test.model, test.effort)
			launch, err := Build(request)
			if err != nil {
				t.Fatal(err)
			}
			if launch.TaskDelivery() != test.wantDelivery {
				t.Fatalf("task delivery=%d, want %d", launch.TaskDelivery(), test.wantDelivery)
			}
			wantArgv := test.wantArgv
			if test.kind == kernel.ProviderCodex {
				permissions, err := codexPermissions(request)
				if err != nil {
					t.Fatal(err)
				}
				wantArgv = slices.Replace(wantArgv, 10, 11, codexUntrustedProjectConfig(request.workingDirectory), "-c", "default_permissions="+tomlBasicString(codexPermissionName(request.runtime)), "-c", `approval_policy="never"`, "-c", permissions, "--disable", "computer_use", "--disable", "browser_use", "--disable", "plugins", "-c", "mcp_servers.factory_attempt={command="+tomlBasicString(runtime.factoryctl)+`,args=["attempt","mcp"],env_vars=["DARK_FACTORY_SOCKET","DARK_FACTORY_ATTEMPT_TOKEN_FILE"],enabled=true,required=true,tools={factory={approval_mode="approve"}}}`)
			}
			if got := launch.Argv(); !slices.Equal(got, wantArgv) {
				t.Fatalf("argv=%q, want %q", got, wantArgv)
			}
			wantHome := runtime.home
			if test.kind == kernel.ProviderClaudeCode {
				wantHome = runtime.accountHome
			} else {
				wantConfig := "CODEX_HOME=" + filepath.Join(runtime.accountHome, codexConfigDir)
				if !slices.Contains(launch.Environment(), wantConfig) {
					t.Fatalf("environment lacks exact config root %q: %q", wantConfig, launch.Environment())
				}
			}
			if !slices.Contains(launch.Environment(), "HOME="+wantHome) {
				t.Fatalf("environment lacks exact HOME %q: %q", wantHome, launch.Environment())
			}
			for _, entry := range launch.Environment() {
				if strings.HasPrefix(entry, "HOME=") && entry != "HOME="+wantHome {
					t.Fatalf("native provider received wrong HOME: %q", entry)
				}
				if test.kind == kernel.ProviderClaudeCode && (strings.HasPrefix(entry, "CODEX_HOME=") || strings.HasPrefix(entry, "CLAUDE_CONFIG_DIR=")) ||
					test.kind == kernel.ProviderCodex && strings.HasPrefix(entry, "CLAUDE_CONFIG_DIR=") {
					t.Fatalf("provider received another provider's config root: %q", entry)
				}
			}

			for _, value := range append(launch.Argv(), launch.Environment()...) {
				if strings.Contains(value, privateTaskSentinel) {
					t.Fatalf("private task reached provider argv or environment: %q", value)
				}
			}
			delivery, payload, err := PrepareTask(test.kind, task)
			if err != nil {
				t.Fatal(err)
			}
			if delivery != launch.TaskDelivery() {
				t.Fatalf("prepared delivery=%d, want %d", delivery, launch.TaskDelivery())
			}
			instructions := string(payload)
			if test.kind == kernel.ProviderCodex {
				instructions = launch.Argv()[len(launch.Argv())-1]
			}
			for _, rule := range []string{"Never recursively search the user home", "command -v", "report the missing prerequisite"} {
				if !strings.Contains(instructions, rule) {
					t.Fatalf("native provider lacks scoped discovery rule %q", rule)
				}
			}
			if test.kind == kernel.ProviderCodex {
				if payload != nil {
					t.Fatalf("Codex task bytes escaped attempt API delivery: %q", payload)
				}
			} else {
				want := runner.ClaudeTaskLead + `"PRIVATE_TASK_SENTINEL\nline 1\n\"quoted\"\u001b café 😀\u007f\u0085"` + "\r"
				if string(payload) != want {
					t.Fatalf("startup payload=%q, want %q", payload, want)
				}
				if payload[len(payload)-1] != '\r' || bytes.IndexByte(payload[:len(payload)-1], '\r') >= 0 || bytes.IndexByte(payload, '\n') >= 0 || bytes.IndexByte(payload, 0x1b) >= 0 || bytes.IndexByte(payload, 0x7f) >= 0 {
					t.Fatalf("startup payload contains raw terminal control: %q", payload)
				}
				var decoded string
				if err := json.Unmarshal(payload[len(runner.ClaudeTaskLead):len(payload)-1], &decoded); err != nil || decoded != string(task) {
					t.Fatalf("JSON task decoded as %q: %v", decoded, err)
				}
			}
		})
	}
}

func TestInstalledBrowserBridgeUsesOnlyRunPathsForBothProviders(t *testing.T) {
	for _, kind := range []kernel.Provider{kernel.ProviderCodex, kernel.ProviderClaudeCode} {
		t.Run(kind.String(), func(t *testing.T) {
			installation, runtime, locator := nativeFixture(t, kind)
			bridge := filepath.Join(filepath.Dir(locator), "dark-factory-browser-mcp")
			for _, name := range []string{bridge, filepath.Join(filepath.Dir(locator), maintainerBridge)} {
				if err := os.WriteFile(name, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			for _, role := range []kernel.AgentRole{kernel.RoleWorker, kernel.RoleOrchestrator} {
				request := roleRequestFor(t, kind, installation, runtime, "", "", role)
				launch, err := Build(request)
				if err != nil {
					t.Fatal(err)
				}
				args := launch.Argv()
				var found bool
				for i, arg := range args {
					if kind == kernel.ProviderCodex && strings.HasPrefix(arg, "mcp_servers.factory_browser=") {
						found = strings.Contains(arg, tomlBasicString(runtime.temp)) && strings.Contains(arg, "required=true") && strings.Contains(arg, `env_vars=["DARK_FACTORY_FACTORYCTL","DARK_FACTORY_SOCKET","DARK_FACTORY_ATTEMPT_TOKEN_FILE"]`) && strings.Contains(arg, `default_tools_approval_mode="approve"`)
					}
					if kind == kernel.ProviderClaudeCode && arg == "--mcp-config" {
						var config struct {
							Servers map[string]struct {
								Command string   `json:"command"`
								Args    []string `json:"args"`
							} `json:"mcpServers"`
						}
						if err := json.Unmarshal([]byte(args[i+1]), &config); err != nil {
							t.Fatal(err)
						}
						server := config.Servers["factory_browser"]
						found = server.Command != "" && slices.Equal(server.Args, []string{"--runtime-dir", runtime.temp})
						if role == kernel.RoleOrchestrator && config.Servers["maintainer"].Command == "" {
							t.Fatal("lost Maintainer bridge")
						}
					}
				}
				if !found {
					t.Fatal("browser bridge is missing or not scoped to this run")
				}
			}
			if err := os.Chmod(bridge, 0o777); err != nil {
				t.Fatal(err)
			}
			if _, err := Build(roleRequestFor(t, kind, installation, runtime, "", "", kernel.RoleWorker)); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("unsafe bridge: %v", err)
			}
		})
	}
}

func TestCodexProjectTrustOverrideUsesExactTomlEncoding(t *testing.T) {
	path := "/private/change.with dots/quote\"slash\\\b\t\n\f\r\x01\x1f\x7f\u0080é/working"
	want := `"/private/change.with dots/quote\"slash\\\b\t\n\f\r\u0001\u001f\u007f\u0080é/working"`
	if got := tomlBasicString(path); got != want {
		t.Fatalf("TOML path=%q, want %q", got, want)
	}
	wantConfig := `projects={"/private/change.with dots/quote\"slash\\\b\t\n\f\r\u0001\u001f\u007f\u0080é/working"={trust_level="untrusted"}}`
	if got := codexUntrustedProjectConfig(path); got != wantConfig {
		t.Fatalf("Codex project config=%q", got)
	}
}

func TestNewRequestRejectsMismatchedProviderAndControls(t *testing.T) {
	shell, shellRuntime := providerFixture(t)
	for _, controls := range []struct{ model, effort string }{
		{model: "model-sentinel"},
		{effort: "high"},
		{model: "model-sentinel", effort: "high"},
	} {
		if _, err := NewRequest(kernel.ProviderShell, shell, controls.model, controls.effort, shellRuntime, "/private/change", kernel.RoleWorker); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Shell controls %+v error=%v, want ErrInvalid", controls, err)
		}
	}
	if _, err := NewRequest(kernel.ProviderCodex, shell, "", "", shellRuntime, "/private/change", kernel.RoleWorker); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatched installation error=%v, want ErrInvalid", err)
	}
	claude, claudeRuntime, _ := nativeFixture(t, kernel.ProviderClaudeCode)
	for _, effort := range []string{"ultra", "speculative"} {
		if _, err := NewRequest(kernel.ProviderClaudeCode, claude, "", effort, claudeRuntime, "/private/change", kernel.RoleWorker); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Claude effort %q error=%v, want ErrInvalid", effort, err)
		}
	}
	codex, codexRuntime, _ := nativeFixture(t, kernel.ProviderCodex)
	if _, err := NewRequest(kernel.ProviderCodex, codex, "", "ultra", codexRuntime, "/private/change", kernel.RoleWorker); err != nil {
		t.Fatalf("Codex durable ultra effort rejected: %v", err)
	}
	for _, model := range []string{string([]byte{0xff}), "model\x00suffix"} {
		if _, err := NewRequest(kernel.ProviderCodex, codex, model, "", codexRuntime, "/private/change", kernel.RoleWorker); !errors.Is(err, ErrInvalid) {
			t.Fatalf("model %x error=%v, want ErrInvalid", []byte(model), err)
		}
	}
	for _, workingDirectory := range []string{"", "relative/change", "/", "/private/../change", "/private/change\x00suffix", string([]byte{0xff})} {
		if _, err := NewRequest(kernel.ProviderCodex, codex, "", "", codexRuntime, workingDirectory, kernel.RoleWorker); !errors.Is(err, ErrInvalid) {
			t.Fatalf("working directory %q error=%v, want ErrInvalid", workingDirectory, err)
		}
	}
	tooLarge := "/" + strings.Repeat("\x7f", maxPathBytes-1)
	if len(codexUntrustedProjectConfig(tooLarge)) <= runner.MaxArgumentBytes {
		t.Fatalf("expanded project config=%d, want larger than argv bound", len(codexUntrustedProjectConfig(tooLarge)))
	}
	if _, err := NewRequest(kernel.ProviderCodex, codex, "", "", codexRuntime, tooLarge, kernel.RoleWorker); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized encoded working directory error=%v, want ErrInvalid", err)
	}
}

func TestNewRuntimePathsRejectsMissingAndMalformedValues(t *testing.T) {
	root := t.TempDir()
	valid := []string{root + "/home", root + "/tmp", "/private/tmp/df-provider-path-test.sock", root + "/token", "/usr/local/bin/factoryctl", root + "/changes", "/usr/bin:/bin", root + "/account"}
	for index := range valid {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			values := append([]string(nil), valid...)
			values[index] = ""
			if _, err := NewRuntimePaths(values[0], values[1], values[2], values[3], values[4], values[5], values[6], values[7], "", ""); !errors.Is(err, ErrInvalid) {
				t.Fatalf("missing index %d error=%v, want ErrInvalid", index, err)
			}
		})
	}
	for _, toolPath := range []string{"relative:/bin", "/usr/bin::/bin", "/usr/bin:/usr/bin"} {
		if _, err := NewRuntimePaths(valid[0], valid[1], valid[2], valid[3], valid[4], valid[5], toolPath, valid[7], "", ""); !errors.Is(err, ErrInvalid) {
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
	if _, err := NewRuntimePaths(invalid[0], invalid[1], invalid[2], invalid[3], invalid[4], invalid[5], invalid[6], invalid[7], "", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("relative home error=%v, want ErrInvalid", err)
	}
	invalid = append([]string(nil), valid...)
	invalid[2] = "/" + strings.Repeat("s", install.MaxSocketPathBytes)
	if _, err := NewRuntimePaths(invalid[0], invalid[1], invalid[2], invalid[3], invalid[4], invalid[5], invalid[6], invalid[7], "", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized socket error=%v, want ErrInvalid", err)
	}
	invalid = append([]string(nil), valid...)
	invalid[5] = valid[5] + ":/private/other-ceiling"
	if _, err := NewRuntimePaths(invalid[0], invalid[1], invalid[2], invalid[3], invalid[4], invalid[5], invalid[6], invalid[7], "", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("multi-ceiling git path error=%v, want ErrInvalid", err)
	}
	for _, accountHome := range []string{"relative", "/", valid[0], valid[1]} {
		if _, err := NewRuntimePaths(valid[0], valid[1], valid[2], valid[3], valid[4], valid[5], valid[6], accountHome, "", ""); !errors.Is(err, ErrInvalid) {
			t.Fatalf("account home %q error=%v, want ErrInvalid", accountHome, err)
		}
	}
}

func TestTaskValidationUsesDeliverySpecificBound(t *testing.T) {
	for _, kind := range []kernel.Provider{kernel.ProviderShell, kernel.ProviderClaudeCode, kernel.ProviderCodex} {
		for _, task := range [][]byte{nil, {}, []byte("bad\x00task"), {0xff}} {
			if _, _, err := PrepareTask(kind, task); !errors.Is(err, ErrInvalid) {
				t.Fatalf("provider=%s invalid task len=%d error=%v, want ErrInvalid", kind, len(task), err)
			}
		}
	}
	shellMaximum := bytes.Repeat([]byte{'x'}, runner.MaxProviderTaskBytes)
	if delivery, program, err := PrepareTask(kernel.ProviderShell, shellMaximum); err != nil || delivery != TaskDeliveryFD11 || !bytes.Equal(program, shellMaximum) {
		t.Fatalf("Shell maximum task error=%v", err)
	}
	if _, _, err := PrepareTask(kernel.ProviderShell, append(shellMaximum, 'x')); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Shell over-limit task error=%v, want ErrInvalid", err)
	}
	claudeMaximum := bytes.Repeat([]byte{'x'}, runner.MaxClaudePrompt-len(runner.ClaudeTaskLead)-3)
	if delivery, _, err := PrepareTask(kernel.ProviderClaudeCode, claudeMaximum); err != nil || delivery != TaskDeliveryStartupTerminal {
		t.Fatalf("Claude exact startup bound rejected: %v", err)
	}
	if _, _, err := PrepareTask(kernel.ProviderClaudeCode, append(claudeMaximum, 'x')); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Claude over-limit task error=%v, want ErrInvalid", err)
	}
	codexMaximum := bytes.Repeat([]byte{'x'}, runner.MaxCodexTaskBytes)
	if delivery, payload, err := PrepareTask(kernel.ProviderCodex, codexMaximum); err != nil || delivery != TaskDeliveryAttemptAPI || payload != nil {
		t.Fatalf("Codex maximum API task delivery=(%d, %d bytes), error=%v", delivery, len(payload), err)
	}
	if _, _, err := PrepareTask(kernel.ProviderCodex, append(codexMaximum, 'x')); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Codex over-limit task error=%v, want ErrInvalid", err)
	}
	jsonExpanding := []byte(strings.Repeat("x", runner.MaxClaudePrompt-len(runner.ClaudeTaskLead)-5) + "\u0085")
	if _, _, err := PrepareTask(kernel.ProviderClaudeCode, jsonExpanding); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Claude expanded valid UTF-8 must exceed the encoded ceiling: %v", err)
	}
	if _, _, err := PrepareTask(kernel.Provider(255), []byte("task")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown provider error=%v, want ErrInvalid", err)
	}
}

func TestLaunchFormattingDoesNotRevealRuntimeOrExecutable(t *testing.T) {
	installation, runtime, _ := nativeFixture(t, kernel.ProviderCodex)
	launch, err := Build(requestFor(t, kernel.ProviderCodex, installation, runtime, "", ""))
	if err != nil {
		t.Fatal(err)
	}
	for _, formatted := range []string{launch.String(), launch.GoString(), installation.String(), runtime.String()} {
		for _, secret := range []string{runtime.home, runtime.socket, runtime.accountHome, installation.executable.Path()} {
			if strings.Contains(formatted, secret) {
				t.Fatalf("formatting exposed %q in %q", secret, formatted)
			}
		}
	}
}

// The Change is a path Claude Code has never seen, so the account's own trust
// record is written for it before launch, and nothing else in that file moves:
// other projects, the login, and a number JSON would otherwise round.
func TestTrustClaudeDirectoryRecordsOnlyTheWorkingDirectory(t *testing.T) {
	accountHome := t.TempDir()
	runtime := runtimeFixture(t, "/usr/bin:/bin", accountHome)
	path := filepath.Join(accountHome, ".claude.json")
	original := `{"oauthAccount":{"emailAddress":"login@example.invalid"},"numStartups":9007199254740993,"lastPrompt":"a<b&c","projects":{"/other":{"hasTrustDialogAccepted":true,"lastCost":0.25}}}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := TrustClaudeDirectory(runtime, "/private/change"); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(written, &config); err != nil {
		t.Fatal(err)
	}
	projects := config["projects"].(map[string]any)
	if projects["/private/change"].(map[string]any)["hasTrustDialogAccepted"] != true {
		t.Fatalf("working directory is not trusted: %s", written)
	}
	other := projects["/other"].(map[string]any)
	if other["hasTrustDialogAccepted"] != true || other["lastCost"] != 0.25 || config["oauthAccount"].(map[string]any)["emailAddress"] != "login@example.invalid" || !bytes.Contains(written, []byte("9007199254740993")) || !bytes.Contains(written, []byte(`"a<b&c"`)) {
		t.Fatalf("other values moved: %s", written)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v", info, err)
	}
	if err := TrustClaudeDirectory(runtime, "/private/change"); err != nil {
		t.Fatal(err)
	}
	if again, err := os.ReadFile(path); err != nil || !bytes.Equal(again, written) {
		t.Fatalf("second record rewrote the file: %v", err)
	}
	// No file yet is a fresh login-less home; the record is still written.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := TrustClaudeDirectory(runtime, "/private/change"); err != nil {
		t.Fatal(err)
	}
	if fresh, err := os.ReadFile(path); err != nil || !bytes.Contains(fresh, []byte(`"/private/change":{"hasTrustDialogAccepted":true}`)) {
		t.Fatalf("fresh record = %s, %v", fresh, err)
	}
	// A linked account keeps its configuration, and so its trust, in its own directory.
	accountConfig := filepath.Join(t.TempDir(), "claude-dogfood")
	if err := os.Mkdir(accountConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	linked, err := NewRuntimePaths(runtime.home, runtime.temp, runtime.socket, runtime.token, runtime.factoryctl, runtime.gitCeiling, runtime.toolPath, accountHome, accountConfig, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := TrustClaudeDirectory(linked, "/private/change"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(accountConfig, ".claude.json")); err != nil {
		t.Fatalf("linked account record: %v", err)
	}
	// The default login discovered as an account names the default directory;
	// its record still lives beside that directory, in the home's own file.
	discovered, err := NewRuntimePaths(runtime.home, runtime.temp, runtime.socket, runtime.token, runtime.factoryctl, runtime.gitCeiling, runtime.toolPath, accountHome, ConfigHome(kernel.ProviderClaudeCode, accountHome), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := TrustClaudeDirectory(discovered, "/private/discovered"); err != nil {
		t.Fatal(err)
	}
	if home, err := os.ReadFile(path); err != nil || !bytes.Contains(home, []byte(`"/private/discovered":{"hasTrustDialogAccepted":true}`)) {
		t.Fatalf("default login record = %s, %v", home, err)
	}
	if _, err := os.Stat(filepath.Join(ConfigHome(kernel.ProviderClaudeCode, accountHome), ".claude.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("default login record landed inside the flags directory: %v", err)
	}
	// Concurrent workers on one account never publish a torn file: each
	// writes its own temporary name and renames whole.
	var group sync.WaitGroup
	for index := range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := TrustClaudeDirectory(runtime, fmt.Sprintf("/private/concurrent-%d", index)); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	final, err := os.ReadFile(path)
	if err != nil || !json.Valid(final) || !bytes.Contains(final, []byte("/private/concurrent-")) {
		t.Fatalf("concurrent records left %s, %v", final, err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(accountHome, ".claude.json.*")); len(leftovers) != 0 {
		t.Fatalf("temporary files remain: %v", leftovers)
	}
	if err := TrustClaudeDirectory(runtime, "relative/change"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("relative working directory = %v, want ErrInvalid", err)
	}
	// An empty or whitespace-only file is a home with no login yet, like a
	// missing one, and gets the record.
	for name, content := range map[string]string{"empty": "", "whitespace": " \n\t\n"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := TrustClaudeDirectory(runtime, "/private/change"); err != nil {
			t.Fatalf("%s file: %v", name, err)
		}
		if recorded, err := os.ReadFile(path); err != nil || !bytes.Contains(recorded, []byte(`"/private/change":{"hasTrustDialogAccepted":true}`)) {
			t.Fatalf("%s file record = %s, %v", name, recorded, err)
		}
	}
	// A file that is not JSON, or whose projects are not the CLI's shape, is
	// refused without its path in the error rather than rewritten.
	for name, content := range map[string]string{"not JSON": "{", "null": "null", "projects not an object": `{"projects":[]}`, "project not an object": `{"projects":{"/private/change":true}}`} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := TrustClaudeDirectory(runtime, "/private/change"); !errors.Is(err, errClaudeConfiguration) || strings.Contains(err.Error(), accountHome) {
			t.Fatalf("%s = %v", name, err)
		}
		if kept, err := os.ReadFile(path); err != nil || string(kept) != content {
			t.Fatalf("%s was rewritten: %s, %v", name, kept, err)
		}
	}
}

func TestCodexOverseerDiscoversScopedControlsWithoutChangingWorkerTask(t *testing.T) {
	installation, runtime, _ := nativeFixture(t, kernel.ProviderCodex)
	request := requestFor(t, kernel.ProviderCodex, installation, runtime, "gpt-5.6-sol", "high")
	worker, err := Build(request)
	if err != nil {
		t.Fatal(err)
	}
	request.role = kernel.RoleOrchestrator
	if _, err := Build(request); !errors.Is(err, ErrUnavailable) {
		t.Fatal("overseer launched without its explicit Maintainer bridge")
	}
	bridge := filepath.Join(filepath.SplitList(runtime.toolPath)[0], maintainerBridge)
	if err := os.WriteFile(bridge, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	resolvedBridge, err := filepath.EvalSymlinks(bridge)
	if err != nil {
		t.Fatal(err)
	}

	overseer, err := Build(request)
	if err != nil {
		t.Fatal(err)
	}
	workerArgs, overseerArgs := worker.Argv(), overseer.Argv()
	if !strings.Contains(strings.Join(overseerArgs, " "), "mcp_servers.dark_factory_maintainer={command=") {
		t.Fatal("overseer lost its explicit Maintainer tools")
	}
	if _, err := runner.PrepareCommittedExecSpec(overseer.Executable(), overseer.Argv(), overseer.Environment(), t.TempDir()); err != nil {
		t.Fatalf("runner rejected Codex overseer environment: %v", err)
	}
	if !slices.Contains(overseer.Environment(), "DARK_FACTORY_MAINTAINER_BRIDGE="+resolvedBridge) {
		t.Fatalf("overseer did not export its exact Maintainer bridge: %q", overseer.Environment())
	}
	if slices.Contains(worker.Environment(), "DARK_FACTORY_MAINTAINER_BRIDGE="+resolvedBridge) {
		t.Fatal("worker inherited the overseer's Maintainer bridge")
	}
	if strings.Contains(strings.Join(workerArgs, " "), "mcp_servers.dark_factory_maintainer=") {
		t.Fatal("worker was granted publication tool approvals")
	}
	if strings.Contains(workerArgs[len(workerArgs)-1], "overseer status") {
		t.Fatal("worker was given overseer authority instructions")
	}
	prompt := overseerArgs[len(overseerArgs)-1]
	for _, command := range []string{`["attempt","task"]`, "overseer status", "next_offset", "next_text_offset", "worker interrupt", "worker replace", "Maintainer App", "structuredContent", "capability refusal", "causal wake", "Continue actionable supervision", "without idle polling"} {
		if !strings.Contains(prompt, command) {
			t.Fatalf("overseer cannot discover %q", command)
		}
	}
	if overseer.TaskDelivery() != TaskDeliveryAttemptAPI {
		t.Fatal("overseer stopped reading the exact durable task")
	}
}

func TestCodexPermissionsDoNotRepeatSharedWorkingDirectory(t *testing.T) {
	installation, runtime, _ := nativeFixture(t, kernel.ProviderCodex)
	request := requestFor(t, kernel.ProviderCodex, installation, runtime, "", "")
	for _, cwd := range []string{runtime.home, runtime.temp} {
		request.workingDirectory = cwd
		policy, err := codexPermissions(request)
		if err != nil || strings.Count(policy, tomlBasicString(cwd)+`="write"`) != 1 {
			t.Fatalf("duplicate working directory makes an invalid TOML table: %s (%v)", policy, err)
		}
	}
}

func TestCodexPermissionsBoundReadsAndRejectOversizedPolicy(t *testing.T) {
	installation, runtime, _ := nativeFixture(t, kernel.ProviderCodex)
	request := requestFor(t, kernel.ProviderCodex, installation, runtime, "", "")
	request.runtime.token += `-"quoted"`
	policy, err := codexPermissions(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []string{`":root"="deny"`, `":minimal"="read"`, tomlBasicString(request.workingDirectory) + `="write"`, tomlBasicString(request.runtime.token) + `="read"`, tomlBasicString(request.runtime.socket) + `="allow"`} {
		if !strings.Contains(policy, rule) {
			t.Fatalf("policy omitted rule %q", rule)
		}
	}
	if strings.Contains(policy, request.runtime.accountHome) || strings.Contains(policy, request.runtime.gitCeiling) {
		t.Fatal("local commands were granted account or other Change access")
	}
	other := request.runtime
	other.home += "-another-run"
	if codexPermissionName(other) == codexPermissionName(request.runtime) {
		t.Fatal("runtime profiles share a configuration name")
	}
	request.runtime.token = "/" + strings.Repeat(`"`, runner.MaxArgumentBytes)
	if _, err := codexPermissions(request); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized policy error = %v", err)
	}
}

func TestCodexToolchainRootsAndCachesStaySeparateFromAccount(t *testing.T) {
	installation, runtime, _ := nativeFixture(t, kernel.ProviderCodex)
	runtime.toolchainReadRoots = "/opt/software/node:/opt/software/go"
	request := requestFor(t, kernel.ProviderCodex, installation, runtime, "", "")
	policy, err := codexPermissions(request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(policy, `"/System/Library/OpenSSL/openssl.cnf"="read"`) || strings.Contains(policy, `"/System/Library/OpenSSL"="read"`) {
		t.Fatal("OpenSSL configuration permission must name only the file")
	}
	for _, root := range filepath.SplitList(runtime.toolchainReadRoots) {
		if !strings.Contains(policy, tomlBasicString(root)+`="read"`) || strings.Contains(policy, tomlBasicString(root)+`="write"`) {
			t.Fatalf("software root permissions: %s", policy)
		}
	}
	launch, err := Build(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.PrepareCommittedExecSpec(launch.Executable(), launch.Argv(), launch.Environment(), t.TempDir()); err != nil {
		t.Fatalf("generated Codex environment rejected by runner: %v", err)
	}
	for _, prefix := range []string{"GOCACHE=", "GOPATH=", "GOMODCACHE=", "COREPACK_HOME=", "npm_config_cache=", "XDG_CACHE_HOME="} {
		found := false
		for _, value := range launch.Environment() {
			if strings.HasPrefix(value, prefix+runtime.home+"/") {
				found = true
			}
		}
		if !found {
			t.Fatalf("cache %s not private", prefix)
		}
	}
	for _, root := range []string{runtime.accountHome, runtime.gitCeiling, runtime.home, runtime.temp} {
		invalid := runtime
		invalid.toolchainReadRoots = root
		if _, err := NewRequest(kernel.ProviderCodex, installation, "", "", invalid, request.workingDirectory, kernel.RoleWorker); err == nil {
			t.Fatalf("accepted private read root %q", root)
		}
	}
}

// Opt-in local proof with an installed Codex CLI; no model or account access.
// Keep fixtures outside the system temp roots permitted by :minimal.
func TestCodexToolchainSandbox(t *testing.T) {
	codex := os.Getenv("DARK_FACTORY_TEST_CODEX")
	if codex == "" {
		t.Skip("set DARK_FACTORY_TEST_CODEX to an installed CLI")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(cwd, ".toolchain-proof-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"home", "tmp", "change", "software", "account"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	software := filepath.Join(root, "software")
	secret := filepath.Join(root, "account", "secret")
	if err := os.WriteFile(secret, []byte("fixture secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(software, "library"), []byte("fixture library"), 0600); err != nil {
		t.Fatal(err)
	}
	installation, runtime, _ := nativeFixture(t, kernel.ProviderCodex)
	runtime.home, runtime.temp, runtime.accountHome = filepath.Join(root, "home"), filepath.Join(root, "tmp"), filepath.Join(root, "account")
	runtime.toolchainReadRoots = software
	request := requestFor(t, kernel.ProviderCodex, installation, runtime, "", "")
	request.workingDirectory = filepath.Join(root, "change")
	run := func(command string, args ...string) ([]byte, error) {
		t.Helper()
		policy, err := codexPermissions(request)
		if err != nil {
			t.Fatal(err)
		}
		argv := []string{"sandbox", "-c", policy, "-P", codexPermissionName(request.runtime), "-C", request.workingDirectory, command}
		cmd := exec.Command(codex, append(argv, args...)...)
		cmd.Env = request.runtime.environment(kernel.ProviderCodex)
		// The sandbox CLI itself uses an empty private config, never account auth.
		for i, value := range cmd.Env {
			if strings.HasPrefix(value, "CODEX_HOME=") {
				cmd.Env[i] = "CODEX_HOME=" + runtime.home
			}
		}
		return cmd.CombinedOutput()
	}
	script := `set -eu
 /bin/cat "$1/library" >/dev/null
 printf cache > "$HOME/cache"
 if /bin/cat "$2" >/dev/null 2>&1; then exit 31; fi
 if printf bad > "$1/write" 2>/dev/null; then exit 32; fi
 `
	if out, err := run("/bin/sh", "-c", script, "proof", software, secret); err != nil {
		t.Fatalf("sandbox isolation: %v\n%s", err, out)
	}
	gitDirectory := filepath.Join(root, "repository.git")
	leaseDirectory := filepath.Join(gitDirectory, "dark-factory-local-ci")
	if err := os.MkdirAll(leaseDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"refs", "hooks", "objects"} {
		if err := os.Mkdir(filepath.Join(gitDirectory, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(gitDirectory, "config"), []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	request.runtime, err = request.runtime.WithLocalCILeaseDirectory(leaseDirectory)
	if err != nil {
		t.Fatal(err)
	}
	leaseScript := `set -eu
 printf lease > "$DARK_FACTORY_LOCAL_CI_DIRECTORY/proof"
 for path in "$1/config" "$1/refs/injected" "$1/hooks/injected" "$1/objects/injected"; do
   if (printf forbidden > "$path") 2>/dev/null; then exit 33; fi
 done
 `
	if out, err := run("/bin/sh", "-c", leaseScript, "proof", gitDirectory); err != nil {
		t.Fatalf("dedicated lease Git isolation: %v\n%s", err, out)
	}
	_, testSource, _, _ := goruntime.Caller(0)
	leaseScripts := filepath.Join(request.workingDirectory, "scripts")
	if err := os.Mkdir(leaseScripts, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"local-ci-lease.sh", "with-local-ci-lease.sh"} {
		body, err := os.ReadFile(filepath.Join(filepath.Dir(testSource), "..", "..", "scripts", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(leaseScripts, name), body, 0700); err != nil {
			t.Fatal(err)
		}
	}
	request.runtime.factoryctl = filepath.Join(root, "factoryctl")
	build := exec.Command("go", "build", "-o", request.runtime.factoryctl, "./cmd/factoryctl")
	build.Dir = filepath.Join(filepath.Dir(testSource), "..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build lease observer fixture: %v\n%s", err, out)
	}
	if out, err := run("/bin/sh", "-c", "\"$DARK_FACTORY_FACTORYCTL\" --local-ci-process-identity $$"); err != nil {
		t.Fatalf("lease process observation: %v\n%s", err, out)
	}
	if out, err := run("/bin/sh", "-c", "./scripts/with-local-ci-lease.sh /usr/bin/true"); err != nil {
		t.Fatalf("generated-profile Git-free lease: %v\n%s", err, out)
	}
	request.runtime.toolchainReadRoots = ""
	if out, err := run("/bin/cat", filepath.Join(software, "library")); err == nil {
		t.Fatalf("baseline unexpectedly reads software: %s", out)
	}
	if roots := os.Getenv("DARK_FACTORY_TEST_TOOLCHAIN_ROOTS"); roots != "" {
		if err := install.CheckToolchainReadRoots(roots, runtime.accountHome); err != nil {
			t.Fatal(err)
		}
		request.runtime.toolchainReadRoots = roots
		request.runtime.toolPath = os.Getenv("DARK_FACTORY_TEST_TOOL_PATH")
		if !install.ValidToolPath(request.runtime.toolPath) {
			t.Fatal("set exact DARK_FACTORY_TEST_TOOL_PATH")
		}
		// Keep Go local, but exercise Node with the actual launch environment.
		// Overriding OPENSSL_CONF here would hide a production permission failure.
		out, err := run("/bin/sh", "-c", `set -eu; export GOENV=off GOTOOLCHAIN=local; node --version; corepack --version; corepack pnpm@11.19.0 --version; go version; printf 'package main\nfunc main() {}\n' > main.go; go run main.go`)
		if err != nil {
			t.Fatalf("installed toolchain: %v\n%s", err, out)
		}
		t.Logf("installed toolchain proof: %s", out)
	}
	if fixture := os.Getenv("DARK_FACTORY_TEST_FIXTURE_BINARY"); fixture != "" {
		request.runtime.sourceReadPaths = append(request.runtime.sourceReadPaths, fixture)
		out, err := run("/usr/bin/env", "DARK_FACTORY_TEST_ANCESTOR_SANDBOX=generated", fixture,
			"-test.run=^TestDispatchFixtureTraversesUnreadableAncestors$", "-test.count=1")
		if err != nil {
			t.Fatalf("generated-profile private fixture: %v\n%s", err, out)
		}
		t.Logf("generated-profile private fixture: %s", out)
	}
}

func TestCodexLocalCILeaseGrantExcludesGitMetadata(t *testing.T) {
	installation, runtime, _ := nativeFixture(t, kernel.ProviderCodex)
	lease := "/private/repository/.git/dark-factory-local-ci"
	withLease, err := runtime.WithLocalCILeaseDirectory(lease)
	if err != nil {
		t.Fatal(err)
	}
	request := requestFor(t, kernel.ProviderCodex, installation, withLease, "", "")
	policy, err := codexPermissions(request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(policy, tomlBasicString(lease)+`="write"`) || strings.Contains(policy, tomlBasicString(filepath.Dir(lease))+`="write"`) {
		t.Fatalf("lease permission is not narrow: %s", policy)
	}
	if !slices.Contains(withLease.environment(kernel.ProviderCodex), "DARK_FACTORY_LOCAL_CI_DIRECTORY="+lease) {
		t.Fatal("lease path missing from launch environment")
	}
	for _, invalid := range []string{"/", "/private/repository/.git", "relative/dark-factory-local-ci", "/private/../dark-factory-local-ci"} {
		if _, err := runtime.WithLocalCILeaseDirectory(invalid); err == nil {
			t.Fatalf("accepted unsafe lease path %q", invalid)
		}
	}
}

func TestCodexLaunchGeneratesPermissionsForOnlyTheExplicitRetainedSource(t *testing.T) {
	installation, runtime, _ := nativeFixture(t, kernel.ProviderCodex)
	source := "/private/factory/changes/0123456789abcdef0123456789abcdef"
	runtime, err := runtime.WithReadOnlySources([]string{source})
	if err != nil {
		t.Fatal(err)
	}
	request := requestFor(t, kernel.ProviderCodex, installation, runtime, "", "")
	launch, err := Build(request)
	if err != nil {
		t.Fatal(err)
	}
	policy := ""
	for _, argument := range launch.Argv() {
		if strings.HasPrefix(argument, "permissions."+codexPermissionName(runtime)+"=") {
			policy = argument
			break
		}
	}
	if !strings.Contains(policy, tomlBasicString(source)+`="read"`) {
		t.Fatalf("launch omitted scoped source permission: %q", policy)
	}
	if strings.Contains(policy, tomlBasicString("/private/factory/changes")+`="read"`) {
		t.Fatal("source grant widened to the Changes parent")
	}
	if strings.Contains(policy, tomlBasicString("/private/factory/changes/ffffffffffffffffffffffffffffffff")+`="read"`) {
		t.Fatal("source grant included an unrelated retained Change")
	}
	for _, bad := range [][]string{{"relative"}, {source, source}, {runtime.home}} {
		if _, err := runtime.WithReadOnlySources(bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("sources %q = %v, want ErrInvalid", bad, err)
		}
	}
}
