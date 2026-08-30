# Security

## Reporting a vulnerability

Use [GitHub private vulnerability reporting](https://github.com/dark-factory-build/dark-factory/security/advisories/new).
Do not open a public issue for a capability, credential, process-ownership, or
network-boundary failure.

Dark Factory is pre-1.0. Only current `main` and the latest release receive
security fixes.

## Threat model

Dark Factory is a local, single-operator application. The shell provider runs
as the operator. The kernel prevents confused or cooperative providers from
acting outside an exact attempt; it does not isolate a hostile same-user process from readable files,
credentials, other processes, or the local socket. That claim requires a
separate OS user, container, or sandbox.

The operator API boundary is a private Unix socket with owner-only
directory/socket modes. The browser boundary is a loopback-only
WebSocket with exact Host/Origin checks and proof-of-possession client keys; it
is not a webhook or generic connector listener. Exposing either local surface
beyond the machine is unsupported.

### Hosted-origin compromise response

A compromised hosted origin or same-origin dependency can exercise every
capability granted to a paired browser client. Stop trusting the hosted page,
revoke every paired browser client through the owner-authenticated local API,
and prove that no browser connection remains. If revocation cannot prove
connection cleanup, quiesce the browser runtime or stop the service and keep it
stopped until cleanup is resolved. Only then remediate and republish the exact
reviewed hosted artifact. Re-pair clients only after that artifact is live.
Origin allowlisting, CSP, and non-exportable browser keys reduce risk but do
not replace this response.

## Principals and capabilities

Every request carries a versioned envelope and is resolved once as one of:

- **Anonymous**: health only.
- **Operator**: authenticated by the private operator credential. Operator
  commands administer durable state but cannot impersonate an attempt for
  completion or blocking.
- **Attempt**: authenticated by a random bearer stored in a private per-run
  file. The store derives exact project, agent, task, run, role, provider, and
  Change scope. The bearer works only while that run is `running`.

Missing credentials never imply operator access. Bearers are redacted from
debug/display output and are not accepted in argv, environment variables,
events, logs, request payloads, or caller-selected identity fields. The first
transition to `finalizing` revokes attempt mutation authority. Old, forged,
cross-project, taskless, and terminal credentials fail closed.

The provider environment contains `DARK_FACTORY_ATTEMPT_TOKEN_FILE`, which is
only the path to the private bearer file, not the bearer itself. When present,
`factoryctl` uses that attempt credential for every local-API request. An
operator-shaped command invoked by a provider is therefore authorized as the
attempt and rejected if outside its allowlist; it never falls back to
`operator.token`.

The current daemon supports worker runs only. Operator and attempt roles confer
no cross-agent authority; task and run ancestry never grant agent authority.

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
`delivery_unknown`. Cancellation derives the same origin in one SQLite
transaction, checks exact request and run revisions, and atomically finalizes,
revokes, resolves, and invalidates before synchronously fencing the exact live
attempt's current terminal binding. Rejected, partial, uncertain, or
controller-failed fencing is visible after the durable commit and is never
retried against another run. Browser results are correlated by envelope,
request, and exact post-transition revisions; malformed or forged results fail
closed.

## Process and cleanup safety

Before provider execution, `factoryd` durably records the admitted run and its
resources. Before the outer runner gate is spawned, the daemon records the
exact identity of a locked private startup file inherited as the gate's stdin.
After a crash, that lock proves whether a gate not yet bound to a PID can still
exist; a missing or replaced bound file is unresolved, never absent.
`factory-runner` prepares a child blocked before `exec`, reports its PID and
process group, and waits. Only after the daemon records those exact identities
and transitions the run to `running` may the child execute.

Success, block, failure, cancellation, and exit converge through `finalizing`.
A restartable daemon finalizer is the only writer of `terminal`. It uses the
authenticated runner while that runner is live; only the runner may signal a
provider group, and only before it reaps the leader child it directly owns. Its
live-child guard retains bounded cleanup authority across cancellation or
unwind and disarms immediately after a successful wait; it is not terminal
authority. Stored numeric identities are observation evidence, never signal
authority. Reused PIDs, paths, runner identities, and job labels are reported
as unresolved rather than touched. A run remains visibly `finalizing` while any
ephemeral resource is active or unresolved.

## Provider and tool boundary

Go uses one concrete `internal/provider.Build(Request) (Launch, error)` boundary.
Admission freezes provider, optional model, and optional reasoning effort only.
`Build` returns one exact absolute executable, ordered argv, and complete ordered
environment. The runner owns the descriptor-bound Change cwd, task delivery,
PTY, input, process group, wait/reap, output, and cleanup.

Provider choice is unrestricted interactive authority in V1. Shell is the only
implemented provider. Its executable is `/bin/sh` and its argv is `/bin/sh`,
`/dev/fd/11`; bounded task bytes are written to that sealed descriptor after
the launch gates pass. The PTY is reserved for later interactive terminal
traffic. Claude Code and Codex are unsupported and fail closed. Non-None
verification policies are unsupported by the current daemon and cannot be
treated as completion proof.

The runner's provider boundary does not grant source or lifecycle authority.

Factory dispatch and provider authority are separate durable controls.
`dispatch_enabled` decides only whether another attempt may be admitted; it
cannot weaken or rewrite an already-admitted attempt. Missing or invalid
external repository state becomes a typed post-admission failure, without
selecting lower work.

## Source and repository boundary

`factoryd` is the only product creator and administrator of Changes. Worker
admission reserves one daemon-derived path for one task incarnation, and a
registered wrapper materializes one exact committed tree before the provider
can execute. The leased provider view is a plain writable directory with no
Git administrative locator. Factoryd exposes no repository status, commit,
push, pull-request, or publication operation.

Managed Change removal requires the exact typed ID, current revision, durable
inode identity, and absence of a live lease; a replacement or ambiguous path
remains visibly pending and is never touched.

## Build and storage boundary

The current daemon has no generic build API or completion verifier. Project
creation uses `VerificationNone`; non-None verification values are
schema-recognized, but every non-None policy is rejected by the supervisor as
unsupported before provider execution. No client or provider may treat
an unimplemented verifier as proof of success.

Resource reclamation may remove only exact, registered, unleased regenerable
runtime data; unique retained Changes are never automatic cleanup targets. A
writer makes status incomplete. Only after exact process-group absence may
remeasurement and cleanup proceed. A reused numeric group ID is not kill
authority and cannot prove the effect absent. Stronger execution isolation
requires a separate user, sandbox, or container.

## Bounded inputs and durable data

Local frames, messages, events, and logs have hard size limits. SQLite uses
durable transactions for authority.

Prompts, raw output, message bodies, and source content do not belong in public
events or diagnostic projections. HumanRequest questions and replies are
private operator data;
public state contains no run/session locator, question, reply, cancellation
descriptor, delivery identity, provider data, process identity, or token.

## Contributor and CI boundary

Tests use a temporary `DARK_FACTORY_HOME`, explicit private socket, disposable
resource labels, and independent cleanup verification. They never inspect or
mutate the installed job or operator home and never send paid provider prompts
unless the task explicitly requires live validation.

A pull request can modify its own workflow, including `runs-on`, and
`.github/` is CODEOWNERS-owned, so that change stops for the owner, who must
inspect it before approving. A green check alone never authorizes merge to
protected `main`: every change needs the `required` aggregate in the merge
queue, including an adversarial-review verdict at the exact head, plus resolved
threads. Authority-surface changes additionally require the owner's approval,
re-earned after every push because stale reviews are dismissed. Persistent CI
runner isolation remains a separate hardening concern.
