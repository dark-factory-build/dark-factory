package kernel

import (
	"context"
	"errors"
	"testing"
)

func TestPeerQuestionCorruptProjectFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, source, _ := runningWorkerRun(t)
	defer store.Close()
	target, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 242), ProjectID: source.ProjectID, AssignedAgentID: source.AgentID, IncarnationID: incarnationID(t, 243), Title: "queued peer"}, mustTime(t, 31))
	if err != nil {
		t.Fatal(err)
	}
	question, err := store.CreatePeerQuestionForAttempt(ctx, source.CredentialDigest, NewPeerQuestion{TargetTaskID: target.ID, IdempotencyKey: peerKey(12), Question: "question"}, mustTime(t, 32))
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 244), Name: "other", Root: "/other"}, mustTime(t, 33))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.Exec(`UPDATE peer_questions SET project_id = ? WHERE id = ?`, other.ID.Bytes(), question.ID.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Snapshot(ctx); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("corrupt project snapshot = %v", err)
	}
	path := storePath(t, store)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if reopened != nil {
		reopened.Close()
	}
	if !errors.Is(err, ErrCorruptState) {
		t.Fatalf("corrupt project reopen = %v", err)
	}
}
