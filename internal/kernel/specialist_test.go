package kernel

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

const specialistCadence = int64(60_000)

func makeSpecialist(t *testing.T, store *Store, agent Agent, wakeOn string, at int64) Agent {
	t.Helper()
	policy, after, instruction := IdleStandingInstruction, uint32(specialistCadence/1000), "Review the factory."
	updated, err := store.UpdateAgent(context.Background(), agent.ID, agent.Revision, AgentPatch{IdlePolicy: &policy, IdleAfterSeconds: &after, IdleInstruction: &instruction, IdleWakeOn: &wakeOn}, mustTime(t, at))
	if err != nil || !updated.Specialist() {
		t.Fatalf("make specialist = %+v, %v", updated, err)
	}
	return updated
}

// runCarrier admits the queued specialist carrier and settles it successfully,
// as settleWorkerRunForTest does but with process identities unique to each
// run. Its seeds must keep every run's identities and runtime root distinct.
func runCarrier(t *testing.T, store *Store, seed byte, at int64) (Task, Run) {
	t.Helper()
	keys := admissionKeys(t, seed, nil)
	admission, err := store.AdmitNext(context.Background(), keys, mustTime(t, at))
	if err != nil || !admission.Admitted() {
		t.Fatalf("carrier admission = %+v, %v", admission, err)
	}
	ctx := context.Background()
	format, _ := NewObjectFormat("sha1")
	commit, _ := NewCommitID(format, bytes.Repeat([]byte{1}, 20))
	repository, _ := NewFileIdentity(61, 62)
	selection, _ := NewChangeSelection(format, commit, repository)
	prepared, err := store.RecordChangePrepared(ctx, *admission.Run.ChangeID, mustRevision(t, 1), selection, mustTime(t, at+1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkChangeAvailable(ctx, prepared.ID, prepared.Revision, selection.commit, mustTime(t, at+1)); err != nil {
		t.Fatal(err)
	}
	run := activateAllResourcesUnique(t, store, *admission.Run, at+2, int64(seed))
	session := terminalSessionForRunTest(t, store, run.ID)
	if _, err := store.ActivateRun(ctx, run.ID, session.ID, run.Revision, session.Revision, mustTime(t, at+10)); err != nil {
		t.Fatal(err)
	}
	proposal, _ := NewSuccessProposal("done")
	if _, err := store.ProposeAttemptOutcome(ctx, keys.AttemptDigest, proposal, mustTime(t, at+11)); err != nil {
		t.Fatal(err)
	}
	observeMissingProcessExits(t, store, run.ID, at+12)
	for index, resource := range resourcesForRunTest(t, store, run.ID) {
		if resource.State != ResourceReleased {
			if _, err := store.ReleaseResource(ctx, run.ID, resource.ID, resource.Revision, resource.Identity, mustTime(t, at+15+int64(index))); err != nil {
				t.Fatal(err)
			}
		}
	}
	closeTerminalSessionAtCurrent(t, store, run.ID, at+20)
	finalizing, _, err := store.Run(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := finalizeTestRun(t, store, finalizing, at+21)
	if err != nil {
		t.Fatal(err)
	}
	task, _, err := store.Task(ctx, terminal.TaskID)
	if err != nil || task.Title != overseerWakeTitle || task.Status != TaskSucceeded {
		t.Fatalf("carrier = %+v, %v", task, err)
	}
	return task, terminal
}

func specialistStateOf(t *testing.T, store *Store, id AgentID) SpecialistState {
	t.Helper()
	snapshot, err := store.ReadPublicSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range snapshot.Agents {
		if agent.ID == id && agent.Specialist != nil {
			return *agent.Specialist
		}
	}
	t.Fatalf("no specialist state for %s", id)
	return SpecialistState{}
}

func setSpecialistLimits(t *testing.T, store *Store, project ProjectID, runs, proposals *uint32, at int64) {
	t.Helper()
	current, _, err := store.Project(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetProjectLimitsWithTokens(context.Background(), project, current.Revision, 0, 0, nil, runs, proposals, mustTime(t, at)); err != nil {
		t.Fatal(err)
	}
}

// A specialist is due at once, then a cadence after each review, doubling for
// each quiet review up to 8x; a contribution resets the backoff, and only its
// wake_on classes bring a review forward, a quarter cadence after the last. A
// review escalation is the overseer's, never an event.
// It holds at most one carrier, across a restart, and pausing or an
// exhausted budget stops its wakes.
func TestSpecialistSchedule(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, path, project, agent := newAdmissionStore(t, RoleWorker, 2)
	agent = makeSpecialist(t, store, agent, "failures", 10)
	if state := specialistStateOf(t, store, agent.ID); state.NextReason != "initial" || state.NextReviewAt != 10 || state.Waiting != "" {
		t.Fatalf("initial state = %+v", state)
	}
	bodies := wakeBodies(t, store, 20)
	if len(bodies) != 1 || !strings.HasPrefix(bodies[0], "Review the factory.\n\nSpecialist wake: reason=initial; prior_task_id=; prior_base=; quiet_reviews=0; open_proposals=0/3; reviews_used=1/unlimited.\nPrior checkpoint:\nnone") {
		t.Fatalf("initial wake = %q", bodies)
	}
	if again := wakeBodies(t, store, 21); len(again) != 0 {
		t.Fatalf("second carrier = %q", again)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if again := wakeBodies(t, store, 22); len(again) != 0 {
		t.Fatalf("carrier after restart = %q", again)
	}
	if state := specialistStateOf(t, store, agent.ID); state.Waiting != "queued" || state.NextReviewAt != 0 {
		t.Fatalf("queued state = %+v", state)
	}

	at := int64(100)
	var task Task
	var run Run
	for round, factor := range []int64{2, 4, 8, 8} {
		task, run = runCarrier(t, store, byte(30+25*round), at)
		state := specialistStateOf(t, store, agent.ID)
		if want := task.CompletedAt.Int64() + factor*specialistCadence; state.NextReason != "scheduled" || state.NextReviewAt != want || state.QuietReviews != min(round+1, 3) || state.LastReviewTaskID != task.ID.String() {
			t.Fatalf("round %d state = %+v, want next %d", round, state, want)
		}
		if early := wakeBodies(t, store, state.NextReviewAt-1); len(early) != 0 {
			t.Fatalf("round %d early wake = %q", round, early)
		}
		next := wakeBodies(t, store, state.NextReviewAt)
		if len(next) != 1 || !strings.Contains(next[0], "reason=scheduled; prior_task_id="+task.ID.String()+"; prior_base=0101010101010101010101010101010101010101;") || !strings.Contains(next[0], "\nPrior checkpoint:\ndone") {
			t.Fatalf("round %d wake = %q", round, next)
		}
		at = state.NextReviewAt + 1
	}

	// A contribution resets the backoff; it is not an event, and neither is a
	// merge for a specialist woken only by failures.
	task, run = runCarrier(t, store, 131, at)
	note := knowledgeSpec(t, project.ID, 91, ContentObservation, KnowledgeMetadata{Status: "tentative"})
	note.Author = contentProvenance(AttemptAuthority{RunID: run.ID, AgentID: agent.ID, Role: RoleWorker})
	if _, err := store.CreateContent(ctx, note, mustTime(t, at+30)); err != nil {
		t.Fatal(err)
	}
	escalateAt := at + 40
	if _, err := store.writer.ExecContext(ctx, `INSERT INTO production_records (project_id, repository, kind, identity, visual_id, document, observed_at_ms) VALUES (?, 'example/factory', 'pull_request', '9', '', '{"number":9,"title":"merged work","state":"merged"}', ?)`, project.ID.Bytes(), escalateAt); err != nil {
		t.Fatal(err)
	}
	settled := task.CompletedAt.Int64()
	if state := specialistStateOf(t, store, agent.ID); state.NextReason != "scheduled" || state.NextReviewAt != settled+specialistCadence || state.QuietReviews != 0 {
		t.Fatalf("contributed state = %+v", state)
	}
	escalate(t, store, project.ID, "7", "open", strings.Repeat("a", 40), strings.Repeat("a", 40), escalateAt)
	if state := specialistStateOf(t, store, agent.ID); state.NextReason != "scheduled" {
		t.Fatalf("escalation woke the specialist: %+v", state)
	}
	current, _, _ := store.Agent(ctx, agent.ID)
	makeSpecialist(t, store, current, "failures,merges", escalateAt+1)
	state := specialistStateOf(t, store, agent.ID)
	if state.NextReason != "events" || state.NextReviewAt != settled+specialistCadence/4 {
		t.Fatalf("event state = %+v", state)
	}
	if early := wakeBodies(t, store, state.NextReviewAt-1); len(early) != 0 {
		t.Fatalf("early event wake = %q", early)
	}
	next := wakeBodies(t, store, state.NextReviewAt)
	if len(next) != 1 || !strings.HasSuffix(next[0], "\nEvents since prior review:\n- PR #9 merged: merged work") {
		t.Fatalf("event wake = %q", next)
	}

	// Pausing, or an exhausted budget, stops its wakes.
	task, _ = runCarrier(t, store, 157, state.NextReviewAt+1)
	current, _, _ = store.Agent(ctx, agent.ID)
	paused := pauseAgent(t, store, current, true, task.CompletedAt.Int64()+1)
	if state := specialistStateOf(t, store, agent.ID); state.Waiting != "paused" || state.NextReviewAt != 0 {
		t.Fatalf("paused state = %+v", state)
	}
	if woken := wakeBodies(t, store, 1<<40); len(woken) != 0 {
		t.Fatalf("paused wake = %q", woken)
	}
	resumed := pauseAgent(t, store, paused, false, task.CompletedAt.Int64()+2)
	budget := resumed.Idle.RunsUsed
	exhausted, err := store.UpdateAgent(ctx, agent.ID, resumed.Revision, AgentPatch{IdleRunBudget: &budget}, mustTime(t, task.CompletedAt.Int64()+3))
	if err != nil {
		t.Fatal(err)
	}
	if state := specialistStateOf(t, store, exhausted.ID); state.Waiting != "budget" {
		t.Fatalf("budget state = %+v", state)
	}
	if woken := wakeBodies(t, store, 1<<40); len(woken) != 0 {
		t.Fatalf("exhausted wake = %q", woken)
	}
}

// A specialist never claims shared work. Its carrier needs a worker slot
// spare for explicit work and room under specialist_runs, which report as
// capacity; work assigned to it explicitly is admitted as usual.
func TestSpecialistAdmission(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, project, specialist, worker := newSharedQueueStore(t, 2)
	defer store.Close()
	specialist = makeSpecialist(t, store, specialist, "", 5)
	shared := sharedTask(t, store, project, 10, 0, 6)
	worker = pauseAgent(t, store, worker, true, 7)
	if admission, err := store.AdmitNext(ctx, admissionKeys(t, 40, nil), mustTime(t, 8)); err != nil || admission.Admitted() {
		t.Fatalf("specialist claimed shared work: %+v, %v", admission, err)
	}
	pauseAgent(t, store, worker, false, 9)
	if admission, err := store.AdmitNext(ctx, admissionKeys(t, 40, nil), mustTime(t, 10)); err != nil || !admission.Admitted() || admission.Run.TaskID != shared.ID || admission.Run.AgentID != worker.ID {
		t.Fatalf("shared admission = %+v, %v", admission, err)
	}
	if bodies := wakeBodies(t, store, 11); len(bodies) != 1 {
		t.Fatalf("carrier = %q", bodies)
	}
	if admission, err := store.AdmitNext(ctx, admissionKeys(t, 50, nil), mustTime(t, 12)); err != nil || admission.Admitted() || admission.Reason != NoAdmissionAtCapacity {
		t.Fatalf("carrier took the spare slot: %+v, %v", admission, err)
	}
	explicit, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 60), ProjectID: project.ID, AssignedAgentID: specialist.ID, IncarnationID: incarnationID(t, 61), Title: "explicit"}, mustTime(t, 13))
	if err != nil {
		t.Fatal(err)
	}
	if admission, err := store.AdmitNext(ctx, admissionKeys(t, 50, nil), mustTime(t, 14)); err != nil || !admission.Admitted() || admission.Run.TaskID != explicit.ID {
		t.Fatalf("explicit admission = %+v, %v", admission, err)
	}

	store, _, project, specialist, _ = newSharedQueueStore(t, 3)
	defer store.Close()
	makeSpecialist(t, store, specialist, "", 5)
	zero, one := uint32(0), uint32(1)
	setSpecialistLimits(t, store, project.ID, &zero, nil, 6)
	if bodies := wakeBodies(t, store, 7); len(bodies) != 1 {
		t.Fatalf("carrier = %q", bodies)
	}
	if admission, err := store.AdmitNext(ctx, admissionKeys(t, 40, nil), mustTime(t, 8)); err != nil || admission.Reason != NoAdmissionAtCapacity {
		t.Fatalf("carrier over specialist_runs: %+v, %v", admission, err)
	}
	if state := specialistStateOf(t, store, specialist.ID); state.Waiting != "capacity" {
		t.Fatalf("held state = %+v", state)
	}
	setSpecialistLimits(t, store, project.ID, &one, nil, 9)
	if admission, err := store.AdmitNext(ctx, admissionKeys(t, 40, nil), mustTime(t, 10)); err != nil || !admission.Admitted() || admission.Run.AgentID != specialist.ID {
		t.Fatalf("carrier admission = %+v, %v", admission, err)
	}
}

// A specialist's carrier is never an overseer item; an open proposal is one
// until a decision resolves it.
func TestSpecialistOverseerItems(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, worker, _ := wakeFixture(t)
	worker = makeSpecialist(t, store, worker, "", 5)
	if bodies := wakeBodies(t, store, 10); len(bodies) != 1 || !strings.Contains(bodies[0], "Specialist wake") {
		t.Fatalf("carrier = %q", bodies)
	}
	carrier, _ := runCarrier(t, store, 40, 20)
	at := int64(200_000)
	for _, body := range wakeBodies(t, store, at) {
		if strings.Contains(body, "Factory causal wake") {
			t.Fatalf("carrier %s woke the overseer: %q", carrier.ID, body)
		}
	}
	proposal := knowledgeSpec(t, worker.ProjectID, 81, ContentObservation, KnowledgeMetadata{Status: "tentative", RecordType: "proposal"})
	proposal.Title = "Split the gate"
	proposal.Author = "run:00 agent:" + worker.ID.String() + " role:worker"
	if _, err := store.CreateContent(ctx, proposal, mustTime(t, at)); err != nil {
		t.Fatal(err)
	}
	line := "\nProposal " + proposal.ID.String() + " from worker: Split the gate [proposal:" + proposal.ID.String() + "]"
	at += overseerWakeSettle.Milliseconds()
	if bodies := wakeBodies(t, store, at); len(bodies) != 1 || !strings.Contains(bodies[0], line) {
		t.Fatalf("proposal wake = %q", bodies)
	}
	settleCarrier(t, store, at+1, 50, "ran")
	decision := knowledgeSpec(t, worker.ProjectID, 82, ContentDecision, KnowledgeMetadata{Status: "tentative", Evidence: []string{"proposal:" + proposal.ID.String()}, RecordType: "proposal", RecordID: proposal.ID.String()})
	if _, err := store.CreateContent(ctx, decision, mustTime(t, at+20)); err != nil {
		t.Fatal(err)
	}
	for _, body := range wakeBodies(t, store, at+2*OverseerRewakeAfter.Milliseconds()) {
		if strings.Contains(body, "Proposal ") {
			t.Fatalf("resolved proposal woke the overseer: %q", body)
		}
	}
}

// A run makes one proposal and an agent keeps a bounded number open; only the
// overseer or the operator resolves one, with one accepted implementation
// active at a time; a review names its source revision and research its
// evidence.
func TestSpecialistRecordLimits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, run, _ := runningWorkerRun(t)
	defer store.Close()
	proposal := func(seed byte) NewContent {
		return knowledgeSpec(t, run.ProjectID, seed, ContentObservation, KnowledgeMetadata{Status: "tentative", RecordType: "proposal"})
	}
	zero, three := uint32(0), uint32(3)
	setSpecialistLimits(t, store, run.ProjectID, nil, &zero, 40)
	if _, err := store.CreateContentForAttempt(ctx, run.CredentialDigest, proposal(90), mustTime(t, 41)); !errors.Is(err, ErrConflict) {
		t.Fatalf("proposal over the open cap: %v", err)
	}
	setSpecialistLimits(t, store, run.ProjectID, nil, &three, 42)
	first, err := store.CreateContentForAttempt(ctx, run.CredentialDigest, proposal(90), mustTime(t, 43))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateContentForAttempt(ctx, run.CredentialDigest, proposal(91), mustTime(t, 44)); !errors.Is(err, ErrConflict) {
		t.Fatalf("second proposal in one run: %v", err)
	}
	resolution := func(seed byte, proposal ContentID, task string) NewContent {
		return knowledgeSpec(t, run.ProjectID, seed, ContentDecision, KnowledgeMetadata{Status: "tentative", Evidence: []string{"proposal:" + proposal.String()}, RecordType: "proposal", RecordID: proposal.String(), TaskID: task})
	}
	if _, err := store.CreateContentForAttempt(ctx, run.CredentialDigest, resolution(92, first.ID, ""), mustTime(t, 45)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("worker resolved a proposal: %v", err)
	}
	review := knowledgeSpec(t, run.ProjectID, 93, ContentObservation, KnowledgeMetadata{Status: "tentative", RecordType: "review"})
	if err := store.ValidateKnowledgeWrite(ctx, review, Revision{}); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("review without source_revision: %v", err)
	}
	review.SourceReferences = `{"status":"tentative","record_type":"review","source_revision":"` + strings.Repeat("ab", 20) + `"}`
	if err := store.ValidateKnowledgeWrite(ctx, review, Revision{}); err != nil {
		t.Fatalf("bound review: %v", err)
	}
	research := knowledgeSpec(t, run.ProjectID, 94, ContentObservation, KnowledgeMetadata{Status: "tentative", RecordType: "research"})
	if err := store.ValidateKnowledgeWrite(ctx, research, Revision{}); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("research without evidence: %v", err)
	}

	second := proposal(95)
	if _, err := store.CreateContent(ctx, second, mustTime(t, 46)); err != nil {
		t.Fatal(err)
	}
	var tasks []Task
	for _, seed := range []byte{100, 102} {
		task, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, seed), ProjectID: run.ProjectID, IncarnationID: incarnationID(t, seed+1), Title: "implement"}, mustTime(t, 47))
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
	}
	overseer := runningOverseerDigest(t, store, run.ProjectID, 50)
	if _, err := store.CreateContentForAttempt(ctx, overseer, resolution(96, first.ID, tasks[0].ID.String()), mustTime(t, 60)); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if _, err := store.CreateContentForAttempt(ctx, overseer, resolution(97, second.ID, tasks[1].ID.String()), mustTime(t, 61)); !errors.Is(err, ErrConflict) {
		t.Fatalf("second active implementation: %v", err)
	}
	if _, err := store.CreateContentForAttempt(ctx, overseer, resolution(97, second.ID, ""), mustTime(t, 62)); err != nil {
		t.Fatalf("decline: %v", err)
	}
}

// Every record shape the specialist runbook uses is creatable by an attempt,
// and each names what it is about.
func TestSpecialistRunbookRecords(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, run, _ := runningWorkerRun(t)
	defer store.Close()
	proposal, err := store.CreateContentForAttempt(ctx, run.CredentialDigest, knowledgeSpec(t, run.ProjectID, 90, ContentObservation, KnowledgeMetadata{Status: "tentative", RecordType: "proposal"}), mustTime(t, 40))
	if err != nil {
		t.Fatal(err)
	}
	revision := strings.Repeat("ab", 20)
	for index, test := range []struct {
		metadata KnowledgeMetadata
		valid    bool
	}{
		{KnowledgeMetadata{Status: "tentative", RecordType: "contribution", TaskID: run.TaskID.String()}, true},
		{KnowledgeMetadata{Status: "tentative", RecordType: "contribution", RecordID: "issue:#12"}, true},
		{KnowledgeMetadata{Status: "tentative", RecordType: "contribution"}, false},
		{KnowledgeMetadata{Status: "tentative", RecordType: "amendment", TaskID: run.TaskID.String()}, true},
		{KnowledgeMetadata{Status: "tentative", RecordType: "amendment"}, false},
		{KnowledgeMetadata{Status: "tentative", RecordType: "review", SourceRevision: revision, Evidence: []string{"pr:#12"}}, true},
		{KnowledgeMetadata{Status: "tentative", RecordType: "review", Evidence: []string{"pr:#12"}}, false},
		{KnowledgeMetadata{Status: "tentative", RecordType: "review", SourceRevision: revision, RecordID: "pr:#12"}, false},
		{KnowledgeMetadata{Status: "tentative", RecordType: "research", Evidence: []string{"https://example.com/doc"}}, true},
		{KnowledgeMetadata{Status: "tentative", RecordType: "follow_up", RecordID: proposal.ID.String()}, true},
		{KnowledgeMetadata{Status: "tentative", RecordType: "follow_up", RecordID: run.TaskID.String()}, false},
	} {
		_, err := store.CreateContentForAttempt(ctx, run.CredentialDigest, knowledgeSpec(t, run.ProjectID, byte(100+index), ContentObservation, test.metadata), mustTime(t, 41+int64(index)))
		if (err == nil) != test.valid {
			t.Fatalf("%+v: err=%v, want valid=%v", test.metadata, err, test.valid)
		}
	}
}

// A specialist's review reads its project's overseer status; it never writes
// an overseer control. A specialist's explicitly assigned work reads it no
// more than a plain worker does.
func TestSpecialistReadsOverseerStatusOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, run, _ := runningWorkerRun(t)
	defer store.Close()
	agent, _, err := store.Agent(ctx, run.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	for _, specialist := range []bool{false, true} {
		if specialist {
			makeSpecialist(t, store, agent, "", 40)
		}
		if _, err := store.OverseerSnapshotForAttempt(ctx, run.CredentialDigest, OverseerSnapshotRequest{}); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("explicit work snapshot (specialist=%v): %v", specialist, err)
		}
	}

	store, _, project, agent := newAdmissionStore(t, RoleWorker, 3)
	defer store.Close()
	makeSpecialist(t, store, agent, "", 4)
	wakeBodies(t, store, 5)
	snapshot, err := store.ReadPublicSnapshot(ctx)
	if err != nil || len(snapshot.Tasks) != 1 {
		t.Fatalf("tasks = %+v, %v", snapshot.Tasks, err)
	}
	review := activatePeerWorker(t, store, Task{ID: snapshot.Tasks[0].ID}, 70)
	if read, err := store.OverseerSnapshotForAttempt(ctx, review.CredentialDigest, OverseerSnapshotRequest{}); err != nil || read.ProjectID != project.ID {
		t.Fatalf("review snapshot = %+v, %v", read, err)
	}
	if _, err := store.EnqueueTaskForOverseer(ctx, review.CredentialDigest, NewTask{ID: taskID(t, 120), ProjectID: project.ID, IncarnationID: incarnationID(t, 121), Title: "write"}, mustTime(t, 51)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("specialist overseer write: %v", err)
	}
}

func TestSpecialistCarrierTitleCannotBeForgedByOverseerOrTaskUpdate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, run, keys := runningOrchestratorRun(t)
	defer store.Close()
	worker, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 126), ProjectID: run.ProjectID, Name: "specialist", Role: RoleWorker, Provider: ProviderShell, ToolBudgetLimit: 1}, mustTime(t, 40))
	if err != nil {
		t.Fatal(err)
	}
	worker = makeSpecialist(t, store, worker, "", 41)
	if _, err := store.EnqueueTaskForOverseer(ctx, keys.AttemptDigest, NewTask{ID: taskID(t, 121), ProjectID: run.ProjectID, AssignedAgentID: worker.ID, IncarnationID: incarnationID(t, 122), Title: overseerWakeTitle}, mustTime(t, 42)); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("overseer carrier forge = %v", err)
	}
	task, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 123), ProjectID: run.ProjectID, AssignedAgentID: worker.ID, IncarnationID: incarnationID(t, 124), Title: "ordinary"}, mustTime(t, 43))
	if err != nil {
		t.Fatal(err)
	}
	title := overseerWakeTitle
	if _, err := store.UpdateTask(ctx, task.ID, task.Revision, TaskPatch{Title: &title}, mustTime(t, 44)); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("task update carrier forge = %v", err)
	}
	carrier, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 127), ProjectID: run.ProjectID, AssignedAgentID: worker.ID, IncarnationID: incarnationID(t, 128), Title: overseerWakeTitle}, mustTime(t, 45))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateTask(ctx, carrier.ID, carrier.Revision, TaskPatch{Cancel: true}, mustTime(t, 46)); err != nil {
		t.Fatalf("carrier cancellation = %v", err)
	}
	plainStore, plainRun, _ := runningWorkerRun(t)
	defer plainStore.Close()
	plainAgent, _, err := plainStore.Agent(ctx, plainRun.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	makeSpecialist(t, plainStore, plainAgent, "", 40)
	authority, err := plainStore.AuthenticateAttempt(ctx, plainRun.CredentialDigest)
	if err != nil || authority.Specialist {
		t.Fatalf("ordinary specialist authority = %+v, %v", authority, err)
	}
}

// Archiving a specialist mid-review stops it as an operator stop would: the
// run is finalizing with its credential revoked, so it can record nothing
// more; its queued carriers are cancelled and other work is untouched. Other
// work queued for it refuses the archive.
func TestArchiveStopsSpecialist(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, project, agent := newAdmissionStore(t, RoleWorker, 3)
	defer store.Close()
	agent = makeSpecialist(t, store, agent, "", 4)
	if bodies := wakeBodies(t, store, 5); len(bodies) != 1 {
		t.Fatalf("carrier = %q", bodies)
	}
	snapshot, err := store.ReadPublicSnapshot(ctx)
	if err != nil || len(snapshot.Tasks) != 1 {
		t.Fatalf("tasks = %+v, %v", snapshot.Tasks, err)
	}
	run := activatePeerWorker(t, store, Task{ID: snapshot.Tasks[0].ID}, 70)
	queued, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 80), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 81), Title: overseerWakeTitle, Body: "Review."}, mustTime(t, 60))
	if err != nil {
		t.Fatal(err)
	}
	shared := sharedTask(t, store, project, 82, 0, 61)
	explicit, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 84), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 85), Title: "explicit"}, mustTime(t, 62))
	if err != nil {
		t.Fatal(err)
	}
	current, _, _ := store.Agent(ctx, agent.ID)
	wait, none, zero := IdleWait, "", uint32(0)
	if _, err := store.UpdateAgent(ctx, agent.ID, current.Revision, AgentPatch{IdlePolicy: &wait, IdleAfterSeconds: &zero, IdleInstruction: &none}, mustTime(t, 62)); !errors.Is(err, ErrConflict) {
		t.Fatalf("policy change under a running review: %v", err)
	}
	archived := true
	if _, err := store.UpdateAgent(ctx, agent.ID, current.Revision, AgentPatch{Archived: &archived}, mustTime(t, 63)); !errors.Is(err, ErrConflict) {
		t.Fatalf("archive with explicit work: %v", err)
	}
	if _, err := store.UpdateTask(ctx, explicit.ID, explicit.Revision, TaskPatch{Cancel: true}, mustTime(t, 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateAgent(ctx, agent.ID, current.Revision, AgentPatch{Archived: &archived}, mustTime(t, 65)); err != nil {
		t.Fatalf("archive: %v", err)
	}
	stopped, _, err := store.Run(ctx, run.ID)
	if err != nil || stopped.Phase != RunFinalizing || stopped.CredentialRevokedAt == nil {
		t.Fatalf("stopped run = %+v, %v", stopped, err)
	}
	note := knowledgeSpec(t, project.ID, 86, ContentObservation, KnowledgeMetadata{Status: "tentative"})
	if _, err := store.CreateContentForAttempt(ctx, run.CredentialDigest, note, mustTime(t, 66)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("stopped run wrote content: %v", err)
	}
	if task, _, _ := store.Task(ctx, queued.ID); task.Status != TaskCancelled {
		t.Fatalf("queued carrier = %+v", task)
	}
	if task, _, _ := store.Task(ctx, shared.ID); task.Status != TaskQueued || task.Revision != shared.Revision {
		t.Fatalf("shared task = %+v", task)
	}
}

// A contribution, amendment or review naming an active task of the project is
// attached to it; a proposal, a terminal task and another project's task are
// not.
func TestSpecialistRecordAttachesToActiveTask(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, run, _ := runningWorkerRun(t)
	defer store.Close()
	enqueue := func(seed byte) Task {
		task, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, seed), ProjectID: run.ProjectID, IncarnationID: incarnationID(t, seed+1), Title: "work"}, mustTime(t, 40))
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	attached := func(task Task) int {
		refs, err := store.TaskContentReferences(ctx, task.ProjectID, task.ID, task.WorkRevision)
		if err != nil {
			t.Fatal(err)
		}
		return len(refs)
	}
	create := func(seed byte, metadata KnowledgeMetadata) error {
		metadata.Status, metadata.SourceRevision = "tentative", strings.Repeat("ab", 20)
		_, err := store.CreateContentForAttempt(ctx, run.CredentialDigest, knowledgeSpec(t, run.ProjectID, seed, ContentObservation, metadata), mustTime(t, 50))
		return err
	}
	queued := enqueue(120)
	for i, kind := range []string{"contribution", "amendment", "review"} {
		if err := create(byte(130+i), KnowledgeMetadata{RecordType: kind, TaskID: queued.ID.String()}); err != nil {
			t.Fatal(err)
		}
	}
	if got := attached(queued); got != 3 {
		t.Fatalf("queued task references = %d, want 3", got)
	}
	if err := create(140, KnowledgeMetadata{RecordType: "contribution", TaskID: run.TaskID.String()}); err != nil || attached(Task{ID: run.TaskID, ProjectID: run.ProjectID, WorkRevision: mustRevision(t, run.AdmittedTaskWorkRevision.Int64())}) != 1 {
		t.Fatalf("running task attach: %v", err)
	}
	proposal := enqueue(122)
	if err := create(141, KnowledgeMetadata{RecordType: "proposal"}); err != nil || attached(proposal) != 0 {
		t.Fatalf("proposal attached: %v", err)
	}
	terminal := enqueue(124)
	if _, err := store.writer.ExecContext(ctx, `UPDATE tasks SET status = 'cancelled', completed_at_ms = 42, updated_at_ms = 42 WHERE id = ?`, terminal.ID.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := create(142, KnowledgeMetadata{RecordType: "contribution", TaskID: terminal.ID.String()}); err != nil || attached(terminal) != 0 {
		t.Fatalf("terminal task attached: %v", err)
	}
	other, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 150), Name: "other", Root: "/other-attach"}, mustTime(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 151), ProjectID: other.ID, IncarnationID: incarnationID(t, 152), Title: "foreign"}, mustTime(t, 5))
	if err != nil {
		t.Fatal(err)
	}
	if err := create(143, KnowledgeMetadata{RecordType: "contribution", TaskID: foreign.ID.String()}); !errors.Is(err, ErrInvalidValue) || attached(foreign) != 0 {
		t.Fatalf("other project's task: %v", err)
	}
}
