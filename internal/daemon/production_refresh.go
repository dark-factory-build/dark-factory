package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/opgraph"
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
		var settled map[kernel.ProductionHead]bool
		if err == nil {
			settled, err = daemon.store.SettledProductionChecks(ctx, project, identity.PublicationRepository)
		}
		var observation kernel.ProductionObservation
		if err == nil {
			observation, err = pullRequestObservation(ctx, daemon.github.MCP, identity.PublicationRepository, githubID, known, settled)
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
		recordCIObservations(daemon.runtimeStore(), repository.ID.String(), observation, at.Int64())
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

// maintainerMCP is the broker call, daemon.github.MCP in production.
type maintainerMCP func(ctx context.Context, request json.RawMessage, repositories map[string]uint64) (json.RawMessage, error)

func pullRequestObservation(ctx context.Context, call maintainerMCP, repository string, githubID uint64, known []kernel.ProductionPullRequest, settled map[kernel.ProductionHead]bool) (kernel.ProductionObservation, error) {
	page, err := readMaintainerPullRequests(ctx, call, repository, githubID, map[string]any{"repository": repository, "page": 1, "per_page": productionRefreshPRLimit})
	if err != nil {
		return kernel.ProductionObservation{}, err
	}
	open := page.PullRequests
	seen := make(map[uint64]bool, len(open))
	for _, pull := range open {
		seen[pull.Number] = true
	}
	for _, prior := range rereadPulls(known, seen) {
		exact, err := readMaintainerPullRequests(ctx, call, repository, githubID, map[string]any{"repository": repository, "page": 1, "per_page": 1, "pull_number": prior.Number})
		if err == nil {
			open = append(open, exact.PullRequests...)
		}
	}
	prior := make(map[uint64]kernel.ProductionReview, len(known))
	for _, pull := range known {
		prior[pull.Number] = pull.Review
	}
	result := kernel.ProductionObservation{Repository: repository, PullRequests: []kernel.ProductionPullRequest{}, Checks: []kernel.ProductionCheck{}}
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
	// Every pull read here is open or was open at the last refresh. Its
	// checks are read until every run on its current head has completed, so
	// the cost follows CI activity, not the number of open pulls. The host
	// controller that recorded checks was deleted (#1133) and nothing
	// replaced it (#1404).
	for _, pr := range result.PullRequests {
		if settled[kernel.ProductionHead{Number: pr.Number, Head: strings.ToLower(pr.Head)}] {
			continue
		}
		checks, err := readMaintainerChecks(ctx, call, repository, githubID, pr)
		if err != nil {
			result.Unavailable = "checks"
			continue
		}
		// One observation writes at most the store's bound. Pulls that do not
		// fit stay unsettled and are read on the next refresh; coverage is not
		// claimed meanwhile.
		if len(result.Checks)+len(checks) > kernel.MaxObservationChecks {
			result.Unavailable = "checks"
			break
		}
		result.Checks = append(result.Checks, checks...)
	}
	return result, nil
}

// recordCIObservations is the github adapter: the checks a refresh read
// light the jobs of the repository's CI unit, and fail them on a failed
// conclusion. A refresh reads only heads whose checks can still change, so a
// job lights while it runs and once when it settles. Silence is claimed only
// when no read failed and the pull page was complete.
func recordCIObservations(store *opgraph.Runtime, repository string, observation kernel.ProductionObservation, now int64) {
	unit := opgraph.CIUnit(repository)
	for _, check := range observation.Checks {
		item := opgraph.Observation{Source: "github", Environment: "ci", Kind: "internal", Start: now - productionRefreshInterval.Milliseconds(), End: now,
			Attributes: map[string]string{"service.name": unit, "cicd.pipeline.task.name": opgraph.CheckName(check.Name)}, Count: 1}
		switch check.Conclusion {
		case "failure", "timed_out", "startup_failure":
			item.Errors = 1
		}
		store.Record(item)
	}
	if observation.Unavailable == "" && observation.Overflow == 0 {
		store.Cover(opgraph.Coverage{Source: "github", Environment: "ci", Unit: unit, Keys: opgraph.CIKeys, AsOf: now, TTL: 2 * productionRefreshInterval.Milliseconds()})
	}
}

// readMaintainerChecks maps observe_pull_request_checks, one record per check
// run, keyed by the run's GitHub id (the last segment of its URL).
func readMaintainerChecks(ctx context.Context, call maintainerMCP, repository string, githubID uint64, pr kernel.ProductionPullRequest) ([]kernel.ProductionCheck, error) {
	content, err := maintainerTool(ctx, call, repository, githubID, "observe_pull_request_checks", map[string]any{"repository": repository, "pull_number": pr.Number, "head_sha": pr.Head})
	if err != nil {
		return nil, err
	}
	var value struct {
		Head   string `json:"head_sha"`
		Checks []struct {
			Name       string  `json:"name"`
			Status     string  `json:"status"`
			Conclusion *string `json:"conclusion"`
			URL        string  `json:"url"`
		} `json:"checks"`
	}
	if json.Unmarshal(content, &value) != nil || !strings.EqualFold(value.Head, pr.Head) || len(value.Checks) > 100 {
		return nil, fmt.Errorf("invalid checks response")
	}
	checks := make([]kernel.ProductionCheck, 0, len(value.Checks))
	for _, check := range value.Checks {
		if check.Conclusion != nil && *check.Conclusion == "skipped" {
			continue // a job whose condition was false never ran; GitHub counts it as passing
		}
		id := path.Base(check.URL)
		if id == "" || strings.Trim(id, "0123456789") != "" {
			return nil, fmt.Errorf("invalid check url")
		}
		conclusion := ""
		if check.Conclusion != nil {
			conclusion = *check.Conclusion
		}
		checks = append(checks, kernel.ProductionCheck{ID: id, Name: check.Name, Revision: strings.ToLower(pr.Head), Scope: "head", State: check.Status, Conclusion: conclusion, URL: check.URL, PullRequests: []uint64{pr.Number}, Jobs: []kernel.ProductionJob{}})
	}
	return checks, nil
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

func readMaintainerPullRequests(ctx context.Context, call maintainerMCP, repository string, githubID uint64, arguments map[string]any) (maintainerPullRequestPage, error) {
	content, err := maintainerTool(ctx, call, repository, githubID, "list_pull_requests", arguments)
	if err != nil {
		return maintainerPullRequestPage{}, err
	}
	return parseMaintainerPullRequestPage(content)
}

// maintainerTool calls one broker tool and returns its structured content.
func maintainerTool(ctx context.Context, call maintainerMCP, repository string, githubID uint64, name string, arguments map[string]any) (json.RawMessage, error) {
	request, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": arguments},
	})
	if err != nil {
		return nil, err
	}
	response, err := call(ctx, request, map[string]uint64{repository: githubID})
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
		return nil, fmt.Errorf("invalid %s response", name)
	}
	return envelope.Result.Content, nil
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
