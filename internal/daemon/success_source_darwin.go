//go:build darwin

package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// validateSuccessSource keeps a successful worker outcome live until its
// implementation is committed. A clean no-change success remains valid.
func (daemon *Daemon) validateSuccessSource(ctx context.Context, live *liveAttempt, proposal kernel.Proposal) error {
	if live == nil || proposal.Kind() != kernel.OutcomeSucceeded {
		return nil
	}
	if daemon == nil || daemon.store == nil {
		return unverifiableSuccessSource("daemon store is unavailable")
	}
	readRun := daemon.store.Run
	if daemon.successSourceRun != nil {
		readRun = daemon.successSourceRun
	}
	run, found, err := readRun(ctx, live.runID)
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
	readChange := daemon.store.Change
	if daemon.successSourceChange != nil {
		readChange = daemon.successSourceChange
	}
	changeState, found, err := readChange(ctx, *run.ChangeID)
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
	inspect := func(ctx context.Context, git, root string, repository change.RepositoryIdentity, path string) (change.WorktreeFacts, error) {
		return change.InspectWorktree(ctx, git, root, repository, path)
	}
	if daemon.successSourceInspect != nil {
		inspect = daemon.successSourceInspect
	}
	facts, err := inspect(ctx, *git, route.Root, repository, filepath.Join(*parent, changeState.ID.String()))
	if err != nil {
		return unverifiableSuccessSource(fmt.Sprintf("Change worktree facts unavailable: %v", err))
	}
	if facts.Dirty() {
		return kernel.NewOutcomeRefusal(errors.Join(kernel.ErrConflict, fmt.Errorf("%w: %s", errDirtyWorkerChange, changeState.ID.String())))
	}
	return nil
}

func unverifiableSuccessSource(reason string) error {
	return kernel.NewOutcomeRefusal(errors.Join(kernel.ErrConflict, fmt.Errorf("cannot verify worker source before success: %s", reason)))
}
