# Browser boundary simplification handover

Status: planning and audit handover. This document does not authorize a
deployment, release, live-daemon operation, or use of the operator's installed
Factory.

## Purpose

This is the execution handover for simplifying the browser protocol, browser
authority, TypeScript client, UI boundary, and the consuming private site. It
records the smallest product contract, the intended deletion order, the
security and lifecycle properties that must survive, and the stop conditions
for each writing tranche.

The original audit was read-only and inspected Dark Factory through
`f1f3f236ff16f75762110194c278d5fe805ab02a`. This handover was materialized on
4 September 2026 from:

- Dark Factory `origin/main`:
  `1eab82250652de0f35c5051d6bbccab1447b221c`.
- Private site locally available `origin/main`:
  `0fdc05b16eac245c80b67d23b4f7331ce4e64458`.
- PR #401 exact head:
  `bdede6dd734e06d0fca88df015e867c56d2681bd`.
- PR #401 squash merge:
  `f1f3f236ff16f75762110194c278d5fe805ab02a`.

The private onboarding, provenance, artifact refresh, offline guidance, and
direct terminal work that blocked the audit is represented on site main by
PRs #11, #13, #15, #17, and #20. A writing agent must nevertheless obtain
fresh exact heads before starting; these values are provenance, not a floating
base instruction.

## Current-head addendum

The central state conclusion remains current at `1eab8225`: the repository
still contains eight-item pages, cursors, `STATE_ENTITY_GET`, per-entity
reconciliation, hidden advances, restart floors and reasons, and the mirrored
TypeScript accumulator.

Several post-audit changes matter:

- Browser task creation now has the live `TASK_ENQUEUE` and
  `TASK_ENQUEUE_RESULT` contract. State simplification must preserve that
  operation, its capability/authority checks, its agent-revision precondition,
  and the resulting snapshot invalidation.
- PRs #466 and #475 simplified terminal interaction and made input immediate.
  PR #504 keeps the terminal sidebar visible through finalization. The terminal
  authority tranche therefore requires a fresh audit against its writing base;
  do not apply the August terminal file list mechanically.
- PR #477 changed exact-full final state-page behavior. Its old local worktree
  is behind current main and must not be reused as a writing base.
- The private site now consumes the direct browser terminal and newer package
  artifacts. Every protocol/client/UI change still needs a separately reviewed
  artifact and provenance refresh after the runtime change lands.

## Executive decision

The first and largest behavior-preserving simplification is:

> Replace browser state pagination and per-entity replication with one bounded,
> coherent current snapshot plus a head-only invalidation.

The current site consumes current state. It does not expose pagination, cursors,
entity events, restart reasons, tombstones, or reconciliation state.

Available realistic evidence is small:

- The audited real site loop created one project, one agent, one task, and zero
  or one actionable HumanRequest.
- The audited UI fixture contained two projects, three agents, four tasks, and
  one HumanRequest.
- Greater-than-eight examples were synthetic pagination tests.
- No production telemetry was available, and the live operator home was not
  inspected.

Eight-item pagination is therefore not a product requirement. It is a response
to the existing 64 KiB control-frame bound. The replacement must solve size
explicitly and must never silently truncate state.

## Smallest product contract

The boundary must support:

- connection and pairing;
- current coherent Factory state;
- notification that current state changed;
- browser task enqueue;
- interactive terminal observation and input;
- HumanRequest list, detail, reply, and cancel;
- durable client revocation;
- reconnect with a fresh authenticated socket generation.

It does not need to preserve paging, entity replication, cursor chronology,
durable unredeemed challenges, terminal lease renewal, or every intermediate
state transition merely because v1 contains them.

## Current architecture

```text
factoryctl / local client
  -> Unix API DTOs
  -> daemon
  -> kernel Store / SQLite

dark-factory-site
  -> vendored @dark-factory/ui
  -> FactoryAppController / TerminalController
  -> @dark-factory/client BrowserClient / BrowserSession
  -> StateAccumulator / TerminalSession
  -> loopback WebSocket
  -> internal/browser Server / connection
  -> Backend / TerminalBackend
  -> daemon browser backend
  -> kernel Store and liveAttempt / runner / PTY
```

Current public state passes through roughly seven representations:

```text
kernel public projection
  -> API DTO or kernel page/entity union
  -> daemon projector
  -> browser wrapper
  -> browserprotocol union
  -> TypeScript wire/reconciliation model
  -> UI view and selection model
```

Only two privacy boundaries are necessary:

1. Durable kernel entity to a positive-allowlist public projection.
2. Public HumanRequest card to capability-gated private detail and actions.

The intermediate copies do not create additional privacy protection.

## Current source and test size

Exact source counts at Dark Factory `1eab8225`, excluding package metadata and
documentation:

| Area | Production lines | Test lines |
| --- | ---: | ---: |
| `internal/browser` | 1,951 | 3,340 |
| `internal/browserprotocol` | 2,750 | 1,938 |
| `internal/daemon/browser*.go` | 1,784 | 1,968 |
| Kernel browser authority | 948 | 1,776 |
| Kernel public state | 606 | 803 |
| Kernel HumanRequest | 874 | 1,121 |
| Kernel terminal session and target | 323 | 540 |
| TypeScript client | 2,686 | 3,896 |
| UI | 2,937 | 3,300 |
| Browser v1 manifest and fixtures | 82 | 64 |

`internal/api/types.go` is 283 lines, but it is a mixed DTO file and is not
charged wholesale to the browser boundary. Shared schema, validation, local
API, live-attempt, and runner files likewise contain relevant sections without
being wholly browser-owned.

These counts are baselines, not deletion targets. Replacement causal tests are
required before obsolete tests are removed.

## Direct product answers

1. The smallest adequate state model is one global coherent snapshot containing
   Factory, projects, agents, tasks, and unresolved HumanRequest cards.
2. Available realistic evidence is 1/1/1/0-1 in the real loop and 2/3/4/1 in
   the UI fixture. Eight-item pagination is not justified by that evidence.
3. The site needs coherent current state, not per-entity revision replication.
   Keep only revisions used as action preconditions.
4. Durable browser authority is required for paired-client keys and revocation,
   run/session lifecycle, HumanRequest delivery and resolution, and Factory
   work state.
5. Pending challenges, authenticated connection identity, state watching, and
   terminal writer ownership may be scoped to one daemon process or connection.
6. Terminal input, an ambiguously delivered HumanRequest reply, and an
   ambiguously delivered pair proof must never be replayed automatically.
7. Go and TypeScript manually duplicate item shapes, enums, bounds, IDs,
   chronology conversion, validators, page/entity unions, and reconciliation.
8. One public semantic projection can serve every retained public transport.
   If the Unix dashboard snapshot still has no consumer beyond Factory revision,
   deleting it and using a narrow Factory read is smaller.
9. Browser security events, permanent key fingerprints, `StateView.sequence`,
   restart reason taxonomy, and `ResetTerminalLeases` have no product consumer.
   Some remain internally reachable only through machinery proposed for
   deletion.
10. Snapshot plus head invalidation gives the largest deletion without changing
    site behavior.

## Recommended tranche order

Expected deletions are estimates from the original audit and must be refreshed
against each exact writing head. They are incremental only when the order below
is followed.

| Order | Tranche | Production deletion | Test deletion |
| ---: | --- | ---: | ---: |
| 1 | Bounded snapshot plus head invalidation | 2,100-2,600 | 2,000-2,800 |
| 2 | Collapse the public-state model | 300-500 | 450-800 |
| 3 | Process-memory pairing challenges | 500-700 | 900-1,300 |
| 4 | Connection-scoped terminal writer | Re-audit required | Re-audit required |
| 5 | Concrete composition and wrapper cleanup | 70-110 | 250-400 |

Do not create a standalone interface-to-callback conversion before the
protocol and authority methods have been deleted. It would produce a handler
table of similar size.

## Tranche 1: bounded snapshot and invalidation

### Primary files

- `internal/kernel/public_state.go`
- `internal/kernel/read.go`
- `internal/kernel/types.go`
- `internal/kernel/errors.go`
- `internal/browserprotocol/state.go`
- `internal/browserprotocol/wire.go`
- `internal/browser/backend.go`
- `internal/browser/cursor.go`
- `internal/browser/connection.go`
- `internal/browser/server.go`
- `internal/daemon/browser_backend.go`
- `internal/daemon/browser_subscription.go`
- `web/packages/client/src/control.ts`
- `web/packages/client/src/manifest.ts`
- `web/packages/client/src/state.ts`
- `web/packages/client/src/session.ts`
- `web/packages/client/src/index.ts`
- `protocol/browser/**`
- Corresponding kernel, browser, browserprotocol, daemon, client, UI, fixture,
  architecture, security, and workflow documentation.

### Retain

- One complete transactionally pinned public snapshot.
- Provider display and unresolved HumanRequest cards.
- Snapshot head and action-precondition revisions.
- Public/private field separation.
- Private HumanRequest detail/reply/cancel.
- `TASK_ENQUEUE` and its exact authority, revision, and result contract.
- Terminal target resolution and terminal behavior.
- Authorization reload, revocation, reconnect generation fencing, and watcher
  cleanup.
- Strict hostile-input and encoded-size bounds.

### Remove

- Page size and cursors.
- Fixed kind traversal.
- `StateItems` and `StateItem` unions.
- `STATE_ENTITY_GET` and `STATE_ENTITY`.
- Per-entity events, tombstones, and refresh requests.
- `hidden_advance`.
- `STATE_RESTART`, restart floors, and restart reason taxonomy.
- TypeScript page staging, pending entity requests, retired entity requests,
  revision maps, and reconciliation.
- The promise that clients observe every intermediate transition.

### Final state protocol

Use an explicit browser protocol generation cutover rather than keeping a
parallel compatibility server:

```text
STATE_GET {}

STATE_SNAPSHOT {
  head,
  factory,
  projects[],
  agents[],
  tasks[],
  human_requests[]
}

STATE_WATCH {
  after_head
}

STATE_CHANGED {
  head
}
```

Watcher registration must close the snapshot-to-watch gap:

1. Install the watcher.
2. Re-read the current durable head.
3. If it is greater than `after_head`, notify immediately.
4. Coalesce subsequent changes to the greatest head.

The TypeScript client must:

- keep the last complete snapshot while refreshing;
- allow at most one refresh in flight;
- debounce a burst into at most one trailing refresh;
- remember the greatest notified head;
- refetch when a returned snapshot is older than that head;
- publish only complete, monotonically newer snapshots;
- reject late messages from an old socket generation;
- reconnect and start with a fresh snapshot after watcher failure.

### Bounds

- Keep client-to-server control messages at no more than 64 KiB.
- Permit an individual server `STATE_SNAPSHOT` of at most 1 MiB.
- Keep the kernel's 4,096 total-entity read guard initially.
- Never truncate or partially publish a snapshot.
- Return one finite `snapshot_too_large` failure.

The 1 MiB value reuses the local API's existing frame scale. If a measured
representative fixture cannot fit, stop. Decide explicitly between a smaller
supported global entity bound and project scoping; do not restore generic
pagination or raise limits without evidence.

### Required causal tests

1. Read snapshot N, commit N+1 before watch registration, subscribe after N,
   and receive `STATE_CHANGED(N+1)` without sleeps.
2. Burst notifications during one refresh and prove at most one in-flight plus
   one trailing request, ending at the greatest notified head.
3. A concurrent writer cannot produce a mixed-head snapshot.
4. Private sentinel values cannot appear in the snapshot.
5. Exact count and encoded-byte bounds fail closed without truncation.
6. Revocation returns only after the state watcher is cancelled and joined.
7. Old-socket snapshot/change frames cannot overwrite a reconnected session.
8. A Go-produced snapshot fixture is consumed by TypeScript.
9. `TASK_ENQUEUE` commits a task, advances/invalidate state, and appears in the
   next coherent snapshot without exposing its private instruction.

Delete paging/entity/restart tests only after these replacement proofs are
green and the production types are gone.

### Site migration

The runtime PR must not silently edit the private site. After it merges, use a
fresh site worktree and:

- rebuild and revendor client/UI tarballs;
- update pnpm integrity and `dark-factory-ui.lock.json`;
- update the public artifact manifest, exact source SHA, hashes, sizes, reviewed
  anchors, and archive member inventory;
- update the real harness for the new state frames;
- retain task enqueue, ready-state onboarding suppression, terminal,
  HumanRequest, reconnect, private-data absence, and provenance proofs.

### Stop conditions

Stop if:

- a real external consumer of page/entity APIs is found;
- representative state cannot fit the chosen bound;
- watcher installation cannot causally close the missed-change window;
- the protocol generation and site artifact cutover cannot be coordinated;
- `TASK_ENQUEUE` or any current site behavior would be weakened;
- public/private projection, revocation, HumanRequest, or terminal authority
  would be weakened;
- another current writing branch overlaps these files.

## Tranche 2: public model collapse

After tranche 1, reduce the representation chain to:

1. Durable kernel entities.
2. One explicit positive-allowlist Go public projection.
3. One decoded immutable TypeScript snapshot consumed by the UI.

Keep private HumanRequest detail and actions separate.

The minimum eventual public snapshot is:

```text
PublicSnapshot {
  head
  factory { dispatch_enabled, capacity, active_runs, revision }
  projects [{ id, name }]
  agents [{ id, name, role, provider, paused, revision }]
  tasks [{ id, project_id, assigned_agent_id, title, status, priority }]
  human_requests [{ id, project_id, agent_id, task_id, status, revision }]
}
```

Keep all currently served fields in tranche 1. Remove unused fields only after
the snapshot cutover is proven.

Fields and data that must remain unrepresentable include:

- project roots and verification policy;
- agent model, reasoning, budgets, usage, account homes, and credentials;
- task body/instruction, result, blocked reason, Change, run, and session data;
- HumanRequest question, reply, delivery identity, resolution metadata, run
  identity, and cancel authority;
- terminal locators and authentication metadata.

If the Unix dashboard snapshot still has no production consumer beyond
`Factory.Revision`, replace it with a narrow Factory operation instead of
maintaining a second partial snapshot. If a consumer exists on the writing
head, use the same semantic projection and preserve its protocol-generation
failure boundary.

The new causal proof must build one kernel fixture containing every public
field and private sentinels, project it through every retained public transport,
and prove semantic parity plus private-field absence.

## Tranche 3: process-memory pairing challenges

Pending, unredeemed challenges are already bound to one random in-memory boot.
Production does not reconstruct the same boot after restart. Durable challenge
rows therefore protect no reachable restart feature.

Use a bounded daemon-memory map:

```text
pending_challenges[digest] = {
  intended_origin,
  expires_at
}
```

Under one mutex, validate origin and expiry and consume exactly once. Restart
or runtime close drops the map. Store only the digest, not the fragment secret.

Retain durably:

```text
browser_clients {
  id,
  public_key_sec1,
  created_at_ms,
  revoked_at_ms nullable
}
```

Retain non-exportable IndexedDB P-256 keys, proof-of-possession transcripts,
durable future-auth rejection, and revocation/live-socket fencing.

Delete after replacement proof:

- durable challenge rows;
- durable abandonment and pruning;
- challenge security events with no reader;
- commit-unknown challenge-mint recovery;
- permanent fingerprint uniqueness;
- dormant per-client capability/revision fields only if the writing head still
  has no product path selecting them.

`pairing_uncertain` remains meaningful after a browser sends `PAIR_PROVE` but
does not observe its result. Do not confuse that with the removable durable
`web_open` commit-unknown outcome.

Required tests cover concurrent single redemption, expiry/origin/capacity,
pending-challenge loss across restart, durable paired-client authentication
after restart, proof non-replay, and durable revocation after another restart.

Physical table/column deletion requires one finite installed-schema transition.
Do not invent a generic migration framework for it.

## Tranche 4: terminal authority

The original audit concluded that writable terminal ownership may be tied to
one authenticated WebSocket connection because connection identity, the live
attempt owner, and the runner controller are all process-local. Durable
authority does not make those live objects recoverable after restart.

However, main has since changed terminal interaction substantially. Before
writing this tranche, re-audit the exact current terminal protocol, daemon
owner, runner mailbox, client, UI, site, and tests. Refresh both deletion
estimates and the exact file list.

The target invariant remains:

```text
writer = { client_id, connection_id } | none
```

- One live authenticated connection is the writer.
- Disconnect, detach, revocation, finalizing, provider exit, owner death, and
  daemon restart clear writer authority.
- A second connection may observe but cannot input until it takes control.
- Input is send-once. Partial or uncertain input closes/fences the writer and
  neither the original bytes nor a suffix is replayed.
- Terminal output sequence, ACK, bounded credit, and replay remain independent
  observation behavior.
- Durable run/session lifecycle and HumanRequest reply/cancel authority remain.
- `browserClientGates` or an equivalent exact-client causal gate remains.

Do not recommend removing durable lease/generation machinery until the current
head proves that exact connection identity, revocation, finalization, and the
owner mailbox close the same stale-writer and no-replay threats.

## Tranche 5: concrete composition

Perform only after the previous protocol methods are gone.

Candidates:

- remove the optional `TerminalBackend` split;
- replace `StateSubscription` and `TerminalAttachment` interfaces with concrete
  daemon-owned lifecycle objects where package composition permits;
- merge redundant package-private TypeScript terminal interfaces;
- inline one-line public constructors only when the package contract and site
  artifact tests are updated.

Retain:

- `browserClientGates`;
- transport-minted `ConnectionID`;
- positive cancellation/join ownership;
- WebSocket, timer, and key-store injection needed for deterministic tests;
- the public terminal capability surface used by UI.

Stop if the replacement is a callback table of similar size, weakens positive
join proof, requires global WebSocket mutation or sleeps, or becomes mostly
file movement.

## Test retention rule

Production concept first, replacement causal proof second, obsolete test third.

Retain unique security proofs for:

- loopback Host/Origin and disabled compression;
- pairing/auth transcript mutation resistance;
- public/private field separation;
- revocation linearization and socket join;
- HumanRequest capability, revision, and at-most-once delivery;
- terminal input no-replay;
- strict Go/TypeScript codecs;
- private-site CSP, pairing URL handling, artifact provenance, and archive
  safety.

Retain unique lifecycle proofs for:

- connection, watcher, and attachment join;
- reconnect generation fencing;
- finalization/input ordering;
- terminal output ACK/replay;
- React StrictMode and controller ownership.

Retain useful component behavior for status mapping, console relationships,
hostile-text rendering, terminal control UX, accessibility, and xterm cleanup.

Tests whose sole subject is page/cursor/entity/restart machinery may disappear
only with tranche 1. Durable challenge recovery/pruning tests may disappear
only with tranche 3. Lease/renewal/generation tests require the fresh terminal
audit and replacement authority proof.

## Readiness checklist for a writing agent

Before creating a writing worktree:

1. Use the repository-authorized remote surface and record the exact current
   Dark Factory and private-site default heads.
2. Confirm the current site artifacts correspond to the current supported
   browser protocol and that no site repair is in flight.
3. Inspect every worktree or observed remote branch whose name or diff overlaps
   state, browser, protocol, terminal, client, or UI files. A stale local
   worktree name is not proof of active work, but it is not safe to ignore.
4. Diff this handover's base `1eab8225` to the new head for all scoped files.
5. Re-read current `AGENTS.md`, `ARCHITECTURE.md`, `SECURITY.md`, and workflow.
6. Confirm `TASK_ENQUEUE`, HumanRequest, terminal, revocation, reconnect, and
   `FactoryAppStatus` behavior to retain.
7. Create a fresh branch/worktree from the exact current `origin/main`.

Do not reuse the old `snapshot-full-page` or `browser-terminal-simplify`
worktrees as an implementation base without proving their exact relationship
to current main.

## Paste-ready first writing prompt

```text
You are the writing agent for the first Dark Factory browser simplification:
replace state pagination/entity replication with one bounded coherent snapshot
plus head-only invalidation.

PRECONDITIONS

1. Read current AGENTS.md, ARCHITECTURE.md, SECURITY.md, this handover, and
   docs/development/WORKFLOW.md in full.
2. Through the authorized repository surface, record the fresh exact default
   heads for dark-factory and dark-factory-site.
3. Confirm the private site's onboarding, provenance, artifact, offline, and
   direct-terminal work is on its default branch and no overlapping repair is
   in flight.
4. Inspect existing local/remote work whose name or diff overlaps browser
   state. Do not reuse stale snapshot-full-page work.
5. Diff handover base 1eab82250652de0f35c5051d6bbccab1447b221c to the
   fresh writing head across the complete scoped browser/client/UI files.
6. If a material contract changed, stop and update the handover rather than
   applying stale line-level instructions.
7. Create a fresh worktree with ./scripts/new-worktree.sh browser-state-snapshot.

GOAL

Replace browser v1 state replication with one coherent STATE_SNAPSHOT and a
head-only STATE_CHANGED watch. Preserve current site behavior, TASK_ENQUEUE,
HumanRequest list/detail/reply/cancel, terminal interaction, revocation,
reconnect, and FactoryAppStatus.

DELETE

- eight-item pages and cursors;
- StateItems and StateItem unions;
- fixed kind traversal;
- STATE_ENTITY_GET and STATE_ENTITY;
- entity_changed, hidden_advance, and tombstones;
- STATE_RESTART floors/reasons;
- TypeScript staging, per-entity requests, revision reconciliation, and retired
  entity responses.

IMPLEMENT

- STATE_GET {};
- one transactionally coherent STATE_SNAPSHOT containing the complete public
  Factory/projects/agents/tasks/unresolved-HumanRequests projection;
- STATE_WATCH { after_head };
- STATE_CHANGED { head };
- watcher-first registration followed by a durable-head reread, with immediate
  notification when a change landed in the snapshot/watch gap;
- one client refresh in flight, debounced notifications, at most one trailing
  refresh, and monotonic complete publication;
- a fresh snapshot after reconnect and rejection of old-socket messages.

Use an explicit protocol generation cutover; do not keep a parallel old state
server. Keep client-to-server control at 64 KiB. Bound a server snapshot at
exactly 1 MiB and retain the 4,096 total-entity read guard initially. Never
truncate. Return a finite snapshot_too_large failure.

Do not change pairing authority, P-256 transcripts, HumanRequest action
semantics, terminal authority, browserClientGates, installed SQLite schema, or
FactoryAppStatus in this tranche.

Add causal tests before deleting obsolete tests:

1. snapshot N, commit N+1 before watch registration, then receive N+1 without
   sleeps;
2. notification burst during refresh produces one in-flight and at most one
   trailing refresh;
3. concurrent writes cannot produce mixed state;
4. private sentinels never appear;
5. exact count/byte bounds fail closed without truncation;
6. revocation joins the watcher;
7. old-socket frames cannot overwrite reconnected state;
8. Go-produced snapshot fixture is consumed by TypeScript;
9. TASK_ENQUEUE invalidates and appears in the next snapshot without exposing
   its private instruction.

Retain/adapt Host/Origin, compression, hostile frame, authorization, revocation,
HumanRequest, task enqueue, terminal target, reconnect, StrictMode, packed
consumer, and site-facing public-contract proofs.

The runtime PR must provide a precise read-only site migration handoff. Do not
edit or deploy dark-factory-site in the same tranche unless the owner separately
authorizes that repository.

Run focused tests appropriate to changed risk and then ./scripts/local-ci.sh.
Report exact results and unverified lanes. Make small coherent commits, publish
through the Maintainer App, obtain a cold exact-head adversarial ALLOW, and do
not merge unreviewed work.

STOP if representative state does not fit 1 MiB, the watch gap cannot be closed
causally, an external page/entity consumer exists, current site behavior would
be lost, another writing branch overlaps, or the protocol/site artifact cutover
cannot be coordinated.
```

## Handover completion criterion

This plan is complete when all tranches have either landed with their causal
proofs and corresponding site artifacts, or have been re-audited and explicitly
rejected because a newly reachable product requirement justifies the retained
machinery. Merely making old paths unreachable is not completion; remove their
production code, tests, protocol fixtures, documentation, and generated public
artifacts together.
