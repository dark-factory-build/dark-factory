//go:build darwin

package daemon

import (
	"context"
	"errors"
	"testing"

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
