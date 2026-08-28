package provider

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

const (
	shellPath            = "/bin/sh"
	shellVersion         = "darwin-system-sh-v1"
	maxInitialInputBytes = runner.MaxProviderTaskBytes
	maxPathBytes         = 4096
	maxSocketBytes       = 103
)

var (
	ErrInvalid     = errors.New("provider: invalid launch contract")
	ErrUnavailable = errors.New("provider: unavailable")
)

// Installation binds one provider kind to one exact executable commitment.
// There is intentionally only a Shell constructor until the native Claude and
// Codex metadata and interactive-input contracts have causal witnesses.
type Installation struct {
	provider   kernel.Provider
	executable runner.ExecutableCommitment
	version    string
}

func NewShellInstallation(executable runner.ExecutableCommitment) (Installation, error) {
	if executable.Path() != shellPath || executable.Verify() != nil {
		return Installation{}, ErrUnavailable
	}
	return Installation{provider: kernel.ProviderShell, executable: executable, version: shellVersion}, nil
}

func (installation Installation) Provider() kernel.Provider { return installation.provider }
func (installation Installation) Version() string           { return installation.version }

func (Installation) String() string   { return "provider installation (private)" }
func (Installation) GoString() string { return "provider.Installation{private}" }

// RuntimePaths is the complete set of external strings permitted to enter the
// provider environment. Construction is lexical only; the daemon-owned Change
// worker must retain and revalidate the exact filesystem/executable
// capabilities that make these paths true immediately around Build and exec.
// This value is never authority by itself.
type RuntimePaths struct {
	home, temp, socket, token, factoryctl, gitCeiling, toolPath string
}

func NewRuntimePaths(home, temp, socket, token, factoryctl, gitCeiling, toolPath string) (RuntimePaths, error) {
	runtime := RuntimePaths{
		home: home, temp: temp, socket: socket, token: token,
		factoryctl: factoryctl, gitCeiling: gitCeiling, toolPath: toolPath,
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
	if !validProvider(kind) || !validOptional(model, 128) || !validOptional(reasoningEffort, 32) || !runtime.valid() {
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
	executable  runner.ExecutableCommitment
	argv        []string
	environment []string
}

func (launch Launch) Executable() runner.ExecutableCommitment { return launch.executable }
func (launch Launch) Argv() []string                          { return append([]string(nil), launch.argv...) }
func (launch Launch) Environment() []string                   { return append([]string(nil), launch.environment...) }

func (Launch) String() string   { return "provider launch (private)" }
func (Launch) GoString() string { return "provider.Launch{private}" }

// Build is the one closed provider-selection switch. Claude and Codex remain
// explicit unavailable cases rather than speculative launch descriptions.
func Build(request Request) (Launch, error) {
	if !request.runtime.valid() {
		return Launch{}, ErrInvalid
	}
	switch request.provider {
	case kernel.ProviderShell:
		if request.installation.provider != request.provider {
			return Launch{}, ErrInvalid
		}
		if request.model != "" || request.reasoningEffort != "" {
			return Launch{}, ErrInvalid
		}
		if request.installation.version != shellVersion || request.installation.executable.Path() != shellPath {
			return Launch{}, ErrInvalid
		}
		if err := request.installation.executable.Verify(); err != nil {
			return Launch{}, errors.Join(ErrUnavailable, err)
		}
		return Launch{
			executable:  request.installation.executable,
			argv:        []string{shellPath, "-s"},
			environment: request.runtime.environment(),
		}, nil
	case kernel.ProviderClaudeCode, kernel.ProviderCodex:
		return Launch{}, unavailableError{provider: request.provider}
	default:
		return Launch{}, ErrInvalid
	}
}

// InitialInput applies only the provider's fixed terminal submission framing.
// It never interprets, normalizes, or duplicates the task body.
func InitialInput(kind kernel.Provider, task []byte) ([]byte, error) {
	switch kind {
	case kernel.ProviderShell:
		if len(task) == 0 || len(task) > maxInitialInputBytes {
			return nil, ErrInvalid
		}
		input := append([]byte(nil), task...)
		if input[len(input)-1] != '\n' {
			input = append(input, '\n')
		}
		if err := runner.ValidateProviderInput(input); err != nil {
			return nil, ErrInvalid
		}
		return input, nil
	case kernel.ProviderClaudeCode, kernel.ProviderCodex:
		return nil, unavailableError{provider: kind}
	default:
		return nil, ErrInvalid
	}
}

type unavailableError struct{ provider kernel.Provider }

func (err unavailableError) Error() string {
	return fmt.Sprintf("provider: %s unavailable", err.provider.String())
}
func (unavailableError) Unwrap() error { return ErrUnavailable }

func (runtime RuntimePaths) valid() bool {
	paths := []string{runtime.home, runtime.temp, runtime.socket, runtime.token, runtime.factoryctl}
	for _, path := range paths {
		if !validAbsolute(path, maxPathBytes) {
			return false
		}
	}
	return len(runtime.socket) <= maxSocketBytes && runtime.home != runtime.temp &&
		validGitCeiling(runtime.gitCeiling) && validToolPath(runtime.toolPath)
}

func (runtime RuntimePaths) environment() []string {
	return []string{
		"DARK_FACTORY_SOCKET=" + runtime.socket,
		"DARK_FACTORY_ATTEMPT_TOKEN_FILE=" + runtime.token,
		"DARK_FACTORY_FACTORYCTL=" + runtime.factoryctl,
		"HOME=" + runtime.home,
		"TMPDIR=" + runtime.temp,
		"PATH=" + runtime.toolPath,
		"LANG=C",
		"LC_ALL=C",
		"TERM=xterm-256color",
		"SHELL=/bin/sh",
		"NO_COLOR=1",
		"GIT_CEILING_DIRECTORIES=" + runtime.gitCeiling,
		"GIT_DISCOVERY_ACROSS_FILESYSTEM=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/usr/bin/false",
		"GIT_SSH_COMMAND=/usr/bin/false",
		"GH_CONFIG_DIR=/dev/null",
	}
}

func validProvider(kind kernel.Provider) bool {
	switch kind {
	case kernel.ProviderShell, kernel.ProviderClaudeCode, kernel.ProviderCodex:
		return true
	default:
		return false
	}
}

func validAbsolute(value string, limit int) bool {
	return validValue(value, limit) && filepath.IsAbs(value) && filepath.Clean(value) == value && value != string(filepath.Separator)
}

func validToolPath(value string) bool {
	if !validValue(value, 8192) {
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

func validGitCeiling(value string) bool {
	return validAbsolute(value, maxPathBytes) && !strings.ContainsRune(value, rune(filepath.ListSeparator))
}

func validOptional(value string, limit int) bool {
	return value == "" || validValue(value, limit)
}

func validValue(value string, limit int) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value) && strings.IndexByte(value, 0) < 0
}
