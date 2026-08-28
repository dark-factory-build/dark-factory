package kernel

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"testing"
)

func terminalTargetClient(t *testing.T, store *Store, id BrowserClientID, capabilities BrowserCapabilityMask) BrowserClient {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	digest := HashBrowserChallenge(append([]byte("terminal target "), id.Bytes()...))
	if _, err := store.CreateBrowserPairingChallenge(context.Background(), digest, browserTestBoot(t, id.Bytes()[0]+1), "https://app.example", capabilities, mustTime(t, 100), mustTime(t, 200)); err != nil {
		t.Fatal(err)
	}
	client, err := store.RedeemBrowserPairingChallenge(context.Background(), digest, browserTestBoot(t, id.Bytes()[0]+1), "https://app.example", id, elliptic.Marshal(elliptic.P256(), key.X, key.Y), mustTime(t, 101))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestResolveAgentTerminalTargetReturnsExactRunningCoordinates(t *testing.T) {
	store, run, _ := runningOrchestratorRun(t)
	defer store.Close()
	client := terminalTargetClient(t, store, browserTestID(t, 201), BrowserCapabilityObserve)
	agent, found, err := store.Agent(context.Background(), run.AgentID)
	if err != nil || !found {
		t.Fatalf("agent = %+v, found=%v, err=%v", agent, found, err)
	}
	session := terminalSessionForRunTest(t, store, run.ID)
	head, err := store.Factory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	target, available, err := store.ResolveAgentTerminalTarget(context.Background(), client.ID, agent.ID, agent.Revision, head.Head)
	if err != nil || !available {
		t.Fatalf("target = %+v, available=%v, err=%v", target, available, err)
	}
	if target.ProjectID() != agent.ProjectID || target.AgentID() != agent.ID || target.RunID() != run.ID || target.SessionID() != session.ID || target.RunRevision() != run.Revision || target.SessionRevision() != session.Revision {
		t.Fatalf("target = %+v", target)
	}
	// Accessors return values and identifier bytes are copied; neither can
	// mutate the authority captured in the target.
	runBytes := target.RunID().Bytes()
	runBytes[0] ^= 0xff
	if target.RunID() != run.ID {
		t.Fatal("target run identity was mutable")
	}
}

func TestTerminalTargetRejectsNonActiveSession(t *testing.T) {
	store, run, _ := runningOrchestratorRun(t)
	defer store.Close()
	session := terminalSessionForRunTest(t, store, run.ID)
	session.State = TerminalSessionDeclared
	if _, err := newTerminalTarget(run.ProjectID, run.AgentID, run, session); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("non-active session error = %v", err)
	}
}

func TestResolveAgentTerminalTargetRejectsStaleObservation(t *testing.T) {
	store, run, _ := runningOrchestratorRun(t)
	defer store.Close()
	client := terminalTargetClient(t, store, browserTestID(t, 202), BrowserCapabilityObserve)
	agent, _, _ := store.Agent(context.Background(), run.AgentID)
	head, _ := store.Factory(context.Background())
	if _, _, err := store.ResolveAgentTerminalTarget(context.Background(), client.ID, agent.ID, agent.Revision, mustSequence(t, head.Head.Int64()+1)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("wrong head error = %v", err)
	}
	if _, _, err := store.ResolveAgentTerminalTarget(context.Background(), client.ID, agent.ID, mustRevision(t, agent.Revision.Int64()+1), head.Head); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("wrong agent revision error = %v", err)
	}
}

func TestResolveAgentTerminalTargetOldHeadCannotSelectReplacementRun(t *testing.T) {
	store, first, firstKeys := runningOrchestratorRun(t)
	defer store.Close()
	client := terminalTargetClient(t, store, browserTestID(t, 203), BrowserCapabilityObserve)
	agent, found, err := store.Agent(context.Background(), first.AgentID)
	if err != nil || !found {
		t.Fatalf("agent = %+v, found=%v, err=%v", agent, found, err)
	}
	oldHead, err := store.Factory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := NewSuccessProposal("done")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ProposeAttemptOutcome(context.Background(), firstKeys.AttemptDigest, proposal, mustTime(t, 40)); err != nil {
		t.Fatal(err)
	}
	observeMissingProcessExits(t, store, first.ID, 45)
	releaseAllRunResources(t, store, first.ID, 50)
	current, found, err := store.Run(context.Background(), first.ID)
	if err != nil || !found {
		t.Fatalf("finalizing run = %+v, found=%v, err=%v", current, found, err)
	}
	session := terminalSessionForRunTest(t, store, current.ID)
	if _, err := store.CloseActiveTerminalSession(context.Background(), current.ID, session.ID, current.Revision, session.Revision, mustTime(t, 55)); err != nil {
		t.Fatal(err)
	}
	current, found, err = store.Run(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinalizeRun(context.Background(), current.ID, current.Revision, mustTime(t, 60)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueTask(context.Background(), NewTask{ID: taskID(t, 204), ProjectID: agent.ProjectID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 205), Title: "replacement"}, mustTime(t, 61)); err != nil {
		t.Fatal(err)
	}
	secondKeys := admissionKeys(t, 206, nil)
	second, err := store.AdmitNext(context.Background(), secondKeys, mustTime(t, 62))
	if err != nil || !second.Admitted() || second.Run.ID == first.ID {
		t.Fatalf("replacement admission = %+v, err=%v", second, err)
	}
	if _, _, err := store.ResolveAgentTerminalTarget(context.Background(), client.ID, agent.ID, agent.Revision, oldHead.Head); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("delayed old-head lookup error = %v", err)
	}
}

func TestResolveAgentTerminalTargetReturnsUnavailableForNonAttachableRun(t *testing.T) {
	tests := []struct {
		name string
		make func(*testing.T) (*Store, Agent)
	}{
		{name: "no run", make: func(t *testing.T) (*Store, Agent) {
			store, _, _, agent := newAdmissionStore(t, RoleOrchestrator, 2)
			return store, agent
		}},
		{name: "admitted", make: func(t *testing.T) (*Store, Agent) {
			store, run, _ := admittedOrchestratorRun(t)
			agent, _, err := store.Agent(context.Background(), run.AgentID)
			if err != nil {
				t.Fatal(err)
			}
			return store, agent
		}},
		{name: "finalizing", make: func(t *testing.T) (*Store, Agent) {
			store, run, keys := runningOrchestratorRun(t)
			proposal, err := NewSuccessProposal("done")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.ProposeAttemptOutcome(context.Background(), keys.AttemptDigest, proposal, mustTime(t, 40)); err != nil {
				t.Fatal(err)
			}
			agent, _, err := store.Agent(context.Background(), run.AgentID)
			if err != nil {
				t.Fatal(err)
			}
			return store, agent
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, agent := test.make(t)
			defer store.Close()
			client := terminalTargetClient(t, store, browserTestID(t, byte(210+len(test.name))), BrowserCapabilityObserve)
			head, err := store.Factory(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			target, available, err := store.ResolveAgentTerminalTarget(context.Background(), client.ID, agent.ID, agent.Revision, head.Head)
			if err != nil || available || target != (TerminalTarget{}) {
				t.Fatalf("target = %+v, available=%v, err=%v", target, available, err)
			}
		})
	}
}

func TestResolveAgentTerminalTargetRequiresObservedClient(t *testing.T) {
	store, run, _ := runningOrchestratorRun(t)
	defer store.Close()
	agent, _, _ := store.Agent(context.Background(), run.AgentID)
	head, _ := store.Factory(context.Background())
	missing := browserTestID(t, 240)
	if _, _, err := store.ResolveAgentTerminalTarget(context.Background(), missing, agent.ID, agent.Revision, head.Head); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("missing client error = %v", err)
	}
	client := terminalTargetClient(t, store, browserTestID(t, 241), BrowserCapabilityObserve)
	if _, err := store.RevokeBrowserClient(context.Background(), client.ID, client.Revision, mustTime(t, 300)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ResolveAgentTerminalTarget(context.Background(), client.ID, agent.ID, agent.Revision, head.Head); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked client error = %v", err)
	}
}

func TestResolveAgentTerminalTargetRejectsInvalidInputsAndClose(t *testing.T) {
	store, run, _ := runningOrchestratorRun(t)
	agent, _, _ := store.Agent(context.Background(), run.AgentID)
	head, _ := store.Factory(context.Background())
	client := terminalTargetClient(t, store, browserTestID(t, 250), BrowserCapabilityObserve)
	cases := []struct {
		name     string
		client   BrowserClientID
		agent    AgentID
		revision Revision
		head     EventSequence
	}{
		{name: "zero client", client: BrowserClientID{}, agent: agent.ID, revision: agent.Revision, head: head.Head},
		{name: "zero agent", client: client.ID, agent: AgentID{}, revision: agent.Revision, head: head.Head},
		{name: "zero revision", client: client.ID, agent: agent.ID, revision: Revision{}, head: head.Head},
		{name: "negative head", client: client.ID, agent: agent.ID, revision: agent.Revision, head: EventSequence{value: -1}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := store.ResolveAgentTerminalTarget(context.Background(), test.client, test.agent, test.revision, test.head); !errors.Is(err, ErrInvalidValue) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ResolveAgentTerminalTarget(context.Background(), client.ID, agent.ID, agent.Revision, head.Head); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("closed store error = %v", err)
	}
}

func TestResolveAgentTerminalTargetNoCrossAgentSelection(t *testing.T) {
	store, _, _, agent := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	otherProject, err := store.CreateProject(context.Background(), NewProject{ID: projectID(t, 251), Name: "other", Root: "/other"}, mustTime(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateAgent(context.Background(), NewAgent{ID: agentID(t, 252), ProjectID: otherProject.ID, Name: "other-agent", Role: RoleOrchestrator, Provider: ProviderCodex, ToolBudgetLimit: 2}, mustTime(t, 5))
	if err != nil {
		t.Fatal(err)
	}
	client := terminalTargetClient(t, store, browserTestID(t, 253), BrowserCapabilityObserve)
	head, _ := store.Factory(context.Background())
	if _, available, err := store.ResolveAgentTerminalTarget(context.Background(), client.ID, agent.ID, agent.Revision, head.Head); err != nil || available {
		t.Fatalf("cross-agent target available=%v err=%v", available, err)
	}
}

func TestResolveAgentTerminalTargetContextCancellation(t *testing.T) {
	store, run, _ := runningOrchestratorRun(t)
	defer store.Close()
	client := terminalTargetClient(t, store, browserTestID(t, 254), BrowserCapabilityObserve)
	agent, _, _ := store.Agent(context.Background(), run.AgentID)
	head, _ := store.Factory(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := store.ResolveAgentTerminalTarget(ctx, client.ID, agent.ID, agent.Revision, head.Head); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled error = %v", err)
	}
}
