//go:build darwin

package daemon

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// attemptSourceHandoff answers an explicit source request for one settled
// Change with where its work is: the worktree, its actual Git
// directory, the branch and the exact head the Change settled at. The
// worktree must still be at that head; a branch that moved since settlement
// is refused rather than described by a stale receipt. A Change from before
// managed worktrees is adopted into one here, at its recorded base and with
// its files untouched, and the adoption recorded as a fact of the same
// Change revision.
func (daemon *Daemon) attemptSourceHandoff(ctx context.Context, handoff kernel.RetainedChangeHandoff) (api.RetainedChangeHandoff, error) {
	parent, git := daemon.changeParent.Load(), daemon.gitExecutable.Load()
	if parent == nil || *parent == "" || git == nil || *git == "" {
		return api.RetainedChangeHandoff{}, errInvalidContract
	}
	changeState, found, err := daemon.store.Change(ctx, handoff.ChangeID)
	if err != nil {
		return api.RetainedChangeHandoff{}, err
	}
	if !found || changeState.Phase != kernel.ChangeRetained || changeState.Revision != handoff.ChangeRevision || changeState.Selection == nil {
		return api.RetainedChangeHandoff{}, errors.Join(kernel.ErrConflict, errInvalidContract)
	}
	route, err := daemon.repositoryForChange(ctx, changeState)
	if err != nil {
		return api.RetainedChangeHandoff{}, err
	}
	repository, err := changeRepositoryIdentity(changeState.Selection.RepositoryIdentity())
	if err != nil {
		return api.RetainedChangeHandoff{}, err
	}
	_, base, err := changeCommit(changeState.Selection.Commit())
	if err != nil {
		return api.RetainedChangeHandoff{}, err
	}
	path := filepath.Join(*parent, changeState.ID.String())
	branch := change.BranchName(changeState.ID.String())
	var facts change.WorktreeFacts
	if changeState.HeadCommit == nil {
		facts, err = change.AdoptWorktree(ctx, *git, route.Root, repository, path, branch, base)
		if err != nil {
			return api.RetainedChangeHandoff{}, err
		}
		head, err := kernelCommit(facts.Head())
		if err != nil {
			return api.RetainedChangeHandoff{}, err
		}
		if changeState, err = daemon.store.RecordChangeWorktree(ctx, changeState.ID, changeState.Revision, head); err != nil {
			return api.RetainedChangeHandoff{}, err
		}
	} else {
		facts, err = change.InspectWorktree(ctx, *git, route.Root, repository, path)
		if err != nil {
			return api.RetainedChangeHandoff{}, err
		}
	}
	_, head, err := changeCommit(*changeState.HeadCommit)
	if err != nil {
		return api.RetainedChangeHandoff{}, err
	}
	if !facts.Head().Equal(head) || facts.Branch() != branch {
		return api.RetainedChangeHandoff{}, errors.Join(kernel.ErrConflict, errors.New("retained Change worktree is not at its settled head"))
	}
	projected := projectHandoffIdentity(handoff)
	projected.HeadCommit = head.Hex()
	projected.Branch = branch
	projected.SourcePath = path
	projected.GitDirectory = facts.GitDirectory()
	projected.Dirty = facts.Dirty()
	return projected, nil
}
