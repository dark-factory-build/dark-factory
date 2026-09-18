package daemon

import (
	"context"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func (daemon *Daemon) repositoryForChange(ctx context.Context, value kernel.Change) (kernel.ProjectRepository, error) {
	readRepository := daemon.store.TaskRepository
	if daemon.successSourceRepository != nil {
		readRepository = daemon.successSourceRepository
	}
	repository, found, err := readRepository(ctx, value.TaskID)
	if err != nil || !found {
		if err == nil {
			err = kernel.ErrCorruptState
		}
		return kernel.ProjectRepository{}, err
	}
	if repository.ProjectID != value.ProjectID {
		return kernel.ProjectRepository{}, kernel.ErrCorruptState
	}
	return repository, nil
}

func registerProjectRepository(ctx context.Context, store *kernel.Store, spec kernel.NewProjectRepository, at kernel.UnixMillis) (kernel.ProjectRepository, error) {
	source, err := inspectRegisteredRepository(ctx, spec.Root, spec.BaseRef)
	if err != nil {
		return kernel.ProjectRepository{}, err
	}
	spec.SourceIdentity = &source
	return store.AddProjectRepository(ctx, spec, at)
}

func registerProject(ctx context.Context, store *kernel.Store, spec kernel.NewProject, at kernel.UnixMillis) (kernel.Project, error) {
	var base string
	current, found, err := store.DefaultProjectRepository(ctx, spec.ID)
	if err != nil {
		return kernel.Project{}, err
	}
	if found {
		base = current.BaseRef
	} else {
		base, err = store.InitialRepositoryBase(ctx)
		if err != nil {
			return kernel.Project{}, err
		}
	}
	source, err := inspectRegisteredRepository(ctx, spec.Root, base)
	if err != nil {
		return kernel.Project{}, err
	}
	spec.SourceIdentity = &source
	return store.CreateProject(ctx, spec, at)
}

func updateRepositoryBase(ctx context.Context, store *kernel.Store, id kernel.RepositoryID, expected kernel.Revision, base string, at kernel.UnixMillis) (kernel.ProjectRepository, error) {
	repository, found, err := store.ProjectRepository(ctx, id)
	if err != nil {
		return kernel.ProjectRepository{}, err
	}
	if !found {
		return kernel.ProjectRepository{}, kernel.ErrNotFound
	}
	source, err := inspectRegisteredRepository(ctx, repository.Root, base)
	if err != nil {
		return kernel.ProjectRepository{}, err
	}
	if err := store.BindRepositorySource(ctx, id, source); err != nil {
		return kernel.ProjectRepository{}, err
	}
	return store.UpdateProjectRepositoryBase(ctx, id, expected, base, at)
}
