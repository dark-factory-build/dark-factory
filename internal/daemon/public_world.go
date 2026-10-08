package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/opgraph"
	"github.com/dark-factory-build/dark-factory/internal/relayhost"
)

// PublicWorld serves the safe projection of one project's factory: the
// allowlist in opgraph.Public, keyed by this home's own secret.
func (backend *browserBackend) PublicWorld(ctx context.Context, rawProject string) ([]byte, error) {
	if backend.owner == nil {
		return nil, browser.ErrNotFound
	}
	projectID, err := decodeID(rawProject, kernel.ProjectIDFromBytes)
	if err != nil {
		return nil, browser.ErrNotFound
	}
	daemon := backend.owner
	graph, err := daemon.ProjectGraph(ctx, projectID)
	if err != nil {
		return nil, mapBrowserError(err)
	}
	secret, err := daemon.publicSecret()
	if err != nil {
		return nil, err
	}
	snapshot, err := daemon.store.ReadPublicSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	asking := map[kernel.AgentID]bool{}
	for _, request := range snapshot.HumanRequests {
		asking[request.AgentID] = true
	}
	var workers []opgraph.Worker
	for _, agent := range snapshot.Agents {
		if agent.ProjectID != projectID || agent.Archived {
			continue
		}
		worker := opgraph.Worker{Activity: "waiting"}
		if agent.Paused {
			worker.Activity = "idle"
		}
		for _, task := range snapshot.Tasks {
			if task.AssignedAgentID != agent.ID || task.Status != "running" {
				continue
			}
			worker.Activity = "busy"
			repository, found, err := daemon.store.TaskRepository(ctx, task.ID)
			if _, paths, pathErr := daemon.RunPaths(ctx, agent.ID); err == nil && found && pathErr == nil {
				for _, path := range paths {
					if worker.Unit = opgraph.Locate(graph.graph, repository.ID.String(), path); worker.Unit != "" {
						break
					}
				}
			}
		}
		if asking[agent.ID] {
			worker.Activity = "needs-you"
		}
		workers = append(workers, worker)
	}
	now := daemon.now().UnixMilli()
	return json.Marshal(opgraph.Public(daemon.liveGraph(projectID, graph), secret, workers, now))
}

// publicFeedInterval is the least time between two publishes of the public
// world; the feed loop looks four times as often so a change waits at most
// an interval and a tick.
const publicFeedInterval = time.Minute

// publicFeed is what the relay last accepted from this run.
type publicFeed struct {
	sent []byte // nil until the first publish of this run
	at   time.Time
}

// publishPublicWorld publishes the PublicWorld bytes of the project named by
// public_project in observe.json, exactly as /v1/public serves them, at most
// once per interval and only when they changed. With no project configured it
// publishes an empty world once per run, which retracts whatever an earlier
// run left on the relay.
func (daemon *Daemon) publishPublicWorld(ctx context.Context, feed *publicFeed, publish func([]byte) bool) {
	now := daemon.now()
	if now.Sub(feed.at) < publicFeedInterval {
		return
	}
	world := []byte{}
	if project := daemon.observeConfig().PublicProject; project != "" {
		var err error
		if world, err = (&browserBackend{owner: daemon}).PublicWorld(ctx, project); err == nil && len(world) > relayhost.MaxPublicWorldBytes {
			err = fmt.Errorf("%d bytes is past the relay's %d", len(world), relayhost.MaxPublicWorldBytes)
		}
		if err != nil {
			// Waits an interval, so a lasting fault logs once a minute.
			feed.at = now
			LogFactoryd(daemon.log, "factoryd: public world not published: %v\n", err)
			return
		}
	}
	if feed.sent != nil && bytes.Equal(world, feed.sent) {
		return
	}
	if publish(world) {
		feed.sent, feed.at = world, now
	}
}

// publicSecret keys public identities. It never leaves the home, so public
// IDs are stable for this factory and unlinkable to its private ones.
func (daemon *Daemon) publicSecret() ([]byte, error) {
	if daemon.home == "" {
		return nil, browser.ErrNotFound
	}
	path := filepath.Join(daemon.home, "public.key")
	secret, err := os.ReadFile(path)
	if err == nil {
		if len(secret) != 32 {
			return nil, fmt.Errorf("%s is damaged; remove it to start new public identities", path)
		}
		return secret, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	// Written whole, then linked into place: the key is never seen half-written,
	// and two first readers agree on whichever link landed first.
	secret = make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	temporary, err := os.CreateTemp(daemon.home, ".public.key-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(secret); err != nil {
		temporary.Close()
		return nil, err
	}
	if err := temporary.Close(); err != nil {
		return nil, err
	}
	if err := os.Link(temporary.Name(), path); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	return os.ReadFile(path)
}
