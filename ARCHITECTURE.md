# Architecture

Dark Factory separates model policy from durable work authority. This file
describes the Go target's attempt kernel, daemon-owned Change model, and
fail-closed completion-verification boundary. It is a contract, not a component
catalogue.

## Current status

Live use remains frozen until an independent exact-head boot review passes.

The integration target at `359d46a3` contains production `factoryd`
composition and ownership of `OperationalHome`, `Store`, `RuntimeParent`, the
Local API, and browser services. This documentation candidate is not that
shipped integration: corrected Change/global-admission/provider integration
remains pending. The shell package at `1ff2e2e6` is a separate unintegrated
candidate, not canonical or shipped; Claude and Codex remain blocked.

The complete design and causal proof matrix live in
[`docs/development/SAFE_KERNEL_REFACTOR.md`](docs/development/SAFE_KERNEL_REFACTOR.md).

### Go hard-cutover planning authority

The retained Rust kernel is historical evidence only. It is not a migration,
compatibility, or implementation contract for the replacement. The canonical
planned Go contract is
[`docs/development/GO_REWRITE.md`](docs/development/GO_REWRITE.md); when this
file's retained-Rust historical wording conflicts with that record, the Go
rewrite record wins and the old wording must not guide implementation.

In particular, planned Go admission is one cursor-free global
`Store.AdmitNext(ctx, keys, at)` `BEGIN IMMEDIATE`. No caller nominates an agent,
task or queue observation. The Store applies durable eligibility and exact
reason precedence. Before selection it validates global settings and runs one
concrete SQL integrity predicate over every row/relation/control that can
occupy capacity or bind active authority, plus every structurally queued rank,
payload, assignment and exact task/agent/project fields. This proof runs before
either RunID reconciliation or a fresh decision. Unknown phases, missing
relations, split resource pairs, invalid IDs/revisions/enums, malformed rank or
payload, and other damaged facts anywhere block all admission rather than
becoming ineligibility. Only after that proof may capacity count the single set
of all nonterminal runs: admitted, running and finalizing.
`internal/kernel/schema.go` and its exact schema allowlist/constraint tests are
the only field/domain authority for the fresh schema; this document does not
duplicate their columns. The cross-row predicate covers the relations and
phase facts needed by admission, including bounded invalidation continuity.
The fresh schema has no profile row, agent or project status field; agent
`paused` is the availability control.
Provider choice inherently means unrestricted interactive authority in V1;
there is no permission-profile field, type, column, or wire value.
This file does not define a second or extensible control validator.
Configured capacity is one integer `C` in `[1, 1024]`; a reserved Change residue
belongs to one nonterminal worker run, so its count is at most `C`. Terminal
retained-Change aggregate retention and adversarial residue bytes remain
explicit cutover gates, not admission policy.
The Store then orders all eligible task+agent rows by priority
descending, creation time ascending and exact 16-byte task-ID `BLOB` bytes
ascending. It validates the selected row's one canonical Change and never skips
a corrupt, unsettled or hard-invalid higher-ranked row for lower work. Fresh
no-admission precedence is `dispatch_disabled`, `at_capacity`, `queue_empty`,
`no_eligible_work`. Known-valid paused,
budget-exhausted or open-run-conflicting queued work is ineligible; either
known role remains eligible and determines the footprint, while a known
nonqueued task is outside the queue. Unknown or malformed durable control is
corruption. Repository and provider executable/configuration/auth availability
are post-admission typed failures, not stale eligibility filters.
The exact contract lives in the rewrite record; this paragraph does not claim
the corrected Change/global-admission/provider contract is implemented in this
docs candidate.

Planned Go process setup has one additional literal barrier: after the outer
runner is active and while its provider process/group pair remains declared
with empty identity, generic outcome, cancellation and infrastructure-failure
transactions refuse. The already-prepared one-shot runner still makes exactly
one inner Start on cancellation or daemon EOF. Exact Start failure publishes
the existing no-child result; successful Start binds an inert child before a
pending outcome may reap it. No generic outcome creates a finalizing run with a
declared provider pair, and no new receipt or lifecycle state is implied here.

## Durable model

`RunId` is the attempt identity. A task can be queued without a run; a run
exists only after admission.

```text
Task: queued --------> running ----------------------> terminal result
                         |
                         v
Run:  admitted -> running -> finalizing -> terminal
          |          |           |
          |          |           +-- immutable outcome request, no authority
          |          +-- exact attempt bearer authorizes bounded effects
          +-- child may be prepared but cannot exec

Resource: declared -> active -> releasing -> released
                                      \----> unresolved
```

The run outcome is distinct from its phase: succeeded, blocked with a reason,
failed with a typed reason, or cancelled with a reason. The first durable move
to `finalizing` freezes its requested outcome. Later completion, block, cancel,
or exit observations are idempotent and cannot replace that request. The
`finalizing` projection exposes that proposal; the terminal projection exposes
the actual result, while append-only events preserve both. For a configured
Rust check, failed verification is the one documented refinement: it converts
proposed success into `failed(unverifiable)` rather than claiming success.

Only the finalizer writes `terminal`. It may do so only when every ephemeral
resource is released and every retained artifact is durably transferred to
its next owner. Cleanup failure leaves the run visibly `finalizing`; it never
pretends the resource disappeared or rewrites the outcome.

## Authority invariants

1. SQLite is the sole durable authority. State mutations and their bounded
   events commit together. Process-local locks serialize work but never prove
   ownership.
2. Every provider-mediated mutation requires one bearer credential for one
   exact `running` run. Authentication derives project, agent, task, run, role,
   provider, and any Change scope from the store.
3. Anonymous local requests may ask only for health. Operator requests require
   the private operator credential. Attempt credentials are valid only while
   the exact run is `running`; admission, `finalizing`, and `terminal` do not
   grant effect authority.
4. Request authorization is exhaustive and fail-closed. A worker may message
   itself, its immediate parent, or its nearest orchestrator ancestor. An
   orchestrator may message itself or
   a strict descendant, create a child of its current task only when assigning
   it to a strict descendant, and assign queued work only to a strict
   descendant; attempt authority cannot edit or unassign tasks. These
   relationship checks rederive the exact running run and `agents.parent_agent_id`
   inside the same immediate Store transaction as the mutation. Task and run
   ancestry never grant agent authority. Operator authority cannot be used as
   an attempt identity for completion, blocking, or hooks.
5. Admission is the only transition from queued work to an attempt. For planned
   Go, one global immediate Store transaction accepts no caller-selected agent,
   task or cursor; it validates global settings and every capacity/authority and
   queued rank/payload/control fact through the concrete SQL integrity
   predicate, then counts admitted plus running plus finalizing against capacity
   and checks dispatch/eligibility,
   selects the canonical task+agent by global priority/time/16-byte-BLOB-ID
   order, validates its one
   Change, derives the provider launch target, and binds the immutable task
   incarnation/work revision before external effects. The retained Rust
   per-agent queue-head implementation is historical only. The factory-wide
   dispatch switch controls only whether this transaction may admit new work;
   changing an agent's provider/model or disabling dispatch cannot rewrite an
   admitted run's launch authority.
6. No admitted attempt means no provider process, tool hook, outcome request,
   or writable source lease.
7. A retry creates a new run and new bearer. It never revives an old process
   or credential.
8. External-input receipt is separate from work authority. An operator-only
   transaction may create an immutable `InputEnvelope`, one quarantined
   `WorkCandidate`, and bounded events. It cannot create a Task, Message, Run,
   Change, provider prompt, ProposedAction, or scheduling event. A changed
   source revision advances an exact expected-current pointer and stales an old
   quarantined candidate atomically. A rejected predecessor keeps its durable
   decision while losing current-source authority. Exact observation or
   rejection replay has no second effect, even after that pointer advances.
   Candidate snapshots derive `is_current` from that pointer so reconciliation
   can recover exact causal authority after restart.

## Browser HumanRequest authority

The pre-release Go runtime keeps HumanRequest authority in SQLite and exposes
only bounded, correlated browser operations. Public HumanRequest state contains
the request/project/agent/task relationships, chronology, revision, kind,
status, the fixed reply bound, and display-only `can_reply`. It contains no run
locator, terminal locator, question, reply, cancel descriptor, process identity,
or other private source data.

Private detail is one pinned SQLite read. A client with
`private_human_request_detail` may receive the exact hostile question and, only
for an open request whose originating run is running with its exact active
terminal session, an observation-only terminal target. Before minting that
target, the same snapshot validates the canonical task assignment, resource
topology and identities, run/resource/session chronology, and requires the
validated terminal session to equal the selected active session.
`human_actions` is checked independently: without it, detail may contain that
target but cannot advertise reply or cancellation. With it, the same snapshot
may mint the one concrete cancellation descriptor containing exact request and
run revisions. Delivering, delivery-unknown, finalizing, terminal, missing, and
non-active origins expose no reply or cancellation authority; corrupt active
relationships fail closed rather than resembling unavailability.

A reply request contains only request ID, expected request revision, and bounded
reply text. The Store transaction reloads the client and request, derives the
originating running run, and commits a unique delivery receipt before the daemon
looks up that exact live owner or writes once to its PTY. Failure or uncertainty
after reservation becomes `delivery_unknown` and is never replayed. Cancellation
likewise contains no caller-selected run: one Store transaction derives the
origin, checks exact request/run revisions and capability, enters finalizing,
revokes attempt and terminal-input authority, resolves the request, and appends
all invalidations. The concrete response may report the server-derived run and
post-transition revisions; it is result metadata, never caller authority.
After that durable commit, the exact live attempt synchronously inspects its
current terminal binding under the shared operation ordering. If one exists it
revokes that binding's actual generation regardless of which authorized client
submitted cancellation; no binding is definitive success. A rejected, partial,
uncertain, or controller-failed fence is returned as post-commit uncertainty
without rollback or retry against another run.

## Process and resource ownership

The retained Rust process model is historical evidence only and is deleted at
the Go cutover. Planned Go launches one fresh runner-owned interactive PTY with
explicit authenticated attach/input authority; no provider process is reused
across runs.

Launch is one nested register-before-exec handshake:

1. `factoryd` records the admitted run with a random runtime claim. The
   claim-derived path is durable before `mkdir`; its inode replaces the claim
   before any credential, configuration, or process is created inside it.
2. `factoryd` creates and locks a private startup file, persists its exact
   filesystem identity, then maps that lock to the inert runner gate's stdin.
   A restart can prove that a gate spawned before PID registration is gone only
   by acquiring the same lock; missing or replaced identities stay unresolved.
3. `factoryd` persists the inert gate's stable PID, then activates that same
   PID into `factory-runner`. The runner creates a second parent-bound child
   gate before provider `exec`
   and reports the stable provider PID and process group.
4. `factoryd` persists those identities and moves the run to `running`.
5. The runner releases the child to provider `exec`.

If preparation or activation fails, the run enters `finalizing`; a provider
must never execute first and become durable later. The runner is a
provider-blind effect host, not a second lifecycle owner.

The resource ledger records process, process group, runner, runtime root, and
other external effects before use. Each record contains enough identity to
refuse PID, path, or job-label reuse, but stored numeric identities never grant
signal authority. The daemon requests shutdown through the authenticated live
runner; the runner may signal its provider group only while it still owns the
unreaped leader child. A live-child guard preserves that authority across
runner cancellation or unwind, then disarms immediately after a successful
wait; it never authorizes terminalization. After leader or runner loss, the
durable finalizer only observes exact absence. A live, reused, or weak identity
remains unresolved and cannot authorize signalling, runtime removal, or
terminalization.

## Provider boundary

The planned Go boundary is one exhaustive concrete
`internal/provider.Build(Request) (Launch, error)` function. It returns only
one exact absolute executable, ordered argv and complete ordered environment.
The runner owns the descriptor-bound Change cwd, fresh interactive PTY, input,
process group, wait/reap, output and cleanup. The provider cannot select a
source path, authority, credential, lifecycle result or fallback. See [the
fresh provider contract](docs/providers.md).

The V1 contract allows only unrestricted interactive authority. Shell is
exactly `/bin/sh -s`; Claude Code and Codex are blocked in this candidate
pending exact integration and fake-witness review. The schema and wire contract
contain no permission profile or bounded-authority field. Admission freezes
provider, optional model, and optional reasoning effort only. After admission,
`Build` resolves and commits the exact native executable/configuration/auth
facts from daemon-sealed sources, then immediately before release revalidates
those facts and the final Change/config identity; these are not admission
schema fields. An unsupported mapping or missing, changed, or inaccessible
executable/configuration/auth is typed post-admission `FailureSpawn`, never
queue ineligibility. Whole-provider API/model network access is not claimed to
be constrained by this command contract.

The runner starts with `env_clear` and one closed ordered environment builder,
private per-run roots, a daemon-sealed `PATH`, and no ambient provider/API,
proxy, Git/GitHub, SSH, loader or plugin variables. It writes the canonical
task body exactly once to the PTY after both gates, with one provider-specific
terminator; the body is never in argv, env or replay. Auth is a copy-only
sealed file or metadata-only Keychain reference. Provider output is opaque and
never lifecycle authority. Whole-provider API/model network access is not
claimed to be constrained by this command contract.

The generic runner exports `DARK_FACTORY_ATTEMPT_TOKEN_FILE` as the path to the
private bearer file. It does not export the bearer value. When that variable is
present, `factoryctl` authenticates every request with the ambient attempt
credential, including commands whose shape is normally operator-only. The
daemon then rejects commands outside the attempt allowlist; the client never
falls back to the operator token.

Provider output is opaque. Hooks are authenticated observations and bounded
requests, not lifecycle authority. The daemon never infers success from text.

## Source ownership

For a worker, admission atomically reserves one Change ID and one daemon-derived
path for the exact task incarnation. A registered, parent-bound wrapper then
selects one full local Git commit, records the repository and staging inode,
reads its bounded manifest and exact blob OIDs through `git cat-file`, and
atomically publishes the resulting safe tree. It does not use `git archive`,
so repository-local export attributes cannot transform committed bytes.
Partial clones are refused before object reads, and lazy promisor fetch is
disabled: the selected commit must already be wholly local. The real provider
replaces that same registered process only after SQLite records the Change as
`available`.

The provider sees a plain writable source tree with no `.git` locator. Git
repository discovery and linked-worktree creation are refused by construction
and by the sanitized environment: the discovery ceiling names the Change root's
*parent*, because Git stops the upward walk only when it would climb into a
listed directory, so naming the Change root itself would still let an ancestor
repository be found from that root. Retries reuse the same retained Change;
deletion is an explicit identity- and revision-checked transition that is
refused while an attempt leases it. Factoryd supplies no status, commit, push,
pull-request, or publication operation.

Pre-kernel source paths live only in `legacy_sources`. They are quarantine
metadata, not Changes: factoryd never touches the recorded filesystem path and
can only forget the metadata row by typed ID.

## Build and storage boundary

Each project has one operator-selected verification policy: `None` or one fixed
`RustWorkspaceTest`. There is no provider-visible generic build operation or
Cargo shim. For a Rust-policy worker, `factoryctl task done` is the single
completion boundary: the daemon moves the run to `finalizing`, revokes its
authority, asks the live runner to reap the provider process group, and only
after exact resource absence snapshots source and starts verification. A lost
runner leaves finalization pending; stored PIDs are not a fallback. Orchestrator
runs are not verified this way.

The source snapshot is a canonical scan/copy/scan of the plain Change;
it is published only when the manifests agree. This deliberately replaces the
earlier private-Git-index and in-flight-writer design: Changes contain no Git
administration, and a hook has no trustworthy `PostToolUse` writer ledger.

Rust verification uses one mutable cache per random project incarnation and
fixed Cargo/rustc identity and configuration, not per Change or source
revision. It compiles only the private snapshot, copies the top-level Cargo
test executables into a content-addressed directory under the run's registered
temporary root, verifies its manifest/identity/digest, and launches those
copies. The stable snapshot is the test working directory and is rechecked
before and after every top-level test; a mutation fails verification before a
later test can launch. Fixtures are not copied into the executable directory,
doctests are not run, and test code may still launch other same-UID processes.
Mutable `target/debug` or
`target/release` top-level launch is forbidden. These checks prevent confused
or cooperative replacement; they are not a sandbox against hostile same-UID
code.

Verifier recovery retains the same ownership rule as provider recovery. If a
process-group leader disappears while descendants remain, the run stays
`finalizing`; a numeric process-group ID without the exact live leader identity
is neither signal authority nor proof that the effect is gone.

Regenerable cache storage has a hard entry count and a measured byte policy.
Starting a writer makes byte status incomplete; after its exact process group
is absent, factoryd remeasures allocated bytes and reclaims unprotected caches
until the policy converges. Healthy verifier shutdown is cooperative: factoryd
publishes a private finish marker and the live leader terminates its own group.
A measured over-limit cache cannot be claimed for
another verification. Status reports aggregate measured bytes, protected entry
count, and recoverable failure count, not an invented protected-byte subtotal.
An ordinary directory
cannot promise a portable instantaneous byte ceiling while Cargo is writing,
so the architecture does not claim one.

## Policy versus correctness

God/orchestrators schedule and prioritize through ordinary authenticated
requests. Factoryd independently checks project scope, task state, capacity,
budget, durable Change policy, and admission. Planned Go treats external
repository/provider availability as a typed post-admission failure, not a
scheduler filter. An orchestrator cannot create
Changes, launch processes directly, mutate capacity or agents, choose an
outcome for another attempt, or finalize a run. Its death cannot prevent the
daemon finalizer from converging.

## Clients and integrations

`factoryctl` and the hosted browser are disposable clients of one local API.
They do not own runtime state. Both use the operator credential for operator
requests.
Generated provider hooks and attempt commands read the private credential file
for their exact run through `DARK_FACTORY_ATTEMPT_TOKEN_FILE` (or an explicit
hook `--token-file`). A provider-invoked `factoryctl` process cannot cross into
operator authority by choosing an operator command.

`factoryd` has no HTTP webhook, GitHub adapter, or generic connector intake.
The separately deployed control plane may authenticate external deliveries,
but it has no daemon or attempt authority. The operator may place bounded
provider-neutral observations into inert quarantine through the authenticated
private local API, then list, inspect, or reject them. Raw content is private
local detail and public events carry only project, envelope, candidate, and
status identities. There is no accept/materialize operation, so receipt cannot
become executable work or bypass admission.

The separately deployed control plane is outside this local architecture and
has no daemon or attempt authority. Its implementation and broker choice do
not load credentials into `factoryd`; any future integration must preserve the
typed, metadata-only boundary. Product intake and the operator/browser API
remain separate planes, and no external delivery can bypass local admission.

## State outside SQLite

Bounded project guidance, rules, and memory remain files under the factory
home. SQLite owns their identities; their prose is not authority. Cross-file
and database operations must state their ordering and failure semantics rather
than pretending to be one transaction.

The local socket, credential files, runtime roots, and generated provider
configuration are private daemon-owned files. The live operator home and
launchd job are never test fixtures.

## Deliberate non-goals

- No new crate, ORM, actor framework, repository/service trait with one
  implementation, generic saga, or event-sourcing framework.
- No protection from a hostile process running as the operator. Bearer scoping
  prevents confused/cooperative cross-attempt behavior; real isolation needs a
  separate OS user, container, or sandbox.
- No migration, upcaster, or compatibility layer; the Go home/schema and
  protocol are fresh, and retained Rust history is an archive only.
- No live installation, release, or external intake before the independent
  exact-head boot review and a separate operator decision.
