package kernel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
)

const maxProviderAdoptionDiagnosticBytes = 4096

// ProviderAdoption is the durable boundary between an executable discovered
// on the daemon's fixed tool path and the executable allowed to launch.
type ProviderAdoption struct {
	Provider            Provider
	ActivePath          string
	ActiveCommitment    []byte
	CandidatePath       string
	CandidateCommitment []byte
	CandidateState      string
	Diagnostic          string
	Revision            Revision
	UpdatedAt           UnixMillis
}

// InitializeProviderAdoption records the first executable as the trusted
// active installation. Initial discovery is not an update and therefore does
// not require a self-check.
func (store *Store) InitializeProviderAdoption(ctx context.Context, provider Provider, activePath string, activeCommitment []byte, at UnixMillis) (ProviderAdoption, error) {
	if provider != ProviderClaudeCode && provider != ProviderCodex || activePath == "" || len(activeCommitment) != DigestBytes {
		return ProviderAdoption{}, fmt.Errorf("%w: invalid provider adoption initialization", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return ProviderAdoption{}, err
	}
	defer tx.Close()
	_, err = tx.connection.ExecContext(ctx, `INSERT INTO provider_adoptions(provider, active_path, active_commitment, candidate_path, candidate_commitment, candidate_state, diagnostic, revision, updated_at_ms) VALUES(?, ?, ?, '', zeroblob(0), 'none', '', 1, ?) ON CONFLICT(provider) DO NOTHING`, provider.String(), activePath, activeCommitment, at.Int64())
	if err != nil {
		return ProviderAdoption{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ProviderAdoption{}, err
	}
	value, _, err := store.ProviderAdoption(ctx, provider)
	return value, err
}

func scanProviderAdoption(row rowScanner) (ProviderAdoption, error) {
	var provider, activePath, candidatePath, state, diagnostic string
	var active, candidate []byte
	var revision, updated int64
	if err := row.Scan(&provider, &activePath, &active, &candidatePath, &candidate, &state, &diagnostic, &revision, &updated); err != nil {
		return ProviderAdoption{}, err
	}
	kind, err := ParseProvider(provider)
	if err != nil || kind == ProviderShell || len(active) != DigestBytes || (state == "none" && (candidatePath != "" || len(candidate) != 0 || diagnostic != "")) || (state != "none" && (candidatePath == "" || len(candidate) != DigestBytes)) {
		return ProviderAdoption{}, fmt.Errorf("%w: invalid provider adoption", ErrCorruptState)
	}
	rev, err := NewRevision(revision)
	if err != nil {
		return ProviderAdoption{}, err
	}
	at, err := NewUnixMillis(updated)
	if err != nil {
		return ProviderAdoption{}, err
	}
	return ProviderAdoption{Provider: kind, ActivePath: activePath, ActiveCommitment: append([]byte(nil), active...), CandidatePath: candidatePath, CandidateCommitment: append([]byte(nil), candidate...), CandidateState: state, Diagnostic: diagnostic, Revision: rev, UpdatedAt: at}, nil
}

// ProviderAdoption reads one provider's durable active/candidate state.
func (store *Store) ProviderAdoption(ctx context.Context, provider Provider) (ProviderAdoption, bool, error) {
	if store == nil || ctx == nil || (provider != ProviderClaudeCode && provider != ProviderCodex) {
		return ProviderAdoption{}, false, fmt.Errorf("%w: invalid provider adoption lookup", ErrInvalidValue)
	}
	row := store.readers.QueryRowContext(ctx, `SELECT provider, active_path, active_commitment, candidate_path, candidate_commitment, candidate_state, diagnostic, revision, updated_at_ms FROM provider_adoptions WHERE provider = ?`, provider.String())
	value, err := scanProviderAdoption(row)
	if err == sql.ErrNoRows {
		return ProviderAdoption{}, false, nil
	}
	return value, err == nil, err
}

// ObserveProviderCandidate records a newly discovered executable without
// making it launchable. Re-observing the same candidate is idempotent, which
// is the one-shot retry boundary after a failed self-check.
func (store *Store) ObserveProviderCandidate(ctx context.Context, provider Provider, activePath string, activeCommitment, candidateCommitment []byte, candidatePath string, at UnixMillis) (ProviderAdoption, error) {
	if provider != ProviderClaudeCode && provider != ProviderCodex || activePath == "" || candidatePath == "" || len(activeCommitment) != DigestBytes || len(candidateCommitment) != DigestBytes || at.Int64() < 0 {
		return ProviderAdoption{}, fmt.Errorf("%w: invalid provider adoption candidate", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return ProviderAdoption{}, err
	}
	defer tx.Close()
	current, err := scanProviderAdoption(tx.connection.QueryRowContext(ctx, `SELECT provider, active_path, active_commitment, candidate_path, candidate_commitment, candidate_state, diagnostic, revision, updated_at_ms FROM provider_adoptions WHERE provider = ?`, provider.String()))
	if err == nil && current.CandidateState == "failed" && bytes.Equal(current.CandidateCommitment, candidateCommitment) {
		_ = tx.Rollback(nil)
		return current, nil
	}
	if err != nil && err != sql.ErrNoRows {
		return ProviderAdoption{}, tx.Rollback(err)
	}
	_, err = tx.connection.ExecContext(ctx, `INSERT INTO provider_adoptions(provider, active_path, active_commitment, candidate_path, candidate_commitment, candidate_state, diagnostic, revision, updated_at_ms) VALUES(?, ?, ?, ?, ?, 'pending', '', 1, ?) ON CONFLICT(provider) DO UPDATE SET active_path = CASE WHEN provider_adoptions.candidate_state = 'none' THEN excluded.active_path ELSE provider_adoptions.active_path END, active_commitment = CASE WHEN provider_adoptions.candidate_state = 'none' THEN excluded.active_commitment ELSE provider_adoptions.active_commitment END, candidate_path = CASE WHEN provider_adoptions.candidate_commitment = excluded.candidate_commitment THEN provider_adoptions.candidate_path ELSE excluded.candidate_path END, candidate_commitment = CASE WHEN provider_adoptions.candidate_commitment = excluded.candidate_commitment THEN provider_adoptions.candidate_commitment ELSE excluded.candidate_commitment END, candidate_state = CASE WHEN provider_adoptions.candidate_commitment = excluded.candidate_commitment THEN provider_adoptions.candidate_state ELSE 'pending' END, diagnostic = CASE WHEN provider_adoptions.candidate_commitment = excluded.candidate_commitment THEN provider_adoptions.diagnostic ELSE '' END, revision = provider_adoptions.revision + CASE WHEN provider_adoptions.candidate_commitment = excluded.candidate_commitment THEN 0 ELSE 1 END, updated_at_ms = excluded.updated_at_ms`, provider.String(), activePath, activeCommitment, candidatePath, candidateCommitment, at.Int64())
	if err != nil {
		return ProviderAdoption{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ProviderAdoption{}, err
	}
	value, _, err := store.ProviderAdoption(ctx, provider)
	return value, err
}

// CompleteProviderAdoption atomically promotes a checked candidate or marks
// it failed. A failed candidate remains visible and is never retried merely by
// reopening the store.
func (store *Store) CompleteProviderAdoption(ctx context.Context, provider Provider, candidateCommitment []byte, passed bool, diagnostic string, at UnixMillis) (ProviderAdoption, error) {
	if provider != ProviderClaudeCode && provider != ProviderCodex || len(candidateCommitment) != DigestBytes || len(diagnostic) > maxProviderAdoptionDiagnosticBytes {
		return ProviderAdoption{}, fmt.Errorf("%w: invalid provider adoption result", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return ProviderAdoption{}, err
	}
	defer tx.Close()
	current, currentErr := scanProviderAdoption(tx.connection.QueryRowContext(ctx, `SELECT provider, active_path, active_commitment, candidate_path, candidate_commitment, candidate_state, diagnostic, revision, updated_at_ms FROM provider_adoptions WHERE provider = ?`, provider.String()))
	if currentErr == nil && current.CandidateState == "failed" && bytes.Equal(current.CandidateCommitment, candidateCommitment) {
		_ = tx.Rollback(nil)
		return current, nil
	}
	if currentErr != nil && currentErr != sql.ErrNoRows {
		return ProviderAdoption{}, tx.Rollback(currentErr)
	}
	var result string
	if passed {
		result = `candidate_state = 'none', active_path = candidate_path, active_commitment = candidate_commitment, candidate_path = '', candidate_commitment = zeroblob(0), diagnostic = ''`
	} else {
		result = `candidate_state = 'failed', diagnostic = ?`
	}
	var args []any
	query := `UPDATE provider_adoptions SET ` + result + `, revision = revision + 1, updated_at_ms = ? WHERE provider = ? AND candidate_commitment = ? AND candidate_state = 'pending'`
	if passed {
		args = []any{at.Int64(), provider.String(), candidateCommitment}
	}
	if !passed {
		args = []any{diagnostic, at.Int64(), provider.String(), candidateCommitment}
	}
	res, err := tx.connection.ExecContext(ctx, query, args...)
	if err != nil {
		return ProviderAdoption{}, tx.Rollback(err)
	}
	if err := requireOneRow(res, nil); err != nil {
		return ProviderAdoption{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ProviderAdoption{}, err
	}
	value, _, err := store.ProviderAdoption(ctx, provider)
	return value, err
}

// CompleteProviderAdoptionForAttempt is the failure edge used by a live
// self-check. It reuses the existing attempt-authenticated human-request path
// instead of inventing a provider-specific operator queue. The commitment is
// the idempotency key, so a replay after a restart cannot create a second
// NEEDS YOU item.
func (store *Store) CompleteProviderAdoptionForAttempt(ctx context.Context, digest AttemptDigest, provider Provider, candidateCommitment []byte, diagnostic string, at UnixMillis) (ProviderAdoption, HumanRequest, error) {
	adoption, err := store.CompleteProviderAdoption(ctx, provider, candidateCommitment, false, diagnostic, at)
	if err != nil {
		return ProviderAdoption{}, HumanRequest{}, err
	}
	keyDigest := sha256.Sum256(append([]byte(provider.String()), candidateCommitment...))
	var key [IDBytes]byte
	copy(key[:], keyDigest[:IDBytes])
	question := NewHumanQuestion{
		IdempotencyKey: key,
		QuestionText:   "Provider adoption self-check failed for " + provider.String() + ": " + diagnostic,
		ReuseExisting:  true,
	}
	request, err := store.CreateHumanQuestionForAttempt(ctx, digest, question, at)
	return adoption, request, err
}
