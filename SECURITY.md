# Security

## Reporting a vulnerability

Use [GitHub private vulnerability
reporting](https://github.com/dark-factory-build/dark-factory/security/advisories/new).
Do not open a public issue for a capability, credential, process-ownership, or
network-boundary failure.

Dark Factory is pre-1.0. Only current `main` and the latest release receive
security fixes.

## Current freeze

Live use remains frozen until an independent exact-main boot review passes.

### Go hard-cutover planning authority

This file describes security properties of the implemented Rust kernel unless
a passage explicitly names planned Go. Retained Rust queue selection,
non-interactive providers and pre-admission executable probing are historical
evidence, not compatibility requirements. The authoritative planned Go
contract is [`docs/development/GO_REWRITE.md`](docs/development/GO_REWRITE.md),
which currently requires fresh exact-head review and does not claim a finished
daemon.

Planned Go admission is one global cursor-free immediate Store transaction with
no caller AgentID/task/observation. It validates global settings and uses one
concrete SQL integrity predicate over every row/relation/control that can occupy
capacity or bind active authority and every structurally queued rank, payload,
assignment and agent/profile/project control. Unknown phases, missing
relations, split pairs, invalid IDs/revisions/enums and malformed queued
ranking/payload facts block all admission. Only after that proof may its one
capacity set count admitted, running and finalizing runs. It then validates
durable eligibility and reason precedence, orders by priority descending,
creation time ascending and exact 16-byte task-ID `BLOB` bytes ascending, then validates the
selected canonical Change without skipping corrupt higher work. Known-valid
paused, budget-exhausted or open-run-conflicting queued work is ineligible;
both known roles remain eligible and determine the footprint; known nonqueued
statuses are outside the queue. Unknown/malformed durable control is
corruption. Exact fresh no-admission
precedence is
`dispatch_disabled`, `at_capacity`, `queue_empty`, `no_eligible_work`; there is
no separate Change-cap reason. External repository and provider executable/
configuration/auth availability becomes typed post-admission failure rather
than a stale scheduler filter.

Configured capacity is an integer `C` in `[1, 1024]`; because each reserved
residue belongs to one nonterminal worker run, its count is at most `C`.
Terminal retained-Change aggregate retention and a same-UID-replaced residue's
bytes are still explicit cutover gates rather than claimed current authority.

After a planned Go outer runner becomes active, a declared empty provider pair
is a serialization barrier, not absence proof. Generic outcomes, operator
cancellation and infrastructure failure refuse until the runner performs its
one already-prepared inner Start. Cancellation or daemon EOF cannot skip that
attempt: exact Start failure publishes the canonical no-child result, while a
successful child remains inert until exact pair binding and daemon release.
Only then may the pending outcome reap it. No generic path moves the declared
empty pair to releasing or creates a finalizing declared-pair state.

## Threat model

Dark Factory is a local, single-operator application. Provider processes run as
the operator and use the operator's Claude/Codex subscription. The current
kernel prevents confused or cooperative providers from acting outside an exact
attempt; it does **not** isolate a hostile same-user process from readable
files, credentials, other processes, or the local socket. That claim requires
a separate OS user, container, or sandbox.

The operator API boundary is a private Unix socket with owner-only
directory/socket modes. The pre-release browser boundary is a loopback-only
WebSocket with exact Host/Origin checks and proof-of-possession client keys; it
is not a webhook or generic connector listener. Exposing either local surface
beyond the machine is external deployment work and is unsupported.

The operator-only quarantine API is not a network ingress or trust decision.
It stores bounded external observations as untrusted `InputEnvelope` and
`WorkCandidate` state. Attempt credentials cannot receive, list, inspect, or
reject that state, and no accept/materialize operation exists.

## Principals and capabilities

Every request carries a versioned envelope and is resolved once as one of:

- **Anonymous**: health only.
- **Operator**: authenticated by the private operator credential. Operator
  commands administer durable state but cannot impersonate an attempt for
  completion, blocking, or hooks.
- **Attempt**: authenticated by a random bearer stored in a private per-run
  file. The store derives exact project, agent, task, run, role, provider, and
  Change scope. The bearer works only while that run is `running`.

Missing credentials never imply operator access. Bearers are redacted from
debug/display output and are not accepted in argv, environment variables,
events, logs, request payloads, or caller-selected identity fields. The first
transition to `finalizing` revokes attempt mutation authority atomically. Old,
forged, cross-project, taskless, and terminal credentials fail closed.

The provider environment contains `DARK_FACTORY_ATTEMPT_TOKEN_FILE`, which is
only the path to the private bearer file, not the bearer itself. When it is
present, `factoryctl` uses that attempt credential for every local-API request.
An operator-shaped command invoked by a provider is therefore authorized as the
attempt and rejected if outside its allowlist; it never falls back to
`operator.token`.

God/orchestrator credentials grant scheduling policy only. They cannot create
source paths, launch or finalize processes, change capacity or agents, publish
repositories, or submit another run's outcome.

Attempt messaging and scheduling follow the durable agent tree, not caller
claims or task/run ancestry. Workers may message only themselves, their
immediate parent, or their nearest orchestrator ancestor. Orchestrators may
message themselves or strict descendants, create child tasks assigned to
strict descendants, and assign queued tasks to strict descendants. They cannot
edit tasks or unassign them. Factoryd rechecks the exact running run, project,
role, current task, and `agents.parent_agent_id` inside the same immediate
SQLite transaction that writes the message, task, revision, and event.

Paired browser clients have one durable, revocable capability mask. Public
observation, private HumanRequest detail, HumanRequest effects, and terminal
input are separate bits and are reloaded for every operation while an exact
per-client gate orders them against revocation. A transport-minted connection
identity fences live effects but is never serialized or accepted from a
caller. Private detail does not imply reply/cancel authority, and a terminal
target returned by detail is only an observation coordinate: attach and input
independently revalidate the exact running run, active session, live owner, and
their own capabilities.

HumanRequest reply and cancellation never accept a run destination. Reply
reserves a unique delivery against the Store-derived origin before live-owner
lookup, writes at most once, and permanently marks ambiguity as
`delivery_unknown`. Cancellation derives the same origin inside one SQLite
transaction, checks exact request and run revisions, and atomically finalizes,
revokes, resolves, and invalidates before synchronously fencing the exact live
attempt's current terminal binding. That owner fence uses the binding's actual
client, connection, and generation rather than the cancelling principal. No
binding is definitive success; rejected, partial, uncertain, or controller
failure is visible after the durable commit and is never retried. A stale
request therefore cannot redirect either effect to a retry run. Browser results
are correlated by envelope, request, and exact post-transition revisions;
malformed, generic-action, or forged results close the operation fail-closed.

## Process and cleanup safety

Before provider execution, `factoryd` durably records the admitted run and its
resources. Before the outer runner gate is spawned, the daemon records the
exact identity of a locked private startup file inherited as the gate's stdin.
After a crash, that lock proves whether a gate not yet bound to a PID can still
exist; a missing or replaced bound file is unresolved, never absent.
`factory-runner` then prepares a child blocked before `exec`, reports its PID
and process group, and waits. Only after the daemon records those exact
identities and transitions the run to `running` may the child execute.

Success, block, failure, cancellation, and exit converge through
`finalizing`. A restartable daemon finalizer is the only writer of `terminal`.
It uses the authenticated runner while that runner is live; only the runner may
signal a provider group, and only before it reaps the leader child it directly
owns. Its live-child guard retains that bounded cleanup authority across
cancellation or unwind and disarms immediately after a successful wait; it is
not terminal authority. Stored numeric process identities are observation
evidence, never signal authority. Reused PIDs, paths, runner identities, and job
labels are reported as unresolved rather than touched. A run remains visibly
`finalizing` while any ephemeral resource is active or unresolved.

## Provider and tool boundary

The retained Rust runtime gives each admitted run one fresh non-interactive
provider process and one startup input. There are no taskless resident
processes, delivery replay, provider resume, or session outboxes. Planned Go
replaces the closed-stdin portion with one fresh runner-owned PTY process and
explicit authenticated attach/input/lease authority as frozen in the rewrite
record; this is not authority to reuse a process across runs.

Provider hooks are authenticated observations and bounded requests.
`PreToolUse` applies the durable tool-call budget and a conservative command
tripwire. The tripwire denies recognized destructive or credential-sensitive
commands, every recognized `git push` publication attempt, direct `cargo`,
`rustc`, and `rustup` invocation, direct launch from a recognized mutable
Cargo `target/.../{debug,release}` path, and unsupported shell syntax. Local
source-editing commands such as `git apply`, `git mv`, and `git rm` remain
permitted. Rust-policy verification belongs to `factoryctl task done`, not a
provider build surface. This is not a sandbox: interpreters, generated
programs, MCP tools, provider defects, and direct syscalls can evade string
inspection.

Factory dispatch and provider authority are separate durable controls.
`dispatch_enabled` decides only whether another attempt may be admitted; turning
it off cannot weaken or rewrite an already-admitted attempt. Every agent instead
has one typed execution mode, frozen onto the run at admission:

- In retained Rust, `PlanOnly` is non-interactive and source-read-only; the two
  exact attempt outcome requests remain available;
- in retained Rust, `WorkspaceWrite` is non-interactive and bounds durable
  writes to the admitted source with the provider's native sandbox. Codex also
  denies both system temp aliases. Claude requires its own per-launch temporary
  scratch directory; that provider-owned ephemeral directory is the one
  explicit write exception; and
- `Unrestricted` uses the provider's explicit approval/sandbox bypass.

For planned Go, the same global `BEGIN IMMEDIATE` validates global settings,
runs the one capacity/authority/queued-rank-and-payload integrity predicate,
then checks dispatch, the single admitted-plus-running-plus-finalizing capacity
set and durable eligibility, then selects the canonical task+agent across
the factory; no caller or per-agent loop chooses a queue head. It validates the
selected Change and derives task revision, role, provider and execution mode in
that transaction. A stale dispatcher read cannot choose work or authority.
The per-agent assigned-queue wording in retained Rust is historical only.

Codex and Claude agents default to `WorkspaceWrite`. The shell test adapter has
no native restriction mechanism and therefore supports only `Unrestricted`
instead of claiming a boundary it cannot enforce. These provider controls never
bypass daemon authentication, attempt scope, run phase, or finalization rules,
and they remain cooperative same-user controls rather than OS isolation.

Claude `WorkspaceWrite` is macOS-only because its exact AF_UNIX sandbox policy
is ignored elsewhere. `PlanOnly` has no sandbox stanza and does not technically
depend on that policy, but is conservatively restricted to the supported macOS
product runtime rather than asserting a second platform claim. `Unrestricted`
remains available elsewhere. Factoryd resolves and validates one exact
reviewed Claude executable and every generated settings shape before opening
the retained Rust Store for admission. Planned Go deliberately does not use
that availability as eligibility: canonical work admits first and missing or
invalid external repository state becomes typed `FailureSource`, while missing
or invalid provider executable/configuration/auth becomes typed `FailureSpawn`,
without selecting lower work. A Claude launch, changed executable, or
unreviewed version fails closed. Codex launch configuration is
parsed under `--strict-config` in every mode so a future CLI cannot silently
ignore the daemon-authored hooks or typed permission profile.

Provider output remains opaque and bounded. It never becomes lifecycle
authority and does not enter public events, local-API responses, or tracing.

## Source and repository boundary

`factoryd` is the only product creator and administrator of Changes. Worker
admission reserves one daemon-derived path for one task incarnation, and a
registered wrapper materializes one exact committed tree before the provider
can execute. The leased provider view is a plain writable directory with no
Git administrative locator. Factoryd exposes no repository status, commit,
push, pull-request, or publication operation.

Pre-kernel paths are retained only as `legacy_sources` metadata. They are never
statted, adopted, measured, leased, renamed, or deleted. Forgetting a legacy
record deletes only that row. Managed Change removal requires the exact typed
ID, current revision, durable inode identity, and absence of a live lease; a
replacement or ambiguous path remains visibly pending and is never touched.

This repository scoping reduces accidental delegation but is still not OS
isolation from a hostile same-user process.

## Build and storage boundary

A Rust-policy completion revokes attempt authority and reaps the provider
before source selection. The daemon accepts a
private source snapshot only when canonical before/copy/after manifests agree,
then builds through one project-incarnation/toolchain cache. It copies Cargo's
top-level test executables into content-addressed staging under the run's
registered temporary root, records exact identity and digest, and verifies
both before launch. The stable snapshot supplies the working tree and is
rechecked before and after every top-level test; mutation fails verification
before another test launches. Fixtures are not copied into staging and doctests
are not run. Mutable Cargo sibling
discovery and one target per checkout are not accepted top-level launch paths.
Cargo dependency resolution may use the network through the registered
verifier process. Its registry, Git, and target data live inside the bounded
project cache, but Rust verification is not a network sandbox.

Identity and digest checks prevent accidental and cooperative substitution;
they do not create isolation from a hostile same-user process racing filesystem
operations. Stronger execution isolation requires a separate user, sandbox, or
container.

Resource reclamation may remove only exact, registered, unleased regenerable
cache data; unique retained Changes are never automatic cleanup targets. A
writer makes byte status incomplete. The daemon publishes a private finish
marker and a healthy verifier leader terminates its own group. Only after exact
group absence may remeasurement and byte-policy convergence proceed; an already
measured over-limit cache is refused for a new verification. The daemon reports total
measured bytes plus protected entry count and recoverable failure count, and
does not claim an instantaneous filesystem byte ceiling while Cargo is writing.
If a verifier group leader disappears while descendants remain, finalization
and cache measurement stay pending. A reused numeric group ID is not kill
authority and cannot prove the effect absent.

## Bounded inputs and durable data

Local frames, hook payloads, guidance, messages, events, logs, and generated
configuration have hard size limits. SQLite uses durable
transactions for authority. Guidance and memory files are bounded content, not
an authority ledger.

Provider credentials, repository credentials, prompts, raw output, message
bodies, and source content do not belong in public events or diagnostic
projections.

HumanRequest questions and replies are private operator data. Public
HumanRequest state/events contain no run/session locator, question, reply,
terminal target, cancellation descriptor, delivery identity, provider data,
process identity, token, or diagnostic sentinel. Only the separately authorized
exact-revision detail response may contain the question; the reply remains
ephemeral across the one-shot PTY delivery.

Quarantined input content is private operator data. Receipt events contain only
bounded project/envelope/candidate IDs; candidate-status events add only the
bounded status. Same-observation replay is effect-idempotent, changed bytes
conflict, source changes require the exact current candidate, and rejection is
revision-bound. None of these paths creates executable work.

## Contributor and CI boundary

Tests use a temporary `DARK_FACTORY_HOME`, explicit private socket, disposable
resource labels, and independent cleanup verification. They never inspect or
mutate the installed job or operator home and never send paid provider prompts
unless the task explicitly requires live validation.

A pull request can modify its own workflow, including `runs-on`, and
`.github/` is CODEOWNERS-owned, so that change stops for the owner, who must
inspect it before approving. A green check alone never authorizes merge to
protected `main`: every change needs the `required` aggregate in the merge
queue -- which includes an adversarial-review verdict recorded at the exact
head -- plus resolved threads, and a change touching the authority surface
(`.github/`, the agent rule and boundary documents, the ruleset and
review-gate scripts) additionally requires the owner's approval, re-earned
after every push because stale reviews are dismissed. Persistent CI runner
isolation remains a separate hardening concern.

Every security-sensitive PR receives an adversarial review that explicitly
tries stale credentials, cross-attempt identity, crash boundaries, resource
reuse, unauthorized source/repository selection, and accidental expansion of
the same-user claim.
