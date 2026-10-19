package kernel

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
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
	repository  string
	url         string
}

// failureLedger reads the ledger for scope (a project's bytes, or nil for every
// project) since since (Unix ms), newest last-seen first, from runs across
// archived (nil reads hot runs only). It records nothing.
// ponytail: it scans every failed run and failed check to find first
// occurrences (about 2k rows on 12 Oct 2026); keep a first-seen table if that
// grows past ~100k.
func failureLedger(ctx context.Context, c *sql.Conn, archived history, scope []byte, since int64) ([]failure, error) {
	var project any
	if scope != nil {
		project = scope
	}
	rows, err := c.QueryContext(ctx, `SELECT r.terminal_kind, COALESCE(r.terminal_code, ''), r.provider_exit_code, r.provider_exit_signal,
			r.runner_exit_code, COALESCE(r.terminal_detail, t.blocked_reason, ''), r.terminal_at_ms, lower(hex(t.id)), '', lower(hex(r.id))
		FROM `+archived.table("runs")+` AS r JOIN `+archived.table("tasks")+` AS t ON t.id = r.task_id JOIN agents AS a ON a.id = r.agent_id
		WHERE (?1 IS NULL OR r.project_id = ?1) AND r.terminal_kind IN ('failed', 'blocked') AND r.terminal_at_ms IS NOT NULL AND NOT `+specialistCarrierSQL+`
		UNION ALL SELECT 'check', json_extract(document, '$.name'), NULL, NULL, NULL,
			CASE WHEN json_extract(document, '$.pull_requests[0]') IS NULL THEN 'main' ELSE 'pull request #' || json_extract(document, '$.pull_requests[0]') END || ' at ' || substr(json_extract(document, '$.revision'), 1, 8) || COALESCE(' ' || (SELECT group_concat(json_extract(j.value, '$.name') || ' ' || json_extract(j.value, '$.annotations')) FROM json_each(production_records.document, '$.jobs') j), ''), observed_at_ms, ''
			, repository, COALESCE(json_extract(document, '$.url'), '')
		FROM `+archived.table("production_records")+` AS production_records WHERE (?1 IS NULL OR project_id = ?1) AND kind = 'check' AND json_extract(document, '$.conclusion') IN ('failure', 'timed_out', 'startup_failure')`, project)
	if err != nil {
		return nil, err
	}
	byKey := map[string]*failure{}
	for rows.Next() {
		var kind, code, detail, task, repository, url string
		var exit, signal, runner sql.NullInt64
		var at int64
		if err := rows.Scan(&kind, &code, &exit, &signal, &runner, &detail, &at, &task, &repository, &url); err != nil {
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
				entry.last, entry.example, entry.repository, entry.url = at, strings.TrimSpace(task+" "+oneLine(detail, 160)), repository, url
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

// failureCauses name a failure's cause class: the first rule whose code
// matches, or whose pattern matches the detail's first 260 bytes, wins. From
// the #1773 evaluation (29/32 hand-checked, against 13/32 for codes alone);
// testdata/failure_causes.json is their regression corpus.
var failureCauses = []struct {
	class, codes string
	pattern      *regexp.Regexp
}{
	{"provider_crash", "provider_exit", regexp.MustCompile(`(?i)provider (worker \S+ )?exited`)},
	{"premise_dispute", "", regexp.MustCompile(`(?i)\A\s*premise not reproduced`)},
	{"usage_limit", "", regexp.MustCompile(`(?i)usage limit|rate.?limit|quota|hit your limit|credits? (exhausted|balance)`)},
	{"other", "", regexp.MustCompile(`\A\s*(REQUEST_CHANGES|ALLOW|BLOCK\b)`)},
	{"transport_api", "protocol", regexp.MustCompile(`(?i)local API[^.]{0,40}(transport failed|unavailable|unauthori[sz]ed|forbidden|failed internally|rejected)|Maintainer[^.]{0,20}unavailable|transport failed|broken pipe|connection (refused|reset|lost)|\bEOF\b|answered 50\d|GitHub connection unavailable|RemoteInternal|context deadline`)},
	{"stale_base", "", regexp.MustCompile(`(?i)conflicts? with (origin/)?main|merge conflict|conflict \((content|modify|add|rename)|lacks (origin/)?main|stale (base|head)|behind (origin/)?main|base (has )?moved|cannot (perform the requested )?rebase|rebase (failed|stopped)`)},
	{"env_toolchain", "spawn activation source", regexp.MustCompile(`(?i)command not found|not installed|not on PATH|no such file|permission denied|operation not permitted|sandbox|toolchain|not a git repository|has no (usable )?(origin|remote)|no \.git|git-free|bad object|read-only file system|executable file not found|cannot read inside|x509:`)},
	{"real_test_failure", "", regexp.MustCompile(`(?i)--- FAIL|\bFAIL:|(test|gate|check)s? (still )?fail|failing (test|gate|check)s?:|failed at Test|fails at \S+_test\.go|panic:|✖|\bnot ok \d`)},
}

// failureCause is the cause class of a failure with this code and detail.
func failureCause(code, detail string) string {
	lead := detail[:min(len(detail), 260)]
	for _, rule := range failureCauses {
		if slices.Contains(strings.Fields(rule.codes), code) || rule.pattern.MatchString(lead) {
			return rule.class
		}
	}
	return "other"
}

// samePreviousFailureClass reports whether the immediately preceding terminal
// run ended in the same normalized failure class.
func samePreviousFailureClass(ctx context.Context, connection *sql.Conn, taskID TaskID, incarnationID IncarnationID, workRevision Revision, code, detail string) (bool, error) {
	var previousCode, previousDetail sql.NullString
	err := connection.QueryRowContext(ctx, `SELECT terminal_code, terminal_detail FROM runs
		WHERE task_id = ? AND task_incarnation_id = ? AND admitted_task_work_revision < ? AND phase = 'terminal'
		ORDER BY admitted_task_work_revision DESC LIMIT 1`, taskID.Bytes(), incarnationID.Bytes(), workRevision.Int64()).Scan(&previousCode, &previousDetail)
	if errors.Is(err, sql.ErrNoRows) || !previousCode.Valid {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return failureCause(code, detail) == failureCause(previousCode.String, previousDetail.String), nil
}

// failureSignature names a run failure by its cause class and structured
// fields. Check failures also include their diagnostic test name.
func failureSignature(kind, code string, exit, signal, runner sql.NullInt64, detail string) string {
	parts := []string{kind, code}
	for _, value := range []sql.NullInt64{exit, signal, runner} {
		if value.Valid {
			parts = append(parts, fmt.Sprint(value.Int64))
		} else {
			parts = append(parts, "-")
		}
	}
	var words []string
	if kind == "check" {
		head, _, _ := strings.Cut(detail, ":")
		for _, raw := range strings.Fields(strings.ToLower(head)) {
			if strings.ContainsFunc(raw, unicode.IsDigit) {
				continue
			}
			word := strings.TrimFunc(raw, func(r rune) bool { return !unicode.IsLetter(r) })
			if word != "" && len(words) < 6 {
				words = append(words, word)
			}
		}
		lower := strings.ToLower(detail)
		for _, marker := range []string{"--- fail:", "✖", "not ok"} {
			if offset := strings.Index(lower, marker); offset >= 0 {
				tail := strings.SplitN(lower[offset+len(marker):], `",`, 2)[0]
				fields := strings.Fields(tail)
				if marker == "--- fail:" && len(fields) > 1 {
					fields = fields[:1]
				}
				if marker == "not ok" && len(fields) > 0 && strings.Trim(fields[0], "0123456789") == "" {
					fields = fields[1:]
				}
				for _, field := range fields {
					if strings.HasPrefix(field, "(") {
						continue
					}
					word := strings.ToLower(strings.TrimFunc(field, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }))
					if word != "" && strings.TrimFunc(word, unicode.IsDigit) != "" {
						if !slices.Contains(words, "test") {
							words = append(words, "test")
						}
						words = append(words, word)
					}
				}
				break
			}
		}
	}
	return failureCause(code, detail) + " " + strings.Join(parts, "/") + " " + strings.Join(words, " ")
}
