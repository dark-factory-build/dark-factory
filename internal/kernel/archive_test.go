package kernel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const archiveFixtureReceipts = 5

// archiveFixture is a store whose worker run, while live, froze a knowledge
// context (snapshot and selected receipts), was supplied a lesson and read
// two pages of it, then ended at 80; and the content history and report that
// must read the same across the archive boundary.
func archiveFixture(t *testing.T) (*Store, string, Run, func(*Store) (string, error)) {
	t.Helper()
	ctx := context.Background()
	var lesson ContentRevision
	proposal, _ := NewBlockedProposal("archived")
	store, finalizing := finalizingReleasedRunAfter(t, RoleWorker, proposal, func(store *Store, run Run, keys AdmissionKeys) {
		spec := contentSpec(t, run.ProjectID, 46, "lesson")
		spec.Kind, spec.Author = ContentLesson, "operator:local"
		spec.SourceReferences = `{"status":"current","evidence":["source"],"verification":"go test ./internal/kernel"}`
		var err error
		if lesson, err = store.CreateContent(ctx, spec, mustTime(t, 31)); err != nil {
			t.Fatal(errors.Join(err, store.Close()))
		}
		ref := ContentAccess{ContentID: lesson.ID, ContentRevision: lesson.Revision, ByteLength: 8}
		if _, err := store.FreezeKnowledgeContext(ctx, run.ID, []ContentAccess{ref}, mustTime(t, 32)); err != nil {
			t.Fatal(errors.Join(err, store.Close()))
		}
		if err := store.RecordSuppliedContentForAttempt(ctx, keys.AttemptDigest, []ContentAccess{ref}, mustTime(t, 33)); err != nil {
			t.Fatal(errors.Join(err, store.Close()))
		}
		read := ContentAccess{ContentID: lesson.ID, ContentRevision: lesson.Revision, Kind: "read", ByteLength: 4, CreatedAt: mustTime(t, 34)}
		if err := store.RecordContentAccessForAttempt(ctx, keys.AttemptDigest, read); err != nil {
			t.Fatal(errors.Join(err, store.Close()))
		}
		read.Offset, read.CreatedAt = 4, mustTime(t, 35)
		if err := store.RecordContentAccessForAttempt(ctx, keys.AttemptDigest, read); err != nil {
			t.Fatal(errors.Join(err, store.Close()))
		}
	})
	change, _, err := store.Change(ctx, *finalizing.ChangeID)
	settlement, settleErr := NewRetainedChangeSettlement(change.Revision, change.HeadCommit)
	if err = errors.Join(err, settleErr); err != nil {
		t.Fatal(errors.Join(err, store.Close()))
	}
	run, err := store.FinalizeWorkerRun(ctx, finalizing.ID, finalizing.Revision, settlement, mustTime(t, 80))
	if err != nil {
		t.Fatal(errors.Join(err, store.Close()))
	}
	if receipts := hotReceipts(t, store); receipts != archiveFixtureReceipts {
		t.Fatal(errors.Join(fmt.Errorf("fixture receipts = %d", receipts), store.Close()))
	}
	history := func(store *Store) (string, error) {
		activity, err := store.ListContentActivity(ctx, run.ProjectID, 0, 8)
		if err != nil {
			return "", err
		}
		accesses, err := store.ListContentRevisionAccesses(ctx, run.ProjectID, lesson.ID, lesson.Revision, 0, 8)
		if err != nil {
			return "", err
		}
		report, err := store.Report(ctx, &run.ProjectID, 0, 3600000)
		// The report's final factoryd-since and WAL lines are live state, not
		// history; both can change across the close/reopen boundary.
		if err == nil && (len(report) < 2 || !strings.HasPrefix(report[len(report)-2], "factoryd since=") || !strings.HasPrefix(report[len(report)-1], "wal_bytes=")) {
			err = fmt.Errorf("report without its live lines: %q", report)
		} else if err == nil {
			report = report[:len(report)-2]
		}
		return fmt.Sprintf("%+v %+v %q", activity, accesses, report), err
	}
	return store, storePath(t, store), run, history
}

func hotReceipts(t *testing.T, store *Store) (count int) {
	t.Helper()
	if err := store.writer.QueryRowContext(context.Background(), `SELECT count(*) FROM content_accesses`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func committedGenerations(t *testing.T, store *Store) (generations []int64) {
	t.Helper()
	rows, err := store.writer.QueryContext(context.Background(), `SELECT generation FROM archive_batches ORDER BY generation`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var generation int64
		if err := rows.Scan(&generation); err != nil {
			t.Fatal(err)
		}
		generations = append(generations, generation)
	}
	return generations
}

// A crash between the archive commit and the hot one loses nothing: the
// receipts stay hot and read the same before and after a restart, and the
// next pass copies them again and commits.
func TestArchiveHistoryRetriesACopyInterruptedBeforeItsHotCommit(t *testing.T) {
	// Not parallel: archiveCopiedHook is package state.
	ctx := context.Background()
	store, path, _, history := archiveFixture(t)
	defer func() { store.Close() }()
	before, err := history(store)
	if err != nil {
		t.Fatal(err)
	}
	crash := errors.New("crash after the archive commit")
	archiveCopiedHook = func() error { return crash }
	moved, err := store.ArchiveHistory(ctx, mustTime(t, 81+HistoryRetention))
	archiveCopiedHook = nil
	if !errors.Is(err, crash) || moved != 0 || hotReceipts(t, store) != archiveFixtureReceipts {
		t.Fatalf("interrupted archive = %d, %v", moved, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err = Open(ctx, path); err != nil {
		t.Fatal(err)
	}
	if after, err := history(store); err != nil || after != before {
		t.Fatalf("history after an interrupted copy = %s, %v", after, err)
	}
	if moved, err := store.ArchiveHistory(ctx, mustTime(t, 82+HistoryRetention)); err != nil || moved != archiveFixtureReceipts || hotReceipts(t, store) != 0 {
		t.Fatalf("retried archive = %d, %v", moved, err)
	}
	if generations := committedGenerations(t, store); !reflect.DeepEqual(generations, []int64{2}) {
		t.Fatalf("retried copy reused the interrupted generation: %v", generations)
	}
	if after, err := history(store); err != nil || after != before {
		t.Fatalf("history after the retried copy = %s, %v", after, err)
	}
}

// Restoring a backup never costs the archive a generation another backup
// recorded: a store restored from before an archive pass copies its hot
// receipts again under a new generation, and a backup recording the first
// generation still reads it afterwards.
func TestArchiveHistoryKeepsEveryBackupsGenerations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, path, _, history := archiveFixture(t)
	defer func() { store.Close() }()
	before, err := history(store)
	if err != nil {
		t.Fatal(err)
	}
	backups := t.TempDir()
	backup := func(name string) string {
		t.Helper()
		target := filepath.Join(backups, name)
		if err := store.BackupTo(ctx, target); err != nil {
			t.Fatal(err)
		}
		// BackupTo writes a rollback-journal file, which Open refuses unless it is a
		// fresh image; the store this restores was in WAL mode.
		converted, err := sql.Open(driverName, target)
		if err == nil {
			_, err = converted.ExecContext(ctx, `PRAGMA journal_mode = WAL`)
			err = errors.Join(err, converted.Close())
		}
		if err != nil {
			t.Fatal(err)
		}
		return target
	}
	restore := func(source string) {
		t.Helper()
		data, err := os.ReadFile(source)
		if err == nil {
			err = store.Close()
		}
		if err == nil {
			err = os.WriteFile(path, data, 0o600)
		}
		for _, sidecar := range []string{path + "-wal", path + "-shm"} {
			if removeErr := os.Remove(sidecar); err == nil && !errors.Is(removeErr, os.ErrNotExist) {
				err = removeErr
			}
		}
		if err == nil {
			store, err = Open(ctx, path)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	unarchived := backup("unarchived")
	if moved, err := store.ArchiveHistory(ctx, mustTime(t, 81+HistoryRetention)); err != nil || moved != archiveFixtureReceipts {
		t.Fatalf("first archive = %d, %v", moved, err)
	}
	first := backup("first")

	restore(unarchived)
	if after, err := history(store); err != nil || after != before || hotReceipts(t, store) != archiveFixtureReceipts {
		t.Fatalf("history restored from before the archive = %s, %v", after, err)
	}
	if moved, err := store.ArchiveHistory(ctx, mustTime(t, 82+HistoryRetention)); err != nil || moved != archiveFixtureReceipts {
		t.Fatalf("archive after the restore = %d, %v", moved, err)
	}
	if generations := committedGenerations(t, store); !reflect.DeepEqual(generations, []int64{2}) {
		t.Fatalf("archive after the restore committed %v, want a new generation", generations)
	}

	restore(first)
	if after, err := history(store); err != nil || after != before || !reflect.DeepEqual(committedGenerations(t, store), []int64{1}) {
		t.Fatalf("history restored from the first generation = %s, %v", after, err)
	}
}

// A terminal run's receipts move to the archive only after HistoryRetention,
// once; content history and the report read the same across the boundary and
// a restart; a generation the store never committed is invisible and kept;
// and a missing or altered committed generation fails crossing reads and the
// next pass closed while hot reads go on.
func TestArchiveHistoryMovesReceiptsAndReadsAcrossTheBoundary(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, path, run, history := archiveFixture(t)
	defer func() { store.Close() }()
	before, err := history(store)
	if err != nil {
		t.Fatal(err)
	}
	hotReceipts := func() int { return hotReceipts(t, store) }

	if moved, err := store.ArchiveHistory(ctx, mustTime(t, 80+HistoryRetention)); err != nil || moved != 0 || hotReceipts() != archiveFixtureReceipts {
		t.Fatalf("archive within retention = %d, %v", moved, err)
	}
	if moved, err := store.ArchiveHistory(ctx, mustTime(t, 81+HistoryRetention)); err != nil || moved != archiveFixtureReceipts || hotReceipts() != 0 {
		t.Fatalf("archive past retention = %d, %v", moved, err)
	}
	if moved, err := store.ArchiveHistory(ctx, mustTime(t, 82+HistoryRetention)); err != nil || moved != 0 {
		t.Fatalf("repeated archive = %d, %v", moved, err)
	}
	if after, err := history(store); err != nil || after != before {
		t.Fatalf("history across the archive = %s, %v\nwant %s", after, err, before)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err = Open(ctx, path); err != nil {
		t.Fatal(err)
	}
	if after, err := history(store); err != nil || after != before {
		t.Fatalf("history after restart = %s, %v", after, err)
	}

	archive, err := sql.Open(driverName, archiveDataSource(path+"-archive", "rw"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	// A generation the store never committed is never read, and the archive keeps it.
	if _, err := archive.ExecContext(ctx, `INSERT INTO content_accesses SELECT 2, run_id, content_id, content_revision, kind, byte_offset + 100, byte_length, created_at_ms, project_id, agent_id, task_id, task_work_revision FROM content_accesses WHERE generation = 1;
		INSERT INTO batches VALUES (2, 5, zeroblob(32), 1)`); err != nil {
		t.Fatal(err)
	}
	if after, err := history(store); err != nil || after != before {
		t.Fatalf("history with an uncommitted generation = %s, %v", after, err)
	}
	var kept int
	if moved, err := store.ArchiveHistory(ctx, mustTime(t, 83+HistoryRetention)); err != nil || moved != 0 {
		t.Fatalf("archive beside an uncommitted generation = %d, %v", moved, err)
	}
	if err := archive.QueryRowContext(ctx, `SELECT count(*) FROM content_accesses WHERE generation = 2`).Scan(&kept); err != nil || kept != archiveFixtureReceipts {
		t.Fatalf("uncommitted generation kept %d rows, %v", kept, err)
	}

	failsClosed := func(cause string) {
		t.Helper()
		if _, err := history(store); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("history with %s = %v", cause, err)
		}
		if _, err := store.ArchiveHistory(ctx, mustTime(t, 84+HistoryRetention)); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("archive with %s = %v", cause, err)
		}
		if _, _, err := store.Run(ctx, run.ID); err != nil {
			t.Fatalf("hot read with %s = %v", cause, err)
		}
	}
	if _, err := archive.ExecContext(ctx, `UPDATE content_accesses SET byte_length = byte_length + 1 WHERE kind = 'read'`); err != nil {
		t.Fatal(err)
	}
	failsClosed("an altered receipt")
	if _, err := archive.ExecContext(ctx, `UPDATE content_accesses SET byte_length = byte_length - 1 WHERE kind = 'read'`); err != nil {
		t.Fatal(err)
	}
	if after, err := history(store); err != nil || after != before {
		t.Fatalf("history with the receipt restored = %s, %v", after, err)
	}
	if _, err := archive.ExecContext(ctx, `DELETE FROM content_accesses WHERE kind = 'snapshot'`); err != nil {
		t.Fatal(err)
	}
	failsClosed("a truncated generation")
	if _, err := archive.ExecContext(ctx, `DROP TABLE content_accesses; DROP TABLE batches`); err != nil {
		t.Fatal(err)
	}
	failsClosed("a missing generation")
	if err := errors.Join(archive.Close(), os.Remove(path+"-archive")); err != nil {
		t.Fatal(err)
	}
	if _, err := history(store); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("history without the archive = %v", err)
	}
}
