//go:build darwin || linux

package daemon

import (
	"context"
	"reflect"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestOperatorOutcomeWriteReadListRoundTrip(t *testing.T) {
	fixture := newDispatchFixture(t)
	client, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	projectID := testID(250)
	done := fixture.serve(t)
	if _, err := client.CreateProject(ctx, api.CreateProjectInput{ID: projectID, Name: "outcomes", Root: contentRepositoryFixture(t)}); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	outcomeID := testID(251)
	document := kernel.OutcomeDocument{Kind: "outcome", State: "open", Objective: "ship", Criteria: "tests pass", SourceIssue: "https://github.com/dark-factory-build/dark-factory/issues/746"}
	done = fixture.serve(t)
	written, err := client.OutcomeWrite(ctx, api.OutcomeWriteInput{ID: outcomeID, ProjectID: projectID, Document: document})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	if written.Revision != 1 || written.Author != "operator:local" || !reflect.DeepEqual(written.Document, document) {
		t.Fatalf("written outcome = %+v", written)
	}
	done = fixture.serve(t)
	read, err := client.OutcomeRead(ctx, api.OutcomeReadInput{ID: outcomeID, ProjectID: projectID})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	if read.Revision != 1 || !reflect.DeepEqual(read.Document, document) {
		t.Fatalf("read outcome = %+v", read)
	}
	done = fixture.serve(t)
	list, err := client.OutcomeList(ctx, api.OutcomeListInput{ProjectID: projectID})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	if len(list.Items) != 1 || list.Items[0].ID != outcomeID || list.Items[0].Objective != document.Objective || list.Items[0].State != "open" || list.Items[0].Document.Objective != "" {
		t.Fatalf("outcome list = %+v", list)
	}
}
