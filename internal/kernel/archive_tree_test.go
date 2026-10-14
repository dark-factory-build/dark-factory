package kernel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// A cancelled intake task moves to the archive with its tree once settled for
// HistoryRetention: the hot store keeps only archived_tasks, the issue is
// never admitted again, why and the status count read the same across the
// boundary and a restart, live work stays hot, and an altered archived row
// fails crossing reads closed.
func TestArchiveHistoryMovesSettledTaskTreesAndNeverReadmitsTheirIssue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, source := intakeExactRevisionStore(t)
	accepted, err := store.AcceptIntakeSnapshot(ctx, source.ID, intakeSnapshotForTest(), mustTime(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.ImportIntakeAcceptance(ctx, accepted.ID, mustTime(t, 11), source)
	if err != nil {
		t.Fatal(err)
	}
	if task, err = store.UpdateTaskForOperator(ctx, task.ID, task.Revision, TaskPatch{Cancel: true}, mustTime(t, 12)); err != nil || task.Status != TaskCancelled {
		t.Fatalf("cancel = %+v, %v", task, err)
	}
	live := intakeSnapshotForTest()
	live.IssueNumber, live.NodeID = 8, "I_kwDOLive"
	liveAccepted, err := store.AcceptIntakeSnapshot(ctx, source.ID, live, mustTime(t, 13))
	if err != nil {
		t.Fatal(err)
	}
	queued, err := store.ImportIntakeAcceptance(ctx, liveAccepted.ID, mustTime(t, 14), source)
	if err != nil {
		t.Fatal(err)
	}
	view := func() (string, error) {
		why, err := store.Why(ctx, nil, 0, task.ID)
		if err != nil {
			return "", err
		}
		status, err := store.Status(ctx)
		return fmt.Sprintf("%q tasks=%d", why, status.Tasks), err
	}
	before, err := view()
	if err != nil {
		t.Fatal(err)
	}

	if moved, err := store.ArchiveHistory(ctx, mustTime(t, 12+HistoryRetention)); err != nil || moved != 0 {
		t.Fatalf("archive within retention = %d, %v", moved, err)
	}
	moved, err := store.ArchiveHistory(ctx, mustTime(t, 13+HistoryRetention))
	if err != nil || moved == 0 {
		t.Fatalf("archive past retention = %d, %v", moved, err)
	}
	if again, err := store.ArchiveHistory(ctx, mustTime(t, 14+HistoryRetention)); err != nil || again != 0 {
		t.Fatalf("repeated archive = %d, %v", again, err)
	}
	if _, found, err := store.Task(ctx, task.ID); err != nil || found {
		t.Fatalf("archived task still hot: %v, %v", found, err)
	}
	if _, found, err := store.Task(ctx, queued.ID); err != nil || !found {
		t.Fatalf("live task archived: %v, %v", found, err)
	}
	if err := store.Validate(ctx); err != nil {
		t.Fatal(err)
	}
	// The issue is still open and listed: it is never admitted again.
	if pending, err := store.PendingIntakeAcceptances(ctx, source.ID, 10); err != nil || len(pending) != 0 {
		t.Fatalf("pending after archive = %+v, %v", pending, err)
	}
	if _, err := store.ImportIntakeAcceptance(ctx, accepted.ID, mustTime(t, 15+HistoryRetention), source); !errors.Is(err, ErrConflict) {
		t.Fatalf("import of an archived task = %v", err)
	}
	if after, err := view(); err != nil || after != before {
		t.Fatalf("view across the archive = %s, %v\nwant %s", after, err, before)
	}
	path := storePath(t, store)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err = Open(ctx, path); err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if after, err := view(); err != nil || after != before {
		t.Fatalf("view after restart = %s, %v", after, err)
	}

	archive, err := sql.Open(driverName, archiveDataSource(path+"-archive", "rw"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if _, err := archive.ExecContext(ctx, `UPDATE tasks SET title = 'altered'`); err != nil {
		t.Fatal(err)
	}
	if _, err := view(); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("view with an altered archived task = %v", err)
	}
	if _, found, err := store.Task(ctx, queued.ID); err != nil || !found {
		t.Fatalf("hot read with an altered archive = %v, %v", found, err)
	}
}

// liveShapedStore is retainedHistoryStore at the shape of the 13 Oct live
// store (#1809): 100 tasks of two runs through the real lifecycle, their
// Changes reclaimed, cloned in SQL into 40 copies 36 hours apart (4000 tasks,
// 8000 runs; about the live 130 runs a day, 5.2k since 1 Sep), then 64
// terminal diagnostics of 300 KB and 8500 production records. It returns the
// time the newest copy ended.
func liveShapedStore(t *testing.T) (*Store, int64) {
	t.Helper()
	ctx := context.Background()
	store := retainedHistoryStore(t, 100, 2)
	const day, apart = 24 * 60 * 60 * 1000, 36 * 60 * 60 * 1000
	reclaimable, err := store.ReclaimableChanges(ctx, mustTime(t, day))
	if err != nil || len(reclaimable) != 100 {
		t.Fatalf("reclaimable = %d, %v", len(reclaimable), err)
	}
	for _, change := range reclaimable {
		if _, err := store.ReclaimChange(ctx, change.ID, change.Revision, change.GivenUp, mustTime(t, day)); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := store.writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`PRAGMA defer_foreign_keys = ON`)
	// Each copy k keeps the project and agent, rewrites every other id and
	// digest in two bytes, moves its paths and process identities apart, and
	// shifts its times by k periods apart.
	shared := map[string]bool{"project_id": true, "agent_id": true, "assigned_agent_id": true, "repository_id": true, "content_id": true}
	for _, tree := range archivedTree {
		rows, err := tx.QueryContext(ctx, `SELECT name, type FROM pragma_table_info(?)`, tree.table)
		if err != nil {
			t.Fatal(err)
		}
		var columns []string
		for rows.Next() {
			var name, kind string
			if err := rows.Scan(&name, &kind); err != nil {
				t.Fatal(err)
			}
			column := name
			switch {
			case shared[name]:
			case kind == "BLOB":
				column = fmt.Sprintf(`iif(length(%[1]s) IN (16, 32), unhex(substr(hex(%[1]s), 1, 2 * length(%[1]s) - 8) || printf('%%04x', ?1) || substr(hex(%[1]s), 2 * length(%[1]s) - 3)), %[1]s)`, name)
			case strings.HasSuffix(name, "_ms"):
				column = name + " + ?2"
			case name == "path_inode" || name == "pid" || name == "pgid":
				column = name + " + ?1 * 100000"
			case name == "path":
				column = "path || '-' || ?1"
			}
			columns = append(columns, column)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatal(err)
		}
		exec(`CREATE TEMP TABLE template_` + tree.table + ` AS SELECT * FROM main.` + tree.table)
		for k := 1; k < 40; k++ {
			copied := strings.NewReplacer("?1", fmt.Sprint(k), "?2", fmt.Sprint(k*apart)).Replace(strings.Join(columns, ", "))
			exec(`INSERT INTO main.` + tree.table + ` SELECT ` + copied + ` FROM template_` + tree.table)
		}
		exec(`DROP TABLE template_` + tree.table)
	}
	exec(`INSERT INTO terminal_diagnostics SELECT id, 0, 300000, randomblob(300000), terminal_at_ms FROM runs ORDER BY id LIMIT 64`)
	exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 8500)
		INSERT INTO production_records SELECT p.id, 'owner/repository', iif(i % 3 = 0, 'pull_request', 'check'), 'record-' || i, '',
			json_object('state', iif(i % 3 = 0, 'merged', 'completed'), 'name', 'build', 'conclusion', 'success', 'padding', hex(randomblob(1000))), i * 100000 FROM n, projects AS p`)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var newest int64
	if err := store.writer.QueryRowContext(ctx, `SELECT max(updated_at_ms) FROM tasks`).Scan(&newest); err != nil {
		t.Fatal(err)
	}
	return store, newest
}

// Phase 1 of docs/development/STORE_SPLIT.md on a store of the live shape:
// archiving the settled trees leaves a week of work hot, so whole-store
// validation walks only that, and the report reads the same across the
// boundary. It logs the before and after numbers the issue asks for; time is
// logged, not asserted, as parallel tests skew it.
func TestArchiveHistoryBoundsALiveShapedStore(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 4000-task store")
	}
	t.Parallel()
	ctx := context.Background()
	store, newest := liveShapedStore(t)
	defer store.Close()
	path := storePath(t, store)
	measure := func(label string) int {
		t.Helper()
		if err := store.CheckpointWAL(ctx, "TRUNCATE"); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		if err := store.Validate(ctx); err != nil {
			t.Fatal(err)
		}
		took := time.Since(started)
		statements, walk := validationStatements(t, store)
		var tasks, runs int
		if err := store.writer.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM tasks), (SELECT count(*) FROM runs)`).Scan(&tasks, &runs); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: store %.1f MB, Validate %s (row walk %s, %d statements), tasks=%d runs=%d", label, float64(info.Size())/1e6, took.Round(time.Millisecond), walk.Round(time.Millisecond), statements, tasks, runs)
		return statements
	}
	report := func() []string {
		t.Helper()
		lines, err := store.Report(ctx, nil, 0, 24*60*60*1000)
		if err != nil {
			t.Fatal(err)
		}
		return lines[:len(lines)-1] // The last is the live WAL size.
	}
	before := measure("before")
	reported := report()

	at := mustTime(t, newest+1)
	started, passes := time.Now(), 0
	for moved := 1; moved > 0; passes++ {
		var err error
		if moved, err = store.ArchiveHistory(ctx, at); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("archived in %d passes, %s", passes, time.Since(started).Round(time.Millisecond))
	factory, err := store.Factory(ctx)
	if err == nil {
		_, err = store.SetDispatch(ctx, factory.Revision, false, at)
	}
	if err == nil {
		_, err = store.CompactStorage(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
	after := measure("after")
	var hot, archived int
	if err := store.writer.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM tasks), (SELECT count(*) FROM archived_tasks)`).Scan(&hot, &archived); err != nil {
		t.Fatal(err)
	}
	// Copies 35 to 39 ended within the week before at.
	if hot != 500 || archived != 3500 || after*6 > before {
		t.Fatalf("hot tasks = %d, archived = %d, validation statements %d then %d", hot, archived, before, after)
	}
	if again := report(); !reflect.DeepEqual(again, reported) {
		t.Fatalf("report across the archive =\n%s\nwant\n%s", strings.Join(again, "\n"), strings.Join(reported, "\n"))
	}
}

// A settled tree whose attachment still holds data stays hot, so retention
// removes the data before the tree moves: the append-only archive could never
// drop it.
func TestArchiveHistoryKeepsATreeHotUntilItsAttachmentIsRemoved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, project, _, _ := newSharedQueueStore(t, 4)
	defer store.Close()
	task, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 90), ProjectID: project.ID, IncarnationID: incarnationID(t, 91), Title: "t"}, mustTime(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.ExecContext(ctx, `INSERT INTO task_attachments(task_id, position, name, data) VALUES (?, 0, 'notes.txt', ?)`, task.ID.Bytes(), []byte("private")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateTaskForOperator(ctx, task.ID, task.Revision, TaskPatch{Cancel: true}, mustTime(t, 11)); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := store.AttachmentRetention(ctx, &enabled); err != nil {
		t.Fatal(err)
	}
	const month = 30 * 24 * 60 * 60 * 1000
	if moved, err := store.ArchiveHistory(ctx, mustTime(t, month+11)); err != nil || moved != 0 {
		t.Fatalf("archive with attachment data = %d, %v", moved, err)
	}
	if err := store.ExpireTaskAttachments(ctx, mustTime(t, month+11)); err != nil {
		t.Fatal(err)
	}
	if moved, err := store.ArchiveHistory(ctx, mustTime(t, month+12)); err != nil || moved == 0 {
		t.Fatalf("archive after the attachment expired = %d, %v", moved, err)
	}
	if _, found, err := store.Task(ctx, task.ID); err != nil || found {
		t.Fatalf("expired tree still hot: %v, %v", found, err)
	}
}

// Archiving keeps the three newest finished carriers specialistSchedule reads
// for its quiet streak, even while a newer carrier is queued: four quiet
// reviews then a queued fifth archive only the oldest, and the backoff holds.
func TestArchiveHistoryKeepsTheCarriersASpecialistBacksOffFrom(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, _, agent := newAdmissionStore(t, RoleWorker, 2)
	defer store.Close()
	agent = makeSpecialist(t, store, agent, "failures", 10)
	if bodies := wakeBodies(t, store, 20); len(bodies) != 1 {
		t.Fatalf("initial wake = %q", bodies)
	}
	at := int64(100)
	for round := range 4 {
		runCarrier(t, store, byte(30+25*round), at)
		state := specialistStateOf(t, store, agent.ID)
		if next := wakeBodies(t, store, state.NextReviewAt); len(next) != 1 {
			t.Fatalf("round %d wake = %q", round, next)
		}
		at = state.NextReviewAt + 1
	}
	before := specialistStateOf(t, store, agent.ID)
	if before.QuietReviews != 3 || before.Waiting != "queued" {
		t.Fatalf("state before archive = %+v", before)
	}
	later := at + HistoryRetention + 1
	reclaimable, err := store.ReclaimableChanges(ctx, mustTime(t, later))
	if err != nil || len(reclaimable) != 4 {
		t.Fatalf("reclaimable = %d, %v", len(reclaimable), err)
	}
	for _, change := range reclaimable {
		if _, err := store.ReclaimChange(ctx, change.ID, change.Revision, change.GivenUp, mustTime(t, later)); err != nil {
			t.Fatal(err)
		}
	}
	for moved := 1; moved > 0; {
		if moved, err = store.ArchiveHistory(ctx, mustTime(t, later)); err != nil {
			t.Fatal(err)
		}
	}
	var archived int
	if err := store.writer.QueryRowContext(ctx, `SELECT count(*) FROM archived_tasks`).Scan(&archived); err != nil || archived != 1 {
		t.Fatalf("archived carriers = %d, %v", archived, err)
	}
	if after := specialistStateOf(t, store, agent.ID); after.QuietReviews != before.QuietReviews || after.LastReviewTaskID != before.LastReviewTaskID {
		t.Fatalf("state after archive = %+v, want %+v", after, before)
	}
}

// A settled task that pinned a proposal stays hot: the pin is the proposal's
// acceptance, so archiving it would reopen the proposal for specialists.
func TestArchiveHistoryKeepsAProposalsAcceptance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, run, _ := runningWorkerRun(t)
	defer store.Close()
	task, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 100), ProjectID: run.ProjectID, IncarnationID: incarnationID(t, 101), Title: "implement"}, mustTime(t, 40))
	if err != nil {
		t.Fatal(err)
	}
	spec := knowledgeSpec(t, run.ProjectID, 90, ContentObservation, KnowledgeMetadata{Status: "tentative"})
	spec.Author = "run:00 agent:" + run.AgentID.String() + " role:worker"
	spec.SourceReferences = `{"status":"tentative","record_type":"proposal"}`
	proposal, err := store.CreateContent(ctx, spec, mustTime(t, 41))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AttachContentToTask(ctx, task.ID, run.ProjectID, proposal.ID, proposal.Revision, mustTime(t, 42)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateTaskForOperator(ctx, task.ID, task.Revision, TaskPatch{Cancel: true}, mustTime(t, 43)); err != nil {
		t.Fatal(err)
	}
	follow := func() (int, string) {
		t.Helper()
		conn, err := store.writer.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		agent, _, err := agentByID(ctx, conn, run.AgentID)
		if err != nil {
			t.Fatal(err)
		}
		var open int
		if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM project_content_revisions AS c WHERE c.id = ? AND `+openProposalSQL, proposal.ID.Bytes()).Scan(&open); err != nil {
			t.Fatal(err)
		}
		body, err := specialistWake(ctx, conn, agent, SpecialistState{}, specialistPrior{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return open, body
	}
	accepted := "- proposal " + proposal.ID.String() + ` "` + spec.Title + `": accepted task ` + task.ID.String() + " cancelled"
	if open, body := follow(); open != 0 || !strings.Contains(body, accepted) {
		t.Fatalf("before archive: open=%d body=%q", open, body)
	}
	if _, err := store.ArchiveHistory(ctx, mustTime(t, 44+HistoryRetention)); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Task(ctx, task.ID); err != nil || !found {
		t.Fatalf("accepting task archived: %v, %v", found, err)
	}
	if open, body := follow(); open != 0 || !strings.Contains(body, accepted) {
		t.Fatalf("after archive: open=%d body=%q", open, body)
	}
}
