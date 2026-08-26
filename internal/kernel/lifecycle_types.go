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
	ChangeSelected
	ChangePrepared
	ChangeAvailable
)

func parseChangePhase(value string) (ChangePhase, error) {
	switch value {
	case "reserved":
		return ChangeReserved, nil
	case "selected":
		return ChangeSelected, nil
	case "prepared":
		return ChangePrepared, nil
	case "available":
		return ChangeAvailable, nil
	default:
		return 0, corruptControl("change phase", value)
	}
}

func (value ChangePhase) String() string {
	switch value {
	case ChangeReserved:
		return "reserved"
	case ChangeSelected:
		return "selected"
	case ChangePrepared:
		return "prepared"
	case ChangeAvailable:
		return "available"
	default:
		return ""
	}
}

type ChangeSelection struct {
	format         ObjectFormat
	commit         CommitID
	commitment     TreeDigest
	entries        uint32
	bytes          uint64
	repositoryRoot string
	repository     FileIdentity
}

func NewChangeSelection(format ObjectFormat, commit CommitID, commitment TreeDigest, entries uint32, totalBytes uint64, repositoryRoot string, repository FileIdentity) (ChangeSelection, error) {
	if format.oidLength() == 0 || commit.format != format || entries > MaxChangeTreeEntries || totalBytes > MaxChangeTreeBlobBytes || !repository.valid() || !validOwnedLocator(repositoryRoot) {
		return ChangeSelection{}, fmt.Errorf("%w: invalid Change selection", ErrInvalidValue)
	}
	return ChangeSelection{format: format, commit: commit, commitment: commitment, entries: entries, bytes: totalBytes, repositoryRoot: repositoryRoot, repository: repository}, nil
}

func (selection ChangeSelection) ObjectFormat() ObjectFormat       { return selection.format }
func (selection ChangeSelection) Commit() CommitID                 { return selection.commit }
func (selection ChangeSelection) Commitment() TreeDigest           { return selection.commitment }
func (selection ChangeSelection) EntryCount() uint32               { return selection.entries }
func (selection ChangeSelection) TotalBytes() uint64               { return selection.bytes }
func (selection ChangeSelection) RepositoryRoot() string           { return selection.repositoryRoot }
func (selection ChangeSelection) RepositoryIdentity() FileIdentity { return selection.repository }
func (selection ChangeSelection) valid() bool {
	return selection.format.oidLength() != 0 && selection.commit.format == selection.format && len(selection.commit.Bytes()) == selection.format.oidLength() && selection.entries <= MaxChangeTreeEntries && selection.bytes <= MaxChangeTreeBlobBytes && selection.repository.valid() && validOwnedLocator(selection.repositoryRoot)
}

type ChangeAvailability struct {
	commitment TreeDigest
	entries    uint32
	bytes      uint64
	source     FileIdentity
}

func NewChangeAvailability(commitment TreeDigest, entries uint32, totalBytes uint64, source FileIdentity) (ChangeAvailability, error) {
	if entries > MaxChangeTreeEntries || totalBytes > MaxChangeTreeBlobBytes || !source.valid() {
		return ChangeAvailability{}, fmt.Errorf("%w: invalid Change availability", ErrInvalidValue)
	}
	return ChangeAvailability{commitment: commitment, entries: entries, bytes: totalBytes, source: source}, nil
}

func (value ChangeAvailability) Commitment() TreeDigest       { return value.commitment }
func (value ChangeAvailability) EntryCount() uint32           { return value.entries }
func (value ChangeAvailability) TotalBytes() uint64           { return value.bytes }
func (value ChangeAvailability) SourceIdentity() FileIdentity { return value.source }
func (value ChangeAvailability) valid() bool {
	return value.entries <= MaxChangeTreeEntries && value.bytes <= MaxChangeTreeBlobBytes && value.source.valid()
}

type Change struct {
	ID                ChangeID
	ProjectID         ProjectID
	TaskID            TaskID
	TaskIncarnationID IncarnationID
	Phase             ChangePhase
	SourceRoot        string
	StagingRoot       string
	Selection         *ChangeSelection
	StageIdentity     *FileIdentity
	Availability      *ChangeAvailability
	Revision          Revision
	CreatedAt         UnixMillis
	UpdatedAt         UnixMillis
	SelectedAt        *UnixMillis
	PreparedAt        *UnixMillis
	AvailableAt       *UnixMillis
}

type ChangeReservation struct {
	ID                    ChangeID
	SourceRoot            string
	StagingRoot           string
	ExpectedReuseRevision *Revision
}

func (reservation ChangeReservation) valid() bool {
	return !reservation.ID.zero() && validOwnedLocator(reservation.SourceRoot) && validOwnedLocator(reservation.StagingRoot) && !pathsOverlap(reservation.SourceRoot, reservation.StagingRoot) && (reservation.ExpectedReuseRevision == nil || reservation.ExpectedReuseRevision.Int64() >= 1)
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

type runnerExitKind uint8

const (
	runnerExitCode runnerExitKind = iota + 1
	runnerExitSignal
	runnerExitRecoveredAbsence
)

func parseRunnerExitKind(value string) (runnerExitKind, error) {
	switch value {
	case "code":
		return runnerExitCode, nil
	case "signal":
		return runnerExitSignal, nil
	case "recovered_absence":
		return runnerExitRecoveredAbsence, nil
	default:
		return 0, corruptControl("runner exit kind", value)
	}
}

func (kind runnerExitKind) String() string {
	switch kind {
	case runnerExitCode:
		return "code"
	case runnerExitSignal:
		return "signal"
	case runnerExitRecoveredAbsence:
		return "recovered_absence"
	default:
		return ""
	}
}

type RunnerExit struct {
	kind     runnerExitKind
	sequence int64
	code     *int64
	signal   *int64
	at       UnixMillis
}

func NewRunnerExitCode(sequence uint64, code int64, at UnixMillis) (RunnerExit, error) {
	if sequence < 1 || sequence > math.MaxInt64 || code < 0 {
		return RunnerExit{}, fmt.Errorf("%w: invalid runner exit code", ErrInvalidValue)
	}
	return RunnerExit{kind: runnerExitCode, sequence: int64(sequence), code: &code, at: at}, nil
}

func NewRunnerExitSignal(sequence uint64, signal int64, at UnixMillis) (RunnerExit, error) {
	if sequence < 1 || sequence > math.MaxInt64 || signal < 1 {
		return RunnerExit{}, fmt.Errorf("%w: invalid runner exit signal", ErrInvalidValue)
	}
	return RunnerExit{kind: runnerExitSignal, sequence: int64(sequence), signal: &signal, at: at}, nil
}

// NewRunnerExitRecoveredAbsence records that recovery, without live-child or
// Wait ownership, positively proved the exact registered runner disappeared.
// It does not represent an uncertain, malformed, or permission-denied probe.
func NewRunnerExitRecoveredAbsence(sequence uint64, at UnixMillis) (RunnerExit, error) {
	if sequence < 1 || sequence > math.MaxInt64 {
		return RunnerExit{}, fmt.Errorf("%w: invalid recovered runner absence", ErrInvalidValue)
	}
	return RunnerExit{kind: runnerExitRecoveredAbsence, sequence: int64(sequence), at: at}, nil
}

func (exit RunnerExit) Sequence() uint64 { return uint64(exit.sequence) }
func (exit RunnerExit) Code() (int64, bool) {
	if exit.code == nil {
		return 0, false
	}
	return *exit.code, true
}
func (exit RunnerExit) Signal() (int64, bool) {
	if exit.signal == nil {
		return 0, false
	}
	return *exit.signal, true
}
func (exit RunnerExit) RecoveredAbsence() bool { return exit.kind == runnerExitRecoveredAbsence }
func (exit RunnerExit) At() UnixMillis         { return exit.at }
func (exit RunnerExit) valid() bool {
	if exit.sequence < 1 {
		return false
	}
	switch exit.kind {
	case runnerExitCode:
		return exit.code != nil && *exit.code >= 0 && exit.signal == nil
	case runnerExitSignal:
		return exit.code == nil && exit.signal != nil && *exit.signal > 0
	case runnerExitRecoveredAbsence:
		return exit.code == nil && exit.signal == nil
	default:
		return false
	}
}
func (exit RunnerExit) equal(other RunnerExit) bool {
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
	Role                     AgentRole
	Provider                 Provider
	ExecutionMode            ExecutionMode
	Model                    string
	ReasoningEffort          string
	VerificationPolicy       VerificationPolicy
	Phase                    RunPhase
	Proposal                 *Proposal
	Terminal                 *Proposal
	CredentialDigest         AttemptDigest
	CredentialRevokedAt      *UnixMillis
	RunnerExit               *RunnerExit
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
	RunID         RunID
	AttemptDigest AttemptDigest
	Change        *ChangeReservation
	Resources     AdmissionResourceIDs
	RuntimeRoot   string
}

func (keys AdmissionKeys) valid() bool {
	if keys.RunID.zero() || !keys.Resources.valid() || !validOwnedLocator(keys.RuntimeRoot) || keys.Change != nil && !keys.Change.valid() {
		return false
	}
	return keys.Change == nil || !pathsOverlap(keys.RuntimeRoot, keys.Change.SourceRoot) && !pathsOverlap(keys.RuntimeRoot, keys.Change.StagingRoot)
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
	ExecutionMode   ExecutionMode
	ChangeID        *ChangeID
}

type RecoverableRun struct {
	Run       Run
	Change    *Change
	Resources []Resource
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
