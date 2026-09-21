//go:build darwin || linux

package daemon

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProductionRefreshConsumesExactMaintainerPullShapeForMergedPR(t *testing.T) {
	var page struct {
		PullRequests []maintainerPullRequest `json:"pull_requests"`
	}
	response := `{"repository_id":7,"next_page":null,"pull_requests":[{"number":7,"body":"body","head_sha":"` + strings.Repeat("a", 40) + `","base_sha":"` + strings.Repeat("b", 40) + `","base_ref":"main","title":"Ship it","url":"https://github.com/team/repo/pull/7","head_ref":"feature/ship","state":"merged","merged":true}]}`
	if err := json.Unmarshal([]byte(response), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.PullRequests) != 1 {
		t.Fatalf("page = %+v", page)
	}
	pull := productionPullRequest(page.PullRequests[0])
	if pull.State != "merged" || pull.Title != "Ship it" || pull.Head != strings.Repeat("a", 40) || pull.Review.Head != pull.Head {
		t.Fatalf("pull = %+v", pull)
	}
}
