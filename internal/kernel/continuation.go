package kernel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
)

// ContinuationState is a yielded human request's wait: waiting for a reply,
// queued with it until the yielded run is terminal, resolved once the task
// is requeued, or cancelled with the request. The request's run names the
// awaiting task and work revision.
type ContinuationState string

const (
	ContinuationWaiting   ContinuationState = "waiting"
	ContinuationQueued    ContinuationState = "queued"
	ContinuationResolved  ContinuationState = "resolved"
	ContinuationCancelled ContinuationState = "cancelled"
)

// ContinuationContext is the causal result of an awaited human request. It
// is copied onto a fresh admission only for the immediately resumed task work
// revision; the durable request remains authoritative across restart.
type ContinuationContext struct {
	ContextDigest    [DigestBytes]byte
	RequestID        HumanRequestID
	ResolutionDetail string
}

// MaxContinuationTaskBytes bounds the private attempt-task envelope. It is
// deliberately larger than either provider's inline task limit so an exact
// provider-limit task can still be returned together with its causal context.
// Reserve 3 KiB of selected knowledge and 4 KiB of explicit revision pins.
// Provider launch bounds stay unchanged; coding providers fetch this envelope.
const MaxContinuationTaskBytes = (128 << 10) + MaxHumanRequestReplyBytes + 2048 + (7 << 10)

// ContinuationTaskText appends contexts to task; shell writes each line as a
// comment.
func ContinuationTaskText(task string, contexts []ContinuationContext, shell bool) string {
	prefix := ""
	if shell {
		prefix = "# "
	}
	var builder strings.Builder
	builder.Grow(len(task) + len(contexts)*128)
	builder.WriteString(task)
	builder.WriteString("\n\n" + prefix + "Factory continuation context:\n")
	for _, context := range contexts {
		builder.WriteString(prefix + "condition=human_request condition_id=" + context.RequestID.String() + " context_digest=" + hex.EncodeToString(context.ContextDigest[:]) + " resolution=" + context.ResolutionDetail + "\n")
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
	return len(ContinuationTaskText(task, contexts, false)) <= limit
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

// yieldHumanQuestion is the shared atomic portion of yielding. The caller
// owns tx and decides when the complete question/yield transaction is
// committed.
func (store *Store) yieldHumanQuestion(ctx context.Context, tx *writeTx, run Run, request HumanRequest, at UnixMillis) error {
	if request.Status != HumanRequestOpen {
		return tx.Rollback(ErrConflict)
	}
	if run.Phase != RunRunning || run.CredentialRevokedAt != nil {
		return tx.Rollback(ErrUnauthorized)
	}
	task, found, err := taskByID(ctx, tx.connection, run.TaskID)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return tx.Rollback(err)
	}
	if task.ProjectID != run.ProjectID || task.IncarnationID != run.TaskIncarnationID || task.WorkRevision != run.AdmittedTaskWorkRevision || task.Status != TaskRunning {
		return tx.Rollback(ErrRevisionConflict)
	}
	if request.Continuation != "" {
		return nil
	}
	updated, err := tx.connection.ExecContext(ctx, `UPDATE human_requests SET continuation='waiting' WHERE id=? AND status='open' AND continuation IS NULL`, request.ID.Bytes())
	if err := requireOneRow(updated, err); err != nil {
		return tx.Rollback(err)
	}
	proposal, err := NewCancelledProposal("yielded awaiting human_request")
	if err != nil {
		return tx.Rollback(err)
	}
	_, err = store.enterFinalizingInTransaction(ctx, tx, run, run.Revision, proposal, at, nil)
	return err
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
	ids, err := humanRequestIDs(ctx, tx.connection, `SELECT id FROM human_requests WHERE continuation='queued' ORDER BY closed_at_ms, id`)
	if err != nil {
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

func humanRequestIDs(ctx context.Context, connection *sql.Conn, query string, args ...any) ([]HumanRequestID, error) {
	rows, err := connection.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []HumanRequestID
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		id, err := HumanRequestIDFromBytes(raw)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
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
	if request.Continuation != ContinuationWaiting {
		return false, tx.Rollback(ErrNotFound)
	}
	if err := resolveHumanContinuationOnConnection(ctx, tx, request, deliveryID, reply, at); err != nil {
		return false, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func resolveHumanContinuationOnConnection(ctx context.Context, tx *writeTx, request HumanRequest, deliveryID HumanRequestDeliveryID, reply string, at UnixMillis) error {
	if request.IdempotencyKey == stalledItemKey {
		// The resumed carrier sees what the operator answered, as far as the
		// bound on a resolution leaves room beside the whole reply.
		const prefix = "Operator reply to: "
		if room := MaxHumanRequestReplyBytes - len(prefix) - 1 - len(reply); room > 0 {
			reply = prefix + strings.ToValidUTF8(request.QuestionText[:min(len(request.QuestionText), room)], "") + "\n" + reply
		}
	}
	if deliveryID.zero() {
		deliveryID = HumanRequestDeliveryID(request.ID)
	}
	var deliveryCollision int
	if err := tx.connection.QueryRowContext(ctx, `SELECT 1 FROM human_requests WHERE delivery_id = ?`, deliveryID.Bytes()).Scan(&deliveryCollision); err == nil {
		return ErrConflict
	} else if err != sql.ErrNoRows {
		return err
	}
	updated, err := tx.connection.ExecContext(ctx, `UPDATE human_requests SET status='resolved', delivery_id=?, delivery_started_at_ms=?, resolution_kind='reply', closed_at_ms=?, continuation='queued', continuation_reply=?, revision=revision+1, updated_at_ms=? WHERE id=? AND status='open' AND continuation='waiting' AND revision=?`, deliveryID.Bytes(), at.Int64(), at.Int64(), reply, at.Int64(), request.ID.Bytes(), request.Revision.Int64())
	if err := requireOneRow(updated, err); err != nil {
		return err
	}
	if err := appendInvalidations(ctx, tx.connection, at, []pendingInvalidation{{kind: EntityHumanRequest, id: request.ID.Bytes(), revision: request.Revision.Int64() + 1}}); err != nil {
		return err
	}
	_, err = promoteContinuationOnConnection(ctx, tx.connection, request.ID, at)
	return err
}

func promoteContinuationOnConnection(ctx context.Context, connection *sql.Conn, id HumanRequestID, at UnixMillis) (*Task, error) {
	var rawTask, rawProject, rawIncarnation []byte
	var work, revision int64
	var status string
	err := connection.QueryRowContext(ctx, `SELECT t.id, t.project_id, t.incarnation_id, t.work_revision, t.status, t.revision FROM human_requests h JOIN runs r ON r.id = h.run_id
		JOIN tasks t ON t.id = r.task_id AND t.incarnation_id = r.task_incarnation_id AND t.work_revision = r.admitted_task_work_revision
		WHERE h.id = ? AND h.continuation = 'queued' AND r.phase = 'terminal'`, id.Bytes()).Scan(&rawTask, &rawProject, &rawIncarnation, &work, &status, &revision)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if status != TaskCancelled.String() && status != TaskFailed.String() && status != TaskBlocked.String() && status != TaskSucceeded.String() {
		return nil, nil
	}
	updated, err := connection.ExecContext(ctx, `UPDATE tasks SET status='queued', work_revision=work_revision+1, blocked_reason=NULL, result=NULL, completed_at_ms=NULL, revision=revision+1, updated_at_ms=? WHERE id=? AND project_id=? AND incarnation_id=? AND work_revision=? AND revision=? AND status IN ('blocked','succeeded','failed','cancelled')`, at.Int64(), rawTask, rawProject, rawIncarnation, work, revision)
	if err := requireOneRow(updated, err); err != nil {
		return nil, err
	}
	updated, err = connection.ExecContext(ctx, `UPDATE human_requests SET continuation='resolved' WHERE id=? AND continuation='queued'`, id.Bytes())
	if err := requireOneRow(updated, err); err != nil {
		return nil, err
	}
	if err := appendInvalidations(ctx, connection, at, []pendingInvalidation{{kind: EntityTask, id: rawTask, revision: revision + 1}}); err != nil {
		return nil, err
	}
	taskID, err := TaskIDFromBytes(rawTask)
	if err != nil {
		return nil, ErrCorruptState
	}
	task, found, err := taskByID(ctx, connection, taskID)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return nil, err
	}
	return &task, nil
}

// resolvedContinuationContextsForTask returns the replies the task's previous
// work revision yielded on, which is the revision task's queued work resumes.
func resolvedContinuationContextsForTask(ctx context.Context, connection *sql.Conn, task Task) ([]ContinuationContext, error) {
	if task.WorkRevision.Int64() <= 1 {
		return nil, nil
	}
	rows, err := connection.QueryContext(ctx, `SELECT h.id, h.continuation_reply FROM human_requests h JOIN runs r ON r.id = h.run_id
		WHERE r.task_id=? AND r.task_incarnation_id=? AND r.admitted_task_work_revision=? AND h.continuation='resolved' ORDER BY h.closed_at_ms, h.id`, task.ID.Bytes(), task.IncarnationID.Bytes(), task.WorkRevision.Int64()-1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", task.ID.String(), task.WorkRevision.Int64()-1)))
	var contexts []ContinuationContext
	for rows.Next() {
		var raw []byte
		var reply string
		if err := rows.Scan(&raw, &reply); err != nil {
			return nil, err
		}
		id, err := HumanRequestIDFromBytes(raw)
		if err != nil {
			return nil, err
		}
		contexts = append(contexts, ContinuationContext{ContextDigest: digest, RequestID: id, ResolutionDetail: reply})
	}
	return contexts, rows.Err()
}
