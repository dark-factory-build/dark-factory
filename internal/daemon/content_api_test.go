//go:build darwin || linux

package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestOperatorContentMetadataAndBodyUseExplicitReadPaths(t *testing.T) {
	fixture := newDispatchFixture(t)
	client, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	projectID := testID(230)
	root, err := os.MkdirTemp("/private/tmp", "dark-factory-content-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	for _, args := range [][]string{{"init", root}, {"-C", root, "config", "user.name", "Dark Factory Test"}, {"-C", root, "config", "user.email", "test@invalid"}} {
		if output, err := exec.Command(change.TrustedGitExecutable, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("content source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-C", root, "add", "README.md"}, {"-C", root, "commit", "-m", "initial"}} {
		if output, err := exec.Command(change.TrustedGitExecutable, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	done := fixture.serve(t)
	if _, err := client.CreateProject(ctx, api.CreateProjectInput{ID: projectID, Name: "content", Root: root}); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	contentID := testID(231)
	done = fixture.serve(t)
	longBody := strings.Repeat("body text ", 600)
	created, err := client.ContentCreate(ctx, api.ContentInput{ID: contentID, ProjectID: projectID, Kind: "custom", Title: "procedure", Body: longBody, SourceReferences: "source"})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	if created.Body != "" || created.Author != "operator:local" {
		t.Fatalf("metadata create leaked body or caller provenance: %+v", created)
	}
	if created.Commit == "" || created.Path == "" || created.ObjectFormat == "" {
		t.Fatalf("create omitted Git provenance: %+v", created)
	}
	done = fixture.serve(t)
	read, err := client.ContentRead(ctx, api.ContentReadInput{ID: contentID, Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	if read.Body != "" || read.Revision != 1 {
		t.Fatalf("metadata read = %+v", read)
	}
	done = fixture.serve(t)
	body, err := client.ContentBody(ctx, api.ContentBodyInput{ID: contentID, Revision: 1, Limit: 64})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	if body.Body != longBody[:64] || body.Complete {
		t.Fatalf("explicit body read = %+v", body)
	}

	done = fixture.serve(t)
	revised, err := client.ContentRevise(ctx, api.ContentInput{ID: contentID, ProjectID: projectID, Kind: "custom", Title: "revised", Body: longBody + "v2", ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	if revised.Revision != 2 || revised.Body != "" || revised.Author != "operator:local" {
		t.Fatalf("metadata revise = %+v", revised)
	}
	done = fixture.serve(t)
	replayed, err := client.ContentRevise(ctx, api.ContentInput{ID: contentID, ProjectID: projectID, Kind: "custom", Title: "revised", Body: longBody + "v2", ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	if replayed.Revision != 2 {
		t.Fatalf("revision replay = %+v", replayed)
	}
	done = fixture.serve(t)
	deprecated, err := client.ContentDeprecate(ctx, api.ContentInput{ID: contentID, ProjectID: projectID, ExpectedRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	if deprecated.Revision != 3 || !deprecated.Deprecated || deprecated.Body != "" {
		t.Fatalf("deprecation = %+v", deprecated)
	}
	done = fixture.serve(t)
	history, err := client.ContentRead(ctx, api.ContentReadInput{ID: contentID, Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	if history.Revision != 1 || history.Deprecated {
		t.Fatalf("historical revision = %+v", history)
	}

	evidenceID := testID(232)
	done = fixture.serve(t)
	evidence, err := client.ContentEvidence(ctx, api.ContentEvidenceInput{ID: evidenceID, ProjectID: projectID, ContentID: contentID, ContentRevision: 2, TestedSource: "go test ./...", Result: "passed", Judgment: "verified"})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	if evidence.Evaluator != "operator:local" {
		t.Fatalf("evidence provenance = %+v", evidence)
	}
	done = fixture.serve(t)
	evidencePage, err := client.ContentEvidenceList(ctx, api.ContentEvidenceListInput{ProjectID: projectID, ContentID: contentID, ContentRevision: 2, Limit: api.MaxContentPageItems})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	if len(evidencePage.Items) != 1 || evidencePage.Items[0].ID != evidenceID {
		t.Fatalf("evidence page = %+v", evidencePage)
	}
	for i := 240; i < 240+api.MaxContentPageItems+1; i++ {
		done = fixture.serve(t)
		if _, err := client.ContentCreate(ctx, api.ContentInput{ID: testID(byte(i)), ProjectID: projectID, Kind: "custom", Title: "page item"}); err != nil {
			t.Fatal(err)
		}
		waitDispatch(t, done)
	}
	done = fixture.serve(t)
	firstPage, err := client.ContentList(ctx, api.ContentListInput{ProjectID: projectID, Limit: api.MaxContentPageItems})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	if len(firstPage.Items) != api.MaxContentPageItems || firstPage.NextOffset != uint64(api.MaxContentPageItems) {
		t.Fatalf("first content page = %+v", firstPage)
	}
	done = fixture.serve(t)
	secondPage, err := client.ContentList(ctx, api.ContentListInput{ProjectID: projectID, Offset: firstPage.NextOffset, Limit: api.MaxContentPageItems})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	if len(secondPage.Items) == 0 || secondPage.NextOffset != 0 {
		t.Fatalf("second content page = %+v", secondPage)
	}
	moved := root + "-original"
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("/bin/cp", "-R", moved+"/.", root).CombinedOutput(); err != nil {
		t.Fatalf("copy replacement repository: %v: %s", err, output)
	}
	done = fixture.serve(t)
	if _, err := client.ContentBody(ctx, api.ContentBodyInput{ID: contentID, Revision: 1, Limit: 64}); err == nil {
		t.Fatal("replacement repository served an existing content pin")
	}
	waitDispatch(t, done)
}

func TestPageContentBodyContinuesFromGitSizedOffsets(t *testing.T) {
	body := strings.Repeat("a", 64*1024) + "£tail"
	id, err := contentID(strings.Repeat("01", 16))
	if err != nil {
		t.Fatal(err)
	}
	rev, err := revision(1)
	if err != nil {
		t.Fatal(err)
	}
	content := kernel.ContentRevision{ID: id, Revision: rev}
	first, err := pageContentBody(content, body, 0, 64*1024)
	if err != nil || first.Complete || first.NextOffset != 64*1024 {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	second, err := pageContentBody(content, body, first.NextOffset, 64*1024)
	if err != nil || !second.Complete || second.Body != "£tail" {
		t.Fatalf("second page = %+v, %v", second, err)
	}
}
