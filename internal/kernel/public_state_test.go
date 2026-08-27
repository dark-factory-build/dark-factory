package kernel

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestPublicStateProjectPageBoundariesAndRawIDOrder(t *testing.T) {
	for _, count := range []int{0, 1, 7, 8, 9} {
		t.Run(fmt.Sprintf("rows_%d", count), func(t *testing.T) {
			store, _ := newTestStore(t)
			defer store.Close()
			insertPublicProjects(t, store, count)
			state, err := store.Factory(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			page, err := store.ReadPublicStatePage(context.Background(), &PublicStateCursor{Head: state.Head, Kind: PublicStateProject})
			if err != nil {
				t.Fatal(err)
			}
			wantFirst := count
			if wantFirst > PublicStatePageSize {
				wantFirst = PublicStatePageSize
			}
			if len(page.Items) != wantFirst || page.Kind != PublicStateProject || page.Head != state.Head {
				t.Fatalf("page = %+v, items=%d", page, len(page.Items))
			}
			for index, item := range page.Items {
				summary, ok := item.Project()
				if !ok || summary.ID != publicProjectID(t, index+1) {
					t.Fatalf("item %d = %+v, project=%+v", index, item, summary)
				}
			}
			if count == 9 {
				if page.NextCursor == nil || page.NextCursor.Kind != PublicStateProject || page.NextCursor.AfterID == nil || page.NextCursor.AfterID.String() != publicProjectID(t, 8).String() {
					t.Fatalf("nine-row continuation = %+v", page.NextCursor)
				}
				last, err := store.ReadPublicStatePage(context.Background(), page.NextCursor)
				if err != nil {
					t.Fatal(err)
				}
				summary, ok := last.Items[0].Project()
				if len(last.Items) != 1 || !ok || summary.ID != publicProjectID(t, 9) || last.NextCursor == nil || last.NextCursor.Kind != PublicStateAgent || last.NextCursor.AfterID != nil {
					t.Fatalf("nine-row tail = %+v, project=%+v", last, summary)
				}
			} else if page.NextCursor == nil || page.NextCursor.Kind != PublicStateAgent || page.NextCursor.AfterID != nil {
				t.Fatalf("short-page continuation = %+v", page.NextCursor)
			}
		})
	}
}

func TestPublicStateTraversalKeepsEveryKindAndNoRowsAreLost(t *testing.T) {
	store, run, _ := runningOrchestratorRun(t)
	defer store.Close()
	ctx := context.Background()
	project, found, err := store.Project(ctx, run.ProjectID)
	if err != nil || !found {
		t.Fatalf("project = %+v, found=%v, err=%v", project, found, err)
	}
	state, err := store.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetCapacity(ctx, state.Revision, 20, mustTime(t, 450)); err != nil {
		t.Fatal(err)
	}
	for index := 9; index >= 1; index-- {
		id := publicAgentID(t, index)
		if _, err := store.CreateAgent(ctx, NewAgent{ID: id, ProjectID: project.ID, Name: fmt.Sprintf("agent-%d", index), Role: RoleOrchestrator, Provider: ProviderCodex, ExecutionMode: ExecutionWorkspaceWrite, ToolBudgetLimit: 1}, mustTime(t, int64(500+index))); err != nil {
			t.Fatal(err)
		}
	}
	for index := 9; index >= 1; index-- {
		id := publicTaskID(t, index)
		if _, err := store.EnqueueTask(ctx, NewTask{ID: id, ProjectID: project.ID, AssignedAgentID: publicAgentID(t, index), IncarnationID: incarnationID(t, byte(100+index)), Title: fmt.Sprintf("task-%d", index), Priority: int64(100 - index)}, mustTime(t, int64(600+index))); err != nil {
			t.Fatal(err)
		}
	}
	runIDs := []RunID{run.ID}
	seeds := []byte{17, 24, 31, 38, 45, 53, 60, 67}
	for index := 1; index <= 8; index++ {
		keys := admissionKeys(t, seeds[index-1], nil)
		admission, err := store.AdmitNext(ctx, publicAgentID(t, index), keys, mustTime(t, int64(800+index)))
		if err != nil || !admission.Admitted() || admission.Run == nil {
			t.Fatalf("admit run %d = %+v, %v", index, admission, err)
		}
		activateAllResourcesUnique(t, store, *admission.Run, int64(900+index*10), int64(5000+index*100))
		session := terminalSessionForRunTest(t, store, admission.Run.ID)
		running, err := store.ActivateRun(ctx, admission.Run.ID, session.ID, admission.Run.Revision, session.Revision, mustTime(t, int64(909+index*10)))
		if err != nil {
			t.Fatal(err)
		}
		runIDs = append(runIDs, running.ID)
	}
	insertPublicHumanRequests(t, store, runIDs)
	state, err = store.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		kind PublicStateKind
		ids  []string
	}{
		{kind: PublicStateAgent, ids: append([]string{run.AgentID.String()}, publicIDs(9, func(index int) string { return publicAgentID(t, index).String() })...)},
		{kind: PublicStateTask, ids: append([]string{run.TaskID.String()}, publicIDs(9, func(index int) string { return publicTaskID(t, index).String() })...)},
		{kind: PublicStateHumanRequest, ids: publicIDs(9, func(index int) string { return publicHumanRequestID(t, index).String() })},
	} {
		cursor := &PublicStateCursor{Head: state.Head, Kind: test.kind}
		var got []string
		for cursor != nil && cursor.Kind == test.kind {
			page, err := store.ReadPublicStatePage(ctx, cursor)
			if err != nil {
				t.Fatalf("%s page: %v", test.kind.String(), err)
			}
			for _, item := range page.Items {
				got = append(got, item.id().String())
			}
			cursor = page.NextCursor
		}
		if !reflect.DeepEqual(got, test.ids) {
			t.Fatalf("%s traversal = %v, want %v", test.kind.String(), got, test.ids)
		}
	}
}

func TestPublicStateFactoryAndEmptyKindTraversal(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	page, err := store.ReadPublicStatePage(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if page.Kind != PublicStateFactory || len(page.Items) != 1 || page.NextCursor == nil || page.NextCursor.Kind != PublicStateProject || page.NextCursor.AfterID != nil {
		t.Fatalf("factory page = %+v", page)
	}
	if page.Items[0].id().String() != "factory" || len(page.Items[0].id().Bytes()) != 0 {
		t.Fatalf("factory exposed durable sentinel: %+v", page.Items[0].id())
	}
	for _, kind := range []PublicStateKind{PublicStateProject, PublicStateAgent, PublicStateTask, PublicStateHumanRequest} {
		page, err = store.ReadPublicStatePage(ctx, page.NextCursor)
		if err != nil {
			t.Fatal(err)
		}
		if page.Kind != kind || page.Items == nil || len(page.Items) != 0 {
			t.Fatalf("empty %s page = %+v", kind.String(), page)
		}
	}
	if page.NextCursor != nil {
		t.Fatalf("final human request cursor = %+v", page.NextCursor)
	}
}

func TestPublicStateCountIncludesFactoryAndRejectsBeforeItems(t *testing.T) {
	for _, count := range []int{PublicStateEntityLimit - 1, PublicStateEntityLimit} {
		t.Run(fmt.Sprintf("dynamic_%d", count), func(t *testing.T) {
			store, _ := newTestStore(t)
			defer store.Close()
			insertPublicProjects(t, store, count)
			page, err := store.ReadPublicStatePage(context.Background(), nil)
			if count == PublicStateEntityLimit-1 {
				if err != nil || len(page.Items) != 1 {
					t.Fatalf("maximum accepted page = %+v, %v", page, err)
				}
				return
			}
			if !errors.Is(err, ErrSnapshotTooLarge) || len(page.Items) != 0 || page.NextCursor != nil {
				t.Fatalf("oversized page = %+v, %v", page, err)
			}
		})
	}
}

func TestPublicStateCountBoundIncludesEveryDynamicKind(t *testing.T) {
	store, run, _ := runningOrchestratorRun(t)
	defer store.Close()
	ctx := context.Background()
	if _, err := store.CreateHumanQuestionForAttempt(ctx, run.CredentialDigest, NewHumanQuestion{IdempotencyKey: humanKey(130), QuestionText: "private"}, mustTime(t, 400)); err != nil {
		t.Fatal(err)
	}
	// The running fixture contributes one project, agent, and task; its open
	// request is the fourth dynamic kind. Fill projects to 4,095 dynamic rows.
	insertPublicProjects(t, store, PublicStateEntityLimit-5)
	page, err := store.ReadPublicStatePage(ctx, nil)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("distributed maximum page = %+v, %v", page, err)
	}
	overflow := publicProjectID(t, PublicStateEntityLimit-4)
	if _, err := store.writer.Exec(`INSERT INTO projects(id, name, root, verification_policy, revision, created_at_ms, updated_at_ms) VALUES(?, 'overflow', '/public/overflow', 'none', 1, 401, 401)`, overflow.Bytes()); err != nil {
		t.Fatal(err)
	}
	page, err = store.ReadPublicStatePage(ctx, nil)
	if !errors.Is(err, ErrSnapshotTooLarge) || len(page.Items) != 0 {
		t.Fatalf("distributed overflow page = %+v, %v", page, err)
	}
}

func TestPublicStateCursorHeadAndIdentityFailClosed(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	insertPublicProjects(t, store, 9)
	first, err := store.ReadPublicStatePage(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	projectPage, err := store.ReadPublicStatePage(ctx, first.NextCursor)
	if err != nil || projectPage.NextCursor == nil || projectPage.NextCursor.AfterID == nil {
		t.Fatalf("project page = %+v, %v", projectPage, err)
	}
	created, err := store.CreateProject(ctx, NewProject{ID: publicProjectID(t, 10), Name: "committed", Root: "/public/committed"}, mustTime(t, 1000))
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.ReadPublicStatePage(ctx, projectPage.NextCursor)
	var restart *PublicStateRestartError
	if !errors.As(err, &restart) || restart.Head.Int64() != first.Head.Int64()+1 || restart.Floor.Int64() != 1 || len(result.Items) != 0 {
		t.Fatalf("stale continuation = %+v, restart=%+v, err=%v", result, restart, err)
	}
	future := &PublicStateCursor{Head: mustSequence(t, restart.Head.Int64()+1), Kind: PublicStateProject}
	if _, err := store.ReadPublicStatePage(ctx, future); !errors.As(err, &restart) || restart.Head.Int64() != first.Head.Int64()+1 {
		t.Fatalf("future continuation = %+v, %v", restart, err)
	}
	badDynamic := PublicStateID{}
	missing, _ := PublicStateIDFromBytes(publicRawID(0xee, 99))
	factoryID := FactoryPublicStateID()
	createdID := mustPublicStateID(t, created.ID.Bytes())
	for _, test := range []struct {
		name   string
		cursor *PublicStateCursor
	}{
		{name: "zero cursor", cursor: &PublicStateCursor{}},
		{name: "unknown kind", cursor: &PublicStateCursor{Head: restart.Head, Kind: PublicStateKind(99)}},
		{name: "factory after", cursor: &PublicStateCursor{Head: restart.Head, Kind: PublicStateFactory, AfterID: &missing}},
		{name: "zero after", cursor: &PublicStateCursor{Head: restart.Head, Kind: PublicStateProject, AfterID: &badDynamic}},
		{name: "factory as after", cursor: &PublicStateCursor{Head: restart.Head, Kind: PublicStateProject, AfterID: &factoryID}},
		{name: "missing after", cursor: &PublicStateCursor{Head: restart.Head, Kind: PublicStateProject, AfterID: &missing}},
		{name: "wrong-kind after", cursor: &PublicStateCursor{Head: restart.Head, Kind: PublicStateAgent, AfterID: &createdID}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := store.ReadPublicStatePage(ctx, test.cursor); !errors.Is(err, ErrInvalidValue) {
				t.Fatalf("cursor %+v error = %v", test.cursor, err)
			}
		})
	}
}

func TestPublicStatePinnedReadCannotMixConcurrentCommit(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	tx, err := store.beginRead(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	if err := validateDurableControls(ctx, tx.connection); err != nil {
		t.Fatal(err)
	}
	state, err := factoryState(ctx, tx.connection)
	if err != nil {
		t.Fatal(err)
	}
	if err := enforcePublicStateCount(ctx, tx.connection); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(ctx, NewProject{ID: publicProjectID(t, 1), Name: "later", Root: "/later"}, mustTime(t, 2)); err != nil {
		t.Fatal(err)
	}
	items, err := readPublicStateItems(ctx, tx.connection, PublicStateProject, nil)
	if err != nil || len(items) != 0 {
		t.Fatalf("pinned items = %+v, %v", items, err)
	}
	current, err := store.ReadPublicStatePage(ctx, &PublicStateCursor{Head: mustSequence(t, state.Head.Int64()+1), Kind: PublicStateProject})
	if err != nil || len(current.Items) != 1 {
		t.Fatalf("current page = %+v, %v", current, err)
	}
}

func TestPublicStateConcurrentWriterNeverMixesPageHeadAndCount(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	specs := make([]NewProject, PublicStatePageSize)
	times := make([]UnixMillis, PublicStatePageSize)
	for index := range specs {
		specs[index] = NewProject{ID: publicProjectID(t, index+1), Name: fmt.Sprintf("project-%d", index+1), Root: fmt.Sprintf("/concurrent/%d", index+1)}
		times[index] = mustTime(t, int64(index+2))
	}
	done := make(chan error, 1)
	go func() {
		for index := range specs {
			_, err := store.CreateProject(ctx, specs[index], times[index])
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	writerDone := false
	for !writerDone {
		factoryPage, err := store.ReadPublicStatePage(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		projectPage, err := store.ReadPublicStatePage(ctx, factoryPage.NextCursor)
		var restart *PublicStateRestartError
		if errors.As(err, &restart) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if projectPage.Head != factoryPage.Head || len(projectPage.Items) != int(projectPage.Head.Int64()) {
			t.Fatalf("mixed page: factory head=%d project head=%d items=%d", factoryPage.Head.Int64(), projectPage.Head.Int64(), len(projectPage.Items))
		}
		for index, item := range projectPage.Items {
			summary, ok := item.Project()
			if !ok || summary.ID != publicProjectID(t, index+1) {
				t.Fatalf("mixed item %d = %+v, summary=%+v", index, item, summary)
			}
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			writerDone = true
		default:
		}
	}
	state, err := store.Factory(ctx)
	if err != nil || state.Head.Int64() != PublicStatePageSize {
		t.Fatalf("final state = %+v, %v", state, err)
	}
}

func TestPublicStateEntityReadTransactionPinsHeadAndRevision(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	tx, err := store.beginRead(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	if err := validateDurableControls(ctx, tx.connection); err != nil {
		t.Fatal(err)
	}
	before, err := factoryState(ctx, tx.connection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetDispatch(ctx, before.Revision, true, mustTime(t, 2)); err != nil {
		t.Fatal(err)
	}
	item, found, err := readPublicStateEntityItem(ctx, tx.connection, PublicStateFactory, FactoryPublicStateID())
	if err != nil || !found {
		t.Fatalf("pinned factory item found=%v err=%v", found, err)
	}
	revision, ok := item.revision()
	if !ok || revision != before.Revision {
		t.Fatalf("pinned item revision = %d, want %d", revision.Int64(), before.Revision.Int64())
	}
	after, err := store.ReadPublicStateEntity(ctx, PublicStateFactory, FactoryPublicStateID())
	if err != nil || after.Head.Int64() != before.Head.Int64()+1 || after.Revision.Int64() != before.Revision.Int64()+1 {
		t.Fatalf("current factory entity = %+v, %v", after, err)
	}
}

func TestPublicStateEntityReadsAreExactSlimAndDeleted(t *testing.T) {
	store, run, _ := runningOrchestratorRun(t)
	defer store.Close()
	ctx := context.Background()
	request, err := store.CreateHumanQuestionForAttempt(ctx, run.CredentialDigest, NewHumanQuestion{IdempotencyKey: humanKey(111), QuestionText: "PRIVATE_QUESTION_SENTINEL"}, mustTime(t, 400))
	if err != nil {
		t.Fatal(err)
	}
	locators := []struct {
		kind PublicStateKind
		id   PublicStateID
	}{
		{PublicStateFactory, FactoryPublicStateID()},
		{PublicStateProject, mustPublicStateID(t, run.ProjectID.Bytes())},
		{PublicStateAgent, mustPublicStateID(t, run.AgentID.Bytes())},
		{PublicStateTask, mustPublicStateID(t, run.TaskID.Bytes())},
		{PublicStateHumanRequest, mustPublicStateID(t, request.ID.Bytes())},
	}
	for _, locator := range locators {
		entity, err := store.ReadPublicStateEntity(ctx, locator.kind, locator.id)
		if err != nil || entity.Deleted || entity.Item == nil || entity.Kind != locator.kind || entity.EntityID.String() != locator.id.String() {
			t.Fatalf("%s entity = %+v, %v", locator.kind.String(), entity, err)
		}
		itemRevision, ok := entity.Item.revision()
		if !ok || entity.Revision != itemRevision {
			t.Fatalf("%s entity revision = %d, item=%d ok=%v", locator.kind.String(), entity.Revision.Int64(), itemRevision.Int64(), ok)
		}
	}
	hr, _ := func() (HumanRequestProjection, bool) {
		entity, _ := store.ReadPublicStateEntity(ctx, PublicStateHumanRequest, mustPublicStateID(t, request.ID.Bytes()))
		return entity.Item.HumanRequest()
	}()
	encoded, err := json.Marshal(hr)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"PRIVATE_QUESTION_SENTINEL", "orchestrator", "task", "Agent needs", "WhyHumanNeeded", "QuestionText", "ProjectName", "AgentName", "TaskTitle"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("human request projection exposed %q: %s", private, encoded)
		}
	}
	if !hr.CanReply || !hr.CanOpenTerminal || hr.ReplyMaxBytes != MaxHumanRequestReplyBytes {
		t.Fatalf("human request actions = %+v", hr)
	}
	wantFields := []string{"ID", "ProjectID", "AgentID", "TaskID", "RunID", "CreatedAt", "UpdatedAt", "Revision", "Kind", "Status", "ReplyMaxBytes", "CanReply", "CanOpenTerminal"}
	projectionType := reflect.TypeOf(HumanRequestProjection{})
	gotFields := make([]string, projectionType.NumField())
	for index := range gotFields {
		gotFields[index] = projectionType.Field(index).Name
	}
	if !reflect.DeepEqual(gotFields, wantFields) {
		t.Fatalf("HumanRequestProjection fields = %v, want %v", gotFields, wantFields)
	}

	missing := mustPublicStateID(t, publicRawID(0xfd, 1))
	entity, err := store.ReadPublicStateEntity(ctx, PublicStateProject, missing)
	if !errors.Is(err, ErrNotFound) || entity != (PublicStateEntityResult{}) {
		t.Fatalf("missing entity = %+v, %v", entity, err)
	}
	if _, err := store.ReadPublicStateEntity(ctx, PublicStateFactory, PublicStateID{}); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("zero factory locator = %v", err)
	}
	if _, err := store.ReadPublicStateEntity(ctx, PublicStateProject, FactoryPublicStateID()); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("literal factory used dynamically = %v", err)
	}

	client := humanQuestionClient(t, store, 112, BrowserCapabilityObserve|BrowserCapabilityHumanActions)
	delivery, err := store.BeginHumanReply(ctx, client.ID, request.ID, request.Revision, humanDeliveryID(t, 113), "private reply", mustTime(t, 401))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AcknowledgeHumanReply(ctx, request.ID, delivery.DeliveryID, delivery.Revision, mustTime(t, 402)); err != nil {
		t.Fatal(err)
	}
	resolved, found, err := store.HumanRequest(ctx, request.ID)
	if err != nil || !found || resolved.Status != HumanRequestResolved {
		t.Fatalf("resolved request = %+v, found=%v, err=%v", resolved, found, err)
	}
	entity, err = store.ReadPublicStateEntity(ctx, PublicStateHumanRequest, mustPublicStateID(t, request.ID.Bytes()))
	if err != nil || !entity.Deleted || entity.Item != nil || entity.Revision != resolved.Revision {
		t.Fatalf("resolved entity = %+v, %v", entity, err)
	}
}

func TestPublicStateEntityStaleTombstoneHasExactRevision(t *testing.T) {
	store, run, _ := runningOrchestratorRun(t)
	defer store.Close()
	ctx := context.Background()
	request, err := store.CreateHumanQuestionForAttempt(ctx, run.CredentialDigest, NewHumanQuestion{IdempotencyKey: humanKey(119), QuestionText: "question"}, mustTime(t, 400))
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := NewBlockedProposal("waiting")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ProposeAttemptOutcome(ctx, run.CredentialDigest, proposal, mustTime(t, 401)); err != nil {
		t.Fatal(err)
	}
	stale, found, err := store.HumanRequest(ctx, request.ID)
	if err != nil || !found || stale.Status != HumanRequestStale {
		t.Fatalf("stale request = %+v, found=%v, err=%v", stale, found, err)
	}
	entity, err := store.ReadPublicStateEntity(ctx, PublicStateHumanRequest, mustPublicStateID(t, request.ID.Bytes()))
	if err != nil || !entity.Deleted || entity.Item != nil || entity.Revision != stale.Revision {
		t.Fatalf("stale entity = %+v, %v", entity, err)
	}
}

func TestPublicStateEntityTombstoneMustBeLatestAndMatchDurableRequest(t *testing.T) {
	store, run, _ := runningOrchestratorRun(t)
	defer store.Close()
	ctx := context.Background()
	client := humanQuestionClient(t, store, 120, BrowserCapabilityObserve|BrowserCapabilityHumanActions)
	request, err := store.CreateHumanQuestionForAttempt(ctx, run.CredentialDigest, NewHumanQuestion{IdempotencyKey: humanKey(121), QuestionText: "question"}, mustTime(t, 400))
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := store.BeginHumanReply(ctx, client.ID, request.ID, request.Revision, humanDeliveryID(t, 122), "reply", mustTime(t, 401))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AcknowledgeHumanReply(ctx, request.ID, delivery.DeliveryID, delivery.Revision, mustTime(t, 402)); err != nil {
		t.Fatal(err)
	}
	id := mustPublicStateID(t, request.ID.Bytes())
	resolved, found, err := store.HumanRequest(ctx, request.ID)
	if err != nil || !found || resolved.Status != HumanRequestResolved {
		t.Fatalf("resolved request = %+v, found=%v, err=%v", resolved, found, err)
	}

	corruptSQL(t, store, `UPDATE invalidations SET deleted = 0 WHERE entity_kind = 'human_request' AND entity_id = ? AND revision = ?`, request.ID.Bytes(), resolved.Revision.Int64())
	if _, err := store.ReadPublicStateEntity(ctx, PublicStateHumanRequest, id); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("nondeleted latest invalidation = %v", err)
	}
	corruptSQL(t, store, `UPDATE invalidations SET deleted = 1, revision = ? WHERE entity_kind = 'human_request' AND entity_id = ? AND revision = ?`, resolved.Revision.Int64()+1, request.ID.Bytes(), resolved.Revision.Int64())
	if _, err := store.ReadPublicStateEntity(ctx, PublicStateHumanRequest, id); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("wrong tombstone revision = %v", err)
	}
}

func TestPublicStateEntityRejectsInventedDynamicDeletion(t *testing.T) {
	for _, deleted := range []int{0, 1} {
		store, _ := newTestStore(t)
		ctx := context.Background()
		id := publicProjectID(t, 128+deleted)
		corruptSQL(t, store, `INSERT INTO invalidations(sequence, occurred_at_ms, entity_kind, entity_id, revision, deleted) VALUES(1, 2, 'project', ?, 1, ?)`, id.Bytes(), deleted)
		corruptSQL(t, store, `UPDATE factory SET next_invalidation_sequence = 2`)
		if _, err := store.ReadPublicStateEntity(ctx, PublicStateProject, mustPublicStateID(t, id.Bytes())); !errors.Is(err, ErrCorruptState) {
			store.Close()
			t.Fatalf("missing project with deleted=%d invalidation = %v", deleted, err)
		}
		store.Close()
	}
}

func TestPublicStateEntityProjectionOmitsPrivateRows(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	project, err := store.CreateProject(ctx, NewProject{ID: publicProjectID(t, 1), Name: "public project", Root: "/PRIVATE_ROOT_SENTINEL"}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := store.CreateAgent(ctx, NewAgent{ID: publicAgentID(t, 1), ProjectID: project.ID, Name: "public agent", Role: RoleWorker, Provider: ProviderCodex, ExecutionMode: ExecutionWorkspaceWrite, Model: "PRIVATE_MODEL_SENTINEL", ReasoningEffort: "xhigh", ToolBudgetLimit: 987654321}, mustTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.EnqueueTask(ctx, NewTask{ID: publicTaskID(t, 1), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 231), Title: "public task", Body: "PRIVATE_BODY_SENTINEL", Priority: 9}, mustTime(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	projectEntity, err := store.ReadPublicStateEntity(ctx, PublicStateProject, mustPublicStateID(t, project.ID.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	projectSummary, _ := projectEntity.Item.Project()
	agentEntity, err := store.ReadPublicStateEntity(ctx, PublicStateAgent, mustPublicStateID(t, agent.ID.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	agentSummary, _ := agentEntity.Item.Agent()
	taskEntity, err := store.ReadPublicStateEntity(ctx, PublicStateTask, mustPublicStateID(t, task.ID.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	taskSummary, _ := taskEntity.Item.Task()
	projectJSON, err := json.Marshal(projectSummary)
	if err != nil {
		t.Fatal(err)
	}
	agentJSON, err := json.Marshal(agentSummary)
	if err != nil {
		t.Fatal(err)
	}
	taskJSON, err := json.Marshal(taskSummary)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(projectJSON) + string(agentJSON) + string(taskJSON)
	for _, private := range []string{"PRIVATE_ROOT_SENTINEL", "PRIVATE_MODEL_SENTINEL", "PRIVATE_BODY_SENTINEL", "xhigh", "987654321", "workspace_write", "codex"} {
		if strings.Contains(encoded, private) {
			t.Fatalf("entity projections exposed %q: %s", private, encoded)
		}
	}
}

func TestPublicStateReadsRejectMalformedDurableControls(t *testing.T) {
	for _, test := range []struct {
		name    string
		corrupt string
		secret  string
	}{
		{name: "factory boolean", corrupt: `UPDATE factory SET dispatch_enabled = -1`},
		{name: "private agent control", corrupt: `UPDATE agents SET provider = 'PRIVATE_PROVIDER_SENTINEL'`, secret: "PRIVATE_PROVIDER_SENTINEL"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _ := newTestStore(t)
			defer store.Close()
			if test.secret != "" {
				seedDurableAuthority(t, store)
			}
			corruptSQL(t, store, test.corrupt)
			if _, err := store.ReadPublicStatePage(context.Background(), nil); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("page = %v", err)
			} else if test.secret != "" && strings.Contains(err.Error(), test.secret) {
				t.Fatalf("page error exposed private control: %v", err)
			}
			if _, err := store.ReadPublicStateEntity(context.Background(), PublicStateFactory, FactoryPublicStateID()); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("entity = %v", err)
			} else if test.secret != "" && strings.Contains(err.Error(), test.secret) {
				t.Fatalf("entity error exposed private control: %v", err)
			}
		})
	}
}

func insertPublicProjects(t *testing.T, store *Store, count int) {
	t.Helper()
	tx, err := store.writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	statement, err := tx.Prepare(`INSERT INTO projects(id, name, root, verification_policy, revision, created_at_ms, updated_at_ms) VALUES(?, ?, ?, 'none', 1, 1, 1)`)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	for index := count; index >= 1; index-- {
		if _, err := statement.Exec(publicProjectID(t, index).Bytes(), fmt.Sprintf("project-%d", index), fmt.Sprintf("/public/%d", index)); err != nil {
			statement.Close()
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := statement.Close(); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func insertPublicHumanRequests(t *testing.T, store *Store, runIDs []RunID) {
	t.Helper()
	tx, err := store.writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	statement, err := tx.Prepare(`INSERT INTO human_requests(id, run_id, idempotency_key, kind, reason_code, question_text, status, revision, created_at_ms, updated_at_ms) VALUES(?, ?, ?, 'question', 'provider_question', ?, 'open', 1, 1200, 1200)`)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	for index := len(runIDs); index >= 1; index-- {
		key := publicRawID(0xbc, index)
		if _, err := statement.Exec(publicHumanRequestID(t, index).Bytes(), runIDs[index-1].Bytes(), key, fmt.Sprintf("PRIVATE_HR_%d", index)); err != nil {
			statement.Close()
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := statement.Close(); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func publicRawID(prefix byte, index int) []byte {
	raw := bytes.Repeat([]byte{prefix}, IDBytes)
	binary.BigEndian.PutUint64(raw[IDBytes-8:], uint64(index))
	return raw
}

func publicProjectID(t *testing.T, index int) ProjectID {
	t.Helper()
	id, err := ProjectIDFromBytes(publicRawID(0xa1, index))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func publicAgentID(t *testing.T, index int) AgentID {
	t.Helper()
	id, err := AgentIDFromBytes(publicRawID(0xa2, index))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func publicTaskID(t *testing.T, index int) TaskID {
	t.Helper()
	id, err := TaskIDFromBytes(publicRawID(0xa3, index))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func publicHumanRequestID(t *testing.T, index int) HumanRequestID {
	t.Helper()
	id, err := HumanRequestIDFromBytes(publicRawID(0xa4, index))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustPublicStateID(t *testing.T, raw []byte) PublicStateID {
	t.Helper()
	id, err := PublicStateIDFromBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func publicIDs(count int, at func(int) string) []string {
	result := make([]string, 0, count)
	for index := 1; index <= count; index++ {
		result = append(result, at(index))
	}
	return result
}
