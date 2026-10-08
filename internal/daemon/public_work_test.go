package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/opgraph"
	"github.com/dark-factory-build/dark-factory/internal/relayhost"
)

// The console (projectCrates in production-view.ts) reads the same fixture.
func TestPublicCratesMatchTheConsoleRule(t *testing.T) {
	body, err := os.ReadFile("../../web/fixtures/production-crates.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Now     int64                     `json:"now"`
		Records []kernel.ProductionRecord `json:"records"`
		Crates  [][3]any                  `json:"crates"`
	}
	if err := json.Unmarshal(body, &fixture); err != nil {
		t.Fatal(err)
	}
	var got, want []string
	for _, crate := range publicCrates(fixture.Records, fixture.Now) {
		got = append(got, fmt.Sprintf("%s %d %v", strings.TrimPrefix(crate.Key, "owner/repo#"), crate.Station, crate.Fault))
	}
	for _, crate := range fixture.Crates {
		want = append(want, fmt.Sprintf("%v %v %v", crate[0], crate[1], crate[2]))
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("crates = %v, want %v", got, want)
	}
	// Order of records is no input.
	slices.Reverse(fixture.Records)
	if again := publicCrates(fixture.Records, fixture.Now); len(again) != len(want) {
		t.Fatalf("reversed records gave %d crates", len(again))
	}
}

func TestPublicLedgerWindowsClockAndPrivacy(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 30, 0, 0, time.UTC)
	pull := func(repository string, number uint64, state string, at time.Time, mergedAt string) kernel.ProductionRecord {
		document, _ := json.Marshal(kernel.ProductionPullRequest{Number: number, Title: repository + " title " + strconv.FormatUint(number, 10), State: state, MergedAt: mergedAt, URL: "https://example.invalid/hostile"})
		return kernel.ProductionRecord{Repository: repository, Kind: "pull_request", ID: strconv.FormatUint(number, 10), ObservedAt: at.UnixMilli(), Document: document}
	}
	records := []kernel.ProductionRecord{
		pull("open/repo", 10, "open", now, ""),
		pull("open/repo", 11, "open", now, ""),
		pull("open/repo", 12, "merged", now.Add(-time.Hour), ""),                                               // observed merged an hour ago
		pull("open/repo", 13, "merged", now, now.Add(-6*24*time.Hour).Format(time.RFC3339)),                    // merged inside the window
		pull("open/repo", 14, "merged", now, now.Add(-8*24*time.Hour).Format(time.RFC3339)),                    // on the clock, not the list
		pull("open/repo", 15, "merged", now.Add(-30*24*time.Hour), ""),                                         // before the clock
		pull("open/repo", 16, "closed", now, ""),                                                               // closed unmerged is not shipped
		pull("secret/repo", 20, "open", now, ""), pull("secret/repo", 21, "merged", now.Add(-time.Minute), ""), // private
		pull("unknown/repo", 30, "open", now, ""), // never probed
	}
	issues := []kernel.IntakeIssue{{Repository: "open/repo", Number: 40, Title: "open/repo issue"}, {Repository: "secret/repo", Number: 41, Title: "secret/repo issue"}}
	repositories := map[string]publicRepository{
		"open/repo":   {public: true, releases: []opgraph.LedgerTag{{Tag: "v1.0.0", URL: "https://github.com/open/repo/releases/tag/v1.0.0", PublishedAt: "2026-10-01T00:00:00Z"}, {Tag: "v0.9.0", PublishedAt: "2026-09-17T23:59:59Z"}}},
		"secret/repo": {public: false, releases: []opgraph.LedgerTag{{Tag: "v9.secret"}}},
	}
	ledger := publicLedger(records, issues, repositories, now)
	encoded, _ := json.Marshal(ledger)
	for _, private := range []string{"secret", "unknown", "hostile"} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatalf("ledger carries %q: %s", private, encoded)
		}
	}
	numbers := func(items []opgraph.LedgerPull) (out []uint64) {
		for _, item := range items {
			out = append(out, item.Number)
		}
		return out
	}
	if !slices.Equal(numbers(ledger.Open), []uint64{11, 10}) || ledger.OpenCount != 2 || ledger.Open[0].URL != "https://github.com/open/repo/pull/11" {
		t.Fatalf("open = %+v", ledger.Open)
	}
	if !slices.Equal(numbers(ledger.Merged), []uint64{12, 13}) || ledger.MergedCount != 2 || ledger.Merged[0].MergedAt != "2026-10-08T11:30:00Z" {
		t.Fatalf("merged = %+v", ledger.Merged)
	}
	if !slices.Equal(numbers(ledger.Issues), []uint64{40}) || ledger.IssueCount != 1 || ledger.Issues[0].URL != "https://github.com/open/repo/issues/40" {
		t.Fatalf("issues = %+v", ledger.Issues)
	}
	if len(ledger.Releases) != 1 || ledger.Releases[0].Tag != "v1.0.0" { // v0.9.0 is before the clock
		t.Fatalf("releases = %+v", ledger.Releases)
	}
	// The clock runs from the first recorded merge (eight days ago) to today, in UTC.
	if len(ledger.Clock) != 9 || ledger.Clock[0].Date != "2026-09-30" || ledger.Clock[8].Date != "2026-10-08" || ledger.Clock[8].Hours[11] != 1 || ledger.Clock[2].Hours[12] != 1 {
		t.Fatalf("clock = %+v", ledger.Clock)
	}
	if again, _ := json.Marshal(publicLedger(slices.Clone(records), issues, repositories, now)); !bytes.Equal(again, encoded) {
		t.Fatal("the ledger is not deterministic")
	}
	if publicLedger(records, issues, map[string]publicRepository{"secret/repo": {}}, now) != nil {
		t.Fatal("a factory with no public repository published a ledger")
	}
}

func TestPublicRepositoryIsWhatAnAnonymousReaderSees(t *testing.T) {
	day := func(ago int) string { return time.Now().AddDate(0, 0, -ago).UTC().Format(time.RFC3339) }
	release := func(tag string, ago int, extra string) string {
		return `{"tag_name":"` + tag + `","published_at":"` + day(ago) + `"` + extra + `}`
	}
	pages := map[string]string{
		"/open/repo/releases?1":    "[" + release("v4", 1, `,"prerelease":true`) + "," + release("v3", 2, "") + "]",
		"/open/repo/releases?2":    "[" + release("draft", 0, `,"draft":true`) + "," + release("bad tag", 3, "") + "]",
		"/open/repo/releases?3":    "[" + release("v2", 20, "") + "," + release("v1", 30, "") + "]",
		"/limited/repo/releases?1": "[" + release("v9", 1, "") + "," + release("v8", 2, "") + "]",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("the visibility read carried credentials")
		}
		if r.URL.Query().Get("per_page") != "2" {
			t.Errorf("per_page = %q", r.URL.Query().Get("per_page"))
		}
		key := r.URL.Path + "?" + r.URL.Query().Get("page")
		switch body, ok := pages[key]; {
		case ok:
			fmt.Fprint(w, body)
		case key == "/limited/repo/releases?2":
			w.WriteHeader(http.StatusForbidden) // rate limited mid-walk
		case key == "/open/repo/releases?4":
			t.Error("paged past the window")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	savedAPI, savedPage := publicRepositoryAPI, publicReleasePage
	publicRepositoryAPI, publicReleasePage = server.URL+"/", 2
	defer func() { publicRepositoryAPI, publicReleasePage = savedAPI, savedPage }()
	daemon := &Daemon{publicRepos: map[string]*publicRepository{}, now: time.Now}
	for _, name := range []string{"open/repo", "secret/repo", "limited/repo"} {
		daemon.publicRepos[name] = &publicRepository{}
		daemon.readPublicRepository(name)
	}
	got := daemon.publicRepositories([]string{"Open/Repo"})["open/repo"]
	var tags []string
	for _, release := range got.releases {
		tags = append(tags, release.Tag)
	}
	if !got.public || !slices.Equal(tags, []string{"v4", "v3", "v2"}) || got.releases[0] != (opgraph.LedgerTag{Tag: "v4", URL: "https://github.com/open/repo/releases/tag/v4", PublishedAt: day(1), Prerelease: true}) {
		t.Fatalf("open/repo = %+v", got)
	}
	for _, name := range []string{"secret/repo", "limited/repo"} {
		if got := daemon.publicRepositories([]string{name})[name]; got.public || len(got.releases) != 0 {
			t.Fatalf("%s = %+v, want private", name, got)
		}
	}
}

// A busy month in two repositories: the public one's work is published within
// the relay's bound, the private one's titles never, and crates carry no
// number or title.
func TestPublicWorldCarriesTheWorkLineAndOnlyPublicLedgerWithinTheBound(t *testing.T) {
	ctx := context.Background()
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve)
	project := relaySeedProject(t, fixture, 3, "busy project")
	fixture.daemon.home = t.TempDir()
	start := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	now := start.Add(28 * 24 * time.Hour)
	fixture.daemon.now = func() time.Time { return now }
	title := func(repository string, number int) string {
		return fmt.Sprintf("%s change %d: %s", repository, number, strings.Repeat("make the factory a little better ", 3))
	}
	head := strings.Repeat("a", 40)
	// More than the bound holds, so the oldest listed merges give way.
	number, perDay := 0, 100
	for day := 0; day <= 28; day++ {
		at := start.Add(time.Duration(day)*24*time.Hour + 13*time.Hour)
		if day == 28 {
			at = now
		}
		for _, repository := range []string{"open/repo", "secret/repo"} {
			observation := kernel.ProductionObservation{Repository: repository, ObservedAt: at.UnixMilli()}
			for range perDay {
				number++
				state := "merged"
				if day == 28 {
					state = "open"
				}
				observation.PullRequests = append(observation.PullRequests, kernel.ProductionPullRequest{Number: uint64(number), Title: title(repository, number), URL: "https://github.com/" + repository + "/pull/" + strconv.Itoa(number), Head: head, State: state, Review: kernel.ProductionReview{Head: head, State: "allow"}})
			}
			if err := fixture.store.RecordProductionObservation(ctx, project.ID, observation, adapterTime(t, at.UnixMilli())); err != nil {
				t.Fatal(err)
			}
		}
	}
	fixture.daemon.publicRepos = map[string]*publicRepository{
		"open/repo":   {public: true, next: time.Now().Add(time.Hour), releases: []opgraph.LedgerTag{{Tag: "v1.0.0", URL: "https://github.com/open/repo/releases/tag/v1.0.0", PublishedAt: "2026-10-01T00:00:00Z"}}},
		"secret/repo": {public: false, next: time.Now().Add(time.Hour)},
	}
	encoded, err := (&browserBackend{owner: fixture.daemon}).PublicWorld(ctx, project.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("published %d bytes for %d open and %d merged pull requests", len(encoded), 2*perDay, number-2*perDay)
	if len(encoded) > relayhost.MaxPublicWorldBytes {
		t.Fatalf("%d bytes is past the relay's bound", len(encoded))
	}
	if bytes.Contains(encoded, []byte("secret")) {
		t.Fatal("a private repository reached the public world")
	}
	var world opgraph.PublicWorld
	if err := json.Unmarshal(encoded, &world); err != nil {
		t.Fatal(err)
	}
	// Crates: every open pull request of both repositories, plus the last day's merges.
	if len(world.Crates) != 4*perDay {
		t.Fatalf("%d crates", len(world.Crates))
	}
	var crates bytes.Buffer
	_ = json.NewEncoder(&crates).Encode(world.Crates)
	if bytes.Contains(crates.Bytes(), []byte("change")) || bytes.Contains(crates.Bytes(), []byte("repo")) || bytes.Contains(crates.Bytes(), []byte(`"number"`)) {
		t.Fatalf("crates carry more than an id, a station and a fault: %s", crates.Bytes()[:200])
	}
	ledger := world.Ledger
	if ledger == nil || ledger.OpenCount != perDay || ledger.MergedCount != 7*perDay || len(ledger.Clock) != 21 || ledger.Clock[20].Hours[13] != 0 {
		t.Fatalf("ledger = open %d merged %d clock %d", ledger.OpenCount, ledger.MergedCount, len(ledger.Clock))
	}
	t.Logf("listed %d of %d merged, %d of %d open", len(ledger.Merged), ledger.MergedCount, len(ledger.Open), ledger.OpenCount)
	if len(ledger.Merged) == 0 || len(ledger.Merged) >= ledger.MergedCount || len(ledger.Open) == 0 || ledger.Merged[0].MergedAt < ledger.Merged[len(ledger.Merged)-1].MergedAt {
		t.Fatalf("listed merged %d open %d", len(ledger.Merged), len(ledger.Open))
	}
}
