package kernel

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// The operator read separates the durable wake from any handling: a failed
// worker task is pending until EnqueueOverseerWakeups has consumed its newest
// event, scheduled from then on, and its disposition stays none until a
// retry, cancel, Needs You from its own run or operator recovery is actually
// recorded. No prose or timestamp associates anything with the task: an
// orchestrator's question that quotes the task id is not its Needs You, and
// events sharing one millisecond are ordered by sequence alone.
func TestTaskRecoveryReportsOverseerNotificationSeparatelyFromDisposition(t *testing.T) {
	ctx := context.Background()
	store, terminal, _ := terminalPreRunningWorker(t)
	defer store.Close()
	recovery, found, err := store.TaskRecovery(ctx, terminal.TaskID, terminal.TaskIncarnationID)
	if err != nil || !found || recovery.Overseer != nil || recovery.OverseerNotification != OverseerNotificationNone || recovery.Disposition() != "none" || recovery.LastProgressAt != terminal.UpdatedAt {
		t.Fatalf("recovery without an overseer = %+v, found=%v, err=%v", recovery, found, err)
	}
	overseerKeys, overseerRun := runningOverseerKeys(t, store, terminal.ProjectID, 35)
	overseer, _, err := store.Agent(ctx, agentID(t, 245))
	if err != nil {
		t.Fatal(err)
	}
	policy, after, budget, instruction := IdleStandingInstruction, uint32(1), uint32(4), "Supervise."
	if _, err := store.UpdateAgent(ctx, overseer.ID, overseer.Revision, AgentPatch{IdlePolicy: &policy, IdleAfterSeconds: &after, IdleRunBudget: &budget, IdleInstruction: &instruction}, mustTime(t, 43)); err != nil {
		t.Fatal(err)
	}
	// The running overseer quotes the worker task id in its own question:
	// that request belongs to the overseer's run, not to this task.
	if _, err := store.CreateHumanQuestionForAttempt(ctx, overseerKeys.AttemptDigest, NewHumanQuestion{IdempotencyKey: humanKey(90), QuestionText: "Retry " + terminal.TaskID.String() + "?"}, mustTime(t, 44)); err != nil {
		t.Fatal(err)
	}
	recovery, _, err = store.TaskRecovery(ctx, terminal.TaskID, terminal.TaskIncarnationID)
	if err != nil || recovery.Overseer == nil || *recovery.Overseer != overseer.ID || recovery.OverseerNotification != OverseerNotificationPending || recovery.HumanRequest != nil || recovery.Disposition() != "none" || recovery.OverseerTask == nil || recovery.OverseerTask.Status != TaskRunning {
		t.Fatalf("recovery before any wake = %+v, err=%v", recovery, err)
	}
	// Busy overseer: its running task blocks the wake, the event stays pending.
	if wakes, err := store.EnqueueOverseerWakeups(ctx, mustTime(t, 2_000)); err != nil || len(wakes) != 0 {
		t.Fatalf("wake during running overseer = %+v, %v", wakes, err)
	}
	failure, _ := NewFailureProposal(FailureInternal, "overseer ended")
	if _, err := store.FailRun(ctx, overseerRun.ID, overseerRun.Revision, failure, mustTime(t, 2_001)); err != nil {
		t.Fatal(err)
	}
	observeMissingProcessExits(t, store, overseerRun.ID, 2_002)
	releaseAllRunResources(t, store, overseerRun.ID, 2_005)
	closed := closeTerminalSessionAtCurrent(t, store, overseerRun.ID, 2_010)
	if _, err := store.FinalizeRun(ctx, closed.ID, closed.Revision, mustTime(t, 2_011)); err != nil {
		t.Fatal(err)
	}
	wakes, err := store.EnqueueOverseerWakeups(ctx, mustTime(t, 4_000))
	if err != nil || len(wakes) != 1 {
		t.Fatalf("wake = %+v, %v", wakes, err)
	}
	recovery, _, err = store.TaskRecovery(ctx, terminal.TaskID, terminal.TaskIncarnationID)
	if err != nil || recovery.OverseerNotification != OverseerNotificationScheduled || recovery.OverseerTask == nil || recovery.OverseerTask.ID != wakes[0].ID || recovery.Disposition() != "none" {
		t.Fatalf("recovery after wake = %+v, err=%v", recovery, err)
	}
	// The overseer's own wake task names no overseer: nothing wakes anyone
	// about it, and the wire refuses an overseer with a "none" notification.
	own, found, err := store.TaskRecovery(ctx, wakes[0].ID, wakes[0].IncarnationID)
	if err != nil || !found || own.Overseer != nil || own.OverseerTask != nil || own.OverseerNotification != OverseerNotificationNone || own.Disposition() != "queued" {
		t.Fatalf("recovery of the overseer's own task = %+v, found=%v, err=%v", own, found, err)
	}
	// Two events in one millisecond after the wake: the send-back and a
	// priority edit. Owed again by sequence, and the send-back is the recorded
	// disposition and newest progress.
	sentBack, err := store.SendBackTask(ctx, terminal.TaskID, recovery.Task.Revision, "retry the same worker", mustTime(t, 4_001))
	if err != nil {
		t.Fatal(err)
	}
	priority := int64(1)
	if _, err := store.UpdateTask(ctx, sentBack.ID, sentBack.Revision, TaskPatch{Priority: &priority}, mustTime(t, 4_001)); err != nil {
		t.Fatal(err)
	}
	recovery, _, err = store.TaskRecovery(ctx, terminal.TaskID, terminal.TaskIncarnationID)
	if err != nil || recovery.OverseerNotification != OverseerNotificationPending || recovery.Disposition() != "retry_queued" || recovery.LastProgressAt.Int64() != 4_001 {
		t.Fatalf("recovery after same-millisecond events = %+v, err=%v", recovery, err)
	}
}

func TestTaskRecoverySkipsPausedAndBudgetExhaustedOverseers(t *testing.T) {
	ctx := context.Background()
	store, terminal, _ := terminalPreRunningWorker(t)
	defer store.Close()

	configure := func(id byte, name string) Agent {
		overseer, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, id), ProjectID: terminal.ProjectID, Name: name, Role: RoleOrchestrator, Provider: ProviderCodex, ToolBudgetLimit: 1}, mustTime(t, int64(id)))
		if err != nil {
			t.Fatal(err)
		}
		policy, after, budget, instruction := IdleStandingInstruction, uint32(1), uint32(4), "Supervise."
		overseer, err = store.UpdateAgent(ctx, overseer.ID, overseer.Revision, AgentPatch{IdlePolicy: &policy, IdleAfterSeconds: &after, IdleRunBudget: &budget, IdleInstruction: &instruction}, mustTime(t, int64(id)+1))
		if err != nil {
			t.Fatal(err)
		}
		return overseer
	}
	paused := configure(30, "paused")
	pausedValue := true
	if _, err := store.UpdateAgent(ctx, paused.ID, paused.Revision, AgentPatch{Paused: &pausedValue}, mustTime(t, 32)); err != nil {
		t.Fatal(err)
	}
	exhausted := configure(40, "exhausted")
	if _, err := store.writer.Exec(`UPDATE agents SET tool_calls_used = tool_budget_limit WHERE id = ?`, exhausted.ID.Bytes()); err != nil {
		t.Fatal(err)
	}
	eligible := configure(50, "eligible")

	recovery, found, err := store.TaskRecovery(ctx, terminal.TaskID, terminal.TaskIncarnationID)
	if err != nil || !found || recovery.Overseer == nil || *recovery.Overseer != eligible.ID {
		t.Fatalf("recovery selected %+v, found=%v, err=%v; want eligible overseer %v", recovery.Overseer, found, err, eligible.ID)
	}
}

// A question raised by the task's own run is its Needs You, and it stays
// the disposition while unresolved.
func TestTaskRecoveryReportsOwnRunHumanRequestAsNeedsYou(t *testing.T) {
	ctx := context.Background()
	store, running, keys := runningWorkerRun(t)
	defer store.Close()
	recovery, found, err := store.TaskRecovery(ctx, running.TaskID, running.TaskIncarnationID)
	if err != nil || !found || recovery.HumanRequest != nil || recovery.Disposition() != "running" {
		t.Fatalf("recovery while running = %+v, found=%v, err=%v", recovery, found, err)
	}
	request, err := store.CreateHumanQuestionForAttempt(ctx, keys.AttemptDigest, NewHumanQuestion{IdempotencyKey: humanKey(91), QuestionText: "Which base?"}, mustTime(t, 400))
	if err != nil {
		t.Fatal(err)
	}
	recovery, _, err = store.TaskRecovery(ctx, running.TaskID, running.TaskIncarnationID)
	if err != nil || recovery.HumanRequest == nil || *recovery.HumanRequest != request.ID || recovery.Disposition() != "needs_you" || recovery.LastProgressAt.Int64() != 400 {
		t.Fatalf("recovery with own question = %+v, err=%v", recovery, err)
	}
}

// An outcome-less provider exit proves nothing about effects: the run's own
// exit and the retained head it left are the evidence a retry decision needs,
// and a worker that moved its Change head before exiting is not a refusal.
func TestTaskRecoveryExposesProviderExitAndMovedHeadAsRetryEvidence(t *testing.T) {
	ctx := context.Background()
	store, running, _ := runningWorkerRun(t)
	defer store.Close()
	exit, err := NewProcessExitCode(1, 1, mustTime(t, 30))
	if err != nil {
		t.Fatal(err)
	}
	finalizing, err := store.ObserveProviderExit(ctx, running.ID, running.Revision, registeredProcessIdentity(t, store, running.ID, ResourceProviderProcess), exit, mustTime(t, 31))
	if err != nil || finalizing.Phase != RunFinalizing || finalizing.Proposal == nil || finalizing.Proposal.Code() != FailureProviderExit {
		t.Fatalf("provider exit = %+v, %v", finalizing, err)
	}
	finalizing = observeMissingProcessExits(t, store, running.ID, 32)
	releaseAllRunResources(t, store, running.ID, 35)
	finalizing = closeTerminalSessionAtCurrent(t, store, running.ID, 40)
	change, found, err := store.Change(ctx, *running.ChangeID)
	if err != nil || !found || change.HeadCommit == nil {
		t.Fatalf("Change = %+v, found=%v, err=%v", change, found, err)
	}
	moved, _ := NewCommitID(change.Selection.format, bytes.Repeat([]byte{0xd2}, change.Selection.format.oidLength()))
	settlement, _ := NewRetainedChangeSettlement(change.Revision, &moved)
	if _, err := store.FinalizeWorkerRun(ctx, running.ID, finalizing.Revision, settlement, mustTime(t, 41)); err != nil {
		t.Fatal(err)
	}
	recovery, found, err := store.TaskRecovery(ctx, running.TaskID, running.TaskIncarnationID)
	if err != nil || !found || recovery.Run == nil || recovery.Run.ProviderExit == nil || recovery.Change == nil || recovery.Change.HeadCommit == nil {
		t.Fatalf("recovery = %+v, found=%v, err=%v", recovery, found, err)
	}
	code, ok := recovery.Run.ProviderExit.Code()
	if !ok || code != 1 || recovery.Run.RunningAt == nil || recovery.Run.TerminalAt == nil || recovery.Run.TerminalAt.Int64()-recovery.Run.RunningAt.Int64() <= 0 {
		t.Fatalf("run evidence = %+v", recovery.Run)
	}
	if !recovery.Change.HeadCommit.equal(moved) || recovery.Change.HeadCommit.equal(change.Selection.Commit()) {
		t.Fatalf("head evidence = %x, base %x", recovery.Change.HeadCommit.Bytes(), change.Selection.Commit().Bytes())
	}
	if recovery.Disposition() != "none" || recovery.Task.Status != TaskFailed {
		t.Fatalf("disposition = %q status %s", recovery.Disposition(), recovery.Task.Status)
	}
	// The overseer's own task view carries the same exit evidence.
	overseerKeys, _ := runningOverseerKeys(t, store, running.ProjectID, 50)
	taskID := running.TaskID
	snapshot, err := store.OverseerSnapshotForAttempt(ctx, overseerKeys.AttemptDigest, OverseerSnapshotRequest{TaskID: &taskID})
	if err != nil || len(snapshot.Tasks) != 1 || !strings.HasSuffix(snapshot.Tasks[0].Result, "; provider exit code 1; ran 11 ms after activation") || !strings.HasPrefix(snapshot.Tasks[0].Result, "provider exited before an attempt outcome") {
		t.Fatalf("overseer view of the failed task = %+v, err=%v", snapshot.Tasks, err)
	}
}

func TestTaskRecoveryRefusesResourceCountBeyondBound(t *testing.T) {
	store, run, _ := admittedOrchestratorRun(t)
	defer store.Close()

	read, err := store.beginRead(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	if _, err := resourcesForRunBounded(context.Background(), read.connection, run.ID, 3); !errors.Is(err, ErrRecoveryBounds) {
		t.Fatalf("bounded resources error = %v", err)
	}
}

func TestTaskRecoveryRefusesTopologyBeyondBound(t *testing.T) {
	store, terminal, retry := retryAdmittedWorker(t, 50, 60)
	defer store.Close()
	task, found, err := store.Task(context.Background(), terminal.TaskID)
	if err != nil || !found {
		t.Fatalf("task = %+v, found=%v, err=%v", task, found, err)
	}

	read, err := store.beginRead(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	if err := validateTaskRunTopologyBounded(context.Background(), read.connection, task, 1); !errors.Is(err, ErrRecoveryBounds) {
		t.Fatalf("bounded topology error = %v (retry=%s)", err, retry.ID)
	}
}

// A task retried past the recovery projection's run bound is still readable
// through LatestTaskRun, which carries the last run's terminal diagnosis.
func TestLatestTaskRunReadsPastRecoveryBound(t *testing.T) {
	store, terminal, _ := terminalPreRunningWorker(t)
	defer store.Close()
	ctx := context.Background()
	loopID := func(kind, i byte) []byte { return append(bytes.Repeat([]byte{0xF0, kind}, IDBytes/2-1), i, 0xF0) }
	for i := byte(0); i < MaxRecoveryRuns; i++ {
		at := int64(100 + int64(i)*10)
		corruptSQL(t, store, `UPDATE tasks SET work_revision = work_revision + 1, status = 'queued', blocked_reason = NULL, result = NULL, completed_at_ms = NULL, revision = revision + 1, updated_at_ms = ? WHERE id = ?`, at, terminal.TaskID.Bytes())
		digest, _ := AttemptDigestFromBytes(bytes.Repeat([]byte{0xF0, i}, DigestBytes/2))
		proof, _ := ResultProofDigestFromBytes(bytes.Repeat([]byte{0xF1, i}, DigestBytes/2))
		runID, _ := RunIDFromBytes(loopID(1, i))
		session, _ := TerminalSessionIDFromBytes(loopID(2, i))
		candidate, _ := ChangeIDFromBytes(loopID(3, i))
		resource := func(kind byte) ResourceID { id, _ := ResourceIDFromBytes(loopID(kind, i)); return id }
		keys := AdmissionKeys{RunID: runID, TerminalSessionID: session, AttemptDigest: digest, ResultProofDigest: proof, CandidateChangeID: candidate, RuntimeRoot: "/runtime/loop-" + string(rune('a'+i%26)) + string(rune('a'+i/26)),
			Resources: AdmissionResourceIDs{RuntimeRoot: resource(4), RunnerProcess: resource(5), ProviderProcess: resource(6), ProviderGroup: resource(7)}}
		admission, err := store.AdmitNext(ctx, keys, mustTime(t, at+1))
		if err != nil || !admission.Admitted() {
			t.Fatalf("retry %d admission = %+v, %v", i, admission, err)
		}
		run := *admission.Run
		runtime := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRuntimeRoot)
		identity, _ := NewPathResourceIdentity(1000+int64(i), 2000+int64(i))
		if _, err := store.ActivateResource(ctx, run.ID, runtime.ID, runtime.Revision, identity, mustTime(t, at+2)); err != nil {
			t.Fatal(err)
		}
		failure, _ := NewFailureProposal(FailureInternal, "refusal "+string(rune('A'+i%26)))
		finalizing, err := store.FailRun(ctx, run.ID, run.Revision, failure, mustTime(t, at+3))
		if err != nil {
			t.Fatal(err)
		}
		runtime = resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRuntimeRoot)
		if _, err := store.ReleaseResource(ctx, run.ID, runtime.ID, runtime.Revision, runtime.Identity, mustTime(t, at+4)); err != nil {
			t.Fatal(err)
		}
		settlement, _ := NewAbandonedChangeSettlement(*run.AdmittedChangeRevision)
		if _, err := store.FinalizeWorkerRun(ctx, run.ID, finalizing.Revision, settlement, mustTime(t, at+5)); err != nil {
			t.Fatal(err)
		}
	}
	task, found, err := store.Task(ctx, terminal.TaskID)
	if err != nil || !found || task.Status != TaskFailed {
		t.Fatalf("task = %+v, found=%v, err=%v", task, found, err)
	}
	if _, _, err := store.TaskRecovery(ctx, task.ID, task.IncarnationID); !errors.Is(err, ErrRecoveryBounds) {
		t.Fatalf("bounded recovery past %d runs = %v", MaxRecoveryRuns, err)
	}
	run, found, err := store.LatestTaskRun(ctx, task.ID, task.IncarnationID)
	if err != nil || !found || run.Terminal == nil || run.Terminal.Detail() != "refusal "+string(rune('A'+(MaxRecoveryRuns-1)%26)) {
		t.Fatalf("latest run = %+v, found=%v, err=%v", run, found, err)
	}
}

func TestTaskRecoveryRefusesMismatchedIncarnation(t *testing.T) {
	store, run, _ := admittedOrchestratorRun(t)
	defer store.Close()
	wrong := incarnationID(t, 251)
	if wrong == run.TaskIncarnationID {
		t.Fatal("test incarnation unexpectedly matched")
	}
	if _, found, err := store.TaskRecovery(context.Background(), run.TaskID, wrong); err != nil || found {
		t.Fatalf("mismatched incarnation recovery = found=%v, err=%v", found, err)
	}
}

func TestTaskRecoveryReportsStaleHumanRequest(t *testing.T) {
	store, run, _ := runningOrchestratorRun(t)
	defer store.Close()
	if _, err := store.CreateHumanQuestionForAttempt(context.Background(), run.CredentialDigest, NewHumanQuestion{
		IdempotencyKey: humanKey(252), QuestionText: "operator recovery check",
	}, mustTime(t, 400)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ProposeAttemptOutcome(context.Background(), run.CredentialDigest, func() Proposal {
		proposal, _ := NewBlockedProposal("needs operator")
		return proposal
	}(), mustTime(t, 401)); err != nil {
		t.Fatal(err)
	}
	recovery, found, err := store.TaskRecovery(context.Background(), run.TaskID, run.TaskIncarnationID)
	if err != nil || !found || !recovery.NeedsOperatorRecovery {
		t.Fatalf("stale human recovery = %+v, found=%v, err=%v", recovery, found, err)
	}
}

func TestLatestRunForTaskUsesWorkRevisionForEqualAdmissionTimes(t *testing.T) {
	store, terminal, _ := terminalPreRunningWorker(t)
	defer store.Close()

	// The retry has the lower ID, so an ID tie-breaker would incorrectly pick
	// the predecessor when both rows have the same admission timestamp.
	_, retryKeys := queueRetryForTerminalSeed(t, store, terminal, 45, 180)
	second, err := store.AdmitNext(context.Background(), retryKeys, mustTime(t, 45))
	if err != nil || !second.Admitted() {
		t.Fatalf("retry admission = %+v, %v", second, err)
	}
	task, found, err := store.Task(context.Background(), terminal.TaskID)
	if err != nil || !found {
		t.Fatalf("task = %+v, found=%v, err=%v", task, found, err)
	}
	// Exercise the selection query directly with equal, scanner-valid run
	// timestamps. Other recovery tests cover complete lifecycle relationships.
	corruptSQL(t, store, `UPDATE runs SET admitted_at_ms = ?, updated_at_ms = ? WHERE id = ?`, terminal.AdmittedAt.Int64(), terminal.AdmittedAt.Int64(), second.Run.ID.Bytes())
	read, err := store.beginRead(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	latest, found, err := latestRunForTask(context.Background(), read.connection, task)
	if err != nil || !found || latest.ID != second.Run.ID || latest.AdmittedTaskWorkRevision.Int64() != 2 {
		t.Fatalf("latest run = %+v, found=%v, err=%v", latest, found, err)
	}
}
