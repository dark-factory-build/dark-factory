package api

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

const (
	maxFrameBytes          = 1 << 20
	MaxRecoveryResultBytes = 65536
	// Four metadata rows fit the frame even when every text byte is JSON-escaped.
	MaxContentPageItems = 4
	maxSnapshotEntries  = 4096
	credentialBytes     = 32
)

var (
	ErrInvalidClient   = errors.New("local API client configuration is invalid")
	ErrInvalidListener = errors.New("local API listener configuration is invalid")
	ErrInvalidInput    = errors.New("local API input is invalid")
	ErrProtocol        = errors.New("local API protocol is invalid")
	ErrTransport       = errors.New("local API transport failed")
)

type RemoteErrorCode string

const (
	RemoteInvalidRequest    RemoteErrorCode = "invalid_request"
	RemoteUnauthorized      RemoteErrorCode = "unauthorized"
	RemoteForbidden         RemoteErrorCode = "forbidden"
	RemoteNotFound          RemoteErrorCode = "not_found"
	RemoteConflict          RemoteErrorCode = "conflict"
	RemoteRevisionConflict  RemoteErrorCode = "revision_conflict"
	RemoteTooLarge          RemoteErrorCode = "too_large"
	RemoteUnavailable       RemoteErrorCode = "unavailable"
	RemoteCleanupUnresolved RemoteErrorCode = "cleanup_unresolved"
	RemoteInternal          RemoteErrorCode = "internal"
)

type RemoteError struct {
	code RemoteErrorCode
}

func (err *RemoteError) Error() string {
	switch err.code {
	case RemoteInvalidRequest:
		return "local API rejected the request"
	case RemoteUnauthorized:
		return "local API credential is unauthorized"
	case RemoteForbidden:
		return "local API request is forbidden"
	case RemoteNotFound:
		return "local API entity was not found"
	case RemoteConflict:
		return "local API request conflicts with durable state"
	case RemoteRevisionConflict:
		return "local API revision is stale"
	case RemoteTooLarge:
		return "local API request exceeds a bound"
	case RemoteUnavailable:
		return "local API is unavailable"
	case RemoteCleanupUnresolved:
		return "local API completed revocation but could not prove browser cleanup"
	case RemoteInternal:
		return "local API failed internally"
	default:
		return "local API returned an invalid error"
	}
}

func (err *RemoteError) Code() RemoteErrorCode { return err.code }

type HealthStatus struct {
	Ready bool `json:"ready"`
}

// AgentPaths is the live worker's sampled modified-directory view. Paths are
// relative to the run's change directory; they are not the worker's current
// working directory.
type AgentPaths struct {
	AgentID string   `json:"agent_id"`
	RunID   string   `json:"run_id,omitempty"`
	Paths   []string `json:"paths"`
}

type AgentPathsInput struct {
	AgentID string `json:"agent_id"`
}

func validAgentPathsInput(value AgentPathsInput) bool { return validID(value.AgentID) }
func validAgentPaths(value AgentPaths) bool {
	if !validID(value.AgentID) || value.RunID != "" && !validID(value.RunID) || value.Paths == nil || len(value.Paths) > 16 {
		return false
	}
	for _, path := range value.Paths {
		if !validText(path, 0, 4096) || strings.HasPrefix(path, "/") {
			return false
		}
	}
	return true
}

// WebStatus is the bounded, non-secret operator view of the loopback browser
// adapter. It intentionally contains no challenge, key, token or client
// identity data.
type WebStatus struct {
	State            string   `json:"state"`
	Ready            bool     `json:"ready"`
	Address          string   `json:"address"`
	Path             string   `json:"path"`
	Origins          []string `json:"origins"`
	ActiveClients    uint64   `json:"active_clients"`
	RevokedClients   uint64   `json:"revoked_clients"`
	ActiveChallenges uint64   `json:"active_challenges"`
}

type WebClient struct {
	ID             string  `json:"id"`
	CapabilityMask uint8   `json:"capability_mask"`
	Revision       uint64  `json:"revision"`
	CreatedAtMs    uint64  `json:"created_at_ms"`
	UpdatedAtMs    uint64  `json:"updated_at_ms"`
	RevokedAtMs    *uint64 `json:"revoked_at_ms"`
}

type WebClientPage struct {
	Clients   []WebClient `json:"clients"`
	NextAfter *string     `json:"next_after"`
}

type WebRevokeResult struct {
	ID       string `json:"id"`
	Revision uint64 `json:"revision"`
}

// RemoteStatus is the bounded, non-secret operator view of the outbound relay
// connector. It carries no ticket, key, challenge or controller identity.
type RemoteStatus struct {
	NodeID      string `json:"node_id"`
	RelayOrigin string `json:"relay_origin"`
	Connected   bool   `json:"connected"`
	Sessions    int    `json:"sessions"`
}

type MutationResult struct {
	Head         uint64                      `json:"head"`
	Revision     uint64                      `json:"revision"`
	Intervention *OverseerInterventionResult `json:"intervention,omitempty"`
	HumanReply   *OverseerHumanReplyResult   `json:"human_reply,omitempty"`
}

// DiscoveredAccount is a non-secret identity published by a provider login.
// It deliberately carries neither credentials nor credential-derived tokens.
type DiscoveredAccount struct {
	Provider               string `json:"provider"`
	Home                   string `json:"home"`
	Label                  string `json:"label"`
	Email                  string `json:"email,omitempty"`
	Organization           string `json:"organization,omitempty"`
	DefaultModel           string `json:"default_model,omitempty"`
	DefaultReasoningEffort string `json:"default_reasoning_effort,omitempty"`
	LinkedID               string `json:"linked_id,omitempty"`
	UnavailableReason      string `json:"unavailable_reason,omitempty"`
}

type Accounts struct {
	Accounts   []DiscoveredAccount `json:"accounts"`
	NextOffset *uint32             `json:"next_offset,omitempty"`
}
type AccountLinkInput struct {
	Provider string `json:"provider"`
	Home     string `json:"home"`
	Label    string `json:"label"`
}
type AgentAccountSelectInput struct {
	AgentID          string `json:"agent_id"`
	ExpectedRevision uint64 `json:"expected_revision"`
	AccountID        string `json:"account_id"`
}

func validDiscoveredAccount(value DiscoveredAccount) bool {
	provider, err := kernel.ParseProvider(value.Provider)
	return err == nil && provider != kernel.ProviderShell && validText(value.Home, 1, 1024) && value.Home[0] == '/' && validText(value.Label, 1, 128) &&
		validText(value.Email, 0, 128) && validText(value.Organization, 0, 128) && validText(value.DefaultModel, 0, 128) && validText(value.DefaultReasoningEffort, 0, 128) && validText(value.UnavailableReason, 0, 128) && (value.LinkedID == "" || validID(value.LinkedID))
}
func validAccounts(value Accounts) bool {
	if value.Accounts == nil || len(value.Accounts) > 4096 || value.NextOffset != nil && *value.NextOffset == 0 {
		return false
	}
	for _, account := range value.Accounts {
		if !validDiscoveredAccount(account) {
			return false
		}
	}
	return true
}
func validAccountLinkInput(value AccountLinkInput) bool {
	return validDiscoveredAccount(DiscoveredAccount{Provider: value.Provider, Home: value.Home, Label: value.Label})
}
func validAgentAccountSelectInput(value AgentAccountSelectInput) bool {
	return validID(value.AgentID) && validID(value.AccountID) && value.ExpectedRevision != 0
}

type OverseerHumanReplyResult struct {
	RequestID string `json:"request_id"`
	State     string `json:"state"`
}

type HumanRequest struct {
	ID       string   `json:"id"`
	RunID    string   `json:"run_id"`
	TaskID   string   `json:"task_id"`
	AgentID  string   `json:"agent_id"`
	Status   string   `json:"status"`
	Revision uint64   `json:"revision"`
	Question string   `json:"question"`
	Options  []string `json:"options"`
}

type HumanRequestList struct {
	Requests []HumanRequest `json:"requests"`
}

func validHumanRequestList(value HumanRequestList) bool {
	if value.Requests == nil || len(value.Requests) > 1024 {
		return false
	}
	for _, request := range value.Requests {
		if !validID(request.ID) || !validID(request.RunID) || !validID(request.TaskID) || !validID(request.AgentID) || request.Revision == 0 || !validText(request.Question, 1, 8192) || request.Status != "open" && request.Status != "delivering" && request.Status != "delivery_unknown" || request.Options == nil || kernel.ValidateHumanOptions(request.Options) != nil {
			return false
		}
	}
	return true
}

func validMutation(result MutationResult) bool {
	if result.Revision == 0 {
		return false
	}
	if result.Intervention != nil && (!validID(result.Intervention.OperationID) || (result.Intervention.State != "delivered" && result.Intervention.State != "unknown" && result.Intervention.State != "rejected") || !validText(result.Intervention.Detail, 0, 4096)) {
		return false
	}
	return result.HumanReply == nil || validID(result.HumanReply.RequestID) && (result.HumanReply.State == "resolved" || result.HumanReply.State == "delivery_unknown")
}

type OverseerInterventionResult struct {
	OperationID string `json:"operation_id"`
	State       string `json:"state"`
	Detail      string `json:"detail"`
}

// AttemptTask is the exact private task text visible only to the authenticated
// live attempt that owns it.
type AttemptTask struct {
	Task                   string `json:"task"`
	TaskID                 string `json:"task_id,omitempty"`
	IncarnationID          string `json:"incarnation_id,omitempty"`
	WorkRevision           uint64 `json:"work_revision,omitempty"`
	ChangeID               string `json:"change_id,omitempty"`
	AdmittedChangeRevision uint64 `json:"admitted_change_revision,omitempty"`
	ChangeRevision         uint64 `json:"change_revision,omitempty"`
	BaseCommit             string `json:"base_commit,omitempty"`
}

// TerminalObserveInput identifies one exact attempt terminal. The API derives
// authority from the bearer and requires all three durable identities to
// match; cursor is a byte cursor in the runner's bounded replay ring.
type TerminalObserveInput struct {
	ProjectID string `json:"project_id"`
	TaskID    string `json:"task_id"`
	RunID     string `json:"run_id"`
	Cursor    uint64 `json:"cursor"`
	MaxBytes  uint32 `json:"max_bytes"`
}

type TerminalObservation struct {
	ProjectID  string `json:"project_id"`
	TaskID     string `json:"task_id"`
	RunID      string `json:"run_id"`
	Cursor     uint64 `json:"cursor"`
	NextCursor uint64 `json:"next_cursor"`
	Floor      uint64 `json:"floor"`
	Head       uint64 `json:"head"`
	Source     string `json:"source"`
	Gap        bool   `json:"gap"`
	Omitted    uint64 `json:"omitted"`
	Payload    []byte `json:"payload"`
}

// MarshalDisplayJSON renders readable, terminal-safe text for CLI/MCP without
// changing the byte payload or raw cursors on the local API wire.
func (value TerminalObservation) MarshalDisplayJSON() ([]byte, error) {
	type plain TerminalObservation
	encoded, err := json.Marshal(struct {
		plain
		Payload string `json:"payload"`
	}{plain(value), string(value.Payload)})
	if err != nil {
		return nil, err
	}
	return terminalSafeJSON(nil, encoded), nil
}

func validTerminalObservationInput(input TerminalObserveInput) bool {
	return validID(input.ProjectID) && validID(input.TaskID) && validID(input.RunID) && input.MaxBytes > 0 && input.MaxBytes <= 65536
}

func validTerminalObservation(value TerminalObservation) bool {
	return validID(value.ProjectID) && validID(value.TaskID) && validID(value.RunID) && value.NextCursor >= value.Cursor && value.Floor <= value.Head && value.NextCursor <= value.Head && (value.Source == "stored" || value.Source == "none") && len(value.Payload) <= 65536 && value.Omitted <= value.NextCursor-value.Cursor && uint64(len(value.Payload)) == value.NextCursor-value.Cursor-value.Omitted
}

// Project content is deliberately a small wire DTO. Bodies are never placed
// in list responses; callers use the explicit bounded body reader.
type Content struct {
	ID               string `json:"id"`
	ProjectID        string `json:"project_id"`
	Kind             string `json:"kind"`
	Title            string `json:"title"`
	Description      string `json:"description"`
	Body             string `json:"body,omitempty"`
	Author           string `json:"author"`
	SourceReferences string `json:"source_references"`
	ObjectFormat     string `json:"object_format,omitempty"`
	Commit           string `json:"commit,omitempty"`
	Path             string `json:"path,omitempty"`
	Revision         uint64 `json:"revision"`
	Deprecated       bool   `json:"deprecated"`
	LatestRevision   uint64 `json:"latest_revision"`
}
type ContentList struct {
	Items      []Content `json:"items"`
	NextOffset uint64    `json:"next_offset,omitempty"`
}
type ContentBody struct {
	ID         string `json:"id"`
	Revision   uint64 `json:"revision"`
	Offset     uint64 `json:"offset"`
	Body       string `json:"body"`
	NextOffset uint64 `json:"next_offset,omitempty"`
	Complete   bool   `json:"complete"`
}
type ContentEvidence struct {
	ID              string `json:"id"`
	ProjectID       string `json:"project_id"`
	ContentID       string `json:"content_id"`
	ContentRevision uint64 `json:"content_revision"`
	TestedSource    string `json:"tested_source"`
	Environment     string `json:"environment"`
	Result          string `json:"result"`
	Location        string `json:"location"`
	Evaluator       string `json:"evaluator"`
	Judgment        string `json:"judgment"`
}
type ContentEvidenceList struct {
	Items      []ContentEvidence `json:"items"`
	NextOffset uint64            `json:"next_offset,omitempty"`
}
type ContentAttachment struct {
	TaskID           string `json:"task_id"`
	ProjectID        string `json:"project_id"`
	TaskWorkRevision uint64 `json:"task_work_revision"`
	ContentID        string `json:"content_id"`
	ContentRevision  uint64 `json:"content_revision"`
	AttachedAtMs     uint64 `json:"attached_at_ms"`
}
type ContentAttachments struct {
	Items []ContentAttachment `json:"items"`
}

type Outcome struct {
	ID                    string                 `json:"id"`
	ProjectID             string                 `json:"project_id"`
	Revision              uint64                 `json:"revision"`
	Document              kernel.OutcomeDocument `json:"document"`
	Kind                  string                 `json:"kind"`
	Objective             string                 `json:"objective"`
	Criteria              string                 `json:"criteria"`
	State                 string                 `json:"state"`
	Author                string                 `json:"author"`
	Authority             string                 `json:"authority"`
	ObjectiveWorkRevision uint64                 `json:"objective_work_revision"`
	Stale                 bool                   `json:"stale"`
	MissingReferences     []string               `json:"missing_references,omitempty"`
}
type OutcomeList struct {
	Items      []Outcome `json:"items"`
	NextOffset uint64    `json:"next_offset,omitempty"`
}
type OutcomeWriteInput struct {
	ID               string                 `json:"id"`
	ProjectID        string                 `json:"project_id"`
	Document         kernel.OutcomeDocument `json:"document"`
	ExpectedRevision uint64                 `json:"expected_revision,omitempty"`
}
type OutcomeReadInput struct {
	ProjectID string `json:"project_id"`
	ID        string `json:"id"`
	Revision  uint64 `json:"revision,omitempty"`
}
type OutcomeListInput struct {
	ProjectID string `json:"project_id"`
	Offset    uint64 `json:"offset,omitempty"`
	Limit     uint64 `json:"limit,omitempty"`
}
type ContentInput struct {
	ID               string `json:"id"`
	ProjectID        string `json:"project_id"`
	Kind             string `json:"kind"`
	Title            string `json:"title"`
	Description      string `json:"description"`
	Body             string `json:"body"`
	SourceReferences string `json:"source_references"`
	Commit           string `json:"commit,omitempty"`
	Path             string `json:"path,omitempty"`
	ExpectedRevision uint64 `json:"expected_revision,omitempty"`
}
type ContentListInput struct {
	ProjectID string `json:"project_id,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Offset    uint64 `json:"offset,omitempty"`
	Limit     uint64 `json:"limit,omitempty"`
}
type ContentReadInput struct {
	ID       string `json:"id"`
	Revision uint64 `json:"revision"`
}
type ContentBodyInput struct {
	ID       string `json:"id"`
	Revision uint64 `json:"revision"`
	Offset   uint64 `json:"offset"`
	Limit    uint64 `json:"limit"`
}
type ContentEvidenceInput struct {
	ID              string `json:"id"`
	ProjectID       string `json:"project_id"`
	ContentID       string `json:"content_id"`
	ContentRevision uint64 `json:"content_revision"`
	TestedSource    string `json:"tested_source"`
	Environment     string `json:"environment"`
	Result          string `json:"result"`
	Location        string `json:"location"`
	Judgment        string `json:"judgment"`
}
type ContentAttachInput struct {
	TaskID          string `json:"task_id"`
	ProjectID       string `json:"project_id"`
	ContentID       string `json:"content_id"`
	ContentRevision uint64 `json:"content_revision"`
}
type ContentEvidenceListInput struct {
	ProjectID       string `json:"project_id,omitempty"`
	ContentID       string `json:"content_id"`
	ContentRevision uint64 `json:"content_revision"`
	Offset          uint64 `json:"offset,omitempty"`
	Limit           uint64 `json:"limit,omitempty"`
}
type ContentAttachmentsInput struct {
	ProjectID        string `json:"project_id,omitempty"`
	TaskID           string `json:"task_id,omitempty"`
	TaskWorkRevision uint64 `json:"task_work_revision"`
}

func (AttemptTask) String() string   { return "AttemptTask(<redacted>)" }
func (AttemptTask) GoString() string { return "AttemptTask(<redacted>)" }

// MarshalJSON keeps private task text safe when factoryctl prints it inside a
// provider terminal. encoding/json already escapes C0 controls; this also
// escapes DEL and C1 controls, which terminal emulators may interpret.
func (task AttemptTask) MarshalJSON() ([]byte, error) {
	if !validAttemptTask(task) {
		return nil, ErrInvalidInput
	}
	type plain AttemptTask
	quoted, err := json.Marshal(plain(task))
	if err != nil {
		return nil, err
	}
	return terminalSafeJSON(nil, quoted), nil
}

func terminalSafeJSON(dst, encoded []byte) []byte {
	const hex = "0123456789abcdef"
	for len(encoded) > 0 {
		value, width := utf8.DecodeRune(encoded)
		if value >= 0x7f && value <= 0x9f {
			dst = append(dst, '\\', 'u', '0', '0', hex[value>>4], hex[value&0xf])
		} else {
			dst = append(dst, encoded[:width]...)
		}
		encoded = encoded[width:]
	}
	return dst
}

func validAttemptTask(task AttemptTask) bool {
	if !validText(task.Task, 0, 131072) {
		return false
	}
	if task.TaskID == "" && task.IncarnationID == "" && task.WorkRevision == 0 && task.ChangeID == "" && task.AdmittedChangeRevision == 0 && task.ChangeRevision == 0 && task.BaseCommit == "" {
		return true
	}
	if !validID(task.TaskID) || !validID(task.IncarnationID) || task.WorkRevision == 0 || !validID(task.ChangeID) || task.AdmittedChangeRevision == 0 || task.ChangeRevision == 0 || len(task.BaseCommit) != 40 && len(task.BaseCommit) != 64 {
		return false
	}
	for _, ch := range task.BaseCommit {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			return false
		}
	}
	return true
}

type FactorySummary struct {
	DispatchEnabled bool   `json:"dispatch_enabled"`
	Capacity        uint16 `json:"capacity"`
	ActiveRuns      uint16 `json:"active_runs"`
	Revision        uint64 `json:"revision"`
}

type ProjectSummary struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	RunBudgetLimit uint64 `json:"run_budget_limit"`
	RunsUsed       uint64 `json:"runs_used"`
	MaxRunSeconds  uint32 `json:"max_run_seconds"`
	Revision       uint64 `json:"revision"`
}

type AgentSummary struct {
	ID               string `json:"id"`
	ProjectID        string `json:"project_id"`
	Name             string `json:"name"`
	Role             string `json:"role"`
	Provider         string `json:"provider"`
	AccountID        string `json:"account_id"`
	Paused           bool   `json:"paused"`
	Archived         bool   `json:"archived"`
	Model            string `json:"model"`
	ReasoningEffort  string `json:"reasoning_effort"`
	IdlePolicy       string `json:"idle_policy"`
	IdleAfterSeconds uint32 `json:"idle_after_seconds"`
	IdleInstruction  string `json:"idle_instruction"`
	IdleRunBudget    uint32 `json:"idle_run_budget"`
	IdleRunsUsed     uint32 `json:"idle_runs_used"`
	Revision         uint64 `json:"revision"`
}

type TaskSummary struct {
	ID              string `json:"id"`
	ProjectID       string `json:"project_id"`
	AssignedAgentID string `json:"assigned_agent_id"`
	IncarnationID   string `json:"incarnation_id"`
	WorkRevision    uint64 `json:"work_revision"`
	Title           string `json:"title"`
	Status          string `json:"status"`
	Priority        int64  `json:"priority"`
	Revision        uint64 `json:"revision"`
}

type TaskRecoveryInput struct {
	TaskID        string `json:"task_id"`
	IncarnationID string `json:"incarnation_id"`
}

type TaskRecovery struct {
	Result                string   `json:"result"`
	ResultTruncated       bool     `json:"result_truncated"`
	BlockedReason         string   `json:"blocked_reason"`
	RunWorkRevision       uint64   `json:"run_work_revision,omitempty"`
	RunOutcome            string   `json:"run_outcome,omitempty"`
	RunDetail             string   `json:"run_detail,omitempty"`
	State                 string   `json:"state"`
	TaskID                string   `json:"task_id"`
	IncarnationID         string   `json:"incarnation_id"`
	ProjectID             string   `json:"project_id"`
	AssignedAgentID       string   `json:"assigned_agent_id"`
	WorkRevision          uint64   `json:"work_revision"`
	Revision              uint64   `json:"revision"`
	Status                string   `json:"status"`
	NeedsOperatorRecovery bool     `json:"needs_operator_recovery"`
	ChangeID              string   `json:"change_id,omitempty"`
	ChangeRevision        uint64   `json:"change_revision,omitempty"`
	ChangePhase           string   `json:"change_phase,omitempty"`
	SourceFormat          string   `json:"source_format,omitempty"`
	SourceBaseCommit      string   `json:"source_base_commit,omitempty"`
	SourceRepositoryDev   int64    `json:"source_repository_dev,omitempty"`
	SourceRepositoryInode int64    `json:"source_repository_inode,omitempty"`
	RunID                 string   `json:"run_id,omitempty"`
	RunRevision           uint64   `json:"run_revision,omitempty"`
	ArtifactPaths         []string `json:"artifact_paths"`
	// Disposition is the decision durable state records after the latest
	// run; OverseerNotification (none, pending, scheduled) says whether the
	// standing overseer's wake cursor has consumed this task's newest event,
	// which is neither seen nor handled; OverseerTask* name what that
	// overseer is running or next queued on; LastProgressAtMs is the newest
	// transition among the task, its runs, their human requests, its peer
	// questions and interventions against it.
	Disposition          string `json:"disposition,omitempty"`
	HumanRequestID       string `json:"human_request_id,omitempty"`
	OverseerAgentID      string `json:"overseer_agent_id,omitempty"`
	OverseerNotification string `json:"overseer_notification,omitempty"`
	OverseerTaskID       string `json:"overseer_task_id,omitempty"`
	OverseerTaskStatus   string `json:"overseer_task_status,omitempty"`
	OverseerTaskTitle    string `json:"overseer_task_title,omitempty"`
	LastProgressAtMs     int64  `json:"last_progress_at_ms,omitempty"`
	// Evidence for deciding whether the returned run refused before acting:
	// its provider exit ("code N", "signal N", "absent"), how long it ran,
	// and the retained Change head it left.
	RunProviderExit  string `json:"run_provider_exit,omitempty"`
	RunRunningMs     int64  `json:"run_running_ms,omitempty"`
	ChangeHeadCommit string `json:"change_head_commit,omitempty"`
}

func validTaskRecovery(value TaskRecovery) bool {
	if !utf8.ValidString(value.Result) || len(value.Result) > MaxRecoveryResultBytes || !utf8.ValidString(value.BlockedReason) || len(value.BlockedReason) > 4096 || !utf8.ValidString(value.RunDetail) || len(value.RunDetail) > 4096 {
		return false
	}
	if (value.Result != "" || value.ResultTruncated) && value.Status != "succeeded" || value.BlockedReason != "" && value.Status != "blocked" {
		return false
	}
	if value.ResultTruncated && len(value.Result) < MaxRecoveryResultBytes-3 {
		return false
	}
	if value.RunID == "" && (value.RunWorkRevision != 0 || value.RunOutcome != "" || value.RunDetail != "") || value.RunID != "" && (value.RunWorkRevision == 0 || value.RunWorkRevision > value.WorkRevision) {
		return false
	}
	switch value.RunOutcome {
	case "":
		if value.RunDetail != "" {
			return false
		}
	case "succeeded":
		if value.RunDetail != "" {
			return false
		}
	case "blocked", "cancelled":
		if value.RunDetail == "" {
			return false
		}
	case "failed":
	default:
		return false
	}

	if value.State == "missing" {
		return value.TaskID == "" && value.IncarnationID == "" && value.ProjectID == "" && value.AssignedAgentID == "" && value.WorkRevision == 0 && value.Revision == 0 && value.Status == "" && !value.NeedsOperatorRecovery && value.ChangeID == "" && value.ChangeRevision == 0 && value.ChangePhase == "" && value.SourceFormat == "" && value.SourceBaseCommit == "" && value.SourceRepositoryDev == 0 && value.SourceRepositoryInode == 0 && value.RunID == "" && value.RunRevision == 0 && value.ArtifactPaths != nil && len(value.ArtifactPaths) == 0 &&
			value.Disposition == "" && value.HumanRequestID == "" && value.OverseerAgentID == "" && value.OverseerNotification == "" && value.OverseerTaskID == "" && value.OverseerTaskStatus == "" && value.OverseerTaskTitle == "" && value.LastProgressAtMs == 0 &&
			value.RunProviderExit == "" && value.RunRunningMs == 0 && value.ChangeHeadCommit == ""
	}
	if value.State != "found" || !validID(value.TaskID) || !validID(value.IncarnationID) || !validID(value.ProjectID) || !validOptionalID(value.AssignedAgentID) || value.WorkRevision == 0 || value.Revision == 0 || !validTaskStatus(value.Status) || value.ArtifactPaths == nil || len(value.ArtifactPaths) > kernel.MaxRecoveryResources {
		return false
	}
	// Empty additive fields are an older daemon's answer, tolerated.
	switch value.Disposition {
	case "", "queued", "retry_queued", "running", "succeeded", "cancelled", "needs_operator_recovery", "needs_you", "none":
	default:
		return false
	}
	switch value.OverseerNotification {
	case "", "none":
		if value.OverseerAgentID != "" {
			return false
		}
	case "pending", "scheduled":
		if !validID(value.OverseerAgentID) {
			return false
		}
	default:
		return false
	}
	if !validOptionalID(value.HumanRequestID) || value.Disposition == "needs_you" != (value.HumanRequestID != "") {
		return false
	}
	if value.OverseerTaskID == "" {
		if value.OverseerTaskStatus != "" || value.OverseerTaskTitle != "" {
			return false
		}
	} else if value.OverseerAgentID == "" || !validID(value.OverseerTaskID) || value.OverseerTaskStatus != "running" && value.OverseerTaskStatus != "queued" || !validText(value.OverseerTaskTitle, 1, 1024) {
		return false
	}
	if value.LastProgressAtMs < 0 || value.RunRunningMs < 0 || value.RunID == "" && (value.RunProviderExit != "" || value.RunRunningMs != 0) {
		return false
	}
	switch {
	case value.RunProviderExit == "", value.RunProviderExit == "absent":
	case strings.HasPrefix(value.RunProviderExit, "code "), strings.HasPrefix(value.RunProviderExit, "signal "):
		if _, err := strconv.ParseInt(value.RunProviderExit[strings.IndexByte(value.RunProviderExit, ' ')+1:], 10, 64); err != nil {
			return false
		}
	default:
		return false
	}
	if value.ChangeHeadCommit != "" && (value.ChangeID == "" || !(value.SourceFormat == "sha1" && len(value.ChangeHeadCommit) == 40 || value.SourceFormat == "sha256" && len(value.ChangeHeadCommit) == 64)) {
		return false
	}
	if value.RunID == "" && value.RunRevision != 0 || value.RunID != "" && (!validID(value.RunID) || value.RunRevision == 0) {
		return false
	}
	if value.ChangeID == "" {
		if value.ChangeRevision != 0 || value.ChangePhase != "" || value.SourceFormat != "" {
			return false
		}
	} else {
		if !validID(value.ChangeID) || value.ChangeRevision == 0 {
			return false
		}
		switch value.ChangePhase {
		case "reserved", "prepared", "available", "retained", "abandoned":
		default:
			return false
		}
	}
	if value.SourceFormat == "" {
		if value.SourceBaseCommit != "" || value.SourceRepositoryDev != 0 || value.SourceRepositoryInode != 0 {
			return false
		}
	} else {
		if !(value.SourceFormat == "sha1" && len(value.SourceBaseCommit) == 40 || value.SourceFormat == "sha256" && len(value.SourceBaseCommit) == 64) || value.SourceRepositoryDev < 0 || value.SourceRepositoryInode <= 0 {
			return false
		}
		for _, ch := range value.SourceBaseCommit {
			if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
				return false
			}
		}
	}
	for _, path := range value.ArtifactPaths {
		if value.RunID == "" || !validCanonicalPath(path, 4096) {
			return false
		}
	}
	return true
}

// DashboardSnapshot deliberately contains only the bounded public Store
// projection. Roots, task bodies/results, models, credentials and source data
// have no representable field here.
type DashboardSnapshot struct {
	Head     uint64           `json:"head"`
	Factory  FactorySummary   `json:"factory"`
	Projects []ProjectSummary `json:"projects"`
	Agents   []AgentSummary   `json:"agents"`
	Tasks    []TaskSummary    `json:"tasks"`
}

// OverseerSnapshot is the private, project-scoped view granted to a running
// orchestrator. Its project identity is derived from the attempt credential;
// callers cannot select a different project.
type OverseerSnapshot struct {
	ProjectID      string                  `json:"project_id"`
	Head           uint64                  `json:"head"`
	NextOffset     *uint64                 `json:"next_offset"`
	NextTextOffset *uint64                 `json:"next_text_offset"`
	Agents         []AgentSummary          `json:"agents"`
	Tasks          []OverseerTask          `json:"tasks"`
	Runs           []OverseerRun           `json:"runs"`
	Questions      []OverseerQuestion      `json:"questions"`
	PeerQuestions  []PeerQuestion          `json:"peer_questions"`
	History        []OverseerIntervention  `json:"history"`
	Handoffs       []RetainedChangeHandoff `json:"retained_change_handoffs"`
}

// RetainedChangeHandoff identifies one settled worker Change by its Git
// identities: the branch its worktree is on, the base it started from and
// the head the daemon settled it at. Consumers must reject it when any
// revision no longer matches their status snapshot. Overseer status carries
// only the identities; attempt source adds where to read them.
type RetainedChangeHandoff struct {
	ChangeID   string `json:"change_id"`
	BaseCommit string `json:"base_commit"`
	// HeadCommit is the settled tip of Branch, the exact head to review and
	// publish. It is empty for a Change that is still a Git-free tree.
	HeadCommit       string `json:"head_commit"`
	Branch           string `json:"branch"`
	TaskID           string `json:"task_id"`
	TaskWorkRevision uint64 `json:"task_work_revision"`
	ChangeRevision   uint64 `json:"change_revision"`
	// SourcePath is the Change's worktree and GitDirectory the project
	// repository's Git directory its commits live in; both are set only by
	// an explicit attempt source request. Dirty reports uncommitted work in
	// the worktree at that moment.
	SourcePath   string `json:"source_path"`
	GitDirectory string `json:"git_directory"`
	Dirty        bool   `json:"dirty"`
}

func validRetainedChangeHandoff(handoff RetainedChangeHandoff) bool {
	return validID(handoff.ChangeID) && validID(handoff.TaskID) && validCommitHex(handoff.BaseCommit) && (handoff.HeadCommit == "" || len(handoff.HeadCommit) == len(handoff.BaseCommit) && validCommitHex(handoff.HeadCommit)) &&
		(handoff.Branch == "" || handoff.Branch == "factory/"+handoff.ChangeID[:12]) && handoff.TaskWorkRevision != 0 && handoff.ChangeRevision != 0 &&
		(handoff.SourcePath == "" || validHandoffSourcePath(handoff.SourcePath, handoff.ChangeID)) && (handoff.GitDirectory == "" || validHandoffGitDirectory(handoff.GitDirectory))
}

// validSourceHandoff is the attempt source reply: every identity and every
// location present.
func validSourceHandoff(handoff RetainedChangeHandoff) bool {
	return validRetainedChangeHandoff(handoff) && handoff.HeadCommit != "" && handoff.Branch != "" && handoff.SourcePath != "" && handoff.GitDirectory != ""
}

func validCommitHex(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

type OverseerIntervention struct {
	OperationID      string `json:"operation_id"`
	TaskID           string `json:"task_id"`
	RunID            string `json:"run_id"`
	SuccessorTaskID  string `json:"successor_task_id"`
	Kind             string `json:"kind"`
	Actor            string `json:"actor"`
	Payload          string `json:"payload"`
	PayloadTruncated bool   `json:"payload_truncated"`
	State            string `json:"state"`
	Detail           string `json:"detail"`
	CreatedAtMs      uint64 `json:"created_at_ms"`
}

// OverseerSnapshotInput pages every collection together. A continuation must
// fence the head returned by the preceding page. Task text is chunked by rune.
type OverseerSnapshotInput struct {
	TaskID       string `json:"task_id,omitempty"`
	Offset       uint64 `json:"offset,omitempty"`
	ExpectedHead uint64 `json:"expected_head,omitempty"`
	TextOffset   uint64 `json:"text_offset,omitempty"`
}

// OverseerTask carries private task progress for the authenticated project's
// live orchestrator. The public dashboard has no fields for these texts.
type OverseerTask struct {
	ID                 string `json:"id"`
	ProjectID          string `json:"project_id"`
	AssignedAgentID    string `json:"assigned_agent_id"`
	Title              string `json:"title"`
	Objective          string `json:"objective"`
	ObjectiveTruncated bool   `json:"objective_truncated"`
	Status             string `json:"status"`
	Priority           int64  `json:"priority"`
	BlockedReason      string `json:"blocked_reason"`
	Result             string `json:"result"`
	ResultTruncated    bool   `json:"result_truncated"`
	Revision           uint64 `json:"revision"`
}

// MarshalJSON applies the same terminal-safe escaping as attempt task text:
// overseer question text is printed by factoryctl inside a provider terminal.
func (snapshot OverseerSnapshot) MarshalJSON() ([]byte, error) {
	type plain OverseerSnapshot
	encoded, err := json.Marshal(plain(snapshot))
	if err != nil {
		return nil, err
	}
	return terminalSafeJSON(nil, encoded), nil
}

type OverseerRun struct {
	ID       string `json:"id"`
	AgentID  string `json:"agent_id"`
	TaskID   string `json:"task_id"`
	Phase    string `json:"phase"`
	Revision uint64 `json:"revision"`
}

// OverseerQuestion carries the exact question only to the project's running
// orchestrator. It is not a browser or operator dashboard projection.
type OverseerQuestion struct {
	ID       string `json:"id"`
	AgentID  string `json:"agent_id"`
	TaskID   string `json:"task_id"`
	Status   string `json:"status"`
	Revision uint64 `json:"revision"`
	Question string `json:"question"`
}

// OverseerTaskCreateInput intentionally has no project selector: the daemon
// derives it from the live orchestrator attempt.
type OverseerTaskCreateInput struct {
	ID              string                  `json:"id"`
	AssignedAgentID string                  `json:"assigned_agent_id"`
	IncarnationID   string                  `json:"incarnation_id"`
	Title           string                  `json:"title"`
	Body            string                  `json:"body"`
	Priority        int64                   `json:"priority"`
	Prerequisites   []TaskPrerequisiteInput `json:"prerequisites,omitempty"`
	ConflictPaths   []string                `json:"conflict_paths,omitempty"`
}

type TaskPrerequisiteInput struct {
	TaskID       string `json:"task_id"`
	WorkRevision uint64 `json:"work_revision"`
}

type OverseerTaskUpdateInput struct {
	TaskID           string  `json:"task_id"`
	ExpectedRevision uint64  `json:"expected_revision"`
	Title            *string `json:"title,omitempty"`
	Body             *string `json:"body,omitempty"`
	Priority         *int64  `json:"priority,omitempty"`
	AssignedAgentID  *string `json:"assigned_agent_id,omitempty"`
	Cancel           bool    `json:"cancel,omitempty"`
	Retry            bool    `json:"retry,omitempty"`
}

type OverseerAgentUpdateInput struct {
	AgentID          string `json:"agent_id"`
	ExpectedRevision uint64 `json:"expected_revision"`
	Paused           *bool  `json:"paused,omitempty"`
	Archived         *bool  `json:"archived,omitempty"`
}

// AgentIdlePolicyInput is the operator-only standing supervision edit. It
// deliberately contains no provider, account, lifecycle, or appearance fields.
type AgentIdlePolicyInput struct {
	AgentID          string `json:"agent_id"`
	ExpectedRevision uint64 `json:"expected_revision"`
	Policy           string `json:"policy"`
	AfterSeconds     uint32 `json:"after_seconds"`
	Instruction      string `json:"instruction"`
	RunBudget        uint64 `json:"run_budget"`
}

func validAgentIdlePolicyInput(input AgentIdlePolicyInput) bool {
	if !validID(input.AgentID) || input.ExpectedRevision == 0 || input.AfterSeconds > kernel.MaxIdleAfterSeconds || input.RunBudget > uint64(kernel.MaxIdleRunBudget) || !validText(input.Instruction, 0, 32768) {
		return false
	}
	return input.Policy == "wait" && input.AfterSeconds == 0 && input.Instruction == "" && input.RunBudget == 0 || input.Policy == "standing_instruction" && input.AfterSeconds > 0 && input.Instruction != ""
}

type OverseerRunStopInput struct {
	OperationID          string `json:"operation_id"`
	TaskID               string `json:"task_id"`
	ExpectedTaskRevision uint64 `json:"expected_task_revision"`
	RunID                string `json:"run_id"`
	ExpectedRunRevision  uint64 `json:"expected_run_revision"`
}

type OverseerRunReplaceInput struct {
	OverseerRunStopInput
	SuccessorTaskID        string `json:"successor_task_id"`
	SuccessorIncarnationID string `json:"successor_incarnation_id"`
	Instruction            string `json:"instruction"`
}

type OverseerWorkerMessageInput struct {
	OperationID          string `json:"operation_id"`
	TaskID               string `json:"task_id"`
	ExpectedTaskRevision uint64 `json:"expected_task_revision"`
	RunID                string `json:"run_id"`
	ExpectedRunRevision  uint64 `json:"expected_run_revision"`
	Message              string `json:"message"`
}

type OverseerWorkerInterruptInput struct {
	OperationID          string `json:"operation_id"`
	TaskID               string `json:"task_id"`
	ExpectedTaskRevision uint64 `json:"expected_task_revision"`
	RunID                string `json:"run_id"`
	ExpectedRunRevision  uint64 `json:"expected_run_revision"`
}

type OverseerHumanReplyInput struct {
	OperationID      string `json:"operation_id"`
	RequestID        string `json:"request_id"`
	ExpectedRevision uint64 `json:"expected_revision"`
	Reply            string `json:"reply"`
}

type CreateProjectInput struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Root string `json:"root"`
}

type ProjectLimitsInput struct {
	ProjectID        string `json:"project_id"`
	ExpectedRevision uint64 `json:"expected_revision"`
	RunBudget        uint64 `json:"run_budget"`
	MaxRunSeconds    uint32 `json:"max_run_seconds"`
}

type CreateAgentInput struct {
	ID              string `json:"id"`
	ProjectID       string `json:"project_id"`
	Name            string `json:"name"`
	Role            string `json:"role"`
	Provider        string `json:"provider"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// AccountID selects one linked provider login. Empty means the provider's
	// default configuration directory.
	AccountID       string `json:"account_id,omitempty"`
	ToolBudgetLimit uint64 `json:"tool_budget_limit"`
}

func validCreateAgentInput(input CreateAgentInput) bool {
	provider, err := kernel.ParseProvider(input.Provider)
	if err != nil {
		return false
	}
	return validID(input.ID) && validID(input.ProjectID) && validText(input.Name, 1, 128) &&
		(input.Role == "worker" || input.Role == "orchestrator") &&
		(input.AccountID == "" || validID(input.AccountID) && provider != kernel.ProviderShell) &&
		kernel.ValidateProviderLaunchControls(provider, input.Model, input.ReasoningEffort) == nil &&
		input.ToolBudgetLimit >= 1 && input.ToolBudgetLimit <= 1_000_000_000
}

type EnqueueTaskInput struct {
	ID              string                  `json:"id"`
	ProjectID       string                  `json:"project_id"`
	AssignedAgentID string                  `json:"assigned_agent_id"`
	IncarnationID   string                  `json:"incarnation_id"`
	Title           string                  `json:"title"`
	Body            string                  `json:"body"`
	Priority        int64                   `json:"priority"`
	Prerequisites   []TaskPrerequisiteInput `json:"prerequisites,omitempty"`
	ConflictPaths   []string                `json:"conflict_paths,omitempty"`
}

// HumanQuestionInput is the bounded provider-authored portion of a
// HumanRequest. The daemon derives the run and all public projection fields
// from the authenticated attempt; callers cannot supply those identities.
type HumanQuestionInput struct {
	IdempotencyKey string   `json:"idempotency_key"`
	Question       string   `json:"question"`
	Options        []string `json:"options,omitempty"`
	ReuseExisting  bool     `json:"reuse_existing,omitempty"`
}

type PeerQuestionInput struct {
	TargetTaskID   string `json:"target_task_id"`
	IdempotencyKey string `json:"idempotency_key"`
	Question       string `json:"question"`
}

type PeerAnswerInput struct {
	QuestionID       string `json:"question_id"`
	ExpectedRevision uint64 `json:"expected_revision"`
	IdempotencyKey   string `json:"idempotency_key"`
	Answer           string `json:"answer"`
}

type PeerStatus struct {
	Head             uint64         `json:"head"`
	Targets          []PeerTarget   `json:"targets"`
	Questions        []PeerQuestion `json:"questions"`
	NextTargetOffset *uint64        `json:"next_target_offset"`
	NextOffset       *uint64        `json:"next_offset"`
}

type PeerStatusInput struct {
	Offset         uint64 `json:"offset"`
	TargetOffset   uint64 `json:"target_offset"`
	ExpectedHead   uint64 `json:"expected_head"`
	IncludeTargets bool   `json:"include_targets"`
}

// Peer status is printed in an authenticated provider terminal.
func (status PeerStatus) MarshalJSON() ([]byte, error) {
	type plain PeerStatus
	encoded, err := json.Marshal(plain(status))
	if err != nil {
		return nil, err
	}
	return terminalSafeJSON(nil, encoded), nil
}

type PeerQuestion struct {
	ID                     string `json:"id"`
	SourceTaskID           string `json:"source_task_id"`
	TargetTaskID           string `json:"target_task_id"`
	Question               string `json:"question"`
	Answer                 string `json:"answer"`
	RecipientDeliveryState string `json:"recipient_delivery_state"`
	AnswerDeliveryState    string `json:"answer_delivery_state"`
	RecipientAvailability  string `json:"recipient_availability"`
	AnswerAvailability     string `json:"answer_availability"`
	Revision               uint64 `json:"revision"`
}

type PeerTarget struct {
	TaskID   string `json:"task_id"`
	AgentID  string `json:"agent_id"`
	Name     string `json:"name"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Revision uint64 `json:"revision"`
}

func validPeerStatus(status PeerStatus) bool {
	if status.Head == 0 || status.Targets == nil || status.Questions == nil || len(status.Targets) > 4 || len(status.Questions) > 1 || status.NextOffset != nil && *status.NextOffset == 0 || status.NextTargetOffset != nil && *status.NextTargetOffset == 0 {
		return false
	}
	for _, target := range status.Targets {
		if !validID(target.TaskID) || !validID(target.AgentID) || !validText(target.Name, 1, 128) || !validText(target.Title, 1, 1024) || !validTaskStatus(target.Status) || target.Revision == 0 {
			return false
		}
	}
	for _, question := range status.Questions {
		if !validPeerQuestion(question) {
			return false
		}
	}
	return true
}

func validPeerQuestion(question PeerQuestion) bool {
	return !(!validID(question.ID) || !validID(question.SourceTaskID) || !validID(question.TargetTaskID) || !validText(question.Question, 1, 2048) || !validText(question.Answer, 0, 2048) || question.Revision == 0 || !validPeerState(question.RecipientDeliveryState) || !validPeerState(question.AnswerDeliveryState) || question.RecipientAvailability != "" && !validPeerAvailability(question.RecipientAvailability) || question.AnswerAvailability != "" && !validPeerAvailability(question.AnswerAvailability))
}

func validPeerState(value string) bool {
	return value == "pending" || value == "delivered" || value == "unknown"
}
func validPeerAvailability(value string) bool {
	return value == "pending" || value == "available" || value == "stale"
}

// SendBackInput returns a finished task to its worker's queue with a note.
// An orchestrator's attempt names a task of its own project; the operator
// names any task.
type SendBackInput struct {
	TaskID string `json:"task_id"`
	Note   string `json:"note"`
}

type WebClientRevocationInput struct {
	ID               string `json:"id"`
	ExpectedRevision uint64 `json:"expected_revision"`
}

type AgentModelSelectInput struct {
	AgentID          string `json:"agent_id"`
	ExpectedRevision uint64 `json:"expected_revision"`
	Model            string `json:"model"`
	ReasoningEffort  string `json:"reasoning_effort"`
}

func validAgentModelSelectInput(value AgentModelSelectInput) bool {
	return validID(value.AgentID) && value.ExpectedRevision != 0 && validText(value.Model, 1, 128) && validText(value.ReasoningEffort, 0, 128)
}
