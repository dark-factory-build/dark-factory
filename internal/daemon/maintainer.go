package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
)

type maintainerRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func (daemon *Daemon) attemptMaintainer(ctx context.Context, call api.Call) api.Reply {
	failure := func(state string) api.Reply { return api.NewContentReply(api.MaintainerResult{State: state}) }
	digest, ok := call.AttemptDigest()
	if !ok {
		return failure("denied")
	}
	kDigest, err := attemptDigest(digest)
	if err != nil {
		return failure("denied")
	}
	authority, err := daemon.store.AuthenticateAttempt(ctx, kDigest)
	if err != nil || authority.Role != kernel.RoleOrchestrator {
		return failure("denied")
	}
	if daemon.liveAttemptForDigest(kDigest) == nil {
		return failure("denied")
	}
	if daemon.github == nil || !daemon.github.CustomerMode() {
		return failure("unavailable")
	}
	input, ok := call.MaintainerInput()
	var request maintainerRequest
	if !ok || json.Unmarshal(input.Request, &request) != nil || request.JSONRPC != "2.0" || len(request.ID) == 0 {
		return failure("invalid")
	}
	repositories := map[string]uint64{}
	switch request.Method {
	case "initialize", "ping", "tools/list":
	case "tools/call":
		var params struct {
			Name      string                     `json:"name"`
			Arguments map[string]json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(request.Params, &params) != nil {
			return failure("invalid")
		}
		var repository, source string
		if json.Unmarshal(params.Arguments["repository"], &repository) != nil || repository == "" {
			return failure("invalid")
		}
		if value, exists := params.Arguments["source_repository"]; exists {
			if json.Unmarshal(value, &source) != nil || source == "" {
				return failure("invalid")
			}
		}
		targets, sources, unbound, err := daemon.projectMaintainerRepositories(ctx, authority.ProjectID)
		if err != nil {
			return failure("unavailable")
		}
		name := strings.ToLower(repository)
		id := targets[name]
		if id == 0 {
			switch params.Name {
			case "list_issues", "observe_issue", "create_issue", "close_issue", "observe_operation":
				id = sources[name]
			}
		}
		if id == 0 {
			if unbound[name] {
				return failure("repository_unbound")
			}
			return failure("denied")
		}
		repositories[name] = id
		if source != "" {
			name = strings.ToLower(source)
			id = sources[name]
			if id == 0 {
				id = targets[name]
			}
			if id == 0 {
				if unbound[name] {
					return failure("repository_unbound")
				}
				return failure("denied")
			}
			repositories[name] = id
		}
	default:
		return failure("invalid")
	}
	// Re-encode the parsed envelope; never forward a second hidden method/ID.
	encoded, err := json.Marshal(request)
	if err != nil {
		return failure("invalid")
	}
	response, err := daemon.github.MCP(ctx, encoded, repositories)
	if err != nil {
		if errors.Is(err, maintainer.ErrDenied) {
			return failure("denied")
		}
		if errors.Is(err, maintainer.ErrInvalid) {
			return failure("invalid")
		}
		return failure("unavailable")
	}
	return api.NewContentReply(api.MaintainerResult{State: "ok", Response: response})
}

func (daemon *Daemon) projectMaintainerRepositories(ctx context.Context, project kernel.ProjectID) (map[string]uint64, map[string]uint64, map[string]bool, error) {
	targets, sources, unbound := map[string]uint64{}, map[string]uint64{}, map[string]bool{}
	repositories, err := daemon.store.ProjectRepositories(ctx, project)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, repository := range repositories {
		if !repository.Enabled {
			continue
		}
		source, verified, err := daemon.store.RepositorySourceIdentity(ctx, repository.ID)
		if err != nil {
			return nil, nil, nil, err
		}
		if !verified || source.PublicationRepository == "" {
			continue
		}
		id, pinned, err := daemon.store.RepositoryGitHubID(ctx, repository.ID)
		if err != nil {
			return nil, nil, nil, err
		}
		name := strings.ToLower(source.PublicationRepository)
		if !pinned {
			unbound[name] = true
			continue
		}
		targets[name] = id
	}
	intake, err := daemon.store.ProjectIntakeSources(ctx, project)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, source := range intake {
		if source.Enabled {
			sources[strings.ToLower(source.GitHubRepositoryName)] = source.GitHubRepositoryID
		}
	}
	return targets, sources, unbound, nil
}
