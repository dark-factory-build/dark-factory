package daemon

import (
	"context"
	"encoding/json"
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
	got, err := pullRequestObservation(context.Background(), call, "o/r", 1, nil, settled)
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
	got, err := pullRequestObservation(context.Background(), call, "o/r", 1, nil, nil)
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
	graph, err := opgraph.Infer("s", []opgraph.Repository{repo})
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
		got[node.Label] = status.Observation + "/" + status.State
	}
	want := map[string]string{"CI / go": "observed/failing", "CI / Lint": "observed/active", "CI / docs": "partial/unknown", "Release / release": "partial/unknown", "GitHub Actions": "partial/failing",
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
	runs := []string{`{"name":"go","status":"completed","conclusion":"failure","url":"https://github.com/o/r/actions/runs/5/job/91"}`, `{"name":"go","status":"completed","conclusion":"success","url":"https://github.com/o/r/actions/runs/6/job/97"}`}
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
	if got[0].ID != got[1].ID || got[1].Conclusion != "success" || got[0].URL == got[1].URL {
		t.Fatalf("the re-run did not replace the failure: %+v", got)
	}
}

// The github deploys adapter: a successful production deployment changes
// over the repository's only deployed unit and a fresh failure counts as its
// error; a preview, an in-progress deploy and an old failure draw nothing.
// The newest successful production creation time is what ships merges.
func TestDeploymentsChangeOverTheDeployedUnit(t *testing.T) {
	graph, err := opgraph.Infer("s", []opgraph.Repository{{ID: "r1", Name: "app", Files: map[string][]byte{"fly.toml": []byte("app = \"web\"\n")}}})
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
		return fmt.Sprintf(`{"id":1,"environment":%q,"production_environment":%t,"sha":"%s","ref":"main","created_at":%q,"state":%q,"updated_at":%q}`, environment, production, strings.Repeat("a", 40), stamp(created), state, stamp(updated))
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
	deployedAt, err := recordDeployments(context.Background(), call, "o/r", 1, units, nil, store, now.UnixMilli())
	if err != nil || deployedAt == nil || *deployedAt != now.Add(-10*time.Minute).UnixMilli() {
		t.Fatalf("deployed at %v, %v", deployedAt, err)
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
		if _, err := recordDeployments(context.Background(), call, "o/r", 1, []string{"web", "worker"}, nil, quiet, now.UnixMilli()); err != nil {
			t.Fatal(err)
		}
		if held, _ := quiet.Snapshot(now.UnixMilli()); len(held) != 0 {
			t.Fatalf("unmapped deployments drawn: %+v", held)
		}
		return
	}
	t.Fatal("no web unit")
}
