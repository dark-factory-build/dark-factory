package daemon

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func (daemon *Daemon) knowledgeRepository(ctx context.Context, project kernel.ProjectID, raw string) (kernel.RepositoryID, error) {
	var repository kernel.ProjectRepository
	var found bool
	var err error
	if raw == "" {
		repository, found, err = daemon.store.DefaultProjectRepository(ctx, project)
	} else {
		id, e := browserID(raw, kernel.RepositoryIDFromBytes)
		if e != nil {
			return kernel.RepositoryID{}, e
		}
		repository, found, err = daemon.store.ProjectRepository(ctx, id)
	}
	if err != nil {
		return kernel.RepositoryID{}, err
	}
	if !found {
		return kernel.RepositoryID{}, kernel.ErrNotFound
	}
	if repository.ProjectID != project {
		return kernel.RepositoryID{}, kernel.ErrUnauthorized
	}
	return repository.ID, nil
}

// Check scope and promotion authority before creating any Git object or ref.
// The store repeats these checks in the transaction that publishes metadata.
func (daemon *Daemon) validateKnowledgeWrite(ctx context.Context, operator bool, digest kernel.AttemptDigest, spec kernel.NewContent, expected kernel.Revision) error {
	var err error
	if operator {
		err = daemon.store.ValidateKnowledgeWrite(ctx, spec, expected)
	} else {
		err = daemon.store.ValidateKnowledgeWriteForAttempt(ctx, digest, spec, expected)
	}
	if err != nil {
		return err
	}
	var metadata kernel.KnowledgeMetadata
	if json.Unmarshal([]byte(spec.SourceReferences), &metadata) != nil || len(metadata.Entities) == 0 {
		return nil
	}
	repositoryID := spec.RepositoryID
	if expected.Int64() > 0 {
		repository, found, e := daemon.store.ContentRepository(ctx, spec.ID, expected)
		if e != nil {
			return e
		}
		if !found {
			return kernel.ErrNotFound
		}
		repositoryID = repository.ID
	}
	if repositoryID == (kernel.RepositoryID{}) {
		repositoryID, err = daemon.knowledgeRepository(ctx, spec.ProjectID, "")
		if err != nil {
			return err
		}
	}
	repository, found, err := daemon.store.ProjectRepository(ctx, repositoryID)
	if err != nil {
		return err
	}
	if !found || repository.ProjectID != spec.ProjectID {
		return kernel.ErrUnauthorized
	}
	snapshot, err := daemon.repositoryTopology(ctx, repository)
	if err != nil {
		return err
	}
	for _, entity := range metadata.Entities {
		matched := false
		for _, node := range snapshot.Nodes {
			if entity == spec.ProjectID.String()+":"+node.ID {
				matched = true
				break
			}
		}
		// Historical entity links may survive removal. Only retain an already
		// validated link, never introduce a forged or foreign node as history.
		if !matched && expected.Int64() > 0 {
			previous, e := daemon.store.Content(ctx, spec.ID, expected.Int64())
			if e != nil {
				return e
			}
			var prior kernel.KnowledgeMetadata
			if json.Unmarshal([]byte(previous.SourceReferences), &prior) == nil {
				for _, held := range prior.Entities {
					if held == entity {
						matched = true
					}
				}
			}
		}
		if !matched || !strings.HasPrefix(entity, spec.ProjectID.String()+":") {
			return kernel.ErrInvalidValue
		}
	}
	return nil
}

// Revalidation is a read-time projection; historical document claims are immutable.
func (daemon *Daemon) knowledgeDTO(ctx context.Context, content kernel.ContentRevision) api.Content {
	result := contentDTO(content)
	if daemon == nil {
		return result
	}
	repository, found, err := daemon.store.ContentRepository(ctx, content.ID, content.Revision)
	if err != nil || !found {
		result.ProjectedStatus = "needs_revalidation"
		return result
	}
	result.RepositoryID = repository.ID.String()
	metadata, parseErr := kernel.ParseKnowledgeMetadata(content.SourceReferences)
	if parseErr != nil {
		return result
	}
	if metadata.SourceRevision == "" && metadata.Branch == "" && metadata.Environment == "" {
		result.ProjectedStatus = metadata.Status
		return result
	}
	snapshot, err := daemon.repositoryTopology(ctx, repository)
	if err != nil {
		result.ProjectedStatus = "needs_revalidation"
		return result
	}
	result.ProjectedStatus = daemon.knowledgeSourceStatus(ctx, content, repository, snapshot.SourceRevision)
	return result
}
