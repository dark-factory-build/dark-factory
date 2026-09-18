package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/topology"
)

// topologyFreshness bounds how stale a served topology may be, avoiding
// repeated source walks inside this window.
const topologyFreshness = 30 * time.Second

type topologySnapshot struct {
	snapshot      topology.Snapshot
	at            time.Time
	configuration string
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
	repositories, err := daemon.store.ProjectRepositories(ctx, projectID)
	if err != nil {
		return topology.Snapshot{}, err
	}
	configurationBytes, err := json.Marshal(repositories)
	if err != nil {
		return topology.Snapshot{}, err
	}
	configuration := string(configurationBytes)
	now := daemon.now()
	if fresh, ok := daemon.freshTopology(projectID, configuration, now); ok {
		return fresh, nil
	}
	// ponytail: the walk is serialized only by each connection's walker, so
	// two connections can walk the same project at once. Add a per-project
	// build gate if that ever costs more than the duplicated walk.
	var snapshot topology.Snapshot
	if len(repositories) == 1 {
		snapshot, err = topology.Build(ctx, repositories[0].Root, repositories[0].ID.String())
		if err != nil {
			return topology.Snapshot{}, err
		}
	} else {
		// The project is a containment group; every checkout keeps a distinct
		// node namespace and repository-prefixed activity paths.
		groupDigest := sha256.Sum256([]byte("dark-factory/project-topology/" + projectID.String()))
		groupID := hex.EncodeToString(groupDigest[:])
		snapshot.Nodes = []topology.Node{{ID: groupID, Kind: topology.NodeRepository, RelativePath: ".", Label: project.Name, SizeBucket: "small"}}
		for _, repository := range repositories {
			part, buildErr := topology.Build(ctx, repository.Root, repository.ID.String())
			if buildErr != nil {
				return topology.Snapshot{}, buildErr
			}
			for index := range part.Nodes {
				node := &part.Nodes[index]
				node.RelativePath = path.Join(repository.ID.String(), node.RelativePath)
				if node.ParentID == "" {
					node.ParentID = groupID
					node.Label = repository.Name
				}
			}
			snapshot.Nodes = append(snapshot.Nodes, part.Nodes...)
			snapshot.Edges = append(snapshot.Edges, part.Edges...)
			if len(snapshot.Nodes) > 4096 || len(snapshot.Edges) > 16384 {
				return topology.Snapshot{}, topology.ErrBounds
			}
		}
		encoded, marshalErr := json.Marshal(snapshot)
		if marshalErr != nil {
			return topology.Snapshot{}, marshalErr
		}
		digest := sha256.Sum256(encoded)
		snapshot.Digest = hex.EncodeToString(digest[:])
	}
	daemon.topologyMu.Lock()
	if daemon.topologies == nil {
		daemon.topologies = make(map[kernel.ProjectID]topologySnapshot)
	}
	daemon.topologies[projectID] = topologySnapshot{snapshot: snapshot, at: now, configuration: configuration}
	daemon.topologyMu.Unlock()
	return snapshot, nil
}

func (daemon *Daemon) freshTopology(projectID kernel.ProjectID, configuration string, now time.Time) (topology.Snapshot, bool) {
	daemon.topologyMu.Lock()
	defer daemon.topologyMu.Unlock()
	held, ok := daemon.topologies[projectID]
	if !ok || held.configuration != configuration || now.Before(held.at) || now.Sub(held.at) >= topologyFreshness {
		return topology.Snapshot{}, false
	}
	return held.snapshot, true
}
