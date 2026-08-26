package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// maxRunWakeEntries bounds the in-memory notification index. Notifications
// are hints only: the Store remains the authority and a missing or dropped
// hint cannot change any durable decision.
const maxRunWakeEntries = 1024

// Daemon is the concrete composition root for the local API. It owns the
// durable Store and the short-lived supervisor wake hints. It does not own an
// accept loop; the caller accepts and hands one connection to
// HandleConnection.
type Daemon struct {
	store *kernel.Store
	now   func() time.Time

	wakeMu sync.Mutex
	wakes  map[kernel.RunID]chan struct{}
}

// NewDaemon creates an API composition root using the wall clock for durable
// timestamps. The Store is never replaced or wrapped by the daemon.
func NewDaemon(store *kernel.Store) (*Daemon, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: nil kernel store", kernel.ErrInvalidValue)
	}
	return &Daemon{store: store, now: time.Now, wakes: make(map[kernel.RunID]chan struct{})}, nil
}

func newDaemon(store *kernel.Store, now func() time.Time) (*Daemon, error) {
	if store == nil || now == nil {
		return nil, fmt.Errorf("%w: invalid daemon", kernel.ErrInvalidValue)
	}
	return &Daemon{store: store, now: now, wakes: make(map[kernel.RunID]chan struct{})}, nil
}

// HandleConnection synchronously consumes exactly one authenticated request,
// dispatches it, writes exactly one response, and closes the connection. The
// API transport has already authenticated the domain and credential before a
// Call is returned; this method does not add an alternate auth path.
func (daemon *Daemon) HandleConnection(ctx context.Context, connection *api.Connection) error {
	if daemon == nil || daemon.store == nil || connection == nil {
		return fmt.Errorf("%w: invalid daemon connection", kernel.ErrInvalidValue)
	}
	defer connection.Close()
	call, err := connection.Receive(ctx)
	if err != nil {
		return err
	}
	return connection.Respond(daemon.dispatch(ctx, call))
}

// dispatch is intentionally one closed switch. API validation belongs to the
// transport and domain validation belongs to kernel constructors/Store
// methods; there is no forwarding service layer here.
func (daemon *Daemon) dispatch(ctx context.Context, call api.Call) api.Reply {
	switch call.Kind() {
	case api.CallHealth:
		return daemon.health(ctx)
	case api.CallSnapshot:
		return daemon.snapshot(ctx)
	case api.CallCreateProject:
		return daemon.createProject(ctx, call)
	case api.CallCreateShellAgent:
		return daemon.createShellAgent(ctx, call)
	case api.CallEnqueueTask:
		return daemon.enqueueTask(ctx, call)
	case api.CallSetDispatch:
		return daemon.setDispatch(ctx, call)
	case api.CallSucceed, api.CallBlock, api.CallFail:
		return daemon.proposeOutcome(ctx, call)
	default:
		return newErrorReply(api.RemoteInvalidRequest)
	}
}

func (daemon *Daemon) health(ctx context.Context) api.Reply {
	if _, err := daemon.store.Factory(ctx); err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	return api.NewHealthReply(api.HealthStatus{Ready: true})
}

func (daemon *Daemon) snapshot(ctx context.Context) api.Reply {
	snapshot, err := daemon.store.Snapshot(ctx)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	public := projectSnapshot(snapshot)
	reply, err := api.NewSnapshotReply(public)
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	return reply
}

func (daemon *Daemon) createProject(ctx context.Context, call api.Call) api.Reply {
	input, ok := call.CreateProjectInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	id, err := parseProjectID(input.ID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	at, err := daemon.timestamp()
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	project, err := daemon.store.CreateProject(ctx, kernel.NewProject{
		ID: id, Name: input.Name, Root: input.Root, VerificationPolicy: kernel.VerificationNone,
	}, at)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	return daemon.mutation(ctx, project.Revision)
}

func (daemon *Daemon) createShellAgent(ctx context.Context, call api.Call) api.Reply {
	input, ok := call.CreateShellAgentInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	id, err := parseAgentID(input.ID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	projectID, err := parseProjectID(input.ProjectID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	role, err := parseAgentRole(input.Role)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	at, err := daemon.timestamp()
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	agent, err := daemon.store.CreateAgent(ctx, kernel.NewAgent{
		ID: id, ProjectID: projectID, Name: input.Name, Role: role,
		Provider: kernel.ProviderShell, ExecutionMode: kernel.ExecutionUnrestricted,
		ToolBudgetLimit: input.ToolBudgetLimit,
	}, at)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	return daemon.mutation(ctx, agent.Revision)
}

func (daemon *Daemon) enqueueTask(ctx context.Context, call api.Call) api.Reply {
	input, ok := call.EnqueueTaskInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	id, err := parseTaskID(input.ID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	projectID, err := parseProjectID(input.ProjectID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	agentID, err := parseAgentID(input.AssignedAgentID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	incarnationID, err := parseIncarnationID(input.IncarnationID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	at, err := daemon.timestamp()
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	task, err := daemon.store.EnqueueTask(ctx, kernel.NewTask{
		ID: id, ProjectID: projectID, AssignedAgentID: agentID, IncarnationID: incarnationID,
		Title: input.Title, Body: input.Body, Priority: input.Priority,
	}, at)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	return daemon.mutation(ctx, task.Revision)
}

func (daemon *Daemon) setDispatch(ctx context.Context, call api.Call) api.Reply {
	expected, enabled, ok := call.Dispatch()
	if !ok || expected > uint64(^uint64(0)>>1) {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	revision, err := kernel.NewRevision(int64(expected))
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	at, err := daemon.timestamp()
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	state, err := daemon.store.SetDispatch(ctx, revision, enabled, at)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	return mutationReply(state.Head, state.Revision)
}

func (daemon *Daemon) proposeOutcome(ctx context.Context, call api.Call) api.Reply {
	digest, ok := call.AttemptDigest()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	kDigest, err := attemptDigest(digest)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	proposal, err := proposalForCall(call)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	at, err := daemon.timestamp()
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	run, err := daemon.store.ProposeAttemptOutcome(ctx, kDigest, proposal, at)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	// The commit completed before ProposeAttemptOutcome returned. The wake is
	// deliberately best-effort and carries no authority or state payload.
	daemon.notifyRun(run.ID)
	return daemon.mutation(ctx, run.Revision)
}

func proposalForCall(call api.Call) (kernel.Proposal, error) {
	switch call.Kind() {
	case api.CallSucceed:
		result, ok := call.Result()
		if !ok {
			return kernel.Proposal{}, fmt.Errorf("%w: missing success result", kernel.ErrInvalidValue)
		}
		return kernel.NewSuccessProposal(result)
	case api.CallBlock:
		detail, ok := call.Detail()
		if !ok {
			return kernel.Proposal{}, fmt.Errorf("%w: missing block detail", kernel.ErrInvalidValue)
		}
		return kernel.NewBlockedProposal(detail)
	case api.CallFail:
		detail, ok := call.Detail()
		if !ok {
			return kernel.Proposal{}, fmt.Errorf("%w: missing failure detail", kernel.ErrInvalidValue)
		}
		return kernel.NewFailureProposal(kernel.FailureAttempt, detail)
	default:
		return kernel.Proposal{}, fmt.Errorf("%w: non-outcome call", kernel.ErrInvalidValue)
	}
}

func (daemon *Daemon) mutation(ctx context.Context, revision kernel.Revision) api.Reply {
	state, err := daemon.store.Factory(ctx)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	return mutationReply(state.Head, revision)
}

func mutationReply(head kernel.EventSequence, revision kernel.Revision) api.Reply {
	reply, err := api.NewMutationReply(api.MutationResult{Head: uint64(head.Int64()), Revision: uint64(revision.Int64())})
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	return reply
}

func (daemon *Daemon) timestamp() (kernel.UnixMillis, error) {
	if daemon == nil || daemon.now == nil {
		return kernel.UnixMillis{}, fmt.Errorf("%w: missing daemon clock", kernel.ErrInvalidValue)
	}
	return kernel.NewUnixMillis(daemon.now().UnixMilli())
}

// registerRunWake returns a capacity-one hint channel. Registering the same
// run twice returns the original channel; exceeding the bounded index fails
// without changing durable state.
func (daemon *Daemon) registerRunWake(runID kernel.RunID) (<-chan struct{}, bool) {
	if daemon == nil {
		return nil, false
	}
	daemon.wakeMu.Lock()
	defer daemon.wakeMu.Unlock()
	if existing := daemon.wakes[runID]; existing != nil {
		return existing, true
	}
	if len(daemon.wakes) >= maxRunWakeEntries {
		return nil, false
	}
	channel := make(chan struct{}, 1)
	daemon.wakes[runID] = channel
	return channel, true
}

func (daemon *Daemon) unregisterRunWake(runID kernel.RunID) {
	if daemon == nil {
		return
	}
	daemon.wakeMu.Lock()
	delete(daemon.wakes, runID)
	daemon.wakeMu.Unlock()
}

func (daemon *Daemon) notifyRun(runID kernel.RunID) {
	if daemon == nil {
		return
	}
	daemon.wakeMu.Lock()
	channel := daemon.wakes[runID]
	daemon.wakeMu.Unlock()
	if channel == nil {
		return
	}
	select {
	case channel <- struct{}{}:
	default:
	}
}

func newErrorReply(code api.RemoteErrorCode) api.Reply {
	reply, err := api.NewErrorReply(code)
	if err != nil {
		return api.Reply{}
	}
	return reply
}

func remoteErrorCode(err error) api.RemoteErrorCode {
	if err == nil {
		return api.RemoteInternal
	}
	var unknown *kernel.OutcomeUnknownError
	if errors.As(err, &unknown) {
		return api.RemoteInternal
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return api.RemoteUnavailable
	case errors.Is(err, kernel.ErrInvalidValue), errors.Is(err, kernel.ErrFutureCursor):
		return api.RemoteInvalidRequest
	case errors.Is(err, kernel.ErrUnauthorized):
		return api.RemoteUnauthorized
	case errors.Is(err, kernel.ErrNotFound):
		return api.RemoteNotFound
	case errors.Is(err, kernel.ErrRevisionConflict):
		return api.RemoteRevisionConflict
	case errors.Is(err, kernel.ErrConflict):
		return api.RemoteConflict
	case errors.Is(err, kernel.ErrSnapshotTooLarge):
		return api.RemoteTooLarge
	case errors.Is(err, kernel.ErrBusy), errors.Is(err, kernel.ErrStoreClosed):
		return api.RemoteUnavailable
	default:
		return api.RemoteInternal
	}
}
