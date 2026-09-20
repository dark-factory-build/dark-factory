// Package linear reads backlog data. It never publishes code or admits work.
package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
)

var ErrUnavailable = errors.New("Linear unavailable; check the connection")

type Team struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Key  string `json:"key"`
}
type Host struct {
	mu     sync.Mutex
	home   *install.OperationalHome
	key    string
	client *http.Client
}

func Open(home *install.OperationalHome) (*Host, error) {
	data, err := home.ReadLinearCredential()
	if err != nil {
		return nil, err
	}
	h := &Host{home: home, client: &http.Client{Timeout: 25 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if len(data) > 0 && json.Unmarshal(data, &h.key) != nil {
		return nil, ErrUnavailable
	}
	return h, nil
}
func (h *Host) Connect(ctx context.Context, key string) ([]Team, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(key) < 10 || len(key) > 512 {
		return nil, ErrUnavailable
	}
	teams, err := h.teams(ctx, key)
	if err != nil {
		return nil, err
	}
	data, _ := json.Marshal(key)
	if err = h.home.WriteLinearCredential(data); err != nil {
		return nil, err
	}
	h.key = key
	return teams, nil
}
func (h *Host) Disconnect() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.home.WriteLinearCredential([]byte(`""`)); err != nil {
		return err
	}
	h.key = ""
	return nil
}
func (h *Host) Teams(ctx context.Context) ([]Team, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.teams(ctx, h.key)
}
func (h *Host) teams(ctx context.Context, key string) ([]Team, error) {
	var data struct {
		Teams struct {
			Nodes    []Team
			PageInfo struct{ HasNextPage bool }
		}
	}
	if err := h.query(ctx, key, `query { teams(first:100) { nodes { id name key } pageInfo { hasNextPage } } }`, nil, &data); err != nil {
		return nil, err
	}
	if data.Teams.Nodes == nil || len(data.Teams.Nodes) > 100 || data.Teams.PageInfo.HasNextPage {
		return nil, ErrUnavailable
	}
	for _, t := range data.Teams.Nodes {
		if !kernel.ValidLinearID(t.ID) || len(t.Name) < 1 || len(t.Name) > 140 || len(t.Key) < 1 || len(t.Key) > 32 {
			return nil, ErrUnavailable
		}
	}
	return data.Teams.Nodes, nil
}
func (h *Host) query(ctx context.Context, key, query string, variables any, out any) error {
	if key == "" {
		return ErrUnavailable
	}
	body, _ := json.Marshal(map[string]any{"query": query, "variables": variables})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.linear.app/graphql", bytes.NewReader(body))
	if err != nil {
		return ErrUnavailable
	}
	req.Header.Set("Authorization", key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 || resp.StatusCode != 200 {
		return ErrUnavailable
	}
	var result struct {
		Data   json.RawMessage
		Errors []json.RawMessage
	}
	if json.Unmarshal(data, &result) != nil || len(result.Errors) > 0 || len(result.Data) == 0 || string(result.Data) == "null" || json.Unmarshal(result.Data, out) != nil {
		return ErrUnavailable
	}
	return nil
}
func (h *Host) Issues(ctx context.Context, team string, page uint32, label string, number uint64) (maintainer.IssuePage, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !kernel.ValidLinearID(team) || page < 1 || page > 40 || number > 9007199254740991 {
		return maintainer.IssuePage{}, ErrUnavailable
	}
	filter := map[string]any{"team": map[string]any{"id": map[string]any{"eq": team}}}
	if number != 0 {
		filter["number"] = map[string]any{"eq": number}
	} else {
		filter["state"] = map[string]any{"type": map[string]any{"nin": []string{"completed", "canceled"}}}
	}
	if label != "" {
		filter["labels"] = map[string]any{"name": map[string]any{"eq": label}}
	}
	// ponytail: the shared controller has numeric pages. Replay at most 40 cursor
	// pages (1,000 issues); replace with persisted opaque cursors for larger feeds.
	var after *string
	for current := uint32(1); current <= page; current++ {
		var data struct {
			Issues struct {
				Nodes []struct {
					ID, Title, Description, URL string
					Number                      uint64
					Team                        Team
					Creator                     *struct{ ID string }
					State                       struct{ Type string }
					Labels                      struct {
						Nodes    []struct{ Name string }
						PageInfo struct{ HasNextPage bool }
					}
				}
				PageInfo struct {
					HasNextPage bool
					EndCursor   string
				}
			}
		}
		err := h.query(ctx, h.key, `query($filter: IssueFilter!, $after: String) { issues(first:25, after:$after, filter:$filter) { nodes { id number title description url team { id name key } creator { id } state { type } labels(first:100) { nodes { name } pageInfo { hasNextPage } } } pageInfo { hasNextPage endCursor } } }`, map[string]any{"filter": filter, "after": after}, &data)
		if err != nil {
			return maintainer.IssuePage{}, err
		}
		if data.Issues.Nodes == nil || len(data.Issues.Nodes) > 25 {
			return maintainer.IssuePage{}, ErrUnavailable
		}
		if current < page {
			if !data.Issues.PageInfo.HasNextPage {
				return maintainer.IssuePage{Issues: []maintainer.Issue{}}, nil
			}
			if data.Issues.PageInfo.EndCursor == "" {
				return maintainer.IssuePage{}, ErrUnavailable
			}
			cursor := data.Issues.PageInfo.EndCursor
			after = &cursor
			continue
		}
		result := maintainer.IssuePage{Issues: []maintainer.Issue{}}
		for _, node := range data.Issues.Nodes {
			if node.Team.ID != team || !kernel.ValidLinearID(node.ID) || node.Number == 0 || node.Number > 9007199254740991 || number != 0 && number != node.Number || node.State.Type == "" || node.Labels.PageInfo.HasNextPage || len(node.Labels.Nodes) > 100 {
				return maintainer.IssuePage{}, ErrUnavailable
			}
			issue := maintainer.Issue{NodeID: node.ID, Number: node.Number, URL: node.URL, Title: node.Title, Body: node.Description, State: "open", Labels: []string{}}
			if node.State.Type == "completed" || node.State.Type == "canceled" {
				issue.State = "closed"
			}
			if node.Creator != nil {
				issue.Author.Login = node.Creator.ID
			}
			issue.Author.Type = "user"
			for _, label := range node.Labels.Nodes {
				if len(label.Name) > 100 {
					return maintainer.IssuePage{}, ErrUnavailable
				}
				issue.Labels = append(issue.Labels, label.Name)
			}
			snapshot := kernel.IntakeIssueSnapshot{LinearTeamID: team, NodeID: node.ID, IssueNumber: node.Number, Title: node.Title, Body: node.Description, URL: node.URL}
			reason := kernel.PreviewIntake(kernel.IntakeSource{Enabled: true}, snapshot, nil)
			if reason == kernel.IntakeInvalidSnapshot {
				return maintainer.IssuePage{}, ErrUnavailable
			}
			result.Issues = append(result.Issues, issue)
		}
		if number != 0 && len(result.Issues) != 1 {
			return maintainer.IssuePage{}, ErrUnavailable
		}
		if data.Issues.PageInfo.HasNextPage {
			if page == 40 || data.Issues.PageInfo.EndCursor == "" {
				return maintainer.IssuePage{}, ErrUnavailable
			}
			next := page + 1
			result.NextPage = &next
		}
		return result, nil
	}
	return maintainer.IssuePage{}, ErrUnavailable
}

func (*Host) String() string   { return "LinearHost(<redacted>)" }
func (*Host) GoString() string { return "LinearHost(<redacted>)" }
