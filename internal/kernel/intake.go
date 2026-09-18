package kernel

import (
	"crypto/sha256"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

const (
	maxGitHubRepositoryNameBytes = 140
	maxGitHubNodeIDBytes         = 256
	maxGitHubLoginBytes          = 39
	maxIntakeLabelBytes          = 100
	maxIntakeTitleBytes          = 900
	maxIntakeBodyBytes           = 5000
)

// IntakePolicy decides whether the first observed content snapshot can enter
// automatically. Every later content edit requires an explicit acceptance.
type IntakePolicy string

const (
	IntakePolicyManual         IntakePolicy = "manual"
	IntakePolicyTrustedAuthors IntakePolicy = "trusted_authors"
)

// IntakeSource is private operator configuration for one GitHub issue feed.
// TargetRepositoryID is selected at configuration time and never follows a
// later project-default change.
type IntakeSource struct {
	ID                   IntakeSourceID
	GitHubRepositoryID   uint64
	GitHubRepositoryName string
	ProjectID            ProjectID
	TargetRepositoryID   RepositoryID
	OverseerAgentID      AgentID
	LabelFilter          string
	Enabled              bool
	Policy               IntakePolicy
	TrustedGitHubLogins  []string
	PollSeconds          uint32
	AdmissionLimit       uint16
	Revision             Revision
	CreatedAt, UpdatedAt UnixMillis
}

type NewIntakeSource struct {
	ID                   IntakeSourceID
	GitHubRepositoryID   uint64
	GitHubRepositoryName string
	ProjectID            ProjectID
	TargetRepositoryID   RepositoryID
	OverseerAgentID      AgentID
	LabelFilter          string
	Policy               IntakePolicy
	TrustedGitHubLogins  []string
	PollSeconds          uint32
	AdmissionLimit       uint16
}

// IntakeIssueSnapshot is the only mutable GitHub issue material that intake
// considers. Metadata such as labels, comments, reactions, and updated_at is
// intentionally absent: none can silently create new work.
type IntakeIssueSnapshot struct {
	GitHubRepositoryID uint64
	IssueNumber        uint64
	NodeID             string
	Title              string
	Body               string
	AuthorLogin        string
	AuthorType         GitHubAuthorType
}

func (value IntakeIssueSnapshot) BodyHash() [DigestBytes]byte {
	return sha256.Sum256([]byte(value.Body))
}
func (value IntakeIssueSnapshot) ContentHash() [DigestBytes]byte {
	return sha256.Sum256([]byte(value.Title + "\x00" + value.Body))
}

type GitHubAuthorType string

const (
	GitHubAuthorUser GitHubAuthorType = "user"
	GitHubAuthorBot  GitHubAuthorType = "bot"
)

// IntakeAcceptance is an immutable reviewed content snapshot and its durable
// import receipt. TaskID and IncarnationID are derived without source config
// identity, so overlapping feeds cannot duplicate the same accepted work.
type IntakeAcceptance struct {
	ID              IntakeAcceptanceID
	Snapshot        IntakeIssueSnapshot
	BodyHash        [DigestBytes]byte
	ProjectID       ProjectID
	RepositoryID    RepositoryID
	OverseerAgentID AgentID
	TaskID          TaskID
	IncarnationID   IncarnationID
	WithdrawnAt     *UnixMillis
	CreatedAt       UnixMillis
}

type IntakeEligibility string

const (
	IntakeEligibleTrusted       IntakeEligibility = "eligible_trusted_author"
	IntakeNeedsManualAcceptance IntakeEligibility = "needs_manual_acceptance"
	IntakeAlreadyAccepted       IntakeEligibility = "already_accepted"
	IntakeWithdrawn             IntakeEligibility = "withdrawn"
	IntakeSourceDisabled        IntakeEligibility = "source_disabled"
	IntakeUntrustedAuthor       IntakeEligibility = "untrusted_author"
	IntakeContentChanged        IntakeEligibility = "content_changed"
	IntakeIdentityChanged       IntakeEligibility = "identity_changed"
	IntakeContentTooLarge       IntakeEligibility = "content_too_large"
	IntakeInvalidSnapshot       IntakeEligibility = "invalid_snapshot"
)

// PreviewIntake is a pure explanation for an observed issue. It does no
// network work and has no model involvement.
func PreviewIntake(source IntakeSource, snapshot IntakeIssueSnapshot, accepted *IntakeAcceptance) IntakeEligibility {
	if !source.Enabled {
		return IntakeSourceDisabled
	}
	if byteLen(snapshot.Title) > maxIntakeTitleBytes || byteLen(snapshot.Body) > maxIntakeBodyBytes {
		return IntakeContentTooLarge
	}
	if !validIntakeIssueSnapshot(snapshot) {
		return IntakeInvalidSnapshot
	}
	if accepted != nil {
		if !sameIntakeIssue(accepted.Snapshot, snapshot) {
			return IntakeIdentityChanged
		}
		if accepted.Snapshot.Title != snapshot.Title || accepted.BodyHash != snapshot.BodyHash() {
			return IntakeContentChanged
		}
		if accepted.WithdrawnAt != nil {
			return IntakeWithdrawn
		}
		return IntakeAlreadyAccepted
	}
	if source.Policy == IntakePolicyTrustedAuthors && snapshot.AuthorType == GitHubAuthorUser && trustedGitHubLogin(source.TrustedGitHubLogins, snapshot.AuthorLogin) {
		return IntakeEligibleTrusted
	}
	if source.Policy == IntakePolicyTrustedAuthors {
		return IntakeUntrustedAuthor
	}
	return IntakeNeedsManualAcceptance
}

func validIntakeSource(value IntakeSource) bool {
	if value.ID.zero() || value.GitHubRepositoryID == 0 || value.GitHubRepositoryID > math.MaxInt64 || !validGitHubRepositoryName(value.GitHubRepositoryName) || value.ProjectID.zero() || value.TargetRepositoryID.zero() || value.Policy != IntakePolicyManual && value.Policy != IntakePolicyTrustedAuthors || value.PollSeconds < 5 || value.PollSeconds > 86400 || value.AdmissionLimit < 1 || value.AdmissionLimit > 200 || value.Revision.Int64() < 1 || value.UpdatedAt.Int64() < value.CreatedAt.Int64() {
		return false
	}
	if value.LabelFilter != "" && !validBoundedIntakeText(value.LabelFilter, 1, maxIntakeLabelBytes) {
		return false
	}
	seen := make(map[string]bool, len(value.TrustedGitHubLogins))
	for _, login := range value.TrustedGitHubLogins {
		if !validGitHubLogin(login) || seen[strings.ToLower(login)] {
			return false
		}
		seen[strings.ToLower(login)] = true
	}
	return value.Policy != IntakePolicyTrustedAuthors || len(value.TrustedGitHubLogins) != 0
}

func validIntakeIssueSnapshot(value IntakeIssueSnapshot) bool {
	return value.GitHubRepositoryID != 0 && value.GitHubRepositoryID <= math.MaxInt64 && value.IssueNumber != 0 && value.IssueNumber <= math.MaxInt64 && validBoundedIntakeText(value.NodeID, 1, maxGitHubNodeIDBytes) && validBoundedIntakeText(value.Title, 1, maxIntakeTitleBytes) && validBoundedIntakeText(value.Body, 0, maxIntakeBodyBytes) && validGitHubLogin(value.AuthorLogin) && (value.AuthorType == GitHubAuthorUser || value.AuthorType == GitHubAuthorBot)
}

func intakeAcceptanceIDs(snapshot IntakeIssueSnapshot, projectID ProjectID, repositoryID RepositoryID) (IntakeAcceptanceID, TaskID, IncarnationID, error) {
	if snapshot.GitHubRepositoryID == 0 || snapshot.GitHubRepositoryID > math.MaxInt64 || snapshot.IssueNumber == 0 || snapshot.IssueNumber > math.MaxInt64 || !validBoundedIntakeText(snapshot.NodeID, 1, maxGitHubNodeIDBytes) || !validBoundedIntakeText(snapshot.Title, 1, maxIntakeTitleBytes) || !validBoundedIntakeText(snapshot.Body, 0, maxIntakeBodyBytes) || projectID.zero() || repositoryID.zero() {
		return IntakeAcceptanceID{}, TaskID{}, IncarnationID{}, ErrInvalidValue
	}
	identity := fmt.Sprintf("github-intake-v1\x00%d\x00%d\x00%s\x00%s\x00%s\x00%x", snapshot.GitHubRepositoryID, snapshot.IssueNumber, snapshot.NodeID, projectID.String(), repositoryID.String(), snapshot.ContentHash())
	acceptance, err := IntakeAcceptanceIDFromBytes(intakeID("acceptance", identity))
	if err != nil {
		return IntakeAcceptanceID{}, TaskID{}, IncarnationID{}, err
	}
	task, err := TaskIDFromBytes(intakeID("task", identity))
	if err != nil {
		return IntakeAcceptanceID{}, TaskID{}, IncarnationID{}, err
	}
	incarnation, err := IncarnationIDFromBytes(intakeID("incarnation", identity))
	if err != nil {
		return IntakeAcceptanceID{}, TaskID{}, IncarnationID{}, err
	}
	return acceptance, task, incarnation, nil
}

func intakeID(kind, identity string) []byte {
	sum := sha256.Sum256([]byte(kind + "\x00" + identity))
	value := append([]byte(nil), sum[:IDBytes]...)
	if allZero(value) {
		value[0] = 1
	}
	return value
}

func allZero(value []byte) bool {
	for _, part := range value {
		if part != 0 {
			return false
		}
	}
	return true
}

func sameIntakeIssue(left, right IntakeIssueSnapshot) bool {
	return left.GitHubRepositoryID == right.GitHubRepositoryID && left.IssueNumber == right.IssueNumber && left.NodeID == right.NodeID
}

func trustedGitHubLogin(logins []string, login string) bool {
	for _, candidate := range logins {
		if strings.EqualFold(candidate, login) {
			return true
		}
	}
	return false
}

func validGitHubRepositoryName(value string) bool {
	if !validBoundedIntakeText(value, 3, maxGitHubRepositoryNameBytes) || strings.Count(value, "/") != 1 {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || len(part) > 100 {
			return false
		}
		for _, r := range part {
			if !('a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || r == '.' || r == '_' || r == '-') {
				return false
			}
		}
	}
	return true
}

func validGitHubLogin(value string) bool {
	if !validBoundedIntakeText(value, 1, maxGitHubLoginBytes) {
		return false
	}
	for _, r := range value {
		if !('a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

func validBoundedIntakeText(value string, low, high int) bool {
	return byteLen(value) >= low && byteLen(value) <= high && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}
