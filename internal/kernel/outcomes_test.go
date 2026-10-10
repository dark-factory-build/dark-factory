package kernel

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func outcomeID(t *testing.T, seed byte) OutcomeID {
	t.Helper()
	id, err := OutcomeIDFromBytes(taskID(t, seed).Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func sourceOutcome() OutcomeDocument {
	return OutcomeDocument{Kind: "outcome", Objective: "reduce failed builds", Criteria: "a reproducible passing check", SourceIssue: "https://example.test/issues/1", State: "open"}
}

func TestOutcomeDocumentBounds(t *testing.T) {
	t.Parallel()
	d := sourceOutcome()
	d.Links = make([]OutcomeLink, OutcomeLinkLimit+1)
	if _, err := d.MarshalBounded(); err == nil {
		t.Fatal("oversized link set accepted")
	}
}

func TestOutcomeObjectiveHashIncludesExactWorkRevision(t *testing.T) {
	t.Parallel()
	one, _ := NewRevision(1)
	two, _ := NewRevision(2)
	if outcomeObjectiveHash("title", "body", one) == outcomeObjectiveHash("title", "body", two) {
		t.Fatal("objective hash ignored work revision")
	}
}

func TestOutcomeSourceReplayHistoryAndMetadata(t *testing.T) {
	t.Parallel()
	store, _ := newTestStore(t)
	defer store.Close()
	project, err := store.CreateProject(context.Background(), NewProject{ID: projectID(t, 90), Name: "outcomes", Root: "/outcomes"}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	spec := NewOutcome{ID: outcomeID(t, 91), ProjectID: project.ID, Document: sourceOutcome()}
	first, err := store.WriteOutcome(context.Background(), spec, 0, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision.Int64() != 1 || first.ObjectiveWorkRevision.Int64() != 1 {
		t.Fatalf("first = %+v", first)
	}
	secondDocument := sourceOutcome()
	secondDocument.Reason = "investigated"
	second, err := store.WriteOutcome(context.Background(), NewOutcome{ID: spec.ID, ProjectID: project.ID, Document: secondDocument}, 1, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision.Int64() != 2 {
		t.Fatalf("second = %+v", second)
	}
	replayed, err := store.WriteOutcome(context.Background(), spec, 0, mustTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Revision.Int64() != 1 {
		t.Fatalf("replay revision = %d", replayed.Revision.Int64())
	}
	changed := sourceOutcome()
	changed.Objective = "different"
	if _, err := store.WriteOutcome(context.Background(), NewOutcome{ID: spec.ID, ProjectID: project.ID, Document: changed}, 0, mustTime(t, 4)); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed create = %v", err)
	}
	page, err := store.ListOutcomes(context.Background(), project.ID, 0, 16)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Document.Objective != "" || page.Items[0].Objective != spec.Document.Objective || page.Items[0].State != "open" {
		t.Fatalf("metadata page = %+v", page)
	}
}

func TestWorkerMayOnlyProposeItsExactAnchor(t *testing.T) {
	t.Parallel()
	store, run, keys := runningWorkerRun(t)
	defer store.Close()
	anchored := OutcomeDocument{Kind: "outcome", Objective: "worker finding", Criteria: "record evidence", AnchorTaskID: run.TaskID.String(), AnchorWorkRevision: uint64(run.AdmittedTaskWorkRevision.Int64()), State: "proposed"}
	if _, err := store.WriteOutcomeForAttempt(context.Background(), keys.AttemptDigest, NewOutcome{ID: outcomeID(t, 100), ProjectID: run.ProjectID, Document: anchored}, 0, mustTime(t, 40)); err != nil {
		t.Fatal(err)
	}
	anchored.State = "accepted"
	anchored.Reason = "worker says so"
	anchored.Conclusion = "worker conclusion"
	anchored.Judgment = "authorized judgment: no"
	if _, err := store.WriteOutcomeForAttempt(context.Background(), keys.AttemptDigest, NewOutcome{ID: outcomeID(t, 101), ProjectID: run.ProjectID, Document: anchored}, 0, mustTime(t, 41)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("worker acceptance = %v", err)
	}
	anchored.State = "proposed"
	anchored.Objective = "rewritten objective"
	if _, err := store.WriteOutcomeForAttempt(context.Background(), keys.AttemptDigest, NewOutcome{ID: outcomeID(t, 100), ProjectID: run.ProjectID, Document: anchored}, 1, mustTime(t, 42)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("worker objective rewrite = %v", err)
	}
}

func TestOutcomeObjectiveEditMustReopenAcceptance(t *testing.T) {
	t.Parallel()
	store, _ := newTestStore(t)
	defer store.Close()
	project, err := store.CreateProject(context.Background(), NewProject{ID: projectID(t, 105), Name: "outcomes-acceptance", Root: "/outcomes-acceptance"}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	doc := sourceOutcome()
	doc.State = "accepted"
	doc.Reason = "reviewed"
	doc.Conclusion = "accepted evidence"
	doc.RemainingWork = "monitor the next release"
	doc.Judgment = "authorized judgment: operator review"
	id := outcomeID(t, 106)
	if _, err := store.WriteOutcome(context.Background(), NewOutcome{ID: id, ProjectID: project.ID, Document: doc}, 0, mustTime(t, 2)); err != nil {
		t.Fatal(err)
	}
	changed := doc
	changed.Objective = "changed source issue objective"
	if _, err := store.WriteOutcome(context.Background(), NewOutcome{ID: id, ProjectID: project.ID, Document: changed}, 1, mustTime(t, 3)); !errors.Is(err, ErrConflict) {
		t.Fatalf("accepted objective edit = %v", err)
	}
	changed.State = "reopened"
	if _, err := store.WriteOutcome(context.Background(), NewOutcome{ID: id, ProjectID: project.ID, Document: changed}, 1, mustTime(t, 4)); err != nil {
		t.Fatalf("reopen objective edit = %v", err)
	}
	historical, err := store.Outcome(context.Background(), project.ID, id, 1)
	if err != nil || !historical.Stale {
		t.Fatalf("source historical acceptance stale=%v err=%v", historical.Stale, err)
	}
}

func TestBrowserOutcomeWriterUsesLiveHumanAuthority(t *testing.T) {
	t.Parallel()
	store, _ := newBrowserStore(t)
	defer store.Close()
	ctx := context.Background()
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 125), Name: "browser-outcomes", Root: "/browser-outcomes"}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	boot := browserTestBoot(t, 126)
	readOnly := pairBrowserClient(t, store, mintBrowserChallenge(t, store, 126, boot, 2, 100, BrowserCapabilityObserve), boot, browserTestID(t, 127), browserKey(t), 3)
	if _, err := store.WriteOutcomeForBrowser(ctx, readOnly.ID, NewOutcome{ID: outcomeID(t, 128), ProjectID: project.ID, Document: sourceOutcome()}, 0, mustTime(t, 4)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("read-only browser write = %v", err)
	}
	client := pairBrowserClient(t, store, mintBrowserChallenge(t, store, 127, boot, 2, 100, BrowserCapabilityObserve|BrowserCapabilityPrivateHumanRequestDetail|BrowserCapabilityHumanActions), boot, browserTestID(t, 128), browserKey(t), 3)
	spec := NewOutcome{ID: outcomeID(t, 129), ProjectID: project.ID, Document: sourceOutcome()}
	written, err := store.WriteOutcomeForBrowser(ctx, client.ID, spec, 0, mustTime(t, 5))
	if err != nil {
		t.Fatal(err)
	}
	if written.Author != "browser:"+client.ID.String() || written.Authority != "human" {
		t.Fatalf("browser provenance = %+v", written)
	}
	revoked, err := store.RevokeBrowserClient(ctx, client.ID, client.Revision, mustTime(t, 6))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteOutcomeForBrowser(ctx, client.ID, NewOutcome{ID: outcomeID(t, 130), ProjectID: project.ID, Document: sourceOutcome()}, 0, mustTime(t, 7)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked browser write = %v", err)
	}
	if revoked.RevokedAt == nil {
		t.Fatal("browser was not revoked")
	}
}

func TestUnusedOutcomesDoNotEnterSnapshotOrAdmission(t *testing.T) {
	t.Parallel()
	store, _, project, agent := newAdmissionStore(t, RoleWorker, 2)
	defer store.Close()
	ctx := context.Background()
	task, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 107), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 108), Title: "ordinary"}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeFootprint := admissionFootprint(t, store)
	if _, err := store.WriteOutcome(ctx, NewOutcome{ID: outcomeID(t, 109), ProjectID: project.ID, Document: sourceOutcome()}, 0, mustTime(t, 3)); err != nil {
		t.Fatal(err)
	}
	after, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeFootprint, admissionFootprint(t, store)) {
		t.Fatalf("unused outcome changed snapshot or admission footprint")
	}
	if task.Status != TaskQueued {
		t.Fatal("outcome write changed task")
	}
}

func TestUnusedOutcomesDoNotAddAdmissionQueries(t *testing.T) {
	t.Parallel()
	type observed struct {
		result     AdmissionResult
		statements int
	}
	var observations [2]observed
	for index, populated := range []bool{false, true} {
		store, _, project, agent := newAdmissionStore(t, RoleWorker, 2)
		if _, err := store.EnqueueTask(context.Background(), NewTask{ID: taskID(t, 121), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 122), Title: "ordinary"}, mustTime(t, 2)); err != nil {
			store.Close()
			t.Fatal(err)
		}
		if populated {
			if _, err := store.WriteOutcome(context.Background(), NewOutcome{ID: outcomeID(t, 123), ProjectID: project.ID, Document: sourceOutcome()}, 0, mustTime(t, 3)); err != nil {
				store.Close()
				t.Fatal(err)
			}
		}
		result, statements := traceAdmission(t, store, admissionKeys(t, 124, nil), mustTime(t, 4))
		observations[index] = observed{result: result, statements: statements}
		store.Close()
	}
	if !reflect.DeepEqual(observations[0].result, observations[1].result) || observations[0].statements != observations[1].statements {
		t.Fatalf("unused outcome changed admission: empty=%+v populated=%+v", observations[0], observations[1])
	}
}

func TestOutcomeAnchorBecomesStaleAndProjectIdentityIsStable(t *testing.T) {
	t.Parallel()
	store, _ := newTestStore(t)
	defer store.Close()
	project, err := store.CreateProject(context.Background(), NewProject{ID: projectID(t, 110), Name: "outcomes-stale", Root: "/outcomes-stale"}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.EnqueueTask(context.Background(), NewTask{ID: taskID(t, 111), ProjectID: project.ID, IncarnationID: incarnationID(t, 112), Title: "anchor", Body: "first body"}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	doc := OutcomeDocument{Kind: "outcome", Objective: "tracked", Criteria: "reviewed", AnchorTaskID: task.ID.String(), AnchorWorkRevision: uint64(task.WorkRevision.Int64()), State: "open"}
	id := outcomeID(t, 113)
	if _, err := store.WriteOutcome(context.Background(), NewOutcome{ID: id, ProjectID: project.ID, Document: doc}, 0, mustTime(t, 3)); err != nil {
		t.Fatal(err)
	}
	revised := doc
	revised.Criteria = "revised criteria"
	if _, err := store.WriteOutcome(context.Background(), NewOutcome{ID: id, ProjectID: project.ID, Document: revised}, 1, mustTime(t, 4)); err != nil {
		t.Fatal(err)
	}
	historical, err := store.Outcome(context.Background(), project.ID, id, 1)
	if err != nil || !historical.Stale {
		t.Fatalf("task historical criteria stale=%v err=%v", historical.Stale, err)
	}
	body := "same revision field changes are stale too"
	if _, err := store.UpdateTask(context.Background(), task.ID, task.Revision, TaskPatch{Body: &body}, mustTime(t, 5)); err != nil {
		t.Fatal(err)
	}
	read, err := store.Outcome(context.Background(), project.ID, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !read.Stale {
		t.Fatalf("anchor edit did not mark stale: %+v", read)
	}
	if replayed, err := store.WriteOutcome(context.Background(), NewOutcome{ID: id, ProjectID: project.ID, Document: doc}, 0, mustTime(t, 5)); err != nil || replayed.Revision.Int64() != 1 {
		t.Fatalf("stale immutable replay = %+v, %v", replayed, err)
	}
	other, err := store.CreateProject(context.Background(), NewProject{ID: projectID(t, 114), Name: "outcomes-other", Root: "/outcomes-other"}, mustTime(t, 5))
	if err != nil {
		t.Fatal(err)
	}
	foreignTask, err := store.EnqueueTask(context.Background(), NewTask{ID: taskID(t, 115), ProjectID: other.ID, IncarnationID: incarnationID(t, 116), Title: "foreign"}, mustTime(t, 6))
	if err != nil {
		t.Fatal(err)
	}
	foreignAnchor := OutcomeDocument{Kind: "outcome", Objective: "no cross project", Criteria: "same project only", AnchorTaskID: foreignTask.ID.String(), AnchorWorkRevision: uint64(foreignTask.WorkRevision.Int64()), State: "open"}
	if _, err := store.WriteOutcome(context.Background(), NewOutcome{ID: outcomeID(t, 117), ProjectID: project.ID, Document: foreignAnchor}, 0, mustTime(t, 7)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("cross-project anchor = %v", err)
	}
	if _, err := store.WriteOutcome(context.Background(), NewOutcome{ID: id, ProjectID: other.ID, Document: sourceOutcome()}, 0, mustTime(t, 6)); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-project identity = %v", err)
	}
}

func TestStoredComparisonRowDoesNotBreakOutcomeList(t *testing.T) {
	t.Parallel()
	store, _ := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 150), Name: "legacy-comparison", Root: "/legacy-comparison"}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	kept, err := store.WriteOutcome(ctx, NewOutcome{ID: outcomeID(t, 151), ProjectID: project.ID, Document: sourceOutcome()}, 0, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	legacy := outcomeID(t, 152)
	corruptSQL(t, store, `INSERT INTO project_outcome_revisions(id, project_id, revision, document, author, authority, objective_hash, objective_work_revision, created_at_ms) VALUES(?, ?, 1, ?, 'operator:local', 'operator', ?, 1, 3)`, legacy.Bytes(), project.ID.Bytes(), `{"kind":"comparison","objective":"x","criteria":"y","state":"open","question":"q"}`, make([]byte, 32))
	page, err := store.ListOutcomes(ctx, project.ID, 0, 16)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != kept.ID {
		t.Fatalf("list = %+v, %v", page, err)
	}
	if _, err := store.Outcome(ctx, project.ID, legacy, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("legacy comparison read = %v", err)
	}
}
