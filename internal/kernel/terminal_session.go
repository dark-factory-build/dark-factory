package kernel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const terminalSessionColumns = `id, run_id, state, unresolved_reason, revision,
    declared_at_ms, activated_at_ms, closed_at_ms, updated_at_ms`

func scanTerminalSession(scanner rowScanner) (TerminalSession, bool, error) {
	var rawID, rawRunID []byte
	var stateValue string
	var reason sql.NullString
	var revision, declaredAt, updatedAt int64
	var activatedAt, closedAt sql.NullInt64
	if err := scanner.Scan(&rawID, &rawRunID, &stateValue, &reason, &revision, &declaredAt, &activatedAt, &closedAt, &updatedAt); err != nil {
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
	if idErr != nil || runErr != nil || stateErr != nil || revisionErr != nil || declaredErr != nil || updatedErr != nil || updatedAt < declaredAt || reason.Valid && (byteLen(reason.String) < 1 || byteLen(reason.String) > 4096) {
		return TerminalSession{}, false, fmt.Errorf("%w: invalid terminal session controls", ErrCorruptState)
	}
	result := TerminalSession{ID: id, RunID: runID, State: state, UnresolvedReason: nullStringValue(reason), Revision: rev, DeclaredAt: declared, UpdatedAt: updated}
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
	return scanTerminalSession(connection.QueryRowContext(ctx, `SELECT `+terminalSessionColumns+` FROM terminal_sessions WHERE run_id = ?`, id.Bytes()))
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
	return session, true, nil
}
