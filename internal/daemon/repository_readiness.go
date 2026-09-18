package daemon

import (
	"context"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// RepositoryReadiness observes current binding state. Only an explicit fetch
// check runs Git, through the same sealed source-selection path as new work.
// Readiness is not durable authority and does not grant publication access.
func (daemon *Daemon) RepositoryReadiness(ctx context.Context, id kernel.RepositoryID, fetch bool) (api.ProjectRepository, error) {
	repository, found, err := daemon.store.ProjectRepository(ctx, id)
	if err != nil {
		return api.ProjectRepository{}, err
	}
	if !found {
		return api.ProjectRepository{}, kernel.ErrNotFound
	}
	result := repositoryDTO(repository)
	result.FetchState, result.PublicationState = "unchecked", "unbound"
	remoteID, pinned, err := daemon.store.RepositoryGitHubID(ctx, id)
	if err != nil {
		return api.ProjectRepository{}, err
	}
	if pinned {
		result.GitHubRepositoryID, result.PublicationState = remoteID, "unchecked"
	}
	if !fetch {
		return result, nil
	}
	source, verified, err := daemon.store.RepositorySourceIdentity(ctx, id)
	if err != nil {
		return api.ProjectRepository{}, err
	}
	result.FetchState = "setup_required"
	result.ReadinessMessage = "Check the registered checkout, configured base and repository-local Git authentication, then retry. GitHub delegation does not configure Git fetch credentials."
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !verified {
		// Explicit operator verification also materializes the legacy first
		// claim. Passive listing never learns or changes checkout identity.
		source, err = inspectRegisteredRepository(bounded, repository.Root, "")
		if err != nil {
			return result, nil
		}
		if err := daemon.store.BindRepositorySource(ctx, id, source); err != nil {
			return api.ProjectRepository{}, err
		}
	}
	root, rootErr := change.NewRepositoryIdentity(source.RootDevice, source.RootInode)
	git, gitErr := change.NewRepositoryIdentity(source.GitDevice, source.GitInode)
	if rootErr != nil || gitErr != nil {
		return api.ProjectRepository{}, kernel.ErrCorruptState
	}
	expected := change.RepositorySourceIdentity{Root: root, Git: git, OriginDigest: source.OriginDigest, PublicationRepository: source.PublicationRepository}
	if _, err := change.SelectRegisteredGit(bounded, change.TrustedGitExecutable, repository.Root, repository.BaseRef, expected); err != nil {
		return result, nil
	}
	result.FetchState, result.ReadinessMessage = "ready", "The configured source is available through this checkout's Git setup."
	return result, nil
}
