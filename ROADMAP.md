# Dark Factory roadmap

Live use remains frozen until an independent exact-head boot review passes.
GitHub issues and pull requests are the execution record; this file records
only Go product order and architectural boundaries. The retained Rust runtime
and TUI are historical evidence, not a current target.

## Phase A — exact-head boot proof

The Go target's kernel and immediate architecture hygiene are the next gate:

- one fresh runner-owned interactive PTY provider process belongs to one
  admitted run;
- admission transactionally checks dispatch and capacity, selects the current
  canonical queue head, and freezes the derived role, provider, optional model
  and effort, task revision, and Change lease;
- worker and orchestrator mutations are restricted by the durable agent
  hierarchy inside the same transaction as the mutation;
- provider choice is unrestricted interactive authority in V1; bounded
  provider authority is deferred until causal OS-effect proof;
- executable HTTP webhook intake and repository publication are absent; and
- daemon finalization owns provider, verifier, runtime, and storage recovery,
  including process-group leader loss.

Before live use, run the complete causal matrix against one exact Go head, soak
only isolated temporary homes, and obtain an independent adversarial boot
decision. Keep release, installation, external intake, and the operator's live
home outside that proof.

## Phase B — behavior-free decomposition

Split only surviving responsibilities, in reviewable serial changes:

1. Fresh Store schema, scalar validation, admission, and recovery.
2. Attempt and resource ownership, including Change authority.
3. Runner supervision, PTY launch, observation, and finalization.
4. Concrete provider `Build` launch facts and fake-witness tests.
5. Local-API framing, authentication, subscriptions, and domain handlers.
6. Change materialization and exact descriptor handoff.
7. Verification and regenerable storage.
8. Browser client and public projection.
9. Bootstrap, install, and service lifecycle.

Each change uses an exact `base..head` diff and preserves schema, wire, text,
filesystem, and process semantics. File size alone is not a reason to split;
the result must expose a real ownership boundary without adding a framework.

## Phase C — semantic simplification

- Introduce one concrete control layer only where it deletes socket-handler
  choreography.
- Replace manual write/delete gate cleanup with narrow RAII leases.
- Separate agent settings, rules, and memory operations so database and
  filesystem failure semantics are honest.
- Make attempt sources, resource identities, paths, digests, revisions, and
  persisted enum conversion typed at their internal boundaries.
- Pass `FactoryLayout`, tool paths, and policies explicitly instead of reading
  ambient state in execution paths.
- Keep one fresh protocol generation and delete compatibility/placeholder fields
  that have no authoritative producer or current consumer.

## Phase D — measured deletion

- Measure the Go verifier against the simplest owned test path; retain snapshot,
  cache, bundle, and manual-execution machinery only where its measured
  protection justifies the code and recovery surface.
- Reassess public cache/storage status if it is not an operator need.
- Reassess the conservative shell tripwire after a hardened execution boundary
  exists; do not grow it into a shell interpreter.
- Consolidate filesystem identity and private-file helpers only where their
  semantics and failure modes are demonstrably identical.

## Boundaries to preserve

- One small concrete Go package graph; no speculative micro-package
  decomposition.
- One SQLite `Store`; no ORM, actor framework, generic saga, or repository and
  service traits with one implementation.
- One exhaustive principal policy and one durable attempt authority.
- One restartable finalizer as the only terminal writer.
- One exhaustive concrete `provider.Build(Request) (Launch, error)` describes
  launch facts only; the runner owns cwd, PTY, input, process, reap, and
  cleanup.
- Orchestrators propose scheduling policy; `factoryd` enforces correctness.
- `factoryctl` and the browser use the same daemon operations; neither owns
  lifecycle or policy.
- Public events stay bounded and omit credentials, prompts, raw output,
  messages, source, and private deliberation.
- Unique retained work is never an automatic storage-reclamation target.

Prefer deleting an obsolete lifecycle over moving it into a smaller file.

The proposed, inactive official-broker and BYO-broker boundary for future
GitHub integration is recorded in
[`docs/development/GITHUB_APP.md`](docs/development/GITHUB_APP.md). It cannot
activate or merge ahead of the exact-head boot decision and #126's reviewed
provider-neutral quarantine.
