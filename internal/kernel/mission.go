package kernel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
)

// MissionCreate is the one transaction that establishes a mission's durable
// identity and its normal overseer anchor task.
type MissionCreate struct {
	ID                    OutcomeID
	ProjectID             ProjectID
	OwnerAgentID          AgentID
	ExpectedAgentRevision Revision
	Objective             string
	Criteria              string
}

type MissionCreateResult struct {
	Mission OutcomeRevision
	Anchor  Task
}

func (store *Store) ListMissionTasks(ctx context.Context, project ProjectID, mission OutcomeID, offset, limit int) ([]Task, int, error) {
	if limit <= 0 || limit > 16 || offset < 0 {
		return nil, 0, fmt.Errorf("%w: invalid mission task page", ErrInvalidValue)
	}
	tx, err := store.beginRead(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Close()
	rows, err := tx.connection.QueryContext(ctx, `SELECT task_id FROM mission_task_bindings WHERE project_id = ? AND mission_id = ? ORDER BY created_at_ms, task_id LIMIT ? OFFSET ?`, project.Bytes(), mission.Bytes(), limit+1, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]Task, 0, limit)
	next := 0
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, 0, err
		}
		id, err := TaskIDFromBytes(raw)
		if err != nil {
			return nil, 0, err
		}
		item, found, err := taskByID(ctx, tx.connection, id)
		if err != nil {
			return nil, 0, err
		}
		if !found {
			continue
		}
		if len(items) == limit {
			next = offset + limit
			break
		}
		items = append(items, item)
	}
	return items, next, rows.Err()
}

func missionDerivedID(prefix string, id OutcomeID) (identifier, error) {
	hash := sha256.Sum256(append([]byte(prefix), id.Bytes()...))
	return identifierFromBytes(hash[:IDBytes])
}

func missionAnchorIDs(id OutcomeID) (TaskID, IncarnationID, error) {
	task, err := missionDerivedID("dark-factory:mission-task:", id)
	if err != nil {
		return TaskID{}, IncarnationID{}, err
	}
	incarnation, err := missionDerivedID("dark-factory:mission-incarnation:", id)
	if err != nil {
		return TaskID{}, IncarnationID{}, err
	}
	return TaskID{task}, IncarnationID{incarnation}, nil
}

// CreateMissionForBrowser checks authority and creates the mission and anchor
// in one transaction. Retrying the supplied mission ID replays only when all
// immutable inputs match.
func (store *Store) CreateMissionForBrowser(ctx context.Context, clientID BrowserClientID, spec MissionCreate, at UnixMillis) (MissionCreateResult, error) {
	if clientID.zero() || spec.ID.zero() || spec.ProjectID.zero() || spec.OwnerAgentID.zero() || spec.ExpectedAgentRevision.Int64() < 1 || spec.Objective == "" || spec.Criteria == "" {
		return MissionCreateResult{}, fmt.Errorf("%w: invalid mission create", ErrInvalidValue)
	}
	taskID, incarnationID, err := missionAnchorIDs(spec.ID)
	if err != nil {
		return MissionCreateResult{}, err
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return MissionCreateResult{}, err
	}
	defer tx.Close()
	client, found, err := browserClientByID(ctx, tx.connection, clientID)
	if err != nil {
		return MissionCreateResult{}, tx.Rollback(err)
	}
	if !found || client.RevokedAt != nil || !client.CapabilityMask.Has(BrowserCapabilityHumanActions) || !client.CapabilityMask.Has(BrowserCapabilityPrivateHumanRequestDetail) {
		return MissionCreateResult{}, tx.Rollback(ErrUnauthorized)
	}
	agent, found, err := agentByID(ctx, tx.connection, spec.OwnerAgentID)
	if err != nil {
		return MissionCreateResult{}, tx.Rollback(err)
	}
	if !found || agent.ProjectID != spec.ProjectID || agent.Role != RoleOrchestrator || agent.Archived || agent.Revision != spec.ExpectedAgentRevision {
		return MissionCreateResult{}, tx.Rollback(ErrRevisionConflict)
	}
	title := "Mission anchor"
	body := spec.Objective + "\n\nAcceptance criteria:\n" + spec.Criteria
	taskSpec := NewTask{ID: taskID, ProjectID: spec.ProjectID, AssignedAgentID: spec.OwnerAgentID, IncarnationID: incarnationID, Title: title, Body: body, Priority: 0}
	if err := validateNewTask(taskSpec); err != nil {
		return MissionCreateResult{}, tx.Rollback(err)
	}
	existing, replay, err := taskCreationReplay(ctx, tx.connection, taskSpec)
	if err != nil {
		return MissionCreateResult{}, tx.Rollback(err)
	}
	if replay {
		mission, e := outcomeOnConnection(ctx, tx.connection, spec.ProjectID, spec.ID, 0)
		if e != nil || mission.Document.Kind != "mission" || mission.Document.Objective != spec.Objective || mission.Document.Criteria != spec.Criteria || mission.Document.AnchorTaskID != taskID.String() {
			if e == nil {
				e = ErrConflict
			}
			return MissionCreateResult{}, tx.Rollback(e)
		}
		if e = tx.Rollback(nil); e != nil {
			return MissionCreateResult{}, e
		}
		return MissionCreateResult{Mission: mission, Anchor: existing}, nil
	}
	anchor, err := insertTaskOnConnection(ctx, tx.connection, taskSpec, at)
	if err != nil {
		return MissionCreateResult{}, tx.Rollback(err)
	}
	if _, err = tx.connection.ExecContext(ctx, `INSERT INTO mission_task_bindings (mission_id, task_id, parent_task_id, project_id, created_at_ms) VALUES (?, ?, NULL, ?, ?)`, spec.ID.Bytes(), anchor.ID.Bytes(), spec.ProjectID.Bytes(), at.Int64()); err != nil {
		return MissionCreateResult{}, tx.Rollback(err)
	}
	mission, err := writeOutcomeTx(ctx, tx, nil, "browser:"+client.ID.String(), "human", NewOutcome{ID: spec.ID, ProjectID: spec.ProjectID, Document: OutcomeDocument{Kind: "mission", Objective: spec.Objective, Criteria: spec.Criteria, AnchorTaskID: anchor.ID.String(), AnchorWorkRevision: 1, State: "open"}}, 0, at)
	if err != nil {
		return MissionCreateResult{}, tx.Rollback(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return MissionCreateResult{}, err
	}
	return MissionCreateResult{Mission: mission, Anchor: anchor}, nil
}

// BindMissionChild records delegation from a mission-owned parent task.
func bindMissionChild(ctx context.Context, connection *sql.Conn, parent, child TaskID, project ProjectID, at UnixMillis) error {
	_, err := connection.ExecContext(ctx, `INSERT INTO mission_task_bindings (mission_id, task_id, parent_task_id, project_id, created_at_ms) SELECT mission_id, ?, ?, ?, ? FROM mission_task_bindings WHERE task_id = ?`, child.Bytes(), parent.Bytes(), project.Bytes(), at.Int64(), parent.Bytes())
	return err
}
