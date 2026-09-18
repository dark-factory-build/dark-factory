package daemon

import (
	"context"
	"strings"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
)

// BindProjectRepositoryGitHub is called only by explicit operator registration.
// The host learns a numeric ID from live broker authority, never caller input.
func (daemon *Daemon) BindProjectRepositoryGitHub(ctx context.Context, id kernel.RepositoryID) error {
	if daemon.github == nil {
		return maintainer.ErrUnavailable
	}
	repository, found, err := daemon.store.ProjectRepository(ctx, id)
	if err != nil {
		return err
	}
	if !found || !repository.Enabled {
		return kernel.ErrConflict
	}
	source, verified, err := daemon.store.RepositorySourceIdentity(ctx, id)
	if err != nil {
		return err
	}
	if !verified || source.PublicationRepository == "" {
		return kernel.ErrConflict
	}
	status, err := daemon.github.Status(ctx)
	if err != nil {
		return err
	}
	if status.State != "connected" {
		return maintainer.ErrDenied
	}
	for _, delegated := range status.Repositories {
		if delegated.RepositoryID > 0 && strings.EqualFold(delegated.Repository, source.PublicationRepository) {
			return daemon.store.BindRepositoryGitHubID(ctx, id, uint64(delegated.RepositoryID))
		}
	}
	return maintainer.ErrDenied
}
