package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/opgraph"
)

func TestProjectGraphKeepsUnregisteredCheckoutUnavailable(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeSourceFixture(t, root, "go.mod", "module example.com/project\n")
	writeSourceFixture(t, root, "one/one.go", "package one\n")
	initial, _ := kernel.NewUnixMillis(1)
	store, err := createTestStore(ctx, filepath.Join(t.TempDir(), "kernel.sqlite"), kernel.FactoryConfig{Capacity: 1}, initial)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	clock := time.Unix(1_750_000_000, 0)
	daemon, err := newDaemon(store, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := kernel.ProjectIDFromBytes(bytes.Repeat([]byte{0x51}, kernel.IDBytes))
	created, _ := kernel.NewUnixMillis(2)
	if _, err := store.CreateProject(ctx, kernel.NewProject{ID: projectID, Name: "graph", Root: root}, created); err != nil {
		t.Fatal(err)
	}
	before, err := store.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A cache directory cannot be resolved; unavailable source remains explicit.
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	if _, err := os.UserCacheDir(); err == nil {
		t.Fatal("fixture unexpectedly has a user cache directory")
	}
	first, err := daemon.ProjectGraph(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	writeSourceFixture(t, root, "two/two.go", "package two\n")
	// The unavailable source observation is cached without scanning working files.
	held, err := daemon.ProjectGraph(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if held.digest != first.digest {
		t.Fatal("graph re-read the tree inside its freshness window")
	}
	clock = clock.Add(graphFreshness)
	second, err := daemon.ProjectGraph(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.graph, second.graph) || second.sources[0].Kind != "unavailable" || second.sources[0].Revision != "" {
		t.Fatalf("unregistered checkout was presented as integrated source: %+v %+v", first.graph, second.sources)
	}
	after, err := store.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.Head != before.Head || after.Revision != before.Revision {
		t.Fatal("graph request mutated authoritative state")
	}
	missing, _ := kernel.ProjectIDFromBytes(bytes.Repeat([]byte{0x52}, kernel.IDBytes))
	if _, err := daemon.ProjectGraph(ctx, missing); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("missing project error = %v", err)
	}
}

func TestProjectGraphReadsOneSourcePerRepositoryAndInvalidatesConfiguration(t *testing.T) {
	ctx := context.Background()
	at, _ := kernel.NewUnixMillis(1)
	store, err := createTestStore(ctx, filepath.Join(t.TempDir(), "kernel.sqlite"), kernel.FactoryConfig{Capacity: 1}, at)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	daemon, err := newDaemon(store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := kernel.ProjectIDFromBytes(bytes.Repeat([]byte{0x61}, kernel.IDBytes))
	firstRoot, secondRoot := t.TempDir(), t.TempDir()
	for _, root := range []string{firstRoot, secondRoot} {
		writeSourceFixture(t, root, "src/code.go", "package src\n")
	}
	if _, err := store.CreateProject(ctx, kernel.NewProject{ID: id, Name: "two repositories", Root: firstRoot}, at); err != nil {
		t.Fatal(err)
	}
	before, err := daemon.ProjectGraph(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := kernel.RepositoryIDFromBytes(bytes.Repeat([]byte{0x62}, kernel.IDBytes))
	if _, err := store.AddProjectRepository(ctx, kernel.NewProjectRepository{ID: other, ProjectID: id, Name: "second", Root: secondRoot, BaseRef: "release"}, at); err != nil {
		t.Fatal(err)
	}
	after, err := daemon.ProjectGraph(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.digest == before.digest {
		t.Fatal("configuration change reused old cache")
	}
	if len(before.sources) != 1 || len(after.sources) != 2 {
		t.Fatalf("sources = %d then %d", len(before.sources), len(after.sources))
	}
}

// A node's paths are repository-prefixed only when more than one repository
// feeds the project, matching run and change paths.
func TestGraphFramePrefixesPathsOnlyForMultipleRepositories(t *testing.T) {
	project, first, second := strings.Repeat("a1", 16), strings.Repeat("b2", 16), strings.Repeat("c3", 16)
	files := map[string][]byte{"go.mod": []byte("module example.com/x\n"), "cmd/x/main.go": []byte(mainSource)}
	graph, err := opgraph.Infer(project, []opgraph.Repository{{ID: first, Name: "one", Files: files}})
	if err != nil || len(graph.Nodes) == 0 {
		t.Fatalf("graph = %+v, %v", graph, err)
	}
	sources := []graphSource{{RepositoryID: first, Name: "one", Kind: "integrated", Revision: strings.Repeat("d4", 20)}}
	for count, want := range map[int]string{1: "cmd/x", 2: first + "/cmd/x"} {
		if count == 2 {
			sources = append(sources, graphSource{RepositoryID: second, Name: "two", Kind: "unavailable"})
		}
		frame := graphFrame(project, projectGraph{graph: graph, sources: sources[:count], digest: strings.Repeat("ab", 32)}, opgraph.Overlay(project, graph, nil, nil, nil, 1, 1000), 1)
		if _, err := browserprotocol.EncodeOperationalGraph("graph", frame); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, node := range frame.Nodes {
			found = found || slices.Contains(node.Paths, want)
		}
		if !found {
			t.Fatalf("%d repositories: no path %q in %+v", count, want, frame.Nodes)
		}
	}
}

const mainSource = "package main\nimport \"net/http\"\nfunc main() { http.HandleFunc(\"GET /x\", nil) }\n"

func writeSourceFixture(t *testing.T, root, rel, body string) {
	t.Helper()
	name := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// At the bound the frame is measured once, trimmed in order and stays valid.
func TestGraphFrameFitsTheBoundQuicklyAndValidly(t *testing.T) {
	frame := browserprotocol.OperationalGraph{ProjectID: strings.Repeat("ab", 16), Digest: strings.Repeat("cd", 32), Sources: []browserprotocol.GraphSource{}}
	reading := func(node browserprotocol.GraphNode) browserprotocol.GraphNode {
		node.Evidence, node.Observation, node.State = "static", "unobserved", "unknown"
		return node
	}
	for unit := 0; unit < 64; unit++ {
		unitID := fmt.Sprintf("%032x", unit+1)
		frame.Nodes = append(frame.Nodes, reading(browserprotocol.GraphNode{ID: unitID, Kind: "processor", Label: "unit", Paths: []string{"cmd/x"}}))
		for leaf := 0; leaf < 63; leaf++ {
			paths := make([]string, 8)
			for index := range paths {
				paths[index] = strings.Repeat("p", 120)
			}
			frame.Nodes = append(frame.Nodes, reading(browserprotocol.GraphNode{ID: fmt.Sprintf("%032x", 100000+unit*100+leaf), Kind: "ingress", Label: strings.Repeat("l", 200), Unit: unitID, Paths: paths}))
		}
	}
	for index := 0; index < 6000; index++ {
		frame.Edges = append(frame.Edges, browserprotocol.GraphEdge{From: frame.Nodes[index%len(frame.Nodes)].ID, To: frame.Nodes[(index*7+1)%len(frame.Nodes)].ID, Kind: "calls", Evidence: "static", Observation: "unobserved", State: "unknown"})
	}
	started := time.Now()
	fitted := fitGraphFrame(frame)
	if took := time.Since(started); took > 5*time.Second {
		t.Fatalf("fitting took %v", took)
	}
	if _, err := browserprotocol.EncodeOperationalGraph("graph", fitted); err != nil {
		t.Fatalf("fitted frame does not encode: %v (%d nodes, %d edges)", err, len(fitted.Nodes), len(fitted.Edges))
	}
	units := 0
	for _, node := range fitted.Nodes {
		if node.Kind == "processor" {
			units++
		}
	}
	if units != 64 || fitted.Omitted == 0 && len(fitted.Nodes) != len(frame.Nodes) {
		t.Fatalf("%d halls kept, %d omitted: halls go last", units, fitted.Omitted)
	}
}

func TestPublicSecretIsWholeOrRefused(t *testing.T) {
	daemon := &Daemon{home: t.TempDir()}
	first, err := daemon.publicSecret()
	if err != nil || len(first) != 32 {
		t.Fatalf("new secret = %d bytes, %v", len(first), err)
	}
	if again, _ := daemon.publicSecret(); string(again) != string(first) {
		t.Fatal("secret changed between reads")
	}
	if err := os.WriteFile(filepath.Join(daemon.home, "public.key"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := daemon.publicSecret(); err == nil {
		t.Fatal("a damaged secret was used")
	}
}
