package daemon

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

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

	subMu       sync.Mutex
	closing     bool
	subs        map[*browserStateSubscription]struct{}
	stateSignal chan struct{}
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
		store: store, now: now, random: random,
		clientGates: &browserClientGates{gates: make(map[kernel.BrowserClientID]*browserClientGate)},
		subs:        make(map[*browserStateSubscription]struct{}), stateSignal: make(chan struct{}),
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

func (backend *browserBackend) StatePage(ctx context.Context, rawClient [browserprotocol.ClientIDSize]byte, cursor *browser.Cursor) (browser.StatePage, error) {
	_, release, _, err := backend.authorize(ctx, rawClient, kernel.BrowserCapabilityObserve)
	if err != nil {
		return browser.StatePage{}, err
	}
	defer release()
	kernelCursor, err := parsePublicCursor(cursor)
	if err != nil {
		return browser.StatePage{}, browser.ErrStale
	}
	page, err := backend.store.ReadPublicStatePage(ctx, kernelCursor)
	if err != nil {
		var restart *kernel.PublicStateRestartError
		if errors.As(err, &restart) {
			return browser.StatePage{}, &browser.RestartError{State: browserprotocol.StateRestart{
				Head: decimalSequence(restart.Head), Floor: decimalSequence(restart.Floor), Reason: browserprotocol.RestartHeadChanged,
			}}
		}
		return browser.StatePage{}, mapBrowserError(err)
	}
	return projectPublicPage(page)
}

func (backend *browserBackend) StateEntity(ctx context.Context, rawClient [browserprotocol.ClientIDSize]byte, request browserprotocol.StateEntityGet) (browserprotocol.StateEntity, error) {
	_, release, _, err := backend.authorize(ctx, rawClient, kernel.BrowserCapabilityObserve)
	if err != nil {
		return browserprotocol.StateEntity{}, err
	}
	defer release()
	kind, err := parsePublicKind(request.Kind)
	if err != nil {
		return browserprotocol.StateEntity{}, browser.ErrStale
	}
	id, err := parsePublicStateID(kind, request.ID)
	if err != nil {
		return browserprotocol.StateEntity{}, browser.ErrStale
	}
	entity, err := backend.store.ReadPublicStateEntity(ctx, kind, id)
	if err != nil {
		return browserprotocol.StateEntity{}, mapBrowserError(err)
	}
	result := browserprotocol.StateEntity{
		Head: decimalSequence(entity.Head), Kind: request.Kind, ID: request.ID,
		Revision: decimalRevision(entity.Revision), Deleted: browserprotocol.Bool(entity.Deleted),
	}
	if entity.Deleted {
		if entity.Item != nil {
			return browserprotocol.StateEntity{}, fmt.Errorf("browser entity has item and tombstone")
		}
		result.Item = browserprotocol.DeletedStateItem()
		return result, nil
	}
	if entity.Item == nil {
		return browserprotocol.StateEntity{}, fmt.Errorf("browser entity omitted item")
	}
	item, err := projectPublicEntityItem(entity.Kind, *entity.Item)
	if err != nil {
		return browserprotocol.StateEntity{}, err
	}
	result.Item = item
	return result, nil
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
	return browserprotocol.HumanRequestDetail{RequestID: detail.ID.String(), Revision: decimalRevision(detail.Revision), Question: detail.QuestionText}, nil
}

func (backend *browserBackend) authorize(ctx context.Context, rawID [browserprotocol.ClientIDSize]byte, capability kernel.BrowserCapabilityMask) (kernel.BrowserClientID, func(), kernel.BrowserClient, error) {
	clientID, err := kernel.BrowserClientIDFromBytes(rawID[:])
	if err != nil {
		return kernel.BrowserClientID{}, nil, kernel.BrowserClient{}, browser.ErrUnauthorized
	}
	release, err := backend.acquireClient(ctx, clientID)
	if err != nil {
		return kernel.BrowserClientID{}, nil, kernel.BrowserClient{}, err
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
	if !client.CapabilityMask.Has(kernel.BrowserCapabilityObserve) || client.RevokedAt != nil {
		return browser.Authentication{}, browser.ErrUnauthorized
	}
	if client.CapabilityMask.Has(kernel.BrowserCapabilityPrivateHumanRequestDetail) {
		capabilities |= browserprotocol.CapabilityPrivateHumanRequestDetail
	}
	var principal browser.Principal
	copy(principal.ClientID[:], client.ID.Bytes())
	return browser.Authentication{Principal: principal, Capabilities: capabilities}, nil
}

func parsePublicCursor(cursor *browser.Cursor) (*kernel.PublicStateCursor, error) {
	if cursor == nil {
		return nil, nil
	}
	if uint64(cursor.Head) > math.MaxInt64 {
		return nil, kernel.ErrInvalidValue
	}
	head, err := kernel.NewEventSequence(int64(cursor.Head))
	if err != nil {
		return nil, err
	}
	kind, err := parsePublicKind(cursor.Kind)
	if err != nil {
		return nil, err
	}
	result := &kernel.PublicStateCursor{Head: head, Kind: kind}
	if cursor.HasAfter {
		id, err := kernel.PublicStateIDFromBytes(cursor.AfterID[:])
		if err != nil {
			return nil, err
		}
		result.AfterID = &id
	}
	return result, nil
}

func parsePublicKind(kind browserprotocol.StateKind) (kernel.PublicStateKind, error) {
	switch kind {
	case browserprotocol.StateFactory:
		return kernel.PublicStateFactory, nil
	case browserprotocol.StateProject:
		return kernel.PublicStateProject, nil
	case browserprotocol.StateAgent:
		return kernel.PublicStateAgent, nil
	case browserprotocol.StateTask:
		return kernel.PublicStateTask, nil
	case browserprotocol.StateHumanRequest:
		return kernel.PublicStateHumanRequest, nil
	default:
		return 0, kernel.ErrInvalidValue
	}
}

func projectPublicKind(kind kernel.PublicStateKind) (browserprotocol.StateKind, error) {
	switch kind {
	case kernel.PublicStateFactory:
		return browserprotocol.StateFactory, nil
	case kernel.PublicStateProject:
		return browserprotocol.StateProject, nil
	case kernel.PublicStateAgent:
		return browserprotocol.StateAgent, nil
	case kernel.PublicStateTask:
		return browserprotocol.StateTask, nil
	case kernel.PublicStateHumanRequest:
		return browserprotocol.StateHumanRequest, nil
	default:
		return "", fmt.Errorf("unknown kernel public state kind")
	}
}

func parsePublicStateID(kind kernel.PublicStateKind, encoded string) (kernel.PublicStateID, error) {
	if kind == kernel.PublicStateFactory {
		if encoded != "factory" {
			return kernel.PublicStateID{}, kernel.ErrInvalidValue
		}
		return kernel.FactoryPublicStateID(), nil
	}
	raw, err := parseID(encoded)
	if err != nil {
		return kernel.PublicStateID{}, err
	}
	return kernel.PublicStateIDFromBytes(raw)
}

func projectPublicPage(page kernel.PublicStatePageResult) (browser.StatePage, error) {
	kind, err := projectPublicKind(page.Kind)
	if err != nil {
		return browser.StatePage{}, err
	}
	items, err := projectPublicItems(page.Kind, page.Items)
	if err != nil {
		return browser.StatePage{}, err
	}
	result := browser.StatePage{Head: decimalSequence(page.Head), Kind: kind, Items: items}
	if page.NextCursor != nil {
		nextKind, err := projectPublicKind(page.NextCursor.Kind)
		if err != nil {
			return browser.StatePage{}, err
		}
		next := browser.Cursor{Head: decimalSequence(page.NextCursor.Head), Kind: nextKind}
		if page.NextCursor.AfterID != nil {
			raw := page.NextCursor.AfterID.Bytes()
			if len(raw) != browserprotocol.ClientIDSize {
				return browser.StatePage{}, fmt.Errorf("invalid public continuation identity")
			}
			copy(next.AfterID[:], raw)
			next.HasAfter = true
		}
		result.NextCursor = &next
	}
	return result, nil
}

func projectPublicItems(kind kernel.PublicStateKind, source []kernel.PublicStateItem) (browserprotocol.StateItems, error) {
	switch kind {
	case kernel.PublicStateFactory:
		items := make([]browserprotocol.FactoryItem, 0, len(source))
		for _, sourceItem := range source {
			item, ok := sourceItem.Factory()
			if !ok {
				return browserprotocol.StateItems{}, fmt.Errorf("mismatched factory state item")
			}
			items = append(items, projectFactory(item))
		}
		return browserprotocol.FactoryItems(items), nil
	case kernel.PublicStateProject:
		items := make([]browserprotocol.ProjectItem, 0, len(source))
		for _, sourceItem := range source {
			item, ok := sourceItem.Project()
			if !ok {
				return browserprotocol.StateItems{}, fmt.Errorf("mismatched project state item")
			}
			items = append(items, projectProject(item))
		}
		return browserprotocol.ProjectItems(items), nil
	case kernel.PublicStateAgent:
		items := make([]browserprotocol.AgentItem, 0, len(source))
		for _, sourceItem := range source {
			item, ok := sourceItem.Agent()
			if !ok {
				return browserprotocol.StateItems{}, fmt.Errorf("mismatched agent state item")
			}
			items = append(items, projectAgent(item))
		}
		return browserprotocol.AgentItems(items), nil
	case kernel.PublicStateTask:
		items := make([]browserprotocol.TaskItem, 0, len(source))
		for _, sourceItem := range source {
			item, ok := sourceItem.Task()
			if !ok {
				return browserprotocol.StateItems{}, fmt.Errorf("mismatched task state item")
			}
			items = append(items, projectTask(item))
		}
		return browserprotocol.TaskItems(items), nil
	case kernel.PublicStateHumanRequest:
		items := make([]browserprotocol.HumanRequestItem, 0, len(source))
		for _, sourceItem := range source {
			item, ok := sourceItem.HumanRequest()
			if !ok {
				return browserprotocol.StateItems{}, fmt.Errorf("mismatched human-request state item")
			}
			projected, err := projectHumanRequest(item)
			if err != nil {
				return browserprotocol.StateItems{}, err
			}
			items = append(items, projected)
		}
		return browserprotocol.HumanRequestItems(items), nil
	default:
		return browserprotocol.StateItems{}, fmt.Errorf("unknown public state item kind")
	}
}

func projectPublicEntityItem(kind kernel.PublicStateKind, source kernel.PublicStateItem) (browserprotocol.StateItem, error) {
	switch kind {
	case kernel.PublicStateFactory:
		item, ok := source.Factory()
		if !ok {
			return browserprotocol.StateItem{}, fmt.Errorf("mismatched factory entity")
		}
		return browserprotocol.FactoryStateItem(projectFactory(item)), nil
	case kernel.PublicStateProject:
		item, ok := source.Project()
		if !ok {
			return browserprotocol.StateItem{}, fmt.Errorf("mismatched project entity")
		}
		return browserprotocol.ProjectStateItem(projectProject(item)), nil
	case kernel.PublicStateAgent:
		item, ok := source.Agent()
		if !ok {
			return browserprotocol.StateItem{}, fmt.Errorf("mismatched agent entity")
		}
		return browserprotocol.AgentStateItem(projectAgent(item)), nil
	case kernel.PublicStateTask:
		item, ok := source.Task()
		if !ok {
			return browserprotocol.StateItem{}, fmt.Errorf("mismatched task entity")
		}
		return browserprotocol.TaskStateItem(projectTask(item)), nil
	case kernel.PublicStateHumanRequest:
		item, ok := source.HumanRequest()
		if !ok {
			return browserprotocol.StateItem{}, fmt.Errorf("mismatched human-request entity")
		}
		projected, err := projectHumanRequest(item)
		if err != nil {
			return browserprotocol.StateItem{}, err
		}
		return browserprotocol.HumanRequestStateItem(projected), nil
	default:
		return browserprotocol.StateItem{}, fmt.Errorf("unknown public state entity kind")
	}
}

func projectFactory(item kernel.FactorySummary) browserprotocol.FactoryItem {
	return browserprotocol.FactoryItem{DispatchEnabled: browserprotocol.Bool(item.DispatchEnabled), Capacity: item.Capacity, ActiveRuns: item.ActiveRuns, Revision: decimalRevision(item.Revision)}
}

func projectProject(item kernel.ProjectSummary) browserprotocol.ProjectItem {
	return browserprotocol.ProjectItem{ID: item.ID.String(), Name: item.Name, Revision: decimalRevision(item.Revision)}
}

func projectAgent(item kernel.AgentSummary) browserprotocol.AgentItem {
	return browserprotocol.AgentItem{ID: item.ID.String(), ProjectID: item.ProjectID.String(), Name: item.Name, Role: item.Role, Paused: browserprotocol.Bool(item.Paused), Revision: decimalRevision(item.Revision)}
}

func projectTask(item kernel.TaskSummary) browserprotocol.TaskItem {
	return browserprotocol.TaskItem{ID: item.ID.String(), ProjectID: item.ProjectID.String(), AssignedAgentID: item.AssignedAgentID.String(), Title: item.Title, Status: item.Status, Priority: item.Priority, Revision: decimalRevision(item.Revision)}
}

func projectHumanRequest(item kernel.HumanRequestProjection) (browserprotocol.HumanRequestItem, error) {
	if item.ReplyMaxBytes > math.MaxUint16 {
		return browserprotocol.HumanRequestItem{}, fmt.Errorf("human-request reply bound exceeds wire")
	}
	return browserprotocol.HumanRequestItem{
		ID: item.ID.String(), ProjectID: item.ProjectID.String(), AgentID: item.AgentID.String(), TaskID: item.TaskID.String(), RunID: item.RunID.String(),
		CreatedAt: decimalMillis(item.CreatedAt), UpdatedAt: decimalMillis(item.UpdatedAt), Revision: decimalRevision(item.Revision),
		Kind: item.Kind.String(), Status: item.Status.String(), ReplyMaxBytes: uint16(item.ReplyMaxBytes),
		CanReply: browserprotocol.Bool(item.CanReply), CanOpenTerminal: browserprotocol.Bool(item.CanOpenTerminal),
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
	case errors.Is(err, kernel.ErrUnauthorized):
		return browser.ErrUnauthorized
	case errors.Is(err, kernel.ErrNotFound):
		return browser.ErrNotFound
	case errors.Is(err, kernel.ErrRevisionConflict), errors.Is(err, kernel.ErrConflict), errors.Is(err, kernel.ErrFutureCursor), errors.Is(err, kernel.ErrInvalidValue):
		return browser.ErrStale
	case errors.Is(err, kernel.ErrSnapshotTooLarge):
		return browser.ErrTooLarge
	case errors.Is(err, kernel.ErrBusy):
		return browser.ErrRateLimited
	default:
		return err
	}
}
