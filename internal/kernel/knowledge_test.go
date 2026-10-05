package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func knowledgeSpec(t *testing.T, project ProjectID, seed byte, kind ContentKind, metadata KnowledgeMetadata) NewContent {
	t.Helper()
	s := contentSpec(t, project, seed, "")
	s.Kind = kind
	s.Author = "operator:local"
	b, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	s.SourceReferences = string(b)
	return s
}

func TestKnowledgeMetadataRequiresCanonicalKeys(t *testing.T) {
	for _, raw := range []string{
		`{"status":"tentative","Branch":"other"}`,
		`{"status":"tentative","Environment":"private"}`,
		`{"status":"tentative","Resolved":true}`,
		`{"status":"current","Status":"tentative"}`,
		`{"status":"tentative","scope":"repository","Scope":"project"}`,
		`{"status":"tentative","status":"current"}`,
	} {
		if _, err := ParseKnowledgeMetadata(raw); !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("noncanonical metadata accepted: %s: %v", raw, err)
		}
	}
}

func TestKnowledgeAuthorityScopeAndImmutableThreads(t *testing.T) {
	store, run, _ := runningWorkerRun(t)
	defer store.Close()
	ctx := context.Background()
	metadata := KnowledgeMetadata{Status: "current", Evidence: []string{"source:README.md"}}
	brief := knowledgeSpec(t, run.ProjectID, 81, ContentProjectBrief, metadata)
	if err := store.ValidateKnowledgeWriteForAttempt(ctx, run.CredentialDigest, brief, Revision{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("brief preflight: %v", err)
	}
	created, err := store.CreateContent(ctx, brief, mustTime(t, 40))
	if err != nil {
		t.Fatal(err)
	}
	brief.Title = "poisoned"
	if _, err := store.ReviseContentForAttempt(ctx, run.CredentialDigest, created.Revision, brief, mustTime(t, 41)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("brief rewrite: %v", err)
	}
	if _, err := store.DeprecateContentForAttempt(ctx, run.CredentialDigest, created.ID, created.Revision, mustTime(t, 41)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("brief deprecate: %v", err)
	}
	metadata.Status = "tentative"
	note := knowledgeSpec(t, run.ProjectID, 82, ContentObservation, metadata)
	note.SourceReferences = `{"status":"tentative","authority":"operator"}`
	if err := store.ValidateKnowledgeWriteForAttempt(ctx, run.CredentialDigest, note, Revision{}); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("forged authority: %v", err)
	}
	note.SourceReferences = `{"status":"tentative"}`
	observation, err := store.CreateContentForAttempt(ctx, run.CredentialDigest, note, mustTime(t, 42))
	if err != nil {
		t.Fatal(err)
	}
	note.Title = "revised"
	revised, err := store.ReviseContentForAttempt(ctx, run.CredentialDigest, observation.Revision, note, mustTime(t, 43))
	if err != nil {
		t.Fatal(err)
	}
	note.Title = "conflicting"
	if _, err := store.ReviseContentForAttempt(ctx, run.CredentialDigest, observation.Revision, note, mustTime(t, 44)); !errors.Is(err, ErrConflict) {
		t.Fatalf("CAS: %v", err)
	}
	other, err := store.AddProjectRepository(ctx, NewProjectRepository{ID: repositoryID(t, 88), ProjectID: run.ProjectID, Name: "other", Root: filepath.Join(t.TempDir(), "other"), BaseRef: "main"}, mustTime(t, 44))
	if err != nil {
		t.Fatal(err)
	}
	foreign := knowledgeSpec(t, run.ProjectID, 83, ContentObservation, metadata)
	foreign.RepositoryID = other.ID
	foreign.RepositoryInode = 3
	for _, refs := range []KnowledgeMetadata{{Status: "tentative", TaskID: run.TaskID.String()}, {Status: "tentative", ChangeID: run.ChangeID.String()}} {
		linked := knowledgeSpec(t, run.ProjectID, 89, ContentObservation, refs)
		linked.RepositoryID = other.ID
		if err := store.ValidateKnowledgeWrite(ctx, linked, Revision{}); !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("cross-repository reference: %v", err)
		}
	}
	foreignRevision, err := store.CreateContent(ctx, foreign, mustTime(t, 45))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ContentForAttempt(ctx, run.CredentialDigest, foreign.ID, 1); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("repository read: %v", err)
	}
	if _, err := store.DeprecateContentForAttempt(ctx, run.CredentialDigest, foreign.ID, foreignRevision.Revision, mustTime(t, 46)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("repository write: %v", err)
	}
	page, err := store.ListContentForAttempt(ctx, run.CredentialDigest, "", 0, 64)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if item.ID == foreign.ID {
			t.Fatal("cross-repository list")
		}
	}
	root := knowledgeSpec(t, run.ProjectID, 84, ContentDiscussion, metadata)
	thread, err := store.CreateContentForAttempt(ctx, run.CredentialDigest, root, mustTime(t, 47))
	if err != nil {
		t.Fatal(err)
	}
	metadata.ThreadID = thread.ID.String()
	reply := knowledgeSpec(t, run.ProjectID, 85, ContentDiscussionReply, metadata)
	response, err := store.CreateContentForAttempt(ctx, run.CredentialDigest, reply, mustTime(t, 48))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReviseContent(ctx, response.Revision, reply, mustTime(t, 49)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("immutable reply: %v", err)
	}
	metadata.ThreadID = foreign.ID.String()
	badReply := knowledgeSpec(t, run.ProjectID, 86, ContentDiscussionReply, metadata)
	if _, err := store.CreateContentForAttempt(ctx, run.CredentialDigest, badReply, mustTime(t, 50)); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("foreign thread: %v", err)
	}
	metadata.ThreadID = ""
	metadata.TaskID = strings.Repeat("f", 32)
	dangling := knowledgeSpec(t, run.ProjectID, 87, ContentObservation, metadata)
	if _, err := store.CreateContentForAttempt(ctx, run.CredentialDigest, dangling, mustTime(t, 51)); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("dangling task: %v", err)
	}
	if revised.Revision.Int64() != 2 {
		t.Fatal("revision lost")
	}
}

func TestKnowledgeFilterBeforePaginationAndAccessFreeze(t *testing.T) {
	store, run, _ := runningWorkerRun(t)
	defer store.Close()
	ctx := context.Background()
	repo := RepositoryID(run.ProjectID)
	var first ContentRevision
	for i := byte(90); i < 94; i++ {
		m := KnowledgeMetadata{Status: "tentative"}
		if i%2 == 1 {
			m.Branch = "other"
		}
		s := knowledgeSpec(t, run.ProjectID, i, ContentObservation, m)
		item, err := store.CreateContentForAttempt(ctx, run.CredentialDigest, s, mustTime(t, int64(i)))
		if err != nil {
			t.Fatal(err)
		}
		if i == 90 {
			first = item
		}
	}
	page, err := store.SearchKnowledge(ctx, run.ProjectID, repo, KnowledgeQuery{Branch: "main", Limit: 1})
	if err != nil || len(page.Items) != 1 || page.NextOffset != 1 {
		t.Fatalf("first page: %+v %v", page, err)
	}
	next, err := store.SearchKnowledge(ctx, run.ProjectID, repo, KnowledgeQuery{Branch: "main", Offset: page.NextOffset, Limit: 1})
	if err != nil || len(next.Items) != 1 || next.NextOffset != 0 || next.Items[0].ID != first.ID {
		t.Fatalf("next page: %+v %v", next, err)
	}
	for _, q := range []KnowledgeQuery{{Offset: -1}, {Limit: 65}} {
		if _, err := store.SearchKnowledge(ctx, run.ProjectID, repo, q); !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("invalid page: %v", err)
		}
	}
	refs := []ContentAccess{{ContentID: first.ID, ContentRevision: first.Revision, ByteLength: 17}}
	frozen, err := store.FreezeKnowledgeContext(ctx, run.ID, refs, mustTime(t, 100))
	if err != nil || len(frozen) != 1 {
		t.Fatalf("freeze: %+v %v", frozen, err)
	}
	if frozen, err := store.FreezeKnowledgeContext(ctx, run.ID, nil, mustTime(t, 101)); err != nil || len(frozen) != 1 {
		t.Fatalf("changed snapshot: %+v %v", frozen, err)
	}
	access := ContentAccess{ContentID: first.ID, ContentRevision: first.Revision, Kind: "read", Offset: 8, ByteLength: 9, CreatedAt: mustTime(t, 102)}
	if err := store.RecordContentAccessForAttempt(ctx, run.CredentialDigest, access); err != nil {
		t.Fatal(err)
	}
	got, err := store.ListContentRevisionAccesses(ctx, run.ProjectID, first.ID, first.Revision, 0, 64)
	if err != nil || len(got.Items) != 1 || got.Items[0].Offset != 8 || got.Items[0].ByteLength != 9 || got.Items[0].TaskID != run.TaskID {
		t.Fatalf("exact partial read: %+v %v", got, err)
	}
	access.Kind = "supplied"
	if err := store.RecordContentAccessForAttempt(ctx, run.CredentialDigest, access); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("forged supplied: %v", err)
	}
}

func TestKnowledgeEmptyContextSurvivesRestart(t *testing.T) {
	store, run, _, path := runningWorkerRunWithPath(t)
	ctx := context.Background()
	if _, err := store.FreezeKnowledgeContext(ctx, run.ID, nil, mustTime(t, 40)); err != nil {
		t.Fatal(err)
	}
	refs, found, err := store.KnowledgeContext(ctx, run.ID)
	if err != nil || !found || len(refs) != 0 {
		t.Fatalf("empty snapshot: %+v %v %v", refs, found, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if refs, found, err = store.KnowledgeContext(ctx, run.ID); err != nil || !found || len(refs) != 0 {
		t.Fatalf("empty snapshot lost after restart: %+v %v %v", refs, found, err)
	}
	store.Close()
}

func TestKnowledgeOpenThreadsFilterBeforePagination(t *testing.T) {
	store, run, _ := runningWorkerRun(t)
	defer store.Close()
	ctx := context.Background()
	for i := byte(100); i < 104; i++ {
		spec := knowledgeSpec(t, run.ProjectID, i, ContentDiscussion, KnowledgeMetadata{Status: "tentative", Resolved: i%2 == 1})
		if _, err := store.CreateContent(ctx, spec, mustTime(t, int64(i))); err != nil {
			t.Fatal(err)
		}
	}
	query := KnowledgeQuery{Kind: ContentDiscussion, OpenOnly: true, Limit: 1}
	first, err := store.SearchKnowledgeForAttempt(ctx, run.CredentialDigest, query)
	if err != nil || len(first.Items) != 1 || first.NextOffset != 1 || first.Items[0].ID != contentID(t, 102) {
		t.Fatalf("first open page: %+v %v", first, err)
	}
	query.Offset = first.NextOffset
	second, err := store.SearchKnowledgeForAttempt(ctx, run.CredentialDigest, query)
	if err != nil || len(second.Items) != 1 || second.NextOffset != 0 || second.Items[0].ID != contentID(t, 100) {
		t.Fatalf("second open page: %+v %v", second, err)
	}
}

func TestKnowledgeAttachmentScopeAndLifecycle(t *testing.T) {
	store, run, _ := runningWorkerRun(t)
	defer store.Close()
	ctx := context.Background()
	other, err := store.AddProjectRepository(ctx, NewProjectRepository{ID: repositoryID(t, 140), ProjectID: run.ProjectID, Name: "other", Root: filepath.Join(t.TempDir(), "other"), BaseRef: "main"}, mustTime(t, 40))
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 141), ProjectID: run.ProjectID, RepositoryID: other.ID, IncarnationID: incarnationID(t, 142), Title: "other repo"}, mustTime(t, 41))
	if err != nil {
		t.Fatal(err)
	}
	spec := knowledgeSpec(t, run.ProjectID, 143, ContentObservation, KnowledgeMetadata{Status: "tentative"})
	content, err := store.CreateContent(ctx, spec, mustTime(t, 42))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AttachContentToTask(ctx, task.ID, run.ProjectID, content.ID, content.Revision, mustTime(t, 43)); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-repository attachment: %v", err)
	}
	if err := store.AttachContentToTask(ctx, run.TaskID, run.ProjectID, content.ID, content.Revision, mustTime(t, 43)); !errors.Is(err, ErrConflict) {
		t.Fatalf("active task attachment: %v", err)
	}
	spec.ID = contentID(t, 144)
	spec.RepositoryID = other.ID
	spec.RepositoryInode = 3
	local, err := store.CreateContent(ctx, spec, mustTime(t, 44))
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(45); i < 47; i++ {
		if err := store.AttachContentToTask(ctx, task.ID, run.ProjectID, local.ID, local.Revision, mustTime(t, i)); err != nil {
			t.Fatalf("queued pin/replay: %v", err)
		}
	}
}

func TestKnowledgeProjectScopeAcrossRepositories(t *testing.T) {
	store, run, _ := runningWorkerRun(t)
	defer store.Close()
	ctx := context.Background()
	other, err := store.AddProjectRepository(ctx, NewProjectRepository{ID: repositoryID(t, 150), ProjectID: run.ProjectID, Name: "other", Root: filepath.Join(t.TempDir(), "other"), BaseRef: "main"}, mustTime(t, 40))
	if err != nil {
		t.Fatal(err)
	}
	briefSpec := knowledgeSpec(t, run.ProjectID, 151, ContentProjectBrief, KnowledgeMetadata{Scope: "project", Status: "current", Evidence: []string{"README.md"}})
	briefSpec.RepositoryID = other.ID
	briefSpec.RepositoryInode = 3
	brief, err := store.CreateContent(ctx, briefSpec, mustTime(t, 41))
	if err != nil {
		t.Fatal(err)
	}
	localSpec := knowledgeSpec(t, run.ProjectID, 152, ContentObservation, KnowledgeMetadata{Status: "tentative"})
	localSpec.RepositoryID = other.ID
	localSpec.RepositoryInode = 3
	local, err := store.CreateContent(ctx, localSpec, mustTime(t, 42))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ContentForAttempt(ctx, run.CredentialDigest, brief.ID, 1); err != nil {
		t.Fatalf("project brief read: %v", err)
	}
	if _, err := store.ContentForAttempt(ctx, run.CredentialDigest, local.ID, 1); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("local other-repo read: %v", err)
	}
	forged := contentSpec(t, run.ProjectID, 158, "")
	forged.RepositoryID = other.ID
	forged.RepositoryInode = 3
	forged.SourceReferences = `{"scope":"project","status":"current"}`
	forged.Author = "operator:local"
	if _, err := store.CreateContent(ctx, forged, mustTime(t, 42)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ContentForAttempt(ctx, run.CredentialDigest, forged.ID, 1); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("custom JSON scope bypass: %v", err)
	}
	page, err := store.SearchKnowledgeForAttempt(ctx, run.CredentialDigest, KnowledgeQuery{})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != brief.ID {
		t.Fatalf("project scope search: %+v %v", page, err)
	}
	if err := store.RecordContentAccessForAttempt(ctx, run.CredentialDigest, ContentAccess{ContentID: brief.ID, ContentRevision: brief.Revision, Kind: "read", ByteLength: 10, CreatedAt: mustTime(t, 43)}); err != nil {
		t.Fatalf("project scope receipt: %v", err)
	}
	task, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 153), ProjectID: run.ProjectID, IncarnationID: incarnationID(t, 154), Title: "consume project brief"}, mustTime(t, 44))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AttachContentToTask(ctx, task.ID, run.ProjectID, brief.ID, brief.Revision, mustTime(t, 45)); err != nil {
		t.Fatalf("project scope attachment: %v", err)
	}
	untrusted := knowledgeSpec(t, run.ProjectID, 155, ContentObservation, KnowledgeMetadata{Scope: "project", Status: "tentative"})
	if err := store.ValidateKnowledgeWriteForAttempt(ctx, run.CredentialDigest, untrusted, Revision{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("agent project scope: %v", err)
	}
	threadSpec := knowledgeSpec(t, run.ProjectID, 156, ContentDiscussion, KnowledgeMetadata{Scope: "project", Status: "tentative"})
	threadSpec.RepositoryID = other.ID
	threadSpec.RepositoryInode = 3
	thread, err := store.CreateContent(ctx, threadSpec, mustTime(t, 46))
	if err != nil {
		t.Fatal(err)
	}
	reply := knowledgeSpec(t, run.ProjectID, 157, ContentDiscussionReply, KnowledgeMetadata{Status: "tentative", ThreadID: thread.ID.String()})
	if _, err := store.CreateContentForAttempt(ctx, run.CredentialDigest, reply, mustTime(t, 47)); err != nil {
		t.Fatalf("reply to project thread: %v", err)
	}
}
