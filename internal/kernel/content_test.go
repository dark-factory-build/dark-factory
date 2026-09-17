package kernel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func contentSpec(t *testing.T, project ProjectID, seed byte, body string) NewContent {
	t.Helper()
	return NewContent{ID: contentID(t, seed), ProjectID: project, Kind: ContentKind("custom_kind"), Title: "procedure", Description: "description", Author: "operator", SourceReferences: "source", ObjectFormat: "sha1", Commit: strings.Repeat(fmt.Sprintf("%02x", seed), 20), Path: ".dark-factory/content/item.md", RepositoryDevice: 1, RepositoryInode: 2}
}

func contentID(t *testing.T, seed byte) ContentID {
	t.Helper()
	id, err := ContentIDFromBytes(repeatBytes(seed, IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func repeatBytes(value byte, count int) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return result
}

func TestContentRevisionReplayAndHistoryRemainCASBound(t *testing.T) {
	store, run, _ := runningWorkerRun(t)
	defer store.Close()
	ctx := context.Background()
	spec := contentSpec(t, run.ProjectID, 40, "first")
	first, err := store.CreateContent(ctx, spec, mustTime(t, 40))
	if err != nil {
		t.Fatal(err)
	}
	revised := spec
	revised.Title = "second"
	revised.Author = "operator-2"
	second, err := store.ReviseContent(ctx, first.Revision, revised, mustTime(t, 41))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.ReviseContent(ctx, first.Revision, revised, mustTime(t, 42))
	if err != nil || replay.Revision != second.Revision {
		t.Fatalf("revision replay = %+v, %v", replay, err)
	}

	thirdSpec := revised
	thirdSpec.Title = "third"
	if _, err := store.ReviseContent(ctx, second.Revision, thirdSpec, mustTime(t, 43)); err != nil {
		t.Fatal(err)
	}
	if replay, err := store.ReviseContent(ctx, first.Revision, revised, mustTime(t, 44)); err != nil || replay.Revision != second.Revision {
		t.Fatalf("stale replay after later revision = %v", err)
	}
	if got, err := store.Content(ctx, spec.ID, 1); err != nil || got.Title != "procedure" || got.Revision.Int64() != 1 {
		t.Fatalf("immutable first revision = %+v, %v", got, err)
	}
}

func TestContentExportRetiresLegacyBodyAndReplays(t *testing.T) {
	store, run, _ := runningWorkerRun(t)
	defer store.Close()
	ctx := context.Background()
	created, err := store.CreateContent(ctx, contentSpec(t, run.ProjectID, 39, "legacy"), mustTime(t, 39))
	if err != nil {
		t.Fatal(err)
	}
	corruptSQL(t, store, `UPDATE project_content_revisions SET body = 'legacy', object_format = NULL, commit_oid = NULL, path = NULL WHERE id = ? AND revision = 1`, created.ID.Bytes())
	if err := store.CompleteContentExport(ctx, created.ID, 1, "legacy", "sha1", strings.Repeat("a", 40), ".dark-factory/content/item.md", 1, 2); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteContentExport(ctx, created.ID, 1, "legacy", "sha1", strings.Repeat("a", 40), ".dark-factory/content/item.md", 1, 2); err != nil {
		t.Fatalf("export replay: %v", err)
	}
	got, err := store.Content(ctx, created.ID, 1)
	if err != nil || got.Commit != strings.Repeat("a", 40) {
		t.Fatalf("exported content = %+v, %v", got, err)
	}
	if _, _, err := store.LegacyContent(ctx, created.ID, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("exported revision remained a legacy body: %v", err)
	}
}

func TestAttemptContentUsesLiveProjectAndProvenance(t *testing.T) {
	store, run, _ := runningWorkerRun(t)
	defer store.Close()
	ctx := context.Background()
	spec := contentSpec(t, run.ProjectID, 42, "worker definition")
	created, err := store.CreateContentForAttempt(ctx, run.CredentialDigest, spec, mustTime(t, 40))
	if err != nil {
		t.Fatal(err)
	}
	if created.Author == spec.Author || !strings.Contains(created.Author, run.ID.String()) || !strings.Contains(created.Author, run.AgentID.String()) || !strings.Contains(created.Author, run.Role.String()) {
		t.Fatalf("content provenance = %q", created.Author)
	}
	other, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 43), Name: "other", Root: "/content-other"}, mustTime(t, 41))
	if err != nil {
		t.Fatal(err)
	}
	foreign := contentSpec(t, other.ID, 44, "private")
	foreignContent, err := store.CreateContent(ctx, foreign, mustTime(t, 42))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ContentForAttempt(ctx, run.CredentialDigest, foreignContent.ID, foreignContent.Revision.Int64()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("cross-project read = %v", err)
	}
	if _, err := store.CreateContentForAttempt(ctx, run.CredentialDigest, foreign, mustTime(t, 43)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("cross-project write = %v", err)
	}
}

func TestContentEvidenceAndAttachmentsEnforceAttemptRoleAndPinWork(t *testing.T) {
	store, worker, _ := runningWorkerRun(t)
	defer store.Close()
	ctx := context.Background()
	content, err := store.CreateContent(ctx, contentSpec(t, worker.ProjectID, 45, "evidence"), mustTime(t, 40))
	if err != nil {
		t.Fatal(err)
	}
	evidenceID, err := ContentEvidenceIDFromBytes(repeatBytes(46, IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := store.CreateContentEvidenceForAttempt(ctx, worker.CredentialDigest, NewContentEvidence{ID: evidenceID, ProjectID: worker.ProjectID, ContentID: content.ID, ContentRevision: content.Revision, TestedSource: "test", Result: "passed", Evaluator: "forged", Judgment: "observed"}, mustTime(t, 41))
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Evaluator == "forged" || !strings.Contains(evidence.Evaluator, worker.ID.String()) {
		t.Fatalf("evidence provenance = %q", evidence.Evaluator)
	}
	if err := store.AttachContentForOrchestrator(ctx, worker.CredentialDigest, worker.TaskID, content.ID, content.Revision, mustTime(t, 42)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("worker attachment = %v", err)
	}

	orchestratorStore, overseer, _ := runningOrchestratorRun(t)
	defer orchestratorStore.Close()
	if overseer.ProjectID != worker.ProjectID {
		t.Fatal("fixtures used different projects")
	}
	target, err := orchestratorStore.EnqueueTask(ctx, NewTask{ID: taskID(t, 47), ProjectID: overseer.ProjectID, AssignedAgentID: overseer.AgentID, IncarnationID: incarnationID(t, 48), Title: "selected"}, mustTime(t, 43))
	if err != nil {
		t.Fatal(err)
	}
	if err := orchestratorStore.AttachContentForOrchestrator(ctx, overseer.CredentialDigest, target.ID, content.ID, content.Revision, mustTime(t, 44)); err == nil {
		t.Fatal("cross-store attachment unexpectedly succeeded")
	}
	// The orchestrator fixture has its own store/database; create the same
	// revision there before checking the queued target pin.
	localContent, err := orchestratorStore.CreateContent(ctx, contentSpec(t, overseer.ProjectID, 49, "selected"), mustTime(t, 45))
	if err != nil {
		t.Fatal(err)
	}
	if err := orchestratorStore.AttachContentForOrchestrator(ctx, overseer.CredentialDigest, target.ID, localContent.ID, localContent.Revision, mustTime(t, 46)); err != nil {
		t.Fatal(err)
	}
	references, err := orchestratorStore.TaskContentReferences(ctx, overseer.ProjectID, target.ID, target.WorkRevision)
	if err != nil || len(references) != 1 || references[0].TaskWorkRevision != target.WorkRevision || references[0].ContentRevision != localContent.Revision {
		t.Fatalf("attachment references = %+v, %v", references, err)
	}
}

func TestAttemptContentRejectsRevokedCredentialWithoutMutation(t *testing.T) {
	store, run, keys := runningWorkerRun(t)
	defer store.Close()
	ctx := context.Background()
	failure, err := NewFailureProposal(FailureInternal, "content credential test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FailRun(ctx, run.ID, run.Revision, failure, mustTime(t, 40)); err != nil {
		t.Fatal(err)
	}
	spec := contentSpec(t, run.ProjectID, 50, "must not write")
	if _, err := store.CreateContentForAttempt(ctx, keys.AttemptDigest, spec, mustTime(t, 41)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked content write = %v", err)
	}
	if _, err := store.Content(ctx, spec.ID, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked write leaked content = %v", err)
	}
}

func TestContentDeprecationReplayUsesExpectedRevisionAfterLaterRevision(t *testing.T) {
	store, run, _ := runningWorkerRun(t)
	defer store.Close()
	ctx := context.Background()
	spec := contentSpec(t, run.ProjectID, 51, "deprecate")
	created, err := store.CreateContentForAttempt(ctx, run.CredentialDigest, spec, mustTime(t, 40))
	if err != nil {
		t.Fatal(err)
	}
	deprecated, err := store.DeprecateContentForAttempt(ctx, run.CredentialDigest, created.ID, created.Revision, mustTime(t, 41))
	if err != nil {
		t.Fatal(err)
	}
	revised := spec
	revised.Author = deprecated.Author
	revised.Title = "later"
	if _, err := store.ReviseContentForAttempt(ctx, run.CredentialDigest, deprecated.Revision, revised, mustTime(t, 42)); err != nil {
		t.Fatal(err)
	}
	replay, err := store.DeprecateContentForAttempt(ctx, run.CredentialDigest, created.ID, created.Revision, mustTime(t, 43))
	if err != nil || replay.Revision != deprecated.Revision || !replay.Deprecated {
		t.Fatalf("deprecation replay = %+v, %v", replay, err)
	}
}

func TestTaskContentAttachmentBoundIsEnforced(t *testing.T) {
	store, run, _ := runningWorkerRun(t)
	defer store.Close()
	ctx := context.Background()
	for index := 0; index < contentPageSize+1; index++ {
		seed := byte(60 + index)
		content, err := store.CreateContent(ctx, contentSpec(t, run.ProjectID, seed, "attachment"), mustTime(t, int64(40+index)))
		if err != nil {
			t.Fatal(err)
		}
		err = store.AttachContentToTask(ctx, run.TaskID, run.ProjectID, content.ID, content.Revision, mustTime(t, int64(100+index)))
		if index < contentPageSize && err != nil {
			t.Fatalf("attachment %d = %v", index, err)
		}
		if index == contentPageSize && !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("attachment bound = %v", err)
		}
	}
}

func TestOrchestratorTaskContentAttachmentBoundIsEnforced(t *testing.T) {
	store, overseer, _ := runningOrchestratorRun(t)
	defer store.Close()
	ctx := context.Background()
	target, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 125), ProjectID: overseer.ProjectID, AssignedAgentID: overseer.AgentID, IncarnationID: incarnationID(t, 126), Title: "bounded selected task"}, mustTime(t, 200))
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < contentPageSize+1; index++ {
		content, err := store.CreateContent(ctx, contentSpec(t, overseer.ProjectID, byte(130+index), "orchestrator attachment"), mustTime(t, int64(210+index)))
		if err != nil {
			t.Fatal(err)
		}
		err = store.AttachContentForOrchestrator(ctx, overseer.CredentialDigest, target.ID, content.ID, content.Revision, mustTime(t, int64(300+index)))
		if index < contentPageSize && err != nil {
			t.Fatalf("orchestrator attachment %d = %v", index, err)
		}
		if index == contentPageSize && !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("orchestrator attachment bound = %v", err)
		}
	}
}
