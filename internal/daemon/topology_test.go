//go:build darwin || linux

package daemon

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestProjectTopologyUsesMemoryFreshnessWithoutDiskCache(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeTopologyFixture(t, root, "go.mod", "module example.com/project\n")
	writeTopologyFixture(t, root, "one/one.go", "package one\n")
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
	if _, err := store.CreateProject(ctx, kernel.NewProject{ID: projectID, Name: "topology", Root: root}, created); err != nil {
		t.Fatal(err)
	}
	before, err := store.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A cache directory cannot be resolved, but topology remains available.
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	if _, err := os.UserCacheDir(); err == nil {
		t.Fatal("fixture unexpectedly has a user cache directory")
	}
	first, err := daemon.ProjectTopology(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	writeTopologyFixture(t, root, "two/two.go", "package two\n")
	// Inside the freshness window the walk is not repeated, so the answer is
	// the one already computed even though the tree moved underneath it.
	held, err := daemon.ProjectTopology(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if held.Digest != first.Digest {
		t.Fatal("topology re-walked the tree inside its freshness window")
	}
	clock = clock.Add(topologyFreshness)
	second, err := daemon.ProjectTopology(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Digest == first.Digest {
		t.Fatal("topology request did not regenerate after the project changed")
	}
	after, err := store.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.Head != before.Head || after.Revision != before.Revision {
		t.Fatal("topology request mutated authoritative state")
	}
	missing, _ := kernel.ProjectIDFromBytes(bytes.Repeat([]byte{0x52}, kernel.IDBytes))
	if _, err := daemon.ProjectTopology(ctx, missing); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("missing project error = %v", err)
	}
}

func writeTopologyFixture(t *testing.T, root, rel, body string) {
	t.Helper()
	name := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
