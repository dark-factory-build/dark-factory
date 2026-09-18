# Yielded continuations

This is the bounded design for a continuation that waits on one durable
event, decision, or dependency without retaining a worker slot, provider
process, terminal, or attempt bearer. It is deliberately provider-neutral.

## Authority and state

The continuation belongs to the existing task incarnation and work revision.
It records an immutable context digest, one await condition, its creation
revision, and a resolved/cancelled state. The await condition names a durable
human request, peer question, handoff, dependency edge, or invalidation head;
it never names a terminal input buffer. Creation atomically revokes the run
credential, closes the terminal as unresolved, and releases owned resources.
The old run therefore cannot accept an attempt result or terminal input after
yielding.

An event resolver compares the continuation revision and condition in the
same transaction that records the event. A matching unresolved continuation is
made queued exactly once; cancellation, task replacement, and a work-revision
change instead resolve it cancelled. Admission creates a new run and fresh
credential from the queued task. It receives the task, immutable context, and
the resolved event metadata, not an old bearer, process identity, or blindly
replayed terminal transcript.

## Ordering and recovery

Both yield and event delivery append invalidations in their write transaction.
The resolver handles either ordering: an event before yield is observed by the
yield transaction; an event after yield resolves the stored condition. A
unique unresolved condition per task work revision and compare-and-swap update
make duplicate daemon recovery or a daemon restart harmless. Delivery with an
unknown prior terminal acknowledgement is recorded as unknown and is never
replayed to a fresh provider.

The scheduler admits normal queued work across projects. It does not reserve
a global waiting overseer slot; project A's open question is a durable await,
not an active orchestration run, so project B remains eligible. Standing
overseer wakeups consume worker events and periodically reconcile unfinished
work after the configured quiet interval. They no longer gate on the historical
`tool_calls_used` allowance.

## Provider boundary

The default resume always launches a fresh native provider. A provider-native
resume may be attached only when the provider exposes a current official
session reference, the reference is causally bound to the continuation, and
the daemon can verify it at fresh admission. Unsupported, missing, stale, or
unverifiable references fall back to the provider-neutral fresh launch.

## Required proof before enabling

- Deterministic shell tests cover event-before-yield and event-after-yield,
  exactly-once wake, daemon restart, cancellation/scope-change precedence,
  resource cleanup, and refusal of the old credential and terminal.
- Capacity-one tests show a worker can yield for a helper or handoff while the
  helper progresses; cross-project tests show one human question cannot block
  another project's supervision.
- Two real native providers run the same scenario with roles exchanged. Each
  proves fresh-authority relaunch; an unsupported native resume reports that
  fallback rather than pretending to continue a session.
