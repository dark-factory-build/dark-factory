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
	if failed.ID != "91" || failed.Name != "go" || failed.Revision != heads[8] || failed.Scope != "head" || failed.State != "completed" || failed.Conclusion != "failure" || len(failed.PullRequests) != 1 || failed.PullRequests[0] != 8 {
		t.Fatalf("failed check %+v", failed)
	}
	if running.ID != "92" || running.State != "in_progress" || running.Conclusion != "" {
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
