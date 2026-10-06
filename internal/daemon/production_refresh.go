package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/review"
)

const productionRefreshPRLimit = 100

// Each refresh lists up to 100 pull requests on the owner's GitHub quota,
// which host tools share; every 30s exhausted it (5,000/h).
const productionRefreshInterval = 5 * time.Minute

// refreshProduction reads the bounded pull-request projection through the
// retained Maintainer connection. It is deliberately best-effort: an
// unavailable remote must leave the last durable observation visible.
func (daemon *Daemon) refreshProduction(ctx context.Context, project kernel.ProjectID) error {
	if daemon == nil || daemon.github == nil || project == (kernel.ProjectID{}) {
		return nil
	}
	if !daemon.productionRefreshAllowed(project, daemon.now()) {
		return nil
	}
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
		known, published, err := daemon.store.KnownProductionPulls(ctx, project, identity.PublicationRepository, productionRefreshPRLimit)
		var observation kernel.ProductionObservation
		if err == nil {
			observation, err = daemon.pullRequestObservation(ctx, identity.PublicationRepository, githubID, known)
		}
		if err != nil {
			LogFactoryd(daemon.log, "factoryd: refresh %s: %v\n", identity.PublicationRepository, err)
			continue
		}
		corrections := changedProductionHeads(known, observation.PullRequests)
		at, err := daemon.timestamp()
		if err != nil {
			return err
		}
		observation.ObservedAt = at.Int64()
		prepared := make([]review.Operation, 0, len(corrections))
		for _, correction := range corrections {
			if !published[correction.Number] {
				continue
			}
			op, err := daemon.preparePublishedReview(ctx, project, identity.PublicationRepository, correction.Number, correction.Head)
			if err != nil {
				return err
			}
			prepared = append(prepared, op)
		}
		if len(prepared) == 0 {
			if err := daemon.store.RecordProductionObservation(ctx, project, observation, at); err != nil {
				return err
			}
		} else {
			claims := make([]kernel.ProductionReviewOperation, 0, len(prepared))
			for _, op := range prepared {
				claims = append(claims, kernel.ProductionReviewOperation{ID: op.ID, Document: op})
			}
			if err := daemon.store.RecordProductionObservationWithReviewOperations(ctx, project, observation, claims, at); err != nil {
				return err
			}
		}
		for _, op := range prepared {
			daemon.launchReview(project, op)
		}
	}
	return nil
}

// refreshProductionDetached serves the browser's Production read, whose 3s
// call budget a scan or a wait on the Maintainer lock (held for whole HTTP
// calls) outlasted, cancelling every refresh. The read shows the last durable
// observation; the refresh runs under the daemon's lifetime, bounded per HTTP
// call by the Maintainer client's timeout once the lock is held.
func (daemon *Daemon) refreshProductionDetached(project kernel.ProjectID) {
	ctx := daemon.cleanupCtx
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		if err := daemon.refreshProduction(ctx, project); err != nil {
			LogFactoryd(daemon.log, "factoryd: refresh %s: %v\n", project, err)
		}
	}()
}

func changedProductionHeads(known []kernel.ProductionPullRequest, observed []kernel.ProductionPullRequest) []kernel.ProductionPullRequest {
	prior := make(map[uint64]kernel.ProductionPullRequest, len(known))
	for _, pull := range known {
		prior[pull.Number] = pull
	}
	changed := make([]kernel.ProductionPullRequest, 0)
	for _, pull := range observed {
		old, ok := prior[pull.Number]
		if ok && old.Head != "" && !strings.EqualFold(old.Head, pull.Head) && pull.State == "open" {
			changed = append(changed, pull)
		}
	}
	return changed
}

func (daemon *Daemon) productionRefreshAllowed(project kernel.ProjectID, now time.Time) bool {
	daemon.productionRefreshMu.Lock()
	defer daemon.productionRefreshMu.Unlock()
	if daemon.productionRefreshAt == nil {
		daemon.productionRefreshAt = make(map[kernel.ProjectID]time.Time)
	}
	if now.Before(daemon.productionRefreshAt[project].Add(productionRefreshInterval)) {
		return false
	}
	daemon.productionRefreshAt[project] = now
	return true
}

type maintainerPullRequest struct {
	Number         uint64                  `json:"number"`
	Title          string                  `json:"title"`
	URL            string                  `json:"url"`
	Head           string                  `json:"head_sha"`
	HeadRepository string                  `json:"head_repository"`
	Branch         string                  `json:"head_ref"`
	Base           string                  `json:"base_ref"`
	BaseSHA        string                  `json:"base_sha"`
	State          string                  `json:"state"`
	Merged         bool                    `json:"merged"`
	Review         kernel.ProductionReview `json:"review"`
}

func (daemon *Daemon) pullRequestObservation(ctx context.Context, repository string, githubID uint64, known []kernel.ProductionPullRequest) (kernel.ProductionObservation, error) {
	page, err := daemon.readMaintainerPullRequests(ctx, repository, githubID, map[string]any{"repository": repository, "page": 1, "per_page": productionRefreshPRLimit})
	if err != nil {
		return kernel.ProductionObservation{}, err
	}
	open := page.PullRequests
	seen := make(map[uint64]bool, len(open))
	for _, pull := range open {
		seen[pull.Number] = true
	}
	for _, prior := range rereadPulls(known, seen) {
		exact, err := daemon.readMaintainerPullRequests(ctx, repository, githubID, map[string]any{"repository": repository, "page": 1, "per_page": 1, "pull_number": prior.Number})
		if err == nil {
			open = append(open, exact.PullRequests...)
		}
	}
	prior := make(map[uint64]kernel.ProductionReview, len(known))
	for _, pull := range known {
		prior[pull.Number] = pull.Review
	}
	result := kernel.ProductionObservation{Repository: repository, PullRequests: []kernel.ProductionPullRequest{}}
	result.Overflow = productionPullRequestOverflow(page)
	for _, value := range open {
		if value.Number == 0 || len(value.Head) != 40 || strings.Trim(value.Head, "0123456789abcdef") != "" {
			return kernel.ProductionObservation{}, fmt.Errorf("invalid pull request head")
		}
		pr := productionPullRequest(value)
		if review, ok := prior[value.Number]; ok && strings.EqualFold(review.Head, value.Head) {
			pr.Review = review
		}
		result.PullRequests = append(result.PullRequests, pr)
	}
	return result, nil
}

// rereadPulls are the pulls last seen open that the open page no longer
// lists. Merged and closed are final, so they are never read again: reading
// every pull ever published cost one call each per refresh.
func rereadPulls(known []kernel.ProductionPullRequest, seen map[uint64]bool) []kernel.ProductionPullRequest {
	reread := []kernel.ProductionPullRequest{}
	for _, pull := range known {
		if pull.State == "open" && !seen[pull.Number] {
			reread = append(reread, pull)
		}
	}
	return reread
}

func productionPullRequestOverflow(page maintainerPullRequestPage) int {
	if page.NextPage != nil {
		return 1
	}
	return 0
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
	return kernel.ProductionPullRequest{Number: value.Number, Title: value.Title, URL: value.URL, Head: value.Head, HeadRepository: value.HeadRepository, Branch: value.Branch, Base: value.Base, BaseSHA: value.BaseSHA, State: state, Review: review}
}

type maintainerPullRequestPage struct {
	PullRequests []maintainerPullRequest `json:"pull_requests"`
	NextPage     *int                    `json:"next_page"`
}

func (daemon *Daemon) readMaintainerPullRequests(ctx context.Context, repository string, githubID uint64, arguments map[string]any) (maintainerPullRequestPage, error) {
	request, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "list_pull_requests", "arguments": arguments},
	})
	if err != nil {
		return maintainerPullRequestPage{}, err
	}
	response, err := daemon.github.MCP(ctx, request, map[string]uint64{repository: githubID})
	if err != nil {
		return maintainerPullRequestPage{}, err
	}
	var envelope struct {
		Result struct {
			IsError bool            `json:"isError"`
			Content json.RawMessage `json:"structuredContent"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(response, &envelope) != nil || envelope.Result.IsError || len(envelope.Error) != 0 {
		return maintainerPullRequestPage{}, fmt.Errorf("invalid pull request response")
	}
	page, err := parseMaintainerPullRequestPage(envelope.Result.Content)
	if err != nil {
		return maintainerPullRequestPage{}, err
	}
	return page, nil
}

func parseMaintainerPullRequestPage(content []byte) (maintainerPullRequestPage, error) {
	var page maintainerPullRequestPage
	if json.Unmarshal(content, &page) != nil || len(page.PullRequests) > productionRefreshPRLimit || (page.NextPage != nil && (*page.NextPage < 2 || *page.NextPage > 1000)) {
		return maintainerPullRequestPage{}, fmt.Errorf("invalid pull request page")
	}
	for _, value := range page.PullRequests {
		if value.Number == 0 || len(value.Head) != 40 || strings.Trim(value.Head, "0123456789abcdef") != "" {
			return maintainerPullRequestPage{}, fmt.Errorf("invalid pull request head")
		}
	}
	return page, nil
}
