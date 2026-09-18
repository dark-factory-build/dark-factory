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
	var observedAcceptance *kernel.IntakeAcceptance
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
		accepted, found, err := daemon.store.IntakeAcceptanceForTask(ctx, authority.TaskID)
		if err != nil {
			return failure("unavailable")
		}
		if found {
			if accepted.WithdrawnAt != nil {
				return failure("denied")
			}
			if params.Name == "list_issues" {
				return failure("accepted_snapshot_required")
			}
			if params.Name == "observe_issue" && strings.EqualFold(repository, accepted.SourceRepository) {
				var number uint64
				if json.Unmarshal(params.Arguments["issue_number"], &number) != nil || number != accepted.Snapshot.IssueNumber {
					return failure("denied")
				}
				observedAcceptance = &accepted
			}
			if params.Name == "create_pull_request" {
				var number uint64
				if json.Unmarshal(params.Arguments["issue_number"], &number) != nil || number != accepted.Snapshot.IssueNumber {
					return failure("denied")
				}
				if source == "" {
					source = repository
				}
				if !strings.EqualFold(source, accepted.SourceRepository) {
					return failure("denied")
				}
			}
			// Accepted work keeps its exact destination, including after a default
			// change or disabling the binding for new work.
			target, verified, readErr := daemon.store.RepositorySourceIdentity(ctx, accepted.RepositoryID)
			if readErr != nil || !verified || target.PublicationRepository == "" {
				return failure("repository_unbound")
			}
			id, pinned, readErr := daemon.store.RepositoryGitHubID(ctx, accepted.RepositoryID)
			if readErr != nil || !pinned {
				return failure("repository_unbound")
			}
			targets = map[string]uint64{strings.ToLower(target.PublicationRepository): id}
			sources = map[string]uint64{strings.ToLower(accepted.SourceRepository): accepted.Snapshot.GitHubRepositoryID}
		}
		name := strings.ToLower(repository)
		id := targets[name]
		if id == 0 {
			switch params.Name {
			case "list_issues", "observe_issue", "observe_operation":
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
	if observedAcceptance != nil && !observedAcceptedContent(response, *observedAcceptance) {
		return failure("accepted_snapshot_required")
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

// Never return revised issue text as execution instructions, including the MCP
// text duplicate. The attempt task already carries the reviewed snapshot.
func observedAcceptedContent(response json.RawMessage, accepted kernel.IntakeAcceptance) bool {
	var reply struct {
		Result struct {
			IsError bool `json:"isError"`
			Issue   *struct {
				Number uint64 `json:"number"`
				Title  string `json:"title"`
				Body   string `json:"body"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	if json.Unmarshal(response, &reply) != nil || reply.Result.IsError || reply.Result.Issue == nil {
		return false
	}
	issue := reply.Result.Issue
	return issue.Number == accepted.Snapshot.IssueNumber && issue.Title == accepted.Snapshot.Title && issue.Body == accepted.Snapshot.Body
}
