package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/topology"
)

// ProjectTopology returns the current regenerable topology for an existing
// project without adding it to durable or browser state.
func (daemon *Daemon) ProjectTopology(ctx context.Context, projectID kernel.ProjectID) (topology.Snapshot, error) {
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return topology.Snapshot{}, fmt.Errorf("resolve Dark Factory cache directory: %w", err)
	}
	return daemon.projectTopology(ctx, projectID, filepath.Join(cacheRoot, "dark-factory"))
}

func (daemon *Daemon) projectTopology(ctx context.Context, projectID kernel.ProjectID, cacheRoot string) (topology.Snapshot, error) {
	if daemon == nil || daemon.store == nil {
		return topology.Snapshot{}, fmt.Errorf("%w: invalid daemon", kernel.ErrInvalidValue)
	}
	project, found, err := daemon.store.Project(ctx, projectID)
	if err != nil {
		return topology.Snapshot{}, err
	}
	if !found {
		return topology.Snapshot{}, fmt.Errorf("%w: project %s", kernel.ErrNotFound, projectID.String())
	}
	cacheFile := filepath.Join(cacheRoot, "topology", projectID.String(), "snapshot.json")
	return topology.BuildCached(project.Root, cacheFile)
}
