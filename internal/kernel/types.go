package kernel

import (
	"encoding/hex"
	"fmt"
	"strconv"
)

const (
	IDBytes                = 16
	DigestBytes            = 32
	EventRetentionLimit    = 4096
	WatchBatchLimit        = 256
	SnapshotEntityLimit    = 4096
	MaxFactoryCapacity     = 1024
	MaxChangeTreeEntries   = 10_000
	MaxChangeTreeBlobBytes = 1 << 30
)

type identifier struct {
	b [IDBytes]byte
}

func identifierFromBytes(value []byte) (identifier, error) {
	if len(value) != IDBytes {
		return identifier{}, fmt.Errorf("%w: identifier is %d bytes, want %d", ErrInvalidValue, len(value), IDBytes)
	}
	var id identifier
	copy(id.b[:], value)
	if id.zero() {
		return identifier{}, fmt.Errorf("%w: identifier is all zero", ErrInvalidValue)
	}
	return id, nil
}

func (id identifier) zero() bool {
	return id.b == [IDBytes]byte{}
}

func (id identifier) bytes() []byte {
	value := make([]byte, IDBytes)
	copy(value, id.b[:])
	return value
}

func (id identifier) String() string { return hex.EncodeToString(id.b[:]) }

type ProjectID struct{ identifier }
type AgentID struct{ identifier }
type TaskID struct{ identifier }
type IncarnationID struct{ identifier }
type ChangeID struct{ identifier }
type RunID struct{ identifier }
type ResourceID struct{ identifier }

func ProjectIDFromBytes(value []byte) (ProjectID, error) {
	id, err := identifierFromBytes(value)
	return ProjectID{id}, err
}

func AgentIDFromBytes(value []byte) (AgentID, error) {
	id, err := identifierFromBytes(value)
	return AgentID{id}, err
}

func TaskIDFromBytes(value []byte) (TaskID, error) {
	id, err := identifierFromBytes(value)
	return TaskID{id}, err
}

func IncarnationIDFromBytes(value []byte) (IncarnationID, error) {
	id, err := identifierFromBytes(value)
	return IncarnationID{id}, err
}

func ChangeIDFromBytes(value []byte) (ChangeID, error) {
	id, err := identifierFromBytes(value)
	return ChangeID{id}, err
}

func RunIDFromBytes(value []byte) (RunID, error) {
	id, err := identifierFromBytes(value)
	return RunID{id}, err
}

func ResourceIDFromBytes(value []byte) (ResourceID, error) {
	id, err := identifierFromBytes(value)
	return ResourceID{id}, err
}

func (id ProjectID) Bytes() []byte     { return id.bytes() }
func (id AgentID) Bytes() []byte       { return id.bytes() }
func (id TaskID) Bytes() []byte        { return id.bytes() }
func (id IncarnationID) Bytes() []byte { return id.bytes() }
func (id ChangeID) Bytes() []byte      { return id.bytes() }
func (id RunID) Bytes() []byte         { return id.bytes() }
func (id ResourceID) Bytes() []byte    { return id.bytes() }

func (id ProjectID) MarshalText() ([]byte, error)     { return []byte(id.String()), nil }
func (id AgentID) MarshalText() ([]byte, error)       { return []byte(id.String()), nil }
func (id TaskID) MarshalText() ([]byte, error)        { return []byte(id.String()), nil }
func (id IncarnationID) MarshalText() ([]byte, error) { return []byte(id.String()), nil }
func (id ChangeID) MarshalText() ([]byte, error)      { return []byte(id.String()), nil }
func (id RunID) MarshalText() ([]byte, error)         { return []byte(id.String()), nil }
func (id ResourceID) MarshalText() ([]byte, error)    { return []byte(id.String()), nil }

type digest struct {
	b [DigestBytes]byte
}

func digestFromBytes(value []byte) (digest, error) {
	if len(value) != DigestBytes {
		return digest{}, fmt.Errorf("%w: digest is %d bytes, want %d", ErrInvalidValue, len(value), DigestBytes)
	}
	var result digest
	copy(result.b[:], value)
	return result, nil
}

func (d digest) Bytes() []byte {
	value := make([]byte, DigestBytes)
	copy(value, d.b[:])
	return value
}

type AttemptDigest struct{ digest }
type TreeDigest struct{ digest }
type BirthDigest struct{ digest }

func AttemptDigestFromBytes(value []byte) (AttemptDigest, error) {
	d, err := digestFromBytes(value)
	return AttemptDigest{d}, err
}

func TreeDigestFromBytes(value []byte) (TreeDigest, error) {
	d, err := digestFromBytes(value)
	return TreeDigest{d}, err
}

func BirthDigestFromBytes(value []byte) (BirthDigest, error) {
	d, err := digestFromBytes(value)
	return BirthDigest{d}, err
}

func (d AttemptDigest) Bytes() []byte { return d.digest.Bytes() }
func (d TreeDigest) Bytes() []byte    { return d.digest.Bytes() }
func (d BirthDigest) Bytes() []byte   { return d.digest.Bytes() }

type UnixMillis struct{ value int64 }

func NewUnixMillis(value int64) (UnixMillis, error) {
	if value < 0 {
		return UnixMillis{}, fmt.Errorf("%w: negative timestamp", ErrInvalidValue)
	}
	return UnixMillis{value: value}, nil
}

func (value UnixMillis) Int64() int64 { return value.value }
func (value UnixMillis) MarshalJSON() ([]byte, error) {
	return strconv.AppendInt(nil, value.value, 10), nil
}

type Revision struct{ value int64 }

func NewRevision(value int64) (Revision, error) {
	if value < 1 {
		return Revision{}, fmt.Errorf("%w: revision must be positive", ErrInvalidValue)
	}
	return Revision{value: value}, nil
}

func (value Revision) Int64() int64 { return value.value }
func (value Revision) MarshalJSON() ([]byte, error) {
	return strconv.AppendInt(nil, value.value, 10), nil
}

type EventSequence struct{ value int64 }

func NewEventSequence(value int64) (EventSequence, error) {
	if value < 0 {
		return EventSequence{}, fmt.Errorf("%w: event sequence must not be negative", ErrInvalidValue)
	}
	return EventSequence{value: value}, nil
}

func (value EventSequence) Int64() int64 { return value.value }
func (value EventSequence) MarshalJSON() ([]byte, error) {
	return strconv.AppendInt(nil, value.value, 10), nil
}

type AgentRole uint8

const (
	RoleOrchestrator AgentRole = iota + 1
	RoleWorker
)

func parseAgentRole(value string) (AgentRole, error) {
	switch value {
	case "orchestrator":
		return RoleOrchestrator, nil
	case "worker":
		return RoleWorker, nil
	default:
		return 0, corruptControl("agent role", value)
	}
}

func (value AgentRole) String() string {
	switch value {
	case RoleOrchestrator:
		return "orchestrator"
	case RoleWorker:
		return "worker"
	default:
		return ""
	}
}
func (value AgentRole) valid() bool {
	return value == RoleOrchestrator || value == RoleWorker
}

type Provider uint8

const (
	ProviderClaudeCode Provider = iota + 1
	ProviderCodex
	ProviderShell
)

func parseProvider(value string) (Provider, error) {
	switch value {
	case "claude_code":
		return ProviderClaudeCode, nil
	case "codex":
		return ProviderCodex, nil
	case "shell":
		return ProviderShell, nil
	default:
		return 0, corruptControl("provider", value)
	}
}

func (value Provider) String() string {
	switch value {
	case ProviderClaudeCode:
		return "claude_code"
	case ProviderCodex:
		return "codex"
	case ProviderShell:
		return "shell"
	default:
		return ""
	}
}
func (value Provider) valid() bool {
	return value == ProviderClaudeCode || value == ProviderCodex || value == ProviderShell
}

type ExecutionMode uint8

const (
	ExecutionPlanOnly ExecutionMode = iota + 1
	ExecutionWorkspaceWrite
	ExecutionUnrestricted
)

func parseExecutionMode(value string) (ExecutionMode, error) {
	switch value {
	case "plan_only":
		return ExecutionPlanOnly, nil
	case "workspace_write":
		return ExecutionWorkspaceWrite, nil
	case "unrestricted":
		return ExecutionUnrestricted, nil
	default:
		return 0, corruptControl("execution mode", value)
	}
}

func (value ExecutionMode) String() string {
	switch value {
	case ExecutionPlanOnly:
		return "plan_only"
	case ExecutionWorkspaceWrite:
		return "workspace_write"
	case ExecutionUnrestricted:
		return "unrestricted"
	default:
		return ""
	}
}
func (value ExecutionMode) valid() bool {
	return value == ExecutionPlanOnly || value == ExecutionWorkspaceWrite || value == ExecutionUnrestricted
}

type TaskStatus uint8

const (
	TaskQueued TaskStatus = iota + 1
	TaskRunning
	TaskBlocked
	TaskSucceeded
	TaskFailed
	TaskCancelled
)

func parseTaskStatus(value string) (TaskStatus, error) {
	switch value {
	case "queued":
		return TaskQueued, nil
	case "running":
		return TaskRunning, nil
	case "blocked":
		return TaskBlocked, nil
	case "succeeded":
		return TaskSucceeded, nil
	case "failed":
		return TaskFailed, nil
	case "cancelled":
		return TaskCancelled, nil
	default:
		return 0, corruptControl("task status", value)
	}
}

func (value TaskStatus) String() string {
	switch value {
	case TaskQueued:
		return "queued"
	case TaskRunning:
		return "running"
	case TaskBlocked:
		return "blocked"
	case TaskSucceeded:
		return "succeeded"
	case TaskFailed:
		return "failed"
	case TaskCancelled:
		return "cancelled"
	default:
		return ""
	}
}

type EntityKind uint8

const (
	EntityFactory EntityKind = iota + 1
	EntityProject
	EntityAgent
	EntityTask
	EntityChange
	EntityRun
)

func parseEntityKind(value string) (EntityKind, error) {
	switch value {
	case "factory":
		return EntityFactory, nil
	case "project":
		return EntityProject, nil
	case "agent":
		return EntityAgent, nil
	case "task":
		return EntityTask, nil
	case "change":
		return EntityChange, nil
	case "run":
		return EntityRun, nil
	default:
		return 0, corruptControl("entity kind", value)
	}
}

func (value EntityKind) String() string {
	switch value {
	case EntityFactory:
		return "factory"
	case EntityProject:
		return "project"
	case EntityAgent:
		return "agent"
	case EntityTask:
		return "task"
	case EntityChange:
		return "change"
	case EntityRun:
		return "run"
	default:
		return ""
	}
}

type FactoryConfig struct {
	DispatchEnabled bool
	Capacity        uint16
}

func (config FactoryConfig) validate() error {
	if config.Capacity < 1 || config.Capacity > MaxFactoryCapacity {
		return fmt.Errorf("%w: capacity %d outside 1..%d", ErrInvalidValue, config.Capacity, MaxFactoryCapacity)
	}
	return nil
}

func (config FactoryConfig) normalized() (FactoryConfig, error) {
	if config.Capacity == 0 {
		config.Capacity = 1
	}
	return config, config.validate()
}

type NewProject struct {
	ID                 ProjectID
	Name               string
	Root               string
	VerificationPolicy VerificationPolicy
}

type NewAgent struct {
	ID              AgentID
	ProjectID       ProjectID
	Name            string
	Role            AgentRole
	Provider        Provider
	ExecutionMode   ExecutionMode
	Model           string
	ReasoningEffort string
	ToolBudgetLimit uint64
}

type NewTask struct {
	ID              TaskID
	ProjectID       ProjectID
	AssignedAgentID AgentID
	IncarnationID   IncarnationID
	Title           string
	Body            string
	Priority        int64
}

type FactoryState struct {
	DispatchEnabled bool
	Capacity        uint16
	Revision        Revision
	Head            EventSequence
	Floor           EventSequence
}

type Project struct {
	ID                 ProjectID
	Name               string
	Root               string
	VerificationPolicy VerificationPolicy
	Revision           Revision
	CreatedAt          UnixMillis
	UpdatedAt          UnixMillis
}

type Agent struct {
	ID              AgentID
	ProjectID       ProjectID
	Name            string
	Role            AgentRole
	Provider        Provider
	ExecutionMode   ExecutionMode
	Model           string
	ReasoningEffort string
	Paused          bool
	ToolBudgetLimit uint64
	ToolCallsUsed   uint64
	Revision        Revision
	CreatedAt       UnixMillis
	UpdatedAt       UnixMillis
}

type Task struct {
	ID              TaskID
	ProjectID       ProjectID
	AssignedAgentID AgentID
	IncarnationID   IncarnationID
	WorkRevision    Revision
	Title           string
	Body            string
	Status          TaskStatus
	Priority        int64
	BlockedReason   string
	Result          string
	CompletedAt     *UnixMillis
	Revision        Revision
	CreatedAt       UnixMillis
	UpdatedAt       UnixMillis
}

type ProjectSummary struct {
	ID       ProjectID
	Name     string
	Revision Revision
}

type AgentSummary struct {
	ID        AgentID
	ProjectID ProjectID
	Name      string
	Role      string
	Paused    bool
	Revision  Revision
}

type TaskSummary struct {
	ID              TaskID
	ProjectID       ProjectID
	AssignedAgentID AgentID
	Title           string
	Status          string
	Priority        int64
	Revision        Revision
}

type FactorySummary struct {
	DispatchEnabled bool
	Capacity        uint16
	ActiveRuns      uint16
	Revision        Revision
}

type DashboardSnapshot struct {
	Head     EventSequence
	Factory  FactorySummary
	Projects []ProjectSummary
	Agents   []AgentSummary
	Tasks    []TaskSummary
}

type Invalidation struct {
	Sequence   EventSequence
	OccurredAt UnixMillis
	EntityKind string
	EntityID   string
	Revision   Revision
	Deleted    bool
}

type WatchBatch struct {
	Head          EventSequence
	Floor         EventSequence
	Invalidations []Invalidation
}

func byteLen(value string) int { return len([]byte(value)) }
