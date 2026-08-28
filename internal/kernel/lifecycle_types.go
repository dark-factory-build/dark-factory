package kernel

import (
	"bytes"
	"fmt"
	"math"
	"path/filepath"
	"strings"
)

type ObjectFormat uint8

const (
	ObjectSHA1 ObjectFormat = iota + 1
	ObjectSHA256
)

func NewObjectFormat(value string) (ObjectFormat, error) {
	switch value {
	case "sha1":
		return ObjectSHA1, nil
	case "sha256":
		return ObjectSHA256, nil
	default:
		return 0, fmt.Errorf("%w: unsupported Git object format", ErrInvalidValue)
	}
}

func parseObjectFormat(value string) (ObjectFormat, error) {
	format, err := NewObjectFormat(value)
	if err != nil {
		return 0, corruptControl("Git object format", value)
	}
	return format, nil
}

func (value ObjectFormat) String() string {
	switch value {
	case ObjectSHA1:
		return "sha1"
	case ObjectSHA256:
		return "sha256"
	default:
		return ""
	}
}

func (value ObjectFormat) oidLength() int {
	switch value {
	case ObjectSHA1:
		return 20
	case ObjectSHA256:
		return 32
	default:
		return 0
	}
}

type CommitID struct {
	format ObjectFormat
	raw    [32]byte
}

func NewCommitID(format ObjectFormat, raw []byte) (CommitID, error) {
	if len(raw) != format.oidLength() {
		return CommitID{}, fmt.Errorf("%w: Git object identifier length", ErrInvalidValue)
	}
	var result CommitID
	result.format = format
	copy(result.raw[:], raw)
	return result, nil
}

func (id CommitID) Format() ObjectFormat { return id.format }
func (id CommitID) Bytes() []byte        { return bytes.Clone(id.raw[:id.format.oidLength()]) }
func (id CommitID) equal(other CommitID) bool {
	return id.format == other.format && id.raw == other.raw
}

type FileIdentity struct {
	device int64
	inode  int64
}

func NewFileIdentity(device, inode int64) (FileIdentity, error) {
	if device < 0 || inode < 1 {
		return FileIdentity{}, fmt.Errorf("%w: invalid filesystem identity", ErrInvalidValue)
	}
	return FileIdentity{device: device, inode: inode}, nil
}

func (identity FileIdentity) Device() int64 { return identity.device }
func (identity FileIdentity) Inode() int64  { return identity.inode }
func (identity FileIdentity) valid() bool   { return identity.device >= 0 && identity.inode > 0 }

type ChangePhase uint8

const (
	ChangeReserved ChangePhase = iota + 1
	ChangePrepared
	ChangeAvailable
	ChangeRetained
	ChangeAbandoned
)

func parseChangePhase(value string) (ChangePhase, error) {
	switch value {
	case "reserved":
		return ChangeReserved, nil
	case "prepared":
		return ChangePrepared, nil
	case "available":
		return ChangeAvailable, nil
	case "retained":
		return ChangeRetained, nil
	case "abandoned":
		return ChangeAbandoned, nil
	default:
		return 0, corruptControl("change phase", value)
	}
}

func (value ChangePhase) String() string {
	switch value {
	case ChangeReserved:
		return "reserved"
	case ChangePrepared:
		return "prepared"
	case ChangeAvailable:
		return "available"
	case ChangeRetained:
		return "retained"
	case ChangeAbandoned:
		return "abandoned"
	default:
		return ""
	}
}

type ChangeSelection struct {
	format     ObjectFormat
	commit     CommitID
	commitment TreeDigest
	entries    uint32
	bytes      uint64
	repository FileIdentity
}

func NewChangeSelection(format ObjectFormat, commit CommitID, commitment TreeDigest, entries uint32, totalBytes uint64, repository FileIdentity) (ChangeSelection, error) {
	if format.oidLength() == 0 || commit.format != format || entries > MaxChangeTreeEntries || totalBytes > MaxChangeTreeBlobBytes || !repository.valid() {
		return ChangeSelection{}, fmt.Errorf("%w: invalid Change selection", ErrInvalidValue)
	}
	return ChangeSelection{format: format, commit: commit, commitment: commitment, entries: entries, bytes: totalBytes, repository: repository}, nil
}

func (selection ChangeSelection) ObjectFormat() ObjectFormat       { return selection.format }
func (selection ChangeSelection) Commit() CommitID                 { return selection.commit }
func (selection ChangeSelection) Commitment() TreeDigest           { return selection.commitment }
func (selection ChangeSelection) EntryCount() uint32               { return selection.entries }
func (selection ChangeSelection) TotalBytes() uint64               { return selection.bytes }
func (selection ChangeSelection) RepositoryIdentity() FileIdentity { return selection.repository }
func (selection ChangeSelection) valid() bool {
	return selection.format.oidLength() != 0 && selection.commit.format == selection.format && len(selection.commit.Bytes()) == selection.format.oidLength() && selection.entries <= MaxChangeTreeEntries && selection.bytes <= MaxChangeTreeBlobBytes && selection.repository.valid()
}

type ChangeAvailability struct {
	commitment TreeDigest
	entries    uint32
	bytes      uint64
	tree       FileIdentity
}

func NewChangeAvailability(commitment TreeDigest, entries uint32, totalBytes uint64, tree FileIdentity) (ChangeAvailability, error) {
	if entries > MaxChangeTreeEntries || totalBytes > MaxChangeTreeBlobBytes || !tree.valid() {
		return ChangeAvailability{}, fmt.Errorf("%w: invalid Change availability", ErrInvalidValue)
	}
	return ChangeAvailability{commitment: commitment, entries: entries, bytes: totalBytes, tree: tree}, nil
}

func (value ChangeAvailability) Commitment() TreeDigest     { return value.commitment }
func (value ChangeAvailability) EntryCount() uint32         { return value.entries }
func (value ChangeAvailability) TotalBytes() uint64         { return value.bytes }
func (value ChangeAvailability) TreeIdentity() FileIdentity { return value.tree }
func (value ChangeAvailability) valid() bool {
	return value.entries <= MaxChangeTreeEntries && value.bytes <= MaxChangeTreeBlobBytes && value.tree.valid()
}

type Change struct {
	ID                ChangeID
	ProjectID         ProjectID
	TaskID            TaskID
	TaskIncarnationID IncarnationID
	Phase             ChangePhase
	Selection         *ChangeSelection
	TreeIdentity      *FileIdentity
	SettledRunID      *RunID
	Revision          Revision
	CreatedAt         UnixMillis
	UpdatedAt         UnixMillis
	PreparedAt        *UnixMillis
	AvailableAt       *UnixMillis
}

type ChangeSettlement struct {
	phase        ChangePhase
	expected     Revision
	availability *ChangeAvailability
}

func NewRetainedChangeSettlement(expected Revision, availability ChangeAvailability) (ChangeSettlement, error) {
	if expected.Int64() < 1 || !availability.valid() {
		return ChangeSettlement{}, fmt.Errorf("%w: invalid retained Change settlement", ErrInvalidValue)
	}
	return ChangeSettlement{phase: ChangeRetained, expected: expected, availability: &availability}, nil
}

func NewAbandonedChangeSettlement(expected Revision) (ChangeSettlement, error) {
	if expected.Int64() < 1 {
		return ChangeSettlement{}, fmt.Errorf("%w: invalid abandoned Change settlement", ErrInvalidValue)
	}
	return ChangeSettlement{phase: ChangeAbandoned, expected: expected}, nil
}

func (settlement ChangeSettlement) valid() bool {
	return settlement.expected.Int64() >= 1 && (settlement.phase == ChangeRetained && settlement.availability != nil && settlement.availability.valid() || settlement.phase == ChangeAbandoned && settlement.availability == nil)
}

type RunPhase uint8

const (
	RunAdmitted RunPhase = iota + 1
	RunRunning
	RunFinalizing
	RunTerminal
)

func parseRunPhase(value string) (RunPhase, error) {
	switch value {
	case "admitted":
		return RunAdmitted, nil
	case "running":
		return RunRunning, nil
	case "finalizing":
		return RunFinalizing, nil
	case "terminal":
		return RunTerminal, nil
	default:
		return 0, corruptControl("run phase", value)
	}
}

func (value RunPhase) String() string {
	switch value {
	case RunAdmitted:
		return "admitted"
	case RunRunning:
		return "running"
	case RunFinalizing:
		return "finalizing"
	case RunTerminal:
		return "terminal"
	default:
		return ""
	}
}

type TerminalSessionState uint8

const (
	TerminalSessionDeclared TerminalSessionState = iota + 1
	TerminalSessionActive
	TerminalSessionClosed
	TerminalSessionUnresolved
)

func parseTerminalSessionState(value string) (TerminalSessionState, error) {
	switch value {
	case "declared":
		return TerminalSessionDeclared, nil
	case "active":
		return TerminalSessionActive, nil
	case "closed":
		return TerminalSessionClosed, nil
	case "unresolved":
		return TerminalSessionUnresolved, nil
	default:
		return 0, corruptControl("terminal session state", value)
	}
}

func (value TerminalSessionState) String() string {
	switch value {
	case TerminalSessionDeclared:
		return "declared"
	case TerminalSessionActive:
		return "active"
	case TerminalSessionClosed:
		return "closed"
	case TerminalSessionUnresolved:
		return "unresolved"
	default:
		return ""
	}
}

type TerminalSession struct {
	ID                TerminalSessionID
	RunID             RunID
	State             TerminalSessionState
	UnresolvedReason  string
	Revision          Revision
	DeclaredAt        UnixMillis
	ActivatedAt       *UnixMillis
	ClosedAt          *UnixMillis
	UpdatedAt         UnixMillis
	LeaseClientID     *BrowserClientID
	LeaseGeneration   uint64
	LeaseExpiresAt    *UnixMillis
	LastInputSequence uint64
}

type OutcomeKind uint8

const (
	OutcomeSucceeded OutcomeKind = iota + 1
	OutcomeBlocked
	OutcomeFailed
	OutcomeCancelled
)

func parseOutcomeKind(value string) (OutcomeKind, error) {
	switch value {
	case "succeeded":
		return OutcomeSucceeded, nil
	case "blocked":
		return OutcomeBlocked, nil
	case "failed":
		return OutcomeFailed, nil
	case "cancelled":
		return OutcomeCancelled, nil
	default:
		return 0, corruptControl("outcome kind", value)
	}
}

func (value OutcomeKind) String() string {
	switch value {
	case OutcomeSucceeded:
		return "succeeded"
	case OutcomeBlocked:
		return "blocked"
	case OutcomeFailed:
		return "failed"
	case OutcomeCancelled:
		return "cancelled"
	default:
		return ""
	}
}

type FailureCode uint8

const (
	FailureSpawn FailureCode = iota + 1
	FailureActivation
	FailureSource
	FailureProviderExit
	FailureRunnerExit
	FailureProtocol
	FailureInternal
	FailureAttempt
)

func parseFailureCode(value string) (FailureCode, error) {
	switch value {
	case "spawn":
		return FailureSpawn, nil
	case "activation":
		return FailureActivation, nil
	case "source":
		return FailureSource, nil
	case "provider_exit":
		return FailureProviderExit, nil
	case "runner_exit":
		return FailureRunnerExit, nil
	case "protocol":
		return FailureProtocol, nil
	case "internal":
		return FailureInternal, nil
	case "attempt":
		return FailureAttempt, nil
	default:
		return 0, corruptControl("failure code", value)
	}
}

func (value FailureCode) String() string {
	switch value {
	case FailureSpawn:
		return "spawn"
	case FailureActivation:
		return "activation"
	case FailureSource:
		return "source"
	case FailureProviderExit:
		return "provider_exit"
	case FailureRunnerExit:
		return "runner_exit"
	case FailureProtocol:
		return "protocol"
	case FailureInternal:
		return "internal"
	case FailureAttempt:
		return "attempt"
	default:
		return ""
	}
}

type Proposal struct {
	kind   OutcomeKind
	code   FailureCode
	detail string
	result string
}

func NewSuccessProposal(result string) (Proposal, error) {
	if byteLen(result) > 131072 {
		return Proposal{}, fmt.Errorf("%w: success result is too large", ErrInvalidValue)
	}
	return Proposal{kind: OutcomeSucceeded, result: result}, nil
}

func NewBlockedProposal(detail string) (Proposal, error) {
	if byteLen(detail) < 1 || byteLen(detail) > 4096 {
		return Proposal{}, fmt.Errorf("%w: invalid blocked detail", ErrInvalidValue)
	}
	return Proposal{kind: OutcomeBlocked, detail: detail}, nil
}

func NewFailureProposal(code FailureCode, detail string) (Proposal, error) {
	if code.String() == "" || byteLen(detail) > 4096 {
		return Proposal{}, fmt.Errorf("%w: invalid failure proposal", ErrInvalidValue)
	}
	return Proposal{kind: OutcomeFailed, code: code, detail: detail}, nil
}

func NewCancelledProposal(detail string) (Proposal, error) {
	if byteLen(detail) < 1 || byteLen(detail) > 4096 {
		return Proposal{}, fmt.Errorf("%w: invalid cancellation detail", ErrInvalidValue)
	}
	return Proposal{kind: OutcomeCancelled, detail: detail}, nil
}

func (proposal Proposal) Kind() OutcomeKind { return proposal.kind }
func (proposal Proposal) Code() FailureCode { return proposal.code }
func (proposal Proposal) Detail() string    { return proposal.detail }
func (proposal Proposal) Result() string    { return proposal.result }
func (proposal Proposal) valid() bool {
	switch proposal.kind {
	case OutcomeSucceeded:
		return proposal.code == 0 && proposal.detail == "" && byteLen(proposal.result) <= 131072
	case OutcomeBlocked, OutcomeCancelled:
		return proposal.code == 0 && byteLen(proposal.detail) >= 1 && byteLen(proposal.detail) <= 4096 && proposal.result == ""
	case OutcomeFailed:
		return proposal.code.String() != "" && byteLen(proposal.detail) <= 4096 && proposal.result == ""
	default:
		return false
	}
}
func (proposal Proposal) equal(other Proposal) bool { return proposal == other }

type VerificationPolicy uint8

const (
	VerificationNone VerificationPolicy = iota + 1
	VerificationRustWorkspaceTest
	VerificationGoWorkspaceTest
)

func parseVerificationPolicy(value string) (VerificationPolicy, error) {
	switch value {
	case "none":
		return VerificationNone, nil
	case "rust_workspace_test":
		return VerificationRustWorkspaceTest, nil
	case "go_workspace_test":
		return VerificationGoWorkspaceTest, nil
	default:
		return 0, corruptControl("verification policy", value)
	}
}

func (policy VerificationPolicy) String() string {
	switch policy {
	case VerificationNone:
		return "none"
	case VerificationRustWorkspaceTest:
		return "rust_workspace_test"
	case VerificationGoWorkspaceTest:
		return "go_workspace_test"
	default:
		return ""
	}
}

type processExitKind uint8

const (
	processExitCode processExitKind = iota + 1
	processExitSignal
	processExitRecoveredAbsence
)

func parseProcessExitKind(value string) (processExitKind, error) {
	switch value {
	case "code":
		return processExitCode, nil
	case "signal":
		return processExitSignal, nil
	case "recovered_absence":
		return processExitRecoveredAbsence, nil
	default:
		return 0, corruptControl("process exit kind", value)
	}
}

func (kind processExitKind) String() string {
	switch kind {
	case processExitCode:
		return "code"
	case processExitSignal:
		return "signal"
	case processExitRecoveredAbsence:
		return "recovered_absence"
	default:
		return ""
	}
}

type ProcessExit struct {
	kind     processExitKind
	sequence int64
	code     *int64
	signal   *int64
	at       UnixMillis
}

func NewProcessExitCode(sequence uint64, code int64, at UnixMillis) (ProcessExit, error) {
	if sequence < 1 || sequence > math.MaxInt64 || code < 0 {
		return ProcessExit{}, fmt.Errorf("%w: invalid process exit code", ErrInvalidValue)
	}
	return ProcessExit{kind: processExitCode, sequence: int64(sequence), code: &code, at: at}, nil
}

func NewProcessExitSignal(sequence uint64, signal int64, at UnixMillis) (ProcessExit, error) {
	if sequence < 1 || sequence > math.MaxInt64 || signal < 1 {
		return ProcessExit{}, fmt.Errorf("%w: invalid process exit signal", ErrInvalidValue)
	}
	return ProcessExit{kind: processExitSignal, sequence: int64(sequence), signal: &signal, at: at}, nil
}

// NewProcessExitRecoveredAbsence records that recovery, without live-child or
// Wait ownership, positively proved the exact registered process disappeared.
// It does not represent an uncertain, malformed, or permission-denied probe.
func NewProcessExitRecoveredAbsence(sequence uint64, at UnixMillis) (ProcessExit, error) {
	if sequence < 1 || sequence > math.MaxInt64 {
		return ProcessExit{}, fmt.Errorf("%w: invalid recovered process absence", ErrInvalidValue)
	}
	return ProcessExit{kind: processExitRecoveredAbsence, sequence: int64(sequence), at: at}, nil
}

func (exit ProcessExit) Sequence() uint64 { return uint64(exit.sequence) }
func (exit ProcessExit) Code() (int64, bool) {
	if exit.code == nil {
		return 0, false
	}
	return *exit.code, true
}
func (exit ProcessExit) Signal() (int64, bool) {
	if exit.signal == nil {
		return 0, false
	}
	return *exit.signal, true
}
func (exit ProcessExit) RecoveredAbsence() bool { return exit.kind == processExitRecoveredAbsence }
func (exit ProcessExit) At() UnixMillis         { return exit.at }
func (exit ProcessExit) valid() bool {
	if exit.sequence < 1 {
		return false
	}
	switch exit.kind {
	case processExitCode:
		return exit.code != nil && *exit.code >= 0 && exit.signal == nil
	case processExitSignal:
		return exit.code == nil && exit.signal != nil && *exit.signal > 0
	case processExitRecoveredAbsence:
		return exit.code == nil && exit.signal == nil
	default:
		return false
	}
}
func (exit ProcessExit) equal(other ProcessExit) bool {
	if exit.kind != other.kind || exit.sequence != other.sequence || exit.at != other.at {
		return false
	}
	leftCode, leftHasCode := exit.Code()
	rightCode, rightHasCode := other.Code()
	leftSignal, leftHasSignal := exit.Signal()
	rightSignal, rightHasSignal := other.Signal()
	return leftCode == rightCode && leftHasCode == rightHasCode && leftSignal == rightSignal && leftHasSignal == rightHasSignal
}

type ResourceKind uint8

const (
	ResourceRuntimeRoot ResourceKind = iota + 1
	ResourceRunnerProcess
	ResourceProviderProcess
	ResourceProviderGroup
)

func parseResourceKind(value string) (ResourceKind, error) {
	switch value {
	case "runtime_root":
		return ResourceRuntimeRoot, nil
	case "runner_process":
		return ResourceRunnerProcess, nil
	case "provider_process":
		return ResourceProviderProcess, nil
	case "provider_group":
		return ResourceProviderGroup, nil
	default:
		return 0, corruptControl("resource kind", value)
	}
}

func (value ResourceKind) String() string {
	switch value {
	case ResourceRuntimeRoot:
		return "runtime_root"
	case ResourceRunnerProcess:
		return "runner_process"
	case ResourceProviderProcess:
		return "provider_process"
	case ResourceProviderGroup:
		return "provider_group"
	default:
		return ""
	}
}

type ResourceState uint8

const (
	ResourceDeclared ResourceState = iota + 1
	ResourceActive
	ResourceReleasing
	ResourceUnresolved
	ResourceReleased
)

func parseResourceState(value string) (ResourceState, error) {
	switch value {
	case "declared":
		return ResourceDeclared, nil
	case "active":
		return ResourceActive, nil
	case "releasing":
		return ResourceReleasing, nil
	case "unresolved":
		return ResourceUnresolved, nil
	case "released":
		return ResourceReleased, nil
	default:
		return 0, corruptControl("resource state", value)
	}
}

func (value ResourceState) String() string {
	switch value {
	case ResourceDeclared:
		return "declared"
	case ResourceActive:
		return "active"
	case ResourceReleasing:
		return "releasing"
	case ResourceUnresolved:
		return "unresolved"
	case ResourceReleased:
		return "released"
	default:
		return ""
	}
}

type resourceIdentityKind uint8

const (
	identityEmpty resourceIdentityKind = iota + 1
	identityPath
	identityProcess
)

type ResourceIdentity struct {
	kind  resourceIdentityKind
	path  FileIdentity
	pid   int64
	pgid  int64
	birth BirthDigest
}

func EmptyResourceIdentity() ResourceIdentity { return ResourceIdentity{kind: identityEmpty} }
func NewPathResourceIdentity(device, inode int64) (ResourceIdentity, error) {
	identity, err := NewFileIdentity(device, inode)
	if err != nil {
		return ResourceIdentity{}, err
	}
	return ResourceIdentity{kind: identityPath, path: identity}, nil
}
func NewProcessResourceIdentity(pid, pgid int64, birth BirthDigest) (ResourceIdentity, error) {
	if pid <= 1 || pgid <= 1 {
		return ResourceIdentity{}, fmt.Errorf("%w: invalid process identity", ErrInvalidValue)
	}
	return ResourceIdentity{kind: identityProcess, pid: pid, pgid: pgid, birth: birth}, nil
}
func (identity ResourceIdentity) Empty() bool { return identity.kind == identityEmpty }
func (identity ResourceIdentity) Path() (FileIdentity, bool) {
	return identity.path, identity.kind == identityPath
}
func (identity ResourceIdentity) Process() (int64, int64, BirthDigest, bool) {
	return identity.pid, identity.pgid, identity.birth, identity.kind == identityProcess
}
func (identity ResourceIdentity) validFor(kind ResourceKind) bool {
	return kind == ResourceRuntimeRoot && identity.kind == identityPath && identity.path.valid() || kind != ResourceRuntimeRoot && kind.String() != "" && identity.kind == identityProcess && identity.pid > 1 && identity.pgid > 1
}

type Run struct {
	ID                       RunID
	ProjectID                ProjectID
	AgentID                  AgentID
	TaskID                   TaskID
	TaskIncarnationID        IncarnationID
	AdmittedTaskWorkRevision Revision
	ChangeID                 *ChangeID
	AdmittedChangeRevision   *Revision
	Role                     AgentRole
	Provider                 Provider
	Model                    string
	ReasoningEffort          string
	VerificationPolicy       VerificationPolicy
	Phase                    RunPhase
	Proposal                 *Proposal
	Terminal                 *Proposal
	CredentialDigest         AttemptDigest
	CredentialRevokedAt      *UnixMillis
	ProviderExit             *ProcessExit
	RunnerExit               *ProcessExit
	Revision                 Revision
	AdmittedAt               UnixMillis
	RunningAt                *UnixMillis
	FinalizingAt             *UnixMillis
	TerminalAt               *UnixMillis
	UpdatedAt                UnixMillis
}

type Resource struct {
	ID               ResourceID
	RunID            RunID
	Kind             ResourceKind
	State            ResourceState
	Path             string
	Identity         ResourceIdentity
	UnresolvedReason string
	Revision         Revision
	DeclaredAt       UnixMillis
	ActivatedAt      *UnixMillis
	UpdatedAt        UnixMillis
	ReleasedAt       *UnixMillis
}

type AdmissionResourceIDs struct {
	RuntimeRoot     ResourceID
	RunnerProcess   ResourceID
	ProviderProcess ResourceID
	ProviderGroup   ResourceID
}

func (ids AdmissionResourceIDs) valid() bool {
	values := []ResourceID{ids.RuntimeRoot, ids.RunnerProcess, ids.ProviderProcess, ids.ProviderGroup}
	seen := make(map[string]bool, len(values))
	for _, id := range values {
		if id.zero() || seen[id.String()] {
			return false
		}
		seen[id.String()] = true
	}
	return true
}

type AdmissionKeys struct {
	RunID             RunID
	TerminalSessionID TerminalSessionID
	AttemptDigest     AttemptDigest
	CandidateChangeID ChangeID
	Resources         AdmissionResourceIDs
	RuntimeRoot       string
}

func (keys AdmissionKeys) valid() bool {
	if keys.RunID.zero() || keys.TerminalSessionID.zero() || keys.CandidateChangeID.zero() || !keys.Resources.valid() || !validOwnedLocator(keys.RuntimeRoot) {
		return false
	}
	return true
}

type NoAdmissionReason uint8

const (
	NoAdmissionDispatchDisabled NoAdmissionReason = iota + 1
	NoAdmissionAtCapacity
	NoAdmissionAgentPaused
	NoAdmissionBudgetExhausted
	NoAdmissionAgentBusy
	NoAdmissionQueueEmpty
	NoAdmissionNotReconciled
)

func (reason NoAdmissionReason) String() string {
	switch reason {
	case NoAdmissionDispatchDisabled:
		return "dispatch_disabled"
	case NoAdmissionAtCapacity:
		return "at_capacity"
	case NoAdmissionAgentPaused:
		return "agent_paused"
	case NoAdmissionBudgetExhausted:
		return "budget_exhausted"
	case NoAdmissionAgentBusy:
		return "agent_busy"
	case NoAdmissionQueueEmpty:
		return "queue_empty"
	case NoAdmissionNotReconciled:
		return "not_reconciled"
	default:
		return ""
	}
}

type AdmissionResult struct {
	Run    *Run
	Reason NoAdmissionReason
}

func (result AdmissionResult) Admitted() bool { return result.Run != nil }

type AttemptAuthority struct {
	RunID           RunID
	ProjectID       ProjectID
	AgentID         AgentID
	TaskID          TaskID
	TaskIncarnation IncarnationID
	Role            AgentRole
	Provider        Provider
	ChangeID        *ChangeID
}

type RecoverableRun struct {
	Run             Run
	Change          *Change
	Resources       []Resource
	TerminalSession TerminalSession
}

func validAbsolutePath(value string) bool {
	return byteLen(value) >= 1 && byteLen(value) <= 4096 && !strings.ContainsRune(value, 0) && filepath.IsAbs(value) && filepath.Clean(value) == value
}

func validOwnedLocator(value string) bool {
	return validAbsolutePath(value) && value != string(filepath.Separator)
}

func pathsOverlap(left, right string) bool {
	if left == right {
		return true
	}
	separator := string(filepath.Separator)
	return strings.HasPrefix(left, right+separator) || strings.HasPrefix(right, left+separator)
}
