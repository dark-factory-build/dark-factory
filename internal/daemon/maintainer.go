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
			if accepted.Snapshot.LinearTeamID != "" {
				// A distinct broker method fails closed against older deployments.
				request.Method = "factory/tools/call_private"
			}
			if accepted.WithdrawnAt != nil {
				return failure("denied")
			}
			if params.Name == "list_issues" {
				return failure("accepted_snapshot_required")
			}
			if params.Name == "observe_issue" && accepted.Snapshot.LinearTeamID != "" {
				return failure("accepted_snapshot_required")
			}
			if params.Name == "observe_issue" {
				if !strings.EqualFold(repository, accepted.SourceRepository) {
					return failure("denied")
				}
				var number uint64
				if json.Unmarshal(params.Arguments["issue_number"], &number) != nil || number != accepted.Snapshot.IssueNumber {
					return failure("denied")
				}
				params.Arguments["repository"], _ = json.Marshal(accepted.SourceRepository)
				params.Arguments["issue_number"], _ = json.Marshal(number)
				request.Params, err = encodeMaintainerToolCall(params)
				if err != nil {
					return failure("invalid")
				}
				observedAcceptance = &accepted
			}
			if params.Name == "create_pull_request" && accepted.Snapshot.LinearTeamID != "" {
				// Source provenance comes from the accepted receipt, never the model.
				params.Arguments["external_source_url"], _ = json.Marshal(accepted.Snapshot.URL)
				params.Arguments["issue_number"] = json.RawMessage("0")
				params.Arguments["close_on_merge"] = json.RawMessage("false")
				delete(params.Arguments, "source_repository")
				source = ""
				request.Params, err = encodeMaintainerToolCall(params)
				if err != nil {
					return failure("invalid")
				}
			}
			if params.Name == "create_pull_request" && accepted.Snapshot.LinearTeamID == "" {
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
			sources = map[string]uint64{}
			if accepted.Snapshot.LinearTeamID == "" {
				sources[strings.ToLower(accepted.SourceRepository)] = accepted.Snapshot.GitHubRepositoryID
			}
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
	if daemon.github == nil || !daemon.github.CustomerMode() {
		return failure("unavailable")
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
	if observedAcceptance != nil {
		response, err = frozenAcceptedIssueResponse(request, *observedAcceptance, response)
		if err != nil {
			return failure("invalid")
		}
	}
	if err := daemon.recordMaintainerPublication(ctx, authority.ProjectID, authority.TaskID, request, response); err != nil {
		return failure("unavailable")
	}
	return api.NewContentReply(api.MaintainerResult{State: "ok", Response: response})
}

func (daemon *Daemon) recordMaintainerPublication(ctx context.Context, project kernel.ProjectID, task kernel.TaskID, request maintainerRequest, response json.RawMessage) error {
	if request.Method != "tools/call" && request.Method != "factory/tools/call_private" {
		return nil
	}
	params, _, err := decodeMaintainerToolCall(request.Params)
	if err != nil {
		return err
	}
	if params.Name == "submit_pull_request_review" {
		var repo, head, event, body string
		var number uint64
		for name, target := range map[string]any{"repository": &repo, "pull_number": &number, "head_sha": &head, "event": &event, "body": &body} {
			if err := json.Unmarshal(params.Arguments[name], target); err != nil {
				return err
			}
		}
		var reply struct {
			Result struct {
				IsError bool `json:"isError"`
				Review  struct {
					Head    string `json:"head_sha"`
					Verdict string `json:"verdict"`
				} `json:"structuredContent"`
			} `json:"result"`
		}
		if json.Unmarshal(response, &reply) != nil || reply.Result.IsError {
			return nil
		}
		state := "block"
		if event == "ALLOW" {
			state = "allow"
		}
		if reply.Result.Review.Head != head || reply.Result.Review.Verdict != state {
			return nil
		}
		at, err := daemon.timestamp()
		if err != nil {
			return err
		}
		return daemon.store.RecordProductionReview(ctx, project, repo, number, kernel.ProductionReview{Head: head, State: state, Findings: body}, at)
	}
	if params.Name != "create_pull_request" {
		return nil
	}
	var reply struct {
		Result struct {
			IsError bool `json:"isError"`
			Pull    struct {
				Number uint64 `json:"number"`
				URL    string `json:"url"`
				Head   string `json:"head_sha"`
				Base   string `json:"base_sha"`
				Body   string `json:"body"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	if json.Unmarshal(response, &reply) != nil || reply.Result.IsError || reply.Result.Pull.Number == 0 {
		return nil
	}
	var repo, title, branch, base string
	for name, target := range map[string]*string{"repository": &repo, "title": &title, "head": &branch, "base": &base} {
		if err := json.Unmarshal(params.Arguments[name], target); err != nil {
			return err
		}
	}
	at, err := daemon.timestamp()
	if err != nil {
		return err
	}
	if err := daemon.store.RecordPublication(ctx, project, task, repo, kernel.ProductionPullRequest{Number: reply.Result.Pull.Number, Title: title, URL: reply.Result.Pull.URL, Head: reply.Result.Pull.Head, Branch: branch, Base: base, State: "open", Review: kernel.ProductionReview{Head: reply.Result.Pull.Head, State: "unknown"}}, at); err != nil {
		return err
	}
	// The publication is durable before the independent review is launched.
	// Keep this transition in the daemon, so a corrected publication follows
	// the same exact-head path instead of the legacy host lane.
	if daemon.reviewPublished != nil {
		_, _ = daemon.reviewPublished(ctx, project, api.ReviewRequest{Repository: strings.ToLower(repo), PullNumber: reply.Result.Pull.Number, Head: strings.ToLower(reply.Result.Pull.Head), Base: strings.ToLower(reply.Result.Pull.Base), Body: reply.Result.Pull.Body, Provider: "codex"})
	} else if daemon.github != nil && daemon.cleanupCtx != nil {
		go func() {
			_, _ = daemon.reviewPublishedPR(daemon.cleanupCtx, project, repo, reply.Result.Pull.Number, reply.Result.Pull.Head)
		}()
	}
	return nil
}

// decodeMaintainerToolCall re-encodes the exact repository fields that local
// authorization checked. The API transport already rejects duplicate names.
func decodeMaintainerToolCall(encoded json.RawMessage) (maintainerToolCall, json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if json.Unmarshal(encoded, &raw) != nil || len(raw) != 2 || raw["name"] == nil || raw["arguments"] == nil {
		return maintainerToolCall{}, nil, errors.New("invalid tool call params")
	}
	var value maintainerToolCall
	if json.Unmarshal(raw["name"], &value.Name) != nil || value.Name == "" || json.Unmarshal(raw["arguments"], &value.Arguments) != nil || value.Arguments == nil {
		return maintainerToolCall{}, nil, errors.New("invalid tool call params")
	}
	for name := range value.Arguments {
		if (strings.EqualFold(name, "repository") || strings.EqualFold(name, "source_repository") || strings.EqualFold(name, "issue_number")) && name != "repository" && name != "source_repository" && name != "issue_number" {
			return maintainerToolCall{}, nil, fmt.Errorf("ambiguous tool argument %q", name)
		}
	}
	canonical, err := encodeMaintainerToolCall(value)
	return value, canonical, err
}

func encodeMaintainerToolCall(value maintainerToolCall) (json.RawMessage, error) {
	return json.Marshal(struct {
		Name      string                     `json:"name"`
		Arguments map[string]json.RawMessage `json:"arguments"`
	}{Name: value.Name, Arguments: value.Arguments})
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
		if source.Enabled && source.LinearTeamID == "" {
			sources[strings.ToLower(source.GitHubRepositoryName)] = source.GitHubRepositoryID
		}
	}
	return targets, sources, unbound, nil
}

// frozenAcceptedIssueResponse keeps reviewed instructions immutable while
// retaining only schema-validated current issue metadata from the broker.
func frozenAcceptedIssueResponse(request maintainerRequest, accepted kernel.IntakeAcceptance, response json.RawMessage) (json.RawMessage, error) {
	issue, err := currentAcceptedIssueMetadata(request, accepted, response)
	if err != nil {
		return nil, err
	}
	issue.Title = accepted.Snapshot.Title
	issue.Body = accepted.Snapshot.Body
	return json.Marshal(frozenIssueResponse{
		JSONRPC: request.JSONRPC,
		ID:      request.ID,
		Result: frozenIssueResult{
			Issue:   issue,
			Content: []mcpTextContent{{Type: "text", Text: "Accepted issue snapshot and current issue metadata were observed."}},
		},
	})
}

func currentAcceptedIssueMetadata(request maintainerRequest, accepted kernel.IntakeAcceptance, response json.RawMessage) (frozenIssue, error) {
	if !api.ValidMaintainerJSON(response) {
		return frozenIssue{}, errors.New("invalid observed issue response")
	}
	var live struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  struct {
			Issue   json.RawMessage  `json:"structuredContent"`
			Content []mcpTextContent `json:"content"`
			IsError *bool            `json:"isError"`
		} `json:"result"`
	}
	decoder := json.NewDecoder(bytes.NewReader(response))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&live); err != nil || decoder.Decode(&struct{}{}) != io.EOF || live.JSONRPC != "2.0" || !bytes.Equal(bytes.TrimSpace(live.ID), bytes.TrimSpace(request.ID)) || live.Result.IsError == nil || *live.Result.IsError || len(live.Result.Content) == 0 {
		return frozenIssue{}, errors.New("invalid observed issue response")
	}
	for _, content := range live.Result.Content {
		if content.Type != "text" || content.Text == "" {
			return frozenIssue{}, errors.New("invalid observed issue content")
		}
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(live.Result.Issue, &fields) != nil || len(fields) != 8 {
		return frozenIssue{}, errors.New("invalid observed issue fields")
	}
	for _, name := range []string{"number", "url", "title", "body", "labels", "updated_at", "state", "state_reason"} {
		if fields[name] == nil {
			return frozenIssue{}, errors.New("missing observed issue field")
		}
	}
	decoder = json.NewDecoder(bytes.NewReader(live.Result.Issue))
	decoder.DisallowUnknownFields()
	var issue frozenIssue
	if err := decoder.Decode(&issue); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return frozenIssue{}, errors.New("invalid observed issue metadata")
	}
	expectedURL := "https://github.com/" + accepted.SourceRepository + "/issues/" + fmt.Sprint(accepted.Snapshot.IssueNumber)
	if issue.Number != accepted.Snapshot.IssueNumber || issue.URL != expectedURL || issue.Title == "" || len(issue.Title) > 256 || len(issue.Body) > 30_000 || issue.Labels == nil || len(issue.Labels) > 100 || issue.State != "open" && issue.State != "closed" {
		return frozenIssue{}, errors.New("observed issue does not match acceptance")
	}
	if parsed, err := time.Parse(time.RFC3339, issue.UpdatedAt); err != nil || parsed.UTC().Format(time.RFC3339) != issue.UpdatedAt {
		return frozenIssue{}, errors.New("invalid observed issue timestamp")
	}
	for _, label := range issue.Labels {
		if label == "" || len(label) > 50 {
			return frozenIssue{}, errors.New("invalid observed issue label")
		}
	}
	return issue, nil
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
