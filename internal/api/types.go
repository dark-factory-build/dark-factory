package api

import "errors"

const (
	maxFrameBytes      = 1 << 20
	maxSnapshotEntries = 4096
	credentialBytes    = 32
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
	RemoteInvalidRequest      RemoteErrorCode = "invalid_request"
	RemoteUnsupportedProtocol RemoteErrorCode = "unsupported_protocol"
	RemoteUnauthorized        RemoteErrorCode = "unauthorized"
	RemoteForbidden           RemoteErrorCode = "forbidden"
	RemoteNotFound            RemoteErrorCode = "not_found"
	RemoteConflict            RemoteErrorCode = "conflict"
	RemoteRevisionConflict    RemoteErrorCode = "revision_conflict"
	RemoteTooLarge            RemoteErrorCode = "too_large"
	RemoteUnavailable         RemoteErrorCode = "unavailable"
	RemoteInternal            RemoteErrorCode = "internal"
)

type RemoteError struct {
	code RemoteErrorCode
}

func (err *RemoteError) Error() string {
	switch err.code {
	case RemoteInvalidRequest:
		return "local API rejected the request"
	case RemoteUnsupportedProtocol:
		return "local API protocol generation is unsupported"
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

type MutationResult struct {
	Head     uint64 `json:"head"`
	Revision uint64 `json:"revision"`
}

type FactorySummary struct {
	DispatchEnabled bool   `json:"dispatch_enabled"`
	Capacity        uint16 `json:"capacity"`
	ActiveRuns      uint16 `json:"active_runs"`
	Revision        uint64 `json:"revision"`
}

type ProjectSummary struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Revision uint64 `json:"revision"`
}

type AgentSummary struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	Paused    bool   `json:"paused"`
	Revision  uint64 `json:"revision"`
}

type TaskSummary struct {
	ID              string `json:"id"`
	ProjectID       string `json:"project_id"`
	AssignedAgentID string `json:"assigned_agent_id"`
	Title           string `json:"title"`
	Status          string `json:"status"`
	Priority        int64  `json:"priority"`
	Revision        uint64 `json:"revision"`
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

type CreateProjectInput struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Root string `json:"root"`
}

type CreateShellAgentInput struct {
	ID              string `json:"id"`
	ProjectID       string `json:"project_id"`
	Name            string `json:"name"`
	Role            string `json:"role"`
	ToolBudgetLimit uint64 `json:"tool_budget_limit"`
}

type EnqueueTaskInput struct {
	ID              string `json:"id"`
	ProjectID       string `json:"project_id"`
	AssignedAgentID string `json:"assigned_agent_id"`
	IncarnationID   string `json:"incarnation_id"`
	Title           string `json:"title"`
	Body            string `json:"body"`
	Priority        int64  `json:"priority"`
}
