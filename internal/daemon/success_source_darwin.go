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
	if live == nil || proposal.Kind() != kernel.OutcomeSucceeded || daemon == nil || daemon.store == nil {
		return nil
	}
	run, found, err := daemon.store.Run(ctx, live.runID)
	if err != nil || !found || run.Role != kernel.RoleWorker || run.ChangeID == nil {
		return nil
	}
	changeState, found, err := daemon.store.Change(ctx, *run.ChangeID)
	if err != nil || !found || changeState.Selection == nil || changeState.HeadCommit == nil {
		return nil
	}
	git := daemon.gitExecutable.Load()
	parent := daemon.changeParent.Load()
	if git == nil || *git == "" || parent == nil || *parent == "" {
		return nil
	}
	project, found, err := daemon.store.Project(ctx, changeState.ProjectID)
	if err != nil || !found {
		return nil
	}
	repository, err := changeRepositoryIdentity(changeState.Selection.RepositoryIdentity())
	if err != nil {
		return nil
	}
	facts, err := change.InspectWorktree(ctx, *git, project.Root, repository, filepath.Join(*parent, changeState.ID.String()))
	if err != nil {
		return nil
	}
	if facts.Dirty() {
		return kernel.NewOutcomeRefusal(errors.Join(kernel.ErrConflict, fmt.Errorf("%w: %s", errDirtyWorkerChange, changeState.ID.String())))
	}
	return nil
}
