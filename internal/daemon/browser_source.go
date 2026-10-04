package daemon

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/topology"
)

func (backend *browserBackend) sourceFiles(ctx context.Context, project kernel.ProjectID, input browserContentInput) (any, error) {
	if backend.owner == nil || input.Limit == 0 || input.Limit > 32 {
		return nil, browser.ErrInvalidRequest
	}
	snapshot, err := backend.owner.ProjectTopology(ctx, project)
	if err != nil {
		return nil, mapBrowserError(err)
	}
	var selected *topology.Node
	for i := range snapshot.Nodes {
		if snapshot.Nodes[i].ID == input.ID {
			selected = &snapshot.Nodes[i]
			break
		}
	}
	if selected == nil {
		return nil, browser.ErrNotFound
	}
	revision := snapshot.SourceRevision
	for _, source := range snapshot.Sources {
		if source.Prefix == "" || selected.RelativePath == source.Prefix || strings.HasPrefix(selected.RelativePath, source.Prefix+"/") {
			revision = source.Revision
		}
	}
	if input.TestedSource == "" || input.TestedSource != revision {
		return nil, browser.ErrStale
	}
	files := []topology.File{}
	canonical, owned := topology.NodeForPath(snapshot, selected.RelativePath)
	if owned && canonical.ID == selected.ID {
		for _, file := range snapshot.Files {
			if path.Dir(file.Path) == selected.RelativePath {
				files = append(files, file)
			}
		}
	}
	total := len(files)
	start := min(int(input.Offset), total)
	end := min(start+int(input.Limit), total)
	next := 0
	if end < total {
		next = end
	}
	return struct {
		Revision   string          `json:"revision"`
		Files      []topology.File `json:"files"`
		Total      int             `json:"total"`
		NextOffset int             `json:"next_offset"`
	}{revision, files[start:end], total, next}, nil
}

func (backend *browserBackend) observeProductionSources(ctx context.Context, project kernel.ProjectID, records []kernel.ProductionRecord) {
	if backend.owner == nil {
		return
	}
	repositories, err := backend.store.ProjectRepositories(ctx, project)
	if err != nil {
		return
	}
	remaining := 56000
	for _, record := range records {
		remaining -= len(record.Document)
	}
	for index := range records {
		item := &records[index]
		if item.Kind != "construction" && item.Kind != "pull_request" {
			continue
		}
		var document map[string]json.RawMessage
		if json.Unmarshal(item.Document, &document) != nil {
			continue
		}
		field := func(key string) string { var value string; _ = json.Unmarshal(document[key], &value); return value }
		observed := change.SourceObservation{Kind: "unavailable", Paths: []change.SourcePath{}, ObservedAt: backend.now().UnixMilli(), Reason: "Exact source commits are unavailable locally."}
		var repository kernel.ProjectRepository
		var worktree, base, head string
		var identity change.RepositorySourceIdentity
		head = field("head")
		if strings.HasPrefix(item.VisualID, "change:") {
			id, e := decodeID(strings.TrimPrefix(item.VisualID, "change:"), kernel.ChangeIDFromBytes)
			if e == nil {
				value, found, e := backend.store.Change(ctx, id)
				if e == nil && found && value.ProjectID == project && value.Selection != nil {
					repository, e = backend.owner.repositoryForChange(ctx, value)
					if e == nil {
						base = hex.EncodeToString(value.Selection.Commit().Bytes())
						if parent := backend.owner.changeParent.Load(); parent != nil && *parent != "" {
							worktree = filepath.Join(*parent, value.ID.String())
						}
						if item.Kind == "construction" {
							head = ""
						}
					}
				}
			}
		}
		if item.Kind == "pull_request" && field("base_sha") != "" {
			base = field("base_sha")
		}
		if repository.ID == (kernel.RepositoryID{}) {
			for _, candidate := range repositories {
				identity, found, e := backend.store.RepositorySourceIdentity(ctx, candidate.ID)
				if e == nil && found && strings.EqualFold(identity.PublicationRepository, item.Repository) {
					repository = candidate
					break
				}
			}
		}
		if repository.ID != (kernel.RepositoryID{}) {
			registered, found, e := backend.store.RepositorySourceIdentity(ctx, repository.ID)
			if e == nil && found {
				expected, e := observationIdentity(registered)
				if e == nil {
					identity = expected
					// A PR's base must be an exact provider-observed SHA. Never substitute
					// today's target for its actual comparison base.
					if base == "" {
						base = field("base_sha")
					}
					observed.Base, observed.Head = base, head
					if base != "" && (head != "" || worktree != "") {
						source, e := change.ObserveSource(ctx, change.TrustedGitExecutable, repository.Root, worktree, base, head, expected)
						if e == nil {
							observed = source
							observed.ObservedAt = backend.now().UnixMilli()
						} else {
							observed.Reason = "Exact base/head or stable working-tree observation is unavailable."
						}
					} else {
						observed.Reason = "Exact comparison base has not been observed."
					}
				}
			}
		}
		enrichSourceObservation(ctx, repository, identity, &observed)
		if len(repositories) > 1 && repository.ID != (kernel.RepositoryID{}) {
			for i := range observed.Relationships {
				observed.Relationships[i].FromPath = path.Join(repository.ID.String(), observed.Relationships[i].FromPath)
				observed.Relationships[i].ToPath = path.Join(repository.ID.String(), observed.Relationships[i].ToPath)
			}
			for i := range observed.Paths {
				observed.Paths[i].Path = path.Join(repository.ID.String(), observed.Paths[i].Path)
				if observed.Paths[i].OldPath != "" {
					observed.Paths[i].OldPath = path.Join(repository.ID.String(), observed.Paths[i].OldPath)
				}
			}
		}
		budget := max(512, remaining-(len(records)-index-1)*512)
		for {
			encoded, _ := json.Marshal(observed)
			if len(encoded) <= budget || len(observed.Paths) == 0 && len(observed.Relationships) == 0 {
				document["source"] = encoded
				remaining -= len(encoded) + 16
				break
			}
			if len(observed.Relationships) > 0 {
				observed.Relationships = observed.Relationships[:len(observed.Relationships)-1]
				observed.RelationshipsOmitted++
			} else {
				observed.Paths = observed.Paths[:len(observed.Paths)-1]
				observed.Omitted++
			}
		}
		item.Document, _ = json.Marshal(document)
	}
}

func observationIdentity(source kernel.RepositorySourceIdentity) (change.RepositorySourceIdentity, error) {
	root, rootErr := change.NewRepositoryIdentity(source.RootDevice, source.RootInode)
	git, gitErr := change.NewRepositoryIdentity(source.GitDevice, source.GitInode)
	if rootErr != nil || gitErr != nil {
		return change.RepositorySourceIdentity{}, kernel.ErrCorruptState
	}
	return change.RepositorySourceIdentity{Root: root, Git: git, OriginDigest: source.OriginDigest, PublicationRepository: source.PublicationRepository}, nil
}

// Reuse the integrated scanner at the exact observed comparison revisions.
// Dirty snapshots keep file operations but cannot claim a committed dependency graph.
func enrichSourceObservation(ctx context.Context, repository kernel.ProjectRepository, identity change.RepositorySourceIdentity, observed *change.SourceObservation) {
	for i := range observed.Paths {
		observed.Paths[i].Resource = topology.Classify(observed.Paths[i].Path)
	}
	observed.Relationships = []change.SourceRelationship{}
	if observed.Kind != "committed" {
		observed.RelationshipsUnavailable = "Exact committed base/head relationships are unavailable."
		if observed.Kind == "working-tree" {
			observed.RelationshipsUnavailable = "Working-tree relationships are unavailable; committed dependencies do not include observed edits."
		}
		return
	}
	var graphs [2]map[string]uint32
	for i, revision := range []string{observed.Base, observed.Head} {
		resolved, archive, err := change.ArchiveSource(ctx, change.TrustedGitExecutable, repository.Root, revision, identity)
		if err != nil || resolved != revision {
			observed.RelationshipsUnavailable = "Exact base/head archives are unavailable for relationship comparison."
			return
		}
		snapshot, err := topology.BuildArchive(ctx, archive, repository.ID.String(), revision)
		if err != nil {
			observed.RelationshipsUnavailable = "Source exceeds the bounded static relationship scan or cannot be read."
			return
		}
		paths := make(map[string]string, len(snapshot.Nodes))
		for _, node := range snapshot.Nodes {
			paths[node.ID] = node.RelativePath
		}
		graphs[i] = make(map[string]uint32, len(snapshot.Edges))
		for _, edge := range snapshot.Edges {
			if edge.Kind != topology.EdgeImports {
				continue
			}
			graphs[i][paths[edge.From]+"\x00"+paths[edge.To]] += edge.Weight
		}
	}
	keys := make([]string, 0, len(graphs[0])+len(graphs[1]))
	for key := range graphs[0] {
		keys = append(keys, key)
	}
	for key := range graphs[1] {
		if _, exists := graphs[0][key]; !exists {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if graphs[0][key] == graphs[1][key] {
			continue
		}
		from, to, _ := strings.Cut(key, "\x00")
		for i, status := range []string{"removed", "added"} {
			if graphs[i][key] == 0 {
				continue
			}
			if len(observed.Relationships) < 32 {
				observed.Relationships = append(observed.Relationships, change.SourceRelationship{Status: status, FromPath: from, ToPath: to, Weight: graphs[i][key]})
			} else {
				observed.RelationshipsOmitted++
			}
		}
	}
}
