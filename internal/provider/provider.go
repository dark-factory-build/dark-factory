package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

const (
	shellPath       = "/bin/sh"
	claudeTool      = "claude"
	codexTool       = "codex"
	maxPathBytes    = 4096
	maxNativePrompt = 8 << 10
	claudeConfigDir = ".claude"
	codexConfigDir  = ".codex"
	nativeTaskLead  = "Complete this Dark Factory task. Before exiting, report the durable outcome with $DARK_FACTORY_FACTORYCTL attempt succeed, block, or fail. Task: "
)

var (
	ErrInvalid     = errors.New("provider: invalid launch contract")
	ErrUnavailable = errors.New("provider: unavailable")
)

// Installation binds one provider kind to one exact executable commitment.
type Installation struct {
	provider   kernel.Provider
	executable runner.ExecutableCommitment
}

// ResolveInstallation selects one executable from the daemon's fixed tool
// path. Native tool locators may be symlinks, but the returned commitment is
// always to the resolved direct Mach-O target; no symlink is trusted again at
// exec. An invalid existing candidate fails closed instead of falling through
// to a different executable with the same name.
func ResolveInstallation(kind kernel.Provider, toolPath string) (Installation, error) {
	if !validToolPath(toolPath) {
		return Installation{}, ErrInvalid
	}
	if kind == kernel.ProviderShell {
		executable, err := runner.CommitExecutableLocator(shellPath)
		if err != nil {
			return Installation{}, unavailable(kind)
		}
		return Installation{provider: kind, executable: executable}, nil
	}
	var tool string
	switch kind {
	case kernel.ProviderClaudeCode:
		tool = claudeTool
	case kernel.ProviderCodex:
		tool = codexTool
	default:
		return Installation{}, ErrInvalid
	}
	for _, directory := range filepath.SplitList(toolPath) {
		candidate := filepath.Join(directory, tool)
		if _, err := os.Lstat(candidate); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return Installation{}, unavailable(kind)
		}
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil || !validAbsolute(resolved, maxPathBytes) {
			return Installation{}, unavailable(kind)
		}
		executable, err := runner.CommitExecutableLocator(resolved)
		if err != nil {
			return Installation{}, unavailable(kind)
		}
		return Installation{provider: kind, executable: executable}, nil
	}
	return Installation{}, unavailable(kind)
}

func (Installation) String() string   { return "provider installation (private)" }
func (Installation) GoString() string { return "provider.Installation{private}" }

// RuntimePaths is the complete set of external strings permitted to enter the
// provider environment. Construction is lexical only; the daemon-owned Change
// worker must retain and revalidate the exact filesystem/executable
// capabilities that make these paths true immediately around Build and exec.
// This value is never authority by itself.
type RuntimePaths struct {
	home, temp, socket, token, factoryctl, gitCeiling, toolPath, accountHome string
}

func NewRuntimePaths(home, temp, socket, token, factoryctl, gitCeiling, toolPath, accountHome string) (RuntimePaths, error) {
	runtime := RuntimePaths{
		home: home, temp: temp, socket: socket, token: token,
		factoryctl: factoryctl, gitCeiling: gitCeiling, toolPath: toolPath, accountHome: accountHome,
	}
	if !runtime.valid() {
		return RuntimePaths{}, ErrInvalid
	}
	return runtime, nil
}

func (RuntimePaths) String() string   { return "provider runtime paths (private)" }
func (RuntimePaths) GoString() string { return "provider.RuntimePaths{private}" }

type Request struct {
	provider        kernel.Provider
	installation    Installation
	model           string
	reasoningEffort string
	runtime         RuntimePaths
}

func NewRequest(kind kernel.Provider, installation Installation, model, reasoningEffort string, runtime RuntimePaths) (Request, error) {
	if kernel.ValidateProviderLaunchControls(kind, model, reasoningEffort) != nil || installation.provider != kind || installation.executable.Path() == "" || !runtime.valid() {
		return Request{}, ErrInvalid
	}
	return Request{
		provider: kind, installation: installation,
		model: model, reasoningEffort: reasoningEffort, runtime: runtime,
	}, nil
}

func (Request) String() string   { return "provider build request (private)" }
func (Request) GoString() string { return "provider.Request{private}" }

// Launch is the entire provider-owned result. It deliberately has no cwd,
// task input, descriptors, callbacks, process controls, or output decoder.
type Launch struct {
	executable   runner.ExecutableCommitment
	argv         []string
	environment  []string
	taskDelivery TaskDelivery
}

func (launch Launch) Executable() runner.ExecutableCommitment { return launch.executable }
func (launch Launch) Argv() []string                          { return append([]string(nil), launch.argv...) }
func (launch Launch) Environment() []string                   { return append([]string(nil), launch.environment...) }
func (launch Launch) TaskDelivery() TaskDelivery              { return launch.taskDelivery }

func (Launch) String() string   { return "provider launch (private)" }
func (Launch) GoString() string { return "provider.Launch{private}" }

// TaskDelivery is the one task-input channel selected with a provider launch.
// Shell reads its program from the inherited sealed descriptor. Native tools
// receive their prompt once through the PTY after provider exec is proven and
// before that terminal is exposed to an operator.
type TaskDelivery uint8

const (
	TaskDeliveryFD11 TaskDelivery = iota + 1
	TaskDeliveryStartupTerminal
)

// Build is the one closed provider-selection switch.
func Build(request Request) (Launch, error) {
	if err := request.installation.executable.Verify(); err != nil {
		return Launch{}, errors.Join(ErrUnavailable, err)
	}
	path := request.installation.executable.Path()
	switch request.provider {
	case kernel.ProviderShell:
		if request.model != "" || request.reasoningEffort != "" || path != shellPath {
			return Launch{}, ErrInvalid
		}
		return Launch{
			executable:   request.installation.executable,
			argv:         []string{shellPath, runner.ProviderTaskPath},
			environment:  request.runtime.environment(request.provider),
			taskDelivery: TaskDeliveryFD11,
		}, nil
	case kernel.ProviderClaudeCode, kernel.ProviderCodex:
		var argv []string
		if request.provider == kernel.ProviderClaudeCode {
			argv = []string{path, "--dangerously-skip-permissions"}
		} else {
			argv = []string{path, "--dangerously-bypass-approvals-and-sandbox", "--no-alt-screen"}
		}
		if request.model != "" {
			argv = append(argv, "--model", request.model)
		}
		if request.reasoningEffort != "" {
			if request.provider == kernel.ProviderClaudeCode {
				argv = append(argv, "--effort", request.reasoningEffort)
			} else {
				argv = append(argv, "-c", fmt.Sprintf("model_reasoning_effort=%q", request.reasoningEffort))
			}
		}
		return Launch{
			executable: request.installation.executable, argv: argv,
			environment: request.runtime.environment(request.provider), taskDelivery: TaskDeliveryStartupTerminal,
		}, nil
	default:
		return Launch{}, ErrInvalid
	}
}

// PrepareTask is also available before executable selection so the daemon can
// freeze native startup input into the runner configuration. Build returns the
// same closed delivery value, which the Change worker must compare before exec.
func PrepareTask(kind kernel.Provider, task []byte) (TaskDelivery, []byte, error) {
	if len(task) == 0 || len(task) > runner.MaxProviderTaskBytes || !utf8.Valid(task) || bytes.IndexByte(task, 0) >= 0 {
		return 0, nil, ErrInvalid
	}
	switch kind {
	case kernel.ProviderShell:
		return TaskDeliveryFD11, bytes.Clone(task), nil
	case kernel.ProviderClaudeCode, kernel.ProviderCodex:
		encoded, err := nativeTaskInput(task)
		if err != nil {
			return 0, nil, ErrInvalid
		}
		return TaskDeliveryStartupTerminal, encoded, nil
	default:
		return 0, nil, ErrInvalid
	}
}

func unavailable(kind kernel.Provider) error {
	return fmt.Errorf("%w: %s", ErrUnavailable, kind.String())
}

func nativeTaskInput(task []byte) ([]byte, error) {
	quoted, err := json.Marshal(string(task))
	if err != nil {
		return nil, ErrInvalid
	}
	payload := make([]byte, 0, len(nativeTaskLead)+len(quoted)+1)
	payload = append(payload, nativeTaskLead...)
	payload = appendTerminalSafeJSON(payload, quoted)
	payload = append(payload, '\r')
	if len(payload) > maxNativePrompt {
		return nil, ErrInvalid
	}
	return payload, nil
}

func appendTerminalSafeJSON(dst, quoted []byte) []byte {
	const hex = "0123456789abcdef"
	for len(quoted) > 0 {
		value, width := utf8.DecodeRune(quoted)
		if value >= 0x7f && value <= 0x9f {
			dst = append(dst, '\\', 'u', '0', '0', hex[value>>4], hex[value&0xf])
		} else {
			dst = append(dst, quoted[:width]...)
		}
		quoted = quoted[width:]
	}
	return dst
}

func (runtime RuntimePaths) valid() bool {
	paths := []string{runtime.home, runtime.temp, runtime.socket, runtime.token, runtime.factoryctl}
	for _, path := range paths {
		if !validAbsolute(path, maxPathBytes) {
			return false
		}
	}
	return len(runtime.socket) <= install.MaxSocketPathBytes && runtime.home != runtime.temp &&
		validGitCeiling(runtime.gitCeiling) && validToolPath(runtime.toolPath) &&
		validAbsolute(runtime.accountHome, maxPathBytes-len("/"+claudeConfigDir)) &&
		runtime.accountHome != runtime.home && runtime.accountHome != runtime.temp
}

func (runtime RuntimePaths) environment(kind kernel.Provider) []string {
	environment := []string{
		"DARK_FACTORY_SOCKET=" + runtime.socket,
		"DARK_FACTORY_ATTEMPT_TOKEN_FILE=" + runtime.token,
		"DARK_FACTORY_FACTORYCTL=" + runtime.factoryctl,
		"HOME=" + runtime.home,
		"TMPDIR=" + runtime.temp,
		"PATH=" + runtime.toolPath,
	}
	switch kind {
	case kernel.ProviderClaudeCode:
		environment = append(environment, "CLAUDE_CONFIG_DIR="+filepath.Join(runtime.accountHome, claudeConfigDir))
	case kernel.ProviderCodex:
		environment = append(environment, "CODEX_HOME="+filepath.Join(runtime.accountHome, codexConfigDir))
	}
	return append(environment,
		"LANG=C",
		"LC_ALL=C",
		"TERM=xterm-256color",
		"SHELL=/bin/sh",
		"GIT_CEILING_DIRECTORIES="+runtime.gitCeiling,
		"GIT_DISCOVERY_ACROSS_FILESYSTEM=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/usr/bin/false",
		"GIT_SSH_COMMAND=/usr/bin/false",
		"GH_CONFIG_DIR=/dev/null",
	)
}

func validAbsolute(value string, limit int) bool {
	return validValue(value, limit) && filepath.IsAbs(value) && filepath.Clean(value) == value && value != string(filepath.Separator)
}

func validToolPath(value string) bool {
	if !validValue(value, runner.MaxEnvironmentEntryBytes-len("PATH=")) {
		return false
	}
	seen := make(map[string]struct{})
	for _, component := range filepath.SplitList(value) {
		if !validAbsolute(component, maxPathBytes) {
			return false
		}
		if _, duplicate := seen[component]; duplicate {
			return false
		}
		seen[component] = struct{}{}
	}
	return len(seen) > 0
}

// ValidateToolPath lets the daemon freeze one bounded startup-owned PATH and
// lets the worker codec reject drift without reimplementing its grammar.
func ValidateToolPath(value string) error {
	if !validToolPath(value) {
		return ErrInvalid
	}
	return nil
}

func validGitCeiling(value string) bool {
	return validAbsolute(value, maxPathBytes) && !strings.ContainsRune(value, rune(filepath.ListSeparator))
}

func validValue(value string, limit int) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value) && strings.IndexByte(value, 0) < 0
}
