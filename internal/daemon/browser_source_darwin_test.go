//go:build darwin

package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/opgraph"
)

// commitSourceFixture commits a Go main package at dir and returns the root.
func commitSourceFixture(t *testing.T, root, dir, source string) {
	t.Helper()
	git := change.TrustedGitExecutable
	writeSourceFixture(t, root, "go.mod", "module example.com/project\n")
	writeSourceFixture(t, root, dir+"/main.go", source)
	supervisorGit(t, git, "-C", root, "add", ".")
	supervisorGit(t, git, "-C", root, "commit", "-qm", "add "+dir)
}

func graphPaths(graph projectGraph) []string {
	var paths []string
	for _, node := range graph.graph.Nodes {
		for _, location := range append(append([]opgraph.Location{}, node.Modules...), node.Sources...) {
			paths = append(paths, location.Repository+":"+location.Path)
		}
	}
	return paths
}

func TestIntegratedGraphIgnoresProposedCheckout(t *testing.T) {
	ctx := context.Background()
	root := contentRepositoryFixture(t)
	fixture := newDispatchFixture(t)
	commitSourceFixture(t, root, "cmd/committed", mainSource)
	supervisorGit(t, change.TrustedGitExecutable, "-C", root, "branch", "integrated")
	source, err := inspectRegisteredRepository(ctx, root, "refs/heads/integrated")
	if err != nil {
		t.Fatal(err)
	}
	project := mustProjectID(t, testID(231))
	at, _ := kernel.NewUnixMillis(200)
	if _, err := fixture.store.CreateProject(ctx, kernel.NewProject{ID: project, Name: "source", Root: root, SourceIdentity: &source}, at); err != nil {
		t.Fatal(err)
	}
	revision, _ := kernel.NewRevision(1)
	if _, err := fixture.store.UpdateProjectRepositoryBase(ctx, kernel.RepositoryID(project), revision, "refs/heads/integrated", at); err != nil {
		t.Fatal(err)
	}
	writeSourceFixture(t, root, "cmd/unfinished/main.go", mainSource)
	observed, err := fixture.daemon.ProjectGraph(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	repository := project.String()
	if len(observed.sources) != 1 || observed.sources[0].Kind != "integrated" || observed.sources[0].Revision == "" {
		t.Fatalf("integrated source = %+v", observed.sources)
	}
	if paths := graphPaths(observed); !slices.Contains(paths, repository+":cmd/committed") || slices.Contains(paths, repository+":cmd/unfinished") {
		t.Fatalf("graph paths = %v", paths)
	}

	secondRoot := contentRepositoryFixture(t)
	commitSourceFixture(t, secondRoot, "cmd/second", mainSource)
	secondSource, err := inspectRegisteredRepository(ctx, secondRoot, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	secondID := kernel.RepositoryID(mustProjectID(t, testID(232)))
	if _, err := fixture.store.AddProjectRepository(ctx, kernel.NewProjectRepository{ID: secondID, ProjectID: project, Name: "second", Root: secondRoot, BaseRef: "HEAD", SourceIdentity: &secondSource}, at); err != nil {
		t.Fatal(err)
	}
	multi, err := fixture.daemon.ProjectGraph(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	paths := graphPaths(multi)
	if len(multi.sources) != 2 || multi.sources[1].Kind != "integrated" || !slices.Contains(paths, repository+":cmd/committed") || !slices.Contains(paths, secondID.String()+":cmd/second") {
		t.Fatalf("multi-repository graph = %+v %v", multi.sources, paths)
	}
}

// A caller that gives up does not stop the build: the graph is ready for the
// next caller, so a build slower than any one call still lands.
func TestAProjectGraphBuildOutlivesItsCaller(t *testing.T) {
	root := contentRepositoryFixture(t)
	fixture := newDispatchFixture(t)
	commitSourceFixture(t, root, "cmd/committed", mainSource)
	source, err := inspectRegisteredRepository(context.Background(), root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	project := mustProjectID(t, testID(233))
	at, _ := kernel.NewUnixMillis(200)
	if _, err := fixture.store.CreateProject(context.Background(), kernel.NewProject{ID: project, Name: "source", Root: root, SourceIdentity: &source}, at); err != nil {
		t.Fatal(err)
	}
	// Once the build has started, hold the lock it needs to finish and let the
	// caller leave: the caller cannot have been answered.
	caller, cancel := context.WithCancel(context.Background())
	answered := make(chan error, 1)
	go func() {
		_, err := fixture.daemon.ProjectGraph(caller, project)
		answered <- err
	}()
	for {
		fixture.daemon.graphMu.Lock()
		if fixture.daemon.graphBuilds[project] != nil {
			break
		}
		fixture.daemon.graphMu.Unlock()
		time.Sleep(time.Millisecond)
	}
	cancel()
	err = <-answered
	fixture.daemon.graphMu.Unlock()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a caller that left mid-build got %v", err)
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		fixture.daemon.graphMu.Lock()
		_, built := fixture.daemon.graphs[project]
		fixture.daemon.graphMu.Unlock()
		if built {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the build stopped with its caller")
		}
	}
}

// The browser listener reports itself: a route it served reads active, a
// route it never served reads quiet, and an unclaimed thing never reads idle.
func TestFactorydObservesItsOwnBrowserRoute(t *testing.T) {
	ctx := context.Background()
	root := contentRepositoryFixture(t)
	fixture := newDispatchFixture(t)
	commitSourceFixture(t, root, "cmd/factoryd", `package main
import ("net"; "net/http")
func main() {
	net.Listen("tcp", "127.0.0.1:43123")
	http.ListenAndServe("", http.HandlerFunc(serve))
}
func serve(w http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/browser" {
		return
	}
	if request.URL.Path == "/pair" {
		return
	}
}
`)
	source, err := inspectRegisteredRepository(ctx, root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	project := mustProjectID(t, testID(235))
	at, _ := kernel.NewUnixMillis(200)
	if _, err := fixture.store.CreateProject(ctx, kernel.NewProject{ID: project, Name: "self", Root: root, SourceIdentity: &source}, at); err != nil {
		t.Fatal(err)
	}
	revision, _ := kernel.NewRevision(1)
	if _, err := fixture.store.UpdateProjectRepositoryBase(ctx, kernel.RepositoryID(project), revision, "HEAD", at); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_750_000_000, 0)
	fixture.daemon.now = func() time.Time { return now }
	backend := &browserBackend{store: fixture.store, owner: fixture.daemon, now: fixture.daemon.now}
	backend.Observe(map[string]string{"url.path": "/browser"})
	graph, err := fixture.daemon.ProjectGraph(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	frame := graphFrame(project.String(), graph, fixture.daemon.liveGraph(project, graph), now.UnixMilli())
	reading := map[string]browserprotocol.GraphNode{}
	for _, node := range frame.Nodes {
		reading[node.Label] = node
		if (node.Kind == "job" || node.Evidence == "uncertain") && node.State == "idle" {
			t.Fatalf("unclaimed %s %q reads idle", node.Kind, node.Label)
		}
	}
	if got := reading["/browser"]; got.Observation != "observed" || got.State != "active" {
		t.Fatalf("/browser = %+v of %+v", got, frame.Nodes)
	}
	// Covered but never matched yet: partial, never idle.
	if got := reading["/pair"]; got.Observation != "partial" || got.State != "unknown" {
		t.Fatalf("/pair = %+v", got)
	}
	// The public projection of the same floor carries no private label and
	// keeps its identities stable across reads under this home's own secret.
	fixture.daemon.home = t.TempDir()
	first, err := backend.PublicWorld(ctx, project.String())
	if err != nil {
		t.Fatal(err)
	}
	second, _ := backend.PublicWorld(ctx, project.String())
	for _, private := range []string{"/browser", "/pair", "factoryd", "43123", project.String()} {
		if strings.Contains(string(first), private) {
			t.Fatalf("public world leaks %q: %s", private, first)
		}
	}
	if string(first) != string(second) || !strings.Contains(string(first), `"activity":`) {
		t.Fatalf("public world unstable or empty: %s", first)
	}
	if info, err := os.Stat(filepath.Join(fixture.daemon.home, "public.key")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("public secret = %v, %v", info, err)
	}
}
