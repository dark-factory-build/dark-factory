package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
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
