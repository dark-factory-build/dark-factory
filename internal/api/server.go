//go:build darwin || linux

package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// CallKind is the closed set of requests accepted by the local API.
type CallKind uint8

const (
	CallHealth CallKind = iota + 1
	CallSnapshot
	CallCreateProject
	CallProjectRepository
	CallProjectLimits
	CallCreateAgent
	CallAgentIdlePolicy
	CallEnqueueTask
	CallSetDispatch
	CallSetCapacity
	CallCompactStorage
	CallAccountsDiscover
	CallAccountLink
	CallAgentSelectAccount
	CallAgentSelectModel
	CallAgentPaths
	CallAttemptTask
	CallAttemptSource
	CallSucceed
	CallBlock
	CallFail
	CallRequestHuman
	CallPeerStatus
	CallPeerAsk
	CallPeerAnswer
	CallTerminalObserve
	CallOperatorTerminalObserve
	CallSendBack
	CallSendBackTask
	CallTaskRecovery
	CallTaskRead
	CallWebStatus
	CallWebListClients
	CallWebRevokeClient
	CallRemoteStatus
	CallOverseerSnapshot
	CallOverseerEnqueueTask
	CallOverseerUpdateTask
	CallOverseerUpdateAgent
	CallOverseerStopRun
	CallOverseerReplaceRun
	CallOverseerMessageWorker
	CallOverseerInterruptWorker
	CallOverseerReplyHuman
	CallOperatorUpdateTask
	CallOperatorUpdateAgent
	CallOperatorStopRun
	CallOperatorReplaceRun
	CallOperatorMessageWorker
	CallOperatorInterruptWorker
	CallOperatorWorkerOperation
	CallHumanRequests
	CallHumanReply
	CallContentCreate
	CallContentRevise
	CallContentDeprecate
	CallContentList
	CallContentRead
	CallContentBody
	CallContentEvidence
	CallContentAttach
	CallContentEvidenceList
	CallContentAttachments
	CallOutcomeWrite
	CallOutcomeRead
	CallOutcomeList
	CallGitHubConnection
	CallIntake
	CallProductionObserve
	CallDeliveryReconcile
	CallMaintainer
)

// AttemptDigest is the SHA-256 digest of one raw attempt bearer. The bearer is
// erased before Receive returns and is never representable outside this package.
type AttemptDigest struct{ value [sha256.Size]byte }

func (digest AttemptDigest) Bytes() [sha256.Size]byte { return digest.value }
func (AttemptDigest) String() string                  { return "AttemptDigest(<redacted>)" }
func (AttemptDigest) GoString() string                { return "AttemptDigest(<redacted>)" }

func digestAttemptCredential(bearer credential) AttemptDigest {
	return AttemptDigest{value: sha256.Sum256(bearer[:])}
}

// Call is an immutable decoded request. Only the accessor matching Kind
// returns true.
type Call struct {
	maintainer          MaintainerInput
	githubConnection    GitHubConnectionInput
	intake              IntakeInput
	attempt             bool
	terminalObserve     TerminalObserveInput
	peerIncludeTargets  bool
	kind                CallKind
	digest              AttemptDigest
	project             CreateProjectInput
	repository          ProjectRepositoryInput
	projectLimits       ProjectLimitsInput
	agent               CreateAgentInput
	agentIdlePolicy     AgentIdlePolicyInput
	agentPaths          AgentPathsInput
	task                EnqueueTaskInput
	humanQuestion       HumanQuestionInput
	peerQuestion        PeerQuestionInput
	peerAnswer          PeerAnswerInput
	peerStatusOffset    uint64
	peerTargetOffset    uint64
	peerExpectedHead    uint64
	sendBack            SendBackInput
	taskRecovery        TaskRecoveryInput
	taskRead            TaskReadInput
	overseerTask        OverseerTaskCreateInput
	overseerSnapshot    OverseerSnapshotInput
	overseerTaskEdit    OverseerTaskUpdateInput
	overseerAgent       OverseerAgentUpdateInput
	overseerRun         OverseerRunStopInput
	overseerReplace     OverseerRunReplaceInput
	overseerMessage     OverseerWorkerMessageInput
	overseerInterrupt   OverseerWorkerInterruptInput
	workerOperation     WorkerOperationInput
	overseerReply       OverseerHumanReplyInput
	humanReply          OverseerHumanReplyInput
	content             ContentInput
	contentList         ContentListInput
	contentRead         ContentReadInput
	contentBody         ContentBodyInput
	contentEvidence     ContentEvidenceInput
	contentAttach       ContentAttachInput
	contentEvidenceList ContentEvidenceListInput
	contentAttachments  ContentAttachmentsInput
	outcomeWrite        OutcomeWriteInput
	outcomeRead         OutcomeReadInput
	outcomeList         OutcomeListInput
	production          ProductionInput
	delivery            DeliveryInput
	webClient           WebClientRevocationInput
	webAfter            string
	expectedRevision    uint64
	enabled             bool
	capacity            uint16
	account             AccountLinkInput
	selection           AgentAccountSelectInput
	accountsOffset      uint32
	modelSelection      AgentModelSelectInput
	text                string
	sourceTaskID        string
}

func (call Call) IntakeInput() (IntakeInput, bool) { return call.intake, call.kind == CallIntake }
func (call Call) DeliveryInput() (DeliveryInput, bool) {
	return call.delivery, call.kind == CallDeliveryReconcile
}

func (call Call) GitHubConnectionInput() (GitHubConnectionInput, bool) {
	return call.githubConnection, call.kind == CallGitHubConnection
}

func (call Call) MaintainerInput() (MaintainerInput, bool) {
	return call.maintainer, call.kind == CallMaintainer
}

func (call Call) Kind() CallKind { return call.kind }
func (call Call) String() string { return "Call(<redacted>)" }
func (call Call) GoString() string {
	return "Call(<redacted>)"
}

func (call Call) AttemptDigest() (AttemptDigest, bool) {
	if !call.attempt {
		return AttemptDigest{}, false
	}
	switch call.kind {
	case CallMaintainer, CallAttemptTask, CallAttemptSource, CallSucceed, CallBlock, CallFail, CallRequestHuman, CallPeerStatus, CallPeerAsk, CallPeerAnswer, CallTerminalObserve, CallSendBack, CallOverseerSnapshot, CallOverseerEnqueueTask, CallOverseerUpdateTask, CallOverseerUpdateAgent, CallOverseerStopRun, CallOverseerReplaceRun, CallOverseerMessageWorker, CallOverseerInterruptWorker, CallOverseerReplyHuman, CallContentCreate, CallContentRevise, CallContentDeprecate, CallContentList, CallContentRead, CallContentBody, CallContentEvidence, CallContentAttach, CallContentEvidenceList, CallContentAttachments, CallOutcomeWrite, CallOutcomeRead, CallOutcomeList:
		return call.digest, true
	default:
		return AttemptDigest{}, false
	}
}

func (call Call) AttemptSourceTaskID() (string, bool) {
	return call.sourceTaskID, call.kind == CallAttemptSource
}

func (call Call) OverseerTaskCreateInput() (OverseerTaskCreateInput, bool) {
	return call.overseerTask, call.kind == CallOverseerEnqueueTask
}

func (call Call) OverseerSnapshotInput() (OverseerSnapshotInput, bool) {
	return call.overseerSnapshot, call.kind == CallOverseerSnapshot
}

func (call Call) OverseerTaskUpdateInput() (OverseerTaskUpdateInput, bool) {
	return call.overseerTaskEdit, call.kind == CallOverseerUpdateTask || call.kind == CallOperatorUpdateTask
}

func (call Call) OverseerAgentUpdateInput() (OverseerAgentUpdateInput, bool) {
	return call.overseerAgent, call.kind == CallOverseerUpdateAgent || call.kind == CallOperatorUpdateAgent
}

func (call Call) OverseerRunStopInput() (OverseerRunStopInput, bool) {
	return call.overseerRun, call.kind == CallOverseerStopRun || call.kind == CallOperatorStopRun
}

func (call Call) OverseerRunReplaceInput() (OverseerRunReplaceInput, bool) {
	return call.overseerReplace, call.kind == CallOverseerReplaceRun || call.kind == CallOperatorReplaceRun
}

func (call Call) OverseerWorkerMessageInput() (OverseerWorkerMessageInput, bool) {
	return call.overseerMessage, call.kind == CallOverseerMessageWorker || call.kind == CallOperatorMessageWorker
}

func (call Call) OverseerWorkerInterruptInput() (OverseerWorkerInterruptInput, bool) {
	return call.overseerInterrupt, call.kind == CallOverseerInterruptWorker || call.kind == CallOperatorInterruptWorker
}

func (call Call) WorkerOperationInput() (WorkerOperationInput, bool) {
	return call.workerOperation, call.kind == CallOperatorWorkerOperation
}

func (call Call) OverseerHumanReplyInput() (OverseerHumanReplyInput, bool) {
	return call.overseerReply, call.kind == CallOverseerReplyHuman
}

func (call Call) HumanReplyInput() (OverseerHumanReplyInput, bool) {
	return call.humanReply, call.kind == CallHumanReply
}

func (call Call) CreateProjectInput() (CreateProjectInput, bool) {
	return call.project, call.kind == CallCreateProject
}
func (call Call) ProjectRepositoryInput() (ProjectRepositoryInput, bool) {
	return call.repository, call.kind == CallProjectRepository
}

func (call Call) ProjectLimitsInput() (ProjectLimitsInput, bool) {
	return call.projectLimits, call.kind == CallProjectLimits
}

func (call Call) CreateAgentInput() (CreateAgentInput, bool) {
	return call.agent, call.kind == CallCreateAgent
}

func (call Call) AgentIdlePolicyInput() (AgentIdlePolicyInput, bool) {
	return call.agentIdlePolicy, call.kind == CallAgentIdlePolicy
}

func (call Call) AgentPathsInput() (AgentPathsInput, bool) {
	return call.agentPaths, call.kind == CallAgentPaths
}

func (call Call) EnqueueTaskInput() (EnqueueTaskInput, bool) {
	return call.task, call.kind == CallEnqueueTask
}

func (call Call) Dispatch() (uint64, bool, bool) {
	return call.expectedRevision, call.enabled, call.kind == CallSetDispatch
}

func (call Call) Capacity() (uint64, uint16, bool) {
	return call.expectedRevision, call.capacity, call.kind == CallSetCapacity
}

func (call Call) AccountLinkInput() (AccountLinkInput, bool) {
	return call.account, call.kind == CallAccountLink
}

func (call Call) AgentAccountSelectInput() (AgentAccountSelectInput, bool) {
	return call.selection, call.kind == CallAgentSelectAccount
}

func (call Call) Result() (string, bool) {
	return call.text, call.kind == CallSucceed
}

func (call Call) Detail() (string, bool) {
	return call.text, call.kind == CallBlock || call.kind == CallFail
}

func (call Call) HumanQuestionInput() (HumanQuestionInput, bool) {
	return call.humanQuestion, call.kind == CallRequestHuman
}
func (call Call) PeerQuestionInput() (PeerQuestionInput, bool) {
	return call.peerQuestion, call.kind == CallPeerAsk
}
func (call Call) PeerAnswerInput() (PeerAnswerInput, bool) {
	return call.peerAnswer, call.kind == CallPeerAnswer
}
func (call Call) TerminalObserveInput() (TerminalObserveInput, bool) {
	return call.terminalObserve, call.kind == CallTerminalObserve || call.kind == CallOperatorTerminalObserve
}
func (call Call) PeerStatusPage() (uint64, uint64, uint64, bool, bool) {
	return call.peerStatusOffset, call.peerTargetOffset, call.peerExpectedHead, call.peerIncludeTargets, call.kind == CallPeerStatus
}

// SendBackInput is the task and note of a send-back, from an orchestrator's
// attempt (send_back) or the operator (send_back_task).
func (call Call) SendBackInput() (SendBackInput, bool) {
	return call.sendBack, call.kind == CallSendBack || call.kind == CallSendBackTask
}

func (call Call) TaskRecoveryInput() (TaskRecoveryInput, bool) {
	return call.taskRecovery, call.kind == CallTaskRecovery
}

func (call Call) TaskReadInput() (TaskReadInput, bool) {
	return call.taskRead, call.kind == CallTaskRead
}

func (call Call) WebClientRevocationInput() (WebClientRevocationInput, bool) {
	return call.webClient, call.kind == CallWebRevokeClient
}

func (call Call) WebListAfter() (string, bool) {
	return call.webAfter, call.kind == CallWebListClients
}
func (call Call) ContentInput() (ContentInput, bool) {
	return call.content, call.kind == CallContentCreate || call.kind == CallContentRevise || call.kind == CallContentDeprecate
}
func (call Call) ContentListInput() (ContentListInput, bool) {
	return call.contentList, call.kind == CallContentList
}
func (call Call) ContentReadInput() (ContentReadInput, bool) {
	return call.contentRead, call.kind == CallContentRead
}
func (call Call) ContentBodyInput() (ContentBodyInput, bool) {
	return call.contentBody, call.kind == CallContentBody
}
func (call Call) ContentEvidenceInput() (ContentEvidenceInput, bool) {
	return call.contentEvidence, call.kind == CallContentEvidence
}
func (call Call) ContentAttachInput() (ContentAttachInput, bool) {
	return call.contentAttach, call.kind == CallContentAttach
}
func (call Call) ContentEvidenceListInput() (ContentEvidenceListInput, bool) {
	return call.contentEvidenceList, call.kind == CallContentEvidenceList
}
func (call Call) ContentAttachmentsInput() (ContentAttachmentsInput, bool) {
	return call.contentAttachments, call.kind == CallContentAttachments
}
func (call Call) OutcomeWriteInput() (OutcomeWriteInput, bool) {
	return call.outcomeWrite, call.kind == CallOutcomeWrite
}
func (call Call) OutcomeReadInput() (OutcomeReadInput, bool) {
	return call.outcomeRead, call.kind == CallOutcomeRead
}
func (call Call) OutcomeListInput() (OutcomeListInput, bool) {
	return call.outcomeList, call.kind == CallOutcomeList
}

func (call Call) ProductionInput() (ProductionInput, bool) {
	return call.production, call.kind == CallProductionObserve
}

type replyKind uint8

const (
	replyHealth replyKind = iota + 1
	replySnapshot
	replyAgentPaths
	replyMutation
	replyAttemptTask
	replyAttemptSource
	replyWebStatus
	replyWebClients
	replyWebRevoke
	replyRemoteStatus
	replyContent
	replyOverseerSnapshot
	replyPeerStatus
	replyTerminalObservation
	replyAccounts
	replyTaskRecovery
	replyTaskText
	replyWorkerOperation
	replyHumanRequests
	replyError
)

// Reply is constructed only through its fixed reply constructors.
type Reply struct {
	terminalObservation TerminalObservation
	kind                replyKind
	health              HealthStatus
	snapshot            DashboardSnapshot
	agentPaths          AgentPaths
	mutation            MutationResult
	attemptTask         AttemptTask
	attemptSource       RetainedChangeHandoff
	webStatus           WebStatus
	webClients          WebClientPage
	webRevoke           WebRevokeResult
	remote              RemoteStatus
	overseer            OverseerSnapshot
	peerStatus          PeerStatus
	content             any
	accounts            Accounts
	taskRecovery        TaskRecovery
	taskText            TaskText
	workerOperation     WorkerOperation
	humanRequests       HumanRequestList
	code                RemoteErrorCode
	detail              string
}

func (Reply) String() string   { return "Reply(<redacted>)" }
func (Reply) GoString() string { return "Reply(<redacted>)" }

func NewHealthReply(status HealthStatus) Reply {
	return Reply{kind: replyHealth, health: status}
}

func NewSnapshotReply(snapshot DashboardSnapshot) (Reply, error) {
	if !validSnapshot(snapshot) {
		return Reply{}, ErrInvalidInput
	}
	projects := make([]ProjectSummary, len(snapshot.Projects))
	copy(projects, snapshot.Projects)
	snapshot.Projects = projects
	agents := make([]AgentSummary, len(snapshot.Agents))
	copy(agents, snapshot.Agents)
	snapshot.Agents = agents
	tasks := make([]TaskSummary, len(snapshot.Tasks))
	copy(tasks, snapshot.Tasks)
	snapshot.Tasks = tasks
	return Reply{kind: replySnapshot, snapshot: snapshot}, nil
}

func NewAgentPathsReply(paths AgentPaths) (Reply, error) {
	if !validAgentPaths(paths) {
		return Reply{}, ErrInvalidInput
	}
	paths.Paths = append([]string{}, paths.Paths...)
	return Reply{kind: replyAgentPaths, agentPaths: paths}, nil
}

func NewOverseerSnapshotReply(snapshot OverseerSnapshot) (Reply, error) {
	snapshot.Agents = append([]AgentSummary{}, snapshot.Agents...)
	snapshot.Tasks = append([]OverseerTask{}, snapshot.Tasks...)
	snapshot.Runs = append([]OverseerRun{}, snapshot.Runs...)
	snapshot.Questions = append([]OverseerQuestion{}, snapshot.Questions...)
	snapshot.PeerQuestions = append([]PeerQuestion{}, snapshot.PeerQuestions...)
	snapshot.History = append([]OverseerIntervention{}, snapshot.History...)
	if !validOverseerSnapshot(snapshot) {
		return Reply{}, ErrInvalidInput
	}
	return Reply{kind: replyOverseerSnapshot, overseer: snapshot}, nil
}

func NewMutationReply(result MutationResult) (Reply, error) {
	if !validMutation(result) {
		return Reply{}, ErrInvalidInput
	}
	return Reply{kind: replyMutation, mutation: result}, nil
}

func NewHumanRequestListReply(result HumanRequestList) Reply {
	if !validHumanRequestList(result) {
		return Reply{kind: replyError, code: RemoteInternal}
	}
	return Reply{kind: replyHumanRequests, humanRequests: result}
}

func NewAttemptTaskReply(task AttemptTask) (Reply, error) {
	if !validAttemptTask(task) {
		return Reply{}, ErrInvalidInput
	}
	return Reply{kind: replyAttemptTask, attemptTask: task}, nil
}

func NewAttemptSourceReply(source RetainedChangeHandoff) (Reply, error) {
	if !validSourceHandoff(source) {
		return Reply{}, ErrInvalidInput
	}
	return Reply{kind: replyAttemptSource, attemptSource: source}, nil
}

func NewTaskRecoveryReply(value TaskRecovery) (Reply, error) {
	if !validTaskRecovery(value) {
		return Reply{}, ErrInvalidInput
	}
	value.ArtifactPaths = append([]string{}, value.ArtifactPaths...)
	return Reply{kind: replyTaskRecovery, taskRecovery: value}, nil
}

func NewTaskTextReply(value TaskText) (Reply, error) {
	if !validID(value.TaskID) || value.Revision == 0 || !validText(value.Instruction, 0, 8192) || !validText(value.Feedback, 0, 8192) || value.Outcome != nil && !validText(*value.Outcome, 0, 8192) || value.NextOffset != nil && *value.NextOffset == 0 {
		return Reply{}, ErrInvalidInput
	}
	return Reply{kind: replyTaskText, taskText: value}, nil
}

func NewWorkerOperationReply(value WorkerOperation) (Reply, error) {
	if !validWorkerOperation(value) {
		return Reply{}, ErrInvalidInput
	}
	return Reply{kind: replyWorkerOperation, workerOperation: value}, nil
}

func NewPeerStatusReply(status PeerStatus) (Reply, error) {
	if !validPeerStatus(status) {
		return Reply{}, ErrInvalidInput
	}
	status.Questions = append([]PeerQuestion{}, status.Questions...)
	return Reply{kind: replyPeerStatus, peerStatus: status}, nil
}

func NewAccountsReply(accounts Accounts) (Reply, error) {
	accounts.Accounts = append([]DiscoveredAccount{}, accounts.Accounts...)
	if !validAccounts(accounts) {
		return Reply{}, ErrInvalidInput
	}
	return Reply{kind: replyAccounts, accounts: accounts}, nil
}

func NewTerminalObservationReply(value TerminalObservation) (Reply, error) {
	if !validTerminalObservation(value) {
		return Reply{}, ErrInvalidInput
	}
	value.Payload = append([]byte(nil), value.Payload...)
	return Reply{kind: replyTerminalObservation, terminalObservation: value}, nil
}

func NewWebStatusReply(status WebStatus) (Reply, error) {
	if !validWebStatus(status) {
		return Reply{}, ErrInvalidInput
	}
	status.Origins = append([]string(nil), status.Origins...)
	return Reply{kind: replyWebStatus, webStatus: status}, nil
}

func NewWebClientsReply(page WebClientPage) (Reply, error) {
	if !validWebClientPage(page) {
		return Reply{}, ErrInvalidInput
	}
	page.Clients = append([]WebClient(nil), page.Clients...)
	if page.NextAfter != nil {
		next := *page.NextAfter
		page.NextAfter = &next
	}
	return Reply{kind: replyWebClients, webClients: page}, nil
}

func NewWebRevokeReply(result WebRevokeResult) (Reply, error) {
	if !validWebRevokeResult(result) {
		return Reply{}, ErrInvalidInput
	}
	return Reply{kind: replyWebRevoke, webRevoke: result}, nil
}

func NewRemoteStatusReply(status RemoteStatus) (Reply, error) {
	if !validRemoteStatus(status) {
		return Reply{}, ErrInvalidInput
	}
	return Reply{kind: replyRemoteStatus, remote: status}, nil
}

func NewErrorReply(code RemoteErrorCode) (Reply, error) {
	return NewErrorDetailReply(code, "")
}

// NewErrorDetailReply refuses a request with the bounded reason its caller
// needs to act on. A refused worker that only learns a code is left with
// finished work and no way to report it.
func NewErrorDetailReply(code RemoteErrorCode, detail string) (Reply, error) {
	if !validRemoteCode(code) || !validRemoteDetail(detail) {
		return Reply{}, ErrInvalidInput
	}
	return Reply{kind: replyError, code: code, detail: detail}, nil
}
func NewContentReply(value any) Reply { return Reply{kind: replyContent, content: value} }

// Listener owns local API framing over one install-retained endpoint. It
// creates no goroutines; the caller owns the accept loop.
type Listener struct {
	authority *install.LocalAPIAuthority
	protocol  *install.LocalAPIProtocol
	closer    *listenerClose
}

type listenerClose struct {
	once sync.Once
	err  error
}

func (Listener) String() string   { return "Listener(<redacted>)" }
func (Listener) GoString() string { return "Listener(<redacted>)" }

// Listen consumes an already-bound install authority. Socket creation, stale
// recovery, operator-principal loading, and filesystem ownership never occur
// in this protocol package.
func Listen(authority *install.LocalAPIAuthority) (*Listener, error) {
	if authority == nil {
		return nil, ErrInvalidListener
	}
	protocol, err := authority.ClaimProtocol()
	if err != nil {
		return nil, ErrInvalidListener
	}
	return &Listener{authority: authority, protocol: protocol, closer: &listenerClose{}}, nil
}

// Accept synchronously accepts one connection and verifies both the socket
// identity and peer EUID. The caller must close the returned Connection.
func (listener *Listener) Accept() (*Connection, error) {
	if listener == nil || listener.protocol == nil {
		return nil, ErrInvalidListener
	}
	connection, err := listener.protocol.Accept()
	if err != nil {
		return nil, ErrTransport
	}
	result := &Connection{connection: connection, protocol: listener.protocol}
	result.self = result
	return result, nil
}

// Close closes the retained authority. A replacement at the recorded name is
// left untouched and makes the authority's stable result uncertain.
func (listener *Listener) Close() error {
	if listener == nil {
		return nil
	}
	listener.closer.once.Do(func() {
		if listener.authority == nil {
			return
		}
		if err := listener.authority.Close(); err != nil {
			listener.closer.err = ErrInvalidListener
		}
	})
	return listener.closer.err
}

type connectionState uint8

const (
	connectionNew connectionState = iota
	connectionReading
	connectionReceived
	connectionDispatching
	connectionDispatched
	connectionResponded
	connectionClosed
)

// Connection owns one accepted socket and permits exactly one Receive and one
// Respond. Receive owns and joins its cancellation watcher before returning.
type Connection struct {
	self       *Connection
	connection *install.LocalAPIConnection
	protocol   *install.LocalAPIProtocol
	domain     byte
	kind       CallKind
	call       Call
	state      connectionState
	receipt    [outcomeReceiptBytes]byte
	receiptDue bool
	closeOnce  sync.Once
	closeErr   error
}

func (*Connection) String() string   { return "Connection(<redacted>)" }
func (*Connection) GoString() string { return "Connection(<redacted>)" }

// Receive reads one complete request. Ordinary calls require client EOF before
// dispatch; attempt outcomes keep the write half open for their response
// receipt. Invalid requests are answered, when a valid response domain is
// available, with a fixed error code.
func (connection *Connection) Receive(ctx context.Context) (Call, error) {
	if connection == nil || connection.self != connection || connection.connection == nil || connection.protocol == nil || connection.state != connectionNew {
		return Call{}, ErrProtocol
	}
	connection.state = connectionReading
	if err := connection.setDeadline(ctx); err != nil {
		return Call{}, err
	}
	stopCancellation := watchCancellation(ctx, connection.connection)
	defer stopCancellation()
	payload, err := readFrame(connection.connection)
	if err != nil {
		return Call{}, classifyFrameError(ctx, err)
	}
	if len(payload) < requestPrelude {
		return Call{}, connection.reject(RemoteInvalidRequest)
	}
	defer clear(payload[:requestPrelude])
	domain := payload[0]
	if domain == operatorDomain || domain == attemptDomain {
		connection.domain = domain
	}
	if err := ctx.Err(); err != nil {
		return Call{}, err
	}
	if domain != operatorDomain && domain != attemptDomain {
		return Call{}, ErrProtocol
	}
	var bearer credential
	copy(bearer[:], payload[1:requestPrelude])
	defer clear(bearer[:])
	call, code := decodeCall(domain, bearer, payload[requestPrelude:])
	if err := ctx.Err(); err != nil {
		return Call{}, err
	}
	if code != "" {
		return Call{}, connection.reject(code)
	}
	if !outcomeCall(call.kind) {
		if err := requireEOF(connection.connection); err != nil {
			if errors.Is(err, ErrProtocol) {
				return Call{}, connection.reject(RemoteInvalidRequest)
			}
			return Call{}, classifyFrameError(ctx, err)
		}
	}
	if domain == operatorDomain && !connection.protocol.CheckOperator(bearer[:]) || domain == attemptDomain && connection.protocol.Verify() != nil {
		return Call{}, connection.reject(RemoteUnauthorized)
	}
	connection.kind = call.kind
	connection.call = call
	connection.state = connectionReceived
	return call, nil
}

// Dispatch enters the one scoped authority lease for the exact call returned
// by Receive. Shutdown begun before this method is called refuses the callback;
// an already-entered callback is joined by OperationalHome.Close.
func (connection *Connection) Dispatch(dispatch func(Call) Reply) (Reply, error) {
	if connection == nil || connection.self != connection || connection.protocol == nil || connection.state != connectionReceived || dispatch == nil {
		return Reply{}, ErrProtocol
	}
	lease, err := connection.protocol.BeginDispatch()
	if err != nil {
		return Reply{}, ErrTransport
	}
	connection.state = connectionDispatching
	var reply Reply
	var closeErr error
	func() {
		defer func() { closeErr = lease.Close() }()
		reply = dispatch(connection.call)
	}()
	if closeErr != nil {
		return Reply{}, ErrTransport
	}
	connection.state = connectionDispatched
	return reply, nil
}

// RefreshDeadline sets the authenticated dispatch budget without extending pre-auth reads.
func (connection *Connection) RefreshDeadline(ctx context.Context) error {
	if connection == nil || connection.self != connection || connection.connection == nil || connection.state != connectionReceived {
		return ErrProtocol
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return ErrProtocol
	}
	if err := connection.connection.SetDeadline(deadline); err != nil {
		return ErrTransport
	}
	return nil
}

func (connection *Connection) setDeadline(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline := time.Now().Add(requestTimeout)
	if requested, ok := ctx.Deadline(); ok && requested.Before(deadline) {
		deadline = requested
	}
	if err := connection.connection.SetDeadline(deadline); err != nil {
		return ErrTransport
	}
	return nil
}

type incomingRequest struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func decodeCall(domain byte, bearer credential, encoded []byte) (Call, RemoteErrorCode) {
	var request incomingRequest
	if err := decodeExact(encoded, &request); err != nil || !jsonObject(request.Params) {
		return Call{}, RemoteInvalidRequest
	}
	kind, methodDomain := methodKind(request.Method)
	if kind == 0 {
		return Call{}, RemoteInvalidRequest
	}
	if methodDomain != domain {
		return Call{}, RemoteForbidden
	}
	call := Call{kind: kind}
	if domain == attemptDomain {
		call.attempt = true
		call.digest = digestAttemptCredential(bearer)
	}
	switch kind {
	case CallMaintainer:
		if err := decodeExact(request.Params, &call.maintainer); err != nil || len(call.maintainer.Request) == 0 || len(call.maintainer.Request) > 512<<10 || !json.Valid(call.maintainer.Request) {
			return Call{}, RemoteInvalidRequest
		}
	case CallIntake:
		if err := decodeExact(request.Params, &call.intake); err != nil || !ValidIntakeInput(call.intake) {
			return Call{}, RemoteInvalidRequest
		}
	case CallGitHubConnection:
		if err := decodeExact(request.Params, &call.githubConnection); err != nil || !ValidGitHubConnectionInput(call.githubConnection) {
			return Call{}, RemoteInvalidRequest
		}
	case CallHealth, CallSnapshot, CallAttemptTask, CallWebStatus, CallRemoteStatus, CallHumanRequests:
		if err := decodeExact(request.Params, &struct{}{}); err != nil {
			return Call{}, RemoteInvalidRequest
		}
	case CallAccountsDiscover:
		var input struct {
			Offset uint32 `json:"offset,omitempty"`
		}
		if err := decodeExact(request.Params, &input); err != nil {
			return Call{}, RemoteInvalidRequest
		}
		call.accountsOffset = input.Offset
	case CallOverseerSnapshot:
		if err := decodeExact(request.Params, &call.overseerSnapshot); err != nil || !validOverseerSnapshotInput(call.overseerSnapshot) {
			return Call{}, RemoteInvalidRequest
		}
	case CallWebListClients:
		var input struct {
			After string `json:"after"`
		}
		if err := decodeExact(request.Params, &input); err != nil || input.After != "" && !validID(input.After) {
			return Call{}, RemoteInvalidRequest
		}
		call.webAfter = input.After
	case CallCreateProject:
		if err := decodeExact(request.Params, &call.project); err != nil || !validID(call.project.ID) || !validText(call.project.Name, 1, 128) || !validText(call.project.Root, 1, 4096) {
			return Call{}, RemoteInvalidRequest
		}
	case CallProjectRepository:
		if err := decodeExact(request.Params, &call.repository); err != nil {
			return Call{}, RemoteInvalidRequest
		}
	case CallProjectLimits:
		if err := decodeExact(request.Params, &call.projectLimits); err != nil || !validID(call.projectLimits.ProjectID) || call.projectLimits.ExpectedRevision == 0 || call.projectLimits.RunBudget > uint64(^uint64(0)>>1) || call.projectLimits.MaxRunSeconds > 86400 {
			return Call{}, RemoteInvalidRequest
		}
	case CallCreateAgent:
		if err := decodeExact(request.Params, &call.agent); err != nil || !validCreateAgentInput(call.agent) {
			return Call{}, RemoteInvalidRequest
		}
	case CallAgentIdlePolicy:
		if err := decodeExact(request.Params, &call.agentIdlePolicy); err != nil || !validAgentIdlePolicyInput(call.agentIdlePolicy) {
			return Call{}, RemoteInvalidRequest
		}
	case CallAgentPaths:
		if err := decodeExact(request.Params, &call.agentPaths); err != nil || !validAgentPathsInput(call.agentPaths) {
			return Call{}, RemoteInvalidRequest
		}
	case CallEnqueueTask:
		if err := decodeExact(request.Params, &call.task); err != nil || !validID(call.task.ID) || !validID(call.task.ProjectID) || !validOptionalID(call.task.RepositoryID) || !validOptionalID(call.task.AssignedAgentID) || !validID(call.task.IncarnationID) || !validText(call.task.Title, 1, 1024) || !validText(call.task.Body, 0, 131072) || call.task.Priority < -1_000_000 || call.task.Priority > 1_000_000 {
			return Call{}, RemoteInvalidRequest
		}
	case CallTaskRecovery:
		if err := decodeExact(request.Params, &call.taskRecovery); err != nil || !validID(call.taskRecovery.TaskID) || !validID(call.taskRecovery.IncarnationID) {
			return Call{}, RemoteInvalidRequest
		}
	case CallTaskRead:
		if err := decodeExact(request.Params, &call.taskRead); err != nil || !validID(call.taskRead.TaskID) || call.taskRead.ExpectedRevision == 0 || call.taskRead.Offset > uint64(^uint64(0)>>1) {
			return Call{}, RemoteInvalidRequest
		}
	case CallCompactStorage:
		if err := decodeExact(request.Params, &struct{}{}); err != nil {
			return Call{}, RemoteInvalidRequest
		}
	case CallSetDispatch:
		var input struct {
			ExpectedRevision uint64 `json:"expected_revision"`
			Enabled          bool   `json:"enabled"`
		}
		if err := decodeExact(request.Params, &input); err != nil || input.ExpectedRevision == 0 {
			return Call{}, RemoteInvalidRequest
		}
		call.expectedRevision, call.enabled = input.ExpectedRevision, input.Enabled
	case CallSetCapacity:
		var input struct {
			ExpectedRevision uint64 `json:"expected_revision"`
			Capacity         uint16 `json:"capacity"`
		}
		if err := decodeExact(request.Params, &input); err != nil || input.ExpectedRevision == 0 || input.Capacity < 1 || input.Capacity > kernel.MaxFactoryCapacity {
			return Call{}, RemoteInvalidRequest
		}
		call.expectedRevision, call.capacity = input.ExpectedRevision, input.Capacity
	case CallAccountLink:
		if err := decodeExact(request.Params, &call.account); err != nil || !validAccountLinkInput(call.account) {
			return Call{}, RemoteInvalidRequest
		}
	case CallAgentSelectAccount:
		if err := decodeExact(request.Params, &call.selection); err != nil || !validAgentAccountSelectInput(call.selection) {
			return Call{}, RemoteInvalidRequest
		}
	case CallAgentSelectModel:
		if err := decodeExact(request.Params, &call.modelSelection); err != nil || !validAgentModelSelectInput(call.modelSelection) {
			return Call{}, RemoteInvalidRequest
		}
	case CallSucceed:
		var input struct {
			Result string `json:"result"`
		}
		if err := decodeExact(request.Params, &input); err != nil || !validText(input.Result, 0, 131072) {
			return Call{}, RemoteInvalidRequest
		}
		call.text = input.Result
	case CallAttemptSource:
		var input struct {
			TaskID string `json:"task_id"`
		}
		if err := decodeExact(request.Params, &input); err != nil || !validID(input.TaskID) {
			return Call{}, RemoteInvalidRequest
		}
		call.sourceTaskID = input.TaskID
	case CallBlock:
		detail, ok := decodeAttemptDetail(request.Params)
		if !ok || !validText(detail, 1, 4096) {
			return Call{}, RemoteInvalidRequest
		}
		call.text = detail
	case CallFail:
		detail, ok := decodeAttemptDetail(request.Params)
		if !ok || !validText(detail, 0, 4096) {
			return Call{}, RemoteInvalidRequest
		}
		call.text = detail
	case CallRequestHuman:
		if err := decodeExact(request.Params, &call.humanQuestion); err != nil || !validID(call.humanQuestion.IdempotencyKey) || !validText(call.humanQuestion.Question, 1, 8192) || kernel.ValidateHumanOptions(call.humanQuestion.Options) != nil {
			return Call{}, RemoteInvalidRequest
		}
	case CallPeerStatus:
		var input PeerStatusInput
		if err := decodeExact(request.Params, &input); err != nil || input.Offset > uint64(^uint64(0)>>1)-1 || input.TargetOffset > uint64(^uint64(0)>>1)-4 || input.ExpectedHead > uint64(^uint64(0)>>1) || !input.IncludeTargets && input.TargetOffset != 0 || input.ExpectedHead == 0 && (input.Offset != 0 || input.TargetOffset != 0) {
			return Call{}, RemoteInvalidRequest
		}
		call.peerStatusOffset, call.peerTargetOffset, call.peerExpectedHead, call.peerIncludeTargets = input.Offset, input.TargetOffset, input.ExpectedHead, input.IncludeTargets
	case CallPeerAsk:
		if err := decodeExact(request.Params, &call.peerQuestion); err != nil || !validID(call.peerQuestion.TargetTaskID) || !validID(call.peerQuestion.IdempotencyKey) || !validText(call.peerQuestion.Question, 1, 2048) {
			return Call{}, RemoteInvalidRequest
		}
	case CallPeerAnswer:
		if err := decodeExact(request.Params, &call.peerAnswer); err != nil || !validID(call.peerAnswer.QuestionID) || !validID(call.peerAnswer.IdempotencyKey) || call.peerAnswer.ExpectedRevision == 0 || !validText(call.peerAnswer.Answer, 1, 2048) {
			return Call{}, RemoteInvalidRequest
		}
	case CallTerminalObserve, CallOperatorTerminalObserve:
		if err := decodeExact(request.Params, &call.terminalObserve); err != nil || !validTerminalObservationInput(call.terminalObserve) || kind == CallTerminalObserve && call.terminalObserve.Text {
			return Call{}, RemoteInvalidRequest
		}
	case CallSendBack, CallSendBackTask:
		if err := decodeExact(request.Params, &call.sendBack); err != nil || !validID(call.sendBack.TaskID) || !validText(call.sendBack.Note, 1, 8192) {
			return Call{}, RemoteInvalidRequest
		}
	case CallOverseerEnqueueTask:
		if err := decodeExact(request.Params, &call.overseerTask); err != nil || !validOverseerTaskCreateInput(call.overseerTask) {
			return Call{}, RemoteInvalidRequest
		}
	case CallOverseerUpdateTask, CallOperatorUpdateTask:
		if err := decodeExact(request.Params, &call.overseerTaskEdit); err != nil || !validOverseerTaskUpdateInput(call.overseerTaskEdit) || kind == CallOverseerUpdateTask && call.overseerTaskEdit.RemoveAttachments {
			return Call{}, RemoteInvalidRequest
		}
	case CallOverseerUpdateAgent, CallOperatorUpdateAgent:
		if err := decodeExact(request.Params, &call.overseerAgent); err != nil || !validID(call.overseerAgent.AgentID) || call.overseerAgent.ExpectedRevision == 0 {
			return Call{}, RemoteInvalidRequest
		}
	case CallOverseerStopRun, CallOperatorStopRun:
		if err := decodeExact(request.Params, &call.overseerRun); err != nil || !validOverseerRunStopInput(call.overseerRun) {
			return Call{}, RemoteInvalidRequest
		}
	case CallOverseerReplaceRun, CallOperatorReplaceRun:
		if err := decodeExact(request.Params, &call.overseerReplace); err != nil || !validOverseerRunReplaceInput(call.overseerReplace) {
			return Call{}, RemoteInvalidRequest
		}
	case CallOverseerMessageWorker, CallOperatorMessageWorker:
		if err := decodeExact(request.Params, &call.overseerMessage); err != nil || !validOverseerWorkerMessageInput(call.overseerMessage) {
			return Call{}, RemoteInvalidRequest
		}
	case CallOverseerInterruptWorker, CallOperatorInterruptWorker:
		if err := decodeExact(request.Params, &call.overseerInterrupt); err != nil || !validOverseerWorkerInterruptInput(call.overseerInterrupt) {
			return Call{}, RemoteInvalidRequest
		}
	case CallOperatorWorkerOperation:
		if err := decodeExact(request.Params, &call.workerOperation); err != nil || !validID(call.workerOperation.OperationID) {
			return Call{}, RemoteInvalidRequest
		}
	case CallOverseerReplyHuman:
		if err := decodeExact(request.Params, &call.overseerReply); err != nil || !validID(call.overseerReply.OperationID) || !validID(call.overseerReply.RequestID) || call.overseerReply.ExpectedRevision == 0 || !validText(call.overseerReply.Reply, 1, 8192) {
			return Call{}, RemoteInvalidRequest
		}
	case CallContentCreate, CallContentRevise:
		if err := decodeExact(request.Params, &call.content); err != nil || !validText(call.content.ID, 1, 64) || !validText(call.content.ProjectID, 1, 64) || !validText(call.content.Kind, 1, 64) || !validText(call.content.Title, 1, 1024) || !validText(call.content.Description, 0, 4096) || !validText(call.content.Body, 0, 1<<20) || !validText(call.content.SourceReferences, 0, 32768) || (call.content.Commit == "") != (call.content.Path == "") || call.content.Commit != "" && (call.content.Body != "" || !validText(call.content.Commit, 40, 64) || !validText(call.content.Path, 1, 4096)) {
			return Call{}, RemoteInvalidRequest
		}
	case CallContentDeprecate:
		if err := decodeExact(request.Params, &call.content); err != nil || !validText(call.content.ID, 1, 64) || !validText(call.content.ProjectID, 1, 64) || call.content.ExpectedRevision == 0 {
			return Call{}, RemoteInvalidRequest
		}
	case CallContentList:
		if err := decodeExact(request.Params, &call.contentList); err != nil || !validText(call.contentList.ProjectID, 1, 64) || call.contentList.Limit > MaxContentPageItems {
			return Call{}, RemoteInvalidRequest
		}
	case CallContentRead:
		if err := decodeExact(request.Params, &call.contentRead); err != nil || !validText(call.contentRead.ID, 1, 64) || call.contentRead.Revision == 0 {
			return Call{}, RemoteInvalidRequest
		}
	case CallContentBody:
		if err := decodeExact(request.Params, &call.contentBody); err != nil || !validText(call.contentBody.ID, 1, 64) || call.contentBody.Revision == 0 || call.contentBody.Limit == 0 || call.contentBody.Limit > 64*1024 {
			return Call{}, RemoteInvalidRequest
		}
	case CallContentEvidence:
		if err := decodeExact(request.Params, &call.contentEvidence); err != nil || !validText(call.contentEvidence.ID, 1, 64) || !validText(call.contentEvidence.ProjectID, 1, 64) || !validText(call.contentEvidence.ContentID, 1, 64) || call.contentEvidence.ContentRevision == 0 || !validText(call.contentEvidence.TestedSource, 1, 4096) || !validText(call.contentEvidence.Environment, 0, 4096) || !validText(call.contentEvidence.Result, 1, 32) || !validText(call.contentEvidence.Location, 0, 4096) || !validText(call.contentEvidence.Judgment, 0, 8192) || (call.contentEvidence.Result != "passed" && call.contentEvidence.Result != "failed" && call.contentEvidence.Result != "incomplete" && call.contentEvidence.Result != "not_run") {
			return Call{}, RemoteInvalidRequest
		}
	case CallContentAttach:
		if err := decodeExact(request.Params, &call.contentAttach); err != nil || !validText(call.contentAttach.TaskID, 1, 64) || !validText(call.contentAttach.ProjectID, 1, 64) || !validText(call.contentAttach.ContentID, 1, 64) || call.contentAttach.ContentRevision == 0 {
			return Call{}, RemoteInvalidRequest
		}
	case CallContentEvidenceList:
		if err := decodeExact(request.Params, &call.contentEvidenceList); err != nil || !validText(call.contentEvidenceList.ProjectID, 0, 64) || !validText(call.contentEvidenceList.ContentID, 1, 64) || call.contentEvidenceList.ContentRevision == 0 || call.contentEvidenceList.Limit > MaxContentPageItems {
			return Call{}, RemoteInvalidRequest
		}
	case CallContentAttachments:
		if err := decodeExact(request.Params, &call.contentAttachments); err != nil || !validText(call.contentAttachments.ProjectID, 0, 64) || !validText(call.contentAttachments.TaskID, 0, 64) || call.contentAttachments.TaskWorkRevision == 0 || call.contentAttachments.ProjectID == "" && call.contentAttachments.TaskID == "" {
			return Call{}, RemoteInvalidRequest
		}
	case CallOutcomeWrite:
		if err := decodeExact(request.Params, &call.outcomeWrite); err != nil || !validText(call.outcomeWrite.ID, 1, 64) || !validText(call.outcomeWrite.ProjectID, 1, 64) {
			return Call{}, RemoteInvalidRequest
		}
		if _, err := call.outcomeWrite.Document.MarshalBounded(); err != nil {
			return Call{}, RemoteInvalidRequest
		}
	case CallOutcomeRead:
		if err := decodeExact(request.Params, &call.outcomeRead); err != nil || !validText(call.outcomeRead.ID, 1, 64) || !validText(call.outcomeRead.ProjectID, 1, 64) {
			return Call{}, RemoteInvalidRequest
		}
	case CallOutcomeList:
		if err := decodeExact(request.Params, &call.outcomeList); err != nil || !validText(call.outcomeList.ProjectID, 1, 64) || call.outcomeList.Limit > MaxContentPageItems {
			return Call{}, RemoteInvalidRequest
		}
	case CallProductionObserve:
		if err := decodeExact(request.Params, &call.production); err != nil || !validProductionInput(call.production) {
			return Call{}, RemoteInvalidRequest
		}
	case CallDeliveryReconcile:
		if err := decodeExact(request.Params, &call.delivery); err != nil || !validDeliveryInput(call.delivery) {
			return Call{}, RemoteInvalidRequest
		}
	case CallHumanReply:
		if err := decodeExact(request.Params, &call.humanReply); err != nil || !validID(call.humanReply.OperationID) || !validID(call.humanReply.RequestID) || call.humanReply.ExpectedRevision == 0 || !validText(call.humanReply.Reply, 1, 8192) {
			return Call{}, RemoteInvalidRequest
		}
	case CallWebRevokeClient:
		if err := decodeExact(request.Params, &call.webClient); err != nil || !validID(call.webClient.ID) || call.webClient.ExpectedRevision == 0 {
			return Call{}, RemoteInvalidRequest
		}
	}
	return call, ""
}

func decodeAttemptDetail(encoded []byte) (string, bool) {
	var input struct {
		Detail string `json:"detail"`
	}
	if err := decodeExact(encoded, &input); err != nil {
		return "", false
	}
	return input.Detail, true
}

func jsonObject(encoded []byte) bool {
	trimmed := bytes.TrimSpace(encoded)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}'
}

func validProductionInput(input ProductionInput) bool {
	return validID(input.ProjectID) && validText(input.Observation.Repository, 1, 4096)
}

func validDeliveryInput(input DeliveryInput) bool {
	return validID(input.ProjectID) && validRepository(input.Repository) && validID(input.OverseerAgentID) && input.PriorityDefault >= -1_000_000 && input.PriorityDefault <= 1_000_000 && validRepository(input.Release.Repository) && len(input.Receipt) > 0 && jsonObject(input.Receipt)
}

func validRepository(value string) bool {
	return validText(value, 3, 140) && strings.Count(value, "/") == 1
}

func methodKind(method string) (CallKind, byte) {
	switch method {
	case "health":
		return CallHealth, operatorDomain
	case "snapshot":
		return CallSnapshot, operatorDomain
	case "create_project":
		return CallCreateProject, operatorDomain
	case "project_repository":
		return CallProjectRepository, operatorDomain
	case "project_limits":
		return CallProjectLimits, operatorDomain
	case "create_agent":
		return CallCreateAgent, operatorDomain
	case "agent_idle_policy":
		return CallAgentIdlePolicy, operatorDomain
	case "agent_paths":
		return CallAgentPaths, operatorDomain
	case "enqueue_task":
		return CallEnqueueTask, operatorDomain
	case "set_dispatch":
		return CallSetDispatch, operatorDomain
	case "compact_storage":
		return CallCompactStorage, operatorDomain
	case "set_capacity":
		return CallSetCapacity, operatorDomain
	case "accounts_discover":
		return CallAccountsDiscover, operatorDomain
	case "account_link":
		return CallAccountLink, operatorDomain
	case "agent_select_account":
		return CallAgentSelectAccount, operatorDomain
	case "task":
		return CallAttemptTask, attemptDomain
	case "source":
		return CallAttemptSource, attemptDomain
	case "agent_select_model":
		return CallAgentSelectModel, operatorDomain
	case "succeed":
		return CallSucceed, attemptDomain
	case "block":
		return CallBlock, attemptDomain
	case "fail":
		return CallFail, attemptDomain
	case "request_human":
		return CallRequestHuman, attemptDomain
	case "peer_status":
		return CallPeerStatus, attemptDomain
	case "peer_ask":
		return CallPeerAsk, attemptDomain
	case "peer_answer":
		return CallPeerAnswer, attemptDomain
	case "terminal_observe":
		return CallTerminalObserve, attemptDomain
	case "operator_terminal_observe":
		return CallOperatorTerminalObserve, operatorDomain
	case "send_back":
		return CallSendBack, attemptDomain
	case "send_back_task":
		return CallSendBackTask, operatorDomain
	case "task_recovery":
		return CallTaskRecovery, operatorDomain
	case "task_read":
		return CallTaskRead, operatorDomain
	case "web_status":
		return CallWebStatus, operatorDomain
	case "web_list_clients":
		return CallWebListClients, operatorDomain
	case "web_revoke_client":
		return CallWebRevokeClient, operatorDomain
	case "attempt_maintainer":
		return CallMaintainer, attemptDomain
	case "intake":
		return CallIntake, operatorDomain
	case "github_connection":
		return CallGitHubConnection, operatorDomain
	case "remote_status":
		return CallRemoteStatus, operatorDomain
	case "overseer_snapshot":
		return CallOverseerSnapshot, attemptDomain
	case "overseer_enqueue_task":
		return CallOverseerEnqueueTask, attemptDomain
	case "overseer_update_task":
		return CallOverseerUpdateTask, attemptDomain
	case "overseer_update_agent":
		return CallOverseerUpdateAgent, attemptDomain
	case "overseer_stop_run":
		return CallOverseerStopRun, attemptDomain
	case "overseer_replace_run":
		return CallOverseerReplaceRun, attemptDomain
	case "overseer_message_worker":
		return CallOverseerMessageWorker, attemptDomain
	case "overseer_interrupt_worker":
		return CallOverseerInterruptWorker, attemptDomain
	case "overseer_reply_human":
		return CallOverseerReplyHuman, attemptDomain
	case "operator_update_task":
		return CallOperatorUpdateTask, operatorDomain
	case "operator_update_agent":
		return CallOperatorUpdateAgent, operatorDomain
	case "content_create":
		return CallContentCreate, operatorDomain
	case "content_revise":
		return CallContentRevise, operatorDomain
	case "content_deprecate":
		return CallContentDeprecate, operatorDomain
	case "content_list":
		return CallContentList, operatorDomain
	case "content_read":
		return CallContentRead, operatorDomain
	case "content_body":
		return CallContentBody, operatorDomain
	case "content_evidence":
		return CallContentEvidence, operatorDomain
	case "content_attach":
		return CallContentAttach, operatorDomain
	case "content_evidence_list":
		return CallContentEvidenceList, operatorDomain
	case "content_attachments":
		return CallContentAttachments, operatorDomain
	case "attempt_content_create":
		return CallContentCreate, attemptDomain
	case "attempt_content_revise":
		return CallContentRevise, attemptDomain
	case "attempt_content_deprecate":
		return CallContentDeprecate, attemptDomain
	case "attempt_content_list":
		return CallContentList, attemptDomain
	case "attempt_content_read":
		return CallContentRead, attemptDomain
	case "attempt_content_body":
		return CallContentBody, attemptDomain
	case "attempt_content_evidence":
		return CallContentEvidence, attemptDomain
	case "attempt_content_attach":
		return CallContentAttach, attemptDomain
	case "attempt_content_evidence_list":
		return CallContentEvidenceList, attemptDomain
	case "attempt_content_attachments":
		return CallContentAttachments, attemptDomain
	case "outcome_write":
		return CallOutcomeWrite, operatorDomain
	case "outcome_read":
		return CallOutcomeRead, operatorDomain
	case "outcome_list":
		return CallOutcomeList, operatorDomain
	case "production_observe":
		return CallProductionObserve, operatorDomain
	case "delivery_reconcile":
		return CallDeliveryReconcile, operatorDomain
	case "attempt_outcome_write":
		return CallOutcomeWrite, attemptDomain
	case "attempt_outcome_read":
		return CallOutcomeRead, attemptDomain
	case "attempt_outcome_list":
		return CallOutcomeList, attemptDomain
	case "human_requests":
		return CallHumanRequests, operatorDomain
	case "human_reply":
		return CallHumanReply, operatorDomain
	case "operator_stop_run":
		return CallOperatorStopRun, operatorDomain
	case "operator_replace_run":
		return CallOperatorReplaceRun, operatorDomain
	case "operator_message_worker":
		return CallOperatorMessageWorker, operatorDomain
	case "operator_interrupt_worker":
		return CallOperatorInterruptWorker, operatorDomain
	case "operator_worker_operation":
		return CallOperatorWorkerOperation, operatorDomain
	default:
		return 0, 0
	}
}

// Respond writes exactly one response for the previously received Call. The
// response domain is taken from that request and cannot be supplied by callers.
func (connection *Connection) Respond(reply Reply) error {
	if connection == nil || connection.self != connection || connection.connection == nil || connection.state != connectionDispatched || !replyMatches(connection.kind, reply.kind) {
		return ErrProtocol
	}
	connection.state = connectionResponded
	if err := connection.writeReply(reply); err != nil {
		return err
	}
	return nil
}

// AwaitOutcomeReceipt joins the response-consumption acknowledgement for a
// successful attempt outcome. Other replies have no receipt and return
// immediately. The random frame cannot be sent before the response is read.
func (connection *Connection) AwaitOutcomeReceipt(ctx context.Context) error {
	if connection == nil || connection.self != connection || connection.connection == nil || connection.state != connectionResponded {
		return ErrProtocol
	}
	if !connection.receiptDue {
		return nil
	}
	expected := connection.receipt
	defer func() {
		clear(connection.receipt[:])
		connection.receiptDue = false
	}()
	if err := connection.setDeadline(ctx); err != nil {
		return err
	}
	stopCancellation := watchCancellation(ctx, connection.connection)
	defer stopCancellation()
	receipt, err := readFrame(connection.connection)
	if err != nil {
		return classifyFrameError(ctx, err)
	}
	if len(receipt) != len(expected) || !bytes.Equal(receipt, expected[:]) {
		return ErrProtocol
	}
	if err := requireEOF(connection.connection); err != nil {
		return classifyFrameError(ctx, err)
	}
	return nil
}

func outcomeCall(kind CallKind) bool {
	return kind == CallSucceed || kind == CallBlock || kind == CallFail
}

func replyMatches(kind CallKind, reply replyKind) bool {
	if reply == replyError {
		return true
	}
	switch kind {
	case CallHealth:
		return reply == replyHealth
	case CallHumanRequests:
		return reply == replyHumanRequests
	case CallSnapshot:
		return reply == replySnapshot
	case CallAgentPaths:
		return reply == replyAgentPaths
	case CallAccountsDiscover:
		return reply == replyAccounts
	case CallAttemptTask:
		return reply == replyAttemptTask
	case CallAttemptSource:
		return reply == replyAttemptSource
	case CallTaskRecovery:
		return reply == replyTaskRecovery
	case CallOperatorWorkerOperation:
		return reply == replyWorkerOperation
	case CallTaskRead:
		return reply == replyTaskText
	case CallTerminalObserve:
		return reply == replyTerminalObservation
	case CallOperatorTerminalObserve:
		return reply == replyTerminalObservation
	case CallPeerStatus:
		return reply == replyPeerStatus
	case CallOverseerSnapshot:
		return reply == replyOverseerSnapshot
	case CallProjectRepository:
		return reply == replyContent
	case CallCreateProject, CallProjectLimits, CallCreateAgent, CallAgentIdlePolicy, CallAccountLink, CallAgentSelectAccount, CallAgentSelectModel, CallEnqueueTask, CallSetDispatch, CallSetCapacity, CallCompactStorage, CallSucceed, CallBlock, CallFail, CallRequestHuman, CallPeerAsk, CallPeerAnswer, CallSendBack, CallSendBackTask, CallOverseerEnqueueTask, CallOverseerUpdateTask, CallOverseerUpdateAgent, CallOverseerStopRun, CallOverseerReplaceRun, CallOverseerMessageWorker, CallOverseerInterruptWorker, CallOverseerReplyHuman, CallOperatorUpdateTask, CallOperatorUpdateAgent, CallOperatorStopRun, CallOperatorReplaceRun, CallOperatorMessageWorker, CallOperatorInterruptWorker, CallHumanReply:
		return reply == replyMutation
	case CallWebStatus:
		return reply == replyWebStatus
	case CallWebListClients:
		return reply == replyWebClients
	case CallWebRevokeClient:
		return reply == replyWebRevoke
	case CallMaintainer, CallGitHubConnection, CallIntake, CallProductionObserve:
		return reply == replyContent
	case CallRemoteStatus:
		return reply == replyRemoteStatus
	case CallContentCreate, CallContentRevise, CallContentDeprecate, CallContentList, CallContentRead, CallContentBody, CallContentEvidence, CallContentAttach, CallContentEvidenceList, CallContentAttachments, CallOutcomeWrite, CallOutcomeRead, CallOutcomeList:
		return reply == replyContent || reply == replyMutation
	default:
		return false
	}
}

func (connection *Connection) reject(code RemoteErrorCode) error {
	if connection.domain != operatorDomain && connection.domain != attemptDomain {
		return ErrProtocol
	}
	reply, err := NewErrorReply(code)
	if err != nil {
		return ErrProtocol
	}
	_ = connection.writeReply(reply)
	connection.state = connectionResponded
	return ErrProtocol
}

func (connection *Connection) writeReply(reply Reply) error {
	var data []byte
	var err error
	switch reply.kind {
	case replyHealth:
		data, err = json.Marshal(reply.health)
	case replySnapshot:
		data, err = json.Marshal(reply.snapshot)
	case replyAgentPaths:
		data, err = json.Marshal(reply.agentPaths)
	case replyMutation:
		data, err = json.Marshal(reply.mutation)
	case replyAttemptTask:
		data, err = json.Marshal(reply.attemptTask)
	case replyAttemptSource:
		data, err = json.Marshal(reply.attemptSource)
	case replyTaskRecovery:
		data, err = json.Marshal(reply.taskRecovery)
	case replyTaskText:
		data, err = json.Marshal(reply.taskText)
	case replyWorkerOperation:
		data, err = json.Marshal(reply.workerOperation)
	case replyWebStatus:
		data, err = json.Marshal(reply.webStatus)
	case replyWebClients:
		data, err = json.Marshal(reply.webClients)
	case replyWebRevoke:
		data, err = json.Marshal(reply.webRevoke)
	case replyRemoteStatus:
		data, err = json.Marshal(reply.remote)
	case replyContent:
		data, err = json.Marshal(reply.content)
	case replyOverseerSnapshot:
		data, err = json.Marshal(reply.overseer)
	case replyTerminalObservation:
		data, err = json.Marshal(reply.terminalObservation)
	case replyPeerStatus:
		data, err = json.Marshal(reply.peerStatus)
	case replyAccounts:
		data, err = json.Marshal(reply.accounts)
	case replyHumanRequests:
		data, err = json.Marshal(reply.humanRequests)
	case replyError:
	default:
		return ErrProtocol
	}
	if err != nil {
		return ErrProtocol
	}
	ok := reply.kind != replyError
	envelope := responseEnvelope{OK: &ok}
	if ok {
		envelope.Data = data
	} else {
		envelope.Error = reply.code
		envelope.Detail = reply.detail
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return ErrProtocol
	}
	if len(encoded)+responsePrelude > maxFrameBytes {
		if reply.kind == replyError {
			return ErrProtocol
		}
		tooLarge, _ := NewErrorReply(RemoteTooLarge)
		return connection.writeReply(tooLarge)
	}
	payload := make([]byte, responsePrelude+len(encoded))
	payload[0] = connection.domain
	copy(payload[responsePrelude:], encoded)
	// Every dispatched outcome response carries a receipt, including an error
	// produced after the durable outcome committed. Receipt presence must not
	// depend on a fallible post-commit response projection.
	withReceipt := outcomeCall(connection.kind)
	if withReceipt {
		if _, err := rand.Read(connection.receipt[:]); err != nil {
			return ErrTransport
		}
	}
	if err := writeFrame(connection.connection, payload); err != nil {
		clear(connection.receipt[:])
		return ErrTransport
	}
	if withReceipt {
		if err := writeFrame(connection.connection, connection.receipt[:]); err != nil {
			clear(connection.receipt[:])
			return ErrTransport
		}
		connection.receiptDue = true
	}
	if err := connection.connection.CloseWrite(); err != nil {
		clear(connection.receipt[:])
		connection.receiptDue = false
		return ErrTransport
	}
	return nil
}

func (connection *Connection) Close() error {
	if connection == nil {
		return nil
	}
	if connection.self != connection {
		return ErrProtocol
	}
	connection.closeOnce.Do(func() {
		connection.state = connectionClosed
		clear(connection.receipt[:])
		connection.receiptDue = false
		if connection.connection != nil {
			if err := connection.connection.Close(); err != nil {
				connection.closeErr = ErrTransport
			}
		}
	})
	return connection.closeErr
}

func (call Call) AgentModelSelectInput() (AgentModelSelectInput, bool) {
	return call.modelSelection, call.kind == CallAgentSelectModel
}

func (call Call) AccountsOffset() (uint32, bool) {
	return call.accountsOffset, call.kind == CallAccountsDiscover
}
