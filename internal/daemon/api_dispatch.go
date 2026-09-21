package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/linear"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
	"github.com/dark-factory-build/dark-factory/internal/provider"
)

const (
	defaultDispatchTimeout           = 10 * time.Second
	retainedSourceDispatchTimeout    = 10 * time.Minute
	storageCompactionDispatchTimeout = 10 * time.Minute
)

// Daemon is the concrete composition root for the local API. It owns the
// durable Store and live attempt owners. It does not own an accept loop; the
// caller accepts and hands one connection to HandleConnection.
type Daemon struct {
	intakeMu             sync.Mutex
	linear               *linear.Host
	github               *maintainer.Host
	intakeControllerHome string
	// intakeIssues is a package-test-only remote failure/race seam.
	intakeIssues func(context.Context, string, uint64, uint32, string, uint64) (maintainer.IssuePage, error)
	// browserRemote is a package-test-only seam for operator calls that wait
	// outside a paired client's gate.
	browserRemote func(context.Context, string)
	maintainerMu  sync.Mutex
	store         *kernel.Store
	now           func() time.Time

	// Cleanup survives caller cancellation but remains interruptible by daemon shutdown.
	cleanupCtx    context.Context
	cleanupCancel context.CancelFunc

	// settleRetained is a package-test-only seam for an inspection that races a
	// durable update. Production settlement always uses retainedSettlement.
	settleRetained func(context.Context, string, kernel.Change) (kernel.ChangeSettlement, error)
	// scheduledRun is a package-test-only seam for the scheduler's terminal
	// completion reread. Production reads from the concrete Store.
	scheduledRun func(context.Context, kernel.RunID) (kernel.Run, bool, error)
	// successSource* are package-test-only seams for failure-injection coverage;
	// production source validation always reads the concrete Store.
	successSourceRun        func(context.Context, kernel.RunID) (kernel.Run, bool, error)
	successSourceChange     func(context.Context, kernel.ChangeID) (kernel.Change, bool, error)
	successSourceRepository func(context.Context, kernel.TaskID) (kernel.ProjectRepository, bool, error)
	beforeSuccessProposal   func()
	successSourceInspect    func(context.Context, string, string, change.RepositoryIdentity, string) (change.WorktreeFacts, error)

	browserMu          sync.Mutex
	browserLifecycleMu sync.Mutex
	browsers           map[*BrowserRuntime]struct{}
	browserClosing     bool
	// relay is the optional outbound relay connector. It is a client of the
	// browser listener above, not a second authority, so it shares that
	// listener's lifecycle gate.
	relay *RelayRuntime
	// push holds device alert subscriptions once a relay home is open.
	push               *pushStore
	browserClientGates *browserClientGates

	// topologies holds the last regenerable topology per project for a short
	// window. It is a cost guard, not state: losing it only costs one walk.
	topologyMu sync.Mutex
	topologies map[kernel.ProjectID]topologySnapshot

	// providerDefaultCache holds the last read of each provider account's own
	// configured model for a short window, on the same terms as topologies:
	// a cost guard over a file read, never state.
	providerDefaultMu    sync.Mutex
	providerDefaultCache map[providerAccount]providerDefault

	// operationMu is the single linearization gate for live-attempt operations
	// that combine durable state with an owner-side action. It is deliberately
	// concrete and global: the local operator has no throughput requirement,
	// and one gate avoids a second digest-to-owner authority index.
	operationMu sync.Mutex

	// changeParent is the changes root the supervisor was given, published
	// without a lock so RunNext never waits on a console read. accountHome is
	// the account every run launches under, published the same way and for the
	// same reason. runPathsMu guards only the map of bounded directory walks,
	// never a walk itself.
	changeParent  atomic.Pointer[string]
	accountHome   atomic.Pointer[string]
	gitExecutable atomic.Pointer[string]
	runPathsMu    sync.Mutex
	runPaths      map[kernel.RunID]runPathsResult

	attemptMu sync.Mutex
	attempts  map[kernel.RunID]*liveAttempt
	closing   bool
	closeDone chan struct{}
	closeErr  error

	// schedulerWake is a bounded hint channel. SQLite remains the only
	// admission and capacity authority; losing or coalescing a hint is safe
	// because the scheduler also polls. schedulerRunning prevents two process
	// owners from creating competing unobserved admission probes.
	schedulerMu      sync.Mutex
	schedulerWake    chan struct{}
	schedulerRunning bool
	// supervisors contains every synchronous RunNext owner, including the
	// phase before a live attempt can register. Registration and closing share
	// attemptMu, so Close can cancel a real owner rather than passively waiting
	// for a caller that may still be blocked in pre-release setup.
	supervisors map[*supervisorRegistration]struct{}
}

type supervisorRegistration struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.Mutex
	result error
}

// NewDaemon creates an API composition root using the wall clock for durable
// timestamps. The Store is never replaced or wrapped by the daemon.
func NewDaemon(store *kernel.Store) (*Daemon, error) {
	return newDaemon(store, time.Now)
}

func newDaemon(store *kernel.Store, now func() time.Time) (*Daemon, error) {
	if store == nil || now == nil {
		return nil, fmt.Errorf("%w: invalid daemon", kernel.ErrInvalidValue)
	}
	cleanupCtx, cleanupCancel := context.WithCancel(context.Background())
	return &Daemon{store: store, now: now, cleanupCtx: cleanupCtx, cleanupCancel: cleanupCancel, browsers: make(map[*BrowserRuntime]struct{}), browserClientGates: &browserClientGates{}, attempts: make(map[kernel.RunID]*liveAttempt), supervisors: make(map[*supervisorRegistration]struct{}), schedulerWake: make(chan struct{}, 1)}, nil
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
	dispatchContext, cancel := context.WithTimeout(ctx, defaultDispatchTimeout)
	defer cancel()
	if call.Kind() == api.CallMaintainer || call.Kind() == api.CallIntake {
		cancel()
		dispatchContext, cancel = context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
	}
	if call.Kind() == api.CallAttemptSource {
		cancel()
		dispatchContext, cancel = context.WithTimeout(ctx, retainedSourceDispatchTimeout)
		defer cancel()
	}
	if call.Kind() == api.CallCompactStorage {
		cancel()
		dispatchContext, cancel = context.WithTimeout(ctx, storageCompactionDispatchTimeout)
		defer cancel()
	}
	if err := connection.RefreshDeadline(dispatchContext); err != nil {
		return err
	}
	if attemptOutcomeCall(call.Kind()) {
		var attempt *liveAttempt
		reply, dispatchErr := connection.Dispatch(func(call api.Call) api.Reply {
			var outcome api.Reply
			outcome, attempt = daemon.proposeOutcome(dispatchContext, call)
			return outcome
		})
		if dispatchErr != nil {
			daemon.clearOutcomeReceipt(attempt)
			return dispatchErr
		}
		responseErr := connection.Respond(reply)
		if responseErr == nil {
			responseErr = connection.AwaitOutcomeReceipt(dispatchContext)
		}
		daemon.clearOutcomeReceipt(attempt)
		return responseErr
	}
	reply, err := connection.Dispatch(func(call api.Call) api.Reply { return daemon.dispatch(dispatchContext, call) })
	if err != nil {
		return err
	}
	return connection.Respond(reply)
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
	case api.CallHumanRequests:
		return daemon.humanRequests(ctx)
	case api.CallHumanReply:
		return daemon.humanReplyOperator(ctx, call)
	case api.CallCreateProject:
		return daemon.createProject(ctx, call)
	case api.CallProjectRepository:
		return daemon.projectRepository(ctx, call)
	case api.CallProjectLimits:
		return daemon.setProjectLimits(ctx, call)
	case api.CallCreateAgent:
		return daemon.createAgent(ctx, call)
	case api.CallAgentIdlePolicy:
		return daemon.setAgentIdlePolicy(ctx, call)
	case api.CallEnqueueTask:
		return daemon.enqueueTask(ctx, call)
	case api.CallTaskRecovery:
		return daemon.taskRecovery(ctx, call)
	case api.CallTaskRead:
		return daemon.taskRead(ctx, call)
	case api.CallOperatorWorkerOperation:
		return daemon.workerOperation(ctx, call)
	case api.CallSetDispatch:
		return daemon.setDispatch(ctx, call)
	case api.CallCompactStorage:
		state, err := daemon.store.CompactStorage(ctx)
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		return mutationReply(state.Head, state.Revision)
	case api.CallSetCapacity:
		return daemon.setCapacity(ctx, call)
	case api.CallAccountsDiscover:
		offset, ok := call.AccountsOffset()
		if !ok {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		return daemon.discoverOperatorAccounts(ctx, offset)
	case api.CallAccountLink:
		return daemon.linkOperatorAccount(ctx, call)
	case api.CallAgentSelectAccount:
		return daemon.selectAgentAccount(ctx, call)
	case api.CallAgentSelectModel:
		return daemon.selectAgentModel(ctx, call)
	case api.CallAgentPaths:
		return daemon.agentPaths(ctx, call)
	case api.CallAttemptTask:
		return daemon.attemptTask(ctx, call)
	case api.CallAttemptSource:
		return daemon.attemptSource(ctx, call)
	case api.CallRequestHuman:
		return daemon.requestHuman(ctx, call)
	case api.CallPeerStatus:
		return daemon.peerStatus(ctx, call)
	case api.CallPeerAsk:
		return daemon.peerAsk(ctx, call)
	case api.CallPeerAnswer:
		return daemon.peerAnswer(ctx, call)
	case api.CallTerminalObserve:
		return daemon.terminalObserve(ctx, call)
	case api.CallOperatorTerminalObserve:
		return daemon.operatorTerminalObserve(ctx, call)
	case api.CallSendBack, api.CallSendBackTask:
		return daemon.sendBack(ctx, call)
	case api.CallOverseerSnapshot:
		return daemon.overseerSnapshot(ctx, call)
	case api.CallOverseerEnqueueTask:
		return daemon.overseerEnqueueTask(ctx, call)
	case api.CallOverseerUpdateTask:
		return daemon.overseerUpdateTask(ctx, call)
	case api.CallOverseerUpdateAgent:
		return daemon.overseerUpdateAgent(ctx, call)
	case api.CallOverseerStopRun:
		return daemon.overseerStopRun(ctx, call)
	case api.CallOverseerReplaceRun:
		return daemon.overseerReplaceRun(ctx, call)
	case api.CallOverseerMessageWorker:
		return daemon.overseerMessageWorker(ctx, call)
	case api.CallOverseerInterruptWorker:
		return daemon.overseerInterruptWorker(ctx, call)
	case api.CallOperatorStopRun:
		return daemon.operatorStopRun(ctx, call)
	case api.CallOperatorReplaceRun:
		return daemon.operatorReplaceRun(ctx, call)
	case api.CallOperatorMessageWorker:
		return daemon.operatorMessageWorker(ctx, call)
	case api.CallOperatorInterruptWorker:
		return daemon.operatorInterruptWorker(ctx, call)
	case api.CallOperatorUpdateTask:
		return daemon.operatorUpdateTask(ctx, call)
	case api.CallOperatorUpdateAgent:
		return daemon.operatorUpdateAgent(ctx, call)
	case api.CallOverseerReplyHuman:
		return daemon.overseerReplyHuman(ctx, call)
	case api.CallContentCreate, api.CallContentRevise, api.CallContentDeprecate, api.CallContentList, api.CallContentRead, api.CallContentBody, api.CallContentEvidence, api.CallContentAttach, api.CallContentEvidenceList, api.CallContentAttachments:
		return daemon.content(ctx, call)
	case api.CallOutcomeWrite, api.CallOutcomeRead, api.CallOutcomeList:
		return daemon.outcomes(ctx, call)
	case api.CallWebStatus:
		status, err := daemon.WebStatus(ctx)
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		reply, err := api.NewWebStatusReply(status)
		if err != nil {
			return newErrorReply(api.RemoteInternal)
		}
		return reply
	case api.CallWebListClients:
		after, ok := call.WebListAfter()
		if !ok {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		page, err := daemon.WebListClients(ctx, after)
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		reply, err := api.NewWebClientsReply(page)
		if err != nil {
			return newErrorReply(api.RemoteInternal)
		}
		return reply
	case api.CallMaintainer:
		return daemon.attemptMaintainer(ctx, call)
	case api.CallIntake:
		input, ok := call.IntakeInput()
		if !ok {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		return api.NewContentReply(daemon.Intake(ctx, input))
	case api.CallProductionObserve:
		input, ok := call.ProductionInput()
		project, err := browserContentProject(input.ProjectID)
		if !ok || err != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		at, err := daemon.timestamp()
		if err == nil {
			err = daemon.store.RecordProductionObservation(ctx, project, input.Observation, at)
		}
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		return api.NewContentReply(api.ProductionResult{State: "recorded"})
	case api.CallGitHubConnection:
		input, ok := call.GitHubConnectionInput()
		if !ok {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		return api.NewContentReply(daemon.GitHubConnection(ctx, input))
	case api.CallRemoteStatus:
		status, err := daemon.RemoteStatus()
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		reply, err := api.NewRemoteStatusReply(status)
		if err != nil {
			return newErrorReply(api.RemoteInternal)
		}
		return reply
	case api.CallWebRevokeClient:
		input, ok := call.WebClientRevocationInput()
		if !ok {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		result, err := daemon.WebRevokeClient(ctx, input)
		if err != nil {
			if errors.Is(err, ErrBrowserClientCleanup) {
				return newErrorReply(api.RemoteCleanupUnresolved)
			}
			return newErrorReply(remoteErrorCode(err))
		}
		reply, err := api.NewWebRevokeReply(result)
		if err != nil {
			return newErrorReply(api.RemoteInternal)
		}
		return reply
	default:
		return newErrorReply(api.RemoteInvalidRequest)
	}
}

func (daemon *Daemon) agentPaths(ctx context.Context, call api.Call) api.Reply {
	input, ok := call.AgentPathsInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	agentID, err := parseAgentID(input.AgentID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	if _, found, err := daemon.store.Agent(ctx, agentID); err != nil {
		return newErrorReply(remoteErrorCode(err))
	} else if !found {
		return newErrorReply(api.RemoteNotFound)
	}
	runID, sourcePath, runtimePath, err := daemon.liveRunLocations(ctx, agentID)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	run := ""
	paths := []string{}
	if runID != (kernel.RunID{}) {
		run = runID.String()
		pathsRunID, observedPaths, pathErr := daemon.RunPaths(ctx, agentID)
		if pathErr != nil {
			return newErrorReply(remoteErrorCode(pathErr))
		}
		if pathsRunID != (kernel.RunID{}) && pathsRunID != runID {
			return newErrorReply(api.RemoteUnavailable)
		}
		if pathsRunID == runID {
			paths = observedPaths
		}
	}
	reply, err := api.NewAgentPathsReply(api.AgentPaths{AgentID: input.AgentID, RunID: run, SourcePath: sourcePath, RuntimePath: runtimePath, Paths: paths})
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	return reply
}

func (daemon *Daemon) taskRead(ctx context.Context, call api.Call) api.Reply {
	input, ok := call.TaskReadInput()
	if !ok || input.ExpectedRevision > uint64(^uint64(0)>>1) {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	id, err := parseTaskID(input.TaskID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	expected, err := kernel.NewRevision(int64(input.ExpectedRevision))
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	task, found, err := daemon.store.Task(ctx, id)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	if !found {
		return newErrorReply(api.RemoteNotFound)
	}
	if task.Revision != expected {
		return newErrorReply(api.RemoteRevisionConflict)
	}
	instruction, instructionMore := taskDetailTextChunk(kernel.TaskInstruction(task), input.Offset)
	feedback, feedbackMore := taskDetailTextChunk(kernel.TaskFeedback(task), input.Offset)
	outcomeText := task.Result
	if outcomeText == "" {
		outcomeText = task.BlockedReason
	}
	outcome, outcomeMore := taskDetailTextChunk(outcomeText, input.Offset)
	attachments, err := daemon.store.TaskAttachments(ctx, id)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	result := api.TaskText{Attachments: attachments, TaskID: task.ID.String(), Revision: uint64(task.Revision.Int64()), Instruction: instruction, Feedback: feedback}
	if outcomeText != "" {
		result.Outcome = &outcome
	}
	if instructionMore || feedbackMore || outcomeMore {
		next := input.Offset + 2048
		result.NextOffset = &next
	}
	reply, err := api.NewTaskTextReply(result)
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	return reply
}

func (daemon *Daemon) taskRecovery(ctx context.Context, call api.Call) api.Reply {
	input, ok := call.TaskRecoveryInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	id, err := parseTaskID(input.TaskID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	incarnation, err := parseIncarnationID(input.IncarnationID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	recovery, found, err := daemon.store.TaskRecovery(ctx, id, incarnation)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	value := api.TaskRecovery{ArtifactPaths: []string{}}
	if !found {
		value.State = "missing"
	} else {
		value.State, value.TaskID, value.IncarnationID = "found", recovery.Task.ID.String(), recovery.Incarnation.String()
		value.ProjectID, value.AssignedAgentID = recovery.Task.ProjectID.String(), optionalAgentText(recovery.Task.AssignedAgentID)
		value.WorkRevision, value.Revision, value.Status = uint64(recovery.Task.WorkRevision.Int64()), uint64(recovery.Task.Revision.Int64()), recovery.Task.Status.String()
		value.NeedsOperatorRecovery = recovery.NeedsOperatorRecovery
		value.Result, value.BlockedReason = recovery.Task.Result, recovery.Task.BlockedReason
		if len(value.Result) > api.MaxRecoveryResultBytes {
			end := api.MaxRecoveryResultBytes
			for !utf8.RuneStart(value.Result[end]) {
				end--
			}
			value.Result, value.ResultTruncated = value.Result[:end], true
		}
		if recovery.Change != nil {
			value.ChangeID, value.ChangeRevision, value.ChangePhase = recovery.Change.ID.String(), uint64(recovery.Change.Revision.Int64()), recovery.Change.Phase.String()
			if recovery.Change.Selection != nil {
				value.SourceFormat = recovery.Change.Selection.ObjectFormat().String()
				value.SourceBaseCommit = hex.EncodeToString(recovery.Change.Selection.Commit().Bytes())
				value.SourceRepositoryDev = recovery.Change.Selection.RepositoryIdentity().Device()
				value.SourceRepositoryInode = recovery.Change.Selection.RepositoryIdentity().Inode()
			}
		}
		if recovery.Run != nil {
			value.RunID, value.RunRevision = recovery.Run.ID.String(), uint64(recovery.Run.Revision.Int64())
			value.RunWorkRevision = uint64(recovery.Run.AdmittedTaskWorkRevision.Int64())
			if recovery.Run.Terminal != nil {
				value.RunOutcome, value.RunDetail = recovery.Run.Terminal.Kind().String(), recovery.Run.Terminal.Detail()
			}
		}
		for _, resource := range recovery.Artifacts {
			if resource.Path != "" {
				value.ArtifactPaths = append(value.ArtifactPaths, resource.Path)
			}
		}
		value.Disposition, value.OverseerNotification, value.LastProgressAtMs = recovery.Disposition(), string(recovery.OverseerNotification), recovery.LastProgressAt.Int64()
		if recovery.HumanRequest != nil {
			value.HumanRequestID = recovery.HumanRequest.String()
		}
		if recovery.Overseer != nil {
			value.OverseerAgentID = recovery.Overseer.String()
		}
		if recovery.OverseerTask != nil {
			value.OverseerTaskID, value.OverseerTaskStatus, value.OverseerTaskTitle = recovery.OverseerTask.ID.String(), recovery.OverseerTask.Status.String(), recovery.OverseerTask.Title
		}
		if recovery.Change != nil && recovery.Change.HeadCommit != nil {
			value.ChangeHeadCommit = hex.EncodeToString(recovery.Change.HeadCommit.Bytes())
		}
		if run := recovery.Run; run != nil {
			if run.RunningAt != nil && run.TerminalAt != nil {
				value.RunRunningMs = run.TerminalAt.Int64() - run.RunningAt.Int64()
			}
			if exit := run.ProviderExit; exit != nil {
				value.RunProviderExit = "absent"
				if code, ok := exit.Code(); ok {
					value.RunProviderExit = fmt.Sprintf("code %d", code)
				} else if signal, ok := exit.Signal(); ok {
					value.RunProviderExit = fmt.Sprintf("signal %d", signal)
				}
			}
		}
	}
	reply, err := api.NewTaskRecoveryReply(value)
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	return reply
}

func (daemon *Daemon) workerOperation(ctx context.Context, call api.Call) api.Reply {
	input, ok := call.WorkerOperationInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	id, err := parseTaskInterventionID(input.OperationID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	receipt, found, err := daemon.store.TaskIntervention(ctx, id)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	if !found {
		return newErrorReply(api.RemoteNotFound)
	}
	detail := ""
	if receipt.ResultDetail != nil {
		detail = *receipt.ResultDetail
	}
	reply, err := api.NewWorkerOperationReply(api.WorkerOperation{OperationID: receipt.OperationID.String(), TaskID: receipt.TaskID.String(), RunID: receipt.RunID.String(), State: receipt.State.String(), Detail: detail})
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	return reply
}

func (daemon *Daemon) humanRequests(ctx context.Context) api.Reply {
	requests, err := daemon.store.OperatorHumanRequests(ctx)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	result := api.HumanRequestList{Requests: make([]api.HumanRequest, 0, len(requests))}
	for _, request := range requests {
		result.Requests = append(result.Requests, api.HumanRequest{ID: request.ID.String(), RunID: request.RunID.String(), TaskID: request.TaskID.String(), AgentID: request.AgentID.String(), Status: request.Status.String(), Revision: uint64(request.Revision.Int64()), Question: request.QuestionText, Options: append([]string{}, request.Options...)})
	}
	return api.NewHumanRequestListReply(result)
}

func (daemon *Daemon) humanReplyOperator(ctx context.Context, call api.Call) api.Reply {
	input, ok := call.HumanReplyInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	requestID, err := parseHumanRequestID(input.RequestID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	deliveryID, err := parseHumanDeliveryID(input.OperationID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	expected, err := kernel.NewRevision(int64(input.ExpectedRevision))
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	if !terminalEffectsSupported {
		return newErrorReply(api.RemoteUnavailable)
	}
	daemon.operationMu.Lock()
	defer daemon.operationMu.Unlock()
	at, err := daemon.timestamp()
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	if handled, continuationErr := daemon.store.ResolveHumanContinuationForOperator(ctx, requestID, expected, deliveryID, input.Reply, at); continuationErr == nil && handled {
		daemon.notifyScheduler()
		projection, _, readErr := daemon.store.HumanRequest(ctx, requestID)
		if readErr != nil {
			return newErrorReply(remoteErrorCode(readErr))
		}
		return daemon.overseerHumanReplyMutation(projection)
	} else if continuationErr != nil && !errors.Is(continuationErr, kernel.ErrNotFound) && !errors.Is(continuationErr, kernel.ErrConflict) {
		return newErrorReply(remoteErrorCode(continuationErr))
	}
	delivery, err := daemon.store.BeginHumanReplyForOperator(ctx, requestID, expected, deliveryID, input.Reply, at)
	if err != nil {
		if terminalStoreOutcomeUnknown(err) {
			deliveryRevision, revisionErr := kernel.NewRevision(expected.Int64() + 1)
			var unknownErr error
			if revisionErr == nil {
				unknownErr = daemon.markHumanReplyUnknown(requestID, deliveryID, deliveryRevision)
			}
			return newErrorReply(remoteErrorCode(errors.Join(err, revisionErr, unknownErr)))
		}
		return newErrorReply(remoteErrorCode(err))
	}
	if delivery.RequestID != requestID || delivery.DeliveryID != deliveryID {
		return newErrorReply(api.RemoteInternal)
	}
	_, effectErr := daemon.deliverHumanReply(ctx, delivery)
	projection, err := daemon.humanReplyOutcome(requestID, effectErr)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	return daemon.overseerHumanReplyMutation(projection)
}

func (daemon *Daemon) attemptTask(ctx context.Context, call api.Call) api.Reply {
	digest, ok := call.AttemptDigest()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	kDigest, err := attemptDigest(digest)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	authority, err := daemon.store.AuthenticateAttempt(ctx, kDigest)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	attachments, err := daemon.store.TaskAttachments(ctx, authority.TaskID)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	instruction, err := kernel.TaskAttachmentInstruction(authority.Task(), attachments)
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	task, err := attemptTaskWithContinuationContext(authority.Provider, []byte(instruction), authority.ContinuationContexts)
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	assignment := api.AttemptTask{Task: string(task)}
	accepted, found, err := daemon.store.IntakeAcceptanceForTask(ctx, authority.TaskID)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	if found {
		hash := accepted.Snapshot.ContentHash()
		assignment.Intake = &api.IntakeTaskSource{LinearTeamID: accepted.Snapshot.LinearTeamID, SourceURL: accepted.Snapshot.URL, AcceptanceID: accepted.ID.String(), Repository: accepted.SourceRepository, RepositoryID: accepted.Snapshot.GitHubRepositoryID, IssueNumber: accepted.Snapshot.IssueNumber, TargetRepositoryID: accepted.RepositoryID.String(), ContentHash: hex.EncodeToString(hash[:])}
	}

	if authority.Role == kernel.RoleWorker {
		if authority.ChangeID == nil || authority.AdmittedChangeRevision == nil || authority.CurrentChangeRevision == nil || len(authority.BaseCommit) == 0 {
			return newErrorReply(api.RemoteInternal)
		}
		assignment.TaskID = authority.TaskID.String()
		assignment.IncarnationID = authority.TaskIncarnation.String()
		assignment.WorkRevision = uint64(authority.AdmittedTaskWorkRevision.Int64())
		assignment.ChangeID = authority.ChangeID.String()
		assignment.AdmittedChangeRevision = uint64(authority.AdmittedChangeRevision.Int64())
		assignment.ChangeRevision = uint64(authority.CurrentChangeRevision.Int64())
		assignment.BaseCommit = hex.EncodeToString(authority.BaseCommit)
	}
	reply, err := api.NewAttemptTaskReply(assignment)
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	return reply
}

func (daemon *Daemon) attemptSource(ctx context.Context, call api.Call) api.Reply {
	digest, ok := call.AttemptDigest()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	kDigest, err := attemptDigest(digest)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	authority, err := daemon.store.AuthenticateAttempt(ctx, kDigest)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	// Only providers with the protected retained-source receipt route may
	// receive this immutable snapshot. Unsupported providers fail before target
	// parsing, so a reassignment cannot silently admit doomed work.
	if !kernel.RetainedSourceReviewSupported(authority.Provider) {
		return newErrorReply(api.RemoteUnavailable)
	}
	taskIDText, ok := call.AttemptSourceTaskID()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	targetTaskID, err := parseTaskID(taskIDText)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	// An overseer reads any settled Change in its project; a worker reads only
	// the target its own handoff line names, in the body the supervisor parsed.
	if authority.Role == kernel.RoleWorker {
		task, _, err := daemon.store.Task(ctx, authority.TaskID)
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		if expected, review, err := kernel.ParseRetainedSourceReviewTask(task.Body); err != nil || !review || expected.TaskID != targetTaskID {
			return newErrorReply(api.RemoteUnauthorized)
		}
	}
	daemon.attemptMu.Lock()
	live := daemon.attempts[authority.RunID]
	daemon.attemptMu.Unlock()
	if live == nil {
		return newErrorReply(api.RemoteUnavailable)
	}
	if !live.beginSourceOperation() {
		return newErrorReply(api.RemoteUnavailable)
	}
	defer live.endSourceOperation()
	handoff, found, err := daemon.store.RetainedChangeHandoffForTask(ctx, authority.ProjectID, targetTaskID)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	if !found {
		return newErrorReply(api.RemoteNotFound)
	}
	projected, err := daemon.attemptSourceHandoff(ctx, handoff)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	reply, err := api.NewAttemptSourceReply(projected)
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	return reply
}

func (daemon *Daemon) peerStatus(ctx context.Context, call api.Call) api.Reply {
	digest, ok := call.AttemptDigest()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	kDigest, err := attemptDigest(digest)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	authority, err := daemon.store.AuthenticateAttempt(ctx, kDigest)
	if err != nil || (authority.Role != kernel.RoleWorker && authority.Role != kernel.RoleOrchestrator) {
		if err == nil {
			err = kernel.ErrUnauthorized
		}
		return newErrorReply(remoteErrorCode(err))
	}
	offset, targetOffset, expectedHead, includeTargets, ok := call.PeerStatusPage()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	head, err := kernel.NewEventSequence(int64(expectedHead))
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	items, nextOffset, head, err := daemon.store.PeerQuestionsForTask(ctx, authority.TaskID, offset, head)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	status := api.PeerStatus{Head: uint64(head.Int64()), Targets: []api.PeerTarget{}, Questions: make([]api.PeerQuestion, 0, len(items)), NextOffset: nextOffset}
	if includeTargets {
		targets, nextTargetOffset, err := daemon.store.PeerTargetsForAttempt(ctx, kDigest, targetOffset, head)
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		status.Targets = make([]api.PeerTarget, 0, len(targets))
		status.NextTargetOffset = nextTargetOffset
		for _, target := range targets {
			status.Targets = append(status.Targets, api.PeerTarget{TaskID: target.TaskID.String(), AgentID: target.AgentID.String(), Name: target.Name, Title: target.Title, Status: target.Status.String(), Revision: uint64(target.Revision.Int64())})
		}
	}
	for _, item := range items {
		status.Questions = append(status.Questions, daemon.projectPeerQuestion(ctx, item))
	}
	state, err := daemon.store.Factory(ctx)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	if state.Head != head {
		return newErrorReply(api.RemoteRevisionConflict)
	}
	reply, err := api.NewPeerStatusReply(status)
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	return reply
}

func (daemon *Daemon) projectPeerQuestion(ctx context.Context, item kernel.PeerQuestion) api.PeerQuestion {
	target, targetFound, _ := daemon.store.Task(ctx, item.TargetTaskID)
	source, sourceFound, _ := daemon.store.Task(ctx, item.SourceTaskID)
	availability := func(task kernel.Task, found bool) string {
		if !found || (task.Status != kernel.TaskQueued && task.Status != kernel.TaskRunning) {
			return "stale"
		}
		if task.Status == kernel.TaskRunning {
			return "available"
		}
		return "pending"
	}
	return api.PeerQuestion{ID: item.ID.String(), SourceTaskID: item.SourceTaskID.String(), TargetTaskID: item.TargetTaskID.String(), Question: item.Question, Answer: item.Answer, RecipientDeliveryState: item.RecipientDeliveryState.String(), AnswerDeliveryState: item.AnswerDeliveryState.String(), RecipientAvailability: availability(target, targetFound), AnswerAvailability: availability(source, sourceFound), Revision: uint64(item.Revision.Int64())}
}

func (daemon *Daemon) peerAsk(ctx context.Context, call api.Call) api.Reply {
	digest, ok := call.AttemptDigest()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	kDigest, err := attemptDigest(digest)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	input, ok := call.PeerQuestionInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	target, err := parseTaskID(input.TargetTaskID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	raw, err := parseID(input.IdempotencyKey)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	var key [kernel.IDBytes]byte
	copy(key[:], raw)
	at, err := daemon.timestamp()
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	question, err := daemon.store.CreatePeerQuestionForAttempt(ctx, kDigest, kernel.NewPeerQuestion{TargetTaskID: target, IdempotencyKey: key, Question: input.Question}, at)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	if err := daemon.notifyPeerDelivery(ctx, question); err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	return daemon.mutation(ctx, question.Revision)
}

func (daemon *Daemon) peerAnswer(ctx context.Context, call api.Call) api.Reply {
	digest, ok := call.AttemptDigest()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	kDigest, err := attemptDigest(digest)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	input, ok := call.PeerAnswerInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	rawID, err := parseID(input.QuestionID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	id, err := kernel.PeerQuestionIDFromBytes(rawID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	raw, err := parseID(input.IdempotencyKey)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	var key [kernel.IDBytes]byte
	copy(key[:], raw)
	revision, err := kernel.NewRevision(int64(input.ExpectedRevision))
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	at, err := daemon.timestamp()
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	question, err := daemon.store.AnswerPeerQuestionForAttempt(ctx, kDigest, kernel.PeerAnswer{QuestionID: id, Expected: revision, IdempotencyKey: key, Answer: input.Answer}, at)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	if err := daemon.notifyPeerDelivery(ctx, question); err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	return daemon.mutation(ctx, question.Revision)
}

func (daemon *Daemon) notifyPeerDelivery(ctx context.Context, question kernel.PeerQuestion) error {
	if !terminalEffectsSupported {
		return nil
	}
	if question.AnswerIdempotencyKey == nil && question.RecipientDeliveryID != nil || question.AnswerIdempotencyKey != nil && question.AnswerDeliveryID != nil {
		return nil
	}
	run, found, err := daemon.store.RunningPeerTarget(ctx, question)
	if err != nil || !found {
		return err
	}
	var raw [kernel.IDBytes]byte
	if _, err := rand.Read(raw[:]); err != nil || raw == ([kernel.IDBytes]byte{}) {
		if err == nil {
			err = kernel.ErrCorruptState
		}
		return err
	}
	deliveryID, err := kernel.PeerDeliveryIDFromBytes(raw[:])
	if err != nil {
		return err
	}
	answer := question.AnswerIdempotencyKey != nil
	daemon.operationMu.Lock()
	defer daemon.operationMu.Unlock()
	at, err := daemon.timestamp()
	if err != nil {
		return err
	}
	delivery, newly, err := daemon.store.ReservePeerDelivery(ctx, question.ID, run.ID, deliveryID, answer, at)
	if err != nil || !newly {
		return err
	}
	attempt, err := daemon.liveTerminalAttempt(run.ID, kernel.TerminalSessionID{})
	if err != nil {
		return nil
	}
	result := attempt.submitEffect(ctx, terminalEffect{kind: terminalEffectHumanReply, payload: delivery.Payload, submit: true})
	if result.effectError(len(delivery.Payload)) != nil {
		return nil
	}
	ackAt, err := daemon.timestamp()
	if err != nil {
		return err
	}
	_, err = daemon.store.AcknowledgePeerDelivery(ctx, question.ID, delivery.DeliveryID, answer, delivery.Revision, ackAt)
	return err
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

func (daemon *Daemon) discoverOperatorAccounts(ctx context.Context, offset uint32) api.Reply {
	home, err := operatorHome()
	if err != nil {
		return newErrorReply(api.RemoteNotFound)
	}
	linked, err := daemon.store.ListAccounts(ctx)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	found := daemon.listedAccounts(home, linked)
	protocolFound := make([]browserprotocol.DiscoveredAccount, 0, len(found))
	for _, candidate := range found {
		protocolFound = append(protocolFound, browserprotocol.DiscoveredAccount{Provider: candidate.Provider, Home: candidate.Home, Label: candidate.Label, Email: candidate.Email, Organization: candidate.Organization, DefaultModel: candidate.DefaultModel, DefaultReasoningEffort: candidate.DefaultReasoningEffort, LinkedID: candidate.LinkedID, UnavailableReason: candidate.UnavailableReason})
	}
	page, err := browserprotocol.PageAccounts(protocolFound, offset)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	accounts := api.Accounts{Accounts: make([]api.DiscoveredAccount, 0, len(page.Accounts)), NextOffset: page.NextOffset}
	for _, candidate := range page.Accounts {
		accounts.Accounts = append(accounts.Accounts, api.DiscoveredAccount{Provider: candidate.Provider, Home: candidate.Home, Label: candidate.Label, Email: candidate.Email, Organization: candidate.Organization, DefaultModel: candidate.DefaultModel, DefaultReasoningEffort: candidate.DefaultReasoningEffort, LinkedID: candidate.LinkedID, UnavailableReason: candidate.UnavailableReason})
	}
	reply, err := api.NewAccountsReply(accounts)
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	return reply
}

func (daemon *Daemon) linkOperatorAccount(ctx context.Context, call api.Call) api.Reply {
	input, ok := call.AccountLinkInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	provider, err := kernel.ParseProvider(input.Provider)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	home, err := operatorHome()
	if err != nil {
		return newErrorReply(api.RemoteNotFound)
	}
	present := false
	for _, candidate := range daemon.discoverAccounts(home) {
		if candidate.Provider == input.Provider && candidate.Home == input.Home {
			present = true
			break
		}
	}
	if !present {
		return newErrorReply(api.RemoteNotFound)
	}
	var raw [kernel.IDBytes]byte
	if _, err := rand.Read(raw[:]); err != nil || raw == ([kernel.IDBytes]byte{}) {
		return newErrorReply(api.RemoteInternal)
	}
	id, err := kernel.AccountIDFromBytes(raw[:])
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	at, err := daemon.timestamp()
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	account, err := daemon.store.LinkAccount(ctx, kernel.NewAccount{ID: id, Provider: provider, Home: input.Home, Label: input.Label}, at)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	return daemon.mutation(ctx, account.Revision)
}

func (daemon *Daemon) selectAgentAccount(ctx context.Context, call api.Call) api.Reply {
	input, ok := call.AgentAccountSelectInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	agentID, err := parseAgentID(input.AgentID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	accountID, err := parseAccountID(input.AccountID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	expected, err := kernel.NewRevision(int64(input.ExpectedRevision))
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	at, err := daemon.timestamp()
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	updated, err := daemon.store.UpdateAgent(ctx, agentID, expected, kernel.AgentPatch{AccountID: &accountID}, at)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	return daemon.mutation(ctx, updated.Revision)
}

// selectAgentModel stores the next admission's native-provider controls. An
// admitted run already holds its own immutable model and effort, so it remains
// unaffected while this update is committed for future runs.

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
	project, err := registerProject(ctx, daemon.store, kernel.NewProject{
		ID: id, Name: input.Name, Root: input.Root, VerificationPolicy: kernel.VerificationNone,
	}, at)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	return daemon.mutation(ctx, project.Revision)
}

func repositoryDTO(value kernel.ProjectRepository) api.ProjectRepository {
	return api.ProjectRepository{ID: value.ID.String(), ProjectID: value.ProjectID.String(), Name: value.Name, Root: value.Root, BaseRef: value.BaseRef, Enabled: value.Enabled, Default: value.Default, Revision: uint64(value.Revision.Int64())}
}
func (daemon *Daemon) projectRepository(ctx context.Context, call api.Call) api.Reply {
	input, ok := call.ProjectRepositoryInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	parseRepo := func() (kernel.RepositoryID, bool) {
		raw, err := parseID(input.ID)
		if err != nil {
			return kernel.RepositoryID{}, false
		}
		value, err := kernel.RepositoryIDFromBytes(raw)
		return value, err == nil
	}
	parseProject := func() (kernel.ProjectID, bool) {
		value, err := parseProjectID(input.ProjectID)
		return value, err == nil
	}
	at, err := daemon.timestamp()
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	switch input.Action {
	case "list":
		project, valid := parseProject()
		if !valid {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		values, err := daemon.store.ProjectRepositories(ctx, project)
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		result := api.ProjectRepositories{Repositories: make([]api.ProjectRepository, 0, len(values))}
		for _, value := range values {
			view, err := daemon.RepositoryReadiness(ctx, value.ID, false)
			if err != nil {
				return newErrorReply(remoteErrorCode(err))
			}
			result.Repositories = append(result.Repositories, view)
		}
		return api.NewContentReply(result)
	case "fetch":
		id, valid := parseRepo()
		if !valid {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		view, err := daemon.RepositoryReadiness(ctx, id, true)
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		return api.NewContentReply(view)
	case "github":
		id, valid := parseRepo()
		if !valid {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		if err := daemon.BindProjectRepositoryGitHub(ctx, id); err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		view, err := daemon.RepositoryReadiness(ctx, id, false)
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		view.PublicationState = "ready"
		view.ReadinessMessage = "Repository identity verified through the GitHub connection. Publication permissions are checked for each operation."
		return api.NewContentReply(view)
	case "add":
		id, valid := parseRepo()
		project, projectValid := parseProject()
		if !valid || !projectValid {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		value, err := registerProjectRepository(ctx, daemon.store, kernel.NewProjectRepository{ID: id, ProjectID: project, Name: input.Name, Root: input.Root, BaseRef: input.BaseRef}, at)
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		return api.NewContentReply(repositoryDTO(value))
	case "name", "base", "default", "enabled", "remove":
		id, valid := parseRepo()
		expected, revisionErr := kernel.NewRevision(int64(input.ExpectedRevision))
		if !valid || revisionErr != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		var value kernel.ProjectRepository
		switch input.Action {
		case "name":
			value, err = daemon.store.UpdateProjectRepositoryName(ctx, id, expected, input.Name, at)
		case "base":
			value, err = updateRepositoryBase(ctx, daemon.store, id, expected, input.BaseRef, at)
		case "default":
			value, err = daemon.store.SetProjectRepositoryDefault(ctx, id, expected, at)
		case "enabled":
			if input.Enabled == nil {
				return newErrorReply(api.RemoteInvalidRequest)
			}
			value, err = daemon.store.SetProjectRepositoryEnabled(ctx, id, expected, *input.Enabled, at)
		case "remove":
			err = daemon.store.RemoveProjectRepository(ctx, id, expected)
		}
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		if input.Action == "remove" {
			return api.NewContentReply(struct{}{})
		}
		return api.NewContentReply(repositoryDTO(value))
	}
	return newErrorReply(api.RemoteInvalidRequest)
}

func (daemon *Daemon) setProjectLimits(ctx context.Context, call api.Call) api.Reply {
	input, ok := call.ProjectLimitsInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	id, err := parseProjectID(input.ProjectID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	expected, err := kernel.NewRevision(int64(input.ExpectedRevision))
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	at, err := daemon.timestamp()
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	project, err := daemon.store.SetProjectLimitsWithTokens(ctx, id, expected, input.RunBudget, input.MaxRunSeconds, input.TokenBudget, at)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	return daemon.mutation(ctx, project.Revision)
}

func (daemon *Daemon) createAgent(ctx context.Context, call api.Call) api.Reply {
	input, ok := call.CreateAgentInput()
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
	provider, err := kernel.ParseProvider(input.Provider)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	var accountID kernel.AccountID
	if input.AccountID != "" {
		decoded, decodeErr := parseID(input.AccountID)
		if decodeErr != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		if accountID, err = kernel.AccountIDFromBytes(decoded); err != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
	}
	at, err := daemon.timestamp()
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	agent, err := daemon.store.CreateAgent(ctx, kernel.NewAgent{
		ID: id, ProjectID: projectID, Name: input.Name, Role: role,
		Provider:        provider,
		Model:           input.Model,
		ReasoningEffort: input.ReasoningEffort,
		AccountID:       accountID,
		ToolBudgetLimit: input.ToolBudgetLimit,
	}, at)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	return daemon.mutation(ctx, agent.Revision)
}

func (daemon *Daemon) setAgentIdlePolicy(ctx context.Context, call api.Call) api.Reply {
	input, ok := call.AgentIdlePolicyInput()
	if !ok || input.ExpectedRevision > uint64(^uint64(0)>>1) {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	id, err := parseAgentID(input.AgentID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	expected, err := kernel.NewRevision(int64(input.ExpectedRevision))
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	policy := kernel.IdlePolicy(input.Policy)
	budget := uint32(input.RunBudget)
	at, err := daemon.timestamp()
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	agent, err := daemon.store.UpdateAgent(ctx, id, expected, kernel.AgentPatch{IdlePolicy: &policy, IdleAfterSeconds: &input.AfterSeconds, IdleInstruction: &input.Instruction, IdleRunBudget: &budget}, at)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	daemon.notifyScheduler()
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
	var repositoryID kernel.RepositoryID
	if input.RepositoryID != "" {
		raw, parseErr := parseID(input.RepositoryID)
		if parseErr != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		repositoryID, err = kernel.RepositoryIDFromBytes(raw)
		if err != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
	}
	agentID, err := parseOptionalAgentID(input.AssignedAgentID)
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
	spec, err := newTaskSpec(id, projectID, repositoryID, agentID, incarnationID, input.Title, input.Body, input.Priority, input.Prerequisites, input.ConflictPaths)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	if err := prepareTaskEnqueue(ctx, daemon.store, spec, false); err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	task, err := daemon.store.EnqueueTask(ctx, spec, at)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	daemon.notifyScheduler()
	return daemon.mutation(ctx, task.Revision)
}

func newTaskSpec(id kernel.TaskID, projectID kernel.ProjectID, repositoryID kernel.RepositoryID, agentID kernel.AgentID, incarnationID kernel.IncarnationID, title, body string, priority int64, prerequisites []api.TaskPrerequisiteInput, paths []string) (kernel.NewTask, error) {
	spec := kernel.NewTask{ID: id, ProjectID: projectID, RepositoryID: repositoryID, AssignedAgentID: agentID, IncarnationID: incarnationID, Title: title, Body: body, Priority: priority, ConflictPaths: append([]string(nil), paths...)}
	spec.Prerequisites = make([]kernel.TaskPrerequisite, 0, len(prerequisites))
	for _, input := range prerequisites {
		taskID, err := parseTaskID(input.TaskID)
		if err != nil {
			return kernel.NewTask{}, err
		}
		workRevision, err := kernel.NewRevision(int64(input.WorkRevision))
		if err != nil {
			return kernel.NewTask{}, err
		}
		spec.Prerequisites = append(spec.Prerequisites, kernel.TaskPrerequisite{TaskID: taskID, WorkRevision: workRevision})
	}
	return spec, nil
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
	if enabled {
		daemon.notifyScheduler()
	}
	return mutationReply(state.Head, state.Revision)
}

func (daemon *Daemon) setCapacity(ctx context.Context, call api.Call) api.Reply {
	expected, capacity, ok := call.Capacity()
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
	state, err := daemon.store.SetCapacity(ctx, revision, capacity, at)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	daemon.notifyScheduler()
	return mutationReply(state.Head, state.Revision)
}

func (daemon *Daemon) proposeOutcome(ctx context.Context, call api.Call) (api.Reply, *liveAttempt) {
	digest, ok := call.AttemptDigest()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest), nil
	}
	kDigest, err := attemptDigest(digest)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest), nil
	}
	// Keep the exact live owner so a refusal caused by a concurrent durable
	// finalization wakes its lifecycle loop immediately. The refusal remains
	// the caller's error; this lookup carries no authority and is not exposed.
	live := daemon.liveAttemptForDigest(kDigest)
	proposal, err := proposalForCall(call)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest), nil
	}
	at, err := daemon.timestamp()
	if err != nil {
		return newErrorReply(api.RemoteInternal), nil
	}
	daemon.operationMu.Lock()
	// Source validation and the durable proposal share one linearization gate.
	// The second validation is a checked snapshot fence: a source mutation
	// observed between the first inspection and proposal is refused.
	if err := daemon.validateSuccessSource(ctx, live, proposal); err != nil {
		daemon.operationMu.Unlock()
		return newErrorReply(remoteErrorCode(err)), nil
	}
	if daemon.beforeSuccessProposal != nil {
		daemon.beforeSuccessProposal()
	}
	if err := daemon.validateSuccessSource(ctx, live, proposal); err != nil {
		daemon.operationMu.Unlock()
		return newErrorReply(remoteErrorCode(err)), nil
	}
	// This durable transition and the owner-side attach check share one
	// linearization gate. Whichever operation acquires it first owns the
	// running/finalizing boundary; notification carries no authority.
	// A complete outcome mutation follows the authenticated request lifetime,
	// not the live owner's short polling budget. Writer contention and durable
	// validation must not discard an otherwise valid result after two seconds.
	run, err := daemon.store.ProposeAttemptOutcome(ctx, kDigest, proposal, at)
	var attempt *liveAttempt
	if err == nil {
		daemon.attemptMu.Lock()
		attempt = daemon.attempts[run.ID]
		if attempt != nil {
			attempt.outcomeReceiptPending = true
			attempt.pendingOutcome = nil
		}
		daemon.attemptMu.Unlock()
		// The commit completed before ProposeAttemptOutcome returned. The owner
		// notification is deliberately best-effort and carries no authority or
		// state payload; it only shortens the next durable Store poll.
		daemon.notifyRun(run.ID)
	}
	if err != nil {
		var refusal *kernel.OutcomeRefusal
		if live != nil && errors.As(err, &refusal) {
			// Only the exact bearer owner receives a refusal action. A foreign
			// bearer remains a plain API error and cannot terminate this run.
			// Retain the first refused proposal only after the kernel has
			// correlated this exact call to a durable refusal. A successful
			// proposal already cleared this slot while holding operationMu.
			if live.pendingOutcome == nil {
				copy := proposal
				live.pendingOutcome = &copy
			}
			live.notifyOutcomeRefusal(refusal)
		} else if live != nil {
			// Unauthorized/non-refusal responses include scope cancellation and
			// credential revocation. Never retain a stale provider proposal across
			// those durable boundaries.
			live.pendingOutcome = nil
		}
		daemon.operationMu.Unlock()
		return newErrorReply(remoteErrorCode(err)), nil
	}
	daemon.operationMu.Unlock()
	return daemon.mutation(ctx, run.Revision), attempt
}

func (daemon *Daemon) clearOutcomeReceipt(attempt *liveAttempt) {
	if daemon == nil || attempt == nil {
		return
	}
	daemon.operationMu.Lock()
	attempt.outcomeReceiptPending = false
	daemon.operationMu.Unlock()
	attempt.notify()
}

func attemptOutcomeCall(kind api.CallKind) bool {
	return kind == api.CallSucceed || kind == api.CallBlock || kind == api.CallFail
}

// sendBack returns a finished task to its queue: through an orchestrator's
// attempt credential, bound to its project, or as the operator, who names any
// task at its current revision.
func (daemon *Daemon) sendBack(ctx context.Context, call api.Call) api.Reply {
	input, ok := call.SendBackInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	taskID, err := parseTaskID(input.TaskID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	at, err := daemon.timestamp()
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	// An attempt is authenticated before anything is read on its behalf, so a
	// credential the kernel would refuse learns nothing about any task.
	var digest kernel.AttemptDigest
	var authority kernel.AttemptAuthority
	raw, attempt := call.AttemptDigest()
	if attempt {
		kDigest, err := attemptDigest(raw)
		if err != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		authority, err = daemon.store.AuthenticateAttempt(ctx, kDigest)
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		if authority.Role != kernel.RoleOrchestrator {
			return newErrorReply(api.RemoteUnauthorized)
		}
		digest = kDigest
	}
	// The body a send-back leaves is the provider's whole task, so it must fit
	// the provider of the agent that will run it, or the retry would be queued
	// only to fail at launch. The kernel edge authorizes again and writes.
	current, found, err := daemon.store.Task(ctx, taskID)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	if !found {
		return newErrorReply(api.RemoteNotFound)
	}
	// Another project's task is not this orchestrator's to read further,
	// let alone measure against its provider.
	if attempt && current.ProjectID != authority.ProjectID {
		return newErrorReply(api.RemoteUnauthorized)
	}
	if current.AssignedAgentID == (kernel.AgentID{}) {
		// Never claimed: there is no run to correct.
		return newErrorReply(api.RemoteConflict)
	}
	agent, found, err := daemon.store.Agent(ctx, current.AssignedAgentID)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	if !found {
		return newErrorReply(api.RemoteConflict)
	}
	if _, _, err := provider.PrepareTask(agent.Provider, []byte(kernel.SentBackBody(current, input.Note))); err != nil {
		return newErrorReply(api.RemoteTooLarge)
	}
	var task kernel.Task
	if attempt {
		task, err = daemon.store.SendBackTaskForAttempt(ctx, digest, taskID, input.Note, at)
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
	} else {
		task, err = daemon.store.SendBackTask(ctx, taskID, current.Revision, input.Note, at)
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
	}
	// The task is queued again; the scheduler should not wait for its tick.
	daemon.notifyScheduler()
	return daemon.mutation(ctx, task.Revision)
}

func (daemon *Daemon) requestHuman(ctx context.Context, call api.Call) api.Reply {
	digest, ok := call.AttemptDigest()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	kDigest, err := attemptDigest(digest)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	input, ok := call.HumanQuestionInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	rawKey, err := parseID(input.IdempotencyKey)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	var key [kernel.IDBytes]byte
	copy(key[:], rawKey)
	at, err := daemon.timestamp()
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	authority, authErr := daemon.store.AuthenticateAttempt(ctx, kDigest)
	if authErr != nil {
		return newErrorReply(remoteErrorCode(authErr))
	}
	question := kernel.NewHumanQuestion{
		IdempotencyKey: key,
		QuestionText:   input.Question,
		Options:        input.Options,
		ReuseExisting:  input.ReuseExisting,
	}
	var request kernel.HumanRequest
	if authority.Provider != kernel.ProviderShell {
		request, err = daemon.store.CreateHumanQuestionAndYieldForAttempt(ctx, kDigest, question, at)
	} else {
		request, err = daemon.store.CreateHumanQuestionForAttempt(ctx, kDigest, question, at)
	}
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	// A replayed idempotency key returns the earlier question; only a question
	// that opened just now wakes the phones.
	if request.CreatedAt == at {
		go daemon.notifyPush(context.WithoutCancel(ctx), pushClient)
	}
	return daemon.mutation(ctx, request.Revision)
}

func (daemon *Daemon) overseerDigest(ctx context.Context, call api.Call) (kernel.AttemptAuthority, kernel.AttemptDigest, *api.Reply) {
	raw, ok := call.AttemptDigest()
	if !ok {
		failure := newErrorReply(api.RemoteInvalidRequest)
		return kernel.AttemptAuthority{}, kernel.AttemptDigest{}, &failure
	}
	digest, err := attemptDigest(raw)
	if err != nil {
		failure := newErrorReply(api.RemoteInvalidRequest)
		return kernel.AttemptAuthority{}, kernel.AttemptDigest{}, &failure
	}
	authority, err := daemon.store.AuthenticateAttempt(ctx, digest)
	if err != nil || authority.Role != kernel.RoleOrchestrator {
		if err == nil {
			err = kernel.ErrUnauthorized
		}
		failure := newErrorReply(remoteErrorCode(err))
		return kernel.AttemptAuthority{}, kernel.AttemptDigest{}, &failure
	}
	return authority, digest, nil
}

func (daemon *Daemon) overseerSnapshot(ctx context.Context, call api.Call) api.Reply {
	authority, digest, failure := daemon.overseerDigest(ctx, call)
	if failure != nil {
		return *failure
	}
	input, ok := call.OverseerSnapshotInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	request := kernel.OverseerSnapshotRequest{Offset: input.Offset, TextOffset: input.TextOffset}
	expectedHead, err := kernel.NewEventSequence(int64(input.ExpectedHead))
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	request.ExpectedHead = expectedHead
	if input.TaskID != "" {
		id, err := parseTaskID(input.TaskID)
		if err != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		request.TaskID = &id
	}
	snapshot, err := daemon.store.OverseerSnapshotForAttempt(ctx, digest, request)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	daemon.attemptMu.Lock()
	live := daemon.attempts[authority.RunID]
	daemon.attemptMu.Unlock()
	if live == nil {
		return newErrorReply(api.RemoteUnavailable)
	}
	projected, err := projectOverseerSnapshot(snapshot)
	if err != nil {
		return newErrorReply(api.RemoteUnavailable)
	}
	reply, err := api.NewOverseerSnapshotReply(projected)
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	return reply
}

func (daemon *Daemon) overseerEnqueueTask(ctx context.Context, call api.Call) api.Reply {
	authority, digest, failure := daemon.overseerDigest(ctx, call)
	if failure != nil {
		return *failure
	}
	input, ok := call.OverseerTaskCreateInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	id, err := parseTaskID(input.ID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	agentID, err := parseOptionalAgentID(input.AssignedAgentID)
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
	spec, err := newTaskSpec(id, authority.ProjectID, kernel.RepositoryID{}, agentID, incarnationID, input.Title, input.Body, input.Priority, input.Prerequisites, input.ConflictPaths)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	if err := prepareTaskEnqueue(ctx, daemon.store, spec, true); err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	task, err := daemon.store.EnqueueTaskForOverseer(ctx, digest, spec, at)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	daemon.notifyScheduler()
	return daemon.mutation(ctx, task.Revision)
}

func (daemon *Daemon) overseerUpdateTask(ctx context.Context, call api.Call) api.Reply {
	_, digest, failure := daemon.overseerDigest(ctx, call)
	if failure != nil {
		return *failure
	}
	input, ok := call.OverseerTaskUpdateInput()
	if !ok || input.ExpectedRevision > uint64(^uint64(0)>>1) {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	id, err := parseTaskID(input.TaskID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	expected, err := kernel.NewRevision(int64(input.ExpectedRevision))
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	var assignedAgentID *kernel.AgentID
	if input.AssignedAgentID != nil {
		agentID, err := parseAgentID(*input.AssignedAgentID)
		if err != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		assignedAgentID = &agentID
	}
	if input.Retry {
		// A zero agent keeps the task's current worker.
		var assigned kernel.AgentID
		if assignedAgentID != nil {
			assigned = *assignedAgentID
		}
		if err := prepareTaskRetry(ctx, daemon.store, id, expected, assigned); err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		at, err := daemon.timestamp()
		if err != nil {
			return newErrorReply(api.RemoteInternal)
		}
		task, err := daemon.store.RetryTaskForOverseer(ctx, digest, id, expected, assigned, at)
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		daemon.notifyScheduler()
		return daemon.mutation(ctx, task.Revision)
	}
	patch := kernel.TaskPatch{Title: input.Title, Body: input.Body, Priority: input.Priority, AssignedAgentID: assignedAgentID, Cancel: input.Cancel}
	if err := daemon.store.AuthorizeWorkerTaskForOverseer(ctx, digest, id); err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	if err := prepareQueuedTaskPatch(ctx, daemon.store, id, expected, patch); err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	at, err := daemon.timestamp()
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	task, err := daemon.store.UpdateTaskForOverseer(ctx, digest, id, expected, patch, at)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	daemon.notifyScheduler()
	return daemon.mutation(ctx, task.Revision)
}

func (daemon *Daemon) overseerUpdateAgent(ctx context.Context, call api.Call) api.Reply {
	_, digest, failure := daemon.overseerDigest(ctx, call)
	if failure != nil {
		return *failure
	}
	input, ok := call.OverseerAgentUpdateInput()
	if !ok || input.ExpectedRevision > uint64(^uint64(0)>>1) {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	id, err := parseAgentID(input.AgentID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	expected, err := kernel.NewRevision(int64(input.ExpectedRevision))
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	if input.Paused == nil && input.Archived == nil || input.Paused != nil && input.Archived != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	at, err := daemon.timestamp()
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	agent, err := daemon.store.UpdateAgentForOverseer(ctx, digest, id, expected, kernel.AgentPatch{Paused: input.Paused, Archived: input.Archived}, at)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	daemon.notifyScheduler()
	return daemon.mutation(ctx, agent.Revision)
}

func (daemon *Daemon) operatorUpdateTask(ctx context.Context, call api.Call) api.Reply {
	input, ok := call.OverseerTaskUpdateInput()
	if !ok || input.ExpectedRevision > uint64(^uint64(0)>>1) {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	id, err := parseTaskID(input.TaskID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	expected, err := kernel.NewRevision(int64(input.ExpectedRevision))
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	if input.PublicationState != "" {
		at, err := daemon.timestamp()
		if err != nil {
			return newErrorReply(api.RemoteInternal)
		}
		task, err := daemon.store.SettlePublicationTask(ctx, id, expected, input.PublicationState, at)
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		return daemon.mutation(ctx, task.Revision)
	}
	if input.RemoveAttachments {
		task, err := daemon.store.RemoveTaskAttachments(ctx, id, expected)
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		return daemon.mutation(ctx, task.Revision)
	}
	var assigned *kernel.AgentID
	if input.AssignedAgentID != nil {
		value, err := parseAgentID(*input.AssignedAgentID)
		if err != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		assigned = &value
	}
	if input.Retry {
		var value kernel.AgentID
		if assigned != nil {
			value = *assigned
		}
		if err := prepareTaskRetry(ctx, daemon.store, id, expected, value); err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		at, err := daemon.timestamp()
		if err != nil {
			return newErrorReply(api.RemoteInternal)
		}
		task, err := daemon.store.RetryTaskForOperator(ctx, id, expected, value, at)
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		daemon.notifyScheduler()
		return daemon.mutation(ctx, task.Revision)
	}
	patch := kernel.TaskPatch{Title: input.Title, Body: input.Body, Priority: input.Priority, AssignedAgentID: assigned, Cancel: input.Cancel}
	if err := prepareQueuedTaskPatch(ctx, daemon.store, id, expected, patch); err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	at, err := daemon.timestamp()
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	task, err := daemon.store.UpdateTaskForOperator(ctx, id, expected, patch, at)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	daemon.notifyScheduler()
	return daemon.mutation(ctx, task.Revision)
}

func (daemon *Daemon) operatorUpdateAgent(ctx context.Context, call api.Call) api.Reply {
	input, ok := call.OverseerAgentUpdateInput()
	if !ok || input.ExpectedRevision > uint64(^uint64(0)>>1) || input.Paused == nil && input.Archived == nil || input.Paused != nil && input.Archived != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	id, err := parseAgentID(input.AgentID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	expected, err := kernel.NewRevision(int64(input.ExpectedRevision))
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	at, err := daemon.timestamp()
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	agent, err := daemon.store.UpdateAgentForOperator(ctx, id, expected, kernel.AgentPatch{Paused: input.Paused, Archived: input.Archived}, at)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	daemon.notifyScheduler()
	return daemon.mutation(ctx, agent.Revision)
}

func (daemon *Daemon) overseerStopRun(ctx context.Context, call api.Call) api.Reply {
	_, digest, failure := daemon.overseerDigest(ctx, call)
	if failure != nil {
		return *failure
	}
	input, ok := call.OverseerRunStopInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	request, err := overseerInterventionRequest(input.OperationID, input.TaskID, input.ExpectedTaskRevision, input.RunID, input.ExpectedRunRevision, kernel.TaskInterventionStop, "")
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	at, err := daemon.timestamp()
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	receipt, err := daemon.store.StopRunForAttempt(ctx, digest, request, nil, at)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	daemon.notifyScheduler()
	return daemon.interventionMutation(ctx, receipt)
}

func (daemon *Daemon) operatorStopRun(ctx context.Context, call api.Call) api.Reply {
	input, ok := call.OverseerRunStopInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	request, err := overseerInterventionRequest(input.OperationID, input.TaskID, input.ExpectedTaskRevision, input.RunID, input.ExpectedRunRevision, kernel.TaskInterventionStop, "")
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	at, err := daemon.timestamp()
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	receipt, err := daemon.store.StopRunForOperator(ctx, request, nil, at)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	daemon.notifyScheduler()
	return daemon.interventionMutation(ctx, receipt)
}

func (daemon *Daemon) overseerReplaceRun(ctx context.Context, call api.Call) api.Reply {
	_, digest, failure := daemon.overseerDigest(ctx, call)
	if failure != nil {
		return *failure
	}
	input, ok := call.OverseerRunReplaceInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	request, err := overseerInterventionRequest(input.OperationID, input.TaskID, input.ExpectedTaskRevision, input.RunID, input.ExpectedRunRevision, kernel.TaskInterventionReplace, "")
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	successorID, err := parseTaskID(input.SuccessorTaskID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	incarnationID, err := parseIncarnationID(input.SuccessorIncarnationID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	if err := daemon.prepareOverseerReplacement(ctx, request.TaskID, input.Instruction); err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	at, err := daemon.timestamp()
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	receipt, err := daemon.store.StopRunForAttempt(ctx, digest, request, &kernel.NewTask{ID: successorID, IncarnationID: incarnationID, Body: input.Instruction}, at)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	daemon.notifyScheduler()
	return daemon.interventionMutation(ctx, receipt)
}

func (daemon *Daemon) operatorReplaceRun(ctx context.Context, call api.Call) api.Reply {
	input, ok := call.OverseerRunReplaceInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	request, err := overseerInterventionRequest(input.OperationID, input.TaskID, input.ExpectedTaskRevision, input.RunID, input.ExpectedRunRevision, kernel.TaskInterventionReplace, "")
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	successorID, err := parseTaskID(input.SuccessorTaskID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	incarnationID, err := parseIncarnationID(input.SuccessorIncarnationID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	if err := daemon.prepareOverseerReplacement(ctx, request.TaskID, input.Instruction); err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	at, err := daemon.timestamp()
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	receipt, err := daemon.store.StopRunForOperator(ctx, request, &kernel.NewTask{ID: successorID, IncarnationID: incarnationID, Body: input.Instruction}, at)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	daemon.notifyScheduler()
	return daemon.interventionMutation(ctx, receipt)
}

func (daemon *Daemon) overseerMessageWorker(ctx context.Context, call api.Call) api.Reply {
	_, digest, failure := daemon.overseerDigest(ctx, call)
	if failure != nil {
		return *failure
	}
	input, ok := call.OverseerWorkerMessageInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	request, err := overseerInterventionRequest(input.OperationID, input.TaskID, input.ExpectedTaskRevision, input.RunID, input.ExpectedRunRevision, kernel.TaskInterventionMessage, input.Message)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	receipt, err := daemon.overseerIntervention(ctx, digest, request)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	return daemon.interventionMutation(ctx, receipt)
}

func (daemon *Daemon) overseerInterruptWorker(ctx context.Context, call api.Call) api.Reply {
	_, digest, failure := daemon.overseerDigest(ctx, call)
	if failure != nil {
		return *failure
	}
	input, ok := call.OverseerWorkerInterruptInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	request, err := overseerInterventionRequest(input.OperationID, input.TaskID, input.ExpectedTaskRevision, input.RunID, input.ExpectedRunRevision, kernel.TaskInterventionInterrupt, "")
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	receipt, err := daemon.overseerIntervention(ctx, digest, request)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	return daemon.interventionMutation(ctx, receipt)
}

func (daemon *Daemon) operatorMessageWorker(ctx context.Context, call api.Call) api.Reply {
	input, ok := call.OverseerWorkerMessageInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	request, err := overseerInterventionRequest(input.OperationID, input.TaskID, input.ExpectedTaskRevision, input.RunID, input.ExpectedRunRevision, kernel.TaskInterventionMessage, input.Message)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	receipt, err := daemon.operatorIntervention(ctx, request)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	return daemon.interventionMutation(ctx, receipt)
}

func (daemon *Daemon) operatorInterruptWorker(ctx context.Context, call api.Call) api.Reply {
	input, ok := call.OverseerWorkerInterruptInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	request, err := overseerInterventionRequest(input.OperationID, input.TaskID, input.ExpectedTaskRevision, input.RunID, input.ExpectedRunRevision, kernel.TaskInterventionInterrupt, "")
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	receipt, err := daemon.operatorIntervention(ctx, request)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	return daemon.interventionMutation(ctx, receipt)
}

func (daemon *Daemon) overseerReplyHuman(ctx context.Context, call api.Call) api.Reply {
	_, digest, failure := daemon.overseerDigest(ctx, call)
	if failure != nil {
		return *failure
	}
	input, ok := call.OverseerHumanReplyInput()
	if !ok || input.ExpectedRevision > uint64(^uint64(0)>>1) {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	requestID, err := parseHumanRequestID(input.RequestID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	deliveryID, err := parseHumanDeliveryID(input.OperationID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	expected, err := kernel.NewRevision(int64(input.ExpectedRevision))
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	if !terminalEffectsSupported {
		return newErrorReply(api.RemoteUnavailable)
	}
	daemon.operationMu.Lock()
	defer daemon.operationMu.Unlock()
	at, err := daemon.timestamp()
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	if handled, continuationErr := daemon.store.ResolveHumanContinuationForAttempt(ctx, digest, requestID, expected, input.Reply, at); continuationErr == nil && handled {
		// Resolution queues fresh work; wake admission immediately while keeping
		// the old attempt bearer permanently revoked.
		daemon.notifyScheduler()
		projection, _, readErr := daemon.store.HumanRequest(ctx, requestID)
		if readErr != nil {
			return newErrorReply(remoteErrorCode(readErr))
		}
		return daemon.overseerHumanReplyMutation(projection)
	} else if continuationErr != nil && !errors.Is(continuationErr, kernel.ErrNotFound) && !errors.Is(continuationErr, kernel.ErrConflict) && !errors.Is(continuationErr, kernel.ErrRevisionConflict) {
		return newErrorReply(remoteErrorCode(continuationErr))
	}
	delivery, err := daemon.store.BeginHumanReplyForAttempt(ctx, digest, requestID, expected, deliveryID, input.Reply, at)
	if err != nil {
		if terminalStoreOutcomeUnknown(err) {
			deliveryRevision, revisionErr := kernel.NewRevision(expected.Int64() + 1)
			var unknownErr error
			if revisionErr == nil {
				unknownErr = daemon.markHumanReplyUnknown(requestID, deliveryID, deliveryRevision)
			}
			return newErrorReply(remoteErrorCode(errors.Join(err, revisionErr, unknownErr)))
		}
		return newErrorReply(remoteErrorCode(err))
	}
	if delivery.RequestID != requestID || delivery.DeliveryID != deliveryID {
		return newErrorReply(api.RemoteInternal)
	}
	_, effectErr := daemon.deliverHumanReply(ctx, delivery)
	projection, err := daemon.humanReplyOutcome(requestID, effectErr)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	return daemon.overseerHumanReplyMutation(projection)
}

func (daemon *Daemon) overseerHumanReplyMutation(projection kernel.HumanRequestProjection) api.Reply {
	readCtx, cancel := context.WithTimeout(context.Background(), liveAttemptStoreTimeout)
	defer cancel()
	state, err := daemon.store.Factory(readCtx)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	reply, err := api.NewMutationReply(api.MutationResult{Head: uint64(state.Head.Int64()), Revision: uint64(projection.Revision.Int64()), HumanReply: &api.OverseerHumanReplyResult{RequestID: projection.ID.String(), State: projection.Status.String()}})
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	return reply
}

func overseerInterventionRequest(operationText, taskText string, taskRevision uint64, runText string, runRevision uint64, kind kernel.TaskInterventionKind, payload string) (kernel.TaskInterventionRequest, error) {
	operation, err := parseTaskInterventionID(operationText)
	if err != nil {
		return kernel.TaskInterventionRequest{}, err
	}
	task, err := parseTaskID(taskText)
	if err != nil {
		return kernel.TaskInterventionRequest{}, err
	}
	run, err := parseRunID(runText)
	if err != nil || taskRevision > uint64(^uint64(0)>>1) || runRevision > uint64(^uint64(0)>>1) {
		return kernel.TaskInterventionRequest{}, kernel.ErrInvalidValue
	}
	expectedTask, err := kernel.NewRevision(int64(taskRevision))
	if err != nil {
		return kernel.TaskInterventionRequest{}, err
	}
	expectedRun, err := kernel.NewRevision(int64(runRevision))
	if err != nil {
		return kernel.TaskInterventionRequest{}, err
	}
	return kernel.TaskInterventionRequest{OperationID: operation, TaskID: task, RunID: run, ExpectedTaskRevision: expectedTask, ExpectedRunRevision: expectedRun, Kind: kind, Payload: payload}, nil
}

func (daemon *Daemon) interventionMutation(_ context.Context, receipt kernel.TaskIntervention) api.Reply {
	readCtx, cancel := context.WithTimeout(context.Background(), liveAttemptStoreTimeout)
	defer cancel()
	task, found, err := daemon.store.Task(readCtx, receipt.TaskID)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	if !found {
		return newErrorReply(api.RemoteInternal)
	}
	state, err := daemon.store.Factory(readCtx)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	detail := ""
	if receipt.ResultDetail != nil {
		detail = *receipt.ResultDetail
	}
	reply, err := api.NewMutationReply(api.MutationResult{Head: uint64(state.Head.Int64()), Revision: uint64(task.Revision.Int64()), Intervention: &api.OverseerInterventionResult{OperationID: receipt.OperationID.String(), State: receipt.State.String(), Detail: detail}})
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	return reply
}

func (daemon *Daemon) prepareOverseerReplacement(ctx context.Context, taskID kernel.TaskID, instruction string) error {
	task, found, err := daemon.store.Task(ctx, taskID)
	if err != nil {
		return err
	}
	if !found {
		return kernel.ErrNotFound
	}
	agent, found, err := daemon.store.Agent(ctx, task.AssignedAgentID)
	if err != nil {
		return err
	}
	if !found {
		return kernel.ErrCorruptState
	}
	_, _, err = provider.PrepareTask(agent.Provider, []byte(instruction))
	return err
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

func (daemon *Daemon) notifyRun(runID kernel.RunID) {
	if daemon == nil {
		return
	}
	daemon.attemptMu.Lock()
	attempt := daemon.attempts[runID]
	daemon.attemptMu.Unlock()
	if attempt != nil {
		attempt.notify()
	}
}

func (daemon *Daemon) registerSupervisor(parent context.Context) (*supervisorRegistration, error) {
	if daemon == nil || parent == nil {
		return nil, fmt.Errorf("%w: invalid supervisor", kernel.ErrInvalidValue)
	}
	ctx, cancel := context.WithCancel(parent)
	registration := &supervisorRegistration{ctx: ctx, cancel: cancel, done: make(chan struct{})}
	daemon.attemptMu.Lock()
	defer daemon.attemptMu.Unlock()
	if daemon.closing {
		cancel()
		return nil, ErrTerminalClosed
	}
	if daemon.supervisors == nil {
		daemon.supervisors = make(map[*supervisorRegistration]struct{})
	}
	daemon.supervisors[registration] = struct{}{}
	return registration, nil
}

func (daemon *Daemon) endSupervisor(registration *supervisorRegistration, result error) {
	if daemon == nil || registration == nil {
		return
	}
	registration.mu.Lock()
	registration.result = result
	close(registration.done)
	registration.mu.Unlock()
	registration.cancel()
	daemon.attemptMu.Lock()
	delete(daemon.supervisors, registration)
	daemon.attemptMu.Unlock()
}

func (registration *supervisorRegistration) wait() error {
	if registration == nil {
		return nil
	}
	<-registration.done
	registration.mu.Lock()
	defer registration.mu.Unlock()
	return registration.result
}

// Close stops accepting live attempts and synchronously joins every owner.
// The API listener and Store are owned by their callers and are intentionally
// not closed here.
func (daemon *Daemon) Close() error {
	if daemon == nil {
		return nil
	}
	if daemon.cleanupCancel != nil {
		daemon.cleanupCancel()
	}
	return errors.Join(daemon.closeBrowsers(), daemon.closeLiveAttempts())
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
	case errors.Is(err, kernel.ErrInvalidValue):
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

func (daemon *Daemon) selectAgentModel(ctx context.Context, call api.Call) api.Reply {
	input, ok := call.AgentModelSelectInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	agentID, err := parseAgentID(input.AgentID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	expected, err := kernel.NewRevision(int64(input.ExpectedRevision))
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	agent, found, err := daemon.store.Agent(ctx, agentID)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	if !found {
		return newErrorReply(api.RemoteNotFound)
	}
	if agent.Role != kernel.RoleWorker {
		return newErrorReply(api.RemoteConflict)
	}
	at, err := daemon.timestamp()
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	updated, err := daemon.store.UpdateAgent(ctx, agentID, expected, kernel.AgentPatch{Model: &input.Model, ReasoningEffort: &input.ReasoningEffort}, at)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	return daemon.mutation(ctx, updated.Revision)
}
