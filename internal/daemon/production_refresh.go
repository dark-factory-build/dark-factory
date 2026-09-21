package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

const productionRefreshPRLimit = 100
const productionRefreshInterval = 30 * time.Second

// refreshProduction reads the bounded pull-request projection through the
// retained Maintainer connection. It is deliberately best-effort: an
// unavailable remote must leave the last durable observation visible.
func (daemon *Daemon) refreshProduction(ctx context.Context, project kernel.ProjectID) error {
	if daemon == nil || daemon.github == nil || project == (kernel.ProjectID{}) {
		return nil
	}
	now := daemon.now()
	daemon.productionRefreshMu.Lock()
	if now.Before(daemon.productionRefreshedAt.Add(productionRefreshInterval)) {
		daemon.productionRefreshMu.Unlock()
		return nil
	}
	daemon.productionRefreshedAt = now
	daemon.productionRefreshMu.Unlock()
	repositories, err := daemon.store.ProjectRepositories(ctx, project)
	if err != nil {
		return err
	}
	for _, repository := range repositories {
		identity, verified, err := daemon.store.RepositorySourceIdentity(ctx, repository.ID)
		if err != nil {
			return err
		}
		githubID, pinned, err := daemon.store.RepositoryGitHubID(ctx, repository.ID)
		if err != nil {
			return err
		}
		if !verified || !pinned || identity.PublicationRepository == "" {
			continue
		}
		known, _ := daemon.knownProductionPulls(ctx, project, identity.PublicationRepository)
		observation, err := daemon.pullRequestObservation(ctx, identity.PublicationRepository, githubID, known)
		if err != nil {
			continue
		}
		at, err := daemon.timestamp()
		if err != nil {
			return err
		}
		observation.ObservedAt = at.Int64()
		if err := daemon.store.RecordProductionObservation(ctx, project, observation, at); err != nil {
			return err
		}
	}
	return nil
}

type maintainerPullRequest struct {
	Number         uint64                  `json:"number"`
	Title          string                  `json:"title"`
	URL            string                  `json:"url"`
	Head           string                  `json:"head_sha"`
	HeadRepository string                  `json:"head_repository"`
	Branch         string                  `json:"head_ref"`
	Base           string                  `json:"base_ref"`
	State          string                  `json:"state"`
	Merged         bool                    `json:"merged"`
	Review         kernel.ProductionReview `json:"review"`
}

func (daemon *Daemon) pullRequestObservation(ctx context.Context, repository string, githubID uint64, known []kernel.ProductionPullRequest) (kernel.ProductionObservation, error) {
	open, err := daemon.readMaintainerPullRequests(ctx, repository, githubID, map[string]any{"repository": repository, "page": 1, "per_page": productionRefreshPRLimit})
	if err != nil {
		return kernel.ProductionObservation{}, err
	}
	seen := make(map[uint64]bool, len(open))
	for _, pull := range open {
		seen[pull.Number] = true
	}
	for _, prior := range known {
		if seen[prior.Number] {
			continue
		}
		exact, err := daemon.readMaintainerPullRequests(ctx, repository, githubID, map[string]any{"repository": repository, "page": 1, "per_page": 1, "pull_number": prior.Number})
		if err == nil {
			open = append(open, exact...)
		}
	}
	prior := make(map[uint64]kernel.ProductionReview, len(known))
	for _, pull := range known {
		prior[pull.Number] = pull.Review
	}
	result := kernel.ProductionObservation{Repository: repository, PullRequests: []kernel.ProductionPullRequest{}}
	for _, value := range open {
		if value.Number == 0 || len(value.Head) != 40 || strings.Trim(value.Head, "0123456789abcdef") != "" {
			return kernel.ProductionObservation{}, fmt.Errorf("invalid pull request head")
		}
		pr := productionPullRequest(value)
		if review, ok := prior[value.Number]; ok && review.Head != "" {
			pr.Review = review
		}
		result.PullRequests = append(result.PullRequests, pr)
	}
	return result, nil
}

func productionPullRequest(value maintainerPullRequest) kernel.ProductionPullRequest {
	state := value.State
	if value.Merged {
		state = "merged"
	}
	if state == "" {
		state = "open"
	}
	review := value.Review
	if review.Head == "" {
		review.Head = value.Head
	}
	if review.State == "" {
		review.State = "unknown"
	}
	return kernel.ProductionPullRequest{Number: value.Number, Title: value.Title, URL: value.URL, Head: value.Head, HeadRepository: value.HeadRepository, Branch: value.Branch, Base: value.Base, State: state, Review: review}
}

func (daemon *Daemon) readMaintainerPullRequests(ctx context.Context, repository string, githubID uint64, arguments map[string]any) ([]maintainerPullRequest, error) {
	request, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "list_pull_requests", "arguments": arguments},
	})
	if err != nil {
		return nil, err
	}
	response, err := daemon.github.MCP(ctx, request, map[string]uint64{repository: githubID})
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Result struct {
			IsError bool            `json:"isError"`
			Content json.RawMessage `json:"structuredContent"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(response, &envelope) != nil || envelope.Result.IsError || len(envelope.Error) != 0 {
		return nil, fmt.Errorf("invalid pull request response")
	}
	var page struct {
		PullRequests []maintainerPullRequest `json:"pull_requests"`
	}
	if json.Unmarshal(envelope.Result.Content, &page) != nil || len(page.PullRequests) > productionRefreshPRLimit {
		return nil, fmt.Errorf("invalid pull request page")
	}
	for _, value := range page.PullRequests {
		if value.Number == 0 || len(value.Head) != 40 || strings.Trim(value.Head, "0123456789abcdef") != "" {
			return nil, fmt.Errorf("invalid pull request head")
		}
	}
	return page.PullRequests, nil
}

func (daemon *Daemon) knownProductionPulls(ctx context.Context, project kernel.ProjectID, repository string) ([]kernel.ProductionPullRequest, error) {
	known := []kernel.ProductionPullRequest{}
	for offset, pages := 0, 0; pages < 128; pages++ {
		page, err := daemon.store.Production(ctx, project, offset, 8)
		if err != nil {
			return nil, err
		}
		for _, record := range page.Records {
			if record.Kind != "pull_request" || !strings.EqualFold(record.Repository, repository) {
				continue
			}
			var pull kernel.ProductionPullRequest
			if json.Unmarshal(record.Document, &pull) == nil {
				known = append(known, pull)
			}
			if len(known) == productionRefreshPRLimit {
				return known, nil
			}
		}
		if page.NextOffset <= offset || page.NextOffset >= page.Total {
			break
		}
		offset = page.NextOffset
	}
	return known, nil
}
