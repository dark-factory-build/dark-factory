//go:build darwin || linux

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
)

func TestMaintainerRefusesWorkerAndInactiveOrchestrator(t *testing.T) {
	for _, role := range []string{"worker", "orchestrator"} {
		t.Run(role, func(t *testing.T) {
			fixture := newDispatchFixture(t)
			active := prepareActiveAttemptInProject(t, fixture, 151, testID(151), role)
			done := fixture.serve(t)
			result, err := active.client.Maintainer(context.Background(), api.MaintainerInput{Request: json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)})
			waitDispatch(t, done)
			if err != nil || result.State != "denied" || len(result.Response) != 0 {
				t.Fatalf("%s response: %+v %v", role, result, err)
			}
		})
	}
}

func TestMaintainerProjectScopeRequiresPinnedTargetAndNeverCrossesProject(t *testing.T) {
	fixture := newDispatchFixture(t)
	ctx := context.Background()
	for index := byte(160); index < 162; index++ {
		id := mustProjectID(t, testID(index))
		proof := kernel.RepositorySourceIdentity{RootDevice: 1, RootInode: uint64(index), GitDevice: 1, GitInode: uint64(index + 1), OriginDigest: [32]byte{index}, PublicationRepository: "team/repo"}
		if index == 161 {
			proof.PublicationRepository = "foreign/private"
		}
		if _, err := fixture.store.CreateProject(ctx, kernel.NewProject{ID: id, Name: "scope", Root: "/scope/" + id.String(), SourceIdentity: &proof}, mustKernelTime(t, 100)); err != nil {
			t.Fatal(err)
		}
	}
	project := mustProjectID(t, testID(160))
	foreign := mustProjectID(t, testID(161))
	if err := fixture.store.BindRepositoryGitHubID(ctx, kernel.RepositoryID(foreign), 9); err != nil {
		t.Fatal(err)
	}
	targets, sources, unbound, err := fixture.daemon.projectMaintainerRepositories(ctx, project)
	if err != nil || len(targets) != 0 || len(sources) != 0 || !unbound["team/repo"] || unbound["foreign/private"] {
		t.Fatalf("unbound scope: %v %v %v %v", targets, sources, unbound, err)
	}
	sourceID, err := kernel.IntakeSourceIDFromBytes(bytes.Repeat([]byte{163}, kernel.IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	source, err := fixture.store.CreateIntakeSource(ctx, kernel.NewIntakeSource{ID: sourceID, ProjectID: project, TargetRepositoryID: kernel.RepositoryID(project), GitHubRepositoryID: 11, GitHubRepositoryName: "feed/issues", Policy: kernel.IntakePolicyManual, PollSeconds: 60, AdmissionLimit: 25}, mustKernelTime(t, 101))
	if err != nil {
		t.Fatal(err)
	}
	_, sources, _, err = fixture.daemon.projectMaintainerRepositories(ctx, project)
	if err != nil || len(sources) != 0 {
		t.Fatal("disabled source exposed")
	}
	if _, err := fixture.store.SetIntakeSourceEnabled(ctx, source.ID, source.Revision, true, mustKernelTime(t, 102)); err != nil {
		t.Fatal(err)
	}
	_, sources, _, err = fixture.daemon.projectMaintainerRepositories(ctx, project)
	if err != nil || len(sources) != 1 || sources["feed/issues"] != 11 {
		t.Fatalf("enabled source: %v %v", sources, err)
	}
	if err := fixture.store.BindRepositoryGitHubID(ctx, kernel.RepositoryID(project), 7); err != nil {
		t.Fatal(err)
	}
	targets, _, unbound, err = fixture.daemon.projectMaintainerRepositories(ctx, project)
	if err != nil || len(targets) != 1 || targets["team/repo"] != 7 || len(unbound) != 0 {
		t.Fatalf("pinned scope: %v %v %v", targets, unbound, err)
	}
}

func TestConnectRefusesLegacyOverseerBeforeCredentialActivation(t *testing.T) {
	fixture := newDispatchFixture(t)
	prepareActiveAttemptInProject(t, fixture, 170, testID(170), "orchestrator")
	// This durable attempt is deliberately absent from the live registry.
	fixture.daemon.github = &maintainer.Host{}
	result := fixture.daemon.GitHubConnection(context.Background(), api.GitHubConnectionInput{Action: "connect"})
	if result.State != "legacy_overseers_running" || fixture.daemon.github.CustomerMode() {
		t.Fatalf("legacy transition = %+v", result)
	}
}

func TestAcceptedIssueObservationDoesNotReplaceReviewedInstructions(t *testing.T) {
	accepted := kernel.IntakeAcceptance{Snapshot: kernel.IntakeIssueSnapshot{IssueNumber: 9, Title: "reviewed", Body: "safe snapshot"}}
	for _, tc := range []struct {
		response string
		want     bool
	}{
		{`{"result":{"structuredContent":{"number":9,"title":"reviewed","body":"safe snapshot"}}}`, true},
		{`{"result":{"structuredContent":{"number":9,"title":"reviewed","body":"edited instructions"},"content":[{"type":"text","text":"edited instructions"}]}}`, false},
		{`{"result":{"structuredContent":{"number":8,"title":"reviewed","body":"safe snapshot"}}}`, false},
		{`{"result":{"isError":true}}`, false},
	} {
		if got := observedAcceptedContent([]byte(tc.response), accepted); got != tc.want {
			t.Fatalf("observation allowed=%v want=%v", got, tc.want)
		}
	}
}
