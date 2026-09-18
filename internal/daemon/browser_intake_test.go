package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
	"testing"
)

func TestBrowserIntakePreservesPriorityRules(t *testing.T) {
	input := browserIntakeInput(browserprotocol.Intake{Action: "update", Configuration: &browserprotocol.IntakeConfiguration{PriorityDefault: 2, PriorityByLabel: map[string]int64{"urgent": 10}}})
	if input.Configuration.PriorityDefault != 2 || input.Configuration.PriorityByLabel["urgent"] != 10 {
		t.Fatal("browser update lost migrated priority rules")
	}
	result := browserIntakeResult(api.IntakeResult{State: "ok", Sources: []api.IntakeSource{{PriorityDefault: input.Configuration.PriorityDefault, PriorityByLabel: input.Configuration.PriorityByLabel}}})
	if len(result.Sources) != 1 || result.Sources[0].PriorityDefault != 2 || result.Sources[0].PriorityByLabel["urgent"] != 10 {
		t.Fatal("browser read lost migrated priority rules")
	}
}

func TestBrowserIntakePreviewAllowsStateAndRevocationDuringRemoteRead(t *testing.T) {
	f := newAdapterFixture(t, kernel.BrowserCapabilityObserve|kernel.BrowserCapabilityAdministration)
	connection := f.pair(t)
	defer connection.CloseNow()
	ctx := context.Background()
	project := mustProjectID(t, testID(190))
	sourceID, _ := kernel.IntakeSourceIDFromBytes(bytesOf(191))
	if _, err := f.store.CreateProject(ctx, kernel.NewProject{ID: project, Name: "intake", Root: "/intake-test"}, adapterTime(t, 100)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateIntakeSource(ctx, kernel.NewIntakeSource{ID: sourceID, ProjectID: project, TargetRepositoryID: kernel.RepositoryID(project), GitHubRepositoryID: 42, GitHubRepositoryName: "team/issues", Policy: kernel.IntakePolicyManual, PollSeconds: 60, AdmissionLimit: 1}, adapterTime(t, 101)); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	f.daemon.intakeIssues = func(context.Context, string, uint64, uint32, string, uint64) (maintainer.IssuePage, error) {
		close(started)
		<-release
		return maintainer.IssuePage{RepositoryID: 42, Issues: []maintainer.Issue{}}, nil
	}
	result := make(chan error, 1)
	go func() {
		_, err := f.backend.Intake(ctx, rawBrowserClient(f.client.ID), browserprotocol.Intake{Action: "preview", SourceID: sourceID.String(), Page: 1})
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("preview did not reach the remote read")
	}
	readCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err := f.backend.StateSnapshot(readCtx, rawBrowserClient(f.client.ID)); err != nil {
		t.Fatalf("state stalled behind remote preview: %v", err)
	}
	if _, err := f.daemon.RevokeBrowserClient(readCtx, f.client.ID, f.client.Revision); err != nil {
		t.Fatalf("revocation stalled behind remote preview: %v", err)
	}
	close(release)
	if err := <-result; !errors.Is(err, browser.ErrUnauthorized) {
		t.Fatalf("revoked preview exposed private result: %v", err)
	}
}
