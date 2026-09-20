package kernel

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxTaskAttachmentBytes = 8 << 20
const MaxTaskAttachments = 8

type TaskAttachment struct {
	Name    string `json:"name"`
	Data    []byte `json:"-"`
	Removed bool   `json:"removed"`
}

// AttachmentFileName keeps user-supplied names out of filesystem paths.
func AttachmentFileName(index int, name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if len(ext) > 12 || strings.IndexFunc(ext, func(r rune) bool { return r != '.' && (r < 'a' || r > 'z') && (r < '0' || r > '9') }) >= 0 {
		ext = ""
	}
	return fmt.Sprintf("attachment-%d%s", index+1, ext)
}

func TaskAttachmentInstruction(instruction string, attachments []TaskAttachment) (string, error) {
	if len(attachments) > MaxTaskAttachments {
		return "", ErrInvalidValue
	}
	total := 0
	for i, item := range attachments {
		if item.Removed {
			return "", fmt.Errorf("%w: attachment %q was removed; create a new task with the required files", ErrConflict, item.Name)
		}
		total += len(item.Data)
		if len(item.Name) < 1 || len(item.Name) > 255 || !utf8.ValidString(item.Name) || strings.IndexFunc(item.Name, unicode.IsControl) >= 0 || len(item.Data) == 0 || total > MaxTaskAttachmentBytes {
			return "", ErrInvalidValue
		}
		if i == 0 {
			instruction += "\n\n# Task attachments: read these files under $DARK_FACTORY_TASK_ATTACHMENTS."
		}
		instruction += fmt.Sprintf("\n# %q: $DARK_FACTORY_TASK_ATTACHMENTS/%s", item.Name, AttachmentFileName(i, item.Name))
	}
	return instruction, nil
}

func taskAttachments(ctx context.Context, connection *sql.Conn, id TaskID) ([]TaskAttachment, error) {
	rows, err := connection.QueryContext(ctx, `SELECT name, data FROM task_attachments WHERE task_id = ? ORDER BY position`, id.Bytes())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []TaskAttachment
	for rows.Next() {
		var item TaskAttachment
		if err := rows.Scan(&item.Name, &item.Data); err != nil {
			return nil, err
		}
		item.Removed = item.Data == nil
		result = append(result, item)
	}
	return result, rows.Err()
}

func (store *Store) TaskAttachments(ctx context.Context, id TaskID) ([]TaskAttachment, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Close()
	return taskAttachments(ctx, tx.connection, id)
}

func sameTaskAttachments(a, b []TaskAttachment) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || !bytes.Equal(a[i].Data, b[i].Data) {
			return false
		}
	}
	return true
}

// RemoveTaskAttachments releases file bytes, preserving task history and names.
// The task revision and terminal status are checked under the same writer lock
// as send-back; completed task timestamps must remain their run's timestamps.
func (store *Store) RemoveTaskAttachments(ctx context.Context, id TaskID, expected Revision) (Task, error) {
	if id.zero() || expected.Int64() < 1 {
		return Task{}, ErrInvalidValue
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return Task{}, err
	}
	defer tx.Close()
	task, found, err := taskByID(ctx, tx.connection, id)
	if err != nil {
		return Task{}, tx.Rollback(err)
	}
	if !found {
		return Task{}, tx.Rollback(ErrNotFound)
	}
	if task.Revision != expected {
		return Task{}, tx.Rollback(ErrRevisionConflict)
	}
	if task.Status != TaskSucceeded && task.Status != TaskCancelled {
		return Task{}, tx.Rollback(ErrConflict)
	}
	var live bool
	if err := tx.connection.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE task_id = ? AND phase <> 'terminal')`, id.Bytes()).Scan(&live); err != nil {
		return Task{}, tx.Rollback(err)
	}
	if live {
		return Task{}, tx.Rollback(ErrConflict)
	}
	if _, err := tx.connection.ExecContext(ctx, `UPDATE task_attachments SET data = NULL WHERE task_id = ? AND data IS NOT NULL`, id.Bytes()); err != nil {
		return Task{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Task{}, err
	}
	return task, nil
}

// AttachmentRetention reads or replaces the opt-in factory-wide setting.
// A missing row is disabled, including immediately after migration.
func (store *Store) AttachmentRetention(ctx context.Context, enabled *bool) (bool, error) {
	if enabled == nil {
		tx, err := store.beginRead(ctx)
		if err != nil {
			return false, err
		}
		defer tx.Close()
		var value bool
		err = tx.connection.QueryRowContext(ctx, `SELECT COALESCE((SELECT enabled FROM attachment_retention WHERE singleton = 1), 0)`).Scan(&value)
		return value, err
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Close()
	if _, err := tx.connection.ExecContext(ctx, `INSERT INTO attachment_retention(singleton, enabled) VALUES(1, ?) ON CONFLICT(singleton) DO UPDATE SET enabled = excluded.enabled`, *enabled); err != nil {
		return false, tx.Rollback(err)
	}
	return *enabled, tx.Commit(ctx)
}

// ExpireTaskAttachments rechecks the setting and terminal state under the same
// writer gate as send-back. Names and terminal task/run history stay intact.
// ponytail: one hourly scan holds the writer gate; batch cleanup if large
// attachment histories make this pause noticeable.
func (store *Store) ExpireTaskAttachments(ctx context.Context, at UnixMillis) error {
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Close()
	_, err = tx.connection.ExecContext(ctx, `UPDATE task_attachments SET data = NULL
 WHERE data IS NOT NULL AND EXISTS (SELECT 1 FROM attachment_retention WHERE enabled = 1)
 AND task_id IN (SELECT id FROM tasks WHERE status IN ('succeeded', 'cancelled') AND updated_at_ms <= ?
 AND NOT EXISTS (SELECT 1 FROM runs WHERE runs.task_id = tasks.id AND phase <> 'terminal'))`, at.Int64()-30*24*60*60*1000)
	if err != nil {
		return tx.Rollback(err)
	}
	return tx.Commit(ctx)
}
