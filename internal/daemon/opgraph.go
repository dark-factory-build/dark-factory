package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/opgraph"
)

// graphFreshness bounds how stale a graph ProjectGraph serves may be, avoiding
// repeated archive reads inside this window. PlantGraph alone may serve an
// older one while its refresh runs.
const graphFreshness = 30 * time.Second

// graphSource records the configured integrated target each repository was
// read at, not its checkout HEAD.
type graphSource struct {
	RepositoryID string `json:"repository_id"`
	Name         string `json:"name"`
	Kind         string `json:"kind"` // integrated | unavailable
	TargetRef    string `json:"target_ref"`
	Revision     string `json:"revision"`
	ObservedAt   int64  `json:"observed_at"`
	Reason       string `json:"reason,omitempty"`
}

type projectGraph struct {
	graph   opgraph.Graph
	sources []graphSource
	digest  string
}

type graphSnapshot struct {
	value         projectGraph
	at            time.Time
	configuration string
}

// ProjectGraph returns the static Operational Graph of a project's
// repositories, regenerated from their integrated targets and never older
// than graphFreshness: knowledge writes are checked against it.
func (daemon *Daemon) ProjectGraph(ctx context.Context, projectID kernel.ProjectID) (projectGraph, error) {
	return daemon.projectGraph(ctx, projectID, false)
}

// PlantGraph is ProjectGraph for drawing the plant: once a graph exists, an
// older one is served at once while its refresh runs, so a build slower than
// a browser call never blanks the plant.
func (daemon *Daemon) PlantGraph(ctx context.Context, projectID kernel.ProjectID) (projectGraph, error) {
	return daemon.projectGraph(ctx, projectID, true)
}

func (daemon *Daemon) projectGraph(ctx context.Context, projectID kernel.ProjectID, stale bool) (projectGraph, error) {
	if daemon == nil || daemon.store == nil {
		return projectGraph{}, fmt.Errorf("%w: invalid daemon", kernel.ErrInvalidValue)
	}
	if _, found, err := daemon.store.Project(ctx, projectID); err != nil || !found {
		if err == nil {
			err = fmt.Errorf("%w: project %s", kernel.ErrNotFound, projectID.String())
		}
		return projectGraph{}, err
	}
	repositories, err := daemon.store.ProjectRepositories(ctx, projectID)
	if err != nil {
		return projectGraph{}, err
	}
	configurationBytes, err := json.Marshal(repositories)
	if err != nil {
		return projectGraph{}, err
	}
	configuration, now := string(configurationBytes), daemon.now()
	daemon.graphMu.Lock()
	held, ok := daemon.graphs[projectID]
	usable := ok && held.configuration == configuration
	if usable && !now.Before(held.at) && now.Sub(held.at) < graphFreshness {
		daemon.graphMu.Unlock()
		return held.value, nil
	}
	// Only a build of this configuration answers this caller.
	build := daemon.graphBuilds[projectID]
	if build == nil || build.configuration != configuration {
		build = &graphBuild{done: make(chan struct{}), configuration: configuration}
		if daemon.graphBuilds == nil {
			daemon.graphBuilds = make(map[kernel.ProjectID]*graphBuild)
		}
		daemon.graphBuilds[projectID] = build
		go daemon.buildProjectGraph(projectID, repositories, configuration, now, build)
	}
	daemon.graphMu.Unlock()
	if usable && stale {
		return held.value, nil
	}
	select {
	case <-build.done:
	case <-ctx.Done():
		return projectGraph{}, ctx.Err()
	}
	daemon.graphMu.Lock()
	held, ok = daemon.graphs[projectID]
	daemon.graphMu.Unlock()
	if build.err != nil || !ok || held.configuration != configuration {
		return projectGraph{}, errors.Join(build.err, errors.New("the project's graph changed while it was built"))
	}
	return held.value, nil
}

// graphBuild is one running graph build; err is set before done closes.
type graphBuild struct {
	done          chan struct{}
	configuration string
	err           error
}

// graphBuildLimit bounds one build. A build reads every repository's archive
// and infers from it, which can outlast any one caller, so it runs detached:
// a caller that gives up still leaves the graph to the next.
const graphBuildLimit = 5 * time.Minute

func (daemon *Daemon) buildProjectGraph(projectID kernel.ProjectID, repositories []kernel.ProjectRepository, configuration string, at time.Time, build *graphBuild) {
	ctx, cancel := context.WithTimeout(context.Background(), graphBuildLimit)
	defer cancel()
	result, err := daemon.inferProjectGraph(ctx, projectID, repositories)
	daemon.graphMu.Lock()
	if err == nil {
		if daemon.graphs == nil {
			daemon.graphs = make(map[kernel.ProjectID]graphSnapshot)
		}
		daemon.graphs[projectID] = graphSnapshot{value: result, at: at, configuration: configuration}
	}
	build.err = err
	if daemon.graphBuilds[projectID] == build {
		delete(daemon.graphBuilds, projectID)
	}
	daemon.graphMu.Unlock()
	close(build.done)
}

func (daemon *Daemon) inferProjectGraph(ctx context.Context, projectID kernel.ProjectID, repositories []kernel.ProjectRepository) (projectGraph, error) {
	var result projectGraph
	inputs := make([]opgraph.Repository, 0, len(repositories))
	for _, repository := range repositories {
		input, source, err := daemon.repositorySource(ctx, repository)
		if err != nil {
			return projectGraph{}, err
		}
		inputs = append(inputs, input)
		result.sources = append(result.sources, source)
	}
	// A system past the node bound is drawn as far as the bound allows,
	// never refused whole.
	var err error
	result.graph, err = opgraph.Infer(projectID.String(), inputs)
	if err != nil && !errors.Is(err, opgraph.ErrBounds) {
		return projectGraph{}, err
	}
	encoded, _ := json.Marshal(struct {
		Graph   opgraph.Graph
		Sources []graphSource
	}{result.graph, result.sources})
	digest := sha256.Sum256(encoded)
	result.digest = hex.EncodeToString(digest[:])
	return result, nil
}

// repositorySource reads one repository's integrated target archive. An
// unavailable target is reported, never replaced with the dirty checkout.
func (daemon *Daemon) repositorySource(ctx context.Context, repository kernel.ProjectRepository) (opgraph.Repository, graphSource, error) {
	input := opgraph.Repository{ID: repository.ID.String(), Name: repository.Name, Files: map[string][]byte{}}
	source := graphSource{RepositoryID: repository.ID.String(), Name: repository.Name, Kind: "unavailable", TargetRef: repository.BaseRef, ObservedAt: daemon.now().UnixMilli()}
	registered, found, err := daemon.store.RepositorySourceIdentity(ctx, repository.ID)
	if err != nil {
		return input, source, err
	}
	if !found {
		source.Reason = "Integrated target has no registered Git identity; repository source is unavailable."
		return input, source, nil
	}
	identity, err := observationIdentity(registered)
	if err != nil {
		return input, source, err
	}
	revision, archive, err := change.ArchiveSource(ctx, change.TrustedGitExecutable, repository.Root, repository.BaseRef, identity)
	if err == nil {
		files, readErr := opgraph.ReadArchive(ctx, archive)
		if readErr == nil {
			input.Files, input.Revision = files, revision
			source.Kind, source.Revision = "integrated", revision
			return input, source, nil
		}
	}
	source.Reason = "Integrated target is unavailable locally; refresh the registered repository target."
	return input, source, nil
}

// repositoryRevision is the integrated revision the current graph was read at.
func (daemon *Daemon) repositoryRevision(ctx context.Context, repository kernel.ProjectRepository) (string, error) {
	graph, err := daemon.ProjectGraph(ctx, repository.ProjectID)
	if err != nil {
		return "", err
	}
	for _, source := range graph.sources {
		if source.RepositoryID == repository.ID.String() {
			return source.Revision, nil
		}
	}
	return "", nil
}

// observe is factoryd watching itself: one aggregate per selector set and
// minute, folded into the same runtime store every adapter uses.
func (daemon *Daemon) observe(kind string, attributes, peer map[string]string, failed bool, took time.Duration) {
	if daemon == nil {
		return
	}
	attributes["service.name"] = "factoryd"
	now := daemon.now().UnixMilli()
	item := opgraph.Observation{Source: "factoryd", Environment: "local", Kind: kind, Start: now, End: now + 1,
		Attributes: attributes, Peer: peer, Count: 1, LatencyP95: float64(took.Milliseconds())}
	if failed {
		item.Errors = 1
	}
	daemon.runtimeStore().Record(item)
}

// observedTransport records each outbound request as a client span: the
// peer host, the method, failure (transport error or 5xx) and duration.
// URLs, paths, queries, headers and bodies are never recorded.
type observedTransport struct {
	daemon *Daemon
	next   http.RoundTripper
}

func (transport observedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	next := transport.next
	if next == nil {
		next = http.DefaultTransport
	}
	started := time.Now()
	response, err := next.RoundTrip(request)
	transport.daemon.observe("client", map[string]string{"http.request.method": request.Method},
		map[string]string{"server.address": strings.ToLower(request.URL.Hostname())}, err != nil || response.StatusCode >= 500, time.Since(started))
	return response, err
}

// observed returns a copy of client whose requests factoryd records.
func (daemon *Daemon) observed(client *http.Client) *http.Client {
	copied := *client
	copied.Transport = observedTransport{daemon: daemon, next: client.Transport}
	return &copied
}

func (daemon *Daemon) runtimeStore() *opgraph.Runtime {
	daemon.graphMu.Lock()
	defer daemon.graphMu.Unlock()
	if daemon.runtime == nil {
		daemon.runtime = opgraph.NewRuntime(time.Hour)
	}
	return daemon.runtime
}

// Self-observation claims exactly what factoryd instruments: its browser
// listener and routes, and its local API socket. Background loops and
// outbound calls are recorded when seen but not claimed, so their silence
// reads as partial or unobserved, never idle.
var selfCoverageKeys = []string{"service.name", "http.route", "url.path", "rpc.method", "network.transport", "server.address", "server.port"}

const runtimeWindow = 15 * time.Minute

// liveGraph overlays the runtime store on one project's static graph.
func (daemon *Daemon) liveGraph(projectID kernel.ProjectID, graph projectGraph) opgraph.Live {
	now := daemon.now().UnixMilli()
	store := daemon.runtimeStore()
	store.Cover(opgraph.Coverage{Source: "factoryd", Environment: "local", Unit: "factoryd", Keys: selfCoverageKeys, AsOf: now, TTL: runtimeWindow.Milliseconds()})
	sources := daemon.observeSources()
	daemon.pollSources(sources)
	aliases := map[string]string{}
	for _, source := range sources {
		for platform, unit := range source.Services {
			aliases[platform] = unit
		}
	}
	observations, coverage := store.Snapshot(now)
	return opgraph.Overlay(projectID.String(), graph.graph, observations, coverage, aliases, now, runtimeWindow.Milliseconds())
}

// observeSource is one pull adapter the operator configured in
// <home>/observe.json. Services maps a platform name (a Worker script) to
// the unit's service.name. The token sits in the file itself, inside the
// owner-only home like operator.token, and is never served.
type observeSource struct {
	Adapter     string            `json:"adapter"`
	Environment string            `json:"environment"`
	Account     string            `json:"account"`
	Token       string            `json:"token"`
	Services    map[string]string `json:"services"`
}

func (daemon *Daemon) observeSources() []observeSource { return daemon.observeConfig().Sources }

// observeConfig is <home>/observe.json. PublicProject opts one project's
// public world into publishing over the relay; absent, nothing is published.
type observeConfig struct {
	Sources       []observeSource `json:"sources"`
	PublicProject string          `json:"public_project"`
}

func (daemon *Daemon) observeConfig() (config observeConfig) {
	if daemon.home == "" {
		return config
	}
	body, err := os.ReadFile(filepath.Join(daemon.home, "observe.json"))
	if err != nil {
		return config
	}
	if err := json.Unmarshal(body, &config); err != nil {
		LogFactoryd(daemon.log, "factoryd: observe.json: %v\n", err)
		return observeConfig{}
	}
	return config
}

const pollInterval = 5 * time.Minute

// pollSources runs each configured pull adapter at most once per interval,
// in the background: a slow provider never delays the floor.
func (daemon *Daemon) pollSources(sources []observeSource) {
	now := daemon.now()
	daemon.graphMu.Lock()
	due := len(sources) > 0 && now.Sub(daemon.polled) >= pollInterval
	if due {
		daemon.polled = now
	}
	daemon.graphMu.Unlock()
	if !due {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		for _, source := range sources {
			if source.Adapter == "github" {
				continue // the production refresh reads GitHub deployments
			}
			if source.Token == "" || source.Adapter != "cloudflare" {
				LogFactoryd(daemon.log, "factoryd: observe %s: unsupported adapter or no token\n", source.Adapter)
				continue
			}
			scripts := make([]string, 0, len(source.Services))
			for script := range source.Services {
				scripts = append(scripts, script)
			}
			observations, coverage, err := opgraph.PullCloudflare(ctx, daemon.observed(observeClient), source.Account, strings.TrimSpace(source.Token), scripts, source.Environment, now, pollInterval)
			if err != nil {
				LogFactoryd(daemon.log, "factoryd: observe cloudflare: %v\n", err)
				continue
			}
			store := daemon.runtimeStore()
			for _, item := range observations {
				store.Record(item)
			}
			for _, item := range coverage {
				store.Cover(item)
			}
		}
	}()
}

var observeClient = &http.Client{Timeout: 30 * time.Second}

// ReceiveTraces folds an OTLP/HTTP JSON export into the runtime store.
func (backend *browserBackend) ReceiveTraces(body []byte, remote bool) error {
	if backend.owner == nil {
		return browser.ErrNotFound
	}
	observations, coverage, err := opgraph.DecodeOTLP(body, backend.owner.now().UnixMilli(), remote)
	if err != nil {
		return err
	}
	store := backend.owner.runtimeStore()
	for _, item := range observations {
		store.Record(item)
	}
	for _, item := range coverage {
		store.Cover(item)
	}
	return nil
}
