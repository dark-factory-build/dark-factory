//go:build darwin

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	secondRoot := contentRepositoryFixture(t)
	if err := os.WriteFile(filepath.Join(secondRoot, "second.go"), []byte("package second\n"), 0600); err != nil {
		t.Fatal(err)
	}
	supervisorGit(t, change.TrustedGitExecutable, "-C", secondRoot, "add", "second.go")
	supervisorGit(t, change.TrustedGitExecutable, "-C", secondRoot, "commit", "-qm", "second repository")
	secondSource, err := inspectRegisteredRepository(ctx, secondRoot, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	secondID := kernel.RepositoryID(mustProjectID(t, testID(232)))
	if _, err := fixture.store.AddProjectRepository(ctx, kernel.NewProjectRepository{ID: secondID, ProjectID: project, Name: "second", Root: secondRoot, BaseRef: "HEAD", SourceIdentity: &secondSource}, at); err != nil {
		t.Fatal(err)
	}
	multi, err := fixture.daemon.ProjectTopology(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range multi.Sources {
		owner, ok := topology.NodeForPath(multi, source.Prefix+"/README.md")
		if !ok {
			t.Fatal("missing prefixed owner")
		}
		query := browserContentInput{ContentInput: api.ContentInput{ID: owner.ID}, TestedSource: source.Revision, Limit: 32}
		page, err := backend.sourceFiles(ctx, project, query)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(page)
		decoded.Files = nil
		json.Unmarshal(encoded, &decoded)
		if len(decoded.Files) == 0 || decoded.Files[0].Path != source.Prefix+"/README.md" {
			t.Fatalf("mixed source namespaces: %s", encoded)
		}
	}
	input.TestedSource = "outdated"
	if _, err := backend.sourceFiles(ctx, project, input); !errors.Is(err, browser.ErrStale) {
		t.Fatalf("stale query=%v", err)
	}
}

func TestProductionSourceRelationshipsCompareCommittedArchives(t *testing.T) {
	ctx := context.Background()
	root := contentRepositoryFixture(t)
	git := change.TrustedGitExecutable
	supervisorGit(t, git, "-C", root, "remote", "add", "origin", "https://github.com/example/relationships.git")
	for name, body := range map[string]string{
		"go.mod": "module example.com/relationships\n",
		"a/a.go": "package a\nimport _ \"example.com/relationships/b\"\n",
		"b/b.go": "package b\n", "c/c.go": "package c\n",
	} {
		writeTopologyFixture(t, root, name, body)
	}
	supervisorGit(t, git, "-C", root, "add", ".")
	supervisorGit(t, git, "-C", root, "commit", "-qm", "base dependency")
	base := strings.TrimSpace(supervisorGitOutput(t, git, "-C", root, "rev-parse", "HEAD"))
	writeTopologyFixture(t, root, "a/a.go", "package a\nimport _ \"example.com/relationships/c\"\n")
	for name, body := range map[string]string{"a/a_test.go": "package a\n", "docs/guide.md": "guide", "settings.yml": "enabled: true", "assets/logo.svg": "<svg/>"} {
		writeTopologyFixture(t, root, name, body)
	}
	supervisorGit(t, git, "-C", root, "add", ".")
	supervisorGit(t, git, "-C", root, "commit", "-qm", "proposed dependency")
	head := strings.TrimSpace(supervisorGitOutput(t, git, "-C", root, "rev-parse", "HEAD"))
	registered, err := inspectRegisteredRepository(ctx, root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := observationIdentity(registered)
	if err != nil {
		t.Fatal(err)
	}
	project := mustProjectID(t, testID(233))
	repository := kernel.ProjectRepository{ID: kernel.RepositoryID(project), ProjectID: project, Root: root}
	observed, err := change.ObserveSource(ctx, git, root, "", base, head, identity)
	if err != nil {
		t.Fatal(err)
	}
	// Neither uncommitted import edits nor a newer checkout head may leak into
	// the two explicitly observed archives.
	writeTopologyFixture(t, root, "a/a.go", "package a\nimport _ \"example.com/relationships/b\"\n")
	enrichSourceObservation(ctx, repository, identity, &observed)
	expected := []change.SourceRelationship{{Status: "removed", FromPath: "a", ToPath: "b", Weight: 1}, {Status: "added", FromPath: "a", ToPath: "c", Weight: 1}}
	if !reflect.DeepEqual(observed.Relationships, expected) || observed.RelationshipsUnavailable != "" || observed.RelationshipsOmitted != 0 {
		t.Fatalf("relationship comparison = %+v", observed)
	}
	resources := map[string]string{}
	for _, file := range observed.Paths {
		resources[file.Path] = file.Resource
	}
	for file, want := range map[string]string{"a/a.go": "source", "a/a_test.go": "tests", "docs/guide.md": "documentation", "settings.yml": "configuration", "assets/logo.svg": "assets"} {
		if resources[file] != want {
			t.Fatalf("resource %s=%s, want %s", file, resources[file], want)
		}
	}
	dirty := change.SourceObservation{Kind: "working-tree", Base: base, Head: head, Paths: observed.Paths}
	enrichSourceObservation(ctx, repository, identity, &dirty)
	if len(dirty.Relationships) != 0 || !strings.Contains(dirty.RelationshipsUnavailable, "Working-tree") {
		t.Fatalf("dirty relationships = %+v", dirty)
	}

	fixture := newDispatchFixture(t)
	at, _ := kernel.NewUnixMillis(200)
	if _, err := fixture.store.CreateProject(ctx, kernel.NewProject{ID: project, Name: "relationships", Root: root, SourceIdentity: &registered}, at); err != nil {
		t.Fatal(err)
	}
	secondRoot := contentRepositoryFixture(t)
	secondSource, err := inspectRegisteredRepository(ctx, secondRoot, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	secondID := kernel.RepositoryID(mustProjectID(t, testID(234)))
	if _, err := fixture.store.AddProjectRepository(ctx, kernel.NewProjectRepository{ID: secondID, ProjectID: project, Name: "other", Root: secondRoot, BaseRef: "HEAD", SourceIdentity: &secondSource}, at); err != nil {
		t.Fatal(err)
	}
	document, _ := json.Marshal(map[string]string{"base_sha": base, "head": head})
	records := []kernel.ProductionRecord{{Kind: "pull_request", Repository: "example/relationships", VisualID: "pr:1", Document: document}}
	backend := &browserBackend{store: fixture.store, owner: fixture.daemon, now: time.Now}
	backend.observeProductionSources(ctx, project, records)
	var decoded struct {
		Source change.SourceObservation `json:"source"`
	}
	if err := json.Unmarshal(records[0].Document, &decoded); err != nil {
		t.Fatal(err)
	}
	for i := range expected {
		expected[i].FromPath = project.String() + "/" + expected[i].FromPath
		expected[i].ToPath = project.String() + "/" + expected[i].ToPath
	}
	if !reflect.DeepEqual(decoded.Source.Relationships, expected) {
		t.Fatalf("prefixed relationships = %+v", decoded.Source)
	}
	for _, file := range decoded.Source.Paths {
		if !strings.HasPrefix(file.Path, project.String()+"/") || file.Resource == "" {
			t.Fatalf("unclassified or unscoped source = %+v", file)
		}
	}

	// Eight full production documents leave 8KB for source facts. Relationships
	// are trimmed before file operations, with omissions retained on the wire.
	payload := map[string]string{"base_sha": base, "head": head, "filler": ""}
	document, _ = json.Marshal(payload)
	payload["filler"] = strings.Repeat("x", 6000-len(document))
	document, _ = json.Marshal(payload)
	records = make([]kernel.ProductionRecord, 8)
	for i := range records {
		records[i] = kernel.ProductionRecord{Kind: "pull_request", Repository: "example/relationships", VisualID: "pr:" + strings.Repeat("x", i+1), Document: document}
	}
	backend.observeProductionSources(ctx, project, records)
	size, omitted := 0, 0
	for _, record := range records {
		size += len(record.Document)
		decoded.Source = change.SourceObservation{}
		if err := json.Unmarshal(record.Document, &decoded); err != nil {
			t.Fatal(err)
		}
		omitted += decoded.Source.RelationshipsOmitted
		if len(decoded.Source.Paths) < len(observed.Paths) && len(decoded.Source.Relationships) > 0 {
			t.Fatal("file operations trimmed before relationships")
		}
	}
	if size > 56000 || omitted == 0 {
		t.Fatalf("bounded source documents size=%d omitted relationships=%d", size, omitted)
	}

	imports := "package a\nimport (\n"
	for i := 0; i < 34; i++ {
		directory := fmt.Sprintf("p%02d", i)
		writeTopologyFixture(t, root, directory+"/source.go", "package "+directory+"\n")
		imports += "_ \"example.com/relationships/" + directory + "\"\n"
	}
	writeTopologyFixture(t, root, "a/a.go", imports+")\n")
	supervisorGit(t, git, "-C", root, "add", ".")
	supervisorGit(t, git, "-C", root, "commit", "-qm", "bounded relationship fanout")
	expanded := strings.TrimSpace(supervisorGitOutput(t, git, "-C", root, "rev-parse", "HEAD"))
	bounded, err := change.ObserveSource(ctx, git, root, "", head, expanded, identity)
	if err != nil {
		t.Fatal(err)
	}
	enrichSourceObservation(ctx, repository, identity, &bounded)
	if len(bounded.Relationships) != 32 || bounded.RelationshipsOmitted != 3 || bounded.RelationshipsUnavailable != "" {
		t.Fatalf("bounded relationships = %+v", bounded)
	}
}
