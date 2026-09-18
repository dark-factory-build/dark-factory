package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

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

type maintainerToolCall struct {
	Name      string
	Arguments map[string]json.RawMessage
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
		params, encoded, err := decodeMaintainerToolCall(request.Params)
		if err != nil {
			return failure("invalid")
		}
		request.Params = encoded
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
	if observedAcceptance != nil {
		if err := daemon.github.AuthorizeRepositories(ctx, repositories); err != nil {
			if errors.Is(err, maintainer.ErrDenied) {
				return failure("denied")
			}
			if errors.Is(err, maintainer.ErrInvalid) {
				return failure("invalid")
			}
			return failure("unavailable")
		}
		response, err := frozenAcceptedIssueResponse(request, *observedAcceptance)
		if err != nil {
			return failure("invalid")
		}
		return api.NewContentReply(api.MaintainerResult{State: "ok", Response: response})
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

// decodeMaintainerToolCall rejects case and duplicate ambiguities before the
// broker receives a canonical copy of the same arguments that authorization
// checked. encoding/json accepts both forms, but the bridge cannot.
func decodeMaintainerToolCall(encoded json.RawMessage) (maintainerToolCall, json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return maintainerToolCall{}, nil, errors.New("tool call params must be an object")
	}
	var value maintainerToolCall
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || seen[name] {
			return maintainerToolCall{}, nil, errors.New("ambiguous tool call params")
		}
		seen[name] = true
		switch name {
		case "name":
			if err := decoder.Decode(&value.Name); err != nil || value.Name == "" {
				return maintainerToolCall{}, nil, errors.New("invalid tool name")
			}
		case "arguments":
			arguments, err := decodeMaintainerArguments(decoder)
			if err != nil {
				return maintainerToolCall{}, nil, err
			}
			value.Arguments = arguments
		default:
			return maintainerToolCall{}, nil, fmt.Errorf("unknown tool call parameter %q", name)
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') || decoder.Decode(&struct{}{}) != io.EOF || !seen["name"] || !seen["arguments"] {
		return maintainerToolCall{}, nil, errors.New("invalid tool call params")
	}
	canonical, err := json.Marshal(struct {
		Name      string                     `json:"name"`
		Arguments map[string]json.RawMessage `json:"arguments"`
	}{Name: value.Name, Arguments: value.Arguments})
	if err != nil {
		return maintainerToolCall{}, nil, err
	}
	return value, canonical, nil
}

func decodeMaintainerArguments(decoder *json.Decoder) (map[string]json.RawMessage, error) {
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("tool arguments must be an object")
	}
	arguments := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || arguments[name] != nil {
			return nil, errors.New("ambiguous tool argument")
		}
		if (strings.EqualFold(name, "repository") || strings.EqualFold(name, "source_repository") || strings.EqualFold(name, "issue_number")) && name != "repository" && name != "source_repository" && name != "issue_number" {
			return nil, fmt.Errorf("ambiguous tool argument %q", name)
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
		if err := validateMaintainerJSON(raw); err != nil {
			return nil, err
		}
		arguments[name] = raw
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, errors.New("unterminated tool arguments")
	}
	return arguments, nil
}

// validateMaintainerJSON follows the local protocol's duplicate-name rule for
// nested tool arguments as well. A raw value is forwarded unchanged only after
// every object in it has one spelling for each member.
func validateMaintainerJSON(encoded json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := scanMaintainerJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing tool argument JSON")
	}
	return nil
}

func scanMaintainerJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("tool argument nesting too deep")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		names := map[string]bool{}
		for decoder.More() {
			token, err := decoder.Token()
			name, ok := token.(string)
			if err != nil || !ok || names[name] {
				return errors.New("ambiguous nested tool argument")
			}
			names[name] = true
			if err := scanMaintainerJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
			return errors.New("unterminated nested tool argument")
		}
	case '[':
		for decoder.More() {
			if err := scanMaintainerJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
			return errors.New("unterminated nested tool argument")
		}
	default:
		return errors.New("invalid tool argument JSON")
	}
	return nil
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

// frozenAcceptedIssueResponse never asks the broker to fetch an accepted
// issue. The attempt receives only the immutable reviewed snapshot and the
// schema-required metadata derived from that receipt.
func frozenAcceptedIssueResponse(request maintainerRequest, accepted kernel.IntakeAcceptance) (json.RawMessage, error) {
	return json.Marshal(frozenIssueResponse{
		JSONRPC: request.JSONRPC,
		ID:      request.ID,
		Result: frozenIssueResult{
			Issue: frozenIssue{
				Number:      accepted.Snapshot.IssueNumber,
				URL:         "https://github.com/" + accepted.SourceRepository + "/issues/" + fmt.Sprint(accepted.Snapshot.IssueNumber),
				Title:       accepted.Snapshot.Title,
				Body:        accepted.Snapshot.Body,
				Labels:      []string{},
				UpdatedAt:   time.UnixMilli(accepted.CreatedAt.Int64()).UTC().Format(time.RFC3339),
				State:       "open",
				StateReason: nil,
			},
			Content: []mcpTextContent{{Type: "text", Text: "Issue state was observed."}},
		},
	})
}

type frozenIssueResponse struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      json.RawMessage   `json:"id"`
	Result  frozenIssueResult `json:"result"`
}

type frozenIssueResult struct {
	Issue   frozenIssue      `json:"structuredContent"`
	Content []mcpTextContent `json:"content"`
	IsError bool             `json:"isError"`
}

type mcpTextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type frozenIssue struct {
	Number      uint64   `json:"number"`
	URL         string   `json:"url"`
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	Labels      []string `json:"labels"`
	UpdatedAt   string   `json:"updated_at"`
	State       string   `json:"state"`
	StateReason *string  `json:"state_reason"`
}
