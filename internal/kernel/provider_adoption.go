package kernel

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const MaxProviderAdoptionDetailBytes = 4096

type ProviderAdoptionState uint8

const (
	ProviderAdoptionPending ProviderAdoptionState = iota + 1
	ProviderAdoptionFailed
)

func (state ProviderAdoptionState) String() string {
	if state == ProviderAdoptionFailed {
		return "failed"
	}
	if state == ProviderAdoptionPending {
		return "pending"
	}
	return ""
}

// ProviderAdoption is the daemon/kernel handoff for an installed native
// provider. Commitments are opaque to the kernel; the provider package owns
// how an executable commitment is made and verified.
type ProviderAdoption struct {
	Provider            Provider
	ActiveCommitment    string
	CandidateCommitment string
	CandidateState      ProviderAdoptionState
	FailureDetail       string
	NeedsYou            bool
	Revision            Revision
	UpdatedAt           UnixMillis
}

func validProviderAdoptionInput(provider Provider, commitment string) error {
	if provider != ProviderClaudeCode && provider != ProviderCodex {
		return fmt.Errorf("%w: provider adoption only supports native providers", ErrInvalidValue)
	}
	if commitment == "" || byteLen(commitment) > MaxProviderAdoptionDetailBytes || !utf8TextWithin(commitment, 1, MaxProviderAdoptionDetailBytes) {
		return fmt.Errorf("%w: invalid provider commitment", ErrInvalidValue)
	}
	return nil
}

func scanProviderAdoption(scanner rowScanner) (ProviderAdoption, bool, error) {
	var rawProvider, active, candidate, state, detail string
	var needs, revision, updated int64
	if err := scanner.Scan(&rawProvider, &active, &candidate, &state, &detail, &needs, &revision, &updated); err != nil {
		if err == sql.ErrNoRows {
			return ProviderAdoption{}, false, nil
		}
		return ProviderAdoption{}, false, fmt.Errorf("scan provider adoption: %w", err)
	}
	provider, err := ParseProvider(rawProvider)
	parsedState := ProviderAdoptionPending
	if state == ProviderAdoptionFailed.String() {
		parsedState = ProviderAdoptionFailed
	}
	at, atErr := NewUnixMillis(updated)
	rev, revErr := NewRevision(revision)
	if err != nil || atErr != nil || revErr != nil || provider == ProviderShell || byteLen(active) > MaxProviderAdoptionDetailBytes || byteLen(candidate) > MaxProviderAdoptionDetailBytes || byteLen(detail) > MaxProviderAdoptionDetailBytes || (needs != 0 && needs != 1) || candidate == "" && (state != ProviderAdoptionPending.String() || detail != "" || needs != 0) || candidate != "" && parsedState == ProviderAdoptionFailed && (detail == "" || needs != 1) {
		return ProviderAdoption{}, false, fmt.Errorf("%w: invalid provider adoption row", ErrCorruptState)
	}
	return ProviderAdoption{Provider: provider, ActiveCommitment: active, CandidateCommitment: candidate, CandidateState: parsedState, FailureDetail: detail, NeedsYou: needs == 1, Revision: rev, UpdatedAt: at}, true, nil
}

func (store *Store) ProviderAdoption(ctx context.Context, provider Provider) (ProviderAdoption, bool, error) {
	if provider != ProviderClaudeCode && provider != ProviderCodex {
		return ProviderAdoption{}, false, fmt.Errorf("%w: provider adoption lookup", ErrInvalidValue)
	}
	connection, err := store.readerConnection(ctx)
	if err != nil {
		return ProviderAdoption{}, false, err
	}
	defer connection.Close()
	return scanProviderAdoption(connection.QueryRowContext(ctx, `SELECT provider, active_commitment, candidate_commitment, candidate_state, failure_detail, needs_you, revision, updated_at_ms FROM provider_adoptions WHERE provider = ?`, provider.String()))
}

// RecordProviderCandidate is idempotent. A failed candidate is not retried;
// only a different commitment clears the durable NEEDS YOU state.
func (store *Store) RecordProviderCandidate(ctx context.Context, provider Provider, commitment string, at UnixMillis) (ProviderAdoption, error) {
	if err := validProviderAdoptionInput(provider, commitment); err != nil {
		return ProviderAdoption{}, err
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return ProviderAdoption{}, err
	}
	defer tx.Close()
	current, found, err := scanProviderAdoption(tx.connection.QueryRowContext(ctx, `SELECT provider, active_commitment, candidate_commitment, candidate_state, failure_detail, needs_you, revision, updated_at_ms FROM provider_adoptions WHERE provider = ?`, provider.String()))
	if err != nil {
		return ProviderAdoption{}, tx.Rollback(err)
	}
	if found && current.ActiveCommitment == commitment {
		if err := tx.Rollback(nil); err != nil {
			return ProviderAdoption{}, err
		}
		return current, nil
	}
	if found && current.CandidateCommitment == commitment {
		if err := tx.Rollback(nil); err != nil {
			return ProviderAdoption{}, err
		}
		return current, nil
	}
	revision := int64(1)
	if found {
		revision = current.Revision.Int64() + 1
	}
	query := `INSERT INTO provider_adoptions(provider, active_commitment, candidate_commitment, candidate_state, failure_detail, needs_you, revision, updated_at_ms) VALUES(?, ?, ?, 'pending', '', 0, ?, ?) ON CONFLICT(provider) DO UPDATE SET candidate_commitment = excluded.candidate_commitment, candidate_state = 'pending', failure_detail = '', needs_you = 0, revision = excluded.revision, updated_at_ms = excluded.updated_at_ms`
	if _, err := tx.connection.ExecContext(ctx, query, provider.String(), func() string {
		if found {
			return current.ActiveCommitment
		}
		return ""
	}(), commitment, revision, at.Int64()); err != nil {
		return ProviderAdoption{}, tx.Rollback(err)
	}
	result, _, err := scanProviderAdoption(tx.connection.QueryRowContext(ctx, `SELECT provider, active_commitment, candidate_commitment, candidate_state, failure_detail, needs_you, revision, updated_at_ms FROM provider_adoptions WHERE provider = ?`, provider.String()))
	if err != nil {
		return ProviderAdoption{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ProviderAdoption{}, err
	}
	return result, nil
}

func (store *Store) CommitProviderCandidate(ctx context.Context, provider Provider, expected Revision, at UnixMillis) (ProviderAdoption, error) {
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return ProviderAdoption{}, err
	}
	defer tx.Close()
	current, found, err := scanProviderAdoption(tx.connection.QueryRowContext(ctx, `SELECT provider, active_commitment, candidate_commitment, candidate_state, failure_detail, needs_you, revision, updated_at_ms FROM provider_adoptions WHERE provider = ?`, provider.String()))
	if err != nil || !found || current.Revision != expected || current.CandidateCommitment == "" || current.CandidateState != ProviderAdoptionPending {
		return ProviderAdoption{}, tx.Rollback(fmt.Errorf("%w: provider candidate is not pending", ErrConflict))
	}
	if _, err := tx.connection.ExecContext(ctx, `UPDATE provider_adoptions SET active_commitment = candidate_commitment, candidate_commitment = '', failure_detail = '', needs_you = 0, revision = revision + 1, updated_at_ms = ? WHERE provider = ? AND revision = ?`, at.Int64(), provider.String(), expected.Int64()); err != nil {
		return ProviderAdoption{}, tx.Rollback(err)
	}
	result, _, err := scanProviderAdoption(tx.connection.QueryRowContext(ctx, `SELECT provider, active_commitment, candidate_commitment, candidate_state, failure_detail, needs_you, revision, updated_at_ms FROM provider_adoptions WHERE provider = ?`, provider.String()))
	if err != nil {
		return ProviderAdoption{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ProviderAdoption{}, err
	}
	return result, nil
}

func (store *Store) FailProviderCandidate(ctx context.Context, provider Provider, expected Revision, detail string, at UnixMillis) (ProviderAdoption, error) {
	if strings.TrimSpace(detail) == "" || byteLen(detail) > MaxProviderAdoptionDetailBytes || !utf8TextWithin(detail, 1, MaxProviderAdoptionDetailBytes) {
		return ProviderAdoption{}, fmt.Errorf("%w: invalid provider adoption failure", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return ProviderAdoption{}, err
	}
	defer tx.Close()
	current, found, err := scanProviderAdoption(tx.connection.QueryRowContext(ctx, `SELECT provider, active_commitment, candidate_commitment, candidate_state, failure_detail, needs_you, revision, updated_at_ms FROM provider_adoptions WHERE provider = ?`, provider.String()))
	if err != nil || !found || current.Revision != expected || current.CandidateCommitment == "" {
		return ProviderAdoption{}, tx.Rollback(fmt.Errorf("%w: provider candidate is not available", ErrConflict))
	}
	if current.CandidateState == ProviderAdoptionFailed {
		if err := tx.Rollback(nil); err != nil {
			return ProviderAdoption{}, err
		}
		return current, nil
	}
	if _, err := tx.connection.ExecContext(ctx, `UPDATE provider_adoptions SET candidate_state = 'failed', failure_detail = ?, needs_you = 1, revision = revision + 1, updated_at_ms = ? WHERE provider = ? AND revision = ?`, detail, at.Int64(), provider.String(), expected.Int64()); err != nil {
		return ProviderAdoption{}, tx.Rollback(err)
	}
	result, _, err := scanProviderAdoption(tx.connection.QueryRowContext(ctx, `SELECT provider, active_commitment, candidate_commitment, candidate_state, failure_detail, needs_you, revision, updated_at_ms FROM provider_adoptions WHERE provider = ?`, provider.String()))
	if err != nil {
		return ProviderAdoption{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ProviderAdoption{}, err
	}
	return result, nil
}
