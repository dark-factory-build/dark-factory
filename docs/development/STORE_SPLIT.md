# Store split: live state and history

Design for [#1809](https://github.com/dark-factory-build/dark-factory/issues/1809).
It extends the history archive that #1819 added for content-access receipts
(`internal/kernel/archive.go`). It adds no second mechanism. The build follows
in the phases at the end. Each phase is a separate pull request with its own
measurement.

## What is known

From the issue (live store, 11 Oct 2026): `factory.sqlite3` was 99 MB and its
WAL reached 79 MB. It held about 5.2k runs, 5.6k production records, 3.2k
tasks, 29k content-access rows and 797 Changes. Whole-store validation took
about 4 s a minute. The WAL grew 20–35 MB an hour. Knowledge supply took 2–15 s
per fetch, and factoryd idled at about 200% CPU.

Measured for this design on a synthetic store
(`retainedHistoryStore` in `validate_history_test.go`, one settled run per
task): `validateDurableControls` costs 0.47–0.52 ms and about 11 SQL
statements for each retained run. 100 runs took 47 ms and 400 runs took 207 ms.
At 5.2k runs the row walk alone is about 2.6 s. `Validate`
(`validateDatabaseSnapshot`) then adds `PRAGMA integrity_check`, which reads
every page of the file. That validation runs at open, every minute from the
scheduler (`storeValidationInterval`), inside `setFactory`, in
`CompactStorage` and in migration. So its cost follows the number of rows and
the bytes that history holds, not the work in progress.

Hypotheses, not yet verified (phase 0 measures them):

- **Idle CPU:** each scheduler tick runs queries over all of history.
  `overseerItems` (`overseer_wakeup.go`) treats every succeeded, blocked or
  failed worker task, and every carrier task, as a candidate on every tick.
  #1882 measured its stalled-item pass at about 31% of a core. The failure
  ledger also scans every failed run.
- **WAL growth:** write volume comes from production-record documents of up to
  64 KiB, upserted on each poll. Checkpoints are held back by long readers,
  and the multi-second validation snapshot is one of them.

## Principle

`factory.sqlite3` (the **hot store**) holds the rows that a coordination
decision can still read or change. `factory.sqlite3-archive` (the **archive**)
holds settled history. The archive file already exists. The archive is
append-only. Each batch is one *generation*, committed in the archive first,
proven equal to the hot rows by a digest, and then recorded in the hot
`archive_batches` row. Hot rows are deleted in that same hot transaction. A
generation the hot store does not record is ignored and never reused, and its
rows, still hot, are copied again. A recorded generation that the archive
lacks makes every read that crosses into the archive fail closed. This is the
#1819 protocol unchanged. This design widens what one generation carries.

## What stays hot

- Configuration and identity: `factory`, `projects`, `project_repositories`,
  `repository_source_identities`, `accounts`, `agents`, `intake_sources`,
  `intake_source_trusted_logins`, `intake_source_priorities`, `browser_*`,
  `project_tokens`, `attachment_retention`, `overseer_wake_cursors`.
- `invalidations`: it is already bounded at `EventRetentionLimit` (4096).
- The Library: `project_content_revisions`, `content_repository_bindings`,
  `project_outcome_revisions`. Knowledge supply reads these on every fetch.
  Phase 0 measures their size. They move only if that measurement shows they
  matter.
- `intake_acceptances`: this is the dedupe key for every issue ever admitted
  (see *References*).
- Every task tree that is not settled, and every production record that is not
  settled (defined below).
- `archive_batches` and the new `archived_tasks` (see *References*).

## What moves

### Settled task trees

A task T and every row keyed to it move together in one generation once all
of the following hold, where `cutoff = now − HistoryRetention` (7 days, the
existing constant):

1. T is `succeeded`, `failed` or `cancelled`, and
   `T.completed_at_ms < cutoff`.
2. Every run of T is terminal, with `terminal_at_ms < cutoff`, and has no
   `content_accesses` left. The existing receipt pass moves those receipts
   first, using the same cutoff.
3. T has no Change, or its Change is `abandoned`. A `retained` or `available`
   Change keeps the tree hot, because reclaim and send-back still act on it.
4. T has no open pull request: no `publication_tasks` row of T has a
   `pull_request` record whose state is `open`.
5. No row outside the tree refers to T. That means no `task_prerequisites` row
   of an unsettled task names T as upstream, and no `mission_task_bindings`
   row of an unsettled task names T as parent. It also means no
   `peer_questions` or `task_interventions` row joins T to an unsettled task.
   Trees connected only to settled trees move in the same generation.
6. T is not one of its agent's three newest carrier tasks, which
   `specialistSchedule` reads for the last review and its quiet streak.

The rows that move with T, in child-first delete order: `terminal_diagnostics`,
`run_tokens`, `human_requests`, `terminal_sessions`, `resources`,
`task_interventions`, `peer_questions`, `publication_tasks`,
`task_prerequisites` (where T is the downstream task), `task_conflict_paths`,
`task_repository_bindings`, `task_content_references`,
`mission_task_bindings`, `task_attachments`, `task_automatic_events`,
`intake_task_bindings`, `changes`, `runs`, `tasks`. `changes.settled_run_id`
and `runs.change_id` refer to each other, so the hot delete runs with
`PRAGMA defer_foreign_keys = ON`. Foreign keys stay enforced at commit.

Every archive table mirrors the column list of its hot table and adds a
trailing `generation` column (`CREATE TABLE … AS SELECT *, 0 AS generation`),
so readers union `SELECT *, NULL` from the hot table with `SELECT *` from the
archive (`history.table`). A user_version bump that changes a moving hot table
changes its archive mirror in the same migration. The archive is attached
read-write there, as in the archive pass. The archive keeps no constraints of
its own, only an index on `generation`: its rows are proven equal to hot rows
that met every hot constraint. The digest (`digestRows`) runs per table over
rows ordered by primary key, and a generation's digest covers every archive
table in a fixed order, so a receipt generation still hashes as it did.

### Settled production records

These move under the same cutoff, on `observed_at_ms`:

- a `pull_request` record whose state is `merged`, `closed` or `superseded`
  and that no hot `publication_tasks` row names;
- the `check` and `reviewer` records of such a pull request, and checks on
  `main` older than the cutoff;
- `delivery` records, except the newest `release:` delivery of each project,
  which `overseerItems` and `production.go` read.

`repository` records stay hot. A poll can observe an archived identity again,
which writes a new hot row. Readers that union the archive keep the hot row
for a given key, and the next pass archives the new copy under a new
generation.

## References across the boundary

The rule is that **no hot row refers to an archived row**. Foreign keys in the
hot store stay as they are and stay enforced. Archived rows may refer to hot
or archived rows. Readers join across the boundary only through unions over
committed generations, as `contentAccessesSQL` does today.

Condition 5 keeps existing references from crossing. New references to an
archived task are refused, because each writer that creates one (prerequisite
add, peer ask, retry, send-back, intervention, content attach, mission parent)
looks the task up in `main.tasks` and returns `ErrNotFound`. Archived history
is read-only. This is a deliberate product change: a week-old settled task
cannot be retried or sent back. New work is queued instead.

One small hot index keeps the boundary visible:
`archived_tasks(task_id PRIMARY KEY, generation)`. The archive pass writes it
in the same hot transaction that deletes the tree, at 24 bytes per task. It
serves two readers:

- **Intake.** `PendingIntakeAcceptances` (`intake_store.go`) treats an
  acceptance whose `task_id` has no row in `tasks` as not yet materialized,
  and creates its task. Without the index, archiving an intake task would
  admit the issue again. The query also excludes acceptances whose `task_id`
  is in `archived_tasks`. Each intake poll also replays the import of every
  listed issue already accepted, so the import (`intakeImportReplay`) refuses
  an archived task with `ErrConflict`, which the poll treats as nothing to do.
  `intake_acceptances` stays hot because intake looks up an issue's acceptance
  by issue identity before it accepts the issue.
- **Lookups.** `why --task` reads an archived task, its runs and publications
  through the union. Task read and the console report it as not found.

Validation adds one check: no `archived_tasks` id is also in `tasks`.

## What each reader reads

| Reader | Reads | Effect of the split |
|---|---|---|
| Admission, recovery, run liveness, Change reclaim, merge pipeline | hot | none: these read only unsettled rows |
| Intake source pass | hot, plus `archived_tasks` | an archived intake task is never admitted again |
| Overseer wake and stalled items | hot | a task settled for longer than the retention window stops being a wake item; the overseer had a week of re-wakes |
| Validation (open, periodic, `setFactory`, compaction, migration) | hot | the row walk and `integrity_check` follow the size of live state; the archive is checked by digest when a read crosses into it and on each pass |
| `factoryctl report` | hot ∪ archive (`beginHistoryRead`, already used) | a `--since` window that reaches past the cutoff (bucket alignment can add up to a day) still counts every run |
| `why --task` | hot, else archive via `archived_tasks` | unchanged output for archived tasks |
| `why --pull`, failure ledger in `report` | hot ∪ archive | first occurrences and merged pulls stay complete |
| Specialist events | hot | they are read inside the scheduler's write, which cannot attach the archive, so a failure signature seen only in archived runs counts as new when it recurs |
| Console task list, status counts, recent tasks | hot | lists live and recent work; the summary adds the `archived_tasks` count; opening an archived task reads the archive |
| Knowledge supply and the Library access view | hot ∪ archive receipts (already) | unchanged |
| Backup, rollback | both files | as in `DEPLOY.md`: the archive only grows, and a restored store ignores generations it does not record |

## The archive pass

`ArchiveHistory` keeps its schedule (hourly, from the scheduler) and its two
commits. One generation carries one batch: the receipts it moves today, up to
256 settled task trees, and their settled production records. The limit keeps
each hold on the writer short. The pass is idempotent. It repeats until a
batch is empty, so the first passes after deploy move the backlog one batch at
a time.

Deleted pages go to the freelist and are reused, so the hot file stops growing
but does not shrink. After the backlog has moved, the operator runs
`factoryctl storage compact` once (`CompactStorage`, `VACUUM`) to bring the
file under the target. Phase 1 measures whether `PRAGMA auto_vacuum =
INCREMENTAL` should replace that manual step.

## Migration and rollback

The `user_version` goes from 43 to 44 (v43 took the intake author columns) and adds `archived_tasks` to the hot
schema. The migration moves no data; the archive pass moves it. The archive
tables are created by the pass (`CREATE TABLE IF NOT EXISTS`), as in #1819.
The release trial migrates a copy of the live store (`DEPLOY.md`). Before that
release, the same copy is archived to completion and measured: size,
`Validate` time, and a `why` and `report` comparison before and after. Rollback
follows `DEPLOY.md`: the previous build refuses version 44, the release backup
is restored, and the archive is left in place. The restored store does not
record the newer generations, so readers ignore them. Their rows are still
hot in the backup and are copied again under new generations.

## What the split makes unnecessary

Each item is re-measured after phase 2 and deleted only if the measurement
holds:

- The content-access-only selection in `archive.go` becomes one table of the
  general batch. Existing generations stay readable.
- The ponytail ceiling on the whole-history scan in `failures.go`.
  `verifyArchive` already skips the rehash while the archive file and the
  newest committed generation are as last proved (`archiveStamp`, phase 1):
  without it, each pass of the backlog rehashed every earlier generation.
- The one-minute validation in the scheduler, if a hot-only `Validate` stays
  under 0.5 s: it can return to running at validated writes.
- The scheduler's forced WAL `TRUNCATE` checkpoint, if SQLite's automatic
  checkpoints keep the WAL bounded once no reader holds a snapshot for
  seconds.

## Phases

0. **Measure** on a copy of the live store, without changing code:
   `SELECT name, sum(pgsize) FROM dbstat GROUP BY name ORDER BY 2 DESC` for
   bytes per table; `Validate` timed with and without `integrity_check`; WAL
   bytes per scheduler tick; a CPU profile of idle factoryd. These settle the
   hypotheses above and whether the Library or diagnostics need to move.
1. **Settled task trees**, `archived_tasks`, version 44, and the readers in
   the table: intake pending, `why`, report, failure ledger, console counts.
   Acceptance on the copy: hot `Validate` under 0.5 s, and intake admits no
   archived issue again (a test that archives an intake task and runs
   `PendingIntakeAcceptances`).
   Built and measured in `TestArchiveHistoryBoundsALiveShapedStore`, on a
   synthetic store of the live shape (4000 tasks and 8000 runs over 60 days,
   8500 production records, 64 terminal diagnostics of 300 KB): 75.0 MB and
   `Validate` 3.2–4.7 s (row walk 2.6–2.8 s, 68 027 statements) before;
   41.7 MB and `Validate` 0.30–0.50 s (row walk 0.21–0.35 s, 8 527
   statements) after archiving and one compaction, with 500 tasks hot. The
   row walk costs about 0.35 ms per hot run, so the 0.5 s target holds up to
   about 1000 runs a week. Production records, which phase 2 moves, are most
   of the remaining file and of `integrity_check`. The backlog took 15 passes
   and 18–23 s.
2. **Settled production records** and their union readers.
3. **Fold and delete**: the content-access path joins the general pass, and
   the items under *What the split makes unnecessary* are deleted where their
   measurements hold.

## Live acceptance signal

After a week of normal work following phase 2:

- `factory.sqlite3` stays under 20 MB.
- `Validate` takes under 0.5 s. Nothing logs this time today, so phase 1 adds
  a log line for each periodic `Validate`.
- `wal_bytes` in `factoryctl report` stays bounded without manual
  compaction.
- factoryd at idle uses less than 30% CPU.
