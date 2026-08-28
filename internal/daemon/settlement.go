package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// settleAbandonedRun commits the terminal outcome of a finalizing run whose
// candidate Change was never published: the change is settled abandoned and
// the durable proposal becomes the terminal record. It composes only the
// reviewed FinalizeRun/FinalizeWorkerRun edges and refuses every state those
// edges refuse — a run holding unresolved residue or a published change is
// returned unchanged with the refusal.
func (daemon *Daemon) settleAbandonedRun(runID kernel.RunID) (kernel.Run, error) {
	if daemon == nil || daemon.store == nil || runID == (kernel.RunID{}) {
		return kernel.Run{}, fmt.Errorf("%w: invalid run settlement", kernel.ErrInvalidValue)
	}
	ctx, cancel := context.WithTimeout(context.Background(), supervisorStoreAttemptWindow)
	defer cancel()
	run, found, err := daemon.store.Run(ctx, runID)
	if err != nil || !found {
		if err == nil {
			err = kernel.ErrNotFound
		}
		return kernel.Run{}, err
	}
	if run.Phase == kernel.RunTerminal {
		return run, nil
	}
	if run.Phase != kernel.RunFinalizing || run.Proposal == nil {
		return run, fmt.Errorf("%w: run is not settleable", kernel.ErrConflict)
	}
	at, err := daemon.timestamp()
	if err != nil {
		return run, err
	}
	if run.Role == kernel.RoleOrchestrator {
		return daemon.store.FinalizeRun(ctx, run.ID, run.Revision, at)
	}
	if run.ChangeID == nil {
		return run, fmt.Errorf("%w: worker run without a candidate change", kernel.ErrCorruptState)
	}
	change, found, err := daemon.store.Change(ctx, *run.ChangeID)
	if err != nil || !found {
		if err == nil {
			err = kernel.ErrCorruptState
		}
		return run, err
	}
	if change.Phase != kernel.ChangeReserved && change.Phase != kernel.ChangePrepared {
		// A published change needs the retained settlement with availability
		// evidence; that reconstruction is not abandoned-arm authority.
		return run, fmt.Errorf("%w: change %s is not abandonable", kernel.ErrConflict, change.Phase.String())
	}
	settlement, err := kernel.NewAbandonedChangeSettlement(change.Revision)
	if err != nil {
		return run, err
	}
	return daemon.store.FinalizeWorkerRun(ctx, run.ID, run.Revision, settlement, at)
}

// errUnsettledCompletion marks a scheduled attempt that converged durably but
// could not be settled to a terminal record.
var errUnsettledCompletion = errors.New("daemon: scheduled completion was not settled")
