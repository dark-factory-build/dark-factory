package kernel

import (
	"context"
	"database/sql"
)

// IntakePriority preserves the legacy highest matching label rule. Scheduling
// metadata never participates in accepted content or deterministic task IDs.
func IntakePriority(source IntakeSource, labels []string) int64 {
	priority, matched := source.PriorityDefault, false
	for _, label := range labels {
		if value, found := source.PriorityByLabel[label]; found && (!matched || value > priority) {
			priority, matched = value, true
		}
	}
	return priority
}

func loadIntakePriorities(ctx context.Context, connection *sql.Conn, source *IntakeSource) error {
	rows, err := connection.QueryContext(ctx, `SELECT label, priority FROM intake_source_priorities WHERE source_id = ?`, source.ID.Bytes())
	if err != nil {
		return err
	}
	defer rows.Close()
	source.PriorityByLabel = map[string]int64{}
	for rows.Next() {
		var label string
		var priority int64
		if err := rows.Scan(&label, &priority); err != nil {
			return err
		}
		if label == "" {
			source.PriorityDefault = priority
		} else {
			source.PriorityByLabel[label] = priority
		}
	}
	return rows.Err()
}
func writeIntakePriorities(ctx context.Context, connection *sql.Conn, source IntakeSource) error {
	if _, err := connection.ExecContext(ctx, `DELETE FROM intake_source_priorities WHERE source_id = ?`, source.ID.Bytes()); err != nil {
		return err
	}
	for label, priority := range source.PriorityByLabel {
		if _, err := connection.ExecContext(ctx, `INSERT INTO intake_source_priorities(source_id,label,priority) VALUES(?,?,?)`, source.ID.Bytes(), label, priority); err != nil {
			return err
		}
	}
	_, err := connection.ExecContext(ctx, `INSERT INTO intake_source_priorities(source_id,label,priority) VALUES(?,'',?)`, source.ID.Bytes(), source.PriorityDefault)
	return err
}
