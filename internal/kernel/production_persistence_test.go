package kernel

import (
	"context"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
)

func TestProductionPersistsFinalizedConstructionPublicationAndRebase(t *testing.T) {
	ctx := context.Background()
	proposal, err := NewSuccessProposal("published")
	if err != nil {
		t.Fatal(err)
	}
	store, finalizing := finalizingReleasedRun(t, RoleWorker, VerificationPolicy{}, proposal)
	defer store.Close()

	change, found, err := store.Change(ctx, *finalizing.ChangeID)
	if err != nil || !found || change.HeadCommit == nil {
		t.Fatalf("settled change = %+v, found=%v, err=%v", change, found, err)
	}
	settlement, err := NewRetainedChangeSettlement(change.Revision, change.HeadCommit)
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := store.FinalizeWorkerRun(ctx, finalizing.ID, finalizing.Revision, settlement, mustTime(t, 79))
	if err != nil {
		t.Fatal(err)
	}
	hexHead := hex.EncodeToString(change.HeadCommit.Bytes())
	branch := "factory/" + change.ID.String()[:12]
	pr := ProductionPullRequest{Number: 7, Title: "Ship the machine", URL: "https://github.com/example/factory/pull/7", Head: hexHead, Branch: branch, Base: "main", State: "open", Review: ProductionReview{Head: hexHead, State: "allow"}}
	observation := ProductionObservation{Repository: "Example/Factory", ObservedAt: 80, PullRequests: []ProductionPullRequest{pr}}
	if err := store.RecordProductionObservation(ctx, terminal.ProjectID, observation, mustTime(t, 80)); err != nil {
		t.Fatal(err)
	}
	page, err := store.Production(ctx, terminal.ProjectID, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	construction := productionRecord(t, page, "construction", "")
	if construction == nil || construction.VisualID != "change:"+change.ID.String() {
		t.Fatalf("finalized construction = %+v", construction)
	}
	identity := productionRecord(t, page, "pull_request", "7")
	if identity == nil || identity.VisualID != "change:"+change.ID.String() {
		t.Fatalf("publication candidate = %+v", identity)
	}

	if err := store.RecordPublication(ctx, terminal.ProjectID, terminal.TaskID, "example/factory", pr, mustTime(t, 81)); err != nil {
		t.Fatal(err)
	}
	page, err = store.Production(ctx, terminal.ProjectID, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	if productionRecord(t, page, "construction", "") != nil {
		t.Fatal("construction disappeared only after publication association was expected")
	}
	published := productionRecord(t, page, "pull_request", "7")
	if published == nil || published.VisualID != identity.VisualID || !containsString(published.Tasks, terminal.TaskID.String()) {
		t.Fatalf("published association = %+v", published)
	}

	rebased := pr
	rebased.Head = strings.Repeat("b", 40)
	rebased.Review.Head = rebased.Head
	rebased.Review.State = "changes_requested"
	observation.ObservedAt = 82
	observation.PullRequests = []ProductionPullRequest{rebased}
	if err := store.RecordProductionObservation(ctx, terminal.ProjectID, observation, mustTime(t, 82)); err != nil {
		t.Fatal(err)
	}
	page, err = store.Production(ctx, terminal.ProjectID, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	updated := productionRecord(t, page, "pull_request", "7")
	if updated == nil || updated.VisualID != published.VisualID || !containsString(updated.Tasks, terminal.TaskID.String()) {
		t.Fatalf("rebased association = %+v", updated)
	}

	second := pr
	second.Number = 8
	second.Title = "Follow-up machine"
	second.URL = "https://github.com/example/factory/pull/8"
	second.Branch = "feature/follow-up"
	second.Head = strings.Repeat("c", 40)
	second.Review.Head = second.Head
	if err := store.RecordPublication(ctx, terminal.ProjectID, terminal.TaskID, "example/factory", second, mustTime(t, 83)); err != nil {
		t.Fatal(err)
	}
	page, err = store.Production(ctx, terminal.ProjectID, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	other := productionRecord(t, page, "pull_request", "8")
	if other == nil || other.VisualID == published.VisualID || !containsString(other.Tasks, terminal.TaskID.String()) {
		t.Fatalf("second PR collapsed or lost task = %+v", other)
	}
}

func TestProductionSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	store, path := newTestStore(t)
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 250), Name: "production", Root: "/production"}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 251), IncarnationID: incarnationID(t, 252), ProjectID: project.ID, Title: "durable task"}, mustTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	pr := ProductionPullRequest{Number: 11, Title: "Durable PR", URL: "https://github.com/example/factory/pull/11", Head: strings.Repeat("d", 40), Branch: "feature/durable", Base: "main", State: "open", Review: ProductionReview{Head: strings.Repeat("d", 40), State: "unknown"}}
	if err := store.RecordPublication(ctx, project.ID, task.ID, "example/factory", pr, mustTime(t, 4)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	page, err := store.Production(ctx, project.ID, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	item := productionRecord(t, page, "pull_request", "11")
	if item == nil || !containsString(item.Tasks, task.ID.String()) {
		t.Fatalf("reopened production = %+v", item)
	}
}

func TestV32MigrationPreservesMissionBindingsAndStandaloneTasks(t *testing.T) {
	ctx := context.Background()
	store, path := newTestStore(t)
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 253), Name: "migration", Root: "/migration"}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 254), IncarnationID: incarnationID(t, 255), ProjectID: project.ID, Title: "mission anchor"}, mustTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	mission, err := store.WriteOutcome(ctx, NewOutcome{ID: outcomeID(t, 256), ProjectID: project.ID, Document: OutcomeDocument{Kind: "mission", Objective: "keep work", Criteria: "all rows survive", AnchorTaskID: anchor.ID.String(), AnchorWorkRevision: 1, State: "open"}}, 0, mustTime(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	standalone, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 257), IncarnationID: incarnationID(t, 258), ProjectID: project.ID, Title: "standalone"}, mustTime(t, 5))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.ExecContext(ctx, "DROP TABLE production_records; DROP TABLE publication_tasks; PRAGMA user_version = 32"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	retained, err := store.Outcome(ctx, project.ID, mission.ID, 0)
	if err != nil || !reflect.DeepEqual(retained, mission) {
		t.Fatalf("migrated mission = %+v, err=%v", retained, err)
	}
	items, next, err := store.ListMissionTasks(ctx, project.ID, mission.ID, 0, 8)
	if err != nil || next != 0 || len(items) != 1 || items[0].ID != anchor.ID {
		t.Fatalf("migrated mission tasks = %+v next=%d err=%v", items, next, err)
	}
	got, found, err := store.Task(ctx, standalone.ID)
	if err != nil || !found || got.ID != standalone.ID {
		t.Fatalf("migrated standalone = %+v found=%v err=%v", got, found, err)
	}
}

func productionRecord(t *testing.T, page ProductionPage, kind, id string) *ProductionRecord {
	t.Helper()
	for index := range page.Records {
		if page.Records[index].Kind == kind && (id == "" || page.Records[index].ID == id) {
			return &page.Records[index]
		}
	}
	return nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
