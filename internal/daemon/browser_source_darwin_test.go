//go:build darwin

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/topology"
)

func TestIntegratedTopologyAndInventoryIgnoreProposedCheckout(t *testing.T) {
	ctx := context.Background()
	root := contentRepositoryFixture(t)
	fixture := newDispatchFixture(t)
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
	if err := os.WriteFile(filepath.Join(root, "unfinished.go"), []byte("package unfinished\n"), 0600); err != nil {
		t.Fatal(err)
	}
	observed, err := fixture.daemon.ProjectTopology(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	if len(observed.Files) != 1 || observed.Sources[0].Kind != "integrated" || observed.SourceRevision == "" {
		t.Fatalf("integrated source = %+v", observed)
	}
	node, ok := topology.NodeForPath(observed, "README.md")
	if !ok {
		t.Fatal("missing owner")
	}
	backend := &browserBackend{store: fixture.store, owner: fixture.daemon, now: func() time.Time { return time.UnixMilli(1000) }}
	input := browserContentInput{ContentInput: api.ContentInput{ID: node.ID}, TestedSource: observed.SourceRevision, Limit: 32}
	page, err := backend.sourceFiles(ctx, project, input)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(page)
	var decoded struct {
		Files []topology.File `json:"files"`
		Total int             `json:"total"`
	}
	json.Unmarshal(encoded, &decoded)
	if decoded.Total != 1 || decoded.Files[0].Path != "README.md" {
		t.Fatalf("source inventory=%s", encoded)
	}
	input.TestedSource = "outdated"
	if _, err := backend.sourceFiles(ctx, project, input); !errors.Is(err, browser.ErrStale) {
		t.Fatalf("stale query=%v", err)
	}
}
