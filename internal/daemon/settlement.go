package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

var errDirtyWorkerChange = errors.New("uncommitted implementation in Change worktree; commit it or clean it up before reporting success")
var errEmptyIntakeChange = errors.New("nothing committed; commit the change, or report `attempt block` with why no change is needed")

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
		if err == nil {
			daemon.pipelineAt.Store(0) // publish a settled Change on the next tick
		}
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
// retry makes a fresh worktree. So is a worktree that fails its own
// validation while the repository and Git verify (say, a worker added a
// remote to its private config): it fails the same way tomorrow. Any other
// fault (the repository, Git, I/O) may pass and leaves the run finalizing.
// A Change that is still a Git-free tree, never adopted, settles as it is,
// with no head.
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
	route, err := daemon.repositoryForChange(ctx, changeState)
	if err != nil {
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
	facts, err := change.InspectWorktree(ctx, *git, route.Root, repository, path)
	if invalid := (*change.ValidationError)(nil); errors.As(err, &invalid) && invalid.Worktree {
		return kernel.NewRefusedChangeSettlement(changeState.Revision, "the Change worktree did not verify: "+invalid.Reason)
	}
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

// changeReclaimBatch bounds the Changes one reclaim pass inspects, so the
// backlog drains a few a minute without long writer holds or I/O spikes.
const changeReclaimBatch = 4

// tickChangeReclaim starts a reclaim pass off the scheduler loop, a minute
// after the last one.
func (daemon *Daemon) tickChangeReclaim(ctx context.Context) {
	if daemon.now().UnixNano() < daemon.reclaimAt.Load() || !daemon.reclaimBusy.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer daemon.reclaimBusy.Store(false)
		daemon.reclaimChanges(ctx)
		daemon.reclaimAt.Store(daemon.now().Add(time.Minute).UnixNano())
	}()
}

// reclaimChanges inspects up to changeReclaimBatch retained Changes the
// kernel's rule allows, removes each one's worktree and Git state
// (change.RemoveWorktree), and records it abandoned, as a Change whose
// worktree is gone: the task's retry makes a fresh one. A Change reopened
// since the rule was read is skipped; one RemoveWorktree refuses is kept and
// logged once. A removal whose record failed is found again next pass and,
// with nothing left on disk, only recorded.
func (daemon *Daemon) reclaimChanges(ctx context.Context) int {
	parent, git := daemon.changeParent.Load(), daemon.gitExecutable.Load()
	at, err := daemon.timestamp()
	if parent == nil || *parent == "" || git == nil || *git == "" || err != nil {
		return 0
	}
	candidates, err := daemon.store.ReclaimableChanges(ctx, at)
	if err != nil {
		LogFactoryd(daemon.log, "factoryd: reclaim Changes: %v\n", err)
		return 0
	}
	if daemon.keptChanges == nil {
		daemon.keptChanges = map[kernel.ChangeID]bool{}
	}
	inspected, reclaimed := 0, 0
	for _, candidate := range candidates {
		if inspected == changeReclaimBatch || ctx.Err() != nil {
			break
		}
		if daemon.keptChanges[candidate.ID] {
			continue
		}
		inspected++
		// A retry or send-back may have reopened it since the rule was read.
		current, found, err := daemon.store.Change(ctx, candidate.ID)
		task, taskFound, taskErr := daemon.store.Task(ctx, candidate.TaskID)
		if err != nil || taskErr != nil || !found || !taskFound || current.Phase != kernel.ChangeRetained || current.Revision != candidate.Revision ||
			task.Status != kernel.TaskSucceeded && task.Status != kernel.TaskFailed && task.Status != kernel.TaskCancelled {
			continue
		}
		route, err := daemon.repositoryForChange(ctx, candidate.Change)
		var repository change.RepositoryIdentity
		var head *change.ObjectID
		if err == nil {
			repository, err = changeRepositoryIdentity(candidate.Selection.RepositoryIdentity())
		}
		if err == nil && candidate.HeadCommit != nil {
			var id change.ObjectID
			_, id, err = changeCommit(*candidate.HeadCommit)
			head = &id
		}
		if err == nil {
			err = change.RemoveWorktree(ctx, *git, route.Root, repository, filepath.Join(*parent, candidate.ID.String()), head, candidate.GivenUp)
		}
		if err != nil {
			daemon.keptChanges[candidate.ID] = true
			LogFactoryd(daemon.log, "factoryd: keeping Change %s: %v\n", candidate.ID, err)
			continue
		}
		if at, err = daemon.timestamp(); err == nil {
			_, err = daemon.store.ReclaimChange(ctx, candidate.ID, candidate.Revision, candidate.GivenUp, at)
		}
		if err != nil {
			LogFactoryd(daemon.log, "factoryd: record reclaimed Change %s: %v\n", candidate.ID, err)
			continue
		}
		reclaimed++
	}
	return reclaimed
}
