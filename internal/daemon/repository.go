package daemon

import (
	"context"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func (daemon *Daemon) repositoryForChange(ctx context.Context, value kernel.Change) (kernel.ProjectRepository, error) {
	repository, found, err := daemon.store.TaskRepository(ctx, value.TaskID)
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
