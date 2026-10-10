package kernel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// A specialist (Agent.Specialist) reviews on its own schedule: its standing
// instruction arrives as an ordinary worker task titled overseerWakeTitle (its
// carrier), queued below explicit work.
const specialistWakePriority = -100

// specialistCarrierSQL is task t, assigned to agent a, being a specialist's
// review carrier.
const specialistCarrierSQL = `(a.role = 'worker' AND a.idle_policy = 'standing_instruction' AND t.assigned_agent_id = a.id AND t.title = '` + overseerWakeTitle + `')`

// specialistCarrierHeldSQL holds carrier t back while admitting it would take
// the last free worker slot (?1 is the factory capacity), or exceed its
// project's specialist_runs.
const specialistCarrierHeldSQL = `((SELECT COUNT(*) FROM runs WHERE role = 'worker' AND phase <> 'terminal') + 1 >= ?1
	OR (SELECT COUNT(*) FROM runs AS r JOIN agents AS s ON s.id = r.agent_id JOIN tasks AS c ON c.id = r.task_id
		WHERE r.project_id = t.project_id AND r.phase <> 'terminal' AND s.role = 'worker' AND s.idle_policy = 'standing_instruction' AND c.title = '` + overseerWakeTitle + `')
	>= (SELECT specialist_runs FROM projects WHERE id = t.project_id))`

// metaC and metaD are the knowledge metadata of content c and d, or '{}'
// when it is not JSON.
const (
	metaC = `CASE WHEN json_valid(c.source_references) THEN c.source_references ELSE '{}' END`
	metaD = `CASE WHEN json_valid(d.source_references) THEN d.source_references ELSE '{}' END`
)

// A proposal is the live latest revision of an observation recorded as one;
// its resolution is a live latest decision naming it. An open proposal has
// none. Both read content revision c, and the resolution is d.
const (
	latestProposalSQL = `c.kind = 'observation' AND c.deprecated = 0 AND c.revision = (SELECT MAX(revision) FROM project_content_revisions WHERE id = c.id)
	AND json_extract(` + metaC + `, '$.record_type') = 'proposal'`
	proposalResolutionSQL = `d.project_id = c.project_id AND d.kind = 'decision' AND d.deprecated = 0 AND d.revision = (SELECT MAX(revision) FROM project_content_revisions WHERE id = d.id)
	AND json_extract(` + metaD + `, '$.record_type') = 'proposal' AND lower(json_extract(` + metaD + `, '$.record_id')) = lower(hex(c.id))`
	openProposalSQL = latestProposalSQL + ` AND NOT EXISTS (SELECT 1 FROM project_content_revisions AS d WHERE ` + proposalResolutionSQL + `)`
	// authorAgentSQL is the agent that authored content c through an attempt.
	authorAgentSQL = `unhex(substr(c.author, instr(c.author, ' agent:') + 7, 32))`
)

// SpecialistState is a specialist's review schedule, as the console shows it.
type SpecialistState struct {
	NextReviewAt      int64  // 0: none scheduled
	NextReason        string // initial, scheduled, events, or ""
	Waiting           string // "", budget, paused, stopped, queued, capacity
	QuietReviews      int
	OpenProposals     int
	OpenProposalLimit int
	LastReviewTaskID  string
}

// specialistPrior is what a wake hands on from the specialist's newest carrier.
type specialistPrior struct {
	task, base, checkpoint string
}

// specialistSchedule is the one rule for when a specialist reviews next. With
// no carrier it is due now (initial); otherwise after its cadence, doubled for
// each of its newest settled carriers (at most three) that contributed
// nothing (scheduled), or a quarter cadence after the last one settled when
// its wake_on classes saw an event since that carrier was created (events).
// It returns the event lines a wake names. The same rule schedules the wake
// and serves the console's projection.
func specialistSchedule(ctx context.Context, c *sql.Conn, agent Agent) (SpecialistState, specialistPrior, []string, error) {
	var state SpecialistState
	var prior specialistPrior
	project, found, err := projectByID(ctx, c, agent.ProjectID)
	if err != nil || !found {
		return state, prior, nil, errors.Join(err, ErrCorruptState)
	}
	state.OpenProposalLimit = int(project.SpecialistOpenProposals)
	if err := c.QueryRowContext(ctx, `SELECT count(*) FROM project_content_revisions AS c WHERE c.project_id = ? AND `+openProposalSQL+` AND `+authorAgentSQL+` = ?`,
		agent.ProjectID.Bytes(), agent.ID.Bytes()).Scan(&state.OpenProposals); err != nil {
		return state, prior, nil, err
	}
	var status string
	var created, settled int64
	var raw []byte
	err = c.QueryRowContext(ctx, `SELECT t.id, t.status, t.created_at_ms, COALESCE(t.completed_at_ms, t.updated_at_ms),
		COALESCE(t.result, t.blocked_reason, (SELECT terminal_detail FROM runs WHERE task_id = t.id ORDER BY admitted_at_ms DESC LIMIT 1), ''),
		COALESCE((SELECT lower(hex(base_commit)) FROM changes WHERE task_id = t.id AND base_commit IS NOT NULL ORDER BY updated_at_ms DESC LIMIT 1), '')
		FROM tasks AS t WHERE t.assigned_agent_id = ? AND t.title = ? ORDER BY t.created_at_ms DESC, t.id DESC LIMIT 1`,
		agent.ID.Bytes(), overseerWakeTitle).Scan(&raw, &status, &created, &settled, &prior.checkpoint, &prior.base)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return state, prior, nil, err
	}
	carried := err == nil
	if carried {
		id, err := TaskIDFromBytes(raw)
		if err != nil {
			return state, prior, nil, err
		}
		prior.task, state.LastReviewTaskID = id.String(), id.String()
		rows, err := c.QueryContext(ctx, `SELECT EXISTS (SELECT 1 FROM runs AS r JOIN project_content_revisions AS p ON p.author GLOB 'run:' || lower(hex(r.id)) || ' *' WHERE r.task_id = t.id)
			OR EXISTS (SELECT 1 FROM peer_questions WHERE source_task_id = t.id)
			FROM tasks AS t WHERE t.assigned_agent_id = ? AND t.title = ? AND t.status NOT IN ('queued', 'running') ORDER BY t.created_at_ms DESC, t.id DESC LIMIT 3`,
			agent.ID.Bytes(), overseerWakeTitle)
		if err != nil {
			return state, prior, nil, err
		}
		for rows.Next() {
			var contributed bool
			if err := rows.Scan(&contributed); err != nil {
				return state, prior, nil, errors.Join(err, rows.Close())
			}
			if contributed {
				break
			}
			state.QuietReviews++
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return state, prior, nil, err
		}
	}
	switch {
	case agent.Archived:
		state.Waiting = "stopped"
	case agent.Paused:
		state.Waiting = "paused"
	case status == "queued" || status == "running":
		if status == "queued" {
			factory, err := factoryState(ctx, c)
			if err != nil {
				return state, prior, nil, err
			}
			held := false
			if err := c.QueryRowContext(ctx, `SELECT `+specialistCarrierHeldSQL+` FROM tasks AS t WHERE t.id = ?2`, factory.Capacity, raw).Scan(&held); err != nil {
				return state, prior, nil, err
			}
			state.Waiting = "queued"
			if held {
				state.Waiting = "capacity"
			}
		}
	case agent.Idle.RunBudget > 0 && agent.Idle.RunsUsed >= agent.Idle.RunBudget:
		state.Waiting = "budget"
	case !carried:
		state.NextReviewAt, state.NextReason = agent.UpdatedAt.Int64(), "initial"
	default:
		cadence := int64(agent.Idle.AfterSeconds) * 1000
		state.NextReviewAt, state.NextReason = settled+cadence<<state.QuietReviews, "scheduled"
		events, err := specialistEvents(ctx, c, agent, created)
		if err != nil {
			return state, prior, nil, err
		}
		if len(events) != 0 && settled+cadence/4 < state.NextReviewAt {
			state.NextReviewAt, state.NextReason = settled+cadence/4, "events"
		}
		return state, prior, events, nil
	}
	return state, prior, nil, nil
}

// specialistEvents is what happened since a specialist's last carrier was
// created, in its wake_on classes: a worker task (not a specialist's carrier)
// that failed or blocked (failures); a pull request merged (merges). A review
// escalation is the overseer's item, not an event. Carriers, knowledge, proposals, decisions and
// peer questions are never events, so specialists cannot wake each other.
func specialistEvents(ctx context.Context, c *sql.Conn, agent Agent, since int64) ([]string, error) {
	failures, merges := strings.Contains(agent.Idle.WakeOn, "failures"), strings.Contains(agent.Idle.WakeOn, "merges")
	if !failures && !merges {
		return nil, nil
	}
	rows, err := c.QueryContext(ctx, `SELECT line FROM (
		SELECT printf('- task %s "%s" %s: %s', lower(hex(t.id)), replace(substr(t.title, 1, 60), char(10), ' '), t.status,
			replace(substr(COALESCE(t.blocked_reason, (SELECT terminal_detail FROM runs WHERE task_id = t.id ORDER BY admitted_at_ms DESC LIMIT 1), ''), 1, 120), char(10), ' ')) AS line, t.updated_at_ms AS at
		FROM tasks AS t JOIN agents AS a ON a.id = t.assigned_agent_id
		WHERE ?3 AND t.project_id = ?1 AND a.role = 'worker' AND t.status IN ('failed', 'blocked') AND t.updated_at_ms > ?2 AND NOT `+specialistCarrierSQL+`
		UNION ALL SELECT printf('- PR #%s merged: %s', identity, replace(substr(COALESCE(json_extract(document, '$.title'), ''), 1, 80), char(10), ' ')), observed_at_ms
		FROM production_records WHERE ?4 AND project_id = ?1 AND kind = 'pull_request' AND json_extract(document, '$.state') = 'merged'
		  AND COALESCE(unixepoch(json_extract(document, '$.merged_at')) * 1000, observed_at_ms) > ?2
	) ORDER BY at DESC LIMIT 16`, agent.ProjectID.Bytes(), since, failures, merges)
	if err != nil {
		return nil, err
	}
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		lines = append(lines, line)
	}
	return lines, errors.Join(rows.Err(), rows.Close())
}

// specialistWake is the body of a due specialist's next carrier: its
// instruction, the schedule, the prior carrier's checkpoint, its proposals'
// follow-ups and the events since. What does not fit the provider's bound is
// dropped in order (events, follow-ups, other specialists, then the checkpoint); never the
// instruction, which was checked against the bound when it was set.
func specialistWake(ctx context.Context, c *sql.Conn, agent Agent, state SpecialistState, prior specialistPrior, events []string) (string, error) {
	rows, err := c.QueryContext(ctx, `SELECT printf('- proposal %s "%s": %s', lower(hex(c.id)), replace(substr(c.title, 1, 80), char(10), ' '),
		COALESCE((SELECT CASE WHEN json_extract(`+metaD+`, '$.task_id') IS NULL
				THEN 'declined: ' || replace(substr(COALESCE(NULLIF(d.description, ''), d.title), 1, 120), char(10), ' ')
				ELSE 'accepted task ' || lower(json_extract(`+metaD+`, '$.task_id')) || ' ' || COALESCE((SELECT status FROM tasks WHERE id = unhex(json_extract(`+metaD+`, '$.task_id'))), 'unknown') END
			FROM project_content_revisions AS d WHERE `+proposalResolutionSQL+` ORDER BY d.created_at_ms DESC LIMIT 1), 'open'))
		FROM project_content_revisions AS c WHERE c.project_id = ? AND `+latestProposalSQL+` AND `+authorAgentSQL+` = ? ORDER BY c.created_at_ms DESC, c.id LIMIT 8`,
		agent.ProjectID.Bytes(), agent.ID.Bytes())
	if err != nil {
		return "", err
	}
	var followUps []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return "", errors.Join(err, rows.Close())
		}
		followUps = append(followUps, line)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return "", err
	}
	peerRows, err := c.QueryContext(ctx, `SELECT name, idle_instruction, paused FROM agents WHERE project_id = ? AND id <> ? AND role = 'worker' AND idle_policy = 'standing_instruction' AND archived = 0 ORDER BY name, id LIMIT 12`,
		agent.ProjectID.Bytes(), agent.ID.Bytes())
	if err != nil {
		return "", err
	}
	var peers []string
	for peerRows.Next() {
		var name, instruction string
		var paused bool
		if err := peerRows.Scan(&name, &instruction, &paused); err != nil {
			return "", errors.Join(err, peerRows.Close())
		}
		line, _, _ := strings.Cut(instruction, "\n")
		line = "- " + name + ": " + strings.ToValidUTF8(line[:min(len(line), 120)], "")
		if paused {
			line += " (paused)"
		}
		peers = append(peers, line)
	}
	if err := errors.Join(peerRows.Err(), peerRows.Close()); err != nil {
		return "", err
	}
	budget := "unlimited"
	if agent.Idle.RunBudget > 0 {
		budget = strconv.FormatUint(uint64(agent.Idle.RunBudget), 10)
	}
	header := fmt.Sprintf("Specialist wake: reason=%s; prior_task_id=%s; prior_base=%s; quiet_reviews=%d; open_proposals=%d/%d; reviews_used=%d/%s.",
		state.NextReason, prior.task, prior.base, state.QuietReviews, state.OpenProposals, state.OpenProposalLimit, agent.Idle.RunsUsed+1, budget)
	checkpoint := strings.ToValidUTF8(prior.checkpoint[:min(len(prior.checkpoint), 2048)], "")
	for {
		body := agent.Idle.Instruction + "\n\n" + header
		if len(peers) != 0 {
			body += "\nOther specialists:\n" + strings.Join(peers, "\n")
		}
		body += "\nPrior checkpoint:\n" + checkpoint
		if checkpoint == "" {
			body += "none"
		}
		if len(followUps) != 0 {
			body += "\nFollow-ups:\n" + strings.Join(followUps, "\n")
		}
		if len(events) != 0 {
			body += "\nEvents since prior review:\n" + strings.Join(events, "\n")
		}
		switch {
		case wakeBodyFits(agent.Provider, body):
			return body, nil
		case len(events) != 0:
			events = nil
		case len(followUps) != 0:
			followUps = nil
		case len(peers) != 0:
			peers = nil
		case len(checkpoint) > 256:
			checkpoint = strings.ToValidUTF8(checkpoint[:256], "")
		case checkpoint != "":
			checkpoint = ""
		default:
			return agent.Idle.Instruction, nil
		}
	}
}

// stopSpecialist ends a specialist's own reviews inside its archive
// transaction: a live carrier run is stopped as an operator stop (or the
// archiving overseer's) stops one, revoking its credential, and its queued
// carriers are cancelled. Its other work is the archive's to refuse.
func (store *Store) stopSpecialist(ctx context.Context, tx *writeTx, agent Agent, overseer *Run, at UnixMillis) error {
	var runs [][]byte
	rows, err := tx.connection.QueryContext(ctx, `SELECT r.id FROM runs AS r JOIN tasks AS t ON t.id = r.task_id WHERE r.agent_id = ? AND r.phase IN ('admitted', 'running') AND t.title = ?`, agent.ID.Bytes(), overseerWakeTitle)
	for err == nil && rows.Next() {
		var id []byte
		if err = rows.Scan(&id); err == nil {
			runs = append(runs, id)
		}
	}
	if rows != nil {
		err = errors.Join(err, rows.Err(), rows.Close())
	}
	if err != nil {
		return err
	}
	for _, raw := range runs {
		id, err := RunIDFromBytes(raw)
		if err != nil {
			return err
		}
		run, found, err := runByID(ctx, tx.connection, id)
		if err != nil || !found {
			return errors.Join(err, ErrCorruptState)
		}
		task, found, err := taskByID(ctx, tx.connection, run.TaskID)
		if err != nil || !found {
			return errors.Join(err, ErrCorruptState)
		}
		digest := sha256.Sum256([]byte("specialist-stop/" + run.ID.String()))
		operation, err := TaskInterventionIDFromBytes(digest[:IDBytes])
		if err != nil {
			return err
		}
		request := TaskInterventionRequest{OperationID: operation, TaskID: task.ID, RunID: run.ID, ExpectedTaskRevision: task.Revision, ExpectedRunRevision: run.Revision, Actor: TaskInterventionOperator, Kind: TaskInterventionStop}
		if overseer != nil {
			request.Actor, request.ActorRunID = TaskInterventionOrchestrator, &overseer.ID
		}
		if _, err := store.stopRunTx(ctx, tx, request, nil, at, false); err != nil {
			return err
		}
	}
	var pending []pendingInvalidation
	rows, err = tx.connection.QueryContext(ctx, `SELECT id, revision + 1 FROM tasks WHERE assigned_agent_id = ? AND status = 'queued' AND title = ?`, agent.ID.Bytes(), overseerWakeTitle)
	for err == nil && rows.Next() {
		var item pendingInvalidation
		item.kind = EntityTask
		if err = rows.Scan(&item.id, &item.revision); err == nil {
			pending = append(pending, item)
		}
	}
	if rows != nil {
		err = errors.Join(err, rows.Err(), rows.Close())
	}
	if err != nil {
		return err
	}
	for _, item := range pending {
		result, err := tx.connection.ExecContext(ctx, `UPDATE tasks SET status = 'cancelled', completed_at_ms = ?1, revision = ?2, updated_at_ms = ?1 WHERE id = ?3 AND status = 'queued' AND revision = ?2 - 1`, at.Int64(), item.revision, item.id)
		if err := requireOneRow(result, err); err != nil {
			return err
		}
	}
	return appendInvalidations(ctx, tx.connection, at, pending)
}

// validateSpecialistRecord applies the record rules of proposals, their
// resolutions, reviews and research (see SPECIALISTS.md). A nil attempt is the
// operator, who is held only to the shape rules.
func validateSpecialistRecord(ctx context.Context, c *sql.Conn, spec NewContent, m KnowledgeMetadata, a *AttemptAuthority) error {
	switch {
	case m.RecordType == "review":
		if spec.Kind == ContentObservation && (m.SourceRevision == "" || !productionSHA(m.SourceRevision)) {
			return fmt.Errorf("%w: a review names the source_revision it examined", ErrInvalidValue)
		}
	case m.RecordType == "research":
		if m.RecordID != "" || len(m.Evidence) == 0 {
			return fmt.Errorf("%w: research cites its evidence", ErrInvalidValue)
		}
	case m.RecordType == "contribution" || m.RecordType == "amendment":
		if spec.Kind != ContentObservation || m.TaskID == "" && m.RecordID == "" {
			return fmt.Errorf("%w: a %s names its task_id or record_id", ErrInvalidValue, m.RecordType)
		}
	case m.RecordType == "follow_up":
		id, err := knowledgeID(m.RecordID)
		if err != nil || spec.Kind != ContentObservation {
			return ErrInvalidValue
		}
		var proposals int
		if err := c.QueryRowContext(ctx, `SELECT count(*) FROM project_content_revisions AS c WHERE c.id = ? AND c.project_id = ? AND `+latestProposalSQL, id, spec.ProjectID.Bytes()).Scan(&proposals); err != nil {
			return err
		}
		if proposals == 0 {
			return fmt.Errorf("%w: a follow_up names an existing proposal", ErrInvalidValue)
		}
	case m.RecordType == "proposal" && spec.Kind == ContentObservation:
		if m.RecordID != "" {
			return ErrInvalidValue
		}
		if a == nil {
			return nil
		}
		var mine, open, limit int
		if err := c.QueryRowContext(ctx, `SELECT
			(SELECT count(DISTINCT c.id) FROM project_content_revisions AS c WHERE c.author GLOB 'run:' || ?3 || ' *' AND c.id <> ?2 AND `+latestProposalSQL+`),
			(SELECT count(*) FROM project_content_revisions AS c WHERE c.project_id = ?1 AND c.id <> ?2 AND `+openProposalSQL+` AND `+authorAgentSQL+` = ?4),
			(SELECT specialist_open_proposals FROM projects WHERE id = ?1)`,
			spec.ProjectID.Bytes(), spec.ID.Bytes(), a.RunID.String(), a.AgentID.Bytes()).Scan(&mine, &open, &limit); err != nil {
			return err
		}
		if mine != 0 || open >= limit {
			return fmt.Errorf("%w: a run makes one proposal, and an agent keeps at most %d open", ErrConflict, limit)
		}
	case m.RecordType == "proposal" && spec.Kind == ContentDecision:
		if a != nil && a.Role != RoleOrchestrator {
			return fmt.Errorf("%w: only the overseer or the operator resolves a proposal", ErrUnauthorized)
		}
		id, err := knowledgeID(m.RecordID)
		if err != nil {
			return err
		}
		var proposals, active int
		if err := c.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM project_content_revisions AS c WHERE c.id = ?2 AND c.project_id = ?1 AND `+latestProposalSQL+`),
			(SELECT count(*) FROM project_content_revisions AS d JOIN tasks AS t ON t.id = unhex(json_extract(`+metaD+`, '$.task_id'))
				WHERE d.project_id = ?1 AND d.id <> ?3 AND d.kind = 'decision' AND d.deprecated = 0 AND d.revision = (SELECT MAX(revision) FROM project_content_revisions WHERE id = d.id)
				AND json_extract(`+metaD+`, '$.record_type') = 'proposal' AND t.status IN ('queued', 'running', 'blocked'))`,
			spec.ProjectID.Bytes(), id, spec.ID.Bytes()).Scan(&proposals, &active); err != nil {
			return err
		}
		if proposals == 0 {
			return fmt.Errorf("%w: unknown proposal", ErrInvalidValue)
		}
		// One self-generated implementation is active at a time.
		if a != nil && m.TaskID != "" && active != 0 {
			return fmt.Errorf("%w: an accepted proposal's task is still active", ErrConflict)
		}
	default:
		return ErrInvalidValue
	}
	return nil
}
