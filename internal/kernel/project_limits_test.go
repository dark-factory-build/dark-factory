package kernel

import (
	"context"
	"testing"
)

func TestOverseerRunHasFixedBackstop(t *testing.T) {
	t.Parallel()
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
			isOverseer := role == RoleOrchestrator
			if overdue(bound-1) || overdue(bound) != isOverseer {
				t.Fatalf("%s backstop wrong", role)
			}
			if _, err := store.writer.Exec(`UPDATE runs SET provider = 'shell' WHERE id = ?`, admitted.Run.ID.Bytes()); err != nil {
				t.Fatal(err)
			}
			if overdue(bound) {
				t.Fatalf("shell %s inherited the overseer backstop", role)
			}
		})
	}
}

func TestAdmissionAllowsProjectPastRetainedRunCeiling(t *testing.T) {
	t.Parallel()
	for _, role := range []AgentRole{RoleWorker} {
		t.Run(role.String(), func(t *testing.T) {
			store, _, exhausted, exhaustedAgent := newAdmissionStore(t, role, 2)
			defer store.Close()
			ctx := context.Background()
			secondAgent, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 250), ProjectID: exhausted.ID, Name: "second", Role: role, Provider: ProviderCodex, ToolBudgetLimit: 5}, mustTime(t, 5))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 212), ProjectID: exhausted.ID, AssignedAgentID: exhaustedAgent.ID, IncarnationID: incarnationID(t, 213), Title: "first"}, mustTime(t, 6)); err != nil {
				t.Fatal(err)
			}
			first, err := store.AdmitNext(ctx, admissionKeys(t, 216, nil), mustTime(t, 7))
			if err != nil || !first.Admitted() {
				t.Fatalf("first admission = %+v, %v", first, err)
			}
			if _, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 214), ProjectID: exhausted.ID, AssignedAgentID: secondAgent.ID, IncarnationID: incarnationID(t, 215), Title: "past ceiling"}, mustTime(t, 9)); err != nil {
				t.Fatal(err)
			}
			result, err := store.AdmitNext(ctx, admissionKeys(t, 230, nil), mustTime(t, 10))
			if err != nil || !result.Admitted() || result.Run.TaskID != taskID(t, 214) {
				t.Fatalf("admission = %+v, %v", result, err)
			}
		})
	}
}
