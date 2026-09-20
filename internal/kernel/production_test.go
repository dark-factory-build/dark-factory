package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestProductionPersistsRevisionEvidenceWithoutRewinding(t *testing.T) {
	store, _, project, _ := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	ctx := context.Background()
	head := strings.Repeat("a", 40)
	pr := ProductionPullRequest{Number: 7, Title: "A machine", Head: head, State: "open", Review: ProductionReview{Head: head, State: "allow"}}
	observation := ProductionObservation{Repository: "Example/Factory", ObservedAt: 10, PullRequests: []ProductionPullRequest{pr}, Checks: []ProductionCheck{{ID: "workflow:12", Name: "CI", Revision: head, Scope: "head", State: "completed", Conclusion: "success", PullRequests: []uint64{7, 8}}}}
	if err := store.RecordProductionObservation(ctx, project.ID, observation, mustTime(t, 10)); err != nil {
		t.Fatal(err)
	}
	page, err := store.Production(ctx, project.ID, 0, 8)
	if err != nil || len(page.Records) != 3 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	identity := ""
	for _, item := range page.Records {
		if item.Kind == "pull_request" {
			identity = item.VisualID
		}
	}
	observation.ObservedAt = 12
	observation.PullRequests[0].Head = strings.Repeat("b", 40)
	if err := store.RecordProductionObservation(ctx, project.ID, observation, mustTime(t, 12)); err != nil {
		t.Fatal(err)
	}
	// A delayed old snapshot cannot put the old head back. Review/check evidence
	// stays attached to its actual revision, never rewritten to the new head.
	observation.ObservedAt = 11
	observation.PullRequests[0].Head = head
	if err := store.RecordProductionObservation(ctx, project.ID, observation, mustTime(t, 13)); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordProductionObservation(ctx, project.ID, ProductionObservation{Repository: "example/factory", ObservedAt: 14, Unavailable: "read failed"}, mustTime(t, 14)); err != nil {
		t.Fatal(err)
	}
	page, err = store.Production(ctx, project.ID, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	checks := 0
	for _, item := range page.Records {
		if item.Kind == "check" {
			checks++
		}
		if item.Kind != "pull_request" {
			continue
		}
		var current ProductionPullRequest
		if err := json.Unmarshal(item.Document, &current); err != nil {
			t.Fatal(err)
		}
		if item.VisualID != identity || current.Head != strings.Repeat("b", 40) || current.Review.Head != head {
			t.Fatalf("lost identity/revision: %+v %+v", item, current)
		}
	}
	if checks != 1 {
		t.Fatalf("shared execution duplicated: %d", checks)
	}
	// A bad member invalidates the whole observation, not just that member.
	observation.ObservedAt = 15
	observation.PullRequests[0].Title = "must roll back"
	observation.Checks[0].PullRequests = []uint64{0}
	if err := store.RecordProductionObservation(ctx, project.ID, observation, mustTime(t, 15)); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("bad input=%v", err)
	}
	page, err = store.Production(ctx, project.ID, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Records {
		if item.Kind == "pull_request" && strings.Contains(string(item.Document), "must roll back") {
			t.Fatal("partial observation survived")
		}
	}
}

func TestProductionMaintenanceRoundTripsAndInvalidObservationRollsBack(t *testing.T) {
	store, _, project, _ := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	ctx := context.Background()
	maintenance := &ProductionMaintenance{Destination: "production", State: "verified"}
	maintenance.Available.Version = "0.5.0"
	maintenance.Available.URL = "https://example.test/releases/0.5.0"
	maintenance.Available.State = "available"
	maintenance.Installed = ProductionBuild{Version: "0.4.0", Source: strings.Repeat("a", 40), Target: "darwin/arm64", BuildID: strings.Repeat("b", 64), Release: true, State: "installed"}
	maintenance.Running = ProductionBuild{Version: "0.4.0", Source: strings.Repeat("a", 40), Target: "darwin/arm64", BuildID: strings.Repeat("b", 64), Release: true, State: "ready"}
	observation := ProductionObservation{Repository: "example/maintenance", ObservedAt: 20, Maintenance: maintenance}
	if err := store.RecordProductionObservation(ctx, project.ID, observation, mustTime(t, 20)); err != nil {
		t.Fatal(err)
	}
	page, err := store.Production(ctx, project.ID, 0, 8)
	if err != nil || len(page.Records) != 1 {
		t.Fatalf("maintenance page = %+v, err=%v", page, err)
	}
	var envelope struct {
		Maintenance ProductionMaintenance `json:"maintenance"`
	}
	if err := json.Unmarshal(page.Records[0].Document, &envelope); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(envelope.Maintenance, *maintenance) {
		t.Fatalf("maintenance roundtrip = %+v, want %+v", envelope.Maintenance, *maintenance)
	}

	bad := observation
	bad.Repository = "example/rollback"
	bad.ObservedAt = 21
	head := strings.Repeat("c", 40)
	bad.PullRequests = []ProductionPullRequest{{Number: 9, Title: "will roll back", Head: head, State: "open", Review: ProductionReview{Head: head, State: "unknown"}}}
	bad.Checks = []ProductionCheck{{ID: "invalid", Name: "CI", Revision: head, Scope: "head", PullRequests: []uint64{0}}}
	if err := store.RecordProductionObservation(ctx, project.ID, bad, mustTime(t, 21)); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("invalid observation = %v", err)
	}
	page, err = store.Production(ctx, project.ID, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range page.Records {
		if record.Repository == "example/rollback" {
			t.Fatalf("rolled-back maintenance record = %+v", record)
		}
	}
}
