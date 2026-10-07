package daemon

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"path"
	"path/filepath"
	"strings"

	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

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
		if len(repositories) > 1 && repository.ID != (kernel.RepositoryID{}) {
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
			if len(encoded) <= budget || len(observed.Paths) == 0 {
				document["source"] = encoded
				remaining -= len(encoded) + 16
				break
			}
			observed.Paths = observed.Paths[:len(observed.Paths)-1]
			observed.Omitted++
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
