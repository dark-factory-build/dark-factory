package daemon

import (
	"cmp"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/opgraph"
)

const (
	// productionStaleAfter is production-view.ts STALE_AFTER: two production
	// refresh intervals, so a record is stale only once a refresh was missed.
	productionStaleAfter = 2 * productionRefreshInterval
	// shippedFor is production-view.ts SHIPPED_FOR and kernel.ProductionRecent.
	shippedFor = kernel.ProductionRecent
	// deployingFor is production-view.ts DEPLOYING_FOR: how recent a
	// repository's last production deployment must be for a merge to wait on
	// the next one.
	deployingFor = 7 * 24 * time.Hour
	// The site's ledger windows: merges listed, and days on the operating clock.
	ledgerWindowDays = 7
	ledgerClockDays  = 21
)

// publicCrates is projectCrates in production-view.ts over the same records,
// timed by the factory's clock: each open pull request at the first gate its
// records have not passed (review, current-head checks, merge queue), each
// merged one shipped for a day, closed unmerged gone. Where GitHub records
// deployments, merged ships only once a successful production deployment was
// created at or after the merge, and waits at the merge queue's end until
// then. A blocked review or a failed current-head check is a fault; merged, a
// blocked or failed delivery.
// ponytail: the console also stales a review whose head its local source
// observation contradicts; factoryd takes the recorded head as the source.
func publicCrates(records []kernel.ProductionRecord, now int64) []opgraph.Crate {
	fresh := func(observed int64, unavailable string) bool {
		for _, part := range strings.Split(unavailable, ",") {
			if part != "" && !slices.Contains([]string{"jobs", "review_journal", "release_journal", "release_config"}, part) {
				return false
			}
		}
		return observed > 0 && now-observed <= productionStaleAfter.Milliseconds()
	}
	healthy := map[string]bool{}
	failed := map[string]bool{}  // repository#number#head with a failed head check
	blocked := map[string]bool{} // repository#number with a blocked latest delivery
	latest := map[string]kernel.ProductionDelivery{}
	deployed := map[string]int64{} // newest successful production deployment
	for _, record := range records {
		switch record.Kind {
		case "repository":
			var health struct {
				Unavailable string
				DeployedAt  *int64 `json:"deployed_at"`
			}
			healthy[record.Repository] = json.Unmarshal(record.Document, &health) == nil && fresh(record.ObservedAt, health.Unavailable)
			if health.DeployedAt != nil {
				deployed[record.Repository] = *health.DeployedAt
			}
		case "check":
			var check kernel.ProductionCheck
			if json.Unmarshal(record.Document, &check) == nil && check.Scope == "head" && slices.Contains([]string{"failure", "timed_out", "action_required"}, check.Conclusion) {
				for _, number := range check.PullRequests {
					failed[record.Repository+"#"+strconv.FormatUint(number, 10)+"#"+check.Revision] = true
				}
			}
		case "delivery":
			var delivery kernel.ProductionDelivery
			if json.Unmarshal(record.Document, &delivery) != nil {
				continue
			}
			for _, number := range delivery.PullRequests {
				key := record.Repository + "#" + strconv.FormatUint(number, 10) + "#" + delivery.Destination
				if prior, ok := latest[key]; !ok || deliveryBefore(delivery, prior) {
					latest[key] = delivery
				}
			}
		}
	}
	for key, delivery := range latest {
		if delivery.State == "blocked" || delivery.State == "failed" {
			blocked[key[:strings.LastIndex(key, "#")]] = true
		}
	}
	crates := []opgraph.Crate{}
	for _, record := range records {
		var pr kernel.ProductionPullRequest
		if record.Kind != "pull_request" || json.Unmarshal(record.Document, &pr) != nil {
			continue
		}
		key := record.Repository + "#" + strconv.FormatUint(pr.Number, 10)
		switch pr.State {
		case "merged":
			at := record.ObservedAt
			if merged, err := time.Parse(time.RFC3339, pr.MergedAt); err == nil {
				at = merged.UnixMilli()
			}
			if now-at <= shippedFor.Milliseconds() {
				station := 3
				// A repository that has not deployed this way within deployingFor
				// of the merge no longer does: the merge ships.
				if deployedAt, ok := deployed[record.Repository]; ok && deployedAt < at && at-deployedAt <= deployingFor.Milliseconds() {
					station = 2
				}
				crates = append(crates, opgraph.Crate{Key: key, Station: station, Fault: blocked[key]})
			}
		case "open":
			current := pr.Review.Head != "" && pr.Review.Head == pr.Head
			allowed := current && pr.Review.State == "allow" && healthy[record.Repository] && fresh(record.ObservedAt, "")
			correction := current && pr.Review.State == "block" || failed[key+"#"+pr.Head]
			station := 0
			if queued := pr.MergeQueue != "" && pr.MergeQueue != "none" && pr.MergeQueue != "unknown"; queued && !correction {
				station = 2
			} else if allowed {
				station = 1
			}
			crates = append(crates, opgraph.Crate{Key: key, Station: station, Fault: correction})
		}
	}
	return crates
}

// deliveryBefore is deliveryOrder in production-view.ts: newest first, an
// unresolved attempt before a verified receipt of the same second, then id.
func deliveryBefore(a, b kernel.ProductionDelivery) bool {
	at := func(d kernel.ProductionDelivery) int64 {
		if d.UpdatedAt != 0 {
			return d.UpdatedAt
		}
		return d.VerifiedAt
	}
	verified := func(d kernel.ProductionDelivery) bool { return d.VerifiedAt > 0 && d.State == "verified" }
	if at(a) != at(b) {
		return at(a) > at(b)
	}
	if verified(a) != verified(b) {
		return !verified(a)
	}
	return a.ID < b.ID
}

// publicLedger is the public repositories' work as factoryd recorded it: open
// pull requests, accepted issues not yet finished, merges in the window, an
// hourly clock of merges, and published releases. A repository not proven
// public contributes nothing; with none, there is no ledger.
func publicLedger(records []kernel.ProductionRecord, issues []kernel.IntakeIssue, repositories map[string]publicRepository, now time.Time) *opgraph.PublicLedger {
	public := func(repository string) bool { return repositories[strings.ToLower(repository)].public }
	ledger := &opgraph.PublicLedger{WindowDays: ledgerWindowDays, Open: []opgraph.LedgerPull{}, Issues: []opgraph.LedgerPull{}, Merged: []opgraph.LedgerPull{}, Releases: []opgraph.LedgerTag{}, Clock: []opgraph.LedgerDay{}}
	today := now.UTC().Truncate(24 * time.Hour)
	clockStart, windowStart := today.AddDate(0, 0, 1-ledgerClockDays), now.Add(-ledgerWindowDays*24*time.Hour)
	anyPublic := false
	for _, repository := range repositories {
		anyPublic = anyPublic || repository.public
		for _, release := range repository.releases {
			// Releases share the clock's window.
			if repository.public && release.PublishedAt >= clockStart.Format(time.RFC3339) {
				ledger.Releases = append(ledger.Releases, release)
			}
		}
	}
	if !anyPublic {
		return nil
	}
	hours := map[string]*[24]int{}
	earliest := today
	for _, record := range records {
		var pr kernel.ProductionPullRequest
		if record.Kind != "pull_request" || !public(record.Repository) || json.Unmarshal(record.Document, &pr) != nil {
			continue
		}
		item := opgraph.LedgerPull{Number: pr.Number, Title: pr.Title, URL: "https://github.com/" + record.Repository + "/pull/" + strconv.FormatUint(pr.Number, 10)}
		switch pr.State {
		case "open":
			ledger.Open = append(ledger.Open, item)
		case "merged":
			// A merge time is recorded only when the merge is; otherwise the
			// first observation of the merged state, within a refresh of it.
			at := time.UnixMilli(record.ObservedAt).UTC()
			if merged, err := time.Parse(time.RFC3339, pr.MergedAt); err == nil {
				at = merged.UTC()
			}
			if at.Before(clockStart) || at.After(now) {
				continue
			}
			day := at.Truncate(24 * time.Hour)
			if day.Before(earliest) {
				earliest = day
			}
			if hours[day.Format(time.DateOnly)] == nil {
				hours[day.Format(time.DateOnly)] = &[24]int{}
			}
			hours[day.Format(time.DateOnly)][at.Hour()]++
			if !at.Before(windowStart) {
				item.MergedAt = at.Format(time.RFC3339)
				ledger.Merged = append(ledger.Merged, item)
			}
		}
	}
	// The clock starts at the first recorded merge: days before the records
	// begin are not days without merges.
	for day := earliest; !day.After(today); day = day.AddDate(0, 0, 1) {
		entry := opgraph.LedgerDay{Date: day.Format(time.DateOnly)}
		if counted := hours[entry.Date]; counted != nil {
			entry.Hours = *counted
		}
		ledger.Clock = append(ledger.Clock, entry)
	}
	for _, issue := range issues {
		if public(issue.Repository) {
			ledger.Issues = append(ledger.Issues, opgraph.LedgerPull{Number: issue.Number, Title: issue.Title, URL: "https://github.com/" + issue.Repository + "/issues/" + strconv.FormatUint(issue.Number, 10)})
		}
	}
	slices.SortFunc(ledger.Open, func(a, b opgraph.LedgerPull) int {
		return cmp.Or(cmp.Compare(b.Number, a.Number), strings.Compare(a.URL, b.URL))
	})
	slices.SortFunc(ledger.Merged, func(a, b opgraph.LedgerPull) int {
		return cmp.Or(strings.Compare(b.MergedAt, a.MergedAt), strings.Compare(a.URL, b.URL))
	})
	slices.SortFunc(ledger.Releases, func(a, b opgraph.LedgerTag) int {
		return cmp.Or(strings.Compare(b.PublishedAt, a.PublishedAt), strings.Compare(a.URL, b.URL))
	})
	ledger.OpenCount, ledger.MergedCount, ledger.IssueCount = len(ledger.Open), len(ledger.Merged), len(ledger.Issues)
	return ledger
}

// publicRepository is what an anonymous reader of GitHub sees of a repository:
// whether it is public at all, and its published releases.
type publicRepository struct {
	public   bool
	releases []opgraph.LedgerTag
	next     time.Time
	pending  bool
}

// Visibility is read anonymously: a repository is public exactly when GitHub
// answers its releases to a reader without credentials. It costs one
// unauthenticated call per repository an hour (one more per hundred releases
// in the window), never the owner's quota; any other answer, or none yet, is
// private.
var (
	publicReleasePage        = 100 // GitHub's largest page
	publicRepositoryAPI      = "https://api.github.com/repos/"
	publicRepositoryInterval = time.Hour
	publicRepositoryRetry    = 5 * time.Minute
)

func (daemon *Daemon) publicRepositories(names []string) map[string]publicRepository {
	daemon.publicRepoMu.Lock()
	defer daemon.publicRepoMu.Unlock()
	if daemon.publicRepos == nil {
		daemon.publicRepos = map[string]*publicRepository{}
	}
	result := map[string]publicRepository{}
	now := time.Now()
	for _, name := range names {
		name = strings.ToLower(name)
		entry := daemon.publicRepos[name]
		if entry == nil {
			entry = &publicRepository{}
			daemon.publicRepos[name] = entry
		}
		if !entry.pending && !now.Before(entry.next) {
			entry.pending = true
			go daemon.readPublicRepository(name)
		}
		result[name] = *entry
	}
	return result
}

// readPublicRepository pages the releases back to the clock's window: the
// first page proves the repository public, and the walk stops at a short page
// or one reaching before the window. Any failed page leaves it private until
// the retry, never a partial list.
func (daemon *Daemon) readPublicRepository(name string) {
	since := time.Now().AddDate(0, 0, -ledgerClockDays)
	public, releases := false, []opgraph.LedgerTag{}
	for page := 1; ; page++ {
		listed, ok := daemon.readReleasePage(name, page)
		if public = ok; !ok {
			releases = nil
			break
		}
		older := false
		for _, release := range listed {
			published, err := time.Parse(time.RFC3339, release.PublishedAt)
			if release.Draft || err != nil {
				continue
			}
			if published.Before(since) {
				older = true
				continue
			}
			if releaseVersion.MatchString(release.TagName) {
				releases = append(releases, opgraph.LedgerTag{Tag: release.TagName, URL: "https://github.com/" + name + "/releases/tag/" + release.TagName, PublishedAt: published.UTC().Format(time.RFC3339), Prerelease: release.Prerelease})
			}
		}
		if older || len(listed) < publicReleasePage {
			break
		}
	}
	daemon.publicRepoMu.Lock()
	defer daemon.publicRepoMu.Unlock()
	entry := daemon.publicRepos[name]
	entry.pending, entry.public, entry.releases = false, public, releases
	entry.next = time.Now().Add(publicRepositoryInterval)
	if !public {
		entry.next = time.Now().Add(publicRepositoryRetry)
	}
}

type listedRelease struct {
	TagName     string `json:"tag_name"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	PublishedAt string `json:"published_at"`
}

func (daemon *Daemon) readReleasePage(name string, page int) ([]listedRelease, bool) {
	request, err := http.NewRequest(http.MethodGet, publicRepositoryAPI+name+"/releases?per_page="+strconv.Itoa(publicReleasePage)+"&page="+strconv.Itoa(page), nil)
	if err != nil {
		return nil, false
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", releaseUserAgent)
	response, err := daemon.observed(releaseClient).Do(request)
	if err != nil {
		return nil, false
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	var listed []listedRelease
	return listed, err == nil && response.StatusCode == http.StatusOK && json.Unmarshal(body, &listed) == nil
}

// publicWork reads the work line and, for public repositories, the ledger.
func (daemon *Daemon) publicWork(ctx context.Context, project kernel.ProjectID) ([]opgraph.Crate, *opgraph.PublicLedger, error) {
	now := daemon.now()
	records, err := daemon.store.PublicProduction(ctx, project, now.AddDate(0, 0, -ledgerClockDays).UnixMilli())
	if err != nil {
		return nil, nil, err
	}
	issues, err := daemon.store.OpenIntakeIssues(ctx, project)
	if err != nil {
		return nil, nil, err
	}
	var names []string
	for _, record := range records {
		if record.Kind == "repository" || record.Kind == "pull_request" {
			names = append(names, record.Repository)
		}
	}
	for _, issue := range issues {
		names = append(names, issue.Repository)
	}
	slices.Sort(names)
	return publicCrates(records, now.UnixMilli()), publicLedger(records, issues, daemon.publicRepositories(slices.Compact(names)), now), nil
}
