package kernel

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

type ContinuationCondition string

const (
	ConditionHumanRequest ContinuationCondition = "human_request"
	ConditionPeerQuestion ContinuationCondition = "peer_question"
	ConditionHandoff      ContinuationCondition = "handoff"
	ConditionDependency   ContinuationCondition = "dependency"
	ConditionInvalidation ContinuationCondition = "invalidation"
)

type ContinuationState string

type ContinuationConditionID [IDBytes]byte

func (id ContinuationConditionID) zero() bool { var empty ContinuationConditionID; return id == empty }
func (id ContinuationConditionID) Bytes() []byte {
	value := make([]byte, IDBytes)
	copy(value, id[:])
	return value
}

const (
	ContinuationWaiting   ContinuationState = "waiting"
	ContinuationQueued    ContinuationState = "queued"
	ContinuationResolved  ContinuationState = "resolved"
	ContinuationCancelled ContinuationState = "cancelled"
)

type NewContinuation struct {
	ID                ContinuationID
	ProjectID         ProjectID
	TaskID            TaskID
	TaskIncarnationID IncarnationID
	WorkRevision      Revision
	ContextDigest     [DigestBytes]byte
	ConditionKind     ContinuationCondition
	ConditionID       ContinuationConditionID
	ConditionRevision Revision
}

type Continuation struct {
	NewContinuation
	State            ContinuationState
	Revision         Revision
	CreatedAt        UnixMillis
	UpdatedAt        UnixMillis
	ResolutionDetail string
	ResolvedAt       *UnixMillis
}

// ContinuationContext is the causal result of an awaited condition. It is
// copied onto a fresh admission only for the immediately resumed task work
// revision; the durable continuation remains authoritative across restart.
type ContinuationContext struct {
	ContextDigest     [DigestBytes]byte
	ConditionKind     ContinuationCondition
	ConditionID       ContinuationConditionID
	ConditionRevision Revision
	ResolutionDetail  string
	ResolvedAt        UnixMillis
}

// MaxContinuationTaskBytes bounds the private attempt-task envelope. It is
// deliberately larger than either provider's inline task limit so an exact
// provider-limit task can still be returned together with its causal context.
// Reserve 3 KiB of selected knowledge and 4 KiB of explicit revision pins.
// Provider launch bounds stay unchanged; coding providers fetch this envelope.
const MaxContinuationTaskBytes = (128 << 10) + MaxHumanRequestReplyBytes + 2048 + (7 << 10)

func continuationTaskTextFull(task string, contexts []ContinuationContext) string {
	var builder strings.Builder
	builder.WriteString(task)
	builder.WriteString("\n\nFactory continuation context:\n")
	for _, context := range contexts {
		builder.WriteString("condition=")
		builder.WriteString(string(context.ConditionKind))
		builder.WriteString(" condition_id=")
		builder.WriteString(hex.EncodeToString(context.ConditionID.Bytes()))
		builder.WriteString(" condition_revision=")
		builder.WriteString(strconv.FormatInt(context.ConditionRevision.Int64(), 10))
		builder.WriteString(" context_digest=")
		builder.WriteString(hex.EncodeToString(context.ContextDigest[:]))
		builder.WriteString(" resolution=")
		builder.WriteString(context.ResolutionDetail)
		builder.WriteByte('\n')
	}
	return builder.String()
}

// ContinuationTaskFits reports whether a fresh provider can receive the full
// causal envelope without dropping any task or condition data. Admission uses
// this before consuming a queued task; an unfit resumed task is refused.
func ContinuationTaskFits(provider Provider, task string, contexts []ContinuationContext) bool {
	if len(contexts) == 0 {
		return true
	}
	limit := 131072
	if provider == ProviderCodex {
		limit = 8192
	}
	if len(task) >= limit {
		return false
	}
	return len(continuationTaskTextFull(task, contexts)) <= limit
}

// ContinuationTaskCanUseFetchFallback reports whether admission can preserve
// progress when the causal envelope cannot fit inline. The daemon then starts
// a fresh provider authority with a bounded instruction to fetch the durable
// task and continuation context; no causal field is discarded.
func ContinuationTaskCanUseFetchFallback(provider Provider, contexts []ContinuationContext) bool {
	if len(contexts) == 0 {
		return false
	}
	return provider == ProviderClaudeCode || provider == ProviderCodex
}

func validateContinuationSpec(spec NewContinuation) error {
	if spec.ID.zero() || spec.ProjectID.zero() || spec.TaskID.zero() || spec.TaskIncarnationID.zero() || spec.ConditionID.zero() ||
		spec.WorkRevision.Int64() < 1 || spec.ConditionRevision.Int64() < 1 || spec.ConditionKind == "" ||
		isZeroDigest(spec.ContextDigest) {
		return fmt.Errorf("%w: invalid continuation", ErrInvalidValue)
	}
	if !validContinuationCondition(spec.ConditionKind) {
		return fmt.Errorf("%w: invalid continuation condition", ErrInvalidValue)
	}
	return nil
}

func validContinuationCondition(kind ContinuationCondition) bool {
	switch kind {
	case ConditionHumanRequest, ConditionPeerQuestion, ConditionHandoff, ConditionDependency, ConditionInvalidation:
		return true
	default:
		return false
	}
}

func isZeroDigest(value [DigestBytes]byte) bool {
	for _, b := range value {
		if b != 0 {
			return false
		}
	}
	return true
}

// yieldContinuationOnConnection is the shared atomic portion of yielding. The
// caller owns tx and decides when the complete question/yield transaction is
// committed.
func (store *Store) yieldContinuationOnConnection(ctx context.Context, tx *writeTx, run Run, kind ContinuationCondition, conditionID ContinuationConditionID, conditionRevision Revision, at UnixMillis) (Continuation, error) {
	if run.Phase != RunRunning || run.CredentialRevokedAt != nil {
		return Continuation{}, tx.Rollback(ErrUnauthorized)
	}
	task, found, err := taskByID(ctx, tx.connection, run.TaskID)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return Continuation{}, tx.Rollback(err)
	}
	if task.ProjectID != run.ProjectID || task.IncarnationID != run.TaskIncarnationID || task.WorkRevision != run.AdmittedTaskWorkRevision || task.Status != TaskRunning {
		return Continuation{}, tx.Rollback(ErrRevisionConflict)
	}
	var existing Continuation
	existing, found, err = continuationByCondition(ctx, tx.connection, task.ID, task.IncarnationID, task.WorkRevision, kind, conditionID)
	if err != nil {
		return Continuation{}, tx.Rollback(err)
	}
	if found {
		return existing, nil
	}
	id, err := insertWaitingContinuation(ctx, tx.connection, task, kind, conditionID, conditionRevision, at)
	if err != nil {
		return Continuation{}, tx.Rollback(err)
	}
	proposal, err := NewCancelledProposal("yielded awaiting " + string(kind))
	if err != nil {
		return Continuation{}, tx.Rollback(err)
	}
	if _, err := store.enterFinalizingInTransaction(ctx, tx, run, run.Revision, proposal, at, nil, &conditionID); err != nil {
		return Continuation{}, err
	}
	value, found, err := continuationByID(ctx, tx.connection, id)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return Continuation{}, tx.Rollback(err)
	}
	return value, nil
}

// insertWaitingContinuation records that task's current work revision waits
// on condition.
func insertWaitingContinuation(ctx context.Context, connection *sql.Conn, task Task, kind ContinuationCondition, conditionID ContinuationConditionID, conditionRevision Revision, at UnixMillis) (ContinuationID, error) {
	var raw [IDBytes]byte
	if _, err := rand.Read(raw[:]); err != nil || raw == ([IDBytes]byte{}) {
		if err == nil {
			err = fmt.Errorf("%w: generated zero continuation identifier", ErrCorruptState)
		}
		return ContinuationID{}, err
	}
	id, err := ContinuationIDFromBytes(raw[:])
	if err != nil {
		return ContinuationID{}, err
	}
	contextDigest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", task.ID.String(), task.WorkRevision.Int64())))
	spec := NewContinuation{ID: id, ProjectID: task.ProjectID, TaskID: task.ID, TaskIncarnationID: task.IncarnationID, WorkRevision: task.WorkRevision, ContextDigest: contextDigest, ConditionKind: kind, ConditionID: conditionID, ConditionRevision: conditionRevision}
	if err := validateContinuationSpec(spec); err != nil {
		return ContinuationID{}, err
	}
	if _, err := connection.ExecContext(ctx, `INSERT INTO continuations(id, project_id, task_id, task_incarnation_id, work_revision, context_digest, condition_kind, condition_id, condition_revision, state, revision, created_at_ms, updated_at_ms) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, 'waiting', 1, ?, ?)`, id.Bytes(), task.ProjectID.Bytes(), task.ID.Bytes(), task.IncarnationID.Bytes(), task.WorkRevision.Int64(), contextDigest[:], string(kind), conditionID.Bytes(), conditionRevision.Int64(), at.Int64(), at.Int64()); err != nil {
		return ContinuationID{}, err
	}
	return id, appendInvalidations(ctx, connection, at, []pendingInvalidation{{kind: EntityContinuation, id: id.Bytes(), revision: 1}})
}

func continuationByCondition(ctx context.Context, connection *sql.Conn, taskID TaskID, incarnation IncarnationID, work Revision, kind ContinuationCondition, conditionID ContinuationConditionID) (Continuation, bool, error) {
	var raw []byte
	err := connection.QueryRowContext(ctx, `SELECT id FROM continuations WHERE task_id=? AND task_incarnation_id=? AND work_revision=? AND condition_kind=? AND condition_id=?`, taskID.Bytes(), incarnation.Bytes(), work.Int64(), string(kind), conditionID.Bytes()).Scan(&raw)
	if err == sql.ErrNoRows {
		return Continuation{}, false, nil
	}
	if err != nil {
		return Continuation{}, false, err
	}
	id, err := ContinuationIDFromBytes(raw)
	if err != nil {
		return Continuation{}, false, err
	}
	return continuationByID(ctx, connection, id)
}

// PromoteQueuedContinuations is the restart-safe admission edge. Resolution
// may race the old run's final cleanup; promotion therefore checks the
// terminal task/run topology in the same write transaction and can be retried
// by the scheduler without polling a provider or replaying input.
func (store *Store) PromoteQueuedContinuations(ctx context.Context, at UnixMillis) ([]Task, error) {
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Close()
	rows, err := tx.connection.QueryContext(ctx, `SELECT id FROM continuations WHERE state='queued' ORDER BY updated_at_ms, id`)
	if err != nil {
		return nil, tx.Rollback(err)
	}
	var ids []ContinuationID
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, tx.Rollback(err)
		}
		id, err := ContinuationIDFromBytes(raw)
		if err != nil {
			rows.Close()
			return nil, tx.Rollback(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, tx.Rollback(err)
	}
	var tasks []Task
	for _, id := range ids {
		task, err := promoteContinuationOnConnection(ctx, tx.connection, id, at)
		if err != nil {
			return nil, tx.Rollback(err)
		}
		if task != nil {
			tasks = append(tasks, *task)
		}
	}
	if len(tasks) == 0 {
		return nil, tx.Rollback(nil)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return tasks, nil
}

// ResolveHumanContinuationForAttempt consumes a reply as a causal event for
// a yielded worker. It deliberately does not send the reply to the old
// provider; that bearer was revoked at yield. The next fresh run observes the
// durable continuation result through the task lifecycle.
func (store *Store) ResolveHumanContinuationForAttempt(ctx context.Context, digest AttemptDigest, requestID HumanRequestID, expected Revision, reply string, at UnixMillis) (bool, error) {
	if requestID.zero() || expected.Int64() < 1 || byteLen(reply) < 1 || byteLen(reply) > MaxHumanRequestReplyBytes {
		return false, fmt.Errorf("%w: invalid human continuation reply", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Close()
	actor, found, err := runByDigest(ctx, tx.connection, digest)
	if err != nil || !found {
		if err == nil {
			err = ErrUnauthorized
		}
		return false, tx.Rollback(err)
	}
	if actor.Role != RoleOrchestrator || actor.Phase != RunRunning || actor.CredentialRevokedAt != nil {
		return false, tx.Rollback(ErrUnauthorized)
	}
	return resolveHumanContinuation(ctx, tx, requestID, expected, actor.ProjectID, HumanRequestDeliveryID{}, reply, at)
}

// ResolveHumanContinuationForBrowser is the public human-action equivalent of
// ResolveHumanContinuationForAttempt. Browser authorization is the bearer;
// the yielded attempt credential is intentionally not revived or required.
func (store *Store) ResolveHumanContinuationForBrowser(ctx context.Context, clientID BrowserClientID, requestID HumanRequestID, expected Revision, reply string, at UnixMillis) (bool, error) {
	if clientID.zero() || requestID.zero() || expected.Int64() < 1 || byteLen(reply) < 1 || byteLen(reply) > MaxHumanRequestReplyBytes {
		return false, fmt.Errorf("%w: invalid browser human continuation reply", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Close()
	client, found, err := browserClientByID(ctx, tx.connection, clientID)
	if err != nil || !found || client.RevokedAt != nil || !client.CapabilityMask.Has(BrowserCapabilityHumanActions) {
		if err == nil {
			err = ErrUnauthorized
		}
		return false, tx.Rollback(err)
	}
	return resolveHumanContinuation(ctx, tx, requestID, expected, ProjectID{}, HumanRequestDeliveryID{}, reply, at)
}

// ResolveHumanContinuationForOperator consumes an operator-authorized reply
// without reviving or delivering to the yielded attempt.
func (store *Store) ResolveHumanContinuationForOperator(ctx context.Context, requestID HumanRequestID, expected Revision, deliveryID HumanRequestDeliveryID, reply string, at UnixMillis) (bool, error) {
	if requestID.zero() || deliveryID.zero() || expected.Int64() < 1 || byteLen(reply) < 1 || byteLen(reply) > MaxHumanRequestReplyBytes {
		return false, fmt.Errorf("%w: invalid operator human continuation reply", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Close()
	return resolveHumanContinuation(ctx, tx, requestID, expected, ProjectID{}, deliveryID, reply, at)
}

// resolveHumanContinuation resolves an open request's yielded continuation
// and commits tx. A nonzero project confines the request's run to it.
func resolveHumanContinuation(ctx context.Context, tx *writeTx, requestID HumanRequestID, expected Revision, project ProjectID, deliveryID HumanRequestDeliveryID, reply string, at UnixMillis) (bool, error) {
	request, found, err := humanRequestByID(ctx, tx.connection, requestID)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return false, tx.Rollback(err)
	}
	if request.Revision != expected || request.Status != HumanRequestOpen {
		return false, tx.Rollback(ErrRevisionConflict)
	}
	if !project.zero() && request.IdempotencyKey == stalledItemKey {
		return false, tx.Rollback(ErrUnauthorized)
	}
	target, found, err := runByID(ctx, tx.connection, request.RunID)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return false, tx.Rollback(err)
	}
	if !project.zero() && target.ProjectID != project || target.Phase != RunTerminal {
		return false, tx.Rollback(ErrConflict)
	}
	var conditionID ContinuationConditionID
	copy(conditionID[:], request.ID.Bytes())
	continuation, found, err := continuationByCondition(ctx, tx.connection, target.TaskID, target.TaskIncarnationID, target.AdmittedTaskWorkRevision, ConditionHumanRequest, conditionID)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return false, tx.Rollback(err)
	}
	if continuation.State != ContinuationWaiting && continuation.State != ContinuationQueued {
		return false, tx.Rollback(ErrRevisionConflict)
	}
	if err := resolveHumanContinuationOnConnection(ctx, tx, request, continuation, deliveryID, reply, at); err != nil {
		return false, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func resolveHumanContinuationOnConnection(ctx context.Context, tx *writeTx, request HumanRequest, continuation Continuation, deliveryID HumanRequestDeliveryID, reply string, at UnixMillis) error {
	if request.IdempotencyKey == stalledItemKey {
		// The resumed carrier sees what the operator answered, as far as the
		// bound on a resolution leaves room beside the whole reply.
		const prefix = "Operator reply to: "
		if room := MaxHumanRequestReplyBytes - len(prefix) - 1 - len(reply); room > 0 {
			reply = prefix + strings.ToValidUTF8(request.QuestionText[:min(len(request.QuestionText), room)], "") + "\n" + reply
		}
	}
	if deliveryID.zero() {
		var err error
		deliveryID, err = HumanRequestDeliveryIDFromBytes(continuation.ID.Bytes())
		if err != nil {
			return err
		}
	}
	var deliveryCollision int
	if err := tx.connection.QueryRowContext(ctx, `SELECT 1 FROM human_requests WHERE delivery_id = ?`, deliveryID.Bytes()).Scan(&deliveryCollision); err == nil {
		return ErrConflict
	} else if err != sql.ErrNoRows {
		return err
	}
	updated, err := tx.connection.ExecContext(ctx, `UPDATE human_requests SET status='resolved', delivery_id=?, delivery_started_at_ms=?, resolution_kind='reply', closed_at_ms=?, revision=revision+1, updated_at_ms=? WHERE id=? AND status='open' AND revision=?`, deliveryID.Bytes(), at.Int64(), at.Int64(), at.Int64(), request.ID.Bytes(), request.Revision.Int64())
	if err := requireOneRow(updated, err); err != nil {
		return err
	}
	if err := appendInvalidations(ctx, tx.connection, at, []pendingInvalidation{{kind: EntityHumanRequest, id: request.ID.Bytes(), revision: request.Revision.Int64() + 1}}); err != nil {
		return err
	}
	if continuation.State == ContinuationWaiting {
		updated, err = tx.connection.ExecContext(ctx, `UPDATE continuations SET state='queued', resolution_detail=?, resolved_at_ms=?, revision=revision+1, updated_at_ms=? WHERE id=? AND state='waiting' AND revision=?`, reply, at.Int64(), at.Int64(), continuation.ID.Bytes(), continuation.Revision.Int64())
		if err := requireOneRow(updated, err); err != nil {
			return err
		}
		if err := appendInvalidations(ctx, tx.connection, at, []pendingInvalidation{{kind: EntityContinuation, id: continuation.ID.Bytes(), revision: continuation.Revision.Int64() + 1}}); err != nil {
			return err
		}
	}
	_, err = promoteContinuationOnConnection(ctx, tx.connection, continuation.ID, at)
	return err
}

func promoteContinuationOnConnection(ctx context.Context, connection *sql.Conn, id ContinuationID, at UnixMillis) (*Task, error) {
	current, found, err := continuationByID(ctx, connection, id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return nil, err
	}
	if current.State != ContinuationQueued {
		return nil, nil
	}
	var status string
	var revision int64
	if err := connection.QueryRowContext(ctx, `SELECT status, revision FROM tasks WHERE id=? AND project_id=? AND incarnation_id=? AND work_revision=?`, current.TaskID.Bytes(), current.ProjectID.Bytes(), current.TaskIncarnationID.Bytes(), current.WorkRevision.Int64()).Scan(&status, &revision); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrCorruptState
		}
		return nil, err
	}
	if status != TaskCancelled.String() && status != TaskFailed.String() && status != TaskBlocked.String() && status != TaskSucceeded.String() {
		return nil, nil
	}
	var terminal int
	if err := connection.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE task_id=? AND task_incarnation_id=? AND admitted_task_work_revision=? AND phase='terminal')`, current.TaskID.Bytes(), current.TaskIncarnationID.Bytes(), current.WorkRevision.Int64()).Scan(&terminal); err != nil {
		return nil, err
	}
	if terminal == 0 {
		return nil, nil
	}
	updated, err := connection.ExecContext(ctx, `UPDATE tasks SET status='queued', work_revision=work_revision+1, blocked_reason=NULL, result=NULL, completed_at_ms=NULL, revision=revision+1, updated_at_ms=? WHERE id=? AND project_id=? AND incarnation_id=? AND work_revision=? AND revision=? AND status IN ('blocked','succeeded','failed','cancelled')`, at.Int64(), current.TaskID.Bytes(), current.ProjectID.Bytes(), current.TaskIncarnationID.Bytes(), current.WorkRevision.Int64(), revision)
	if err := requireOneRow(updated, err); err != nil {
		return nil, err
	}
	updated, err = connection.ExecContext(ctx, `UPDATE continuations SET state='resolved', revision=revision+1, updated_at_ms=COALESCE(resolved_at_ms, ?) WHERE id=? AND state='queued'`, at.Int64(), id.Bytes())
	if err := requireOneRow(updated, err); err != nil {
		return nil, err
	}
	if err := appendInvalidations(ctx, connection, at, []pendingInvalidation{{kind: EntityTask, id: current.TaskID.Bytes(), revision: revision + 1}, {kind: EntityContinuation, id: id.Bytes(), revision: current.Revision.Int64() + 1}}); err != nil {
		return nil, err
	}
	task, found, err := taskByID(ctx, connection, current.TaskID)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return nil, err
	}
	return &task, nil
}

func continuationByID(ctx context.Context, connection *sql.Conn, id ContinuationID) (Continuation, bool, error) {
	if id.zero() {
		return Continuation{}, false, fmt.Errorf("%w: zero continuation identifier", ErrInvalidValue)
	}
	var rawID, project, task, incarnation, digest, conditionID []byte
	var work, conditionRevision, revision, created, updated, resolved sql.NullInt64
	var kind, state string
	var detail sql.NullString
	err := connection.QueryRowContext(ctx, `SELECT id, project_id, task_id, task_incarnation_id, work_revision, context_digest, condition_kind, condition_id, condition_revision, state, resolution_detail, revision, created_at_ms, updated_at_ms, resolved_at_ms FROM continuations WHERE id=?`, id.Bytes()).Scan(&rawID, &project, &task, &incarnation, &work, &digest, &kind, &conditionID, &conditionRevision, &state, &detail, &revision, &created, &updated, &resolved)
	if err == sql.ErrNoRows {
		return Continuation{}, false, nil
	}
	if err != nil {
		return Continuation{}, false, err
	}
	parsedID, err := ContinuationIDFromBytes(rawID)
	if err != nil || len(digest) != DigestBytes {
		return Continuation{}, false, ErrCorruptState
	}
	projectID, e1 := ProjectIDFromBytes(project)
	taskID, e2 := TaskIDFromBytes(task)
	incarnationID, e3 := IncarnationIDFromBytes(incarnation)
	condition, e4 := identifierFromBytes(conditionID)
	wr, e5 := NewRevision(work.Int64)
	cr, e6 := NewRevision(conditionRevision.Int64)
	rev, e7 := NewRevision(revision.Int64)
	ca, e8 := NewUnixMillis(created.Int64)
	ua, e9 := NewUnixMillis(updated.Int64)
	if err != nil || e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || e6 != nil || e7 != nil || e8 != nil || e9 != nil || updated.Int64 < created.Int64 {
		return Continuation{}, false, ErrCorruptState
	}
	var digestValue [DigestBytes]byte
	copy(digestValue[:], digest)
	var conditionIDValue ContinuationConditionID
	copy(conditionIDValue[:], condition.Bytes())
	result := Continuation{NewContinuation: NewContinuation{ID: parsedID, ProjectID: projectID, TaskID: taskID, TaskIncarnationID: incarnationID, WorkRevision: wr, ContextDigest: digestValue, ConditionKind: ContinuationCondition(kind), ConditionID: conditionIDValue, ConditionRevision: cr}, State: ContinuationState(state), Revision: rev, CreatedAt: ca, UpdatedAt: ua, ResolutionDetail: detail.String}
	if resolved.Valid {
		value, e := NewUnixMillis(resolved.Int64)
		if e != nil {
			return Continuation{}, false, ErrCorruptState
		}
		result.ResolvedAt = &value
	}
	return result, true, nil
}

func (store *Store) Continuation(ctx context.Context, id ContinuationID) (Continuation, bool, error) {
	connection, err := store.readerConnection(ctx)
	if err != nil {
		return Continuation{}, false, err
	}
	defer connection.Close()
	return continuationByID(ctx, connection, id)
}

func resolvedContinuationContextsForTask(ctx context.Context, connection *sql.Conn, task Task) ([]ContinuationContext, error) {
	if task.WorkRevision.Int64() <= 1 {
		return nil, nil
	}
	rows, err := connection.QueryContext(ctx, `SELECT id FROM continuations WHERE task_id=? AND task_incarnation_id=? AND work_revision=? AND state='resolved' ORDER BY updated_at_ms, id`, task.ID.Bytes(), task.IncarnationID.Bytes(), task.WorkRevision.Int64()-1)
	if err != nil {
		return nil, err
	}
	var ids []ContinuationID
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, err
		}
		id, err := ContinuationIDFromBytes(raw)
		if err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var contexts []ContinuationContext
	for _, id := range ids {
		continuation, found, err := continuationByID(ctx, connection, id)
		if err != nil {
			return nil, err
		}
		if !found || continuation.ResolvedAt == nil || continuation.ResolutionDetail == "" {
			return nil, fmt.Errorf("%w: invalid resolved continuation context", ErrCorruptState)
		}
		contexts = append(contexts, ContinuationContext{
			ContextDigest: continuation.ContextDigest, ConditionKind: continuation.ConditionKind,
			ConditionID: continuation.ConditionID, ConditionRevision: continuation.ConditionRevision,
			ResolutionDetail: continuation.ResolutionDetail, ResolvedAt: *continuation.ResolvedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return contexts, nil
}
