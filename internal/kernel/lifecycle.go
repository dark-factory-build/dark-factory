package kernel

import (
	"context"
	"database/sql"
	"fmt"
)

func (store *Store) ActivateResource(ctx context.Context, runID RunID, resourceID ResourceID, expected Revision, identity ResourceIdentity, at UnixMillis) (Resource, error) {
	return store.transitionResource(ctx, runID, resourceID, expected, identity, "", ResourceActive, at)
}

// ActivateProviderResources binds the one provider process/group authority in
// one transaction. Neither resource may acquire an identity independently.
func (store *Store) ActivateProviderResources(ctx context.Context, runID RunID, processID ResourceID, processExpected Revision, groupID ResourceID, groupExpected Revision, identity ResourceIdentity, at UnixMillis) (Resource, Resource, error) {
	if runID.zero() || processID.zero() || groupID.zero() || processID == groupID || !identity.validFor(ResourceProviderProcess) {
		return Resource{}, Resource{}, fmt.Errorf("%w: invalid provider resource activation", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return Resource{}, Resource{}, err
	}
	defer tx.Close()
	run, found, err := runByID(ctx, tx.connection, runID)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return Resource{}, Resource{}, tx.Rollback(err)
	}
	process, processFound, err := resourceByID(ctx, tx.connection, processID)
	if err != nil {
		return Resource{}, Resource{}, tx.Rollback(err)
	}
	group, groupFound, err := resourceByID(ctx, tx.connection, groupID)
	if err != nil {
		return Resource{}, Resource{}, tx.Rollback(err)
	}
	if !processFound || !groupFound {
		return Resource{}, Resource{}, tx.Rollback(ErrNotFound)
	}
	if process.RunID != runID || process.Kind != ResourceProviderProcess || group.RunID != runID || group.Kind != ResourceProviderGroup {
		return Resource{}, Resource{}, tx.Rollback(ErrConflict)
	}
	if (process.ActivatedAt == nil) != (group.ActivatedAt == nil) || process.ActivatedAt != nil && *process.ActivatedAt != *group.ActivatedAt {
		return Resource{}, Resource{}, tx.Rollback(ErrCorruptState)
	}
	if process.State == ResourceActive && group.State == ResourceActive && resourceIdentityEqual(process.Identity, identity) && resourceIdentityEqual(group.Identity, identity) && process.Revision.Int64() == processExpected.Int64()+1 && group.Revision.Int64() == groupExpected.Int64()+1 {
		if err := tx.Rollback(nil); err != nil {
			return Resource{}, Resource{}, err
		}
		return process, group, nil
	}
	if run.Phase != RunAdmitted || process.State != ResourceDeclared || group.State != ResourceDeclared || process.Revision != processExpected || group.Revision != groupExpected || at.Int64() < process.UpdatedAt.Int64() || at.Int64() < group.UpdatedAt.Int64() {
		return Resource{}, Resource{}, tx.Rollback(ErrRevisionConflict)
	}
	if err := ensureResourceIdentityUnused(ctx, tx.connection, process, identity); err != nil {
		return Resource{}, Resource{}, tx.Rollback(err)
	}
	pid, pgid, birth, _ := identity.Process()
	updated, err := tx.connection.ExecContext(ctx, `UPDATE resources SET state = 'active', pid = ?, pgid = ?, birth_digest = ?, activated_at_ms = ?, revision = revision + 1, updated_at_ms = ? WHERE run_id = ? AND state = 'declared' AND pid IS NULL AND pgid IS NULL AND birth_digest IS NULL AND activated_at_ms IS NULL AND ((id = ? AND kind = 'provider_process' AND revision = ?) OR (id = ? AND kind = 'provider_group' AND revision = ?))`,
		pid, pgid, birth.Bytes(), at.Int64(), at.Int64(), runID.Bytes(), processID.Bytes(), processExpected.Int64(), groupID.Bytes(), groupExpected.Int64())
	if err := requireRows(updated, err, 2); err != nil {
		return Resource{}, Resource{}, tx.Rollback(err)
	}
	process, processFound, err = resourceByID(ctx, tx.connection, processID)
	if err != nil || !processFound {
		if err == nil {
			err = ErrCorruptState
		}
		return Resource{}, Resource{}, tx.Rollback(err)
	}
	group, groupFound, err = resourceByID(ctx, tx.connection, groupID)
	if err != nil || !groupFound {
		if err == nil {
			err = ErrCorruptState
		}
		return Resource{}, Resource{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Resource{}, Resource{}, err
	}
	return process, group, nil
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
	if target == ResourceActive && (resource.Kind == ResourceProviderProcess || resource.Kind == ResourceProviderGroup) {
		return Resource{}, tx.Rollback(ErrConflict)
	}
	run, found, err := runByID(ctx, tx.connection, runID)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return Resource{}, tx.Rollback(err)
	}
	if target == ResourceReleased && !resource.Identity.Empty() {
		switch resource.Kind {
		case ResourceProviderProcess, ResourceProviderGroup:
			if run.ProviderExit == nil {
				return Resource{}, tx.Rollback(ErrConflict)
			}
		case ResourceRunnerProcess:
			if run.RunnerExit == nil {
				return Resource{}, tx.Rollback(ErrConflict)
			}
		}
	}
	if resource.State == target && resourceIdentityEqual(resource.Identity, identity) && resource.UnresolvedReason == reason && resource.Revision.Int64() == expected.Int64()+1 {
		if err := tx.Rollback(nil); err != nil {
			return Resource{}, err
		}
		return resource, nil
	}
	if target == ResourceReleased && !resource.Identity.Empty() {
		switch resource.Kind {
		case ResourceProviderProcess, ResourceProviderGroup:
			if at.Int64() < run.ProviderExit.At().Int64() {
				return Resource{}, tx.Rollback(ErrRevisionConflict)
			}
		case ResourceRunnerProcess:
			if at.Int64() < run.RunnerExit.At().Int64() {
				return Resource{}, tx.Rollback(ErrRevisionConflict)
			}
		}
	}
	if resource.Revision != expected || at.Int64() < resource.UpdatedAt.Int64() {
		return Resource{}, tx.Rollback(ErrRevisionConflict)
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
			result, err = tx.connection.ExecContext(ctx, `UPDATE resources SET state = 'active', path_dev = ?, path_inode = ?, activated_at_ms = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND run_id = ? AND state = 'declared' AND revision = ? AND path_dev IS NULL AND path_inode IS NULL AND activated_at_ms IS NULL`, pathIdentity.device, pathIdentity.inode, at.Int64(), at.Int64(), resourceID.Bytes(), runID.Bytes(), expected.Int64())
		} else {
			pid, pgid, birth, _ := identity.Process()
			result, err = tx.connection.ExecContext(ctx, `UPDATE resources SET state = 'active', pid = ?, pgid = ?, birth_digest = ?, activated_at_ms = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND run_id = ? AND state = 'declared' AND revision = ? AND pid IS NULL AND pgid IS NULL AND birth_digest IS NULL AND activated_at_ms IS NULL`, pid, pgid, birth.Bytes(), at.Int64(), at.Int64(), resourceID.Bytes(), runID.Bytes(), expected.Int64())
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
	if err := requireResourceTime(resources, at); err != nil {
		return Run{}, tx.Rollback(err)
	}
	if run.Role == RoleWorker && (relationships.change.AvailableAt == nil || relationships.change.AvailableAt.Int64() > at.Int64() || relationships.change.UpdatedAt.Int64() > at.Int64()) {
		return Run{}, tx.Rollback(ErrRevisionConflict)
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

// FailRun records a daemon-owned infrastructure failure before or during a
// running attempt. It never acts as attempt authority and never overwrites an
// outcome that reached finalizing first.
func (store *Store) FailRun(ctx context.Context, runID RunID, expected Revision, failure Proposal, at UnixMillis) (Run, error) {
	if runID.zero() || !failure.valid() || failure.kind != OutcomeFailed {
		return Run{}, fmt.Errorf("%w: invalid run failure", ErrInvalidValue)
	}
	switch failure.code {
	case FailureSpawn, FailureActivation, FailureSource, FailureProtocol, FailureInternal:
	default:
		return Run{}, fmt.Errorf("%w: failure code is not daemon infrastructure authority", ErrInvalidValue)
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
	if run.Phase == RunFinalizing && run.Proposal != nil && run.Proposal.equal(failure) && run.Revision.Int64() == expected.Int64()+1 {
		if err := tx.Rollback(nil); err != nil {
			return Run{}, err
		}
		return run, nil
	}
	if run.Phase != RunAdmitted && run.Phase != RunRunning {
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
	if err := requireFinalizingTime(ctx, tx.connection, run, at); err != nil {
		return Run{}, tx.Rollback(err)
	}
	kind, code, detail, result := proposalSQL(proposal)
	updated, err := tx.connection.ExecContext(ctx, `UPDATE runs SET phase = 'finalizing', proposal_kind = ?, proposal_code = ?, proposal_detail = ?, proposal_result = ?, credential_revoked_at_ms = ?, finalizing_at_ms = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND phase IN ('admitted', 'running') AND proposal_kind IS NULL AND credential_revoked_at_ms IS NULL AND revision = ?`,
		kind, code, detail, result, at.Int64(), at.Int64(), at.Int64(), run.ID.Bytes(), expected.Int64())
	if err := requireOneRow(updated, err); err != nil {
		return Run{}, tx.Rollback(err)
	}
	releasing, err := tx.connection.ExecContext(ctx, `UPDATE resources SET state = 'releasing', revision = revision + 1, updated_at_ms = ? WHERE run_id = ? AND state IN ('declared', 'active')`, at.Int64(), run.ID.Bytes())
	if err := requireRows(releasing, err, 4); err != nil {
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

type processExitOwner uint8

const (
	providerExitOwner processExitOwner = iota + 1
	runnerExitOwner
)

func (store *Store) ObserveProviderExit(ctx context.Context, runID RunID, expected Revision, identity ResourceIdentity, exit ProcessExit, at UnixMillis) (Run, error) {
	return store.observeProcessExit(ctx, runID, expected, identity, exit, at, providerExitOwner)
}

func (store *Store) ObserveRunnerExit(ctx context.Context, runID RunID, expected Revision, identity ResourceIdentity, exit ProcessExit, at UnixMillis) (Run, error) {
	return store.observeProcessExit(ctx, runID, expected, identity, exit, at, runnerExitOwner)
}

func (store *Store) observeProcessExit(ctx context.Context, runID RunID, expected Revision, identity ResourceIdentity, exit ProcessExit, at UnixMillis, owner processExitOwner) (Run, error) {
	wantKind := ResourceRunnerProcess
	if owner == providerExitOwner {
		wantKind = ResourceProviderProcess
	}
	if runID.zero() || !identity.validFor(wantKind) || !exit.valid() || at.Int64() < exit.at.Int64() {
		return Run{}, fmt.Errorf("%w: invalid process exit observation", ErrInvalidValue)
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
	if exit.at.Int64() < run.AdmittedAt.Int64() {
		return Run{}, tx.Rollback(ErrInvalidValue)
	}
	resources, err := resourcesForRun(ctx, tx.connection, runID)
	if err != nil {
		return Run{}, tx.Rollback(err)
	}
	activatedAt, matched := exitIdentityMatches(resources, owner, identity)
	if !matched {
		return Run{}, tx.Rollback(ErrConflict)
	}
	if exit.at.Int64() < activatedAt.Int64() {
		return Run{}, tx.Rollback(ErrConflict)
	}
	existing := run.RunnerExit
	if owner == providerExitOwner {
		existing = run.ProviderExit
	}
	if existing != nil {
		if !existing.equal(exit) {
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
	exitKind, code, signal := exitSQL(exit)
	newRevision := expected.Int64() + 1
	if run.Phase == RunAdmitted || run.Phase == RunRunning {
		if err := requireFinalizingTime(ctx, tx.connection, run, at); err != nil {
			return Run{}, tx.Rollback(err)
		}
		failureCode, detail := FailureRunnerExit, "runner exited before an attempt outcome"
		if owner == providerExitOwner {
			failureCode, detail = FailureProviderExit, "provider exited before an attempt outcome"
		}
		proposal, _ := NewFailureProposal(failureCode, detail)
		kind, proposalCode, proposalDetail, result := proposalSQL(proposal)
		var updated sql.Result
		if owner == providerExitOwner {
			updated, err = tx.connection.ExecContext(ctx, `UPDATE runs SET phase = 'finalizing', proposal_kind = ?, proposal_code = ?, proposal_detail = ?, proposal_result = ?, credential_revoked_at_ms = ?, finalizing_at_ms = ?, provider_exit_kind = ?, provider_exit_sequence = ?, provider_exit_code = ?, provider_exit_signal = ?, provider_exit_at_ms = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND phase IN ('admitted', 'running') AND proposal_kind IS NULL AND provider_exit_kind IS NULL AND revision = ?`,
				kind, proposalCode, proposalDetail, result, at.Int64(), at.Int64(), exitKind, exit.sequence, code, signal, exit.at.Int64(), at.Int64(), run.ID.Bytes(), expected.Int64())
		} else {
			updated, err = tx.connection.ExecContext(ctx, `UPDATE runs SET phase = 'finalizing', proposal_kind = ?, proposal_code = ?, proposal_detail = ?, proposal_result = ?, credential_revoked_at_ms = ?, finalizing_at_ms = ?, runner_exit_kind = ?, runner_exit_sequence = ?, runner_exit_code = ?, runner_exit_signal = ?, runner_exit_at_ms = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND phase IN ('admitted', 'running') AND proposal_kind IS NULL AND runner_exit_kind IS NULL AND revision = ?`,
				kind, proposalCode, proposalDetail, result, at.Int64(), at.Int64(), exitKind, exit.sequence, code, signal, exit.at.Int64(), at.Int64(), run.ID.Bytes(), expected.Int64())
		}
		if err := requireOneRow(updated, err); err != nil {
			return Run{}, tx.Rollback(err)
		}
		releasing, err := tx.connection.ExecContext(ctx, `UPDATE resources SET state = 'releasing', revision = revision + 1, updated_at_ms = ? WHERE run_id = ? AND state IN ('declared', 'active')`, at.Int64(), run.ID.Bytes())
		if err := requireRows(releasing, err, 4); err != nil {
			return Run{}, tx.Rollback(err)
		}
	} else if run.Phase == RunFinalizing {
		var updated sql.Result
		if owner == providerExitOwner {
			updated, err = tx.connection.ExecContext(ctx, `UPDATE runs SET provider_exit_kind = ?, provider_exit_sequence = ?, provider_exit_code = ?, provider_exit_signal = ?, provider_exit_at_ms = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND phase = 'finalizing' AND provider_exit_kind IS NULL AND revision = ?`, exitKind, exit.sequence, code, signal, exit.at.Int64(), at.Int64(), run.ID.Bytes(), expected.Int64())
		} else {
			updated, err = tx.connection.ExecContext(ctx, `UPDATE runs SET runner_exit_kind = ?, runner_exit_sequence = ?, runner_exit_code = ?, runner_exit_signal = ?, runner_exit_at_ms = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND phase = 'finalizing' AND runner_exit_kind IS NULL AND revision = ?`, exitKind, exit.sequence, code, signal, exit.at.Int64(), at.Int64(), run.ID.Bytes(), expected.Int64())
		}
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

func exitIdentityMatches(resources []Resource, owner processExitOwner, identity ResourceIdentity) (UnixMillis, bool) {
	runnerMatched := false
	providerProcessMatched := false
	providerGroupMatched := false
	var activatedAt UnixMillis
	var activationFound bool
	for _, resource := range resources {
		if resource.State == ResourceDeclared || !resourceIdentityEqual(resource.Identity, identity) {
			continue
		}
		matched := false
		switch resource.Kind {
		case ResourceRunnerProcess:
			runnerMatched = true
			matched = owner == runnerExitOwner
		case ResourceProviderProcess:
			providerProcessMatched = true
			matched = owner == providerExitOwner
		case ResourceProviderGroup:
			providerGroupMatched = true
			matched = owner == providerExitOwner
		}
		if matched {
			if resource.ActivatedAt == nil || activationFound && *resource.ActivatedAt != activatedAt {
				return UnixMillis{}, false
			}
			activatedAt = *resource.ActivatedAt
			activationFound = true
		}
	}
	if owner == providerExitOwner {
		return activatedAt, providerProcessMatched && providerGroupMatched && activationFound
	}
	return activatedAt, runnerMatched && activationFound
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
	if err := requireResourceTime(relationships.resources, at); err != nil {
		return Run{}, tx.Rollback(err)
	}
	if relationships.task.UpdatedAt.Int64() > at.Int64() {
		return Run{}, tx.Rollback(ErrRevisionConflict)
	}
	if relationships.change != nil && relationships.change.UpdatedAt.Int64() > at.Int64() {
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
	requiresProviderExit := false
	requiresRunnerExit := false
	for _, resource := range resources {
		if !resource.Identity.Empty() {
			switch resource.Kind {
			case ResourceProviderProcess, ResourceProviderGroup:
				requiresProviderExit = true
			case ResourceRunnerProcess:
				requiresRunnerExit = true
			}
		}
		if resource.State != ResourceReleased {
			return Run{}, tx.Rollback(ErrConflict)
		}
	}
	if requiresProviderExit && run.ProviderExit == nil || requiresRunnerExit && run.RunnerExit == nil {
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

func requireResourceTime(resources []Resource, at UnixMillis) error {
	for _, resource := range resources {
		if resource.UpdatedAt.Int64() > at.Int64() {
			return ErrRevisionConflict
		}
	}
	return nil
}

func requireFinalizingTime(ctx context.Context, connection *sql.Conn, run Run, at UnixMillis) error {
	resources, err := resourcesForRun(ctx, connection, run.ID)
	if err != nil {
		return err
	}
	var change *Change
	if run.ChangeID != nil {
		value, found, err := changeByID(ctx, connection, *run.ChangeID)
		if err != nil {
			return err
		}
		if !found {
			return ErrCorruptState
		}
		change = &value
	}
	if err := requireResourceTime(resources, at); err != nil {
		return err
	}
	if change != nil && change.UpdatedAt.Int64() > at.Int64() {
		return ErrRevisionConflict
	}
	return nil
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

func exitSQL(exit ProcessExit) (kind string, code, signal any) {
	kind = exit.kind.String()
	if exit.code != nil {
		code = *exit.code
	}
	if exit.signal != nil {
		signal = *exit.signal
	}
	return
}
