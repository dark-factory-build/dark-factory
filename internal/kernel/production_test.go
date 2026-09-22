package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestProductionObservationPersistsConfiguredDeliveryDestinations(t *testing.T) {
	store, _, project, _ := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	if err := store.RecordProductionObservation(context.Background(), project.ID, ProductionObservation{
		Repository: "example/factory", ObservedAt: 10, DeliveryDestinations: []string{"production"}, DeliveryDestinationsObserved: true,
	}, mustTime(t, 10)); err != nil {
		t.Fatal(err)
	}
	page, err := store.Production(context.Background(), project.ID, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range page.Records {
		if record.Kind != "repository" {
			continue
		}
		var document struct {
			DeliveryDestinations []string `json:"delivery_destinations"`
		}
		if err := json.Unmarshal(record.Document, &document); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(document.DeliveryDestinations, []string{"production"}) {
			t.Fatalf("destination fact = %#v", document.DeliveryDestinations)
		}
		return
	}
	t.Fatal("repository projection missing")
}

func TestProductionObservationPreservesIncompleteDeliveryConfiguration(t *testing.T) {
	store, _, project, _ := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	if err := store.RecordProductionObservation(context.Background(), project.ID, ProductionObservation{
		Repository: "example/factory", ObservedAt: 10, DeliveryDestinations: []string{"production"}, DeliveryDestinationsObserved: false,
	}, mustTime(t, 10)); err != nil {
		t.Fatal(err)
	}
	page, err := store.Production(context.Background(), project.ID, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range page.Records {
		if record.Kind != "repository" {
			continue
		}
		var document struct {
			DeliveryDestinations         []string `json:"delivery_destinations"`
			DeliveryDestinationsObserved bool     `json:"delivery_destinations_observed"`
		}
		if err := json.Unmarshal(record.Document, &document); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(document.DeliveryDestinations, []string{"production"}) || document.DeliveryDestinationsObserved {
			t.Fatalf("incomplete destination fact = %+v", document)
		}
		return
	}
	t.Fatal("repository projection missing")
}

func TestProductionMaintenanceRoundTripsAndInvalidObservationRollsBack(t *testing.T) {
	store, _, project, _ := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	ctx := context.Background()
	observation := ProductionObservation{Repository: "example/health", ObservedAt: 20, Unavailable: "jobs", Overflow: 3}
	if err := store.RecordProductionObservation(ctx, project.ID, observation, mustTime(t, 20)); err != nil {
		t.Fatal(err)
	}
	page, err := store.Production(ctx, project.ID, 0, 8)
	if err != nil || len(page.Records) != 1 {
		t.Fatalf("health page = %+v, err=%v", page, err)
	}
	var envelope struct {
		Unavailable string `json:"unavailable"`
		Overflow    int    `json:"overflow"`
	}
	if err := json.Unmarshal(page.Records[0].Document, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Unavailable != "jobs" || envelope.Overflow != 3 {
		t.Fatalf("health roundtrip = %+v", envelope)
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
			t.Fatalf("rolled-back health record = %+v", record)
		}
	}
}

func TestProductionPrioritizesLiveFactsOverTerminalConstruction(t *testing.T) {
	store, _, project, agent := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	ctx := context.Background()
	insert := func(kind, id, document string) {
		t.Helper()
		if _, err := store.writer.ExecContext(ctx, `INSERT INTO production_records(project_id, repository, kind, identity, visual_id, document, observed_at_ms) VALUES(?, 'example/factory', ?, ?, '', ?, 1)`, project.ID.Bytes(), kind, id, document); err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < 250; index++ {
		id := make([]byte, IDBytes)
		id[0] = byte(index + 1)
		base := "main"
		baseCommit := []byte(strings.Repeat("\x01", 20))
		head := []byte(strings.Repeat("\x01", 20))
		if index%3 == 1 {
			baseCommit = []byte(strings.Repeat("\x02", 20))
			head = []byte(strings.Repeat("\x01", 20))
		} else if index%3 == 2 {
			baseCommit = nil
			head = []byte(strings.Repeat("\x02", 20))
			head = nil
		}
		if _, err := store.writer.ExecContext(ctx, `INSERT INTO tasks(id, project_id, assigned_agent_id, incarnation_id, work_revision, title, body, status, priority, completed_at_ms, revision, created_at_ms, updated_at_ms) VALUES(?, ?, NULL, ?, 1, ?, '', 'cancelled', 0, ?, 1, 0, ?)`, id, project.ID.Bytes(), append([]byte{byte(index + 1)}, make([]byte, IDBytes-1)...), fmt.Sprintf("terminal-%03d", index), index+1, index+1); err != nil {
			t.Fatal(err)
		}
		if _, err := store.writer.ExecContext(ctx, `INSERT INTO task_repository_bindings(task_id, repository_id, base_ref) VALUES(?, ?, ?)`, id, project.ID.Bytes(), base); err != nil {
			t.Fatal(err)
		}
		var err error
		if baseCommit == nil {
			_, err = store.writer.ExecContext(ctx, `INSERT INTO changes(id, project_id, task_id, task_incarnation_id, phase, revision, created_at_ms, updated_at_ms) VALUES(?, ?, ?, ?, 'reserved', 1, 0, 2)`, id, project.ID.Bytes(), id, append([]byte{byte(index + 1)}, make([]byte, IDBytes-1)...))
		} else {
			_, err = store.writer.ExecContext(ctx, `INSERT INTO changes(id, project_id, task_id, task_incarnation_id, phase, object_format, base_commit, repository_dev, repository_inode, head_commit, prepared_at_ms, available_at_ms, revision, created_at_ms, updated_at_ms) VALUES(?, ?, ?, ?, 'available', 'sha1', ?, 1, 1, ?, 1, 2, 1, 0, 2)`, id, project.ID.Bytes(), id, append([]byte{byte(index + 1)}, make([]byte, IDBytes-1)...), baseCommit, head)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	insert("repository", "example/factory", `{"overflow":0}`)
	insert("pull_request", "7", `{"number":7,"title":"current","state":"open","head":"`+strings.Repeat("a", 40)+`","review":{"head":"`+strings.Repeat("a", 40)+`","state":"allow"}}`)
	insert("pull_request", "8", `{"number":8,"state":"merged"}`)
	insert("check", "workflow:7", `{"id":"workflow:7","state":"completed"}`)
	insert("delivery", "delivery:7", `{"id":"delivery:7","state":"verified"}`)
	page, err := store.Production(ctx, project.ID, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 255 || len(page.Records) != 8 {
		t.Fatalf("page total/size = %d/%d", page.Total, len(page.Records))
	}
	want := []string{"repository", "pull_request", "delivery", "pull_request", "check"}
	for index, kind := range want {
		if page.Records[index].Kind != kind {
			t.Fatalf("record %d = %+v, want %s", index, page.Records[index], kind)
		}
	}
	page, err = store.Production(ctx, project.ID, 2, 1)
	if err != nil || len(page.Records) != 1 || page.Records[0].Kind != "delivery" {
		t.Fatalf("delivery must precede merged work across pages: %+v, %v", page, err)
	}
	page, err = store.Production(ctx, project.ID, 5, 3)
	if err != nil || len(page.Records) != 3 {
		t.Fatalf("construction page = %+v, %v", page, err)
	}
	for index, want := range []any{false, true, nil} {
		var construction map[string]any
		if err := json.Unmarshal(page.Records[index].Document, &construction); err != nil {
			t.Fatal(err)
		}
		if construction["has_changes"] != want {
			t.Fatalf("construction %d has_changes = %#v, want %#v", index, construction["has_changes"], want)
		}
	}
	// More than a page of receipts must not displace queued/running/blocked work.
	for index := 0; index < 9; index++ {
		insert("delivery", fmt.Sprintf("delivery:%d", index+10), `{"state":"verified"}`)
	}
	for _, status := range []string{"queued", "running", "blocked"} {
		if _, err := store.writer.ExecContext(ctx, `UPDATE tasks SET assigned_agent_id = ?, status = ?, completed_at_ms = NULL, blocked_reason = CASE WHEN ? = 'blocked' THEN 'dependency_failed' ELSE NULL END WHERE title = 'terminal-000'`, agent.ID.Bytes(), status, status); err != nil {
			t.Fatal(err)
		}
		page, err = store.Production(ctx, project.ID, 0, 8)
		if err != nil || len(page.Records) != 8 || page.Records[2].Kind != "construction" {
			t.Fatalf("%s construction must precede deliveries: %+v, %v", status, page, err)
		}
	}

}

func TestProductionCanonicalizesLegacyRuntimeDestinationsAndDeduplicates(t *testing.T) {
	store, _, project, _ := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	ctx := context.Background()
	path := "runtime:/Users/example/.dark-factory"
	revision := strings.Repeat("a", 40)
	legacyID := path + ":release:" + revision
	legacy := ProductionDelivery{ID: legacyID, Kind: "release", Destination: path, Revision: revision, State: "verified", PullRequests: []uint64{7}}
	legacyBody, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	canonicalID := canonicalProductionRuntime(path) + ":release:" + revision
	newer := ProductionDelivery{ID: canonicalID, Kind: "release", Destination: canonicalProductionRuntime(path), Revision: revision, State: "blocked", PullRequests: []uint64{8}}
	newerBody, err := json.Marshal(newer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.ExecContext(ctx, `INSERT INTO production_records (project_id, repository, kind, identity, visual_id, document, observed_at_ms) VALUES (?, ?, 'delivery', ?, '', ?, ?)`, project.ID.Bytes(), "example/factory", legacyID, string(legacyBody), 10); err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.ExecContext(ctx, `INSERT INTO production_records (project_id, repository, kind, identity, visual_id, document, observed_at_ms) VALUES (?, ?, 'delivery', ?, '', ?, ?)`, project.ID.Bytes(), "example/factory", canonicalID, string(newerBody), 12); err != nil {
		t.Fatal(err)
	}
	before, err := store.Production(ctx, project.ID, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range before.Records {
		if strings.Contains(string(item.Document), "/Users/") || strings.Contains(item.ID, "/Users/") {
			t.Fatal("legacy read exposed a runtime path")
		}
	}
	// Migration must not replace a newer opaque receipt with legacy success.
	if err := store.RecordProductionObservation(ctx, project.ID, ProductionObservation{Repository: "example/factory", ObservedAt: 18}, mustTime(t, 18)); err != nil {
		t.Fatal(err)
	}
	var retained string
	if err := store.writer.QueryRowContext(ctx, `SELECT document FROM production_records WHERE project_id = ? AND kind = 'delivery'`, project.ID.Bytes()).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(retained, `"state":"blocked"`) {
		t.Fatal("legacy migration rewound newer evidence")
	}
	observation := ProductionObservation{Repository: "example/factory", ObservedAt: 20,
		Deliveries: []ProductionDelivery{{ID: legacyID, Kind: "release", Destination: path, Revision: revision, State: "verified", PullRequests: []uint64{7, 8}}}}
	if err := store.RecordProductionObservation(ctx, project.ID, observation, mustTime(t, 20)); err != nil {
		t.Fatal(err)
	}
	page, err := store.Production(ctx, project.ID, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, item := range page.Records {
		if strings.Contains(string(item.Document), path) || strings.Contains(item.ID, path) {
			t.Fatalf("legacy runtime path leaked: %+v", item)
		}
		if item.Kind == "delivery" {
			count++
			if item.ID != canonicalID {
				t.Fatalf("delivery identity = %q, want %q", item.ID, canonicalID)
			}
		}
	}
	if count != 1 {
		t.Fatalf("delivery count = %d, want one canonical row", count)
	}
	var stored string
	if err := store.writer.QueryRowContext(ctx, `SELECT document FROM production_records WHERE project_id = ? AND repository = ? AND kind = 'delivery' AND identity = ?`, project.ID.Bytes(), "example/factory", canonicalID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored, `"pull_requests":[7,8]`) {
		t.Fatalf("canonical delivery lost membership: %s", stored)
	}
}
