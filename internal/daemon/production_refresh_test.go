package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/opgraph"
)

func TestRefreshRereadsOnlyPullsLastSeenOpen(t *testing.T) {
	known := []kernel.ProductionPullRequest{{Number: 1, State: "open"}, {Number: 2, State: "open"}, {Number: 3, State: "merged"}, {Number: 4, State: "closed"}}
	got := rereadPulls(known, map[uint64]bool{1: true})
	if len(got) != 1 || got[0].Number != 2 {
		t.Fatalf("reread %+v, want only #2", got)
	}
}

func TestRefreshExactReadsNewChangedOrUnsettledPulls(t *testing.T) {
	head := strings.Repeat("a", 40)
	base := strings.Repeat("b", 40)
	mergeable := true
	if pullRequestNeedsExactRead(maintainerPullRequest{Number: 1, Head: head, Base: "main", BaseSHA: base}, map[uint64]kernel.ProductionPullRequest{
		1: {Number: 1, Head: head, Base: "main", BaseSHA: base, Mergeable: &mergeable},
	}) {
		t.Fatal("unchanged pull needs an exact read")
	}
	if !pullRequestNeedsExactRead(maintainerPullRequest{Number: 1, Head: strings.Repeat("c", 40), Base: "main", BaseSHA: base}, map[uint64]kernel.ProductionPullRequest{
		1: {Number: 1, Head: head, Base: "main", BaseSHA: base, Mergeable: &mergeable},
	}) {
		t.Fatal("changed head was not selected for an exact read")
	}
	if !pullRequestNeedsExactRead(maintainerPullRequest{Number: 2, Head: head, Base: "main", BaseSHA: base}, map[uint64]kernel.ProductionPullRequest{}) {
		t.Fatal("new pull was not selected for an exact read")
	}
}

func TestRefreshPreservesMergeabilityWhenListOmitsIt(t *testing.T) {
	head := strings.Repeat("a", 40)
	baseSHA := strings.Repeat("b", 40)
	exactReads := 0
	call := func(_ context.Context, request json.RawMessage, _ map[string]uint64) (json.RawMessage, error) {
		var value struct {
			Params struct {
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.Unmarshal(request, &value); err != nil {
			t.Fatal(err)
		}
		pull := `{"number":7,"head_sha":"` + head + `","base_ref":"main","base_sha":"` + baseSHA + `","state":"open"}`
		if _, exact := value.Params.Arguments["pull_number"]; exact {
			exactReads++
			if exactReads > 1 {
				pull = `{"number":7,"head_sha":"` + head + `","base_ref":"main","base_sha":"` + baseSHA + `","state":"open","mergeable":false,"merge_state_status":"dirty"}`
			}
		}
		return json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"pull_requests":[` + pull + `],"next_page":null}}}`), nil
	}
	settled := map[kernel.ProductionHead]bool{{Number: 7, Head: head}: true}
	first, err := pullRequestObservation(context.Background(), call, "o/r", 1, nil, settled, func(error) {})
	if err != nil || len(first.PullRequests) != 1 || first.PullRequests[0].Mergeable != nil {
		t.Fatalf("initial unsettled mergeability = %+v, %v", first.PullRequests, err)
	}
	second, err := pullRequestObservation(context.Background(), call, "o/r", 1, first.PullRequests, settled, func(error) {})
	if err != nil || len(second.PullRequests) != 1 || second.PullRequests[0].Mergeable == nil || *second.PullRequests[0].Mergeable || second.PullRequests[0].MergeState != "dirty" {
		t.Fatalf("preserved mergeability = %+v, %v", second.PullRequests, err)
	}
}

func TestRefreshReadsQueueStateForApprovedPulls(t *testing.T) {
	head := strings.Repeat("a", 40)
	pull := kernel.ProductionPullRequest{Number: 7, Head: head, Base: "main"}
	for _, test := range []struct {
		state, want string
	}{
		{"ACTIVE_QUEUE", "active"},
		{"NOT_QUEUED", "none"},
	} {
		t.Run(test.state, func(t *testing.T) {
			call := func(_ context.Context, request json.RawMessage, _ map[string]uint64) (json.RawMessage, error) {
				if !strings.Contains(string(request), "observe_pull_request_merge") {
					t.Fatalf("unexpected request %s", request)
				}
				content := fmt.Sprintf(`{"pull_number":7,"head_sha":"%s","state":"%s"}`, head, test.state)
				if test.state == "NOT_QUEUED" {
					content = fmt.Sprintf(`{"pull_number":7,"head_sha":"%s","state":"NOT_QUEUED","merge_group":{"run_id":12,"url":"https://github.com/o/r/actions/runs/12","conclusion":"failure","failed_jobs":[{"name":"go","conclusion":"failure","annotations":["--- FAIL: TestQueue (0.01s)"]}]}}`, head)
				}
				return json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":` + content + `}}`), nil
			}
			got, group, err := readMaintainerMergeQueue(context.Background(), call, "o/r", 1, pull)
			if err != nil || got != test.want {
				t.Fatalf("queue=%q err=%v, want %q", got, err, test.want)
			}
			if test.state == "NOT_QUEUED" && (group == nil || group.ID != 12 || group.Jobs[0].Annotations[0] != "--- FAIL: TestQueue (0.01s)") {
				t.Fatalf("merge group = %+v", group)
			}
			if test.state == "ACTIVE_QUEUE" && group != nil {
				t.Fatalf("active queue unexpectedly had merge group %+v", group)
			}
		})
	}
}

func TestRefreshProjectsMergeGroupAndMainChecks(t *testing.T) {
	head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	call := func(_ context.Context, request json.RawMessage, _ map[string]uint64) (json.RawMessage, error) {
		var value struct {
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if err := json.Unmarshal(request, &value); err != nil {
			t.Fatal(err)
		}
		switch value.Params.Name {
		case "list_pull_requests":
			return json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"pull_requests":[{"number":7,"head_sha":"` + head + `","base_ref":"main","base_sha":"` + base + `","state":"open","review":{"head":"` + head + `","state":"allow"}}],"next_page":null}}}`), nil
		case "observe_pull_request_merge":
			return json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"pull_number":7,"head_sha":"` + head + `","state":"NOT_QUEUED","merge_group":{"run_id":12,"url":"https://github.com/o/r/actions/runs/12","conclusion":"failure","failed_jobs":[{"name":"go","conclusion":"failure","annotations":["--- FAIL: TestQueue"]}]}}}}`), nil
		case "observe_repository_checks":
			return json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"revision":"` + base + `","checks":[{"name":"main","status":"completed","conclusion":"failure","url":"https://github.com/o/r/actions/runs/13","required":false,"annotations":[]}]}}}`), nil
		case "observe_pull_request_checks":
			return json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"pull_number":7,"head_sha":"` + head + `","checks":[]}}}`), nil
		default:
			t.Fatalf("unexpected maintainer tool %s", value.Params.Name)
			return nil, nil
		}
	}
	got, err := productionObservation(context.Background(), call, "o/r", 1, nil, nil, func(error) {})
	if err != nil || len(got.Checks) != 2 {
		t.Fatalf("observation = %+v, %v", got, err)
	}
	if got.Checks[0].Scope != "merge_group" || got.Checks[0].Jobs[0].Annotations[0] != "--- FAIL: TestQueue" || got.Checks[1].Scope != "main" || got.Checks[1].Revision != base {
		t.Fatalf("projected checks = %+v", got.Checks)
	}
	withoutMain := func(ctx context.Context, request json.RawMessage, repositories map[string]uint64) (json.RawMessage, error) {
		if strings.Contains(string(request), "observe_repository_checks") {
			return nil, errors.New("main checks unavailable")
		}
		return call(ctx, request, repositories)
	}
	retained, err := productionObservation(context.Background(), withoutMain, "o/r", 1, nil, nil, func(error) {})
	if err != nil || retained.Unavailable != "main_checks" || len(retained.Checks) != 1 || retained.Checks[0].Scope != "merge_group" {
		t.Fatalf("retained observation = %+v, %v", retained, err)
	}
}

func TestProductionObservationKeepsSameRevisionHeadAndMainChecks(t *testing.T) {
	revision := strings.Repeat("a", 40)
	call := func(_ context.Context, request json.RawMessage, _ map[string]uint64) (json.RawMessage, error) {
		var value struct {
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if err := json.Unmarshal(request, &value); err != nil {
			t.Fatal(err)
		}
		switch value.Params.Name {
		case "list_pull_requests":
			return json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"pull_requests":[{"number":7,"head_sha":"` + revision + `","base_ref":"main","base_sha":"` + revision + `","state":"open"}],"next_page":null}}}`), nil
		case "observe_pull_request_checks":
			return json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"head_sha":"` + revision + `","checks":[{"name":"build","status":"completed","conclusion":"failure","url":"https://github.com/o/r/actions/runs/13","required":false}]}}}`), nil
		case "observe_repository_checks":
			return json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"revision":"` + revision + `","checks":[{"name":"build","status":"completed","conclusion":"failure","url":"https://github.com/o/r/actions/runs/14","required":false}]}}}`), nil
		default:
			t.Fatalf("unexpected maintainer tool %s", value.Params.Name)
			return nil, nil
		}
	}
	got, err := productionObservation(context.Background(), call, "o/r", 1, nil, nil, func(error) {})
	if err != nil || len(got.Checks) != 2 {
		t.Fatalf("observation = %+v, %v", got, err)
	}
	if got.Checks[0].Scope != "head" || got.Checks[1].Scope != "main" || got.Checks[0].ID == got.Checks[1].ID {
		t.Fatalf("same-revision checks = %+v", got.Checks)
	}
}

func TestReadMaintainerMainChecksKeepsPerRevisionUnfinishedRuns(t *testing.T) {
	current, previous := strings.Repeat("a", 40), strings.Repeat("b", 40)
	call := func(_ context.Context, _ json.RawMessage, _ map[string]uint64) (json.RawMessage, error) {
		return json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"revision":"` + current + `","checks":[{"revision":"` + previous + `","name":"build","status":"in_progress","conclusion":null,"url":"https://github.com/o/r/actions/runs/1","required":false},{"revision":"` + previous + `","name":"build","status":"completed","conclusion":"failure","url":"https://github.com/o/r/actions/runs/2","required":false}]}}}`), nil
	}
	checks, err := readMaintainerMainChecks(context.Background(), call, "o/r", 1)
	if err != nil || len(checks) != 2 || checks[0].Revision != previous || checks[0].Status != "in_progress" || checks[1].Revision != previous {
		t.Fatalf("main checks = %+v, %v", checks, err)
	}
	converted := mainChecksToProduction(checks)
	if len(converted) != 1 || converted[0].Revision != previous || converted[0].Conclusion != "failure" {
		t.Fatalf("converted main checks = %+v", converted)
	}
}

func TestRefreshRejectsStaleQueueObservation(t *testing.T) {
	head := strings.Repeat("a", 40)
	pull := kernel.ProductionPullRequest{Number: 7, Head: head, Base: "main"}
	call := func(_ context.Context, _ json.RawMessage, _ map[string]uint64) (json.RawMessage, error) {
		return json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"pull_number":7,"head_sha":"` + strings.Repeat("b", 40) + `","state":"NOT_QUEUED"}}}`), nil
	}
	if _, _, err := readMaintainerMergeQueue(context.Background(), call, "o/r", 1, pull); err == nil {
		t.Fatal("stale merge observation accepted")
	}
}

// #1404: the refresh read pull requests but never their checks, so no check
// record was stored once the host controller was deleted. It reads checks
// only while they can change: #7's head settled, #8's stored checks are
// still running, #9 settled an older head and moved, and #10's completed
// checks include a failure, so the store leaves it unsettled.
func TestRefreshObservesCurrentHeadChecks(t *testing.T) {
	head := func(c string) string { return strings.Repeat(c, 40) }
	heads := map[float64]string{7: head("a"), 8: head("b"), 9: head("c"), 10: head("e")}
	var read []float64
	call := func(_ context.Context, request json.RawMessage, _ map[string]uint64) (json.RawMessage, error) {
		var value struct {
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.Unmarshal(request, &value); err != nil {
			t.Fatal(err)
		}
		content := `{"pull_requests":[{"number":7,"head_sha":"` + heads[7] + `","state":"open"},{"number":8,"head_sha":"` + heads[8] + `","state":"open"},{"number":9,"head_sha":"` + heads[9] + `","state":"open"},{"number":10,"head_sha":"` + heads[10] + `","state":"open"}],"next_page":null}`
		if value.Params.Name == "observe_pull_request_checks" {
			number := value.Params.Arguments["pull_number"].(float64)
			if value.Params.Arguments["head_sha"] != heads[number] {
				t.Fatalf("checks arguments %v", value.Params.Arguments)
			}
			read = append(read, number)
			content = `{"pull_number":9,"head_sha":"` + heads[number] + `","checks":[]}`
			if number == 8 {
				content = `{"pull_number":8,"head_sha":"` + heads[8] + `","checks":[{"name":"go","status":"completed","conclusion":"failure","url":"https://github.com/o/r/actions/runs/5/job/91"},{"name":"lint","status":"in_progress","conclusion":null,"url":"https://github.com/o/r/runs/92"},{"name":"docs","status":"completed","conclusion":"skipped","url":"https://github.com/o/r/runs/93"}]}`
			}
		}
		return json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":` + content + `}}`), nil
	}
	settled := map[kernel.ProductionHead]bool{{Number: 7, Head: heads[7]}: true, {Number: 9, Head: head("d")}: true}
	got, err := pullRequestObservation(context.Background(), call, "o/r", 1, nil, settled, func(error) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(read) != 3 || read[0] != 8 || read[1] != 9 || read[2] != 10 || len(got.Checks) != 2 || got.Unavailable != "" || got.Overflow != 0 {
		t.Fatalf("read %v, observation %+v", read, got)
	}
	failed, running := got.Checks[0], got.Checks[1]
	if len(failed.ID) != 32 || failed.Name != "go" || failed.Revision != heads[8] || failed.Scope != "head" || failed.State != "completed" || failed.Conclusion != "failure" || len(failed.PullRequests) != 1 || failed.PullRequests[0] != 8 {
		t.Fatalf("failed check %+v", failed)
	}
	if running.ID == failed.ID || len(running.ID) != 32 || running.State != "in_progress" || running.Conclusion != "" {
		t.Fatalf("running check %+v", running)
	}
}

// Many unsettled pulls never overflow one observation: reading stops at the
// store's bound, coverage is not claimed, and what is left is read next time.
func TestRefreshStopsAtTheObservationsCheckBound(t *testing.T) {
	pulls := make([]string, 0, 30)
	for number := 1; number <= 30; number++ {
		pulls = append(pulls, fmt.Sprintf(`{"number":%d,"head_sha":"%040x","state":"open"}`, number, number))
	}
	reads := 0
	call := func(_ context.Context, request json.RawMessage, _ map[string]uint64) (json.RawMessage, error) {
		content := `{"pull_requests":[` + strings.Join(pulls, ",") + `],"next_page":null}`
		if strings.Contains(string(request), "observe_pull_request_checks") {
			reads++
			var value struct {
				Params struct {
					Arguments map[string]any `json:"arguments"`
				} `json:"params"`
			}
			_ = json.Unmarshal(request, &value)
			checks := make([]string, 0, 20)
			for run := 0; run < 20; run++ {
				checks = append(checks, fmt.Sprintf(`{"name":"job%d","status":"in_progress","conclusion":null,"url":"https://github.com/o/r/runs/%d%02d"}`, run, reads, run))
			}
			content = `{"pull_number":1,"head_sha":"` + value.Params.Arguments["head_sha"].(string) + `","checks":[` + strings.Join(checks, ",") + `]}`
		}
		return json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":` + content + `}}`), nil
	}
	got, err := pullRequestObservation(context.Background(), call, "o/r", 1, nil, nil, func(error) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Checks) > kernel.MaxObservationChecks || len(got.Checks) != 240 || got.Unavailable != "checks" || len(got.PullRequests) != 30 {
		t.Fatalf("%d checks, unavailable %q, %d pulls", len(got.Checks), got.Unavailable, len(got.PullRequests))
	}
}

// The github adapter: a pull-request workflow's jobs light and fail from the
// checks a refresh read; a job never seen and a push-only workflow's job stay
// partial, and CI alone does not count as the repository's code.
func TestRefreshedChecksLightTheirWorkflowJobs(t *testing.T) {
	repo := opgraph.Repository{ID: "r1", Name: "app", Files: map[string][]byte{
		".github/workflows/ci.yml":      []byte("name: CI\non:\n  pull_request:\n  merge_group:\njobs:\n  go:\n    runs-on: x\n  lint:\n    name: Lint (${{ matrix.os }})\n  docs:\n    runs-on: x\n"),
		".github/workflows/release.yml": []byte("name: Release\non: [push]\njobs:\n  release:\n    runs-on: x\n"),
	}}
	graph, err := opgraph.Infer("s", []opgraph.Repository{repo}, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := int64(10 * time.Hour / time.Millisecond)
	store := opgraph.NewRuntime(time.Hour)
	recordCIObservations(store, "r1", kernel.ProductionObservation{Checks: []kernel.ProductionCheck{
		{Name: "go", State: "completed", Conclusion: "failure"},
		{Name: "Lint (macos)", State: "in_progress"},
	}}, now)
	observations, coverage := store.Snapshot(now)
	live := opgraph.Overlay("s", graph, observations, coverage, nil, now, runtimeWindow.Milliseconds())
	got := map[string]string{}
	for _, node := range live.Graph.Nodes {
		status := live.Nodes[node.ID]
		got[node.Label] = string(status.Observation) + "/" + string(status.State)
	}
	want := map[string]string{"CI / go": "observed/failing", "CI / Lint": "observed/active", "CI / docs": "partial/unknown", "Release / release": "partial/unknown", "GitHub Actions · app": "partial/failing",
		"app": "unobserved/unknown", "No recognised entry points": "unobserved/unknown"}
	for label, reading := range want {
		if got[label] != reading {
			t.Fatalf("%s reads %q, want %q (all %v)", label, got[label], reading, got)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("nodes %v", got)
	}
}

// A re-run is a new GitHub check run under the same name: it must land on
// the same record, so a passing re-run replaces the failure it retried.
func TestARerunReplacesTheCheckItRetried(t *testing.T) {
	head := strings.Repeat("f", 40)
	runs := []string{`{"name":"go","status":"completed","conclusion":"failure","url":"https://github.com/o/r/actions/runs/5/job/91"}`, `{"name":"go","status":"completed","conclusion":"success","required":true,"url":"https://github.com/o/r/actions/runs/6/job/97"}`}
	var got []kernel.ProductionCheck
	for _, run := range runs {
		call := func(context.Context, json.RawMessage, map[string]uint64) (json.RawMessage, error) {
			return json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"pull_number":3,"head_sha":"` + head + `","checks":[` + run + `]}}}`), nil
		}
		checks, err := readMaintainerChecks(context.Background(), call, "o/r", 1, kernel.ProductionPullRequest{Number: 3, Head: head})
		if err != nil || len(checks) != 1 {
			t.Fatalf("checks %+v, %v", checks, err)
		}
		got = append(got, checks[0])
	}
	if got[0].ID != got[1].ID || got[1].Conclusion != "success" || got[0].URL == got[1].URL || got[0].Required || !got[1].Required {
		t.Fatalf("the re-run did not replace the failure: %+v", got)
	}
}

// The github deploys adapter: a successful production deployment changes
// over the repository's only deployed unit and a fresh failure counts as its
// error; a preview, an in-progress deploy and an old failure draw nothing.
// The newest successful production creation time is what ships merges.
func TestDeploymentsChangeOverTheDeployedUnit(t *testing.T) {
	graph, err := opgraph.Infer("s", []opgraph.Repository{{ID: "r1", Name: "app", Files: map[string][]byte{"fly.toml": []byte("app = \"web\"\n")}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var project kernel.ProjectID
	daemon := &Daemon{graphs: map[kernel.ProjectID]graphSnapshot{project: {value: projectGraph{graph: graph}}}}
	units := daemon.deployedUnits(project, "r1")
	if len(units) != 1 || units[0] != "web" || len(daemon.deployedUnits(project, "r2")) != 0 {
		t.Fatalf("deployed units %v", units)
	}
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	stamp := func(ago time.Duration) string { return now.Add(-ago).Format(time.RFC3339) }
	deployment := func(environment string, production bool, state string, created, updated time.Duration) string {
		return fmt.Sprintf(`{"id":1,"environment":%q,"production_environment":%t,"sha":"%s","ref":"main","created_at":%q,"state":%q,"updated_at":%q,"environment_host":"%s.example.app"}`, environment, production, strings.Repeat("a", 40), stamp(created), state, stamp(updated), state)
	}
	var calls []string
	call := func(_ context.Context, request json.RawMessage, _ map[string]uint64) (json.RawMessage, error) {
		calls = append(calls, string(request))
		list := strings.Join([]string{
			deployment("Production", true, "in_progress", time.Minute, time.Minute),
			deployment("Preview", false, "success", 2*time.Minute, 2*time.Minute),
			deployment("Production", true, "failure", 3*time.Minute, 3*time.Minute),
			deployment("Production", true, "success", 10*time.Minute, 8*time.Minute),
			deployment("Production", true, "error", 2*time.Hour, 2*time.Hour),
		}, ",")
		return json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"deployments":[` + list + `]}}}`), nil
	}
	store := opgraph.NewRuntime(time.Hour)
	deployedAt, hosts, err := recordDeployments(context.Background(), call, "o/r", 1, units, nil, store, now.UnixMilli(), func(error) {})
	if err != nil || deployedAt == nil || *deployedAt != now.Add(-10*time.Minute).UnixMilli() {
		t.Fatalf("deployed at %v, %v", deployedAt, err)
	}
	// The unit serves where its successful deployments say; an unmapped
	// preview's host is no unit's.
	if len(hosts) != 1 || strings.Join(hosts["web"], ",") != "success.example.app" {
		t.Fatalf("hosts %v", hosts)
	}
	if len(calls) != 1 || !strings.Contains(calls[0], `"name":"list_deployments"`) || !strings.Contains(calls[0], `"per_page":30`) {
		t.Fatalf("calls %v", calls)
	}
	observations, coverage := store.Snapshot(now.UnixMilli())
	live := opgraph.Overlay("s", graph, observations, coverage, nil, now.UnixMilli(), runtimeWindow.Milliseconds())
	for _, node := range live.Graph.Nodes {
		if node.Label != "web" {
			continue
		}
		status := live.Nodes[node.ID]
		changed := now.Add(-8 * time.Minute).Truncate(time.Minute).Add(time.Minute).UnixMilli()
		if status.DeployedAt != changed || status.ErrorRate != 1 || status.State != "failing" || len(coverage) != 0 {
			t.Fatalf("web %+v, coverage %v", status, coverage)
		}
		// Several deployed units and no services mapping: nothing lands.
		quiet := opgraph.NewRuntime(time.Hour)
		if _, _, err := recordDeployments(context.Background(), call, "o/r", 1, []string{"web", "worker"}, nil, quiet, now.UnixMilli(), func(error) {}); err != nil {
			t.Fatal(err)
		}
		if held, _ := quiet.Snapshot(now.UnixMilli()); len(held) != 0 {
			t.Fatalf("unmapped deployments drawn: %+v", held)
		}
		return
	}
	t.Fatal("no web unit")
}
