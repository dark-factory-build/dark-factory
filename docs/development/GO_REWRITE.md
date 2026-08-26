# Go local-runtime hard cutover

This is the canonical design, proof plan, and permanent record for replacing
Dark Factory's local Rust runtime with Go. It is organized around product
authority and external effects, not around the existing Rust crates. Git
history remains the archive; the Go runtime will not migrate a Rust home,
schema, event log, protocol, or serialized state.

The document starts as the pre-implementation contract. It must be updated as
the kernel proof, broader implementation, mutation work, reviews, and final
cutover produce evidence. Production Go implementation does not begin until
this initial contract is committed.

## 1. Exact starting point

### Repository

- Local rewrite base: `c21c8d4c0933435c6811ad6f252a9e3bc346e57d`
  (`Test storage completeness with a live lease (#389)`).
- Branch: `go-hard-cutover`.
- Worktree: `/Users/baziyer/dark-factory/.worktrees/go-hard-cutover`.
- Base chosen by `./scripts/new-worktree.sh go-hard-cutover` from the locally
  available `origin/main`, as required by `AGENTS.md`.
- Source status before planning: clean (`git status --short` returned no
  output).
- The root checkout was older (`9a4d7a8`); it was read before the helper
  selected the newer local `origin/main`. All audits and this plan use
  `c21c8d4`.
- The public remote moved after the worktree was created: PR #391 is reported
  merged at `3631e4712068c01e25d8327318e2e1b2d8b7c16d`. That post-base change is
  evidence, not part of the starting tree. A later integration refresh must
  be explicit and reviewed; it does not rewrite this starting point.

### Toolchains

Recorded on 2026-08-25:

```text
$ git rev-parse HEAD
c21c8d4c0933435c6811ad6f252a9e3bc346e57d

$ git status --short
<no output>

$ go version
go version go1.27.0 darwin/arm64

$ rustc --version
rustc 1.90.0-nightly (667787527 2025-07-02)

$ cargo --version
cargo 1.90.0-nightly (930b4f62c 2025-06-28)
```

Go was not initially installed on `PATH`. The official Go 1.27.0 Darwin ARM64
archive was downloaded to `/private/tmp`, its SHA-256 was verified as
`90493b3bbd5e10f91d12153198bf1994fd756399b4fec93b49b0c6e2acdeeb3e`,
and it was extracted at `/private/tmp/dark-factory-go1.27.0`. This does not
modify a global Go installation. Repository commands use that exact binary
until the repository pins and documents its supported Go toolchain.

### Rust baseline

`./scripts/local-ci.sh` passed from the clean rewrite base before any tracked
change. It passed the lease/mutation, release-source, publisher, packaging,
launchd static, environment, worktree, build-headroom, workflow-summary,
review-gate and inline-chokepoint scripts; `cargo fmt --check`; workspace
Clippy with `-D warnings`; the full serialized workspace test suite; doc tests;
and `git diff --check`. The two opt-in real launchd release tests were ignored
by their test binary as documented. No real provider, live daemon, installed
job, live socket, or operator home was used.

Baseline size, measured mechanically over `crates/` and retained only for
later comparison:

- five local-runtime crates;
- 85 Rust source files;
- 55,330 lines under crate `src/` trees;
- 13,665 lines under crate `tests/` trees;
- 68,995 Rust lines in total under the five local-runtime crates.

These are physical line counts, not semantic LOC. Final measurements must use
one repeatable method for both sides and distinguish production from tests.

### Evidence sources and authority limits

The required authority files were read in full at the exact base. Current
source, tests, migrations, process lifecycle, Store/admission, Change,
verification, clients, install/release scripts, open issues, and recent PRs
were inspected. Anonymous public GitHub API reads supplied issue/PR evidence.
The Maintainer App status was verified as repository
`dark-factory-build/dark-factory`, repository ID `1335380107`, permission
revision `maintainer-operations-v1`. Its current typed surface cannot update
issues, so issue closure at the end requires either a later typed operation or
an explicit human handoff; operator credentials are never a fallback.

## 2. Product contract

The categories below are behavioral decisions. Existing Rust code is evidence,
not a compatibility target.

### KEEP

- SQLite is the sole durable authority. A mutex may serialize access but never
  authorizes a transition. State, revisions, and their durable invalidations
  commit in one transaction.
- Admission is one immediate write transaction. It reads durable dispatch and
  capacity, selects the canonical current queue head, derives current agent
  role/profile/provider/execution mode, binds exact task incarnation/revision,
  reserves the required Change, creates one run and one credential, and
  declares initial resources before any external effect.
- One random credential belongs to one exact admitted/running attempt, but it
  authenticates attempt requests only while that exact run is `running`.
  Authentication derives project, agent, task, run, role, provider, execution
  mode, Change, and authority. The first transition to `finalizing` deletes or
  irreversibly revokes it in the same transaction. Operator authentication
  cannot impersonate an attempt.
- The normal lifecycle remains `admitted -> running -> finalizing -> terminal`.
  Operator cancellation and any spawn, activation, source-selection,
  materialization, or other unrecoverable pre-exec failure take the guarded
  exceptional edge `admitted -> finalizing`; no state goes directly terminal.
  Finalizing represents real external uncertainty. The first durable outcome
  request wins; only the finalizer writes terminal; cleanup uncertainty stays
  visible and nonterminal.
- Resources follow one monotonic graph. `declared` may become `active` when
  exact external identity is bound. Declared or active resources enter
  `releasing`; declared/active/releasing may become `unresolved` when absence
  cannot be proved, or `released` on positive cleanup/absence proof.
  `unresolved` may only become `released` after later positive proof, never
  active/releasing or signal-authoritative. `released` and terminal are
  absorbing. Any non-released resource forbids terminalization.
- One admitted run owns one fresh, non-interactive provider process and one
  startup input. There is no resident provider session, PTY attach, prompt
  replay, process resume, or stdout-derived lifecycle authority.
- Register-before-exec remains a two-owner proof: an outer daemon-to-runner
  gate and an inner runner-to-provider gate. Exact runner/provider identities
  are durable before the corresponding activation. The runner alone may
  signal its still-unreaped direct-child process group.
- A stored PID or PGID is observation evidence, never signal authority.
  `EPERM`, malformed identity, reuse, missing birth proof, or any inconclusive
  liveness result is present/`unresolved`, never absent/released.
- Runner terminal observation remains durable and replayable across daemon
  loss. Store commit precedes runner acknowledgement. Output remains opaque.
- Provider launch uses `env_clear` plus a closed allowlist of ordinary identity,
  locale, shell, path, and temporary-directory values and explicit validated
  provider-home configuration. It disables Git discovery above the Change,
  prompting, SSH, credential helpers, GitHub configuration, dynamic-loader
  injection, and ambient API/provider/proxy credentials. The attempt bearer is
  written only to an exact owner-only `0600` per-run file; the environment
  carries only that file's path. The bearer never appears in argv, environment,
  startup input, request bodies, logs, errors, events, or debug output, and a
  provider-launched `factoryctl` never falls back to operator credentials.
- Workers receive a daemon-owned, writable, retained, `.git`-free Change for
  one exact task incarnation and base commit. The daemon owns materialization,
  leasing, identity-checked removal, and retry reuse.
- Source selection rejects partial/lazy object fetch, unsafe paths, links,
  gitlinks, unexpected modes, `.git`, upward Git discovery, transformed blob
  bytes, path replacement, and unbounded trees.
- Change creation keeps a registered, blocked source wrapper in the provider
  process lineage. It reports and durably binds the exact commit before blob
  reads, materializes into private staging, reports bounded canonical
  digest/count/bytes plus published identity, and blocks again before provider
  exec. The daemon marks the Change available only after rechecking those
  facts, then releases that same registered process into the provider. A
  Git/materializer subprocess is never an unregistered prelude to an otherwise
  safe provider launch.
- The private Change-worker mode requires exact owner-only inherited control
  descriptors and the parent-bound activation handshake; ordinary direct
  invocation fails before reading Git, creating a path, or launching a child.
- Verification is fixed and typed: `None`, `RustWorkspaceTest`, or
  `GoWorkspaceTest`. It applies only to a worker's proposed success;
  orchestrator outcomes and every non-success proposal launch no verifier.
  `None` launches no verifier and creates no source snapshot. A configured
  verifier begins only after attempt authority is revoked and the provider is
  fully reaped, and operates on one stable source snapshot. Failure may refine
  only proposed success to terminal `failed(unverifiable)`; the immutable first
  proposal remains recorded.
- Verifier declaration, blocked activation, live child/group ownership, reap,
  result consumption, resources, cache handoff, and cleanup are daemon-owned.
  A bounded result is create-only/atomically published and is consumed only
  after exact verifier-group absence. Once activation was attempted, recovery
  never reruns verification against a current/later Change: exact absence plus
  no valid result is failed verification; uncertain absence or cleanup remains
  finalizing. Cache measurement/handoff occurs only after exact writer absence.
- One private owner-only Unix-socket API serves both CLI and TUI. The CLI is
  the canonical non-interactive recovery/automation surface. The daemon owns
  all policy.
- Public/watch state is bounded and excludes credentials, prompts, raw output,
  message bodies, task bodies/results, source content/paths, environment,
  private guidance, deliberation, and runner resource identities.
- BUILDING and AGENT remain the TUI's useful core, including queue, current
  work, bounded activity/status, a real NEEDS YOU decision inbox, and basic
  project/agent/task actions.
- Independent adversarial review binds to the exact head and remains required
  before merge.

### FIX

- Check every guarded write's affected-row count, including the currently
  unchecked queued-task transition in admission (#317). A suppressed or stale
  update must roll back the entire admission footprint.
- Record terminal runner exit evidence once. Exact replay is idempotent;
  conflicting sequence/status evidence fails closed instead of overwriting the
  first observation.
- Represent credential digests as exact 32-byte blobs. Do not repeat the Rust
  schema's weak `GLOB '[0-9a-f]*'` suffix constraint.
- Persist `unresolved` for malformed, weak, replaced, or reused process/path
  identities instead of merely returning “not absent.”
- Prove real daemon and runner crash cuts in the launch window. Base
  `c21c8d4` does not contain #313's required real factoryd SIGKILL proof, and
  open PR #390 metadata is not proof for this base.
- Make kill-and-wait structurally mandatory on every owned-child error path.
  Go has no Rust `Drop`; a goroutine or context ending is not a reap.
- Make resource release and terminalization one-way and causally tested
  (#335); make the observer finalize from freshly reloaded durable state
  without relying on a periodic sweep (#336).
- Replace snapshot-heavy durable/public events with bounded invalidations or
  safe summary deltas. The current shape can expose `ProjectSnapshot.root` and
  couples event privacy to read DTO growth (#340/#344).
- Replace the TUI's competing bootstrap, event, response, and independent
  status projections with one revisioned snapshot/watch model. Gaps force
  resync; delayed responses cannot regress newer state (#341/#342/#344).
- Return task summaries from list/watch and fetch selected private detail by
  revision (#39). Add empty-state project and agent creation through the same
  API used by the CLI (#30).
- Replace display-string control flow and whole-profile read/modify/write with
  typed actions and revision-checked granular updates.
- Define NEEDS YOU as a typed projection derived from canonical durable facts,
  not a second decision table. Initial actionable producers are
  paused-with-queued-work (`ResumeAgent`), exhausted-budget-with-queued-work
  (`ResetBudget`), and a blocked task (`RetryTask`). Failed runs,
  finalization stalls, and capacity waits remain status/inspection, not fake
  decisions. Each choice binds source entity/revision; only operator authority
  may execute it, and the mutation, invalidation, and resulting durable intent
  commit atomically. Exact replay is idempotent; stale/conflicting/already
  resolved or attempt-authenticated choices fail closed (#199, #358, #362).
- Prove budget exhaustion directly gates admission (#332) and make durable
  capacity policy Store-owned rather than ordinary-caller supplied.
- Give `GoWorkspaceTest` an explicit, bounded environment:
  `GOTOOLCHAIN=local`, private `GOCACHE`, `GOMODCACHE`, and `GOTMPDIR`,
  `GOENV=off`, `GOWORK=off`, controlled locale/color, deliberate
  `GOPROXY`/`GOSUMDB` and network policy, timeout, output bound, stable
  snapshot cwd, and registered process group.
- On recovery after a staging rename but before the ready checkpoint, recompute
  the complete selected-commit manifest. Inode and size equality cannot adopt
  a same-inode, same-size corrupted Change.
- Verify every executable in a verifier bundle again immediately before its
  launch. A sibling cannot be trusted because an earlier sibling passed an
  initial bundle scan (#353).
- Preserve the admission order as a frozen product rule: within one assigned
  project/agent queue, `priority DESC, created_at_ms ASC, id ASC` under binary
  ID collation. Admitted, running, and cleanup-stalled finalizing runs all
  consume the Store-owned factory-wide capacity; independent unique constraints
  permit at most one nonterminal run per agent, task incarnation, and Change.
  Disabled dispatch, paused/non-working agents, and exhausted durable budget
  return an ordinary typed “no admission” result. These facts, queue selection,
  the guarded task update, Change, credential, resources, run, and
  invalidations are read/written in the same immediate transaction.

### SIMPLIFY

- Use one fresh schema and one schema creation path; no ORM, repository layer,
  migration framework, or generic transaction API.
- Keep a small concrete Store with one frozen immediate-write pattern, one
  writer owner, and explicit read transactions for coherent projections.
- Keep the two blocked-exec checkpoints but express launch as one visible
  choreography rather than forwarding wrappers and scattered callbacks.
- Share one private activation-file/lease primitive across daemon and runner.
- Model provider leader/group as one owned aggregate where this deletes
  duplication, while retaining distinct leader-reaped and group-absent facts.
- Keep three concrete provider launch adapters. The consuming daemon boundary
  may use a small function/interface only if multiple production adapters make
  it smaller; providers never choose authority or ownership.
- Use one bounded dashboard snapshot at sequence N and one watch from N.
  Durable events carry only sequence, safe entity kind/ID, and revision. A
  gap triggers canonical resync; details remain explicit reads.
- Feed mutation responses and watch invalidations through one revision-aware
  client reducer.
- Keep one obvious CLI command per operation and a small, testable Bubble Tea
  model. Use typed Open/Back/Next/Previous actions rather than navigation
  synonyms.
- Retain an internal project verification cache only if focused measurement
  shows enough value. If retained, use one lifecycle with active lease/known
  measurement rather than three states and do not expose read-only storage
  status without an operator action.
- Restrict the initially supported local runtime to macOS/Darwin. Platform
  seams exist only where process/syscall/launchd behavior genuinely differs.
- Keep install/update as one concrete transaction over exactly four binaries,
  one immutable version directory, one relative atomic `bin/current` link,
  one release receipt, and one exact owned launchd job. Parse archives in Go
  and reject traversal, links, duplicates, extras, mixed builds, wrong
  architecture, and unbounded members before publication.

### DELETE

- Migrations `0001`–`0035`, legacy event decoding/upcasting, historical event
  markers, old protocol compatibility, old home-layout handling, and all
  Rust-to-Go state migration.
- Retired webhook/document/connector tables and deletion-only choreography;
  unused event indexes; `legacy_sources`; compatibility defaults; serialized
  resident-session/delivery authority.
- Dead run/control projection fields: `runtime_control_mode`, activity,
  wait/observer fields, unused reconciliation metadata, and any other value
  with no authoritative producer.
- Always-null monetary spend, unconstructed failure variants, free-form event
  action strings, and attention states derived only from dead fields.
- TUI announcements, speculative theme framework, unused glyph tables, stale
  four-view residue, no-op actions, orphan task-fetch comments, and direct
  filesystem mutations that bypass the API.
- Exact CLI aliases, phantom commands, the `claude-code` spelling alias,
  `factoryctl usage`, and read-only Rust storage status with no action.
- The current multi-command TUI bootstrap, `LatestEventSequence` workaround,
  duplicate response/event map writers, and independent polling projection.
- Generic build/plugin/workflow/provider frameworks, actor systems, DI/service
  locators, ORMs, generic sagas, generic repositories, `utils`/`common`/
  `helpers`, and speculative portability interfaces.
- After the cutover gate: all Rust local-runtime crates, the root Rust
  workspace manifest/lockfile if nothing else uses them, transitional Go/Rust
  build paths, and dual-runtime CI/release support. `control-plane/` remains a
  standalone Rust workspace.

Every final deletion must record the invariant/test that made it safe. The
items above are candidates until that evidence exists.

### DEFER

- Historical database/home/protocol/event migration.
- Linux stable runtime, systemd, Linux packaging/provider proof, and portable
  process abstraction beyond compile-time seams genuinely required by tests.
- Automatic update, updater UI/re-exec polish, new release machinery, and
  GoReleaser.
- New GitHub intake, workflows, personas, review features, public integration
  surfaces, quarantine/intake storage, storage-management features, and
  unrelated product work.
- Real Claude/Codex subscription runs. The cutover requires deterministic
  launch/config tests, not paid sessions, unless a later separately justified
  validation explicitly needs them.
- Strong hostile-same-user isolation. The supported claim remains scoped
  cooperative/confused-deputy authority; stronger isolation needs another OS
  user, sandbox, container, or VM.

The existing release freeze remains in force while the Go runtime is being
proved. The cutover must retain the current Darwin ARM64 and AMD64 artifact
contract unless support is deliberately narrowed in a separately reviewed
product decision. Manual verified update/rollback and explicit service
lifecycle are cutover work; background automatic update is not.

## 3. Target architecture

### Binaries

```text
factoryd        sole durable owner, scheduler, supervisor registry, finalizer,
                Change owner, verifier owner, and local-API server
factory-runner  provider-blind live-child owner and blocked-exec/replay host
factoryctl      canonical typed CLI and daemon-absent lifecycle bootstrap
factory-tui     disposable Bubble Tea client using the same API as factoryctl
```

The first kernel slice builds only enough of `factoryd`, `factory-runner`, and
`factoryctl` to drive the proof. TUI, installers, update logic, and broad
provider integration remain blocked until the kernel go/no-go decision.

### Packages and dependency direction

The initial package hypothesis is deliberately smaller than the Rust module
graph:

```text
cmd/*
  -> internal/daemon  -> internal/kernel
                      -> internal/runner
                      -> internal/change
                      -> internal/verify
                      -> internal/provider
                      -> internal/api

cmd/factoryctl -> internal/api
cmd/factory-tui -> internal/tui -> internal/api
cmd/factory-runner -> internal/runner

internal/install is added only after client/kernel cutover readiness.
```

- `internal/kernel`: typed durable domain values plus the one concrete SQLite
  Store, fresh embedded schema, admission, attempt authentication, resources,
  first-outcome/finalization, revisions, and durable invalidations. No Store
  interface, repository layer, or public mutable authoritative structs.
- `internal/daemon`: ownership tree and visible orchestration of scheduling,
  launch checkpoints, runner observation, finalization, API handlers, and
  recovery. It owns policy; `api` does not.
- `internal/runner`: runner protocol, outer/inner gate primitives, live-child
  process-group owner, terminal spool/replay/ack, and build-tagged Darwin
  process identity. It does not know project/task/Change policy.
- `internal/change`: exact Git-tree selection/materialization, Change
  filesystem identity, manifests, adoption/recovery, and stable snapshot
  creation. The same package is used by a private `factoryd --change-worker`
  mode so `factory-runner` remains provider- and Change-policy-blind. It cannot
  launch a verifier or decide an outcome.
- `internal/verify`: the three fixed verification policies and their closed
  launch specifications/environments, bounded result/bundle validators,
  and policy-specific refinement. It consumes a stable snapshot; it does not
  know Git, mutate the live Change, start/reap a process, or update Store.
- `internal/provider`: concrete shell, Claude, and Codex launch-spec builders.
  It receives frozen daemon facts and returns only executable/argv/env/private
  config. Shell is implemented first.
- `internal/api`: bounded private protocol, framing, typed wire values, and the
  three narrow clients (`HealthClient`, `OperatorClient`, `AttemptClient`). It
  has no Store or lifecycle logic.
- `internal/tui`: ordinary testable Bubble Tea update/view code and one shared
  revision-aware client model.
- `internal/install`: private home/bootstrap, safe four-member archive,
  receipt/version activation, and macOS launchd/install/update transaction,
  added late. It is concrete Darwin code, not a speculative service-manager
  interface, and is not part of kernel authority.

If this graph needs forwarding packages or import cycles, collapse packages;
do not add interfaces to preserve the sketch.

### Fresh home and schema

An initialized Go home carries an owner-only regular file named `format` with
exact contents `dark-factory-go-home-v1\n` and mode `0600`. The database is
`factory.sqlite3`, with SQLite `application_id=0x4446474f` (`DFGO`) and
`user_version=1`. Initialization creates, configures, validates, closes, and
syncs the database before atomically publishing `format` as the final home
publication point. An exact database-only Go v1 partial initialization may be
validated and completed; marker-only, mixed, symlinked, Rust-layout, unknown
application-ID/version, or otherwise nonempty unmarked homes fail closed
without any write and explain that Rust-home migration is unsupported.

The intended layout is one private home with database, operator credential,
socket, daemon-owned Changes, per-run runtime roots/token files/runner sockets
and terminal spool, and verification scratch/cache. Exact names are frozen by
the kernel slice and then shared by daemon/install/tests; no binary reads an
ambient default during tests.

The fresh schema contains only current product authority:

- factory settings;
- projects, agents, private profiles/budgets;
- tasks with incarnation and work revision;
- Changes and exact filesystem/base identity;
- runs with immutable admission binding, phase, proposed outcome, terminal
  outcome, exact one-time exit evidence, and timestamps;
- one exact 32-byte attempt digest for each admitted/running attempt, accepted
  only in running and atomically removed/revoked on first finalizing;
- resources with closed kind/state and private exact locators;
- agent messages;
- bounded invalidation records with monotonic sequence and safe entity
  identity/revision;
- concrete verification-effect/result state required by the fixed policies.
  No quarantine/intake or cache table exists; each is added later only if a
  retained producer and measured cache value respectively justify it.

Invalid states are constrained with foreign keys, `STRICT`, `CHECK`, unique
open-run/lease indexes, exact digest lengths, and guarded updates. Unknown
control values make open, read, authentication, and mutation fail closed.

### SQLite ownership

- `factoryd` is the only normal writer process. Database initialization sets
  and reads back the application ID/schema version, `journal_mode=WAL`, and
  `synchronous=FULL`.
- `Store` owns one dedicated initialized writer connection. One unexported
  concrete write helper executes literal `BEGIN IMMEDIATE`, performs domain
  work, rolls back on every error/panic/cancellation, and commits state plus
  invalidations. Domain methods own transactions; callers never receive a
  generic transaction callback.
- The writer uses one `database/sql` handle capped at one open/idle connection
  and one pinned `*sql.Conn` for each whole begin/body/commit-or-rollback. The
  bounded reader handle permits at most four physical connections and uses
  explicit pinned read transactions for multi-query snapshots.
- Every physical writer/reader connection, including replacements, sets and
  reads back `foreign_keys=ON`, `busy_timeout=5000`, and
  `synchronous=FULL`; it verifies persistent WAL before use. There is no
  unbounded application retry. Initialization must be implemented through a
  driver connector/DSN or per-checkout hook that is causally proven to cover
  new pooled connections, not a one-time `sql.DB` bootstrap call.
- If begin/commit/rollback or connection state is ambiguous, the entire writer
  handle is discarded and reopened. The operation returns outcome-unknown and
  is never blindly replayed. Before any later write, its domain method
  reconciles through the already-existing run/entity ID, expected revision,
  immutable first outcome, terminal-observation identity, or other natural
  unique key. If canonical state cannot distinguish the result, the daemon
  stays unavailable for that operation and clients resync; there is no generic
  operations table, mutation-ID ledger, or second protocol identity.
- A mutex serializes use of the dedicated writer connection but grants no
  authority. Tests open independent connections/Store instances to prove
  SQLite conflict behavior.
- Bounded readers use separately initialized WAL connections and explicit
  read transactions for multi-query snapshots.
- The focused candidate-neutral proof selected
  `github.com/ncruces/go-sqlite3/driver` v0.35.3. Both it and
  `modernc.org/sqlite` v1.57.0 passed initial CGO-free build/open probes, but
  `ncruces` also passed the causal contract for foreign keys, WAL, exact busy
  policy, immediate writer exclusion, guarded row counts, crash/reopen,
  concurrent readers, cancellation, connection replacement and ambiguous
  begin/commit/rollback responses with the smaller linked dependency graph.
  `modernc` was therefore removed rather than retained as a fallback. The proof
  lives temporarily in `internal/sqlitecontract` and is deleted once those
  semantics and causal tests move into concrete Store methods; its generic
  callback is not a production Store API.

### Goroutine ownership

`factoryd` main owns one root context and one wait group. Its children are:

- one API accept loop; each bounded connection handler is registered, has a
  deadline/cancellation path, and is joined;
- one scheduler loop that calls synchronous admission/launch work;
- one finalizer/recovery loop that reloads durable work and performs one
  authoritative transition at a time;
- one owned supervisor per live runner connection for framed I/O and terminal
  observation, registered in a daemon registry and joined before removal;
- bounded verifier subprocess I/O drains owned by the synchronous verifier
  operation and joined with it.

There are no fire-and-forget goroutines, authoritative transition callbacks,
or goroutines created merely to make a synchronous call look concurrent. Each
owner records cancellation, terminal condition, and completion observation in
its type/tests. Process termination is separately proven after goroutine exit.

### Provider lifecycle and process boundaries

The launch choreography is one explicit function with durable checkpoints.
The source wrapper has two internal releases in addition to the outer runner
activation; these preserve evidence that the selected commit preceded blob
reads and that the complete Change preceded provider execution:

1. `AdmitNext` commits run, credential, Change lease with exact final and
   staging locators, runtime claim, initial resource declarations, and
   invalidations. The staging name is caller-generated and durable before any
   directory exists; the materializer never invents a hidden random locator.
2. Daemon creates the exact private runtime root and binds its inode.
3. Daemon creates/binds a private startup lease before outer runner spawn.
4. Daemon starts an inert parent-bound runner gate and records exact runner
   PID/birth before releasing it into `factory-runner`.
5. Runner prepares a second parent-bound child as process-group leader. That
   child is the private `factoryd --change-worker`, a registered source wrapper
   blocked before Git selection/provider `exec`; runner reports exact
   PID/PGID/birth.
6. Daemon binds those identities and releases only source selection. The
   wrapper selects an exact local commit without lazy fetch, computes a
   canonical Git-tree commitment, reports OID/digest/count/bytes, and blocks
   before reading materialized blobs. Daemon records the bounded selection.
   The wrapper create-only prepares the already-declared empty staging
   directory and reports its exact identity; daemon persists that prepared
   checkpoint before releasing population. The already registered wrapper
   process group covers its Git descendants; the wrapper synchronously owns,
   bounds, and kill-and-waits every direct Git child, and provider exec cannot
   overlap any of them. No per-command durable resource/gate state is added.
7. The wrapper populates that prepared directory with at most 10,000 total
   entries, depth 64, 1,023-byte relative paths, 255-byte components, 256 MiB
   per blob and 1 GiB total blobs. It publishes without replacement, reports
   the commitment and exact published path identity, and blocks before
   provider `exec`. Daemon scans/hashes the plain published tree
   without Git and compares digest/count/bytes to the selected commitment,
   records the Change available, atomically moves admitted to running, and only
   then releases provider execution. After a publish-before-ready crash, a
   surviving wrapper replays the commitment. If it is gone, the daemon compares
   a fresh bounded no-Git scan with the already durable selected commitment;
   this may retain the Change for a later retry but the current run finalizes
   and no replacement child may move it to running or execute its provider.
8. The same registered child revalidates the frozen native provider commitment
   after activation and pathname-`exec`s it once. Its PID, PGID, and birth
   remain unchanged; startup input is sent once and stdin closes.
9. Runner durably spools one terminal observation. Daemon commits it before
   exact acknowledgement.
10. Completion/blocked/failure/exit requests first outcome, enters finalizing,
   and revokes the credential.
11. Live runner owns stop/signal/reap. Recovery holding only persisted numeric
    identities observes absence and otherwise persists unresolved.
12. For worker proposed success with a configured verifier only: stable source
    snapshot, declared/blocked verifier effect, exact activation, group absence,
    bounded result consumption, and optional post-measurement cache handoff.
    Other outcomes and `None` skip this step.
13. Resource cleanup, runtime-root removal, and terminalization occur in that
    order.

No `exec.CommandContext` call, context cancellation, or goroutine completion
is accepted as proof of process absence.

On Darwin, provider activation uses identity-checked pathname `execve` from
the already registered gate. Each concrete adapter resolves the
operator-facing executable to one canonical native Mach-O target and freezes
its absolute path, device/inode, owner, complete mode, bounded size, nanosecond
timestamps, SHA-256, running-architecture support, argv, environment, working
directory identity, and stdin setup before readiness. After create-only
activation, that same PID reopens and verifies the frozen path, hashes its
bytes, compares a second `fstat` plus named-path identity, closes the verifier
descriptor, and immediately calls `execve` on the committed path. Mismatch,
removal, or `execve` failure is a visible typed launch failure and never causes
retargeting or replay.

Darwin has no supported descriptor-exec primitive, and `/dev/fd` execution is
not viable. The final pathname lookup is therefore not claimed inode-atomic or
secure against a hostile same-UID race, consistent with the repository threat
model. Version-selection symlinks are resolved before readiness; an admitted
run keeps its frozen immutable version path, and old versions referenced by
nonterminal runs are retained. V1 accepts native Mach-O provider executables,
not generic shebang scripts.

### Client architecture

- One owner-only Unix socket, one fresh protocol generation, length-bounded
  frames, structured errors, and explicit principal clients. Request IDs are
  transport correlation only, never durable idempotency/authority identities.
- One read transaction returns a bounded dashboard snapshot and head sequence.
  `Watch(after)` streams monotonic `StateChanged(sequence, entity kind, entity
  ID, entity revision)` invalidations. Clients fetch bounded canonical
  summaries/details; a duplicate is idempotent and a gap forces resync.
- Mutation replies include the committed sequence/revision. Replies and watch
  invalidations use one reducer, so delayed replies cannot regress state.
- CLI and TUI share client methods. No TUI-only mutation or direct filesystem
  policy path exists.
- Private detail reads are explicit and revisioned. Public/dashboard/watch
  payload types cannot grow by adding a field to a private Store model.
- One finite internal `EventRetentionLimit` is used; the spike starts at 4,096
  records and measures bounded snapshot/watch cost before the client contract
  freezes it. It is not a wire guarantee. Each invalidation is bounded to 4 KiB.
  The database durably stores head and retained floor; pruning occurs in the
  same transaction as the new invalidation. `Watch(after)` returns typed
  `ResyncRequired(head, floor)` before streaming anything when `after <
  floor-1`, including a prune between snapshot and watch. Deletions emit safe
  tombstones with the entity's final revision, and every watchable mutation
  advances that entity revision.

### Platform transaction

Platform work begins only after the kernel and clients are stable. `init`,
manual update, and `factoryctl service install|start|restart|stop|uninstall`
use one concrete install library and shared mutator prelude:

1. Canonicalize and verify exact home/job ownership and format before any
   write. An unknown/Rust home receives no Go marker, database, `bin/`, link,
   or plist.
2. Acquire one runtime-mutation lock, recover any pending record, and bind
   exact home device/inode/uid, socket, current receipt/link, plist digest,
   launchd label/domain, and operation authority.

Init/manual update then parse gzip/tar in-process and require exactly
four root regular executable members with bounded individual/aggregate bytes,
expected names/modes, Darwin architecture, version/build identity, and hashes.
They write/sync private staging and receipt, atomically publish one immutable
version directory without replacement, durably record old/new activation and
plist/service phases before effects, swap the relative `bin/current`, render
and reload one allowlisted plist with `AbandonProcessGroup=true`, and prove
launchd PID, actual daemon executable/build identity, and all four receipt-bound
siblings. Exact new health commits/removes pending state. Every known pre-health
crash rolls back link then plist/service; unknown ownership/control state fails
closed and preserves evidence.

Service install validates an already-active exact receipt/current runtime,
installs only its allowlisted job, and never stages an archive or changes the
version link. Start validates the exact installed receipt/current/job,
bootstraps it, and proves health. Restart validates the same authority, reloads
only the daemon, proves health, and preserves exact admitted runner/provider
identities. Stop
requires exact job/PID ownership, distinguishes documented launchd absence from
spawn/permission/parse/service errors, boots out, and proves the old PID and
socket absent; through `AbandonProcessGroup` it does not invent ownership of
admitted child groups. Uninstall requires stopped exact ownership and no
nonterminal work/resources, removes only the allowed job/link/receipts/runtime
metadata, and preserves SQLite and retained Changes by default. These verbs do
not enter archive staging or activation paths unless their definition above
requires it.

`service status` is a read-only validator/projection. It validates the exact
home marker, receipt/current link, plist and label/domain, launchd result/PID,
actual executable/build identity, socket, and pending-record state, and reports
typed absent, stopped, running, healthy, degraded, pending, or ambiguous state
while preserving operational launchctl errors. It never creates/acquires a
mutation lock, recovers a pending mutation, stages/activates a version, writes
a link/plist/database, or bootstraps/boots out a job. Recovery requires an
explicit mutating command.

Release adaptation retains two deterministic Darwin archives, `SHA256SUMS`,
`latest.json`, and `dark-factory.rb`, all bound to one immutable source tag,
commit, receipt/build identity, and exact publisher reconciliation. Live tap
publication, signing/notarization, and automatic update remain explicitly
deferred; no stronger distribution claim is made. Release/non-PR gates require
hosted ephemeral execution or a separately proven isolated runner rather than
silently retaining the current persistent-runner credential exposure (#54).

## 4. Kernel proof

The first implementation is a vertical, integrated go/no-go spike. It includes
only the Store/domain, the two-gate runner, shell provider, minimal exact
Change, finalizer/recovery, the private API, and enough CLI to drive it.

The deterministic witness is:

```text
fresh Go-marked home and SQLite schema
  -> create project with tiny local Git fixture and exact base commit
  -> create agent with shell/unrestricted profile
  -> create assigned task
  -> atomic canonical admission inside BEGIN IMMEDIATE
  -> reserve daemon-owned Change and exact source/base authority
  -> mint one attempt credential and declare runtime/runner/provider resources
  -> prepare outer runner and registered source/provider wrapper before exec
  -> persist exact identities; release exact commit selection before blob read
  -> materialize/publish and revalidate the complete exact Change manifest
  -> persist Change available and transition admitted -> running
  -> release the exact shell provider once
  -> provider makes a typed authenticated completion request
  -> transition to finalizing and revoke credential
  -> stop/reap provider; for configured worker success only, take a stable
     snapshot and run the once-only registered verifier effect
  -> release resources, remove runtime last, finalizer writes terminal
  -> client snapshot/watch agrees with SQLite
```

The shell provider writes an external witness only after activation. Tests
count that witness and startup-input digest, rather than trusting callbacks or
stdout. The slice then repeats across real daemon/runner SIGKILL cuts after
every durable/effect checkpoint and proves at most one provider execution, no
input replay, no invented release, no unsafe signal, and eventual deterministic
convergence where exact absence is provable.

Go is accepted for broad implementation only if this slice is smaller and
easier to trace than the corresponding Rust path while retaining the
invariants and killing the required mutations. If the two-gate lifecycle,
Store transaction semantics, or recovery requires more/larger abstraction, or
if exact process ownership becomes weaker, stop and reassess the language
decision. TUI/provider/update progress cannot compensate for a failed spike.

## 5. Work graph

### Wave 0: read-only contract audits

- Kernel/process audit — agent `kernel_process_audit`: complete at `c21c8d4`.
- Store/state audit — agent `store_state_audit`: complete at `c21c8d4`.
- Simplification/client/TUI audit — agent `simplification_clients_audit`:
  complete at `c21c8d4`.
- Change/verification audit — assigned read-only after the kernel audit.
- Platform/release audit — assigned read-only after the Store audit.
- Test-contract/mutation audit — assigned read-only after the client audit.

All six audits completed read-only at `c21c8d4`; no audit agent wrote source or
ran a provider/process fixture. Their must-keep, known-defect, deletion, Go
shape, and causal-test findings are incorporated here.

This document consolidates the reports; audit agents have no write ownership.

### Wave 1: kernel spike, serial contract freeze

1. SQLite driver/transaction proof: one agent owns `go.mod`, `go.sum`, and
   candidate-neutral driver tests in its own worktree. No product schema/Store
   is ported until one driver passes and the rejected candidate is removed.
2. Durable kernel: one agent owns typed state, Store admission/credential/
   outcome/resource/finalizer transitions and invalidations. It starts only
   after the transaction pattern is reviewed and frozen.
3. Runtime gate: a separate worktree agent owns `internal/runner` and
   `cmd/factory-runner`, driven by frozen protocol fixtures. It does not import
   Store and must not change schema/domain types unilaterally.
4. Minimal source/verification: separate non-overlapping agents own
   `internal/change` and `internal/verify` only after their kernel-facing
   records are frozen. The Change wrapper/runner protocol is integrated with
   the runtime lane, not independently reinvented.
5. Integration/API: the orchestrator integrates the three exact commits, then
   assigns a bounded agent to `internal/daemon`, `internal/provider/shell`,
   `internal/api`, and the minimal CLI driver after shared types are frozen.
6. Fresh independent reviewers attack Store/authority and process lifecycle on
   the exact integrated head. Findings are fixed and re-reviewed.
7. Mutation agent applies temporary guard-breaking changes one at a time and
   records the killing test. No mutation code is committed.
8. The authoritative kernel matrix decides go/no-go.

Only non-overlapping package ownership runs concurrently. Schema, durable
enums, protocol envelope, and resource identity are orchestrator-owned shared
contracts; a lane needing a change stops and coordinates before editing.

### Wave 2: expansion after go

- Lane A, kernel: remaining project/agent/task/message behavior and derived
  typed NEEDS YOU actions
  behavior and bounded invalidations.
- Lane B, runtime: Claude/Codex concrete launch adapters, observation/recovery
  hardening, and deterministic fake-executable tests.
- Lane C, source: complete Change lifecycle, Rust/Go verification, retained
  cache only if measured.
- Lane D, clients: full private API/client, concise CLI, revisioned model, and
  Bubble Tea BUILDING/AGENT TUI.
- Lane E, platform: Go bootstrap/install/launchd/packaging after A-D stabilize.

Each writing assignment gets its own worktree, one owned file/package set, a
commit, focused commands, exact results, and unresolved risks. The orchestrator
integrates continuously in dependency order and runs broad gates only at
milestones so process/Cargo/Go gates do not conflict on the machine.

### Wave 3: hard cutover

1. Freeze Go contracts and pass full Go gates/reviews.
2. Run a dedicated code-elegance audit on the complete Go local runtime, make
   the accepted DRY/YAGNI refactors, and obtain a fresh ALLOW on the resulting
   exact head. This audit is separate from correctness and deletion reviews.
3. Make Go binaries the only local runtime and Go gates the local-runtime CI.
4. Update architecture, security, workflow, provider, install, and release
   documentation in the same change.
5. Delete the five Rust local-runtime crates and root Rust workspace artifacts;
   retain `control-plane/` as its standalone Rust workspace and gate.
6. Delete transitional scaffolding/compatibility and repeat clean-checkout
   build, dependency/package review, isolated install E2E, and process census.
7. Obtain final independent architecture, security, process, Store/concurrency,
   and simplification reviews on the exact head, fix every finding, and repeat
   the affected gates.

### Code-elegance audit

After the complete Go runtime works and before Rust deletion, a reviewer who
did not author the majority of the implementation performs a cold structural
audit. It is explicitly not a request for code golf or premature abstraction.
The reviewer must trace the production package graph and representative
project/task/run lifecycles, then look for:

- policy, transition, validation, serialization, cleanup or error mapping that
  has more than one owner;
- repeated code that can become one direct concrete function without merging
  distinct authority checks or creating a framework;
- interfaces with one production implementation, forwarding wrappers,
  pass-through layers, generic callback machinery, speculative extension
  points, unused exports and packages with no independent reason to change;
- persisted fields or states that are derivable bookkeeping rather than real
  external conditions;
- control flow, goroutine/channel use, error types, naming and file/package
  boundaries that make ownership harder to follow than a direct call would;
- test helpers that prove their own callbacks instead of real state, process or
  filesystem effects, and fixtures whose abstraction hides causal order;
- dependencies whose retained value is smaller than the code/attack surface
  they introduce.

The audit returns an inventory of duplication and unnecessary machinery,
specific deletions/collapses, cases where apparent duplication must remain
because the authority or failure semantics differ, and ALLOW/BLOCK. Accepted
changes are implemented as small commits, the affected causal/race/process
gates are rerun, and a fresh reviewer re-checks the exact refactored head. No
refactor may collapse `running -> finalizing -> terminal`, widen credentials,
replace durable Store guards with memory, weaken exact resource identity, or
trade fail-closed uncertainty for a common helper.

## 6. Test contract

Every row names a causal external/durable observation and a mutation it must
kill. Passing Rust tests are evidence only; Go tests are written from these
contracts.

| Invariant | Causal Go proof | Required mutation killed |
| --- | --- | --- |
| SQLite configuration | On native macOS and Linux, open fresh/reopen/concurrent connections; assert foreign keys on every pooled connection, WAL readers during an immediate writer, bounded busy behavior, literal immediate exclusion, rollback after SIGKILL, and acknowledged state/event survival | deferred `BEGIN`; connection without PRAGMAs; swallowed/unbounded busy error; split transaction |
| Atomic canonical admission | Race stale observation, priority/timestamp/ID insertion, dispatch disable, budget/agent gates, factory-wide capacity including finalizing, and two admits; separately prove one-open-run uniqueness per agent/task incarnation/Change and inspect the exact footprint | caller-selected task; stale queue head; per-agent capacity; unchecked task row; process-local-only capacity |
| Commit ambiguity | Cut/interrupt begin, commit response, and rollback; discard handle, reopen, reconcile by domain ID/revision, return outcome-unknown where not provable, and perform no second transition | retry blindly; reuse ambiguous connection; generic receipt fallback |
| Fresh schema allowlist | Query schema objects after init and assert the exact table/index/trigger allowlist excludes operations, mutation receipts, decisions, quarantine, intake, compatibility, and migration residue | add speculative authority table; retain Rust compatibility object |
| Exact attempt authority | Exercise forged, old, admitted, wrong-run/project, operator, finalizing and terminal credentials against every attempt mutation | drop phase join; accept caller IDs; operator fallback; reuse credential on retry |
| First outcome/finalizing | Run completion-before-exit and exit-before-completion; assert immutable proposal and revoked credential | overwrite first proposal; mutate during finalizing; direct running->terminal |
| Finalizer only/one-way | With all resources released, repeated/concurrent finalizers create one terminal record/event; with any unresolved resource, they create none; later positive absence permits only unresolved->released->one terminal | terminalize unresolved; released->active/unresolved; duplicate terminal event |
| Register-before-exec | External provider witness remains absent until run/resource identities are committed running; replacement before activation, version-symlink swap, target removal, byte/mode mutation, final-check failure, and lost activation acknowledgement preserve the frozen launch or fail without execution; a controlled post-check replacement records the explicitly out-of-scope same-UID pathname seam | release either gate early; omit preparation leash; persist identity after exec; re-resolve installation symlink; omit final metadata/digest comparison; retarget on mismatch; claim inode-atomic execution |
| Owned process authority | Live runner stops exact direct-child group; recovered daemon with only PID/PGID never signals | daemon killpg fallback; retain signal authority after Wait; skip kill-and-wait |
| Liveness fails closed | Real ESRCH, EPERM where feasible, malformed/overflow IDs, weak/mismatched/reused identity and leader-with-descendant | EPERM as absent; malformed as released; leader exit equals group absence |
| Crash/restart at-most-once | SIGKILL daemon/runner at every launch, exit, cleanup and acknowledgement cut; count external witness/input; reopen same home | relaunch admitted run; ack before Store commit; remove runtime before absence |
| Change exactness | Materialize a real commit and verify manifest/blob/mode/path/base/inode; deny Git discovery/worktree and replacements | resolve moving ref; `git archive`; allow symlink/gitlink/.git; wrong base; delete replacement |
| Change crash adoption | Kill after staging publish but before ready; corrupt published bytes without changing inode/size; restart must recompute the selected-commit manifest and refuse adoption | trust inode/size checkpoint; skip blob digest on recovery |
| Private Change worker | Invoke the private mode without inherited owner-only descriptors/parent gate and prove no Git read/path/child effect; exercise the registered mode normally | accept direct argv invocation; perform effect before capability check |
| Stable verification | Provider attempts concurrent write while finalizing; provider must be reaped; scan/copy/scan either yields one digest or refuses; verifier launches controlled snapshot | verify live Change; inherit GOENV/cache/temp/network; launch mutable build output |
| Verifier bundle identity | Copy two executable fixtures, mutate the second after the first runs, and prove the second never executes | validate a verifier bundle only once; omit immediate pre-launch recheck |
| Verification applicability/refinement | Proposed worker success with configured policy verifies once; `None`, orchestrator, blocked, failed, and exit proposals launch no snapshot/effect; verification failure preserves proposal and refines only terminal result | verify every outcome; snapshot for `None`; overwrite first proposal |
| Verifier crash authority | Cut after declaration, activation, result publication, leader exit, group absence, cache measurement, and temp cleanup; valid result is read only after group absence and attempted/no-result never reruns | trust result while live; rerun after restart; cache before writer absence |
| State/event atomicity | Force invalidation insert failure after state DML and state DML failure before event; reopen | separate commits; event from stale pre-write snapshot; missing derived invalidation |
| Sequence/resync/client agreement | Snapshot at N plus watch N+1 during concurrent mutations; inject duplicate, gap, lag, restart and delayed response; compare client to fresh canonical state | discard lagging unseen event; accept gap; delayed reply overwrites newer revision |
| Bounded invalidation retention | Fill to `EventRetentionLimit+1`, prune between snapshot/watch, request before floor, delete entities, and restart; require `ResyncRequired` before later frames and exact tombstones/revisions | unbounded log; stream partial tail; omit deletion revision |
| Public privacy/bounds | Seed unique sentinels in every private field, serialize every dashboard/event/status frame, and scan sizes/content | expose root/body/result/message/token/prompt/output; grow snapshot beyond cap |
| Provider environment/token | Seed ambient API/provider/proxy/Git/GitHub/SSH/loader sentinels; inspect child argv/env, token mode/content, startup frames, logs/errors/events, and provider-launched CLI auth | inherit `os.Environ`; put bearer in env/argv; operator fallback |
| TUI/CLI parity | Enumerate every TUI mutation and assert it invokes the same typed client method/daemon request as CLI | direct filesystem/policy mutation; display label selects behavior |
| NEEDS YOU authority | Derive only the three actionable typed projections; resolve with exact operator/source revision and atomically commit state/intent/invalidation; test exact replay, stale/conflict/already-resolved/attempt denial and public-label privacy | duplicate policy table; inert choice; stale/attempt action |
| Reducer deletion monotonicity | Delete an agent/task, then deliver a delayed mutation reply at an older/equal revision; entity remains absent and client equals fresh snapshot | resurrect deleted entity; ignore delete invalidation |
| Operator reply loss | Drop a reply after durable operator mutation commit, reconnect/resync without payload replay, and prove one state effect/invalidation | automatically retry request ID; treat transport correlation as authority |
| Goroutine/resource ownership | Leak-check each owner after normal/error/cancel/crash; exact process/path census; run race detector | unjoined goroutine; cancel treated as reap; fixture cleanup only in killed process |

### Required crash cuts

- after admission;
- after runtime/resource declaration and after exact path binding;
- before and after outer spawn/PID binding/activation;
- after inner child preparation and identity persistence;
- before/after source-selection release, selection report/commit,
  materialization release, staging publication, readiness report, plain-tree
  rehash, and Change-available commit;
- immediately before/after provider release and lost release acknowledgement;
- after provider exit, terminal spool publication, Store observation, and
  before/after runner acknowledgement;
- during each resource cleanup and runtime-root removal;
- before terminal transaction and before client acknowledgement;
- after verifier temp/resource declaration and identity binding;
- immediately before/after verifier activation and result publication;
- after verifier leader exit/group absence, cache measurement/handoff, and
  temporary-root cleanup.

Outer spawn, PID binding, activation, source selection/materialization, and
provider-exec failure are also direct causal cases: they execute no provider
before authority is ready, take the correct admitted/running-to-finalizing
edge, kill-and-wait every still-owned child, and terminalize only after exact
resource release.

Each fixture uses an isolated private home/socket/repository/process group and
two independent cleanup mechanisms: a normal owner/reaper and an autonomous
hard safety reaper that survives test panic/process death. No sleep proves
absence, no broad command-name kill is allowed, and every test ends with an
exact process/path census.

### Required platform/release proofs

- marker-only, database-only, exact recoverable Go partial init, Rust home,
  unknown application ID/version, symlinked components, and proof that every
  refused home receives no write;
- archive traversal, absolute path, link/hardlink, duplicate, extra, mixed
  build, wrong architecture/mode, compression/aggregate bound, tampering, and
  exact four-member success;
- cuts after every staging file/receipt/directory sync, version publication,
  pending-record phase, current-link swap, plist write, bootout/bootstrap, and
  health proof, followed by exact commit/rollback/refusal;
- random-label isolated service install/start/status/restart/stop/uninstall,
  operational launchctl error classification, exact daemon executable/PID and
  socket absence, runner/provider survival across restart, nonterminal uninstall
  refusal, database/Change preservation, and proof that start/stop/uninstall do
  not enter archive staging/version-activation paths;
- with every pending-record and launchd outcome class, status reports exact
  typed state/error while marker, database, pending record, current link, plist,
  job/PID, and socket remain byte-for-byte/identity unchanged;
- reproducible Darwin ARM64/AMD64 archives and exact
  `SHA256SUMS`/`latest.json`/formula membership, immutable tag/commit binding,
  partial publication/digest mismatch/extra-asset reconciliation, and cold
  extraction/build identity checks;
- a before/after assertion that the production home, label, plist, socket, and
  daemon were neither read nor changed.

### Required mutation ledger

At minimum record the killing test for:

- queue-head transaction selection and queued-task row count;
- attempt phase/credential check and operator fallback;
- `EPERM`, malformed PID/PGID, and weak identity handling;
- finalization idempotency and released-resource terminality;
- exact Change base identity and existing-source deletion protection;
- public privacy filtering and encoded bounds;
- event sequence/gap handling and stale response/event ordering;
- both provider-before-registration gates;
- observer fresh-state reload and exit observation idempotency;
- complete Change manifest adoption after publish-before-ready crash;
- verifier sibling identity immediately before each launch;
- outer runner activation-error kill-and-wait;
- source-selection-before-blob-read and Change-available-before-provider-exec;
- provider `env_clear`, attempt token-file-only delivery, and operator fallback;
- verification applicability, no-rerun recovery, and result-after-group-absence;
- domain-key commit-ambiguity reconciliation with no blind replay;
- private Change-worker inherited capability check;
- derived NEEDS YOU revision/operator checks and no inert decision;
- deletion tombstone defeating a delayed stale response;
- transport request ID never authorizing automatic mutation replay;
- fresh-schema allowlist excluding speculative/compatibility tables.

Mutation changes are temporary, one at a time, and never retained. A flaky or
unrelated failure is not a kill; the focused expected assertion must fail and
then pass after restoration.

### Development and authoritative gates

`scripts/go-check.sh` is the fast default and will perform, in order:

1. exact pinned-Go check;
2. `gofmt` check;
3. `go vet ./...`;
4. focused pure package/unit tests that do not launch process fixtures;
5. `git diff --check`.

`scripts/go-ci.sh` is the authoritative local-runtime Go sub-gate and is
serialized through the repository's shared gate lease where process/Rust/Go
coexist. It will perform:

1. fast gate;
2. all Go unit/integration tests;
3. `go test -race` over race-safe packages/tests;
4. serial process/crash/Change/verification tests with independent reaper;
5. deterministic shell-provider end-to-end lifecycle and restart proof;
6. deterministic fake Claude/Codex launch/config tests;
7. exact post-test process/path/resource census;
8. `git diff --check` on the exact head.

Broad process gates never run concurrently in multiple worktrees. Focused
package tests are the agent default; the orchestrator runs authoritative gates
at integration milestones.

`./scripts/local-ci.sh` remains the one authoritative repository entry point
named by `AGENTS.md` and CI. At hard cutover it is changed atomically to invoke
the Go sub-gate plus retained release/script checks and the standalone
`control-plane/` Rust gate. `AGENTS.md` and workflow commands change in that
same cutover commit; two competing authoritative entry points never exist.
Script-fixture coverage proves this top-level gate invokes the Go and standalone
control-plane sub-gates exactly once each.

Process-sensitive fixtures use an externally supervised ownership harness.
Readiness/cuts use inherited pipes or locked descriptors, not sleeps. The
outer supervisor records its direct child before assertions and independently
reaps it after panic, timeout, or inner SIGKILL. Test success and cleanup
success are distinct; PASS is printed only after owned children, groups,
sockets, roots, file descriptors, and scoped goroutines reach zero. Uncertain
identity preserves the scratch root and fails instead of broad-signaling or
deleting evidence.

## 7. Simplification candidates

The initial deletion ledger follows. Final entries add exact deletion commit
and proof.

| Candidate | Why it is not the product contract | Evidence required before deletion |
| --- | --- | --- |
| 35 Rust migrations and legacy event/home compatibility | No users/state require migration; Git history is archive | Go home/schema refusal tests; fresh install E2E |
| Webhook/connector/document/quarantine residue | No cutover-critical intake producer/action survives; future intake can define a smaller authority boundary | schema/package search; no Go intake producer/consumer; kernel admission source tests |
| `legacy_sources` | Fresh Go schema can never create a row | old-home refusal; no Go producer/consumer |
| Snapshot-heavy durable events | SQLite is authority; snapshots couple privacy/read fields to replay | invalidation sequence/resync/client agreement/privacy matrix |
| Dead run/observer/control fields and failure variants | No authoritative producer/reachable state | constructor/writer search plus exhaustive typed Go transitions |
| Announcements/theme/four-view TUI residue | Not rendered or useful to core BUILDING/AGENT workflows | TUI behavior/parity tests for retained views/actions |
| CLI aliases/usage/read-only storage status | Duplicate/no-action surface; no scheduling consumer | one-command-per-operation CLI tests and operator-action inventory |
| Whole-profile update and display-string actions | Duplicate validation and lost-update/control risk | granular revision-conflict and typed-action tests |
| Latest-sequence/bootstrap/status polling machinery | Compensates for non-atomic client projection | snapshot-at-N/watch-from-N gap/resync tests |
| Three-state Rust cache lifecycle and public status | Regenerable bookkeeping, not external uncertainty; no operator action | verification benchmark and cache lease/reclaim tests |
| Repeated toolchain hashes/bundle wrappers/full sync policy | Duplicate cost/identity work may not buy a distinct guarantee | adversarial replacement tests and measured durability/performance |
| Rust local-runtime crates/root workspace | Replaced process/product boundary after cutover | full Go gate, isolated install, exact-head reviews, clean checkout |
| Legacy plist argv/environment carry-forward | Hard cutover owns one exact job; silent partial parsing widens authority | foreign/malformed plist refusal and exact rendered-job tests |
| System `tar -xzf` extraction | Archive traversal/link/duplicate behavior is not a product feature | in-process bounded archive rejection matrix |
| Persistent privileged release runner | Repository automation must not inherit long-lived operator credentials (#54) | hosted ephemeral or separately isolated runner proof before release unfreezes |

Candidates that represent real external uncertainty—finalizing, active/
releasing/unresolved resources, Change lease/identity, provider/verifier
process groups—are not deleted merely because their current code is verbose.

## 8. Risks

### Process supervision

Go's `exec.Cmd`, contexts, and goroutines do not supply ownership proof.
Every live child needs one owner through `Wait`; every error path must kill and
reap; a process group can outlive its leader. Darwin process birth identity is
a focused design/review item. Weak identity is safe only if it remains visibly
unresolved and cannot authorize a signal or removal.

### Register-before-exec

Both gates and every durable checkpoint are load-bearing. Parent death before
release must close/leash the inert child, and restart must prove it gone rather
than infer absence. A smaller Go design may consolidate primitives, not the two
ownership boundaries.

The Darwin gate performs a final identity and SHA-256 comparison of its frozen
native executable commitment and then immediately pathname-`exec`s it. This
catches ordinary replacement, partial update, deletion, corruption, mode
change, and version-symlink retargeting without inventing an unsupported
descriptor-exec mechanism. A hostile same-UID replacement in the final
check-to-`execve` interval remains outside the security boundary and is tested
as a documented negative assurance, never described as a prevented race.

### Process-group identity

Numeric PGID reuse, leader loss, `EPERM`, `ESRCH`, malformed locators, and
observation races can create orphans or false cleanup. Only the live runner
with the unreaped direct child signals. Recovery observes exact absence or
stalls unresolved.

### SQLite semantics

`database/sql` pooling and `BeginTx` do not imply connection PRAGMAs or
`BEGIN IMMEDIATE`. Driver/pool behavior must be demonstrated. Cancellation,
busy timeouts, commit ambiguity, guarded row counts, WAL readers, and reopen
are part of the kernel proof, not configuration assumptions.

### Go domain invariants

Go lacks exhaustive enums and ownership types. Persisted/wire control values
use private fields, typed string constants, explicit constructors/transition
switches, validation at every boundary, and database constraints. Unknown
values never default to a plausible behavior.

### Event/state consistency

The current snapshot-heavy event and multi-projection client can lose or
regress state. The invalidation model risks extra local reads but makes SQLite
authority and privacy explicit. It must be measured under bounded fleet/history
limits and proven across gaps/restarts before it is frozen.

### Filesystem identity

Paths are not authority. Runtime roots, Changes, staging, snapshots, caches,
token/config files, and removal targets need private creation, bounded names,
device/inode/owner/mode checks, no unsafe links, and explicit ordering with
SQLite. Cross-filesystem rename and crash ambiguity must fail visibly.

### Verification

Verification code executes same-UID project tests and is not a hostile-code
sandbox. It must never start while provider writes continue, reuse mutable
top-level artifacts without identity proof, inherit ambient Go configuration,
or turn cleanup uncertainty into success. Network policy must be explicit.

### Goroutine/resource leaks

Cancellation is not cleanup. A goroutine can outlive its run and a returned
goroutine can leave FDs/processes/locks. Ownership trees, wait groups, bounded
channels only where needed, race tests, goleak-style evidence if useful, and
exact OS census are required.

### Hard cutover and platform

There is intentionally no rollback to a Rust home or dual runtime. The Go
format marker must fail old homes clearly. macOS launchd/install tests must use
randomized scratch labels and homes; the installed label/home/socket remain
untouched. `control-plane/` must keep its independent Rust gate after root
Cargo removal.

The platform transaction must distinguish launchd “not found” from permission,
spawn, parse, and service errors; bind the actual daemon executable plus all
four sibling binaries to one receipt/build identity; reject symlinked path
components and foreign plists; and recover every activation/reload cut. An
incompatible Go home is never activated over a Rust database, so pointer
rollback cannot masquerade as database rollback.

### Scope pressure

The issue backlog contains useful future product work that is not required for
the rewrite. Kernel correctness, basic useful work, client parity, and local
macOS replacement define cutover. Intake, auto-update, Linux release, new
workflows/personas/review features, and storage product expansion remain
deferred even if adjacent code is being replaced.

## 9. Cutover gate

Rust local-runtime deletion is blocked until one exact Go head has all of the
following evidence:

- fresh schema/home marker and explicit old-Rust-home refusal;
- SQLite driver proof and schema constraint/corruption matrix green;
- atomic admission, credential, outcome/finalization, resource, and event
  causal matrices green;
- two-gate register-before-exec proof and every real crash cut green;
- at-most-one provider witness, no startup-input replay, and exact terminal
  convergence across daemon/runner death;
- `EPERM`/`ESRCH`/malformed/reuse/leader-loss behavior green with no unsafe
  signal or invented release;
- Change exact-base/bounded-tree/Git-boundary/removal matrix green;
- `None`/non-success/orchestrator no-verifier proofs and configured
  `RustWorkspaceTest`/`GoWorkspaceTest` stable-snapshot, once-only recovery,
  controlled-environment, and exact resource-cleanup proofs green;
- public privacy/size matrix, invalidation sequence/gap/resync, and canonical
  client agreement green;
- CLI/TUI operation parity and basic useful autonomous shell-provider flow
  green;
- deterministic fake Claude/Codex native launch/configuration tests green;
- `scripts/go-check.sh`, `scripts/go-ci.sh`, full race suite, serialized process
  suite, deterministic shell E2E, and post-test resource census green;
- fresh isolated macOS build/init/start/CLI/TUI/task/restart/stop/uninstall E2E
  green without observing or changing the live installation;
- exact four-member, traversal/link/duplicate/extra-resistant archive tests;
  deterministic Darwin ARM64/AMD64 artifacts; one receipt/build identity; and
  crash-safe activate/reload/rollback tests green;
- packaging hashes/binary set and standalone `control-plane/` Rust gate green;
- required mutations killed by the intended focused tests and mutation code
  absent;
- dependency tree/package graph inspected with no accidental framework or
  transitional dual-runtime dependency;
- dedicated code-elegance/DRY/YAGNI audit completed, accepted refactors landed,
  affected causal gates rerun, and a fresh reviewer returns ALLOW on the exact
  refactored head;
- independent architecture, security/authority, process lifecycle,
  Store/concurrency, and simplification reviewers return ALLOW on the exact
  head after all findings are resolved;
- a clean checkout builds all four Go binaries, runs the authoritative gates,
  and ends with a clean process/path/resource census.

Only then may the cutover commit make Go the sole local runtime, delete the
five Rust crates and obsolete root workspace/build/release paths, update the
repository authority/architecture/security/workflow/install/provider docs, and
remove rewrite scaffolding. A green source build, a passing happy path, a
merged PR, or a deterministic shell run alone is not cutover approval.

## Permanent evidence record

This section is intentionally populated as work lands. Before final success it
must contain:

- final SHA and shipped package/process/schema/client architecture;
- every major design change and known Rust bug fixed;
- every deleted/not-ported behavior with its proof;
- dependencies added and why each remains;
- exact commands/results for focused, full, race, crash, mutation, privacy,
  process-cleanup, install and clean-checkout evidence;
- causal kernel proof and independent review verdicts;
- code-elegance audit findings, accepted/rejected DRY/YAGNI changes, resulting
  package/dependency reductions, rerun gates and exact-head ALLOW;
- remaining risks and deliberately deferred work;
- before/after production LOC, test LOC, local-runtime package/crate count,
  direct dependencies, clean build time, focused/full gate time, binary sizes,
  and idle daemon memory where practical.

If Go is larger, this record must explain which retained safety proof or
operator behavior accounts for it. LOC is evidence about comprehensibility,
not a target.

### Landed kernel evidence

SQLite/driver proof on integrated head `fc533eb`:

- Selected the CGO-free `github.com/ncruces/go-sqlite3` v0.35.3 driver after
  running the same initial build/open comparison against `modernc.org/sqlite`
  v1.57.0; no comparator or fallback dependency remains.
- Proved exact per-checkout `foreign_keys=ON`, `journal_mode=WAL`,
  `synchronous=FULL` and `busy_timeout=5000`, including poisoned pooled
  connections and verified replacements.
- Proved literal `BEGIN IMMEDIATE`, same-process and independent-process writer
  exclusion, concurrent WAL readers, cancellation of a blocked writer,
  guarded state/event atomicity, SIGKILL before/after commit, reopen, and
  bounded connection/goroutine/file-descriptor ownership.
- Test-only driver faults distinguish begin, commit and rollback response
  ambiguity before and after the underlying call. Each returns typed
  outcome-unknown, preserves its cause, discards the exact physical
  connection, never replays the callback, and exposes zero or one durable
  footprint as appropriate. Rollback pre/post forwarding has an independent
  call-order witness because its durable footprint is intentionally identical.
- Mutations killed include changed busy policy, skipped replacement
  verification, timeout mistaken for crash, swallowed commit response error,
  retained ambiguous connection, leaked cancellation goroutine, plain
  cancellation without outcome-unknown, retained rollback-ambiguous
  connection, and collapsed rollback pre/post cuts. Mutation code was removed.
- The original author ran `go test ./... -count=3` in `30.731s`,
  `go test -race ./... -count=1` in `13.798s`, `go vet ./...`,
  `go mod verify`, formatting/diff checks and a clean process/temp census on
  exact proof head `7e808236`. A fresh independent reviewer returned ALLOW on
  that exact head after three earlier BLOCK/fix cycles.
- After unchanged cherry-pick, the orchestrator ran CGO-free
  `go test ./... -count=1 -timeout=120s` in package time `10.521s`,
  `go test -race ./... -count=1 -timeout=150s` in package time `13.876s`,
  `go vet ./...`, `go mod verify`, `gofmt -d`, `git diff --check`, and clean
  test-temp/process censuses. The first orchestrator attempt did not compile:
  the read-only owner module-cache parent could not be created; rerunning from
  a task-owned `/private/tmp` cache passed and did not touch a Dark Factory
  home.
- Darwin ARM64 executed natively. Darwin AMD64 and Linux AMD64 CGO-free test
  binaries cross-built successfully; Linux runtime behavior remains deferred.

Darwin process-semantics proof on integrated head `47e50e7`:

- Proved the exact `kern.proc.pid` start-time/PGID identity remains stable
  across `syscall.Exec`, remains observable for an exited unreaped direct
  child, and disappears after the sole `Wait`. On this Darwin version the
  post-Wait sysctl result is `EIO`, so absence requires both exact PID and
  negative-PGID probes to return `ESRCH`; `EIO` alone is never absence.
- Proved an acknowledged pre-exit `EVFILT_PROC/NOTE_EXIT` registration reports
  exit without reaping. A fresh registration on a known unreaped zombie returns
  an `EV_RECEIPT` `ESRCH`; therefore production must register while the child
  is still inert. A raced registration is launch failure followed by the live
  owner's synchronous sole `Wait`, never a recovered/missed watcher.
- Proved leader exit does not imply group absence, group KILL reaches a
  remaining exact member while the leader stays unreaped, TERM-ignore requires
  bounded KILL escalation, create-only/no-follow markers do not replace, leash
  EOF prevents pre-marker effect, marker creation linearizes a single possible
  effect, and an explicit kqueue wake permits bounded watcher join.
- Mutations killed include ignoring birth time, a watcher calling `Wait`,
  waiting the leader before group cleanup, dropping `O_EXCL`, inheriting an
  extra leash writer, mapping `EPERM` to absent, releasing before the first
  kqueue receipt, and accepting a zero-timeout observation. Mutation code was
  removed.
- The first independent review BLOCKED an uncontrolled immediate-child
  registration race. The repaired test constructs a blocked child, registers,
  releases, observes NOTE_EXIT, verifies exact unreaped identity, then performs
  the late-registration negative. The reviewer stress-ran it 100 times and
  returned ALLOW on exact proof head `033d86fc`.
- The author ran the focused package three times (`0.277s`), its race test
  (`6.352s`), the full Go suite (`10.648s`), vet/module/format/diff checks and
  clean resource censuses. After unchanged cherry-pick, the orchestrator ran
  focused count-three (`0.415s`) and race (`6.349s`) successfully.
- This narrow OS proof deliberately does not claim a provider-forked descendant
  or actual runner-parent-death descriptor hygiene. The production runner's
  real process/crash suite must prove both with group-wide independent safety
  cleanup before the kernel go/no-go gate.

Store foundation on integrated head `6272a8d`:

- Added one concrete `internal/kernel` package with immutable typed IDs,
  digests, revisions, times and closed domain values; exact DFGO application ID
  and schema fingerprint; eight `STRICT` tables; one verified writer and four
  verified readers; literal `BEGIN IMMEDIATE`; bounded invalidations; and
  concrete project/agent/task/factory/snapshot/watch methods. No migration,
  ORM, repository, operation ledger, event snapshot or public transaction
  callback exists.
- `Create` requires an absent absolute 0600 file. `Open` first validates the
  exact schema, complete durable controls, integrity, foreign keys and
  invalidation continuity inside one unconfigured pinned read-only snapshot;
  only a valid database may open configured RW/WAL pools. Empty, partial,
  foreign, Rust and exact-schema-but-corrupt fixtures are refused without
  changing file hash/inode/size/mode/mtime or directory entries. Configured
  validation is independently pinned.
- Creation replay uses caller-generated identity and immutable intent, not a
  later daemon timestamp. Snapshot and Watch validate private control fields in
  the same SQLite snapshot as their bounded safe projection, without exposing
  roots, bodies, results, model/guidance, credentials, Change paths or process
  identities. Retention is exactly 4,096 and watch batches are at most 256.
- The fresh Change row keeps selected commit identity separate from its
  base-bound commitment, stores `entry_count` (not file count) bounded to
  10,000 and total blob bytes bounded to 1 GiB. Additional depth/path/component
  and per-blob bounds belong to the concrete Change validator rather than
  duplicated schema columns.
- The first independent review reproduced four blockers outside the package:
  later-clock replay conflict, WAL mutation before corrupt-home refusal,
  Snapshot accepting hidden corruption, and mixed-snapshot false corruption
  during concurrent Open. The repaired exact head passed a 300-open/2,000-write
  stress three times and under race; the reviewer returned ALLOW.
- Author and reviewer full/race/vet/module/format/cross-build gates passed. On
  the unchanged integrated head the orchestrator ran the full CGO-free suite
  (`kernel 4.293s`, process proof `0.323s`, SQLite proof `10.958s`) and full
  race suite (`kernel 97.283s`, process proof `6.890s`, SQLite proof `13.773s`),
  plus vet/module/format/diff and clean process/temp censuses.
- `internal/sqlitecontract` remains deliberately temporary. It cannot be
  deleted until its injected begin/commit/rollback ambiguity, crash/reopen and
  exact busy proofs exercise concrete domain methods and natural reconciliation
  without replay.

Change commitment/materialization proof on integrated head `42056bd`:

- Added one immutable base-bound `ManifestCommitmentV1`: domain prefix/version,
  closed SHA-1/SHA-256 format tag, raw selected base OID, total entry/blob-byte
  counts, and raw-byte-sorted path/mode/size/blob-OID entries, hashed with
  SHA-256. Materialization rehashes exact bytes using Git's blob encoding and
  reconstructs the same commitment without Git metadata.
- Admission bounds are 10,000 total files plus implied directories, depth 64,
  1,023-byte relative paths on Darwin, 255-byte components, 256 MiB per blob
  and 1 GiB total blobs. Invalid UTF-8 is refused before effect because native
  Darwin filesystem calls reject it; valid composed/decomposed UTF-8 remains
  byte-distinct.
- The concrete Darwin lifecycle is caller-declared `Prepare`, durable identity
  bind, explicit single-use `PopulateAndPublish`, then no-replace
  `RenameatxNp`. It uses dirfd-relative no-follow operations, exact owner/mode/
  special-bit/device/inode/link checks, file and bottom-up directory fsync, two
  complete scans, and final cancellation/root checks immediately before
  publication. Existing targets are never removed or replaced.
- `ParseCommitment`, signed-SQLite-compatible `NewStageIdentity`,
  `InspectPublished` and identity-guarded `RemoveRecordedTree` let recovery
  reconstruct authority rather than duplicate the scanner. Inspection stops at
  the first over-limit observation; removal deliberately ignores admission
  bounds so a provider-expanded retained tree can converge with constant
  memory/descriptor ownership. Tests remove 10,017 siblings and depth 96; the
  traversal uses at most three transient descriptors in addition to its stable
  parent/root descriptors.
- Independent review required three BLOCK/fix cycles: unregistered random
  staging/unbounded depth/missing inspection, then nonconstructible persisted
  identity/unbounded directory reads/misplaced rename proof, then exact bounded
  recovery verification. Final exact-head review returned ALLOW and judged the
  added recovery/lifecycle code proportional; no VFS, walker framework, Git
  library, archive/worktree or portability abstraction was introduced.
- Mutations killed include omitting base/format, display-order sorting, unsafe
  modes, trusting blob OIDs, replacing rename, following intermediate symlinks,
  deleting on uncertainty, hidden/random staging, losing a post-mkdir locator,
  directory-excluding counts, missing depth/root/context checks, trusting
  inspection facts, invalid recovered identity, draining an oversized
  directory, depth-held FDs, admission-capped cleanup and an early rename hook.
  All mutation code was removed.
- Author/reviewer focused, full, race, vet/module/format/diff, cross-build and
  resource gates passed. After unchanged integration the orchestrator ran
  Change count-three (`2.916s`), Change race (`2.267s`), and the full CGO-free
  suite (`Change 1.084s`, kernel `4.089s`, process proof `0.752s`, SQLite proof
  `10.693s`) successfully.
- This package proves structural two-phase ordering, not the durable Store
  commits around it. The Store/change-worker E2E must still prove locator
  commit, Prepare, identity-bind commit, population/publish and
  available-before-provider-exec. Cooperative same-UID mutation, power loss,
  Git selection and stable verification remain separate explicit proofs.
