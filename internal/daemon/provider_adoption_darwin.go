//go:build darwin

package daemon

import (
	"context"
	"fmt"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/provider"
)

// AdoptProviderCandidate is the daemon-owned orchestration boundary. It
// records a newly resolved executable, runs exactly one confined native
// self-check, and promotes only its complete receipt. A failed receipt is
// retained as one durable NEEDS YOU state; the active executable is untouched.
func (daemon *Daemon) AdoptProviderCandidate(ctx context.Context, request provider.Request, checkout string, runner provider.NativeSelfCheckRunner) (kernel.ProviderAdoption, error) {
	if daemon == nil || daemon.store == nil {
		return kernel.ProviderAdoption{}, fmt.Errorf("%w: provider adoption daemon", kernel.ErrInvalidValue)
	}
	at, err := daemon.timestamp()
	if err != nil {
		return kernel.ProviderAdoption{}, err
	}
	candidate, err := daemon.store.RecordProviderCandidate(ctx, request.Provider(), request.Commitment(), at)
	if err != nil {
		return kernel.ProviderAdoption{}, err
	}
	if candidate.ActiveCommitment == candidate.CandidateCommitment || candidate.CandidateCommitment == "" || candidate.NeedsYou {
		return candidate, nil
	}
	proof, checkErr := provider.RunNativeSelfCheck(ctx, request, checkout, runner)
	if checkErr != nil {
		at, err = daemon.timestamp()
		if err != nil {
			return kernel.ProviderAdoption{}, err
		}
		return daemon.store.FailProviderCandidate(ctx, request.Provider(), candidate.Revision, checkErr.Error(), at)
	}
	at, err = daemon.timestamp()
	if err != nil {
		return kernel.ProviderAdoption{}, err
	}
	return daemon.store.CommitProviderCandidate(ctx, request.Provider(), candidate.Revision, proof, at)
}
