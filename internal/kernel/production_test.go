package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	sqliteDriver "github.com/ncruces/go-sqlite3/driver"
)

func TestProductionReviewUpsertsBeforeRefresh(t *testing.T) {
	store, _, project, _ := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	ctx := context.Background()
	head := strings.Repeat("a", 40)
	if err := store.RecordProductionReview(ctx, project.ID, "example/factory", 7, ProductionReview{Head: head, State: "allow"}, mustTime(t, 10)); err != nil {
		t.Fatal(err)
	}
	page, err := store.Production(ctx, project.ID, 0, 8, UnixMillis{})
	if err != nil || len(page.Records) != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	var pull ProductionPullRequest
	if err := json.Unmarshal(page.Records[0].Document, &pull); err != nil {
		t.Fatal(err)
	}
	if pull.Number != 7 || pull.Head != head || pull.Review.Head != head || pull.Review.State != "allow" {
		t.Fatalf("pull=%+v", pull)
	}
}

// A refresh reads the known reviews, waits on the remote, then writes a
// snapshot carrying that copy. A verdict recorded during the wait must
// survive the later write.
func TestProductionObservationKeepsAReviewRecordedDuringTheRefresh(t *testing.T) {
	store, _, project, _ := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	ctx := context.Background()
	head := strings.Repeat("a", 40)
	pr := ProductionPullRequest{Number: 7, Title: "A machine", Head: head, State: "open"}
	snapshot := ProductionObservation{Repository: "example/factory", ObservedAt: 10, PullRequests: []ProductionPullRequest{pr}}
	if err := store.RecordProductionObservation(ctx, project.ID, snapshot, mustTime(t, 10)); err != nil {
		t.Fatal(err)
	}
	// The refresh has read its snapshot (no review); the verdict lands now.
	if err := store.RecordProductionReview(ctx, project.ID, "example/factory", 7, ProductionReview{Head: head, State: "allow"}, mustTime(t, 11)); err != nil {
		t.Fatal(err)
	}
	snapshot.ObservedAt = 12
	if err := store.RecordProductionObservation(ctx, project.ID, snapshot, mustTime(t, 12)); err != nil {
		t.Fatal(err)
	}
	page, err := store.Production(ctx, project.ID, 0, 8, UnixMillis{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Records {
		if item.Kind != "pull_request" {
			continue
		}
		var got ProductionPullRequest
		if err := json.Unmarshal(item.Document, &got); err != nil {
			t.Fatal(err)
		}
		if got.Review.Head != head || got.Review.State != "allow" {
			t.Fatalf("review erased by the stale snapshot: %+v", got.Review)
		}
		return
	}
	t.Fatal("pull request record missing")
}

func TestProductionReviewBlockSurvivesASameHeadAllow(t *testing.T) {
	store, _, project, _ := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	ctx := context.Background()
	head := strings.Repeat("a", 40)
	block := ProductionReview{Head: head, State: "block", Findings: "fix the exact finding", OperationID: "block-operation"}
	if err := store.RecordProductionReview(ctx, project.ID, "example/factory", 7, block, mustTime(t, 10)); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordProductionReview(ctx, project.ID, "example/factory", 7, ProductionReview{Head: head, State: "allow", OperationID: "plain-allow"}, mustTime(t, 11)); err != nil {
		t.Fatal(err)
	}
	page, err := store.Production(ctx, project.ID, 0, 8, UnixMillis{})
	if err != nil {
		t.Fatal(err)
	}
	var pull ProductionPullRequest
	for _, record := range page.Records {
		if record.Kind == "pull_request" {
			if err := json.Unmarshal(record.Document, &pull); err != nil {
				t.Fatal(err)
			}
		}
	}
	if pull.Review.State != "block" || pull.Review.Findings != block.Findings || pull.Review.OperationID != block.OperationID {
		t.Fatalf("plain allow replaced block: %+v", pull.Review)
	}
}

func TestProductionReviewIdentitylessBlockSurvivesPlainAllow(t *testing.T) {
	store, _, project, _ := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	ctx := context.Background()
	head := strings.Repeat("b", 40)
	block := ProductionReview{Head: head, State: "block", Findings: "native finding"}
	if err := store.RecordProductionReview(ctx, project.ID, "example/factory", 8, block, mustTime(t, 20)); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordProductionReview(ctx, project.ID, "example/factory", 8, ProductionReview{Head: head, State: "allow", OperationID: "plain-allow"}, mustTime(t, 21)); err != nil {
		t.Fatal(err)
	}
	page, err := store.Production(ctx, project.ID, 0, 8, UnixMillis{})
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range page.Records {
		if record.Kind != "pull_request" || record.ID != "8" {
			continue
		}
		var pull ProductionPullRequest
		if err := json.Unmarshal(record.Document, &pull); err != nil {
			t.Fatal(err)
		}
		if pull.Review.State != "block" || pull.Review.Findings != block.Findings || pull.Review.OperationID != "" {
			t.Fatalf("plain allow replaced identity-less block: %+v", pull.Review)
		}
		return
	}
	t.Fatal("pull request record missing")
}

func TestCorrectedProductionHeadStoresRecoverableReviewClaimAtomically(t *testing.T) {
	store, path, project, _ := newAdmissionStore(t, RoleOrchestrator, 2)
	ctx := context.Background()
	oldHead := strings.Repeat("a", 40)
	newHead := strings.Repeat("b", 40)
	old := ProductionObservation{Repository: "example/factory", ObservedAt: 10, PullRequests: []ProductionPullRequest{{Number: 7, Title: "A machine", Head: oldHead, State: "open"}}}
	if err := store.RecordProductionObservation(ctx, project.ID, old, mustTime(t, 10)); err != nil {
		store.Close()
		t.Fatal(err)
	}
	operationID := "corrected-review"
	// A claim persisted as gating, before the pre-review gate was removed, recovers like a running one.
	operation := map[string]any{"id": operationID, "state": "gating", "request": map[string]any{"repository": "example/factory", "head": newHead}}
	corrected := ProductionObservation{Repository: "example/factory", ObservedAt: 20, PullRequests: []ProductionPullRequest{{Number: 7, Title: "A machine", Head: newHead, State: "open"}}}
	if err := store.RecordProductionObservationWithReviewOperations(ctx, project.ID, corrected, []ProductionReviewOperation{{ID: operationID, Document: operation}}, mustTime(t, 20)); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if count, err := restarted.RecoverRunningReviewOperations(ctx, mustTime(t, 21)); err != nil || count != 1 {
		t.Fatalf("recovered corrected review count=%d err=%v", count, err)
	}
	page, err := restarted.Production(ctx, project.ID, 0, 8, UnixMillis{})
	if err != nil {
		t.Fatal(err)
	}
	var foundHead, foundState string
	for _, record := range page.Records {
		switch record.Kind {
		case "pull_request":
			var pull ProductionPullRequest
			if err := json.Unmarshal(record.Document, &pull); err != nil {
				t.Fatal(err)
			}
			foundHead = pull.Head
		case "reviewer":
			var claim map[string]any
			if err := json.Unmarshal(record.Document, &claim); err != nil {
				t.Fatal(err)
			}
			foundState, _ = claim["state"].(string)
		}
	}
	if foundHead != newHead || foundState != "running" { // no verdict yet: relaunched at startup
		t.Fatalf("corrected head/review after restart = %q/%q", foundHead, foundState)
	}
}

func TestRequestChangesReviewRecoveryPreservesRoutePending(t *testing.T) {
	store, _, project, _ := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	ctx := context.Background()
	operationID := "request-changes-recovery"
	operation := map[string]any{
		"id":        operationID,
		"state":     "running",
		"verdict":   "request_changes",
		"submitted": true,
		"request": map[string]any{
			"repository":  "example/factory",
			"pull_number": 7,
			"head":        strings.Repeat("a", 40),
			"base":        strings.Repeat("b", 40),
			"base_ref":    "main",
			"body":        "review",
			"provider":    "codex",
		},
	}
	if err := store.RecordReviewOperation(ctx, project.ID, "example/factory", operationID, operation, mustTime(t, 10)); err != nil {
		t.Fatal(err)
	}
	if count, err := store.RecoverRunningReviewOperations(ctx, mustTime(t, 11)); err != nil || count != 1 {
		t.Fatalf("recovered request-changes count=%d err=%v", count, err)
	}
	document, found, err := store.ReviewOperation(ctx, project.ID, operationID)
	if err != nil || !found {
		t.Fatalf("recovered request-changes document found=%v err=%v", found, err)
	}
	var recovered map[string]any
	if err := json.Unmarshal(document, &recovered); err != nil {
		t.Fatal(err)
	}
	if recovered["state"] != "completed" || recovered["route_pending"] != true {
		t.Fatalf("recovered request-changes operation=%v", recovered)
	}
	pending, err := store.InFlightReviewOperations(ctx)
	if err != nil || len(pending) != 1 || pending[0].ID != operationID {
		t.Fatalf("pending request-changes operations=%+v err=%v", pending, err)
	}
}

func TestRequestChangesBeforeSubmitDoesNotBecomeRoutePending(t *testing.T) {
	store, _, project, _ := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	ctx := context.Background()
	operationID := "request-changes-before-submit"
	operation := map[string]any{
		"id":      operationID,
		"state":   "running",
		"verdict": "request_changes",
		"request": map[string]any{
			"repository":  "example/factory",
			"pull_number": 7,
			"head":        strings.Repeat("a", 40),
			"base":        strings.Repeat("b", 40),
			"base_ref":    "main",
			"body":        "review",
			"provider":    "codex",
		},
	}
	if err := store.RecordReviewOperation(ctx, project.ID, "example/factory", operationID, operation, mustTime(t, 10)); err != nil {
		t.Fatal(err)
	}
	if count, err := store.RecoverRunningReviewOperations(ctx, mustTime(t, 11)); err != nil || count != 1 {
		t.Fatalf("recovered pre-submit count=%d err=%v", count, err)
	}
	document, found, err := store.ReviewOperation(ctx, project.ID, operationID)
	if err != nil || !found {
		t.Fatalf("recovered pre-submit document found=%v err=%v", found, err)
	}
	var recovered map[string]any
	if err := json.Unmarshal(document, &recovered); err != nil {
		t.Fatal(err)
	}
	if recovered["state"] != "failed" || recovered["route_pending"] == true {
		t.Fatalf("pre-submit operation became routable=%v", recovered)
	}
	if pending, err := store.InFlightReviewOperations(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("pre-submit pending operations=%+v err=%v", pending, err)
	}
}

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
	if err := store.RecordProductionReview(ctx, project.ID, "example/factory", 7, ProductionReview{Head: head, State: "block", Findings: strings.Repeat("f", 8193)}, mustTime(t, 10)); err != nil {
		t.Fatalf("review findings boundary: %v", err)
	}
	if err := store.RecordProductionReview(ctx, project.ID, "example/factory", 7, ProductionReview{Head: head, State: "block", Findings: strings.Repeat("f", 16001)}, mustTime(t, 10)); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("oversized review findings = %v", err)
	}
	page, err := store.Production(ctx, project.ID, 0, 8, UnixMillis{})
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
	page, err = store.Production(ctx, project.ID, 0, 8, UnixMillis{})
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
		if item.VisualID != identity || current.Head != strings.Repeat("b", 40) || current.Review.Head != strings.Repeat("b", 40) || current.Review.State != "unknown" {
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
	page, err = store.Production(ctx, project.ID, 0, 8, UnixMillis{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Records {
		if item.Kind == "pull_request" && strings.Contains(string(item.Document), "must roll back") {
			t.Fatal("partial observation survived")
		}
	}
}

func TestProductionHealthRoundTripsAndInvalidObservationRollsBack(t *testing.T) {
	store, _, project, _ := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	ctx := context.Background()
	observation := ProductionObservation{Repository: "example/health", ObservedAt: 20, Unavailable: "jobs", Overflow: 3}
	if err := store.RecordProductionObservation(ctx, project.ID, observation, mustTime(t, 20)); err != nil {
		t.Fatal(err)
	}
	page, err := store.Production(ctx, project.ID, 0, 8, UnixMillis{})
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
	page, err = store.Production(ctx, project.ID, 0, 8, UnixMillis{})
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range page.Records {
		if record.Repository == "example/rollback" {
			t.Fatalf("rolled-back health record = %+v", record)
		}
	}
}

func TestProductionReadsTheRelevantSetWithoutACap(t *testing.T) {
	store, _, project, agent := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UnixMilli()
	stale := now - ProductionRecent.Milliseconds() - 60_000
	insert := func(kind, id, document string, at int64) {
		t.Helper()
		if _, err := store.writer.ExecContext(ctx, `INSERT INTO production_records(project_id, repository, kind, identity, visual_id, document, observed_at_ms) VALUES(?, 'example/factory', ?, ?, '', ?, ?)`, project.ID.Bytes(), kind, id, document, at); err != nil {
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
	// Recently updated construction is relevant whatever its status.
	if _, err := store.writer.ExecContext(ctx, `UPDATE changes SET updated_at_ms = ? WHERE id IN (SELECT id FROM changes ORDER BY id LIMIT 3)`, now); err != nil {
		t.Fatal(err)
	}
	insert("repository", "example/factory", `{"overflow":0}`, stale)
	insert("pull_request", "7", `{"number":7,"title":"current","state":"open"}`, stale)
	insert("check", "workflow:7", `{"id":"workflow:7","state":"completed","pull_requests":[7]}`, stale)
	insert("reviewer", "reviewer:7", `{"number":7,"state":"allow"}`, stale)
	insert("reviewer", "operation:7", `{"id":"operation:7","request":{"PullNumber":7}}`, stale)
	insert("pull_request", "8", `{"number":8,"state":"merged"}`, stale)
	insert("check", "workflow:8", `{"state":"completed","pull_requests":[8]}`, stale)
	insert("reviewer", "reviewer:8", `{"number":8,"state":"allow"}`, stale)
	insert("delivery", "delivery:8", `{"state":"verified","pull_requests":[8]}`, stale)
	insert("pull_request", "9", `{"number":9,"state":"merged"}`, now)
	insert("check", "workflow:9", `{"state":"completed","pull_requests":[9]}`, stale)
	insert("reviewer", "reviewer:9", `{"number":9,"state":"allow"}`, stale)
	insert("delivery", "delivery:9", `{"state":"verified","pull_requests":[8,9]}`, stale)
	// A delivery still running keeps its merged PR relevant.
	insert("pull_request", "10", `{"number":10,"state":"merged"}`, stale)
	insert("delivery", "delivery:10", `{"state":"running","pull_requests":[10]}`, stale)
	for index := 0; index < 300; index++ {
		insert("delivery", fmt.Sprintf("old:%d", index), `{"state":"verified","pull_requests":[]}`, stale)
		insert("pull_request", fmt.Sprint(1000+index), fmt.Sprintf(`{"number":%d,"state":"merged"}`, 1000+index), stale)
		// More relevant records than the old 256-record ceiling.
		insert("pull_request", fmt.Sprint(2000+index), fmt.Sprintf(`{"number":%d,"state":"merged"}`, 2000+index), now-int64(index))
	}
	// A blocked delivery stays until a later verified one at its destination.
	insert("delivery", "blocked:old", `{"kind":"release","destination":"site:a","state":"blocked","updated_at":100,"pull_requests":[]}`, stale)
	insert("delivery", "verified:a", `{"kind":"release","destination":"site:a","state":"verified","updated_at":200,"pull_requests":[]}`, stale)
	insert("delivery", "blocked:live", `{"kind":"release","destination":"site:b","state":"blocked","updated_at":300,"pull_requests":[]}`, stale)
	insert("delivery", "verified:b", `{"kind":"release","destination":"site:b","state":"verified","updated_at":200,"pull_requests":[]}`, stale)
	// An unaccepted mission keeps its old PR; an accepted one lets it age out.
	insert("pull_request", "11", `{"number":11,"state":"merged"}`, stale)
	insert("check", "workflow:11", `{"state":"completed","pull_requests":[11]}`, stale)
	insert("pull_request", "12", `{"number":12,"state":"merged"}`, stale)
	for index, mission := range []struct{ state []string }{{[]string{"open"}}, {[]string{"open", "accepted"}}} {
		missionID, task := append([]byte{0xee, byte(index)}, make([]byte, IDBytes-2)...), append([]byte{byte(index + 5)}, make([]byte, IDBytes-1)...)
		for revision, state := range mission.state {
			if _, err := store.writer.ExecContext(ctx, `INSERT INTO project_outcome_revisions(id, project_id, revision, document, author, authority, objective_hash, objective_work_revision, created_at_ms) VALUES(?, ?, ?, ?, 'test', 'human', ?, 1, 0)`, missionID, project.ID.Bytes(), revision+1, `{"kind":"mission","state":"`+state+`"}`, make([]byte, 32)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.writer.ExecContext(ctx, `INSERT INTO mission_task_bindings(mission_id, task_id, project_id, created_at_ms) VALUES(?, ?, ?, 0)`, missionID, task, project.ID.Bytes()); err != nil {
			t.Fatal(err)
		}
		if _, err := store.writer.ExecContext(ctx, `INSERT INTO publication_tasks(project_id, repository, pull_number, task_id, created_at_ms) VALUES(?, 'example/factory', ?, ?, 0)`, project.ID.Bytes(), 11+index, task); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.writer.ExecContext(ctx, `UPDATE tasks SET assigned_agent_id = ?, status = 'blocked', completed_at_ms = NULL, blocked_reason = 'dependency_failed' WHERE title = 'terminal-100'`, agent.ID.Bytes()); err != nil {
		t.Fatal(err)
	}
	read := func() ([]ProductionRecord, int) {
		t.Helper()
		var records []ProductionRecord
		for offset := 0; ; {
			page, err := store.Production(ctx, project.ID, offset, 8, mustTime(t, now))
			if err != nil {
				t.Fatal(err)
			}
			records = append(records, page.Records...)
			if page.NextOffset == 0 {
				return records, page.Total
			}
			offset = page.NextOffset
		}
	}
	records, total := read()
	got := map[string]bool{}
	for _, record := range records {
		if record.Kind != "construction" && !strings.HasPrefix(record.ID, "2") {
			got[record.Kind+":"+record.ID] = true
		}
	}
	want := []string{"repository:example/factory", "pull_request:7", "check:workflow:7", "reviewer:reviewer:7", "reviewer:operation:7", "pull_request:9", "check:workflow:9", "reviewer:reviewer:9", "delivery:delivery:9", "pull_request:10", "delivery:delivery:10", "delivery:blocked:live", "pull_request:11", "check:workflow:11"}
	for _, key := range want {
		if !got[key] {
			t.Fatalf("relevant record %s missing from %v", key, got)
		}
	}
	if len(got) != len(want) || total != len(records) || total != len(want)+4+300 {
		t.Fatalf("relevant set = %v (%d records, total %d), want %v plus 4 constructions and 300 recent PRs", got, len(records), total, want)
	}
	var constructions []map[string]any
	for _, record := range records {
		if record.Kind == "construction" {
			var construction map[string]any
			if err := json.Unmarshal(record.Document, &construction); err != nil {
				t.Fatal(err)
			}
			constructions = append(constructions, construction)
		}
	}
	if constructions[3]["status"] != "blocked" {
		t.Fatalf("stale blocked construction missing: %+v", constructions)
	}
	for index, want := range []any{false, true, nil} {
		if constructions[index]["has_changes"] != want {
			t.Fatalf("construction %d has_changes = %#v, want %#v", index, constructions[index]["has_changes"], want)
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
	before, err := store.Production(ctx, project.ID, 0, 8, UnixMillis{})
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
	page, err := store.Production(ctx, project.ID, 0, 8, UnixMillis{})
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

func TestCorrectedHeadSupersedesOlderInFlightReview(t *testing.T) {
	store, _, project, _ := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	ctx := context.Background()
	oldHead, newHead := strings.Repeat("a", 40), strings.Repeat("b", 40)
	old := ProductionObservation{Repository: "example/factory", ObservedAt: 10, PullRequests: []ProductionPullRequest{{Number: 7, Title: "A machine", Head: oldHead, State: "open"}}}
	if err := store.RecordProductionObservation(ctx, project.ID, old, mustTime(t, 10)); err != nil {
		t.Fatal(err)
	}
	stale := map[string]any{"id": "stale", "state": "enqueued", "request": map[string]any{"Repository": "example/factory", "PullNumber": 7, "Head": oldHead}}
	other := map[string]any{"id": "other", "state": "enqueued", "request": map[string]any{"Repository": "example/factory", "PullNumber": 8, "Head": oldHead}}
	for id, op := range map[string]any{"stale": stale, "other": other} {
		if err := store.RecordReviewOperation(ctx, project.ID, "example/factory", id, op, mustTime(t, 11)); err != nil {
			t.Fatal(err)
		}
	}
	fresh := map[string]any{"id": "fresh", "state": "running", "request": map[string]any{"Repository": "example/factory", "PullNumber": 7, "Head": newHead}}
	corrected := ProductionObservation{Repository: "example/factory", ObservedAt: 20, PullRequests: []ProductionPullRequest{{Number: 7, Title: "A machine", Head: newHead, State: "open"}}}
	if err := store.RecordProductionObservationWithReviewOperations(ctx, project.ID, corrected, []ProductionReviewOperation{{ID: "fresh", Document: fresh}}, mustTime(t, 20)); err != nil {
		t.Fatal(err)
	}
	pending, err := store.InFlightReviewOperations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var live []string
	for _, op := range pending {
		live = append(live, op.ID)
	}
	if strings.Join(live, ",") != "fresh,other" && strings.Join(live, ",") != "other,fresh" {
		t.Fatalf("in-flight=%v, want the corrected head's claim and the untouched pull", live)
	}
	if err := store.RecordReviewOperation(ctx, project.ID, "example/factory", "stale", stale, mustTime(t, 21)); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("revive superseded err=%v, want ErrSuperseded", err)
	}
	document, _, err := store.ReviewOperation(ctx, project.ID, "stale")
	if err != nil || !strings.Contains(string(document), `"state":"superseded"`) {
		t.Fatalf("stale=%s err=%v", document, err)
	}
	// A pull with no factory task (factoryctl review) records its corrected
	// head through the plain observation path.
	plain := ProductionObservation{Repository: "example/factory", ObservedAt: 30, PullRequests: []ProductionPullRequest{{Number: 8, Title: "By hand", Head: newHead, State: "open"}}}
	if err := store.RecordProductionObservation(ctx, project.ID, plain, mustTime(t, 30)); err != nil {
		t.Fatal(err)
	}
	if document, _, err = store.ReviewOperation(ctx, project.ID, "other"); err != nil || !strings.Contains(string(document), `"state":"superseded"`) {
		t.Fatalf("plain-path other=%s err=%v", document, err)
	}
}

// The refresh runs every five minutes over every production record; paging
// the UI view cost a sorted UNION per eight rows and outran its deadline.
func TestKnownProductionPullsIsOneBoundedQuery(t *testing.T) {
	store, _, project, _ := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	ctx := context.Background()
	task, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 131), ProjectID: project.ID, IncarnationID: incarnationID(t, 132), Title: "publisher"}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	store.readers.SetMaxOpenConns(1)
	connection, err := store.readers.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	statements := 0
	if err := connection.Raw(func(driverConnection any) error {
		return driverConnection.(sqliteDriver.Conn).Raw().Trace(sqlite3.TRACE_STMT, func(sqlite3.TraceEvent, any, any) error {
			statements++
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	connection.Close()
	read := func(want int) (int, map[uint64]bool) {
		t.Helper()
		statements = 0
		pulls, published, err := store.KnownProductionPulls(ctx, project.ID, "Example/Factory", 100)
		if err != nil || len(pulls) != want {
			t.Fatalf("pulls=%d want=%d err=%v", len(pulls), want, err)
		}
		for _, pull := range pulls {
			if pull.State != "open" {
				t.Fatalf("non-open pull %+v", pull)
			}
		}
		return statements, published
	}
	observe := func(at int64, first, count uint64) {
		t.Helper()
		observation := ProductionObservation{Repository: "example/factory", ObservedAt: at}
		for number := first; number < first+count; number++ {
			state := "open"
			if number%2 == 0 {
				state = "merged"
			}
			observation.PullRequests = append(observation.PullRequests, ProductionPullRequest{Number: number, Title: "pull", Head: strings.Repeat("a", 40), State: state})
		}
		if err := store.RecordProductionObservation(ctx, project.ID, observation, mustTime(t, at)); err != nil {
			t.Fatal(err)
		}
	}
	observe(10, 1, 2)
	few, _ := read(1)
	for batch := uint64(0); batch < 6; batch++ {
		observe(int64(11+batch), 3+batch*200, 200)
	}
	if _, err := store.writer.ExecContext(ctx, `INSERT INTO publication_tasks (project_id, repository, pull_number, task_id, created_at_ms) VALUES (?, 'example/factory', 3, ?, 20)`, project.ID.Bytes(), task.ID.Bytes()); err != nil {
		t.Fatal(err)
	}
	many, published := read(100)
	if few == 0 || many != few || len(published) != 1 || !published[3] {
		t.Fatalf("statements few=%d many=%d published=%v", few, many, published)
	}
}

// A pull head is settled once every stored head check on it has completed
// without failing; one running or failed check, or a merge-group run, never
// settles it.
func TestSettledProductionChecks(t *testing.T) {
	store, _, project, _ := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	ctx := context.Background()
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	check := func(id, revision, scope, state string, pull uint64) ProductionCheck {
		conclusion := ""
		if state == "completed" {
			conclusion = "success"
		}
		return ProductionCheck{ID: id, Name: "go", Revision: revision, Scope: scope, State: state, Conclusion: conclusion, URL: "https://github.com/o/r/runs/" + id, PullRequests: []uint64{pull}, Jobs: []ProductionJob{}}
	}
	observation := ProductionObservation{Repository: "example/factory", ObservedAt: 10, Checks: []ProductionCheck{
		check("1", a, "head", "completed", 1), check("2", a, "head", "completed", 1),
		check("3", b, "head", "completed", 2), check("4", b, "head", "in_progress", 2),
		check("5", a, "merge_group", "completed", 3),
	}}
	for index, conclusion := range []string{"failure", "timed_out", "cancelled", "action_required", "startup_failure"} {
		failed := check(fmt.Sprint(10+index), b, "head", "completed", uint64(4+index))
		failed.Conclusion = conclusion
		observation.Checks = append(observation.Checks, check(fmt.Sprint(20+index), b, "head", "completed", uint64(4+index)), failed)
	}
	if err := store.RecordProductionObservation(ctx, project.ID, observation, mustTime(t, 10)); err != nil {
		t.Fatal(err)
	}
	settled, err := store.SettledProductionChecks(ctx, project.ID, "Example/Factory")
	if err != nil || len(settled) != 1 || !settled[ProductionHead{Number: 1, Head: a}] {
		t.Fatalf("settled %v, %v", settled, err)
	}
}

// Deployment records, once seen, survive a refresh that could not read them
// and only move forward, so a failed read never ships or unships a merge.
func TestProductionRepositoryKeepsItsNewestDeployment(t *testing.T) {
	store, _, project, _ := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	ctx := context.Background()
	deployed := func(at int64) *int64 { return &at }
	var got []string
	for index, value := range []*int64{nil, deployed(0), deployed(7), nil, deployed(5)} {
		at := int64(10 + index)
		if err := store.RecordProductionObservation(ctx, project.ID, ProductionObservation{Repository: "example/factory", ObservedAt: at, DeployedAt: value}, mustTime(t, at)); err != nil {
			t.Fatal(err)
		}
		page, err := store.Production(ctx, project.ID, 0, 8, UnixMillis{})
		if err != nil || len(page.Records) != 1 {
			t.Fatalf("page=%+v err=%v", page, err)
		}
		var health struct {
			DeployedAt *int64 `json:"deployed_at"`
		}
		if err := json.Unmarshal(page.Records[0].Document, &health); err != nil {
			t.Fatal(err)
		}
		if health.DeployedAt == nil {
			got = append(got, "none")
		} else {
			got = append(got, fmt.Sprint(*health.DeployedAt))
		}
	}
	if strings.Join(got, " ") != "none 0 7 7 7" {
		t.Fatalf("deployed_at over refreshes = %v", got)
	}
}
