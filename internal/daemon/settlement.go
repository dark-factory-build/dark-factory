package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

var errDirtyWorkerChange = errors.New("uncommitted implementation in Change worktree; commit it or clean it up before reporting success")

type successSettlementContextKey struct{}

// settleRun commits the terminal outcome of a finalizing run through the
// reviewed finalize edges. A change without a worktree settles abandoned; a
// change with one settles retained at the branch head its worktree is at.
// Every state the kernel edges refuse is returned unchanged with the refusal
// — settlement never invents evidence.
func (daemon *Daemon) settleRun(ctx context.Context, changeParent string, runID kernel.RunID) (kernel.Run, error) {
	if daemon == nil || daemon.store == nil || runID == (kernel.RunID{}) {
		return kernel.Run{}, fmt.Errorf("%w: invalid run settlement", kernel.ErrInvalidValue)
	}

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
	if run.Role == kernel.RoleOrchestrator {
		at, err := daemon.timestamp()
		if err != nil {
			return run, err
		}

		final, err := daemon.store.FinalizeRun(ctx, run.ID, run.Revision, at)
		return final, err
	}
	if run.ChangeID == nil {
		return run, fmt.Errorf("%w: worker run without a candidate change", kernel.ErrCorruptState)
	}

	changeState, found, err := daemon.store.Change(ctx, *run.ChangeID)
	if err != nil || !found {
		if err == nil {
			err = kernel.ErrCorruptState
		}
		return run, err
	}
	switch changeState.Phase {
	case kernel.ChangeReserved, kernel.ChangePrepared:
		settlement, err := kernel.NewAbandonedChangeSettlement(changeState.Revision)
		if err != nil {
			return run, err
		}
		at, err := daemon.timestamp()
		if err != nil {
			return run, err
		}

		final, err := daemon.store.FinalizeWorkerRun(ctx, run.ID, run.Revision, settlement, at)
		return final, err
	case kernel.ChangeAvailable:
		settleRetained := daemon.settleRetained
		if settleRetained == nil {
			settleRetained = daemon.retainedSettlement
		}
		inspectionCtx, cancel := context.WithTimeout(ctx, supervisorInspectionWindow)
		inspectionCtx = context.WithValue(inspectionCtx, successSettlementContextKey{}, run.Proposal.Kind() == kernel.OutcomeSucceeded)
		settlement, err := settleRetained(inspectionCtx, changeParent, changeState)
		cancel()
		if err != nil {
			return run, err
		}
		at, err := daemon.timestamp()
		if err != nil {
			return run, err
		}

		final, err := daemon.store.FinalizeWorkerRun(ctx, run.ID, run.Revision, settlement, at)
		return final, err
	default:
		return run, fmt.Errorf("%w: change %s is not settleable for a finalizing run", kernel.ErrCorruptState, changeState.Phase.String())
	}
}

// retainedSettlement reads the Change's worktree as the worker left it and
// retains the Change at the branch head found there, the evidence the
// supervisor's own finalize takes. A worktree that is gone from the Change's
// name cannot be retained and cannot come back: that is the run's outcome,
// a visible source failure, and the Change is abandoned so that the task's
// retry makes a fresh worktree. Any other fault (the repository, Git, the
// arguments) may pass tomorrow and leaves the run finalizing. A Change that
// is still a Git-free tree, never adopted, settles as it is, with no head.
func (daemon *Daemon) retainedSettlement(ctx context.Context, changeParent string, changeState kernel.Change) (kernel.ChangeSettlement, error) {
	if changeParent == "" || changeState.Selection == nil {
		return kernel.ChangeSettlement{}, fmt.Errorf("%w: published change lacks retained evidence", kernel.ErrConflict)
	}
	if changeState.HeadCommit == nil {
		return kernel.NewRetainedChangeSettlement(changeState.Revision, nil)
	}
	git := daemon.gitExecutable.Load()
	if git == nil || *git == "" {
		return kernel.ChangeSettlement{}, fmt.Errorf("%w: no Git executable for settlement", kernel.ErrConflict)
	}
	project, found, err := daemon.store.Project(ctx, changeState.ProjectID)
	if err != nil || !found {
		if err == nil {
			err = kernel.ErrCorruptState
		}
		return kernel.ChangeSettlement{}, err
	}
	repository, err := changeRepositoryIdentity(changeState.Selection.RepositoryIdentity())
	if err != nil {
		return kernel.ChangeSettlement{}, err
	}
	path := filepath.Join(changeParent, changeState.ID.String())
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return kernel.NewRefusedChangeSettlement(changeState.Revision, "the Change worktree is gone from changes/"+changeState.ID.String())
	}
	facts, err := change.InspectWorktree(ctx, *git, project.Root, repository, path)
	if err != nil {
		return kernel.ChangeSettlement{}, errors.Join(fmt.Errorf("%w: Change worktree did not verify", kernel.ErrConflict), err)
	}
	if success, _ := ctx.Value(successSettlementContextKey{}).(bool); success {
		if facts.Dirty() {
			return kernel.NewRefusedChangeSettlement(changeState.Revision, fmt.Sprintf("%s: commit it or clean it up before reporting success", errDirtyWorkerChange))
		}
	}
	head, err := kernelCommit(facts.Head())
	if err != nil {
		return kernel.ChangeSettlement{}, err
	}
	return kernel.NewRetainedChangeSettlement(changeState.Revision, &head)
}
