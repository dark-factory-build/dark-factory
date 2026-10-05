//go:build darwin

package daemon

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// validateSuccessSource keeps a successful worker outcome live until its
// implementation is committed. A clean no-change success remains valid
// unless the task is an accepted intake issue.
func (daemon *Daemon) validateSuccessSource(ctx context.Context, live *liveAttempt, proposal kernel.Proposal) error {
	if live == nil || proposal.Kind() != kernel.OutcomeSucceeded {
		return nil
	}
	if daemon == nil || daemon.store == nil {
		return unverifiableSuccessSource("daemon store is unavailable")
	}
	run, found, err := daemon.store.Run(ctx, live.runID)
	if err != nil {
		return unverifiableSuccessSource(fmt.Sprintf("run facts unavailable: %v", err))
	}
	if !found {
		return unverifiableSuccessSource("run facts are missing")
	}
	if run.Role != kernel.RoleWorker {
		return nil
	}
	if run.ChangeID == nil {
		return unverifiableSuccessSource("worker has no Change identity")
	}
	changeState, found, err := daemon.store.Change(ctx, *run.ChangeID)
	if err != nil {
		return unverifiableSuccessSource(fmt.Sprintf("Change facts unavailable: %v", err))
	}
	if !found {
		return unverifiableSuccessSource("Change facts are missing")
	}
	if changeState.HeadCommit == nil {
		return nil
	}
	if changeState.Selection == nil {
		return unverifiableSuccessSource("Change source selection is missing")
	}
	git := daemon.gitExecutable.Load()
	parent := daemon.changeParent.Load()
	if git == nil || *git == "" || parent == nil || *parent == "" {
		return unverifiableSuccessSource("Change worktree path or Git executable is unavailable")
	}
	route, err := daemon.repositoryForChange(ctx, changeState)
	if err != nil {
		return unverifiableSuccessSource(fmt.Sprintf("repository route unavailable: %v", err))
	}
	repository, err := changeRepositoryIdentity(changeState.Selection.RepositoryIdentity())
	if err != nil {
		return unverifiableSuccessSource(fmt.Sprintf("repository identity unavailable: %v", err))
	}
	facts, err := change.InspectWorktree(ctx, *git, route.Root, repository, filepath.Join(*parent, changeState.ID.String()))
	if err != nil {
		return unverifiableSuccessSource(fmt.Sprintf("Change worktree facts unavailable: %v", err))
	}
	if facts.Dirty() {
		return kernel.NewOutcomeRefusal(fmt.Errorf("%w: %s (%w)", errDirtyWorkerChange, changeState.ID.String(), kernel.ErrConflict))
	}
	// An accepted issue promises a published Change; an empty one cannot be.
	if head, err := kernelCommit(facts.Head()); err == nil && kernelCommitEqual(head, changeState.Selection.Commit()) {
		if _, intake, err := daemon.store.IntakeAcceptanceForTask(ctx, run.TaskID); err != nil {
			return unverifiableSuccessSource(fmt.Sprintf("intake facts unavailable: %v", err))
		} else if intake {
			return kernel.NewOutcomeRefusal(fmt.Errorf("%w: %s (%w)", errEmptyIntakeChange, changeState.ID.String(), kernel.ErrConflict))
		}
	}
	return nil
}

func unverifiableSuccessSource(reason string) error {
	return kernel.NewOutcomeRefusal(fmt.Errorf("cannot verify worker source before success: %s (%w)", reason, kernel.ErrConflict))
}
