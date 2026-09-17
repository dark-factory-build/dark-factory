package kernel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const changeColumns = `id, project_id, task_id, task_incarnation_id, phase,
    object_format, base_commit, repository_dev, repository_inode, tree_digest, entry_count, total_bytes, tree_dev, tree_inode,
    prepared_at_ms, available_at_ms, settled_run_id, revision, created_at_ms, updated_at_ms`

func changeByID(ctx context.Context, connection *sql.Conn, id ChangeID) (Change, bool, error) {
	if id.zero() {
		return Change{}, false, fmt.Errorf("%w: zero Change identifier", ErrInvalidValue)
	}
	return scanChange(connection.QueryRowContext(ctx, `SELECT `+changeColumns+` FROM changes WHERE id = ?`, id.Bytes()))
}

func changeForTask(ctx context.Context, connection *sql.Conn, task Task) (Change, bool, error) {
	rows, err := connection.QueryContext(ctx, `SELECT `+changeColumns+` FROM changes WHERE project_id = ? AND task_id = ? AND task_incarnation_id = ?`, task.ProjectID.Bytes(), task.ID.Bytes(), task.IncarnationID.Bytes())
	if err != nil {
		return Change{}, false, err
	}
	if !rows.Next() {
		err := errors.Join(rows.Err(), rows.Close())
		return Change{}, false, err
	}
	change, found, err := scanChange(rows)
	if err != nil || !found {
		rows.Close()
		if err == nil {
			err = ErrCorruptState
		}
		return Change{}, false, err
	}
	if rows.Next() {
		rows.Close()
		return Change{}, false, fmt.Errorf("%w: multiple canonical Changes", ErrCorruptState)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return Change{}, false, err
	}
	return change, true, nil
}

func scanChange(scanner rowScanner) (Change, bool, error) {
	var rawID, rawProjectID, rawTaskID, rawIncarnationID []byte
	var phase string
	var objectFormat sql.NullString
	var baseCommit, treeDigest nullableBlob
	var repositoryDev, repositoryInode, entryCount, totalBytes, treeDev, treeInode sql.NullInt64
	var preparedAt, availableAt sql.NullInt64
	var rawSettledRunID nullableBlob
	var revision, createdAt, updatedAt int64
	if err := scanner.Scan(
		&rawID, &rawProjectID, &rawTaskID, &rawIncarnationID, &phase,
		&objectFormat, &baseCommit, &repositoryDev, &repositoryInode, &treeDigest, &entryCount, &totalBytes, &treeDev, &treeInode,
		&preparedAt, &availableAt, &rawSettledRunID, &revision, &createdAt, &updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Change{}, false, nil
		}
		return Change{}, false, fmt.Errorf("scan Change: %w", err)
	}
	id, idErr := ChangeIDFromBytes(rawID)
	projectID, projectErr := ProjectIDFromBytes(rawProjectID)
	taskID, taskErr := TaskIDFromBytes(rawTaskID)
	incarnationID, incarnationErr := IncarnationIDFromBytes(rawIncarnationID)
	parsedPhase, phaseErr := parseChangePhase(phase)
	rev, revisionErr := NewRevision(revision)
	created, createdErr := NewUnixMillis(createdAt)
	updated, updatedErr := NewUnixMillis(updatedAt)
	if idErr != nil || projectErr != nil || taskErr != nil || incarnationErr != nil || phaseErr != nil || revisionErr != nil || createdErr != nil || updatedErr != nil || updatedAt < createdAt {
		return Change{}, false, fmt.Errorf("%w: invalid Change row", ErrCorruptState)
	}
	result := Change{
		ID: id, ProjectID: projectID, TaskID: taskID, TaskIncarnationID: incarnationID,
		Phase: parsedPhase, Revision: rev, CreatedAt: created, UpdatedAt: updated,
	}
	preparedFields := objectFormat.Valid || baseCommit.valid || repositoryDev.Valid || repositoryInode.Valid || treeDigest.valid || entryCount.Valid || totalBytes.Valid || treeDev.Valid || treeInode.Valid || preparedAt.Valid
	if preparedFields {
		if !objectFormat.Valid || !baseCommit.valid || !repositoryDev.Valid || !repositoryInode.Valid || !treeDigest.valid || len(treeDigest.bytes) != DigestBytes || !entryCount.Valid || !totalBytes.Valid || !treeDev.Valid || !treeInode.Valid || !preparedAt.Valid || entryCount.Int64 < 0 || entryCount.Int64 > MaxChangeTreeEntries || totalBytes.Int64 < 0 || totalBytes.Int64 > MaxChangeTreeBlobBytes {
			return Change{}, false, fmt.Errorf("%w: partial prepared Change", ErrCorruptState)
		}
		format, formatErr := parseObjectFormat(objectFormat.String)
		commit, commitErr := NewCommitID(format, baseCommit.bytes)
		commitment, commitmentErr := TreeDigestFromBytes(treeDigest.bytes)
		repository, repositoryErr := NewFileIdentity(repositoryDev.Int64, repositoryInode.Int64)
		selection, selectionErr := NewChangeSelection(format, commit, commitment, uint32(entryCount.Int64), uint64(totalBytes.Int64), repository)
		tree, treeErr := NewFileIdentity(treeDev.Int64, treeInode.Int64)
		prepared, preparedErr := NewUnixMillis(preparedAt.Int64)
		if formatErr != nil || commitErr != nil || commitmentErr != nil || repositoryErr != nil || selectionErr != nil || treeErr != nil || preparedErr != nil || preparedAt.Int64 < createdAt || preparedAt.Int64 > updatedAt {
			return Change{}, false, fmt.Errorf("%w: invalid prepared Change", ErrCorruptState)
		}
		result.Selection = &selection
		result.TreeIdentity = &tree
		result.PreparedAt = &prepared
	}
	if availableAt.Valid {
		available, err := NewUnixMillis(availableAt.Int64)
		if err != nil || result.PreparedAt == nil || availableAt.Int64 < preparedAt.Int64 || availableAt.Int64 > updatedAt {
			return Change{}, false, fmt.Errorf("%w: invalid available Change", ErrCorruptState)
		}
		result.AvailableAt = &available
	}
	if rawSettledRunID.valid {
		settled, err := RunIDFromBytes(rawSettledRunID.bytes)
		if err != nil {
			return Change{}, false, fmt.Errorf("%w: invalid settled Run identifier", ErrCorruptState)
		}
		result.SettledRunID = &settled
	}
	switch parsedPhase {
	case ChangeReserved:
		if result.Selection != nil || result.AvailableAt != nil || result.SettledRunID != nil {
			return Change{}, false, fmt.Errorf("%w: inconsistent reserved Change", ErrCorruptState)
		}
	case ChangePrepared:
		if result.Selection == nil || result.AvailableAt != nil || result.SettledRunID != nil {
			return Change{}, false, fmt.Errorf("%w: inconsistent prepared Change", ErrCorruptState)
		}
	case ChangeAvailable:
		if result.Selection == nil || result.AvailableAt == nil || result.SettledRunID != nil {
			return Change{}, false, fmt.Errorf("%w: inconsistent available Change", ErrCorruptState)
		}
	case ChangeRetained:
		if result.Selection == nil || result.AvailableAt == nil || result.SettledRunID == nil {
			return Change{}, false, fmt.Errorf("%w: inconsistent retained Change", ErrCorruptState)
		}
	case ChangeAbandoned:
		if result.Selection != nil || result.AvailableAt != nil || result.SettledRunID == nil {
			return Change{}, false, fmt.Errorf("%w: inconsistent abandoned Change", ErrCorruptState)
		}
	}
	return result, true, nil
}

func (store *Store) Change(ctx context.Context, id ChangeID) (Change, bool, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return Change{}, false, err
	}
	defer tx.Close()
	return changeByID(ctx, tx.connection, id)
}

func (store *Store) RecordChangePrepared(ctx context.Context, id ChangeID, expected Revision, selection ChangeSelection, tree FileIdentity, at UnixMillis) (Change, error) {
	if id.zero() || !selection.valid() || !tree.valid() {
		return Change{}, fmt.Errorf("%w: invalid prepared Change request", ErrInvalidValue)
	}
	return store.advanceChange(ctx, id, expected, at, func(change Change, connection *sql.Conn) (bool, error) {
		if change.Phase == ChangePrepared && change.Revision.Int64() == expected.Int64()+1 && change.Selection != nil && change.TreeIdentity != nil && changeSelectionEqual(*change.Selection, selection) && *change.TreeIdentity == tree {
			return true, nil
		}
		if change.Phase != ChangeReserved || change.Revision != expected || at.Int64() < change.CreatedAt.Int64() {
			return false, ErrRevisionConflict
		}
		if err := requireChangeMutationOwner(ctx, connection, change, expected, at); err != nil {
			return false, err
		}
		result, err := connection.ExecContext(ctx, `UPDATE changes SET phase = 'prepared', object_format = ?, base_commit = ?, repository_dev = ?, repository_inode = ?, tree_digest = ?, entry_count = ?, total_bytes = ?, tree_dev = ?, tree_inode = ?, prepared_at_ms = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND phase = 'reserved' AND revision = ?`,
			selection.format.String(), selection.commit.Bytes(), selection.repository.device, selection.repository.inode, selection.commitment.Bytes(), int64(selection.entries), int64(selection.bytes), tree.device, tree.inode, at.Int64(), at.Int64(), id.Bytes(), expected.Int64())
		return false, requireOneRow(result, err)
	})
}

func (store *Store) MarkChangeAvailable(ctx context.Context, id ChangeID, expected Revision, available ChangeAvailability, at UnixMillis) (Change, error) {
	if id.zero() || !available.valid() {
		return Change{}, fmt.Errorf("%w: invalid available Change request", ErrInvalidValue)
	}
	return store.advanceChange(ctx, id, expected, at, func(change Change, connection *sql.Conn) (bool, error) {
		if change.Phase == ChangeAvailable && change.Revision.Int64() == expected.Int64()+1 && changeAvailabilityMatches(change, available) {
			return true, nil
		}
		if change.Phase != ChangePrepared || change.Revision != expected || change.TreeIdentity == nil || *change.TreeIdentity != available.tree || at.Int64() < change.UpdatedAt.Int64() || !changeAvailabilityMatches(change, available) {
			return false, ErrRevisionConflict
		}
		if err := requireChangeMutationOwner(ctx, connection, change, expected, at); err != nil {
			return false, err
		}
		result, err := connection.ExecContext(ctx, `UPDATE changes SET phase = 'available', available_at_ms = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND phase = 'prepared' AND revision = ? AND tree_dev = ? AND tree_inode = ?`,
			at.Int64(), at.Int64(), id.Bytes(), expected.Int64(), available.tree.device, available.tree.inode)
		return false, requireOneRow(result, err)
	})
}

func (store *Store) advanceChange(ctx context.Context, id ChangeID, expected Revision, at UnixMillis, apply func(Change, *sql.Conn) (bool, error)) (Change, error) {
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return Change{}, err
	}
	defer tx.Close()
	change, found, err := changeByID(ctx, tx.connection, id)
	if err != nil {
		return Change{}, tx.Rollback(err)
	}
	if !found {
		return Change{}, tx.Rollback(ErrNotFound)
	}
	replayed, err := apply(change, tx.connection)
	if err != nil {
		return Change{}, tx.Rollback(err)
	}
	if replayed {
		if err := tx.Rollback(nil); err != nil {
			return Change{}, err
		}
		return change, nil
	}
	newRevision := expected.Int64() + 1
	if err := appendInvalidations(ctx, tx.connection, at, []pendingInvalidation{{kind: EntityChange, id: id.Bytes(), revision: newRevision}}); err != nil {
		return Change{}, tx.Rollback(err)
	}
	change, found, err = changeByID(ctx, tx.connection, id)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return Change{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Change{}, err
	}
	return change, nil
}

func requireChangeMutationOwner(ctx context.Context, connection *sql.Conn, change Change, expected Revision, at UnixMillis) error {
	run, found, err := scanRun(connection.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE change_id = ? AND phase <> 'terminal'`, change.ID.Bytes()))
	if err != nil {
		return err
	}
	if !found {
		return ErrConflict
	}
	if _, err := classifyWorkerChangeOwnership(ctx, connection, run, change); err != nil {
		return err
	}
	if run.Phase != RunAdmitted || run.RunningAt != nil || change.Revision != expected || at.Int64() < run.UpdatedAt.Int64() {
		return ErrConflict
	}
	return nil
}

func changeSelectionEqual(left, right ChangeSelection) bool {
	return left.format == right.format && left.commit.equal(right.commit) && left.commitment == right.commitment && left.entries == right.entries && left.bytes == right.bytes && left.repository == right.repository
}

func changeAvailabilityMatches(change Change, available ChangeAvailability) bool {
	return change.Selection != nil && change.TreeIdentity != nil && change.Selection.commitment == available.commitment && change.Selection.entries == available.entries && change.Selection.bytes == available.bytes && *change.TreeIdentity == available.tree
}

func requireOneRow(result sql.Result, err error) error {
	return requireRows(result, err, 1)
}

func requireRows(result sql.Result, err error, want int64) error {
	if err != nil {
		return err
	}
	if result == nil || want < 1 {
		return ErrRevisionConflict
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != want {
		return ErrRevisionConflict
	}
	return nil
}
