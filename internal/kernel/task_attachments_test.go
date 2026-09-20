package kernel

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestTaskAttachmentsAtomicReplayAndReopen(t *testing.T) {
	store, path, _, agent := newAdmissionStore(t, RoleOrchestrator, 2)
	ctx := context.Background()
	client := terminalTargetClient(t, store, browserTestID(t, 180), BrowserCapabilityObserve|BrowserCapabilityHumanActions)
	id := taskID(t, 181)
	files := []TaskAttachment{{Name: "../Screenshot.png", Data: []byte{0, 1, 255}}}
	enqueue := func(files []TaskAttachment) (BrowserTaskEnqueue, error) {
		return store.EnqueueTaskForBrowserAgentRepositoryMode(ctx, client.ID, id, incarnationID(t, 182), agent.ID, agent.Revision, RepositoryID{}, "Inspect this", BrowserEnqueueQueue, mustTime(t, 102), files...)
	}
	result, err := enqueue(files)
	if err != nil {
		t.Fatal(err)
	}
	if result.Task.Body != "Inspect this" {
		t.Fatal(result.Task.Body)
	}
	if _, err := enqueue(files); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	changed := []TaskAttachment{{Name: files[0].Name, Data: []byte("changed")}}
	if _, err := enqueue(changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed bytes replay: %v", err)
	}
	editedBody := "Updated instruction without attachment paths"
	if _, err := store.UpdateTask(ctx, id, result.Task.Revision, TaskPatch{Body: &editedBody}, mustTime(t, 103)); err != nil {
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
	got, err := store.TaskAttachments(ctx, id)
	if err != nil || !sameTaskAttachments(got, files) {
		t.Fatalf("reopened attachments: %+v, %v", got, err)
	}
	badID := taskID(t, 183)
	_, err = store.EnqueueTaskForBrowserAgentRepositoryMode(ctx, client.ID, badID, incarnationID(t, 184), agent.ID, agent.Revision, RepositoryID{}, "bad", BrowserEnqueueQueue, mustTime(t, 103), TaskAttachment{Name: "empty.txt"})
	if !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("empty file accepted: %v", err)
	}
	if _, found, err := store.Task(ctx, badID); err != nil || found {
		t.Fatalf("partial task committed: %v %v", found, err)
	}
}

func TestV27MigrationPreservesTaskAndAddsEmptyAttachments(t *testing.T) {
	ctx := context.Background()
	store, path, _, _ := newAdmissionStore(t, RoleWorker, 2)
	connection, err := store.writer.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotSchemaRows(t, ctx, connection, v27SchemaStatements(), false)
	connection.Close()
	if _, err := store.writer.ExecContext(ctx, `DROP TABLE task_attachments; PRAGMA user_version = 27`); err != nil {
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
	connection, err = store.writer.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	after := snapshotSchemaRows(t, ctx, connection, v27SchemaStatements(), false)
	connection.Close()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("migration changed existing data")
	}
	var count int
	if err := store.writer.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_attachments`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("new attachments: %d %v", count, err)
	}
}

func TestTaskAttachmentBoundsAndSafeNames(t *testing.T) {
	for _, item := range []TaskAttachment{{Name: "a\n.png", Data: []byte("x")}, {Name: "x", Data: make([]byte, MaxTaskAttachmentBytes+1)}} {
		if _, err := TaskAttachmentInstruction("text", []TaskAttachment{item}); !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("bad attachment accepted: %v", err)
		}
	}
	if got := AttachmentFileName(0, "../../a.$(touch pwn)"); got != "attachment-1" {
		t.Fatal(got)
	}
}

func TestAttachmentCleanupAndCompactionPreserveHistory(t *testing.T) {
	ctx := context.Background()
	store, path, _, agent := newAdmissionStore(t, RoleWorker, 1)
	client := terminalTargetClient(t, store, browserTestID(t, 180), BrowserCapabilityHumanActions|BrowserCapabilityObserve)
	id := taskID(t, 181)
	created, err := store.EnqueueTaskForBrowserAgentRepositoryMode(ctx, client.ID, id, incarnationID(t, 182), agent.ID, agent.Revision, RepositoryID{}, "Inspect file", BrowserEnqueueQueue, mustTime(t, 102), TaskAttachment{Name: "large.png", Data: make([]byte, 2<<20)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RemoveTaskAttachments(ctx, id, created.Task.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("queued cleanup: %v", err)
	}
	if _, err := store.CompactStorage(ctx); !errors.Is(err, ErrConflict) {
		t.Fatalf("dispatch-on compaction: %v", err)
	}
	keys := admissionKeys(t, 40, nil)
	admission, err := store.AdmitNext(ctx, keys, mustTime(t, 103))
	if err != nil || !admission.Admitted() {
		t.Fatalf("admit: %+v %v", admission, err)
	}
	settleWorkerRunForTest(t, store, *admission.Run, keys, 110)
	settled, _, err := store.Task(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RemoveTaskAttachments(ctx, id, created.Task.Revision); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale cleanup: %v", err)
	}
	if _, err := store.writer.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		got, err := store.RemoveTaskAttachments(ctx, id, settled.Revision)
		if err != nil || !reflect.DeepEqual(got, settled) {
			t.Fatalf("cleanup changed history: %+v %v", got, err)
		}
	}
	files, err := store.TaskAttachments(ctx, id)
	if err != nil || len(files) != 1 || files[0].Name != "large.png" || !files[0].Removed || files[0].Data != nil {
		t.Fatalf("removed metadata: %+v %v", files, err)
	}
	if _, err := TaskAttachmentInstruction("retry", files); !errors.Is(err, ErrConflict) {
		t.Fatalf("removed file delivered: %v", err)
	}
	if _, err := store.SendBackTask(ctx, id, settled.Revision, "again", mustTime(t, 140)); !errors.Is(err, ErrConflict) {
		t.Fatalf("cleaned task sent back: %v", err)
	}
	state, err := store.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state, err = store.SetDispatch(ctx, state.Revision, false, mustTime(t, 141))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = openOperationalTestStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	gotState, err := store.CompactStorage(ctx)
	if err != nil || !reflect.DeepEqual(state, gotState) {
		t.Fatalf("compact: %+v %v", gotState, err)
	}
	after, err := os.Stat(path)
	if err != nil || after.Size() >= before.Size() || !os.SameFile(before, after) {
		t.Fatalf("compaction did not shrink same file: before=%d after=%v err=%v", before.Size(), after, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = openOperationalTestStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	reopened, found, err := store.Task(ctx, id)
	if err != nil || !found || !reflect.DeepEqual(reopened, settled) {
		t.Fatalf("reopened history: %+v %v", reopened, err)
	}
	files, err = store.TaskAttachments(ctx, id)
	if err != nil || len(files) != 1 || !files[0].Removed {
		t.Fatalf("reopened removal: %+v %v", files, err)
	}
}
