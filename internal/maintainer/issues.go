package maintainer

import (
	"context"
	"encoding/json"
)

// Issue is remote display/content data, never acceptance authority.
type Issue struct {
	ID     int64  `json:"id"`
	NodeID string `json:"node_id"`
	Number uint64 `json:"number"`
	URL    string `json:"url"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Author struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"author"`
	Labels    []string `json:"labels"`
	State     string   `json:"state"`
	UpdatedAt string   `json:"updated_at"`
}
type IssuePage struct {
	RepositoryID uint64  `json:"repository_id"`
	Issues       []Issue `json:"issues"`
	NextPage     *uint32 `json:"next_page"`
}

func (host *Host) Issues(ctx context.Context, repository string, repositoryID uint64, page uint32, label string, number uint64) (IssuePage, error) {
	if repositoryID == 0 || page < 1 || page > 1000 || number != 0 && (page != 1 || label != "") {
		return IssuePage{}, ErrInvalid
	}
	arguments := map[string]any{"repository": repository, "page": page}
	if label != "" {
		arguments["label"] = label
	}
	if number != 0 {
		arguments["issue_number"] = number
	}
	request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "list_issues", "arguments": arguments}})
	if err != nil {
		return IssuePage{}, ErrInvalid
	}
	data, err := host.MCP(ctx, request, map[string]uint64{repository: repositoryID})
	if err != nil {
		return IssuePage{}, err
	}
	var reply struct {
		Result struct {
			IsError bool       `json:"isError"`
			Page    *IssuePage `json:"structuredContent"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &reply) != nil || len(reply.Error) != 0 || reply.Result.IsError || reply.Result.Page == nil {
		return IssuePage{}, ErrUnavailable
	}
	result := *reply.Result.Page
	if result.RepositoryID != repositoryID || result.Issues == nil || len(result.Issues) > 25 || result.NextPage != nil && (*result.NextPage <= page || *result.NextPage > 1000) {
		return IssuePage{}, ErrInvalid
	}
	for _, issue := range result.Issues {
		if issue.ID <= 0 || issue.Number == 0 || issue.NodeID == "" || (issue.State != "open" && issue.State != "closed") || number != 0 && issue.Number != number {
			return IssuePage{}, ErrInvalid
		}
	}
	if number != 0 && (len(result.Issues) != 1 || result.NextPage != nil) {
		return IssuePage{}, ErrInvalid
	}
	return result, nil
}
