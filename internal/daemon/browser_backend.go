package daemon

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
	"github.com/dark-factory-build/dark-factory/internal/topology"
)

// browserStatePollInterval bounds how long a paired browser waits to learn
// that durable state moved. There is no commit-time push today; the watcher
// reads the durable head on a bounded poll.
const browserStatePollInterval = 100 * time.Millisecond

// browserBackend is the direct browser-to-Store adapter. It deliberately
// keeps no durable projection or capability cache: every authenticated
// operation reloads the exact browser client while holding its ephemeral
// operation gate.
type browserBackend struct {
	owner *Daemon
	store *kernel.Store
	now   func() time.Time
	boot  kernel.BootID

	randomMu sync.Mutex
	random   io.Reader

	clientGates *browserClientGates

	// home answers the operator home discovery reads. Production wires the
	// account record; a test points it at a directory it owns, because
	// user.Current() is not something a test can redirect.
	home func() (string, error)

	inviteMu    sync.Mutex
	inviteMints [4]time.Time
	inviteNext  int

	subMu          sync.Mutex
	closing        bool
	subs           map[*browserStateWatch]struct{}
	observerCancel context.CancelFunc
	observerDone   chan struct{}
	observerWake   chan struct{}

	// package-test-only seam for a task-detail read that races a durable edit.
	afterTaskDetailTaskRead func()
}

type browserClientGate struct {
	users int
	slot  chan struct{}
}

// browserClientGates serializes only operations for one exact durable browser
// client. Store reads and writes remain the authority; this small ephemeral
// gate merely closes the authenticate/effect-versus-revoke race.
type browserClientGates struct {
	mu    sync.Mutex
	gates map[kernel.BrowserClientID]*browserClientGate
}

func newBrowserBackend(store *kernel.Store, now func() time.Time, random io.Reader) (*browserBackend, error) {
	if store == nil || now == nil || random == nil {
		return nil, fmt.Errorf("%w: invalid browser backend", kernel.ErrInvalidValue)
	}
	backend := &browserBackend{
		store: store, now: now, random: random, home: operatorHome,
		clientGates: &browserClientGates{gates: make(map[kernel.BrowserClientID]*browserClientGate)},
		subs:        make(map[*browserStateWatch]struct{}),
	}
	raw, err := backend.randomIdentifier()
	if err != nil {
		return nil, err
	}
	backend.boot, err = kernel.BootIDFromBytes(raw[:])
	if err != nil {
		return nil, fmt.Errorf("browser boot identity: %w", err)
	}
	return backend, nil
}

func newProductionBrowserBackend(daemon *Daemon) (*browserBackend, error) {
	if daemon == nil || daemon.store == nil || daemon.now == nil || daemon.browserClientGates == nil {
		return nil, fmt.Errorf("%w: invalid daemon browser backend", kernel.ErrInvalidValue)
	}
	backend, err := newBrowserBackend(daemon.store, daemon.now, rand.Reader)
	if err != nil {
		return nil, err
	}
	backend.clientGates = daemon.browserClientGates
	backend.owner = daemon
	return backend, nil
}

func (backend *browserBackend) Identity(ctx context.Context) (browser.Identity, error) {
	if backend == nil || backend.store == nil {
		return browser.Identity{}, browser.ErrUnauthorized
	}
	state, err := backend.store.Factory(ctx)
	if err != nil {
		return browser.Identity{}, mapBrowserError(err)
	}
	var result browser.Identity
	copy(result.DaemonID[:], state.DaemonID.Bytes())
	copy(result.BootID[:], backend.boot.Bytes())
	return result, nil
}

func (backend *browserBackend) Pair(ctx context.Context, request browser.PairRequest) (browser.Authentication, error) {
	identity, err := backend.Identity(ctx)
	if err != nil || request.Identity != identity {
		return browser.Authentication{}, browser.ErrUnauthorized
	}
	transcript, err := browserprotocol.BuildPairTranscript(browserprotocol.PairTranscript{
		DaemonID: request.DaemonID[:], BootID: request.BootID[:], ConnectionNonce: request.ConnectionNonce[:],
		Challenge: request.Challenge[:], PublicKeySEC1: request.PublicKeySEC1[:],
		ValidatedHost: request.Host, ValidatedOrigin: request.Origin,
	})
	if err != nil || browserprotocol.VerifySignature(request.PublicKeySEC1[:], request.Signature[:], transcript) != nil {
		return browser.Authentication{}, browser.ErrUnauthorized
	}
	rawID, err := backend.randomIdentifier()
	if err != nil {
		return browser.Authentication{}, err
	}
	clientID, err := kernel.BrowserClientIDFromBytes(rawID[:])
	if err != nil {
		return browser.Authentication{}, err
	}
	at, err := backend.timestamp()
	if err != nil {
		return browser.Authentication{}, err
	}
	client, err := backend.store.RedeemBrowserPairingChallenge(ctx, kernel.HashBrowserChallenge(request.Challenge[:]), backend.boot, request.Origin, clientID, request.PublicKeySEC1[:], at)
	if err != nil {
		return browser.Authentication{}, mapBrowserError(err)
	}
	return projectBrowserAuthentication(client)
}

func (backend *browserBackend) Authenticate(ctx context.Context, request browser.AuthRequest) (browser.Authentication, error) {
	identity, err := backend.Identity(ctx)
	if err != nil || request.Identity != identity {
		return browser.Authentication{}, browser.ErrUnauthorized
	}
	clientID, release, client, err := backend.authorize(ctx, request.ClientID, kernel.BrowserCapabilityObserve)
	if err != nil {
		return browser.Authentication{}, err
	}
	defer release()
	if client.ID != clientID {
		return browser.Authentication{}, browser.ErrUnauthorized
	}
	transcript, err := browserprotocol.BuildAuthTranscript(browserprotocol.AuthTranscript{
		DaemonID: request.DaemonID[:], BootID: request.BootID[:], ConnectionNonce: request.ConnectionNonce[:],
		ClientID: request.ClientID[:], ValidatedHost: request.Host, ValidatedOrigin: request.Origin,
	})
	if err != nil || browserprotocol.VerifySignature(client.PublicKey, request.Signature[:], transcript) != nil {
		return browser.Authentication{}, browser.ErrUnauthorized
	}
	return projectBrowserAuthentication(client)
}

func (backend *browserBackend) StateSnapshot(ctx context.Context, rawClient [browserprotocol.ClientIDSize]byte) (browserprotocol.StateSnapshot, error) {
	_, release, client, err := backend.authorize(ctx, rawClient, kernel.BrowserCapabilityObserve)
	if err != nil {
		return browserprotocol.StateSnapshot{}, err
	}
	defer release()
	snapshot, err := backend.store.ReadPublicSnapshot(ctx)
	if err != nil {
		return browserprotocol.StateSnapshot{}, mapBrowserError(err)
	}
	return projectPublicSnapshotForClient(snapshot, backend.owner.providerAccountDefaults, client.CapabilityMask.Has(kernel.BrowserCapabilityAdministration))
}

func (backend *browserBackend) HumanRequestDetail(ctx context.Context, rawClient [browserprotocol.ClientIDSize]byte, request browserprotocol.HumanRequestDetailGet) (browserprotocol.HumanRequestDetail, error) {
	clientID, release, _, err := backend.authorize(ctx, rawClient, kernel.BrowserCapabilityPrivateHumanRequestDetail)
	if err != nil {
		return browserprotocol.HumanRequestDetail{}, err
	}
	defer release()
	rawRequest, err := parseID(request.RequestID)
	if err != nil {
		return browserprotocol.HumanRequestDetail{}, browser.ErrStale
	}
	requestID, err := kernel.HumanRequestIDFromBytes(rawRequest)
	if err != nil || uint64(request.ExpectedRevision) > math.MaxInt64 {
		return browserprotocol.HumanRequestDetail{}, browser.ErrStale
	}
	revision, err := kernel.NewRevision(int64(request.ExpectedRevision))
	if err != nil {
		return browserprotocol.HumanRequestDetail{}, browser.ErrStale
	}
	detail, err := backend.store.HumanRequestDetail(ctx, clientID, requestID, revision)
	if err != nil {
		return browserprotocol.HumanRequestDetail{}, mapBrowserError(err)
	}
	result := browserprotocol.HumanRequestDetail{
		RequestID: detail.ID.String(), Revision: decimalRevision(detail.Revision), Question: detail.QuestionText, Options: detail.Options,
		CanReply: browserprotocol.Bool(detail.CanReply), ReplyMaxBytes: uint16(detail.ReplyMaxBytes),
	}
	if detail.TerminalTarget != nil {
		result.TerminalTarget = &browserprotocol.TerminalTargetDescriptor{
			RunID: detail.TerminalTarget.RunID().String(), SessionID: detail.TerminalTarget.SessionID().String(),
			RunRevision: decimalRevision(detail.TerminalTarget.RunRevision()), SessionRevision: decimalRevision(detail.TerminalTarget.SessionRevision()),
		}
	}
	if detail.CancelRun != nil {
		result.CancelRun = &browserprotocol.HumanRequestCancelRunDescriptor{
			RunID:                   detail.CancelRun.RunID().String(),
			ExpectedRequestRevision: decimalRevision(detail.CancelRun.ExpectedRequestRevision()),
			ExpectedRunRevision:     decimalRevision(detail.CancelRun.ExpectedRunRevision()),
		}
	}
	return result, nil
}

func (backend *browserBackend) TerminalTarget(ctx context.Context, rawClient [browserprotocol.ClientIDSize]byte, request browserprotocol.TerminalTargetGet) (browserprotocol.TerminalTarget, error) {
	clientID, release, _, err := backend.authorize(ctx, rawClient, kernel.BrowserCapabilityObserve)
	if err != nil {
		return browserprotocol.TerminalTarget{}, err
	}
	defer release()
	agentID, err := browserID(request.AgentID, kernel.AgentIDFromBytes)
	if err != nil {
		return browserprotocol.TerminalTarget{}, browser.ErrStale
	}
	expectedAgent, err := browserDecimal(request.ExpectedAgentRevision)
	if err != nil {
		return browserprotocol.TerminalTarget{}, browser.ErrStale
	}
	if request.ExpectedHead > math.MaxInt64 {
		return browserprotocol.TerminalTarget{}, browser.ErrStale
	}
	expectedHead, err := kernel.NewEventSequence(int64(request.ExpectedHead))
	if err != nil {
		return browserprotocol.TerminalTarget{}, browser.ErrStale
	}
	target, available, err := backend.store.ResolveAgentTerminalTarget(ctx, clientID, agentID, expectedAgent, expectedHead)
	if err != nil {
		return browserprotocol.TerminalTarget{}, mapBrowserError(err)
	}
	result := browserprotocol.TerminalTarget{AgentID: request.AgentID, AgentRevision: request.ExpectedAgentRevision, Head: request.ExpectedHead}
	if available {
		result.Target = &browserprotocol.TerminalTargetDescriptor{
			RunID: target.RunID().String(), SessionID: target.SessionID().String(),
			RunRevision: decimalRevision(target.RunRevision()), SessionRevision: decimalRevision(target.SessionRevision()),
		}
	}
	return result, nil
}

// PairLink is the pair page's mint: the one-shot launch link OpenBrowser
// returns.
func (backend *browserBackend) PairLink(ctx context.Context) (string, error) {
	if backend == nil || backend.owner == nil {
		return "", browser.ErrUnauthorized
	}
	return backend.owner.OpenBrowser(ctx)
}

func (backend *browserBackend) AuthorizeTaskAttachments(ctx context.Context, rawClient [browserprotocol.ClientIDSize]byte) error {
	_, release, _, err := backend.authorize(ctx, rawClient, kernel.BrowserCapabilityHumanActions)
	if err != nil {
		return err
	}
	release()
	return nil
}

func (backend *browserBackend) EnqueueTask(ctx context.Context, rawClient [browserprotocol.ClientIDSize]byte, request browserprotocol.TaskEnqueue) (browserprotocol.TaskEnqueueResult, error) {
	clientID, release, _, err := backend.authorize(ctx, rawClient, kernel.BrowserCapabilityHumanActions)
	if err != nil {
		return browserprotocol.TaskEnqueueResult{}, err
	}
	defer release()
	taskID, err := browserID(request.TaskID, kernel.TaskIDFromBytes)
	if err != nil {
		return browserprotocol.TaskEnqueueResult{}, browser.ErrStale
	}
	incarnationID, err := browserID(request.IncarnationID, kernel.IncarnationIDFromBytes)
	if err != nil {
		return browserprotocol.TaskEnqueueResult{}, browser.ErrStale
	}
	agentID, err := browserID(request.AgentID, kernel.AgentIDFromBytes)
	if err != nil {
		return browserprotocol.TaskEnqueueResult{}, browser.ErrStale
	}
	expectedAgentRevision, err := browserDecimal(request.ExpectedAgentRevision)
	if err != nil {
		return browserprotocol.TaskEnqueueResult{}, browser.ErrStale
	}
	var repositoryID kernel.RepositoryID
	if request.RepositoryID != "" {
		repositoryID, err = browserID(request.RepositoryID, kernel.RepositoryIDFromBytes)
		if err != nil {
			return browserprotocol.TaskEnqueueResult{}, browser.ErrStale
		}
	}
	at, err := backend.timestamp()
	if err != nil {
		return browserprotocol.TaskEnqueueResult{}, mapBrowserError(err)
	}
	mode := kernel.BrowserEnqueueNow
	switch request.Mode {
	case "queue":
		mode = kernel.BrowserEnqueueQueue
	case "any":
		mode = kernel.BrowserEnqueueAnyWorker
	}
	attachments := make([]kernel.TaskAttachment, len(request.Attachments))
	for i, item := range request.Attachments {
		attachments[i] = kernel.TaskAttachment{Name: item.Name, Data: item.Data}
	}
	instruction, err := kernel.TaskAttachmentInstruction(request.Instruction, attachments)
	if err != nil {
		return browserprotocol.TaskEnqueueResult{}, browser.ErrTooLarge
	}
	if mode == kernel.BrowserEnqueueAnyWorker {
		if err := prepareSharedTaskText("Direct instruction", instruction); err != nil {
			return browserprotocol.TaskEnqueueResult{}, browser.ErrTooLarge
		}
	} else if err := backend.prepareAgentInstruction(ctx, agentID, instruction); err != nil {
		return browserprotocol.TaskEnqueueResult{}, err
	}
	result, err := backend.store.EnqueueTaskForBrowserAgentRepositoryMode(ctx, clientID, taskID, incarnationID, agentID, expectedAgentRevision, repositoryID, request.Instruction, mode, at, attachments...)
	if err != nil {
		return browserprotocol.TaskEnqueueResult{}, mapBrowserError(err)
	}
	if backend.owner != nil {
		backend.owner.notifyScheduler()
	}
	return browserprotocol.TaskEnqueueResult{TaskID: result.Task.ID.String(), Revision: decimalRevision(result.Task.Revision), AgentRevision: decimalRevision(result.AgentRevision)}, nil
}

// The transport discovers the console half by type assertion, so a signature
// that drifts would silently answer unauthorized instead of failing to build.
var _ browser.ConsoleBackend = (*browserBackend)(nil)

func (backend *browserBackend) GitHubConnection(ctx context.Context, rawClient [browserprotocol.ClientIDSize]byte, request browserprotocol.GitHubConnection) (browserprotocol.GitHubConnectionResult, error) {
	clientID, release, client, err := backend.authorize(ctx, rawClient, kernel.BrowserCapabilityAdministration)
	if err != nil {
		return browserprotocol.GitHubConnectionResult{}, err
	}
	if client.ID != clientID || backend.owner == nil {
		release()
		return browserprotocol.GitHubConnectionResult{}, browser.ErrUnauthorized
	}
	// The request was admitted while authorized. Let revocation close the
	// transport and cancel an in-flight broker call, then check authority again
	// before returning any private connection or repository metadata.
	release()
	input := api.GitHubConnectionInput{Action: request.Action, Code: request.Code, Page: request.Page, InstallationID: request.InstallationID}
	for _, item := range request.Repositories {
		input.Repositories = append(input.Repositories, maintainer.Delegation{InstallationID: item.InstallationID, RepositoryID: item.RepositoryID, Repository: item.Repository})
	}
	if backend.owner.browserRemote != nil {
		backend.owner.browserRemote(ctx, "github")
	}
	result := backend.owner.GitHubConnection(ctx, input)
	_, release, _, err = backend.authorize(ctx, rawClient, kernel.BrowserCapabilityAdministration)
	if err != nil {
		return browserprotocol.GitHubConnectionResult{}, err
	}
	release()
	return projectGitHubConnection(result), nil
}

func projectGitHubConnection(result api.GitHubConnectionResult) browserprotocol.GitHubConnectionResult {
	value := browserprotocol.GitHubConnectionResult{State: result.State}
	if result.Authorization != nil {
		value.Authorization = &browserprotocol.GitHubAuthorization{ConnectionID: result.Authorization.ConnectionID, URL: result.Authorization.URL, ExpiresAt: browserprotocol.Decimal(result.Authorization.ExpiresAt)}
	}
	if result.Status != nil {
		value.Status = &browserprotocol.GitHubStatus{ConnectionID: result.Status.ConnectionID, State: result.Status.State, Repositories: make([]browserprotocol.GitHubDelegation, len(result.Status.Repositories))}
		if result.Status.User != nil {
			value.Status.User = &browserprotocol.GitHubUser{ID: result.Status.User.ID, Login: result.Status.User.Login, Type: result.Status.User.Type}
		}
		for i, item := range result.Status.Repositories {
			value.Status.Repositories[i] = browserprotocol.GitHubDelegation{InstallationID: item.InstallationID, RepositoryID: item.RepositoryID, Repository: item.Repository}
		}
	}
	if result.Installations != nil {
		value.Installations = &browserprotocol.GitHubInstallations{Items: make([]browserprotocol.GitHubInstallation, len(result.Installations.Items)), NextPage: result.Installations.NextPage, InstallationURL: result.Installations.InstallationURL}
		for i, item := range result.Installations.Items {
			value.Installations.Items[i] = browserprotocol.GitHubInstallation{ID: item.ID, Account: browserprotocol.GitHubUser{ID: item.Account.ID, Login: item.Account.Login, Type: item.Account.Type}, SuspendedAt: item.SuspendedAt, URL: item.URL, Eligibility: item.Eligibility}
		}
	}
	if result.Repositories != nil {
		value.Repositories = &browserprotocol.GitHubRepositories{Items: make([]browserprotocol.GitHubRepository, len(result.Repositories.Items)), NextPage: result.Repositories.NextPage}
		for i, item := range result.Repositories.Items {
			value.Repositories.Items[i].ID, value.Repositories.Items[i].Name = item.ID, item.Name
			value.Repositories.Items[i].Permissions.Pull, value.Repositories.Items[i].Permissions.Push, value.Repositories.Items[i].Permissions.Maintain, value.Repositories.Items[i].Permissions.Admin = item.Permissions.Pull, item.Permissions.Push, item.Permissions.Maintain, item.Permissions.Admin
		}
	}
	return value
}

func (backend *browserBackend) UpdateAgent(ctx context.Context, rawClient [browserprotocol.ClientIDSize]byte, request browserprotocol.AgentUpdate) (browserprotocol.AgentUpdateResult, error) {
	_, release, client, err := backend.authorize(ctx, rawClient, kernel.BrowserCapabilityHumanActions)
	if err != nil {
		return browserprotocol.AgentUpdateResult{}, err
	}
	defer release()
	// Which login an agent runs as is administration, like the logins themselves.
	if request.AccountID != nil && !client.CapabilityMask.Has(kernel.BrowserCapabilityAdministration) {
		return browserprotocol.AgentUpdateResult{}, browser.ErrUnauthorized
	}
	agentID, err := browserID(request.AgentID, kernel.AgentIDFromBytes)
	if err != nil {
		return browserprotocol.AgentUpdateResult{}, browser.ErrStale
	}
	expected, err := browserDecimal(request.ExpectedRevision)
	if err != nil {
		return browserprotocol.AgentUpdateResult{}, browser.ErrStale
	}
	at, err := backend.timestamp()
	if err != nil {
		return browserprotocol.AgentUpdateResult{}, mapBrowserError(err)
	}
	patch := kernel.AgentPatch{Model: request.Model, ReasoningEffort: request.ReasoningEffort}
	if request.Appearance != nil {
		patch.Appearance = &kernel.AgentAppearance{Automatic: bool(request.Appearance.Automatic), Skin: request.Appearance.Skin, Hair: request.Appearance.Hair, HairColour: request.Appearance.HairColour, Face: request.Appearance.Face, Outfit: request.Appearance.Outfit, ClothesColour: request.Appearance.ClothesColour, Shoes: request.Appearance.Shoes, Tool: request.Appearance.Tool, Headwear: request.Appearance.Headwear}
	}
	if request.AccountID != nil {
		// An empty account clears the selection back to the provider default;
		// anything else must be one canonical account identity.
		selected := kernel.AccountID{}
		if *request.AccountID != "" {
			if selected, err = browserID(*request.AccountID, kernel.AccountIDFromBytes); err != nil {
				return browserprotocol.AgentUpdateResult{}, browser.ErrStale
			}
		}
		patch.AccountID = &selected
	}
	if request.Paused != nil {
		paused := bool(*request.Paused)
		patch.Paused = &paused
	}
	if request.Archived != nil {
		archived := bool(*request.Archived)
		patch.Archived = &archived
	}
	if request.IdlePolicy != nil {
		policy := kernel.IdlePolicy(*request.IdlePolicy)
		patch.IdlePolicy = &policy
	}
	patch.IdleAfterSeconds, patch.IdleInstruction, patch.IdleRunBudget = request.IdleAfterSeconds, request.IdleInstruction, request.IdleRunBudget
	agent, err := backend.store.UpdateAgent(ctx, agentID, expected, patch, at)
	if err != nil {
		return browserprotocol.AgentUpdateResult{}, consoleUpdateError(err)
	}
	if backend.owner != nil {
		backend.owner.notifyScheduler()
	}
	return browserprotocol.AgentUpdateResult{AgentID: agent.ID.String(), Revision: decimalRevision(agent.Revision)}, nil
}

func (backend *browserBackend) SetProjectLimits(ctx context.Context, rawClient [browserprotocol.ClientIDSize]byte, request browserprotocol.ProjectLimits) (browserprotocol.ProjectLimitsResult, error) {
	_, release, _, err := backend.authorize(ctx, rawClient, kernel.BrowserCapabilityAdministration)
	if err != nil {
		return browserprotocol.ProjectLimitsResult{}, err
	}
	defer release()
	projectID, err := browserID(request.ProjectID, kernel.ProjectIDFromBytes)
	if err != nil {
		return browserprotocol.ProjectLimitsResult{}, browser.ErrStale
	}
	expected, err := browserDecimal(request.ExpectedRevision)
	if err != nil {
		return browserprotocol.ProjectLimitsResult{}, browser.ErrStale
	}
	at, err := backend.timestamp()
	if err != nil {
		return browserprotocol.ProjectLimitsResult{}, mapBrowserError(err)
	}
	project, err := backend.store.SetProjectLimits(ctx, projectID, expected, uint64(request.RunBudget), request.MaxRunSeconds, at)
	if err != nil {
		return browserprotocol.ProjectLimitsResult{}, consoleUpdateError(err)
	}
	if backend.owner != nil {
		backend.owner.notifyScheduler()
	}
	return browserprotocol.ProjectLimitsResult{ProjectID: project.ID.String(), Revision: decimalRevision(project.Revision)}, nil
}

func (backend *browserBackend) SetDispatch(ctx context.Context, rawClient [browserprotocol.ClientIDSize]byte, request browserprotocol.FactoryDispatch) (browserprotocol.FactoryDispatchResult, error) {
	_, release, _, err := backend.authorize(ctx, rawClient, kernel.BrowserCapabilityAdministration)
	if err != nil {
		return browserprotocol.FactoryDispatchResult{}, err
	}
	defer release()
	expected, err := browserDecimal(request.ExpectedRevision)
	if err != nil {
		return browserprotocol.FactoryDispatchResult{}, browser.ErrStale
	}
	at, err := backend.timestamp()
	if err != nil {
		return browserprotocol.FactoryDispatchResult{}, mapBrowserError(err)
	}
	state, err := backend.store.SetDispatch(ctx, expected, bool(request.Enabled), at)
	if err != nil {
		return browserprotocol.FactoryDispatchResult{}, consoleUpdateError(err)
	}
	if backend.owner != nil {
		backend.owner.notifyScheduler()
	}
	return browserprotocol.FactoryDispatchResult{Revision: decimalRevision(state.Revision), Enabled: browserprotocol.Bool(state.DispatchEnabled)}, nil
}

func (backend *browserBackend) CreateProject(ctx context.Context, rawClient [browserprotocol.ClientIDSize]byte, request browserprotocol.ProjectCreate) (browserprotocol.ProjectCreateResult, error) {
	_, release, _, err := backend.authorize(ctx, rawClient, kernel.BrowserCapabilityAdministration)
	if err != nil {
		return browserprotocol.ProjectCreateResult{}, err
	}
	defer release()
	projectID, err := browserID(request.ProjectID, kernel.ProjectIDFromBytes)
	if err != nil {
		return browserprotocol.ProjectCreateResult{}, browser.ErrStale
	}
	at, err := backend.timestamp()
	if err != nil {
		return browserprotocol.ProjectCreateResult{}, mapBrowserError(err)
	}
	project, err := registerProject(ctx, backend.store, kernel.NewProject{ID: projectID, Name: request.Name, Root: request.Root, VerificationPolicy: kernel.VerificationNone}, at)
	if err != nil {
		return browserprotocol.ProjectCreateResult{}, consoleUpdateError(err)
	}
	return browserprotocol.ProjectCreateResult{ProjectID: project.ID.String(), Revision: decimalRevision(project.Revision)}, nil
}

func browserRepository(value api.ProjectRepository) browserprotocol.Repository {
	result := browserprotocol.Repository{ID: value.ID, ProjectID: value.ProjectID, Name: value.Name, Root: value.Root, BaseRef: value.BaseRef, Enabled: browserprotocol.Bool(value.Enabled), Default: browserprotocol.Bool(value.Default), Revision: browserprotocol.Decimal(value.Revision), FetchState: value.FetchState, PublicationState: value.PublicationState, ReadinessMessage: value.ReadinessMessage}
	if value.GitHubRepositoryID != 0 {
		id := browserprotocol.Decimal(value.GitHubRepositoryID)
		result.GitHubRepositoryID = &id
	}
	return result
}
func (backend *browserBackend) Repositories(ctx context.Context, rawClient [browserprotocol.ClientIDSize]byte, request browserprotocol.RepositoriesGet) (browserprotocol.Repositories, error) {
	_, release, _, err := backend.authorize(ctx, rawClient, kernel.BrowserCapabilityAdministration)
	if err != nil {
		return browserprotocol.Repositories{}, err
	}
	defer release()
	projectID, err := browserID(request.ProjectID, kernel.ProjectIDFromBytes)
	if err != nil {
		return browserprotocol.Repositories{}, browser.ErrStale
	}
	values, err := backend.store.ProjectRepositories(ctx, projectID)
	if err != nil {
		return browserprotocol.Repositories{}, mapBrowserError(err)
	}
	result := browserprotocol.Repositories{ProjectID: request.ProjectID, Items: make([]browserprotocol.Repository, 0, len(values))}
	for _, value := range values {
		view := repositoryDTO(value)
		if backend.owner != nil {
			view, err = backend.owner.RepositoryReadiness(ctx, value.ID, false)
			if err != nil {
				return browserprotocol.Repositories{}, mapBrowserError(err)
			}
		}
		result.Items = append(result.Items, browserRepository(view))
	}
	return result, nil
}
func (backend *browserBackend) MutateRepository(ctx context.Context, rawClient [browserprotocol.ClientIDSize]byte, request browserprotocol.RepositoryMutate) (browserprotocol.RepositoryMutateResult, error) {
	_, release, _, err := backend.authorize(ctx, rawClient, kernel.BrowserCapabilityAdministration)
	if err != nil {
		return browserprotocol.RepositoryMutateResult{}, err
	}
	remote := request.Action == "fetch" || request.Action == "github"
	if remote {
		// Source inspection and GitHub verification can wait on a remote. A
		// paired-client revocation must be able to cancel that transport.
		release()
	} else {
		defer release()
	}
	at, err := backend.timestamp()
	if err != nil {
		return browserprotocol.RepositoryMutateResult{}, mapBrowserError(err)
	}
	parse := func() (kernel.RepositoryID, error) { return browserID(request.ID, kernel.RepositoryIDFromBytes) }
	if request.Action == "add" {
		id, err := parse()
		if err != nil {
			return browserprotocol.RepositoryMutateResult{}, browser.ErrStale
		}
		project, err := browserID(request.ProjectID, kernel.ProjectIDFromBytes)
		if err != nil {
			return browserprotocol.RepositoryMutateResult{}, browser.ErrStale
		}
		value, err := registerProjectRepository(ctx, backend.store, kernel.NewProjectRepository{ID: id, ProjectID: project, Name: request.Name, Root: request.Root, BaseRef: request.BaseRef}, at)
		if err != nil {
			return browserprotocol.RepositoryMutateResult{}, consoleUpdateError(err)
		}
		item := browserRepository(repositoryDTO(value))
		return browserprotocol.RepositoryMutateResult{Repository: &item}, nil
	}
	id, err := parse()
	if err != nil {
		return browserprotocol.RepositoryMutateResult{}, browser.ErrStale
	}
	if remote {
		if backend.owner == nil {
			return browserprotocol.RepositoryMutateResult{}, browser.ErrStale
		}
		if backend.owner.browserRemote != nil {
			backend.owner.browserRemote(ctx, "repository")
		}
		var callErr error
		if request.Action == "github" {
			callErr = backend.owner.BindProjectRepositoryGitHub(ctx, id)
		}
		var view api.ProjectRepository
		if callErr == nil {
			view, callErr = backend.owner.RepositoryReadiness(ctx, id, request.Action == "fetch")
		}
		_, release, _, err = backend.authorize(ctx, rawClient, kernel.BrowserCapabilityAdministration)
		if err != nil {
			return browserprotocol.RepositoryMutateResult{}, err
		}
		release()
		if callErr != nil {
			return browserprotocol.RepositoryMutateResult{}, consoleUpdateError(callErr)
		}
		if request.Action == "github" {
			view.PublicationState = "ready"
			view.ReadinessMessage = "Repository identity verified through the GitHub connection. Publication permissions are checked for each operation."
		}
		item := browserRepository(view)
		return browserprotocol.RepositoryMutateResult{Repository: &item}, nil
	}
	expected, err := browserDecimal(request.ExpectedRevision)
	if err != nil {
		return browserprotocol.RepositoryMutateResult{}, browser.ErrStale
	}
	var value kernel.ProjectRepository
	switch request.Action {
	case "name":
		value, err = backend.store.UpdateProjectRepositoryName(ctx, id, expected, request.Name, at)
	case "base":
		value, err = updateRepositoryBase(ctx, backend.store, id, expected, request.BaseRef, at)
	case "default":
		value, err = backend.store.SetProjectRepositoryDefault(ctx, id, expected, at)
	case "enabled":
		if request.Enabled == nil {
			return browserprotocol.RepositoryMutateResult{}, browser.ErrStale
		}
		value, err = backend.store.SetProjectRepositoryEnabled(ctx, id, expected, bool(*request.Enabled), at)
	case "remove":
		err = backend.store.RemoveProjectRepository(ctx, id, expected)
	default:
		return browserprotocol.RepositoryMutateResult{}, browser.ErrStale
	}
	if err != nil {
		return browserprotocol.RepositoryMutateResult{}, consoleUpdateError(err)
	}
	if request.Action == "remove" {
		return browserprotocol.RepositoryMutateResult{}, nil
	}
	item := browserRepository(repositoryDTO(value))
	return browserprotocol.RepositoryMutateResult{Repository: &item}, nil
}

func (backend *browserBackend) UpdateTask(ctx context.Context, rawClient [browserprotocol.ClientIDSize]byte, request browserprotocol.TaskUpdate) (browserprotocol.TaskUpdateResult, error) {
	_, release, _, err := backend.authorize(ctx, rawClient, kernel.BrowserCapabilityHumanActions)
	if err != nil {
		return browserprotocol.TaskUpdateResult{}, err
	}
	defer release()
	taskID, err := browserID(request.TaskID, kernel.TaskIDFromBytes)
	if err != nil {
		return browserprotocol.TaskUpdateResult{}, browser.ErrStale
	}
	expected, err := browserDecimal(request.ExpectedRevision)
	if err != nil {
		return browserprotocol.TaskUpdateResult{}, browser.ErrStale
	}
	patch := kernel.TaskPatch{Title: request.Title, Body: request.Body, Priority: request.Priority, Cancel: request.Status != nil && *request.Status == "cancelled"}
	if request.AssignedAgentID != nil {
		assigned, err := browserID(*request.AssignedAgentID, kernel.AgentIDFromBytes)
		if err != nil {
			return browserprotocol.TaskUpdateResult{}, browser.ErrStale
		}
		patch.AssignedAgentID = &assigned
	}
	retry := request.Status != nil && *request.Status == "queued"
	var assigned kernel.AgentID
	if patch.AssignedAgentID != nil {
		assigned = *patch.AssignedAgentID
	}
	if retry {
		err = prepareTaskRetry(ctx, backend.store, taskID, expected, assigned)
	} else {
		err = prepareQueuedTaskPatch(ctx, backend.store, taskID, expected, patch)
	}
	if err != nil {
		return browserprotocol.TaskUpdateResult{}, consoleUpdateError(err)
	}
	at, err := backend.timestamp()
	if err != nil {
		return browserprotocol.TaskUpdateResult{}, mapBrowserError(err)
	}
	var task kernel.Task
	if retry {
		task, err = backend.store.RetryTaskForOperator(ctx, taskID, expected, assigned, at)
	} else {
		task, err = backend.store.UpdateTask(ctx, taskID, expected, patch, at)
	}
	if err != nil {
		return browserprotocol.TaskUpdateResult{}, consoleUpdateError(err)
	}
	if backend.owner != nil {
		backend.owner.notifyScheduler()
	}
	return browserprotocol.TaskUpdateResult{TaskID: task.ID.String(), Revision: decimalRevision(task.Revision)}, nil
}

// Topology serves the regenerable project structure the daemon already caches
// on disk. Nodes only: containment is implied by parent, so v1 has no edges.
func (backend *browserBackend) Topology(ctx context.Context, rawClient [browserprotocol.ClientIDSize]byte, request browserprotocol.TopologyGet) (browserprotocol.Topology, error) {
	_, release, _, err := backend.authorize(ctx, rawClient, kernel.BrowserCapabilityObserve)
	if err != nil {
		return browserprotocol.Topology{}, err
	}
	// The walk reads no client state and may take most of its budget, so it
	// runs outside the client's gate: a state or terminal call from the same
	// client is not charged the walk's time. Each connection's walker keeps
	// that connection to one walk at a time; a client with several
	// connections may walk on each of them.
	release()
	if backend.owner == nil {
		return browserprotocol.Topology{}, browser.ErrNotFound
	}
	projectID, err := browserID(request.ProjectID, kernel.ProjectIDFromBytes)
	if err != nil {
		return browserprotocol.Topology{}, browser.ErrStale
	}
	snapshot, err := backend.owner.ProjectTopology(ctx, projectID)
	if err != nil {
		return browserprotocol.Topology{}, mapBrowserError(err)
	}
	return projectTopology(request.ProjectID, snapshot), nil
}

// projectTopology is the one conversion from the derived graph to the wire.
// The tree is a filesystem and bounds nothing; the wire bounds every node's
// text. A node past a bound is dropped with everything under it, so one
// long directory name costs its subtree instead of making the whole project
// unserveable. Truncating instead would invent a label and could silently
// merge two siblings that differ only past the cut.
func projectTopology(projectID string, snapshot topology.Snapshot) browserprotocol.Topology {
	result := browserprotocol.Topology{
		ProjectID: projectID, Digest: snapshot.Digest, SourceRevision: snapshot.SourceRevision,
		Nodes: make([]browserprotocol.TopologyNode, 0, len(snapshot.Nodes)),
	}
	// Nodes are ordered by path and kind, which is not ancestry order: a root
	// module sorts before the repository that contains it. So each node is
	// decided by walking its own ancestor chain, never by slice position.
	byID := make(map[string]topology.Node, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		byID[node.ID] = node
	}
	decided := make(map[string]bool, len(snapshot.Nodes))
	var servable func(string) bool
	servable = func(id string) bool {
		if result, ok := decided[id]; ok {
			return result
		}
		node, ok := byID[id]
		if !ok {
			return false
		}
		// Provisionally false, so a chain that loops back on corrupt input
		// terminates and fails closed rather than recursing forever.
		decided[id] = false
		result := servableTopologyText(node) && (node.ParentID == "" || servable(node.ParentID))
		decided[id] = result
		return result
	}
	for _, node := range snapshot.Nodes {
		if !servable(node.ID) {
			continue
		}
		result.Nodes = append(result.Nodes, browserprotocol.TopologyNode{
			ID: node.ID, ParentID: node.ParentID, Kind: string(node.Kind), Path: node.RelativePath,
			Label: node.Label, Language: node.Language, SizeBucket: node.SizeBucket,
		})
	}
	dependencies := &browserprotocol.TopologyDependencies{Source: "go-imports-package-manifests", Edges: []browserprotocol.TopologyEdge{}}
	for _, edge := range snapshot.Edges {
		if edge.Kind == topology.EdgeImports {
			dependencies.Omitted++
		}
	}
	result.Dependencies = dependencies
	inventoryOmitted := uint32(len(result.Nodes))
	result.InventoryOmitted = &inventoryOmitted
	// Reserve the control envelope; never grow the existing 1 MiB topology cap.
	base, _ := json.Marshal(result)
	remaining := browserprotocol.MaxSnapshotBytes - len(base) - 512
	seen := make(map[[2]string]bool)
	for _, edge := range snapshot.Edges {
		pair := [2]string{edge.From, edge.To}
		if edge.Kind != topology.EdgeImports || !servable(edge.From) || !servable(edge.To) || edge.From == edge.To || edge.Weight == 0 || seen[pair] {
			continue
		}
		observed := browserprotocol.TopologyEdge{From: edge.From, To: edge.To, Weight: edge.Weight}
		encoded, _ := json.Marshal(observed)
		if len(dependencies.Edges) == browserprotocol.MaxTopologyEdges || len(encoded)+1 > remaining {
			continue
		}
		dependencies.Edges = append(dependencies.Edges, observed)
		dependencies.Omitted--
		seen[pair] = true
		remaining -= len(encoded) + 1
	}
	// Count summaries follow useful dependencies. Filenames are last so one
	// room cannot consume the budget needed to describe another room.
	for i := range result.Nodes {
		inventory := byID[result.Nodes[i].ID].Inventory
		if inventory == nil {
			inventoryOmitted--
			continue
		}
		projected := &browserprotocol.TopologyInventory{
			Direct:  browserprotocol.TopologyInventoryCounts(inventory.Direct),
			Total:   browserprotocol.TopologyInventoryCounts(inventory.Total),
			Samples: []string{}, SamplesOmitted: inventory.SamplesOmitted + uint32(len(inventory.Samples)),
		}
		encoded, _ := json.Marshal(projected)
		cost := len(encoded) + len(`,"inventory":`)
		if cost > remaining {
			continue
		}
		result.Nodes[i].Inventory = projected
		inventoryOmitted--
		remaining -= cost
	}
	for i := range result.Nodes {
		projected := result.Nodes[i].Inventory
		if projected == nil {
			continue
		}
		inventory := byID[result.Nodes[i].ID].Inventory
		if len(inventory.Samples) == 0 {
			continue
		}
		withSamples := *projected
		withSamples.Samples = inventory.Samples
		withSamples.SamplesOmitted = inventory.SamplesOmitted
		before, _ := json.Marshal(projected)
		after, _ := json.Marshal(withSamples)
		cost := len(after) - len(before)
		if cost > remaining {
			continue
		}
		projected.Samples = append([]string{}, inventory.Samples...)
		projected.SamplesOmitted = inventory.SamplesOmitted
		remaining -= cost
	}
	return result
}

// consoleUpdateError separates a member the domain refuses from a lost race.
// Both are the operator's to resolve, but only one is resolved by refetching:
// a reasoning effort no provider accepts is still refused after a refresh.
func consoleUpdateError(err error) error {
	if errors.Is(err, kernel.ErrInvalidValue) {
		return browser.ErrInvalidRequest
	}
	return mapBrowserError(err)
}

func servableTopologyText(node topology.Node) bool {
	return validTopologyText(node.RelativePath, 1, browserprotocol.MaxTaskTitleBytes) &&
		validTopologyText(node.Label, 1, browserprotocol.MaxAgentNameBytes) &&
		validTopologyText(node.Language, 0, browserprotocol.MaxAgentNameBytes)
}

func validTopologyText(value string, minimum, maximum int) bool {
	return len(value) >= minimum && len(value) <= maximum && utf8.ValidString(value)
}

// SubscribePush stores one device's alert subscription under its own client
// identity. Observing is enough: a device may only ever ask to be woken.
func (backend *browserBackend) SubscribePush(ctx context.Context, rawClient [browserprotocol.ClientIDSize]byte, subscription browserprotocol.PushSubscribe) error {
	clientID, release, _, err := backend.authorize(ctx, rawClient, kernel.BrowserCapabilityObserve)
	if err != nil {
		return err
	}
	defer release()
	if _, err := parsePushKeys(subscription); err != nil {
		return browser.ErrInvalidRequest
	}
	if backend.owner == nil {
		return browser.ErrUnauthorized
	}
	backend.owner.browserMu.Lock()
	store := backend.owner.push
	backend.owner.browserMu.Unlock()
	if store == nil {
		return browser.ErrUnauthorized
	}
	if err := store.update(func(subscriptions map[string]browserprotocol.PushSubscribe) {
		subscriptions[clientID.String()] = subscription
	}); err != nil {
		return mapBrowserError(err)
	}
	return nil
}

// RemoteInvite mints one remote pairing invitation for a paired operator, plus
// its scannable code. The mint is never retried; a failure is reported and the
// operator asks again.
func (backend *browserBackend) RemoteInvite(ctx context.Context, rawClient [browserprotocol.ClientIDSize]byte) (browserprotocol.RemoteInviteResult, error) {
	_, release, client, err := backend.authorize(ctx, rawClient, kernel.BrowserCapabilityHumanActions)
	if err != nil {
		return browserprotocol.RemoteInviteResult{}, err
	}
	defer release()
	// terminal_input is the one bit a remote grant never carries, so requiring
	// it means only a client paired on this machine's own loopback can invite a
	// phone: a remote controller cannot propagate its own pairing.
	if !client.CapabilityMask.Has(kernel.BrowserCapabilityTerminalInput) {
		return browserprotocol.RemoteInviteResult{}, browser.ErrUnauthorized
	}
	if backend.owner == nil {
		return browserprotocol.RemoteInviteResult{}, browser.ErrUnauthorized
	}
	if !backend.admitRemoteInvite() {
		return browserprotocol.RemoteInviteResult{}, browser.ErrRateLimited
	}
	invitation, err := backend.owner.RemotePair(ctx)
	if err != nil {
		return browserprotocol.RemoteInviteResult{}, mapBrowserError(err)
	}
	// The challenge is already committed. A render that fails here leaves one
	// unredeemed challenge, which nobody can reach and which expires on its own.
	code, err := qrSVG(invitation.Link)
	if err != nil {
		return browserprotocol.RemoteInviteResult{}, mapBrowserError(err)
	}
	return browserprotocol.RemoteInviteResult{Link: invitation.Link, ExpiresAtMS: browserprotocol.Decimal(invitation.Expires * 1000), SVG: code}, nil
}

// admitRemoteInvite bounds minting to four invitations per challenge TTL. The
// page asks; it does not choose this bound. Four per TTL means even a console
// looping REMOTE_INVITE holds at most four of the 32 live challenge slots, so
// the loopback /pair page can always still mint one, while an operator
// re-minting after a failed scan never reaches the limit. An admitted attempt
// spends its slot whether or not the mint that follows succeeds.
func (backend *browserBackend) admitRemoteInvite() bool {
	backend.inviteMu.Lock()
	defer backend.inviteMu.Unlock()
	now := backend.now()
	// The slot about to be overwritten holds the oldest of the four.
	if oldest := backend.inviteMints[backend.inviteNext]; !oldest.IsZero() && now.Sub(oldest) < webChallengeTTL {
		return false
	}
	backend.inviteMints[backend.inviteNext] = now
	backend.inviteNext = (backend.inviteNext + 1) % len(backend.inviteMints)
	return true
}

// RunPaths supplies sampled modified directories, not current worker attention. Like Topology it
// is regenerable observation, so it carries no revision and no head.
func (backend *browserBackend) RunPaths(ctx context.Context, rawClient [browserprotocol.ClientIDSize]byte, request browserprotocol.RunPathsGet) (browserprotocol.RunPaths, error) {
	_, release, _, err := backend.authorize(ctx, rawClient, kernel.BrowserCapabilityObserve)
	if err != nil {
		return browserprotocol.RunPaths{}, err
	}
	release() // outside the client's gate, as Topology
	if backend.owner == nil {
		return browserprotocol.RunPaths{}, browser.ErrNotFound
	}
	agentID, err := browserID(request.AgentID, kernel.AgentIDFromBytes)
	if err != nil {
		return browserprotocol.RunPaths{}, browser.ErrStale
	}
	runID, paths, err := backend.owner.RunPaths(ctx, agentID)
	if err != nil {
		return browserprotocol.RunPaths{}, mapBrowserError(err)
	}
	result := browserprotocol.RunPaths{AgentID: request.AgentID, Paths: paths}
	if runID != (kernel.RunID{}) {
		run, found, err := backend.store.Run(ctx, runID)
		if err != nil || !found {
			return browserprotocol.RunPaths{}, browser.ErrStale
		}
		repository, found, err := backend.store.TaskRepository(ctx, run.TaskID)
		if err != nil || !found {
			return browserprotocol.RunPaths{}, browser.ErrStale
		}
		repositories, err := backend.store.ProjectRepositories(ctx, run.ProjectID)
		if err != nil {
			return browserprotocol.RunPaths{}, mapBrowserError(err)
		}
		if len(repositories) > 1 {
			result.Paths = make([]string, len(paths))
			for index, relative := range paths {
				result.Paths[index] = path.Join(repository.ID.String(), relative)
			}
		}
		result.RunID = runID.String()
	}
	return result, nil
}

// DiscoverAccounts reports the provider logins that already exist under the
// operator's home, marked with the account row each one is linked to. It reads
// the login directories' own identity files and never a token value.
func (backend *browserBackend) DiscoverAccounts(ctx context.Context, rawClient [browserprotocol.ClientIDSize]byte, request browserprotocol.AccountsDiscover) (browserprotocol.Accounts, error) {
	_, release, _, err := backend.authorize(ctx, rawClient, kernel.BrowserCapabilityAdministration)
	if err != nil {
		return browserprotocol.Accounts{}, err
	}
	defer release()
	if backend.owner == nil {
		return browserprotocol.Accounts{}, browser.ErrNotFound
	}
	home, err := backend.home()
	if err != nil {
		return browserprotocol.Accounts{}, browser.ErrNotFound
	}
	linked, err := backend.store.ListAccounts(ctx)
	if err != nil {
		return browserprotocol.Accounts{}, mapBrowserError(err)
	}
	found := backend.owner.listedAccounts(home, linked)
	result, err := browserprotocol.PageAccounts(found, request.Offset)
	if err != nil {
		return browserprotocol.Accounts{}, browser.ErrStale
	}
	return result, nil
}

// LinkAccount registers one login the operator can point an agent at. Only a
// directory discovery actually found may be linked: the browser names a login,
// it does not name an arbitrary directory for a provider to read.
func (backend *browserBackend) LinkAccount(ctx context.Context, rawClient [browserprotocol.ClientIDSize]byte, request browserprotocol.AccountLink) (browserprotocol.AccountLinkResult, error) {
	_, release, _, err := backend.authorize(ctx, rawClient, kernel.BrowserCapabilityAdministration)
	if err != nil {
		return browserprotocol.AccountLinkResult{}, err
	}
	defer release()
	if backend.owner == nil {
		return browserprotocol.AccountLinkResult{}, browser.ErrNotFound
	}
	provider, err := kernel.ParseProvider(request.Provider)
	if err != nil {
		return browserprotocol.AccountLinkResult{}, browser.ErrStale
	}
	home, err := backend.home()
	if err != nil {
		return browserprotocol.AccountLinkResult{}, browser.ErrNotFound
	}
	present := false
	for _, candidate := range backend.owner.discoverAccounts(home) {
		if candidate.Provider == request.Provider && candidate.Home == request.Home {
			present = true
			break
		}
	}
	if !present {
		return browserprotocol.AccountLinkResult{}, browser.ErrNotFound
	}
	id, err := backend.randomIdentifier()
	if err != nil {
		return browserprotocol.AccountLinkResult{}, mapBrowserError(err)
	}
	accountID, err := kernel.AccountIDFromBytes(id[:])
	if err != nil {
		return browserprotocol.AccountLinkResult{}, mapBrowserError(err)
	}
	at, err := backend.timestamp()
	if err != nil {
		return browserprotocol.AccountLinkResult{}, mapBrowserError(err)
	}
	account, err := backend.store.LinkAccount(ctx, kernel.NewAccount{ID: accountID, Provider: provider, Home: request.Home, Label: request.Label}, at)
	if err != nil {
		return browserprotocol.AccountLinkResult{}, consoleUpdateError(err)
	}
	return browserprotocol.AccountLinkResult{AccountID: account.ID.String(), Revision: decimalRevision(account.Revision)}, nil
}

// ListBrowserClients projects every identity this factory has granted and
// not revoked, newest first, bounded to what one frame carries. Nothing here
// is a key or a fingerprint.
func (backend *browserBackend) ListBrowserClients(ctx context.Context, rawClient [browserprotocol.ClientIDSize]byte) (browserprotocol.BrowserClients, error) {
	_, release, _, err := backend.authorize(ctx, rawClient, kernel.BrowserCapabilityAdministration)
	if err != nil {
		return browserprotocol.BrowserClients{}, err
	}
	defer release()
	var active []kernel.BrowserClientSummary
	var after *kernel.BrowserClientID
	for {
		page, err := backend.store.ListBrowserClients(ctx, after)
		if err != nil {
			return browserprotocol.BrowserClients{}, mapBrowserError(err)
		}
		for _, item := range page.Items {
			if item.RevokedAt == nil {
				active = append(active, item)
			}
		}
		if page.NextAfter == nil {
			break
		}
		after = page.NextAfter
	}
	sort.Slice(active, func(i, j int) bool { return active[i].CreatedAt.Int64() > active[j].CreatedAt.Int64() })
	result := browserprotocol.BrowserClients{Clients: []browserprotocol.BrowserClientItem{}}
	for _, item := range active {
		if len(result.Clients) == browserprotocol.MaxJSONArray {
			result.More = true
			break
		}
		result.Clients = append(result.Clients, browserprotocol.BrowserClientItem{
			ClientID:     item.ID.String(),
			Capabilities: browserprotocol.Capabilities(item.CapabilityMask),
			Revision:     decimalRevision(item.Revision),
			CreatedAtMS:  browserprotocol.Decimal(item.CreatedAt.Int64()),
		})
	}
	return result, nil
}

// RevokeBrowserClient withdraws another identity through the daemon's own
// revocation, which also ends that identity's live sessions. An identity
// never revokes itself: the console it drives would vanish under it.
func (backend *browserBackend) RevokeBrowserClient(ctx context.Context, rawClient [browserprotocol.ClientIDSize]byte, request browserprotocol.BrowserClientRevoke) (browserprotocol.BrowserClientRevokeResult, error) {
	self, release, _, err := backend.authorize(ctx, rawClient, kernel.BrowserCapabilityAdministration)
	if err != nil {
		return browserprotocol.BrowserClientRevokeResult{}, err
	}
	defer release()
	id, err := browserID(request.ClientID, kernel.BrowserClientIDFromBytes)
	if err != nil || id == self {
		return browserprotocol.BrowserClientRevokeResult{}, browser.ErrInvalidRequest
	}
	expected, err := kernel.NewRevision(int64(request.ExpectedRevision))
	if err != nil {
		return browserprotocol.BrowserClientRevokeResult{}, browser.ErrStale
	}
	if backend.owner == nil {
		return browserprotocol.BrowserClientRevokeResult{}, browser.ErrUnauthorized
	}
	client, err := backend.owner.RevokeBrowserClient(ctx, id, expected)
	if err != nil {
		return browserprotocol.BrowserClientRevokeResult{}, consoleUpdateError(err)
	}
	return browserprotocol.BrowserClientRevokeResult{ClientID: client.ID.String(), Revision: decimalRevision(client.Revision)}, nil
}

func (backend *browserBackend) UpdateAccount(ctx context.Context, rawClient [browserprotocol.ClientIDSize]byte, request browserprotocol.AccountUpdate) (browserprotocol.AccountUpdateResult, error) {
	_, release, _, err := backend.authorize(ctx, rawClient, kernel.BrowserCapabilityAdministration)
	if err != nil {
		return browserprotocol.AccountUpdateResult{}, err
	}
	defer release()
	id, err := browserID(request.AccountID, kernel.AccountIDFromBytes)
	if err != nil {
		return browserprotocol.AccountUpdateResult{}, browser.ErrStale
	}
	expected, err := kernel.NewRevision(int64(request.ExpectedRevision))
	if err != nil {
		return browserprotocol.AccountUpdateResult{}, browser.ErrStale
	}
	at, err := backend.timestamp()
	if err != nil {
		return browserprotocol.AccountUpdateResult{}, mapBrowserError(err)
	}
	account, err := backend.store.UpdateAccount(ctx, id, expected, request.Label, request.Remove != nil && bool(*request.Remove), at)
	if err != nil {
		return browserprotocol.AccountUpdateResult{}, consoleUpdateError(err)
	}
	return browserprotocol.AccountUpdateResult{AccountID: account.ID.String(), Revision: decimalRevision(account.Revision)}, nil
}

func (backend *browserBackend) authorize(ctx context.Context, rawID [browserprotocol.ClientIDSize]byte, capability kernel.BrowserCapabilityMask) (kernel.BrowserClientID, func(), kernel.BrowserClient, error) {
	clientID, err := kernel.BrowserClientIDFromBytes(rawID[:])
	if err != nil {
		return kernel.BrowserClientID{}, nil, kernel.BrowserClient{}, browser.ErrUnauthorized
	}
	release, err := backend.acquireClient(ctx, clientID)
	if err != nil {
		return kernel.BrowserClientID{}, nil, kernel.BrowserClient{}, mapBrowserError(err)
	}
	client, found, err := backend.store.BrowserClient(ctx, clientID)
	if err != nil || !found || client.RevokedAt != nil || !client.CapabilityMask.Has(capability) {
		release()
		if err != nil {
			return kernel.BrowserClientID{}, nil, kernel.BrowserClient{}, mapBrowserError(err)
		}
		return kernel.BrowserClientID{}, nil, kernel.BrowserClient{}, browser.ErrUnauthorized
	}
	return clientID, release, client, nil
}

func (backend *browserBackend) acquireClient(ctx context.Context, clientID kernel.BrowserClientID) (func(), error) {
	if backend == nil || backend.clientGates == nil {
		return nil, browser.ErrUnauthorized
	}
	return backend.clientGates.acquire(ctx, clientID)
}

func (gates *browserClientGates) acquire(ctx context.Context, clientID kernel.BrowserClientID) (func(), error) {
	if gates == nil {
		return nil, browser.ErrUnauthorized
	}
	gates.mu.Lock()
	if gates.gates == nil {
		gates.gates = make(map[kernel.BrowserClientID]*browserClientGate)
	}
	gate := gates.gates[clientID]
	if gate == nil {
		gate = &browserClientGate{slot: make(chan struct{}, 1)}
		gate.slot <- struct{}{}
		gates.gates[clientID] = gate
	}
	gate.users++
	gates.mu.Unlock()
	select {
	case <-gate.slot:
		return func() {
			gate.slot <- struct{}{}
			gates.release(clientID, gate)
		}, nil
	case <-ctx.Done():
		gates.release(clientID, gate)
		return nil, ctx.Err()
	}
}

func (gates *browserClientGates) release(clientID kernel.BrowserClientID, gate *browserClientGate) {
	gates.mu.Lock()
	gate.users--
	if gate.users == 0 && gates.gates[clientID] == gate {
		delete(gates.gates, clientID)
	}
	gates.mu.Unlock()
}

func (backend *browserBackend) randomIdentifier() ([kernel.IDBytes]byte, error) {
	backend.randomMu.Lock()
	defer backend.randomMu.Unlock()
	var result [kernel.IDBytes]byte
	if _, err := io.ReadFull(backend.random, result[:]); err != nil {
		return result, fmt.Errorf("browser random identity: %w", err)
	}
	for _, value := range result {
		if value != 0 {
			return result, nil
		}
	}
	return result, fmt.Errorf("browser random identity is zero")
}

func (backend *browserBackend) timestamp() (kernel.UnixMillis, error) {
	return kernel.NewUnixMillis(backend.now().UnixMilli())
}

func projectBrowserAuthentication(client kernel.BrowserClient) (browser.Authentication, error) {
	capabilities := browserprotocol.CapabilityObserve
	if client.CapabilityMask&^kernel.BrowserCapabilityKnownMask != 0 || !client.CapabilityMask.Has(kernel.BrowserCapabilityObserve) || client.RevokedAt != nil {
		return browser.Authentication{}, browser.ErrUnauthorized
	}
	if client.CapabilityMask.Has(kernel.BrowserCapabilityPrivateHumanRequestDetail) {
		capabilities |= browserprotocol.CapabilityPrivateHumanRequestDetail
	}
	if client.CapabilityMask.Has(kernel.BrowserCapabilityHumanActions) {
		capabilities |= browserprotocol.CapabilityHumanActions
	}
	if client.CapabilityMask.Has(kernel.BrowserCapabilityTerminalInput) {
		capabilities |= browserprotocol.CapabilityTerminalInput
	}
	if client.CapabilityMask.Has(kernel.BrowserCapabilityAdministration) {
		capabilities |= browserprotocol.CapabilityAdministration
	}
	var principal browser.Principal
	copy(principal.ClientID[:], client.ID.Bytes())
	return browser.Authentication{Principal: principal, Capabilities: capabilities}, nil
}

// projectPublicSnapshot is the one positive-allowlist conversion from the
// kernel public snapshot to the wire. Nothing private is reachable from here.
func projectPublicSnapshot(snapshot kernel.PublicSnapshot, providerDefaults func(string, string) (string, string, string)) (browserprotocol.StateSnapshot, error) {
	return projectPublicSnapshotForClient(snapshot, providerDefaults, true)
}

func projectPublicSnapshotForClient(snapshot kernel.PublicSnapshot, providerDefaults func(string, string) (string, string, string), administration bool) (browserprotocol.StateSnapshot, error) {
	result := browserprotocol.StateSnapshot{
		Head:          decimalSequence(snapshot.Head),
		Factory:       projectFactory(snapshot.Factory),
		Projects:      make([]browserprotocol.ProjectItem, 0, len(snapshot.Projects)),
		Agents:        make([]browserprotocol.AgentItem, 0, len(snapshot.Agents)),
		Tasks:         make([]browserprotocol.TaskItem, 0, len(snapshot.Tasks)),
		HumanRequests: make([]browserprotocol.HumanRequestItem, 0, len(snapshot.HumanRequests)),
		Accounts:      make([]browserprotocol.AccountItem, 0, len(snapshot.Accounts)),
	}
	for _, item := range snapshot.Projects {
		result.Projects = append(result.Projects, projectProject(item))
	}
	// An agent's defaults are read from the account it launches under, so the
	// snapshot's own account rows resolve the directory before the agents do.
	homes := make(map[kernel.AccountID]string, len(snapshot.Accounts))
	for _, item := range snapshot.Accounts {
		homes[item.ID] = item.Home
	}
	for _, item := range snapshot.Agents {
		result.Agents = append(result.Agents, projectAgentForClient(item, homes[item.AccountID], providerDefaults, administration))
	}
	for _, item := range snapshot.Tasks {
		if item.AssignedAgentID == (kernel.AgentID{}) {
			result.SharedTasks = append(result.SharedTasks, projectTask(item))
			continue
		}
		result.Tasks = append(result.Tasks, projectTask(item))
	}
	for _, item := range snapshot.PeerQuestions {
		result.PeerQuestions = append(result.PeerQuestions, browserprotocol.PeerQuestionItem{ID: item.ID.String(), SourceTaskID: item.SourceTaskID.String(), TargetTaskID: item.TargetTaskID.String(), Answered: browserprotocol.Bool(item.Answered), Revision: decimalRevision(item.Revision)})
	}
	for _, item := range snapshot.HumanRequests {
		projected, err := projectHumanRequest(item)
		if err != nil {
			return browserprotocol.StateSnapshot{}, err
		}
		result.HumanRequests = append(result.HumanRequests, projected)
	}
	for _, item := range snapshot.Accounts {
		home := item.Home
		if !administration {
			home = ""
		}
		result.Accounts = append(result.Accounts, browserprotocol.AccountItem{ID: item.ID.String(), Provider: item.Provider, Home: home, Label: item.Label, Revision: decimalRevision(item.Revision)})
	}
	return result, nil
}

func projectFactory(item kernel.FactorySummary) browserprotocol.FactoryItem {
	return browserprotocol.FactoryItem{DispatchEnabled: browserprotocol.Bool(item.DispatchEnabled), Capacity: item.Capacity, ActiveRuns: item.ActiveRuns, Revision: decimalRevision(item.Revision)}
}

func projectProject(item kernel.ProjectSummary) browserprotocol.ProjectItem {
	return browserprotocol.ProjectItem{ID: item.ID.String(), Name: item.Name, RunBudgetLimit: browserprotocol.Decimal(item.RunBudgetLimit), RunsUsed: browserprotocol.Decimal(item.RunsUsed), MaxRunSeconds: item.MaxRunSeconds, Revision: decimalRevision(item.Revision)}
}

// projectAgent resolves what the agent will actually run with. An agent that
// names no model is launched without one and the provider CLI picks its own,
// so the console is served that CLI's configured default and the file it came
// from rather than a blank the operator cannot interpret. configHome is the
// account's own directory when the agent selects one, so the default shown is
// the one that account will actually launch with; empty means the operator's
// own login, which is what the daemon falls back to.
func projectAgent(item kernel.AgentSummary, configHome string, providerDefaults func(string, string) (string, string, string)) browserprotocol.AgentItem {
	return projectAgentForClient(item, configHome, providerDefaults, true)
}

func projectAgentForClient(item kernel.AgentSummary, configHome string, providerDefaults func(string, string) (string, string, string), administration bool) browserprotocol.AgentItem {
	defaultModel, defaultEffort, source := providerDefaults(item.Provider, configHome)
	effectiveModel, effectiveEffort := item.Model, item.ReasoningEffort
	if effectiveModel == "" {
		effectiveModel = defaultModel
	}
	if effectiveEffort == "" {
		effectiveEffort = defaultEffort
	}
	if item.Model != "" {
		source = "agent"
	}
	if !administration {
		source = ""
	}
	projected := browserprotocol.AgentItem{ID: item.ID.String(), ProjectID: item.ProjectID.String(), Name: item.Name, Role: item.Role, Provider: item.Provider, Paused: browserprotocol.Bool(item.Paused), Archived: browserprotocol.Bool(item.Archived), Appearance: browserprotocol.SpriteAppearance{Automatic: browserprotocol.Bool(item.Appearance.Automatic), Skin: item.Appearance.Skin, Hair: item.Appearance.Hair, HairColour: item.Appearance.HairColour, Face: item.Appearance.Face, Outfit: item.Appearance.Outfit, ClothesColour: item.Appearance.ClothesColour, Shoes: item.Appearance.Shoes, Tool: item.Appearance.Tool, Headwear: item.Appearance.Headwear}, Model: item.Model, ReasoningEffort: item.ReasoningEffort, EffectiveModel: effectiveModel, EffectiveReasoningEffort: effectiveEffort, ModelSource: source, Revision: decimalRevision(item.Revision),
		IdlePolicy: string(item.Idle.Policy), IdleAfterSeconds: item.Idle.AfterSeconds, IdleInstruction: item.Idle.Instruction, IdleRunBudget: item.Idle.RunBudget, IdleRunsUsed: item.Idle.RunsUsed}
	if (item.AccountID != kernel.AccountID{}) {
		projected.AccountID = item.AccountID.String()
	}
	return projected
}

func projectTask(item kernel.TaskSummary) browserprotocol.TaskItem {
	return browserprotocol.TaskItem{ID: item.ID.String(), ProjectID: item.ProjectID.String(), AssignedAgentID: optionalAgentText(item.AssignedAgentID), Title: item.Title, Status: item.Status, BlockedReason: item.BlockedReason, Priority: item.Priority, Revision: decimalRevision(item.Revision), UpdatedAtMillis: decimalMillis(item.UpdatedAt)}
}

func projectHumanRequest(item kernel.HumanRequestProjection) (browserprotocol.HumanRequestItem, error) {
	if item.ReplyMaxBytes > math.MaxUint16 {
		return browserprotocol.HumanRequestItem{}, fmt.Errorf("human-request reply bound exceeds wire")
	}
	return browserprotocol.HumanRequestItem{
		ID: item.ID.String(), ProjectID: item.ProjectID.String(), AgentID: item.AgentID.String(), TaskID: item.TaskID.String(),
		CreatedAt: decimalMillis(item.CreatedAt), UpdatedAt: decimalMillis(item.UpdatedAt), Revision: decimalRevision(item.Revision),
		Kind: item.Kind.String(), Status: item.Status.String(), ReplyMaxBytes: uint16(item.ReplyMaxBytes),
		CanReply: browserprotocol.Bool(item.CanReply),
	}, nil
}

func decimalSequence(value kernel.EventSequence) browserprotocol.Decimal {
	return browserprotocol.Decimal(value.Int64())
}
func decimalRevision(value kernel.Revision) browserprotocol.Decimal {
	return browserprotocol.Decimal(value.Int64())
}
func decimalMillis(value kernel.UnixMillis) browserprotocol.Decimal {
	return browserprotocol.Decimal(value.Int64())
}

func mapBrowserError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// An owner-side effect that already reached a verdict keeps it. Its
		// cause often carries the deadline that produced it, but "the effect
		// may already have landed" is never retryable busyness: retrying would
		// attempt it a second time. remoteErrorCode fences OutcomeUnknownError
		// ahead of its own context arm for the same reason.
		if terminalEffectVerdict(err) {
			return err
		}
		// Otherwise the caller gave up, or its budget expired before anything
		// was attempted. That is retryable busyness, not a fault: the same
		// request converges when it is made again with a budget it fits in.
		return browser.ErrRateLimited
	case errors.Is(err, kernel.ErrStoreClosed):
		// A connected browser's state watch can observe the store closing
		// during the bounded daemon handoff; that plain condition is lifecycle
		// busyness, not a permanent internal fault, so it is retryable. But a
		// human-reply acknowledgement or lease renewal that raced a closed
		// Store joins this same kernel.ErrStoreClosed with its own
		// terminal/uncertain effect marker (terminal_effects.go); that owner
		// verdict must survive, exactly as the context arm above preserves
		// its own. Unlike that arm's cause, a revision conflict or other
		// case below is never joined with ErrStoreClosed, so this guard is
		// scoped to this case alone and leaves every other case's ordering
		// untouched (see TestReleaseAfterCancelIsStaleNotInternal).
		if terminalEffectVerdict(err) {
			return err
		}
		return browser.ErrRateLimited
	case errors.Is(err, kernel.ErrUnauthorized):
		return browser.ErrUnauthorized
	case errors.Is(err, kernel.ErrNotFound):
		return browser.ErrNotFound
	case errors.Is(err, kernel.ErrRevisionConflict), errors.Is(err, kernel.ErrConflict), errors.Is(err, kernel.ErrInvalidValue):
		return browser.ErrStale
	case errors.Is(err, kernel.ErrSnapshotTooLarge):
		return browser.ErrTooLarge
	case errors.Is(err, kernel.ErrBusy):
		return browser.ErrRateLimited
	case errors.Is(err, ErrTerminalNotReady):
		// The owner has not yet consumed the runner's ready frame for a session
		// the Store already shows active. Retryable busyness, not an internal
		// failure: the client retries the same target and converges.
		return browser.ErrRateLimited
	default:
		return err
	}
}

func (backend *browserBackend) AttachmentRetention(ctx context.Context, rawClient [browserprotocol.ClientIDSize]byte, request browserprotocol.AttachmentRetention) (browserprotocol.AttachmentRetentionResult, error) {
	_, release, _, err := backend.authorize(ctx, rawClient, kernel.BrowserCapabilityAdministration)
	if err != nil {
		return browserprotocol.AttachmentRetentionResult{}, err
	}
	defer release()
	var enabled *bool
	if request.Enabled != nil {
		value := bool(*request.Enabled)
		enabled = &value
	}
	value, err := backend.store.AttachmentRetention(ctx, enabled)
	return browserprotocol.AttachmentRetentionResult{Enabled: browserprotocol.Bool(value)}, mapBrowserError(err)
}
