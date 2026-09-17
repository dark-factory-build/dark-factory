package kernel

// Optional outcomes deliberately store only a bounded, typed document and
// immutable references. Task/run results remain authoritative in their own
// tables; this package never copies or changes them.
import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

const (
	OutcomeDocumentLimit = 32 * 1024
	OutcomeLinkLimit     = 32
)

type OutcomeID struct{ identifier }

func OutcomeIDFromBytes(value []byte) (OutcomeID, error) {
	id, err := identifierFromBytes(value)
	return OutcomeID{id}, err
}
func (id OutcomeID) MarshalText() ([]byte, error) { return []byte(id.String()), nil }

type OutcomeLink struct {
	Relation         string `json:"relation"`
	Stage            string `json:"stage,omitempty"`
	TaskID           string `json:"task_id,omitempty"`
	TaskWorkRevision uint64 `json:"task_work_revision,omitempty"`
	RunID            string `json:"run_id,omitempty"`
	ChangeID         string `json:"change_id,omitempty"`
	ContentID        string `json:"content_id,omitempty"`
	ContentRevision  uint64 `json:"content_revision,omitempty"`
	EvidenceID       string `json:"evidence_id,omitempty"`
	Source           string `json:"source,omitempty"`
	Environment      string `json:"environment,omitempty"`
}

type ComparisonCandidate struct {
	TaskID             string `json:"task_id"`
	TaskWorkRevision   uint64 `json:"task_work_revision"`
	RunID              string `json:"run_id,omitempty"`
	ChangeID           string `json:"change_id,omitempty"`
	Source             string `json:"source,omitempty"`
	Environment        string `json:"environment,omitempty"`
	EvidenceID         string `json:"evidence_id,omitempty"`
	ScenarioEvidenceID string `json:"scenario_evidence_id,omitempty"`
	EvidenceKind       string `json:"evidence_kind,omitempty"`
}

type OutcomeDocument struct {
	Kind                    string                `json:"kind"` // outcome, comparison, or mission
	Objective               string                `json:"objective"`
	Criteria                string                `json:"criteria"`
	AnchorTaskID            string                `json:"anchor_task_id,omitempty"`
	AnchorWorkRevision      uint64                `json:"anchor_work_revision,omitempty"`
	SourceIssue             string                `json:"source_issue,omitempty"`
	Links                   []OutcomeLink         `json:"links,omitempty"`
	State                   string                `json:"state"` // open, proposed, accepted, reopened
	Decision                string                `json:"decision,omitempty"`
	SelectedCandidateTaskID string                `json:"selected_candidate_task_id,omitempty"`
	Reason                  string                `json:"reason,omitempty"`
	Conclusion              string                `json:"conclusion,omitempty"`
	RemainingWork           string                `json:"remaining_work,omitempty"`
	Evidence                []string              `json:"evidence,omitempty"`
	Judgment                string                `json:"judgment,omitempty"`
	Question                string                `json:"question,omitempty"`
	Baseline                *ComparisonCandidate  `json:"baseline,omitempty"`
	Candidates              []ComparisonCandidate `json:"candidates,omitempty"`
	MilestoneOf             string                `json:"milestone_of,omitempty"`
}

type OutcomeRevision struct {
	ID                               OutcomeID
	ProjectID                        ProjectID
	Revision                         Revision
	Document                         OutcomeDocument
	Kind, Objective, Criteria, State string
	Author                           string
	Authority                        string
	ObjectiveHash                    [32]byte
	ObjectiveWorkRevision            Revision
	CreatedAt                        UnixMillis
	Stale                            bool
	MissingReferences                []string
}

type NewOutcome struct {
	ID        OutcomeID
	ProjectID ProjectID
	Document  OutcomeDocument
}
type OutcomePage struct {
	Items      []OutcomeRevision
	NextOffset int
}

func outcomeObjectiveHash(title, body string, work Revision) [32]byte {
	return sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s", work.Int64(), title, body)))
}

func validateOutcomeDocument(document OutcomeDocument) error {
	if document.Kind != "outcome" && document.Kind != "comparison" && document.Kind != "mission" {
		return fmt.Errorf("%w: invalid outcome kind", ErrInvalidValue)
	}
	if document.State != "open" && document.State != "proposed" && document.State != "accepted" && document.State != "reopened" {
		return fmt.Errorf("%w: invalid outcome state", ErrInvalidValue)
	}
	if document.Objective == "" || document.Criteria == "" || !validOutcomeText(document.Objective, 8192) || !validOutcomeText(document.Criteria, 8192) || !validOutcomeText(document.Reason, 8192) || !validOutcomeText(document.Conclusion, 8192) || !validOutcomeText(document.RemainingWork, 8192) || !validOutcomeText(document.Judgment, 8192) || !validOutcomeText(document.Question, 8192) || !validOutcomeText(document.SourceIssue, 4096) {
		return fmt.Errorf("%w: invalid outcome text", ErrInvalidValue)
	}
	if (document.AnchorTaskID == "") == (document.SourceIssue == "") || document.AnchorTaskID != "" && document.AnchorWorkRevision == 0 || document.SourceIssue != "" && !validSourceIssue(document.SourceIssue) {
		return fmt.Errorf("%w: outcome requires exactly one anchor", ErrInvalidValue)
	}
	if len(document.Links) > OutcomeLinkLimit || len(document.Candidates) > OutcomeLinkLimit || len(document.Evidence) > OutcomeLinkLimit {
		return fmt.Errorf("%w: too many outcome references", ErrInvalidValue)
	}
	if document.Kind == "comparison" && (document.Question == "" || document.Baseline == nil || len(document.Candidates) == 0) {
		return fmt.Errorf("%w: comparison question, baseline, and candidates required", ErrInvalidValue)
	}
	if document.Kind == "comparison" && document.Decision != "" && document.Decision != "select" && document.Decision != "keep_baseline" && document.Decision != "inconclusive" {
		return fmt.Errorf("%w: invalid comparison decision", ErrInvalidValue)
	}
	if document.Decision == "select" && document.SelectedCandidateTaskID == "" || document.Decision != "select" && document.SelectedCandidateTaskID != "" {
		return fmt.Errorf("%w: invalid comparison selection", ErrInvalidValue)
	}
	if document.Kind != "comparison" && (document.Decision != "" || document.SelectedCandidateTaskID != "" || document.Question != "" || document.Baseline != nil || len(document.Candidates) != 0) {
		return fmt.Errorf("%w: comparison fields on non-comparison", ErrInvalidValue)
	}
	if document.State == "accepted" || document.State == "reopened" {
		if document.Reason == "" || !hasOutcomeEvidence(document) && !strings.HasPrefix(document.Judgment, "authorized judgment:") {
			return fmt.Errorf("%w: accepted or reopened outcome needs reason and evidence or authorized judgment", ErrInvalidValue)
		}
	}
	if document.State == "accepted" && document.Conclusion == "" {
		return fmt.Errorf("%w: accepted outcome needs conclusion", ErrInvalidValue)
	}
	if document.Kind == "comparison" && document.State == "accepted" && document.Decision == "" {
		return fmt.Errorf("%w: accepted comparison needs a decision", ErrInvalidValue)
	}
	if document.Kind != "mission" && document.MilestoneOf != "" {
		return fmt.Errorf("%w: milestone must be a mission", ErrInvalidValue)
	}
	encoded, err := json.Marshal(document)
	if err != nil || len(encoded) > OutcomeDocumentLimit {
		return fmt.Errorf("%w: outcome document too large", ErrInvalidValue)
	}
	for _, link := range document.Links {
		if link.Relation != "implementation" && link.Relation != "investigation" && link.Relation != "review" && link.Relation != "delivery" || link.Stage != "" && link.Stage != "implemented" && link.Stage != "reviewed" && link.Stage != "merged" && link.Stage != "deployed" || link.Stage != "" && link.EvidenceID == "" || link.TaskWorkRevision == 0 || !validID(link.TaskID) || link.ContentID != "" && (link.ContentRevision == 0 || !validID(link.ContentID)) || link.ContentID == "" && link.ContentRevision != 0 || link.RunID != "" && !validID(link.RunID) || link.ChangeID != "" && !validID(link.ChangeID) || link.EvidenceID != "" && !validID(link.EvidenceID) {
			return fmt.Errorf("%w: invalid outcome link", ErrInvalidValue)
		}
	}
	if document.Baseline != nil && !validCandidate(*document.Baseline) {
		return fmt.Errorf("%w: invalid baseline", ErrInvalidValue)
	}
	for _, candidate := range document.Candidates {
		if !validCandidate(candidate) {
			return fmt.Errorf("%w: invalid comparison candidate", ErrInvalidValue)
		}
	}
	for _, evidence := range document.Evidence {
		if !validID(evidence) {
			return fmt.Errorf("%w: invalid outcome evidence", ErrInvalidValue)
		}
	}
	return nil
}

func validOutcomeText(value string, maximum int) bool {
	return byteLen(value) <= maximum && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}
func validSourceIssue(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}
func validID(value string) bool { _, err := idBytes(value); return err == nil }
func idBytes(value string) ([]byte, error) {
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != IDBytes {
		return nil, fmt.Errorf("%w: invalid id", ErrInvalidValue)
	}
	return raw, nil
}
func validCandidate(candidate ComparisonCandidate) bool {
	return candidate.TaskWorkRevision != 0 && validID(candidate.TaskID) && validID(candidate.RunID) && validID(candidate.ScenarioEvidenceID) && candidate.Source != "" && candidate.Environment != "" && validOutcomeText(candidate.Source, 4096) && validOutcomeText(candidate.Environment, 4096) && (candidate.ChangeID == "" || validID(candidate.ChangeID)) && (candidate.EvidenceID == "" || validID(candidate.EvidenceID)) && (candidate.EvidenceKind == "measurement" || candidate.EvidenceKind == "subjective")
}
func hasOutcomeEvidence(document OutcomeDocument) bool {
	if len(document.Evidence) != 0 {
		return true
	}
	if document.Baseline != nil && document.Baseline.ScenarioEvidenceID != "" {
		return true
	}
	for _, candidate := range document.Candidates {
		if candidate.ScenarioEvidenceID != "" {
			return true
		}
	}
	return false
}

func (document OutcomeDocument) MarshalBounded() (string, error) {
	if err := validateOutcomeDocument(document); err != nil {
		return "", err
	}
	b, err := json.Marshal(document)
	if err != nil || len(b) > OutcomeDocumentLimit {
		return "", fmt.Errorf("%w: outcome document too large", ErrInvalidValue)
	}
	return string(b), nil
}

func DecodeOutcomeDocument(raw string) (OutcomeDocument, error) {
	if len(raw) == 0 || len(raw) > OutcomeDocumentLimit || strings.IndexByte(raw, 0) >= 0 {
		return OutcomeDocument{}, fmt.Errorf("%w: invalid outcome document", ErrInvalidValue)
	}
	var document OutcomeDocument
	if err := json.Unmarshal([]byte(raw), &document); err != nil {
		return OutcomeDocument{}, fmt.Errorf("%w: invalid outcome document", ErrInvalidValue)
	}
	if err := validateOutcomeDocument(document); err != nil {
		return OutcomeDocument{}, err
	}
	return document, nil
}

func (store *Store) WriteOutcome(ctx context.Context, spec NewOutcome, expected int64, at UnixMillis) (OutcomeRevision, error) {
	if spec.ID.zero() || spec.ProjectID.zero() || expected < 0 {
		return OutcomeRevision{}, fmt.Errorf("%w: invalid outcome write", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return OutcomeRevision{}, err
	}
	defer tx.Close()
	return writeOutcomeTx(ctx, tx, nil, "", "", spec, expected, at)
}

func (store *Store) WriteOutcomeForAttempt(ctx context.Context, digest AttemptDigest, spec NewOutcome, expected int64, at UnixMillis) (OutcomeRevision, error) {
	if spec.ID.zero() || spec.ProjectID.zero() || expected < 0 {
		return OutcomeRevision{}, fmt.Errorf("%w: invalid outcome write", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return OutcomeRevision{}, err
	}
	defer tx.Close()
	authority, err := authenticateAttempt(ctx, tx.connection, digest)
	if err != nil {
		return OutcomeRevision{}, tx.Rollback(err)
	}
	return writeOutcomeTx(ctx, tx, &authority, "", "", spec, expected, at)
}

// WriteOutcomeForBrowser records the paired browser as the human author. Its
// capability is reloaded in the outcome write transaction, never trusted from
// a browser session cache.
func (store *Store) WriteOutcomeForBrowser(ctx context.Context, clientID BrowserClientID, spec NewOutcome, expected int64, at UnixMillis) (OutcomeRevision, error) {
	if clientID.zero() || spec.ID.zero() || spec.ProjectID.zero() || expected < 0 {
		return OutcomeRevision{}, fmt.Errorf("%w: invalid browser outcome write", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return OutcomeRevision{}, err
	}
	defer tx.Close()
	client, found, err := browserClientByID(ctx, tx.connection, clientID)
	if err != nil {
		return OutcomeRevision{}, tx.Rollback(err)
	}
	if !found {
		return OutcomeRevision{}, tx.Rollback(ErrNotFound)
	}
	if client.RevokedAt != nil || !client.CapabilityMask.Has(BrowserCapabilityPrivateHumanRequestDetail) || !client.CapabilityMask.Has(BrowserCapabilityHumanActions) {
		return OutcomeRevision{}, tx.Rollback(ErrUnauthorized)
	}
	return writeOutcomeTx(ctx, tx, nil, "browser:"+client.ID.String(), "human", spec, expected, at)
}

func writeOutcomeTx(ctx context.Context, tx *writeTx, authority *AttemptAuthority, author, auth string, spec NewOutcome, expected int64, at UnixMillis) (OutcomeRevision, error) {
	if err := validateOutcomeDocument(spec.Document); err != nil {
		return OutcomeRevision{}, tx.Rollback(err)
	}
	if authority != nil && spec.ProjectID != authority.ProjectID {
		return OutcomeRevision{}, tx.Rollback(ErrUnauthorized)
	}
	raw, err := spec.Document.MarshalBounded()
	if err != nil {
		return OutcomeRevision{}, tx.Rollback(err)
	}
	if authority != nil && authority.Role == RoleWorker {
		if spec.Document.State != "proposed" {
			return OutcomeRevision{}, tx.Rollback(ErrUnauthorized)
		}
		if spec.Document.AnchorTaskID == "" || spec.Document.AnchorTaskID != authority.TaskID.String() || spec.Document.AnchorWorkRevision != uint64(authority.AdmittedTaskWorkRevision.Int64()) {
			return OutcomeRevision{}, tx.Rollback(ErrUnauthorized)
		}
	}
	if authority != nil && authority.Role != RoleWorker && authority.Role != RoleOrchestrator {
		return OutcomeRevision{}, tx.Rollback(ErrUnauthorized)
	}
	if err := outcomeProjectExists(ctx, tx.connection, spec.ProjectID); err != nil {
		return OutcomeRevision{}, tx.Rollback(err)
	}
	var foreignProject []byte
	err = tx.connection.QueryRowContext(ctx, "SELECT project_id FROM project_outcome_revisions WHERE id = ? LIMIT 1", spec.ID.Bytes()).Scan(&foreignProject)
	if err != nil && err != sql.ErrNoRows {
		return OutcomeRevision{}, tx.Rollback(err)
	}
	if err == nil && string(foreignProject) != string(spec.ProjectID.Bytes()) {
		return OutcomeRevision{}, tx.Rollback(ErrConflict)
	}
	if author == "" {
		author, auth = "operator:local", "operator"
	}
	if authority != nil {
		author, auth = contentProvenance(*authority), authority.Role.String()
	}
	var current sql.NullInt64
	err = tx.connection.QueryRowContext(ctx, "SELECT MAX(revision) FROM project_outcome_revisions WHERE id = ? AND project_id = ?", spec.ID.Bytes(), spec.ProjectID.Bytes()).Scan(&current)
	if err != nil {
		return OutcomeRevision{}, tx.Rollback(err)
	}
	actual := int64(0)
	if current.Valid {
		actual = current.Int64
	}
	if expected != actual {
		found, replayErr := outcomeOnConnection(ctx, tx.connection, spec.ProjectID, spec.ID, expected+1)
		if replayErr == nil && found.Author == author && found.Authority == auth {
			foundRaw, marshalErr := found.Document.MarshalBounded()
			if marshalErr != nil {
				return OutcomeRevision{}, tx.Rollback(marshalErr)
			}
			if foundRaw == raw {
				if err := tx.Rollback(nil); err != nil {
					return OutcomeRevision{}, err
				}
				return found, nil
			}
		} else if replayErr != nil && replayErr != ErrNotFound {
			return OutcomeRevision{}, tx.Rollback(replayErr)
		}
		return OutcomeRevision{}, tx.Rollback(ErrConflict)
	}
	var old OutcomeRevision
	if expected > 0 {
		old, err = outcomeOnConnection(ctx, tx.connection, spec.ProjectID, spec.ID, expected)
		if err != nil {
			return OutcomeRevision{}, tx.Rollback(err)
		}
		if outcomeObjectiveChanged(old.Document, spec.Document) && spec.Document.State == "accepted" {
			return OutcomeRevision{}, tx.Rollback(fmt.Errorf("%w: objective edit must reopen acceptance", ErrConflict))
		}
		if authority != nil && authority.Role == RoleWorker && outcomeObjectiveChanged(old.Document, spec.Document) {
			return OutcomeRevision{}, tx.Rollback(ErrUnauthorized)
		}
	}
	var title, body string
	work := int64(1)
	if spec.Document.AnchorTaskID != "" {
		id, e := idFromString(spec.Document.AnchorTaskID)
		if e != nil {
			return OutcomeRevision{}, tx.Rollback(e)
		}
		var anchorProject []byte
		if err := tx.connection.QueryRowContext(ctx, "SELECT title, body, work_revision, project_id FROM tasks WHERE id = ?", id.Bytes()).Scan(&title, &body, &work, &anchorProject); err != nil {
			if err == sql.ErrNoRows {
				err = ErrNotFound
			}
			return OutcomeRevision{}, tx.Rollback(err)
		}
		if string(anchorProject) != string(spec.ProjectID.Bytes()) {
			return OutcomeRevision{}, tx.Rollback(ErrUnauthorized)
		}
		if int64(spec.Document.AnchorWorkRevision) != work {
			return OutcomeRevision{}, tx.Rollback(ErrConflict)
		}
	}
	hash := sourceOutcomeHash(spec.Document.Objective, spec.Document.Criteria)
	if spec.Document.AnchorTaskID != "" {
		hash = outcomeObjectiveHash(title, body, mustRevisionValue(work))
	}
	if err := validateOutcomeReferences(ctx, tx.connection, spec.ProjectID, spec.ID, spec.Document); err != nil {
		return OutcomeRevision{}, tx.Rollback(err)
	}
	next := actual + 1
	if _, err = tx.connection.ExecContext(ctx, `INSERT INTO project_outcome_revisions(id, project_id, revision, document, author, authority, objective_hash, objective_work_revision, created_at_ms) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`, spec.ID.Bytes(), spec.ProjectID.Bytes(), next, raw, author, auth, hash[:], work, at.Int64()); err != nil {
		return OutcomeRevision{}, tx.Rollback(err)
	}
	result, err := outcomeOnConnection(ctx, tx.connection, spec.ProjectID, spec.ID, next)
	if err != nil {
		return OutcomeRevision{}, tx.Rollback(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return OutcomeRevision{}, err
	}
	return result, nil
}

func outcomeObjectiveChanged(before, after OutcomeDocument) bool {
	return before.Objective != after.Objective || before.Criteria != after.Criteria || before.AnchorTaskID != after.AnchorTaskID || before.AnchorWorkRevision != after.AnchorWorkRevision || before.SourceIssue != after.SourceIssue
}

func mustRevisionValue(v int64) Revision { r, _ := NewRevision(v); return r }
func sourceOutcomeHash(objective, criteria string) [32]byte {
	return sha256.Sum256([]byte("source\x001\x00" + objective + "\x00" + criteria))
}
func idFromString(s string) (TaskID, error) {
	b, err := idBytes(s)
	if err != nil {
		return TaskID{}, err
	}
	return TaskIDFromBytes(b)
}

func outcomeProjectExists(ctx context.Context, connection *sql.Conn, project ProjectID) error {
	var exists int
	if err := connection.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM projects WHERE id = ?)", project.Bytes()).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return ErrNotFound
	}
	return nil
}

// validateOutcomeReferences checks every typed locator in the same write
// transaction as the immutable revision.  A link has no scheduling effect.
func validateOutcomeReferences(ctx context.Context, connection *sql.Conn, project ProjectID, outcome OutcomeID, document OutcomeDocument) error {
	for _, link := range document.Links {
		if err := validateLinkReference(ctx, connection, project, link); err != nil {
			return err
		}
	}
	if document.Baseline != nil {
		if err := validateCandidateReference(ctx, connection, project, *document.Baseline); err != nil {
			return err
		}
	}
	selected := false
	for _, candidate := range document.Candidates {
		if err := validateCandidateReference(ctx, connection, project, candidate); err != nil {
			return err
		}
		if candidate.TaskID == document.SelectedCandidateTaskID {
			selected = true
		}
	}
	if document.SelectedCandidateTaskID != "" && !selected {
		return fmt.Errorf("%w: selected candidate is absent", ErrInvalidValue)
	}
	for _, evidence := range document.Evidence {
		if err := validateEvidenceReference(ctx, connection, project, evidence); err != nil {
			return err
		}
	}
	if document.MilestoneOf != "" {
		raw, err := idBytes(document.MilestoneOf)
		if err != nil {
			return err
		}
		parentID, err := OutcomeIDFromBytes(raw)
		if err != nil {
			return err
		}
		parent, err := outcomeOnConnection(ctx, connection, project, parentID, 0)
		if err != nil {
			return err
		}
		if parentID == outcome || parent.Document.Kind != "mission" {
			return ErrConflict
		}
	}
	return nil
}
func validateLinkReference(ctx context.Context, connection *sql.Conn, project ProjectID, link OutcomeLink) error {
	taskID, err := TaskIDFromBytes(mustOutcomeID(link.TaskID))
	if err != nil {
		return err
	}
	task, found, err := taskByID(ctx, connection, taskID)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	if task.ProjectID != project || task.WorkRevision.Int64() != int64(link.TaskWorkRevision) {
		return ErrUnauthorized
	}
	if link.RunID != "" {
		if err := validateRunReference(ctx, connection, project, taskID, link.TaskWorkRevision, link.RunID); err != nil {
			return err
		}
	}
	if link.ChangeID != "" {
		if err := validateChangeReference(ctx, connection, project, taskID, link.ChangeID); err != nil {
			return err
		}
	}
	if link.EvidenceID != "" {
		if err := validateEvidenceReference(ctx, connection, project, link.EvidenceID); err != nil {
			return err
		}
	}
	if link.ContentID == "" {
		return nil
	}
	var contentProject []byte
	err = connection.QueryRowContext(ctx, "SELECT project_id FROM project_content_revisions WHERE id = ? AND revision = ?", mustOutcomeID(link.ContentID), link.ContentRevision).Scan(&contentProject)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if string(contentProject) != string(project.Bytes()) {
		return ErrUnauthorized
	}
	return nil
}
func validateCandidateReference(ctx context.Context, connection *sql.Conn, project ProjectID, candidate ComparisonCandidate) error {
	taskID, err := TaskIDFromBytes(mustOutcomeID(candidate.TaskID))
	if err != nil {
		return err
	}
	task, found, err := taskByID(ctx, connection, taskID)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	if task.ProjectID != project {
		return ErrUnauthorized
	}
	// A captured work revision must be one that actually existed. Tasks retain
	// only the current body, so a later edit makes the old locator stale rather
	// than silently retargeting it.
	if task.WorkRevision.Int64() != int64(candidate.TaskWorkRevision) {
		return ErrConflict
	}
	if candidate.RunID != "" {
		if err := validateRunReference(ctx, connection, project, taskID, candidate.TaskWorkRevision, candidate.RunID); err != nil {
			return err
		}
	}
	if candidate.ChangeID != "" {
		if err := validateChangeReference(ctx, connection, project, taskID, candidate.ChangeID); err != nil {
			return err
		}
	}
	if candidate.EvidenceID != "" {
		if err := validateEvidenceReference(ctx, connection, project, candidate.EvidenceID); err != nil {
			return err
		}
	}
	if candidate.ScenarioEvidenceID != "" {
		if err := validateScenarioEvidenceReference(ctx, connection, project, candidate.ScenarioEvidenceID); err != nil {
			return err
		}
	}
	return nil
}
func validateRunReference(ctx context.Context, connection *sql.Conn, project ProjectID, taskID TaskID, work uint64, value string) error {
	runID, err := RunIDFromBytes(mustOutcomeID(value))
	if err != nil {
		return err
	}
	run, found, err := runByID(ctx, connection, runID)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	if run.ProjectID != project || run.TaskID != taskID || run.AdmittedTaskWorkRevision.Int64() != int64(work) {
		return ErrUnauthorized
	}
	return nil
}
func validateChangeReference(ctx context.Context, connection *sql.Conn, project ProjectID, taskID TaskID, value string) error {
	changeID, err := ChangeIDFromBytes(mustOutcomeID(value))
	if err != nil {
		return err
	}
	change, found, err := changeByID(ctx, connection, changeID)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	if change.ProjectID != project || change.TaskID != taskID {
		return ErrUnauthorized
	}
	return nil
}
func validateEvidenceReference(ctx context.Context, connection *sql.Conn, project ProjectID, value string) error {
	var evidenceProject []byte
	err := connection.QueryRowContext(ctx, "SELECT project_id FROM project_content_evidence WHERE id = ?", mustOutcomeID(value)).Scan(&evidenceProject)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if string(evidenceProject) != string(project.Bytes()) {
		return ErrUnauthorized
	}
	return nil
}
func validateScenarioEvidenceReference(ctx context.Context, connection *sql.Conn, project ProjectID, value string) error {
	var evidenceProject []byte
	var kind string
	err := connection.QueryRowContext(ctx, `SELECT e.project_id, c.kind FROM project_content_evidence e JOIN project_content_revisions c ON c.id = e.content_id AND c.revision = e.content_revision WHERE e.id = ?`, mustOutcomeID(value)).Scan(&evidenceProject, &kind)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if string(evidenceProject) != string(project.Bytes()) {
		return ErrUnauthorized
	}
	if ContentKind(kind) != ContentAcceptanceScenario {
		return fmt.Errorf("%w: comparison scenario evidence is not an acceptance scenario", ErrConflict)
	}
	return nil
}
func mustOutcomeID(value string) []byte { raw, _ := idBytes(value); return raw }

func (store *Store) Outcome(ctx context.Context, project ProjectID, id OutcomeID, revision int64) (OutcomeRevision, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return OutcomeRevision{}, err
	}
	defer tx.Close()
	return outcomeOnConnection(ctx, tx.connection, project, id, revision)
}
func (store *Store) OutcomeForAttempt(ctx context.Context, digest AttemptDigest, id OutcomeID, revision int64) (OutcomeRevision, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return OutcomeRevision{}, err
	}
	defer tx.Close()
	a, err := authenticateAttempt(ctx, tx.connection, digest)
	if err != nil {
		return OutcomeRevision{}, err
	}
	return outcomeOnConnection(ctx, tx.connection, a.ProjectID, id, revision)
}
func (store *Store) ListOutcomes(ctx context.Context, project ProjectID, offset, limit int) (OutcomePage, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return OutcomePage{}, err
	}
	defer tx.Close()
	return listOutcomesOnConnection(ctx, tx.connection, project, offset, limit)
}
func (store *Store) ListOutcomesForAttempt(ctx context.Context, digest AttemptDigest, offset, limit int) (OutcomePage, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return OutcomePage{}, err
	}
	defer tx.Close()
	a, err := authenticateAttempt(ctx, tx.connection, digest)
	if err != nil {
		return OutcomePage{}, err
	}
	return listOutcomesOnConnection(ctx, tx.connection, a.ProjectID, offset, limit)
}

func outcomeOnConnection(ctx context.Context, c *sql.Conn, project ProjectID, id OutcomeID, revision int64) (OutcomeRevision, error) {
	if revision == 0 {
		var latest sql.NullInt64
		if err := c.QueryRowContext(ctx, "SELECT MAX(revision) FROM project_outcome_revisions WHERE project_id = ? AND id = ?", project.Bytes(), id.Bytes()).Scan(&latest); err != nil {
			return OutcomeRevision{}, err
		}
		if !latest.Valid {
			return OutcomeRevision{}, ErrNotFound
		}
		revision = latest.Int64
	}
	var raw, author, authority string
	var p, hash []byte
	var rev, work, created int64
	if err := c.QueryRowContext(ctx, "SELECT project_id, revision, document, author, authority, objective_hash, objective_work_revision, created_at_ms FROM project_outcome_revisions WHERE project_id = ? AND id = ? AND revision = ?", project.Bytes(), id.Bytes(), revision).Scan(&p, &rev, &raw, &author, &authority, &hash, &work, &created); err != nil {
		if err == sql.ErrNoRows {
			err = ErrNotFound
		}
		return OutcomeRevision{}, err
	}
	doc, err := DecodeOutcomeDocument(raw)
	if err != nil {
		return OutcomeRevision{}, err
	}
	pid, err := ProjectIDFromBytes(p)
	if err != nil {
		return OutcomeRevision{}, err
	}
	rr, _ := NewRevision(rev)
	wr, _ := NewRevision(work)
	at, _ := NewUnixMillis(created)
	result := OutcomeRevision{ID: id, ProjectID: pid, Revision: rr, Document: doc, Kind: doc.Kind, Objective: doc.Objective, Criteria: doc.Criteria, State: doc.State, Author: author, Authority: authority, ObjectiveWorkRevision: wr, CreatedAt: at}
	copy(result.ObjectiveHash[:], hash)
	result.Stale, result.MissingReferences = outcomeReadFlags(ctx, c, project, doc, hash, work)
	return result, nil
}

// Reads retain history even when a linked row later changes or is removed.
// Missing links are reported to callers instead of making the outcome vanish.
func outcomeReadFlags(ctx context.Context, connection *sql.Conn, project ProjectID, document OutcomeDocument, storedHash []byte, storedWork int64) (bool, []string) {
	var missing []string
	stale := false
	if document.AnchorTaskID != "" {
		taskID, err := idFromString(document.AnchorTaskID)
		task, found, readErr := taskByID(ctx, connection, taskID)
		if err != nil || readErr != nil || !found || task.ProjectID != project {
			missing = append(missing, "anchor_task")
		} else if task.WorkRevision.Int64() != storedWork || outcomeObjectiveHash(task.Title, task.Body, task.WorkRevision) != hashArray(storedHash) {
			stale = true
		}
	}
	for _, link := range document.Links {
		if err := validateLinkReference(ctx, connection, project, link); err != nil {
			missing = append(missing, "link:"+link.TaskID)
		}
	}
	for _, evidence := range document.Evidence {
		if err := validateEvidenceReference(ctx, connection, project, evidence); err != nil {
			missing = append(missing, "evidence:"+evidence)
		}
	}
	if document.Baseline != nil {
		if err := validateCandidateReference(ctx, connection, project, *document.Baseline); err != nil {
			missing = append(missing, "baseline:"+document.Baseline.TaskID)
		}
	}
	for _, candidate := range document.Candidates {
		if err := validateCandidateReference(ctx, connection, project, candidate); err != nil {
			missing = append(missing, "candidate:"+candidate.TaskID)
		}
	}
	return stale, missing
}
func hashArray(value []byte) (result [32]byte) { copy(result[:], value); return result }
func listOutcomesOnConnection(ctx context.Context, c *sql.Conn, project ProjectID, offset, limit int) (OutcomePage, error) {
	if offset < 0 || limit < 0 || limit > 16 {
		return OutcomePage{}, fmt.Errorf("%w: invalid outcome page", ErrInvalidValue)
	}
	if limit == 0 {
		limit = 16
	}
	rows, err := c.QueryContext(ctx, "SELECT id, MAX(revision) FROM project_outcome_revisions WHERE project_id = ? GROUP BY id ORDER BY id LIMIT ? OFFSET ?", project.Bytes(), limit+1, offset)
	if err != nil {
		return OutcomePage{}, err
	}
	defer rows.Close()
	page := OutcomePage{}
	for rows.Next() {
		var raw []byte
		var rev int64
		if err := rows.Scan(&raw, &rev); err != nil {
			return OutcomePage{}, err
		}
		id, err := OutcomeIDFromBytes(raw)
		if err != nil {
			return OutcomePage{}, err
		}
		value, err := outcomeOnConnection(ctx, c, project, id, rev)
		if err != nil {
			return OutcomePage{}, err
		}
		value.Document = OutcomeDocument{}
		if len(page.Items) == limit {
			page.NextOffset = offset + limit
			break
		}
		page.Items = append(page.Items, value)
	}
	return page, rows.Err()
}
