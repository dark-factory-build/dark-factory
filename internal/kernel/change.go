package kernel

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const changeColumns = `id, project_id, task_id, task_incarnation_id, phase, source_root, staging_root,
    object_format, selected_commit, repository_root, repository_dev, repository_inode, selected_at_ms,
    stage_dev, stage_inode, prepared_at_ms, tree_digest, entry_count, total_bytes, source_dev, source_inode,
    available_at_ms, revision, created_at_ms, updated_at_ms`

func changeByID(ctx context.Context, connection *sql.Conn, id ChangeID) (Change, bool, error) {
	if id.zero() {
		return Change{}, false, fmt.Errorf("%w: zero Change identifier", ErrInvalidValue)
	}
	return scanChange(connection.QueryRowContext(ctx, `SELECT `+changeColumns+` FROM changes WHERE id = ?`, id.Bytes()))
}

func scanChange(scanner rowScanner) (Change, bool, error) {
	var rawID, rawProjectID, rawTaskID, rawIncarnationID []byte
	var phase, sourceRoot, stagingRoot string
	var objectFormat, repositoryRoot sql.NullString
	var selectedCommit, treeDigest []byte
	var repositoryDev, repositoryInode, selectedAt sql.NullInt64
	var stageDev, stageInode, preparedAt sql.NullInt64
	var entryCount, totalBytes, sourceDev, sourceInode, availableAt sql.NullInt64
	var revision, createdAt, updatedAt int64
	if err := scanner.Scan(
		&rawID, &rawProjectID, &rawTaskID, &rawIncarnationID, &phase, &sourceRoot, &stagingRoot,
		&objectFormat, &selectedCommit, &repositoryRoot, &repositoryDev, &repositoryInode, &selectedAt,
		&stageDev, &stageInode, &preparedAt, &treeDigest, &entryCount, &totalBytes, &sourceDev, &sourceInode,
		&availableAt, &revision, &createdAt, &updatedAt,
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
	if idErr != nil || projectErr != nil || taskErr != nil || incarnationErr != nil || phaseErr != nil || revisionErr != nil || createdErr != nil || updatedErr != nil || !validAbsolutePath(sourceRoot) || !validAbsolutePath(stagingRoot) || sourceRoot == stagingRoot || updatedAt < createdAt {
		return Change{}, false, fmt.Errorf("%w: invalid Change row", ErrCorruptState)
	}
	result := Change{
		ID: id, ProjectID: projectID, TaskID: taskID, TaskIncarnationID: incarnationID,
		Phase: parsedPhase, SourceRoot: sourceRoot, StagingRoot: stagingRoot,
		Revision: rev, CreatedAt: created, UpdatedAt: updated,
	}
	selectionFields := objectFormat.Valid || selectedCommit != nil || repositoryRoot.Valid || repositoryDev.Valid || repositoryInode.Valid || selectedAt.Valid
	if selectionFields {
		if !objectFormat.Valid || !repositoryRoot.Valid || !repositoryDev.Valid || !repositoryInode.Valid || !selectedAt.Valid {
			return Change{}, false, fmt.Errorf("%w: partial Change selection", ErrCorruptState)
		}
		format, err := parseObjectFormat(objectFormat.String)
		if err != nil {
			return Change{}, false, err
		}
		commit, commitErr := NewCommitID(format, selectedCommit)
		repository, repositoryErr := NewFileIdentity(repositoryDev.Int64, repositoryInode.Int64)
		selection, selectionErr := NewChangeSelection(format, commit, repositoryRoot.String, repository)
		selectedTime, selectedTimeErr := NewUnixMillis(selectedAt.Int64)
		if commitErr != nil || repositoryErr != nil || selectionErr != nil || selectedTimeErr != nil || selectedAt.Int64 < createdAt {
			return Change{}, false, fmt.Errorf("%w: invalid Change selection", ErrCorruptState)
		}
		result.Selection = &selection
		result.SelectedAt = &selectedTime
	}
	stageFields := stageDev.Valid || stageInode.Valid || preparedAt.Valid
	if stageFields {
		if !stageDev.Valid || !stageInode.Valid || !preparedAt.Valid {
			return Change{}, false, fmt.Errorf("%w: partial prepared Change", ErrCorruptState)
		}
		identity, identityErr := NewFileIdentity(stageDev.Int64, stageInode.Int64)
		preparedTime, timeErr := NewUnixMillis(preparedAt.Int64)
		if identityErr != nil || timeErr != nil || preparedAt.Int64 < selectedAt.Int64 {
			return Change{}, false, fmt.Errorf("%w: invalid prepared Change", ErrCorruptState)
		}
		result.StageIdentity = &identity
		result.PreparedAt = &preparedTime
	}
	availableFields := treeDigest != nil || entryCount.Valid || totalBytes.Valid || sourceDev.Valid || sourceInode.Valid || availableAt.Valid
	if availableFields {
		if len(treeDigest) != DigestBytes || !entryCount.Valid || !totalBytes.Valid || !sourceDev.Valid || !sourceInode.Valid || !availableAt.Valid || entryCount.Int64 < 0 || entryCount.Int64 > MaxChangeTreeEntries || totalBytes.Int64 < 0 || totalBytes.Int64 > MaxChangeTreeBlobBytes {
			return Change{}, false, fmt.Errorf("%w: partial available Change", ErrCorruptState)
		}
		digest, digestErr := TreeDigestFromBytes(treeDigest)
		source, sourceErr := NewFileIdentity(sourceDev.Int64, sourceInode.Int64)
		availability, availabilityErr := NewChangeAvailability(digest, uint32(entryCount.Int64), uint64(totalBytes.Int64), source)
		availableTime, timeErr := NewUnixMillis(availableAt.Int64)
		if digestErr != nil || sourceErr != nil || availabilityErr != nil || timeErr != nil || result.StageIdentity == nil || source != *result.StageIdentity || availableAt.Int64 < preparedAt.Int64 {
			return Change{}, false, fmt.Errorf("%w: invalid available Change", ErrCorruptState)
		}
		result.Availability = &availability
		result.AvailableAt = &availableTime
	}
	switch parsedPhase {
	case ChangeReserved:
		if result.Selection != nil || result.StageIdentity != nil || result.Availability != nil {
			return Change{}, false, fmt.Errorf("%w: inconsistent reserved Change", ErrCorruptState)
		}
	case ChangeSelected:
		if result.Selection == nil || result.StageIdentity != nil || result.Availability != nil {
			return Change{}, false, fmt.Errorf("%w: inconsistent selected Change", ErrCorruptState)
		}
	case ChangePrepared:
		if result.Selection == nil || result.StageIdentity == nil || result.Availability != nil {
			return Change{}, false, fmt.Errorf("%w: inconsistent prepared Change", ErrCorruptState)
		}
	case ChangeAvailable:
		if result.Selection == nil || result.StageIdentity == nil || result.Availability == nil {
			return Change{}, false, fmt.Errorf("%w: inconsistent available Change", ErrCorruptState)
		}
	}
	return result, true, nil
}

func (store *Store) Change(ctx context.Context, id ChangeID) (Change, bool, error) {
	connection, err := store.readerConnection(ctx)
	if err != nil {
		return Change{}, false, err
	}
	defer connection.Close()
	return changeByID(ctx, connection, id)
}

func (store *Store) RecordChangeSelection(ctx context.Context, id ChangeID, expected Revision, selection ChangeSelection, at UnixMillis) (Change, error) {
	if id.zero() || !selection.valid() {
		return Change{}, fmt.Errorf("%w: invalid Change selection request", ErrInvalidValue)
	}
	return store.advanceChange(ctx, id, expected, at, func(change Change, connection *sql.Conn) (bool, error) {
		if change.Selection != nil {
			if changeSelectionEqual(*change.Selection, selection) {
				return true, nil
			}
			return false, ErrConflict
		}
		if change.Phase != ChangeReserved || change.Revision != expected || at.Int64() < change.CreatedAt.Int64() {
			return false, ErrRevisionConflict
		}
		result, err := connection.ExecContext(ctx, `UPDATE changes SET phase = 'selected', object_format = ?, selected_commit = ?, repository_root = ?, repository_dev = ?, repository_inode = ?, selected_at_ms = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND phase = 'reserved' AND revision = ?`,
			selection.format.String(), selection.commit.Bytes(), selection.repositoryRoot, selection.repository.device, selection.repository.inode, at.Int64(), at.Int64(), id.Bytes(), expected.Int64())
		return false, requireOneRow(result, err)
	})
}

func (store *Store) RecordChangePrepared(ctx context.Context, id ChangeID, expected Revision, stage FileIdentity, at UnixMillis) (Change, error) {
	if id.zero() || !stage.valid() {
		return Change{}, fmt.Errorf("%w: invalid prepared Change request", ErrInvalidValue)
	}
	return store.advanceChange(ctx, id, expected, at, func(change Change, connection *sql.Conn) (bool, error) {
		if change.StageIdentity != nil {
			if *change.StageIdentity == stage {
				return true, nil
			}
			return false, ErrConflict
		}
		if change.Phase != ChangeSelected || change.Revision != expected || at.Int64() < change.UpdatedAt.Int64() {
			return false, ErrRevisionConflict
		}
		result, err := connection.ExecContext(ctx, `UPDATE changes SET phase = 'prepared', stage_dev = ?, stage_inode = ?, prepared_at_ms = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND phase = 'selected' AND revision = ?`,
			stage.device, stage.inode, at.Int64(), at.Int64(), id.Bytes(), expected.Int64())
		return false, requireOneRow(result, err)
	})
}

func (store *Store) MarkChangeAvailable(ctx context.Context, id ChangeID, expected Revision, available ChangeAvailability, at UnixMillis) (Change, error) {
	if id.zero() || available.entries > MaxChangeTreeEntries || available.bytes > MaxChangeTreeBlobBytes || !available.source.valid() {
		return Change{}, fmt.Errorf("%w: invalid available Change request", ErrInvalidValue)
	}
	return store.advanceChange(ctx, id, expected, at, func(change Change, connection *sql.Conn) (bool, error) {
		if change.Availability != nil {
			if changeAvailabilityEqual(*change.Availability, available) {
				return true, nil
			}
			return false, ErrConflict
		}
		if change.Phase != ChangePrepared || change.Revision != expected || change.StageIdentity == nil || *change.StageIdentity != available.source || at.Int64() < change.UpdatedAt.Int64() {
			return false, ErrRevisionConflict
		}
		result, err := connection.ExecContext(ctx, `UPDATE changes SET phase = 'available', tree_digest = ?, entry_count = ?, total_bytes = ?, source_dev = ?, source_inode = ?, available_at_ms = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND phase = 'prepared' AND revision = ? AND stage_dev = ? AND stage_inode = ?`,
			available.commitment.Bytes(), int64(available.entries), int64(available.bytes), available.source.device, available.source.inode, at.Int64(), at.Int64(), id.Bytes(), expected.Int64(), available.source.device, available.source.inode)
		return false, requireOneRow(result, err)
	})
}

func (store *Store) advanceChange(ctx context.Context, id ChangeID, expected Revision, at UnixMillis, apply func(Change, *sql.Conn) (bool, error)) (Change, error) {
	tx, err := store.beginWrite(ctx)
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
		if change.Revision.Int64() <= expected.Int64() {
			return Change{}, tx.Rollback(ErrConflict)
		}
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

func changeSelectionEqual(left, right ChangeSelection) bool {
	return left.format == right.format && left.commit.equal(right.commit) && left.repositoryRoot == right.repositoryRoot && left.repository == right.repository
}

func changeAvailabilityEqual(left, right ChangeAvailability) bool {
	return bytes.Equal(left.commitment.Bytes(), right.commitment.Bytes()) && left.entries == right.entries && left.bytes == right.bytes && left.source == right.source
}

func requireOneRow(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrRevisionConflict
	}
	return nil
}
