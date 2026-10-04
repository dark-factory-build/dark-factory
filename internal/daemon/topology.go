package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/change"
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
		snapshot, err = daemon.repositoryTopology(ctx, repositories[0])
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
			part, buildErr := daemon.repositoryTopology(ctx, repository)
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
			for index := range part.Files {
				part.Files[index].Path = path.Join(repository.ID.String(), part.Files[index].Path)
			}
			for index := range part.Sources {
				part.Sources[index].Prefix = repository.ID.String()
			}
			snapshot.Files = append(snapshot.Files, part.Files...)
			snapshot.Sources = append(snapshot.Sources, part.Sources...)
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

func (daemon *Daemon) repositoryTopology(ctx context.Context, repository kernel.ProjectRepository) (topology.Snapshot, error) {
	source := topology.Source{RepositoryID: repository.ID.String(), Kind: "unavailable", TargetRef: repository.BaseRef, ObservedAt: daemon.now().UnixMilli()}
	registered, found, err := daemon.store.RepositorySourceIdentity(ctx, repository.ID)
	if err != nil {
		return topology.Snapshot{}, err
	}
	if !found {
		source.Reason = "Integrated target has no registered Git identity; repository source is unavailable."
	} else {
		identity, err := observationIdentity(registered)
		if err != nil {
			return topology.Snapshot{}, err
		}
		revision, archive, err := change.ArchiveSource(ctx, change.TrustedGitExecutable, repository.Root, repository.BaseRef, identity)
		if err == nil {
			observed, buildErr := topology.BuildArchive(ctx, archive, repository.ID.String(), revision)
			if buildErr == nil {
				source.Kind, source.Revision = "integrated", revision
				observed.Sources = []topology.Source{source}
				return observed, nil
			}
		}
		source.Reason = "Integrated target is unavailable locally; refresh the registered repository target."
	}
	root := topology.Node{ID: fmt.Sprintf("%x", sha256.Sum256([]byte("unavailable:"+repository.ID.String()))), Kind: topology.NodeRepository, RelativePath: ".", Label: repository.Name, SizeBucket: "empty"}
	encoded, _ := json.Marshal(source)
	return topology.Snapshot{Digest: fmt.Sprintf("%x", sha256.Sum256(encoded)), Nodes: []topology.Node{root}, Edges: []topology.Edge{}, Sources: []topology.Source{source}}, nil
}
