package kernel

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/runner"
)

func TestProductionPersistsFinalizedConstructionPublicationAndRebase(t *testing.T) {
	ctx := context.Background()
	proposal, err := NewSuccessProposal("published")
	if err != nil {
		t.Fatal(err)
	}
	store, finalizing := finalizingReleasedRun(t, RoleWorker, VerificationNone, proposal)
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
	// Make the retained Change's committed head differ from its base so this
	// fixture exercises the attention flag rather than the no-op path.
	moved, err := NewCommitID(change.Selection.format, bytes.Repeat([]byte{0xd2}, change.Selection.format.oidLength()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.ExecContext(ctx, `UPDATE changes SET head_commit = ? WHERE id = ?`, moved.Bytes(), change.ID.Bytes()); err != nil {
		t.Fatal(err)
	}
	change.HeadCommit = &moved
	page, err := store.Production(ctx, terminal.ProjectID, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	construction := productionRecord(t, page, "construction", "")
	if construction == nil || construction.VisualID != "change:"+change.ID.String() {
		t.Fatalf("finalized construction = %+v", construction)
	}
	var constructionDocument map[string]any
	if err := json.Unmarshal(construction.Document, &constructionDocument); err != nil || constructionDocument["needs_you"] != true {
		t.Fatalf("stale unpublished construction needs_you = %#v, err=%v", constructionDocument["needs_you"], err)
	}
	hexHead := hex.EncodeToString(change.HeadCommit.Bytes())
	branch := "factory/" + change.ID.String()[:12]
	pr := ProductionPullRequest{Number: 7, Title: "Ship the machine", URL: "https://github.com/example/factory/pull/7", Head: hexHead, Branch: branch, Base: "main", State: "open", Review: ProductionReview{Head: hexHead, State: "allow"}}
	observation := ProductionObservation{Repository: "Example/Factory", ObservedAt: 80, PullRequests: []ProductionPullRequest{pr}}
	if err := store.RecordProductionObservation(ctx, terminal.ProjectID, observation, mustTime(t, 80)); err != nil {
		t.Fatal(err)
	}
	page, err = store.Production(ctx, terminal.ProjectID, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	if productionRecord(t, page, "construction", "") != nil {
		t.Fatal("matching observed factory branch acquired publication association without RecordPublication")
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
	if construction := productionRecord(t, page, "construction", ""); construction != nil {
		t.Fatalf("publication did not clear needs_you construction = %+v", construction)
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

func TestOverseerWakeForStalePublicationIsEdgeTriggeredAndRearmsOnChange(t *testing.T) {
	ctx := context.Background()
	proposal, err := NewSuccessProposal("published")
	if err != nil {
		t.Fatal(err)
	}
	store, finalizing := finalizingReleasedRun(t, RoleWorker, VerificationNone, proposal)
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
	firstHead, err := NewCommitID(change.Selection.format, bytes.Repeat([]byte{0xd2}, change.Selection.format.oidLength()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.ExecContext(ctx, `UPDATE changes SET head_commit = ? WHERE id = ?`, firstHead.Bytes(), change.ID.Bytes()); err != nil {
		t.Fatal(err)
	}
	overseer, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 191), ProjectID: terminal.ProjectID, Name: "publication overseer", Role: RoleOrchestrator, Provider: ProviderCodex, ToolBudgetLimit: 8}, mustTime(t, 80))
	if err != nil {
		t.Fatal(err)
	}
	policy, after, budget, instruction := IdleStandingInstruction, uint32(1), uint32(4), "Publish completed Changes."
	overseer, err = store.UpdateAgent(ctx, overseer.ID, overseer.Revision, AgentPatch{IdlePolicy: &policy, IdleAfterSeconds: &after, IdleRunBudget: &budget, IdleInstruction: &instruction}, mustTime(t, 81))
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.EnqueueOverseerWakeups(ctx, mustTime(t, 200_080))
	if err != nil || len(first) != 1 || !strings.Contains(first[0].Body, "publication_change="+change.ID.String()+":") || !strings.Contains(first[0].Body, hex.EncodeToString(firstHead.Bytes())) {
		t.Fatalf("stale publication wake = %+v, %v", first, err)
	}
	keys := admissionKeys(t, 192, nil)
	admission, err := store.AdmitNext(ctx, keys, mustTime(t, 200_081))
	if err != nil || !admission.Admitted() || admission.Run == nil || admission.Run.TaskID != first[0].ID {
		t.Fatalf("overseer admission = %+v, %v", admission, err)
	}
	runID := admission.Run.ID
	activateAllResourcesUnique(t, store, *admission.Run, 200_082, 192)
	running, found, err := store.Run(ctx, runID)
	if err != nil || !found {
		t.Fatalf("activated overseer run = %+v, found=%v, err=%v", running, found, err)
	}
	activated := running
	session := terminalSessionForRunTest(t, store, running.ID)
	running, err = store.ActivateRun(ctx, running.ID, session.ID, running.Revision, session.Revision, mustTime(t, 200_086))
	if err != nil {
		t.Fatalf("activate overseer run id=%s run_revision=%d session=%s session_run=%s session_revision=%d: %v", activated.ID, activated.Revision.Int64(), session.ID, session.RunID, session.Revision.Int64(), err)
	}
	if _, err := store.ProposeAttemptOutcome(ctx, keys.AttemptDigest, proposal, mustTime(t, 200_087)); err != nil {
		t.Fatal(err)
	}
	running = observeMissingProcessExits(t, store, running.ID, 200_088)
	releaseAllRunResources(t, store, running.ID, 200_089)
	closed := closeTerminalSessionAtCurrent(t, store, running.ID, 200_092)
	if _, err := store.FinalizeRun(ctx, closed.ID, closed.Revision, mustTime(t, 200_093)); err != nil {
		t.Fatal(err)
	}
	if repeated, err := store.EnqueueOverseerWakeups(ctx, mustTime(t, 400_000)); err != nil || len(repeated) != 0 {
		t.Fatalf("duplicate stale publication wake = %+v, %v", repeated, err)
	}
	secondHead, err := NewCommitID(change.Selection.format, bytes.Repeat([]byte{0xd3}, change.Selection.format.oidLength()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.ExecContext(ctx, `UPDATE changes SET head_commit = ? WHERE id = ?`, secondHead.Bytes(), change.ID.Bytes()); err != nil {
		t.Fatal(err)
	}
	rearmed, err := store.EnqueueOverseerWakeups(ctx, mustTime(t, 400_001))
	if err != nil || len(rearmed) != 1 || !strings.Contains(rearmed[0].Body, hex.EncodeToString(secondHead.Bytes())) {
		t.Fatalf("changed publication wake = %+v, %v", rearmed, err)
	}
	pr := ProductionPullRequest{Number: 7, Title: "Ship the machine", URL: "https://github.com/example/factory/pull/7", Head: hex.EncodeToString(secondHead.Bytes()), Branch: "factory/" + change.ID.String()[:12], Base: "main", State: "open", Review: ProductionReview{Head: hex.EncodeToString(secondHead.Bytes()), State: "allow"}}
	if err := store.RecordPublication(ctx, terminal.ProjectID, terminal.TaskID, "example/factory", pr, mustTime(t, 400_002)); err != nil {
		t.Fatal(err)
	}
	read, err := store.beginRead(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	if candidates, err := unpublishedPublicationTargets(ctx, read.connection, terminal.ProjectID, overseer.ID, 400_003); err != nil || len(candidates) != 0 {
		t.Fatalf("published Change remained a wake candidate = %+v, %v", candidates, err)
	}
}

func TestPublicationWakeOverflowFallsBackToBoundedFullReconciliation(t *testing.T) {
	targets := make([]publicationWakeTarget, 32)
	for index := range targets {
		targets[index] = publicationWakeTarget{TaskID: taskID(t, byte(index+1)), ChangeID: changeID(t, byte(index+60)), ChangeRevision: mustRevision(t, 4), HeadCommitDigest: strings.Repeat("d", 40)}
	}
	instruction := strings.Repeat("x", runner.MaxCodexTaskBytes-512)
	body, fits := overseerWakeInstructionWithPublication(ProviderCodex, instruction, nil, nil, false, targets)
	if !fits || len(body) > runner.MaxCodexTaskBytes || !strings.Contains(body, "mode=full") || !strings.Contains(body, "publication attention overflow: full reconciliation required") || strings.Contains(body, "publication_change=") {
		t.Fatalf("overflow wake body fits=%v bytes=%d mode=%v overflow=%v marker=%v body suffix=%q", fits, len(body), strings.Contains(body, "mode=full"), strings.Contains(body, "publication attention overflow: full reconciliation required"), strings.Contains(body, "publication_change="), body[max(0, len(body)-160):])
	}
}

func TestPublicationWakeOverflowRearmsForChangedCandidateAndClearsOnPublication(t *testing.T) {
	ctx := context.Background()
	proposal, err := NewSuccessProposal("published")
	if err != nil {
		t.Fatal(err)
	}
	store, finalizing := finalizingReleasedRun(t, RoleWorker, VerificationNone, proposal)
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
	firstHead, err := NewCommitID(change.Selection.format, bytes.Repeat([]byte{0xd2}, change.Selection.format.oidLength()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.ExecContext(ctx, `UPDATE changes SET head_commit = ? WHERE id = ?`, firstHead.Bytes(), change.ID.Bytes()); err != nil {
		t.Fatal(err)
	}
	overseer, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 193), ProjectID: terminal.ProjectID, Name: "overflow publication overseer", Role: RoleOrchestrator, Provider: ProviderCodex, ToolBudgetLimit: 8}, mustTime(t, 80))
	if err != nil {
		t.Fatal(err)
	}
	policy, after, budget, instruction := IdleStandingInstruction, uint32(1), uint32(4), strings.Repeat("x", runner.MaxCodexTaskBytes-128)
	overseer, err = store.UpdateAgent(ctx, overseer.ID, overseer.Revision, AgentPatch{IdlePolicy: &policy, IdleAfterSeconds: &after, IdleRunBudget: &budget, IdleInstruction: &instruction}, mustTime(t, 81))
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.EnqueueOverseerWakeups(ctx, mustTime(t, 200_080))
	if err != nil || len(first) != 1 || !strings.Contains(first[0].Body, "publication attention overflow: full reconciliation required") || strings.Contains(first[0].Body, "publication_change=") {
		t.Fatalf("overflow publication wake = %+v, %v", first, err)
	}
	keys := admissionKeys(t, 194, nil)
	admission, err := store.AdmitNext(ctx, keys, mustTime(t, 200_081))
	if err != nil || !admission.Admitted() || admission.Run == nil || admission.Run.TaskID != first[0].ID {
		t.Fatalf("overflow overseer admission = %+v, %v", admission, err)
	}
	activateAllResourcesUnique(t, store, *admission.Run, 200_082, 194)
	running, found, err := store.Run(ctx, admission.Run.ID)
	if err != nil || !found {
		t.Fatalf("activated overflow run = %+v, found=%v, err=%v", running, found, err)
	}
	session := terminalSessionForRunTest(t, store, running.ID)
	running, err = store.ActivateRun(ctx, running.ID, session.ID, running.Revision, session.Revision, mustTime(t, 200_086))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ProposeAttemptOutcome(ctx, keys.AttemptDigest, proposal, mustTime(t, 200_087)); err != nil {
		t.Fatal(err)
	}
	running = observeMissingProcessExits(t, store, running.ID, 200_088)
	releaseAllRunResources(t, store, running.ID, 200_089)
	closed := closeTerminalSessionAtCurrent(t, store, running.ID, 200_092)
	if _, err := store.FinalizeRun(ctx, closed.ID, closed.Revision, mustTime(t, 200_093)); err != nil {
		t.Fatal(err)
	}
	if repeated, err := store.EnqueueOverseerWakeups(ctx, mustTime(t, 400_000)); err != nil || len(repeated) != 0 {
		t.Fatalf("duplicate overflow publication wake = %+v, %v", repeated, err)
	}
	secondHead, err := NewCommitID(change.Selection.format, bytes.Repeat([]byte{0xd3}, change.Selection.format.oidLength()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.ExecContext(ctx, `UPDATE changes SET head_commit = ?, updated_at_ms = ? WHERE id = ?`, secondHead.Bytes(), 200_094, change.ID.Bytes()); err != nil {
		t.Fatal(err)
	}
	read, err := store.beginRead(ctx)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := unpublishedPublicationTargets(ctx, read.connection, terminal.ProjectID, overseer.ID, 400_002)
	read.Close()
	if err != nil || len(candidates) != 1 {
		t.Fatalf("changed overflow publication candidate = %+v, %v", candidates, err)
	}
}

func TestProductionPublicationUsesOwnedChangeForTransformedHead(t *testing.T) {
	for _, historical := range []bool{false, true} {
		t.Run(fmt.Sprint("historical=", historical), func(t *testing.T) {
			ctx := context.Background()
			proposal, err := NewSuccessProposal("published")
			if err != nil {
				t.Fatal(err)
			}
			store, finalizing := finalizingReleasedRun(t, RoleWorker, VerificationNone, proposal)
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
			publisherAgent, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 240), ProjectID: terminal.ProjectID, Name: "publisher", Role: RoleOrchestrator, Provider: ProviderCodex, ToolBudgetLimit: 4}, mustTime(t, 79))
			if err != nil {
				t.Fatal(err)
			}
			publisher, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 241), IncarnationID: incarnationID(t, 242), ProjectID: terminal.ProjectID, AssignedAgentID: publisherAgent.ID, Title: "publish"}, mustTime(t, 79))
			if err != nil {
				t.Fatal(err)
			}
			pr := ProductionPullRequest{Number: 7, Title: "Ship the transformed tree", URL: "https://github.com/example/factory/pull/7", Head: strings.Repeat("b", 40), Branch: "factory/" + change.ID.String()[:12], Base: "main", State: "open", Review: ProductionReview{Head: strings.Repeat("b", 40), State: "allow"}}
			if err := store.RecordProductionObservation(ctx, terminal.ProjectID, ProductionObservation{Repository: "example/factory", ObservedAt: 79, PullRequests: []ProductionPullRequest{pr}}, mustTime(t, 79)); err != nil {
				t.Fatal(err)
			}
			page, err := store.Production(ctx, terminal.ProjectID, 0, 8)
			if err != nil || productionRecord(t, page, "pull_request", "7").VisualID == "change:"+change.ID.String() {
				t.Fatalf("unacknowledged transformed observation = %+v, err=%v", page, err)
			}
			if historical {
				// The old publication hook saved the publisher but could not link a rewritten commit.
				if _, err := store.writer.ExecContext(ctx, `INSERT INTO publication_tasks (project_id, repository, pull_number, task_id, created_at_ms) VALUES (?, 'example/factory', 7, ?, 80)`, terminal.ProjectID.Bytes(), publisher.ID.Bytes()); err != nil {
					t.Fatal(err)
				}
				if err := store.RecordProductionObservation(ctx, terminal.ProjectID, ProductionObservation{Repository: "example/factory", ObservedAt: 80, PullRequests: []ProductionPullRequest{pr}}, mustTime(t, 80)); err != nil {
					t.Fatal(err)
				}
			} else if err := store.RecordPublication(ctx, terminal.ProjectID, publisher.ID, "example/factory", pr, mustTime(t, 80)); err != nil {
				t.Fatal(err)
			}
			page, err = store.Production(ctx, terminal.ProjectID, 0, 8)
			if err != nil {
				t.Fatal(err)
			}
			published := productionRecord(t, page, "pull_request", "7")
			if published == nil || published.VisualID != "change:"+change.ID.String() || !containsString(published.Tasks, publisher.ID.String()) || !containsString(published.Tasks, terminal.TaskID.String()) {
				t.Fatalf("transformed publication = %+v", published)
			}
			var linkedPublisher, linkedWorker int
			if err := store.writer.QueryRowContext(ctx, `SELECT SUM(p.task_id = ?), SUM(p.task_id = ?) FROM publication_tasks p JOIN changes c ON c.id = p.change_id WHERE p.project_id = ? AND p.repository = 'example/factory' AND p.pull_number = 7`, publisher.ID.Bytes(), terminal.TaskID.Bytes(), terminal.ProjectID.Bytes()).Scan(&linkedPublisher, &linkedWorker); err != nil {
				t.Fatal(err)
			}
			if linkedPublisher != 0 || linkedWorker != 1 {
				t.Fatalf("publication ownership = publisher %d, worker %d", linkedPublisher, linkedWorker)
			}
			if productionRecord(t, page, "construction", "") != nil {
				t.Fatal("owned Change remained as construction")
			}
			workerAgent, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 243), ProjectID: terminal.ProjectID, Name: "worker", Role: RoleWorker, Provider: ProviderCodex, ToolBudgetLimit: 4}, mustTime(t, 80))
			if err != nil {
				t.Fatal(err)
			}
			unrelatedTask, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 244), IncarnationID: incarnationID(t, 245), ProjectID: terminal.ProjectID, AssignedAgentID: workerAgent.ID, Title: "unrelated publisher"}, mustTime(t, 80))
			if err != nil {
				t.Fatal(err)
			}
			unrelated := pr
			unrelated.Number = 8
			unrelated.URL = "https://github.com/example/factory/pull/8"
			if err := store.RecordPublication(ctx, terminal.ProjectID, unrelatedTask.ID, "example/factory", unrelated, mustTime(t, 81)); err != nil {
				t.Fatal(err)
			}
			page, err = store.Production(ctx, terminal.ProjectID, 0, 8)
			if err != nil {
				t.Fatal(err)
			}
			if other := productionRecord(t, page, "pull_request", "8"); other == nil || other.VisualID == "change:"+change.ID.String() {
				t.Fatalf("unrelated publication = %+v", other)
			}
			var unrelatedChange []byte
			if err := store.writer.QueryRowContext(ctx, `SELECT change_id FROM publication_tasks WHERE project_id = ? AND repository = 'example/factory' AND pull_number = 8`, terminal.ProjectID.Bytes()).Scan(&unrelatedChange); err != nil && !errors.Is(err, sql.ErrNoRows) {
				t.Fatal(err)
			}
			if unrelatedChange != nil {
				t.Fatalf("unrelated publication acquired Change %x", unrelatedChange)
			}
			// Two valid Changes may share the shortened branch prefix.
			collisionTask, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 246), IncarnationID: incarnationID(t, 247), ProjectID: terminal.ProjectID, AssignedAgentID: terminal.AgentID, Priority: 100, Title: "colliding Change"}, mustTime(t, 82))
			if err != nil {
				t.Fatal(err)
			}
			collisionBytes := change.ID.Bytes()
			collisionBytes[15] ^= 1
			collision, err := ChangeIDFromBytes(collisionBytes)
			if err != nil {
				t.Fatal(err)
			}
			admission, err := store.AdmitNext(ctx, admissionKeys(t, 210, &collision), mustTime(t, 82))
			if err != nil || admission.Run == nil || admission.Run.TaskID != collisionTask.ID {
				t.Fatalf("collision admission = %+v, %v", admission, err)
			}
			prepared, err := store.RecordChangePrepared(ctx, collision, mustRevision(t, 1), *change.Selection, mustTime(t, 82))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.MarkChangeAvailable(ctx, collision, prepared.Revision, change.Selection.commit, mustTime(t, 82)); err != nil {
				t.Fatal(err)
			}
			ambiguous := pr
			ambiguous.Number = 9
			ambiguous.URL = "https://github.com/example/factory/pull/9"
			if err := store.RecordPublication(ctx, terminal.ProjectID, publisher.ID, "example/factory", ambiguous, mustTime(t, 83)); err != nil {
				t.Fatal(err)
			}
			page, err = store.Production(ctx, terminal.ProjectID, 0, 8)
			if err != nil || productionRecord(t, page, "pull_request", "9").VisualID != "example/factory#9" {
				t.Fatalf("ambiguous publication = %+v, %v", page, err)
			}
			var linked int
			if err := store.writer.QueryRowContext(ctx, `SELECT count(*) FROM publication_tasks WHERE project_id = ? AND repository = 'example/factory' AND pull_number = 9 AND change_id IS NOT NULL`, terminal.ProjectID.Bytes()).Scan(&linked); err != nil || linked != 0 {
				t.Fatalf("ambiguous Change links = %d, %v", linked, err)
			}

		})
	}
}

func TestProductionObservationUsesVerifiedHeadRepositoryForTransformedHead(t *testing.T) {
	ctx := context.Background()
	proposal, err := NewSuccessProposal("published")
	if err != nil {
		t.Fatal(err)
	}
	store, finalizing := finalizingReleasedRun(t, RoleWorker, VerificationNone, proposal)
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
	digest := [32]byte{1}
	if _, err := store.writer.ExecContext(ctx, `UPDATE repository_source_identities SET root_dev = 61, root_inode = 62, git_dev = 61, git_inode = 63, origin_digest = ?, publication_repository = 'example/factory' WHERE repository_id = (SELECT repository_id FROM task_repository_bindings WHERE task_id = ?)`, digest[:], terminal.TaskID.Bytes()); err != nil {
		t.Fatal(err)
	}
	pr := ProductionPullRequest{Number: 7, Title: "Ship the transformed tree", URL: "https://github.com/example/factory/pull/7", Head: strings.Repeat("b", 40), HeadRepository: "example/factory", Branch: "factory/" + change.ID.String()[:12], Base: "main", State: "open", Review: ProductionReview{Head: strings.Repeat("b", 40), State: "allow"}}
	for _, repository := range []string{"", "other/factory"} {
		if _, err := store.writer.ExecContext(ctx, `UPDATE repository_source_identities SET publication_repository = ? WHERE repository_id = (SELECT repository_id FROM task_repository_bindings WHERE task_id = ?)`, repository, terminal.TaskID.Bytes()); err != nil {
			t.Fatal(err)
		}
		if err := store.RecordProductionObservation(ctx, terminal.ProjectID, ProductionObservation{Repository: "example/factory", ObservedAt: 79, PullRequests: []ProductionPullRequest{pr}}, mustTime(t, 79)); err != nil {
			t.Fatal(err)
		}
		page, err := store.Production(ctx, terminal.ProjectID, 0, 8)
		if err != nil || containsString(productionRecord(t, page, "pull_request", "7").Tasks, terminal.TaskID.String()) {
			t.Fatalf("unverified task repository linked producer: %+v, %v", page, err)
		}
	}
	if _, err := store.writer.ExecContext(ctx, `UPDATE repository_source_identities SET publication_repository = 'example/factory' WHERE repository_id = (SELECT repository_id FROM task_repository_bindings WHERE task_id = ?)`, terminal.TaskID.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordProductionObservation(ctx, terminal.ProjectID, ProductionObservation{Repository: "example/factory", ObservedAt: 80, PullRequests: []ProductionPullRequest{pr}}, mustTime(t, 80)); err != nil {
		t.Fatal(err)
	}
	page, err := store.Production(ctx, terminal.ProjectID, 0, 8)
	item := productionRecord(t, page, "pull_request", "7")
	if err != nil || item == nil || item.VisualID != "change:"+change.ID.String() || !containsString(item.Tasks, terminal.TaskID.String()) || productionRecord(t, page, "construction", "") != nil {
		t.Fatalf("verified transformed observation = %+v, err=%v", page, err)
	}
	for _, headRepository := range []string{"", "other/factory"} {
		fork := pr
		fork.Number = 8
		fork.URL = "https://github.com/example/factory/pull/8"
		fork.HeadRepository = headRepository
		if err := store.RecordProductionObservation(ctx, terminal.ProjectID, ProductionObservation{Repository: "example/factory", ObservedAt: 81, PullRequests: []ProductionPullRequest{fork}}, mustTime(t, 81)); err != nil {
			t.Fatal(err)
		}
		page, err = store.Production(ctx, terminal.ProjectID, 0, 8)
		item := productionRecord(t, page, "pull_request", "8")
		if err != nil || item == nil || item.VisualID != "example/factory#8" {
			t.Fatalf("unverified head repository %q associated Change: %+v, err=%v", headRepository, item, err)
		}
		var linked int
		if err := store.writer.QueryRowContext(ctx, `SELECT count(*) FROM publication_tasks WHERE project_id = ? AND repository = 'example/factory' AND pull_number = 8 AND change_id IS NOT NULL`, terminal.ProjectID.Bytes()).Scan(&linked); err != nil || linked != 0 {
			t.Fatalf("unverified head repository %q linked Change rows = %d, err=%v", headRepository, linked, err)
		}
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
	mission, err := store.WriteOutcome(ctx, NewOutcome{ID: outcomeID(t, 240), ProjectID: project.ID, Document: OutcomeDocument{Kind: "mission", Objective: "keep work", Criteria: "all rows survive", AnchorTaskID: anchor.ID.String(), AnchorWorkRevision: 1, State: "open"}}, 0, mustTime(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	standalone, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 241), IncarnationID: incarnationID(t, 242), ProjectID: project.ID, Title: "standalone"}, mustTime(t, 5))
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
