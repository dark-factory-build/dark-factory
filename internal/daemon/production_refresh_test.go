//go:build darwin || linux

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
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

func TestProductionRefreshPreservesPullRequestOverflow(t *testing.T) {
	pulls := make([]maintainerPullRequest, 100)
	for index := range pulls {
		pulls[index] = maintainerPullRequest{Number: uint64(index + 1), Head: strings.Repeat("a", 40)}
	}
	payload, err := json.Marshal(maintainerPullRequestPage{PullRequests: pulls, NextPage: intPointer(2)})
	if err != nil {
		t.Fatal(err)
	}
	page, err := parseMaintainerPullRequestPage(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.PullRequests) != 100 || page.NextPage == nil || *page.NextPage != 2 {
		t.Fatalf("page = %+v", page)
	}
	if productionPullRequestOverflow(page) != 1 {
		t.Fatal("paginated page was not marked incomplete")
	}
}

func TestCorrectionHeadSchedulesFreshReview(t *testing.T) {
	oldHead := strings.Repeat("a", 40)
	newHead := strings.Repeat("b", 40)
	observed := []kernel.ProductionPullRequest{{Number: 7, Head: newHead, State: "open"}}
	corrections := changedProductionHeads([]kernel.ProductionPullRequest{{Number: 7, Head: oldHead, State: "open"}}, observed)
	if len(corrections) != 1 || corrections[0].Head != newHead {
		t.Fatalf("corrections=%+v", corrections)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests := make(chan api.ReviewRequest, 1)
	project := mustProjectID(t, testID(254))
	daemon := &Daemon{cleanupCtx: ctx, reviewPublished: func(_ context.Context, _ kernel.ProjectID, request api.ReviewRequest) (string, error) {
		requests <- request
		return "review-op", nil
	}}
	daemon.launchPublishedReview(project, "team/repo", corrections[0].Number, corrections[0].Head)
	select {
	case request := <-requests:
		if request.Repository != "team/repo" || request.PullNumber != 7 || request.Head != newHead {
			t.Fatalf("correction review request=%+v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("correction review was not scheduled")
	}
}

func intPointer(value int) *int { return &value }
