package daemon

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
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

func TestPlanChangePublicationTransitions(t *testing.T) {
	legacy := ChangePublicationEvent{ChangeID: "one", SettledHead: "new", PublishedHead: "old", Clean: true}
	if got := PlanChangePublication(legacy); got != ChangePublicationNone {
		t.Fatalf("legacy published head without a source head action = %v", got)
	}
	base := ChangePublicationEvent{ChangeID: "one", SettledHead: "new", PublishedHead: "old", PublishedSourceHead: "old", PublishOperation: "publish-old", BodyOperation: "body-old", Clean: true}
	if got := PlanChangePublication(base); got != ChangePublicationPublishAndRefresh {
		t.Fatalf("corrected clean head action = %v", got)
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
	base.Clean = false
	if got := PlanChangePublication(base); got != ChangePublicationNone {
		t.Fatalf("dirty head action = %v", got)
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

func TestProcessChangePublicationEventsRecordsFailureAndContinues(t *testing.T) {
	events := []ChangePublicationEvent{
		{ChangeID: "broken", SettledHead: "new", PublishedHead: "old", PublishedSourceHead: "old", PublishOperation: "p", BodyOperation: "b", Clean: true},
		{ChangeID: "fine", SettledHead: "new", PublishedHead: "old", PublishedSourceHead: "old", PublishOperation: "p", BodyOperation: "b", Clean: true},
	}
	var mu sync.Mutex
	published, recorded := map[string]bool{}, map[string]string{}
	actions := ChangePublicationActions{
		PublishAndRefresh: func(_ context.Context, event ChangePublicationEvent, _ string) error {
			mu.Lock()
			defer mu.Unlock()
			published[event.ChangeID] = true
			if event.ChangeID == "broken" {
				return errors.New("publish_commit refused")
			}
			return nil
		},
		RecordFailure: func(_ context.Context, event ChangePublicationEvent, cause error) error {
			mu.Lock()
			defer mu.Unlock()
			recorded[event.ChangeID] = cause.Error()
			return nil
		},
	}
	if err := ProcessChangePublicationEvents(context.Background(), events, actions); err != nil {
		t.Fatalf("a recorded publication failure reached the scheduler: %v", err)
	}
	if !published["fine"] || recorded["broken"] != "publish_commit refused" || len(recorded) != 1 {
		t.Fatalf("published=%v recorded=%v", published, recorded)
	}
	actions.RecordFailure = func(context.Context, ChangePublicationEvent, error) error { return errors.New("store closed") }
	if err := ProcessChangePublicationEvents(context.Background(), events[:1], actions); err == nil || !strings.Contains(err.Error(), "store closed") {
		t.Fatalf("store failure while recording = %v", err)
	}
}

func TestProcessChangePublicationEventsRunsChangesConcurrently(t *testing.T) {
	events := []ChangePublicationEvent{
		{ChangeID: "one", SettledHead: "one-new", PublishedHead: "one-old", PublishedSourceHead: "one-old", PublishOperation: "p", BodyOperation: "b", Clean: true},
		{ChangeID: "two", SettledHead: "two-new", PublishedHead: "two-old", PublishedSourceHead: "two-old", PublishOperation: "p", BodyOperation: "b", Clean: true},
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
