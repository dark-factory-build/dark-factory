package daemon

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestNewDaemonWiresProductionChangePublicationLoop(t *testing.T) {
	fixture := newDispatchFixture(t)
	if fixture.daemon.changePublicationEvents == nil || fixture.daemon.changePublicationActions.PublishAndRefresh == nil || fixture.daemon.changePublicationActions.RequestReview == nil {
		t.Fatal("NewDaemon did not install the production Change publication loop")
	}
	if err := fixture.daemon.processChangePublications(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMaintainerResponseValidationRejectsTopLevelErrors(t *testing.T) {
	valid := []byte(`{"jsonrpc":"2.0","id":1,"result":{"isError":false}}`)
	if !validMaintainerResponse(valid) {
		t.Fatal("valid maintainer response was rejected")
	}
	for _, response := range [][]byte{
		[]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"failed"}}`),
		[]byte(`{"jsonrpc":"2.0","id":1,"result":{"isError":true}}`),
		[]byte(`{"jsonrpc":"2.0","id":2,"result":{"isError":false}}`),
	} {
		if validMaintainerResponse(response) {
			t.Fatalf("failed maintainer response was accepted: %s", response)
		}
	}
}

func TestPlanChangePublicationTransitions(t *testing.T) {
	base := ChangePublicationEvent{ChangeID: "one", SettledHead: "new", PublishedHead: "old", PublishedSourceHead: "old", Clean: true}
	if got := PlanChangePublication(base); got != ChangePublicationPublishAndRefresh {
		t.Fatalf("unpublished clean head action = %v", got)
	}
	base.PublishedHead = "new"
	base.PublishedSourceHead = "new"
	if got := PlanChangePublication(base); got != ChangePublicationRequestReview {
		t.Fatalf("unreviewed head action = %v", got)
	}
	base.ReviewHead, base.ReviewState = "new", "allow"
	if got := PlanChangePublication(base); got != ChangePublicationNone {
		t.Fatalf("settled head action = %v", got)
	}
	base.ReviewOperation = ""
	base.SettledHead = "newer"
	base.PublishedHead = "newer"
	base.PublishedSourceHead = "newer"
	base.ReviewHead = "new"
	if got := PlanChangePublication(base); got != ChangePublicationRequestReview {
		t.Fatalf("new published head with stale review action = %v", got)
	}
	base.Clean = false
	if got := PlanChangePublication(base); got != ChangePublicationNone {
		t.Fatalf("dirty head action = %v", got)
	}
}

func TestPersistedPublicationFactPlansFreshReviewAfterHeadChange(t *testing.T) {
	fact := kernel.ChangePublicationFact{ChangeID: "persisted", SettledHead: strings.Repeat("c", 40), PublishedHead: strings.Repeat("c", 40), PublishedSourceHead: strings.Repeat("c", 40), PublishOperation: "publish-new", BodyOperation: "body-new", ReviewHead: strings.Repeat("b", 40), ReviewState: "allow"}
	if got := PlanChangePublication(changePublicationEventFromFact(fact)); got != ChangePublicationRequestReview {
		t.Fatalf("persisted old-head review fact action = %v", got)
	}
}

func TestRefreshChangeBodyPreservesProseAndConverges(t *testing.T) {
	body := "Ship the change.\n<!-- dark-factory:head=old -->\n<!-- dark-factory:base=old-base -->\n"
	want := "Ship the change.\n<!-- dark-factory:head=new -->\n<!-- dark-factory:base=base -->\n<!-- dark-factory:delta=delta -->\n"
	if got := RefreshChangeBody(body, "new", "base", "delta"); got != want {
		t.Fatalf("refreshed body = %q, want %q", got, want)
	}
	if got := RefreshChangeBody(want, "new", "base", "delta"); got != want {
		t.Fatalf("body was not idempotent: %q", got)
	}
}

func TestRefreshChangeBodyKeepsIssueFooterTerminal(t *testing.T) {
	body := "Ship the change.\nRefs #386\n"
	want := "Ship the change.\n<!-- dark-factory:head=new -->\n<!-- dark-factory:base=base -->\n<!-- dark-factory:delta=-4 -->\nRefs #386\n"
	if got := RefreshChangeBody(body, "new", "base", "-4"); got != want {
		t.Fatalf("footer moved = %q, want %q", got, want)
	}
}

func TestRefreshChangeReviewRequestKeepsIssueFooterTerminal(t *testing.T) {
	body := "Ship the change.\nRefs #386\n"
	want := "Ship the change.\n<!-- dark-factory:review-request=12345678-1234-4123-8123-123456789abc:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa -->\nRefs #386\n"
	if got := RefreshChangeReviewRequest(body, "12345678-1234-4123-8123-123456789abc", strings.Repeat("a", 40)); got != want {
		t.Fatalf("review request footer = %q, want %q", got, want)
	}
}

func TestChangedProductionLineDeltaExcludesNonProductionPaths(t *testing.T) {
	numstat := "8\t3\tinternal/daemon/service.go\n10\t4\tinternal/daemon/service_test.go\n20\t1\tdocs/design.md\n"
	if got, err := changedProductionLineDelta(numstat); err != nil || got != 5 {
		t.Fatalf("production delta = %d, err=%v", got, err)
	}
}

func TestPublicationDiffBaseUsesLocalFactsForLegacyPublishedPR(t *testing.T) {
	event := ChangePublicationEvent{ChangeID: "legacy", PublishedHead: strings.Repeat("f", 40), BaseCommit: strings.Repeat("b", 40)}
	if got, err := publicationDiffBase(event); err != nil || got != event.BaseCommit {
		t.Fatalf("legacy diff base = %q, err=%v", got, err)
	}
	event.BaseCommit = ""
	if _, err := publicationDiffBase(event); err == nil {
		t.Fatal("legacy publication without a local base was allowed to use the remote SHA")
	}
}

func TestPlanChangePublicationSkipsLegacyPublishedPRWithoutSourceHead(t *testing.T) {
	event := ChangePublicationEvent{ChangeID: "legacy", SettledHead: strings.Repeat("c", 40), PublishedHead: strings.Repeat("b", 40), BaseCommit: strings.Repeat("a", 40), Clean: true}
	if got := PlanChangePublication(event); got != ChangePublicationNone {
		t.Fatalf("legacy publication action = %v", got)
	}
}

func TestProcessChangePublicationEventsRunsChangesConcurrently(t *testing.T) {
	events := []ChangePublicationEvent{
		{ChangeID: "one", SettledHead: "one-new", PublishedHead: "one-old", PublishedSourceHead: "one-old", Clean: true},
		{ChangeID: "two", SettledHead: "two-new", PublishedHead: "two-old", PublishedSourceHead: "two-old", Clean: true},
	}
	started := make(chan string, len(events))
	gate := make(chan struct{})
	var mu sync.Mutex
	seen := map[string]bool{}
	done := make(chan error, 1)
	go func() {
		done <- ProcessChangePublicationEvents(context.Background(), events, ChangePublicationActions{
			PublishAndRefresh: func(ctx context.Context, event ChangePublicationEvent, body string) error {
				started <- event.ChangeID
				<-gate
				mu.Lock()
				seen[event.ChangeID] = body != ""
				mu.Unlock()
				return nil
			},
		})
	}()
	for range events {
		<-started
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(seen) != len(events) {
		t.Fatalf("processed changes = %v", seen)
	}
}

func TestProcessChangePublicationEventsRecordsFailureAndContinues(t *testing.T) {
	events := []ChangePublicationEvent{
		{ChangeID: "failed", SettledHead: "failed-new", PublishedHead: "failed-old", PublishedSourceHead: "failed-old", Clean: true},
		{ChangeID: "continued", SettledHead: "continued-new", PublishedHead: "continued-old", PublishedSourceHead: "continued-old", Clean: true},
	}
	var failed, continued bool
	err := ProcessChangePublicationEvents(context.Background(), events, ChangePublicationActions{
		PublishAndRefresh: func(ctx context.Context, event ChangePublicationEvent, body string) error {
			if event.ChangeID == "failed" {
				return fmt.Errorf("remote failure")
			}
			continued = true
			return nil
		},
		RecordFailure: func(ctx context.Context, event ChangePublicationEvent, err error) error {
			failed = event.ChangeID == "failed" && err.Error() == "remote failure"
			return nil
		},
	})
	if err != nil || !failed || !continued {
		t.Fatalf("failure persistence/continuation = err %v, failed %v, continued %v", err, failed, continued)
	}
}
