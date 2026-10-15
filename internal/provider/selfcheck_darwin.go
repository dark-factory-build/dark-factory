//go:build darwin

package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/dark-factory-build/dark-factory/internal/gitauthor"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

const maxSelfCheckDiagnosticBytes = kernel.MaxProviderAdoptionDetailBytes

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
	if _, err := runner.PrepareCommittedExecSpec(launch.Executable(), launch.Argv(), launch.Environment(), checkout); err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, launch.Argv()[0], launch.Argv()[1:]...)
	command.Dir = checkout
	command.Env = launch.Environment()
	command.Stdin = strings.NewReader("")
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return nil, err
	}
	process := &confinedNativeSelfCheckProcess{command: command, done: make(chan error, 1), checkout: checkout, factoryctl: factoryctl.Path(), environment: launch.Environment()}
	go func() {
		err := command.Wait()
		process.waitMu.Lock()
		process.waitErr = err
		process.waitMu.Unlock()
		process.done <- err
	}()
	return process, nil
}

type confinedNativeSelfCheckProcess struct {
	command     *exec.Cmd
	done        chan error
	waitMu      sync.Mutex
	waitErr     error
	waitDone    bool
	checkout    string
	factoryctl  string
	environment []string
}

func (p *confinedNativeSelfCheckProcess) ReadCheckout(ctx context.Context) error {
	if p == nil || p.command == nil {
		return ErrInvalid
	}
	if err := p.running(); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, "/usr/bin/git", "-C", p.checkout, "rev-parse", "--show-toplevel")
	command.Env = []string{"PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	output, err := command.Output()
	if err != nil {
		return err
	}
	want, err := filepath.EvalSymlinks(p.checkout)
	if err != nil || filepath.Clean(strings.TrimSpace(string(output))) != filepath.Clean(want) {
		return fmt.Errorf("%w: self-check checkout is not a real Change", ErrInvalid)
	}
	return p.running()
}

func (p *confinedNativeSelfCheckProcess) CommitChange(ctx context.Context) error {
	if p == nil || p.command == nil {
		return ErrInvalid
	}
	if err := p.running(); err != nil {
		return err
	}
	if err := p.git(ctx, "-C", p.checkout, "add", "--all"); err != nil {
		return err
	}
	if err := p.git(ctx, "-C", p.checkout, "-c", "user.name="+gitauthor.AutomationName, "-c", "user.email="+gitauthor.AutomationEmail, "commit", "--allow-empty", "-m", "provider self-check"); err != nil {
		return err
	}
	return p.running()
}

func (p *confinedNativeSelfCheckProcess) ReachAttemptAPI(ctx context.Context) error {
	if p == nil || p.command == nil {
		return ErrInvalid
	}
	if err := p.running(); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, p.factoryctl, "attempt", "task")
	command.Dir = p.checkout
	command.Env = append([]string(nil), p.environment...)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return err
	}
	return p.running()
}

func (p *confinedNativeSelfCheckProcess) running() error {
	if p == nil || p.command == nil || p.command.Process == nil || p.done == nil {
		return ErrInvalid
	}
	select {
	case err := <-p.done:
		p.waitMu.Lock()
		p.waitErr = err
		p.waitDone = true
		p.waitMu.Unlock()
		return fmt.Errorf("native provider self-check provider exited: %w", err)
	default:
		if err := p.command.Process.Signal(syscall.Signal(0)); err != nil {
			return fmt.Errorf("native provider self-check provider exited: %w", err)
		}
		return nil
	}
}

func (p *confinedNativeSelfCheckProcess) git(ctx context.Context, args ...string) error {
	command := exec.CommandContext(ctx, "/usr/bin/git", args...)
	command.Env = []string{"PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run()
}

func (p *confinedNativeSelfCheckProcess) Close() error {
	if p == nil || p.command == nil || p.command.Process == nil || p.done == nil {
		return nil
	}
	select {
	case <-p.done:
		return nil
	default:
	}
	p.waitMu.Lock()
	waitDone := p.waitDone
	p.waitMu.Unlock()
	if waitDone {
		return nil
	}
	_ = p.command.Process.Kill()
	select {
	case err := <-p.done:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil
		}
		return err
	case <-time.After(time.Second):
		return errors.New("native provider self-check provider did not exit")
	}
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
