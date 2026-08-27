package kernel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const terminalSessionColumns = `id, run_id, state, unresolved_reason, revision,
    declared_at_ms, activated_at_ms, closed_at_ms, updated_at_ms,
    lease_client_id, lease_generation, lease_expires_at_ms, last_input_sequence`

func scanTerminalSession(scanner rowScanner) (TerminalSession, bool, error) {
	var rawID, rawRunID []byte
	var rawLeaseClientID nullableBlob
	var stateValue string
	var reason sql.NullString
	var revision, declaredAt, updatedAt int64
	var activatedAt, closedAt, leaseExpiresAt sql.NullInt64
	var leaseGeneration, lastInputSequence int64
	if err := scanner.Scan(&rawID, &rawRunID, &stateValue, &reason, &revision, &declaredAt, &activatedAt, &closedAt, &updatedAt, &rawLeaseClientID, &leaseGeneration, &leaseExpiresAt, &lastInputSequence); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TerminalSession{}, false, nil
		}
		return TerminalSession{}, false, fmt.Errorf("scan terminal session: %w", err)
	}
	id, idErr := TerminalSessionIDFromBytes(rawID)
	runID, runErr := RunIDFromBytes(rawRunID)
	state, stateErr := parseTerminalSessionState(stateValue)
	rev, revisionErr := NewRevision(revision)
	declared, declaredErr := NewUnixMillis(declaredAt)
	updated, updatedErr := NewUnixMillis(updatedAt)
	if idErr != nil || runErr != nil || stateErr != nil || revisionErr != nil || declaredErr != nil || updatedErr != nil || updatedAt < declaredAt || reason.Valid && (byteLen(reason.String) < 1 || byteLen(reason.String) > 4096) || leaseGeneration < 0 || lastInputSequence < 0 || rawLeaseClientID.valid && (state != TerminalSessionActive || leaseGeneration < 1) || rawLeaseClientID.valid != leaseExpiresAt.Valid || !rawLeaseClientID.valid && lastInputSequence != 0 {
		return TerminalSession{}, false, fmt.Errorf("%w: invalid terminal session controls", ErrCorruptState)
	}
	result := TerminalSession{ID: id, RunID: runID, State: state, UnresolvedReason: nullStringValue(reason), Revision: rev, DeclaredAt: declared, UpdatedAt: updated, LeaseGeneration: uint64(leaseGeneration), LastInputSequence: uint64(lastInputSequence)}
	if rawLeaseClientID.valid {
		leaseClientID, err := BrowserClientIDFromBytes(rawLeaseClientID.bytes)
		if err != nil {
			return TerminalSession{}, false, fmt.Errorf("%w: invalid terminal lease client", ErrCorruptState)
		}
		result.LeaseClientID = &leaseClientID
		if leaseExpiresAt.Int64 < 0 {
			return TerminalSession{}, false, fmt.Errorf("%w: invalid terminal lease expiry", ErrCorruptState)
		}
		expires, err := NewUnixMillis(leaseExpiresAt.Int64)
		if err != nil {
			return TerminalSession{}, false, fmt.Errorf("%w: invalid terminal lease expiry", ErrCorruptState)
		}
		result.LeaseExpiresAt = &expires
	}
	if activatedAt.Valid {
		value, err := NewUnixMillis(activatedAt.Int64)
		if err != nil || activatedAt.Int64 < declaredAt || activatedAt.Int64 > updatedAt {
			return TerminalSession{}, false, fmt.Errorf("%w: invalid terminal activation time", ErrCorruptState)
		}
		result.ActivatedAt = &value
	}
	if closedAt.Valid {
		value, err := NewUnixMillis(closedAt.Int64)
		if err != nil || closedAt.Int64 < declaredAt || result.ActivatedAt != nil && closedAt.Int64 < result.ActivatedAt.Int64() || closedAt.Int64 != updatedAt {
			return TerminalSession{}, false, fmt.Errorf("%w: invalid terminal close time", ErrCorruptState)
		}
		result.ClosedAt = &value
	}
	valid := false
	switch state {
	case TerminalSessionDeclared:
		valid = !reason.Valid && !activatedAt.Valid && !closedAt.Valid && updatedAt == declaredAt
	case TerminalSessionActive:
		valid = !reason.Valid && activatedAt.Valid && !closedAt.Valid
	case TerminalSessionUnresolved:
		valid = reason.Valid && !closedAt.Valid
	case TerminalSessionClosed:
		valid = !reason.Valid && closedAt.Valid
	}
	if !valid {
		return TerminalSession{}, false, fmt.Errorf("%w: inconsistent terminal session state", ErrCorruptState)
	}
	return result, true, nil
}

func terminalSessionByID(ctx context.Context, connection *sql.Conn, id TerminalSessionID) (TerminalSession, bool, error) {
	if id.zero() {
		return TerminalSession{}, false, fmt.Errorf("%w: zero terminal session identifier", ErrInvalidValue)
	}
	return scanTerminalSession(connection.QueryRowContext(ctx, `SELECT `+terminalSessionColumns+` FROM terminal_sessions WHERE id = ?`, id.Bytes()))
}

func terminalSessionByRunID(ctx context.Context, connection *sql.Conn, id RunID) (TerminalSession, bool, error) {
	if id.zero() {
		return TerminalSession{}, false, fmt.Errorf("%w: zero run identifier", ErrInvalidValue)
	}
	rows, err := connection.QueryContext(ctx, `SELECT `+terminalSessionColumns+` FROM terminal_sessions WHERE run_id = ? ORDER BY id LIMIT 2`, id.Bytes())
	if err != nil {
		return TerminalSession{}, false, err
	}
	if !rows.Next() {
		return TerminalSession{}, false, errors.Join(rows.Err(), rows.Close())
	}
	session, _, err := scanTerminalSession(rows)
	if err != nil {
		return TerminalSession{}, false, errors.Join(err, rows.Close())
	}
	if rows.Next() {
		return TerminalSession{}, false, errors.Join(fmt.Errorf("%w: run has multiple terminal sessions", ErrCorruptState), rows.Close())
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return TerminalSession{}, false, err
	}
	return session, true, nil
}

func (store *Store) TerminalSession(ctx context.Context, id TerminalSessionID) (TerminalSession, bool, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return TerminalSession{}, false, err
	}
	defer tx.Close()
	session, found, err := terminalSessionByID(ctx, tx.connection, id)
	if err != nil || !found {
		return session, found, err
	}
	run, runFound, err := runByID(ctx, tx.connection, session.RunID)
	if err != nil || !runFound {
		if err == nil {
			err = ErrCorruptState
		}
		return TerminalSession{}, false, err
	}
	if _, err := loadRunRelationships(ctx, tx.connection, run); err != nil {
		return TerminalSession{}, false, err
	}
	if err := validateTerminalSessionLease(ctx, tx.connection, run, session); err != nil {
		return TerminalSession{}, false, err
	}
	return session, true, nil
}

func (store *Store) TerminalSessionForRun(ctx context.Context, runID RunID) (TerminalSession, bool, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return TerminalSession{}, false, err
	}
	defer tx.Close()
	run, runFound, err := runByID(ctx, tx.connection, runID)
	if err != nil || !runFound {
		return TerminalSession{}, runFound, err
	}
	session, found, err := terminalSessionByRunID(ctx, tx.connection, runID)
	if err != nil {
		return TerminalSession{}, false, err
	}
	if !found {
		return TerminalSession{}, false, ErrCorruptState
	}
	if _, err := loadRunRelationships(ctx, tx.connection, run); err != nil {
		return TerminalSession{}, false, err
	}
	if err := validateTerminalSessionLease(ctx, tx.connection, run, session); err != nil {
		return TerminalSession{}, false, err
	}
	return session, true, nil
}

func validateTerminalSessionLease(ctx context.Context, connection *sql.Conn, run Run, session TerminalSession) error {
	if session.LeaseClientID == nil {
		return nil
	}
	if run.Phase != RunRunning || session.State != TerminalSessionActive || session.LeaseExpiresAt == nil {
		return fmt.Errorf("%w: invalid terminal lease relationship", ErrCorruptState)
	}
	client, found, err := browserClientByID(ctx, connection, *session.LeaseClientID)
	if err != nil {
		return err
	}
	if !found || client.RevokedAt != nil || !client.CapabilityMask.Has(BrowserCapabilityTerminalInput) {
		return fmt.Errorf("%w: invalid terminal lease client relationship", ErrCorruptState)
	}
	return nil
}
