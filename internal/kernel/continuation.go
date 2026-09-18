package kernel

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"fmt"
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

func (store *Store) CreateContinuation(ctx context.Context, spec NewContinuation, at UnixMillis) (Continuation, error) {
	if err := validateContinuationSpec(spec); err != nil {
		return Continuation{}, err
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return Continuation{}, err
	}
	defer tx.Close()
	task, found, err := taskByID(ctx, tx.connection, spec.TaskID)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return Continuation{}, tx.Rollback(err)
	}
	if task.ProjectID != spec.ProjectID || task.IncarnationID != spec.TaskIncarnationID || task.WorkRevision != spec.WorkRevision || task.Status != TaskRunning {
		return Continuation{}, tx.Rollback(ErrRevisionConflict)
	}
	_, err = tx.connection.ExecContext(ctx, `INSERT INTO continuations(id, project_id, task_id, task_incarnation_id, work_revision, context_digest, condition_kind, condition_id, condition_revision, state, revision, created_at_ms, updated_at_ms) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, 'waiting', 1, ?, ?)`, spec.ID.Bytes(), spec.ProjectID.Bytes(), spec.TaskID.Bytes(), spec.TaskIncarnationID.Bytes(), spec.WorkRevision.Int64(), spec.ContextDigest[:], string(spec.ConditionKind), spec.ConditionID.Bytes(), spec.ConditionRevision.Int64(), at.Int64(), at.Int64())
	if err != nil {
		return Continuation{}, tx.Rollback(err)
	}
	if err := appendInvalidations(ctx, tx.connection, at, []pendingInvalidation{{kind: EntityContinuation, id: spec.ID.Bytes(), revision: 1}}); err != nil {
		return Continuation{}, tx.Rollback(err)
	}
	value, found, err := continuationByID(ctx, tx.connection, spec.ID)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return Continuation{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Continuation{}, err
	}
	return value, nil
}

// YieldContinuationForAttempt is the daemon boundary for a provider request
// that can outlive its bearer.  The continuation and credential revocation
// are committed together; the caller may then terminate the old provider and
// release its slot.  No provider input is retained or replayed here.
func (store *Store) YieldContinuationForAttempt(ctx context.Context, digest AttemptDigest, kind ContinuationCondition, conditionID ContinuationConditionID, conditionRevision Revision, at UnixMillis) (Continuation, error) {
	if kind == "" || !validContinuationCondition(kind) || conditionID.zero() || conditionRevision.Int64() < 1 {
		return Continuation{}, fmt.Errorf("%w: invalid continuation event", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return Continuation{}, err
	}
	defer tx.Close()
	run, found, err := runByDigest(ctx, tx.connection, digest)
	if err != nil || !found {
		if err == nil {
			err = ErrUnauthorized
		}
		return Continuation{}, tx.Rollback(err)
	}
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
	var existingFound bool
	existing, existingFound, err = continuationByCondition(ctx, tx.connection, task.ID, task.IncarnationID, task.WorkRevision, kind, conditionID)
	if err != nil {
		return Continuation{}, tx.Rollback(err)
	}
	if existingFound {
		return existing, tx.Rollback(nil)
	}
	var raw [IDBytes]byte
	if _, err := rand.Read(raw[:]); err != nil || raw == ([IDBytes]byte{}) {
		if err == nil {
			err = fmt.Errorf("%w: generated zero continuation identifier", ErrCorruptState)
		}
		return Continuation{}, tx.Rollback(err)
	}
	id, err := ContinuationIDFromBytes(raw[:])
	if err != nil {
		return Continuation{}, tx.Rollback(err)
	}
	contextDigest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", task.ID.String(), task.WorkRevision.Int64())))
	spec := NewContinuation{ID: id, ProjectID: task.ProjectID, TaskID: task.ID, TaskIncarnationID: task.IncarnationID, WorkRevision: task.WorkRevision, ContextDigest: contextDigest, ConditionKind: kind, ConditionID: conditionID, ConditionRevision: conditionRevision}
	if err := validateContinuationSpec(spec); err != nil {
		return Continuation{}, tx.Rollback(err)
	}
	if _, err := tx.connection.ExecContext(ctx, `INSERT INTO continuations(id, project_id, task_id, task_incarnation_id, work_revision, context_digest, condition_kind, condition_id, condition_revision, state, revision, created_at_ms, updated_at_ms) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, 'waiting', 1, ?, ?)`, id.Bytes(), task.ProjectID.Bytes(), task.ID.Bytes(), task.IncarnationID.Bytes(), task.WorkRevision.Int64(), contextDigest[:], string(kind), conditionID.Bytes(), conditionRevision.Int64(), at.Int64(), at.Int64()); err != nil {
		return Continuation{}, tx.Rollback(err)
	}
	if err := appendInvalidations(ctx, tx.connection, at, []pendingInvalidation{{kind: EntityContinuation, id: id.Bytes(), revision: 1}}); err != nil {
		return Continuation{}, tx.Rollback(err)
	}
	proposal, err := NewCancelledProposal("yielded awaiting " + string(kind))
	if err != nil {
		return Continuation{}, tx.Rollback(err)
	}
	if _, err := store.enterFinalizing(ctx, tx, run, run.Revision, proposal, at, nil, &conditionID); err != nil {
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
	var raw [IDBytes]byte
	if _, err := rand.Read(raw[:]); err != nil || raw == ([IDBytes]byte{}) {
		if err == nil {
			err = fmt.Errorf("%w: generated zero continuation identifier", ErrCorruptState)
		}
		return Continuation{}, tx.Rollback(err)
	}
	id, err := ContinuationIDFromBytes(raw[:])
	if err != nil {
		return Continuation{}, tx.Rollback(err)
	}
	contextDigest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", task.ID.String(), task.WorkRevision.Int64())))
	spec := NewContinuation{ID: id, ProjectID: task.ProjectID, TaskID: task.ID, TaskIncarnationID: task.IncarnationID, WorkRevision: task.WorkRevision, ContextDigest: contextDigest, ConditionKind: kind, ConditionID: conditionID, ConditionRevision: conditionRevision}
	if err := validateContinuationSpec(spec); err != nil {
		return Continuation{}, tx.Rollback(err)
	}
	if _, err := tx.connection.ExecContext(ctx, `INSERT INTO continuations(id, project_id, task_id, task_incarnation_id, work_revision, context_digest, condition_kind, condition_id, condition_revision, state, revision, created_at_ms, updated_at_ms) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, 'waiting', 1, ?, ?)`, id.Bytes(), task.ProjectID.Bytes(), task.ID.Bytes(), task.IncarnationID.Bytes(), task.WorkRevision.Int64(), contextDigest[:], string(kind), conditionID.Bytes(), conditionRevision.Int64(), at.Int64(), at.Int64()); err != nil {
		return Continuation{}, tx.Rollback(err)
	}
	if err := appendInvalidations(ctx, tx.connection, at, []pendingInvalidation{{kind: EntityContinuation, id: id.Bytes(), revision: 1}}); err != nil {
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

func (store *Store) ResolveContinuation(ctx context.Context, id ContinuationID, expected Revision, detail string, at UnixMillis) (Continuation, error) {
	if id.zero() || len(detail) == 0 || byteLen(detail) > 4096 {
		return Continuation{}, fmt.Errorf("%w: invalid continuation resolution", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return Continuation{}, err
	}
	defer tx.Close()
	current, found, err := continuationByID(ctx, tx.connection, id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return Continuation{}, tx.Rollback(err)
	}
	return resolveContinuationOnConnection(ctx, tx, current, expected, detail, at)
}

// ResolveContinuationForEvent is the event-delivery seam for continuations.
// Callers must identify the exact durable condition that caused the wake.  The
// condition revision is checked before the compare-and-swap in
// ResolveContinuation, so an old answer, question, or handoff cannot wake a
// newer continuation after a task revision changed.
func (store *Store) ResolveContinuationForEvent(ctx context.Context, id ContinuationID, expected Revision, kind ContinuationCondition, conditionID ContinuationConditionID, conditionRevision Revision, detail string, at UnixMillis) (Continuation, error) {
	if id.zero() || !validContinuationCondition(kind) || conditionID.zero() || conditionRevision.Int64() < 1 {
		return Continuation{}, fmt.Errorf("%w: invalid continuation event", ErrInvalidValue)
	}
	if len(detail) == 0 || byteLen(detail) > 4096 {
		return Continuation{}, fmt.Errorf("%w: invalid continuation resolution", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return Continuation{}, err
	}
	defer tx.Close()
	current, found, err := continuationByID(ctx, tx.connection, id)
	if err != nil {
		return Continuation{}, tx.Rollback(err)
	}
	if !found {
		return Continuation{}, tx.Rollback(ErrNotFound)
	}
	if current.ConditionKind != kind || current.ConditionID != conditionID || current.ConditionRevision != conditionRevision {
		return Continuation{}, tx.Rollback(ErrRevisionConflict)
	}
	return resolveContinuationOnConnection(ctx, tx, current, expected, detail, at)
}

// resolveContinuationOnConnection validates the current durable condition and
// performs the wake CAS while the caller's write transaction is still open.
// In particular, ResolveContinuationForEvent must not validate an event in a
// read transaction and then wake a different continuation revision.
func resolveContinuationOnConnection(ctx context.Context, tx *writeTx, current Continuation, expected Revision, detail string, at UnixMillis) (Continuation, error) {
	if current.State == ContinuationQueued && current.Revision.Int64() == expected.Int64()+1 && current.ResolutionDetail == detail {
		if err := tx.Rollback(nil); err != nil {
			return Continuation{}, err
		}
		return current, nil
	}
	if current.State != ContinuationWaiting || current.Revision != expected || at.Int64() < current.UpdatedAt.Int64() {
		return Continuation{}, tx.Rollback(ErrRevisionConflict)
	}
	// A queued continuation is resolved but not yet admitted. Keep the
	// resolution in the durable row so fresh admission can receive causal
	// event metadata.
	updated, err := tx.connection.ExecContext(ctx, `UPDATE continuations SET state='queued', resolution_detail=?, resolved_at_ms=?, revision=revision+1, updated_at_ms=? WHERE id=? AND state='waiting' AND revision=?`, detail, at.Int64(), at.Int64(), current.ID.Bytes(), expected.Int64())
	if err := requireOneRow(updated, err); err != nil {
		return Continuation{}, tx.Rollback(err)
	}
	if err := appendInvalidations(ctx, tx.connection, at, []pendingInvalidation{{kind: EntityContinuation, id: current.ID.Bytes(), revision: expected.Int64() + 1}}); err != nil {
		return Continuation{}, tx.Rollback(err)
	}
	if _, err := promoteContinuationOnConnection(ctx, tx.connection, current.ID, at); err != nil {
		return Continuation{}, tx.Rollback(err)
	}
	value, found, err := continuationByID(ctx, tx.connection, current.ID)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return Continuation{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Continuation{}, err
	}
	return value, nil
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
	target, found, err := runByID(ctx, tx.connection, request.RunID)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return false, tx.Rollback(err)
	}
	if target.ProjectID != actor.ProjectID || target.Phase != RunTerminal {
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
	if err := resolveHumanContinuationOnConnection(ctx, tx, request, continuation, HumanRequestDeliveryID{}, reply, at); err != nil {
		return false, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
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
	target, found, err := runByID(ctx, tx.connection, request.RunID)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return false, tx.Rollback(err)
	}
	if target.Phase != RunTerminal {
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
	if err := resolveHumanContinuationOnConnection(ctx, tx, request, continuation, HumanRequestDeliveryID{}, reply, at); err != nil {
		return false, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
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
	target, found, err := runByID(ctx, tx.connection, request.RunID)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return false, tx.Rollback(err)
	}
	if target.Phase != RunTerminal {
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
	if deliveryID.zero() {
		var err error
		deliveryID, err = HumanRequestDeliveryIDFromBytes(continuation.ID.Bytes())
		if err != nil {
			return err
		}
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

func (store *Store) CancelContinuation(ctx context.Context, id ContinuationID, expected Revision, detail string, at UnixMillis) (Continuation, error) {
	if id.zero() || len(detail) == 0 || byteLen(detail) > 4096 {
		return Continuation{}, fmt.Errorf("%w: invalid continuation cancellation", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return Continuation{}, err
	}
	defer tx.Close()
	current, found, err := continuationByID(ctx, tx.connection, id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return Continuation{}, tx.Rollback(err)
	}
	if current.State == ContinuationCancelled && current.Revision.Int64() == expected.Int64()+1 && current.ResolutionDetail == detail {
		return current, tx.Rollback(nil)
	}
	if current.State != ContinuationWaiting && current.State != ContinuationQueued || current.Revision != expected {
		return Continuation{}, tx.Rollback(ErrRevisionConflict)
	}
	if at.Int64() < current.UpdatedAt.Int64() {
		return Continuation{}, tx.Rollback(ErrRevisionConflict)
	}
	updated, err := tx.connection.ExecContext(ctx, `UPDATE continuations SET state='cancelled', resolution_detail=?, resolved_at_ms=?, revision=revision+1, updated_at_ms=? WHERE id=? AND state IN ('waiting', 'queued') AND revision=?`, detail, at.Int64(), at.Int64(), id.Bytes(), expected.Int64())
	if err := requireOneRow(updated, err); err != nil {
		return Continuation{}, tx.Rollback(err)
	}
	if err := appendInvalidations(ctx, tx.connection, at, []pendingInvalidation{{kind: EntityContinuation, id: id.Bytes(), revision: expected.Int64() + 1}}); err != nil {
		return Continuation{}, tx.Rollback(err)
	}
	value, _, err := continuationByID(ctx, tx.connection, id)
	if err != nil {
		return Continuation{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Continuation{}, err
	}
	return value, nil
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
