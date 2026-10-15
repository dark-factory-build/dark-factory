//go:build darwin

package provider

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
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
	}
	return errors.New(message)
}
