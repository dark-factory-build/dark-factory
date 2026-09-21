//go:build darwin || linux

package daemon

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestProductionRefreshFreshnessIsProjectScoped(t *testing.T) {
	projectA, _ := kernel.ProjectIDFromBytes(bytes.Repeat([]byte{0x61}, kernel.IDBytes))
	projectB, _ := kernel.ProjectIDFromBytes(bytes.Repeat([]byte{0x62}, kernel.IDBytes))
	daemon := &Daemon{}
	first := time.Unix(100, 0)
	if !daemon.productionRefreshAllowed(projectA, first) {
		t.Fatal("first project refresh was suppressed")
	}
	if daemon.productionRefreshAllowed(projectA, first.Add(time.Second)) {
		t.Fatal("same project refreshed before interval")
	}
	if !daemon.productionRefreshAllowed(projectB, first.Add(time.Second)) {
		t.Fatal("different project was suppressed by another project's refresh")
	}
}

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
