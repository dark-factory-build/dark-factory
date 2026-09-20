package kernel

import (
	"context"
	"reflect"
	"testing"
)

func TestV31MigrationPreservesMissionAndStandaloneWork(t *testing.T) {
	ctx := context.Background()
	store, path := newTestStore(t)
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 180), Name: "missions", Root: "/missions"}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 181), IncarnationID: incarnationID(t, 182), ProjectID: project.ID, Title: "existing work"}, mustTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	mission, err := store.WriteOutcome(ctx, NewOutcome{ID: outcomeID(t, 183), ProjectID: project.ID, Document: OutcomeDocument{Kind: "mission", Objective: "existing objective", Criteria: "existing criteria", AnchorTaskID: task.ID.String(), AnchorWorkRevision: 1, State: "open"}}, 0, mustTime(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.ExecContext(ctx, "DROP TABLE mission_task_bindings; PRAGMA user_version = 31"); err != nil {
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
		t.Fatalf("mission changed: %+v, %v", retained, err)
	}
	tasks, next, err := store.ListMissionTasks(ctx, project.ID, mission.ID, 0, 8)
	if err != nil || len(tasks) != 1 || !reflect.DeepEqual(tasks[0], task) || next != 0 {
		t.Fatalf("legacy related work = %+v, %d, %v", tasks, next, err)
	}
	if _, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 184), IncarnationID: incarnationID(t, 185), ProjectID: project.ID, Title: "standalone work"}, mustTime(t, 5)); err != nil {
		t.Fatal(err)
	}
}
