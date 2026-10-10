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
	"strings"
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
	graph, err := daemon.PlantGraph(ctx, projectID)
	if err != nil {
		return nil, err
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
	crates, ledger, err := daemon.publicWork(ctx, projectID)
	if err != nil {
		return nil, err
	}
	named, err := daemon.publicNames(ctx, projectID)
	if err != nil {
		return nil, err
	}
	world := opgraph.Public(daemon.liveGraph(projectID, graph), secret, workers, crates, named, daemon.now().UnixMilli())
	world.Ledger = ledger
	encoded, err := json.Marshal(world)
	// Past the relay's bound, the oldest merges are listed no more, then the
	// oldest open pull requests (their counts stay), then the ledger goes.
	for err == nil && len(encoded) > relayhost.MaxPublicWorldBytes && world.Ledger != nil {
		ledger := *world.Ledger
		world.Ledger = &ledger
		switch {
		case len(ledger.Merged) > 0:
			ledger.Merged = ledger.Merged[:len(ledger.Merged)/2]
		case len(ledger.Open) > 0:
			ledger.Open = ledger.Open[:len(ledger.Open)/2]
		default:
			world.Ledger = nil
		}
		encoded, err = json.Marshal(world)
	}
	return encoded, err
}

// publicFeedInterval is the least time between two publishes of the public
// world; the feed loop looks four times as often so a change waits at most
// an interval and a tick.
const publicFeedInterval = time.Minute

// publicFeed is what this run last handed the relay, and on which connection.
type publicFeed struct {
	sent []byte // nil until the first publish of this run
	on   any    // the relay connection it was queued on
	at   time.Time
}

// publicNames is the set of names that may be published: the IDs and
// lowercase owner/names of the project's repositories GitHub serves anonymously.
func (daemon *Daemon) publicNames(ctx context.Context, project kernel.ProjectID) (map[string]bool, error) {
	repositories, err := daemon.store.ProjectRepositories(ctx, project)
	if err != nil {
		return nil, err
	}
	ids, names := map[string]string{}, []string{}
	for _, repository := range repositories {
		identity, found, err := daemon.store.RepositorySourceIdentity(ctx, repository.ID)
		if err != nil {
			return nil, err
		}
		if name := strings.ToLower(identity.PublicationRepository); found && name != "" {
			ids[name] = repository.ID.String()
			names = append(names, name)
		}
	}
	named := map[string]bool{}
	for name, entry := range daemon.publicRepositories(names) {
		if entry.public {
			named[name], named[ids[name]] = true, true
		}
	}
	return named, nil
}

// publishPublicWorld publishes the PublicWorld bytes of the project named by
// public_project in observe.json, exactly as /v1/public serves them, at most
// once per interval and only when they changed. With no project configured it
// publishes an empty world once per run, which retracts whatever an earlier
// run left on the relay.
func (daemon *Daemon) publishPublicWorld(ctx context.Context, feed *publicFeed, publish func([]byte) any, connection func() any) {
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
	// A connection lost after the record was queued may have lost it too, so a
	// new connection is sent the current world, retraction included, again.
	if feed.sent != nil && bytes.Equal(world, feed.sent) && feed.on == connection() {
		return
	}
	if on := publish(world); on != nil {
		feed.sent, feed.on, feed.at = world, on, now
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
