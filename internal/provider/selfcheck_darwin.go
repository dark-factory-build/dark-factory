//go:build darwin

package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/dark-factory-build/dark-factory/internal/gitauthor"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

const maxSelfCheckDiagnosticBytes = kernel.MaxProviderAdoptionDetailBytes
const selfCheckMarkerDir = ".dark-factory/self-check"

// NativeSelfCheckProcess is the narrow real-provider contract used during
// adoption. The caller supplies the confined runner; this package owns the
// launch and the required causal order.
type NativeSelfCheckProcess interface {
	ReadCheckout(context.Context) error
	CommitChange(context.Context) error
	ReachAttemptAPI(context.Context) error
	Close() error
}

type NativeSelfCheckRunner interface {
	Start(context.Context, Launch, string) (NativeSelfCheckProcess, error)
}

// NewConfinedNativeSelfCheckRunner returns the production runner for the
// adoption boundary. It reuses the launch's committed executable and closed
// environment, and uses the runtime's fixed factoryctl path for the API edge.
func NewConfinedNativeSelfCheckRunner(request Request) NativeSelfCheckRunner {
	return confinedNativeSelfCheckRunner{factoryctl: request.runtime.factoryctl}
}

type confinedNativeSelfCheckRunner struct{ factoryctl string }

func (r confinedNativeSelfCheckRunner) Start(ctx context.Context, launch Launch, checkout string) (NativeSelfCheckProcess, error) {
	if ctx == nil || r.factoryctl == "" || !filepath.IsAbs(checkout) || filepath.Clean(checkout) != checkout {
		return nil, fmt.Errorf("%w: invalid confined native self-check", ErrInvalid)
	}
	if err := launch.Executable().Verify(); err != nil {
		return nil, err
	}
	factoryctl, err := runner.CommitExecutableLocator(r.factoryctl)
	if err != nil {
		return nil, err
	}
	if err := factoryctl.Verify(); err != nil {
		return nil, err
	}
	argv := launch.Argv()
	if len(argv) == 0 {
		return nil, fmt.Errorf("%w: missing native self-check prompt", ErrInvalid)
	}
	argv[len(argv)-1] = nativeSelfCheckPrompt(checkout, factoryctl.Path())
	if _, err := runner.PrepareCommittedExecSpec(launch.Executable(), argv, launch.Environment(), checkout); err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Dir = checkout
	command.Env = launch.Environment()
	command.Stdin = nil
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return nil, err
	}
	process := &confinedNativeSelfCheckProcess{command: command, done: make(chan error, 1), checkout: checkout}
	go func() {
		err := command.Wait()
		process.waitMu.Lock()
		process.waitErr = err
		process.waitDone = true
		process.waitMu.Unlock()
		process.done <- err
	}()
	return process, nil
}

type confinedNativeSelfCheckProcess struct {
	command  *exec.Cmd
	done     chan error
	waitMu   sync.Mutex
	waitErr  error
	waitDone bool
	checkout string
}

func (p *confinedNativeSelfCheckProcess) ReadCheckout(ctx context.Context) error {
	if p == nil || p.command == nil {
		return ErrInvalid
	}
	if err := p.wait(ctx); err != nil {
		return err
	}
	return p.marker("checkout")
}

func (p *confinedNativeSelfCheckProcess) CommitChange(ctx context.Context) error {
	if p == nil || p.command == nil {
		return ErrInvalid
	}
	if err := p.wait(ctx); err != nil {
		return err
	}
	return p.marker("commit")
}

func (p *confinedNativeSelfCheckProcess) ReachAttemptAPI(ctx context.Context) error {
	if p == nil || p.command == nil {
		return ErrInvalid
	}
	if err := p.wait(ctx); err != nil {
		return err
	}
	return p.marker("attempt-api")
}

func (p *confinedNativeSelfCheckProcess) wait(ctx context.Context) error {
	if p == nil || p.command == nil || p.command.Process == nil || p.done == nil {
		return ErrInvalid
	}
	p.waitMu.Lock()
	if p.waitDone {
		err := p.waitErr
		p.waitMu.Unlock()
		if err != nil {
			return fmt.Errorf("native provider self-check provider exited: %w", err)
		}
		return nil
	}
	p.waitMu.Unlock()
	select {
	case err := <-p.done:
		p.waitMu.Lock()
		p.waitErr = err
		p.waitDone = true
		p.waitMu.Unlock()
		if err != nil {
			return fmt.Errorf("native provider self-check provider exited: %w", err)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *confinedNativeSelfCheckProcess) marker(name string) error {
	if _, err := os.Stat(filepath.Join(p.checkout, selfCheckMarkerDir, name)); err != nil {
		return fmt.Errorf("%w: provider self-check marker %s: %v", ErrInvalid, name, err)
	}
	return nil
}

func (p *confinedNativeSelfCheckProcess) Close() error {
	if p == nil || p.command == nil || p.command.Process == nil || p.done == nil {
		return nil
	}
	p.waitMu.Lock()
	waitDone := p.waitDone
	p.waitMu.Unlock()
	if waitDone {
		return nil
	}
	_ = p.command.Process.Kill()
	err := <-p.done
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil
	}
	return err
}

func nativeSelfCheckPrompt(checkout, factoryctl string) string {
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	marker := filepath.Join(checkout, selfCheckMarkerDir)
	return fmt.Sprintf("# Run this provider self-check in the current checkout using your own shell.\nset -eu\nexport GIT_TERMINAL_PROMPT=0 GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null\ntest \"$(git rev-parse --show-toplevel)\" = %s\nmkdir -p %s\nprintf checkout > %s/checkout\ngit add --all %s\ngit -c user.name=%s -c user.email=%s -c commit.gpgSign=false commit --no-verify --allow-empty -m 'provider self-check'\nprintf commit > %s/commit\n%s attempt peer status\nprintf attempt-api > %s/attempt-api\n", quote(checkout), quote(marker), quote(marker), quote(marker), quote(gitauthor.AutomationName), quote(gitauthor.AutomationEmail), quote(marker), quote(factoryctl), quote(marker))
}

// RunNativeSelfCheck builds the exact native launch, starts it once in the
// supplied confined checkout, and stops at the first failed edge. It never
// retries a candidate: the daemon records the returned proof or diagnostic
// through the kernel adoption boundary.
func RunNativeSelfCheck(ctx context.Context, request Request, checkout string, runner NativeSelfCheckRunner) (kernel.ProviderSelfCheck, error) {
	if ctx == nil || runner == nil || !filepath.IsAbs(checkout) || filepath.Clean(checkout) != checkout || request.provider == kernel.ProviderShell {
		return kernel.ProviderSelfCheck{}, fmt.Errorf("%w: invalid native provider self-check", ErrInvalid)
	}
	launch, err := Build(request)
	if err != nil {
		return kernel.ProviderSelfCheck{}, err
	}
	process, err := runner.Start(ctx, launch, checkout)
	if err != nil {
		return kernel.ProviderSelfCheck{}, boundedSelfCheckError("start", err)
	}
	defer process.Close()
	proof := kernel.ProviderSelfCheck{Started: true}
	if err := process.ReadCheckout(ctx); err != nil {
		return proof, boundedSelfCheckError("read checkout", err)
	}
	proof.ReadCheckout = true
	if err := process.CommitChange(ctx); err != nil {
		return proof, boundedSelfCheckError("commit Change", err)
	}
	proof.Committed = true
	if err := process.ReachAttemptAPI(ctx); err != nil {
		return proof, boundedSelfCheckError("attempt API", err)
	}
	proof.AttemptAPI = true
	return proof, nil
}

func boundedSelfCheckError(edge string, err error) error {
	message := strings.TrimSpace(fmt.Sprintf("native provider self-check %s: %v", edge, err))
	message = strings.ToValidUTF8(message, "")
	if len(message) > maxSelfCheckDiagnosticBytes {
		message = message[:maxSelfCheckDiagnosticBytes]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
	}
	if !utf8.ValidString(message) {
		return errors.New("native provider self-check diagnostic is not valid UTF-8")
	}
	return errors.New(message)
}
