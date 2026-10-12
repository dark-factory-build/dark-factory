package kernel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
)

// A terminal run's receipts move to the archive only after HistoryRetention,
// once; content history and the report read the same across the boundary and
// a restart; an uncommitted generation is invisible and discarded; and a
// missing or altered committed generation fails crossing reads and the next
// pass closed while hot reads go on.
func TestArchiveHistoryMovesReceiptsAndReadsAcrossTheBoundary(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	proposal, _ := NewSuccessProposal("verified result")
	store, finalizing := finalizingReleasedRun(t, RoleOrchestrator, proposal)
	path := storePath(t, store)
	defer func() { store.Close() }()
	run, err := store.FinalizeRun(ctx, finalizing.ID, finalizing.Revision, mustTime(t, 80))
	if err != nil {
		t.Fatal(err)
	}
	spec := contentSpec(t, run.ProjectID, 46, "lesson")
	spec.Kind, spec.Author = ContentLesson, "operator:local"
	spec.SourceReferences = `{"status":"current","evidence":["source"],"verification":"go test ./internal/kernel"}`
	lesson, err := store.CreateContent(ctx, spec, mustTime(t, 40))
	if err != nil {
		t.Fatal(err)
	}
	corruptSQLUnvalidated(t, store, `INSERT INTO content_accesses(run_id, content_id, content_revision, kind, byte_offset, byte_length, created_at_ms) VALUES
		(?1, NULL, NULL, 'snapshot', 0, 0, 41), (?1, ?2, 1, 'read', 0, 4, 42), (?1, ?2, 1, 'read', 4, 4, 43)`, run.ID.Bytes(), lesson.ID.Bytes())
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
		return fmt.Sprintf("%+v %+v %q", activity, accesses, report), err
	}
	before, err := history(store)
	if err != nil {
		t.Fatal(err)
	}
	hotReceipts := func() (count int) {
		t.Helper()
		if err := store.writer.QueryRowContext(ctx, `SELECT count(*) FROM content_accesses`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}

	if moved, err := store.ArchiveHistory(ctx, mustTime(t, 80+HistoryRetention)); err != nil || moved != 0 || hotReceipts() != 3 {
		t.Fatalf("archive within retention = %d, %v", moved, err)
	}
	if moved, err := store.ArchiveHistory(ctx, mustTime(t, 81+HistoryRetention)); err != nil || moved != 3 || hotReceipts() != 0 {
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
	archiveRows := func() (rows []int64) {
		t.Helper()
		result, err := archive.QueryContext(ctx, `SELECT generation FROM batches UNION ALL SELECT generation FROM content_accesses ORDER BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		defer result.Close()
		for result.Next() {
			var generation int64
			if err := result.Scan(&generation); err != nil {
				t.Fatal(err)
			}
			rows = append(rows, generation)
		}
		return rows
	}
	// A copy interrupted before its hot commit is never read, then discarded.
	if _, err := archive.ExecContext(ctx, `INSERT INTO content_accesses SELECT 2, run_id, content_id, content_revision, 'supplied', byte_offset + 100, byte_length, created_at_ms, project_id, agent_id, task_id, task_work_revision FROM content_accesses WHERE generation = 1;
		INSERT INTO batches VALUES (2, 3, zeroblob(32), 1)`); err != nil {
		t.Fatal(err)
	}
	if after, err := history(store); err != nil || after != before {
		t.Fatalf("history with an uncommitted generation = %s, %v", after, err)
	}
	if moved, err := store.ArchiveHistory(ctx, mustTime(t, 83+HistoryRetention)); err != nil || moved != 0 || !reflect.DeepEqual(archiveRows(), []int64{1, 1, 1, 1}) {
		t.Fatalf("discarding an interrupted copy = %d, %v, %v", moved, err, archiveRows())
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
