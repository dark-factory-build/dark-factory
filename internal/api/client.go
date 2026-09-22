package api

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// A frame's prelude is its auth domain and nothing else. There is no protocol
// generation: factoryctl, factory-runner and factoryd are siblings of one
// build, and the sibling-binary boundary already refuses a mixed installation.
const (
	operatorDomain      byte = 1
	attemptDomain       byte = 2
	requestPrelude           = 1 + credentialBytes
	responsePrelude          = 1
	outcomeReceiptBytes      = 32
	requestTimeout           = 5 * time.Second
	attemptTokenFileEnv      = "DARK_FACTORY_ATTEMPT_TOKEN_FILE"
)

type credential [credentialBytes]byte

func (credential) String() string   { return "credential(<redacted>)" }
func (credential) GoString() string { return "credential(<redacted>)" }

type client struct {
	socketPath string
	tokenPath  string
	token      tokenRecord
	socket     socketRecord
	domain     byte
}

type OperatorClient struct{ client client }
type AttemptClient struct{ client client }

func (OperatorClient) String() string   { return "OperatorClient(<redacted>)" }
func (OperatorClient) GoString() string { return "OperatorClient(<redacted>)" }
func (AttemptClient) String() string    { return "AttemptClient(<redacted>)" }
func (AttemptClient) GoString() string  { return "AttemptClient(<redacted>)" }

func NewOperatorClient(socketPath, tokenPath string) (*OperatorClient, error) {
	base, err := newClient(socketPath, tokenPath, operatorDomain)
	if err != nil {
		return nil, err
	}
	return &OperatorClient{client: base}, nil
}

// NewAttemptClientFromEnvironment reads only DARK_FACTORY_ATTEMPT_TOKEN_FILE.
// It has no operator-token locator and therefore cannot fall back across auth
// domains when the attempt credential is absent or invalid.
func NewAttemptClientFromEnvironment(socketPath string) (*AttemptClient, error) {
	tokenPath, found := os.LookupEnv(attemptTokenFileEnv)
	if !found || tokenPath == "" {
		return nil, ErrInvalidClient
	}
	base, err := newClient(socketPath, tokenPath, attemptDomain)
	if err != nil {
		return nil, err
	}
	return &AttemptClient{client: base}, nil
}

func newClient(socketPath, tokenPath string, domain byte) (client, error) {
	if domain != operatorDomain && domain != attemptDomain || !validCanonicalPath(socketPath, install.MaxSocketPathBytes) {
		return client{}, ErrInvalidClient
	}
	token, err := loadToken(tokenPath)
	if err != nil {
		return client{}, err
	}
	socket, err := inspectSocket(socketPath)
	if err != nil {
		return client{}, err
	}
	return client{socketPath: socketPath, tokenPath: tokenPath, token: token, socket: socket, domain: domain}, nil
}

func (client *OperatorClient) Health(ctx context.Context) (HealthStatus, error) {
	var result HealthStatus
	err := client.client.call(ctx, "health", struct{}{}, &result)
	return result, err
}

func (client *OperatorClient) WebStatus(ctx context.Context) (WebStatus, error) {
	var result WebStatus
	if err := client.client.call(ctx, "web_status", struct{}{}, &result); err != nil {
		return WebStatus{}, err
	}
	if !validWebStatus(result) {
		return WebStatus{}, ErrProtocol
	}
	return result, nil
}

func (client *OperatorClient) TerminalObserve(ctx context.Context, input TerminalObserveInput) (TerminalObservation, error) {
	if !validTerminalObservationInput(input) {
		return TerminalObservation{}, ErrInvalidInput
	}
	var result TerminalObservation
	if err := client.client.call(ctx, "operator_terminal_observe", input, &result); err != nil {
		return TerminalObservation{}, err
	}
	if !validTerminalObservation(result) || result.ProjectID != input.ProjectID || result.TaskID != input.TaskID || result.RunID != input.RunID || result.Cursor != input.Cursor || len(result.Payload) > int(input.MaxBytes) || len(result.Text) > int(input.MaxBytes) || input.Text != result.TextMode || (!result.Gap && result.NextCursor-input.Cursor > uint64(input.MaxBytes)) {
		return TerminalObservation{}, ErrProtocol
	}
	return result, nil
}

func (client *OperatorClient) WebListClients(ctx context.Context, after string) (WebClientPage, error) {
	if after != "" && !validID(after) {
		return WebClientPage{}, ErrInvalidInput
	}
	params := struct {
		After string `json:"after"`
	}{After: after}
	var result WebClientPage
	if err := client.client.call(ctx, "web_list_clients", params, &result); err != nil {
		return WebClientPage{}, err
	}
	if !validWebClientPage(result) {
		return WebClientPage{}, ErrProtocol
	}
	return result, nil
}

func (client *OperatorClient) WebRevokeClient(ctx context.Context, id string, expectedRevision uint64) (WebRevokeResult, error) {
	if !validID(id) || expectedRevision == 0 {
		return WebRevokeResult{}, ErrInvalidInput
	}
	params := struct {
		ID               string `json:"id"`
		ExpectedRevision uint64 `json:"expected_revision"`
	}{ID: id, ExpectedRevision: expectedRevision}
	var result WebRevokeResult
	if err := client.client.call(ctx, "web_revoke_client", params, &result); err != nil {
		return WebRevokeResult{}, err
	}
	if !validWebRevokeResult(result) {
		return WebRevokeResult{}, ErrProtocol
	}
	return result, nil
}

func (client *OperatorClient) RemoteStatus(ctx context.Context) (RemoteStatus, error) {
	var result RemoteStatus
	if err := client.client.call(ctx, "remote_status", struct{}{}, &result); err != nil {
		return RemoteStatus{}, err
	}
	if !validRemoteStatus(result) {
		return RemoteStatus{}, ErrProtocol
	}
	return result, nil
}

func (client *OperatorClient) Snapshot(ctx context.Context) (DashboardSnapshot, error) {
	var result DashboardSnapshot
	if err := client.client.call(ctx, "snapshot", struct{}{}, &result); err != nil {
		return DashboardSnapshot{}, err
	}
	if !validSnapshot(result) {
		return DashboardSnapshot{}, ErrProtocol
	}
	return result, nil
}

func (client *OperatorClient) AgentPaths(ctx context.Context, input AgentPathsInput) (AgentPaths, error) {
	if !validAgentPathsInput(input) {
		return AgentPaths{}, ErrInvalidInput
	}
	var result AgentPaths
	if err := client.client.call(ctx, "agent_paths", input, &result); err != nil {
		return AgentPaths{}, err
	}
	if !validAgentPaths(result) {
		return AgentPaths{}, ErrProtocol
	}
	return result, nil
}

func (client *OperatorClient) HumanRequests(ctx context.Context) (HumanRequestList, error) {
	var result HumanRequestList
	if err := client.client.call(ctx, "human_requests", struct{}{}, &result); err != nil {
		return HumanRequestList{}, err
	}
	if !validHumanRequestList(result) {
		return HumanRequestList{}, ErrProtocol
	}
	return result, nil
}

func (client *OperatorClient) HumanReply(ctx context.Context, input OverseerHumanReplyInput) (MutationResult, error) {
	if !validID(input.OperationID) || !validID(input.RequestID) || input.ExpectedRevision == 0 || !validText(input.Reply, 1, 8192) {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "human_reply", input)
}

func (client *OperatorClient) StopRun(ctx context.Context, input OverseerRunStopInput) (MutationResult, error) {
	if !validOverseerRunStopInput(input) {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "operator_stop_run", input)
}

func (client *OperatorClient) ReplaceRun(ctx context.Context, input OverseerRunReplaceInput) (MutationResult, error) {
	if !validOverseerRunReplaceInput(input) {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "operator_replace_run", input)
}

func (client *OperatorClient) MessageWorker(ctx context.Context, input OverseerWorkerMessageInput) (MutationResult, error) {
	if !validOverseerWorkerMessageInput(input) {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "operator_message_worker", input)
}

func (client *OperatorClient) InterruptWorker(ctx context.Context, input OverseerWorkerInterruptInput) (MutationResult, error) {
	if !validOverseerWorkerInterruptInput(input) {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "operator_interrupt_worker", input)
}

func (client *OperatorClient) DiscoverAccounts(ctx context.Context) (Accounts, error) {
	accounts := make([]DiscoveredAccount, 0)
	for offset := uint32(0); ; {
		var page Accounts
		params := struct {
			Offset uint32 `json:"offset,omitempty"`
		}{Offset: offset}
		if err := client.client.call(ctx, "accounts_discover", params, &page); err != nil {
			return Accounts{}, err
		}
		if !validAccounts(page) {
			return Accounts{}, ErrProtocol
		}
		accounts = append(accounts, page.Accounts...)
		if page.NextOffset == nil {
			return Accounts{Accounts: accounts}, nil
		}
		if uint64(*page.NextOffset) != uint64(offset)+uint64(len(page.Accounts)) || len(page.Accounts) == 0 {
			return Accounts{}, ErrProtocol
		}
		offset = *page.NextOffset
	}
}
func (client *OperatorClient) LinkAccount(ctx context.Context, input AccountLinkInput) (MutationResult, error) {
	if !validAccountLinkInput(input) {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "account_link", input)
}

func (client *OperatorClient) SelectAgentAccount(ctx context.Context, input AgentAccountSelectInput) (MutationResult, error) {
	if !validAgentAccountSelectInput(input) {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "agent_select_account", input)
}

func (client *OperatorClient) CreateProject(ctx context.Context, input CreateProjectInput) (MutationResult, error) {
	if !validID(input.ID) || !validText(input.Name, 1, 128) || !validText(input.Root, 1, 4096) {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "create_project", input)
}

func (client *OperatorClient) ProjectRepositories(ctx context.Context, projectID string) (ProjectRepositories, error) {
	if !validID(projectID) {
		return ProjectRepositories{}, ErrInvalidInput
	}
	var result ProjectRepositories
	if err := client.client.call(ctx, "project_repository", ProjectRepositoryInput{Action: "list", ProjectID: projectID}, &result); err != nil {
		return ProjectRepositories{}, err
	}
	return result, nil
}
func (client *OperatorClient) ProjectRepository(ctx context.Context, input ProjectRepositoryInput) (ProjectRepository, error) {
	var result ProjectRepository
	if err := client.client.call(ctx, "project_repository", input, &result); err != nil {
		return ProjectRepository{}, err
	}
	return result, nil
}
func (client *OperatorClient) RemoveProjectRepository(ctx context.Context, id string, revision uint64) error {
	if !validID(id) || revision == 0 {
		return ErrInvalidInput
	}
	return client.client.call(ctx, "project_repository", ProjectRepositoryInput{Action: "remove", ID: id, ExpectedRevision: revision}, &struct{}{})
}

func (client *OperatorClient) SetProjectLimits(ctx context.Context, input ProjectLimitsInput) (MutationResult, error) {
	if !validID(input.ProjectID) || input.ExpectedRevision == 0 || input.RunBudget > uint64(^uint64(0)>>1) || input.MaxRunSeconds > 86400 {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "project_limits", input)
}

func (client *OperatorClient) CreateAgent(ctx context.Context, input CreateAgentInput) (MutationResult, error) {
	if !validCreateAgentInput(input) {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "create_agent", input)
}

func (client *OperatorClient) SetAgentIdlePolicy(ctx context.Context, input AgentIdlePolicyInput) (MutationResult, error) {
	if !validAgentIdlePolicyInput(input) {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "agent_idle_policy", input)
}

// SendBackTask returns a finished task to its queue with a note.
func (client *OperatorClient) SendBackTask(ctx context.Context, input SendBackInput) (MutationResult, error) {
	if !validID(input.TaskID) || !validText(input.Note, 1, 8192) {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "send_back_task", input)
}

func (client *OperatorClient) TaskRecovery(ctx context.Context, input TaskRecoveryInput) (TaskRecovery, error) {
	if !validID(input.TaskID) || !validID(input.IncarnationID) {
		return TaskRecovery{}, ErrInvalidInput
	}
	var result TaskRecovery
	if err := client.client.call(ctx, "task_recovery", input, &result); err != nil {
		return TaskRecovery{}, err
	}
	if !validTaskRecovery(result) || result.State == "found" && (result.TaskID != input.TaskID || result.IncarnationID != input.IncarnationID) {
		return TaskRecovery{}, ErrProtocol
	}
	return result, nil
}

func (client *OperatorClient) WorkerOperation(ctx context.Context, operationID string) (WorkerOperation, error) {
	if !validID(operationID) {
		return WorkerOperation{}, ErrInvalidInput
	}
	var result WorkerOperation
	if err := client.client.call(ctx, "operator_worker_operation", WorkerOperationInput{OperationID: operationID}, &result); err != nil {
		return WorkerOperation{}, err
	}
	if !validWorkerOperation(result) || result.OperationID != operationID {
		return WorkerOperation{}, ErrProtocol
	}
	return result, nil
}

func (client *OperatorClient) ReadTask(ctx context.Context, input TaskReadInput) (TaskText, error) {
	if !validID(input.TaskID) || input.ExpectedRevision == 0 || input.Offset > uint64(^uint64(0)>>1) {
		return TaskText{}, ErrInvalidInput
	}
	var result TaskText
	if err := client.client.call(ctx, "task_read", input, &result); err != nil {
		return TaskText{}, err
	}
	if result.TaskID != input.TaskID || result.Revision != input.ExpectedRevision || !validText(result.Instruction, 0, 8192) || !validText(result.Feedback, 0, 8192) || result.Outcome != nil && !validText(*result.Outcome, 0, 8192) || result.NextOffset != nil && *result.NextOffset != input.Offset+2048 {
		return TaskText{}, ErrProtocol
	}
	return result, nil
}

func (client *OperatorClient) EnqueueTask(ctx context.Context, input EnqueueTaskInput) (MutationResult, error) {
	if !validID(input.ID) || !validID(input.ProjectID) || !validOptionalID(input.RepositoryID) || !validOptionalID(input.AssignedAgentID) || !validID(input.IncarnationID) || !validText(input.Title, 1, 1024) || !validText(input.Body, 0, 131072) || input.Priority < -1_000_000 || input.Priority > 1_000_000 {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "enqueue_task", input)
}

func (client *OperatorClient) UpdateTask(ctx context.Context, input OverseerTaskUpdateInput) (MutationResult, error) {
	if !validOverseerTaskUpdateInput(input) {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "operator_update_task", input)
}

func (client *OperatorClient) CompactStorage(ctx context.Context) (MutationResult, error) {
	return client.client.mutate(ctx, "compact_storage", struct{}{})
}

func (client *OperatorClient) UpdateAgent(ctx context.Context, input OverseerAgentUpdateInput) (MutationResult, error) {
	if !validID(input.AgentID) || input.ExpectedRevision == 0 {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "operator_update_agent", input)
}

func (client *OperatorClient) SetDispatch(ctx context.Context, expectedRevision uint64, enabled bool) (MutationResult, error) {
	if expectedRevision == 0 {
		return MutationResult{}, ErrInvalidInput
	}
	params := struct {
		ExpectedRevision uint64 `json:"expected_revision"`
		Enabled          bool   `json:"enabled"`
	}{ExpectedRevision: expectedRevision, Enabled: enabled}
	return client.client.mutate(ctx, "set_dispatch", params)
}

func (client *OperatorClient) SetCapacity(ctx context.Context, expectedRevision uint64, capacity uint16) (MutationResult, error) {
	if expectedRevision == 0 || capacity < 1 || capacity > kernel.MaxFactoryCapacity {
		return MutationResult{}, ErrInvalidInput
	}
	params := struct {
		ExpectedRevision uint64 `json:"expected_revision"`
		Capacity         uint16 `json:"capacity"`
	}{ExpectedRevision: expectedRevision, Capacity: capacity}
	return client.client.mutate(ctx, "set_capacity", params)
}

func (client *AttemptClient) Succeed(ctx context.Context, result string) (MutationResult, error) {
	if !validText(result, 0, 131072) {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "succeed", struct {
		Result string `json:"result"`
	}{Result: result})
}

func (client *AttemptClient) Task(ctx context.Context) (AttemptTask, error) {
	var result AttemptTask
	if err := client.client.call(ctx, "task", struct{}{}, &result); err != nil {
		return AttemptTask{}, err
	}
	if !validAttemptTask(result) {
		return AttemptTask{}, ErrProtocol
	}
	return result, nil
}

func (client *AttemptClient) Source(ctx context.Context, taskID string) (RetainedChangeHandoff, error) {
	if !validID(taskID) {
		return RetainedChangeHandoff{}, ErrInvalidInput
	}
	var result RetainedChangeHandoff
	if err := client.client.call(ctx, "source", struct {
		TaskID string `json:"task_id"`
	}{TaskID: taskID}, &result); err != nil {
		return RetainedChangeHandoff{}, err
	}
	if !validSourceHandoff(result) {
		return RetainedChangeHandoff{}, ErrProtocol
	}
	return result, nil
}

func (client *AttemptClient) Block(ctx context.Context, detail string) (MutationResult, error) {
	if !validText(detail, 1, 4096) {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.attemptDetail(ctx, "block", detail)
}

func (client *AttemptClient) Fail(ctx context.Context, detail string) (MutationResult, error) {
	if !validText(detail, 0, 4096) {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.attemptDetail(ctx, "fail", detail)
}

func (client *AttemptClient) RequestHuman(ctx context.Context, input HumanQuestionInput) (MutationResult, error) {
	if !validID(input.IdempotencyKey) || !validText(input.Question, 1, 8192) || kernel.ValidateHumanOptions(input.Options) != nil {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "request_human", input)
}

func (client *AttemptClient) PeerStatus(ctx context.Context) (PeerStatus, error) {
	return client.PeerStatusPage(ctx, 0, 0, 0)
}

func (client *AttemptClient) PeerStatusPage(ctx context.Context, offset, targetOffset, expectedHead uint64) (PeerStatus, error) {
	return client.peerStatusPage(ctx, offset, targetOffset, expectedHead, true)
}

// PeerInboxPage reads only the caller's task-linked conversation. Target
// discovery stays available through PeerStatusPage when the caller needs it.
func (client *AttemptClient) PeerInboxPage(ctx context.Context, offset, expectedHead uint64) (PeerStatus, error) {
	return client.peerStatusPage(ctx, offset, 0, expectedHead, false)
}

func (client *AttemptClient) peerStatusPage(ctx context.Context, offset, targetOffset, expectedHead uint64, includeTargets bool) (PeerStatus, error) {
	if offset > uint64(^uint64(0)>>1)-1 || targetOffset > uint64(^uint64(0)>>1)-4 || expectedHead > uint64(^uint64(0)>>1) || expectedHead == 0 && (offset != 0 || targetOffset != 0) {
		return PeerStatus{}, ErrInvalidInput
	}
	if !includeTargets && targetOffset != 0 {
		return PeerStatus{}, ErrInvalidInput
	}
	var result PeerStatus
	if err := client.client.call(ctx, "peer_status", PeerStatusInput{Offset: offset, TargetOffset: targetOffset, ExpectedHead: expectedHead, IncludeTargets: includeTargets}, &result); err != nil {
		return PeerStatus{}, err
	}
	if !validPeerStatus(result) || expectedHead != 0 && result.Head != expectedHead {
		return PeerStatus{}, ErrProtocol
	}
	return result, nil
}

func (client *AttemptClient) PeerAsk(ctx context.Context, input PeerQuestionInput) (MutationResult, error) {
	if !validID(input.TargetTaskID) || !validID(input.IdempotencyKey) || !validText(input.Question, 1, 2048) {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "peer_ask", input)
}

func (client *AttemptClient) PeerAnswer(ctx context.Context, input PeerAnswerInput) (MutationResult, error) {
	if !validID(input.QuestionID) || !validID(input.IdempotencyKey) || input.ExpectedRevision == 0 || !validText(input.Answer, 1, 2048) {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "peer_answer", input)
}

func (client *AttemptClient) TerminalObserve(ctx context.Context, input TerminalObserveInput) (TerminalObservation, error) {
	if !validTerminalObservationInput(input) || input.Text {
		return TerminalObservation{}, ErrInvalidInput
	}
	var result TerminalObservation
	if err := client.client.call(ctx, "terminal_observe", input, &result); err != nil {
		return TerminalObservation{}, err
	}
	futureCursorReset := result.Gap && input.Cursor > result.Head && result.Cursor == result.Head
	if !validTerminalObservation(result) || result.ProjectID != input.ProjectID || result.TaskID != input.TaskID || result.RunID != input.RunID || result.Cursor != input.Cursor && !futureCursorReset || len(result.Payload) > int(input.MaxBytes) || (!result.Gap && result.NextCursor-input.Cursor > uint64(input.MaxBytes)) {
		return TerminalObservation{}, ErrProtocol
	}
	return result, nil
}

// SendBack returns a finished task of the attempt's project to its queue
// with a note; only an orchestrator's attempt is allowed to.
func (client *AttemptClient) SendBack(ctx context.Context, input SendBackInput) (MutationResult, error) {
	if !validID(input.TaskID) || !validText(input.Note, 1, 8192) {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "send_back", input)
}

func (client *AttemptClient) OverseerSnapshot(ctx context.Context) (OverseerSnapshot, error) {
	return client.overseerSnapshot(ctx, OverseerSnapshotInput{})
}

func (client *AttemptClient) OverseerTaskSnapshot(ctx context.Context, taskID string) (OverseerSnapshot, error) {
	if !validID(taskID) {
		return OverseerSnapshot{}, ErrInvalidInput
	}
	return client.overseerSnapshot(ctx, OverseerSnapshotInput{TaskID: taskID})
}

func (client *AttemptClient) OverseerSnapshotPage(ctx context.Context, input OverseerSnapshotInput) (OverseerSnapshot, error) {
	if !validOverseerSnapshotInput(input) {
		return OverseerSnapshot{}, ErrInvalidInput
	}
	return client.overseerSnapshot(ctx, input)
}

func (client *AttemptClient) overseerSnapshot(ctx context.Context, input OverseerSnapshotInput) (OverseerSnapshot, error) {
	var result OverseerSnapshot
	if err := client.client.call(ctx, "overseer_snapshot", input, &result); err != nil {
		return OverseerSnapshot{}, err
	}
	if !validOverseerSnapshot(result) {
		return OverseerSnapshot{}, ErrProtocol
	}
	return result, nil
}

func (client *AttemptClient) OverseerEnqueueTask(ctx context.Context, input OverseerTaskCreateInput) (MutationResult, error) {
	if !validOverseerTaskCreateInput(input) {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "overseer_enqueue_task", input)
}

func (client *AttemptClient) OverseerUpdateTask(ctx context.Context, input OverseerTaskUpdateInput) (MutationResult, error) {
	if !validOverseerTaskUpdateInput(input) {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "overseer_update_task", input)
}

func (client *AttemptClient) OverseerUpdateAgent(ctx context.Context, input OverseerAgentUpdateInput) (MutationResult, error) {
	if !validID(input.AgentID) || input.ExpectedRevision == 0 {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "overseer_update_agent", input)
}

func (client *AttemptClient) OverseerStopRun(ctx context.Context, input OverseerRunStopInput) (MutationResult, error) {
	if !validOverseerRunStopInput(input) {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "overseer_stop_run", input)
}

func (client *AttemptClient) OverseerReplaceRun(ctx context.Context, input OverseerRunReplaceInput) (MutationResult, error) {
	if !validOverseerRunReplaceInput(input) {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "overseer_replace_run", input)
}

func (client *AttemptClient) OverseerMessageWorker(ctx context.Context, input OverseerWorkerMessageInput) (MutationResult, error) {
	if !validOverseerWorkerMessageInput(input) {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "overseer_message_worker", input)
}

func (client *AttemptClient) OverseerInterruptWorker(ctx context.Context, input OverseerWorkerInterruptInput) (MutationResult, error) {
	if !validOverseerWorkerInterruptInput(input) {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "overseer_interrupt_worker", input)
}

func (client *AttemptClient) OverseerReplyHuman(ctx context.Context, input OverseerHumanReplyInput) (MutationResult, error) {
	if !validID(input.OperationID) || !validID(input.RequestID) || input.ExpectedRevision == 0 || !validText(input.Reply, 1, 8192) {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "overseer_reply_human", input)
}

func (client client) attemptDetail(ctx context.Context, method, detail string) (MutationResult, error) {
	return client.mutate(ctx, method, struct {
		Detail string `json:"detail"`
	}{Detail: detail})
}

func (client client) mutate(ctx context.Context, method string, params any) (MutationResult, error) {
	var result MutationResult
	if err := client.call(ctx, method, params, &result); err != nil {
		return MutationResult{}, err
	}
	if !validMutation(result) {
		return MutationResult{}, ErrProtocol
	}
	return result, nil
}

type requestEnvelope struct {
	Method string `json:"method"`
	Params any    `json:"params"`
}

type responseEnvelope struct {
	OK     *bool           `json:"ok"`
	Data   json.RawMessage `json:"data,omitempty"`
	Error  RemoteErrorCode `json:"error,omitempty"`
	Detail string          `json:"detail,omitempty"`
}

func (client client) call(ctx context.Context, method string, params, output any) error {
	current, err := loadToken(client.tokenPath)
	if err != nil || !current.same(client.token) {
		return ErrInvalidClient
	}
	encoded, err := json.Marshal(requestEnvelope{Method: method, Params: params})
	if err != nil || len(encoded)+requestPrelude > maxFrameBytes {
		return ErrInvalidInput
	}

	// Callers with an explicit deadline own their complete operation budget.
	// The default still bounds background callers, while an overseer mutation can
	// wait for its bounded native terminal effect instead of being cut off at 5s.
	cancel := func() {}
	if _, bounded := ctx.Deadline(); !bounded {
		ctx, cancel = context.WithTimeout(ctx, requestTimeout)
	}
	defer cancel()
	before, err := inspectSocket(client.socketPath)
	if err != nil || !before.same(client.socket) {
		return ErrInvalidClient
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", client.socketPath)
	if err != nil {
		return classifyTransport(ctx)
	}
	defer connection.Close()
	if err := verifySocketConnection(connection, before); err != nil {
		return err
	}
	if err := setConnectionDeadline(connection, ctx); err != nil {
		return classifyTransport(ctx)
	}
	stop := watchCancellation(ctx, connection)
	defer stop()

	payload := make([]byte, requestPrelude+len(encoded))
	payload[0] = client.domain
	copy(payload[1:requestPrelude], client.token.bearer[:])
	copy(payload[requestPrelude:], encoded)
	if err := writeFrame(connection, payload); err != nil {
		return classifyTransport(ctx)
	}
	outcomeReceipt := client.domain == attemptDomain && outcomeMethod(method)
	unix, ok := connection.(*net.UnixConn)
	if !ok {
		return ErrTransport
	}
	if !outcomeReceipt && unix.CloseWrite() != nil {
		return ErrTransport
	}
	response, err := readFrame(connection)
	if err != nil {
		return classifyFrameError(ctx, err)
	}
	if len(response) < responsePrelude || response[0] != client.domain {
		return ErrProtocol
	}
	responseErr := decodeResponse(response[responsePrelude:], output)
	if outcomeReceipt && responseErr == nil {
		result, ok := output.(*MutationResult)
		if !ok || result.Revision == 0 {
			responseErr = ErrProtocol
		}
	}
	if outcomeReceipt && !acknowledgeableOutcomeResponse(responseErr) {
		if err := client.revalidate(before); err != nil {
			return err
		}
		return responseErr
	}
	if outcomeReceipt {
		receipt, err := readFrame(connection)
		if err != nil {
			// Rejections before the server accepts an outcome call have no
			// receipt. Once dispatch begins, every outcome response does.
			if responseErr == nil && errors.Is(err, io.EOF) {
				return ErrProtocol
			}
			if responseErr == nil || !errors.Is(err, io.EOF) {
				return classifyFrameError(ctx, err)
			}
		} else {
			if len(receipt) != outcomeReceiptBytes {
				return ErrProtocol
			}
			if err := requireEOF(connection); err != nil {
				return classifyFrameError(ctx, err)
			}
			if err := client.revalidate(before); err != nil {
				return err
			}
			if err := writeFrame(connection, receipt); err != nil {
				return classifyTransport(ctx)
			}
			clear(receipt)
			if err := unix.CloseWrite(); err != nil {
				return ErrTransport
			}
			return responseErr
		}
	}
	if err := requireEOF(connection); err != nil {
		return classifyFrameError(ctx, err)
	}
	if err := client.revalidate(before); err != nil {
		return err
	}
	return responseErr
}

func verifySocketConnection(connection net.Conn, expected socketRecord) error {
	if err := verifyPeerEUID(connection); err != nil {
		return err
	}
	current, err := inspectSocket(connection.RemoteAddr().String())
	if err != nil || !current.same(expected) {
		return ErrInvalidClient
	}
	return nil
}

func (client client) revalidate(before socketRecord) error {
	after, err := inspectSocket(client.socketPath)
	if err != nil || !after.same(before) {
		return ErrInvalidClient
	}
	latest, err := loadToken(client.tokenPath)
	if err != nil || !latest.same(client.token) {
		return ErrInvalidClient
	}
	return nil
}

func outcomeMethod(method string) bool {
	return method == "succeed" || method == "block" || method == "fail"
}

func acknowledgeableOutcomeResponse(err error) bool {
	if err == nil {
		return true
	}
	var remote *RemoteError
	return errors.As(err, &remote)
}

func writeFrame(writer io.Writer, payload []byte) error {
	if len(payload) == 0 || len(payload) > maxFrameBytes {
		return ErrProtocol
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if err := writeAll(writer, header[:]); err != nil {
		return err
	}
	return writeAll(writer, payload)
}

func writeAll(writer io.Writer, value []byte) error {
	for len(value) > 0 {
		written, err := writer.Write(value)
		if err != nil {
			return err
		}
		if written < 1 || written > len(value) {
			return io.ErrShortWrite
		}
		value = value[written:]
	}
	return nil
}

func readFrame(reader io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > maxFrameBytes {
		return nil, ErrProtocol
	}
	payload := make([]byte, int(size))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func requireEOF(reader io.Reader) error {
	var extra [1]byte
	count, err := reader.Read(extra[:])
	if count != 0 || err == nil {
		return ErrProtocol
	}
	if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func decodeResponse(encoded []byte, output any) error {
	var envelope responseEnvelope
	if err := decodeExact(encoded, &envelope); err != nil {
		return ErrProtocol
	}
	if envelope.OK == nil {
		return ErrProtocol
	}
	if *envelope.OK {
		if envelope.Error != "" || envelope.Detail != "" || len(envelope.Data) == 0 || bytes.Equal(envelope.Data, []byte("null")) {
			return ErrProtocol
		}
		if err := decodeExact(envelope.Data, output); err != nil {
			return ErrProtocol
		}
		return nil
	}
	if len(envelope.Data) != 0 || !validRemoteCode(envelope.Error) || !validRemoteDetail(envelope.Detail) {
		return ErrProtocol
	}
	return &RemoteError{code: envelope.Error, detail: envelope.Detail}
}

// decodeExact reads one bounded JSON value with this protocol's exact grammar.
// A member neither side's build knows is ignored, so factoryctl, factoryd and
// factory-runner tolerate an added member instead of failing closed on it.
// Every bound, and every explicit required-member and value check made by the
// caller, still applies.
func decodeExact(encoded []byte, output any) error {
	if err := validateJSONNames(encoded); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	if err := decoder.Decode(output); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return ErrProtocol
	}
	return nil
}

const maxJSONDepth = 64

// validateJSONNames rejects byte sequences encoding/json intentionally accepts
// but this exact local protocol does not: malformed UTF-8, unpaired UTF-16
// escapes, and duplicate object names. The frame bound caps total work and
// storage; maxJSONDepth caps stack.
func validateJSONNames(encoded []byte) error { return validateJSON(encoded, true) }

// validateJSON preserves the local decoder's Unicode, duplicate-name and depth
// checks for an embedded foreign JSON document. MCP defines camel-case names,
// so only its enclosing local-API object uses canonicalJSONName.
func validateJSON(encoded []byte, canonicalNames bool) error {
	if len(encoded) == 0 || len(encoded) > maxFrameBytes || !utf8.Valid(encoded) || !validJSONUnicodeEscapes(encoded) {
		return ErrProtocol
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := validateJSONValue(decoder, 0, canonicalNames); err != nil {
		return ErrProtocol
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrProtocol
	}
	return nil
}

// validJSONUnicodeEscapes scans only JSON string boundaries and escapes. It
// prevents encoding/json from collapsing distinct lone surrogate escapes to
// U+FFFD; the ordinary decoder remains authority for all other JSON grammar.
func validJSONUnicodeEscapes(encoded []byte) bool {
	inString := false
	for index := 0; index < len(encoded); index++ {
		switch encoded[index] {
		case '"':
			inString = !inString
		case '\\':
			if !inString || index+1 >= len(encoded) {
				continue
			}
			if encoded[index+1] != 'u' {
				index++
				continue
			}
			unit, ok := jsonHexCodeUnit(encoded[index+2:])
			if !ok {
				return false
			}
			index += 5
			switch {
			case unit >= 0xdc00 && unit <= 0xdfff:
				return false
			case unit >= 0xd800 && unit <= 0xdbff:
				next := index + 1
				if next+6 > len(encoded) || encoded[next] != '\\' || encoded[next+1] != 'u' {
					return false
				}
				low, ok := jsonHexCodeUnit(encoded[next+2:])
				if !ok || low < 0xdc00 || low > 0xdfff {
					return false
				}
				index += 6
			}
		}
	}
	return true
}

func jsonHexCodeUnit(encoded []byte) (uint16, bool) {
	if len(encoded) < 4 {
		return 0, false
	}
	var value uint16
	for _, character := range encoded[:4] {
		value <<= 4
		switch {
		case character >= '0' && character <= '9':
			value |= uint16(character - '0')
		case character >= 'a' && character <= 'f':
			value |= uint16(character-'a') + 10
		case character >= 'A' && character <= 'F':
			value |= uint16(character-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

func validateJSONValue(decoder *json.Decoder, depth int, canonicalNames bool) error {
	if depth > maxJSONDepth {
		return ErrProtocol
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		names := make(map[string]struct{})
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok || canonicalNames && !canonicalJSONName(name) {
				return ErrProtocol
			}
			if _, duplicate := names[name]; duplicate {
				return ErrProtocol
			}
			names[name] = struct{}{}
			if err := validateJSONValue(decoder, depth+1, canonicalNames); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return ErrProtocol
		}
	case '[':
		for decoder.More() {
			if err := validateJSONValue(decoder, depth+1, canonicalNames); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return ErrProtocol
		}
	default:
		return ErrProtocol
	}
	return nil
}

func canonicalJSONName(name string) bool {
	if len(name) == 0 || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for index := 1; index < len(name); index++ {
		character := name[index]
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func validRemoteCode(code RemoteErrorCode) bool {
	switch code {
	case RemoteInvalidRequest, RemoteUnauthorized, RemoteForbidden, RemoteNotFound, RemoteConflict, RemoteRevisionConflict, RemoteTooLarge, RemoteUnavailable, RemoteCleanupUnresolved, RemoteInternal:
		return true
	default:
		return false
	}
}

func validID(value string) bool {
	if len(value) != 32 || value == strings.Repeat("0", 32) {
		return false
	}
	decoded := make([]byte, 16)
	_, err := hex.Decode(decoded, []byte(value))
	return err == nil && value == strings.ToLower(value)
}

// validOptionalID accepts a task's assigned agent: empty is any eligible
// worker in the project, until admission claims it for one.
func validOptionalID(value string) bool { return value == "" || validID(value) }

func validText(value string, minimum, maximum int) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, 0) && len(value) >= minimum && len(value) <= maximum
}

func validSnapshot(snapshot DashboardSnapshot) bool {
	if snapshot.Factory.Capacity < 1 || snapshot.Factory.Capacity > 1024 || snapshot.Factory.ActiveRuns > 1025 || snapshot.Factory.ActiveRuns > snapshot.Factory.Capacity+1 || snapshot.Factory.Revision == 0 || snapshot.Projects == nil || snapshot.Agents == nil || snapshot.Tasks == nil || len(snapshot.Projects) > maxSnapshotEntries || len(snapshot.Agents) > maxSnapshotEntries || len(snapshot.Tasks) > maxSnapshotEntries {
		return false
	}
	for _, project := range snapshot.Projects {
		if !validID(project.ID) || !validText(project.Name, 1, 128) || project.RunBudgetLimit != 0 && project.RunsUsed > project.RunBudgetLimit || project.MaxRunSeconds > 86400 || project.Revision == 0 {
			return false
		}
	}
	for _, agent := range snapshot.Agents {
		if !validID(agent.ID) || !validID(agent.ProjectID) || !validText(agent.Name, 1, 128) || agent.Role != "worker" && agent.Role != "orchestrator" || !validProvider(agent.Provider) || agent.AccountID != "" && !validID(agent.AccountID) || agent.Revision == 0 {
			return false
		}
	}
	for _, task := range snapshot.Tasks {
		if !validID(task.ID) || !validID(task.ProjectID) || !validOptionalID(task.AssignedAgentID) || !validID(task.IncarnationID) || task.WorkRevision == 0 || !validText(task.Title, 1, 1024) || !validTaskStatus(task.Status) || task.Priority < -1_000_000 || task.Priority > 1_000_000 || task.Revision == 0 {
			return false
		}
	}
	return true
}

func validOverseerTaskCreateInput(input OverseerTaskCreateInput) bool {
	return validID(input.ID) && validOptionalID(input.AssignedAgentID) && validID(input.IncarnationID) && input.ID != input.IncarnationID &&
		validText(input.Title, 1, 1024) && validText(input.Body, 0, 131072) && input.Priority >= -1_000_000 && input.Priority <= 1_000_000
}

func validOverseerTaskUpdateInput(input OverseerTaskUpdateInput) bool {
	if input.PublicationState != "" {
		return validID(input.TaskID) && input.ExpectedRevision > 0 && input.Title == nil && input.Body == nil && input.Priority == nil && input.AssignedAgentID == nil && !input.Cancel && !input.Retry && !input.RemoveAttachments && (input.PublicationState == "succeeded" || input.PublicationState == "failed")
	}
	if input.RemoveAttachments {
		return validID(input.TaskID) && input.ExpectedRevision > 0 && input.Title == nil && input.Body == nil && input.Priority == nil && input.AssignedAgentID == nil && !input.Cancel && !input.Retry
	}
	if !validID(input.TaskID) || input.ExpectedRevision == 0 || input.Title == nil && input.Body == nil && input.Priority == nil && input.AssignedAgentID == nil && !input.Cancel && !input.Retry {
		return false
	}
	if input.Retry && (input.Title != nil || input.Body != nil || input.Priority != nil || input.Cancel) {
		return false
	}
	if input.Title != nil && !validText(*input.Title, 1, 1024) || input.Body != nil && !validText(*input.Body, 0, 131072) {
		return false
	}
	if input.Priority != nil && (*input.Priority < -1_000_000 || *input.Priority > 1_000_000) {
		return false
	}
	return input.AssignedAgentID == nil || validID(*input.AssignedAgentID)
}

func validOverseerSnapshot(snapshot OverseerSnapshot) bool {
	if !validID(snapshot.ProjectID) || snapshot.Head == 0 || snapshot.Agents == nil || snapshot.Tasks == nil || snapshot.Runs == nil || snapshot.Questions == nil || snapshot.PeerQuestions == nil || snapshot.History == nil || snapshot.Handoffs == nil || len(snapshot.Agents) > kernel.OverseerSnapshotPageSize || len(snapshot.Tasks) > kernel.OverseerSnapshotPageSize || len(snapshot.Runs) > kernel.OverseerSnapshotPageSize || len(snapshot.Questions) > kernel.OverseerSnapshotPageSize || len(snapshot.PeerQuestions) > 1 || len(snapshot.History) > kernel.OverseerSnapshotPageSize || len(snapshot.Handoffs) > kernel.OverseerSnapshotPageSize {
		return false
	}
	for _, handoff := range snapshot.Handoffs {
		if !validRetainedChangeHandoff(handoff) {
			return false
		}
	}
	if snapshot.NextOffset != nil && *snapshot.NextOffset == 0 || snapshot.NextTextOffset != nil && *snapshot.NextTextOffset == 0 {
		return false
	}
	for _, agent := range snapshot.Agents {
		if !validID(agent.ID) || agent.ProjectID != snapshot.ProjectID || !validText(agent.Name, 1, 128) || (agent.Role != "worker" && agent.Role != "orchestrator") || !validProvider(agent.Provider) || agent.Revision == 0 {
			return false
		}
	}
	for _, task := range snapshot.Tasks {
		if !validID(task.ID) || task.ProjectID != snapshot.ProjectID || !validOptionalID(task.AssignedAgentID) || !validText(task.Title, 1, 1024) || !validText(task.Objective, 0, 131072) || !validTaskStatus(task.Status) || task.Priority < -1_000_000 || task.Priority > 1_000_000 || !validText(task.BlockedReason, 0, 8192) || !validText(task.Result, 0, 131072) || task.Revision == 0 {
			return false
		}
	}
	for _, run := range snapshot.Runs {
		if !validID(run.ID) || !validID(run.AgentID) || !validID(run.TaskID) || (run.Phase != "admitted" && run.Phase != "running" && run.Phase != "finalizing") || run.Revision == 0 {
			return false
		}
	}
	for _, question := range snapshot.Questions {
		if !validID(question.ID) || !validID(question.AgentID) || !validID(question.TaskID) || (question.Status != "open" && question.Status != "delivering" && question.Status != "delivery_unknown") || question.Revision == 0 || !validText(question.Question, 1, 8192) {
			return false
		}
	}
	for _, question := range snapshot.PeerQuestions {
		if !validPeerQuestion(question) {
			return false
		}
	}

	for _, item := range snapshot.History {
		if !validID(item.OperationID) || !validID(item.TaskID) || !validID(item.RunID) || item.SuccessorTaskID != "" && !validID(item.SuccessorTaskID) || (item.Kind != "message" && item.Kind != "interrupt" && item.Kind != "stop" && item.Kind != "replace") || (item.Actor != "operator" && item.Actor != "orchestrator") || !validText(item.Payload, 0, kernel.MaxTaskInterventionPayloadBytes) || (item.State != "pending" && item.State != "delivered" && item.State != "unknown" && item.State != "rejected") || !validText(item.Detail, 0, 4096) {
			return false
		}
	}
	return true
}

func validHandoffSourcePath(value, changeID string) bool {
	return validText(value, 1, 4096) && filepath.IsAbs(value) && filepath.Clean(value) == value && filepath.Base(value) == changeID
}

func validHandoffGitDirectory(value string) bool {
	return validText(value, 1, 4096) && filepath.IsAbs(value) && filepath.Clean(value) == value && filepath.Base(value) == ".git"
}

func validOverseerSnapshotInput(input OverseerSnapshotInput) bool {
	if input.TaskID != "" && !validID(input.TaskID) || input.ExpectedHead > uint64(^uint64(0)>>1) || input.Offset > uint64(^uint64(0)>>1)-kernel.OverseerSnapshotPageSize || input.TextOffset > 131072 || input.TextOffset != 0 && input.TaskID == "" {
		return false
	}
	return input.ExpectedHead != 0 || input.Offset == 0 && input.TextOffset == 0
}

func validOverseerWorkerMessageInput(input OverseerWorkerMessageInput) bool {
	return validID(input.OperationID) && validID(input.TaskID) && input.ExpectedTaskRevision != 0 && validID(input.RunID) && input.ExpectedRunRevision != 0 && validText(input.Message, 1, 8192)
}

func validOverseerWorkerInterruptInput(input OverseerWorkerInterruptInput) bool {
	return validID(input.OperationID) && validID(input.TaskID) && input.ExpectedTaskRevision != 0 && validID(input.RunID) && input.ExpectedRunRevision != 0
}

func validOverseerRunStopInput(input OverseerRunStopInput) bool {
	return validID(input.OperationID) && validID(input.TaskID) && input.ExpectedTaskRevision != 0 && validID(input.RunID) && input.ExpectedRunRevision != 0
}

func validOverseerRunReplaceInput(input OverseerRunReplaceInput) bool {
	return validOverseerRunStopInput(input.OverseerRunStopInput) && validID(input.SuccessorTaskID) && validID(input.SuccessorIncarnationID) && input.SuccessorTaskID != input.SuccessorIncarnationID && validText(input.Instruction, 1, 131072)
}

func validProvider(value string) bool {
	return value == "shell" || value == "claude_code" || value == "codex"
}

func validWebStatus(status WebStatus) bool {
	if status.State != "ready" && status.State != "stopped" && status.State != "degraded" {
		return false
	}
	if status.State == "stopped" {
		return !status.Ready && status.Address == "" && status.Path == "" && status.Origins == nil && status.ActiveClients == 0 && status.RevokedClients == 0 && status.ActiveChallenges == 0 && validBuildIdentity(status.Build)
	}
	if status.Ready != (status.State == "ready") || !validText(status.Address, 1, 128) || !validText(status.Path, 1, 128) || len(status.Origins) == 0 || len(status.Origins) > 8 {
		return false
	}
	if !validBuildIdentity(status.Build) {
		return false
	}
	for _, origin := range status.Origins {
		if !validText(origin, 1, 4096) || strings.ContainsAny(origin, " \t\r\n*") {
			return false
		}
	}
	return true
}

func validBuildIdentity(identity BuildIdentity) bool {
	if !validText(identity.Version, 0, 64) || !validText(identity.Source, 0, 64) || !validText(identity.Target, 0, 32) || !validText(identity.BuildID, 0, 128) {
		return false
	}
	if identity.Release {
		return identity.Version != "" && identity.Source != "" && identity.Target != "" && identity.BuildID != ""
	}
	return true
}

func validRemoteStatus(status RemoteStatus) bool {
	if !validNodeID(status.NodeID) || !validText(status.RelayOrigin, 1, 4096) || strings.ContainsAny(status.RelayOrigin, " \t\r\n*") {
		return false
	}
	if !strings.HasPrefix(status.RelayOrigin, "wss://") && !strings.HasPrefix(status.RelayOrigin, "ws://") {
		return false
	}
	// A lost host connection closes every relayed session under one lock, so a
	// disconnected connector reporting live sessions is not a state the daemon
	// can occupy.
	return status.Sessions >= 0 && (status.Connected || status.Sessions == 0)
}

// validNodeID is the relay's self-certifying node id: exactly 32 characters of
// lowercase RFC 4648 base32 without padding.
func validNodeID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '2' && character <= '7' {
			continue
		}
		return false
	}
	return true
}

func validDigest(value string) bool {
	if len(value) != 64 || value == strings.Repeat("0", 64) {
		return false
	}
	decoded := make([]byte, 32)
	_, err := hex.Decode(decoded, []byte(value))
	return err == nil && value == strings.ToLower(value)
}

func validWebClient(client WebClient) bool {
	if !validID(client.ID) || client.CapabilityMask == 0 || client.CapabilityMask&1 == 0 || client.CapabilityMask&^31 != 0 || client.Revision == 0 || client.UpdatedAtMs < client.CreatedAtMs {
		return false
	}
	return client.RevokedAtMs == nil || *client.RevokedAtMs >= client.CreatedAtMs && *client.RevokedAtMs <= client.UpdatedAtMs
}

func validWebClientPage(page WebClientPage) bool {
	if page.Clients == nil || len(page.Clients) > 128 {
		return false
	}
	seen := make(map[string]struct{}, len(page.Clients))
	for index, client := range page.Clients {
		if !validWebClient(client) {
			return false
		}
		if index > 0 {
			previous := page.Clients[index-1]
			if client.CreatedAtMs < previous.CreatedAtMs || client.CreatedAtMs == previous.CreatedAtMs && client.ID <= previous.ID {
				return false
			}
		}
		if _, ok := seen[client.ID]; ok {
			return false
		}
		seen[client.ID] = struct{}{}
	}
	if page.NextAfter != nil {
		if len(page.Clients) != 128 || !validID(*page.NextAfter) || *page.NextAfter != page.Clients[len(page.Clients)-1].ID {
			return false
		}
	}
	return true
}

func validWebRevokeResult(result WebRevokeResult) bool {
	return validID(result.ID) && result.Revision > 0
}

func validTaskStatus(status string) bool {
	switch status {
	case "queued", "running", "blocked", "succeeded", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func classifyTransport(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrTransport
}

func classifyFrameError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return context.DeadlineExceeded
	}
	if errors.Is(err, ErrProtocol) {
		return ErrProtocol
	}
	return ErrTransport
}

func setConnectionDeadline(connection net.Conn, ctx context.Context) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return ErrTransport
	}
	return connection.SetDeadline(deadline)
}

type deadlineConnection interface {
	SetDeadline(time.Time) error
}

func watchCancellation(ctx context.Context, connection deadlineConnection) func() {
	finished := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		select {
		case <-ctx.Done():
			_ = connection.SetDeadline(time.Now())
		case <-finished:
		}
	}()
	return func() {
		close(finished)
		wait.Wait()
	}
}

func (left credential) equal(right credential) bool {
	return subtle.ConstantTimeCompare(left[:], right[:]) == 1
}

func (client *OperatorClient) SelectAgentModel(ctx context.Context, input AgentModelSelectInput) (MutationResult, error) {
	if !validAgentModelSelectInput(input) {
		return MutationResult{}, ErrInvalidInput
	}
	return client.client.mutate(ctx, "agent_select_model", input)
}
