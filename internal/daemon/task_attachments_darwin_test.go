//go:build darwin

package daemon

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestOperatorAttachmentCleanupAndCompaction(t *testing.T) {
	fixture := newDispatchFixture(t)
	ctx := context.Background()
	operator, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	project, err := fixture.store.CreateProject(ctx, kernel.NewProject{ID: supervisorProjectID(t, 1), Name: "attachments", Root: "/attachments"}, mustKernelTime(t, 101))
	if err != nil {
		t.Fatal(err)
	}
	task, err := fixture.store.EnqueueTask(ctx, kernel.NewTask{ID: supervisorTaskID(t, 3), ProjectID: project.ID, IncarnationID: supervisorIncarnationID(t, 4), Title: "task"}, mustKernelTime(t, 102))
	if err != nil {
		t.Fatal(err)
	}
	execSupervisorSQL(t, fixture.databasePath, `INSERT INTO task_attachments(task_id,position,name,data) VALUES (?,0,'notes.txt',?)`, task.ID.Bytes(), []byte("private"))
	done := fixture.serve(t)
	_, err = operator.UpdateTask(ctx, api.OverseerTaskUpdateInput{TaskID: task.ID.String(), ExpectedRevision: uint64(task.Revision.Int64()), RemoveAttachments: true})
	waitDispatch(t, done)
	var remote *api.RemoteError
	if !errors.As(err, &remote) || remote.Code() != api.RemoteConflict {
		t.Fatalf("queued cleanup: %v", err)
	}
	cancelled, err := fixture.store.UpdateTask(ctx, task.ID, task.Revision, kernel.TaskPatch{Cancel: true}, mustKernelTime(t, 103))
	if err != nil {
		t.Fatal(err)
	}
	done = fixture.serve(t)
	receipt, err := operator.UpdateTask(ctx, api.OverseerTaskUpdateInput{TaskID: task.ID.String(), ExpectedRevision: uint64(cancelled.Revision.Int64()), RemoveAttachments: true})
	waitDispatch(t, done)
	if err != nil || receipt.Revision != uint64(cancelled.Revision.Int64()) {
		t.Fatalf("cleanup receipt: %+v %v", receipt, err)
	}
	done = fixture.serve(t)
	detail, err := operator.ReadTask(ctx, api.TaskReadInput{TaskID: task.ID.String(), ExpectedRevision: receipt.Revision})
	waitDispatch(t, done)
	if err != nil || len(detail.Attachments) != 1 || !detail.Attachments[0].Removed || detail.Attachments[0].Name != "notes.txt" || detail.Attachments[0].Data != nil {
		t.Fatalf("cleanup read: %+v %v", detail, err)
	}
	done = fixture.serve(t)
	_, err = operator.CompactStorage(ctx)
	waitDispatch(t, done)
	if err != nil {
		t.Fatalf("compact API: %v", err)
	}
}

func TestSchedulerExpiresAttachmentsWhileDispatchOff(t *testing.T) {
	fixture := newDispatchFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	project, err := fixture.store.CreateProject(ctx, kernel.NewProject{ID: supervisorProjectID(t, 1), Name: "retention", Root: "/retention"}, mustKernelTime(t, 101))
	if err != nil {
		t.Fatal(err)
	}
	ids := []kernel.TaskID{supervisorTaskID(t, 3), supervisorTaskID(t, 5)}
	for i, id := range ids {
		task, err := fixture.store.EnqueueTask(ctx, kernel.NewTask{ID: id, ProjectID: project.ID, IncarnationID: supervisorIncarnationID(t, byte(4+i*2)), Title: "done"}, mustKernelTime(t, 102))
		if err != nil {
			t.Fatal(err)
		}
		execSupervisorSQL(t, fixture.databasePath, `INSERT INTO task_attachments(task_id,position,name,data) VALUES (?,0,'notes.txt',?)`, id.Bytes(), []byte("private"))
		if _, err := fixture.store.UpdateTask(ctx, id, task.Revision, kernel.TaskPatch{Cancel: true}, mustKernelTime(t, int64(103+i))); err != nil {
			t.Fatal(err)
		}
	}
	enabled := true
	if _, err := fixture.store.AttachmentRetention(ctx, &enabled); err != nil {
		t.Fatal(err)
	}
	factory, err := fixture.store.Factory(ctx)
	if err != nil || factory.DispatchEnabled {
		t.Fatalf("fixture must have dispatch off: %+v %v", factory, err)
	}
	var clock atomic.Int64
	const due = int64(30*24*60*60*1000 + 103)
	clock.Store(due)
	fixture.daemon.now = func() time.Time { return time.UnixMilli(clock.Load()) }
	polls := make(chan time.Time)
	done := make(chan error, 1)
	go func() { done <- fixture.daemon.RunScheduler(ctx, SupervisorSpec{schedulerPoll: polls}) }()
	defer func() {
		cancel()
		if err := waitSchedulerDone(t, done); err != nil {
			t.Error(err)
		}
	}()
	tick := func() {
		t.Helper()
		select {
		case polls <- time.Now():
		case <-time.After(2 * time.Second):
			t.Fatal("scheduler tick blocked")
		}
	}
	removed := func(id kernel.TaskID) bool {
		t.Helper()
		files, err := fixture.store.TaskAttachments(ctx, id)
		if err != nil || len(files) != 1 {
			t.Fatalf("files: %+v %v", files, err)
		}
		return files[0].Removed
	}
	waitRemoved := func(id kernel.TaskID) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for !removed(id) {
			if time.Now().After(deadline) {
				t.Fatal("automatic cleanup did not run")
			}
			time.Sleep(time.Millisecond)
		}
	}
	tick()
	waitRemoved(ids[0])
	clock.Store(due + 1)
	tick()
	tick() // The second send waits until the preceding tick has completed.
	if removed(ids[1]) {
		t.Fatal("cleanup ran more than hourly")
	}
	clock.Store(due + int64(time.Hour/time.Millisecond))
	tick()
	waitRemoved(ids[1])
}
