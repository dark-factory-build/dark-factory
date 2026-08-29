# Go local-runtime hard cutover

This is the canonical design, proof plan, and permanent record for replacing
Dark Factory's local Rust runtime with Go. It is organized around product
authority and external effects, not around the existing Rust crates. Git
history remains the archive; the Go runtime will not migrate a Rust home,
schema, event log, protocol, or serialized state.

## Web-first redirection (authoritative from 2026-08-26)

This section supersedes every later statement that requires a Go TUI, Bubble
Tea, CLI/TUI parity, closed-stdin provider input, or the Rust attention
model. The later sections remain the chronological implementation record and
historical evidence for earlier kernel work. They are not authority when they
conflict with this redirection, and an old “integrated head” entry does not
make corrected Change, global admission, or provider integration shipped. The
final elegance pass must remove superseded prose after its replacement tests
are green.

The target product is a local Go daemon with durable authority and PTY-backed
agents, controlled primarily by a hosted responsive web application. The
production Go daemon is not yet shipped: this document distinguishes package
proof, in-process composition proof, and installed-runtime proof throughout.
There is no Go `factory-tui`. `factoryctl` remains the bootstrap, service,
recovery, diagnostic, automation, and browser-pairing client; it is not a
second primary operator interface and does not owe visual feature parity.

The hard-cutover decision is unchanged: fresh Go home, schema and protocols;
no Rust migration, event upcasting, mixed runtime, or compatibility period.
The existing Rust TUI and other replaced Rust local-runtime crates are deleted
only after the revised web/PTY gate passes.

### Canonical integration status

The reviewed documentation contract through `276f9468` was manually merged
with the canonical implementation at `359d46a3`; `bc48df7f` is the resulting
integration commit. The corrected canonical record at `51fae159` received a
fresh independent exact-head **ALLOW**. That review establishes the contract
and status record only; it is not cutover authorization.

The canonical implementation includes production `factoryd` composition and
ownership of `OperationalHome`, `Store`, `RuntimeParent`, the Local API, and
browser services. `RuntimeParent` was independently allowed through
`7464e02a` and `15879fe2`; the composition fixes at `9ffc13e3` and `bcfdb44b`
were independently allowed; and the obsolete `ExecutionMode` dimension was
deleted through `24aaccd1` and `359d46a3`, also with independent ALLOW.

Corrected Change settlement and retained-Change authority are canonical
through `eb70d9a9` and received independent exact-head **ALLOW**, including a
mutation that proves retained retries no longer require live `.git` authority.
Global Store-owned admission is canonical through `67a5e96f` and received
independent exact-head **ALLOW** after its integration compile repair,
different-agent last-capacity causal test, race proof, and a mutation that
moved capacity observation outside `BEGIN IMMEDIATE` and was killed. Read-only
launchd service status is canonical
through `960f55d5` and received independent exact-head **ALLOW** after three
rounds of fail-closed race attacks. The Shell provider boundary is canonical
through `45dcfee5`: the exact task reaches the provider only through an
unlinked read-only descriptor, PTY bytes remain exclusively interactive, the
worker config pathname is removed and durably unlinked before provider exec,
and recovery requires the causal inner-activation marker before accepting
post-consumption config absence. Fresh independent review **BLOCKED** two
earlier recovery grammars, both were repaired, and then returned exact-head
**ALLOW** for the source and canonical composition. Claude and Codex remain
unavailable until deterministic native launch witnesses are reviewed. Release
candidate `43723780` is historical focused-gate evidence only and must be
replayed after recovery and production scheduling are composed.

One joined global scheduler is canonical but deliberately dormant through
`afa63f36`. Independent review returned **ALLOW** after mutations proved the
production admission callback, one held unobserved probe under wake/poll
races, bounded idle behavior, single ownership and durable terminal rereads.
It has no `factoryd` call site; a finite synchronous startup recovery pass must
land before activation. Exact runner-start/AttemptResult Store grammar remains
in proof in `.worktrees/attempt-result-kernel`. In the private site repository,
real hosted-origin pairing/state/refresh against canonical runtime `45dcfee5`
is repaired through exact commit `5f3eea29`. An independent review of that
exact head returned **ALLOW** after attacking the four earlier blockers: the
Go binaries now build only from a private `git archive` of the requested clean
commit, installed client/UI trees are byte-for-byte and inventory checked
against reviewed tarballs before and after the Next build, browser close is
bounded without blocking daemon/site cleanup, and the pairing challenge is
permitted only in its correlated `PAIR_PROVE` frame. The focused independent
rerun passed 16/16 tests and `git diff --check`; the author's full gate passed
69/69 Vitest tests, 18 Playwright tests with two deliberate skips, production
build/server/artifact gates, and the real hosted-origin loop with a clean
process/port/root census. This remains bootstrap evidence, not yet the full
provider terminal and HumanRequest product loop.

The local threat boundary assumes the lifetime-locked operational home and its
single daemon-owned Store are the only SQLite writers. Arbitrary same-EUID raw
SQLite corruption after the final guarded transaction is not defended: that
principal can also replace runtime files or signal/inspect owned processes. A
disposable review attack demonstrated that such corruption can change a bound
Change fact after the final Store read; this is retained as an explicit
residual, not represented as a supported mutation race.

### Redirection starting point and branch inventory

The redirection began from clean canonical Go head
`7eb417660fd189b8fa412e8e64215c7e6e7b3f90` in worktree
`.worktrees/go-hard-cutover`. The only worktree modification while this section
was written is this document. No branch was published or merged, no Go TUI
implementation exists, and no live installation, daemon, socket, provider or
credential was touched.

The private sister repository `/Users/baziyer/dark-factory-site` exists at
`cfef147717ed1c166944bc66a8780ac95a54f7d6`. Its current main worktree contains
pre-existing uncommitted marketing/wordmark/test assets; they belong to the
operator and must not be overwritten. Future site integration uses a separate
branch/worktree and obeys that repository's `AGENTS.md`.

In the table below, “atomic admission” includes the cursor-free global Store
selection now integrated in the canonical branch.

| State | Exact work retained or stopped |
|---|---|
| Complete and retained | Fresh SQLite contract; typed kernel; atomic admission; exact attempt credentials; run/finalizing/resource state; bounded invalidations; Change ownership/materialization groundwork; owner-only Unix control API; typed `factoryctl` client; Darwin process identity; two blocked-exec gates; gated Darwin PTY primitive; durable terminal-session admission, activation, recovery uncertainty and finalization guards; durable browser clients/challenges/revocation/input leases; exact PAIR/AUTH transcripts; strict browser-v1 handshake/binary codecs; strict runner terminal union, complete-write poisoning, incremental frame decoder and one fixed replay ring through `f1f72aa`; reviewed framework-neutral `@dark-factory/client` handshake/transcript/binary core and exact package gate through `d03491f`; independently reviewed question-only durable HumanRequest creation, private detail, reply reservation/acknowledgement/uncertainty, restart recovery, lifecycle convergence and bounded public projection through `40f5873`; the single-owner PTY execution loop, exact ready/input handoff, correlated retained replay, bounded filter retirement, poisoned writes, actual-EOF ordering and daemon-loss convergence through `ebcfd24`; exact two-field `AUTH_PROVE` through `4b18c38`; runner-owned exact HumanRequest PTY reply through `0f313a9`; daemon live-attempt registry, mailbox, bounded observers, finalization gate, active supervisor cancellation and joined shutdown through `d9709b9`; the closed attempt-only `request_human` API plus direct durable dispatch through `c29d154`; exact 8 KiB browser/Go/TypeScript terminal payload bound through `9ab44c3`; read-only exact lease authorization and one-shot failed-install/input-reservation revocation through `8853acb`; finalization/release linearization, natural-exit acknowledgement convergence, cancellation visibility and real descendant reaping through `ea1ee4b`; canonical Darwin runtime, Change and Change-worker fixtures through `699515d`; exact committed provider access to the attempt-only `request-human` command through `d4ce713`; independently reviewed fixed-page browser canonical state and private-detail separation through `1a562e4`; strict Go/TypeScript browser state/detail wire and causal reducer through `9b7689d`; exact-run HumanRequest terminal projection, fail-closed loopback browser state transport, guarded framework-neutral TypeScript Session client, direct daemon Store adapter and daemon-owned durable browser revocation through `b61fca8`; private transport-minted per-WebSocket connection identity through `53d68dd`; independently reviewed public MIT `@dark-factory/ui` package and contributor fixture through `18b5b0e`; exact daemon terminal acquire/renew/release/input/resize and HumanRequest reply effects through `ae28dc8`; exact browser-v1 terminal/HumanRequest manifest, Go codecs, golden fixtures and TypeScript mirror through `a10b9f0`; independently reviewed `factoryctl web status/open/list-clients/revoke`, exact launch ambiguity and bounded browser-runtime cleanup through `2f883c1`; reviewed framework-neutral TypeScript terminal Session authority through `b2eef51`; reviewed Go browser terminal/HumanRequest effect transport and cleanup through `7f449ce`; the exact agent terminal-target wire/Store/browser-daemon route and four-capability production pairing mask through `219d036`; high-level TypeScript target discovery, opaque terminal authority and automatic lease lifecycle; implemented, causally tested and independently allowed `RuntimeParent` lifetime/child-operation ownership through `7464e02a` and `15879fe2` |
| Reusable with adaptation | `internal/runner` live-child/process-group ownership and old result-spool identity/removal mechanics; daemon supervisor choreography; bounded API framing/auth separation; dashboard projection/client reducer direction; rebased recovery branch `go-recovery-reserved-fix` at `185cd5f`; fail-closed runtime/spool/Change close branches at `f239815`, `347c977`, and `4183205` |
| In progress but held | Exact runner-start/AttemptResult Store grammar; finite startup recovery and final live result publication; the independently allowed private-site bootstrap loop still lacks the provider terminal/HumanRequest product loop. The reviewed scheduler is canonical but remains unactivated. The development/Go sub-gates are integrated and green. |
| Obsolete | Startup-input-only/closed-stdin provider contract; separate stdin/stdout/stderr provider pipes as the product transport; TUI/Bubble Tea packages, lanes and parity tests; generic attention projection; message-on-next-run as the live-question answer |
| Proved for revised architecture | Current Chrome on macOS can connect from the protected hosted HTTPS preview to exact `ws://127.0.0.1:43123` with the dedicated loopback permission; strict Origin/Host checks, binary traffic, reconnect, denial, no-daemon, port-collision and cross-site refusal are causal. A fresh Darwin PTY child remains inert until release, owns a controlling terminal/process group and is reaped without orphaning. SQLite owns exactly one terminal session per admitted run and refuses terminalization until its exact close is proved. The outer runner owns the live PTY loop without goroutines, gates terminal commands on readiness, bounds and correlates replay before and after actual EOF, and writes one HumanRequest reply byte-for-byte without borrowing browser lease authority. The focused provider candidate additionally proves that the exact task arrives only on an unlinked read-only descriptor while every PTY byte is post-ready interactive input. The daemon registers one joined owner before release, rejects wrong sessions, routes bounded replay to multiple observers, actively cancels pre-live supervisors on shutdown and serializes infrastructure failure with terminal effects. Its exact effect bridge binds the durable client to one private WebSocket identity and generation, commits Store authority before runner effects, never replays ambiguous input/replies, and preserves positive terminal evidence until exact supervisor acknowledgement. The loopback server now enforces exact Host/Origin, pairing and per-operation durable client authority, serves bounded canonical state, joins subscriptions/connections, and couples exact-revision durable revocation to all-runtime socket close. The TypeScript Session client signs exact transcripts, publishes only complete fixed-head state, fences stale generations and ambiguous pairing, rate-bounds reconnect, and consumes the same pagination/empty-chronology contract. Each authenticated socket receives a private transport-minted identity that cannot be selected by a backend, serialized or exposed by formatting. The public React package renders bounded BUILDING, AGENT, task and read-only NEEDS YOU state without private detail or policy, installs and builds under the stripped Corepack gate, and remains consumable as an exact packed artifact. |
| Blocked until proved | Canonical AttemptResult publication/consumption; finite startup recovery; production scheduler/factoryd activation; interactive real-daemon terminal/reply/cancel product loop; exact private-host product-loop review; remaining factoryctl recovery/cutover plumbing; revised crash-cut vertical slice |

The final HumanRequest authority slice landed through `bd674b0`: one pinned
private detail with canonical active-run relationship validation, Store-derived
reply/cancel routing, a synchronous current-binding cancellation fence, a
concrete cancel result, no public run/terminal routing, and a high-level frozen
TypeScript Session API. It adds no generic action framework or compatibility
surface.

Read-only redirection audits were assigned without overlapping writes:

- `web_redirect_pty_audit`: current runner, supervisor and recovery reuse versus
  obsolete pipe/session behavior;
- `web_redirect_browser_audit`: current Chrome/Safari loopback rules, browser
  security, and the public-UI/private-host repository split;
- `web_redirect_human_protocol_audit`: durable `HumanRequest`, browser v1,
  Go/TypeScript drift prevention and UI package boundaries.

Their concrete conclusions are incorporated below. The revised plan gate is
complete; the exact current-head work graph below now governs production work.

### Pre-cutover contract checkpoint (2026-08-28; predecessor history)

This is a historical pre-cutover checkpoint. Later sections retain the exact
evidence and decisions available at their named heads, but cannot make an
older candidate or test result current.

- This checkpoint originated on the unpublished
  `go-change-contract-recovery-docs` worktree. Reviewed predecessor commits
  were `05d3bef7` and `cac06b60`; the record was later merged and independently
  reviewed at the canonical heads named above.
- Integration-target evidence is `359d46a3`: it contains production `factoryd`
  composition and `OperationalHome`/`Store`/`RuntimeParent`/Local API/browser
  ownership. It was manually merged with this reviewed documentation contract
  at `bc48df7f`; the corrected record was independently allowed at `51fae159`.
  The superseded shell package at `1ff2e2e6` was review-BLOCKED; its repaired
  successor is tracked in the current status above. Shell, Claude and Codex
  remain unshipped.
  `main`, remotes, the private site, the operator installation, live service,
  socket/home and real providers remain untouched.
- The operational Store is now integrated through `495ba3f5`, `5be1a76d`,
  `9d775b84` and `497ecfe4`. `OperationalHome.OpenStore(ctx)` activates it
  exactly once from retained home/database descriptors. One sealed finite
  connector eagerly opens one writer and four readers, proves every physical
  connection and `PERSIST_WAL`, permits no lazy replacement, and retains exact
  main/WAL/SHM plus ancestry authority until Store close is positively complete.
- Exact candidate head `e2925edbd14ebbb6a64fb920eed756a046b117d1`
  received independent **ALLOW** with no finding after earlier reviews blocked
  loss of partial-pool and close-uncertain authority. The unchanged stack was
  then integrated as the four canonical commits above.
- Canonical normal tests passed for the complete affected packages:
  `internal/kernel` in `26.316s` and `internal/install` in `4.263s`. The focused
  race matrices passed: kernel `11.026s`, install `9.082s`. Affected-package
  vet passed in `0.19s`; diff check and source status were clean.
- The final raw-retention mutation removed the finite connector's saved raw
  connection. `TestFiniteConnectorRetainsRawConnectionForgottenByDatabaseSQLClose`
  killed it causally: a real ncruces SQLite connection returned a close error,
  `database/sql` forgot it and reported zero open connections, while the exact
  raw connection remained live and owned without a reconnect or retry. The
  test also proved exact identity and one close/one connect. No mutation code
  remains.
- The integration-target branch also contains the exact `RuntimeParent` range
  `7464e02a` and `15879fe2`, plus production factoryd composition and the
  OperationalHome/Store/Local API/browser owners. This is package/composition
  evidence only; after the manual merge it still does not make corrected
  Change/global-admission or provider integration canonical.
- The public-artifact gate and its exact `c732f103` proof remain integrated and
  are preserved in the historical checkpoint immediately below. Store and
  RuntimeParent commits did not change those artifact files, but no new 13/13
  artifact or full `go-check` run is claimed here.

| Contract or gate | Current state | Next proof boundary |
|---|---|---|
| operational home and Store | integrated, causally tested and independently reviewed | composition must call `OpenStore` once and close child owners in the frozen order |
| public artifact gate | integrated; exact proof remains at `c732f103` | rerun after the next clean record/integration milestone |
| local API/browser authority | integrated through the canonical `359d46a3` composition and retained by `bc48df7f`; exact record reviewed at `51fae159` | retain authority closure through the installed browser product loop |
| `RuntimeParent` | integrated through `7464e02a` and `15879fe2`; production composition allowed through `bcfdb44b` | continue proving exact child ownership in crash/restart E2E |
| Change disposition/descriptor handoff | exact review blocked `c675f96e`; its narrow ownership-classification repair is in proof and is not canonical | reviewer ALLOW, integration, then retained-retry provider proof |
| global admission/recovery/provider integration | global candidate `1c4eb6c6` depends on the Change repair; provider candidate `e1b0759e` is under exact review | exact kernel transaction, sealed provider Build, restart/crash and fake-witness proofs |
| service/release/private host | not cut over | isolated install/service proof and exact public-artifact site integration |
| final elegance and deletion | deliberately not started | whole-runtime DRY/YAGNI audit, mutations, exact-head reviews, then Rust deletion |

The predecessor weighted estimate was approximately 42 percent. It is retained
only as historical scope evidence, not as a current completion claim.
Operational Store is a real authority proof inside an already-credited kernel/install gate; it does
not yet complete a black-box daemon, recovery, service, private-host or cutover
gate. The estimate is therefore not inflated for integration activity.

#### Current canonical composition evidence

- `RuntimeParent` lifetime, exact child capability and close/join semantics are
  integrated through `7464e02a` and `15879fe2`, with independent ALLOW.
- Production `factoryd` composition, startup ordering and joined shutdown are
  integrated through `9ffc13e3` and `bcfdb44b`, with independent ALLOW.
- The unused `ExecutionMode` policy dimension and its duplicate validation were
  deleted through `24aaccd1` and `359d46a3`, with independent ALLOW.
- `./scripts/go-check.sh` passed at `359d46a3`: vet, focused Go tests, 188
  TypeScript/UI tests, packed-consumer proof and diff check.

This evidence does not prove corrected global admission, provider integration,
service cutover or the installed browser product loop.

#### Integrated operational Store boundary

The finite physical set is authority, not a pooling optimization. Activation
uses a caller-derived context with the existing bounded ten-second ceiling,
installs the file binding before post-pool validation and creates a Store owner
as soon as any physical pool exists. Partial fields close safely. Path binding
and exact main/WAL/SHM identities are rechecked around every checkout; a
replacement poisons subsequent Store use rather than retargeting it.

`Store.Close` first rejects new work, joins the already-admitted writer and
every checked-out connection, closes both finite pools, then releases the exact
file binding. `OperationalHome.Close` closes the Store before its retained
members and releases the lifetime home lease last. If connection, pool or file
shutdown is uncertain, the owning Store and home lease remain retained,
`install.ErrUncertain` is visible and repeat close returns one stable result.
Another opener remains `ErrBusy` rather than inventing ownership.

The finite connector retains each raw driver connection from physical open.
This is necessary because `database/sql` may call a driver's failing `Close`,
drop the connection from its own accounting and never call it again. Retaining
closed `*sql.Conn` wrappers was deleted because they no longer own the driver
or database reference; the raw connector owner is the smaller effective
authority. There is no repository layer, reconnect fallback, generic pool
framework or second transaction pattern.

#### Corrected local API authority boundary (historical at `497ecfe4`; superseded by canonical `359d46a3`)

This subsection records the planned/no-implementation status at old head
`497ecfe4`. The later integration target `359d46a3` contains production
`factoryd` composition with Local API and `RuntimeParent` ownership; that
composition is now canonical and its status is recorded above. The earlier
`c732f103` proposal to let `internal/api` bind the Unix socket from
a capability is superseded. `OpenLocalAPI` lives in `internal/install` and owns
the fixed endpoint lifecycle, including create/bind, exact socket identity,
mode, stale-socket decision and removal. `internal/api` owns only the bounded
framing, credential-domain parsing and typed calls over that already-owned
endpoint.

`LocalAPIAuthority` is structurally a child of its `OperationalHome`, not a
detached duplicated capability. The home close order is local API first, then
Store, then retained members/ancestry, with the home lease released last. The
composition root must stop and join API framing/connection users before this
child is released; the authority cannot outlive or independently reopen the
home.

Authentication `Verify` rechecks only the retained immutable core home
binding, exact operator token, `runtimes/` parent and exact socket binding. It
does not rescan the complete operational-home census and does not bind mutable
SQLite WAL/SHM contents: the Store already owns those files and legitimate
database activity changes them. Token mutation, core/runtimes replacement or
socket replacement poisons authentication without widening the check into a
second Store/home validator.

The socket leaf is exactly `runtimes/factory.sock` and is reserved from runtime
identifiers. Stale removal requires an EUID-owned exact socket, stable identity
across the probe and exact `ECONNREFUSED`. A live peer, `EPERM`, changed
identity, malformed object or any ambiguous response is retained. Darwin's
absolute bind locator is never ownership authority. There is no compatibility
socket, alternate token, hot rotation, listener factory or server-side path
fallback. No part of this Local API contract was implemented at `497ecfe4`.

#### Global transactional admission contract (planned; implementation gate)

The V1 decision is one concrete `Store.AdmitNext(ctx, keys, at)` call. The call accepts
no caller `AgentID`, task ID, queue observation, pagination token, round-robin
position or fairness cursor. `keys` contains a complete fresh daemon-minted
candidate footprint for every call,
including an unconditional fresh candidate Change ID. That Change ID is used
only when the transaction selects a worker task incarnation with no canonical
Change row; an existing Change or selected non-worker ignores it. No candidate
value selects work.

Inside one literal `BEGIN IMMEDIATE`, the Store validates the complete fresh
schema image before it accepts either RunID reconciliation or a new decision.
The exact column names, SQLite storage classes, scalar bounds, enum sets,
nullability, `CHECK`s, foreign keys and unique indexes are owned only by
`internal/kernel/schema.go` and its exact schema allowlist/constraint tests.
This record does not duplicate that column table. A schema object, column,
constraint, or wire field absent from that executable allowlist is not part of
V1. A missing row, unknown enum/status or invalid control value is
`ErrCorruptState`; global counters are admission authority, not schema-only
checks. Configured capacity is an integer in `[1, 1024]`. The one concrete SQL
integrity predicate then covers both:

- every row/relation/control that can occupy capacity or bind active authority,
  including all run, attempt-credential, resource, terminal-session and Change
  phase/control facts; and
- every structurally queued task assignment plus its required agent row,
  project row and complete rank/payload facts.

The predicate rejects an unknown run/resource/session/Change phase, invalid
ID/revision/enum, missing required relation, phase/fact mismatch, incomplete or
split provider pair, malformed resource/session footprint, or authority whose
scope cannot be derived exactly. Only after it proves every run phase is known
may the Store count `admitted`, `running` and `finalizing` for capacity.
Fresh-schema constraints prevent ordinary invalid writes; this SQL predicate
is the causal proof against constraint-bypassed or damaged durable state, not a
second validation framework or an application row scan.

The schema/constraint tests own SQLite storage classes, byte bounds, enum sets,
numeric limits and nullability. Shared Go create/read/wire validation owns
UTF-8 and NUL rules; it runs before durable creation and on the selected
canonical task/control facts. A malformed selected value is corruption before
admission, never normalized or silently coerced. Lower-ranked queued prose is
not globally UTF-8 scanned merely to decide capacity. **Implementation gate
(not yet landed):** project source-root validation and schema tests must reject
`/`; accepted roots are clean absolute paths with no NUL, empty, `.` or `..`
component, repeated separator, or trailing separator.
A root outside that grammar is refused rather than normalized into validity.

The fresh schema has no profile row or status, and no permission-profile field,
type, column or wire value. Agent `paused` is its availability switch;
provider choice means unrestricted interactive authority in V1. Shell has no
model or reasoning-effort value; Claude and Codex carry independent optional
values. Bounded authority is deferred until causal OS-effect proof exists.

The schema/constraint tests own every scalar domain and storage class. The
cross-row predicate additionally requires each structurally queued assignment
to have a valid same-project task/agent/project relationship and complete rank
and payload facts, with queued lifecycle facts absent. After the global
predicate selects the canonical task, shared validation checks its bounded
UTF-8/NUL text; malformed canonical control or payload, an unknown status,
reversed timestamp, or damaged fact in the admission footprint is
`ErrCorruptState`. Known-valid paused, exhausted, conflicting, or nonqueued
facts remain ordinary eligibility outcomes. This is one direct Store SQL
predicate plus the shared value validator, not a second admission row scan or
duplicate validation framework.

The same predicate proves the invalidation algebra before any capacity or
no-admission result: `head = next_invalidation_sequence - 1`; the empty log
has exactly zero rows, `head = 0`, and `invalidation_floor = 1`; a nonempty log
has at most `EventRetentionLimit` rows, minimum sequence equal to
`invalidation_floor`, maximum equal to `head`, and exactly one row for every
sequence in that closed interval. Each row must satisfy the executable schema
allowlist and reference a valid entity ID/revision with an exact deletion bit.
There are no gaps, duplicate sequence values, or metadata-only advances.

Only after global-settings validation and the integrity predicate pass
does the transaction apply this exact no-admission precedence:

| Order | Transactional decision | Exact result |
|---|---|---|
| 1 | durable dispatch control is valid but disabled | `dispatch_disabled` |
| 2 | durable factory-wide capacity across every nonterminal run (`admitted`, `running` and `finalizing`) is exhausted | `at_capacity` |
| 3 | no task row is queued | `queue_empty` |
| 4 | queued rows exist but none satisfies every durable eligibility predicate below | `no_eligible_work` |
| 5 | select the globally canonical eligible task+agent, then validate its one canonical Change | commit the full footprint, or fail/return exactly as below without considering lower-ranked work |

Eligibility is exactly: task status is `queued`; its assigned agent exists, is
valid and belongs to the same project; that agent is not paused and has durable
budget remaining; the role is one of the
two valid values (`worker` or `orchestrator`); and there is no conflicting open
run. Both valid roles are eligible—role determines the admitted footprint,
including whether a Change is required, not whether an external tool is
currently available. Global dispatch-disabled is the earlier exact reason.
Provider, model, effort, project verification and timestamp controls must satisfy
the executable schema allowlist, but external provider
executable/configuration/auth availability is deliberately not eligibility.
Installed-version/model compatibility is checked by provider `Build`/start
after admission; incompatibility is typed `FailureSpawn`/finalizing, never
durable corruption or queue ineligibility.
Known-valid paused, budget-exhausted or conflicting-open-run assignments are
ordinary ineligibility and may produce `no_eligible_work`; known nonqueued task
statuses are outside the queue.
Corruption is never ineligibility: the preselection predicate blocks admission
even when the malformed structurally queued row ranks below an otherwise valid
candidate.

The globally canonical eligible row is ordered by:

```text
priority DESC, created_at_ms ASC, task id BLOB ASC
```

Task IDs are exactly 16-byte SQLite `BLOB`s and the last comparison is bytewise
ascending; text collation, UUID display form and locale never participate.
These are SQL predicates/order inside the transaction, not a scheduler census
or memory filter. After selection, the Store validates the selected task
incarnation's one canonical Change. A corrupt, unsettled or hard-invalid Change
fails `ErrCorruptState` and is never skipped in favour of lower-priority work.
Canonical Change corruption is the only Change-specific pre-admission
decision in V1.

Only then does the transaction derive launch facts, select/reuse or reserve the
Change, create the complete run/credential/resource/terminal/task/Change
footprint and append every invalidation before commit. External repository
availability and provider executable/configuration/auth availability are
deliberately not stale eligibility filters; after admission their absence
converges through typed `FailureSource` or `FailureSpawn`. A stale observation
can never nominate work or authority. A lost reply reconciles only by the
supplied fresh run ID.

Agent-specific pause, budget and open-run reasons collapse into
`no_eligible_work`. There is no agent enumeration/pagination API for the
scheduler, queue cache, round-robin loop, fairness table or cursor. Strict
global priority can starve lower-priority work; that is an accepted visible V1
risk. Any future fairness policy must become durable Store policy rather than a
process-local cursor.

One synchronous `RunNext` calls this operation with a fresh footprint and
derives every launch fact from the committed Run it receives. It does not reuse
an earlier task/agent observation. The scheduler is therefore only a joined
loop around `RunNext`, not a second admission authority or abstraction.

The causal admission matrix races multiple agents at different and equal
priorities, inserts higher-priority work after a stale observation, exercises
every eligibility/reason-precedence row, and performs concurrent admits at the
last factory-capacity slot. One admitted setup-stalled run occupies that last
slot exactly like running and finalizing runs. Exact ties use the 16-byte BLOB
order. Tests put a corrupt/unsettled Change ahead of valid work and prove no
skip. Restart changes no ordering because no cursor exists.
Required mutations accept a caller AgentID/task/cursor, enumerate agents before
the transaction, select per-agent or from a cached/stale queue, check capacity/
eligibility outside the write transaction, omit global-settings validation or
the integrity predicate, let malformed authority or higher/lower ranked queued
facts fall through, exclude an admitted setup-stalled run from capacity,
compare text IDs, reorder reasons, skip corrupt canonical work, treat external
availability as eligibility, omit a fresh footprint or conditionally mint the
Change candidate, let that candidate select/reconcile an existing Change, or make process-local
fairness state affect the choice. Each must be killed by the committed-footprint
and external-effect tests.

#### Corrected Change disposition and descriptor contract (planned)

Successive cold reviews **BLOCKED** the literal `c732f103`, `f05eff86`,
`88a8ab22`, `884d63c3`, `7dc2e83b`, `a8d3c395`, `c38ce8a6`, `4a9b7672`,
`7e6d00c4`, `d856dc7b` and `debac058`
plans. They found stale retry revision, child-live abandonment, caller-selected
reuse, incomplete crash recovery, a leaked staging namespace, contradictory
worker executables and finally a transient no-start receipt that became
unrecoverable on frame loss. The `4a9b7672` review additionally found a false
ready-frame distinction, no honest outer-runner starting uncertainty, missing
initial-tree durability, insufficient prepared-abandonment owner proof and
cross-document admission conflicts. The `7e6d00c4` review then found a capacity
undercount, an outcome race that could strand a declared empty provider pair,
and incomplete queued-corruption coverage. The `d856dc7b` review found that
unknown authority phases could escape capacity, queued rank/payload facts were
still underspecified, eligibility contained an undefined label and the
claimed retention bound was unsupported. The `debac058` review then found a
stale Change-specific no-admission decision and queued-control domains that
still were not field-exact. The contract below incorporates
every required correction and supersedes those literal tables. It still
requires a fresh independent exact-contract **ALLOW** before implementation
begins. No production Change schema, runner or worker code has implemented it
yet.

The fresh Change scalar columns and domains are owned only by
`internal/kernel/schema.go` and its exact schema allowlist/constraint tests;
this record intentionally does not duplicate that list. The following are
cross-row relations and transition facts for the target implementation, not a
claim that those relations are implemented in this old-base candidate.

Phases are exactly `reserved`, `prepared`, `available`, `retained` and
`abandoned`. There is no `selected` or durable `unresolved` Change phase, no
source/staging/repository pathname, no selected timestamp, and no separate
stage/source identity. Deterministic names come only from `ChangeID` below the
retained `OperationalHome.Changes` descriptor. One `tree_dev/tree_inode`
identity survives the staging-to-final no-replace rename.

Repository identity is a live descriptor proof during Git selection only. The
worker binds and rechecks that descriptor while selecting `base_commit`; after
the selected objects and bounded tree commitment are fixed, no recovery or
authority decision consumes a repository device/inode. Those values therefore
do not become durable Change columns. Object format, base commit and exact
published tree/content facts remain durable.

| Phase | Selection/tree facts | Phase timestamps | Settlement |
|---|---|---|---|
| `reserved` | all null | prepared and available null | null |
| `prepared` | all present | prepared present; available null | null |
| `available` | all present | prepared and available present | null |
| `retained` | all present | prepared and available present | exact settling run |
| `abandoned` | all null | prepared and available null | exact settling run |

For `prepared`, `available`, and `retained`, all materialization facts are
present together. `updated_at_ms` is at least every present Change timestamp;
each transition and retry supplies an `at` no earlier than the current
`updated_at_ms`, and stores that `at` as the new `updated_at_ms`. A reversed or
out-of-range timestamp, wrong SQLite storage class, null/non-null mismatch,
wrong commit/digest length, invalid count/byte/device/inode, or broken
settlement relation is `ErrCorruptState` before capacity and before any fresh
no-admission result.

Every Change mutation advances `changes.revision` exactly once and appends its
matching Change invalidation with that new revision in the same immediate
transaction. A transition replay returns the already-committed value without
another increment or invalidation only while the current run and Change still
prove that exact post-transition state and immutable payload. Once a later
transition advances either aggregate, the old transition call conflicts;
lost-admission and historical-finalization reconciliation use their explicit
domain relationships instead of replaying an intermediate transition. There
is no receipt ledger, transition history or generic mutation identity. There
is also no rule equating a phase timestamp with `updated_at_ms`: retry
legitimately updates the row while preserving original preparation/publication
timestamps.

Admission owns Change selection. The schema has one unique canonical Change key
`(project_id, task_id, task_incarnation_id)`; zero or multiple rows for that key
is corruption, never a reason to skip work. After global `AdmitNext` selects the
canonical task/agent and exact task incarnation inside its immediate write transaction,
it queries the Change for that incarnation. Every admission call supplies
exactly one freshly generated candidate Change ID and no phase, revision,
pathname, reuse ID, AgentID or task ID. A selected non-worker ignores the
candidate. For a selected worker, an existing row also ignores the candidate
completely and the row decides reuse; only when no row exists may the
transaction insert it. Existing reuse is eligible only when `retained` or `abandoned` and
settled by the unique predecessor worker run whose
`admitted_task_work_revision == task.work_revision - 1`. Timestamps, row order
and a query for the merely latest run never select that predecessor. The same
transaction performs the retry transition and binds the actual run, attempt
digest, actual Change ID and post-transition
`runs.admitted_change_revision`.

Settlement is valid only for a terminal predecessor worker run whose exact
`change_id`, project, task, task incarnation and admitted work revision match
the Change. A nonterminal, orchestrator, cross-task, cross-incarnation or
different-Change run cannot settle it. The composite foreign key proves row
identity; the predicate and Change transition prove terminal predecessor and
exact aggregate equality.

Lost-response reconciliation compares the candidate only for a fresh
reservation at `A=1`. For existing-row reuse it returns the actual committed
Change and admitted revision, even though that Change ID differs from the
ignored fresh candidate. Neither path requires the Change's later current
revision to remain equal to its admitted revision.

Let `A` be the new run's `admitted_change_revision`. The exact transitions and
deltas are:

| Admission/transition | Exact revision and committed proof |
|---|---|
| no row -> `reserved` | revision 1 and `A=1`; insert the request's fresh candidate as one path-free Change and bind it to the new run |
| retained retry N -> `available` | `N+1` and `A=N+1`; preserve all object, base, content, tree and timestamp facts, clear settlement, bind the new run atomically |
| abandoned retry N -> `reserved` | `N+1` and `A=N+1`; preserve no obsolete materialization facts, clear settlement, bind the new run atomically |
| fresh/abandoned `reserved` -> `prepared` | `A+1`; exact Git selection, commitment and empty stage identity commit together before any materialized blob read |
| fresh/abandoned `prepared` -> `available` | `A+2`; the one central durability scanner fsyncs every accepted file/directory in stage or recovered final, atomic no-replace publication preserves exact identity, the retained parent is fsynced and every binding/content/stage fact is stably revalidated; settlement remains null |
| retained retry `available` -> `retained` | `A+1`; the exact settlement arm below revalidates stable identity, content facts and stage absence, then binds this run |
| fresh/abandoned `available` -> `retained` | `A+3`; the exact settlement arm below revalidates stable identity, content facts and stage absence, then binds this run |
| direct `reserved` -> `abandoned` | `A+1`; stable deterministic-stage and final absence proof below succeeds without deletion and settlement binds this run |
| `prepared` -> `abandoned` | `A+2`; stable prior stage absence or exact recorded-identity removal plus the absence proof below succeeds, obsolete selection/tree/timestamp facts are cleared, and settlement binds this run |

Anything else is durable corruption, not a recoverable phase variant. There is
no admission-mode column. “Preserve retained facts” never means preserve the
row revision. A retained retry reaches provider activation at exact revision
`A`, performs no Git selection or materialization, and immediately before exec
requires the final name's identity, digest, entry count and total bytes to
equal the retained facts literally. At `prepared -> available`, the final
name's device/inode must equal the identity persisted at `prepared`.

There is one central bounded Change durability scanner, not separate initial-
publication and settlement implementations. It descriptor-walks one exact safe
stage/final tree, validates the bounded path/mode/link/device/content grammar,
fsyncs every accepted regular file and every directory bottom-up including that
tree root, and repeats the full identity/content commitment before returning.
The normal initial-publication arm applies it to the exact stage before rename;
the recovered-publication arm applies it to the exact final tree. The nonnull-
`RunningAt` settlement arm below reuses the same scanner after provider write
authority. No caller may replace its fsync/recheck behavior with a digest-only
scan.

Every `available -> retained` arm binds the Change parent, proves the final
name still has that exact identity and proves the deterministic stage name
stably absent before and after the parent-binding recheck. A present,
reappeared or replaced stage remains finalizing and is never deleted by this
transition. When `runs.running_at_ms` (`Run.RunningAt`) is null, the final
tree's digest, entry count and total bytes must equal the available facts
literally and the transition may not rewrite them. When it is nonnull, the
provider had durable write authority. The central scanner durably rechecks the
safe tree, then the caller fsyncs the exact retained Change parent so stage
absence is durable and repeats a full stable parent/final binding,
identity, content commitment and stage-absence check before the Store commit.
Only that arm may replace digest/count/bytes; tree identity stays fixed. The
presence or absence of `inner.activate` does not choose these arms: that marker
proves only that the selection gate crossed, never that provider exec or a write
occurred. A mismatch never adopts a replacement or rewrites facts to bless it.

One durable canonical `AttemptResult` spool is the only inner-process result
authority and replaces both the old `TerminalRecord` and the transient no-start
receipt. Its fixed leaf is `attempt-result.json` below the exact retained
runtime capability. The runner validates the fresh absent leaf, creates it
without replacement, writes one bounded canonical value, fsyncs the file and
runtime directory, then reopens and hashes the exact inode. The result is an
EUID-owned one-link regular file, exact mode `0600`, on the runtime device and
at most 1,024 bytes. The value is the following closed union and has no other
fields:

```text
{ version: 1, attempt_id: DAEMON_CONFIGURED_ID, kind: inner_unregistered_converged }

{ version: 1, attempt_id: DAEMON_CONFIGURED_ID, kind: inner_converged,
  process: { pid, pgid, birth }, exit: { code | signal } }
```

There are no caller IDs, booleans, messages, launch errors, flags, timestamps,
payloads, history rows or second receipts. `inner_converged` has exactly one of
code or signal; unknown/duplicate fields and trailing bytes fail. `inner_unregistered_converged` can be minted
only by the launch primitive while the exact controller is configured but
before `AttemptInnerReady`: it first validates the gate and fresh result name,
then the sole `cmd.Start` call returns a non-nil error. A caller cannot infer or
assert this kind. Once `cmd.Start` returns nil, the exact owner is retained
through every outcome. It must positively acquire the exact PID/PGID/birth
identity. While the direct leader remains unreaped, the owner observes its exit,
retains the birth-pinned process-group identity, converges or kills descendants
under that live ownership and positively proves group absence. Only then does
it sole-`Wait` the leader. It never signals or probes the numeric process group
after `Wait`, when leader identity could be reused. If birth identity cannot be
acquired after a successful start, the owner still converges and waits what it
owns but emits no result; that pre-identity uncertainty remains nonterminal
rather than writing a weak identity.

The same outer controller owns the PTY master and bounded output reader. Its
result-publication primitive is reachable only after terminal input is frozen,
the group-absence proof and sole `Wait` complete in that order, the bounded
final output is drained serially, the output reader is synchronously joined and
the PTY master is closed. It does not accept a caller-supplied “output closed”
flag. Thus the authenticated spool also proves that controller ordering without
adding terminal bytes, an output receipt or a second durable artifact.

A short write, malformed value or identity/name change leaves a corrupt
no-replace artifact. The runner never deletes, repairs or replaces it and never
respawns; the run remains finalizing for operator-visible recovery rather than
manufacturing a result. A complete canonical residue from a crash before the
publisher's fsync is different: the common consumer can promote those exact
bytes durably as described below. There is no durable or observable
“previously fsynced” bit, so the contract never guesses whether a complete
residue reached storage before recovery.

`inner_converged` is valid before `AttemptInnerReady` after a spawned child's
readiness failure and in every post-`AttemptInnerReady`, pre-result controller
state. After `AttemptInnerReady`, its process identity must equal that already
reported PID/PGID/birth exactly. The result is no-replace and single-use, so
the outer runner has no respawn or second-publication path. It persists and
fsyncs the spool before a best-effort `AttemptResultReady` frame carrying only
the exact result inode/device and SHA-256 on the authenticated control socket,
then exits without waiting for an acknowledgement. The daemon sole-waits its
outer child; the frame is neither authority nor a stored receipt, and a lost
frame is ordinary recovery rather than lost authority.

Live notification and restart recovery call the same descriptor-relative
result opener. It binds the fixed leaf to the exact retained runtime and
daemon-configured attempt ID, verifies no-follow owner, mode, link count and
size bound, parses the closed union, and derives the same inode/device and
SHA-256. A live frame must match those facts; recovery derives them from the
same spool. Neither path trusts socket payload as the result or trusts that a
prior fsync occurred. Before any `ConsumeAttemptResult` call, the common
consumer opens and validates the exact file grammar/inode/digest, fsyncs that
exact open file, revalidates its descriptor, fixed name and complete contents,
fsyncs the retained runtime directory, then revalidates descriptor/name/content
once more. This promotes a complete canonical no-replace residue to durable
evidence without changing a byte. A partial, malformed or replaced artifact,
or any failed/changed revalidation, remains finalizing and is never repaired.

The provider process and provider group are one pair-atomic Store unit from
declaration onward. Every pair transition requires exactly two affected rows;
their phases are equal, their identities are either both empty or the same
exact PID/PGID/birth tuple, and any split update rolls back. Concrete pair
operations exclusively declare, activate, begin release, mark unresolved and
consume/release the pair. Generic per-row activation, releasing, unresolved or
release operations reject both provider kinds.

The initial resource set is exactly `runtime_root`, `runner_process`,
`provider_process` and `provider_group`. Only `runner_process` adds one phase:
`declared -> starting -> active`. `starting` has empty identity and means one
outer `cmd.Start` call was durably permitted but its success/failure is not yet
durably known. No other resource may use that value, and generic resource or
outcome mutations reject it. The fresh-schema `CHECK` and every row scanner
enforce `starting` only for an empty-identity runner row; constraint-bypassed
use by any other kind is `ErrCorruptState`.

After all setup and immediately before the sole outer `cmd.Start`, one concrete
`BeginRunnerStart` immediate transaction requires an admitted run with no
proposal, active runtime root, declared/empty runner, declared/empty provider
pair and declared never-activated terminal; it moves exactly the runner row to
`starting` and commits the matching run/resource revision and invalidation.
An outcome or cancellation racing while the runner is still `declared` wins in
one transaction: it preserves that exact first proposal (including
`cancelled`), enters `finalizing`, revokes authority, moves runtime root to
`releasing`, directly releases the exact three never-created empty process
resources and closes the never-activated terminal. `BeginRunnerStart` then
cannot pass its guards. If `BeginRunnerStart` wins, generic outcome/cancellation
is refused and retried after the launch primitive resolves; it never guesses
that no child exists.

An exact non-nil outer Start error is consumed only by one
`RecordRunnerNeverStarted` immediate transaction. It requires the same admitted
run still without a proposal, null `RunningAt`, runner `starting`/empty,
provider pair declared/empty, terminal declared/never activated, no inner-ready
or AttemptResult evidence and the exact live launch call's error. It installs
`FailureSpawn`, enters `finalizing`, revokes authority, moves the runtime root
to `releasing`, directly releases runner plus the empty provider pair and
closes the terminal atomically: exact affected counts are runtime one, process
resources three and terminal one. A successful Start instead must bind its
exact runner PID/birth in one guarded `starting -> active` transaction before
any further outcome can commit. A crash, ambiguous Start return or lost binding
while `starting` remains real external uncertainty: recovery exposes the start
as unresolved, never signals or relaunches, and leaves the durable runner state
`starting` rather than using a no-child arm. There is no start receipt, ledger
or second state machine.

Runner activation creates one smaller serialization barrier. While the outer
runner is `active` but the provider process/group pair remains `declared` with
empty identity, every generic outcome, operator cancellation and
infrastructure-failure outcome transaction refuses and is retried; attempt
success cannot exist because attempt authority is not yet `running`. No such
outcome can move the empty pair to `releasing` or create a finalizing run with
a declared provider pair.

The already-prepared one-shot outer runner must perform its sole inner
`cmd.Start` attempt even when cancellation or daemon control EOF races. An
exact Start error publishes the existing `inner_unregistered_converged` AttemptResult. A
successful Start keeps the child inert behind `inner.activate`, acquires exact
birth identity and drives the ready/activation binding. A pending outcome can
then win against the active exact pair and cause its live owner to converge and
reap it before provider exec. If daemon EOF arrived first, the runner still
resolves that one Start, never crosses the activation gate, publishes the
applicable ready/result evidence and converges its owned child before exit; it
never skips Start, respawns or invents no-child proof. The current one-shot
runner choreography must prove this property before implementation is allowed;
if it cannot, this contract is blocked rather than widened with another receipt
or durable state.

Once the provider pair is `active` with exact identity, an ordinary first
outcome from `admitted` or `running` atomically records the immutable proposal,
enters `finalizing`, revokes attempt/input authority, moves all four resources
from `active` to `releasing`, and moves the terminal from declared/active to
`releasing`. Its concrete DML affects runtime/runner two, provider pair two and
terminal one; any count, phase or identity mismatch rolls back. Consequently a
finalizing run cannot durably retain a declared/active resource or terminal,
while runner `starting` is the explicit fail-closed pre-outcome exception
above. Exact AttemptResult consumption below may direct-release a declared
empty pair; no generic outcome may do so.

One concrete Store `ConsumeAttemptResult` transaction accepts only this matrix:

| Run phase | Exact resource/session precondition and result | One atomic consequence |
|---|---|---|
| `admitted`, inner child never created | runtime root and runner both `active`; provider pair both `declared` with empty identity; terminal `declared`; exact `inner.activate` absence; `inner_unregistered_converged` | install `FailureSpawn` if no proposal exists; preserve any existing proposal; enter `finalizing`; revoke authority; move the exact runtime/runner two rows and terminal to `releasing`; deliberately release the empty provider pair directly with no `ProviderExit` |
| `admitted` after `AttemptInnerReady`, activation committed | runtime root and runner both `active`; provider pair both `active` with exact stored identities matching `inner_converged`; terminal `declared`; exact marker census is known, not uncertain | install `FailureActivation` if no proposal exists; preserve any existing proposal; enter `finalizing`; revoke authority; move the exact runtime/runner two rows and terminal to `releasing`; record exact `ProviderExit`; deliberately release the provider pair directly |
| `admitted`, spawned child never durably activated | runtime root and runner both `active`; provider pair both `declared` with empty identities; terminal `declared`; exact `inner.activate` absence; a runtime/attempt-bound valid `inner_converged` supplies identities because the ready frame never arrived or activation DML never committed | install `FailureActivation` if no proposal exists; preserve any existing proposal; enter `finalizing`; revoke authority; atomically bind the result identities only while recording exact `ProviderExit` sequence 1 and directly releasing the provider pair's two rows; move runtime/runner two and terminal one to `releasing` |
| `running` | runtime root and runner both `active`; provider pair both `active` with the exact result identity; terminal `active`; matching `inner_converged`; exact marker census is known, not uncertain | install `FailureProviderExit` if no proposal exists, never success; preserve any existing proposal; enter `finalizing`; revoke authority; move the exact runtime/runner two rows and terminal to `releasing`; record exact `ProviderExit`; deliberately release the provider pair directly |
| `finalizing` | runtime root and runner are each `releasing` or `unresolved`; provider pair are both `releasing` or both `unresolved` with exact nonempty identities matching `inner_converged`; terminal is `releasing` or `unresolved` | preserve the required existing proposal and all non-provider phases; record exact `ProviderExit`; release exactly the provider pair's two rows |

Every state, proposal, credential, pair, terminal lifecycle and bounded
invalidation/event consequence named by one row commits in that same immediate
transaction. Within result consumption, admitted/running rows are the direct
provider-pair release exception: authenticated result supplies positive
terminal process evidence while the same transaction establishes finalizing
and moves the other two resources/session to cleanup. The two outer-runner
pre-start transactions above separately direct-release the empty pair only
because their exact declared/Start-error facts prove no outer runner or
provider child exists. After outer-runner activation, only authenticated
`inner_unregistered_converged` or exact converged AttemptResult consumption may direct-
release the provider pair; generic outcome paths are barred while it is
declared/empty.
Admitted/running result DML counts are runtime/runner two, provider pair two and
terminal one; the finalizing row changes exactly the provider pair two. Any
partial count rolls back.

`inner_unregistered_converged` is valid only for an empty-identity pair and deterministically
maps to `FailureSpawn` without `ProviderExit`. Every admitted
`inner_converged`, whether activation committed or the bound result supplies
the identities after missing readiness/activation durability, maps to
`FailureActivation` and records `ProviderExit`.
A running active `inner_converged` with no proposal maps to
`FailureProviderExit`, regardless of exit zero; provider output never creates
success. An existing first proposal is never overwritten.
`inner_converged` is valid for an exact-identity active/releasing/unresolved pair
or for the admitted declared/empty spawned-child case. The Store never claims
to distinguish a readiness frame that did not arrive from activation DML that
did not commit; neither is durable authority. The declared arm validates the
result's runtime/attempt binding and identities and binds them only inside the
same two-row release/ProviderExit transaction; it never creates an intermediate
active pair. A split phase, one-sided identity, result/row identity mismatch,
other run/pair phase or generic provider-resource mutation fails without state
change.

Every result-derived `ProviderExit` has sequence exactly 1 and preserves the
closed union literally: exactly one of code or signal and the exact value from
`inner_converged`. `provider_exit_at_ms` is Store commit metadata, not runner
input. Exact lost-response replay accepts and preserves the already-stored
valid timestamp; it neither recomputes nor compares a caller timestamp.

For every declared/empty pair, the exact durable `inner.activate` marker must be
positively absent in the result's bound runtime census. The descriptor-relative
result opener observes the fixed marker through the same retained runtime
descriptor, checks absence around result identity/digest validation and
rechecks it immediately before the transaction; no pathname or caller boolean
can stand in for that observation. A present marker means the selection gate
crossed and declared/empty facts are corrupt/unresolved. An active matching pair
may validly have the marker present or absent; either state must be positively
and stably observed, but neither authorizes identity or outcome. Marker
uncertainty always fails without mutation.

`ConsumeAttemptResult` never closes a terminal session. Admitted/running
consumption moves it to `releasing`; finalizing consumption requires it already
`releasing` or `unresolved` and preserves that phase. Lost Store responses
reconcile only through those natural postconditions: exact released pair,
immutable first proposal, matching typed `ProviderExit` only for a nonempty
pair, and the exact nonclosed session phase. There is no result receipt/history
row or blind second transition.

The daemon next either sole-waits its still-owned exact outer runner and records
its real code/signal, or recovery never signals and requires the persisted
runner PID/birth to observe `Absent` or `Reused` before recording
`recovered_absence`. That same transaction releases the runner resource.
`Unknown`, `EPERM`, a weak/replaced identity or uncertain observation remains
nonterminal. Only after the provider pair and runner resource are released may
one concrete `CloseTerminalAfterRunner` transition reauthenticate the exact
AttemptResult spool, validate the controller's bounded final-output/PTY-close
ordering, and close that run's `releasing` or `unresolved` session idempotently.
A declared/active session attached to a finalizing run is corruption, never a
close arm. The transition validates exact run/result/runner postconditions on
a closed replay. No browser call, generic session helper or result consumption
can bypass this order: the live PTY owner is gone first.

Change settlement always waits for runner release and terminal closure;
closing a Change-parent duplicate is necessary descriptor hygiene but is not a
substitute. The daemon removes the AttemptResult only after a Store reread
proves provider pair released, runner released and session closed. If the exact
spool is present, it descriptor-removes that verified inode, fsyncs the runtime
directory and rechecks the leaf absent. If a crash lands after unlink but
before directory fsync, restart may finish the cleanup only when those exact
durable Store postconditions already hold and descriptor-relative checks prove
the leaf stably absent across a runtime-identity recheck; it fsyncs the
directory and checks absence again. Stable absence before those Store
postconditions, or a present malformed/replaced inode, proves nothing and
remains finalizing. Runtime-root release is a later cleanup precondition, not
Change-settlement authority.

Absent or corrupt result spool plus control EOF, outer PID absence, the runtime
lifetime flock or empty provider rows proves nothing and leaves the run
finalizing after any outer Start was permitted. The two exact pre-start
transactions above need no spool because they atomically close the never-
created process/session facts while the live daemon still owns the decisive
`declared` or exact Start-error fact. A crash while runner state is `starting`
cannot reconstruct either proof. The old recovered-no-start test is inverted:
weaker facts must fail to release rather than converge. Numeric recovery never
gains signal authority. Concretely,
`TestRecoveredNoStartUnresolvedConvergesWithoutExitEvidence` is renamed and
inverted to require `AttemptResult`: absent/corrupt spool returns the typed
unresolved result with zero provider/session/runner mutation.

A crash immediately after deterministic stage creation but before the
`prepared` commit leaves the Change `reserved` without durable stage identity.
Recovery may take `reserved -> abandoned` only after every child authority is
positively gone, the final name is absent and repeated descriptor-relative
checks prove the deterministic stage name stably absent around a retained-
parent identity recheck. It fsyncs the exact Change parent and checks both names
absent again before the Store commit. Any present, replaced, unstable or
ambiguous stage has no durable identity and remains `reserved` with the run
finalizing; recovery never deletes it. No stage identity, receipt, table or
durable unresolved phase is added merely to reclaim this crash residue.

Each such `reserved` residue belongs to the one exact Change of one admitted/
finalizing worker run. For configured capacity `C`, its count is therefore:

```text
reserved residue count <= nonterminal worker runs
                       <= all nonterminal runs <= C <= 1024
```
Terminal retained Changes need an explicit retention/count/byte policy before
cutover; until that policy is
chosen, their aggregate storage is an acknowledged cutover blocker rather than
an admission reason. The intended residue is empty because population is not
released before the `prepared` commitment; accepted materialized trees are
later bounded by the central scanner's entry, byte and depth limits. Those limits do **not**
bound a same-UID-replaced present `reserved` stage that recovery correctly
refuses to traverse. A storage-byte bound for that adversarial residue is thus
an explicit missing implementation/cutover gate, visible in diagnostics and
fail-closed, not a claim that existing scanner limits cover it. The plan adds
no auto-delete remediation system or durable stage identity to hide that risk.

`prepared -> abandoned` first requires positive absence of every Change-
mutating child, outer runner, provider group and retained Change-parent/stage
descriptor authority. A live or uncertain owner leaves the Change `prepared`
and the run finalizing before recovery inspects or removes a name. Only then
does final absence permit exactly two stage facts: stable prior absence, or a
present name matching the persisted stage identity that is descriptor-removed.
Both paths fsync the parent and recheck both deterministic names absent before
the Store commit. Recovery is cut and restarted after removal but before parent
fsync, and again after fsync but before the Store transition; neither cut may
adopt or leak a replacement.

A published `prepared` Change instead has two `prepared -> available` arms:
the normal admitted worker is blocked at the publication checkpoint, while a
finalizing recovery may proceed only after every Change-mutating child
authority is positively gone. The normal arm runs the one central durability
scanner over the exact populated stage, fsyncing all accepted file data and
directories before the no-replace rename. The recovered-final arm runs that
same scanner over the exact final tree; it never duplicates a digest-only
recovery path. Both arms require the final name to match the persisted device/
inode and bounded commitment and require the deterministic stage positively
absent. After rename/final checks, each arm fsyncs the exact retained Change
parent, then revalidates that parent descriptor, final binding/content
commitment and stage absence before the Store commit. Dirty file pages,
unflushed tree directories, a simultaneous/reappeared/replaced stage, failed
fsync or changed post-fsync fact leave the Change `prepared` and the run
finalizing. Recovery then settles `available -> retained` without
activating or replaying a provider only through the null-`RunningAt` arm: exact
identity and digest/count/bytes must still match and the stage-absence proof is
repeated. This publication-recovery settlement is permitted only after the
matching AttemptResult was successfully consumed, the runner resource released,
the terminal session closed and every other Change-settlement precondition
holds. A run with nonnull `RunningAt` instead uses the one central durability
scanner described above because provider write authority existed; it may
replace only those three content facts, never identity. `inner.activate`
selects neither arm. Uncertainty leaves the factual phase unchanged and the run
finalizing; no durable `unresolved` Change phase is added.

`FinalizeRun` requires exact `retained` or `abandoned` settlement by the
current run for its first `finalizing -> terminal` write. A replay of an
already-terminal historical run remains valid after a later retry clears
settlement and reopens the same Change; validation must not require every
historical terminal run to match the Change's latest phase/revision or to
postdate its latest `updated_at_ms`.

The settlement relationship uses one composite foreign key from
`changes(settled_run_id, id)` to the corresponding unique
`runs(id, change_id)` key. The schema test must prove this circular fresh-schema
relationship with foreign keys enabled. Worker runs have nonnull `change_id`
and `admitted_change_revision`; orchestrator runs have neither.

Materialization authority remains descriptor-only. Each production launch
commitment contains one concrete `changeParent` capability; generic
`ExtraFiles` packing never chooses its descriptor. At each exec gate the
control descriptor is staged at FD 11 and the retained Change parent at FD 12;
the gate validates both, remaps control to target FD 3 first, remaps the Change
parent to target FD 11, then closes FD 12. The outer one-shot attempt runner
receives target FD 11, passes it to the inner worker through the same staging
arrangement, closes its duplicate immediately after successful child
preparation and has no respawn path. The worker validates target FD 11, sets
`CLOEXEC` immediately and closes it before provider exec. Git/provider
descriptor censuses must exclude it. The package-test final-check seam sits
above these production descriptors instead of consuming or renumbering them.

Required Change mutations include an optional or reused candidate, comparing
that candidate on existing-row reuse, timestamp/latest-run predecessor
selection, caller-supplied phase/revision/path, retry outside canonical task
selection, a loose `>` revision comparison, wrong exact delta or missing
same-transaction invalidation/run binding, and accepting an intermediate replay
after later progress. The exact schema allowlist also kills reintroduction of
unused `repository_dev`/`repository_inode` Change columns. Attempt-result
mutations sole-Wait the leader before positive birth-pinned group absence,
signal/probe the numeric group after Wait, publish before serial final drain/
PTY close, mint `inner_unregistered_converged` outside the launch primitive or after
`cmd.Start` returned nil, reject a valid pre-ready or post-ready
`inner_converged`, pretend a nondurable readiness frame distinguishes the
declared spawned-child arm, accept a post-ready identity mismatch, publish
without positively acquired birth identity, emit on uncertainty, overwrite the
fixed spool, trust frame payload, wait for an ACK, respawn, or ignore attempt/
runtime/inode/digest disagreement. Consumer mutations trust the publisher's prior
fsync, omit file/directory promotion or one revalidation, reject a complete
canonical pre-fsync residue, or repair partial/malformed/replaced bytes.

Store mutations permit any generic per-row provider activate/release/releasing/
unresolved update, commit a split pair, allow a generic outcome/cancellation/
infrastructure failure while an active runner still has a declared empty
provider pair, move that pair to releasing, move fewer than all four active
resources or the terminal on ordinary first outcome, or omit the exact runtime/runner
two-row move during admitted/running result consumption. The matrix tests also
kill rejection of either admitted `inner_converged` arm, failure to bind the
result identities while releasing a declared spawned-child pair, omission of
`ProviderExit` from either converged arm, acceptance of an active
`inner_unregistered_converged` or mismatched identity, acceptance of declared rows plus a
present `inner.activate` marker, requiring that marker for a valid active pair,
inventing `ProviderExit` for an empty pair, overwriting the first proposal,
treating a running provider exit as success, choosing the wrong `FailureSpawn`/
`FailureActivation`/`FailureProviderExit` mapping, writing ProviderExit sequence
other than 1, collapsing code/signal, replacing its Store timestamp on replay,
rejecting a matching result in finalizing/releasing or finalizing/unresolved,
accepting declared/active runtime, runner or terminal facts while finalizing,
closing a terminal during result consumption, or settling Change before result
consumption, runner release and terminal closure. Runner-start mutations omit
`BeginRunnerStart`, allow it with an existing proposal, let a generic outcome
mutate `starting`, accept `starting` on another resource or with identity,
infer no-child after crash/ambiguity, permit Start while a
declared-runner outcome commits, fail to preserve cancellation/other first
proposal, accept an inexact Start error, bind success without exact PID/birth,
or change anything other than runtime one, process resources three and terminal
one in either exact no-child transaction. Provider-start mutations skip the
one prepared inner Start on cancellation or daemon EOF, cross `inner.activate`
without daemon release, create a finalizing declared pair, fail to retry the
pending outcome after ready/activation, or leave a live child after EOF
convergence.

Recovery mutations signal, consume absent or corrupt result evidence,
reconstruct no-start from outer PID absence/EOF/flock/empty rows, close a
declared/active terminal, close before runner release or without exact result/
output proof, accept spool absence before exact Store postconditions, or refuse
stable post-unlink absence after those postconditions.

Filesystem/process mutations include a crash after stage mkdir before
`prepared`, deletion of any present `reserved` stage, abandonment while that
name is present/replaced/unstable, failure to accept stable absence, skipped
publication-parent fsync or any post-fsync parent/final/content/stage
revalidation, and `available` with a simultaneous/reappeared/replaced stage.
They also include initial publication without central-scanner fsync of one
regular file, directory or tree root, accepting dirty-page/directory loss after
crash, prepared abandonment with any live/uncertain Change-mutating child/
runner/group/descriptor authority, publish recovery that replays activation,
publication settlement before result consumption/runner release/terminal closure, adopting
a different final inode, rewriting content facts when `Run.RunningAt` is null,
or duplicating/weakening the one central scanner. Its initial-publication and
nonnull-settlement tests individually kill omission of a regular-file,
directory, tree-root or parent fsync and omission of the full stable identity/
content/final-binding/stage-absence recheck. Further mutations use
`inner.activate` as exec/write proof, rewrite retained facts from a replacement, permit retained retry exec
after digest/count/bytes mismatch, leak FD 11 to Git/provider, retain the outer
runner's FD 11 duplicate, or add a second daemon-hosted worker mode. Historical
terminal replay rejected after one or multiple retries is also a required
killed mutation. Each temporary mutation must fail its focused effect/state
test before this contract is accepted.

Open implementation risks are the real filesystem-proof/Store-commit crash
gaps, common-consumer AttemptResult durability promotion, unreaped-leader/group/
Wait order, the total pair-atomic run/result matrix, durable runner-start
serialization/uncertainty, terminal close after runner release, initial tree/
publication-parent/retained-tree durability, consumption/post-unlink removal
cuts, null/non-null-RunningAt Change settlement, exact descriptor-remap behavior
through both gates, the circular
settlement foreign key under actual SQLite enforcement, and historical-run
validation after later retries. A retained-tree rescan may also find provider-
created unsafe content; that remains visible and nonterminal rather than being
silently abandoned.

#### Exact dependency order from this checkpoint

1. Implement and independently review the corrected install-owned Local API
   endpoint and authority contract.
2. Obtain independent exact-contract `ALLOW`, implement the corrected Change
   disposition/schema contract, then FD 11 worker handoff and retry
   reinspection in that order.
3. Add one concrete `cmd/factoryd` root and recovery coordinator; only after
   recovery converges add one scheduler. Prove isolated restart/crash cuts.
4. Complete `factoryctl` service/recovery, release packaging and the exact-
   artifact private-site integration.
5. Run the dedicated whole-runtime elegance/DRY/YAGNI audit, mutation matrix
   and five independent final reviews. Delete the Rust local runtime only after
   every hard-cutover gate is green on one exact head.

At the older checkpoint `497ecfe4`, no production `cmd/factoryd`, installed
service, final private-host product loop or hard cutover existed. The later
`359d46a3` composition evidence is recorded in the current pre-cutover status
above and does not make that historical checkpoint shipped.

### Historical exact-head checkpoint (later 2026-08-28, `c732f103`)

This was the current status before Operational Store integration. Its artifact
evidence and then-frozen contracts remain useful history; its candidate labels
and dependency order no longer describe the current head.

- Canonical source head is
  `c732f103cf8870e559aafe74ed0765f9a87745a8` on unpublished branch
  `go-hard-cutover`. It includes the reviewed operational-home authority,
  browser-runtime termination observation and public-artifact gate repairs.
  This documentation update is being prepared in its own worktree; it does not
  claim a newer production head until that commit is integrated and its gates
  are rerun. `main`, remotes, the private site, the operator installation,
  live service/socket/home and real providers remain untouched.
- The exact clean source head passed `./scripts/go-check.sh` in `51.63s`:
  kernel `24.813s`, browser protocol `0.536s`, SQLite contract `10.985s`,
  TypeScript typecheck and all `188/188` web tests. The separate frozen-offline
  public-artifact proof passed `13/13` in `109.84s`.
- The installed TypeScript 5.8.3 tree matched the reviewed SHA-512
  `d981b85faf14b893ded04d707227b9b85620cd649f092192cc7e4ff9b0bf648938615058b256400a0479b318639dda19e89e75d02a1d3b7f5a55825be7c59c6f`.
  Its tree root was mode `0755`, `package.json` was `0644`, and `bin/tsc`
  was `0755`. The gate causally reconstructs an absent or stale dependency
  tree, verifies exact content and modes, and proves a second reconstruction
  uses the pinned package-manager cache with network disabled.
- The artifact gate keeps the fast and authoritative web stages owned by their
  direct callers. TERM joins the exact stage supervisor/process group before
  scratch cleanup; restoring the former process-owning wrapper is killed by
  the signal fixture. Scratch and dependency-tree identities are fenced around
  move, digest and discard, failed discard/removal propagates, and the
  production `go-ci` path cannot select an ambient replacement library. The
  independent exact-head reviewer returned **ALLOW** before these commits were
  integrated.
- The seven-member operational Go home remains integrated through `8a0e53a7`.
  `home.lock` and `.anchor` are the same exact two-link inode; the lifetime
  lease and retained database/runtime/Change capabilities preserve ancestry
  authority and release the lease last. `46850bca` adds the narrow observation
  needed for a composition root to detect browser-runtime termination. Neither
  commit creates a production `cmd/factoryd`.
- The operational Store binding is a separate candidate, currently through
  `82e03a8d`, and is **not integrated or shipped**. It is waiting for a fresh
  independent exact-head `ALLOW` after earlier reviews correctly blocked
  partial-pool activation and shutdown-uncertainty authority loss. No Store
  evidence from that worktree is counted as canonical until review,
  cherry-pick and canonical focused/race gates all pass.

| Contract or gate | State at `c732f103` | Next proof boundary at that head |
|---|---|---|
| operational home and public artifact gate | integrated and independently reviewed | rerun after this record lands; retain exact offline artifact bytes/modes |
| operational Store binding | candidate only at this historical head; not evidence for the current integrated Store | retain every partially opened pool/file owner and classify hidden close uncertainty |
| `LocalAPIAuthority` | reviewed contract frozen below; no implementation | capability-bound server listener and stale-socket causal matrix |
| `RuntimeParent` | reviewed contract frozen below; no implementation | lifetime parent capability, child-operation join and recovery matrix |
| Change disposition/descriptor handoff | then-proposed literal table; later audits BLOCKED its revision, abandonment and recovery rules | superseded by the latest corrected architecture above |
| standalone daemon, recovery and scheduling | not implemented | concrete `cmd/factoryd`, restart/crash cuts, then one scheduler |
| service/release/private host | not cut over | isolated install/service proof and exact public-artifact site integration |
| final elegance and deletion | deliberately not started | whole-runtime DRY/YAGNI audit, mutations, exact-head reviews, then Rust deletion |

At `c732f103` the weighted estimate remained approximately 42 percent. That
work strengthened already-credited operational-home and gate evidence without
completing a new black-box daemon, recovery, service, private-host or cutover
gate.

#### Frozen operational Store boundary (candidate, not shipped)

`OperationalHome.OpenStore(ctx)` is the only intended activation entry. It
hands the retained database capability to one concrete Store, eagerly opens a
finite one-writer/four-reader physical pool with no lazy reconnect, proves
`PERSIST_WAL`, and retains the exact main/WAL/SHM and ancestry identities until
`Store.Close`. Checked-out connections are fenced. The home closes the Store
before any other member and releases its lifetime lease last.

Activation derives one bounded ten-second context from the caller. As soon as
the first physical pool exists, an owning Store exists; partial fields close
safely. File authority is installed before any post-pool validation. If any
pool/file shutdown is uncertain, the call returns the still-owning Store with
an error satisfying `install.ErrUncertain`; installation retains it and the
home lease rather than discarding authority. Repeated close returns one stable
uncertainty. No independent writer may invent another transaction/pool pattern,
path reopen or repository layer.

#### Frozen `LocalAPIAuthority` boundary (planned)

`install.OperationalHome.LocalAPI` mints one `install.LocalAPIAuthority` by
duplicating the already-retained operator-token and socket-parent descriptors.
The capability is immutable and owns their lifetime. `Verify` rechecks the
home/root ancestry, both retained objects, exact token identity and content,
and the socket-parent binding immediately before each authority effect.
Mutation poisons the local API; V1 has no hot token rotation or fallback token
path.

The server entry is one concrete call:
`api.Listen(authority *install.LocalAPIAuthority)`. It never reloads server
authority from an operator-token pathname. `factoryctl` remains a
path-validating client because it cannot possess the daemon's retained home
capability. The one socket is the fixed reserved leaf
`runtimes/factory.sock`; the runtime-parent grammar must exclude that leaf from
runtime IDs. Inspection, mode checks and removal are descriptor-relative.
Darwin still requires an absolute pathname for `bind(2)`, so that path is a
non-authoritative locator followed by exact binding rechecks, not proof of
ownership.

Stale-socket cleanup is intentionally narrow: current EUID, exact socket type
and stable identity across the probe, plus exact `ECONNREFUSED`. A live peer,
permission error, changed identity, malformed object or any ambiguous result is
retained. There is no compatibility socket, second server token, generic
listener factory or server-side path reopen.

#### Frozen `RuntimeParent` boundary (planned)

One `OpenRuntimeParent(ctx, install.MemberCapability, locator)` consumes the
retained operational-home capability; the locator is diagnostic only. Every
filesystem authority effect begins from `MemberCapability.Open`. One
lifetime-held `.runtime.lock` protects the parent, replacing separate Create
and Open paths and all per-operation reopen/flock choreography. A missing lock
may be created only in an otherwise empty parent; a populated parent without
it fails unchanged.

Beginning an operation holds the parent gate until that exact operation closes.
The parent maintains concrete child ownership/refcounts so `RuntimeParent.Close`
cannot race or abandon a begun operation. A `Runtime` retains only its parent
owner plus its exact child capability; it does not copy a parent pathname,
identity or lock. Darwin `F_GETPATH` and an absolute reopen may remain useful
diagnostics but never authorize create/adopt/observe/remove. Do not add a
platform interface, a second parent type or a lock framework for this one
Darwin implementation.

#### Superseded Change disposition table (historical; do not implement)

The later read-only audit **BLOCKED** this literal table. In particular,
“preserving the retained revision” is unsafe, retry transitions need new
revisions and same-transaction invalidations, and abandonment cannot commit
while a registered child may still create or publish. The corrected current
contract above supersedes every conflicting statement in this historical
subsection; Git history retains the original planning record.

The then-proposed design uses deterministic names derived from `ChangeID`
under `OperationalHome.Changes`. It deletes durable `source_root` and
`staging_root` path columns, their uniqueness/overlap validation and the
intermediate `selected` phase. Materialization authority is descriptor-only.
The registered Change worker receives the exact retained Change directory as
fixed FD 11; provider and Git descendants inherit no Change descriptor. A
pathname may exist only as a non-authoritative diagnostic label or
`GIT_CEILING_DIRECTORIES` value.

There is deliberately no durable `unresolved` Change phase. Filesystem/process
uncertainty leaves the last positively established Change phase unchanged,
keeps the run `finalizing`, and uses the existing resource uncertainty where
applicable. The factual transitions are:

| Transition | Exact committed proof |
|---|---|
| none -> `reserved` | admission binds the deterministic Change identity and exact run/task facts |
| `reserved` -> `prepared` | exact Git selection and exact empty staging identity commit before any blob read |
| `prepared` -> `available` | atomic no-replace publication followed by an independent exact plain-tree scan |
| `available` -> `retained` | superseded: also requires exact exit/reap evidence before rescan and settlement |
| `reserved` -> `abandoned` | superseded: requires child-release/exit/reap proof, absence fsync/recheck and exact current-run settlement |
| `prepared` -> `abandoned` | superseded: requires the same child proof, exact removal/absence and clearing obsolete facts before exact current-run settlement |
| `retained` -> `available` | superseded: retry preserves content facts, clears settlement and advances N to N+1 |
| `abandoned` -> `reserved` | superseded: retry clears settlement and advances N to N+1 |

`FinalizeRun` requires that the run's Change is `retained` or `abandoned` and
that `settled_run_id` equals that exact current run. Each run records
`admitted_change_revision`. A retry of retained work performs no Git selection
or materialization: admission binds the exact current post-transition revision
and retained facts, then the worker descriptor-opens and fully rescans the tree
immediately before exec.
There is no scan/copy/scan solely for retry; a copy remains verification work,
not Change admission bookkeeping.

#### Historical dependency order from this checkpoint

1. Obtain independent exact-head `ALLOW` for the operational Store candidate,
   integrate it, and rerun canonical focused normal/race tests.
2. Implement and independently review `LocalAPIAuthority`.
3. Implement and independently review `RuntimeParent`; reserve the API socket
   leaf and remove path/F_GETPATH authority before changing shared runner code.
4. Implement the corrected Change disposition/schema contract, then FD 11
   worker handoff and retry reinspection in that order.
5. Add one concrete `cmd/factoryd` root and recovery coordinator; only after
   recovery converges add one scheduler. Prove isolated restart/crash cuts.
6. Complete `factoryctl` service/recovery, release packaging and the exact-
   artifact private-site integration.
7. Run the dedicated whole-runtime elegance/DRY/YAGNI audit, mutation matrix
   and five independent final reviews. Delete the Rust local runtime only after
   every hard-cutover gate is green on one exact head.

No production `cmd/factoryd`, installed service, final private-host product
loop or hard cutover exists at `c732f103`.

### Historical exact-head checkpoint (earlier 2026-08-28, `25eb8ea`)

This checkpoint superseded the earlier branch-inventory table when it was
written. It remains historical evidence only; the `497ecfe4` checkpoint above
contains the then-current historical status, corrected Change contract and
dependency order.

- Source head: `25eb8ea47a63ab0e68ad41bc1bf35a71aa233db8`
  on branch `go-hard-cutover`, unpublished and 378 local commits ahead of the
  old `origin/main` base. This plan update is intentionally uncommitted while
  it is reviewed, so the worktree is not currently clean. `main`, remotes, the
  operator installation, live sockets/services and real providers remain
  untouched.
- Immediately before this documentation edit, `./scripts/go-check.sh` passed
  on that clean source head: format, vet, focused kernel/browser-protocol/
  SQLite-contract tests, frozen web install, TypeScript checking, 188 web tests
  and `git diff --check`. This is pre-edit evidence and must be rerun after the
  plan commit before it is called current-head proof.
- The exact reviewed fresh-home slice is integrated through `6c22139`. It
  atomically publishes the Go home, retains descriptor/ancestry
  authority across every construction boundary, refuses adoption or repair,
  and includes real subprocess SIGKILL cuts. Before this edit, focused
  `go test ./internal/install ./cmd/factoryctl -count=1` and
  `go test -race ./internal/install ./cmd/factoryctl -count=1` passed after
  integration; the result is explicitly pre-edit evidence.
- The independently reviewed operational-home authority is integrated through
  `13167f98`. The fresh format now has seven fixed names: `home.lock` and
  `home.lock.anchor` are two names for one exact two-link inode. A live handle
  holds the exclusive lock through its complete close boundary, retains every
  fixed member and ancestry identity, exposes descriptor capabilities rather
  than path strings, accepts only paired exact SQLite WAL/SHM sidecars and
  populated `runtimes/`/`changes/`, and never repairs or traverses descendants.
- Exact reviewed SQLite image construction/inspection is integrated through
  `2bf7004`; recovered runtime/spool evidence is integrated through `c642fb9`;
  the fixed local public artifact provenance gate is integrated through
  `25eb8ea`. Before this edit, the artifact suite passed 13/13 after a frozen
  offline install and the normal TypeScript suite passed 188/188. During cold
  review of this uncommitted edit the direct TypeScript suite again passed
  188/188, while the artifact suite intentionally refused the dirty source
  tree (3 setup tests passed and 10 packaging tests refused). The exact 13/13
  artifact proof must be rerun after this plan is committed.
- The real Go/TypeScript browser PTY proof is present in the integrated tree,
  as are public React/xterm, HumanRequest reply/cancel and packed client/UI
  artifacts. This is still an in-process composition proof, not a standalone
  installed-daemon or crash/restart proof.
- Before this edit, one isolated
  `go test ./internal/runner ./internal/daemon -count=1` run passed, but an
  immediate repeat exposed a nondeterministic test-provider descriptor census
  failure on a post-exec unnamed Darwin socket in
  `TestCurrentExecUsesTransferredCwdAfterParentPathReplacement` and the
  no-extra-descriptor arm of `TestCurrentExecRejectsExactInheritedDescriptor`.
  The integrated `e8b5b82` change had fixed one earlier scan-descriptor class,
  but did not cover this Go-runtime-created socket class. The guard remains
  fail-closed while a focused causal repair is independently reviewed. A green
  full/race gate is not claimed at this source head.

At this checkpoint, cutover readiness was approximately 42 percent, expressed as a 35–45
percent range because several remaining gates contain unknown crash/platform
work. This is a weighted product-gate estimate, never a commit or test-count
estimate. “Package” means causal unit/package proof, “in-process” means the
real components are composed without a production daemon executable,
“black-box” means the installed binary survived the required external cuts,
and “cutover” means exact-head review plus the authoritative clean-checkout
gate.

| Product gate | Weight | Current credit | Evidence boundary |
|---|---:|---:|---|
| kernel and durable authority | 20 | 16.0 | strong package proof; installed recovery still absent |
| PTY and process runtime | 15 | 8.25 | real in-process owner proof; descriptor flake and black-box cuts open |
| browser protocol and transport | 10 | 6.5 | package and in-process proof; installed endpoint absent |
| public UI and artifacts | 8 | 4.0 | implementation/package proof; installed host absent |
| recovery and crash convergence | 15 | 2.25 | evidence readers exist; production coordinator/cuts absent |
| `factoryctl` and service lifecycle | 10 | 2.5 | init/web/attempt commands exist; service/recovery commands absent |
| install and release integration | 8 | 2.0 | atomic home exists; runnable install and Go release switch absent |
| private host integration | 6 | 0 | not started on the exact public artifacts |
| final reviews, elegance and cutover | 8 | 0 | deliberately last and not started |
| **Total** | **100** | **41.5** | **rounded to about 42 percent** |

At this checkpoint, the next work graph was deliberately narrow:

1. Stabilize the causal runner descriptor gate without ignoring real inherited
   descriptors, then rerun the integrated runner/daemon proof.
2. Add one install-owned operational-home handle: validate the populated Go
   layout, retain exact home/ancestry identity, acquire the nonblocking
   exclusive lifetime lock, expose only the fixed member paths/capabilities,
   and release last. Keep stopped/fresh `Doctor` unchanged.
3. Add the narrow missing browser-runtime failure-observation seam, then one
   concrete `cmd/factoryd` bootstrap root that owns the home handle, Store,
   runtime parent, private API listener and loopback browser runtime. API
   handlers must be bounded and joined; listener failure cancels the root;
   cleanup removes only the exact owned socket.
4. Add one explicit daemon recovery coordinator and causal cuts before adding
   scheduling. Only after recovery converges may one concrete scheduler call
   `RunNext`; shutdown must stop admission and join recovery, scheduler,
   handlers, browser connections, attempts, runtime parent, Store and home lock
   in their proven order. No parallel writing agent may modify shared
   `internal/daemon`, `internal/api`, `internal/browser`, recovery or
   `cmd/factoryd` files during steps 2–4. Do not add a service container,
   generic recovery/scheduler framework or interface for one implementation.
5. Prove the composed binary black-box in an isolated home: init/start/API/browser,
   deterministic shell task, daemon stop/restart, crash cuts, no replay, and
   exact process/socket/PTY/FD/goroutine cleanup.
6. Finish `factoryctl` service/recovery plumbing and replace Rust/TUI release
   packaging with the exact Go binaries and exact public client/UI artifacts.
7. After the daemon endpoint and artifact contract are stable, integrate the
   private host from its own worktree against those exact packed artifacts.
   The private repository remains a thin host and does not own protocol or
   authority.
8. Run a dedicated whole-runtime elegance audit before any Rust deletion. It
   must attack DRY/YAGNI, package graph, exported surface, duplicate validation,
   wrappers/interfaces with one implementation, test-only seams, dependencies,
   artifact-tool complexity and obsolete Rust/TUI/release residue. Every
   accepted deletion or collapse retains a causal guard and is followed by
   focused, full, race, browser-E2E and artifact gates plus a fresh independent
   `ALLOW` on the exact refactored head.
9. Run the final architecture, security/authority, process, Store/concurrency
   and simplification reviews; only then switch docs/CI/release authority,
   delete the replaced Rust local-runtime crates and perform the clean-checkout
   system census.

The production cutover remains blocked specifically on `cmd/factoryd`,
black-box restart/crash proof, service/release plumbing, private-host exact
artifact integration, the elegance pass, final exact-head reviews and the
authoritative clean-checkout gate. Exact-head green evidence must be recreated
after this plan commit. Historical green results below do not waive any gate.

Two further cold audits reviewed exact canonical head `d03491f` after the
browser-authority, runner-protocol and TypeScript foundations landed:

- `human_request_contract_audit_v2` blocked the ambiguous generic request/action
  wording and allowed only the question-first, one-table contract recorded
  below. It removed duplicate ownership identifiers, arbitrary public prose,
  the action table, speculative action kinds and automatic uncertain replay.
- `daemon_terminal_backend_audit` blocked WebSocket implementation until one
  joined live-attempt owner, a bounded command mailbox, per-observer cursor
  routing, lease-install failure revocation and non-abandoning shutdown are
  explicit. It confirmed that the runner ring can remain the only replay copy
  and that no generic pubsub/backend service layer is needed.

Both audits were read-only and returned their causal/mutation matrices. They
could not execute Go tests in their isolated shells because `go` was absent
from those PATHs; their conclusions are design evidence, not test evidence.

### PTY and durable terminal-session checkpoint

Canonical head `9c91731d5fbbb5589fbfbb06e1b29aa567ecf193` contains two
independently reviewed foundations for the revised runtime:

- `c4b83a2` and `69881e1` add the dependency-free Darwin PTY primitive. The
  child receives a fresh session, controlling terminal and process group but
  remains behind the existing register-before-exec gate. Ownership is installed
  immediately after `Start` so a later slave-close failure still kills and
  reaps the exact child. Darwin PTY hangup drains buffered output and becomes
  EOF. The independent process reviewer returned **ALLOW** on the exact source
  commit before integration.
- `0a52298`, `ddfaef7` and `9c91731` add the strict `terminal_sessions` row and
  its Store lifecycle. Admission creates one declared session in the same
  transaction as the run and resources. Activation requires exact run/session
  identities and revisions and changes both authorities in one transaction.
  Declared no-start, live active and recovered unresolved closure remain
  separate concrete operations; recovered numeric absence cannot pass a live
  close. Missing rows, cross-table chronology disagreement and suppressed
  invalidations fail closed. `FinalizeRun` requires the session closed as well
  as every exact resource released.
- The first Store candidate was blocked because normal fixtures did not yet
  establish the new mandatory relationship. A provisional reviewer then found
  recovered absence could pass the live-close path, and exact-head reviewers
  later found a `Task` read bypass, no-start convergence gap, unchecked
  invalidation insert and an overly generic close helper. All were repaired and
  both Store/authority and slice-elegance reviewers returned **ALLOW** on exact
  source head `916c1a7`, whose tree is integrated at `9c91731`.
- Root verification on the reviewed tree: full kernel tests passed (`11.523s`),
  the full kernel race suite passed before the final narrow repair (`293.016s`),
  and the affected final-head terminal/admission/recovery/concurrency race
  matrix passed (`4.971s`). The isolated daemon API/supervisor regression
  matrix passed outside the socket sandbox (`4.078s`); vet and diff checks
  passed. The broad daemon command still encounters the pre-existing ELOOP
  runtime-fixture limitation in this harness, reproduced on base `69881e1`;
  that failed invocation is not counted as green evidence.

That checkpoint is historical, not authority for recovered no-start release.
The later receipt audit proved its convergence test fail-open when the daemon
lost transient runner evidence. The target architecture above inverts that
test: absent/corrupt `AttemptResult` plus empty rows or numeric/EOF/flock facts
must remain nonterminal.

This checkpoint does not yet provide browser attachment or live terminal data.
The next shared schema slice adds real browser clients, pairing credentials and
terminal lease columns together so no fake foreign key or temporary identity
model is introduced.

### Daemon live-attempt and HumanRequest reply checkpoint

Canonical head `d9709b9bfde97f12715695a0dc43e24b6f361d23` retains the
reviewed authority and live-owner work needed before a browser can control a
PTY:

- `4b18c38` removes the redundant public key from `AUTH_PROVE`. Pairing remains
  the only operation that installs the client key; subsequent authentication
  sends exactly the client identity and transcript signature. Go and
  TypeScript fixtures share that two-field body, and an obsolete third field
  is rejected rather than silently tolerated.
- `2266e50` adds the independently reviewed closed `request_human` API method.
  It is attempt-domain only, accepts exactly a nonzero 16-byte idempotency key
  encoded as lower-case hex plus a bounded question, and returns only the
  ordinary mutation projection. The operator client has no equivalent method,
  and callers cannot select a run, request identity, action or destination.
  The API-only exact head passed normal and race tests and received **ALLOW**.
  Direct daemon dispatch at `c29d154` also received independent **ALLOW**: it
  passes only the authenticated digest, decoded idempotency key, question and
  daemon time to the Store, returns only the mutation projection and creates no
  PTY effect or caller-selected destination.
- `0f313a9` adds a distinct runner `HumanReply` operation. It writes one
  bounded reply byte-for-byte to the exact live PTY, adds no newline, never
  retries a suffix after an uncertain write and does not borrow the browser
  terminal input lease or sequence space. Normal and race runner suites passed;
  removing the generation rule and adding a newline were both killed by tests.
- `3e32eae` through `d9709b9` install one daemon-owned live-attempt controller
  before provider release. A bounded mailbox serializes authoritative effects;
  observer attachment is session-bound and receives retained runner replay;
  infrastructure failure is serialized with those effects. Shutdown rejects
  new supervisors, actively cancels pre-live supervisors, joins their result,
  then joins any installed live owner. The candidate was blocked three times
  for controller abandonment, wrong-session attachment, a non-joined outer
  supervisor and a finalization race; the repaired exact source head
  `cedba46bbfce38b6559a7137a8c9ab2feb1fda40` received independent **ALLOW**
  before integration.
- The provider-exit fixture now exits explicitly. A PTY remains open for
  interactive input, so treating stdin EOF as provider completion would encode
  the obsolete closed-stdin process model.

This checkpoint did not yet contain the browser terminal gate. The later
daemon terminal-effect checkpoint below now proves exact lease generations,
input, resize and reserved HumanRequest reply delivery. Binary WebSocket
routing remains the next boundary.

The dedicated process lane repaired the older lifecycle failures at author
head `33c2b4b`, integrated as `ea1ee4b`. Provider release now holds the same
`operationMu` as finalization, rereads the exact running run and active terminal
session immediately before the irreversible release, and refuses a stale
release after finalization wins. Natural provider exit can consume only an
already-satisfied, valid bare `terminate` frame while it waits boundedly for
the exact terminal acknowledgement. Cancellation remains observable even when
durable cleanup reaches terminal, and terminal observation prevents a second
termination. The descendant fixture now proves one exact live TERM-ignoring
child in the provider group before cleanup and exact absence afterward instead
of relying on interactive-shell `$!` behavior. Independent review returned
**ALLOW**; the integrated six-test lifecycle matrix passed in `4.407s`.

The remaining daemon setup failures were fixture defects, not waived product
guards. Darwin's lexical `/var` alias violated the production canonical-path
contract when nominal tests used ambient `t.TempDir`. Independently reviewed,
test-only commits through `699515d` now create scoped `0700` roots directly
under `/private/tmp` for daemon runtime, Change and Change-worker fixtures.
Reverting representative helpers reproduced ELOOP/selection-EOF failures;
the integrated runtime matrix passed in `0.222s`, Change in `13.185s` and
Change worker in `5.932s`. Production no-follow and identity checks were not
changed. No full daemon or process-cleanup gate is claimed at this checkpoint.

### Browser effect-contract checkpoint

Two read-only audits against `c29d154` blocked WebSocket implementation until
the daemon effect and wire boundaries are honest:

- the browser manifest and Go/TypeScript codecs advertised 64 KiB terminal
  payloads while the runner and daemon accept 8 KiB; `9ab44c3` fixes v1 at
  exactly 8 KiB, with 8,192-byte acceptance and 8,193-byte rejection in both
  languages and an independent **ALLOW**;
- the current local API projection drops the kernel's HumanRequest projection,
  and a maximally valid kernel snapshot cannot fit the existing 64 KiB control
  frame. Browser state therefore needs its own smaller bounded canonical
  projection before `STATE_SNAPSHOT` freezes. Raising an arbitrary frame limit
  or silently truncating the existing projection is not an accepted shortcut;
- the live owner needs one correlated effect-wait loop that continues routing
  output, EOF and terminal evidence while awaiting generation/input/resize or
  HumanReply results. It must not add a controller reader goroutine, generic
  RPC layer, event bus or second replay ring.

The focused Store audit found two missing concrete operations. `d968df7` adds a
pinned, read-only exact lease check plus exact failed-install and reserved-input
revocation. Its first cleanup candidate was **BLOCKED** because an already
cleared generation returned false idempotent success to a different client.
`8853acb` deletes that branch: failed-install revocation is now a one-shot exact
holder/generation transition, succeeds even after expiry, and protects every
newer lease. Same-client and cross-client replays both conflict. If its commit
result is uncertain the daemon must converge/finalize and must not retry; this
kept the fresh schema smaller by avoiding a cleanup-receipt column or generic
operation ledger. The repaired exact head received independent **ALLOW** and
passed the canonical full kernel and focused race gates.

These were foundations rather than a terminal-effect pass. The later exact
daemon effect bridge now proves acquire/install failure cleanup, input
partial/uncertain cleanup, resize authorization, HumanReply delivery-unknown,
finalization races and terminal-before-result routing. Browser transport still
must preserve those decisions without implementing a second authority layer.

### Daemon terminal-effect contract

Canonical commits `09a2e4c` and `ae28dc8` integrate the independently reviewed
daemon effect bridge from exact repaired source head
`5e8f99bdce5f20a81c03d39b3391a79cb1ef5625`:

- every terminal mutation starts with an authenticated browser `Principal`,
  reloads its durable client gate, binds the private transport-minted
  connection identity and exact lease generation, and enters the one live
  attempt owner mailbox;
- the owner holds its operation gate through the Store transition and runner
  result. Acquire, release and input commit durable authority before the live
  effect. Input and HumanRequest reply are never replayed after an uncertain
  write; resize uncertainty revokes the generation rather than claiming a
  known terminal shape;
- a reserved HumanRequest reply follows one explicit begin/write/acknowledge
  path. A missing exact write acknowledgement becomes `delivery_unknown` and
  cannot be redirected to a later run;
- renewal validation, Store commit, returned-lease validation and the exact
  binding postcondition are one owner operation. A terminal event cannot clear
  the binding between commit and a false successful return;
- provider-terminal evidence clears writable authority immediately but is a
  positive fence rather than permission to close the controller. The live
  owner continues to consume the exact supervisor acknowledgement, preventing
  a correlated effect from racing terminal cleanup into a false process result;
- public terminal attachment and finalization use the same operation gate.
  Finalization-first emits no attachment; attachment-first finishes before the
  authority-revoking transition proceeds.

The first independent review returned **BLOCK** for two high-risk races: lease
renewal could report success after terminal evidence cleared its binding, and a
terminal event during a correlated effect could close the controller before
the exact supervisor acknowledgement. It also required a causal test at the
public attach/finalization boundary. The repaired candidate received **ALLOW**
after all three defects were closed.

The reviewer repeated the repaired race schedules ten times, the whole daemon
suite three times and the daemon race suite once; browser, kernel and runner
dependencies, vet, Linux unsupported-path compile, formatting and diff checks
also passed. The review census stayed at five descriptors and two goroutines.
Temporary mutations that removed the renewal binding postcondition, terminal
fence or public attach operation gate were each killed by their causal tests
and restored.

Canonical-head reproduction repeated the six repaired schedules ten times
(`14.716s`), the complete daemon suite three times (`60.041s`) and the daemon
race suite once (`65.482s`). Browser (`21.801s`), browser protocol (`0.276s`),
kernel (`13.532s`) and runner with an explicit isolated build cache (`21.318s`)
passed; affected-package vet, formatting, diff checks and a compile-only
Linux/amd64 daemon test binary passed. The first combined dependency invocation
is not counted as green because two runner witness builds lacked `GOCACHE` in
their deliberately scrubbed environment; the isolated-cache rerun passed. The
first Linux invocation is likewise not counted because it tried to execute the
foreign binary and correctly returned `exec format error`; the compile-only
rerun passed.

This is the daemon-effect gate, not the browser-wire gate. Structured terminal
controls, binary terminal frames, slow-client acknowledgement/backpressure and
TypeScript Session/UI effects remain blocked until they consume this exact
contract without widening it.

### Browser canonical-state contract

An adversarial read-only audit of the unchanged state/protocol files between
`3db05a0` and `33d7bbe` returned **ALLOW** for one narrow implementation and
**BLOCK** for sending the existing operator `DashboardSnapshot` through the
browser. The operator snapshot has a 1 MiB transport, can contain 4,096 total
entities and currently drops HumanRequests at its API projection. It is not a
64 KiB browser message and will remain a separate local-API contract.

Browser v1 instead uses head-pinned typed pages:

```text
STATE_GET { cursor? }
  -> STATE_SNAPSHOT { head, kind, items[0..8], next_cursor }
  -> STATE_RESTART { head, floor, reason }
  -> ERROR
```

The first request pins the current durable invalidation head. One opaque cursor
decodes to `{ head, kind, after_id? }`. Missing `after_id` means the first page
of that kind and is never represented by an all-zero identifier. Kinds traverse
a fixed order:

```text
factory -> project -> agent -> task -> human_request
```

The factory page has exactly one item and no `after_id`; its `next_cursor` is
the first project cursor with `after_id` omitted. A full dynamic page continues
the same kind after its final exact raw 16-byte ID. A short or empty page moves
to the next kind with `after_id` omitted. Empty kinds remain explicit parts of
the traversal. After the final HumanRequest page, `next_cursor` is JSON `null`.
Factory events use entity kind `factory` and literal entity ID `factory`; the
all-zero SQLite invalidation identity never crosses the browser boundary.
Every dynamic kind uses raw 16-byte identifier order.
The wire `kind` is the closed five-value union above, its item shape is selected
by that kind, every items array is present and bounded, and `next_cursor` is
always present as either a bounded opaque string or JSON `null`.

Each Store page opens one read transaction, validates the durable controls and
counts all browser-visible entities, including the singleton factory. It
refuses the entire read above 4,096, so projects + agents + tasks + unresolved
HumanRequests may total at most 4,095. A continuation succeeds only while the
current head still equals the cursor head. Any intervening commit returns
`STATE_RESTART`; the client discards staged pages rather than mixing heads. The
final page's pinned head is also the exact `after` value used to begin
subscription, so a commit between the page and subscription is replayed or
causes an explicit retention gap. Cursors are read-only locators, never
authority; arbitrary decoded values still undergo the same kind, head, ID and
bound validation.

Eight is the fixed v1 page size, not a caller-selected tuning parameter. At
the current field bounds, eight task summaries containing maximally escaped
1,024-byte titles encode in approximately 51.4 KiB including the envelope and
cursor. One maximally escaped 8 KiB private question detail encodes in
approximately 49.4 KiB. Both remain below 64 KiB and below the existing JSON
depth/member/array limits. With 4,095 dynamic entities distributed across four
kinds, the worst valid state needs at most `1 + 512 + 3 = 516` pages. 4,097
visible entities including factory return a bounded `too_large` error with no
partial snapshot.

The HumanRequest list item reuses one smaller kernel projection:

```text
id project_id agent_id task_id
created_at updated_at revision kind status
reply_max_bytes can_reply
```

It deletes duplicated project/agent/task names and fixed display prose. The
browser joins exact canonical summaries and owns copy such as “Agent needs your
reply.” Question and reply text remain absent. Only unresolved `open`,
`delivering` and `delivery_unknown` requests appear; a resolved/stale row is a
browser projection deletion even though SQLite retains its audit row. The same
write transaction that changes an unresolved request to `resolved` or `stale`
appends its invalidation with `deleted=true`; `open`, `delivering` and
`delivery_unknown` transitions use `deleted=false`. The browser never infers a
deletion by reading a private terminal status.

After the snapshot, v1 uses small durable invalidations rather than embedded
snapshots. `STATE_EVENT` has exactly two variants:

```text
entity_changed { sequence, head, entity_kind, entity_id, revision, deleted }
hidden_advance { sequence, head }
```

Public project/agent/task/HumanRequest changes identify only their safe entity
and prompt one bounded `STATE_ENTITY_GET`. A Change-only invalidation emits one
real `hidden_advance` frame with no entity ID, and the client advances only its
sequence/head. A hidden run invalidation, unknown hidden dependency, gap or
pruned history emits `STATE_RESTART { head, floor, reason }` and terminates the
subscription because derived factory/task presentation may have changed. This
can later be optimized by a direct derived refresh only if measurement
justifies the added mapping. Hidden durable sequence values are never omitted.

Entity refresh and subscription are exact:

```text
STATE_ENTITY_GET { kind, entity_id }
  -> STATE_ENTITY { head, kind, entity_id, deleted, item }

STATE_SUBSCRIBE { after }
```

`STATE_ENTITY.item` is the exact kind-selected page item when `deleted=false`
and JSON `null` when `deleted=true`. Factory uses literal entity ID `factory`;
all dynamic IDs are lower-case 32-character hex. The first subscription event
has sequence `after + 1`; every later event is exactly one greater. A gap,
prune, hidden dependency or run invalidation sends one `STATE_RESTART` and
closes that subscription. Restart reason is the closed union `head_changed`,
`gap`, `pruned` or `hidden_dependency`; arbitrary prose cannot cross the wire.
Browser `ERROR.code` adds the finite `too_large` value for the over-4,096 state
case. Entity responses, restart reasons and errors retain the same 64 KiB
encoder bound as every other control frame.

Private question detail is a separate exact-revision operation and capability:

```text
HUMAN_REQUEST_DETAIL_GET { request_id, expected_revision }
  -> HUMAN_REQUEST_DETAIL {
       request_id, revision, question, can_reply, reply_max_bytes,
       terminal_target: null | {
         run_id, session_id, run_revision, session_revision
       },
       cancel_run: null | {
         expected_request_revision, expected_run_revision
       }
     }
```

Only a durable nonrevoked `private_human_request_detail` client can read it;
`human_actions` alone is insufficient. The Store projects every field from one
pinned snapshot. An exact open request/running origin/active terminal session
may expose its observation target even to a private-detail-only client, but
reply and the one concrete cancel descriptor require `human_actions` as well.
Before projecting any target/reply/cancel metadata, that active branch reuses
the canonical run relationship validator on the same pinned connection and
requires its validated session to equal the selected session. Task assignment,
resource topology/identity, or run/session/resource chronology corruption
therefore returns corrupt state rather than plausible null affordances.
Delivering, delivery-unknown, finalizing, terminal, missing and non-active
origins advertise no target/reply/cancel authority. Impossible durable
relationships fail closed. Reply and cancel requests carry no run destination;
the Store derives it transactionally. Browser v1 has no generic HumanRequest
action union, argument bag, token, policy table, or compatibility alias.

Required mutations remove the 4,096 total guard, cursor-head equality, private-
detail capability or delayed-response revision guard; replace the cursor with
offsets; expose private question/source/token sentinels; omit a closed request's
projection deletion; publish hidden run/Change IDs; and let Go fixtures pass
while the TypeScript client drifts. Each must be killed before the state union
is accepted. No schema, generic pagination framework or browser-side policy is
introduced.

The kernel half landed at integrated head `1a562e4`. It fixes page size at
eight with a ninth-row witness, orders opaque identities by their raw 16 bytes,
includes the literal `factory` item in the 4,096-visible-entity bound and pins
every page to one exact durable head. A stale/fabricated cursor returns a typed
restart with no partial items. The public HumanRequest projection contains only
bounded identifiers, chronology, status and capability booleans; capability
plus exact revision gates private detail. Resolved and stale requests commit a
`deleted=true` invalidation in the same transaction, while delivery uncertainty
remains visible and non-deleted. Twelve deliberate guard mutations were killed
and restored. Independent review returned ALLOW after focused count-three and
race tests, a full `-race` kernel run (`320.858s`), vet and diff checks; the
unchanged integrated kernel suite passed in `13.259s`.

The browser wire/client half landed through integrated head `9b7689d`. Fifteen
closed control entries and nine exact state/detail fixtures now round-trip in
Go and TypeScript. Chronology is decimal-string/`bigint`, public and private
HumanRequest fields are disjoint and the largest hostile escaped frames remain
below 64 KiB. The client reducer publishes only one complete fixed traversal:
page cardinality determines continuation, raw IDs increase bytewise, snapshot
request IDs are lifetime-monotonic and reducer-owned, and rollback, stale
tombstones, refresh-during-staging and response replay force a finite restart.
Three adversarial repair rounds found and closed those gaps before landing.
Exact TypeScript 5.8.3 typecheck and all 28 package/clean-consumer tests passed;
integrated Go browser-protocol count-three, kernel (`13.126s`), vet and diff
checks also passed.

### Product and repository ownership

The runtime repository is MIT and intentionally owns the largest useful
contribution surface:

```text
dark-factory
├── cmd/factoryd
├── cmd/factory-runner
├── cmd/factoryctl
├── internal/kernel
├── internal/daemon
├── internal/runner
├── internal/browser
├── internal/browserprotocol
├── internal/api
├── protocol/browser/v1
└── web
    ├── packages/client     @dark-factory/client, framework-neutral
    ├── packages/ui         React product UI and xterm adapter
    └── apps/dev            runnable contributor/test host

dark-factory-site (private)
└── thin production Next.js host
    ├── domain/deployment/site-access shell
    ├── private branding/marketing/ledger
    └── exact pinned public UI artifact
```

The public repository owns protocol semantics, pairing client behavior,
browser/application state, terminal adapter, BUILDING, AGENT, NEEDS YOU,
responsive and accessibility behavior, mocks, fixtures, and real-server tests.
The public UI uses React because the existing production host is Next.js 15 and
React 19; the transport client remains framework-neutral. The dev host may use
the smallest Vite/Playwright setup that exercises the same packages without
copying product state. The public UI owns the xterm lifecycle adapter behind a
small product-neutral terminal component, with xterm.js as a deliberate peer
dependency. The private host must not reimplement that adapter; keeping it MIT
is part of the contributor contract, not merely a packaging preference.

The private site stays a thin host. It imports exact public npm versions of
`@dark-factory/client` and `@dark-factory/ui`, supplies only production hosting,
origin, site-access, deployment and branding configuration, and retains its
existing marketing and ledger pages. Runtime pairing, client credentials,
authorization and state remain exclusively daemon-owned; site login/access is
not runtime authority. The host must not copy protocol definitions, reducers,
xterm integration, scheduling policy, action authorization, or daemon state.

Each public package contains a generated build provenance record with exact
source commit, protocol version, package version and build-tool versions.
Public CI tests the packed tarballs, publishes immutable exact versions with
registry provenance, and records their SHA-512 integrity. The private host uses
exact versions with the `pnpm-lock.yaml` integrity plus a small committed
`dark-factory-ui.lock.json` naming source commit/protocol/package versions. Its
CI imports the installed provenance, compares every field and digest, runs the
browser-v1 capability/fixture contract, and causally fails on a mismatched
package, source commit, protocol version or integrity. No floating range or
unreviewed local package is accepted for the cutover proof.

`pnpm-lock.yaml` remains the sole dependency-resolution authority.
`dark-factory-ui.lock.json` is an independent human-reviewed provenance
assertion used by the integration gate; it must agree with the resolved
packages but does not select or override them.

No React, CSS, xterm or visual code enters Store, scheduler, process-kernel or
daemon packages. No private host credential or deployment behavior enters the
MIT packages. Product UI work in `dark-factory-site` begins only after the
public protocol/client/UI slice is stable, in a separate clean worktree that
preserves the current dirty main worktree.

### Revised process and PTY contract

One admitted run owns one fresh PTY-backed provider process. The runner owns
the PTY master, direct child, process group, live terminal session, output
reader, input serialization, signal authority and reap. Many authenticated
browser clients may observe; at most one holds a writable terminal-input lease.
Closing every browser must not stop the provider.

The existing process kernel is adapted, not restarted. Preserve:

- atomic admission and durable resource declaration before external effects;
- outer daemon-to-runner and inner runner-to-provider blocked-exec gates;
- exact Darwin PID/PGID/birth identity and explicit process groups;
- only the component holding the live child may signal or reap it;
- recovered numeric process identity is observation, never signal authority;
- exact absence before release, with uncertainty persisted as unresolved;
- one no-replace, fsynced `AttemptResult` spool for both never-created and
  converged inner outcomes, with Store postconditions before exact removal;
- finalizing as a real nonterminal external-uncertainty phase.

Replace the provider pipe contract with this visible order:

```text
admit run
→ declare runtime, runner, provider/group and terminal-session resources
→ create blocked outer runner and persist its exact identity
→ runner allocates PTY master/slave with close-on-exec and prepares an inert child
→ child creates a fresh session and binds the slave as controlling terminal
→ report exact provider identity and terminal-session identity
→ persist every identity and activation checkpoint
→ finish Change preparation/materialization
→ mark the run running
→ release provider exec
→ read PTY output, assign sequences and fan out without blocking the provider
→ accept leased input and bounded resize while that exact run remains running
→ revoke input and observe the exact leader exit while it remains unreaped
→ converge/kill owned descendants and positively prove birth-pinned group absence
→ sole-Wait the leader; never signal or probe that numeric group again
→ serially drain final output, join the reader and close the PTY master
→ publish and fsync the canonical AttemptResult
→ consume the provider pair and leave the terminal releasing/unresolved
→ release the exact outer runner
→ close the terminal from exact result/output/runner proof
→ remove and fsync the exact result spool
→ verify, release resources and terminalize
```

The live PTY is one concrete runner-owned `TerminalSession`, not a generic
actor or plugin. A small durable `terminal_sessions` row records only its random
session ID, exact run, lifecycle and current input-lease facts. The exact
runner resource is derived from the run's unique `runner_process` resource; a
second copied owner ID would add a cross-table consistency problem without
adding authority. The table contains no FD, PTY number, PID, PGID, terminal
bytes, output cursor or signal authority:

```text
id                     16-byte random primary key
run_id                 16-byte unique foreign key
state                  declared | active | releasing | closed | unresolved
unresolved_reason      bounded text only in unresolved
lease_client_id        nullable exact browser client
lease_generation       nonnegative and advanced on acquire/revoke/release
lease_expires_at_ms    nullable, present only with a lease holder
last_input_sequence    nonnegative, zero without a holder
revision               positive
declared_at_ms
activated_at_ms        nullable, present after activation
closed_at_ms           nullable, present only in closed
updated_at_ms
```

SQLite checks exact 16-byte IDs and the closed five-value state enum. Holder and expiry
are either both present or both absent; a holder is an exact 16-byte client ID,
is a foreign key to `browser_clients`, and a lease may exist only while the
session is active. `unresolved_reason` is
present only in `unresolved` and is 1–4096 bytes. `activated_at_ms` is absent
in declared, required in active, and optional in releasing/closed/unresolved
because a pre-session convergence closes a session that never activated; it
never precedes declaration.
`closed_at_ms` is present only in closed and never precedes declaration or
activation. Every timestamp is nonnegative and no later timestamp precedes an
earlier present one. Generation and input sequence are nonnegative SQLite
integers; an increment at the signed 64-bit limit fails closed instead of
wrapping. No holder also requires input sequence zero. Store validation derives
and checks the same run's unique `runner_process` resource on every lifecycle
read/write; the schema continues to forbid duplicate `(run_id, kind)` resources.

The legal lifecycle is `declared → active`, then `declared/active → releasing`
and, on uncertainty, `releasing → unresolved`. Only the single post-runner
transition from `releasing` or `unresolved` may reach `closed`. A finalizing run
with a declared/active session is corrupt. `closed` is absorbing and
`unresolved` is never treated as closed.
Admission inserts the declared row in
the same immediate transaction as the run, Change reservation, four existing
resources, task transition and invalidation. Activation requires the exact
session/run revision and already-registered runner/provider identities, then
changes the session to active and the run to running in one transaction.
Session lifecycle uses the existing run aggregate and run invalidation; V1
does not add a fifth terminal event/entity kind.

Lease acquisition is one guarded row update that requires `active` plus the
durable run phase `running`, a client with the finite `terminal_input`
capability, and no current unexpired holder. It advances the generation and
resets the input sequence. Explicit release requires the exact holder and
generation. Replacement acquisition treats an expired lease as revoked in the
same transaction; neither clock expiry nor replacement pretends an actor
released it. Every release/replacement advances the generation, so stale
holders cannot affect a new lease. Reserving an input sequence is a guarded Store update before the
bounded runner write. A reserved operation that lacks a complete ACK is
reported as delivery-uncertain and is never retried automatically. A known
partial write consumes the sequence, revokes the lease and reports the exact
byte count; no suffix is queued or retried. A later deliberate correction
requires an explicit fresh lease generation, which is the protocol barrier
preventing silent pipelining. If persisting the known-partial revocation fails
or its commit result is uncertain, the daemon accepts no more input, closes the
bound runner channel to invoke the existing owner-death convergence path, and
never retries the suffix. Recovery must observe the lease invalidated or fail
the run into finalizing; it never continues under the old generation. Unknown PTY delivery, ACK loss, or an uncertain
reservation transaction immediately freezes input, revokes the lease and
fails the run into finalizing when Store authority is available. If Store
authority itself is unavailable, the daemon shuts down the bound runner
channel so its existing death path freezes input and converges the owned
provider; restart recovery never restores that lease. No subsequent input is
accepted on the affected run. This is one failure path, not a durable delivery
framework.

The durable lease TTL is exactly 30 seconds. Acquisition calculates the expiry
from the daemon-supplied transaction timestamp; the browser never supplies an
expiry. Expiry means `now >= lease_expires_at_ms`. The exact live connection
that acquired the lease renews it before expiry with its client ID and current
generation; renewal changes only the expiry and is rejected if the timestamp
would move the expiry backwards. It never advances generation or resets input
sequence. A backward wall-clock jump may conservatively keep an old holder
longer, but can never grant a second writer; daemon restart clears every lease.
The control protocol therefore has explicit acquire, renew and release
operations. A per-session live owner also binds the durable client/generation
to one exact WebSocket connection, so two tabs sharing one browser-profile key
cannot both write. Disconnect best-effort releases the durable lease; expiry
and generation remain the independent safety mechanism.

`last_input_sequence` is the server-reserved sequence within one lease
generation. Each terminal input frame names the exact generation and exact
next positive sequence; the Store accepts only `last_input_sequence + 1` and
commits that reservation before the daemon attempts the bounded PTY write.
Duplicate, skipped and stale sequences fail without a write. A known partial
write whose revocation commits leaves the run running but requires an explicit
fresh lease before any deliberate correction; unknown delivery or inability
to commit revocation takes the existing fail-closed owner-death/finalizing
path. Sequence or generation increment at SQLite's signed-integer maximum
fails without state change.

An ordinary admitted/running-to-finalizing transaction with active exact runner
and provider pair clears the holder, advances the generation, revokes the
attempt credential, moves all four exact initial resources to `releasing` and
moves the declared/active session to `releasing` together. While the active
runner's provider pair remains declared/empty, every generic outcome refuses
until the mandatory one-shot inner Start resolves through ready/activation or
AttemptResult. The admitted/running AttemptResult
consumer is the ordinary direct provider-pair-release exception and still moves
runtime root, runner and terminal to `releasing` in that same transaction. The
two pre-start transactions are the other explicit exceptions: an outcome that
wins while the runner is still declared, or exact `RecordRunnerNeverStarted`
after `BeginRunnerStart`, directly closes the never-created process/session
facts atomically. Runner `starting` refuses every generic outcome/session close.
Later uncertainty may move only releasing resources/session to unresolved.
Close otherwise requires provider pair released, outer runner released and
exact authenticated AttemptResult/output-close proof. None of these mutexes or
rows creates process/signal authority by itself.

Session `revision` changes on lifecycle transitions only. Lease generation and
input sequence are the private concurrency guards for lease/input mutations;
they emit no public invalidation and do not change the lifecycle revision.
`updated_at_ms` therefore tracks the lifecycle revision rather than lease
traffic. Every lifecycle change also advances the run aggregate revision and
emits its single bounded run invalidation. On daemon start, one immediate
`ResetTerminalLeases` transaction clears every holder/expiry, zeros its input
sequence and advances every affected generation before browser service starts.
Old connections are independently invalid because `boot_id` changed.

Terminal closure after a started runner has one non-browser Store transition
and it is deliberately not `ConsumeAttemptResult`. The result consumer applies
the exact run/provider matrix above, releases the provider pair and leaves the terminal session
releasing or unresolved. The exact outer runner is then sole-waited live or
observed absent/reused without signaling during recovery and its exit/resource
transition commits. Only `CloseTerminalAfterRunner` may then reauthenticate the
same canonical AttemptResult, validate the trusted controller's bounded output
drain/master-close sequence and close only a releasing or unresolved session.
A declared/active terminal on a finalizing run is corruption. Exact replay is
idempotent; a browser call, generic close, split provider pair, missing runner
release, arbitrary boolean, frame loss, EOF, outer PID absence, lifetime flock
or empty rows without the exact spool cannot close it.

The pre-start branches close the never-activated terminal inside their own
serialized outcome transaction, not through a later cleanup helper. The
declared-runner branch preserves the exact winning proposal; the `starting`
branch accepts only the live primitive's exact Start error and installs
`FailureSpawn`. An ambiguous `starting` row closes nothing.

Admission declares the session before allocation; activation binds the
already-active exact runner identity. The live runner supplies process, PTY and
output evidence but does not write authoritative terminal closure. Recovery
may close only after the exact provider pair and runner resource are released
from typed AttemptResult and recovered-absence evidence; every weaker case
remains `unresolved`. Provider leader and group remain separate durable facts
inside their pair-atomic ownership unit because leader reap is not group
absence.

The Darwin preparation sequence is explicit and gets its own syscall spike.
The attempt runner calls `openpty` with close-on-exec, retains the master, and
passes only the slave as child descriptors 0/1/2. A trusted inert provider
wrapper becomes a fresh session leader, makes the slave its controlling
terminal (`setsid`, then `TIOCSCTTY`/the proved Go `SysProcAttr` equivalent),
reports PID/PGID/birth/session identity, and blocks before provider exec. The
runner closes its slave copy immediately after the child is established. The
provider exec inherits only 0/1/2 and its deliberately documented private
descriptor set; it never inherits the master, control/gate descriptors or
other runtime FDs. Every descriptor is enumerated, close-on-exec tested and
censused. If `os/exec` cannot prove that exact Darwin order, use one small
Darwin helper rather than a portability or process framework.

V1 does not invent interactive authority after daemon/runner loss. A runner
owns an exact daemon-lifetime channel. Daemon death closes it; the still-live
runner synchronously freezes input, retains the unreaped birth-pinned leader,
converges/kills descendants and proves group absence, sole-waits the leader,
then drains output and closes the PTY master. Only after that order may it
publish `inner_converged` and exit without an ACK. It never signals or probes
the numeric group after Wait. Uncertainty publishes nothing. The
restarted daemon never reattaches to that terminal and returns an explicit
terminal reset plus canonical run state.

Darwin has no assumed parent-death signal or signal-capability FD. If the
runner itself is killed while its provider/group may remain, recovered numeric
identity cannot authorize a kill. The run and terminal session remain visibly
`unresolved` until later exact natural absence is proved; there is no false
terminal or automatic signal. Real crash tests use an independent fixture
safety owner, established before release, solely to clean test processes after
asserting the product's unresolved result. A clean test census must not be
misreported as product recovery. This honest fail-closed limitation is accepted
for V1 unless the Darwin spike proves a smaller OS-enforced death leash; do not
add a resident watchdog or per-run launchd abstraction merely to hide it.

Transparent reattachment to an orphaned PTY, automatic convergence after lost
runner signal authority, and durable live-session survival across daemon death
are deferred. Browser refresh/reconnect while the same daemon and runner remain
live is fully supported.

The following current details are deleted during PTY implementation:

- `ExecSpec.Stdin`, provider-startup anonymous input files, closed-stdin/EOF
  assertions, and startup-input replay semantics;
- provider-facing separate stdin/stdout/stderr pipe descriptors and forwarding
  wrappers that the PTY aggregate replaces;
- use of the old final JSON terminal spool as a live output transport. The spool
  is replaced by the bounded canonical `AttemptResult`, never terminal bytes.

Deleting provider `stdin` does not delete the autonomous run's initial task
input. It changes its ownership and delivery semantics. After the provider
release, the inner change worker sends one bounded `initial_terminal_input` to
the already-registered outer runner and waits for an exact registration ACK
before it can exec. The outer runner retains that input only in bounded memory,
then writes it once through the PTY after the worker-control handoff. Browser
input remains disabled until this first write completes. A partial, timed-out
or uncertain write terminates the attempt and is never retried; runner or
daemon loss never reconstructs or replays it. The shell adapter supplies one
line terminator when needed rather than depending on closed-stdin EOF. No task
input is placed in argv, environment, output events, the `AttemptResult`
or an anonymous provider-stdin file. This is one concrete launch step, not a
second terminal-input authority or compatibility path.

#### Private runner protocol freeze

The existing process tree is retained deliberately:

```text
factoryd
└── outer attempt-runner child (ordinary blocked process; no PTY)
    └── `factory-runner --change-worker-shell` (blocked PTY child)
        └── same PID/PGID after exec: shell, Claude or Codex provider
```

The outer attempt runner, not `factoryd`, calls `StartBlockedPTY` for the inner
change worker. It owns the PTY master, exact inner child and process group. The
inner worker performs source selection/preparation/population, then preserves
only its PTY slave on descriptors 0/1/2 while closing private preparation
authority and execing the provider in place. The daemon continues to own only
the outer runner child and its exact `AttemptController`; allocating a PTY in
the daemon would target the wrong process boundary.

One existing full-duplex private Unix control socket carries the finite runner
protocol. A second data socket would duplicate registration, EOF and recovery
semantics. Each endpoint has exactly one frame reader and one serialized frame
writer. In the daemon the per-run supervisor event loop owns both controller
read and write; browser handlers submit bounded commands through the per-run
gate and never call `AttemptController.Next` or write the socket. In the outer
runner one synchronous Darwin kqueue loop owns daemon frames, worker/provider
exit, PTY reads, the output ring and every response. No runner goroutine is
introduced.

Private framing must perform complete bounded
writes: header and body partial writes, `EAGAIN`, timeout and peer closure are
explicit outcomes. A successful method never means that only a prefix was
written. Lifecycle and terminal frames remain a small closed union; terminal
frames do not advance the attempt checkpoint state machine.

Runner output sequences count bytes as half-open ranges `[start,end)`. The
runner ring is the only scrollback copy. The daemon owns bounded per-connection
queues, not a second replay ring. `terminal-attach` names a cursor;
`terminal-credit` bounds runner-to-daemon output; insufficient retention returns
`terminal-reset`. PTY EOF is never child-exit evidence, and child exit does not
discard a buffered PTY tail. Exact child wait and owned-group convergence remain
the process authority before `AttemptResult` publication.

Durable lease acquisition commits first, then the daemon tells the runner the
exact new generation. Setting the same generation is idempotent only to recover
a lost control acknowledgement and never writes PTY bytes. A failed generation
install revokes that lease before another input is accepted. Each terminal
input command contains the Store-reserved exact generation and next sequence;
the runner accepts it once, performs one bounded PTY write and reports complete,
known-partial or failure without retrying a suffix. Revocation advances the
generation in Store before the runner is told to freeze the old one. Resize is
an idempotent exact-size command but still requires the current live lease at
the daemon gate.

Daemon/control EOF after provider release is an ownership event, not permission
to keep working: the outer runner freezes input, terminates and waits its exact
owned group, drains/closes the PTY as far as positively known, publishes final
`inner_converged` result evidence and exits. This intentionally deletes the old
behavior that allowed a provider to continue indefinitely after daemon EOF.

### Terminal ownership, replay and backpressure

The runner owns one bounded in-memory scrollback ring, monotonically increasing
output sequence and ephemeral retained floor/head per live session. No output
cursor is written to SQLite or used as recovery authority. V1 deliberately has
no durable live-output journal: browser reconnect can replay retained output
only while the same daemon and runner session remain live. Daemon/runner loss
produces an explicit terminal reset and canonical lifecycle state; only final
inner-process convergence uses the durable `AttemptResult` spool. This is at-most-once
observation, not a claim that every terminal byte survives a crash.

Output frames are binary. The daemon/browser adapter owns subscriber queues and
drops/resets a slow client instead of blocking the PTY reader or provider. The
runner-to-daemon link is also an explicit bounded credit channel: the runner
never waits indefinitely for a live-but-stalled daemon consumer. When credit
is exhausted it keeps draining the PTY into the fixed ring, advances the
retained floor, and emits one coalesced reset/head notification when credit
returns; it does not enqueue unbounded frames or block provider output. The
daemon can then resume from the retained floor or reset every affected browser
observer. Bounds are frozen by tests before release and apply to individual
frames, runner-to-daemon credit, per-client queued bytes and in-memory
scrollback. No disk scrollback is added until measurement proves it necessary
and its crash semantics are designed.

Attach names an exact run and last acknowledged output sequence. If retained
data covers the cursor, the runner returns ordered scrollback followed by live
output. Otherwise it returns an explicit reset containing the retained floor
and current head; the client discards its terminal projection. Output ACKs or
credits bound every observer. Terminal EOF is observation only and does not
substitute for exact child/group exit evidence.

Input frames carry an exact client, terminal session, lease generation and
monotonic client input sequence. The daemon grants at most one lease per run;
read-only clients cannot acquire it. Disconnect, expiry, operator revocation,
run transition from running, or daemon restart revokes the lease.

One per-run daemon operation gate serializes ordinary terminal input,
HumanRequest delivery and the first outcome/finalizing transition. An input
operation revalidates durable running state and the current lease while holding
that gate, then waits for one bounded runner write result before releasing it.
The PTY master is nonblocking for input. The runner writes only the current
bounded frame until complete, the fixed write deadline expires, or revocation/
closure interrupts it; it never hides a partial write or puts the unwritten
suffix into a background queue. The result contains the exact byte count when
known and otherwise an explicit delivery-uncertain error. Neither result is
automatically retried. This bound prevents a provider that stops reading from
holding the daemon operation gate indefinitely.

Finalizing takes the same gate, waits only for that bounded prior result, closes
the browser input gate, commits the durable finalizing/credential-revocation
transaction, advances the runner input generation synchronously, and never
reopens it. A queued stale call can run only afterward, reloads finalizing and
fails. The runner also checks accepting state and lease generation immediately
before each PTY write; no buffered write may cross its revocation barrier. The
mutex orders effects but never authorizes them; SQLite phase and exact
credentials remain authority.

The runner ACKs input only after a complete live PTY write. Partial, timed-out
or unknown results are surfaced with no retry. A client never automatically
retransmits an unacknowledged frame after reconnect because delivery is
uncertain.

Multiple readers are ordinary observers. Collaborative/multi-writer terminal
editing, durable lease survival, LAN listeners, and PTY recovery by numeric
identity are explicitly deferred.

### Durable `HumanRequest` / NEEDS YOU

`HumanRequest` is a first-class durable request for one specific human reply.
It is not a generic warning or attention score. The one daemon-authorized
`cancel_run` operation shares its browser card without creating an action
framework or widening the first question foundation.
Routine failure, delay, capacity wait, finalization stall and any condition
routine automation can resolve stay in task/run/system status.

The first fresh-schema foundation is exactly one `human_requests` table. It
stores a 16-byte request ID, exact `run_id`, 16-byte idempotency key, closed
`question` kind and `provider_question` reason code, private UTF-8
`question_text` capped at 8 KiB, positive revision and timestamps. Its closed
statuses are `open`, `delivering`, `delivery_unknown`, `resolved`, and `stale`.
Delivery ID/start time, resolution kind and close time are nullable only in the
states that use them. A unique `(run_id, idempotency_key)` makes creation
idempotent; a partial unique index permits at most one `open`, `delivering` or
`delivery_unknown` request per run. The same immediate write transaction caps
all unresolved requests at 1,024. Store only `run_id`: project, agent, task and
incarnation are derived through canonical joins instead of duplicating foreign
keys and creating mismatch states. There is no action table, arbitrary JSON,
generic request kind or dismissal state in this slice.

The bounded watch/dashboard projection contains only daemon-derived safe
metadata:

```text
id, project_id, agent_id, task_id,
created_at, updated_at, revision, kind, status,
reply_max_bytes, can_reply
```

An attempt may supply exactly one bounded private `question_text` field and an
idempotency identity. It may not supply title, summary, why text, context,
interaction labels, action choices, destinations or public fields. The daemon
derives the public card from canonical agent/task/run facts and a closed reason
code. Arbitrary agent question text is stored as private request detail, never
embedded in invalidations, snapshots, logs, metrics or errors. It is fetched
only by a paired client with the explicit
`private_human_request_detail` capability,
escaped/rendered as hostile text, and capped independently. Observation-only
clients without that capability see only safe metadata. This is an intentional
private operator channel, not a public projection or sanitization claim.

The public question card exposes no interaction union or terminal locator. Its
`can_reply` bit is display eligibility for the exact open/running condition,
never authority. The separately authorized private detail may expose the exact
active terminal target as an observation coordinate and, with `human_actions`,
reply availability plus one concrete cancel precondition.

An agent may create only a bounded private question through an authenticated
`request_human` operation. It cannot mint public card text, a button, label,
action kind, arguments or authority. The daemon derives all identity from the
exact attempt credential and decides which interactions exist.

Creation is unique for one exact `(run, request idempotency identity)`. Viewing,
subscribing or opening the terminal never resolves it. Reply accepts only the
request ID, expected request revision, and bounded text. `BeginHumanReply`
transactionally reloads the client/request and derives the exact current
run/state before returning the delivery's run to the daemon. A stale origin
never routes to a later run. Underlying-state resolution and explicit staleness
each commit once with one `HumanRequest` invalidation.

Inline PTY delivery is an external effect and therefore explicit:

```text
open
→ daemon commits delivering(delivery_id) before issuing the runner command
→ exact live runner writes once and acknowledges delivery_id
→ resolved(reply)

or, after any partial/timed-out/uncertain effect:

delivering → delivery_unknown → never replay automatically
```

The daemon owns the Store transaction and durable delivery state; the runner is
Store-blind. `BeginHumanReply` commits the exact delivery receipt before the
daemon sends one bounded reply to the live runner; the reply bytes remain
ephemeral and are not copied into durable/public state. Only a complete PTY
write ACK lets the daemon commit `resolved(reply)`. Partial, timeout, daemon
crash, lost ACK or any uncertainty commits or recovers to `delivery_unknown`;
restart converts a stranded `delivering` row to `delivery_unknown` and never
replays it. The request remains visible and non-resolved for operator recovery.
This fail-closed edge is preferable to injecting a duplicate answer.

Entering finalizing atomically changes `open` to `stale` and `delivering` to
`delivery_unknown` in the same transaction as outcome selection, attempt and
terminal-input revocation, and run/request invalidations. Terminalization may
then make residual `delivery_unknown` requests stale exactly once. No request
transition may route to a later retry run.

The one concrete cancellation is `cancel_run`, because the kernel already owns
that finite transition. A paired client with `human_actions` submits the
request ID and exact request/run revisions from the daemon-minted descriptor,
but no run ID or argument bag. One immediate Store transaction derives the run,
revalidates both sources and client capability, enters the exact run into
finalizing, revokes attempt/input authority, resolves the request as
`cancel_run`, and emits both invalidations. The result reports the
server-derived run ID and exact post-transition revisions. Still under the
daemon's operation serialization, the exact live attempt then inspects its
current binding and sends one generation revoke for that binding regardless of
the cancelling client. No binding is definitive success; rejected, partial,
uncertain and controller-failed fences remain visible after the durable commit
without rollback or retry. There is no action table, action/status result
wrapper, generic union, token, arbitrary arguments, or compatibility alias. Do
not implement approve, reject, retry, resume, publish, or permission grants
without a concrete product contract.

### Browser protocol v1
`factoryd` initially hosts the loopback WebSocket adapter in
`internal/browser`. That package owns HTTP/WebSocket upgrade parsing, Host and
Origin policy, pairing/client authentication, multiplexing, bounds and browser
DTOs. It depends on narrow daemon operations; Store, scheduler, runner and
provider packages do not import it. This boundary permits a future separate
adapter process without pre-building one now.

Structured control uses bounded versioned JSON. Terminal bytes use binary
WebSocket frames and are never base64 JSON. V1 explicitly covers:

```text
HELLO / PAIR_PROVE / PAIR_RESULT / AUTH_PROVE / AUTH_RESULT
STATE_GET / STATE_SNAPSHOT / STATE_RESTART
STATE_SUBSCRIBE / STATE_EVENT / STATE_ENTITY_GET / STATE_ENTITY
HUMAN_REQUEST_DETAIL_GET / HUMAN_REQUEST_DETAIL
HUMAN_REQUEST_REPLY / HUMAN_REQUEST_REPLY_RESULT
HUMAN_REQUEST_CANCEL_RUN / HUMAN_REQUEST_CANCEL_RUN_RESULT
TERMINAL_TARGET_GET / TERMINAL_TARGET
TERMINAL_ATTACH / TERMINAL_ATTACHED / TERMINAL_ACK
TERMINAL_LEASE_ACQUIRE / TERMINAL_LEASE_RENEW / TERMINAL_LEASE_RELEASE
TERMINAL_LEASE_RESULT / TERMINAL_RESIZE / TERMINAL_RESIZED
TERMINAL_DETACH / TERMINAL_DETACHED / TERMINAL_INPUT_RESULT
TERMINAL_EOF / TERMINAL_EXIT / TERMINAL_RESET
ERROR
```

Binary terminal input/output frames include a fixed v1 opcode, exact run or
session identifier, sequence/generation metadata and bounded payload. Exact
encoding and maximums freeze only after Go/TypeScript fixture and malformed
frame tests. Unknown versions, capabilities, messages, opcodes or control
values fail closed.

`internal/browser` owns one small consumer-side backend interface implemented
directly by `internal/daemon`. It contains only pairing/authentication,
canonical state/watch, exact agent terminal-target discovery, HumanRequest
detail/reply/cancel, and terminal attach/lease/input/resize operations. Target
discovery accepts only an observed agent ID, agent revision and state head;
it does not accept a caller-selected run or session. The only streaming member is one terminal attachment
whose owner synchronously closes and joins it. Principals contain the browser
client ID plus a private transport-minted per-connection identity; every
effect reloads durable capability and target state. The connection identity
cannot be selected by a backend or serialized to the browser.
The adapter never imports `internal/kernel` or `internal/runner`, and no service
or repository wrapper sits between daemon and Store.

The daemon now owns a registered live-attempt object outside the old
indivisible `RunNext` call. Its root owns and joins one named execution context
per admitted attempt. That
context is the sole reader and sole serialized writer of its
`AttemptController`; browser handlers and local-API handlers never read or
write the controller. It owns a bounded typed command mailbox, exact run and
terminal-session identity, lifecycle state and a direct subscriber map. It
exposes no PID, PGID, descriptor, signal or child authority.

The command mailbox, not a generic RPC/pubsub layer, lets the same owner loop
interleave runner frames and terminal operations. Each command has one
capacity-one result path and a bounded deadline. The owner never spawns an
operation goroutine and never abandons a reserved input sequence because its
caller disconnected. Once Store reserves input, the owner either obtains the
one bounded runner result or revokes that generation as unusable; it never
retries a partial or unknown effect. Durable lease acquisition precedes runner
generation installation. Failure or uncertain acknowledgement advances and
revokes that exact Store generation before any later input may be accepted.
Local-API outcome requests use this same per-run serialization path rather
than calling finalizing Store transitions around it.

The runner ring remains the only replay buffer. Each daemon subscriber stores
only its expected byte cursor plus a bounded transient delivery queue. For an
output range, exact start advances the subscriber, an already-covered end is
suppressed, a gap or overlap resets that subscriber, and reconnect replay is
routed only to the requesting subscriber. A slow observer is reset/detached;
it cannot block controller reads, PTY drain or another observer. Multiple
attachments therefore do not turn the runner's aggregate cursor into shared
browser state or require a second byte ring.

The browser-owned backend interface uses only browser DTOs for pairing/auth,
snapshot/watch, HumanRequest operations, terminal attach/detach, lease,
input and resize. A terminal attachment exposes one receive-only event stream
backed directly by the daemon's existing bounded subscriber queue plus a
synchronous `Close` that detaches and joins. This is the one place a channel is
needed so the connection owner can select terminal output with browser input;
it is not a second buffer, replay ring or generic event bus. The adapter never
imports Store or runner types. Client then run is the only gate order; the
attempt owner never acquires a client gate.

PTY EOF remains observation, not terminal/process authority. Exact child wait
and owned-group convergence precede resource release and browser terminal-exit
projection. Normal daemon shutdown closes listeners, rejects new commands,
asks every live runner to converge and synchronously joins each attempt,
connection and attachment owner. It remains visibly stopping and does not
abandon a live owner merely because a timeout elapsed. Forced daemon death is
handled by restart as unresolved numeric identity, never recovered signal
authority. Revocation commits before a non-reentrant callback closes that
client's connections.

The source of truth stays deliberately small:

- `protocol/browser/v1/manifest.json` owns protocol version, capabilities,
  control names, terminal opcodes and wire bounds, not payload schemas;
- explicit Go wire definitions in `internal/browserprotocol` own control and
  binary payload shapes; the TypeScript union is a readable client mirror,
  never a second semantic authority;
- the Go implementation produces a golden valid/error fixture for every
  manifest entry;
- TypeScript tests consume every fixture and exercise the real Go server;
- a gate regenerates fixtures to a temporary directory, compares the checked-in
  set, proves every manifest entry maps to exactly one Go decoder/encoder and
  TypeScript handler, rejects extra registry entries, and rejects drift.

This avoids a general schema/code-generation framework while satisfying the
rule that a Go protocol change cannot pass with a silently incompatible
TypeScript client.

The shared-contract commit adds these exact manifest entries. “Attach ID” is
the original required `TERMINAL_ATTACH` envelope ID reused for repeated events
only while that one connection-local attachment exists; it is not a backend or
durable identity.

| Type | Direction | Envelope ID | Fixture |
|---|---|---|---|
| `HUMAN_REQUEST_REPLY` | client | required request ID | `human_request_reply.json` |
| `HUMAN_REQUEST_REPLY_RESULT` | server | required matching request ID | `human_request_reply_result.json` |
| `HUMAN_REQUEST_CANCEL_RUN` | client | required request ID | `human_request_cancel_run.json` |
| `HUMAN_REQUEST_CANCEL_RUN_RESULT` | server | required matching request ID | `human_request_cancel_run_result.json` |
| `TERMINAL_ATTACH` | client | required request/attachment ID | `terminal_attach.json` |
| `TERMINAL_ATTACHED` | server | required matching attach ID | `terminal_attached.json` |
| `TERMINAL_ACK` | client | forbidden | `terminal_ack.json` |
| `TERMINAL_LEASE_ACQUIRE` | client | required request ID | `terminal_lease_acquire.json` |
| `TERMINAL_LEASE_RENEW` | client | required request ID | `terminal_lease_renew.json` |
| `TERMINAL_LEASE_RELEASE` | client | required request ID | `terminal_lease_release.json` |
| `TERMINAL_LEASE_RESULT` | server | required matching request ID | `terminal_lease_result.json` |
| `TERMINAL_RESIZE` | client | required request ID | `terminal_resize.json` |
| `TERMINAL_RESIZED` | server | required matching request ID | `terminal_resized.json` |
| `TERMINAL_DETACH` | client | required request ID | `terminal_detach.json` |
| `TERMINAL_DETACHED` | server | required matching request ID | `terminal_detached.json` |
| `TERMINAL_INPUT_RESULT` | server | required attach ID | `terminal_input_result.json` |
| `TERMINAL_EOF` | server | required attach ID | `terminal_eof.json` |
| `TERMINAL_EXIT` | server | required attach ID | `terminal_exit.json` |
| `TERMINAL_RESET` | server | required attach ID | `terminal_reset.json` |

Each manifest row maps to exactly one role-specific Go codec case, one checked
golden fixture and one TypeScript registry case. An ordinary response ID appears
once; an attach ID may repeat only on `TERMINAL_INPUT_RESULT`, `TERMINAL_EOF`,
`TERMINAL_EXIT` and `TERMINAL_RESET` for that live attachment. Detach or reset
retires it, and a later attachment requires a fresh request ID.

### Browser terminal wire pre-implementation audit

Two independent read-only audits at canonical head `c84f4bb` returned
**BLOCK** on calling the current adapter terminal-capable. That is the intended
pre-implementation result, not a kernel regression:

- the manifest currently registers only the binary terminal input/output
  opcodes, not attach, lease, resize, acknowledgement, reset, exit, detach or
  HumanRequest effect controls;
- the browser backend is state-only, the Go connection rejects binary frames,
  the TypeScript socket sends only strings and its Session rejects non-string
  frames;
- the existing binary terminal session ID carries no authority by itself. The
  authenticated Principal, exact run/session/revisions, private connection
  identity and current lease generation must all reach the daemon effect seam;
- the initial audit reported an output-end mismatch. Direct arithmetic review
  shows both current codecs already allow an exclusive end equal to
  `MaxUint64` and reject an end equal to `2^64`; neither suite proves both sides
  in both languages. V1 freezes that rule explicitly and adds shared fixtures
  plus mutations at the last accepted and first rejected endpoints.

The first wire slice freezes one small state machine. Every ordinary client
request has a required envelope ID and exactly one response with the same ID;
`ERROR` also echoes that ID. The client-to-server output `TERMINAL_ACK` is
unilateral and forbids an ID. Server terminal events use the original attach
request ID for the lifetime of that attachment, so no second public attachment
identity exists. Structured controls own these exact bounded bodies:

```text
TERMINAL_ATTACH {
  run_id, session_id, expected_run_revision, expected_session_revision,
  after_sequence
}
  -> TERMINAL_ATTACHED {
       session_id, floor, head, acknowledged_sequence,
       max_unacked_bytes
     }
  | TERMINAL_RESET { session_id, floor, head }
  | ERROR

TERMINAL_ACK { session_id, next_sequence } -> no response

TERMINAL_LEASE_ACQUIRE {
  run_id, session_id, expected_run_revision, expected_session_revision
}
TERMINAL_LEASE_RENEW | TERMINAL_LEASE_RELEASE {
  run_id, session_id, generation,
  expected_run_revision, expected_session_revision
}
  -> TERMINAL_LEASE_RESULT {
       operation: acquired | renewed | released,
       run_id, session_id, generation, expires_at_ms?,
       last_input_sequence, run_revision, session_revision
     }
  | ERROR

TERMINAL_RESIZE {
  run_id, session_id, generation,
  expected_run_revision, expected_session_revision, rows, cols
}
  -> TERMINAL_RESIZED { session_id, generation, rows, cols }
  | ERROR

TERMINAL_DETACH { session_id }
  -> TERMINAL_DETACHED { session_id }
  | ERROR

binary TERMINAL_INPUT
  -> TERMINAL_INPUT_RESULT {
       session_id, generation, sequence,
       status: accepted | rejected | partial | uncertain,
       accepted_bytes
     }

HUMAN_REQUEST_REPLY {
  request_id, expected_revision, reply
}
  -> HUMAN_REQUEST_REPLY_RESULT {
       request_id, revision, status: resolved | delivery_unknown
     }
  | ERROR

HUMAN_REQUEST_CANCEL_RUN {
  request_id, expected_request_revision, expected_run_revision
}
  -> HUMAN_REQUEST_CANCEL_RUN_RESULT {
       request_id, run_id, request_revision, run_revision
     }
  | ERROR

TERMINAL_EOF { session_id }             # attach ID; observation only
TERMINAL_EXIT { session_id, exit_code, exit_signal, aborted }
                                             # attach ID; observation only
TERMINAL_RESET { session_id, floor, head }   # attach ID; detach follows
```

Terminal exit has one canonical wire status arm: ordinary exit is
`exit_code >= 0, exit_signal == 0`, while a signaled provider is
`exit_code == 0, exit_signal > 0`. The daemon alone translates the runner's
exact internal `Code == -1, Signal > 0` wait sentinel to that signaled wire
shape; contradictory, negative or oversized pairs fail closed in both Go and
TypeScript.

All identifiers are exact lower-case 16-byte hex; revisions, generations,
sequences, timestamps and byte counts use their existing bounded decimal wire
forms. Rows and columns are integers in the closed range 1 through 4,096.
Reply is bounded UTF-8 text, not terminal bytes. Cancellation is one concrete
request/result pair; it has no action string or argument bag. Lease results never identify another
client and a released result omits expiry. Attach authorizes observation with
the exact Principal and revalidates the running run, active session and
supplied revisions under the same daemon operation gate that installs the live
observer.

Binary client frames carry exact session, positive lease generation, strictly
next input sequence and at most 8 KiB of bytes. Binary server frames carry the
exact session, generation zero, contiguous output range and at most 8 KiB of
bytes. For output, `sequence + payload_length <= MaxUint64`; the exclusive end
`MaxUint64` is accepted and `2^64` is rejected. Wrong direction,
unattached/wrong session, zero/overflow, unknown opcode, malformed or oversized
input closes the connection fail-closed. A structurally valid exact-session
input gets one `TERMINAL_INPUT_RESULT`; partial or uncertain input
freezes/revokes the generation and is never retried. Output reset carries exact
retained floor/head and forces a fresh rendering correlation. EOF/exit does not
authorize process completion.

Browser output uses one explicit per-attachment acknowledgement window, not a
second queue. The manifest fixes `max_terminal_unacked_bytes` at 65,536 and
`terminal_ack_timeout_ms` at 10,000. Attach starts with
`acknowledged_sequence = after_sequence`; the server advances `sent_head` only
after a successful WebSocket write and sends no frame that would make
`sent_head - acknowledged_sequence` exceed the window. It may retain at most
one already-selected 8 KiB event while waiting for credit. `TERMINAL_ACK`
advances monotonically only within `(acknowledged_sequence, sent_head]`; an
equal or out-of-range ACK is malformed and closes the connection. Credit
reopens by the acknowledged byte delta. Lost credit never blocks the daemon or
PTY: the existing attachment queue stays bounded, and the connection
synchronously detaches/closes when the ACK deadline expires. A reset or close
discards credit state; a new attach begins a new correlation.

Memory remains explicit across the two distinct links. The daemon attachment
queue is the existing 64-event queue, so its absolute output-payload ceiling is
512 KiB; replay staging remains independently capped at 256 KiB inside the live
attempt. The browser connection adds no payload queue: its 64 KiB unacknowledged
window plus at most one selected 8 KiB event is the entire adapter-owned byte
budget. Payload slices may be shared read-only until the WebSocket write
returns, then released. Tests census both the event and byte bounds.

One authenticated connection owner selects its WebSocket input, state watch
and the attachment's existing bounded event queue. It owns at most one terminal
attachment, synchronously closes/joins it on detach or socket loss and never
stops the provider. Slow-client policy may reset or detach that observer, but
cannot block PTY drain or another observer. Runner scrollback remains the only
replay ring.

The framework-neutral TypeScript surface owns a `TerminalHandle`, explicit
lease, complete input receipts, typed reset/output/EOF/exit events and
revision-bound HumanRequest reply plus the single `cancel_run` descriptor. It
does not expose connection IDs, delivery IDs, runner/process identities,
another client's lease, generic action arguments or private reply bytes in
public state. `connect()` reaching authenticated/syncing is distinct from
canonical state becoming ready; UI code must observe the ready state rather
than infer it from connection resolution.

Shared protocol ownership is serialized before implementation fans out:

1. one contract owner changes the manifest, Go control/binary codecs, checked
   fixtures and TypeScript codec registry together, including the exact-maximum
   sequence mutation;
2. after independent review freezes that commit, the Go transport owner may
   change `internal/browser` and the direct daemon browser adapter while the
   TypeScript owner changes only `web/packages/client`;
3. a real-Go-server TypeScript gate must prove attach, replay/live output,
   input/resize/lease, reset/reconnect, exact HumanRequest reply,
   `delivery_unknown`, `cancel_run` and structured errors before the public UI
   consumes the effects;
4. the public UI then owns the xterm adapter and NEEDS YOU interactions; the
   private repository remains an exact-artifact host rather than the terminal
   implementation.

Required causal tests cover replay/live ordering, gap/reset, exact 64 KiB
window exhaustion/replenishment/lost-ACK timeout, acknowledgement chronology,
stale connection/generation/revision, partial/uncertain input,
resize uncertainty, reply one-shot delivery, attach/finalization races,
multiple observers/one writer, revocation, malformed frames, slow-client
isolation and attachment/socket/goroutine cleanup. Mutations must kill the
accepted-`MaxUint64`/rejected-`2^64` endpoint boundary, removed private
connection identity, skipped lease/revision checks, input replay, reset
suppression, output past credit, equal/ahead ACK acceptance, missing ACK timeout
and detach without join.

### Browser security and pairing

The browser transport carries operator authority and terminal input. V1 must:

- bind only an explicit loopback address and refuse wildcard/LAN binds;
- validate exact `Host` and exact production/development `Origin` at upgrade;
- expose no permissive CORS or missing-Origin fallback;
- reject malformed, oversized and rate-exceeding control/binary frames;
- separate paired observation from terminal-input authority;
- issue one revocable proof-of-possession client identity per browser profile;
- persist pairing/client/revocation facts and security invalidations;
- never send the Unix operator token, attempt credential, provider credential,
  private source, raw debug data or permanent secret to browser JavaScript;
- revoke input on finalizing even if the browser/daemon has stale state.

Identity across restart is explicit. The fresh `factory` singleton owns one
random persistent 16-byte `daemon_id`; every daemon start creates a fresh
random 16-byte in-memory `boot_id`. Existing paired clients survive restart by
proving their key against the new boot challenge. An unredeemed pairing
challenge is bound to the boot that minted it and cannot survive restart.

Two small authority tables and one bounded audit table are sufficient:

```text
browser_pairing_challenges
  secret_digest          32-byte SHA-256 primary key; raw challenge never stored
  boot_id                exact boot that minted it
  intended_origin        exact allowlisted origin
  capability_mask        only daemon-minted known bits
  created_at_ms
  expires_at_ms          greater than created, fixed five-minute maximum
  redeemed_at_ms         nullable

browser_clients
  id                     16-byte random primary key
  public_key             exact 65-byte uncompressed SEC1 P-256 point
  fingerprint            unique SHA-256 of public_key
  capability_mask        only known bits
  revision               positive
  created_at_ms
  updated_at_ms
  revoked_at_ms          nullable

browser_security_events
  monotonic sequence primary key, bounded pairing/revocation/security kind,
  client reference and timestamp;
  never a challenge, signature, terminal byte, provider output or private text
```

The initial capability bits are exactly `observe`,
`private_human_request_detail`, `human_actions`, and `terminal_input`. Unknown
bits fail closed and `observe` is mandatory; the other three bits are
independent and imply no hidden authority. The owner-authenticated mint request
may select a subset of those fixed optional bits, subject to daemon policy.
The browser never submits, proposes or widens capabilities. This is one
concrete bitmask, not a generic permission framework.

Security-event kinds are exactly `challenge_minted`, `challenge_abandoned`,
`client_paired`, `duplicate_fingerprint`, and `client_revoked` in v1. Challenge consumption is
already represented by its row and does not receive a duplicate event.
Unauthenticated refusal traffic is bounded in memory rather than becoming a
SQLite write-amplification path. After appending an event, the same transaction
deletes every event older than the newest 4,096 by monotonic sequence; it never
rejects an authorized mutation merely because the audit is full. The event
contains no free-form detail. Challenge minting in the same transaction removes
redeemed, expired and old-boot rows, permits at most 32 current unredeemed
challenges, and otherwise fails closed. Thus neither table grows without a
fixed bound during normal operation.

Each browser profile creates a non-exportable WebCrypto P-256 signing key and
stores the `CryptoKey` in IndexedDB; there is no long-lived bearer/localStorage
fallback. Pairing persists only the client ID, exact public key/fingerprint,
granted capabilities, revision and revocation state in SQLite. Tabs may use
the same profile key but have distinct connections and terminal leases. If
durable non-exportable key storage is unavailable, the browser must pair again
rather than weakening the credential.

The persistent daemon ID is read from the singleton rather than copied into
every challenge row. The challenge's current `boot_id` binds it to this daemon
start; copying the database necessarily copies its singleton identity too.

After exact HTTP path/Host/Origin validation, the daemon sends a bounded v1
`HELLO` containing only protocol version, `daemon_id`, `boot_id` and a fresh
32-byte connection nonce. There are exactly two signed transcripts:

```text
PAIR domain bytes:  "dark-factory/browser/v1/pair\x00"
AUTH domain bytes:  "dark-factory/browser/v1/auth\x00"

transcript =
  domain bytes
  || u16be(1)                                      # protocol version
  || repeated(u32be(byte_length) || raw field bytes)

PAIR fields, in order:
  daemon_id[16]
  boot_id[16]
  connection_nonce[32]
  raw_challenge[32]
  public_key_sec1[65]
  exact_validated_host_utf8
  exact_validated_origin_utf8

AUTH fields, in order:
  daemon_id[16]
  boot_id[16]
  connection_nonce[32]
  client_id[16]
  exact_validated_host_utf8
  exact_validated_origin_utf8
```

The domain bytes are not length-prefixed; every following field is. All
integers and lengths are unsigned big-endian. IDs/nonces/challenge/key are
their raw fixed-length bytes, never textual encodings inside the transcript.
Host and Origin bytes come only from the already-validated HTTP request, not a
frame field. Empty, oversized, invalid-UTF-8 or length-mismatched fields fail
before signature verification.

The browser invokes
`crypto.subtle.sign({name: "ECDSA", hash: "SHA-256"}, key, transcript)` and
must produce exactly 64-byte IEEE-P1363 `r || s`. Go hashes the complete
transcript exactly once with SHA-256, rejects any signature not exactly 64
bytes, splits two unsigned 32-byte integers, requires both in the P-256 scalar
range, parses only a 65-byte uncompressed SEC1 point beginning with `0x04`,
requires the point on P-256, and calls `ecdsa.Verify`. ASN.1, compressed points,
JSON canonicalization and alternate encodings are rejected. One checked
cross-language golden fixture is required by the first implementation commit
and fixes all transcript bytes, signature bytes and malformed-length cases.
The connection nonce is single-use and authentication has a short fixed
deadline.

For pairing, the daemon hashes the raw challenge for lookup, reconstructs the
PAIR transcript from stored/current facts and verifies the proof, then uses one
immediate transaction to guard `redeemed_at_ms IS NULL`, current `boot_id`,
exact intended Origin and unexpired time, insert the client, record the
security event and mark the challenge redeemed. Exactly one affected row wins
concurrent redemption. Capabilities come only from that challenge row and are
returned after commit in `PAIR_RESULT`; the browser neither signs nor submits
them. Inside the immediate transaction the daemon first guards and marks the
challenge consumed, then explicitly checks the fingerprint. An existing
fingerprint records a bounded conflict event, commits consumption and returns a
generic conflict. Otherwise it inserts the client with the challenge's exact
mask, records the pairing event and commits. The browser must create a new key
and `factoryctl web open` must mint a new challenge after conflict. Other
transaction failures roll back without claiming redemption. A lost success
response also consumes the challenge and is never replayed.

Unexpired means strictly `now < expires_at_ms`; the exact boundary is expired.
Wrong boot, Origin, proof or expiry refuses without consuming the challenge.
The Store derives the fingerprint itself as SHA-256 of the exact validated
65-byte uncompressed on-curve P-256 point and never accepts a caller-provided
fingerprint. A duplicate fingerprint conflicts even when its existing client
is revoked; pairing never revives or replaces an identity. The loopback server
accepts one configured exact Host including its port. Host is therefore bound
by upgrade policy and the signed transcript rather than copied into every
challenge row; a challenge cannot move to a second accepted Host.

Existing-client `AUTH_PROVE` names only the client ID. The daemon loads the
stored public key and reconstructs AUTH; ordinary authentication never accepts
a replacement public key.

Authentication yields only a client-ID principal. Every operation reloads the
client row, rejects revocation, checks the exact finite capability, validates
typed arguments/expected revisions and revalidates durable target state; the
browser never supplies a trusted project, task, agent, run, request or terminal
authority identity. No per-operation signature is needed inside the
authenticated WebSocket.

A small per-client daemon operation gate orders every browser effect with the
owner-only Unix revocation path. Terminal input, HumanRequest reply/cancel,
lease acquire/release, mutation and subscription setup all acquire that exact
client gate, reload active client/capabilities, perform their bounded external
effect or durable transaction, then release it. Terminal/run operations also
take their per-run gate in the fixed client-then-run order and revalidate
session, holder, lease generation, input sequence and durable running phase
immediately before a bounded PTY write.

`factoryctl web revoke` acquires the same client gate, then one immediate
transaction marks the client revoked, advances its revision, records the event
and clears every terminal lease held by that client while advancing each lease
generation and zeroing its input sequence. It then closes all active client
connections before releasing the gate. An operation already inside the gate
may finish before revocation commits; no later browser effect begins after the
durable revocation. These gates serialize effects but never replace Store
authorization. Finalizing revokes terminal input independently of browser-
client revocation. Revocation requires the exact expected client revision. The
first successful revoke advances it once; a call using the current revision on
an already revoked client is an idempotent no-op with no duplicate event or
lease change, while a stale revision conflicts. Client revisions do not change
on daemon restart.

Lease state is not a public event entity and never exposes another client ID.
Attach/acquire/renew/release responses project only whether input is available,
whether this authenticated connection owns it, its own generation and expiry.
The live terminal hub sends bounded ownership-control updates to attached
connections; reconnect/attach reloads canonical durable state. A lost lease
hint can make controls look stale briefly but cannot authorize input because
every operation revalidates Store state. Run finalization still advances the
run revision/invalidation and sends a terminal reset/exit control after commit.

Each WebSocket connection accepts exactly one successful PAIR or AUTH proof
against its single-use nonce and short deadline. Any failed proof closes that
connection. These are in-memory transport invariants, not durable session rows.

Revocation, key mismatch, duplicate identity, old connection nonce, profile
reset, capability escalation and daemon restart are explicit tests.

`factoryctl web open` asks the owner-only Unix API to create one short-lived,
single-use challenge bound to the current boot ID and intended Origin, then
opens the hosted app with that challenge only as exact
`#df_pair=<64 lowercase hex>` in the URL fragment. A challenge
is forbidden in the path or query, including an empty query marker. The host
sends `Referrer-Policy: no-referrer` and loads no analytics or third-party
resource. Its first synchronous first-party bootstrap reads and clears the
fragment with `history.replaceState` before starting any application network
request, then registers the browser public key with the loopback daemon. HTTP
access logs, requests, referrers, telemetry, errors and copied post-bootstrap
URLs must never contain the challenge. Reuse, expiry, wrong daemon/origin/key/
client and revocation fail closed. The exact CLI recovery surface is
`web status`, `web open`, `web list-clients`, and `web revoke`; there is no
separate pairing alias.

The daemon returns a typed launch outcome. `ready` means the challenge mint
transaction committed and factoryctl may invoke its injected browser opener.
If SQLite reports an ambiguous COMMIT, the Store reconciles the exact digest
on a fresh reader: a durably observed challenge is returned as `uncertain`
with the URL fragment and SHA-256 digest still paired, but factoryctl never
opens or retries it and instead performs one exact owner-authenticated
abandonment. A commit-before-apply or otherwise durably absent challenge
returns no launch identity and the ordinary pre-mint error. If exact identity
or cleanup cannot be proved, factoryctl reports `challenge cleanup remains
unresolved`; it never chooses a digest from a malformed or mismatched URL.
Successful abandonment is an empty acknowledgement backed by the durable
`challenge_abandoned` security event and proves that no active exact challenge
remains.

The hosted app must use first-party bundled scripts, strict CSP, controlled
dependencies, no third-party analytics in the terminal context, and safe
rendering of hostile terminal bytes. These are cross-repository requirements;
the local daemon cannot enforce a remote page's CSP.

A compromised hosted origin or same-origin dependency can ask a paired,
non-exportable key to sign and can exercise that client's granted authority;
proof-of-possession prevents key export and cloning, not same-origin script
abuse. This is an explicit residual trust boundary. Mitigations are least
capabilities, exact public-artifact provenance, strict CSP, no third-party
terminal-context code, bounded daemon operations, and immediate `factoryctl`
client revocation. Before the browser surface is enabled, `SECURITY.md` must
document the incident response:
revoke affected clients, terminate their existing connections, remediate and
republish the hosted artifact, then re-pair. A compromised hosted artifact is
a release incident. A causal recovery test starts with the hosted page absent
or explicitly untrusted, uses only the owner-authenticated Unix
`factoryctl web revoke` path, and proves revocation terminates existing
connections and refuses every later proof/action from that client.

The production browser transport remains blocked until the schema and exact
transcript above have causal Store and cross-language fixture tests. OAuth,
JWT, certificates/PKI, browser bearer tokens, client-selected capabilities,
signed JSON, per-operation signatures, local TLS and a cloud relay are not
introduced.

### Hosted-origin connectivity spike

Direct hosted HTTPS to loopback WebSocket remains a go/no-go transport spike,
not an assumption. The installed environment currently has Chrome
`151.0.7922.174` and Safari `26.5`. Current browser rules around mixed content
and Local Network Access are version-sensitive, and WebSocket behavior cannot
be inferred from ordinary Fetch.

The first candidate is:

```text
https://app.darkfactory.build
        → ws://127.0.0.1:<one configured stable port>/browser/v1
```

Only `127.0.0.1` is in the first support claim; `localhost`, IPv6, random-port
discovery and Safari support require their own evidence. A stable port removes
random-port discovery/coordination; it is not a secret and provides no security.
Collision must fail visibly and be diagnosed by `factoryctl web status`.
Loopback binding, canonical exact Host+port, exact Origin and the browser's LNA
permission are request filters and user mediation; none authenticates a Dark
Factory client or grants product authority. Product authority comes only from
proof-of-possession pairing plus daemon-side authorization of every operation.
Forwarded Host or proxy headers are never trusted.

The spike uses a real hosted/preview HTTPS origin and a disposable local server
outside the live home. It records WebSocket upgrade, mixed-content/LNA
permission behavior, Origin, Host, binary data, reconnect and denial in fresh
automated Chrome profiles. It deliberately has no pairing or credential
challenge. Headed first-run prompt UX and permission persistence are later
platform tests. Safari/WebKit is not claimed without a pass.

Connectivity-spike cases include the exact `127.0.0.1` candidate, fixed versus
occupied port, granted/denied/reset LNA, exact/wrong/missing Origin, exact/wrong
Host, reconnect, cross-site WebSocket hijack and no-listener behavior. Because
plain loopback `ws` passed, locally trusted `wss` is not built. The production
browser lane later proves fresh/expired/replayed/revoked pairing, browser
refresh, daemon restart, stale output cursor reset and bounded failure UX.

If direct ws fails, choose in order: a local bootstrap HTTPS origin that hands
control back to the hosted app; a locally trusted HTTPS endpoint if its install
and renewal UX is acceptable; then a custom `darkfactory://` bootstrap only if
required. Do not add a relay, extension, service worker, LAN listener or remote
mobile path.

#### Spike result: direct loopback WebSocket selected for Chrome

The direct candidate is **GREEN** on installed Google Chrome
`151.0.7922.174` on macOS for this exact first support contract:

```text
hosted HTTPS page
  → ws://127.0.0.1:43123/browser/v1
  → exact Host 127.0.0.1:43123
  → exact hosted Origin
  → dedicated Chrome loopback-network permission
```

This does not claim `localhost`, IPv6, Safari, another Chrome version or a
random port. It does not require local TLS, a local bootstrap origin, a custom
URL scheme, an extension or a relay. Chrome 147 expanded Local Network Access
to WebSockets, and current Chromium splits the old compatibility permission
into `local-network` and `loopback-network`; the actual `127.0.0.1` grant is
`loopback-network`, not the legacy `local-network-access` alias. See the
[Chrome 147 release notes](https://developer.chrome.com/release-notes/147) and
[Chromium compatibility context](https://chromium.googlesource.com/chromium/src/%2B/f7eb223f51392d3eeb51a7d4b32db0762bf70d02/components/permissions/contexts/local_network_access_compat_permission_context.h).

Evidence used the reviewed public harness at canonical public head
`160fca4804225348356c5e9e05fdd4c6c22f3e4e`, the independently reviewed thin
private host at `7cdbee3f9c03c185295a1a5755a02c5c070d31cb`, and Vercel preview deployment
`dpl_8MqaCyGAnQ1rTfVZ7whFbQY93uyt`. The hosted artifact was byte-identical to
the public 4,513-byte fixture with SHA-512
`0efad89d96c62e937c5e21c62d52a115cba7110a97ea8e07642c89ddbcdf36879f21970a83fcc4f751b19ec07ff0e7fc236c4adcc595e2d2630e0ef3195ea0d8`.
Canonical, query and Vercel-normalized encoded aliases could never serve the
artifact without its hash-only CSP, `no-referrer`, `nosniff` and `no-store`;
other aliases returned a different error body. The preview remained behind
Vercel authentication. A test-only automation bypass was carried only in an
owner-readable cookie, never a URL, and was revoked after the matrix; the
project reported zero remaining automation-bypass credentials.

#### Current private-host deployment evidence (2026-08-28)

The separately reviewed private-host application at `1144e12b` was deployed to
the existing Vercel production project as deployment
`dpl_4cNg9XngaPLHe9vmogPEppQKpTG7`. The immutable deployment URL is
`https://dark-factory-site-4jyhnjgix-baziyers-projects.vercel.app`;
`www.darkfactory.build` serves the deployment with HTTP 200. The host consumes
public client/UI content whose source commit `88a8ab22` is an ancestor of this
canonical runtime head, with no intervening `web/` changes.

Vercel has attached the intended `app.darkfactory.build` alias, but the
authoritative Cloudflare zone has no DNS record for that host. Consequently
the application host does not resolve and its custom-host TLS cannot yet be
proved. The exact Host/Origin allowlist remains fail closed; no alternate host,
wildcard origin, relay, or secret-bearing workaround was introduced. Adding
the authoritative DNS record and then proving the exact app-origin product
loop remain cutover blockers. Deployment did not touch the operator's live
Dark Factory installation or provider credentials.

Causal Chrome results:

- Three independent fresh profiles granted `loopback-network`. Each opened the
  exact socket, exchanged a 20-byte binary frame, closed, explicitly
  reconnected, and exchanged a second 20-byte binary frame. The harness
  recorded exact Host/Origin, two `101` upgrades and two bounded binary echoes
  per profile. There was no mixed-content error.
- Granting the legacy `local-network-access` alias was deliberately tried and
  killed as a false positive: its permission query reported `granted` while
  the connection failed with `ERR_BLOCKED_BY_LOCAL_NETWORK_ACCESS_CHECKS` and
  never reached the server.
- A fresh profile with `loopback-network` denied failed with that same Chrome
  error and produced no harness event. A headless `prompt` profile remained
  prompt and failed closed; headless reset resolved denied and also failed
  closed. The actual headed first-run prompt wording/acceptance UX remains a
  platform cutover test, not an architecture uncertainty.
- A fresh granted profile loaded from `https://example.com` reached the
  harness with that exact wrong Origin and received `403`; it never opened.
- With no listener, a granted profile failed visibly with
  `ERR_CONNECTION_REFUSED`. Restarting the harness restored both binary echoes
  and reconnect in a new fresh profile without changing the hosted page.
- A second server on the fixed port failed synchronously with
  `bind: address already in use`. Exact wrong/missing Origin, wrong Host,
  malformed/oversized frames, connection capacity and partial reads also pass
  the raw-socket causal harness suite.

All harnesses, Chrome processes, sockets and temporary profiles were censused
and removed. An early disposable runner did leak two Chrome trees; the census
caught them, exact isolated PIDs were terminated, and eleven task-specific
profile directories from the exploratory runners were deleted. The replacement
runner used unconditional close/removal, and all later per-scenario censuses
were empty. The public spike's five-second idle
timeout is intentionally test-only and is not the production heartbeat policy.

Two fresh read-only reviewers rechecked exact public documentation head
`3194ab84206ad54d2f74ef057d5462889905c34e` and private host head
`7cdbee3f9c03c185295a1a5755a02c5c070d31cb` and returned **ALLOW** for the
narrow connectivity conclusion. They independently verified that the artifact
hash and strict headers still match, both worktrees are clean, no test bypass
remains, no task process/socket/profile remains, and the text no longer treats
Origin, Host, loopback or LNA as product authority. Both explicitly keep
pairing, revocation and per-operation authorization as blockers for the
production browser transport.

### Updated vertical slice

The revised kernel go/no-go proof is:

```text
fresh SQLite database
→ create project, agent and task
→ atomic admission and exact attempt credential
→ declare/register runtime, runner, PTY session and provider resources
→ prepare blocked PTY-backed provider and persist exact identities
→ materialize the exact daemon-owned Change
→ mark running, then release provider exec
→ pair an authenticated browser client
→ attach and receive sequenced binary terminal output
→ acquire one input lease, send input and observe exact provider response
→ provider explicitly creates one bounded HumanRequest
→ browser renders it without transcript/private fields
→ inline reply is acknowledged at most once by that exact run, or becomes
  visibly delivery-unknown without replay
→ one daemon-minted concrete cancellation is revalidated and committed
→ completion request enters finalizing and revokes all input
→ provider, descendants and PTY are reaped/closed
→ stable verification and resource cleanup
→ terminal state
→ browser refresh replays live retained scrollback; daemon restart returns an
  explicit terminal reset and converges without input/process replay
```

Use only deterministic shell fixtures. A browser or daemon disconnect does not
authorize replay. Real Claude/Codex sessions and credentials remain prohibited.

### Revised work graph and integration order

Shared contracts are orchestrator-owned. No two writing agents own schema,
browser manifest, terminal messages or HumanRequest transitions concurrently.

1. **Plan gate** — incorporate the three read-only redirect audits, commit this
   document, then obtain a cold plan review.
2. **Connectivity spike** — isolated loopback harness plus real hosted-origin
   Chrome evidence. Freeze direct ws or the smallest fallback before production
   browser transport.
3. **Lane A: kernel** — complete current recovery/close integration, then own
   the question-only HumanRequest schema, Store transitions, lifecycle staling,
   private detail and safe public projection. Add only `cancel_run` after that
   foundation and the daemon run gate are proven; do not prebuild action rows.
4. **Lane B: PTY runtime** — adapt `internal/runner` and supervisor to PTY,
   bounded scrollback, input sequencing/receipts, lease enforcement, shutdown
   and process/FD/goroutine proof.
5. **Shared browser contract gate** — one writer owns
   `protocol/browser/v1`, `internal/browserprotocol`, checked fixtures and the
   TypeScript codec registry until message names, directions, binary bounds and
   exact fixtures receive independent **ALLOW**. No transport/client writer
   overlaps this gate.
6. **Lane C: browser transport** — own `internal/browser` and the direct
   `internal/daemon` browser adapter: loopback WebSocket control/binary routing,
   terminal attachment lifecycle, multiplexing, backpressure and reconnect. It
   consumes the already-proved daemon effect contract and frozen wire contract.
7. **Lane D: TypeScript client** — own `web/packages/client` and real-Go-server
   compatibility tests after the shared wire commit freezes. It does not own
   the manifest or Go fixtures concurrently with Lane C.
8. **Lane E: public UI** — own `web/packages/ui` and `web/apps/dev`: BUILDING,
   AGENT, xterm terminal, NEEDS YOU, responsive/accessibility and browser-state
   tests. It depends only on the public client.
9. **Lane F: platform/CLI** — init, service, doctor, recovery, web status/open/
   list-clients/revoke, packaging and hard-cutover plumbing after C stabilizes.
10. **Lane G: private host** — separate `dark-factory-site` worktree consuming
   an exact public artifact after D/E stabilize; no daemon-contract edits.
11. **Integration gate** — revised shell-provider vertical slice, crash cuts,
    mutation matrix, authoritative race/process/browser tests and leak census.
12. **Elegance/cutover** — dedicated whole-runtime and whole-web DRY/YAGNI
    audit, independent architecture/security/process/Store/browser reviews,
    delete Rust local runtime/TUI and transitional residue, clean-checkout gate.

The old recovery branch is not discarded. After the plan and PTY contract
freeze, rebase/adapt its synchronous recovery and apply the independently
approved close patches; remove only assumptions specifically invalidated by
the PTY design.

### Revised causal and mutation contract

In addition to the existing admission/credential/finalization/Change/process
matrix, the following must exist and kill the named guard mutations:

- PTY cannot exist before admission/resource declaration; release-before-
  identity-commit mutation is killed by an external execution witness.
- Darwin cuts after `openpty`, fork, `setsid`, controlling-terminal bind, slave
  handoff, identity persistence and every parent/child descriptor close prove
  the provider never executes early and inherits no master/control descriptor.
- Input before running, without the current lease, from a stale lease/client,
  or after finalizing is refused; removing the phase or lease-generation check
  is killed by an exact fixture input count.
- A provider that never reads PTY input cannot hold the operation gate:
  nonblocking bounded writes return complete, exact partial or
  delivery-uncertain within the fixed deadline; finalizing then advances and
  no suffix/background write crosses revocation. Removing nonblocking mode,
  the deadline or partial-result propagation is killed by this fixture.
- Detach leaves provider live; reconnect from retained sequence yields exact
  scrollback/live order; removing the retention-floor reset is killed.
- Slow clients cannot grow memory/disk or block PTY/provider; removing the
  runner-to-daemon or daemon-to-browser queue/credit bound is killed by
  measured limits. One fixture stalls a still-live daemon consumer while the
  provider writes continuously and proves bounded memory, advancing retained
  floor, explicit reset and continued provider progress.
- Runner/daemon death at every PTY/gate/write/ack cut causes no provider or
  terminal-input replay, no unsafe signal and no false release.
- Daemon death proves its live runner freezes input and kill-and-waits its owned
  group. Runner `SIGKILL` with a live/descendant-held PTY proves the product
  records unresolved and does not signal; the independent test safety owner
  then removes the fixture and the report distinguishes those two effects.
- Browser refresh on a live runner replays the retained range; daemon or runner
  loss yields `TERMINAL_RESET` and canonical state, never a fabricated durable
  output replay.
- Provider descendants, PTY descriptors, control sockets and every owner
  goroutine are absent after normal/error/cancel/crash tests, with independent
  safety cleanup.
- HumanRequest creation is idempotent; viewing does not clear; restart
  preserves; stale run/revision refuses; duplicate/uncertain delivery never
  injects twice; routine failure never creates one. The same write transaction
  enforces one unresolved request per run and the fixed 1,024 global bound.
- HumanRequest delivery cuts after the daemon receipt commit, before the runner
  command, after the PTY write and before the ACK prove that the Store-blind
  runner never owns durable state and every uncertain delivery remains visible
  without replay.
- Attempts can supply only private bounded question text. Mutations that copy it
  into safe metadata/watch/log/error frames are killed by per-field secret
  exfiltration tests; clients without `private_human_request_detail` cannot
  fetch it.
- An attempt cannot mint a cancel descriptor. Removing daemon derivation or
  exact request/run precondition checks is killed by authority tests.
- Every public/browser frame is scanned against private sentinels from task
  bodies, prompts, output, credentials, source and result detail.
- Terminal/UI tests treat all bytes as hostile, disable or explicitly mediate
  OSC 52 clipboard, OSC 8 links, title changes and device/control queries, and
  bound escape sequences and binary frames without interpreting them as HTML.
- Non-loopback bind, wrong/missing Origin, wrong Host, wildcard origin,
  unpaired/expired/replayed/revoked/wrong-client credentials, read-only input,
  malformed/oversized/rate-exceeding frames are rejected causally.
- Pairing tests prove fragment-only bootstrap, no-referrer, clearing before app
  requests, and absence from HTTP logs/history/referrers/telemetry. Client tests
  cover non-exportable key persistence, fresh nonce proof, copied/mismatched
  keys, profile reset, tab sharing and revocation across daemon restart.
- A cut between input enqueue, runner generation check, PTY write, ACK and
  finalizing barrier proves no byte is written after durable finalizing.
- Every manifest message/opcode has Go and TypeScript fixtures; changing a
  message, version, capability or binary envelope without updating the client
  fails the gate.
- The TypeScript client is proved against a real Go server for pairing, state,
  HumanRequest detail/reply/cancel, attach, input, resize, binary output, reset,
  reconnect and structured errors; TypeScript compilation alone is not proof.
- Public UI tests prove revision/gap/stale-response behavior, terminal lease and
  reconnect UX, HumanRequest card rendering without transcript reads,
  accessibility and responsive layouts.
- The private host proves it imports the exact public artifact and completes
  the same pair/state/terminal/request/cancel/refresh slice without a protocol
  or reducer copy.

Required temporary mutations now also include PTY release ordering, terminal
phase/lease guard, output retention floor, slow-client bound, HumanRequest
idempotency and stale destination, delivery receipt-before-write, typed-action
allowlist, Origin/Host checks, pairing replay/revocation, public privacy, and
Go/TypeScript manifest drift. Mutation code is never retained.

### Redirect simplification and explicit deletions

Do not add `factory-tui`, Bubble Tea, Lip Gloss, keymaps, mouse support, TUI
folding or browser/TUI parity. Delete those Rust surfaces only at final cutover.
Delete generic attention and message-next-run behavior once HumanRequest proves
their replacement. Do not retain both PTY and provider-pipe interaction paths.

Do not add GraphQL, event sourcing, a generic action/workflow framework, a
terminal actor system, multi-writer collaboration, cloud relay, Wails, native
mobile, push notifications, public factory view, browser extension, speculative
custom scheme, general protocol generator, large design system, or duplicated
private-site client/reducer. The public UI is not embedded into `factoryd`; the
private site is not a second implementation.

### Revised hard-cutover gate

Rust local-runtime deletion requires all original durable-authority, Change,
verification and process proofs plus:

```text
PTY/process lifecycle and leak matrix GREEN
runner-loss unresolved/no-signal proof and independent fixture cleanup GREEN
Go race detector GREEN
hosted-origin browser connectivity contract recorded and GREEN on supported Chrome
loopback Host/Origin/pairing/revocation/security matrix GREEN
interactive terminal output/input/resize/reconnect/reset GREEN
single-writer lease and finalization revocation GREEN
stalled-provider input bound and no-post-revocation-write GREEN
HumanRequest inline reply/cancel, stale/idempotent/restart/privacy GREEN
browser protocol v1 Go/TypeScript fixture and real-server integration GREEN
factoryctl web bootstrap/recovery GREEN
public UI BUILDING/AGENT/NEEDS YOU/terminal/accessibility/responsive gate GREEN
private site exact-artifact pair/state/terminal/request/cancel/refresh integration GREEN
fresh isolated install/service/restart/uninstall GREEN
terminal Change retention count/byte policy and adversarial reserved-residue storage bound GREEN
whole-runtime/web elegance audit complete
independent exact-head architecture/security/process/Store/browser reviews ALLOW
hosted-origin compromise/revocation runbook recorded in SECURITY.md
```

If private-site integration cannot run, hard cutover stops and reports that
exact blocker; no full product frontend is substituted into daemon packages.
When the gate passes, delete the Rust TUI and replaced local crates, remove all
TUI and old provider documentation/CI, retain `control-plane/`
independently, and run final clean-checkout process/socket/PTY/goroutine/browser
client census.

The document began as the pre-implementation contract and is updated in place
as proofs, redirections, mutations, reviews, and final cutover produce evidence.
The original plan gate was committed before production Go work; the web-first
redirection above is the next plan gate.

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
- Admission is one global immediate write transaction. `AdmitNext` accepts
  fresh daemon IDs but no AgentID, task, observation or cursor; it reads durable
  dispatch and, before counting, uses one integrity predicate to validate every
  run/resource/session/Change/credential authority fact and all queued rank/
  payload/control facts. It then counts every admitted, running and finalizing
  run in the one capacity set and applies exact reason precedence/eligibility,
  selecting the
  canonical eligible task+agent by priority descending then creation time/exact
  16-byte BLOB task ID ascending, validates rather than skips its one Change,
  derives the provider launch target, binds exact task incarnation/revision, and creates the
  complete run/credential/resource/session footprint and invalidations before
  any external effect. Canonical Change corruption is the only Change-specific
  pre-admission decision. External
  repository/provider executable/configuration/auth availability is post-
  admission typed failure. `RunNext` launches only from that committed Run.
- One random credential belongs to one exact admitted/running attempt, but it
  authenticates attempt requests only while that exact run is `running`.
  Authentication derives project, agent, task, run, role, provider, Change,
  and authority. The first transition to `finalizing` deletes or
  irreversibly revokes it in the same transaction. Operator authentication
  cannot impersonate an attempt.
- The normal lifecycle remains `admitted -> running -> finalizing -> terminal`.
  Operator cancellation and any spawn, activation, source-selection,
  materialization, or other unrecoverable pre-exec failure take the guarded
  exceptional edge `admitted -> finalizing`; no state goes directly terminal.
  Finalizing represents real external uncertainty. The first durable outcome
  request wins. With an active runner and active exact provider pair its
  ordinary transaction moves the exact four initial resources and declared/
  active terminal session to releasing. With an active runner and declared
  empty provider pair, generic outcome/cancellation/infrastructure failure
  refuses until the mandatory one inner Start resolves.
  authenticated admitted/running AttemptResult consumption is the direct
  provider-pair-release exception and still moves runtime, runner and terminal
  to releasing. The exact declared-runner outcome and exact Start-error branches
  instead close never-created process/session facts in their single serialized
  transaction. Only the finalizer writes terminal; cleanup uncertainty stays
  visible and nonterminal.
- Resources follow one monotonic graph. `declared` may become `active` when
  exact external identity is bound, recording one immutable activation time.
  The runner alone adds `declared -> starting -> active`: `starting` is an
  empty-identity durable Start permit and refuses generic outcome/cancellation.
  A crash there remains visibly unresolved and authorizes no signal, retry or
  fabricated no-child cleanup.
  Non-provider resources enter `releasing` only through their concrete guarded
  transitions; an active provider pair may enter `releasing` only atomically,
  while a declared empty provider pair never does. Releasing resources may
  become `unresolved` when absence cannot be proved, or `released` on positive
  cleanup/absence proof. Exit evidence must match the exact activated
  identity and cannot predate its activation time. `unresolved` may only
  become `released` after later positive proof, never active/releasing or
  signal-authoritative. `released` and terminal are absorbing. Any
  non-released resource forbids terminalization. The provider-process and
  provider-group rows are the deliberate concrete exception to generic
  per-resource mutation: every transition after their joint declaration is a
  pair-only update with equal phases and consistent empty/exact identities;
  every generic single-resource mutator rejects those two kinds. The exact
  first-outcome/result matrices validate affected counts two plus two and roll
  back before any split durable state.
- Before runner Start, a first outcome may win only while the runner is still
  declared and then atomically preserves its exact proposal, releases the three
  never-created process rows, closes the terminal and moves runtime to
  releasing. After `BeginRunnerStart`, only an exact non-nil Start error may
  install `FailureSpawn` through `RecordRunnerNeverStarted` with the same
  process/session closure; successful Start must bind exact identity first.
- One admitted run owns one fresh PTY-backed provider process and one exact live
  terminal session. Attach/detach/input/resize are explicit authenticated
  operations; terminal bytes and provider prose never become lifecycle
  authority, and input is never replayed after uncertainty.
- Register-before-exec remains a two-owner proof: an outer daemon-to-runner
  gate and an inner runner-to-provider gate. Exact runner/provider identities
  are durable before the corresponding activation. The runner alone may
  signal its still-unreaped direct-child process group.
- A stored PID or PGID is observation evidence, never signal authority.
  `EPERM`, malformed identity, reuse, missing birth proof, or any inconclusive
  liveness result is present/`unresolved`, never absent/released.
- Runner final exit/cleanup observation remains durable and replayable across
  daemon loss through the canonical `AttemptResult`. Provider-pair consumption,
  outer-runner exit/resource release and the separate terminal-close transition
  precede exact spool removal; result consumption itself never closes the
  terminal. The runner never waits for acknowledgement. Live PTY output is
  bounded and opaque but intentionally not crash-durable in V1.
- Provider launch uses `env_clear` plus a closed allowlist of ordinary identity,
  locale, shell, path, and temporary-directory values and explicit validated
  provider-home configuration. It disables Git discovery above the Change,
  prompting, SSH, credential helpers, GitHub configuration, dynamic-loader
  injection, and ambient API/provider/proxy credentials. The attempt bearer is
  written only to an exact owner-only `0600` per-run file; the environment
  carries only that file's path. The bearer never appears in argv, environment,
  terminal input, browser frames, request bodies, logs, errors, events, or debug
  output, and a
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
- One private owner-only Unix control API serves CLI/bootstrap/attempt traffic,
  while an isolated loopback WebSocket adapter serves paired browser clients.
  Both invoke the same daemon-owned policy; neither transport owns Store or
  lifecycle rules.
- Public/watch state is bounded and excludes credentials, prompts, raw output,
  message bodies, task bodies/results, source content/paths, environment,
  private guidance, deliberation, and runner resource identities.
- BUILDING and AGENT remain the browser application's useful core, including
  queue, current work, bounded activity/status, interactive terminals, a real
  HumanRequest-backed NEEDS YOU inbox, and basic project/agent/task actions.
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
- Replace competing client bootstrap, event, response, and independent status
  projections with one revisioned snapshot/watch model shared by the public
  browser client. Gaps force resync; delayed responses cannot regress newer
  state (#341/#342/#344).
- Return task summaries from list/watch and fetch selected private detail by
  revision (#39). Add empty-state project and agent creation through the same
  API used by the CLI (#30).
- Replace display-string control flow and combined-agent read/modify/write with
  typed actions and revision-checked granular updates.
- Replace the old generic NEEDS YOU/attention projection with the question-first
  durable `HumanRequest` contract above. A request exists only for one exact
  authenticated running attempt that explicitly needs a human reply. Failed
  runs, finalization stalls, capacity waits, paused agents, exhausted budgets
  and blocked tasks remain canonical status unless a later concrete
  daemon-owned action contract proves they require an operator decision. The
  sole concrete operation is exact revision-checked `cancel_run`; no
  `ResumeAgent`, `ResetBudget`, `RetryTask` or generic action rows are carried
  forward merely because the Rust projection once named them (#199, #358,
  #362).
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
- Preserve the admission order as a frozen global product rule:
  `priority DESC, created_at_ms ASC, 16-byte task id BLOB ASC`. Admitted,
  running, and cleanup-stalled finalizing runs all consume the one Store-owned
  factory-wide capacity set; independent unique constraints permit at most one
  nonterminal run per agent, task incarnation, and Change. The same immediate
  transaction validates global settings and runs the one capacity/authority/
  queued-rank-and-payload integrity predicate before ordinary
  dispatch/eligibility reasons.
  Valid paused agents, exhausted durable budget and open-run conflicts make
  queued work ineligible; task status defines queue membership, and either
  valid role remains eligible while determining the footprint. Malformed facts
  fail `ErrCorruptState`. The guarded task update, Change, credential,
  resources, run, session and invalidations commit with the global selection.

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
- Keep one exhaustive concrete `provider.Build(Request) (Launch, error)`
  constructor for every V1 provider. It returns only the exact launch facts;
  the runner owns cwd, PTY, input, process, reap, and cleanup. Do not add a
  second provider abstraction, registry, plugin, profile, or conditional
  implementation escape hatch.
- Use one bounded dashboard snapshot at sequence N and one watch from N.
  Durable events carry only sequence, safe entity kind/ID, and revision. A
  gap triggers canonical resync; details remain explicit reads.
- Feed mutation responses and watch invalidations through one revision-aware
  client reducer.
- Keep one obvious CLI command per bootstrap/recovery operation and one public,
  testable React browser application consuming the framework-neutral client.
  Browser navigation and visual state never select daemon policy.
- Retain an internal project verification cache only if focused measurement
  shows enough value. If retained, use one lifecycle with active lease/known
  measurement rather than three states and do not expose read-only storage
  status without an operator action.
- Restrict the initially supported local runtime to macOS/Darwin. Platform
  seams exist only where process/syscall/launchd behavior genuinely differs.
- Keep install/update as one concrete transaction over exactly three binaries,
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
- Runtime update/upgrade, manifest consumption, rollback/version activation,
  updater UI/re-exec polish, and GoReleaser. V1 installs one fresh exact build;
  a later replacement design starts from evidence rather than retained Rust
  machinery.
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
product decision. Fresh installation and explicit service lifecycle are
cutover work; runtime update/rollback is not.

## 3. Target architecture

### Binaries

```text
factoryd        sole durable owner, scheduler, supervisor registry, finalizer,
                Change owner, verifier owner, and local-API server
factory-runner  provider-blind live-child owner and blocked-exec/replay host
factoryctl      canonical typed CLI and daemon-absent lifecycle bootstrap
browser app     public React/xterm package hosted by a thin private site shell
```

The first redirected slice builds only enough of `factoryd`, `factory-runner`,
`factoryctl`, browser v1 and the public web packages to drive the PTY/browser/
HumanRequest proof. Installers, update logic, private hosting and broad provider
integration remain blocked until that go/no-go decision.

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
internal/browser -> daemon operations + protocol/browser/v1
web/packages/ui -> web/packages/client -> protocol/browser/v1 fixtures
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
  process-group owner, canonical `AttemptResult` publication/recovery, and
  build-tagged Darwin process identity. It does not know project/task/Change
  policy.
- `internal/change`: exact Git-tree selection/materialization, Change
  filesystem identity, manifests, adoption/recovery, and stable snapshot
  creation. One private `factory-runner --change-worker-shell` mode calls the
  concrete `internal/changeworker` path; the daemon has no Change-worker mode,
  and `internal/runner` remains provider- and Change-policy-blind. The worker
  cannot launch a verifier or decide an outcome.
- `internal/verify`: the three fixed verification policies and their closed
  launch specifications/environments, bounded result/bundle validators,
  and policy-specific refinement. It consumes a stable snapshot; it does not
  know Git, mutate the live Change, start/reap a process, or update Store.
- `internal/provider`: one concrete `Build(Request) (Launch, error)` function
  for shell, Claude Code and Codex. It receives frozen daemon facts and
  returns only one absolute executable, ordered argv and complete ordered env;
  the runner owns descriptor cwd, PTY, input, process, reap and cleanup. There
  `Build` is the only provider boundary; there is no registry, plugin, profile,
  or provider-owned supervision framework. The exact fresh contract is [the
  provider guide](../providers.md).
- `internal/api`: bounded private protocol, framing, typed wire values, and the
  three narrow clients (`HealthClient`, `OperatorClient`, `AttemptClient`). It
  has no Store or lifecycle logic.
- `internal/browser`: loopback-only WebSocket, Host/Origin checks, pairing,
  client credentials, terminal multiplexing and bounded browser DTOs. Public
  TypeScript client/UI code lives under `web/`, never in daemon packages.
- `internal/install`: private home/bootstrap, one fresh three-binary install,
  receipt, and concrete macOS launchd/service transaction,
  added late. It is concrete Darwin code, not a speculative service-manager
  interface, and is not part of kernel authority.

If this graph needs forwarding packages or import cycles, collapse packages;
do not add interfaces to preserve the sketch.

### Preserved provider-launch audit (2026-08-28)

The external read-only launch audit inspected `88a8ab22`. Its reusable finding
is retained: the PTY/process boundary already supplies the hard part. One
registered Change worker becomes the provider in place; exact process, group,
birth and terminal identity is registered before exec; the runner owns the
live PTY; initial bytes are delivered once after release; and output is never
lifecycle authority. Its proposed closed three-provider switch and deletion
list remain the intended simplification, but its implementation proposal was
not adopted unchanged. It made executable availability a pre-admission
filter, gave Claude an isolated HOME while assuming ambient file
authentication, allowed PlanOnly Bash without proving a read-only sandbox,
returned mergeable environment additions, and used a system-only PATH that
would exclude ordinary Go/Homebrew toolchains. Those choices would violate
global admission, make authentication unreliable, weaken execution-mode
authority, duplicate environment policy, or make the product needlessly
unusable.

The corrected boundary is one concrete exhaustive `Build(Request) (Launch,
error)` switch over shell, Claude Code and Codex. `Launch` contains only the
exact committed executable, ordered argv and complete ordered environment; it
contains no cwd, task bytes, descriptors, callbacks, output parser, process
controls or mergeable additions. The Change worker supplies the independently
re-inspected final Change descriptor as cwd, while the runner owns process,
PTY, input, reap and cleanup. The adapter cannot select a source, run,
credential, Change, terminal, completion destination or publication policy.
Unknown provider/mode/model/effort is an error: there is no default arm,
plugin, registry, trait-shaped interface or generic command builder.

Provider executables are daemon-sealed absolute native commitments and
`argv[0]` is that exact path. Metadata/version probing uses null stdin, an
empty private cwd and the same closed metadata environment; it never starts a
session. Probe failure is a typed post-admission launch failure, never global
SQL eligibility or a reason to skip a higher canonical task. A missing,
changed or unsupported provider therefore follows the admitted-to-finalizing
`FailureSpawn`/provider-unavailable path with no provider exec. Shell is
exactly `/bin/sh -s`; Claude and Codex use only their reviewed interactive
native roots and admitted optional settings. Neither accepts print/prompt,
resume, remote/cloud, app-server, browser or plugin authority. Claude
PlanOnly and Codex PlanOnly must each be causally read-only, and WorkspaceWrite
must be Change-bounded; neither may silently retry unsandboxed.

Exact Claude/Codex flag and config spellings freeze only after deterministic
native fake witnesses plus metadata-only checks against supported binaries.
No real prompt/session or credential read is needed for this gate. Provider-
aware model/effort validation has one kernel owner shared by durable profile
reads/writes and launch validation; adapters do not duplicate model, role,
default or Sol/escalation policy.

The environment is built once after `env_clear`. It contains only the exact
daemon socket, attempt-token-file path, committed `factoryctl`, private
runtime HOME/TMPDIR, a bounded daemon-owned absolute-component tool PATH,
controlled locale, `TERM=xterm-256color`, and fixed Git/GitHub/SSH
prompting/discovery/config denials. No proxy, API key, GitHub token, SSH
agent, loader injection, operator token, entity ID, ambient provider home or
arbitrary inherited variable survives. Authentication is a separate
startup-owned boundary: file-backed credentials use one exact
identity-checked copy/link from a daemon-committed source, while keychain-
backed credentials receive no fabricated file. No credential bytes appear in
argv, environment, browser, wire/config fixtures, logs, diagnostics or
errors.

The implementation cleanup is part of this same slice: delete `RunShell`,
`runShell`, shell-path hardcoding, `--change-worker-shell`, the shell-only
supervisor condition and provider-facing stdin/stdout/stderr fields once
their non-provider fixtures use the PTY path. The causal matrix uses native
fake executables to prove exact argv/environment/cwd for every provider/mode,
invalid/NUL/overlong controls, no ambient sentinels, descriptor cwd across
path replacement, no marker before both registration gates/Change
availability/release, input once, terminal prose not completing a run, auth
privacy and a clean process/PTY census. Required mutations restore
`os.Environ`, Claude print/Codex exec, task bytes in argv/env, unsandboxed
PlanOnly, unknown-provider acceptance, exec before release, terminal-output
completion, changed executable/capability acceptance and the compatibility
alias; each must be killed by a focused causal test. This is deterministic
fake-witness evidence, not a paid Claude/Codex session or a claim that any
provider is shipped.

### Concrete V1 provider boundary

The V1 contract defines only unrestricted interactive launches. No provider is
yet claimed shipped. The shell implementation at `1ff2e2e6` is separate and
review-BLOCKED on exact input framing, argv UTF-8 closure, authority-sealed
runtime paths, production-path deletion and causal PTY effect proof. Claude
Code and Codex remain blocked pending exact integration and fake-witness
review. The schema and wire contract have no permission-mode/profile field,
and no bounded provider authority is enabled. A later bounded contract must prove its actual
filesystem, process, socket and network effects with a real OS witness.

`Build` cannot select authority, a source path, a working directory, a
credential, a lifecycle result or a fallback. Admission freezes only provider,
optional model and optional reasoning effort. After admission, the daemon
resolves and seals the exact `Installation` and native
executable/configuration/auth launch facts. `Build` consumes and revalidates
that sealed input; immediately before provider release, the daemon/runner
revalidates it plus the final Change descriptor/config identity and digest. No
executable, version, or digest is an admission schema field. Missing or
changed executable, configuration or auth is typed `FailureSpawn` after
admission. In shell candidate `1ff2e2e6`, `Installation` construction precedes
`Build`; both are post-admission. Its repair must preserve the semantic order:
the daemon seals installation and launch facts, then `Build` only consumes and
revalidates them without performing locator discovery.

Shell is exactly `/bin/sh` with argv `["/bin/sh", "-s"]`. Claude Code and
Codex use an ordered, version-sealed argv containing only the reviewed native
unrestricted bypass and admitted optional settings; no task body, Change path,
secret, Claude `-p`/`--print`, Codex `exec`, resume, remote/cloud, app-server,
browser or plugin selector is allowed. The exact version/flag witness belongs
in [the provider guide](../providers.md); a changed native semantic fails
closed rather than being guessed.

The runner starts from `env_clear` and one closed ordered environment builder,
with private per-run `HOME`, `TMP`, provider state/config/auth and runtime
roots. It permits one daemon-sealed validated `PATH` for non-authoritative
child behavior; `/bin/sh` and authority helpers remain absolute. The selected
Change cwd is descriptor-bound and `.git`-free. The body is canonical bounded
UTF-8 and is written exactly once to the fresh PTY after both gates, followed
by one provider-specific terminator. It is never in argv, env or replay.
Output is opaque and never lifecycle authority. Auth is copy-only sealed file
or metadata-only Keychain reference; no fallback or secret leakage is allowed.
Whole-provider API/model network remains unrestricted and is not claimed to be
constrained by this command contract.

### Fresh home and schema

An initialized but not-yet-started Go home is one atomically published private
directory with this exact bounded census:

```text
format             regular 0600, "dark-factory-go-home-v1\n"
factory.sqlite3    regular 0600, one link, complete rollback-header image
operator.token     regular 0600, one link, exactly 32 nonzero random bytes
home.lock          regular 0600, two links, empty and reserved for daemon use
home.lock.anchor   second name for the same exact lock inode
runtimes/          directory 0700, empty
changes/           directory 0700, empty
```

Both lock names have link count two and resolve to the same empty owner-only
regular inode. The anchor prevents replacement of only the public lock name
from admitting a second cooperative daemon. Deliberate same-EUID replacement
of both names is outside the local trust boundary; no pathname lock can defend
against an owner intentionally replacing the entire namespace.

The database uses SQLite `application_id=0x4446474f` (`DFGO`) and
`user_version=1`. Initialization never creates or completes a prefix inside the
final home. It creates exactly one fixed private sibling staging directory,
writes and syncs every member through directory-relative descriptors, validates
the database with the read-only immutable-image seam, validates the complete
stage census, and publishes the directory with Darwin no-replace rename before
syncing the parent. The fixed staging name is
`.<home-basename>.dark-factory-go-v1.stage`; an existing staging object is
durable crash evidence and is refused, not adopted, repaired, randomized,
deleted, or overwritten.

The final home is also never adopted or repaired. An exact already-ready home
may be reported ready after strict read-only inspection. A database-only,
marker-only, mixed, partial, symlinked, Rust-layout, unknown-version, or
otherwise unmarked home fails unchanged and explains that Rust-home migration
is unsupported. A publication result that cannot prove both the no-replace
rename outcome and parent-directory durability remains explicitly uncertain;
initialization never retries an ambiguous rename.

Fresh-home inspection opens the absolute path component by component from `/`
with `openat`/`O_NOFOLLOW`, retains and rechecks the parent/home/database
identities, enumerates an exact bounded census, and passes the already-open
sidecar-free database descriptor to `kernel.InspectImmutable`. It performs no
write-capable SQLite open, flock, creation, chmod, rename, unlink, repair, or
cleanup. SQLite journals/WAL/SHM, a socket, populated runtime/change
directories, a nonempty, malformed or nonidentical reserved lock pair,
arbitrary entries,
symlinks, hard links, wrong owners/modes, and schema or byte disagreement are
refused without mutation. The lock pair is exact reserved lifetime authority,
not permission to adopt or repair a home. The initial
`factoryctl doctor --home ABSOLUTE` is this strict stopped/fresh-home
inspector; later live-service diagnostics must use the owner API rather than
weakening this filesystem contract.

After the daemon starts, the same private root owns daemon Changes, per-run
runtime roots/token files/runner sockets, the fixed `AttemptResult` spool,
verification scratch, and paired SQLite WAL/SHM sidecars. `OpenOperationalHome` allows only
those populated child directories and the two exact sidecar names while
retaining the seven fixed root members; it does not traverse child contents.
It returns descriptor-bound database/runtime/change capabilities and no token
bytes or unchecked member paths. The private API socket remains a separately
owned filesystem object whose exact placement and stale-socket recovery must
be frozen by the production `factoryd` composition slice; it is not silently
added to the home census. Exact names are shared by kernel/install tests and no
binary reads an ambient default during tests.

The fresh schema contains only current product authority:

- factory settings;
- projects and agents with embedded provider/model, pause, and budget fields;
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
- Every physical writer/reader connection in the finite retained set sets and
  reads back `foreign_keys=ON`, `busy_timeout=5000`, and
  `synchronous=FULL`; it verifies persistent WAL before use. There is no
  unbounded application retry. Initialization must be implemented through a
  driver connector/DSN or per-checkout hook that is causally proven to cover
  new pooled connections, not a one-time `sql.DB` bootstrap call.
- If that per-checkout verification is interrupted by its cancelled caller, an
  autocommit-clean connection is returned to the sealed set and the operation
  still fails. A live-context interrupt, configuration mismatch, other driver
  failure, or non-autocommit connection is discarded instead; cancellation
  never forgives an unverified configuration or a live transaction.
- If begin/commit/rollback or connection state is ambiguous, SQLite's real
  autocommit bit decides whether the affected connection is clean: a connection
  still holding a transaction is discarded, while one with autocommit on is
  returned to the finite sealed pool. The operation returns outcome-unknown
  and is never blindly replayed. Before any later write, its domain method
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
The source wrapper has one combined preparation report before population and
one provider release after publication. These preserve evidence that the
selected commit and empty stage preceded materialized blob reads and that the
complete Change preceded provider execution without adding a `selected` state
or separate selection checkpoint:

1. `RunNext` gives cursor-free `AdmitNext(ctx, keys, at)` one complete fresh
   daemon footprint. Inside one immediate write transaction the Store selects
   the globally canonical eligible queued task+agent, then its task incarnation
   and Change. It ignores the unconditional fresh candidate Change ID for a
   non-worker or existing row and inserts it only for a selected worker with no
   row. The transaction performs the exact retry/reservation transition and
   commits the run, credential digest, actual Change ID/revision, runtime claim,
   initial resource/session declarations and invalidations. No AgentID, task,
   scheduler cursor, durable filesystem locator or caller-selected reuse fact
   enters the decision. `RunNext` derives launch facts from the committed Run.
2. Daemon creates the exact private runtime root and binds its inode.
3. Daemon creates/binds a private startup lease and completes all setup. Its
   final action before the sole outer `cmd.Start` is the exact
   `BeginRunnerStart` transaction. An outcome/cancellation that wins while the
   runner remains declared atomically closes the never-created process/session
   facts and prevents that permit; once `starting` commits, generic outcomes
   refuse until Start resolves.
4. Daemon starts the inert parent-bound runner gate. Exact Start error uses the
   one `RecordRunnerNeverStarted` transaction; successful Start binds exact
   runner PID/birth `starting -> active` before any other outcome or release.
   Crash/uncertainty while `starting` stays visibly unresolved with no signal,
   relaunch or no-child inference.
5. Runner prepares a second parent-bound child as process-group leader. That
   child is the one private `factory-runner --change-worker-shell` source
   wrapper, blocked before Git selection/provider `exec`; runner reports exact
   PID/PGID/birth. There is no second daemon-hosted worker process. Both gates
   use the frozen control-FD-3/Change-parent-FD-11 remap, and the outer runner
   closes its Change-parent duplicate after this one-shot preparation.
   Cancellation, infrastructure outcome or daemon EOF does not cancel this
   already-prepared one-shot Start: while the provider pair remains declared/
   empty, generic outcomes retry and the runner resolves the sole Start. If the
   launch primitive proves `cmd.Start` never succeeded it publishes
   `inner_unregistered_converged`; any successfully created child stays inert behind
   `inner.activate`, binds the exact pair through ready/activation and remains
   owned while its leader is unreaped, through descendant convergence and positive
   group absence, and only then sole-Waits before serial PTY drain/close and
   `inner_converged` publication. It never signals or probes the numeric group
   after Wait. Uncertainty publishes nothing. All cases use the one durable
   result spool.
6. Daemon binds those identities and releases preparation once. The wrapper
   selects an exact local commit without lazy fetch, computes a canonical
   Git-tree commitment, descriptor-creates the deterministic empty stage and
   sends one combined report containing the selection commitment and exact
   stage identity. It then blocks before reading materialized blobs. Daemon
   commits `reserved -> prepared` once and releases population. There is no
   durable `selected` phase, selection report/commit pair or selection-only
   release. The already registered wrapper process group covers its Git
   descendants; the wrapper synchronously owns, bounds, and kill-and-waits
   every direct Git child, and provider exec cannot overlap any of them. No
   per-command durable resource/gate state is added.
7. The wrapper populates that prepared stage with at most 10,000 total
   entries, depth 64, 1,023-byte relative paths, 255-byte components, 256 MiB
   per blob and 1 GiB total blobs. Before normal publication, the one central
   scanner validates and fsyncs every accepted stage file/directory including
   its root. The wrapper then publishes without replacement, reports the
   commitment and exact published identity, and blocks before provider `exec`.
   Daemon requires that identity to equal the one committed at `prepared`,
   compares the exact commitment, fsyncs the retained parent, rechecks parent/
   final/content/stage facts, commits `prepared -> available`, atomically moves
   admitted to running, and only then releases provider execution. Recovered
   publication runs the same scanner over final, then the same parent fsync and
   rechecks. It may proceed only after every mutator is positively gone and
   those exact durable facts still match. It may settle the Change retained without
   activating or replaying a provider only after matching AttemptResult
   consumption, runner release, terminal closure and every other settlement
   prerequisite. A surviving worker uses the normal blocked checkpoint, not an
   alternate adoption protocol.
8. The same registered child revalidates the frozen native provider commitment
   after activation and pathname-`exec`s it once with the registered PTY slave
   as its controlling terminal. Its PID, PGID, and birth remain unchanged;
   input is accepted only through the exact live terminal session and current
   lease while the run remains running.
9. Runner durably publishes the one canonical `AttemptResult`, best-effort
   notifies the daemon and exits without acknowledgement. Live and recovery
   consume the same exact spool. Result consumption releases only the atomic
   provider pair and leaves the session nonclosed; exact outer-runner release
   precedes the one terminal-close transition. The spool is removed only after
   provider-pair, runner and closed-session Store postconditions are durable.
10. With an active runner and active exact provider pair, completion/blocked/
    failure requests first outcome, enter finalizing, revoke the credential and
    atomically move all four initial resources plus the terminal to releasing.
    With an active runner and declared empty provider pair, every generic
    outcome/cancellation/infrastructure failure refuses until the mandatory
    inner Start yields ready/activation or an AttemptResult; no finalizing
    declared-pair state exists. Declared/starting outer-runner races use only the
    exact pre-start transactions above. AttemptResult-first admitted/running
    failure uses the exact direct provider-pair-release matrix instead.
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
the already registered gate. After admission, the daemon resolves and seals
the `Installation`; `provider.Build` consumes and revalidates that exact input
and freezes the
its absolute path, device/inode, owner, complete mode, bounded size, nanosecond
timestamps, SHA-256, running-architecture support, argv, environment, working
directory identity, and exact PTY/control descriptor setup before readiness.
After create-only
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
- Mutation replies include the canonical head observed after the operation and
  the affected entity revision. They do not pretend the head identifies one
  exact invalidation: an idempotent no-op has none, and a later concurrent
  commit may already be visible. Clients fetch/fold canonical state through
  one revision-aware reducer, so delayed replies cannot regress state or skip
  unseen invalidations.
- CLI and browser adapters invoke the same daemon operations where their scopes
  overlap. Browser-only interaction remains typed daemon policy; no client has
  a direct filesystem or lifecycle shortcut.
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

An external read-only platform/hard-cutover audit inspected `88a8ab22` and
returned **BLOCK**, as expected: no production `cmd/factoryd`, recovery
composition, install-owned Local API, fixed production browser bind, service
transaction or Go release bundle existed at that head. Its useful dependency,
deletion, isolation and mutation findings are consolidated here; the scratch
audit document is not retained as a second status record. The audit's proposal
to delete this rewrite record is rejected because the mission requires this
document to become the permanent cutover evidence record.

Platform work begins only after the kernel and clients are stable. V1 is
fresh-install only. `init` and `factoryctl service
install|start|status|recover|restart|stop|uninstall` use one concrete install
library and shared mutator prelude. There is no Go `update`/`upgrade` command,
download cache, manifest consumer, second installed version, Rust migration or
version rollback framework:

1. Canonicalize and verify exact home/job ownership and format before any
   write. An unknown/Rust home receives no Go marker, database, `bin/`, link,
   or plist.
2. Acquire one runtime-mutation lock, recover any pending record, and bind
   exact home device/inode/uid, socket, current receipt/link, plist digest,
   launchd label/domain, and operation authority.

Fresh service installation resolves and validates the invoking `factoryctl`
and its exact sibling `factoryd` and `factory-runner`: three regular Darwin
executables, mode `0755`, one version/source/build/target identity, bounded
sizes and exact digests. It copies/syncs them into one immutable private
`bin/<build-id>/` directory, publishes the one-component relative
`bin/current` link and exact receipt, renders one allowlisted plist with
`AbandonProcessGroup=true`, and proves launchd PID, actual daemon executable,
health build identity and both receipt-bound siblings. A bounded pending record
makes every fresh-install cut recover to exact completion, exact cleanup, or
visible retained uncertainty; it never activates an older/newer runtime.
Unknown filesystem or launchd ownership fails closed and preserves evidence.

Repeated service install validates the already-active exact
receipt/current/job and is an idempotent no-op; it never stages another build
or changes the version link. Start validates the exact installed receipt/current/job,
bootstraps it, and proves health. Restart validates the same authority, reloads
only the daemon, proves health, and preserves exact admitted runner/provider
identities. Stop
requires exact job/PID ownership, distinguishes documented launchd absence from
spawn/permission/parse/service errors, boots out, and proves the old PID and
socket absent; through `AbandonProcessGroup` it does not invent ownership of
admitted child groups. Uninstall requires stopped exact ownership and no
nonterminal work/resources, removes only the allowed job/link/receipts/runtime
metadata, and preserves SQLite and retained Changes by default. These verbs do
not enter another-build or fresh-bundle staging paths unless fresh service
installation explicitly requires it.

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
publication, signing/notarization and any runtime updater remain explicitly
deferred. `latest.json` is inert release metadata retained only for the
independent control-plane five-asset validator; no Go command fetches or
consumes it. Each archive contains exactly the three root regular mode-`0755`
binaries and no directories, links, xattrs or extra members. The formula
installs only those binaries and contains no TUI/update behavior. No stronger
distribution claim is made. Release/non-PR gates require
hosted ephemeral execution or a separately proven isolated runner rather than
silently retaining the current persistent-runner credential exposure (#54).

The platform implementation graph is deliberately serial where authority
overlaps:

1. production `factoryd`, recovery/scheduler ownership, install-owned Local
   API, fixed `127.0.0.1:43123` bind and versioned health;
2. one `VERSION`/`internal/buildinfo` identity and fresh-only bundle/receipt;
3. concrete Darwin service code plus `factoryctl` verbs and fake/real isolated
   launchctl fixtures;
4. direct-Go release/package/formula scripts and independent control-plane
   compatibility proof; then
5. top-level gates, documentation and final deletion.

At hard cutover delete every tracked file under
`crates/factory-core`, `crates/factory-runner`, `crates/factory-tui`,
`crates/factoryctl` and `crates/factoryd`; root `Cargo.toml`/`Cargo.lock`; the
old launchd template/proof; Rust/Linux contributor-smoke paths; and obsolete
Rust build-headroom machinery. Retain the local-CI lease/environment isolation,
the standalone `control-plane/` Rust workspace and the permanent evidence in
this document. Update `AGENTS.md`, `README.md`, architecture/security,
contributor/install/provider/workflow guidance and CI atomically so no retained
text presents the Rust TUI/updater as current product authority.

## 4. Kernel proof

The Store/two-gate/Change/API/CLI implementation through `7eb4176` is retained
foundation, not the redirected go/no-go proof by itself. The first web-first
integrated proof includes that foundation plus the PTY session, HumanRequest,
loopback browser adapter, browser-v1 TypeScript client and minimal public dev UI
needed to exercise the authoritative updated vertical slice above. Private-site
integration remains a later cutover gate after the public packages stabilize.

The deterministic witness is:

```text
fresh Go-marked home and SQLite schema
  -> create project with tiny local Git fixture and exact base commit
  -> create agent with shell provider and embedded provider/model/budget fields
  -> create assigned task
  -> atomic canonical admission inside BEGIN IMMEDIATE
  -> reserve daemon-owned Change and exact source/base authority
  -> mint one attempt credential and declare runtime/runner/provider/terminal session
  -> prepare outer runner, PTY and registered source/provider wrapper before exec
  -> persist exact identities; release exact commit selection before blob read
  -> materialize/publish and revalidate the complete exact Change manifest
  -> persist Change available and transition admitted -> running
  -> release the exact shell provider once
  -> pair browser; attach, observe output, acquire lease, input and resize
  -> create exact HumanRequest; fetch private detail with explicit capability
  -> deliver reply at most once or surface delivery-unknown; commit one concrete cancel
  -> provider makes a typed authenticated completion request
  -> serialize finalizing barrier; revoke credential and all input
  -> stop/reap provider; for configured worker success only, take a stable
     snapshot and run the once-only registered verifier effect
  -> close terminal, release resources, remove runtime last, finalizer writes terminal
  -> browser refresh/reconnect/reset and client state agree with SQLite
```

The shell provider writes an external witness only after activation. Tests
count that witness, exact terminal output sequences and exact fixture input
receipts rather than trusting callbacks or stdout. The slice then repeats
across real daemon/runner SIGKILL cuts after
every durable/effect checkpoint and proves at most one provider execution, no
input replay, no invented release, no unsafe signal, and eventual deterministic
convergence where exact absence is provable.

Go is accepted for broad implementation only if this slice is smaller and
easier to trace than the corresponding Rust path while retaining the
invariants and killing the required mutations. If the two-gate lifecycle,
Store transaction semantics, or recovery requires more/larger abstraction, or
if exact process ownership becomes weaker, stop and reassess the language
decision. Browser/UI/provider/update progress cannot compensate for a failed
PTY/process/authority spike.

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
- Lane B, runtime: the concrete `provider.Build` launch path (Claude/Codex
  remain blocked), observation/recovery hardening, and deterministic
  fake-executable tests.
- Lane C, source: complete Change lifecycle, Rust/Go verification, retained
  cache only if measured.
- Lane D, clients/browser: browser v1, pairing/WebSocket adapter,
  framework-neutral TypeScript client, and public BUILDING/AGENT/NEEDS YOU UI.
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
5. Delete the replaced Rust local-runtime packages and root workspace artifacts;
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
| Atomic canonical admission | Call `AdmitNext` without AgentID/task/cursor while racing priority, stale insertion, RunID retry, and the last capacity slot. Prove the complete schema image and one cross-row SQL predicate run before reconciliation, capacity, or reason precedence; all nonterminal runs count; ties use bytewise task-ID BLOB order; corrupt canonical work never falls through; source/provider availability is post-admission. | caller AgentID/task/cursor; per-agent/cache selection; integrity or capacity outside `BEGIN IMMEDIATE`; count before integrity; omit admitted setup-stalled runs; malformed rows become ineligible; skip corrupt Change; external-availability filter; optional candidate selection; process-local fairness; launch from observation |
| Schema allowlist and scalar domains | Query the exact `schema.go` allowlist and constraint fixtures. Mutate every admission-relevant storage class/domain boundary, including zero IDs, `/` as a project root, reversed timestamps, unknown enums and non-Boolean switches. Exercise shared create/read/wire UTF-8/NUL validation and malformed canonical selected task/control; lower-ranked queued prose need not be globally scanned. Any mode/profile field in schema or wire is rejected before admission/decoding. | duplicate prose validator; SQLite coercion; accept root `/`; normalize malformed canonical text; add a mode/profile column or wire field; unknown value treated as ineligibility |
| Canonical Change admission/revision | In the same transaction, require one unique `(project_id, task_id, task_incarnation_id)` Change, ignore the fresh candidate when a row exists, require the unique terminal predecessor with exact Change/task/incarnation/work-revision equality, and prove fresh/retained/abandoned relative revision deltas and lost-reply reconciliation after later Change progress. | duplicate Change; nonterminal/orchestrator/cross-task settlement; optional/reused candidate; compare candidate on reuse; timestamp/latest-run predecessor; loose revision; require current revision during reconciliation |
| Commit ambiguity | Cut/interrupt begin, commit response, and rollback; discard handle, reopen, reconcile by domain ID/revision, return outcome-unknown where not provable, and perform no second transition | retry blindly; reuse ambiguous connection; generic receipt fallback |
| Fresh schema allowlist | Query schema objects and columns after init and assert the exact executable allowlist excludes operations, mutation receipts, decisions, quarantine, intake, compatibility, migration residue, unused durable Change repository identity, and permission-profile fields | add speculative authority table; retain Rust compatibility object; add `repository_dev`/`repository_inode`; add a mode/profile field |
| Exact attempt authority | Exercise forged, old, admitted, wrong-run/project, operator, finalizing and terminal credentials against every attempt mutation | drop phase join; accept caller IDs; operator fallback; reuse credential on retry |
| First outcome/finalizing | With an active exact runner and provider pair, run completion-before-exit and exit-before-completion from admitted/running; assert immutable proposal, revoked credential, exact runtime/runner two plus provider-pair two all releasing, terminal releasing and one transaction rollback on any count mismatch; runner starting and active-runner/declared-provider states use only their serialization rows below | overwrite first proposal; leave one resource/session declared or active; accept runner starting or declared empty provider pair; split cleanup transaction; direct running->terminal |
| Finalizer only/one-way | With all resources released, repeated/concurrent finalizers create one terminal transition/invalidation; with any unresolved resource, they create none; later positive absence permits only unresolved->released->one terminal | terminalize unresolved; released->active/unresolved; duplicate terminal event |
| Register-before-exec | External provider witness remains absent until run/resource identities are committed running; replacement before activation, version-symlink swap, target removal, byte/mode mutation, final-check failure, and lost activation acknowledgement preserve the frozen launch or fail without execution; a controlled post-check replacement records the explicitly out-of-scope same-UID pathname seam | release either gate early; omit preparation leash; persist identity after exec; re-resolve installation symlink; omit final metadata/digest comparison; retarget on mismatch; claim inode-atomic execution |
| Owned process authority | Force `cmd.Start` failure, successful Start with birth-identity acquisition failure, leader exit with live descendant, every pre/post-ready result state, frame loss and restart; while leader remains unreaped prove birth-pinned group absence before sole Wait, then serially drain/close; authenticate and promote the exact no-replace AttemptResult by file fsync, revalidation, directory fsync and revalidation before consumption | transient receipt; mint `inner_unregistered_converged` after successful Start; Wait before group absence; numeric group use after Wait; publish before drain/close; omit consumer durability promotion; trust frame; respawn; release on EOF/PID/flock/empty rows |
| Liveness fails closed | Real ESRCH, EPERM where feasible, malformed/overflow IDs, weak/mismatched/reused identity and leader-with-descendant | EPERM as absent; malformed as released; leader exit equals group absence |
| Crash/restart at-most-once | SIGKILL daemon/runner at every launch, exit, cleanup and acknowledgement cut; count external witness/input; reopen same home | relaunch admitted run; ack before Store commit; remove runtime before absence |
| Change exactness | Materialize a real commit and verify manifest/blob/mode/path/base/inode; prove the one central scanner fsyncs every accepted regular file/directory/root for normal stage, recovered final and nonnull-RunningAt settlement, with parent fsync and a full stable identity/content/final-binding/stage recheck; null RunningAt requires literal equality/no rewrite; deny Git discovery/worktree and replacements | resolve moving ref; `git archive`; allow symlink/gitlink/.git; wrong base; delete replacement; digest without fsync; omit any file/directory/root/parent fsync or recheck; rescan null-RunningAt; treat marker as exec proof |
| Change crash recovery | Kill with dirty file pages or unflushed directories before/after stage scan, rename, parent fsync and Store commit; kill after reserved mkdir and prepared-stage removal; prove a present reserved stage is untouched, prepared abandonment refuses every live/uncertain mutating child/runner/group/descriptor owner, both publication arms use the central scanner, and recovery settlement waits every process/session prerequisite | delete/abandon reserved stage; remove/inspect prepared stage with uncertain owner; duplicate digest-only recovery scanner; skip data/directory/parent fsync or post-fsync recheck; accept reappeared stage; settle early; replay provider |
| Provider pair atomicity | Inject failure between every provider process/group DML and race activation, finalizing, result and recovery; assert exactly two rows move with equal phases and consistent empty/exact identities or the whole transaction rolls back | generic per-row provider mutation; affected-row count one; commit split phase or identity |
| Attempt-result run matrix | Exercise admitted inner-not-created; admitted active/exact after durable activation; admitted declared/empty after a spawned child whose ready frame never arrived or activation DML never committed; running active; and finalizing releasing/unresolved. Require exact marker/runtime/attempt binding, direct pair-two release, declared identity binding, deterministic failure and ProviderExit sequence/code-or-signal/timestamp replay | distinguish missing nondurable ready frame; reject declared spawned-child or active arm; omit identity binding/ProviderExit; accept present marker with declared rows or uncertain active census; overwrite proposal; infer success; wrong count/failure; invent empty-pair exit; replace replay timestamp |
| Runner-start serialization | Race `BeginRunnerStart` against cancellation and every outcome in both orders; before permit, the exact proposal wins and closes three never-created process rows plus terminal while moving runtime releasing; after permit, generic outcome refuses, exact Start error uses `RecordRunnerNeverStarted`, exact success binds PID/birth `starting -> active`, and crash/ambiguity remains `starting` and is reported unresolved with no signal/replay | Start without permit; permit with proposal; allow starting on another resource/with identity; mutate starting generically; lose winning cancellation; use guessed/inexact Start error; wrong 1+3+1 counts; bind success without identity; infer no-child, signal or relaunch after starting crash |
| Provider-start serialization | With an active outer runner and declared empty provider pair, race cancellation, every generic/infrastructure outcome and daemon EOF against inner Start. An early outcome refuses then commits only after ready/activation; ready first permits that same outcome; exact Start error publishes/consumes `inner_unregistered_converged`; EOF still performs the sole prepared Start, never creates `inner.activate` without daemon release and converges with no live child | move declared pair to releasing; create finalizing declared pair; skip or duplicate Start on cancellation/EOF; cross provider gate; lose retried outcome; add another result/state/receipt; leave child live |
| Terminal close and Change settlement | Cut after result consumption, runner Wait/absence, runner release, terminal close, result unlink and directory fsync; prove result consumption never closes, close accepts only releasing/unresolved after exact provider/runner/result/output proof, and Change settlement waits for close | close during result consumption; close declared/active; close before runner release; generic close; settle before terminal close; accept precondition-free missing spool |
| Private Change worker | Invoke the private mode without inherited owner-only descriptors/parent gate and prove no Git read/path/child effect; exercise the registered mode normally | accept direct argv invocation; perform effect before capability check |
| Stable verification | Provider attempts concurrent write while finalizing; provider must be reaped; scan/copy/scan either yields one digest or refuses; verifier launches controlled snapshot | verify live Change; inherit GOENV/cache/temp/network; launch mutable build output |
| Verifier bundle identity | Copy two executable fixtures, mutate the second after the first runs, and prove the second never executes | validate a verifier bundle only once; omit immediate pre-launch recheck |
| Verification applicability/refinement | Proposed worker success with configured policy verifies once; `None`, orchestrator, blocked, failed, and exit proposals launch no snapshot/effect; verification failure preserves proposal and refines only terminal result | verify every outcome; snapshot for `None`; overwrite first proposal |
| Verifier crash authority | Cut after declaration, activation, result publication, leader exit, group absence, cache measurement, and temp cleanup; valid result is read only after group absence and attempted/no-result never reruns | trust result while live; rerun after restart; cache before writer absence |
| State/event atomicity | Force invalidation insert failure after state DML and state DML failure before event; reopen | separate commits; event from stale pre-write snapshot; missing derived invalidation |
| Sequence/resync/client agreement | Snapshot at N plus watch N+1 during concurrent mutations; inject duplicate, gap, lag, restart and delayed response; compare client to fresh canonical state | discard lagging unseen event; accept gap; delayed reply overwrites newer revision |
| Bounded invalidation retention | Mutate the durable metadata and rows through empty, one-row, full, prune, delete, restart and gap states. Require `head = next_invalidation_sequence-1`; empty iff zero rows/head 0/floor 1; otherwise exact count/min/max, contiguous floor..head rows, retention bound, valid IDs/revisions and deletion bits before `Watch` or admission | metadata-only advance; empty log with advanced head/floor; gap/duplicate; wrong min/max/count; unbounded log; stream partial tail; omit deletion revision |
| Public privacy/bounds | Seed unique sentinels in every private field, serialize every dashboard/event/status frame, and scan sizes/content | expose root/body/result/message/token/prompt/output; grow snapshot beyond cap |
| Provider environment/token | Seed ambient API/provider/proxy/Git/GitHub/SSH/loader sentinels; inspect child argv/env, token mode/content, startup frames, logs/errors/events, and provider-launched CLI auth | inherit `os.Environ`; put bearer in env/argv; operator fallback |
| Browser authority boundary | Enumerate every browser mutation and assert it invokes one typed daemon operation with paired authority and revalidated state | direct filesystem/policy mutation; display label or agent prose selects behavior |
| NEEDS YOU authority | Derive only the three actionable typed projections; resolve with exact operator/source revision and atomically commit state/intent/invalidation; test exact replay, stale/conflict/already-resolved/attempt denial and public-label privacy | duplicate policy table; inert choice; stale/attempt action |
| Reducer deletion monotonicity | Delete an agent/task, then deliver a delayed mutation reply at an older/equal revision; entity remains absent and client equals fresh snapshot | resurrect deleted entity; ignore delete invalidation |
| Operator reply loss | Drop a reply after durable operator mutation commit, reconnect/resync without payload replay, and prove one state effect/invalidation | automatically retry request ID; treat transport correlation as authority |
| Goroutine/resource ownership | Leak-check each owner after normal/error/cancel/crash; exact process/path census; run race detector | unjoined goroutine; cancel treated as reap; fixture cleanup only in killed process |

### Required crash cuts

- before/after RunID reconciliation, global-settings validation and the one
  concrete capacity/authority/queued-rank-and-payload integrity predicate,
  dispatch/all-nonterminal-capacity checks (including an admitted setup-stalled
  last-slot run), global eligible task/agent selection, BLOB tie comparison,
  every reason-precedence boundary,
  canonical Change corruption decision and every full-footprint/invalidation
  write in `AdmitNext`; restart must reconcile only the fresh RunID and preserve
  the exact global order without a scheduler cursor or lower-work fallback;
- after runtime/resource declaration and after exact path binding;
- before/after an outcome that wins on declared runner, `BeginRunnerStart`, the
  sole outer `cmd.Start`, exact Start-error return, `RecordRunnerNeverStarted`,
  successful Start and exact PID/birth binding; kill while `starting` and prove
  no generic outcome, no-child inference, signal or relaunch;
- after outer-runner activation but before inner Start: inject cancellation,
  every generic/infrastructure outcome and daemon EOF in both orders around
  ready/activation; the early transaction must refuse and retry, the runner
  must perform exactly one Start, exact Start failure must publish
  `inner_unregistered_converged`, and successful Start must remain inert without an
  `inner.activate` release and leave no child after owned EOF convergence;
- after inner child preparation and identity persistence;
- before result-name/gate validation, before/after `cmd.Start`, child readiness,
  exact birth acquisition, unreaped-leader exit observation, descendant
  convergence, positive birth-pinned group absence, sole Wait, serial final
  output drain and PTY close for each result kind;
- after no-replace result creation, partial/full write and each publisher file/
  runtime fsync, reopen/hash, best-effort frame, frame loss and outer exit;
- after the common consumer's initial result validation, exact open-file fsync,
  descriptor/name/content revalidation, retained-runtime-directory fsync and
  final revalidation, including a complete publisher-pre-fsync residue;
- before/after live/recovery result authentication; admitted inner-not-created,
  admitted active-converged and admitted declared-converged after either missing
  readiness or activation durability, running and finalizing matrix
  transactions; injected failures between result-identity binding,
  runtime/runner, provider-pair, ProviderExit and terminal updates; existing
  first-proposal preservation; exact failure projection; and each rejected run/
  resource/session/marker/result combination;
- after provider-pair result consumption but before exact outer Wait/recovered
  absence; after runner exit/release but before terminal close; after terminal
  close but before spool unlink; after unlink but before runtime fsync; and after
  fsync but before the stable absence recheck;
- between each recovered exact PID/birth observation, exact group-absence
  observation, typed `recovered_absence` commit and resource release;
- before/after the one preparation release; immediately after deterministic
  stage mkdir but before the combined prepared report/commit, where a present
  reserved stage must remain untouched/finalizing; before/after population and
  each central-scanner regular-file/directory/stage-root fsync, including dirty
  file-page and directory-entry loss; before/after no-replace rename; on
  recovery before/after each final-tree scanner fsync; after final checks but
  before exact parent fsync; after parent fsync but before the full post-fsync
  parent/final/content/stage recheck; and after that recheck but before
  Change-available commit;
- before/after every available-to-retained parent binding, deterministic stage
  absence/recheck and null/non-null `Run.RunningAt` content decision; for the
  nonnull arm, after each central-scanner regular-file/directory/Change-root/
  parent fsync and before/after the full stable identity/content/final-binding/
  stage recheck;
- during recovered publication after result consumption but before runner
  release, after runner release but before terminal closure, and after closure
  before every remaining Change-settlement prerequisite; none may settle early;
- after exact prepared-stage removal but before parent fsync, and after parent
  fsync/absence recheck but before the abandonment Store commit; before every
  prepared-abandonment child/runner/group/descriptor absence proof and with
  each owner live or uncertain, which must prevent inspection/removal;
- immediately before/after provider release and lost release acknowledgement;
- after provider exit, `AttemptResult` publication and Store observation;
- during each resource cleanup and runtime-root removal;
- before terminal transaction and before client acknowledgement;
- after verifier temp/resource declaration and identity binding;
- immediately before/after verifier activation and result publication;
- after verifier leader exit/group absence, cache measurement/handoff, and
  temporary-root cleanup.

At every `AttemptResult` cut, the same live/recovery consumer promotes any
complete canonical no-replace residue by fsyncing the exact open file,
revalidating descriptor/name/content, fsyncing the retained runtime directory
and revalidating again before Store consumption. A complete write from before
the publisher fsync can therefore converge; the system does not and cannot
classify whether those bytes were previously durable. An absent, partial,
malformed or replaced result is never repaired or overwritten and leaves the
run finalizing. If pair consumption committed before the crash, recovery
reconciles its exact nonclosed session and immutable-proposal postconditions,
finishes runner release, closes the terminal from the same exact spool/output
proof, and only then removes the same inode. A crash after unlink may reconcile
stable descriptor-relative absence only when pair release, runner release and
session close were already durable; the same absence before those facts proves
nothing.

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
- release-archive traversal, absolute path, link/hardlink, duplicate, extra,
  mixed build, wrong architecture/mode, compression/aggregate bound, tampering,
  and exact three-member success; installed input independently proves three
  exact same-build sibling regular executables before copying any of them;
- cuts after every installed-binary/file/directory sync, receipt/pending-record
  phase, current-link publication, plist write, bootout/bootstrap and health
  proof, followed by exact fresh-install completion, exact cleanup or retained
  visible uncertainty; no cut can activate a second version;
- random-label isolated service install/start/status/restart/stop/uninstall,
  operational launchctl error classification, exact daemon executable/PID and
  socket absence, runner/provider survival across restart, nonterminal uninstall
  refusal, database/Change preservation, and proof that start/stop/uninstall do
  not enter fresh-bundle staging or install another build;
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

- global multi-agent admission order, stale-observation immunity, exact
  priority/creation-time/16-byte-BLOB-task-ID ties, SQL eligibility, exact
  reason precedence and concurrent all-nonterminal factory-capacity
  enforcement, including an admitted setup-stalled last-slot run;
- integrity-before-capacity across unknown or malformed run/credential/
  resource/session/Change phases, relations, pairs, IDs, revisions and enums;
  the test kills counting only recognized phases and thereby hiding corruption;
- refusal of caller AgentID/task/cursor or agent enumeration/cache/fairness
  state, unconditional fresh candidate Change ID, RunID-only reconciliation and
  restart independence from any scheduler cursor;
- canonical Change corruption refusing with zero footprint/no lower-work skip,
  global-setting corruption, every malformed capacity/authority row and every
  malformed higher/lower structurally queued rank/payload/relation/control
  class returning `ErrCorruptState` before the capacity count through direct
  settings validation and the one concrete SQL integrity predicate, and
  repository/provider availability becoming post-admission typed failure;
- attempt phase/credential check and operator fallback;
- `EPERM`, malformed PID/PGID, and weak identity handling;
- finalization idempotency and released-resource terminality;
- exact Change base identity and existing-source deletion protection;
- public privacy filtering and encoded bounds;
- event sequence/gap handling and stale response/event ordering;
- both provider-before-registration gates;
- observer fresh-state reload and exit observation idempotency;
- refusal to delete or abandon any present/replaced/uncertain `reserved` stage
  after the mkdir-before-prepared crash, with only stable absence permitting
  abandonment;
- complete Change commitment/identity recovery after publish-before-available;
- verifier sibling identity immediately before each launch;
- outer runner activation-error kill-and-wait;
- combined prepared report/commit before blob read and
  Change-available-before-provider-exec;
- provider `env_clear`, attempt token-file-only delivery, and operator fallback;
- verification applicability, no-rerun recovery, and result-after-group-absence;
- domain-key commit-ambiguity reconciliation with no blind replay;
- private Change-worker inherited capability check;
- derived NEEDS YOU revision/operator checks and no inert decision;
- deletion tombstone defeating a delayed stale response;
- transport request ID never authorizing automatic mutation replay;
- fresh-schema allowlist excluding speculative/compatibility tables;
- canonical Change selection by admitted task incarnation, exact retry
  settlement predecessor and lost-response reconciliation after later revision;
- exact admitted-relative Change revision deltas and same-transaction
  invalidations;
- exact admitted/running/finalizing AttemptResult matrix across runtime root,
  runner, provider pair and terminal, including active exact and declared spawned-
  child converged arms without relying on nondurable readiness receipt,
  declared-row identity binding, exact two+two+one counts, immutable first
  proposal and deterministic failure rather than inferred success;
- `BeginRunnerStart` versus every outcome/cancellation in both orders,
  `RecordRunnerNeverStarted` only on exact Start error, `starting -> active`
  exact identity binding on success, exact runtime-one/process-three/terminal-
  one effects, and crash/ambiguity retaining `starting` while reporting
  unresolved with no signal, relaunch or no-child arm;
- active-runner/declared-empty-provider serialization against generic outcome,
  cancellation, infrastructure failure and daemon EOF; mandatory one-shot inner
  Start, retry after ready/activation, exact `inner_unregistered_converged` Start failure,
  no marker before daemon release and no child after EOF convergence;
- provider process/group pair atomicity for activation, releasing, unresolved
  and release, with every generic single-row mutation refused;
- exact prepared/final/retained identity, stage-absence and content-fact checks,
  with normal stage, recovered final and nonnull-RunningAt settlement all using
  the one central scanner to fsync every regular file/directory/root, followed
  by exact parent fsync and full stable recheck; null RunningAt forbids rewrite
  and `inner.activate` never stands in for provider write authority;
- stable prior prepared-stage absence, parent fsync and both-name recheck;
- prepared abandonment refusing even inspection/removal until every Change-
  mutating child/runner/group/descriptor authority is positively absent;
- both prepared-to-available arms fsyncing the exact retained parent after
  rename/final checks and revalidating the parent descriptor, final binding,
  content commitment and stage absence before Store commit;
- prepared-to-available refusal while the deterministic stage is present,
  reappears or is replaced around the parent-binding check, and publication-
  recovery settlement refusal before result consumption, runner release,
  terminal closure or any other settlement prerequisite;
- Change-parent FD 11 exclusion from Git/provider and outer-runner duplicate
  closure after one-shot child preparation;
- canonical no-replace `AttemptResult` creation, closed union and attempt/
  runtime/inode/digest authentication in both live and recovery paths, followed
  by common-consumer open-file fsync, revalidation, directory fsync and final
  revalidation before Store consumption;
- complete publisher-pre-fsync residue promoted to durable evidence; partial/
  malformed/replaced result retained as nonterminal and never repaired or
  overwritten; exact spool removal only after durable Store postconditions;
- `inner_unregistered_converged` only when the launch primitive proves `cmd.Start` failed,
  and `inner_converged` after every successful spawn has positive group absence
  while the leader is unreaped, then sole Wait, including readiness failure and
  each post-ready state;
- exact positive birth acquisition and process identity equality after
  `AttemptInnerReady`; leader retained unreaped through descendant convergence
  and positive group absence before sole Wait; no numeric group signal/probe
  after Wait; serial final drain/PTY close before publication; no publication on
  pre-identity/other uncertainty, no respawn, best-effort frame and no ACK wait;
- concrete atomic provider-pair consumption for active matching and declared
  empty spawned-child rows without relying on nondurable readiness receipt,
  exact result-identity binding, ProviderExit, runtime/runner/session
  transitions, split/mismatch and generic release refusal, and no terminal
  closure by result consumption;
- exact `FailureSpawn`, `FailureActivation` and `FailureProviderExit` mapping;
  ProviderExit sequence 1 with exact code-or-signal and replay-preserved Store
  commit timestamp;
- declared-pair `inner.activate` absence, refusal when it is present, and no
  marker requirement for an active matching child that was never released;
- refusal to reconstruct result authority from outer PID absence, control EOF,
  lifetime flock or still-empty resource rows when the spool is absent/corrupt;
- exact outer Wait/recovered absence without signal, runner release before one
  concrete terminal close from releasing/unresolved only, exact spool/output
  validation, declared/active-finalizing corruption refusal, terminal close
  before Change settlement, and pair/runner/session postconditions before spool
  removal;
- stable post-unlink spool absence accepted only after those durable
  postconditions and runtime-directory fsync/recheck, never before them;
- exact `factory-runner --change-worker-shell` worker executable with no
  daemon-hosted worker fallback;
- intermediate transition replay rejected after later aggregate progress;
- historical terminal replay after one and multiple Change retries.

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

Integrated Go sub-gate checkpoint on head `b5e42ee`:

- `scripts/go-check.sh` and `scripts/go-ci.sh` now share one concrete fast
  stage. The latter acquires the repository lease once and owns serial full Go,
  race and TypeScript stages; it does not expose a caller-forgeable lease
  marker. Stage environments use owned scratch HOME/cache/config locations and
  captured absolute Node/Corepack tools, never the operator's HOME or Git
  credential configuration.
- Bounded stage supervision creates a fresh process group before exec, forwards
  TERM, joins descendants, distinguishes timeout (`124`) from unresolved
  cleanup (`125`), and refuses a pass while the group remains live or uncertain.
  Exact-root identity checks precede output redirection and cleanup; ordinary
  read-only module caches are made owner-writable only after that check and are
  then removed. SIGKILL can still retain a mode-0700 quarantine and requires the
  final external census; malicious concurrent same-UID pathname races remain
  outside the cooperative gate threat model.
- Independent reviews blocked earlier candidates for ambient bootstrap tools,
  disabled Corepack integrity, nondeterministic timeout status, unsafe output
  redirection, weak tool identity, Git redirection, forgeable lease ownership,
  unjoined TERM, a real Node/Corepack failure and leaked read-only caches. The
  repaired exact head `50462ea` passed the real focused gate, the shell attack
  harness twice, syntax/diff checks, old-Node refusal and exact cleanup; the
  reviewer returned **ALLOW**.
- After unchanged integration the orchestrator ran `scripts/test-go-gates.sh`
  and `git diff --check` successfully, then ran `scripts/go-check.sh`. Its first
  attempt failed before testing because the sandbox denied DNS to the pinned Go
  proxy; the authorized dependency-download rerun passed module verification,
  vet, kernel (`12.958s`), browser protocol (`0.586s`) and SQLite contract
  (`11.017s`) tests, frozen pnpm 11.19.0 install, TypeScript typecheck and all ten
  client tests (`452ms`), plus the final diff check. The failed network attempt
  is not counted as green evidence.
- Final shell-provider/browser E2E and the exact post-test process/socket/PTY/
  goroutine census are intentionally not claimed by this checkpoint. They must
  be added before the hard-cutover gate can pass. The roughly 514 production
  shell lines and duplicated entrypoint setup remain explicit targets for the
  scheduled whole-runtime elegance audit.

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
| Announcements/theme/four-view TUI residue | Superseded by the public BUILDING/AGENT/NEEDS YOU web application | browser behavior/authority tests for retained views/actions |
| CLI aliases/usage/read-only storage status | Duplicate/no-action surface; no scheduling consumer | one-command-per-operation CLI tests and operator-action inventory |
| Combined-agent update and display-string actions | Duplicate validation and lost-update/control risk | granular revision-conflict and typed-action tests |
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

### Global admission and starvation

V1 deliberately uses one strict global order and no memory fairness cursor.
Sustained higher-priority arrivals can therefore starve lower-priority queued
work; this is visible policy, not a scheduler bug to hide with round-robin
state. A later fairness policy must be represented and selected durably inside
the same immediate Store transaction so restart and concurrent schedulers make
the same decision.

### Process supervision

Go's `exec.Cmd`, contexts, and goroutines do not supply ownership proof.
Every live child needs one owner through `Wait`; every error path must kill and
reap; a process group can outlive its leader. Darwin process birth identity is
a focused design/review item. Weak identity is safe only if it remains visibly
unresolved and cannot authorize a signal or removal.

Runner `starting` deliberately makes the outer Start ambiguity visible. A
daemon crash after the durable permit but before exact PID/birth binding can
leave an admitted run that cannot safely be cancelled, signalled, relaunched or
terminalized automatically. That operationally awkward state is preferable to
inventing no-child proof; implementation and UI must expose it explicitly.

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
SQLite. The one central Change scanner must prove file-data and directory-entry
durability for initial stage, recovered final and provider-written settlement;
a digest alone cannot close dirty-page or rename/parent-fsync cuts.
Cross-filesystem rename and crash ambiguity must fail visibly.

The all-nonterminal run capacity bounds the count of ambiguous `reserved`
residues because each belongs to one such run. The intended pre-`prepared`
residue is empty,
and every accepted tree is entry/byte/depth bounded. A same-UID-replaced present
reserved stage is deliberately neither traversed nor deleted, however, so those
scanner limits do not bound its bytes. A concrete storage bound for that
adversarial residue remains a cutover gate; diagnostics expose it and recovery
stays fail-closed rather than inventing authority or remediation state.
Aggregate count/byte retention for terminal Changes is also an unresolved
cutover gate and is never presented as current admission authority.

Darwin does not provide an inode-conditional `unlinkat`. Local-API socket
cleanup therefore performs a descriptor-relative identity check immediately
before descriptor-relative unlink and preserves every substitution it
observes, but it does not claim an impossible compare-and-unlink guarantee
across the final syscall boundary. The held home flock excludes a cooperating
second daemon. A deliberately racing same-EUID process is outside this local
trust boundary (and can already read the owner-only operator token); the
residual final-stat-to-unlink window remains explicit rather than disguised by
a test hook.

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
spawn, parse, and service errors; bind all three Go binaries to one
receipt/build identity; reject symlinked path
components and foreign plists; and recover every activation/reload cut. An
incompatible Go home is never activated over a Rust database, so pointer
rollback cannot masquerade as database rollback.

### Scope pressure

The issue backlog contains useful future product work that is not required for
the rewrite. Kernel correctness, basic useful web operation, and local macOS
replacement define cutover. Intake, auto-update, Linux release, new
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
- at-most-one provider witness, no terminal-input replay, and exact terminal
  convergence across daemon/runner death;
- `EPERM`/`ESRCH`/malformed/reuse/leader-loss behavior green with no unsafe
  signal or invented release;
- Change exact-base/bounded-tree/Git-boundary/removal matrix green;
- a concrete terminal Change retention count/byte policy and bounded handling
  of an adversarially replaced reserved residue are implemented and green;
- `None`/non-success/orchestrator no-verifier proofs and configured
  `RustWorkspaceTest`/`GoWorkspaceTest` stable-snapshot, once-only recovery,
  controlled-environment, and exact resource-cleanup proofs green;
- public privacy/size matrix, invalidation sequence/gap/resync, and canonical
  client agreement green;
- browser typed-operation authority and basic useful interactive shell-provider
  flow green;
- deterministic fake Claude/Codex native launch/configuration tests green;
- `scripts/go-check.sh`, `scripts/go-ci.sh`, full race suite, serialized process
  suite, deterministic shell E2E, and post-test resource census green;
- fresh isolated macOS build/init/start/CLI/browser/task/restart/stop/uninstall E2E
  green without observing or changing the live installation;
- exact three-member, traversal/link/duplicate/extra-resistant archive tests;
  deterministic Darwin ARM64/AMD64 artifacts; one receipt/build identity; and
  crash-safe fresh-install/reload/recovery tests green;
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
- a clean checkout builds all three Go binaries and the public web packages,
  runs the authoritative gates,
  and ends with a clean process/path/resource census.

Only then may the cutover commit make Go the sole local runtime, delete the
replaced Rust local-runtime packages and obsolete root workspace/build/release paths, update the
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
- Proved leader exit does not imply group absence: `EPERM` retains the exact
  unreaped owner and live descendant as unresolved until a later retry observes
  real group quiescence. TERM-ignore requires bounded KILL escalation,
  create-only/no-follow markers do not replace, leash
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

Fresh-home SQLite publication uses one bounded kernel seam rather than a
path-based partial-home recovery protocol. `NewDatabaseImage` builds the one
canonical schema and initial control row through the normal pinned SQLite VFS
in an unpredictable current-user 0700 scratch directory with one create-only,
one-link, exact 0600 main file. It returns an at-most 8 MiB rollback-header
image only after every SQLite handle closes and the scratch contains no
sidecar. The unused path-publishing
`kernel.Create` API was deleted: production publication belongs to the install
layer's descriptor-owned staging directory, while tests compose
`NewDatabaseImage`, one create-only 0600 write, and `Open` in test-only helpers.
This removes a sidecar-ownership lifecycle from the kernel instead of teaching
it to adopt SQLite-recreated pathnames.

`InspectImmutable` bounds the declared image at 256 MiB before allocation,
copies it from the caller's stable `ReaderAt` in exact 128 KiB chunks, writes
those owned bytes into a second private scratch, and opens it read-only and
immutable through the normal SQLite VFS. The image path has no custom VFS,
registry, shared name, channel, counter or mutex; the only global registration
is the ordinary pinned `database/sql` driver. Any caller error is preserved even
when a full read also returns or wraps EOF; short nil-error reads fail.
`InspectPristine` reads the already-validated dynamic daemon ID, configuration
and timestamp, rebuilds the canonical image by the same fixed transaction, and
requires byte-for-byte equality including SQLite history headers and free page
contents. Thus insert/delete, change/restore, `VACUUM`, `ANALYZE`, retained rows,
schema history and a mature WAL header fail even when current rows and free-page
counts look fresh. Both APIs leave reader ownership with the caller; the caller
owns the home lifetime lock, stable-descriptor and no-sidecar proof.

Scratch is disposable validation data, never durable authority. Its threat
contract is same-EUID process-local use: an unpredictable name beneath the
process-selected temporary root, a revalidated current-user 0700 directory and
exact regular 0600 one-link file are sufficient because no identifier or bytes
escape before cleanup and no scratch artifact is adopted after restart. All
close and removal errors are joined and image creation returns no bytes on
cleanup uncertainty. This does not claim hostile same-EUID containment; the
authoritative home uses retained descriptors and a lifetime lock instead.

Mature WAL reopen requires one clean canonical absolute path, walks every
parent component descriptor-relatively from root without following links,
retains every component binding through `Open`, and requires the final database
parent to be an exact current-user 0700 directory. It pins exact owner-only
regular main/WAL/SHM files, bounds main at 256 MiB, WAL at 272 MiB and SHM at
4 MiB, and validates a nonempty WAL header before SQLite sees it. The complete
main plus WAL is streamed into a private 0700 disposable directory; SHM is not
copied because it is a rebuildable cache. The pinned SQLite build reconstructs
only the disposable SHM and validates the recovered schema, controls and
integrity there. Only then may configured pools touch the authoritative files,
whose parent and pathname bindings stay pinned through the final validation.
SQLite remains the authority for valid-frame/crash-tail recovery; the kernel
does not duplicate its WAL frame parser. All runtime sidecars are exact regular
0600 files; malformed pairs, hot rollback journals, unsafe identities and
over-bound files fail before authoritative SQLite open.

SHA-256 digests and exact lengths bind disposable validation to the pinned
main, WAL and SHM bytes immediately before the real path open. Any same-inode
content or length change is one visible fail-closed error; `Open` never retries
or spins behind a concurrent writer. A final component, binding, identity and
mode recheck follows. The caller's external home lifetime lock spans the entire
`Open` call, including SQLite's unavoidable final path open; the kernel does
not add a second locking abstraction. Fresh rollback activation reserves the
SHM with an exact create-only descriptor so SQLite cannot widen its mode.
Failed activation leaves bounded sidecar evidence visible and never unlinks a
pathname whose creation identity it cannot prove.

The focused mutation pass removed each guard temporarily and then restored it.
The scratch-required test killed substituting the old `ext/serdes` global VFS
and channel; the literal-prefix matrix killed the former wildcard predicate;
the reversible-history matrix killed removal of canonical byte comparison; the
concurrent-writer test killed retrying a changed WAL snapshot; the failed-
activation evidence test killed pathname unlink; and the path-authority matrix
killed omitted ancestor binding and final-parent mode checks.
`TestOpenRefusesRetainedRollbackDatabaseWithoutMutation` killed removal of the
fresh-only rollback guard; `TestInspectPristineRequiresExactFreshRollbackState`
killed permitting `sqlite_stat`/`ANALYZE` state; the immutable corruption test
killed short-nil reads and ignoring full wrapped-EOF caller errors; the
pre-read oversize test killed removal of the 256 MiB bound;
`TestDatabaseFileBindingsAreRecheckedAfterPreflight` killed omitted main/WAL/SHM
digest comparison; `TestRejectedOpenPreservesStoreCloseError` killed discarding
the failed Store close; and `TestValidateWALHeaderFailsClosed` killed a disabled
malformed-WAL validator. `Store.initialize`, `openDatabaseParent`, the global
readervfs/serdes image path, retry-only control flow, guessed sidecar removal,
the manual pristine table/internal-object walk and path-based `Create`
ownership were deleted. No mutation remains in the tree.

The repaired pre-rebase candidate passed the focused
image/pristine/WAL/path/live-writer matrix three times (`14.532s`), the focused
race matrix (`68.203s`; `70.50s` wall), the full kernel suite (`26.310s`;
`26.62s` wall), and the full kernel race suite (`484.259s`; `484.84s` wall).
`go vet`, the complete `scripts/go-check.sh` gate, daemon/E2E compile-only
checks, Linux amd64 CGO-free kernel compilation and diff checks passed. The
race cost is reported honestly: cryptographic snapshot continuity makes the
300-open/2,000-write stress more expensive, while ordinary focused and full
non-race time remains under 27 seconds.

The database capability now occupies 1,594 production lines across the
351-line Store/transaction core, 457-line image/immutable file and 786-line
existing-file/WAL file; exact schema validation is another 438 lines. Against
the independently blocked `b42e418` candidate, those four files grow from
1,843 to 2,032 lines (+189). That increase is the concrete private-scratch,
canonical-history, descriptor-walk, identity/mode/bound/digest and crash-tail
proof, not compatibility or a framework. In exchange, the repair deletes the
exported path-creation lifecycle, `Store.initialize`, `openDatabaseParent`,
the custom global VFS registration/channel/counter/error recorder, retry-only
control flow, guessed sidecar removal and the manual pristine-state walk. The
focused image/path/schema tests occupy 2,118 lines, up from 1,649 in the blocked
candidate; explicit package-local test bootstrap helpers replace production
`Create` without exporting test or publication policy.

Admission, attempt authority and finalization Store proof on integrated head
`3f2c255`:

- Added canonical in-transaction admission, one attempt digest, the durable
  Change checkpoints, exact four-resource ledger, explicit
  admitted/running/finalizing/terminal phases, first proposal, exit
  observation, recovery reads and finalizer-only terminalization without a new
  table or compatibility state. Concrete lost-response and SIGKILL cuts cover
  `AdmitNext` and `FinalizeRun`; `internal/sqlitecontract` remains because its
  wider driver/process matrix is not yet fully duplicated by domain methods.
- Four independent BLOCK/repair cycles found and closed real green-suite gaps:
  broad same-run process identity aliasing, resource release with a live
  credential, aliased Change locators, an incomplete verification-refinement
  schema, corrupt run/task joins that still authenticated, overlapping runtime
  and retained-Change cleanup authority, configured success bypassing
  verification, and public mutations replaying/committing under pre-existing
  durable-graph corruption.
- Only same-run provider-process/provider-group records may share an exact
  process identity. Resource activation is admitted-only; cleanup is
  finalizing-only. Every run/task/Change/resource join and every canonical
  runtime/source/staging locator is validated fail-closed, including lexical
  ancestor/descendant overlap. Configured worker success remains finalizing;
  the speculative verification-result API and `unverifiable` state were
  deleted until a complete durable verifier-attempt/result lifecycle exists.
- Every one of the 19 public mutators now enters one literal
  `BEGIN IMMEDIATE`, validates the complete durable controls, relationships,
  identity collisions, ownership locators and invalidation sequence on that
  same pinned writer, then performs replay/decision/DML. The raw write entry is
  initializer-only. A 57-case mutator-by-corruption matrix and exact resource/
  Change replay attacks prove validation precedes every successful effect.
- The author passed kernel count-three (`25.793s`), kernel race (`232.156s`),
  CGO-free/full/vet/module/cross-build gates and clean censuses. The final
  reviewer reran the focused 57-case matrix (`0.798s`) and returned ALLOW on
  exact author head `41ea79a`; unchanged cherry-pick produced integrated head
  `3f2c255`. The orchestrator's first two integration-test launches did not run
  because sandboxed default Go cache/module parents were unwritable; the
  approved cache-enabled rerun passed `internal/kernel` in `8.854s`.
- Full-graph validation measured about 179 microseconds incremental on one
  project/agent/task/Change/admitted-run/four-resource fixture (about 30.7x
  transaction-entry cost, still sub-millisecond). This deliberate correctness
  cost must be measured and simplified only through a separately reviewed
  proof-preserving design during the scheduled elegance/performance audit.

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

Sealed Git selection/blob proof on integrated head `dcceb83`:

- Added one concrete Darwin Git boundary. `SelectGit` resolves one exact commit,
  freezes the repository and trusted Git executable identities, computes the
  complete bounded manifest without reading materialized blob bodies, and
  returns immutable selection authority. `OpenGitBlobs` later reads only the
  selected OIDs in order and verifies type, declared size, Git blob hash,
  delimiter, exact EOF and child exit. No Git library, repository abstraction,
  lazy fetch, worktree/archive path or provider-visible `.git` authority was
  added.
- Public execution accepts only a root-owned, mode-safe, native Git below the
  fixed Command Line Tools or Xcode Developer path grammar. Every path
  component is opened no-follow; the executable metadata, architecture and
  SHA-256 are rechecked around each phase. `/usr/bin/git`, PATH/xcrun lookup at
  execution, scripts, symlinks, renamed user-owned Mach-O files and forged
  selections fail before effect.
- The repository root, `.git`, config and complete supported object-store
  grammar are owner/device/mode/link/type checked and structurally committed.
  The bounded no-follow scanner covers loose objects, packs, indexes, MIDX,
  bitmaps and commit graphs, rejects alternates/promisor/unknown/deep/special
  layouts, and rechecks between every metadata phase and blob read. Git child
  pipes and the exact process group remain synchronously owned through one
  Wait; cancellation and leader-first descendants are cleaned before return.
- The hard-cutover config contract deliberately supports one bounded ASCII
  `.git/config` only. BOM, C0/DEL/non-ASCII controls, includes/includeIf,
  `extensions.worktreeConfig`, and any `config.worktree` file, directory,
  symlink or FIFO are rejected before a Git child starts. This deletes the
  need to emulate Git's expanding config scopes and their duplicated authority.
- Independent attack review repeatedly BLOCKED apparently green heads and
  demonstrated eight initial process/config/blob/identity gaps, nested object
  indirection and Apple shim acceptance, writable repository authority and a
  renamed native witness, `config.worktree`/BOM external includes, and finally
  the omitted DEL byte. Each was repaired with causal public effects or
  zero-start witnesses. The reviewer returned ALLOW on exact author head
  `c3f7118`; the unchanged stack integrated through `dcceb83`.
- Author exact-head gates included Change count-three (`39.310s`), race
  (`28.076s`), CGO-free (`13.094s`), full Go, vet/module/format/diff,
  cross-build and clean process/temp evidence across the substantive repairs.
  The final one-byte DEL repair reran its focused matrix count-three
  (`0.550s`). The orchestrator's first integrated Change run failed because
  the default macOS `/var` temporary locator resolves through `/private/var`
  and the canonical-path fixtures correctly refused that alias. The explicit
  canonical isolated `TMPDIR=/private/tmp/df-root-change-tmp` rerun passed
  `internal/change` in `13.221s`.
- Remaining explicit costs/boundaries are complete object-store rechecks before
  each blob, deliberate refusal of unknown future Git layouts, root trust for
  Developer tools, and the documented cooperative same-UID mutation seam.
  Realistic large-repository cost must be measured during the scheduled
  elegance/performance audit without deleting the causal checkpoints.

Selected-manifest Store/recovery proof on integrated head `dd72fbb`:

- `RecordChangeSelection` now commits the exact base-bound manifest digest,
  entry count and total blob bytes with commit/repository authority before any
  materialized blob read. Prepared and available phases require those facts.
  `MarkChangeAvailable` compares all three independently observed plain-tree
  facts plus exact prepared source identity, but persists only source identity
  and time; reads derive availability facts from the one selected copy. No
  duplicate durable digest/count/bytes or compatibility columns remain.
- Crash/reopen and replay tests prove selected facts survive, exact replay is
  revision/invalidation stable, and every digest/count/bytes mismatch has zero
  durable footprint. The fresh schema explicitly requires both available
  source identity components rather than relying on a SQLite `CHECK` whose
  NULL result would otherwise pass.
- Independent review BLOCKED the first repair after proving that ncruces scans
  a present `zeroblob(0)` into a nil byte slice. An already-open Store therefore
  mistook a corrupt selected digest for SQL NULL and allowed a later validated
  factory write. One concrete `nullableBlob` scanner now preserves NULL versus
  present zero-length values and rejects wrong driver types for every nullable
  durable BLOB: Change selected commit/digest, run Change ID and resource birth
  digest. Direct reads, reopen and the complete validated-write entry all fail
  closed without an invalidation or state footprint. The exact repaired head
  `c75f56e` received independent ALLOW.
- Mutations restoring nil-slice presence logic were killed by all four field
  attacks; removing selected/available equality or source non-NULL guards was
  killed by the intended restart/schema tests. Author gates passed kernel
  count-three (`26.114s`), race (`238.065s`), CGO-free (`8.983s`), vet/module/
  format/diff and Darwin/Linux AMD64 cross-builds. The unchanged integrated
  root passed `internal/kernel` in `8.964s` with the pinned Go toolchain and
  canonical isolated temporary root.

Two-owner runner/process proof on integrated head `85d4841`:

- `internal/runner` now contains one concrete outer attempt runner and one
  inner registered wrapper/provider child. The daemon releases an inert outer
  gate only after recording its exact identity; the outer runner records and
  reports the inner identity before any wrapper effect. A fixed FD 3 control
  capability and closed selection/preparation/population/provider releases
  replace arbitrary inherited descriptors or a generic supervisor protocol.
- The attempt runner is synchronous: one kqueue multiplexes controller,
  wrapper and exact child-exit observations; no goroutine, channel, interface
  or actor abstraction was added. The runner is provider-blind, owns the one
  direct child and process group through the sole Wait, preserves the inner
  PID/PGID/birth across the wrapper's final provider pathname exec, and closes
  private control descriptors before provider code runs.
- EOF before provider release aborts or converges the registered inner work;
  EOF after provider release never cancels useful authorized work. The
  then-named terminal record was create-only and published only after exact
  group convergence and the sole Wait. Its recovery acknowledgement bound the
  full attempt/process/exit/message value, digest and file identity and removed
  the spool only after the caller asserted the exact Store commit. The current
  contract replaces that artifact with the smaller two-kind `AttemptResult`.
  Numeric recovered identities remain observation-only and gain no signal path.
- Independent reviews repeatedly BLOCKED green heads after finding natural
  leader exit before descendant cleanup, terminal publication on cleanup
  uncertainty, inexact terminal acknowledgement, inert pre-release exit with
  no Wait branch, protocol EOF events starving process convergence, and a
  transient EOF-filter deletion failure terminating an already-released
  provider. Repairs centralized waited-only publication, retained the live
  owner during uncertainty, made the child-state switch exhaustive, retired
  protocol filters before process-only convergence and kept post-release
  retirement uncertainty inside the natural-completion loop. Exact immutable
  review head `06d9f59` received ALLOW.
- Causal real-process tests cover ordered zero-effect releases, parent/control
  EOF at every stage, same-PID shell exec and input-once EOF, executable
  replacement/mode/byte/symlink/removal, leader-first and TERM-ignoring
  descendants, first-N and permanent process/filter uncertainty, inert exit,
  repeated malformed/readable EOF, exact spool replay/forgery, and clean
  process/FD/goroutine/temp censuses. Required mutations removed ordering,
  waited publication, uncertainty retry, full-terminal equality, inert-exit
  handling, process-only filter transition and post-release retirement
  isolation; each was killed and reverted.
- Final author gates passed runner/command count-three (`16.13s`), race
  (`29.27s`), CGO-free (`5.99s`), full Go (`11.57s`), vet/module/format/diff
  and Darwin/Linux AMD64 cross-builds. The orchestrator's first integrated run
  reached two native-witness fixture build failures because their intentionally
  cleared environment had no `GOCACHE` or `HOME`. The explicit task-owned
  `GOCACHE`, `GOPATH`, `GOMODCACHE` and canonical TMPDIR rerun passed runner
  (`5.556s`) and command (`0.460s`); no Dark Factory home or provider was used.
- Permanent Darwin observation/filter uncertainty deliberately retains the
  exact runner and unreaped child without terminal publication. This is
  fail-closed ownership, not convergence. The later elegance audit must review
  production-compiled test seams and repeated frame validation, but may not
  collapse either ownership boundary or the explicit uncertainty behavior.

Bounded local-API client proof on integrated head `3ca153f`:

- `internal/api` exposes only six operator calls (`health`, `snapshot`,
  `create_project`, `create_shell_agent`, `enqueue_task`, `set_dispatch`),
  three attempt outcome calls (`succeed`, `block`, `fail`) and the bounded
  attempt-only `request_human` call. The attempt client
  can discover only its one token-file environment variable; it cannot name or
  fall back to operator authority. Attempt requests carry no run/task/entity ID
  or caller-selected failure code.
- The private wire is generation 1, one length-bounded request and response per
  Unix connection, with explicit operator/attempt domains and raw 32-byte
  bearers. Clients half-close after the request, require response EOF, and
  recheck the exact socket and token identities around each call. Public DTOs
  can represent only the bounded canonical dashboard projection; task bodies,
  roots, credentials, provider input/output, source data and private result
  detail have no public field.
- Every path component is opened descriptor-relative from `/` without following
  symlinks. Token opens are nonblocking and accept exactly one EUID-owned,
  mode-0600, single-link, 32-byte regular file; socket inspection requires the
  exact private owner/mode/type/link/identity chain and verifies the connected
  peer UID. This closes FIFO blocking, leaf symlink, intermediate-component
  replacement and post-dial socket swap attacks without a general filesystem
  abstraction.
- JSON decoding first validates UTF-8, depth, exact object-name uniqueness and
  the canonical `[a-z][a-z0-9_]*` member grammar recursively, then uses
  `DisallowUnknownFields` and exact EOF. This rejects the case-fold aliases
  accepted by Go's ordinary struct decoder, including uppercase escapes and
  Unicode Kelvin-sign folds, while retaining escaped canonical lowercase
  names.
- Independent review BLOCKED three successive heads for a regular-file-to-FIFO
  token race, intermediate symlink replacement, duplicate/malformed JSON, and
  finally case-folded field aliases. Each became a causal test; exact immutable
  review head `033812e` received ALLOW. No router, middleware stack, generic
  `Do`, retry loop, watch stream, request ID or compatibility generation was
  introduced.
- Final author gates passed API count-three (`2.154s`), race (`1.945s`),
  CGO-free (`0.859s`), vet/format/diff, Darwin/Linux AMD64 cross-builds and
  clean FD/goroutine/process/temp censuses. After unchanged integration the
  orchestrator passed `internal/api` in `0.898s` using task-owned caches and a
  canonical isolated temporary root.
- The server half is intentionally not claimed by this proof. It must preserve
  the same framing and path predicates, make peer UID a prerequisite rather
  than authentication, compare only the credential for the declared domain,
  call the atomic attempt outcome mutator directly, bound every handler and
  connection lifetime, and return only fixed sanitized errors.

Prepared-Change crash adoption proof on integrated head `ebb88b8`:

- `AdoptPrepared` closes the crash cut after the caller-declared staging
  directory is created but before its exact identity reaches SQLite. It adopts
  only an existing empty EUID-owned, mode-0700, single-link ordinary directory
  beneath the exact no-follow parent while the final target remains absent,
  then returns the normal `Prepared` lifecycle. It does not delete, repair,
  rename, populate or publish during adoption; ordinary `Prepare` remains
  create-only.
- The parent, stage pathname and open descriptor identities are checked around
  open, emptiness and target-absence observations. Parent/stage swaps,
  nonempty/deep/wide/special content, wrong metadata, target appearance and
  cancellation all retain the existing object and fail visibly. The later
  populate path rechecks stage emptiness and identity before its first effect,
  covering a cooperative same-UID insertion after adoption returns.
- A mutation deleting the final target-absence recheck was killed by the
  target-appearance test. Author gates passed focused count-three, full Change
  (`13.835s`), race (`28.179s`), CGO-free (`13.158s`), vet/cross-build/format/
  diff and descriptor/process/temp censuses. Independent exact-head review
  returned ALLOW; the integrated focused adoption/unsupported run passed in
  `0.314s`.
- Concurrent adopters can retain handles to the same verified stage, but gain
  no mutation authority and create-only publication remains the arbiter. The
  daemon is the sole coordinator; a true concurrent-adoption stress case is
  retained for the integrated crash matrix rather than adding a lock or second
  durable owner to this package.

Recovered-runner-absence Store proof on integrated head `d2b3e97`:

- Runner exit evidence now has one explicit closed discriminant: `code`,
  `signal` or `recovered_absence`. The last variant carries the exact per-run
  observation sequence and time but no invented wait status. It means only
  that the live owner was lost and a recovery caller separately proved the
  exact registered runner identity disappeared; uncertainty, malformed facts
  and permission failures cannot construct that proof accidentally.
- The fresh schema, scanner and Go value exhaustively bind kind/status NULL
  shape. Admitted/running rows cannot already carry exit evidence. Exact replay
  is idempotent, conflicting later evidence is rejected, the first outcome
  still wins, attempt authority is revoked on finalizing, and terminalization
  still requires every resource released.
- Independent review BLOCKED the first head after demonstrating that a
  constraint-bypassed `declared` runner row could smuggle PID/PGID/birth and
  become recovered-exit authority. The schema and scanner now require every
  declared resource to have no bound identity, and the recovered-exit mutator
  independently requires a non-declared runner identity. The attack is causal
  across schema, reopen, direct reads, every validated write and recovered
  observation, including provider process/group and runtime-path variants.
- Mutations accepting an unknown exit kind and omitting the scanner's declared
  identity rejection were killed and removed. Author gates passed focused
  count-three, full kernel (`9.310s` after the repair), race, CGO-free, vet,
  cross-build/format/diff and clean censuses. Exact-head re-review returned
  ALLOW; the unchanged integrated focused run passed in `0.337s`.
- The Store validates evidence shape and prior durable registration, not OS
  liveness. The later daemon recovery test must prove that only exact
  `Absent`/`Reused` observation reaches this constructor and that `Unknown`,
  `EPERM` or weak identity remains unresolved.

Private local-API server proof on integrated head `1ca7a31`:

- `internal/api` now owns one concrete synchronous `Listener` and opaque
  one-shot `Connection`, but no Store import, handler interface, router,
  callback framework or goroutine accept loop. The daemon owns accept-handler
  registration and joins; each `Receive` owns only the short-lived cancellation
  watcher required to unblock socket I/O and joins it before returning.
- Listener creation requires the exact private parent/token predicates, records
  the mode-0600 Unix socket identity, verifies peer EUID on accept and removes
  only that recorded socket identity on close. Operator authentication and
  token identity rechecks remain inside transport. An attempt raw bearer is
  erased after conversion to one redacted SHA-256 digest; no raw bearer is
  representable in the decoded call.
- A closed nine-call union and fixed reply constructors preserve the exact
  domain/method matrix and bounded public DTOs. Request EOF is required before
  dispatch. Mutation replies deliberately carry the canonical observed `head`
  (zero is valid) plus positive entity revision rather than falsely claiming
  one exact invalidation identity.
- Independent review BLOCKED the first server head three times: empty
  `fail.detail` contradicted the fixed kernel attempt-failure contract; context
  cancellation could retain a partial header/payload/missing-EOF connection
  until the five-second deadline; and Go's JSON decoder silently replaced lone
  UTF-16 surrogate escapes with U+FFFD. Separate repairs permit empty failure
  only (Block stays nonempty), join cancellation at all three cuts with zero
  dispatch, and lexically reject lone/reversed/mismatched surrogate escapes on
  both request and response while preserving valid pairs and literal U+FFFD.
- Domain-guard, surrogate and cancellation mutations were killed and removed.
  Final author gates passed API count-three (`4.322s`), race (`2.872s`),
  CGO-free (`1.650s`), vet/format/diff, cross-builds and exact FD/goroutine/
  socket/process/temp censuses. Exact-head re-review returned ALLOW; the
  unchanged integrated API suite passed in `1.675s`.
- Linux remains compile-only. Unix pathname bind has no descriptor-relative
  primitive: a cooperative same-EUID parent replacement is detected after
  bind and fails closed, but can leave an inert socket artifact in the replaced
  parent. The integrated creation-race and cleanup census must keep that
  residual visible; it is not authority to unlink an unrecorded path.

Minimal typed attempt CLI proof on integrated head `f812ae6`:

- The initial Go `factoryctl` has only three commands: `attempt succeed`,
  `attempt block` and `attempt fail`. A direct fixed-arity parser rejects
  positional aliases, equals syntax, mixed/duplicate/unknown flags, entity IDs,
  failure codes, socket/token selectors, result files and extra arguments
  before it reads environment state or constructs a client. No CLI framework,
  command interface, retry or goroutine was added.
- The command reads only a nonempty `DARK_FACTORY_SOCKET`; the reviewed
  `AttemptClient` alone discovers the attempt token file. A missing/invalid
  attempt token cannot fall back to operator authority, including when an
  operator token is present. Empty success/failure text remains valid and an
  empty Block remains invalid.
- One five-second-bounded typed API call returns a fixed redacted acceptance
  line with canonical head and revision. It says only that the outcome request
  was accepted and never claims the task or run is terminal/successful. Result,
  detail, token, bearer, socket/path and wrapped-error sentinels never appear in
  stdout or stderr.
- Mutating the success line to claim terminal success was killed and reverted.
  Author gates passed count-three (`0.560s`), race (`1.385s`), CGO-free
  (`0.342s`), vet/format/diff/cross-build and exact FD/goroutine/socket/process/
  temp censuses. Independent exact-head review returned ALLOW; the unchanged
  integrated command suite passed in `0.370s`.
- The accepted result/detail necessarily appears in the provider's command
  argv when a human invokes it directly. The bearer does not. Broken output
  pipes currently do not change the already-completed API result; output-error
  policy and any later non-attempt commands remain explicit subjects for the
  CLI portion of the final elegance audit.

Exact provider `request-human` proof on integrated head `d4ce713`:

- `factoryctl attempt request-human --idempotency-key HEX32 --question TEXT`
  is the only added spelling. It validates an exact lowercase, nonzero 32-byte
  hex identity and bounded UTF-8/NUL-free question before environment access,
  then uses only `AttemptClient`; operator authority is never a fallback.
- Historical evidence from that old integrated slice recorded the exact
  absolute native `factoryctl` helper before provider execution. The current
  V1 rule supersedes its timing: admission freezes provider/model/effort only;
  post-admission the daemon resolves/seals the helper `Installation`, and
  `provider.Build` consumes and revalidates those launch facts immediately
  before release. The provider receives only that locator; its cleared `PATH`
  remains `/usr/bin:/bin`.
- A deterministic shell-provider test invokes the exact helper twice with one
  idempotency identity and observes exactly one durable HumanRequest, with no
  provider effect before the final release stage. Independent review returned
  ALLOW. The integrated causal test passed three repetitions in `3.870s` using
  only isolated temporary homes and the shell fixture.
- Same-UID replacement between the final pathname verification and `execve`
  remains a documented Darwin pathname TOCTOU outside the current threat model.
  The release layout must retain an immutable sibling `factoryctl` executable;
  this does not authorize path search or a compatibility fallback.

Closed runner environment proof on integrated head `c7d74cc`:

- `internal/runner` accepts one exact, ordered environment grammar shared by
  new launch specifications and recovered attempt configuration. It rejects
  empty or malformed names, NULs, invalid UTF-8, exact duplicate names,
  forbidden proxy/provider/API/Git/GitHub/SSH/loader authority, entity
  metadata, ambient home variables and arbitrary additions before executable,
  working-directory or activation commitment.
- The grammar contains only the daemon-decided private socket and token-file
  locators plus the provider home/temp, deterministic executable/search and
  locale controls, Git/GitHub discovery controls and terminal/color
  controls. The runner does not inherit `os.Environ`, widen the list, normalize
  names or interpret provider-specific values; concrete providers must supply
  and test those exact values.
- Independent review BLOCKED the first head after demonstrating that invalid
  UTF-8 environment bytes survived runner validation but were silently changed
  by the JSON handoff. One `utf8.ValidString` guard now closes both fresh and
  recovered paths. Causal tests reject invalid bytes in names and values and
  prove valid multibyte bytes reach a real child unchanged. No second parser or
  wire compatibility path was added.
- Final author gates passed focused count-three (`0.326s`), full runner
  count-three (`16.199s`), selected race (`1.366s`), CGO-free (`5.555s`), vet,
  format and diff checks with a stable descriptor and process census. Exact-head
  review returned ALLOW; the unchanged integrated environment/config tests
  passed in `0.288s`.
- The retained `SHELL`, `USER`, `LOGNAME` and `LC_CTYPE` names remain a concrete
  question for provider integration and the final elegance audit. They must be
  removed if no shipped provider needs them; this proof freezes validation and
  ordering, not speculative visibility.

Interim code-elegance inventory on integrated head `6460de9`:

- A read-only reviewer who had not authored the integrated foundation traced
  the complete current Go package graph and returned **ALLOW continued kernel
  implementation, BLOCK final simplicity sign-off**. No ORM, DI container,
  router framework, workflow framework, generic repository, or production
  interface hierarchy exists. The current graph is still intentionally
  disconnected until the daemon becomes its concrete composition root.
- `internal/sqlitecontract` and `internal/processcontract` remain explicit
  deletion candidates, not permanent architecture. After the integrated
  kernel duplicates their unique busy/crash/transaction and Darwin process
  causal coverage through concrete Store/runner effects, those proof-only
  packages and any redundant tests must be removed rather than retained as a
  second abstraction layer.
- `AuthenticateAttempt`/`AttemptAuthority`, unused typed JSON/text marshal
  methods, and provisional exports must be rechecked after the API dispatcher
  and supervisor establish the only real consumers. Any still-unused surface
  is deleted. Attempt outcome handlers must continue to call the atomic Store
  proposal method directly; an operator cannot manufacture attempt authority.
- Production-compiled filesystem, Git, API and process fault hooks are retained
  only while the crash/mutation matrix needs them. The final elegance audit
  must either move or delete seams that no longer add causal proof. This may
  not remove the before/after observations at distinct trust boundaries, the
  runner's sole-Wait ownership, or the Store's pinned durable validation merely
  because those checks look repetitive.
- The final audit must also correct dependency metadata, decide the narrow
  unsupported-platform build shape, measure the complete Store-validation
  cost, and inspect the real post-daemon export/package graph. In-memory cached
  validation, generic scanners, and a generic transaction context are not
  accepted simplifications unless an independent proof shows SQLite remains
  the sole current authority.
- The static audit measured approximately 13,000 production Go lines at this
  intermediate head (`kernel` 5,063, `change` 3,405, `runner` 2,648, `api`
  1,770, command entry points 186). These are an interim trend point, not a
  success metric or final before/after claim. The reviewer ran only static
  source/package/export inspection, as required, so process, goroutine and
  crash safety remain kernel-gate questions rather than inferred green results.

Shell-provider candidate review at unintegrated head `d0ce0d9`:

- Independent exact-head re-review returned **BLOCK**. The repair successfully
  replaced a caller-selected Change pathname with an unforgeable `Published`
  value, reused the central Change scanner, rejected `.git` and upward Git
  discovery for an unchanged tree, and rechecked committed HOME/TMP identities
  at both runner activation boundaries. Focused, race, CGO-free, vet and
  cross-compile gates passed, but those green tests did not prove the final
  authority boundary.
- The reviewer changed an already-published ordinary file in place, preserving
  its size and root-directory identity, after `PrepareShell`. The registered
  process then executed and read the changed bytes because the runner's final
  gate retained only the working-directory identity, not the `Published` tree
  facts. The same gap could admit a nested `.git` inserted after the earlier
  scan. The final central `Reinspect` therefore belongs inside the registered
  Change worker after the provider release and immediately before its pathname-
  free provider exec; adding a runner tree walker is forbidden duplication.
- The reviewer also proved that caller-selected owner-only HOME/TMP directories
  outside any admitted runtime were accepted. Private HOME/TMP/socket/token/
  config locators must instead be derived from one unforgeable runtime binding
  and reopened descriptor-relatively. Lexical prefix checks are not authority.
  Canonical-then-path-open intermediate-component replacement remains part of
  that coordinated runtime/worker repair.
- This candidate is not integrated and is not counted as kernel progress. The
  repair is sequenced behind the independently reviewed runtime-parent/lifetime-
  lease contract so only one writer changes shared runner files. Required
  regression attacks are same-size content mutation, late `.git`, unrelated
  private directories, intermediate replacement, binding forgery and the
  unchanged successful shell path.

Runtime-ownership candidate review at unintegrated head `ca222fd`:

- The candidate added the reviewed cooperative `.runtime.lock`, exact
  `RuntimeBinding`, fixed top-level grammar, inherited lifetime flock, bounded
  descriptor-relative cleanup, zero-effect terminal-spool preflight, durable
  absence recheck and exact scratch cleanup. Its focused, race, CGO-free,
  cross-build and full Go gates passed, and mutations killed the parent-lock,
  lifetime-lock, binding-metadata, terminal-guard, hardlink, symlink and final-
  absence guards. Independent review nevertheless returned **BLOCK**.
- The reviewer traced the real current-exec path and found that FD 9 is marked
  close-on-exec and explicitly closed immediately before the final provider
  `exec`. Normal tests passed only because the outer runner retained another
  duplicate until its Wait. If that runner dies while the provider survives,
  the provider carries no lifetime lease; recovery can observe the lease as
  available and remove a live runtime. A real crash test must prove the final
  provider retains one least-privilege lifetime capability until its exit even
  after outer-runner death.
- The repair must not blindly expose a runtime-directory descriptor containing
  config/source authority to provider code. It must state and test the exact
  provider-visible descriptor set and use the smallest lifetime capability
  that survives both exec gates. No wrapper process, PID authority, second
  liveness state or generic lease framework is permitted.
- The reviewer also found that initial invalid metadata on the terminal scratch
  returned before exact-inode cleanup, leaving a cleanup-stalling residue. The
  repair must split observation failure from known invalid metadata and remove
  only the exact opened scratch identity, never a replacement.
- All earlier review items held: fresh independent product lock opens, exact
  creation cleanup, complete lock metadata checks, centralized runner names,
  nested cleanup progress semantics, zero-effect terminal guard, bounded reads/
  depth/descriptors and already-absent fsync/recheck. This candidate remains
  unintegrated until both new findings receive causal tests and exact-head
  independent ALLOW.

Runtime-ownership re-review at unintegrated head `b640b78`:

- The repaired candidate replaced the runtime-directory lease with a fixed
  empty read-only regular lifetime inode. A real provider retained only FD 10
  across final exec; FD 3/control and FD 9/runtime closed. The reviewer killed
  the outer runner and proved the same live provider kept recovery's fresh
  flock blocked, then released it on provider exit. The provider could not use
  FD 10 for `openat`, `fchdir`, write or truncate. The exact terminal-scratch
  residue repair also held. These close the previous two findings.
- Exact-head re-review still returned **BLOCK** after proving that
  `AdoptRuntime` created missing fixed children before it acquired an already-
  existing lifetime flock. A held/live runtime with a partial layout was
  therefore mutated before recovery returned busy. Existing lifetime authority
  must be acquired and rechecked before the first adoption effect; only a
  recognized pre-lifetime crash layout may create the lifetime under the
  cooperative parent lock.
- The reviewer also replaced the newly created lifetime inode during an
  injected creation failure. The inner exact cleanup retained/removed the
  original safely, but outer layout cleanup then unlinked the valid replacement
  by name. Failed-create cleanup must carry the exact lifetime identity created
  by that operation and may never unlink a replacement merely because its
  metadata looks valid.
- The next repair is deliberately limited to effect ordering and exact identity
  threading. All prior provider-lease, parent-lock, Binding, fixed-grammar,
  terminal preflight, bounded cleanup and scratch tests remain required, and
  the stack remains unintegrated until the same reviewer returns exact-head
  ALLOW.

Runtime ownership and cleanup proof on integrated head `a35a435`:

- One pre-created EUID-owned `0600` regular `.runtime.lock` serializes every
  cooperating product create/adopt/observe/remove through fresh independent
  opens and exact metadata/identity rechecks. It closes Darwin's mkdir-to-first-
  identity gap within the documented cooperative same-EUID boundary; a mutex
  remains serialization only. Failure cleanup never removes a replacement.
- One validating `RuntimeBinding` is the source of runtime root, fixed HOME/TMP,
  attempt-token and worker-config locators. The daemon-global API socket remains
  separate authority. A dedicated fixed empty read-only lifetime inode—not the
  runtime-directory descriptor—is duplicated across outer gate, registered
  worker and final provider as FD 10. FD 3/control and FD 9/runtime authority
  close before provider exec; the provider cannot use FD 10 for `openat`,
  `fchdir`, write or truncate.
- A real causal test SIGKILLs the outer runner while the same provider PID/PGID/
  birth remains alive. Independent recovery still observes the lifetime flock
  held and sees it available only after provider exit. Missing, malformed,
  replaced, linked, mode- or size-mutated lifetime authority fails closed.
- Runtime cleanup uses a fixed top-level grammar and runner-owned names. An
  unacknowledged terminal spool blocks every effect. HOME/TMP traversal accepts
  only bounded same-device/EUID regular single-link content; symlinks, FIFOs,
  sockets, hardlinks, unexpected entries, depth/effect exhaustion, cancellation
  and identity uncertainty never become release. Directory/root/parent fsync
  and a final absence recheck make successful retry durable.
- Three independent BLOCK/repair cycles found and closed the missing cooperative
  mutation lock, dropped final-provider lease, invalid terminal-scratch cleanup,
  adoption effects before lifetime acquisition, and failed-create deletion of
  a replacement lifetime. Adoption now acquires/rechecks an existing lifetime
  before any repair, and only the exact pre-provider pre-lifetime crash grammar
  may create one under the parent lock. Exact created lifetime identity is
  threaded through bounded cleanup; replacements remain visible.
- Mutations removing parent/lifetime flocks, lock metadata checks, terminal
  preflight, hardlink/symlink refusal, final absence recheck, provider FD 10,
  scratch exact cleanup, adoption-before-lock ordering and created-identity
  comparison were killed and removed. Exact repaired review head `e5785eb`
  received independent **ALLOW** with no findings.
- Author full, focused, race, CGO-free, vet, cross-build, mutation and clean-
  census gates passed. After unchanged integration the orchestrator passed
  daemon/runner/command focused tests, the full nine-package Go suite, targeted
  race (`daemon 2.367s`, `runner 32.128s`, command 1.677s), vet, format/diff,
  an empty task temporary root and an exact process census with no runner,
  attempt-worker or lifetime-provider residue.
- The remaining boundary is deliberate: a provider can voluntarily close its
  inherited read-only lease after simultaneous outer loss, and hostile same-
  EUID processes can ignore advisory locks. The documented product/cooperative
  threat model does not claim OS isolation. Final Change tree/CWD reinspection
  remains assigned to the registered Change worker and is not implied by this
  proof.

Final Change-directory authority and runner cwd proof on integrated head
`68ef763`:

- `Published.Reinspect` now invokes the one central Change scanner after the
  caller's chosen lifecycle cut and returns an opaque `VerifiedPublished`
  retaining that exact scanner-owned root descriptor and immutable tree facts.
  It exposes only checked facts, exact `F_DUPFD_CLOEXEC` duplication and
  explicit close. Copies share close state; independently duplicated descriptors
  own their own lifetime. No path, provider, runner, callback or second walker
  entered the Change package.
- Same-size content mutation, late nested `.git`/case variants, commitment/
  count/bytes/base/format/identity corruption, in-scan root replacement,
  cancellation and closed/corrupt capability attacks fail. Replacement of the
  published pathname after a successful scan cannot retarget duplication; the
  retained original inode remains authority. Exact Change head `9a9e068`
  received independent **ALLOW** with no findings.
- `WorkerControl.ExecProvider` now consumes one exact caller-owned cwd
  descriptor on every outcome. Runner validates strict directory metadata and
  equality to its prepared cwd commitment, rechecks immediately before
  `fchdir`, closes the descriptor plus FD 3/control and FD 9/runtime-directory,
  retains only the read-only FD 10 lifetime lease, and execs. It imports no
  Change policy and never reopens or stats the cwd pathname at final exec.
- `WorkerControl.DuplicateRuntimeDirectory` supplies the private worker exactly
  one caller-owned `F_DUPFD_CLOEXEC` duplicate of its already committed FD 9
  during the initial selection state. It validates the original before and
  after duplication and the duplicate itself; cancellation, repetition,
  post-stage use, replacement and corruption close without an FD effect. This
  eliminates raw-FD wrapper aliasing while retaining the runner's original.
  Exact head `5c7ba60` received independent **ALLOW** with no findings.
- Parent/name replacement after descriptor acquisition executes in the
  retained original inode. Unrelated directory, ordinary file, closed/reused
  descriptor, mode/final-seam mutation and error/unsupported ownership paths
  reject without provider effect or descriptor leak. Same PID/PGID/birth,
  one-input/EOF, sole-Wait, executable replacement, outer-runner-death lifetime
  and provider FD census proofs remain green. Exact runner head `76e4fe9`
  received independent **ALLOW** with no findings.
- Mutations disabling reconstructed facts equality, reopening Change by path,
  reopening cwd by path and omitting descriptor/commit equality were killed by
  the intended causal tests and removed. After unchanged integration the
  orchestrator passed the combined Change/runner/command focused gate, full
  nine-package Go suite and combined race gate (`change 28.174s`, `runner`
  40.678s, command 1.706s).
- Orchestrator tracing found and removed an accidental second deferred cwd
  close on the error path. The direct ownership repair clears the deferred
  owner before the one explicit close; reused-FD error chains and exact FD
  censuses prove no spurious already-closed result. Exact repair head `39ebe6a`
  received independent **ALLOW** before integration.
- Composition still must call `Reinspect` only after the provider release is
  authorized and immediately pass its duplicate to `ExecProvider`. The
  capability proves scan-time content, not hostile same-EUID immutability; no
  cooperative product mutator may exist in the scan-to-exec interval.

Bounded API dispatcher proof on integrated head `157af0c`:

- One concrete synchronous daemon switch maps exactly nine typed calls to the
  Store. Attempt calls use only the transport-derived digest and call atomic
  `ProposeAttemptOutcome` directly; fixed creation defaults and sanitized error
  codes prevent caller-selected provider/verification/failure authority.
- The public projection contains only bounded factory/project/agent/task
  summaries. Roots, bodies, results, tokens, provider/source/path/Change and
  private detail are not representable. Empty collections now clone
  independently and serialize as `[]`, including head-zero fresh state.
- Capacity-one run wakes occur only after durable proposal, are nonblocking,
  never closed during unregister and remain hints requiring Store reread.
  Credential/domain, concurrent-first-outcome, revision, privacy, framing,
  cancellation, wake-race and empty-array attacks held over a real isolated
  Unix listener. Exact head `cbb6f6b` received independent **ALLOW** with no
  findings; focused/race/CGO-free/vet/cross-build gates passed.
- A later package-parallel gate exposed a production path-walk bug rather than
  a reason to serialize clients: directory observations compared mutable child-
  churn metadata (link count, size and mtime) while other fixtures legitimately
  created entries under `/private/tmp`. Exactly five directory-object checks now
  reuse `sameDirectory` (device, inode, EUID and full mode); strict full identity
  remains on token and socket objects. The causal test changes an unrelated
  sibling between observations and fails under the old comparison. Exact repair
  `aad8058` received independent **ALLOW** with no findings, including causal
  mutation replay, race and replacement/symlink/owner/mode attacks. After
  integration at `3f36fe2`, API and daemon passed 20 concurrent-package
  iterations (`api 27.001s`, `daemon 4.195s`).

Registered Change-worker proof on integrated head `43a94ee`:

- The private checkpoint codec moved, rather than copied, from daemon into one
  concrete `internal/changeworker`. `factory-runner --change-worker-shell`
  performs one serial four-release sequence: select and report the exact Git
  base; prepare and report the Change staging identity; populate, publish and
  report; then re-inspect the stable published tree and pass its retained exact
  directory descriptor to the already prepared shell provider. No provider
  effect is possible before all four daemon releases.
- The worker derives HOME, TMP and token locations from the retained runtime
  descriptor. It validates config and every fixed child before the first Git
  effect and again immediately before provider exec, including exact root-
  device equality. Every capture failure closes the newly opened descriptor
  and all earlier authority descriptors before selection, staging, checkpoint
  or provider effects. It exposes no raw bearer, HOME, TMP or token location in
  worker configuration.
- The initial independent review at `4d00168` returned **BLOCK** after finding
  discarded HOME/TMP/token capture errors and missing root-device equality.
  Repair `3063fda` centralized checked child capture, added initial/final
  device binding and removed a redundant error wrapper. Mutations restoring a
  swallowed capture error or deleting directory-device equality were killed
  by the new causal tests and removed. Exact-head re-review returned **ALLOW**
  with no new finding.
- Real Git and shell tests prove the four releases, same PID/PGID/birth through
  final exec, Git descendant reaping, exact blob ordering, fixed HOME, final
  `.git` rejection, same-size tree mutation rejection, retained cwd authority,
  provider descriptor grammar, private-value filtering, and exact FD/goroutine
  census. Wrong-device, closed-parent and malformed config/HOME/TMP/token cases
  produce no selection, staging, provider or descriptor effect.
- After unchanged integration the orchestrator passed the full serial Go suite
  and the serial race gate across API, daemon, kernel, Change, Change worker,
  runner and `factory-runner` (`api 2.868s`, `daemon 3.513s`, `kernel
  248.826s`, `change 28.311s`, `changeworker 8.776s`, `runner 40.559s`,
  command 2.271s). Vet, format and diff checks passed; the isolated task root
  was empty and the exact process census found no runner, Change worker, Git
  batch or shell-provider residue.
- One initial package-parallel combined invocation failed the daemon dispatcher
  fixture at its fourth client call with fixed `ErrInvalidClient`; all kernel,
  Change, worker, runner and command packages in that invocation passed. The
  exact daemon test then passed 20/20 alone, the seven-package gate passed
  three times with package serialization, and the full serial and race gates
  passed. This failed invocation is not counted as green evidence; process-
  sensitive integration gates remain serial while the fast-gate concurrency
  policy is finalized.
- The remaining go/no-go work is composition, not another wrapper abstraction:
  a concrete daemon supervisor must bind these checkpoints to Store commits,
  release the provider once, observe the canonical `AttemptResult`, clean exact
  resources, and prove crash/restart convergence. The cooperative same-EUID boundary and
  scan-to-exec assumption remain explicit residual limits.

Historical synchronous shell-supervisor proof on old integrated head `4c2da24`
(not canonical or shipped provider integration):

- `Daemon.RunNext` is one concrete synchronous owner for the kernel vertical
  slice. It admits the canonical task, creates and binds the private runtime,
  starts the inert outer runner, durably binds its exact identity, receives and
  atomically binds the exact provider process/group identity, commits each
  Change checkpoint, marks the run running and only then releases the shell
  provider. No production goroutine, alternate provider boundary, retry
  framework, stage table or second state authority was added.
- Provider and outer-runner termination are separate durable external facts.
  One private-field `ProcessExit` representation backs explicitly named
  `ProviderExit` and `RunnerExit` fields. The daemon validates and commits the
  then-named provider terminal record before acknowledging and deleting its
  spool, then waits the outer child and commits that distinct exit. The current
  contract replaces that path with `ConsumeAttemptResult`. A causal provider
  exit-7/outer-exit-0 test kills the old conflation. Either exact recovered
  absence may arrive first after a crash; reused, malformed, permission-denied
  or otherwise uncertain identity never becomes absence.
- Releasing a nonempty provider process/group requires `ProviderExit`; releasing
  a nonempty outer-runner resource requires `RunnerExit`. Finalization derives
  the same requirements from the durable resource identities, including an
  admitted run that acquired processes before failing. Exit observations are
  identity- and timestamp-bound, exact replay is idempotent, conflicts fail,
  and the first typed outcome or exit-domain failure remains immutable.
- Provider process and group activation is one concrete Store transaction.
  `ActivateProviderResources` binds the same identity to exactly two declared
  rows and requires an affected-row count of two; single-resource activation
  rejects either provider kind. Trigger suppression of either row rolls back
  both, commit ambiguity leaves both declared or both active, and durable
  validation rejects one-sided or mismatched identities while allowing later
  sequential cleanup states.
- Commit/revocation reconciliation is bounded to three independent 250 ms Store
  attempts. Permanent unavailability returns typed outcome-unknown with no
  stale `Run`; the one-time bearer is discarded and any live owner is still
  synchronously joined. The durable nonterminal row remains discoverable by
  `RecoverableRuns` for the next recovery pass. Ownership-convergence loops
  that retain a live child or wait for exact runtime removal remain deliberately
  distinct from transaction replay and are retained for the final audit.
- Independent review repeatedly blocked green candidates. The Store review
  found unchecked admission/finalizing footprints, forged failure domains and
  unresolved admission ambiguity. Process review found release-after-cancel,
  a replay-insensitive witness, missing descendant proof and the inner/outer
  exit conflation. The slice elegance review found unbounded Store retry. The
  final Store review found independently committed provider process/group rows
  could create an unreleasable partial identity. Repairs received exact-head
  **ALLOW** from Store/authority, process-lifecycle and slice-elegance reviewers
  at `cf00fe0`; the whole-runtime elegance audit remains a later hard gate.
- Mutations removing admission or finalizing row-count guards, bounded typed-
  unknown handoff, pre-release cancellation, exact-one witness detection,
  provider/runner exit separation, exit timestamp checks, process-release exit
  guards and the atomic provider two-row guard were killed by their causal
  tests and removed. Real descendants, activation acknowledgement loss,
  partial provider release, unavailable Store, completion/exit orderings and
  provider-7/outer-0 all leave an exact process/authority census.
- After unchanged integration with the API directory-identity repair, the full
  serial Go suite passed (`factory-runner 0.139s`, `factoryctl 0.214s`, `api
  1.610s`, `change 12.916s`, `changeworker 1.250s`, `daemon 3.541s`, `kernel
  9.690s`, `processcontract 0.177s`, `runner 5.904s`, `sqlitecontract
  10.390s`). The full serial race suite passed (`factory-runner 2.289s`,
  `factoryctl 1.394s`, `api 2.876s`, `change 28.219s`, `changeworker 8.812s`,
  `daemon 26.095s`, `kernel 257.457s`, `processcontract 6.468s`, `runner
  40.801s`, `sqlitecontract 13.803s`). API and daemon then passed 20 package-
  parallel iterations (`26.495s`, `68.656s`). Vet, format and diff checks were
  clean.
- Two setup-only attempts are not counted as evidence: a deliberately fresh
  empty module cache could not reach the network sandbox, and an unprivileged
  run was denied isolated Unix-socket binds before its assertions. Reusing the
  established isolated module cache and permitting only the temporary socket
  fixtures produced the green gates above. The failed socket attempt left six
  exact private fixture roots; after confirming their ownership, contents and
  absence of live processes, the orchestrator unlinked only those socket/token
  artifacts and removed the now-empty roots.
- This proves the normal integrated shell lifecycle but does **not** pass the
  kernel go/no-go gate. A concrete recovery sweep and real subprocess daemon
  crash matrix must still prove every pre/post-release, `AttemptResult`, Store-
  observation, cleanup and terminal-commit cut without replay, invented
  release or recovered numeric signal authority.

Loopback browser state transport and TypeScript Session proof on integrated
head `b61fca8`:

- `internal/browser` is one state-only loopback adapter with an exact IPv4
  bind, path, Host including port and finite Origin allowlist. It disables
  compression and wildcard CORS, bounds connections, frame bytes, request
  lifetime, snapshot traversal and subscription queues, owns one reader per
  connection, and synchronously joins connection and subscription cleanup.
  HELLO, PAIR and AUTH are exact transcript operations. Only durable
  `observe` and private-detail capabilities are advertised; terminal and
  HumanRequest effects remain unimplemented rather than accepted by a stub.
- The concrete daemon adapter maps the kernel's fixed-head public pages,
  exact entity/tombstone reads, private HumanRequest detail and typed watch
  restarts directly into that transport. It adds no service, repository,
  cache or second event ring. Each browser operation reloads the exact durable
  client under one bounded per-client gate. One owned polling goroutine exists
  per subscription; cancellation, terminal restart and daemon shutdown expose
  an observable join.
- One daemon-owned `RevokeBrowserClient` operation shares that gate across all
  browser runtimes. Exact-revision revocation, its private security event and
  terminal-lease clearing commit before every matching socket is closed and
  joined. Cleanup uncertainty is returned distinctly while the durable revoke
  remains committed. Listener registration is fenced so a concurrent revoke
  or daemon close cannot miss a newly accepted runtime.
- Canonical chronology is explicit. A requested subscription cursor is
  untrusted: only its first exact `gap` restart may return a lower canonical
  Store head. Once server chronology is accepted, regression poisons and joins
  the connection. The one valid empty chronology is exactly `head=0,floor=1`;
  Go and TypeScript reject every other floor-above-head shape. Final
  HumanRequest pages may contain exactly eight items with no continuation;
  nine items split as eight plus a cursor and one final item.
- `@dark-factory/client` now owns a dependency-free browser `Session` and
  reconnecting `BrowserClient`. WebCrypto P-256 proofs, non-exportable key
  persistence, pairing uncertainty, fixed-head snapshot correlation, state
  subscription/entity refresh, stale-generation fencing, guarded consumer
  callbacks and bounded exponential reconnect are framework neutral. Pairing
  is never replayed while result persistence is uncertain. React, xterm.js,
  CSS, routing and site assumptions remain absent.
- Exact-head reviews repeatedly blocked green candidates. The integrated Go
  review found the exact-eight protocol mismatch, regressed backend restarts
  and direct HumanRequest lease-validation bypass. Cross-language re-review
  then found the TypeScript decoder retained the obsolete page rule. Three
  TypeScript review rounds found post-pair reconnect suppression, callback-
  exception reader loss, zero-delay retry churn, stale timer ownership,
  async-persistence resurrection, reentrant close before promise ownership,
  duplicate one-shot PAIR proof and timer-handle aliasing. Adapter review found
  durable revocation was not coupled to socket close/join and that an honest
  future cursor could not be represented. All findings became causal tests;
  final reviewers returned **ALLOW** on TypeScript head `a0b7fae`, shared Go
  repair `06ad16a`, and adapter repair `dd4073b` before unchanged integration.
- Mutations killed include cached post-revocation state authority, accepting a
  regressed established restart, omitting the daemon client gate, restoring
  the obsolete exact-eight predicate, removing direct lease validation,
  dropping session generation/auth phase guards and allowing user callbacks to
  escape cleanup. Mutation code was removed.
- Author/reviewer gates passed browser/daemon/protocol/kernel count-three and
  full race matrices, vet, format/diff, exact loopback Store/WebSocket tests,
  clean connection/process censuses, TypeScript typecheck, packed clean-
  consumer import and all 44 client tests. After integration the orchestrator
  passed the four affected Go packages (`daemon 16.525s`, `browser 21.567s`,
  `browserprotocol 0.793s`, `kernel 15.025s`), daemon/browser race (`51.419s`,
  `24.750s`), vet, all 44 web tests, typecheck, formatting and diff checks.
- Two independently reviewed additions now sit above that state-only adapter.
  The transport mints a fresh private 16-byte identity for every successfully
  authenticated WebSocket and clears it on unregister or client close. A
  backend cannot select it, reconnect cannot reuse it, JSON excludes it, and
  one `fmt.Formatter` redacts every diagnostic verb. The formatter test first
  reproduced the prior `%d` byte-array leak and then killed that mutation.
  Exact-head review returned **ALLOW** at `aca9df5`; canonical count-three
  browser tests passed in `64.433s`, browser race in `24.676s`, with protocol
  tests and vet green.
- The public MIT web slice adds one controlled `@dark-factory/ui`
  `FactoryConsole`, an explicit stylesheet export and a static contributor
  fixture. It renders only bounded canonical state and finite errors: BUILDING,
  projects, agents, tasks and read-only NEEDS YOU. React text escaping,
  prototype-key error fallbacks, canonical fixture joins, semantic keyboard
  structure and omission of private question/provider data are causal. Review
  blocked the first head for an unapproved esbuild install, bad fixture join,
  prototype lookup and an unnecessary ReactDOM peer; canonical integration
  then exposed an ambient-`pnpm` gate assumption. All were repaired and
  independently re-reviewed **ALLOW** through `b59b2f8`. On canonical head,
  offline frozen install, typecheck, all 54 tests, packed-consumer import,
  Vite production build and the stripped-path gate self-test passed.
- This state-transport proof deliberately excludes terminal and HumanRequest
  effect frames. Exact daemon PTY input/resize/lease and HumanRequest reply
  effects are proved separately at `ae28dc8`; the binary browser wire, concrete
  cancel result, operator web bootstrap integration, interactive terminal/reply/cancel
  UI and private site host remain outside this checkpoint. They must reuse the
  durable authority and live-owner seams rather than widen this state-only
  browser capability set implicitly.

Shared browser terminal contract proof on integrated head `a10b9f0`:

- Browser v1 now has one exact manifest row, direction, envelope-ID policy,
  fixture and closed Go/TypeScript codec case for terminal attach, output ACK,
  lease acquire/renew/release, resize, detach, input result, EOF/exit/reset,
  HumanRequest reply and the single `cancel_run` descriptor. Ordinary responses
  echo one request ID; attachment-lifetime events reuse only the attach ID;
  `TERMINAL_ACK` alone forbids an ID.
- The binary contract remains two opcodes and no base64. Input and output are
  capped at 8 KiB. Output accepts an exclusive endpoint of `MaxUint64` and
  rejects `2^64`. JSON decimal control values remain at most SQLite `MaxInt64`;
  terminal dimensions are exactly 1 through 4,096. The manifest fixes a 64 KiB
  unacknowledged window and ten-second ACK timeout for the later transport.
- Cross-language validation is deliberately strict. Nonzero lower-case IDs,
  revision/generation/status unions, conditional lease expiry, exact action,
  bounded UTF-8 reply text, missing-versus-null scalars and all terminal bounds
  agree. Escaped lone UTF-16 surrogates are rejected before Go's JSON decoder
  can replace them; valid pairs and literal U+FFFD remain valid.
- Input-result status/count relations match the runner: accepted or partial
  carries 1 through 8,192 bytes, while rejected or uncertain carries zero.
  The later Session/real-server layer correlates a partial count with the exact
  original binary payload; the codec does not invent that missing context.
- Plan review blocked three revisions until the exclusive endpoint, ACK/credit
  chronology and complete message registry were explicit. Code review then
  blocked green candidates for an incomplete exported TypeScript manifest,
  Go-only surrogate/null acceptance and impossible input-result combinations.
  Each repair received exact-head **ALLOW** at `dde8807` before integration.
- Mutations removing manifest rows/bounds, role or ID rules, surrogate/null
  guards and the input-result predicates were killed in both languages and
  restored. Fixtures are an exact allowlist and every valid fixture round-trips
  in its sender role while failing the opposite role.
- On canonical head, browserprotocol passed count-three (`0.281s`), race
  (`1.304s`) and vet. Offline frozen Corepack install, TypeScript typecheck,
  packed-consumer checks and all 56 web tests passed; formatting, diff and
  status were clean.
- This freezes the shared wire only. `internal/browser` still rejects terminal
  binary traffic and `BrowserSession` still rejects non-string frames. Direct
  daemon forwarding, attachment/credit ownership, TypeScript Session effects,
  real-server proof and UI consumption remain the next causal slices.

Factoryctl browser bootstrap and shutdown proof on integrated head `2f883c1`:

- The exact recovery/bootstrap surface is `factoryctl web status`, `web open`,
  `web list-clients` and `web revoke`. The owner-only Unix API returns bounded
  typed launch state. A ready launch opens one fixed hosted URL; an ambiguous
  challenge commit is reconciled by exact digest and abandoned without opening
  or retrying. URLs carry no query, userinfo or permanent secret, and the
  one-time challenge remains only in the fragment.
- One daemon-owned `BrowserRuntime` owns the listener, connections, Store watch
  subscriptions and exact-boot unredeemed challenges. Open and close share one
  lifecycle gate. Closing rejects new opens before minting, retains registry
  ownership through joined server/backend cleanup, and removes the runtime only
  after cleanup succeeds. Unresolved cleanup stays registered and every later
  direct or daemon close returns the same once-owned error.
- The Store now uses one capacity-one, context-aware writer gate for every write
  transaction. Waiting for serialized writer admission observes cancellation
  before touching SQLite; commit, rollback, discard and error paths release
  exactly once. `Store.Close` marks closed, joins an admitted writer, rejects
  queued/new writers and closes both pools once. This makes the browser cleanup
  deadline real without adding a special transaction path.
- Independent review blocked four successive heads for opener capacity leaks,
  URL/digest mismatch cleanup, launch commit ambiguity, direct-close versus
  daemon-close registry races, an unbounded pre-SQL mutex wait, and forgotten
  cleanup errors. Each failure became a causal test. Exact head `3ca7313`
  received **ALLOW** after the reviewer reproduced the former writer-wait and
  repeat-close attacks and confirmed they now fail closed.
- Mutations removing closing-state admission, early registry removal, the
  context-aware writer select and failed-runtime retention were killed and
  removed. The accepted candidate passed focused tests at count twenty, the
  five-package normal gate, vet and the race gate (`kernel 343.283s`, `browser
  24.730s`, `daemon 53.514s`, `api 3.682s`, `factoryctl 2.619s`). After
  integration the orchestrator reran kernel, browser, daemon, API and
  `factoryctl` successfully (`15.241s`, `22.059s`, `20.714s`, `2.574s`,
  `0.758s`), with vet and diff checks green.
- This completes browser bootstrap and client recovery, not the platform
  cutover. Isolated launchd install/start/stop/uninstall, doctor and the final
  service/recovery E2E remain required; no operator home, live socket or
  installed service was touched.

Framework-neutral TypeScript terminal Session proof on integrated head
`b2eef51`:

- `@dark-factory/client` owns one `TerminalHandle` for attach, retained replay,
  live binary output, acknowledgement, exact input chronology, resize, lease
  operations, detach, exit and reset. It is framework-neutral and exposes no
  React, xterm.js, visual state, raw WebSocket authority or public request
  destination getter.
- One serialized operation gate prevents attach, input, resize, lease and
  detach reordering. Correlated immutable snapshots, sequence and generation
  floors, bounded retired-response handling and exact partial/uncertain input
  semantics fence stale or ambiguous authority. Callback failure cannot ACK
  unseen output, and close settles every pending operation without leaking an
  asynchronous rejection.
- HumanRequest detail, reply and `cancel_run` are bounded correlated operations.
  `BrowserSession` now exposes only high-level detail-bound methods: it deeply
  freezes Store-produced detail/cancel values, never accepts a run destination,
  and fences every pending operation on exact envelope/subject/revision,
  capacity, close and reconnect. The raw HumanRequest helper was deleted.
- Independent review repeatedly blocked apparently green candidates for
  chronology rewinds, reentrant output/ACK handling, ambiguous sends, lease
  mutation races, detach ordering, unbounded pending work and raw authority
  widening. Exact candidate `35c0c8641ce1e4e41328a83276d70a3e1720858c`
  received **ALLOW** only after those attacks became causal tests.
- On the canonical integrated branch, frozen offline install, typecheck, all
  84 client/UI tests and packed clean-consumer import passed under strict
  unhandled-rejection handling as part of `scripts/go-check.sh`.
- Lease renewal remains intentionally manual in this checkpoint. The next
  reviewed contract slice moves its fixed bounded policy into the high-level
  client before the public UI consumes terminal input.

Go browser terminal and HumanRequest effect transport proof on integrated head
`7f449ce`:

- The loopback connection routes exact terminal attach, binary input/output,
  acknowledgement, lease acquire/renew/release, resize, detach, EOF, exit and
  reset through the already-proved daemon live owner. It also routes exact
  HumanRequest reply and the single typed `cancel_run` transition; browser
  output or terminal prose never becomes lifecycle authority.
- Each connection owns at most one attachment and one bounded credit state.
  Output cannot exceed the 64 KiB unacknowledged window, a lost ACK closes the
  observer without blocking the provider, reconnect resumes only from a known
  retained sequence, and stale replay yields an explicit reset. Many observers
  remain possible while the durable lease permits only one writer.
- Cleanup is fail-closed. The connection clears an attachment only after
  `Close` succeeds; failed detach retains the exact attachment, refuses
  reattach and retries the same owner during shutdown. Invalid backend
  attachments are synchronously closed, and any close uncertainty reaches
  `Server.Close` and the daemon's stable `BrowserRuntime` cleanup result.
- Independent review blocked the first final head because explicit detach
  dropped ownership before a failing close and an invalid nil-events backend
  attachment was not closed. The repair at candidate
  `d8b97c074411f21ca805f34d54547f9369001f22` killed both mutations and received
  exact-head **ALLOW**. The reviewer also attacked stale attach, finalization,
  replay/credit, forged effects, wrong binary identity, duplicate close and
  terminal-exit races.
- Candidate normal browser/daemon/kernel tests, browser/daemon race tests, vet,
  Linux compile-only checks and diff checks passed. After integration with the
  newer Store writer gate, runtime lifecycle and TypeScript work, the
  orchestrator ran `scripts/go-check.sh` successfully, all 84 web tests passed,
  and browser/daemon/kernel/API/factoryctl passed (`21.692s`, `22.668s`,
  `14.921s`, `1.660s`, `1.239s`). The canonical browser and daemon race suites
  passed (`24.614s`, `71.359s`).
- This proves the transport boundary, not product discoverability. The UI must
  not invent run/session coordinates or cancel preconditions. Candidate
  `219d036` adds the exact `TERMINAL_TARGET_GET`/`TERMINAL_TARGET` route: an
  observe-authorized request pins one public agent revision and state head,
  returns only the exact run/session and revisions from Store, serializes
  against client revocation, and leaves attach to its independent live-owner
  and revision checks. Production pairing now grants exactly
  `observe | private_human_request_detail | human_actions | terminal_input`,
  with no unknown capability bit. Target-specific revocation linearization,
  backend cancellation, codec, null-result, stale-observation and forged
  correlation tests are causal and pass at count twenty.
- HumanRequest private-detail authority, canonical active-graph validation,
  exact current-binding cancel fencing and the high-level TypeScript methods
  are complete through `bd674b0`. The high-level TypeScript terminal client
  now owns correlated target discovery, opaque target authority, serialized
  attach/input/resize/release/detach effects, and automatic lease renewal with
  expiry fencing. Interactive public UI terminal/request/cancel work, recovery
  replay, private host integration and the final cutover gates remain
  incomplete.

Real Go/TypeScript browser PTY lifecycle candidate on 2026-08-27:

- `scripts/go-browser-e2e.sh` builds the production `factory-runner`,
  `factoryctl` and framework-neutral TypeScript client, then runs one Darwin
  package serially with a two-minute outer bound. Its explicit `--race` mode
  builds both Go binaries with race instrumentation and runs the same real
  lifecycle under `go test -race`; the authoritative Go gate invokes both
  modes. The Node harness uses only the built client. Its sole test dependency
  is `ws@8.18.3`, needed to set the exact production Origin header; no browser
  protocol or lifecycle behavior is reimplemented in the harness.
- Each subtest creates a private temporary Git repository, Go home, Store,
  owner-only API socket, runtime parent and loopback browser listener. It uses
  real `Daemon.RunNext`, one-time pairing, Host/Origin validation, the real
  runner-owned PTY/process group and exact Store/browser authority. It never
  reads an operator home, live socket, installed service, provider credential
  or real Claude/Codex session.
- Scenario A proves attach and binary output, one leased input and exact
  response, an exact resize whose rows/columns are observed by `stty` inside
  the provider PTY, deliberate browser disconnect, exactly one authenticated
  reconnect, retained contiguous output without input replay, one explicit
  bounded HumanRequest, exact inline reply, typed success and terminal exit.
  The second WebSocket remains the same usable authenticated session through
  any legitimate state restart/late retired response ordering and a post-exit
  target request. The wire exit must be one canonical normal or signaled arm
  and must match the durable provider exit exactly; cleanup TERM after an
  already committed success does not rewrite the durable succeeded outcome.
- Scenario B proves one daemon-minted HumanRequest cancellation, immediate
  durable finalization/input revocation, refusal of stale terminal input, no
  forbidden provider marker, a canonical signaled exit, exact provider/group
  reap and a same-WebSocket post-exit target request. The request resolves once
  and the run cannot be retargeted.
- Both scenarios require terminal outcome/proposal agreement, revoked attempt
  credential, closed terminal session without a lease, one resolved
  HumanRequest, all four resources released, and all three recorded process
  identities positively absent or reused. Cleanup joins the Node child,
  browser runtime, daemon owner, API listener, runtime parent and Store;
  rebinds the exact browser address; removes the private root; and returns to
  the starting FD and goroutine census. Ordinary and separately registered
  fallback cleanups both retain the actual `exec.Cmd` Node owner and
  daemon/controller capabilities; fallback does not share the ordinary
  `sync.Once`, and Store/root closure cannot precede a failed daemon-owner
  retry. Persisted process identities are used only for final observation and
  never authorize a signal. This in-process fallback covers failed and panicked
  Go tests; it does not claim to survive SIGKILL or a forced test-process exit.
- After the cleanup/resize repair, the candidate passed one serial run
  (`3.441s`), three serial repeats (`7.787s`) and the explicit serial race run
  (`9.439s`) through the real built runner. A separate verbose run returned to
  exactly five file descriptors and three goroutines after both the normal and
  fallback cleanup callbacks in each scenario. Earlier candidate failures
  exposed and retained causal coverage for contradictory signaled exits, late
  entity replies during state restart, the Darwin natural-exit/TERM observation
  race, unread terminal readiness, resize starvation and SQLite subscription
  interruption during exact owner cancellation. Each production repair was
  independently reviewed before entering this combined branch.
- This is an in-process composition-root proof because the repository still
  has no production `cmd/factoryd` binary. A clean black-box installed-daemon
  test, daemon-restart terminal reset/recovery, private-host UI integration and
  crash-cut matrix remain separate cutover gates. This candidate must receive
  an independent exact-head review before canonical integration.
- The composition test intentionally finishes well before the ten-second input
  lease renewal interval. Renewal scheduling, correlation, expiry fencing and
  retry prohibition remain covered by the deterministic TypeScript lifecycle
  tests; this E2E does not claim a real-time renewal composition proof.

Public UI lifecycle and HumanRequest candidate on 2026-08-27:

- The MIT `@dark-factory/ui` package now owns one `FactoryApp` effect and one
  DOM-free concrete controller. Pairing is consumed and scrubbed before client
  construction; the exact loopback URL supplies Host while the actual page
  supplies Origin. FactoryApp/controller own connect/close lifecycle and
  canonical state; BrowserClient alone owns automatic reconnect, and the UI
  exposes no pairing-proof replay or manual retry action. The controller also
  owns finite errors and generation fencing. Server rendering imports and renders
  without reading `window`, history, IndexedDB or WebSocket.
- `FactoryConsole` remains controlled and presentational. It may select at most
  one exact public HumanRequest revision, while the controller alone retains
  the returned private detail and daemon-minted `cancel_run` descriptor. View
  does not resolve; reply and cancel each consume that exact authority once.
  Reconnect, deletion, revision change and unmount clear or fence private state.
  React never constructs a request destination, revision or action argument.
- DOM-free lifecycle and authority tests, hostile-text/accessibility rendering,
  a mounted StrictMode setup/cleanup/setup proof, SSR, packed-consumer import,
  UI/dev typecheck and the production Vite build are the causal gate for this
  checkpoint. The endpoint has no public configuration surface, and reply
  drafts are retained only within the exact daemon-minted UTF-8 byte bound.
  Interactive xterm lifecycle remains the next separate reviewed UI commit; no
  terminal abstraction enters this slice.

Factoryd activation: boot recovery sweep, scheduler, and the black-box
daemon lifecycle on branch s1-finish:

- factoryd now runs the finite recovery sweep after the daemon opens and
  before any listener exists, so no client ever observes pre-sweep durable
  state; each disposition is reported on stderr and unresolved residue never
  refuses boot. The reviewed dormant `RunScheduler` starts after the
  listeners with the boot-proven supervisor specification and is joined
  before the daemon closes, because it owns synchronous RunNext children
  holding daemon resources. Causal tests pin the sweep-before-socket
  ordering with seeded residue, the scheduler driving a queued task through
  a real attempt to a terminal task record, the join-before-daemon-close
  ordering, SIGTERM convergence and double-boot refusal.
- Settlement is composed from the reviewed edges at the scheduler
  completion seam and the sweep's converging arms. A finalizing run with a
  fully released footprint settles to its terminal record: abandoned when
  the candidate change was never published, retained when the published
  tree re-reads and verifies against the durable selection (finalName is
  the change ID; format, base and stage identity come from the stored
  selection and tree identity — no new durable state). A refusal is
  surfaced, never fatal and never silent: the sweep's
  `result-consumed-unsettled` disposition and the SupervisorSpec's
  UnsettledCompletion reporter carry it while the scheduler keeps serving.
  The proven limit: the runner is the sole exit-observation authority for
  its provider, so a runner death mid-attempt leaves releasing residue no
  edge can honestly settle — the run stays deliberately nonterminal (live
  cell D), consuming a capacity slot and blocking its agent until an
  operator resolution surface exists; at the default capacity of one this
  idles the factory rather than inventing an outcome. Sweep tests migrated
  to the settled terminal endpoint, and direct unit tests pin the
  abandoned, retained-refusal, orchestrator, replay and surfaced-unsettled
  arms.
- `scripts/go-daemon-e2e.sh` is the installed-shape black-box proof, wired
  into `go-ci-owned.sh`: freshly built factoryd/factoryctl/factory-runner
  siblings, a factoryctl-initialized temporary home, operator subcommands
  over the real socket, one shell task to a succeeded terminal record with
  a published change, a pre-admission SIGKILL whose queued task survives
  and runs on reboot, a mid-attempt SIGKILL landed only while a sentinel
  proves the provider live post-publish whose orphan publishes a result
  the next boot consumes and settles — retained published change, terminal
  failed task — before the socket opens, a post-recovery task that
  succeeds, a live runner SIGKILL the daemon survives with the wedge
  surfaced and capacity honestly accounted, double-boot refusal against
  the live home, and a SIGTERM teardown census: socket and lock released,
  exactly the wedged run's runtime child retained, and a process census
  proving nothing carrying the fixture root survives. The provider task
  must declare its own outcome exactly as a real provider session does; a
  provider that exits silently fails honestly with "provider exited
  before an attempt outcome".
- Gates on the branch: gofmt/vet clean, `go build ./...`, the full serial
  module suite green twice consecutively, `go test -race` for
  internal/daemon (167s) and cmd/factoryd (26s), the browser PTY E2E and
  the new daemon E2E green in repeated runs, `git diff --check` clean.

Managed launchd service installation on 2026-08-29:

- The paused managed installation is live for the Go daemon. One
  `internal/install` service engine owns install/start/stop/uninstall and
  the receipt-proven status projection; `factoryctl service` verbs are thin
  wrappers with an explicit `--label`/`--plist-dir` isolation surface whose
  production defaults are unchanged. Install copies the invoking
  factoryctl's own resolved sibling binaries into the sibling
  `<home>.service` directory — the home census contract is untouched — and
  a canonical durable receipt makes installed/running provable states:
  status reports them only when receipt, plist bytes, and the installed
  program digest agree exactly, and every other present fact stays
  ambiguous. Foreign bytes are never deleted; crash residue resolves only
  through uninstall, which removes exactly this installation's property.
- Reality corrected three reviewed launchctl assumptions that fakes had
  never exercised: a not-found print carries a fixed two-line stderr naming
  the queried label and uid (now accepted in exactly that shape), real
  print output holds blank lines and parenthesised field names (now
  parsed), and jobs pass through transient spawn states whose print the
  strict parser refuses (each observation stays exact; bootstrap
  confirmation waits out the transient within one bound). The service
  status census also no longer demands byte-stability of the SQLite
  members a live daemon legitimately rewrites; identity, bounds, member
  census, and every durable member remain exact. Service-root and live
  `runtimes`/`changes` directory identities intentionally exclude ctime, size,
  and link count because legitimate entry churn updates them; a transient
  rename-away/decoy/rename-back after the recheck remains a residual blind
  window, while in-window identity and binding checks still fail closed.
- Review hardened the mutation ordering: uninstall is evidence-first — no
  mutating launchctl verb until a matching receipt or an exactly rendered
  plist proves the label maps to the named home (a wrong --home against
  the default label previously reached bootout before refusing, which
  could have unloaded the live daemon). Uninstall now also resolves the
  engine's own staged-write crash residue by exact stage name, staged
  writers refuse a pre-existing stage instead of silently deleting it,
  and the bootout absence probe applies the same stderr strictness as
  status observation. Install bootstraps last (binaries, plist, receipt,
  then bootstrap), so a loaded job can never be receipt-less crash
  residue of this engine.
- `scripts/go-service-e2e.sh` is in the authoritative gate: real binaries,
  real launchd, one disposable unique label in the user gui domain, all
  files under a temporary root, guaranteed bootout on exit. It proves
  install → running, a real task through the managed daemon, stop → the
  socket dies, start → a second task, uninstall → launchctl 113, no
  artifacts, and an empty process census for the home. The operator's
  ~/.dark-factory and the production label are never touched; migrating
  the live install remains an owner-only action.
