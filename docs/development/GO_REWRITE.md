# Go local-runtime hard cutover

This is the canonical design, proof plan, and permanent record for replacing
Dark Factory's local Rust runtime with Go. It is organized around product
authority and external effects, not around the existing Rust crates. Git
history remains the archive; the Go runtime will not migrate a Rust home,
schema, event log, protocol, or serialized state.

## Web-first redirection (authoritative from 2026-08-26)

This section supersedes every later statement that requires a Go TUI, Bubble
Tea, CLI/TUI parity, non-interactive provider stdin, or the Rust attention
model. The later sections remain the chronological implementation record and
evidence for already completed kernel work. They are not authority when they
conflict with this redirection. The final elegance pass must remove the
superseded prose after its replacement tests are green.

The product is now a local Go daemon with durable authority and PTY-backed
agents, controlled primarily by a hosted responsive web application. There is
no Go `factory-tui`. `factoryctl` remains the bootstrap, service, recovery,
diagnostic, automation, and browser-pairing client; it is not a second primary
operator interface and does not owe visual feature parity.

The hard-cutover decision is unchanged: fresh Go home, schema and protocols;
no Rust migration, event upcasting, mixed runtime, or compatibility period.
The existing Rust TUI and other replaced Rust local-runtime crates are deleted
only after the revised web/PTY gate passes.

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

| State | Exact work retained or stopped |
|---|---|
| Complete and retained | Fresh SQLite contract; typed kernel; atomic admission; exact attempt credentials; run/finalizing/resource state; bounded invalidations; Change ownership/materialization groundwork; owner-only Unix control API; typed `factoryctl` client; Darwin process identity; two blocked-exec gates; terminal-exit spool; gated Darwin PTY primitive; durable terminal-session admission, activation, recovery uncertainty and finalization guards; durable browser clients/challenges/revocation/input leases; exact PAIR/AUTH transcripts; strict browser-v1 handshake/binary codecs; strict runner terminal union, complete-write poisoning, incremental frame decoder and one fixed replay ring through `f1f72aa`; reviewed framework-neutral `@dark-factory/client` handshake/transcript/binary core and exact package gate through `d03491f`; independently reviewed question-only durable HumanRequest creation, private detail, reply reservation/acknowledgement/uncertainty, restart recovery, lifecycle convergence and bounded public projection through `40f5873`; the single-owner PTY execution loop, exact ready/input handoff, correlated retained replay, bounded filter retirement, poisoned writes, actual-EOF ordering and daemon-loss convergence through `ebcfd24`; exact two-field `AUTH_PROVE` through `4b18c38`; runner-owned exact HumanRequest PTY reply through `0f313a9`; daemon live-attempt registry, mailbox, bounded observers, finalization gate, active supervisor cancellation and joined shutdown through `d9709b9`; the closed attempt-only `request_human` API plus direct durable dispatch through `c29d154`; exact 8 KiB browser/Go/TypeScript terminal payload bound through `9ab44c3`; read-only exact lease authorization and one-shot failed-install/input-reservation revocation through `8853acb`; finalization/release linearization, natural-exit acknowledgement convergence, cancellation visibility and real descendant reaping through `ea1ee4b`; canonical Darwin runtime, Change and Change-worker fixtures through `699515d`; exact committed provider access to the attempt-only `request-human` command through `d4ce713`; independently reviewed fixed-page browser canonical state and private-detail separation through `1a562e4`; strict Go/TypeScript browser state/detail wire and causal reducer through `9b7689d`; exact-run HumanRequest terminal projection, fail-closed loopback browser state transport, guarded framework-neutral TypeScript Session client, direct daemon Store adapter and daemon-owned durable browser revocation through `b61fca8`; private transport-minted per-WebSocket connection identity through `53d68dd`; independently reviewed public MIT `@dark-factory/ui` package and contributor fixture through `18b5b0e`; exact daemon terminal acquire/renew/release/input/resize and HumanRequest reply effects through `ae28dc8` |
| Reusable with adaptation | `internal/runner` live-child/process-group ownership; daemon supervisor choreography; bounded API framing/auth separation; dashboard projection/client reducer direction; rebased recovery branch `go-recovery-reserved-fix` at `185cd5f`; fail-closed runtime/spool/Change close branches at `f239815`, `347c977`, and `4183205` |
| In progress but held | Browser terminal binary/effect routing is the next isolated lane above the reviewed exact daemon effects. Recovery still needs replay onto the PTY design. The development/Go sub-gates are integrated and green; final browser E2E and the post-test system census remain cutover blockers. |
| Obsolete | Startup-input-only/closed-stdin provider contract; separate stdin/stdout/stderr provider pipes as the product transport; TUI/Bubble Tea packages, lanes and parity tests; generic attention projection; message-on-next-run as the live-question answer |
| Proved for revised architecture | Current Chrome on macOS can connect from the protected hosted HTTPS preview to exact `ws://127.0.0.1:43123` with the dedicated loopback permission; strict Origin/Host checks, binary traffic, reconnect, denial, no-daemon, port-collision and cross-site refusal are causal. A fresh Darwin PTY child remains inert until release, owns a controlling terminal/process group and is reaped without orphaning. SQLite owns exactly one terminal session per admitted run and refuses terminalization until its exact close is proved. The outer runner owns the live PTY loop without goroutines, transfers initial input exactly once, gates terminal commands on readiness, bounds and correlates replay before and after actual EOF, and writes one HumanRequest reply byte-for-byte without borrowing browser lease authority. The daemon registers one joined owner before release, rejects wrong sessions, routes bounded replay to multiple observers, actively cancels pre-live supervisors on shutdown and serializes infrastructure failure with terminal effects. Its exact effect bridge binds the durable client to one private WebSocket identity and generation, commits Store authority before runner effects, never replays ambiguous input/replies, and preserves positive terminal evidence until exact supervisor acknowledgement. The loopback server now enforces exact Host/Origin, pairing and per-operation durable client authority, serves bounded canonical state, joins subscriptions/connections, and couples exact-revision durable revocation to all-runtime socket close. The TypeScript Session client signs exact transcripts, publishes only complete fixed-head state, fences stale generations and ambiguous pairing, rate-bounds reconnect, and consumes the same pagination/empty-chronology contract. Each authenticated socket receives a private transport-minted identity that cannot be selected by a backend, serialized or exposed by formatting. The public React package renders bounded BUILDING, AGENT, task and read-only NEEDS YOU state without private detail or policy, installs and builds under the stripped Corepack gate, and remains consumable as an exact packed artifact. |
| Blocked until proved | Binary terminal and typed HumanRequest browser transport; interactive terminal/reply/action expansion of the public UI; complete private host integration; factoryctl web bootstrap/recovery; revised crash-cut vertical slice |

Read-only redirection audits were assigned without overlapping writes:

- `web_redirect_pty_audit`: current runner, supervisor and recovery reuse versus
  obsolete pipe/session behavior;
- `web_redirect_browser_audit`: current Chrome/Safari loopback rules, browser
  security, and the public-UI/private-host repository split;
- `web_redirect_human_protocol_audit`: durable `HumanRequest`, browser v1,
  Go/TypeScript drift prevention and UI package boundaries.

Their concrete conclusions are incorporated below. Broad production work does
not resume until this revised plan is committed.

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
  the obsolete non-interactive process model.

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
id project_id agent_id task_id run_id
created_at updated_at revision kind status
reply_max_bytes can_reply can_open_terminal
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
  -> HUMAN_REQUEST_DETAIL { request_id, revision, question }
```

An observe-only or human-reply-only client cannot read it. Reply remains the
existing exact-revision `BeginHumanReply -> one runner write -> acknowledge or
delivery_unknown` transition. Browser v1 will not advertise a generic
`HUMAN_REQUEST_ACTION` merely to reserve a shape. The existing `human_actions`
capability gates exact HumanRequest replies; it does not advertise an arbitrary
action operation. The first typed action is added with its concrete daemon
operation, finite arguments and preconditions; `cancel_run` remains the likely
first slice.

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
- Store commit before terminal-spool acknowledgement/removal;
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
→ revoke input, terminate/reap, prove group absence and close the PTY
→ publish exact exit evidence
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
state                  declared | active | closed | unresolved
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

SQLite checks exact 16-byte IDs and the closed state enum. Holder and expiry
are either both present or both absent; a holder is an exact 16-byte client ID,
is a foreign key to `browser_clients`, and a lease may exist only while the
session is active. `unresolved_reason` is
present only in `unresolved` and is 1–4096 bytes. `activated_at_ms` is absent
in declared, required in active, optional in closed/unresolved only because a
pre-exec session can close or become unresolved, and never precedes declaration.
`closed_at_ms` is present only in closed and never precedes declaration or
activation. Every timestamp is nonnegative and no later timestamp precedes an
earlier present one. Generation and input sequence are nonnegative SQLite
integers; an increment at the signed 64-bit limit fails closed instead of
wrapping. No holder also requires input sequence zero. Store validation derives
and checks the same run's unique `runner_process` resource on every lifecycle
read/write; the schema continues to forbid duplicate `(run_id, kind)` resources.

The legal lifecycle is `declared → active → closed`, `declared → closed`, or
`declared/active → unresolved → closed`; `closed` is absorbing and
`unresolved` is never treated as closed. Admission inserts the declared row in
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

The running-to-finalizing transaction clears the holder, advances the generation
and revokes the attempt credential together. Close requires positive typed
evidence from the live owner; recovery may close only after exact runner and
provider-group absence. Otherwise it marks unresolved and terminalization
remains blocked. None of these mutexes or rows creates process/signal
authority by itself.

Session `revision` changes on lifecycle transitions only. Lease generation and
input sequence are the private concurrency guards for lease/input mutations;
they emit no public invalidation and do not change the lifecycle revision.
`updated_at_ms` therefore tracks the lifecycle revision rather than lease
traffic. Every lifecycle change also advances the run aggregate revision and
emits its single bounded run invalidation. On daemon start, one immediate
`ResetTerminalLeases` transaction clears every holder/expiry, zeros its input
sequence and advances every affected generation before browser service starts.
Old connections are independently invalid because `boot_id` changed.

Terminal closure has three concrete, non-browser entry points. Live-owner
closure requires the daemon's exact bound runner control, an active session on
a finalizing run, frozen input, recorded provider exit and positive owned-group
absence; one Store transaction releases the provider process/group resources
and closes the session. Recovered closure requires already-recorded exact
runner and provider-group absence plus corresponding recovered resource/exit
evidence. Pre-exec closure requires the exact registered outer owner to have
synchronously aborted the inert child while provider identity was never
activated; one transaction releases the still-declared provider resources and
closes the declared session. Arbitrary booleans, browser calls and recovered
numeric PID/PGID are not closure evidence.

Admission declares it before allocation; activation binds the already-active
exact runner identity. A live runner may close it after it has stopped writes,
closed the master and proved provider-group absence. Recovery may close it only
after exact runner and provider-group absence; every weaker case becomes
`unresolved`. Provider leader and group remain separate durable facts because
leader reap is not group absence.

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
runner synchronously freezes input, kill-and-waits its owned provider group,
closes the PTY master, publishes final exit/cleanup evidence and exits. The
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
- use of the final JSON terminal spool as a live output transport. The spool
  remains only for bounded final exit/recovery evidence.

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
input is placed in argv, environment, output events, the final terminal spool
or an anonymous provider-stdin file. This is one concrete launch step, not a
second terminal-input authority or compatibility path.

#### Private runner protocol freeze

The existing process tree is retained deliberately:

```text
factoryd
└── outer attempt-runner child (ordinary blocked process; no PTY)
    └── inner change-worker child (blocked PTY child)
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

Before terminal frames are added, private framing must perform complete bounded
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
the terminal authority before spool publication.

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
spool evidence and exits. This intentionally deletes the old behavior that
allowed a provider to continue indefinitely after daemon EOF.

### Terminal ownership, replay and backpressure

The runner owns one bounded in-memory scrollback ring, monotonically increasing
output sequence and ephemeral retained floor/head per live session. No output
cursor is written to SQLite or used as recovery authority. V1 deliberately has
no durable live-output journal: browser reconnect can replay retained output
only while the same daemon and runner session remain live. Daemon/runner loss
produces an explicit terminal reset and canonical lifecycle state; only final
exit/cleanup evidence uses the durable terminal spool. This is at-most-once
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
It is not a generic warning or attention score. Daemon-authorized typed actions
share its browser card later, but do not widen the first question foundation.
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
id, project, agent, task, run,
created_at, updated_at, revision,
kind, title, summary, why_human_is_needed,
safe typed context items, available interactions, status
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

The question card initially exposes only this explicit interaction union:

- `InlineReply(max_bytes)`;
- `OpenTerminal(run_id)`.

An agent may create only a bounded private question through an authenticated
`request_human` operation. It cannot mint public card text, a button, label,
action kind, arguments or authority. The daemon derives all identity from the
exact attempt credential and decides which interactions exist.

Creation is unique for one exact `(run, request idempotency identity)`. Viewing,
subscribing or opening the terminal never resolves it. Reply requires expected
request revision and revalidates the exact current run/state. A stale origin
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

The first real typed action is deliberately deferred until the question
foundation and live runner gate hold. Its frozen boundary is only
`cancel_run`, because the kernel already owns that finite transition. A paired
client with `human_actions` submits no arguments, only exact request and run
revisions. One immediate Store transaction revalidates both sources and the
client capability, enters the exact run into finalizing, revokes attempt/input
authority, resolves the request as `action_cancel_run`, and emits both
invalidations. Do not add action rows or implement approve, reject, retry,
resume, publish or permission grants without a concrete product contract.

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
HUMAN_REQUEST_DETAIL_GET / HUMAN_REQUEST_DETAIL / HUMAN_REQUEST_REPLY
TERMINAL_ATTACH / TERMINAL_ATTACHED / TERMINAL_ACK
TERMINAL_LEASE_ACQUIRE / TERMINAL_LEASE_RENEW / TERMINAL_LEASE_RELEASE
TERMINAL_RESIZE / TERMINAL_DETACH / TERMINAL_EXIT / TERMINAL_RESET
ERROR
```

Binary terminal input/output frames include a fixed v1 opcode, exact run or
session identifier, sequence/generation metadata and bounded payload. Exact
encoding and maximums freeze only after Go/TypeScript fixture and malformed
frame tests. Unknown versions, capabilities, messages, opcodes or control
values fail closed.

`internal/browser` owns one small consumer-side backend interface implemented
directly by `internal/daemon`. It contains only pairing/authentication,
canonical state/watch, HumanRequest reply/action, and terminal attach/lease/
input/resize operations. The only streaming member is one terminal attachment
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
- Go accepts an output range whose half-open endpoint is exactly `2^64`; the
  TypeScript codec currently rejects it. The first contract change must choose
  one exact rule, align both codecs and add a mutation test at that boundary.

The first wire slice freezes one small state machine. Structured controls own:

```text
attach -> attached | reset | error
lease acquire | renew | release -> exact lease result | error
resize -> terminal acknowledgement | error
detach -> terminal acknowledgement | error
HumanRequest reply -> resolved | delivery_unknown | error
cancel_run -> resolved | error
terminal EOF/exit -> observation only
```

Binary client frames carry exact session, positive lease generation, strictly
next input sequence and at most 8 KiB of bytes. Binary server frames carry the
exact session, generation zero, contiguous output range and at most 8 KiB of
bytes. Wrong direction, unattached/wrong session, zero/overflow, unknown
opcode, malformed or oversized input closes the connection fail-closed. Input
acknowledgement distinguishes complete acceptance from rejected, partial and
uncertain outcomes; partial or uncertain input freezes/revokes the generation
and is never retried. Output reset carries exact retained floor/head and forces
a fresh rendering correlation. EOF/exit does not authorize process completion.

One authenticated connection owner selects its WebSocket input, state watch
and the attachment's existing bounded event queue. It owns at most one terminal
attachment, synchronously closes/joins it on detach or socket loss and never
stops the provider. Slow-client policy may reset or detach that observer, but
cannot block PTY drain or another observer. Runner scrollback remains the only
replay ring.

The framework-neutral TypeScript surface owns a `TerminalHandle`, explicit
lease, complete input receipts, typed reset/output/EOF/exit events and
revision-bound HumanRequest reply plus the single `cancel_run` action. It does
not expose connection IDs, delivery IDs, runner/process identities, another
client's lease, generic action arguments or private reply bytes in public
state. `connect()` reaching authenticated/syncing is distinct from canonical
state becoming ready; UI code must observe the ready state rather than infer it
from connection resolution.

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

Required causal tests cover replay/live ordering, gap/reset, acknowledgement
chronology, stale connection/generation/revision, partial/uncertain input,
resize uncertainty, reply one-shot delivery, attach/finalization races,
multiple observers/one writer, revocation, malformed frames, slow-client
isolation and attachment/socket/goroutine cleanup. Mutations must kill the
exact-maximum endpoint mismatch, removed private connection identity, skipped
lease/revision checks, input replay, reset suppression, output without ACK
credit and detach without join.

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

Security-event kinds are exactly `challenge_minted`, `client_paired`,
`duplicate_fingerprint`, and `client_revoked` in v1. Challenge consumption is
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
owner-only Unix revocation path. Terminal input, HumanRequest reply/action,
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
opens the hosted app with that challenge only in the URL fragment. A challenge
is forbidden in the path or query. The host sends `Referrer-Policy: no-referrer`
and loads no analytics or third-party resource. Its first synchronous
first-party bootstrap reads and clears the fragment with `history.replaceState`
before starting any application network request, then registers the browser
public key with the loopback daemon. HTTP access logs, requests, referrers,
telemetry, errors and copied post-bootstrap URLs must never contain the
challenge. Reuse, expiry, wrong daemon/origin/key/client and revocation fail
closed. `web status`, `pair/open`, `list-clients`, and `revoke` are the initial
CLI recovery surfaces; exact names may collapse if one command suffices.

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
→ one daemon-minted typed action is revalidated and committed
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
9. **Lane F: platform/CLI** — init, service, doctor, recovery, web open/pair/list/
   revoke, packaging and hard-cutover plumbing after C stabilizes.
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
- An attempt cannot mint a typed action. Removing daemon action allowlisting or
  current-state precondition checks is killed by authority tests.
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
  HumanRequest, typed action, attach, input, resize, binary output, reset,
  reconnect and structured errors; TypeScript compilation alone is not proof.
- Public UI tests prove revision/gap/stale-response behavior, terminal lease and
  reconnect UX, HumanRequest card rendering without transcript reads,
  accessibility and responsive layouts.
- The private host proves it imports the exact public artifact and completes
  the same pair/state/terminal/request/action/refresh slice without a protocol
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
HumanRequest inline reply, typed action, stale/idempotent/restart/privacy GREEN
browser protocol v1 Go/TypeScript fixture and real-server integration GREEN
factoryctl web bootstrap/recovery GREEN
public UI BUILDING/AGENT/NEEDS YOU/terminal/accessibility/responsive gate GREEN
private site exact-artifact pair/state/terminal/request/action/refresh integration GREEN
fresh isolated install/service/restart/uninstall GREEN
whole-runtime/web elegance audit complete
independent exact-head architecture/security/process/Store/browser reviews ALLOW
hosted-origin compromise/revocation runbook recorded in SECURITY.md
```

If private-site integration cannot run, hard cutover stops and reports that
exact blocker; no full product frontend is substituted into daemon packages.
When the gate passes, delete the Rust TUI and replaced local crates, remove all
TUI and non-interactive-provider documentation/CI, retain `control-plane/`
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
  exact external identity is bound, recording one immutable activation time.
  Declared or active resources enter `releasing`; declared/active/releasing may
  become `unresolved` when absence cannot be proved, or `released` on positive
  cleanup/absence proof. Exit evidence must match the exact activated
  identity and cannot predate its activation time. `unresolved` may only
  become `released` after later positive proof, never active/releasing or
  signal-authoritative. `released` and terminal are absorbing. Any
  non-released resource forbids terminalization.
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
  daemon loss. Store commit precedes runner acknowledgement. Live PTY output is
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
- Replace display-string control flow and whole-profile read/modify/write with
  typed actions and revision-checked granular updates.
- Replace the old generic NEEDS YOU/attention projection with the question-first
  durable `HumanRequest` contract above. A request exists only for one exact
  authenticated running attempt that explicitly needs a human reply. Failed
  runs, finalization stalls, capacity waits, paused agents, exhausted budgets
  and blocked tasks remain canonical status unless a later concrete
  daemon-owned action contract proves they require an operator decision. The
  first such typed action is only exact revision-checked `cancel_run`; no
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
- `internal/browser`: loopback-only WebSocket, Host/Origin checks, pairing,
  client credentials, terminal multiplexing and bounded browser DTOs. Public
  TypeScript client/UI code lives under `web/`, never in daemon packages.
- `internal/install`: private home/bootstrap, safe three-member archive,
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
   after activation and pathname-`exec`s it once with the registered PTY slave
   as its controlling terminal. Its PID, PGID, and birth remain unchanged;
   input is accepted only through the exact live terminal session and current
   lease while the run remains running.
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
three root regular executable members with bounded individual/aggregate bytes,
expected names/modes, Darwin architecture, version/build identity, and hashes.
They write/sync private staging and receipt, atomically publish one immutable
version directory without replacement, durably record old/new activation and
plist/service phases before effects, swap the relative `bin/current`, render
and reload one allowlisted plist with `AbandonProcessGroup=true`, and prove
launchd PID, actual daemon executable/build identity, and all three
receipt-bound siblings. Exact new health commits/removes pending state. Every
known pre-health
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
  -> create agent with shell/unrestricted profile
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
  -> deliver reply at most once or surface delivery-unknown; commit one typed action
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
- Lane B, runtime: Claude/Codex concrete launch adapters, observation/recovery
  hardening, and deterministic fake-executable tests.
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
| Browser authority boundary | Enumerate every browser mutation and assert it invokes one typed daemon operation with paired authority and revalidated state | direct filesystem/policy mutation; display label or agent prose selects behavior |
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
  exact three-member success;
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
- a clean checkout builds all three Go binaries and the public web packages,
  runs the authoritative gates,
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
  EOF after provider release never cancels useful authorized work. A terminal
  record is create-only and published only after exact group convergence and
  the sole Wait. Recovery acknowledgement binds the full attempt/process/exit/
  message value, digest and file identity and removes the spool only after the
  caller asserts the exact Store commit. Numeric recovered identities remain
  observation-only and gain no signal path.
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
- A closed nine-call union and four reply constructors preserve the exact
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
- The daemon and Change worker commit the exact absolute native `factoryctl`
  executable identity before selection/admission and revalidate it immediately
  before provider execution/release. The provider receives only that locator;
  its cleared `PATH` remains `/usr/bin:/bin`.
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
  locale controls, noninteractive Git/GitHub controls and terminal/color
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
  release the provider once, observe its terminal spool, clean exact resources,
  and prove crash/restart convergence. The cooperative same-EUID boundary and
  scan-to-exec assumption remain explicit residual limits.

Synchronous shell-supervisor proof on integrated head `4c2da24`:

- `Daemon.RunNext` is one concrete synchronous owner for the kernel vertical
  slice. It admits the canonical task, creates and binds the private runtime,
  starts the inert outer runner, durably binds its exact identity, receives and
  atomically binds the exact provider process/group identity, commits each
  Change checkpoint, marks the run running and only then releases the shell
  provider. No production goroutine, provider interface, retry framework,
  stage table or second state authority was added.
- Provider and outer-runner termination are separate durable external facts.
  One private-field `ProcessExit` representation backs explicitly named
  `ProviderExit` and `RunnerExit` fields. The daemon validates and commits the
  exact provider terminal record before acknowledging and deleting its spool,
  then waits the outer child and commits that distinct exit. A causal provider
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
  crash matrix must still prove every pre/post-release, terminal-spool, Store-
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
  effects are proved separately at `ae28dc8`; the binary browser wire, typed
  action, operator web bootstrap integration, interactive terminal/reply/action
  UI and private site host remain outside this checkpoint. They must reuse the
  durable authority and live-owner seams rather than widen this state-only
  browser capability set implicitly.
