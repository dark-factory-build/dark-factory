package daemon

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
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
			daemon.noteMaintainerFault(err)
			LogFactoryd(daemon.log, "factoryd: refresh %s: %v\n", identity.PublicationRepository, err)
			continue
		}
		corrections := changedProductionHeads(known, observation.PullRequests)
		at, err := daemon.timestamp()
		if err != nil {
			return err
		}
		observation.ObservedAt = at.Int64()
		if units := daemon.deployedUnits(project, repository.ID.String()); len(units) > 0 {
			var hosts map[string][]string
			if observation.DeployedAt, hosts, err = recordDeployments(ctx, daemon.github.MCP, identity.PublicationRepository, githubID, units, daemon.observeSources(), daemon.runtimeStore(), at.Int64()); err != nil {
				LogFactoryd(daemon.log, "factoryd: refresh %s deployments: %v\n", identity.PublicationRepository, err)
			} else {
				daemon.setHosts("github\x00"+identity.PublicationRepository, hosts)
			}
		}
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

// githubQuotaLow holds back non-urgent GitHub polling (pull request refresh,
// merge-stage observation, issue intake) while the owner's quota, shared with
// host sessions and agents, is under a tenth until it resets (#1510).
func (daemon *Daemon) githubQuotaLow() bool {
	if daemon.github == nil {
		return false
	}
	quota, ok := daemon.github.Quota()
	return ok && quota.Low(daemon.now())
}

// holdHealth holds the condition key (or, with no detail, drops it), keeping
// when it began while it stays held.
func (daemon *Daemon) holdHealth(key string, project kernel.ProjectID, detail string) {
	daemon.healthMu.Lock()
	defer daemon.healthMu.Unlock()
	held, ok := daemon.heldHealth[key]
	if detail == "" {
		delete(daemon.heldHealth, key)
		return
	}
	if !ok {
		since, err := kernel.NewUnixMillis(daemon.now().UnixMilli())
		if err != nil {
			return
		}
		held = kernel.OverseerHealth{Key: key, Since: since}
	}
	if daemon.heldHealth == nil {
		daemon.heldHealth = map[string]kernel.OverseerHealth{}
	}
	held.Project, held.Detail = project, detail
	daemon.heldHealth[key] = held
}

// noteMaintainerFault counts a logged Maintainer failure: an unavailable
// Maintainer (its 503s) or an answer outside its contract. Faults closer
// than two merge-stage passes apart are one streak.
func (daemon *Daemon) noteMaintainerFault(err error) {
	if !errors.Is(err, maintainer.ErrUnavailable) && !strings.Contains(err.Error(), "Maintainer returned an invalid") {
		return
	}
	now := daemon.now()
	daemon.healthMu.Lock()
	defer daemon.healthMu.Unlock()
	faults := &daemon.maintainerFaults
	if now.Sub(faults.last) > 2*productionRefreshInterval {
		faults.first, faults.count = now, 0
	}
	faults.last, faults.fault = now, err.Error()
	faults.count++
}

// overseerHealth is every condition factoryd holds only in memory that the
// overseer must see: the owner's GitHub quota under a tenth (500 of 5,000), a
// failing intake sync (held by pollIntakeSource), and a streak of three or
// more Maintainer faults.
func (daemon *Daemon) overseerHealth() []kernel.OverseerHealth {
	quota := ""
	if daemon.githubQuotaLow() {
		value, _ := daemon.github.Quota()
		quota = fmt.Sprintf("GitHub quota %d/%d remaining (Maintainer x-ratelimit-remaining) until %s; GitHub polling waits", value.Remaining, value.Limit, time.Unix(value.Reset, 0).UTC().Format(time.RFC3339))
	}
	daemon.holdHealth("github-quota", kernel.ProjectID{}, quota)
	now := daemon.now()
	daemon.healthMu.Lock()
	defer daemon.healthMu.Unlock()
	health := slices.Collect(maps.Values(daemon.heldHealth))
	if faults := daemon.maintainerFaults; faults.count >= 3 && now.Sub(faults.last) <= 2*productionRefreshInterval {
		if since, err := kernel.NewUnixMillis(faults.first.UnixMilli()); err == nil {
			health = append(health, kernel.OverseerHealth{Key: "maintainer", Since: since,
				Detail: fmt.Sprintf("%d Maintainer faults in factoryd.stderr.log, last at %s: %s", faults.count, faults.last.UTC().Format(time.RFC3339), faults.fault)})
		}
	}
	return health
}

func (daemon *Daemon) productionRefreshAllowed(project kernel.ProjectID, now time.Time) bool {
	if daemon.githubQuotaLow() {
		return false
	}
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

// deployedUnits are the service names of a repository's units that a
// deployment declaration names, from the plant graph last built; none
// before the first build.
func (daemon *Daemon) deployedUnits(project kernel.ProjectID, repository string) []string {
	daemon.graphMu.Lock()
	graph := daemon.graphs[project].value.graph
	daemon.graphMu.Unlock()
	var units []string
	for _, node := range graph.Nodes {
		if name := node.Selectors["service.name"]; node.Deployed && name != "" && len(node.Modules) > 0 && node.Modules[0].Repository == repository {
			units = append(units, name)
		}
	}
	return units
}

// recordDeployments is the github deploys adapter. Each deployment GitHub
// records whose newest status is success is a deploy on its unit: the one a
// github source in observe.json maps its environment to in services, else,
// for the production environment, the repository's only deployed unit. A
// failure or error since the last refresh counts as an error there; other
// states are not drawn, and no coverage is claimed. It returns the newest
// successful production deployment's creation time (0 when production has
// none), nil when GitHub records no production deployment, and the hosts
// each unit's successful deployments serve at.
func recordDeployments(ctx context.Context, call maintainerMCP, repository string, githubID uint64, units []string, sources []observeSource, store *opgraph.Runtime, now int64) (*int64, map[string][]string, error) {
	content, err := maintainerTool(ctx, call, repository, githubID, "list_deployments", map[string]any{"repository": repository, "per_page": 30})
	if err != nil {
		return nil, nil, err
	}
	var value struct {
		Deployments []struct {
			Environment string `json:"environment"`
			Production  bool   `json:"production_environment"`
			CreatedAt   string `json:"created_at"`
			State       string `json:"state"`
			UpdatedAt   string `json:"updated_at"`
			Host        string `json:"environment_host"`
		} `json:"deployments"`
	}
	if json.Unmarshal(content, &value) != nil || len(value.Deployments) > 30 {
		return nil, nil, fmt.Errorf("invalid deployments response")
	}
	// ponytail: one production environment and alias map per home, not per
	// repository; key them by repository if two systems ever disagree.
	production, aliases := "", map[string]string{}
	for _, source := range sources {
		if source.Adapter == "github" {
			production = cmp.Or(source.Environment, production)
			maps.Copy(aliases, source.Services)
		}
	}
	var deployedAt *int64
	hosts := map[string][]string{}
	for _, deployment := range value.Deployments {
		created, createdErr := time.Parse(time.RFC3339, deployment.CreatedAt)
		updated, updatedErr := time.Parse(time.RFC3339, deployment.UpdatedAt)
		if createdErr != nil || updatedErr != nil {
			return nil, nil, fmt.Errorf("invalid deployment time")
		}
		isProduction := deployment.Environment == production || production == "" && deployment.Production
		if isProduction {
			at := int64(0)
			if deployedAt != nil {
				at = *deployedAt
			}
			if deployment.State == "success" {
				at = max(at, created.UnixMilli())
			}
			deployedAt = &at
		}
		unit := aliases[deployment.Environment]
		if unit == "" && isProduction && len(units) == 1 {
			unit = units[0]
		}
		item := opgraph.Observation{Source: "github", Environment: deployment.Environment, Start: updated.UnixMilli(), End: updated.UnixMilli(),
			Attributes: map[string]string{"service.name": unit}, Count: 1}
		switch {
		case unit == "":
		case deployment.State == "success":
			item.Kind = "deploy"
			store.Record(item)
			if deployment.Host != "" {
				hosts[unit] = append(hosts[unit], deployment.Host)
			}
		case (deployment.State == "failure" || deployment.State == "error") && item.End > now-productionRefreshInterval.Milliseconds():
			// Read again every refresh, a failure counts once: refreshes are
			// at least an interval apart. ponytail: one older than that when
			// first read is not counted; remember the last refresh if it matters.
			item.Kind, item.Errors = "internal", 1
			store.Record(item)
		}
	}
	return deployedAt, hosts, nil
}

// readMaintainerChecks maps observe_pull_request_checks to one record per
// head and check name, so a re-run replaces the earlier result instead of
// sitting beside it; the run itself stays named by its URL.
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
		key := sha256.Sum256([]byte(strings.ToLower(pr.Head) + "\x00" + check.Name))
		checks = append(checks, kernel.ProductionCheck{ID: hex.EncodeToString(key[:16]), Name: check.Name, Revision: strings.ToLower(pr.Head), Scope: "head", State: check.Status, Conclusion: conclusion, URL: check.URL, PullRequests: []uint64{pr.Number}, Jobs: []kernel.ProductionJob{}})
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
