# Overseer snapshot reduction evidence

This note records the evidence for the focused reduction in this Change. It
does not change the wire contract or add a compatibility layer.

## Real request and response path

The production path is one request and one response:

1. `factoryctl overseer status --task ID` emits the `overseer_snapshot` API
   request (`cmd/factoryctl/main.go`, `internal/api/client.go`).
2. `internal/api/server.go` decodes that request, and
   `internal/daemon/api_dispatch.go` calls
   `Store.OverseerSnapshotForAttempt`.
3. The Store returns one targeted task and its assigned agent. A task with no
   resolvable assignment retains the existing project-scoped, paged roster
   fallback. Full authored objective/result text still uses the existing
   `--text-offset` continuation.
4. `projectOverseerSnapshot` and `NewOverseerSnapshotReply` produce the API
   response. `AttemptClient.overseerSnapshot` parses and validates the same
   response shape.

`TestTargetedOverseerSnapshotSerializationOmitsRosterEnvelope` exercises the
request decoder, real reply constructor, JSON parser/validator, preserved
next-action text, and omitted roster identity. The kernel regression
`TestOverseerSnapshotIsProjectScopedAndTaskSelected` also invokes the real
Store targeted and full snapshots and serializes those returned values before
checking that the targeted response is smaller. The API contract test's
deterministic serialized measurements are:

| exchange | calls | response bytes |
| --- | ---: | ---: |
| complete four-agent roster | 1 | 1,340 |
| targeted one-agent read | 1 | 734 |
| reduction | 0 additional | 606 |

These are API response bytes from the runnable focused test, not token
estimates. The parent-observed terminal baseline was 249–279 KiB per complete
management pass and 266 KiB median for workers across 14 transcripts; no
full-terminal after measurement was available in this checkout.

## Representative decisions and preserved safeguards

| decision | same evidence/next action | executable coverage |
| --- | --- | --- |
| routine task supervision | targeted task, assigned agent, current status and result; request one text continuation only when needed | `internal/kernel/overseer_test.go`, `internal/api/overseer_control_test.go` |
| review or delivery intervention | task/run identity and current revision remain in the targeted response; intervention is issued through the existing typed command | `internal/kernel/task_intervention_test.go`, `internal/api/overseer_control_test.go` |
| receipt recovery | no new delivery path; saved/delivered/unknown receipts and idempotent replay remain the existing Store paths | `internal/kernel/task_intervention_test.go`, `internal/kernel/human_request_test.go`, `internal/kernel/peer_question_test.go` |
| retained source handoff | current Change, task work revision and Change revision remain together; stale/superseded evidence is not launch authority | `internal/kernel/overseer_test.go`, `internal/daemon/supervisor_darwin_test.go` |

The focused kernel tests cover two assigned agents, a genuine SQL `NULL`
queued pooled task, cross-project refusal, zero-match assignment fallback,
exact-head paging, text chunking, and invalid continuations. The current
schema permits `assigned_agent_id` to be NULL only for queued or cancelled
tasks; its project-scoped foreign key still protects non-NULL assignments.
Admission claims a queued pooled task for an eligible worker before it runs.
Unusual roles remain refused by the existing API validator rather than being
silently treated as an assignment.

## Maintainer ownership boundary

The Maintainer App is outside this checkout and owns its MCP
`structuredContent`. The local API must not mirror or reinterpret that payload.
The upstream requirement is: a successful typed Maintainer read/write must
return its authoritative structured result in `structuredContent`; short MCP
`content` is acknowledgement only, and callers must not repeat an identical
read/write to obtain another representation. Ambiguous writes must be
reconciled by the existing operation journal. No credentials, internal payload
or lossy wrapper is introduced here.

The narrow slice intentionally defers reductions in review/delivery App
payloads, Maintainer response ownership, and whole-terminal round-trip
benchmarking to their owning work. No publish, merge, deploy, or install was
performed.
