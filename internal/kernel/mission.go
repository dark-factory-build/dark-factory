package kernel

import (
	"context"
	"database/sql"
)

// BindMissionChild records delegation from a mission-owned parent task.
func bindMissionChild(ctx context.Context, connection *sql.Conn, parent, child TaskID, project ProjectID, at UnixMillis) error {
	_, err := connection.ExecContext(ctx, `INSERT INTO mission_task_bindings (mission_id, task_id, parent_task_id, project_id, created_at_ms)
        SELECT mission_id, ?, ?, ?, ? FROM mission_task_bindings WHERE task_id = ?
        UNION
        SELECT id, ?, ?, ?, ? FROM project_outcome_revisions current WHERE project_id = ?
        AND revision = (SELECT MAX(revision) FROM project_outcome_revisions latest WHERE latest.id = current.id)
        AND json_extract(document, '$.kind') = 'mission'
        AND (json_extract(document, '$.anchor_task_id') = ? OR ? IN (SELECT json_extract(value, '$.task_id') FROM json_each(document, '$.links')))`,
		child.Bytes(), parent.Bytes(), project.Bytes(), at.Int64(), parent.Bytes(),
		child.Bytes(), parent.Bytes(), project.Bytes(), at.Int64(), project.Bytes(), parent.String(), parent.String())
	return err
}
