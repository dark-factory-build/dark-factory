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

func TestProjectTopologyUsesProjectRootAndRegenerableProjectCache(t *testing.T) {
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
	daemon, err := newDaemon(store, time.Now)
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
	cacheRoot := t.TempDir()
	first, err := daemon.projectTopology(ctx, projectID, cacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	cacheFile := filepath.Join(cacheRoot, "topology", projectID.String(), "snapshot.json")
	if _, err := os.Stat(cacheFile); err != nil {
		t.Fatalf("project-keyed cache: %v", err)
	}
	writeTopologyFixture(t, root, "two/two.go", "package two\n")
	second, err := daemon.projectTopology(ctx, projectID, cacheRoot)
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
	if _, err := daemon.projectTopology(ctx, missing, cacheRoot); !errors.Is(err, kernel.ErrNotFound) {
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
