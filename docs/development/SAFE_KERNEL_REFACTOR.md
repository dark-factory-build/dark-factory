# Safe kernel contract and boot proof

Live use remains frozen until an independent exact-head boot review passes.

This document records the enduring kernel contract and the causal proof needed
for that review. It is not a merge diary. GitHub owns issue and pull-request
history; [the roadmap](../../ROADMAP.md) owns work after the boot decision.

## Go hard-cutover planning authority

The Rust kernel is deleted; Rust-specific process, crate and queue wording
below is the historical record where it conflicts with the implementation. The
canonical Go contract is [`GO_REWRITE.md`](GO_REWRITE.md). Go implementation
must not copy the deleted per-agent admission loop, the obsolete closed-stdin
provider contract, cache selection or crate graph merely because it appears
here.

## Kernel model

`RunId` is the attempt identity. Queued work has no run and no provider
process. Admission creates one run, one exact bearer, and, for a worker, one
lease on a retained Change.

```text
Task: queued --------> running ----------------------> terminal result
                         |
                         v
Run:  admitted -> running -> finalizing -> terminal
          |          |           |
          |          |           +-- immutable outcome request, no authority
          |          +-- exact bearer authorizes bounded effects
          +-- child may be prepared but cannot exec

Resource: declared -> active -> releasing -> released
                                      \----> unresolved

Change: one retained .git-free writable tree across serial attempts
```

The first durable transition to `finalizing` freezes the requested outcome and
revokes attempt authority. Success, block, failure, cancellation, provider
exit, and spawn failure all converge through that phase. Late observations are
idempotent and cannot replace the first request. A configured verification
failure may refine only proposed success to `failed(unverifiable)`.

Only the restartable daemon finalizer writes `terminal`. It does so after every
ephemeral resource is released and every retained artifact has transferred to
its next durable owner. Cleanup uncertainty leaves the run visibly
`finalizing`; it never becomes an invented success or absence.

## Admission and principals

Planned Go admission is one global cursor-free
`Store.AdmitNext(ctx, keys, at)` immediate transaction. It accepts no AgentID,
task, queue observation or fairness cursor. Every call supplies fresh daemon
IDs, including one unconditional candidate Change ID that is used only for a
selected worker incarnation with no existing canonical Change.

Before either RunID reconciliation or a new decision, the transaction validates
the complete fresh schema image. `internal/kernel/schema.go` and its exact
schema allowlist/constraint tests are the only authority for column names,
SQLite storage classes, scalar bounds, enum sets, nullability, `CHECK`s,
foreign keys and unique indexes; this document does not duplicate those
columns. Shared Go create/read/wire validation owns UTF-8 and NUL rules. One
concrete SQL integrity predicate then covers every
row/relation/control that can occupy capacity or bind active
run/credential/resource/session/Change authority and every structurally queued
assignment/rank/payload and required task/agent/project relationship. Unknown
phases, missing relations, split pairs, invalid IDs/revisions/enums, reversed
timestamps, or malformed queued facts are `ErrCorruptState` before capacity.
After canonical selection, shared value validation rejects malformed UTF-8/NUL
text in the selected task/control; lower-ranked queued prose is not globally
scanned merely to decide capacity. This is not an application admission row
scan or second SQL validation layer.

The same predicate proves invalidation continuity: `head =
next_invalidation_sequence - 1`; an empty log has zero rows, `head = 0` and
`invalidation_floor = 1`; otherwise rows are contiguous from floor through
head, with exact count/minimum/maximum and no more than the retention limit.
**Implementation gate (not yet landed):** shared Go path validation and schema
tests must reject project root `/`; accepted roots are clean absolute paths
with no NUL, empty, `.` or `..` component, repeated separator, or trailing
separator. Provider choice inherently means unrestricted interactive
authority in V1; no permission-profile field exists, and bounded authority is
deferred until causal OS-effect proof. Exact fresh
decision precedence after those checks is `dispatch_disabled`, `at_capacity`,
`queue_empty`, then `no_eligible_work`. Only after every run phase is known may
capacity count exactly the one nonterminal set: admitted, running and finalizing.
Configured capacity is one integer `C` in `[1, 1024]`. One reserved residue
belongs to one nonterminal worker run, so its count is at most `C`; this does
not bound terminal retained-Change aggregate storage or an adversarially
replaced residue's bytes.
Eligibility means task status exactly queued, valid same-project assigned
agent, either known role (`worker` or `orchestrator`), not paused, durable
budget remaining and no conflicting open run. Role determines the footprint,
not external availability; known nonqueued status is outside the queue.
Provider/model/effort/project-verification controls must satisfy the executable
schema allowlist, but external availability
is not eligibility. Installed-version/model compatibility is checked by
provider Build/start after admission and becomes typed `FailureSpawn`/finalizing,
never durable corruption or queue ineligibility. Known-valid paused,
budget-exhausted or open-run-conflicting queued rows are ineligible; corrupt
facts are never ineligibility. The
Store selects globally by priority
descending, creation time ascending and exact 16-byte task-ID SQLite `BLOB`
bytes ascending. It then validates that row's canonical Change: corrupt,
unsettled or hard-invalid state fails closed without skipping to lower work.
Canonical Change corruption is the only Change-specific pre-admission
decision. A successful transaction writes the full task/run/bearer/resource/
session/Change/invalidation footprint and returns only the
committed launch target. Reconcile-only failure is `not_reconciled`.

Repository availability and provider executable/configuration/auth availability
are deliberate post-admission `FailureSource` and `FailureSpawn` outcomes
respectively, never scheduler filters.
A reorder or higher-priority insertion before the transaction changes what is
admitted; a caller's stale observation cannot nominate work. Process-local
locks may serialize effects but never prove durable assignment or authority.

After the outer runner is active, its declared empty provider process/group
pair is a serialization barrier. Generic outcome, cancellation and
infrastructure-failure transactions refuse while the already-prepared runner
performs its sole inner Start even across daemon EOF. Exact Start failure uses
the existing no-child AttemptResult; successful Start binds an inert exact pair
before a pending outcome may reap it. No generic transition produces a
finalizing declared pair, and this rule adds no receipt or lifecycle state.

The bearer resolves to one exact project, agent, task, run, role, provider, and
Change. It authorizes effects only while that run is `running`. Attempt calls
cannot supply or widen any of those identities, and operator authentication
cannot impersonate an attempt for hooks or outcomes.

Relationship checks occur in the same Store transaction as the mutation:

- a worker may message itself, its immediate parent, or its nearest
  orchestrator ancestor;
- an orchestrator may message itself or a strict descendant;
- an orchestrator may create a child of its current task only when assigning
  it to a strict descendant, and may assign queued work only to a strict
  descendant; and
- attempts cannot edit tasks, unassign work, administer agents, or mutate
  factory policy.

Task and run ancestry never substitute for the durable agent hierarchy.

Dispatch and provider authority are separate. `dispatch_enabled` controls only
new admission. Every admitted run freezes its provider and optional model and
effort; provider choice inherently means unrestricted interactive authority in
V1. Changing the provider/model/effort or dispatch later does not rewrite that
run.

A retry creates a new run, bearer, runtime, and provider process. It may reuse
the retained Change only after the preceding run is terminal. It never revives
an old credential, conversation, or process.

## Process and resource ownership

The Rust process model is deleted. Go launches one fresh runner-owned
interactive PTY provider per run with explicit authenticated attach/input
lease. No provider process is reused across runs, and no task body is replayed
after an uncertain write.

Launch uses register-before-exec gates:

1. factoryd records the run, runtime claim, and declared resources;
2. factoryd records an exact locked startup-file identity before an inert
   runner gate can inherit that lock and spawn;
3. factoryd records the gate's stable PID before activation;
4. the runner prepares a provider child blocked before `exec`;
5. factoryd records the provider PID, process group, and birth identity and
   moves the run to `running`; and
6. only then may the same child execute the provider.

The resource ledger owns processes, process groups, runners, runtime roots,
temporary roots, and verification effects. Stored process numbers support
absence checks, not signalling. The authenticated live runner may signal its
provider group only while it owns the unreaped leader child; recovery otherwise
waits for exact absence. Reused or weak identities stay unresolved.

A process group may outlive its leader. If the recorded leader disappears
while descendants remain, its numeric group ID is not signal authority and is
not proof of absence. Finalization, temporary-root cleanup, and cache
remeasurement remain pending until exact group absence is independently
established.

The runner's live-child guard preserves its bounded group authority across
cancellation or unwind and disarms immediately after a successful wait. Its
`Drop`, shell traps, and provider exit handlers may accelerate cleanup; none is
terminal authority.

## Change and repository ownership

For a worker, factoryd reserves one daemon-derived Change path for the exact
task incarnation. A registered parent-bound materializer selects one full
local Git commit, validates its bounded tree, reads exact blob objects, and
atomically publishes a plain writable tree. Partial clones and unsafe paths,
links, gitlinks, modes, or attribute-transformed export behavior are refused.

The provider view contains no `.git` administrative locator. Ordinary
repository discovery and linked-worktree creation fail. The product exposes no
status, commit, push, pull-request, or publication operation. Retries lease the
same retained Change serially; automatic storage reclamation never targets
unique retained work.

Managed removal requires an explicit operator request, no live lease, and the
exact recorded identity and revision. Replacement or ambiguity remains a
visible recoverable failure and is never touched. Source paths retained from
pre-kernel databases are metadata-only quarantine: factoryd does not inspect,
adopt, measure, launch from, rename, or delete them.

## Verification and regenerable storage

A project selects `None` or the fixed `RustWorkspaceTest` completion policy.
There is no generic provider-visible build API. For a Rust completion,
factoryd first moves the run to `finalizing`, revokes its bearer, and reaps all
provider resources. It then publishes a stable scan/copy/scan snapshot of the
Change or fails unverifiable.

Compilation uses one mutable cache keyed by project incarnation, exact Cargo
and rustc identity, target, and fixed policy. Source revision is provenance,
not a cache-namespace dimension. Cargo's top-level test executables are copied
into attempt-owned staging with a manifest binding source, build
configuration, toolchain, identities, and digests. The daemon re-verifies each
copy before launch and rechecks the stable source snapshot before and after
each test. Mutable Cargo output is never a top-level launch target.

The verifier is one registered effect with bounded output and time. Shutdown
publishes a private finish marker; a healthy live verifier kills its own group.
Recovery consumes only an atomically published result after exact group absence
and never rebuilds against later Change contents. The same leader-loss rule
above prevents premature completion or cache handoff.

Cache count is checked before effects. A writer makes measured bytes
incomplete. After its exact effect is absent, factoryd remeasures allocated
bytes and reclaims only identity-matched, unleased regenerable caches. A known
over-limit cache cannot be claimed for another run. Status reports measured
bytes, protected entry count, and recoverable failures without claiming an
instantaneous filesystem byte ceiling.

These identity and digest checks protect against mistaken and cooperative
substitution. Provider and test code still run as the operator; hostile
same-user isolation requires a separate OS user, container, or sandbox.

## External input boundary

There is no HTTP webhook or generic connector listener. Work enters only
through the authenticated private local API. A future integration must first
store an authenticated, bounded input envelope as a non-executable candidate,
then cross an explicit quarantine and acceptance boundary before it can create
or message work. External payloads must never materialize executable tasks
directly.

## Deleted Rust evidence (historical for Go)

The old Rust package/crate graph and TUI are deleted; they survive only in git
history. They do not define a five-crate Go target and must not be preserved by
compatibility code. The Go target uses one fresh schema and protocol with no
migration chain, upcaster, or compatibility layer. Do not add an ORM, actor
framework, generic saga, event-sourcing framework, micro-packages, or
repository/service traits with one implementation. Split a large module only
when surviving code has distinct owners; prefer deletion over relocating
obsolete authority.

## Causal proof matrix

Each proof must exercise an external effect or durable observation, not only a
callback or row:

| Proof | Required boundaries and assertions |
| --- | --- |
| Crash and restart | Inject failure after admission, resource declaration, each blocked-exec release, provider exit, external cleanup, and before acknowledgement. Restart yields at most one provider execution, no input replay, exact identity, and idempotent convergence. |
| Taskless refusal | With no admitted run, no provider exists. Old, forged, taskless, finalizing, and terminal credentials cannot mutate task, budget, source, or outcome state. |
| Queue race | Race multiple agents, insert higher-priority work after stale observation, and exercise exact reason precedence, SQL eligibility and priority/time/16-byte-BLOB ties. Put an admitted setup-stalled run in the last slot and prove admitted/running/finalizing are the one capacity set. Corrupt each run/credential/resource/session/Change authority class and each queued rank/payload/relation/control above and below valid work; the concrete integrity predicate blocks all admission before capacity. Corrupt/unsettled canonical work never falls through; caller AgentID/task/cursor and external-availability filters cannot affect selection. |
| Hierarchy scope | Construct alternate durable agent hierarchies and attempt cross-agent messaging, creation, and assignment. The same mutation transaction uses the stored hierarchy and refuses siblings, ancestors outside the worker allowance, and non-descendants. |
| Outcome and exit race | Exercise request-before-exit and exit-before-request. The first `finalizing` request wins; only failed configured verification may refine proposed success. |
| Completion ordering | After a success proposal, further attempt mutation and successor admission are refused until provider reap, exact snapshot and configured verification, release of every resource, and terminalization. |
| Failure, cancel, and retry | Spawn failure, provider crash, and cancellation converge through finalization. Retry is refused before terminal and creates a fresh run and bearer afterward. |
| External finalization | Kill provider, runner, verifier, and daemon at separate cuts. Recovery touches only exact resources. Leader loss with live descendants remains nonterminal past restart and converges only after exact group absence. The operator launchd service is outside attempt ownership, is not a `KernelResource`, and is proven separately by a release fixture. |
| Source boundary | Materialize one exact commit; repository discovery and ordinary worktree creation fail in the provider view. Removal refuses a replacement identity. |
| Immutable source and launch | Concurrent Change mutation yields one canonical snapshot or fails before compilation. Replacing or tampering with prepared test output cannot change what launches. |
| Cache reuse and Change storage | Two revisions reuse one project/toolchain cache while producing distinct source-bound manifests. Cache count and measured-byte pressure refuse or reclaim only regenerable entries. Nonterminal capacity bounds the count of reserved residues; accepted trees meet entry/byte/depth limits. Terminal retained-Change aggregate retention and a same-UID-replaced reserved stage's bytes remain explicit cutover gates, not invented admission authority. |
| Orchestrator policy only | Orchestrators remain subject to hierarchy scope and cannot direct-launch, publish repositories, submit another attempt's outcome, mutate capacity or budgets, or invoke operator control. |
| Dispatch and provider authority | Disabled dispatch admits nothing. An admitted run retains its frozen provider/model/effort across agent edits and dispatch changes. No permission-profile field or value may appear in the fresh schema, Store model, or wire contract; bounded provider authority is deferred until its causal OS-effect proof. |

Process tests use a temporary `DARK_FACTORY_HOME`, explicit private socket,
unique disposable paths and labels, deterministic providers, and two
unconditional reaping mechanisms for any descendant a fixture deliberately
leaves alive past its leader: a `Drop` guard that fires on the pass, fail, or
panic path, and a hard iteration cap on any backgrounded shell wait that
reclaims the descendant on its own even if the test process is killed
outright. They never address the operator home, socket, job, credentials, or
paid provider subscription.

## Exact-head boot gate

The independent reviewer must inspect one exact integration-target commit and
confirm:

- the run phase/outcome contract is the only work and terminalization
  authority;
- no provider exists or mutates without one exact running attempt;
- planned Go queue selection is one global cursor-free immediate transaction
  with exact eligibility/reason/BLOB ordering and no caller nomination, while
  hierarchy authorization remains transactional;
- provider choice is separate from dispatch and frozen at admission; V1 provider authority is unrestricted interactive authority;
- crashes, identity reuse, and verifier leader loss cannot cause replay,
  unsafe cleanup, or premature terminalization;
- factoryd alone creates and removes Changes, and provider views expose no Git
  administrative or publication surface;
- completion verification launches only exact staged artifacts from a stable
  source snapshot and storage convergence touches only regenerable data;
- external executable intake remains absent;
- the causal matrix and authoritative local and hosted gates pass on that same
  commit; and
- surviving compatibility and complexity have been challenged for deletion.

The review records its exact base/head, commands, results, unverified lanes,
and explicit allow or block decision. Passing it does not install, release,
start, enable dispatch, or modify the live operator system; each remains a
separate operator action.
