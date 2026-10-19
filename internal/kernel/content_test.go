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

func TestMissionBindingIgnoresNonDecisionContent(t *testing.T) {
	if err := bindMissionDecision(context.Background(), nil, ProjectID{}, ContentAcceptanceScenario, `{"record_type":"proposal","record_id":"00000000000000000000000000000000","task_id":"00000000000000000000000000000000"}`, mustTime(t, 1)); err != nil {
		t.Fatalf("non-decision content reached mission binding: %v", err)
	}
}

func TestContentRevisionReplayAndHistoryRemainCASBound(t *testing.T) {
	t.Parallel()
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

func TestAttemptContentUsesLiveProjectAndProvenance(t *testing.T) {
	t.Parallel()
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

func TestAttemptContentRejectsRevokedCredentialWithoutMutation(t *testing.T) {
	t.Parallel()
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

func TestOverseerEnqueuePinsContentAtInitialWorkRevision(t *testing.T) {
	t.Parallel()
	store, overseer, _ := runningOrchestratorRun(t)
	defer store.Close()
	ctx := context.Background()
	proposalSpec := contentSpec(t, overseer.ProjectID, 125, "accepted proposal")
	proposalSpec.Kind = ContentObservation
	proposalSpec.SourceReferences = `{"status":"tentative","record_type":"proposal"}`
	content, err := store.CreateContent(ctx, proposalSpec, mustTime(t, 40))
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.EnqueueTaskForOverseer(ctx, overseer.CredentialDigest, NewTask{ID: taskID(t, 126), ProjectID: overseer.ProjectID, IncarnationID: incarnationID(t, 127), Title: "accepted proposal", Body: "Source proposal " + content.ID.String() + " revision 1.\n\nOwner authority: accept the proposal."}, mustTime(t, 41), TaskContentReference{ContentID: content.ID, ContentRevision: content.Revision})
	if err != nil {
		t.Fatal(err)
	}
	references, err := store.TaskContentReferences(ctx, overseer.ProjectID, task.ID, task.WorkRevision, false)
	if err != nil || len(references) != 1 || references[0].TaskWorkRevision.Int64() != 1 || references[0].ContentRevision.Int64() != 1 {
		t.Fatalf("enqueue references = %+v, %v", references, err)
	}
}

func TestOverseerEnqueueProposalDispatchIsAtomicAndScopedToProposal(t *testing.T) {
	t.Parallel()
	store, overseer, _ := runningOrchestratorRun(t)
	defer store.Close()
	ctx := context.Background()
	proposal := func(seed byte) ContentRevision {
		t.Helper()
		content, err := store.CreateContent(ctx, knowledgeSpec(t, overseer.ProjectID, seed, ContentObservation, KnowledgeMetadata{Status: "tentative", RecordType: "proposal"}), mustTime(t, int64(seed)))
		if err != nil {
			t.Fatal(err)
		}
		return content
	}
	firstProposal := proposal(130)
	secondProposal := proposal(131)
	first, err := store.EnqueueTaskForOverseer(ctx, overseer.CredentialDigest, NewTask{ID: taskID(t, 133), ProjectID: overseer.ProjectID, IncarnationID: incarnationID(t, 134), Title: "first"}, mustTime(t, 140), TaskContentReference{ContentID: firstProposal.ID, ContentRevision: firstProposal.Revision})
	if err != nil {
		t.Fatal(err)
	}
	// Existing proposal acceptance semantics keep one implementation active at a time.
	_, err = store.EnqueueTaskForOverseer(ctx, overseer.CredentialDigest, NewTask{ID: taskID(t, 136), ProjectID: overseer.ProjectID, IncarnationID: incarnationID(t, 137), Title: "second"}, mustTime(t, 142), TaskContentReference{ContentID: secondProposal.ID, ContentRevision: secondProposal.Revision})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("different active proposal dispatch: %v", err)
	}
	// The refusal is keyed on the pin, whatever the title or body says, and carries its reason.
	var refusal *OutcomeRefusal
	if _, err := store.EnqueueTaskForOverseer(ctx, overseer.CredentialDigest, NewTask{ID: taskID(t, 138), ProjectID: overseer.ProjectID, IncarnationID: incarnationID(t, 139), Title: "Owner-priority correction", Body: "Owner-priority correction accepted from proposal " + firstProposal.ID.String() + "."}, mustTime(t, 143), TaskContentReference{ContentID: firstProposal.ID, ContentRevision: firstProposal.Revision}); !errors.Is(err, ErrConflict) || !errors.As(err, &refusal) {
		t.Fatalf("duplicate proposal dispatch: %v", err)
	}
	cancelled, err := store.UpdateTask(ctx, first.ID, first.Revision, TaskPatch{Cancel: true}, mustTime(t, 144))
	if err != nil || cancelled.Status != TaskCancelled {
		t.Fatalf("terminal first task = %+v err=%v", cancelled, err)
	}
	if _, err := store.EnqueueTaskForOverseer(ctx, overseer.CredentialDigest, NewTask{ID: taskID(t, 140), ProjectID: overseer.ProjectID, IncarnationID: incarnationID(t, 141), Title: "replacement"}, mustTime(t, 145), TaskContentReference{ContentID: firstProposal.ID, ContentRevision: firstProposal.Revision}); err != nil {
		t.Fatalf("replacement after terminal task: %v", err)
	}
	// A failed pin rolls back the task row as well as its would-be provenance.
	failedID := taskID(t, 142)
	missing := contentID(t, 250)
	if _, err := store.EnqueueTaskForOverseer(ctx, overseer.CredentialDigest, NewTask{ID: failedID, ProjectID: overseer.ProjectID, IncarnationID: incarnationID(t, 143), Title: "atomic"}, mustTime(t, 146), TaskContentReference{ContentID: missing, ContentRevision: mustRevision(t, 1)}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("invalid provenance pin: %v", err)
	}
	if _, found, err := store.Task(ctx, failedID); err != nil || found {
		t.Fatalf("failed enqueue left task: found=%v err=%v", found, err)
	}
}

func TestProposalDispatchStaysBlockedAfterSuccessEvenWhenPullMerges(t *testing.T) {
	t.Parallel()
	store, _, project, agent := newAdmissionStore(t, RoleWorker, 4)
	defer store.Close()
	ctx := context.Background()
	proposal := contentSpec(t, project.ID, 150, "accepted implementation")
	proposal.Kind = ContentObservation
	proposal.SourceReferences = `{"status":"tentative","record_type":"proposal"}`
	content, err := store.CreateContent(ctx, proposal, mustTime(t, 40))
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 151), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 152), Title: "implementation"}, mustTime(t, 41))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AttachContentToTask(ctx, first.ID, project.ID, content.ID, content.Revision, mustTime(t, 42)); err != nil {
		t.Fatal(err)
	}
	keys := admissionKeys(t, 153, nil)
	admission, err := store.AdmitNext(ctx, keys, mustTime(t, 43))
	if err != nil || !admission.Admitted() {
		t.Fatalf("admission = %+v, %v", admission, err)
	}
	terminal := settleWorkerRunForTest(t, store, *admission.Run, keys, 50)
	head := changeHead(t, store, terminal.TaskID)
	pr := ProductionPullRequest{Number: 77, Title: "Implementation", URL: "https://github.com/example/factory/pull/77", Head: head, Branch: "factory/implementation", Base: "main", State: "open", Review: ProductionReview{Head: head, State: "unknown"}}
	if err := store.RecordPublication(ctx, project.ID, terminal.TaskID, "example/factory", pr, mustTime(t, 80)); err != nil {
		t.Fatal(err)
	}
	second, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 154), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 155), Title: "duplicate"}, mustTime(t, 81))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AttachContentToTask(ctx, second.ID, project.ID, content.ID, content.Revision, mustTime(t, 82)); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate after success with open pull: %v", err)
	}
	pr.State, pr.MergedAt = "merged", "2026-10-18T00:00:00Z"
	if err := store.RecordProductionObservation(ctx, project.ID, ProductionObservation{Repository: "example/factory", ObservedAt: 90, PullRequests: []ProductionPullRequest{pr}}, mustTime(t, 90)); err != nil {
		t.Fatal(err)
	}
	// Merging releases the one-active-implementation rule for other proposals,
	// but the merged proposal revision keeps its one task.
	if err := store.AttachContentToTask(ctx, second.ID, project.ID, content.ID, content.Revision, mustTime(t, 91)); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate after merge: %v", err)
	}
	another := contentSpec(t, project.ID, 156, "another proposal")
	another.Kind = ContentObservation
	another.SourceReferences = proposal.SourceReferences
	other, err := store.CreateContent(ctx, another, mustTime(t, 92))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AttachContentToTask(ctx, second.ID, project.ID, other.ID, other.Revision, mustTime(t, 93)); err != nil {
		t.Fatalf("different proposal after merge: %v", err)
	}
}

func TestContentDeprecationReplayUsesExpectedRevisionAfterLaterRevision(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	store, run, _ := runningWorkerRun(t)
	defer store.Close()
	ctx := context.Background()
	target, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 130), ProjectID: run.ProjectID, IncarnationID: incarnationID(t, 131), Title: "queued attachments"}, mustTime(t, 39))
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < contentPageSize+1; index++ {
		seed := byte(60 + index)
		content, err := store.CreateContent(ctx, contentSpec(t, run.ProjectID, seed, "attachment"), mustTime(t, int64(40+index)))
		if err != nil {
			t.Fatal(err)
		}
		err = store.AttachContentToTask(ctx, target.ID, run.ProjectID, content.ID, content.Revision, mustTime(t, int64(100+index)))
		if index < contentPageSize && err != nil {
			t.Fatalf("attachment %d = %v", index, err)
		}
		if index == contentPageSize && !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("attachment bound = %v", err)
		}
	}
}

func TestContentActivityListsRecordedWritesAndReadsOnly(t *testing.T) {
	t.Parallel()
	store, run, _ := runningWorkerRun(t)
	defer store.Close()
	ctx := context.Background()
	operator, err := store.CreateContent(ctx, contentSpec(t, run.ProjectID, 46, "operator"), mustTime(t, 40))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := store.CreateContentForAttempt(ctx, run.CredentialDigest, contentSpec(t, run.ProjectID, 47, "agent"), mustTime(t, 41))
	if err != nil {
		t.Fatal(err)
	}
	read := ContentAccess{ContentID: operator.ID, ContentRevision: operator.Revision, Kind: "read", ByteLength: 4, CreatedAt: mustTime(t, 42)}
	if err := store.RecordContentAccessForAttempt(ctx, run.CredentialDigest, read); err != nil {
		t.Fatal(err)
	}
	read.Offset, read.CreatedAt = 4, mustTime(t, 43)
	if err := store.RecordContentAccessForAttempt(ctx, run.CredentialDigest, read); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListContentActivity(ctx, run.ProjectID, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, item := range page.Items {
		got = append(got, fmt.Sprintf("%s %s %d %v %d", item.Operation, item.Content.Title, item.Content.ID.Bytes()[0], item.AgentID == run.AgentID, item.At.Int64()))
	}
	// Two pages of one read are one receipt at its latest page; the agent's own write names no run here (its author does).
	if want := []string{"read procedure 46 true 43", "revision procedure 47 false 41", "revision procedure 46 false 40"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("activity = %q", got)
	}
	if !strings.Contains(page.Items[1].Content.Author, run.AgentID.String()) || agent.ID != page.Items[1].Content.ID {
		t.Fatalf("agent provenance = %+v", page.Items[1].Content)
	}
	if first, err := store.ListContentActivity(ctx, run.ProjectID, 0, 1); err != nil || len(first.Items) != 1 || first.NextOffset != 1 {
		t.Fatalf("bounded page = %+v, %v", first, err)
	}
	if foreign, err := store.ListContentActivity(ctx, projectID(t, 48), 0, 8); err != nil || len(foreign.Items) != 0 {
		t.Fatalf("foreign project = %+v, %v", foreign, err)
	}
	if _, err := store.ListContentActivity(ctx, run.ProjectID, 0, 0); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("unbounded page = %v", err)
	}
}
