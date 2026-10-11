package kernel

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// report counts a run's tokens by who spent them against the merges in its
// bucket; why names a pull request's head, the reconciler's escalation and
// whom it waits on, and a task without one.
func TestReportAndWhyReadTheReconcilersState(t *testing.T) {
	t.Parallel()
	store, _, project, agent := newAdmissionStore(t, RoleOrchestrator, 1)
	defer store.Close()
	ctx := context.Background()
	task, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 201), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 202), Title: "oversee"}, mustTime(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := store.AdmitNext(ctx, admissionKeys(t, 210, nil), mustTime(t, 5))
	if err != nil || !admitted.Admitted() {
		t.Fatalf("admission = %+v, %v", admitted, err)
	}
	if err := store.AddRunTokens(ctx, admitted.Run.ID, 300); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO production_records (project_id, repository, kind, identity, visual_id, document, observed_at_ms) VALUES (?, 'example/factory', 'pull_request', '9', '', ?, 6)`
	if _, err := store.writer.ExecContext(ctx, insert, project.ID.Bytes(), `{"number":9,"state":"merged","head":"`+strings.Repeat("c", 40)+`","merged_at":"1970-01-01T00:00:01Z"}`); err != nil {
		t.Fatal(err)
	}
	lines, err := store.Report(ctx, &project.ID, 0, 3600000)
	want := []string{
		"1970-01-01T00:00Z runs=1 failed=0% tokens=300 worker=0 overseer=300 specialist=0 merges=1 tokens/merge=300",
		"top task tokens=300 " + task.ID.String() + ` running "oversee"`,
	}
	if err != nil || !slices.Equal(lines, want) {
		t.Fatalf("report = %q, %v", lines, err)
	}
	head := strings.Repeat("a", 40)
	escalate(t, store, project.ID, "7", "open", head, head, 7)
	lines, err = store.Why(ctx, &project.ID, 7, TaskID{})
	if err != nil || len(lines) != 8 || lines[1] != "head: "+head || !strings.Contains(lines[6], "escalated: stuck 7") || lines[7] != "waits on: operator: factoryd escalated it to the overseer" {
		t.Fatalf("why pull = %q, %v", lines, err)
	}
	lines, err = store.Why(ctx, &project.ID, 0, task.ID)
	if err != nil || len(lines) != 4 || lines[2] != "pull: none published" || lines[3] != "waits on: worker: its task is running" {
		t.Fatalf("why task = %q, %v", lines, err)
	}
	if _, err := store.Why(ctx, &project.ID, 8, TaskID{}); err != ErrNotFound {
		t.Fatalf("why of an unknown pull = %v", err)
	}
}
