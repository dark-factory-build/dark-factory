package kernel

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// failure is one signature in the failure ledger: what failed, not which task.
// Every failed or blocked run (a worker's or the overseer's, not a
// specialist's own carrier) and every failed pull-request check counts. A
// signature is new when its first occurrence ever falls at or after since:
// one occurrence is enough to surface it, because any failure is a sign of
// unreliability, not something to wait out.
type failure struct {
	signature   string
	count       int64 // since
	first, last int64
	example     string
}

// failureLedger reads the ledger for scope (a project's bytes, or nil for every
// project) since since (Unix ms), newest last-seen first. It records nothing.
// ponytail: it scans every failed run and failed check to find first
// occurrences (about 2k rows on 12 Oct 2026); keep a first-seen table if that
// grows past ~100k.
func failureLedger(ctx context.Context, c *sql.Conn, scope []byte, since int64) ([]failure, error) {
	var project any
	if scope != nil {
		project = scope
	}
	rows, err := c.QueryContext(ctx, `SELECT r.terminal_kind, COALESCE(r.terminal_code, ''), r.provider_exit_code, r.provider_exit_signal,
			r.runner_exit_code, COALESCE(r.terminal_detail, t.blocked_reason, ''), r.terminal_at_ms, lower(hex(t.id))
		FROM runs AS r JOIN tasks AS t ON t.id = r.task_id JOIN agents AS a ON a.id = r.agent_id
		WHERE (?1 IS NULL OR r.project_id = ?1) AND r.terminal_kind IN ('failed', 'blocked') AND r.terminal_at_ms IS NOT NULL AND NOT `+specialistCarrierSQL+`
		UNION ALL SELECT 'check', json_extract(document, '$.name'), NULL, NULL, NULL,
			'pull request #' || json_extract(document, '$.pull_requests[0]') || ' at ' || substr(json_extract(document, '$.revision'), 1, 8) || COALESCE(' ' || (SELECT group_concat(json_extract(j.value, '$.name') || ' ' || json_extract(j.value, '$.annotations')) FROM json_each(production_records.document, '$.jobs') j), ''), observed_at_ms, ''
		FROM production_records WHERE (?1 IS NULL OR project_id = ?1) AND kind = 'check' AND json_extract(document, '$.conclusion') IN ('failure', 'timed_out', 'startup_failure')`, project)
	if err != nil {
		return nil, err
	}
	byKey := map[string]*failure{}
	for rows.Next() {
		var kind, code, detail, task string
		var exit, signal, runner sql.NullInt64
		var at int64
		if err := rows.Scan(&kind, &code, &exit, &signal, &runner, &detail, &at, &task); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		key := failureSignature(kind, code, exit, signal, runner, detail)
		entry := byKey[key]
		if entry == nil {
			entry = &failure{signature: key, first: at}
			byKey[key] = entry
		}
		entry.first = min(entry.first, at)
		if at >= since {
			entry.count++
			if at >= entry.last {
				entry.last, entry.example = at, strings.TrimSpace(task+" "+oneLine(detail, 160))
			}
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	var ledger []failure
	for _, entry := range byKey {
		if entry.count > 0 {
			ledger = append(ledger, *entry)
		}
	}
	slices.SortFunc(ledger, func(a, b failure) int {
		return cmp.Or(cmp.Compare(b.last, a.last), strings.Compare(a.signature, b.signature))
	})
	return ledger, nil
}

// failureSignature names a failure by its structured fields and, for a free
// text cause, its first clause with identifiers and numbers removed, so the
// same failure on another task, head or run is the same signature.
func failureSignature(kind, code string, exit, signal, runner sql.NullInt64, detail string) string {
	parts := []string{kind, code}
	for _, value := range []sql.NullInt64{exit, signal, runner} {
		if value.Valid {
			parts = append(parts, fmt.Sprint(value.Int64))
		} else {
			parts = append(parts, "-")
		}
	}
	head, _, _ := strings.Cut(detail, ":")
	var words []string
	for _, word := range strings.Fields(strings.ToLower(head)) {
		if word = strings.TrimFunc(word, func(r rune) bool { return !unicode.IsLetter(r) }); word != "" && !strings.ContainsFunc(word, unicode.IsDigit) && len(words) < 6 {
			words = append(words, word)
		}
	}
	return strings.Join(parts, "/") + " " + strings.Join(words, " ")
}
