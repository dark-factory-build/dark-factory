package kernel

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"unicode/utf8"
)

// ProviderAdoption is the durable installation decision for one native
// provider. A candidate is optional and is deliberately limited to one so a
// later self-check cannot create an unbounded update queue.
type ProviderAdoption struct {
	Provider            Provider
	ActiveVersion       string
	ActiveCommitment    string
	CandidateVersion    string
	CandidateCommitment string
	UpdatedAt           UnixMillis
}

func (store *Store) SetProviderAdoption(ctx context.Context, adoption ProviderAdoption, at UnixMillis) error {
	if !validProviderAdoption(adoption) || at.Int64() < 0 {
		return fmt.Errorf("%w: invalid provider adoption", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Close()
	_, err = tx.connection.ExecContext(ctx, `INSERT INTO provider_adoptions(provider, active_version, active_commitment, candidate_version, candidate_commitment, updated_at_ms)
VALUES(?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?)
ON CONFLICT(provider) DO UPDATE SET active_version = excluded.active_version, active_commitment = excluded.active_commitment,
candidate_version = excluded.candidate_version, candidate_commitment = excluded.candidate_commitment, updated_at_ms = excluded.updated_at_ms`,
		adoption.Provider.String(), adoption.ActiveVersion, adoption.ActiveCommitment, adoption.CandidateVersion, adoption.CandidateCommitment, at.Int64())
	if err != nil {
		return tx.Rollback(err)
	}
	return tx.Commit(ctx)
}

func (store *Store) ProviderAdoption(ctx context.Context, provider Provider) (ProviderAdoption, bool, error) {
	if provider != ProviderClaudeCode && provider != ProviderCodex {
		return ProviderAdoption{}, false, fmt.Errorf("%w: provider adoption is native-only", ErrInvalidValue)
	}
	connection, err := store.readerConnection(ctx)
	if err != nil {
		return ProviderAdoption{}, false, err
	}
	defer connection.Close()
	var adoption ProviderAdoption
	var providerName string
	var candidateVersion, candidateCommitment sql.NullString
	var updatedAt int64
	err = connection.QueryRowContext(ctx, `SELECT provider, active_version, active_commitment, candidate_version, candidate_commitment, updated_at_ms FROM provider_adoptions WHERE provider = ?`, provider.String()).Scan(
		&providerName, &adoption.ActiveVersion, &adoption.ActiveCommitment, &candidateVersion, &candidateCommitment, &updatedAt)
	if err == sql.ErrNoRows {
		return ProviderAdoption{}, false, nil
	}
	if err != nil {
		return ProviderAdoption{}, false, err
	}
	adoption.Provider, err = ParseProvider(providerName)
	if err != nil {
		return ProviderAdoption{}, false, fmt.Errorf("%w: invalid provider adoption row", ErrCorruptState)
	}
	adoption.UpdatedAt = UnixMillis{value: updatedAt}
	if candidateVersion.Valid {
		adoption.CandidateVersion = candidateVersion.String
		adoption.CandidateCommitment = candidateCommitment.String
	}
	if !validProviderAdoption(adoption) {
		return ProviderAdoption{}, false, fmt.Errorf("%w: invalid provider adoption row", ErrCorruptState)
	}
	return adoption, true, nil
}

func (store *Store) ListProviderAdoptions(ctx context.Context) ([]ProviderAdoption, error) {
	connection, err := store.readerConnection(ctx)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	rows, err := connection.QueryContext(ctx, `SELECT provider, active_version, active_commitment, candidate_version, candidate_commitment, updated_at_ms FROM provider_adoptions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ProviderAdoption
	for rows.Next() {
		var adoption ProviderAdoption
		var providerName string
		var candidateVersion, candidateCommitment sql.NullString
		var updatedAt int64
		if err := rows.Scan(&providerName, &adoption.ActiveVersion, &adoption.ActiveCommitment, &candidateVersion, &candidateCommitment, &updatedAt); err != nil {
			return nil, err
		}
		adoption.UpdatedAt = UnixMillis{value: updatedAt}
		adoption.Provider, err = ParseProvider(providerName)
		if err != nil || !validProviderAdoption(adoption) {
			return nil, fmt.Errorf("%w: invalid provider adoption row", ErrCorruptState)
		}
		if candidateVersion.Valid {
			adoption.CandidateVersion = candidateVersion.String
			adoption.CandidateCommitment = candidateCommitment.String
		}
		result = append(result, adoption)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Provider < result[j].Provider })
	return result, nil
}

func validProviderAdoption(adoption ProviderAdoption) bool {
	return (adoption.Provider == ProviderClaudeCode || adoption.Provider == ProviderCodex) &&
		validAdoptionText(adoption.ActiveVersion, 128) && validAdoptionText(adoption.ActiveCommitment, 4096) &&
		(adoption.CandidateVersion == "") == (adoption.CandidateCommitment == "") &&
		(adoption.CandidateVersion == "" || validAdoptionText(adoption.CandidateVersion, 128) && validAdoptionText(adoption.CandidateCommitment, 4096))
}

func validAdoptionText(value string, max int) bool {
	return utf8.ValidString(value) && len(value) >= 1 && len(value) <= max
}
