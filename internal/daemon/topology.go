package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/topology"
)

// topologyFreshness bounds how stale a served topology may be, avoiding
// repeated source walks inside this window.
const topologyFreshness = 30 * time.Second

type topologySnapshot struct {
	snapshot topology.Snapshot
	at       time.Time
}

// ProjectTopology returns the current regenerable topology for an existing
// project without adding it to durable or browser state.
func (daemon *Daemon) ProjectTopology(ctx context.Context, projectID kernel.ProjectID) (topology.Snapshot, error) {
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
	now := daemon.now()
	if fresh, ok := daemon.freshTopology(projectID, now); ok {
		return fresh, nil
	}
	// ponytail: the walk is serialized only by each connection's walker, so
	// two connections can walk the same project at once. Add a per-project
	// build gate if that ever costs more than the duplicated walk.
	snapshot, err := topology.Build(ctx, project.Root, projectID.String())
	if err != nil {
		return topology.Snapshot{}, err
	}
	daemon.topologyMu.Lock()
	if daemon.topologies == nil {
		daemon.topologies = make(map[kernel.ProjectID]topologySnapshot)
	}
	daemon.topologies[projectID] = topologySnapshot{snapshot: snapshot, at: now}
	daemon.topologyMu.Unlock()
	return snapshot, nil
}

func (daemon *Daemon) freshTopology(projectID kernel.ProjectID, now time.Time) (topology.Snapshot, bool) {
	daemon.topologyMu.Lock()
	defer daemon.topologyMu.Unlock()
	held, ok := daemon.topologies[projectID]
	if !ok || now.Before(held.at) || now.Sub(held.at) >= topologyFreshness {
		return topology.Snapshot{}, false
	}
	return held.snapshot, true
}
