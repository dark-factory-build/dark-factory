//go:build darwin || linux

package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/api"
)

func TestOperatorContentMetadataAndBodyUseExplicitReadPaths(t *testing.T) {
	fixture := newDispatchFixture(t)
	client, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	projectID := testID(230)
	done := fixture.serve(t)
	if _, err := client.CreateProject(ctx, api.CreateProjectInput{ID: projectID, Name: "content", Root: filepath.Join(t.TempDir(), "source")}); err != nil {
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
}
