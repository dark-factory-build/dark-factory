//go:build darwin || linux

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	source, err = fixture.store.SetIntakeSourceEnabled(ctx, source.ID, source.Revision, false, mustKernelTime(t, 101))
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

func TestAcceptedIssueObservationReturnsOnlyFrozenSnapshot(t *testing.T) {
	accepted := kernel.IntakeAcceptance{SourceRepository: "feed/original", Snapshot: kernel.IntakeIssueSnapshot{IssueNumber: 9, Title: "reviewed", Body: "safe snapshot"}}
	// A broker response can carry revised instructions in content even when
	// structuredContent still happens to resemble the accepted issue.
	upstream := []byte(`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"number":9,"url":"https://github.com/feed/original/issues/9","title":"revised instructions","body":"revised untrusted instructions","labels":["regression"],"updated_at":"2026-09-18T20:00:00Z","state":"closed","state_reason":"completed"},"content":[{"type":"text","text":"revised untrusted instructions"}],"isError":false}}`)
	if !bytes.Contains(upstream, []byte("revised untrusted instructions")) {
		t.Fatal("divergent broker fixture lost its untrusted content")
	}
	response, err := frozenAcceptedIssueResponse(maintainerRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`)}, accepted, upstream)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(response, []byte("revised untrusted instructions")) {
		t.Fatalf("frozen response exposed broker content: %s", response)
	}
	var reply struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  struct {
			Issue   frozenIssue `json:"structuredContent"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.JSONRPC != "2.0" || string(reply.ID) != "1" || reply.Result.IsError || len(reply.Result.Content) != 1 || reply.Result.Content[0].Type != "text" || reply.Result.Content[0].Text != "Accepted issue snapshot and current issue metadata were observed." {
		t.Fatalf("frozen envelope = %s", response)
	}
	issue := reply.Result.Issue
	if issue.Number != accepted.Snapshot.IssueNumber || issue.URL != "https://github.com/feed/original/issues/9" || issue.Title != accepted.Snapshot.Title || issue.Body != accepted.Snapshot.Body || len(issue.Labels) != 1 || issue.Labels[0] != "regression" || issue.UpdatedAt != "2026-09-18T20:00:00Z" || issue.State != "closed" || issue.StateReason == nil || *issue.StateReason != "completed" {
		t.Fatalf("frozen response = %s", response)
	}
	for _, malformed := range []json.RawMessage{
		bytes.Replace(upstream, []byte(`"labels":["regression"]`), []byte(`"labels":[],"labels":["regression"]`), 1),
		bytes.Replace(upstream, []byte(`"title":"revised instructions"`), []byte(`"title":"\ud800"`), 1),
	} {
		if _, err := frozenAcceptedIssueResponse(maintainerRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`)}, accepted, malformed); err == nil {
			t.Fatalf("malformed broker response accepted: %s", malformed)
		}
	}
}

func TestMaintainerTransportRejectsAmbiguousRepositoryArguments(t *testing.T) {
	ctx := context.Background()
	duplicate := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"observe_issue","arguments":{"repository":"feed/original","repository":"foreign/private","issue_number":9}}}`)
	fixture := newDispatchFixture(t)
	active := prepareActiveAttemptInProject(t, fixture, 175, testID(175), "orchestrator")
	if _, err := active.client.Maintainer(ctx, api.MaintainerInput{Request: duplicate}); !errors.Is(err, api.ErrInvalidInput) {
		t.Fatalf("duplicate tool argument = %v", err)
	}

	mixed := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"observe_issue","arguments":{"repository":"feed/original","Repository":"foreign/private","issue_number":9}}}`)
	session, found, err := fixture.store.TerminalSessionForRun(ctx, active.run.ID)
	if err != nil || !found {
		t.Fatalf("session: %v %v", found, err)
	}
	owner := newLiveAttempt(fixture.daemon, active.run.ID, session.ID, nil)
	owner.agentID, owner.attemptDigest = active.run.AgentID, active.run.CredentialDigest
	if err := fixture.daemon.registerLiveAttempt(owner); err != nil {
		t.Fatal(err)
	}
	defer fixture.daemon.unregisterLiveAttempt(active.run.ID, owner)
	done := fixture.serve(t)
	result, err := active.client.Maintainer(ctx, api.MaintainerInput{Request: mixed})
	waitDispatch(t, done)
	if err != nil || result.State != "invalid" || len(result.Response) != 0 {
		t.Fatalf("mixed-case tool argument = %+v %v", result, err)
	}
}

func TestAcceptedAttemptContextAndRestrictionsSurviveSourceSettingsChanges(t *testing.T) {
	fixture := newDispatchFixture(t)
	ctx := context.Background()
	project := mustProjectID(t, testID(180))
	if _, err := fixture.store.CreateProject(ctx, kernel.NewProject{ID: project, Name: "project", Root: "/project/180", SourceIdentity: &kernel.RepositorySourceIdentity{RootDevice: 1, RootInode: 180, GitDevice: 1, GitInode: 181, OriginDigest: [32]byte{180}, PublicationRepository: "publish/destination"}}, mustKernelTime(t, 999)); err != nil {
		t.Fatal(err)
	}
	var source kernel.IntakeSource
	var accepted kernel.IntakeAcceptance
	active := prepareActiveAttemptInProject(t, fixture, 180, project.String(), "orchestrator", func() {
		id, err := kernel.IntakeSourceIDFromBytes(bytes.Repeat([]byte{200}, kernel.IDBytes))
		if err != nil {
			t.Fatal(err)
		}
		agent, err := parseAgentID(testID(181))
		if err != nil {
			t.Fatal(err)
		}
		source, err = fixture.store.CreateIntakeSource(ctx, kernel.NewIntakeSource{ID: id, ProjectID: project, TargetRepositoryID: kernel.RepositoryID(project), OverseerAgentID: agent, GitHubRepositoryID: 42, GitHubRepositoryName: "feed/original", Policy: kernel.IntakePolicyManual, PollSeconds: 60, AdmissionLimit: 25}, mustKernelTime(t, 1000))
		if err != nil {
			t.Fatal(err)
		}
		source, err = fixture.store.SetIntakeSourceEnabled(ctx, source.ID, source.Revision, true, mustKernelTime(t, 1000))
		if err != nil {
			t.Fatal(err)
		}
		accepted, err = fixture.store.AcceptIntakeSnapshot(ctx, source.ID, kernel.IntakeIssueSnapshot{GitHubRepositoryID: 42, IssueNumber: 9, NodeID: "I_original", Title: "reviewed", Body: "exact accepted body", AuthorLogin: "reporter", AuthorType: kernel.GitHubAuthorUser}, mustKernelTime(t, 1000))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.ImportIntakeAcceptance(ctx, accepted.ID, mustKernelTime(t, 1000)); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := fixture.store.UpdateIntakeSource(ctx, source.ID, source.Revision, kernel.NewIntakeSource{ID: source.ID, ProjectID: project, TargetRepositoryID: source.TargetRepositoryID, GitHubRepositoryID: 99, GitHubRepositoryName: "feed/changed", Policy: kernel.IntakePolicyManual, PollSeconds: 60, AdmissionLimit: 25}, false, mustKernelTime(t, 1001)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.BindRepositoryGitHubID(ctx, accepted.RepositoryID, 43); err != nil {
		t.Fatal(err)
	}
	done := fixture.serve(t)
	assignment, err := active.client.Task(ctx)
	waitDispatch(t, done)
	if err != nil || assignment.Task != accepted.Snapshot.Body || assignment.Intake == nil || assignment.Intake.AcceptanceID != accepted.ID.String() || assignment.Intake.Repository != "feed/original" || assignment.Intake.RepositoryID != 42 || assignment.Intake.IssueNumber != 9 || assignment.Intake.TargetRepositoryID != accepted.RepositoryID.String() {
		t.Fatalf("frozen accepted assignment: %+v %v", assignment, err)
	}
	session, found, err := fixture.store.TerminalSessionForRun(ctx, active.run.ID)
	if err != nil || !found {
		t.Fatalf("session: %v %v", found, err)
	}
	owner := newLiveAttempt(fixture.daemon, active.run.ID, session.ID, nil)
	owner.agentID = active.run.AgentID
	owner.attemptDigest = active.run.CredentialDigest
	if err := fixture.daemon.registerLiveAttempt(owner); err != nil {
		t.Fatal(err)
	}
	defer fixture.daemon.unregisterLiveAttempt(active.run.ID, owner)
	// No customer host is installed. The valid receipt request reaches the host
	// boundary (unavailable); the two mismatches are denied before it.
	for _, check := range []struct {
		request api.MaintainerInput
		state   string
	}{
		{api.MaintainerInput{Request: json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"observe_issue","arguments":{"repository":"publish/destination","issue_number":9}}}`)}, "denied"},
		{api.MaintainerInput{Request: json.RawMessage(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"observe_issue","arguments":{"repository":"feed/original","issue_number":10}}}`)}, "denied"},
		{api.MaintainerInput{Request: json.RawMessage(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"observe_issue","arguments":{"repository":"feed/original","issue_number":9}}}`)}, "unavailable"},
	} {
		done := fixture.serve(t)
		result, err := active.client.Maintainer(ctx, check.request)
		waitDispatch(t, done)
		if err != nil || result.State != check.state || len(result.Response) != 0 {
			t.Fatalf("accepted issue observation: %+v %v", result, err)
		}
	}
	// An offline customer connection exercises local rejection without any
	// remote request or private credential in the fixture.
	if err := fixture.home.WriteMaintainerCredential([]byte(`{"disabled":true}`)); err != nil {
		t.Fatal(err)
	}
	fixture.daemon.github, err = maintainer.OpenHost(fixture.home)
	if err != nil {
		t.Fatal(err)
	}
	request := api.MaintainerInput{Request: json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_issues","arguments":{"repository":"feed/original"}}}`)}
	for _, want := range []string{"accepted_snapshot_required", "denied"} {
		done := fixture.serve(t)
		result, err := active.client.Maintainer(ctx, request)
		waitDispatch(t, done)
		if err != nil || result.State != want || len(result.Response) != 0 {
			t.Fatalf("accepted attempt response: %+v %v; want %s", result, err, want)
		}
		if _, err := fixture.store.WithdrawIntakeAcceptance(ctx, accepted.ID, mustKernelTime(t, 1002)); err != nil {
			t.Fatal(err)
		}
	}
}
