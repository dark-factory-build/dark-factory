//go:build darwin

package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/gitauthor"
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

// testAgentID and testIncarnationID stand in for the durable agent and task
// incarnation identifiers a real supervisor call site always supplies.
const (
	testAgentID       = "agent-fixture"
	testIncarnationID = "incarnation-fixture"
)

func roleRequestFor(t *testing.T, kind kernel.Provider, installation Installation, runtime RuntimePaths, model, effort string, role kernel.AgentRole) Request {
	t.Helper()
	request, err := NewRequest(kind, installation, model, effort, runtime, filepath.Join(t.TempDir(), "change"), role, testAgentID, testIncarnationID)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestPrepareGoModuleCacheCreatesTrustedTree(t *testing.T) {
	if err := (RuntimePaths{}).PrepareGoModuleCache(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid runtime account home error = %v", err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	accountHome := filepath.Join(root, "account")
	if err := os.Mkdir(accountHome, 0o700); err != nil {
		t.Fatal(err)
	}
	runtime := runtimeFixture(t, "/usr/bin:/bin", accountHome)
	if err := runtime.PrepareGoModuleCache(); err != nil {
		t.Fatal(err)
	}
	cache := goModuleCachePath(accountHome)
	info, err := os.Stat(cache)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("trusted Go module cache is not a directory: %s", cache)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("trusted Go module cache mode = %o, want 700", info.Mode().Perm())
	}
}

func TestPrepareWebDependenciesInstallsOfflineFromTheTrustedStore(t *testing.T) {
	root := t.TempDir()
	toolDir := filepath.Join(root, "tools")
	if err := os.Mkdir(toolDir, 0o700); err != nil {
		t.Fatal(err)
	}
	node := filepath.Join(toolDir, "node")
	if err := os.WriteFile(node, []byte("#!/bin/sh\ncase \"$*\" in *--offline*--frozen-lockfile*--config.confirmModulesPurge=false*--config.storeDir=*--config.virtualStoreDir=*--config.stateDir=*) printf '%s' \"$COREPACK_ENABLE_NETWORK $pnpm_config_store_dir\" > .registry; mkdir -p node_modules/.pnpm; exit 0 ;; *) printf ran > .pnpmfile-ran; exit 1 ;; esac\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	account := filepath.Join(root, "account")
	if err := os.MkdirAll(filepath.Join(account, "Library", "Caches", "dark-factory", "local-ci", "trusted", "pnpm-store"), 0o700); err != nil {
		t.Fatal(err)
	}
	corepackPnpm := filepath.Join(account, "Library", "Caches", "dark-factory", "local-ci", "trusted", "corepack", "v1", "pnpm", pnpmVersion)
	if err := os.MkdirAll(corepackPnpm, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corepackPnpm, "package.json"), []byte(`{"name":"pnpm","version":"11.19.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := runtimeFixture(t, toolDir, account)
	working := filepath.Join(root, "change")
	web := filepath.Join(working, "web")
	if err := os.MkdirAll(web, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"package.json":   `{"packageManager":"pnpm@11.19.0"}`,
		"pnpm-lock.yaml": "packages:\n  x@1.0.0:\n    resolution: {integrity: sha512-AAAA+/=}\n",
	} {
		if err := os.WriteFile(filepath.Join(web, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := runtime.PrepareWebDependencies(context.Background(), working); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(web, ".pnpmfile-ran")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pnpmfile ran: %v", err)
	}
	if err := os.WriteFile(filepath.Join(web, ".pnpmfile.cjs"), []byte("require('fs').writeFileSync('.pnpmfile-ran', 'ran')"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runtime.PrepareWebDependencies(context.Background(), working); err == nil {
		t.Fatal("accepted project pnpmfile")
	}
	if err := os.Remove(filepath.Join(web, ".pnpmfile.cjs")); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(web, ".registry")); err != nil || string(got) != "0 "+pnpmStorePath(account) {
		t.Fatalf("install registry = %q, %v", got, err)
	}
	for name, content := range map[string]string{
		".npmrc":              "@cloudflare:registry=https://unlisted.example\n",
		"pnpm-workspace.yaml": "'@cloudflare:registry': https://unlisted.example\n",
		"pnpm-lock.yaml":      "    resolution: {tarball: https://unlisted.example/x.tgz}\n",
	} {
		original, _ := os.ReadFile(filepath.Join(web, name))
		if err := os.WriteFile(filepath.Join(web, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := runtime.PrepareWebDependencies(context.Background(), working); err == nil {
			t.Fatalf("accepted unlisted source in %s", name)
		}
		restore := func() error { return os.Remove(filepath.Join(web, name)) }
		if original != nil {
			restore = func() error { return os.WriteFile(filepath.Join(web, name), original, 0o600) }
		}
		if err := restore(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPrepareWebDependenciesRunsRealOfflineWebTests(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	corepack, err := exec.LookPath("corepack")
	if err != nil {
		t.Fatal(err)
	}
	corepackHome := os.Getenv("COREPACK_HOME")
	if corepackHome == "" {
		t.Fatal("COREPACK_HOME is required for the real offline web preparation test")
	}
	root := t.TempDir()
	account := filepath.Join(root, "account")
	store := pnpmStorePath(account)
	corepackSource := filepath.Join(corepackHome, "v1", "pnpm", pnpmVersion)
	if _, err := os.Stat(filepath.Join(corepackSource, "package.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(store), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := copyProviderTree(t, corepackSource, filepath.Join(account, "Library", "Caches", "dark-factory", "local-ci", "trusted", "corepack", "v1", "pnpm", pnpmVersion)); err != nil {
		t.Fatal(err)
	}
	working := filepath.Join(root, "change")
	if err := copyProviderTree(t, filepath.Join("..", "..", "web"), filepath.Join(working, "web")); err != nil {
		t.Fatal(err)
	}
	if err := copyProviderTree(t, filepath.Join("..", "..", "protocol"), filepath.Join(working, "protocol")); err != nil {
		t.Fatal(err)
	}
	toolDir := filepath.Dir(node)
	host := exec.Command(node, corepack, "pnpm", "fetch", "--frozen-lockfile", "--store-dir="+store)
	host.Dir = filepath.Join(working, "web")
	host.Env = append(os.Environ(), "COREPACK_ENABLE_NETWORK=1", "COREPACK_HOME="+corepackHome, "pnpm_config_store_dir="+store)
	if output, err := host.CombinedOutput(); err != nil {
		t.Fatalf("host pnpm fetch: %v: %s", err, output)
	}
	runtime := runtimeFixture(t, strings.Join([]string{toolDir, "/usr/bin", "/bin"}, string(filepath.ListSeparator)), account)
	if err := runtime.PrepareWebDependencies(context.Background(), working); err != nil {
		t.Fatal(err)
	}
	testCommand := exec.Command(node, corepack, "pnpm", "test")
	testCommand.Dir = filepath.Join(working, "web")
	testCommand.Env = append(runtime.environmentForRole(kernel.ProviderCodex, kernel.RoleWorker), "CI=1")
	if output, err := testCommand.CombinedOutput(); err != nil {
		t.Fatalf("offline fresh-Change web tests: %v: %s", err, output)
	}
}

func copyProviderTree(t *testing.T, source, destination string) error {
	t.Helper()
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		nodeModules := string(filepath.Separator) + "node_modules"
		if relative == "node_modules" || strings.HasPrefix(relative, "node_modules"+string(filepath.Separator)) || strings.HasSuffix(relative, nodeModules) || strings.Contains(relative, nodeModules+string(filepath.Separator)) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in real web fixture: %s", relative)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(target, contents, info.Mode().Perm())
	})
}

func TestCommittedWebWorkspaceUsesOnlyTheRegistry(t *testing.T) {
	if err := validatePnpmSources(filepath.Join("..", "..", "web")); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareWebDependenciesRejectsSymlinkedWebWorkspace(t *testing.T) {
	root := t.TempDir()
	runtime := runtimeFixture(t, "/usr/bin:/bin", filepath.Join(root, "account"))
	working := filepath.Join(root, "change")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(working, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(working, "web")); err != nil {
		t.Fatal(err)
	}
	if err := runtime.PrepareWebDependencies(context.Background(), working); err == nil {
		t.Fatal("symlinked web workspace was accepted")
	}
}

func TestPrepareGoModuleCacheConvergesConcurrentCreation(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	accountHome := filepath.Join(root, "account")
	if err := os.Mkdir(accountHome, 0o700); err != nil {
		t.Fatal(err)
	}
	runtime := runtimeFixture(t, "/usr/bin:/bin", accountHome)
	const workers = 16
	errorsFound := make(chan error, workers)
	var group sync.WaitGroup
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			errorsFound <- runtime.PrepareGoModuleCache()
		}()
	}
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatalf("concurrent trusted Go module cache preparation failed: %v", err)
		}
	}
}

func TestPrepareGoModuleCacheRejectsUnsafeExistingMode(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	accountHome := filepath.Join(root, "account")
	if err := os.MkdirAll(filepath.Join(accountHome, "Library"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(accountHome, "Library"), 0o777); err != nil {
		t.Fatal(err)
	}
	runtime := runtimeFixture(t, "/usr/bin:/bin", accountHome)
	if err := runtime.PrepareGoModuleCache(); err == nil {
		t.Fatal("group/other-writable trusted Go module cache ancestor was accepted")
	}
}

func TestPrepareGoModuleCacheRejectsUnsafeExistingOwner(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("ownership fixture requires a non-root managed account")
	}
	runtime := runtimeFixture(t, "/usr/bin:/bin", "/private")
	if err := runtime.PrepareGoModuleCache(); err == nil {
		t.Fatal("root-owned trusted Go module cache account was accepted")
	}
}

func TestPrepareGoModuleCacheRejectsSymlinkAncestor(t *testing.T) {
	accountRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	linked, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	accountHome := filepath.Join(accountRoot, "account")
	linked = filepath.Join(linked, "linked-library")
	if err := os.Mkdir(accountHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(linked, filepath.Join(accountHome, "Library")); err != nil {
		t.Fatal(err)
	}
	runtime := runtimeFixture(t, "/usr/bin:/bin", accountHome)
	if err := runtime.PrepareGoModuleCache(); err == nil {
		t.Fatal("symlinked trusted Go module cache ancestor was accepted")
	}
	if _, err := os.Stat(filepath.Join(linked, "Caches")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink target was modified: %v", err)
	}
}

// wantClaudeWorkerSessionFlag computes the exact --session-id/--resume pair
// Build derives for a Claude Code worker request, so exact-argv tests can
// splice in the value without hardcoding a UUID that depends on per-test
// temp-directory paths.
func wantClaudeWorkerSessionFlag(t *testing.T, request Request) []string {
	t.Helper()
	id, resume, err := claudeSessionSelection(request.runtime, request.workingDirectory, request.agentID, request.taskIncarnationID)
	if err != nil {
		t.Fatal(err)
	}
	if resume {
		return []string{"--resume", id}
	}
	return []string{"--session-id", id}
}

// Claude receives only the installed attempt server, plus factoryd's
// Maintainer server for a connected home's orchestrators; account and
// Change-local MCP configuration is not trusted.
func TestBuildOrchestratorClaudeIsGivenTheMaintainer(t *testing.T) {
	installation, runtime, locator := nativeFixture(t, kernel.ProviderClaudeCode)
	workerRequest := roleRequestFor(t, kernel.ProviderClaudeCode, installation, runtime, "", "", kernel.RoleWorker)
	worker, err := Build(workerRequest)
	if err != nil {
		t.Fatal(err)
	}
	wantWorkerArgv := append([]string{"/usr/bin/true", "--permission-mode", "dontAsk", "--setting-sources", ""}, wantClaudeWorkerSessionFlag(t, workerRequest)...)
	wantWorkerArgv = append(wantWorkerArgv, "--strict-mcp-config", "--agents", claudeSweepAgent, "--settings", wantClaudeSettings(t, workerRequest, "factory_attempt"), "--mcp-config", wantClaudeServers(t, workerRequest, nil), "--", bootstrapPromptFor(workerRequest, "factory_attempt", false))
	if !reflect.DeepEqual(worker.Argv(), wantWorkerArgv) {
		t.Fatalf("worker argv = %q, want %q", worker.Argv(), wantWorkerArgv)
	}
	for _, required := range []string{
		"DARK_FACTORY_SOCKET=" + runtime.socket,
		"DARK_FACTORY_ATTEMPT_TOKEN_FILE=" + runtime.token,
		"DARK_FACTORY_FACTORYCTL=" + runtime.factoryctl,
	} {
		if !slices.Contains(worker.Environment(), required) {
			t.Fatalf("Claude worker attempt transport lacks %q: %q", required, worker.Environment())
		}
	}
	// An unconnected home's orchestrator has no Maintainer server at all.
	unconnected := roleRequestFor(t, kernel.ProviderClaudeCode, installation, runtime, "", "", kernel.RoleOrchestrator)
	launch, err := Build(unconnected)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/bin/true", "--permission-mode", "dontAsk", "--setting-sources", "", "--strict-mcp-config", "--settings", wantClaudeSettings(t, unconnected, "factory_attempt"), "--mcp-config", wantClaudeServers(t, unconnected, nil), "--", bootstrapPromptFor(unconnected, "factory_attempt", false)}
	if !reflect.DeepEqual(launch.Argv(), want) {
		t.Fatalf("unconnected orchestrator argv = %q, want %q", launch.Argv(), want)
	}
	overseerRequest := roleRequestFor(t, kernel.ProviderClaudeCode, installation, runtime.WithCustomerMaintainer(true), "", "", kernel.RoleOrchestrator)
	launch, err = Build(overseerRequest)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"/usr/bin/true", "--permission-mode", "dontAsk", "--setting-sources", "", "--strict-mcp-config", "--settings", wantClaudeSettings(t, overseerRequest, "factory_attempt", "maintainer"), "--mcp-config", wantClaudeServers(t, overseerRequest, map[string]any{"maintainer": map[string]any{"command": runtime.factoryctl, "args": []string{"attempt", "maintainer-mcp"}}}), "--", bootstrapPromptFor(overseerRequest, "factory_attempt", false)}
	if !reflect.DeepEqual(launch.Argv(), want) {
		t.Fatalf("orchestrator argv = %q, want %q", launch.Argv(), want)
	}
	if _, err := runner.PrepareCommittedExecSpec(launch.Executable(), launch.Argv(), launch.Environment(), t.TempDir()); err != nil {
		t.Fatalf("runner rejected Claude overseer environment: %v", err)
	}
	// An installed bridge is a script, which the CLI commitment would refuse;
	// it needs only to be a regular executable nobody but its owner can write.
	// One that is present but unfit is refused by name.
	bridge := filepath.Join(filepath.Dir(locator), "dark-factory-browser-mcp")
	for name, mode := range map[string]os.FileMode{"not executable": 0o644, "group writable": 0o775} {
		if err := os.WriteFile(bridge, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(bridge, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := Build(workerRequest); !errors.Is(err, ErrUnavailable) || !errors.Is(err, errBridgeUnfit) {
			t.Fatalf("%s bridge = %v, want ErrUnavailable and errBridgeUnfit", name, err)
		}
	}
	if _, err := NewRequest(kernel.ProviderClaudeCode, installation, "", "", runtime, "/private/change", kernel.AgentRole(0), testAgentID, testIncarnationID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("request without a role = %v, want ErrInvalid", err)
	}
	for _, part := range []string{"", string([]byte{0xff}), "id\x00suffix"} {
		if _, err := NewRequest(kernel.ProviderClaudeCode, installation, "", "", runtime, "/private/change", kernel.RoleWorker, part, testIncarnationID); !errors.Is(err, ErrInvalid) {
			t.Fatalf("request with agent id %q = %v, want ErrInvalid", part, err)
		}
		if _, err := NewRequest(kernel.ProviderClaudeCode, installation, "", "", runtime, "/private/change", kernel.RoleWorker, testAgentID, part); !errors.Is(err, ErrInvalid) {
			t.Fatalf("request with task incarnation id %q = %v, want ErrInvalid", part, err)
		}
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
		"DARK_FACTORY_TASK_ATTACHMENTS=" + filepath.Join(runtime.home, "task-attachments"),
		"DARK_FACTORY_SOCKET=" + runtime.socket,
		"DARK_FACTORY_ATTEMPT_TOKEN_FILE=" + runtime.token,
		"DARK_FACTORY_FACTORYCTL=" + runtime.factoryctl,
		"TMPDIR=" + runtime.temp,
		"PATH=" + runtime.toolPath,
		"HOME=" + runtime.home,
		"DEVELOPER_DIR=" + install.TrustedSystemToolchainRoot(),
		"LANG=C", "LC_ALL=C", "TERM=xterm-256color", "SHELL=/bin/sh",
		"GIT_AUTHOR_NAME=" + GitIdentityName, "GIT_AUTHOR_EMAIL=" + GitIdentityEmail, "GIT_COMMITTER_NAME=" + GitIdentityName, "GIT_COMMITTER_EMAIL=" + GitIdentityEmail,
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
			kind: kernel.ProviderClaudeCode, model: "claude-model", effort: "max", wantDelivery: TaskDeliveryPrompt,
			wantArgv: []string{"/usr/bin/true", "--permission-mode", "dontAsk", "--setting-sources", "", "--model", "claude-model", "--effort", "max", "--strict-mcp-config", "--agents", claudeSweepAgent},
		},
		{kind: kernel.ProviderCodex, model: "codex-model", effort: "xhigh", wantDelivery: TaskDeliveryPrompt,
			wantArgv: []string{"/usr/bin/true", "-c", `hooks.Stop=[{hooks=[{type="command",command="<factoryctl>"}]}]`, "--strict-config", "--dangerously-bypass-hook-trust", "--no-alt-screen", "-c", `tui.resume_cwd="current"`, "-c", "check_for_update_on_startup=false", "-c", "tool_output_token_limit=32768", "-c", "projects=<working-directory>", "--model", "codex-model", "-c", `model_reasoning_effort="xhigh"`, bootstrapPrompt},
		},
	}
	for _, test := range tests {
		t.Run(test.kind.String(), func(t *testing.T) {
			const privateTaskSentinel = "PRIVATE_TASK_SENTINEL"
			task := []byte(privateTaskSentinel + "\nline 1\n\"quoted\"\x1b café 😀\u007f\u0085")
			installation, runtime, _ := nativeFixture(t, test.kind)
			request := requestFor(t, test.kind, installation, runtime, test.model, test.effort).WithTask(task)
			launch, err := Build(request)
			if err != nil {
				t.Fatal(err)
			}
			if launch.TaskDelivery() != test.wantDelivery {
				t.Fatalf("task delivery=%d, want %d", launch.TaskDelivery(), test.wantDelivery)
			}
			wantArgv := test.wantArgv
			if test.kind == kernel.ProviderClaudeCode {
				wantArgv = slices.Insert(slices.Clone(wantArgv), 5, wantClaudeWorkerSessionFlag(t, request)...)
				wantArgv = append(wantArgv, "--settings", wantClaudeSettings(t, request, "factory_attempt"), "--mcp-config", wantClaudeServers(t, request, nil), "--", bootstrapPromptFor(request, "factory_attempt", false))
			}
			if test.kind == kernel.ProviderCodex {
				stop := "'" + strings.ReplaceAll(runtime.factoryctl, "'", `'\''`) + "' attempt turn-complete"
				wantArgv[2] = "hooks.Stop=[{hooks=[{type=\"command\",command=" + tomlBasicString(stop) + "}]}]"
				permissions, err := codexPermissions(request)
				if err != nil {
					t.Fatal(err)
				}
				wantArgv = slices.Replace(wantArgv, 12, 14, "-c", codexUntrustedProjectConfig(request.workingDirectory), "-c", "default_permissions="+tomlBasicString(codexPermissionName(request.runtime)), "-c", `approval_policy="never"`, "-c", permissions, "--disable", "computer_use", "--disable", "browser_use", "--disable", "plugins", "-c", "mcp_servers."+codexAttemptServerName(request.runtime)+"={command="+tomlBasicString(runtime.factoryctl)+`,args=["attempt","mcp"],env_vars=["DARK_FACTORY_SOCKET","DARK_FACTORY_ATTEMPT_TOKEN_FILE","DARK_FACTORY_OVERSEER_SESSION_RESUMED"],enabled=true,required=true,tools={factory={approval_mode="approve"}}}`)
				wantArgv[len(wantArgv)-1] = bootstrapPromptFor(request, codexAttemptServerName(request.runtime), false)
			}
			if got := launch.Argv(); !slices.Equal(got, wantArgv) {
				t.Fatalf("argv=%q, want %q", got, wantArgv)
			}
			wantHome := runtime.home
			if test.kind == kernel.ProviderClaudeCode {
				wantHome = runtime.accountHome
				if account, err := user.Current(); err != nil || !slices.Contains(launch.Environment(), "USER="+account.Username) {
					t.Fatalf("claude environment lacks USER for its keychain login: %q", launch.Environment())
				}
			} else {
				wantConfig := "CODEX_HOME=" + filepath.Join(runtime.accountHome, codexConfigDir)
				if !slices.Contains(launch.Environment(), wantConfig) {
					t.Fatalf("environment lacks exact config root %q: %q", wantConfig, launch.Environment())
				}
			}
			if !slices.Contains(launch.Environment(), "HOME="+wantHome) {
				t.Fatalf("environment lacks exact HOME %q: %q", wantHome, launch.Environment())
			}
			if test.kind == kernel.ProviderClaudeCode && !slices.Contains(launch.Environment(), "DISABLE_AUTOUPDATER=1") {
				t.Fatalf("Claude launch lacks updater suppression: %q", launch.Environment())
			}
			if test.kind == kernel.ProviderCodex && slices.Contains(launch.Environment(), "DISABLE_AUTOUPDATER=1") {
				t.Fatalf("Codex launch inherited Claude updater suppression: %q", launch.Environment())
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

			// The task is in the prompt exactly once, at its end, and nowhere else.
			argv := launch.Argv()
			if prompt := argv[len(argv)-1]; strings.Count(prompt, privateTaskSentinel) != 1 || !strings.HasSuffix(prompt, "\n\nTask:\n"+string(task)) {
				t.Fatalf("prompt does not end with the exact task once: %q", prompt)
			}
			for _, value := range append(argv[:len(argv)-1], launch.Environment()...) {
				if strings.Contains(value, privateTaskSentinel) {
					t.Fatalf("task reached provider argv or environment outside its prompt: %q", value)
				}
			}
			delivery, payload, err := PrepareTask(test.kind, task)
			if err != nil {
				t.Fatal(err)
			}
			if delivery != launch.TaskDelivery() {
				t.Fatalf("prepared delivery=%d, want %d", delivery, launch.TaskDelivery())
			}
			for _, rule := range []string{"Never recursively search the user home", "command -v", "report the missing prerequisite"} {
				if !strings.Contains(launch.Argv()[len(launch.Argv())-1], rule) {
					t.Fatalf("native provider lacks scoped discovery rule %q", rule)
				}
			}
			if !bytes.Equal(payload, task) {
				t.Fatalf("prepared prompt task = %q, want %q", payload, task)
			}
		})
	}
}

func TestCodexAttemptMCPNamesAreBoundToRuntimeHome(t *testing.T) {
	installation, firstRuntime, _ := nativeFixture(t, kernel.ProviderCodex)
	secondRuntime := runtimeFixture(t, firstRuntime.toolPath, firstRuntime.accountHome)
	first, err := Build(requestFor(t, kernel.ProviderCodex, installation, firstRuntime, "", ""))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(requestFor(t, kernel.ProviderCodex, installation, secondRuntime, "", ""))
	if err != nil {
		t.Fatal(err)
	}
	configuredName := func(argv []string) string {
		for _, arg := range argv {
			if !strings.HasPrefix(arg, "mcp_servers.factory_attempt_") {
				continue
			}
			name, _, _ := strings.Cut(strings.TrimPrefix(arg, "mcp_servers."), "=")
			return name
		}
		t.Fatalf("Codex launch has no attempt MCP server: %q", argv)
		return ""
	}
	firstName := configuredName(first.Argv())
	secondName := configuredName(second.Argv())
	if firstName == secondName {
		t.Fatalf("distinct runtime homes configured the same MCP server %q", firstName)
	}
	for _, test := range []struct {
		name string
		argv []string
	}{
		{name: firstName, argv: first.Argv()},
		{name: secondName, argv: second.Argv()},
	} {
		if !strings.Contains(strings.Join(test.argv, "\n"), "mcp__"+test.name+"__factory tool") {
			t.Fatalf("bootstrap prompt does not name its configured server %q: %q", test.name, test.argv)
		}
	}
}

// A worker's Claude Code launch starts a fresh, deterministic session when
// nothing is on disk yet for its exact cwd, and never for an orchestrator.
func TestClaudeWorkerSessionStartsFreshWhenNoTranscriptExists(t *testing.T) {
	installation, runtime, _ := nativeFixture(t, kernel.ProviderClaudeCode)
	cwd := filepath.Join(t.TempDir(), "change")
	id, resume, err := claudeSessionSelection(runtime, cwd, testAgentID, testIncarnationID)
	if err != nil {
		t.Fatal(err)
	}
	if resume {
		t.Fatal("resume=true with no transcript on disk")
	}
	request, err := NewRequest(kernel.ProviderClaudeCode, installation, "", "", runtime, cwd, kernel.RoleWorker, testAgentID, testIncarnationID)
	if err != nil {
		t.Fatal(err)
	}
	launch, err := Build(request)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(launch.Argv(), "--session-id") || slices.Contains(launch.Argv(), "--resume") {
		t.Fatalf("worker argv = %q, want --session-id %q and no --resume", launch.Argv(), id)
	}
	if i := slices.Index(launch.Argv(), "--session-id"); i < 0 || launch.Argv()[i+1] != id {
		t.Fatalf("worker argv = %q, want --session-id %q", launch.Argv(), id)
	}
	// TestBuildOrchestratorClaudeIsGivenTheMaintainer covers the
	// orchestrator side: its exact worker/orchestrator argv comparison shows
	// no --session-id/--resume reaches an orchestrator launch. Its cwd is a
	// fresh directory on every run (internal/daemon: the runtime root behind
	// it), so a chosen id could never be found again; this is not wired up
	// as a no-op that would never fire.
}

// The transcript directory name is the CLI's, observed from a real launch,
// not this package's own function fed back to itself: a daemon home under a
// dotted directory (`~/.dark-factory-recovered`) produced
// `-Users-op--dark-factory-recovered-changes-<id>`. A wrong escape relaunches
// `--session-id` with an id the CLI refuses as already in use (exit 1 before
// any attempt outcome), which is how every send-back retry on a Claude worker
// failed on 2026-09-17.
func TestClaudeProjectPathEscapeMatchesTheCLI(t *testing.T) {
	if got, want := escapeClaudeProjectPath("/Users/op/.dark-factory-recovered/changes/f01a5315d8e32af9b23058b314368c4f"), "-Users-op--dark-factory-recovered-changes-f01a5315d8e32af9b23058b314368c4f"; got != want {
		t.Fatalf("escape = %q, want %q", got, want)
	}
	if got, want := escapeClaudeProjectPath("/private/tmp/df_dev.X1/factory"), "-private-tmp-df-dev-X1-factory"; got != want {
		t.Fatalf("escape = %q, want %q", got, want)
	}
}

// A worker's retry (send-back on the same task incarnation, reusing the same
// Change directory: see internal/kernel/change.go) resumes its own
// conversation once that conversation's transcript exists on disk, instead of
// starting an unrelated one. The cwd carries a dotted component, the shape of
// a real daemon home.
func TestClaudeWorkerSessionResumesWhenTranscriptExistsUnderLimit(t *testing.T) {
	installation, runtime, _ := nativeFixture(t, kernel.ProviderClaudeCode)
	cwd := filepath.Join(t.TempDir(), ".dark-factory", "changes", "change")
	id, resume, err := claudeSessionSelection(runtime, cwd, testAgentID, testIncarnationID)
	if err != nil || resume {
		t.Fatalf("initial selection id=%q resume=%v err=%v", id, resume, err)
	}
	transcriptDir := filepath.Join(ConfigHome(kernel.ProviderClaudeCode, runtime.accountHome), "projects", escapeClaudeProjectPath(cwd))
	if err := os.MkdirAll(transcriptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(transcriptDir, id+".jsonl"), []byte(`{"type":"session_meta"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request, err := NewRequest(kernel.ProviderClaudeCode, installation, "", "", runtime, cwd, kernel.RoleWorker, testAgentID, testIncarnationID)
	if err != nil {
		t.Fatal(err)
	}
	launch, err := Build(request)
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.Index(launch.Argv(), "--resume"); i < 0 || launch.Argv()[i+1] != id {
		t.Fatalf("worker argv = %q, want --resume %q", launch.Argv(), id)
	}
	if slices.Contains(launch.Argv(), "--session-id") {
		t.Fatalf("worker argv = %q, want no --session-id once a transcript exists", launch.Argv())
	}
}

// A linked Claude account's launch exports CLAUDE_CONFIG_DIR set to the
// account's own directory (see TestAccountConfigDirectorySelectsTheProviderLogin),
// so its transcripts live under that directory, not the account home's own
// default .claude; discovery must look in the exact same place the launch
// itself was actually told to use (claudeConfigHome, shared with
// environment()), or a retry on a linked account would always see a fresh
// session instead of resuming its own.
func TestClaudeWorkerLinkedAccountSessionResumesUnderItsOwnConfigDirectory(t *testing.T) {
	installation, runtime, _ := nativeFixture(t, kernel.ProviderClaudeCode)
	accountConfig := filepath.Join(t.TempDir(), "claude-dogfood")
	if err := os.Mkdir(accountConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	linked, err := NewRuntimePaths(runtime.home, runtime.temp, runtime.socket, runtime.token, runtime.factoryctl, runtime.gitCeiling, runtime.toolPath, runtime.accountHome, accountConfig, "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(linked.environment(kernel.ProviderClaudeCode), "CLAUDE_CONFIG_DIR="+accountConfig) {
		t.Fatalf("fixture is not actually a linked account launch: %q", linked.environment(kernel.ProviderClaudeCode))
	}
	cwd := filepath.Join(t.TempDir(), "change")
	id, resume, err := claudeSessionSelection(linked, cwd, testAgentID, testIncarnationID)
	if err != nil || resume {
		t.Fatalf("initial selection id=%q resume=%v err=%v", id, resume, err)
	}
	// The transcript lives directly under the linked account's own
	// directory, not a further nested .claude beneath it.
	transcriptDir := filepath.Join(accountConfig, "projects", escapeClaudeProjectPath(cwd))
	if err := os.MkdirAll(transcriptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(transcriptDir, id+".jsonl"), []byte(`{"type":"session_meta"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request, err := NewRequest(kernel.ProviderClaudeCode, installation, "", "", linked, cwd, kernel.RoleWorker, testAgentID, testIncarnationID)
	if err != nil {
		t.Fatal(err)
	}
	launch, err := Build(request)
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.Index(launch.Argv(), "--resume"); i < 0 || launch.Argv()[i+1] != id {
		t.Fatalf("linked worker argv = %q, want --resume %q", launch.Argv(), id)
	}
	if slices.Contains(launch.Argv(), "--session-id") {
		t.Fatalf("linked worker argv = %q, want no --session-id once a transcript exists", launch.Argv())
	}
}

// Bounding growth: a generation whose transcript has reached the rotation
// ceiling is retired in favor of a fresh one, the simplest measurable rule
// for a task incarnation with an unbounded number of send-back retries.
func TestClaudeWorkerSessionRotatesWhenTranscriptExceedsLimit(t *testing.T) {
	installation, runtime, _ := nativeFixture(t, kernel.ProviderClaudeCode)
	cwd := filepath.Join(t.TempDir(), "change")
	firstGeneration, resume, err := claudeSessionSelection(runtime, cwd, testAgentID, testIncarnationID)
	if err != nil || resume {
		t.Fatalf("initial selection id=%q resume=%v err=%v", firstGeneration, resume, err)
	}
	transcriptDir := filepath.Join(ConfigHome(kernel.ProviderClaudeCode, runtime.accountHome), "projects", escapeClaudeProjectPath(cwd))
	if err := os.MkdirAll(transcriptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	oversized, err := os.Create(filepath.Join(transcriptDir, firstGeneration+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := oversized.Truncate(nativeSessionRotateBytes); err != nil {
		t.Fatal(err)
	}
	if err := oversized.Close(); err != nil {
		t.Fatal(err)
	}
	secondGeneration, resume, err := claudeSessionSelection(runtime, cwd, testAgentID, testIncarnationID)
	if err != nil {
		t.Fatal(err)
	}
	if resume || secondGeneration == firstGeneration {
		t.Fatalf("rotation id=%q resume=%v, want a fresh id distinct from %q", secondGeneration, resume, firstGeneration)
	}
	request, err := NewRequest(kernel.ProviderClaudeCode, installation, "", "", runtime, cwd, kernel.RoleWorker, testAgentID, testIncarnationID)
	if err != nil {
		t.Fatal(err)
	}
	launch, err := Build(request)
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.Index(launch.Argv(), "--session-id"); i < 0 || launch.Argv()[i+1] != secondGeneration {
		t.Fatalf("worker argv = %q, want --session-id %q", launch.Argv(), secondGeneration)
	}
}

// writeFakeCodexRollout creates a minimal Codex rollout file whose first
// JSON line records the given session id and cwd, the two fields
// codexSessionSelection reads. size, when nonzero, pads the file (sparsely)
// to an exact total size for rotation tests; it must be at least the header
// line's own length.
func writeFakeCodexRollout(t *testing.T, dayDirectory, timestamp, id, cwd string, size int64) string {
	t.Helper()
	if err := os.MkdirAll(dayDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dayDirectory, fmt.Sprintf("rollout-%s-%s.jsonl", timestamp, id))
	line, err := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": id, "cwd": cwd}})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	if size > 0 {
		if err := file.Truncate(size); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// A worker's Codex launch is unchanged (no "resume" leading argv) when its
// account has no rollout history at all yet for its exact cwd.
func TestCodexWorkerStartsFreshWhenNoRolloutExists(t *testing.T) {
	installation, runtime, _ := nativeFixture(t, kernel.ProviderCodex)
	cwd := filepath.Join(t.TempDir(), "change")
	request, err := NewRequest(kernel.ProviderCodex, installation, "", "", runtime, cwd, kernel.RoleWorker, testAgentID, testIncarnationID)
	if err != nil {
		t.Fatal(err)
	}
	launch, err := Build(request)
	if err != nil {
		t.Fatal(err)
	}
	if launch.Argv()[1] == "resume" {
		t.Fatalf("worker argv = %q, want no resume with no rollout on disk", launch.Argv())
	}
}

// A worker's Codex launch resumes the newest rollout recorded for its exact
// cwd, ahead of any older rollout for that same cwd and any rollout, however
// new, for a different one.
func TestCodexWorkerResumesTheNewestRolloutForItsExactCwd(t *testing.T) {
	installation, runtime, _ := nativeFixture(t, kernel.ProviderCodex)
	cwd := filepath.Join(t.TempDir(), "change")
	sessionsRoot := filepath.Join(codexConfigHome(runtime), "sessions")
	writeFakeCodexRollout(t, filepath.Join(sessionsRoot, "2026", "09", "17"), "2026-09-17T09-00-00", "01a00000-0000-7000-8000-00000000fefe", "/unrelated/cwd", 0)
	writeFakeCodexRollout(t, filepath.Join(sessionsRoot, "2026", "09", "16"), "2026-09-16T08-00-00", "01a00000-0000-7000-8000-000000000aaa", cwd, 0)
	const newest = "01a00000-0000-7000-8000-000000000bbb"
	writeFakeCodexRollout(t, filepath.Join(sessionsRoot, "2026", "09", "17"), "2026-09-17T08-00-00", newest, cwd, 0)
	request, err := NewRequest(kernel.ProviderCodex, installation, "", "", runtime, cwd, kernel.RoleWorker, testAgentID, testIncarnationID)
	if err != nil {
		t.Fatal(err)
	}
	launch, err := Build(request)
	if err != nil {
		t.Fatal(err)
	}
	if launch.Argv()[1] != "resume" || launch.Argv()[2] != newest {
		t.Fatalf("worker argv = %q, want a leading resume of %q", launch.Argv(), newest)
	}
}

// A rollout recorded for a different cwd is never mistaken for this one's own.
func TestCodexWorkerIgnoresRolloutsForOtherCwds(t *testing.T) {
	installation, runtime, _ := nativeFixture(t, kernel.ProviderCodex)
	cwd := filepath.Join(t.TempDir(), "change")
	sessionsRoot := filepath.Join(codexConfigHome(runtime), "sessions")
	writeFakeCodexRollout(t, filepath.Join(sessionsRoot, "2026", "09", "17"), "2026-09-17T08-00-00", "01a00000-0000-7000-8000-000000000ccc", filepath.Join(t.TempDir(), "other-change"), 0)
	request, err := NewRequest(kernel.ProviderCodex, installation, "", "", runtime, cwd, kernel.RoleWorker, testAgentID, testIncarnationID)
	if err != nil {
		t.Fatal(err)
	}
	launch, err := Build(request)
	if err != nil {
		t.Fatal(err)
	}
	if launch.Argv()[1] == "resume" {
		t.Fatalf("worker argv = %q, want no resume of an unrelated cwd's rollout", launch.Argv())
	}
}

// A rollout's payload.id is untrusted file content. An oversized, NUL-laced,
// or otherwise non-UUID value must never reach argv, where the runner's own
// argument-size guard would fail the whole launch instead of
// codexSessionSelection's intended fallback to a fresh session; it is
// skipped like any other unusable candidate, and the launch stays fresh.
// The rollout's own filename stays well-formed regardless: only the CLI's
// own recorded content is untrusted, and codexSessionSelection never reads
// an id out of a filename.
func TestCodexWorkerIgnoresRolloutsWithAMalformedSessionID(t *testing.T) {
	for name, id := range map[string]string{
		"oversized":          strings.Repeat("a", runner.MaxArgumentBytes+1),
		"embedded NUL":       "01a00000-0000-7000-8000-00000000\x0000",
		"not a UUID":         "not-a-uuid-at-all",
		"uppercase hex":      "01A00000-0000-7000-8000-000000000ABC",
		"wrong hyphen shape": "01a000000-000-7000-8000-000000000abc",
	} {
		t.Run(name, func(t *testing.T) {
			installation, runtime, _ := nativeFixture(t, kernel.ProviderCodex)
			cwd := filepath.Join(t.TempDir(), "change")
			dayDirectory := filepath.Join(codexConfigHome(runtime), "sessions", "2026", "09", "17")
			if err := os.MkdirAll(dayDirectory, 0o700); err != nil {
				t.Fatal(err)
			}
			line, err := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": id, "cwd": cwd}})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dayDirectory, "rollout-2026-09-17T08-00-00-malformed.jsonl")
			if err := os.WriteFile(path, append(line, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			request, err := NewRequest(kernel.ProviderCodex, installation, "", "", runtime, cwd, kernel.RoleWorker, testAgentID, testIncarnationID)
			if err != nil {
				t.Fatal(err)
			}
			launch, err := Build(request)
			if err != nil {
				t.Fatal(err)
			}
			if launch.Argv()[1] == "resume" {
				t.Fatalf("worker argv = %q, want no resume of a malformed session id", launch.Argv())
			}
		})
	}
}

// Bounding growth: once the newest matching rollout has reached the shared
// rotation ceiling, Build starts fresh rather than resuming it or falling
// back to an older, smaller rollout for the same cwd.
func TestCodexWorkerRotatesWhenNewestMatchingRolloutExceedsLimit(t *testing.T) {
	installation, runtime, _ := nativeFixture(t, kernel.ProviderCodex)
	cwd := filepath.Join(t.TempDir(), "change")
	sessionsRoot := filepath.Join(codexConfigHome(runtime), "sessions")
	writeFakeCodexRollout(t, filepath.Join(sessionsRoot, "2026", "09", "16"), "2026-09-16T08-00-00", "01a00000-0000-7000-8000-000000000ddd", cwd, 0)
	writeFakeCodexRollout(t, filepath.Join(sessionsRoot, "2026", "09", "17"), "2026-09-17T08-00-00", "01a00000-0000-7000-8000-000000000eee", cwd, nativeSessionRotateBytes)
	request, err := NewRequest(kernel.ProviderCodex, installation, "", "", runtime, cwd, kernel.RoleWorker, testAgentID, testIncarnationID)
	if err != nil {
		t.Fatal(err)
	}
	launch, err := Build(request)
	if err != nil {
		t.Fatal(err)
	}
	if launch.Argv()[1] == "resume" {
		t.Fatalf("worker argv = %q, want fresh once the newest matching rollout is over the ceiling", launch.Argv())
	}
}

// An orchestrator's own cwd is a fresh runtime root every run, so it cannot
// carry a resumable session itself; WithPreviousWorkingDirectory (the same
// agent's last terminal run's cwd, durable through kernel.Store) is what lets
// its standing tasks share one continuing Codex session.
func TestCodexOrchestratorResumesFromPreviousWorkingDirectory(t *testing.T) {
	installation, runtime, _ := nativeFixture(t, kernel.ProviderCodex)
	previous := filepath.Join(t.TempDir(), "previous-runtime-home")
	sessionsRoot := filepath.Join(codexConfigHome(runtime), "sessions")
	const previousSession = "01a00000-0000-7000-8000-0000000000ff"
	writeFakeCodexRollout(t, filepath.Join(sessionsRoot, "2026", "09", "17"), "2026-09-17T08-00-00", previousSession, previous, 0)
	current := filepath.Join(t.TempDir(), "current-runtime-home")
	request, err := NewRequest(kernel.ProviderCodex, installation, "", "", runtime, current, kernel.RoleOrchestrator, testAgentID, testIncarnationID)
	if err != nil {
		t.Fatal(err)
	}
	withoutHint, err := Build(request)
	if err != nil {
		t.Fatal(err)
	}
	if withoutHint.Argv()[1] == "resume" {
		t.Fatalf("orchestrator argv = %q, want no resume without a previous working directory", withoutHint.Argv())
	}
	request, err = request.WithPreviousWorkingDirectory(previous)
	if err != nil {
		t.Fatal(err)
	}
	launch, err := Build(request)
	if err != nil {
		t.Fatal(err)
	}
	if launch.Argv()[1] != "resume" || launch.Argv()[2] != previousSession {
		t.Fatalf("orchestrator argv = %q, want a leading resume of %q", launch.Argv(), previousSession)
	}
}

func TestInstalledBrowserBridgeUsesOnlyRunPathsForBothProviders(t *testing.T) {
	for _, kind := range []kernel.Provider{kernel.ProviderCodex, kernel.ProviderClaudeCode} {
		t.Run(kind.String(), func(t *testing.T) {
			installation, runtime, locator := nativeFixture(t, kind)
			runtime = runtime.WithCustomerMaintainer(true)
			bridge := filepath.Join(filepath.Dir(locator), "dark-factory-browser-mcp")
			if err := os.WriteFile(bridge, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
				t.Fatal(err)
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
						found = strings.Contains(arg, tomlBasicString(runtime.temp)) && strings.Contains(arg, "required=false") && !strings.Contains(arg, "env_vars") && strings.Contains(arg, `default_tools_approval_mode="approve"`)
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
							t.Fatal("lost Maintainer server")
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

// The installed bridge starts its server in each TMPDIR shape the Change
// worker gives a run, with no attempt credentials or factory call (#1753).
func TestBrowserBridgeStartsInRunnerTemp(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("../../scripts/dark-factory-browser-mcp.py")
	if err != nil {
		t.Fatal(err)
	}
	bridge := filepath.Join(root, "dark-factory-browser-mcp")
	node := filepath.Join(root, "node")
	for path, body := range map[string]string{bridge: string(source), node: "#!/bin/sh\nprintf '%s\\n' \"$PWD\" \"$@\" > \"$0.out\"\n", root + "/cli.js": "", root + "/browser": ""} {
		if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	config, _ := json.Marshal(map[string]any{"command": []string{node, root + "/cli.js"}, "executable": root + "/browser", "origins": []string{"http://127.0.0.1:5196"}, "headless": true})
	if err := os.WriteFile(bridge+".json", config, 0o600); err != nil {
		t.Fatal(err)
	}
	// The runtime's own tmp, and the short one when the shared directory allows it.
	runtime := filepath.Join(root, "1c9ffc2e693fc7e136a920e20a3092ed")
	temps := []string{filepath.Join(runtime, "tmp")}
	if err := os.MkdirAll(temps[0], 0o700); err != nil {
		t.Fatal(err)
	}
	if short := runner.ClaimPrivateTemp(runtime); short != "" {
		t.Cleanup(func() { _ = os.RemoveAll(short) })
		temps = append(temps, short)
	}
	for _, temp := range temps {
		command := exec.Command(python, "-I", bridge, "--runtime-dir", temp)
		command.Env = []string{"PATH=/usr/bin:/bin"}
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("bridge in %s: %v: %s", temp, err, output)
		}
		launched, err := os.ReadFile(node + ".out")
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(launched), "\n")
		if !strings.HasPrefix(lines[0], temp+"/browser-") || !slices.Contains(lines, lines[0]+"/output") {
			t.Fatalf("bridge in %s launched %q", temp, lines)
		}
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
		if _, err := NewRequest(kernel.ProviderShell, shell, controls.model, controls.effort, shellRuntime, "/private/change", kernel.RoleWorker, testAgentID, testIncarnationID); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Shell controls %+v error=%v, want ErrInvalid", controls, err)
		}
	}
	if _, err := NewRequest(kernel.ProviderCodex, shell, "", "", shellRuntime, "/private/change", kernel.RoleWorker, testAgentID, testIncarnationID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatched installation error=%v, want ErrInvalid", err)
	}
	claude, claudeRuntime, _ := nativeFixture(t, kernel.ProviderClaudeCode)
	for _, effort := range []string{"ultra", "speculative"} {
		if _, err := NewRequest(kernel.ProviderClaudeCode, claude, "", effort, claudeRuntime, "/private/change", kernel.RoleWorker, testAgentID, testIncarnationID); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Claude effort %q error=%v, want ErrInvalid", effort, err)
		}
	}
	codex, codexRuntime, _ := nativeFixture(t, kernel.ProviderCodex)
	if _, err := NewRequest(kernel.ProviderCodex, codex, "", "ultra", codexRuntime, "/private/change", kernel.RoleWorker, testAgentID, testIncarnationID); err != nil {
		t.Fatalf("Codex durable ultra effort rejected: %v", err)
	}
	for _, model := range []string{string([]byte{0xff}), "model\x00suffix"} {
		if _, err := NewRequest(kernel.ProviderCodex, codex, model, "", codexRuntime, "/private/change", kernel.RoleWorker, testAgentID, testIncarnationID); !errors.Is(err, ErrInvalid) {
			t.Fatalf("model %x error=%v, want ErrInvalid", []byte(model), err)
		}
	}
	for _, workingDirectory := range []string{"", "relative/change", "/", "/private/../change", "/private/change\x00suffix", string([]byte{0xff})} {
		if _, err := NewRequest(kernel.ProviderCodex, codex, "", "", codexRuntime, workingDirectory, kernel.RoleWorker, testAgentID, testIncarnationID); !errors.Is(err, ErrInvalid) {
			t.Fatalf("working directory %q error=%v, want ErrInvalid", workingDirectory, err)
		}
	}
	tooLarge := "/" + strings.Repeat("\x7f", maxPathBytes-1)
	if len(codexUntrustedProjectConfig(tooLarge)) <= runner.MaxArgumentBytes {
		t.Fatalf("expanded project config=%d, want larger than argv bound", len(codexUntrustedProjectConfig(tooLarge)))
	}
	if _, err := NewRequest(kernel.ProviderCodex, codex, "", "", codexRuntime, tooLarge, kernel.RoleWorker, testAgentID, testIncarnationID); !errors.Is(err, ErrInvalid) {
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
	for _, kind := range []kernel.Provider{kernel.ProviderClaudeCode, kernel.ProviderCodex} {
		maximum := bytes.Repeat([]byte{'x'}, runner.MaxNativeTaskBytes)
		if delivery, payload, err := PrepareTask(kind, maximum); err != nil || delivery != TaskDeliveryPrompt || !bytes.Equal(payload, maximum) {
			t.Fatalf("%s maximum API task delivery=(%d, %d bytes), error=%v", kind, delivery, len(payload), err)
		}
		if _, _, err := PrepareTask(kind, append(maximum, 'x')); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s over-limit task error=%v, want ErrInvalid", kind, err)
		}
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
	var concurrentConfig struct {
		Projects map[string]struct {
			Trusted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(final, &concurrentConfig); err != nil {
		t.Fatal(err)
	}
	for index := range 8 {
		if !concurrentConfig.Projects[fmt.Sprintf("/private/concurrent-%d", index)].Trusted {
			t.Fatalf("concurrent trust record %d was lost: %s", index, final)
		}
	}
	if leftovers, _ := filepath.Glob(filepath.Join(accountHome, ".claude.json.*")); len(leftovers) != 0 {
		for _, leftover := range leftovers {
			if filepath.Base(leftover) != ".claude.json.lock" {
				t.Fatalf("temporary files remain: %v", leftovers)
			}
		}
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
	unconnected, err := Build(request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(unconnected.Argv(), " "), "mcp_servers.dark_factory_maintainer=") {
		t.Fatal("unconnected overseer was given Maintainer tools")
	}
	request.runtime = request.runtime.WithCustomerMaintainer(true)
	overseer, err := Build(request)
	if err != nil {
		t.Fatal(err)
	}
	workerArgs, overseerArgs := worker.Argv(), overseer.Argv()
	if !strings.Contains(strings.Join(overseerArgs, " "), "mcp_servers.dark_factory_maintainer={command=") {
		t.Fatal("overseer lost its explicit Maintainer tools")
	}
	if !strings.Contains(strings.Join(overseerArgs, " "), "mcp_servers.dark_factory_maintainer={command=") || !strings.Contains(strings.Join(overseerArgs, " "), "enabled=true,required=false,default_tools_approval_mode=\"approve\"") {
		t.Fatalf("overseer Maintainer must be optional while retaining tools: %q", overseerArgs)
	}
	if _, err := runner.PrepareCommittedExecSpec(overseer.Executable(), overseer.Argv(), overseer.Environment(), t.TempDir()); err != nil {
		t.Fatalf("runner rejected Codex overseer environment: %v", err)
	}
	if strings.Contains(strings.Join(workerArgs, " "), "mcp_servers.dark_factory_maintainer=") {
		t.Fatal("worker was granted publication tool approvals")
	}
	if strings.Contains(workerArgs[len(workerArgs)-1], "overseer status") {
		t.Fatal("worker was given overseer authority instructions")
	}
	prompt := overseerArgs[len(overseerArgs)-1]
	if !strings.Contains(prompt, "factoryd reviews and enqueues every published head") || !strings.Contains(prompt, "never create review tasks for workers") || !strings.Contains(prompt, "never submit a verdict or enqueue yourself") {
		t.Fatal("overseer lacks factoryd's verdict guidance")
	}
	for _, command := range []string{"overseer status", "next_offset", "next_text_offset", "worker interrupt", "worker replace", "Maintainer App", "structuredContent", "capability refusal", "causal wake", "Continue actionable supervision", "without idle polling", "only next event is external", "merge queue", "attempt succeed", "30 minutes after admission", "overseer task update --body", "preserve the original acceptance criteria", "Send-back replaces previous feedback", "factoryd publishes that itself", "fully qualified source repository", "close_on_merge", "Refs #N", "external_source_url"} {
		if !strings.Contains(prompt, command) {
			t.Fatalf("overseer cannot discover %q", command)
		}
	}
	if strings.Contains(prompt, "docs/development/OVERSEER.md") {
		t.Fatal("overseer requires Dark Factory development documents in a customer checkout")
	}
	if overseer.TaskDelivery() != TaskDeliveryPrompt {
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
	for _, rule := range []string{`":root"="deny"`, `":minimal"="read"`, `"/private/tmp"="deny"`, `"/private/var/tmp"="deny"`, tomlBasicString(request.workingDirectory) + `="write"`, tomlBasicString(request.runtime.token) + `="read"`, tomlBasicString(request.runtime.socket) + `="allow"`} {
		if !strings.Contains(policy, rule) {
			t.Fatalf("policy omitted rule %q", rule)
		}
	}
	if strings.Contains(policy, tomlBasicString(request.runtime.accountHome)+`="`) || strings.Contains(policy, request.runtime.gitCeiling) {
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
	for _, prefix := range []string{"GOCACHE=", "GOPATH=", "CARGO_HOME=", "COREPACK_HOME=", "npm_config_cache=", "XDG_CACHE_HOME="} {
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
	sharedModuleCache := goModuleCachePath(runtime.accountHome)
	if !slices.Contains(launch.Environment(), "DF_CI_GO_MODULE_CACHE="+sharedModuleCache) || slices.ContainsFunc(launch.Environment(), func(entry string) bool { return strings.HasPrefix(entry, "GOMODCACHE=") }) {
		t.Fatalf("worker Go downloads must land in the private GOPATH, reading the shared cache only as a proxy: %q", launch.Environment())
	}
	for _, want := range []string{"GOPROXY=file://" + sharedModuleCache + "/cache/download,https://proxy.golang.org", "GOSUMDB=sum.golang.org", "COREPACK_ENABLE_NETWORK=0", "pnpm_config_store_dir=" + pnpmStorePath(runtime.accountHome)} {
		if !slices.Contains(launch.Environment(), want) {
			t.Fatalf("native launch lacks offline dependency setting %s: %q", want, launch.Environment())
		}
	}
	for _, sharedCache := range []string{goModuleCachePath(runtime.accountHome), pnpmStorePath(runtime.accountHome)} {
		if !strings.Contains(policy, tomlBasicString(sharedCache)+`="read"`) || strings.Contains(policy, tomlBasicString(sharedCache)+`="write"`) {
			t.Fatalf("shared dependency cache grant is not read-only: %s", policy)
		}
	}
	if strings.Contains(policy, tomlBasicString(runtime.accountHome)+`="read"`) || strings.Contains(policy, tomlBasicString(runtime.accountHome)+`="write"`) {
		t.Fatalf("account home was granted broadly: %s", policy)
	}
	if !slices.Contains(launch.Environment(), "RUSTUP_HOME="+runtime.accountHome+"/.rustup") {
		t.Fatal("rustup metadata must remain in the selected account installation")
	}
	for _, root := range []string{runtime.accountHome, runtime.gitCeiling, runtime.home, runtime.temp} {
		invalid := runtime
		invalid.toolchainReadRoots = root
		if _, err := NewRequest(kernel.ProviderCodex, installation, "", "", invalid, request.workingDirectory, kernel.RoleWorker, testAgentID, testIncarnationID); err == nil {
			t.Fatalf("accepted private read root %q", root)
		}
	}
}

func TestNativeLocalCICacheStaysInRuntimeHome(t *testing.T) {
	for _, kind := range []kernel.Provider{kernel.ProviderCodex, kernel.ProviderClaudeCode} {
		t.Run(kind.String(), func(t *testing.T) {
			installation, runtime, _ := nativeFixture(t, kind)
			request := requestFor(t, kind, installation, runtime, "", "")
			request.workingDirectory = t.TempDir()
			launch, err := Build(request)
			if err != nil {
				t.Fatal(err)
			}
			wantCacheRoot := "DF_CI_CACHE_ROOT=" + filepath.Join(runtime.home, ".cache", "dark-factory", "local-ci", "trusted")
			if !slices.Contains(launch.Environment(), wantCacheRoot) {
				t.Fatalf("local-CI cache root = %q, want %q", launch.Environment(), wantCacheRoot)
			}
			if !slices.Contains(launch.Environment(), "DF_CI_GO_MODULE_CACHE="+goModuleCachePath(runtime.accountHome)) {
				t.Fatalf("lost the trusted shared Go module cache: %q", launch.Environment())
			}
			if _, err := runner.PrepareCommittedExecSpec(launch.Executable(), launch.Argv(), launch.Environment(), request.workingDirectory); err != nil {
				t.Fatalf("generated environment rejected by runner: %v", err)
			}
		})
	}
}

func TestNativeOrchestratorDoesNotProjectSharedGoModuleCache(t *testing.T) {
	for _, kind := range []kernel.Provider{kernel.ProviderCodex, kernel.ProviderClaudeCode} {
		t.Run(kind.String(), func(t *testing.T) {
			installation, runtime, _ := nativeFixture(t, kind)
			runtime = runtime.WithCustomerMaintainer(true)
			request := roleRequestFor(t, kind, installation, runtime, "", "", kernel.RoleOrchestrator)
			launch, err := Build(request)
			if err != nil {
				t.Fatal(err)
			}
			for _, prefix := range []string{"GOMODCACHE=", "DF_CI_GO_MODULE_CACHE="} {
				if slices.ContainsFunc(launch.Environment(), func(entry string) bool { return strings.HasPrefix(entry, prefix) }) {
					t.Fatalf("orchestrator received worker-only %s projection: %q", prefix, launch.Environment())
				}
			}
			for _, want := range []string{"COREPACK_ENABLE_NETWORK=1", "COREPACK_ENV_FILE=0", "COREPACK_NPM_REGISTRY=https://registry.npmjs.org", "npm_config_registry=https://registry.npmjs.org", "pnpm_config_store_dir=" + filepath.Join(runtime.home, ".cache", "pnpm-store")} {
				if !slices.Contains(launch.Environment(), want) {
					t.Fatalf("orchestrator lost public dependency setting %s: %q", want, launch.Environment())
				}
			}
			if kind == kernel.ProviderCodex {
				policy, err := codexPermissions(request)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(policy, tomlBasicString(goModuleCachePath(runtime.accountHome))) {
					t.Fatalf("orchestrator received worker-only Go module cache grant: %s", policy)
				}
			}
		})
	}
}

// Opt-in local proof with an installed Codex CLI; no model or account access.
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
	shared, err := os.MkdirTemp(runner.ShortTempDir(), "toolchain-proof-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(shared)
	if err := os.WriteFile(filepath.Join(shared, "sentinel"), []byte("fixture sentinel"), 0600); err != nil {
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
 if /bin/cat "$3/sentinel" >/dev/null 2>&1; then exit 34; fi
 if printf bad > "$3/write" 2>/dev/null; then exit 35; fi
 if printf bad > "/private/var/tmp/toolchain-proof-$$" 2>/dev/null; then exit 36; fi
 `
	if out, err := run("/bin/sh", "-c", script, "proof", software, secret, shared); err != nil {
		t.Fatalf("sandbox isolation: %v\n%s", err, out)
	}
	gitDirectory := filepath.Join(root, "repository", ".git")
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
	body, err := os.ReadFile(filepath.Join(filepath.Dir(testSource), "..", "..", "scripts", "with-local-ci-lease.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leaseScripts, "with-local-ci-lease.sh"), body, 0700); err != nil {
		t.Fatal(err)
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
		if slices.Contains(filepath.SplitList(roots), "/Library/Developer/CommandLineTools") {
			out, err := run("/bin/sh", "-c", `set -eu
printf '#include <stdio.h>\nint main(void) { puts("sdk-ok"); return 0; }\n' > sdk-proof.c
/Library/Developer/CommandLineTools/usr/bin/clang -isysroot /Library/Developer/CommandLineTools/SDKs/MacOSX.sdk sdk-proof.c -o sdk-proof
./sdk-proof`)
			if err != nil || !strings.Contains(string(out), "sdk-ok") {
				t.Fatalf("installed SDK compile: %v\n%s", err, out)
			}
			t.Logf("installed SDK compile: %s", out)
		}
	}
	// A worker's profile writes the repository's Git directory, where its
	// Change branch lives; the repository's working tree stays unreachable.
	request.runtime, err = request.runtime.WithGitCommonDirectory(gitDirectory, true)
	if err != nil {
		t.Fatal(err)
	}
	// The Change is a linked worktree; Codex protects the registration its
	// gitfile names, where a commit writes index.lock and COMMIT_EDITMSG.
	registration := filepath.Join(gitDirectory, "worktrees", filepath.Base(request.workingDirectory))
	if err := os.MkdirAll(registration, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(request.workingDirectory, ".git"), []byte("gitdir: "+registration+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	worktreeScript := `set -eu
 printf lock > "$3/index.lock"
 printf ref > "$1/refs/factory-branch"
 printf object > "$1/objects/new"
 if (printf forbidden > "$2/tracked.go") 2>/dev/null; then exit 34; fi
 `
	if out, err := run("/bin/sh", "-c", worktreeScript, "proof", gitDirectory, root, registration); err != nil {
		t.Fatalf("worker Git directory grant: %v\n%s", err, out)
	}
	request.runtime, err = request.runtime.WithGitCommonDirectory(gitDirectory, false)
	if err != nil {
		t.Fatal(err)
	}
	readerScript := `set -eu
 /bin/cat "$1/refs/factory-branch" >/dev/null
 if (printf forbidden > "$1/refs/another") 2>/dev/null; then exit 35; fi
 `
	if out, err := run("/bin/sh", "-c", readerScript, "proof", gitDirectory); err != nil {
		t.Fatalf("orchestrator Git directory grant: %v\n%s", err, out)
	}
	request.runtime.gitCommonDir, request.runtime.gitCommonDirWritable = "", false
	if fixture := os.Getenv("DARK_FACTORY_TEST_FIXTURE_BINARY"); fixture != "" {
		request.runtime.toolchainReadRoots = strings.TrimPrefix(request.runtime.toolchainReadRoots+string(filepath.ListSeparator)+filepath.Dir(fixture), string(filepath.ListSeparator))
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
	if !strings.Contains(policy, `"/usr/bin/ruby"="read"`) || strings.Contains(policy, `"/System/Library/Perl"="read"`) {
		t.Fatalf("lease process-group runtime permission is not exact: %s", policy)
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

// The one shared thing a Change worktree needs is the repository's Git
// directory: a worker writes it, an orchestrator reads it and each other
// project repository's, and nothing else of the repository or the Changes
// parent is granted.
func TestCodexLaunchGrantsTheRepositoryGitDirectoryByRole(t *testing.T) {
	installation, runtime, _ := nativeFixture(t, kernel.ProviderCodex)
	site := "/private/site/.git"
	for _, writable := range []bool{true, false} {
		request := requestFor(t, kernel.ProviderCodex, installation, runtime, "", "")
		// A fresh Change's private Git, and its registration Git names after
		// the worktree: Codex 0.160 makes it read-only unless granted itself.
		gitDirectory := change.GitDirectoryForChange("/private/project", request.workingDirectory)
		registration := filepath.Join(gitDirectory, "worktrees", filepath.Base(request.workingDirectory))
		var project []string
		if !writable {
			project = []string{site}
		}
		granted, err := runtime.WithGitCommonDirectory(gitDirectory, writable, project...)
		if err != nil {
			t.Fatal(err)
		}
		request.runtime = granted
		launch, err := Build(request)
		if err != nil {
			t.Fatal(err)
		}
		policy := ""
		for _, argument := range launch.Argv() {
			if strings.HasPrefix(argument, "permissions."+codexPermissionName(granted)+"=") {
				policy = argument
				break
			}
		}
		access := "read"
		if writable {
			access = "write"
		}
		if !strings.Contains(policy, tomlBasicString(gitDirectory)+`="`+access+`"`) {
			t.Fatalf("launch omitted the Git directory grant: %q", policy)
		}
		if strings.Contains(policy, tomlBasicString(registration)+`="write"`) != writable {
			t.Fatalf("worktree registration write grant = %v, want %v: %q", !writable, writable, policy)
		}
		if strings.Contains(policy, tomlBasicString(site)+`="read"`) == writable || strings.Contains(policy, tomlBasicString(site)+`="write"`) {
			t.Fatalf("other project repository grant wrong for writable=%v: %q", writable, policy)
		}
		// Claude renders the same grants.
		settings, err := claudeSettings(request, nil)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(settings, `"Read(/`+site+`/**)"`) == writable || strings.Contains(settings, `"Edit(/`+site+`/**)"`) {
			t.Fatalf("Claude other project repository grant wrong for writable=%v: %s", writable, settings)
		}
		if strings.Contains(policy, tomlBasicString("/private/project")+`="`) || strings.Contains(policy, tomlBasicString("/private/factory/changes")+`="`) {
			t.Fatalf("Git directory grant widened: %q", policy)
		}
		for _, environment := range []string{"GIT_AUTHOR_NAME=" + GitIdentityName, "GIT_COMMITTER_EMAIL=" + GitIdentityEmail, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ASKPASS=/usr/bin/false"} {
			if !slices.Contains(launch.Environment(), environment) {
				t.Fatalf("launch environment lacks %q", environment)
			}
		}
	}
	for _, bad := range []string{"relative", "/private/project", "/private/project/.git/", runtime.home} {
		if _, err := runtime.WithGitCommonDirectory(bad, true); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Git directory %q = %v, want ErrInvalid", bad, err)
		}
		if _, err := runtime.WithGitCommonDirectory(site, false, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("project Git directory %q = %v, want ErrInvalid", bad, err)
		}
	}
}

// Claude and Codex start from one bootstrap prompt, the last argv element,
// naming only their own attempt server; neither has a task typed into its PTY.
func TestNativeProvidersShareTheBootstrapPrompt(t *testing.T) {
	if !strings.HasPrefix(bootstrapPrompt, `Your exact task follows "Task:" below`) || strings.Contains(bootstrapPrompt, `"attempt","task"`) || !strings.Contains(bootstrapPrompt, "including corrections after send-back") || strings.Contains(bootstrapPrompt, "attempt source") || !strings.Contains(bootstrapPrompt, "Never substitute another task or private Change path") || !strings.Contains(bootstrapPrompt, "screenshots are illustrative only and never blocking evidence") || !strings.Contains(bootstrapPrompt, `checks-failed send-back is the exception: CI's failure on the exact head is its premise`) {
		t.Fatal("bootstrap prompt loses its task fetch, own-checkout or checks-failed premise instruction")
	}
	for _, role := range []kernel.AgentRole{kernel.RoleWorker, kernel.RoleOrchestrator} {
		prompts := map[kernel.Provider]string{}
		for _, kind := range []kernel.Provider{kernel.ProviderClaudeCode, kernel.ProviderCodex} {
			installation, runtime, _ := nativeFixture(t, kind)
			request := roleRequestFor(t, kind, installation, runtime, "", "", role)
			launch, err := Build(request)
			if err != nil || launch.TaskDelivery() != TaskDeliveryPrompt {
				t.Fatalf("%s %s delivery=%d err=%v", kind, role, launch.TaskDelivery(), err)
			}
			argv := launch.Argv()
			prompt := argv[len(argv)-1]
			if kind == kernel.ProviderCodex {
				prompt = strings.ReplaceAll(prompt, "mcp__"+codexAttemptServerName(runtime)+"__factory", "mcp__factory_attempt__factory")
			} else if argv[len(argv)-2] != "--" {
				t.Fatalf("Claude prompt is not separated from variadic options: %q", argv)
			}
			prompts[kind] = prompt
		}
		if prompts[kernel.ProviderClaudeCode] != prompts[kernel.ProviderCodex] || !strings.HasPrefix(prompts[kernel.ProviderCodex], bootstrapPrompt) {
			t.Fatalf("%s prompts differ:\nclaude=%q\ncodex=%q", role, prompts[kernel.ProviderClaudeCode], prompts[kernel.ProviderCodex])
		}
	}
}

func TestBootstrapPromptSettlesMergedLiveObservation(t *testing.T) {
	installation, runtime, _ := nativeFixture(t, kernel.ProviderCodex)
	request := roleRequestFor(t, kernel.ProviderCodex, installation, runtime, "", "", kernel.RoleOrchestrator)
	prompt := bootstrapPromptFor(request, "factory_attempt", false)
	if strings.Contains(prompt, "send-back that task at its line's change head") {
		t.Fatal("bootstrap prompt sends a merged task back for live observation")
	}
	for _, want := range []string{
		"if the signal is pending or does not hold but no concrete code defect is identified",
		"record the quoted gap and exact external gate",
		"finish successfully without sending the merged task back",
		"create new actionable intake work for the correction",
		"never resurrecting the merged task",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("bootstrap prompt lacks %q", want)
		}
	}
}

// The longest launch, a connected overseer with the largest native task,
// still passes the runner's argv bound, so PrepareTask's acceptance is the
// only gate a native task meets. A resumed Codex overseer session gets only
// its causal wake; a fresh one gets the whole task.
func TestNativePromptCarriesMaximumTaskAndTrimsResumedOverseer(t *testing.T) {
	for _, kind := range []kernel.Provider{kernel.ProviderClaudeCode, kernel.ProviderCodex} {
		installation, runtime, _ := nativeFixture(t, kind)
		request := roleRequestFor(t, kind, installation, runtime.WithCustomerMaintainer(true), "", "", kernel.RoleOrchestrator)
		launch, err := Build(request.WithTask(bytes.Repeat([]byte{0x7f}, runner.MaxNativeTaskBytes)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runner.PrepareCommittedExecSpec(launch.Executable(), launch.Argv(), launch.Environment(), t.TempDir()); err != nil {
			t.Fatalf("%s maximum prompt refused: %v", kind, err)
		}
	}
	installation, runtime, _ := nativeFixture(t, kernel.ProviderCodex)
	overseer := roleRequestFor(t, kernel.ProviderCodex, installation, runtime, "", "", kernel.RoleOrchestrator).WithTask([]byte("standing overseer task\n\nFactory causal wake: mode=delta"))
	if got := bootstrapPromptFor(overseer, "s", true); !strings.HasSuffix(got, "\n\nTask:\nFactory causal wake: mode=delta") || strings.Contains(got, "standing overseer task") {
		t.Fatalf("resumed overseer prompt = %q", got)
	}
	if got := bootstrapPromptFor(overseer, "s", false); !strings.HasSuffix(got, "\n\nTask:\nstanding overseer task\n\nFactory causal wake: mode=delta") {
		t.Fatalf("fresh overseer prompt = %q", got)
	}
	worker := roleRequestFor(t, kernel.ProviderCodex, installation, runtime, "", "", kernel.RoleWorker).WithTask([]byte("work\n\nFactory causal wake: x"))
	if got := bootstrapPromptFor(worker, "s", true); !strings.HasSuffix(got, "\n\nTask:\nwork\n\nFactory causal wake: x") {
		t.Fatalf("resumed worker prompt lost its task: %q", got)
	}
}

func TestCustomerMaintainerIsOrchestratorOnly(t *testing.T) {
	for _, kind := range []kernel.Provider{kernel.ProviderCodex, kernel.ProviderClaudeCode} {
		t.Run(kind.String(), func(t *testing.T) {
			installation, runtime, _ := nativeFixture(t, kind)
			runtime = runtime.WithCustomerMaintainer(true)
			for _, role := range []kernel.AgentRole{kernel.RoleOrchestrator, kernel.RoleWorker} {
				request := roleRequestFor(t, kind, installation, runtime, "", "", role)
				launch, err := Build(request)
				if err != nil {
					t.Fatal(err)
				}
				argv := strings.Join(launch.Argv(), " ")
				if strings.Contains(argv, "maintainer-mcp") != (role == kernel.RoleOrchestrator) {
					t.Fatalf("wrong bridge role: %s", argv)
				}
			}
		})
	}
}

func TestGitAuthorEnvironmentUsesVerifiedOperator(t *testing.T) {
	runtime := runtimeFixture(t, "/usr/bin:/bin", filepath.Join(t.TempDir(), "account"))
	author := gitauthor.Identity{ID: 123, Login: "operator"}
	var err error
	runtime, err = runtime.WithGitAuthor(author)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []kernel.Provider{kernel.ProviderShell, kernel.ProviderCodex, kernel.ProviderClaudeCode} {
		env := runtime.environment(kind)
		for _, want := range []string{"GIT_AUTHOR_NAME=operator", "GIT_AUTHOR_EMAIL=123+operator@users.noreply.github.com", "GIT_COMMITTER_NAME=operator", "GIT_COMMITTER_EMAIL=123+operator@users.noreply.github.com"} {
			if !slices.Contains(env, want) {
				t.Fatalf("%s missing %s", kind, want)
			}
		}
	}
	if _, err := runtime.WithGitAuthor(gitauthor.Identity{ID: 123, Login: "spoof\nAuthor"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed author: %v", err)
	}
}

// wantClaudeServers is the attempt tool every Claude launch carries, plus any
// role-specific servers.
func wantClaudeServers(t *testing.T, request Request, servers map[string]any) string {
	t.Helper()
	if servers == nil {
		servers = map[string]any{}
	}
	servers["factory_attempt"] = map[string]any{"command": request.runtime.factoryctl, "args": []string{"attempt", "mcp"}}
	config, err := json.Marshal(map[string]any{"mcpServers": servers})
	if err != nil {
		t.Fatal(err)
	}
	return string(config)
}

func wantClaudeSettings(t *testing.T, request Request, servers ...string) string {
	t.Helper()
	settings, err := claudeSettings(request, servers)
	if err != nil {
		t.Fatal(err)
	}
	return settings
}

// A Claude run gets the grants Codex gets: the Change and runtime paths are
// writable, the operator's home is unreadable, nothing prompts and nothing
// leaves the sandbox.
func TestClaudeSettingsConfineTheRunToItsGrants(t *testing.T) {
	installation, runtime, _ := nativeFixture(t, kernel.ProviderClaudeCode)
	request := requestFor(t, kernel.ProviderClaudeCode, installation, runtime, "", "")
	var settings struct {
		Hooks struct {
			Stop []struct {
				Hooks []struct{ Type, Command string }
			}
		}
		Permissions struct{ Allow []string }
		Sandbox     struct {
			Enabled, FailIfUnavailable, AllowUnsandboxedCommands bool
			Filesystem                                           struct{ AllowWrite, DenyRead, AllowRead []string }
			Network                                              struct{ AllowUnixSockets []string }
		}
	}
	if err := json.Unmarshal([]byte(wantClaudeSettings(t, request, "factory_browser")), &settings); err != nil {
		t.Fatal(err)
	}
	// A turn that ends without an outcome reaches factoryd at once.
	if stop := settings.Hooks.Stop; len(stop) != 1 || len(stop[0].Hooks) != 1 || stop[0].Hooks[0].Type != "command" || stop[0].Hooks[0].Command != "'"+runtime.factoryctl+"' attempt turn-complete" {
		t.Fatalf("Stop hook = %+v", stop)
	}
	sandbox, change := settings.Sandbox, "/"+request.workingDirectory
	if !sandbox.Enabled || !sandbox.FailIfUnavailable || sandbox.AllowUnsandboxedCommands {
		t.Fatalf("sandbox can be skipped: %+v", sandbox)
	}
	for _, region := range []string{"//Users", "//Volumes", "//private/tmp", "//private/var/folders"} {
		if !slices.Contains(sandbox.Filesystem.DenyRead, region) {
			t.Fatalf("%s stays readable: %q", region, sandbox.Filesystem.DenyRead)
		}
	}
	if !slices.Contains(sandbox.Filesystem.AllowRead, change) || slices.Contains(sandbox.Filesystem.AllowRead, "//private/tmp") {
		t.Fatalf("read boundary = %+v", sandbox.Filesystem)
	}
	if !slices.Contains(sandbox.Filesystem.AllowWrite, change) || slices.Contains(sandbox.Filesystem.AllowWrite, "/"+runtime.factoryctl) {
		t.Fatalf("write grants = %q", sandbox.Filesystem.AllowWrite)
	}
	// Bash and the file tools get no route to the attempt API: no socket, and
	// neither it nor the token is readable. The factory_attempt tool has both.
	if len(sandbox.Network.AllowUnixSockets) != 0 {
		t.Fatalf("sockets = %q", sandbox.Network.AllowUnixSockets)
	}
	for _, private := range []string{runtime.token, runtime.socket} {
		if slices.Contains(sandbox.Filesystem.AllowRead, "/"+private) || slices.Contains(settings.Permissions.Allow, "Read(/"+private+")") {
			t.Fatalf("%s is readable inside the sandbox", private)
		}
	}
	// A worker may delegate reading to its one sweep sub-agent: a cheaper
	// model with only the read tools, so no Bash or edit tool can write.
	var agents map[string]struct {
		Tools []string
		Model string
	}
	if err := json.Unmarshal([]byte(claudeSweepAgent), &agents); err != nil || len(agents) != 1 || agents["sweep"].Model != "haiku" ||
		slices.ContainsFunc(agents["sweep"].Tools, func(tool string) bool { return tool != "Read" && tool != "Grep" && tool != "Glob" }) {
		t.Fatalf("sweep agent = %+v, %v", agents, err)
	}
	for _, rule := range []string{"Edit(" + change + "/**)", "mcp__factory_browser", "Agent(sweep)"} {
		if !slices.Contains(settings.Permissions.Allow, rule) {
			t.Fatalf("allow rules lack %s: %q", rule, settings.Permissions.Allow)
		}
	}
	launch, err := Build(request)
	if err != nil || slices.Contains(launch.Argv(), "--dangerously-skip-permissions") {
		t.Fatalf("launch still bypasses permissions: %q, %v", launch.Argv(), err)
	}
	// A checkout's own .claude/settings.json must not be able to widen this.
	if sources := slices.Index(launch.Argv(), "--setting-sources"); sources < 0 || launch.Argv()[sources+1] != "" {
		t.Fatalf("launch reads user or project settings: %q", launch.Argv())
	}
}

func TestWorkerEnvironmentExportsTracesToTheConfiguredReceiver(t *testing.T) {
	want := []string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://127.0.0.1:43999/v1/traces", "OTEL_RESOURCE_ATTRIBUTES=deployment.environment.name=local"}
	for _, kind := range []kernel.Provider{kernel.ProviderClaudeCode, kernel.ProviderCodex} {
		installation, runtime, _ := nativeFixture(t, kind)
		launch, err := Build(requestFor(t, kind, installation, runtime.WithTraceReceiver(43999, ""), "", ""))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range want {
			if !slices.Contains(launch.Environment(), entry) {
				t.Fatalf("%s worker environment lacks %q: %q", kind, entry, launch.Environment())
			}
		}
		if _, err := runner.PrepareCommittedExecSpec(launch.Executable(), launch.Argv(), launch.Environment(), t.TempDir()); err != nil {
			t.Fatalf("runner refused the %s worker environment: %v", kind, err)
		}
		for _, other := range [][]string{
			runtime.environmentForRole(kind, kernel.RoleWorker),
			runtime.WithTraceReceiver(43999, "").environmentForRole(kind, kernel.RoleOrchestrator),
		} {
			for _, entry := range other {
				if strings.HasPrefix(entry, "OTEL_") {
					t.Fatalf("%s exported traces without a worker receiver: %q", kind, entry)
				}
			}
		}
		// Without a run id the agent CLI's own telemetry stays off.
		if slices.ContainsFunc(launch.Environment(), func(entry string) bool { return strings.HasPrefix(entry, "CLAUDE_CODE_ENABLE_TELEMETRY") }) ||
			slices.ContainsFunc(launch.Argv(), func(arg string) bool { return strings.HasPrefix(arg, "otel.") }) {
			t.Fatalf("%s enabled agent telemetry without a run id: %q %q", kind, launch.Environment(), launch.Argv())
		}
	}
}

// A worker's agent CLI exports its own metrics and logs to the receiver, with
// the run id factoryd attributes them by; prompt content stays off.
func TestWorkerAgentTelemetryNamesItsRun(t *testing.T) {
	const run = "0123456789abcdef0123456789abcdef"
	resource := "OTEL_RESOURCE_ATTRIBUTES=deployment.environment.name=local,dark_factory.run.id=" + run
	for _, test := range []struct {
		kind kernel.Provider
		env  []string
		argv []string
	}{
		{kernel.ProviderClaudeCode, []string{resource, "CLAUDE_CODE_ENABLE_TELEMETRY=1", "OTEL_METRICS_EXPORTER=otlp", "OTEL_LOGS_EXPORTER=otlp", "OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
			"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT=http://127.0.0.1:43999/v1/metrics", "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://127.0.0.1:43999/v1/logs"}, nil},
		{kernel.ProviderCodex, []string{resource}, []string{`otel.exporter={otlp-http={endpoint="http://127.0.0.1:43999/v1/logs",protocol="binary"}}`,
			`otel.metrics_exporter={otlp-http={endpoint="http://127.0.0.1:43999/v1/metrics",protocol="binary"}}`, "otel.log_user_prompt=false"}},
	} {
		installation, runtime, _ := nativeFixture(t, test.kind)
		launch, err := Build(requestFor(t, test.kind, installation, runtime.WithTraceReceiver(43999, run), "", ""))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range test.env {
			if !slices.Contains(launch.Environment(), entry) {
				t.Fatalf("%s worker environment lacks %q: %q", test.kind, entry, launch.Environment())
			}
		}
		for _, entry := range test.argv {
			if index := slices.Index(launch.Argv(), entry); index < 1 || launch.Argv()[index-1] != "-c" {
				t.Fatalf("%s worker argv lacks -c %q: %q", test.kind, entry, launch.Argv())
			}
		}
		if test.kind == kernel.ProviderCodex && slices.ContainsFunc(launch.Environment(), func(entry string) bool { return strings.HasPrefix(entry, "CLAUDE_CODE_") }) {
			t.Fatalf("codex worker carries Claude Code switches: %q", launch.Environment())
		}
		if _, err := runner.PrepareCommittedExecSpec(launch.Executable(), launch.Argv(), launch.Environment(), t.TempDir()); err != nil {
			t.Fatalf("runner refused the %s worker telemetry: %v", test.kind, err)
		}
		for _, entry := range runtime.WithTraceReceiver(43999, run).environmentForRole(test.kind, kernel.RoleOrchestrator) {
			if strings.HasPrefix(entry, "OTEL_") || strings.HasPrefix(entry, "CLAUDE_CODE_ENABLE") {
				t.Fatalf("%s orchestrator exported telemetry: %q", test.kind, entry)
			}
		}
	}
}
