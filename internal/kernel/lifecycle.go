package kernel

import (
	"context"
	"database/sql"
	"fmt"
)

func (store *Store) ActivateResource(ctx context.Context, runID RunID, resourceID ResourceID, expected Revision, identity ResourceIdentity, at UnixMillis) (Resource, error) {
	return store.transitionResource(ctx, runID, resourceID, expected, identity, "", ResourceActive, at)
}

func (store *Store) BeginResourceRelease(ctx context.Context, runID RunID, resourceID ResourceID, expected Revision, identity ResourceIdentity, at UnixMillis) (Resource, error) {
	return store.transitionResource(ctx, runID, resourceID, expected, identity, "", ResourceReleasing, at)
}

func (store *Store) MarkResourceUnresolved(ctx context.Context, runID RunID, resourceID ResourceID, expected Revision, identity ResourceIdentity, reason string, at UnixMillis) (Resource, error) {
	if byteLen(reason) < 1 || byteLen(reason) > 4096 {
		return Resource{}, fmt.Errorf("%w: invalid unresolved reason", ErrInvalidValue)
	}
	return store.transitionResource(ctx, runID, resourceID, expected, identity, reason, ResourceUnresolved, at)
}

func (store *Store) ReleaseResource(ctx context.Context, runID RunID, resourceID ResourceID, expected Revision, identity ResourceIdentity, at UnixMillis) (Resource, error) {
	return store.transitionResource(ctx, runID, resourceID, expected, identity, "", ResourceReleased, at)
}

func (store *Store) transitionResource(ctx context.Context, runID RunID, resourceID ResourceID, expected Revision, identity ResourceIdentity, reason string, target ResourceState, at UnixMillis) (Resource, error) {
	if runID.zero() || resourceID.zero() || target.String() == "" {
		return Resource{}, fmt.Errorf("%w: invalid resource transition", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return Resource{}, err
	}
	defer tx.Close()
	resource, found, err := resourceByID(ctx, tx.connection, resourceID)
	if err != nil {
		return Resource{}, tx.Rollback(err)
	}
	if !found {
		return Resource{}, tx.Rollback(ErrNotFound)
	}
	if resource.RunID != runID {
		return Resource{}, tx.Rollback(ErrConflict)
	}
	if resource.State == target && resourceIdentityEqual(resource.Identity, identity) && resource.UnresolvedReason == reason && resource.Revision.Int64() == expected.Int64()+1 {
		if err := tx.Rollback(nil); err != nil {
			return Resource{}, err
		}
		return resource, nil
	}
	if resource.Revision != expected || at.Int64() < resource.UpdatedAt.Int64() {
		return Resource{}, tx.Rollback(ErrRevisionConflict)
	}
	run, found, err := runByID(ctx, tx.connection, runID)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return Resource{}, tx.Rollback(err)
	}
	if target == ResourceActive && run.Phase != RunAdmitted || target != ResourceActive && run.Phase != RunFinalizing {
		return Resource{}, tx.Rollback(ErrConflict)
	}
	if err := validateResourceEdge(resource, target, identity, reason); err != nil {
		return Resource{}, tx.Rollback(err)
	}
	if target == ResourceActive {
		if err := ensureResourceIdentityUnused(ctx, tx.connection, resource, identity); err != nil {
			return Resource{}, tx.Rollback(err)
		}
	}
	var result sql.Result
	switch target {
	case ResourceActive:
		if resource.Kind == ResourceRuntimeRoot {
			pathIdentity, _ := identity.Path()
			result, err = tx.connection.ExecContext(ctx, `UPDATE resources SET state = 'active', path_dev = ?, path_inode = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND run_id = ? AND state = 'declared' AND revision = ? AND path_dev IS NULL AND path_inode IS NULL`, pathIdentity.device, pathIdentity.inode, at.Int64(), resourceID.Bytes(), runID.Bytes(), expected.Int64())
		} else {
			pid, pgid, birth, _ := identity.Process()
			result, err = tx.connection.ExecContext(ctx, `UPDATE resources SET state = 'active', pid = ?, pgid = ?, birth_digest = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND run_id = ? AND state = 'declared' AND revision = ? AND pid IS NULL AND pgid IS NULL AND birth_digest IS NULL`, pid, pgid, birth.Bytes(), at.Int64(), resourceID.Bytes(), runID.Bytes(), expected.Int64())
		}
	case ResourceReleasing:
		result, err = tx.connection.ExecContext(ctx, `UPDATE resources SET state = 'releasing', revision = revision + 1, updated_at_ms = ? WHERE id = ? AND run_id = ? AND state IN ('declared', 'active') AND revision = ?`, at.Int64(), resourceID.Bytes(), runID.Bytes(), expected.Int64())
	case ResourceUnresolved:
		result, err = tx.connection.ExecContext(ctx, `UPDATE resources SET state = 'unresolved', unresolved_reason = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND run_id = ? AND state IN ('declared', 'active', 'releasing') AND revision = ?`, reason, at.Int64(), resourceID.Bytes(), runID.Bytes(), expected.Int64())
	case ResourceReleased:
		result, err = tx.connection.ExecContext(ctx, `UPDATE resources SET state = 'released', unresolved_reason = NULL, released_at_ms = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND run_id = ? AND state IN ('declared', 'active', 'releasing', 'unresolved') AND revision = ?`, at.Int64(), at.Int64(), resourceID.Bytes(), runID.Bytes(), expected.Int64())
	default:
		return Resource{}, tx.Rollback(ErrInvalidValue)
	}
	if err := requireOneRow(result, err); err != nil {
		return Resource{}, tx.Rollback(err)
	}
	resource, found, err = resourceByID(ctx, tx.connection, resourceID)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return Resource{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Resource{}, err
	}
	return resource, nil
}

func validateResourceEdge(resource Resource, target ResourceState, identity ResourceIdentity, reason string) error {
	exactIdentity := resourceIdentityEqual(resource.Identity, identity)
	switch target {
	case ResourceActive:
		if resource.State != ResourceDeclared || !resource.Identity.Empty() || !identity.validFor(resource.Kind) {
			return ErrConflict
		}
	case ResourceReleasing:
		if resource.State != ResourceDeclared && resource.State != ResourceActive || !exactIdentity || reason != "" {
			return ErrConflict
		}
	case ResourceUnresolved:
		if resource.State != ResourceDeclared && resource.State != ResourceActive && resource.State != ResourceReleasing || !exactIdentity || byteLen(reason) < 1 || byteLen(reason) > 4096 {
			return ErrConflict
		}
	case ResourceReleased:
		if resource.State == ResourceReleased || resource.State.String() == "" || !exactIdentity || reason != "" {
			return ErrConflict
		}
	default:
		return ErrInvalidValue
	}
	return nil
}

func resourceIdentityEqual(left, right ResourceIdentity) bool { return left == right }

func ensureResourceIdentityUnused(ctx context.Context, connection *sql.Conn, resource Resource, identity ResourceIdentity) error {
	if resource.Kind == ResourceRuntimeRoot {
		var collisions int
		pathIdentity, _ := identity.Path()
		if err := connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM resources WHERE id <> ? AND path_dev = ? AND path_inode = ?`, resource.ID.Bytes(), pathIdentity.device, pathIdentity.inode).Scan(&collisions); err != nil {
			return err
		}
		if collisions != 0 {
			return ErrConflict
		}
		return nil
	}
	pid, pgid, birth, _ := identity.Process()
	rows, err := connection.QueryContext(ctx, `SELECT run_id, kind FROM resources WHERE id <> ? AND pid = ? AND pgid = ? AND birth_digest = ?`, resource.ID.Bytes(), pid, pgid, birth.Bytes())
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var rawRunID []byte
		var rawKind string
		if err := rows.Scan(&rawRunID, &rawKind); err != nil {
			return err
		}
		runID, runErr := RunIDFromBytes(rawRunID)
		kind, kindErr := parseResourceKind(rawKind)
		providerPair := resource.RunID == runID && (resource.Kind == ResourceProviderProcess && kind == ResourceProviderGroup || resource.Kind == ResourceProviderGroup && kind == ResourceProviderProcess)
		if runErr != nil || kindErr != nil || !providerPair {
			return ErrConflict
		}
	}
	return rows.Err()
}

func (store *Store) ActivateRun(ctx context.Context, runID RunID, expected Revision, at UnixMillis) (Run, error) {
	if runID.zero() {
		return Run{}, fmt.Errorf("%w: zero run identifier", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return Run{}, err
	}
	defer tx.Close()
	run, found, err := runByID(ctx, tx.connection, runID)
	if err != nil {
		return Run{}, tx.Rollback(err)
	}
	if !found {
		return Run{}, tx.Rollback(ErrNotFound)
	}
	if run.Phase == RunRunning && run.Revision.Int64() == expected.Int64()+1 {
		if err := tx.Rollback(nil); err != nil {
			return Run{}, err
		}
		return run, nil
	}
	if run.Phase != RunAdmitted || run.Revision != expected || at.Int64() < run.UpdatedAt.Int64() {
		return Run{}, tx.Rollback(ErrRevisionConflict)
	}
	relationships, err := loadRunRelationships(ctx, tx.connection, run)
	if err != nil {
		return Run{}, tx.Rollback(err)
	}
	if run.Role == RoleWorker && relationships.change.Phase != ChangeAvailable {
		return Run{}, tx.Rollback(ErrConflict)
	}
	resources := relationships.resources
	if !exactResourceSet(resources, true) {
		return Run{}, tx.Rollback(ErrConflict)
	}
	result, err := tx.connection.ExecContext(ctx, `UPDATE runs SET phase = 'running', running_at_ms = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND phase = 'admitted' AND revision = ?`, at.Int64(), at.Int64(), run.ID.Bytes(), expected.Int64())
	if err := requireOneRow(result, err); err != nil {
		return Run{}, tx.Rollback(err)
	}
	if err := appendInvalidations(ctx, tx.connection, at, []pendingInvalidation{{kind: EntityRun, id: run.ID.Bytes(), revision: expected.Int64() + 1}}); err != nil {
		return Run{}, tx.Rollback(err)
	}
	run, found, err = runByID(ctx, tx.connection, run.ID)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return Run{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Run{}, err
	}
	return run, nil
}

func (store *Store) ProposeAttemptOutcome(ctx context.Context, digest AttemptDigest, proposal Proposal, at UnixMillis) (Run, error) {
	if !proposal.valid() {
		return Run{}, fmt.Errorf("%w: invalid attempt proposal", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return Run{}, err
	}
	defer tx.Close()
	run, found, err := runByDigest(ctx, tx.connection, digest)
	if err != nil {
		return Run{}, tx.Rollback(err)
	}
	if !found || run.Phase != RunRunning || run.CredentialRevokedAt != nil {
		return Run{}, tx.Rollback(ErrUnauthorized)
	}
	return store.enterFinalizing(ctx, tx, run, run.Revision, proposal, at)
}

func (store *Store) FailAdmitted(ctx context.Context, runID RunID, expected Revision, failure Proposal, at UnixMillis) (Run, error) {
	if runID.zero() || !failure.valid() || failure.kind != OutcomeFailed {
		return Run{}, fmt.Errorf("%w: invalid admitted failure", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return Run{}, err
	}
	defer tx.Close()
	run, found, err := runByID(ctx, tx.connection, runID)
	if err != nil {
		return Run{}, tx.Rollback(err)
	}
	if !found {
		return Run{}, tx.Rollback(ErrNotFound)
	}
	if run.Phase == RunFinalizing && run.RunningAt == nil && run.Proposal != nil && run.Proposal.equal(failure) && run.Revision.Int64() == expected.Int64()+1 {
		if err := tx.Rollback(nil); err != nil {
			return Run{}, err
		}
		return run, nil
	}
	if run.Phase != RunAdmitted {
		return Run{}, tx.Rollback(ErrConflict)
	}
	return store.enterFinalizing(ctx, tx, run, expected, failure, at)
}

func (store *Store) CancelRun(ctx context.Context, runID RunID, expected Revision, detail string, at UnixMillis) (Run, error) {
	proposal, err := NewCancelledProposal(detail)
	if err != nil {
		return Run{}, err
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return Run{}, err
	}
	defer tx.Close()
	run, found, err := runByID(ctx, tx.connection, runID)
	if err != nil {
		return Run{}, tx.Rollback(err)
	}
	if !found {
		return Run{}, tx.Rollback(ErrNotFound)
	}
	if (run.Phase == RunFinalizing || run.Phase == RunTerminal) && run.Proposal != nil && run.Proposal.equal(proposal) && run.Revision.Int64() > expected.Int64() {
		if err := tx.Rollback(nil); err != nil {
			return Run{}, err
		}
		return run, nil
	}
	if run.Phase != RunAdmitted && run.Phase != RunRunning {
		return Run{}, tx.Rollback(ErrConflict)
	}
	return store.enterFinalizing(ctx, tx, run, expected, proposal, at)
}

func (store *Store) enterFinalizing(ctx context.Context, tx *writeTx, run Run, expected Revision, proposal Proposal, at UnixMillis) (Run, error) {
	if run.Revision != expected || at.Int64() < run.UpdatedAt.Int64() {
		return Run{}, tx.Rollback(ErrRevisionConflict)
	}
	kind, code, detail, result := proposalSQL(proposal)
	updated, err := tx.connection.ExecContext(ctx, `UPDATE runs SET phase = 'finalizing', proposal_kind = ?, proposal_code = ?, proposal_detail = ?, proposal_result = ?, credential_revoked_at_ms = ?, finalizing_at_ms = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND phase IN ('admitted', 'running') AND proposal_kind IS NULL AND credential_revoked_at_ms IS NULL AND revision = ?`,
		kind, code, detail, result, at.Int64(), at.Int64(), at.Int64(), run.ID.Bytes(), expected.Int64())
	if err := requireOneRow(updated, err); err != nil {
		return Run{}, tx.Rollback(err)
	}
	if _, err := tx.connection.ExecContext(ctx, `UPDATE resources SET state = 'releasing', revision = revision + 1, updated_at_ms = ? WHERE run_id = ? AND state IN ('declared', 'active')`, at.Int64(), run.ID.Bytes()); err != nil {
		return Run{}, tx.Rollback(err)
	}
	if err := appendInvalidations(ctx, tx.connection, at, []pendingInvalidation{{kind: EntityRun, id: run.ID.Bytes(), revision: expected.Int64() + 1}}); err != nil {
		return Run{}, tx.Rollback(err)
	}
	run, found, err := runByID(ctx, tx.connection, run.ID)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return Run{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Run{}, err
	}
	return run, nil
}

func (store *Store) ObserveRunnerExit(ctx context.Context, runID RunID, expected Revision, exit RunnerExit, at UnixMillis) (Run, error) {
	if runID.zero() || !exit.valid() || at.Int64() < exit.at.Int64() {
		return Run{}, fmt.Errorf("%w: invalid runner exit observation", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return Run{}, err
	}
	defer tx.Close()
	run, found, err := runByID(ctx, tx.connection, runID)
	if err != nil {
		return Run{}, tx.Rollback(err)
	}
	if !found {
		return Run{}, tx.Rollback(ErrNotFound)
	}
	if run.RunnerExit != nil {
		if !run.RunnerExit.equal(exit) {
			return Run{}, tx.Rollback(ErrConflict)
		}
		if err := tx.Rollback(nil); err != nil {
			return Run{}, err
		}
		return run, nil
	}
	if run.Phase == RunTerminal || run.Revision != expected || at.Int64() < run.UpdatedAt.Int64() {
		return Run{}, tx.Rollback(ErrRevisionConflict)
	}
	code, signal := exitSQL(exit)
	newRevision := expected.Int64() + 1
	if run.Phase == RunAdmitted || run.Phase == RunRunning {
		proposal, _ := NewFailureProposal(FailureRunnerExit, "runner exited before an attempt outcome")
		kind, failureCode, detail, result := proposalSQL(proposal)
		updated, err := tx.connection.ExecContext(ctx, `UPDATE runs SET phase = 'finalizing', proposal_kind = ?, proposal_code = ?, proposal_detail = ?, proposal_result = ?, credential_revoked_at_ms = ?, finalizing_at_ms = ?, runner_exit_sequence = ?, runner_exit_code = ?, runner_exit_signal = ?, runner_exit_at_ms = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND phase IN ('admitted', 'running') AND proposal_kind IS NULL AND runner_exit_sequence IS NULL AND revision = ?`,
			kind, failureCode, detail, result, at.Int64(), at.Int64(), exit.sequence, code, signal, exit.at.Int64(), at.Int64(), run.ID.Bytes(), expected.Int64())
		if err := requireOneRow(updated, err); err != nil {
			return Run{}, tx.Rollback(err)
		}
		if _, err := tx.connection.ExecContext(ctx, `UPDATE resources SET state = 'releasing', revision = revision + 1, updated_at_ms = ? WHERE run_id = ? AND state IN ('declared', 'active')`, at.Int64(), run.ID.Bytes()); err != nil {
			return Run{}, tx.Rollback(err)
		}
	} else if run.Phase == RunFinalizing {
		updated, err := tx.connection.ExecContext(ctx, `UPDATE runs SET runner_exit_sequence = ?, runner_exit_code = ?, runner_exit_signal = ?, runner_exit_at_ms = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND phase = 'finalizing' AND runner_exit_sequence IS NULL AND revision = ?`, exit.sequence, code, signal, exit.at.Int64(), at.Int64(), run.ID.Bytes(), expected.Int64())
		if err := requireOneRow(updated, err); err != nil {
			return Run{}, tx.Rollback(err)
		}
	} else {
		return Run{}, tx.Rollback(ErrConflict)
	}
	if err := appendInvalidations(ctx, tx.connection, at, []pendingInvalidation{{kind: EntityRun, id: run.ID.Bytes(), revision: newRevision}}); err != nil {
		return Run{}, tx.Rollback(err)
	}
	run, found, err = runByID(ctx, tx.connection, run.ID)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return Run{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Run{}, err
	}
	return run, nil
}

func (store *Store) FinalizeRun(ctx context.Context, runID RunID, expected Revision, at UnixMillis) (Run, error) {
	if runID.zero() {
		return Run{}, fmt.Errorf("%w: zero run identifier", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return Run{}, err
	}
	defer tx.Close()
	run, found, err := runByID(ctx, tx.connection, runID)
	if err != nil {
		return Run{}, tx.Rollback(err)
	}
	if !found {
		return Run{}, tx.Rollback(ErrNotFound)
	}
	relationships, err := loadRunRelationships(ctx, tx.connection, run)
	if err != nil {
		return Run{}, tx.Rollback(err)
	}
	if run.Phase == RunTerminal && run.Revision.Int64() > expected.Int64() {
		if run.Proposal == nil || run.Terminal == nil || !run.Terminal.equal(*run.Proposal) {
			return Run{}, tx.Rollback(ErrConflict)
		}
		if err := tx.Rollback(nil); err != nil {
			return Run{}, err
		}
		return run, nil
	}
	if run.Phase != RunFinalizing || run.Proposal == nil || run.CredentialRevokedAt == nil || run.Revision != expected || at.Int64() < run.UpdatedAt.Int64() {
		return Run{}, tx.Rollback(ErrRevisionConflict)
	}
	if run.Role == RoleWorker && run.VerificationPolicy != VerificationNone && run.Proposal.kind == OutcomeSucceeded {
		return Run{}, tx.Rollback(ErrConflict)
	}
	terminal := *run.Proposal
	resources := relationships.resources
	if !exactResourceSet(resources, false) {
		return Run{}, tx.Rollback(ErrConflict)
	}
	requiresExit := run.RunningAt != nil
	for _, resource := range resources {
		if resource.Kind == ResourceRunnerProcess && !resource.Identity.Empty() {
			requiresExit = true
		}
		if resource.State != ResourceReleased {
			return Run{}, tx.Rollback(ErrConflict)
		}
	}
	if requiresExit && run.RunnerExit == nil {
		return Run{}, tx.Rollback(ErrConflict)
	}
	task := relationships.task
	taskRevision := task.Revision.Int64() + 1
	var taskStatus string
	var blocked, result, completed any
	switch terminal.kind {
	case OutcomeSucceeded:
		taskStatus, result, completed = TaskSucceeded.String(), terminal.result, at.Int64()
	case OutcomeBlocked:
		taskStatus, blocked = TaskBlocked.String(), terminal.detail
	case OutcomeFailed:
		taskStatus, completed = TaskFailed.String(), at.Int64()
	case OutcomeCancelled:
		taskStatus, completed = TaskCancelled.String(), at.Int64()
	default:
		return Run{}, tx.Rollback(ErrCorruptState)
	}
	updated, err := tx.connection.ExecContext(ctx, `UPDATE tasks SET status = ?, blocked_reason = ?, result = ?, completed_at_ms = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND project_id = ? AND incarnation_id = ? AND work_revision = ? AND status = 'running' AND revision = ?`,
		taskStatus, blocked, result, completed, at.Int64(), task.ID.Bytes(), run.ProjectID.Bytes(), run.TaskIncarnationID.Bytes(), run.AdmittedTaskWorkRevision.Int64(), task.Revision.Int64())
	if err := requireOneRow(updated, err); err != nil {
		return Run{}, tx.Rollback(err)
	}
	terminalKind, terminalCode, terminalDetail, terminalResult := proposalSQL(terminal)
	updated, err = tx.connection.ExecContext(ctx, `UPDATE runs SET phase = 'terminal', terminal_kind = ?, terminal_code = ?, terminal_detail = ?, terminal_result = ?, terminal_at_ms = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND phase = 'finalizing' AND proposal_kind IS NOT NULL AND credential_revoked_at_ms IS NOT NULL AND revision = ?`, terminalKind, terminalCode, terminalDetail, terminalResult, at.Int64(), at.Int64(), run.ID.Bytes(), expected.Int64())
	if err := requireOneRow(updated, err); err != nil {
		return Run{}, tx.Rollback(err)
	}
	factory, err := factoryState(ctx, tx.connection)
	if err != nil {
		return Run{}, tx.Rollback(err)
	}
	factoryRevision := factory.Revision.Int64() + 1
	updated, err = tx.connection.ExecContext(ctx, `UPDATE factory SET revision = revision + 1, updated_at_ms = ? WHERE singleton = 1 AND revision = ?`, at.Int64(), factory.Revision.Int64())
	if err := requireOneRow(updated, err); err != nil {
		return Run{}, tx.Rollback(err)
	}
	if err := appendInvalidations(ctx, tx.connection, at, []pendingInvalidation{
		{kind: EntityFactory, id: factoryEntityID[:], revision: factoryRevision},
		{kind: EntityTask, id: task.ID.Bytes(), revision: taskRevision},
		{kind: EntityRun, id: run.ID.Bytes(), revision: expected.Int64() + 1},
	}); err != nil {
		return Run{}, tx.Rollback(err)
	}
	run, found, err = runByID(ctx, tx.connection, run.ID)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return Run{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Run{}, err
	}
	return run, nil
}

func exactResourceSet(resources []Resource, requireActive bool) bool {
	if len(resources) != 4 {
		return false
	}
	seen := map[ResourceKind]bool{}
	for _, resource := range resources {
		if resource.Kind.String() == "" || seen[resource.Kind] || requireActive && resource.State != ResourceActive {
			return false
		}
		seen[resource.Kind] = true
	}
	return seen[ResourceRuntimeRoot] && seen[ResourceRunnerProcess] && seen[ResourceProviderProcess] && seen[ResourceProviderGroup]
}

func proposalSQL(proposal Proposal) (kind string, code, detail, result any) {
	kind = proposal.kind.String()
	if proposal.code != 0 {
		code = proposal.code.String()
	}
	if proposal.detail != "" {
		detail = proposal.detail
	}
	if proposal.kind == OutcomeSucceeded {
		result = proposal.result
	}
	return
}

func exitSQL(exit RunnerExit) (code, signal any) {
	if exit.code != nil {
		code = *exit.code
	}
	if exit.signal != nil {
		signal = *exit.signal
	}
	return
}
