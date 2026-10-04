package kernel

import (
	"context"
	"testing"
)

func TestProjectRunAllowanceCountsAdmissionsOnce(t *testing.T) {
	store, _, project, agent := newAdmissionStore(t, RoleOrchestrator, 1)
	defer store.Close()
	ctx := context.Background()
	project, err := store.SetProjectLimits(ctx, project.ID, project.Revision, 1, 0, mustTime(t, 3))
	if err != nil || project.RunBudgetLimit != 1 || project.RunsUsed != 0 {
		t.Fatalf("set allowance = %+v, %v", project, err)
	}
	if _, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 201), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 202), Title: "one"}, mustTime(t, 4)); err != nil {
		t.Fatal(err)
	}
	keys := admissionKeys(t, 203, nil)
	first, err := store.AdmitNext(ctx, keys, mustTime(t, 5))
	if err != nil || !first.Admitted() {
		t.Fatalf("first admission = %+v, %v", first, err)
	}
	replay, err := store.AdmitNext(ctx, keys, mustTime(t, 6))
	if err != nil || !replay.Admitted() || replay.Run.ID != first.Run.ID {
		t.Fatalf("admission replay = %+v, %v", replay, err)
	}
	project, found, err := store.Project(ctx, project.ID)
	if err != nil || !found || project.RunsUsed != 1 {
		t.Fatalf("used runs = %+v, found=%v, err=%v", project, found, err)
	}
	project, err = store.SetProjectLimits(ctx, project.ID, project.Revision, 0, 1, mustTime(t, 6))
	if err != nil {
		t.Fatal(err)
	}
	due, err := store.OverdueRuns(ctx, mustTime(t, 1005))
	if err != nil || len(due) != 1 || due[0].ID != first.Run.ID {
		t.Fatalf("overdue runs = %+v, %v", due, err)
	}
}

func TestProjectLimitsUseAdditionalAllowanceAndDefaultToDisabled(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 210), Name: "p", Root: "/limits"}, mustTime(t, 1))
	if err != nil || project.RunBudgetLimit != 0 || project.MaxRunSeconds != 0 {
		t.Fatalf("new project limits = %+v, %v", project, err)
	}
	if _, err := store.writer.Exec(`UPDATE projects SET runs_used = 7 WHERE id = ?`, project.ID.Bytes()); err != nil {
		t.Fatal(err)
	}
	project, found, err := store.Project(ctx, project.ID)
	if err != nil || !found {
		t.Fatal(err)
	}
	project, err = store.SetProjectLimits(ctx, project.ID, project.Revision, 3, 120, mustTime(t, 2))
	if err != nil || project.RunBudgetLimit != 10 || project.RunsUsed != 7 || project.MaxRunSeconds != 120 {
		t.Fatalf("additional limits = %+v, %v", project, err)
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil || len(snapshot.Projects) != 1 {
		t.Fatalf("dashboard snapshot = %+v, %v", snapshot, err)
	}
	if got := snapshot.Projects[0]; got.RunBudgetLimit != 10 || got.RunsUsed != 7 || got.MaxRunSeconds != 120 {
		t.Fatalf("dashboard project limits = %+v", got)
	}
}

func TestOverseerRunHasBackstopBelowDisabledOrLongerProjectLimit(t *testing.T) {
	for _, role := range []AgentRole{RoleOrchestrator, RoleWorker} {
		t.Run(role.String(), func(t *testing.T) {
			store, _, project, agent := newAdmissionStore(t, role, 1)
			defer store.Close()
			ctx := context.Background()
			if _, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 220), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 221), Title: "external gate"}, mustTime(t, 4)); err != nil {
				t.Fatal(err)
			}
			admitted, err := store.AdmitNext(ctx, admissionKeys(t, 222, nil), mustTime(t, 5))
			if err != nil || !admitted.Admitted() {
				t.Fatalf("admission = %+v, %v", admitted, err)
			}
			bound := int64(5 + MaxOverseerRunSeconds*1000)
			overdue := func(at int64) bool {
				t.Helper()
				due, err := store.OverdueRuns(ctx, mustTime(t, at))
				if err != nil || len(due) > 1 || len(due) == 1 && due[0].ID != admitted.Run.ID {
					t.Fatalf("overdue at %d = %+v, %v", at, due, err)
				}
				return len(due) == 1
			}
			setCeiling := func(seconds uint32) {
				t.Helper()
				current, found, err := store.Project(ctx, project.ID)
				if err != nil || !found {
					t.Fatalf("project = %+v, found=%v, err=%v", current, found, err)
				}
				if _, err := store.SetProjectLimits(ctx, current.ID, current.Revision, 0, seconds, mustTime(t, 6)); err != nil {
					t.Fatal(err)
				}
			}
			isOverseer := role == RoleOrchestrator
			if overdue(bound-1) || overdue(bound) != isOverseer {
				t.Fatalf("disabled ceiling: %s backstop wrong", role)
			}
			setCeiling(2700)
			if overdue(bound-1) || overdue(bound) != isOverseer || !overdue(5+2_700_000) {
				t.Fatalf("longer ceiling: %s backstop wrong", role)
			}
			setCeiling(1)
			if !overdue(1005) {
				t.Fatalf("shorter configured ceiling did not apply to %s", role)
			}
			setCeiling(0)
			if _, err := store.writer.Exec(`UPDATE runs SET provider = 'shell' WHERE id = ?`, admitted.Run.ID.Bytes()); err != nil {
				t.Fatal(err)
			}
			if overdue(bound) {
				t.Fatalf("shell %s inherited the overseer backstop", role)
			}
		})
	}
}

func TestAdmissionSkipsExhaustedProjectBeforePriority(t *testing.T) {
	for _, role := range []AgentRole{RoleWorker, RoleOrchestrator} {
		t.Run(role.String(), func(t *testing.T) {
			store, _, exhausted, exhaustedAgent := newAdmissionStore(t, role, 4)
			defer store.Close()
			ctx := context.Background()
			if _, err := store.writer.Exec(`UPDATE projects SET run_budget_limit = 1, runs_used = 1 WHERE id = ?`, exhausted.ID.Bytes()); err != nil {
				t.Fatal(err)
			}
			funded, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 210), Name: "funded", Root: "/funded"}, mustTime(t, 4))
			if err != nil {
				t.Fatal(err)
			}
			fundedAgent, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 211), ProjectID: funded.ID, Name: "funded", Role: role, Provider: ProviderCodex, ToolBudgetLimit: 5}, mustTime(t, 5))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 212), ProjectID: exhausted.ID, AssignedAgentID: exhaustedAgent.ID, IncarnationID: incarnationID(t, 213), Title: "exhausted", Priority: 9}, mustTime(t, 6)); err != nil {
				t.Fatal(err)
			}
			fundedTask, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 214), ProjectID: funded.ID, AssignedAgentID: fundedAgent.ID, IncarnationID: incarnationID(t, 215), Title: "funded", Priority: 1}, mustTime(t, 7))
			if err != nil {
				t.Fatal(err)
			}
			result, err := store.AdmitNext(ctx, admissionKeys(t, 216, nil), mustTime(t, 8))
			if err != nil || !result.Admitted() || result.Run.TaskID != fundedTask.ID {
				t.Fatalf("admission = %+v, %v", result, err)
			}
		})
	}
}
