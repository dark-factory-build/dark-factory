# Factory-floor checkpoint ledger

Each checkpoint ships sequentially: implement, required checks, inspect the
real component, exact-head review, merge. The owner subsequently waived
deployments when local visual verification is available. Fixture evidence is
labelled; local screenshots are not deployed or connected-state proof. No animation or geometry is lifecycle authority.

| Checkpoint | Scope | Status |
| --- | --- | --- |
| 1 | Connected project buildings, corridors, open doors, neutral workstations and reachable standing points; fixed topology membership/coordinates despite worker changes | Merged as `5afd73a2`; local desktop/phone and merge-group checks verified |
| 2 | Existing task work objects, all observed affected areas, queued counts, HumanRequest attention, truthful outcomes and provenance | Merged as `e2c3a147652c0adb653fa6a161aa8e82044af8ad` (PR661) |
| 3 | Browser-local elapsed-time waypoint movement, one clock, layered sprites, retarget/reconnect/visibility reconciliation and reduced motion | Merged as `12a8ff4b5f178d08b8a403bd89a6c6097a77c85d` |
| 4A | Bounded served-hierarchy navigation, breadcrumbs, discoverable off-scope activity and stable viewport | Implemented from checkpoint-3 merge `12a8ff4b5f178d08b8a403bd89a6c6097a77c85d`; host integration checked; exact-head review pending |
| 4B | Bounded dependency evidence and compatibility; size-bucket footprints; contents from kind, language/composition, manifests, subcomponents and endpoints | Pending checkpoint 4A merge |
| 5 | Inspect the resulting experience; close only evidenced observation gaps and document unknowns | Pending checkpoint 4B merge |

For 4B, footprint means coarse size, contents mean evidenced composition or
responsibility, and connections mean observed relationships. Use a small set
of deterministic footprints, never linear byte scaling or decoration that
claims unmeasured quality, complexity or runtime activity.

## Checkpoint 4A

- Served source: checkpoint-3 merge `12a8ff4b5f178d08b8a403bd89a6c6097a77c85d`.
- Replaces the root-plus-children projection with local, parent-ID-led project
  and subsystem scopes. Breadcrumbs, Back and per-room Enter are browser-only;
  selected task/detail routes, motion, atlas and geometry remain shared.
- Each scope displays at most 24 rooms. Omitted children and active work beyond
  the current scope are named; workers retain either their displayed location,
  explicit outside-display placement, or unknown location.
- Host inspected the actual production fixture at 1280×720 and 390×844:
  repository landing, South's differently nested served IDs, one-level Back,
  breadcrumbs and explicit off-screen work. Current screenshots are in the
  task conversation. Saved `checkpoint4a-*.png` files include earlier revisions
  and must not be presented as proof of this final revision.
- Revision 3 reuses a valid served root as its project landing (only unavailable
  structure gets the fallback room), rejects malformed parent cycles without
  inventing links, makes Back one ancestor at a time, caps compact-scope scale,
  and says activity is outside displayed rooms rather than outside a selected
  hierarchy. `fixture=movement` now also carries the differently nested South
  topology. The host replaced three skipped legacy tests with scope, identity,
  worker-preservation and capacity assertions: all 433 web tests passed with
  no skips, and full `local-ci.sh` passed. Logs are
  `/private/tmp/subsystem-restored-{web-check,local-ci}.log`. A final cleanup
  removes a duplicate room map and the obsolete synthetic-project branch;
  its verification is recorded separately before publication.

## Checkpoint 1

- Baseline source: `0ba6ea5c21242f3ab9cd2dd201d24d3d7e0c28e5`.
- Removes activity-driven room eviction, resting-population building offsets,
  guessed overseer control-room locations, empty-room dimming and straight-line
  position transitions. Replaces the closed-door atlas tile with one neutral
  workstation tile; door openings now come from the shared layout.
- Keeps the 24-room root/direct-child display bound; eligible omitted rooms
  remain identifiable and never fall back to a displayed root just to fit.
- Checks: `./scripts/local-ci.sh` passed; final `corepack pnpm run check`
  passed all 421 tests after the hit-target/artwork cleanup. All 15
  `pnpm run artifact:test` checks passed. Code commit
  `93d2d330c560f36e1a3c33bb0ca30f7576b51044`, PR #658;
  deployment: not requested after local visual proof. Production line delta: -53 (tests/docs/dev fixtures
  and preview HTML excluded; binary atlas uncounted).
- Sanitised visual evidence: `/private/tmp/connected-floor-*.png` (labelled
  fixtures; desktop and phone). Final filenames/results recorded after checks.
- Limits: static positions, uniform footprints, one workstation and five
  standing slots per room. Common areas can extend below established rooms.
  Corridors express access, not imports. Unknown locations remain unknown.
- Before the handoff, the local factory was running with dispatch enabled
  and zero active runs (head 3346). Its Codex overseer accepted the review task. No authorised browser session
  was found; native browser inventory reported the Mac locked.

### Dogfooding recovery and diagnostic repair

The first review attempt stopped before settlement. The scheduler hid its
underlying error behind “admitted attempt returned no run”. Managed service
start authenticated the existing result and recovered the run; the same task
was sent back at its next work revision and successfully obtained independent
ALLOW and queued PR #658. No database writes or duplicate review task were used.

PR #659 preserves the underlying error on a safety stop (production delta +2).
Regression failed before the fix; scheduler race and `./scripts/local-ci.sh`
passed. Merged as `20ce39fe990ff95469c1c4fdfe2bb4c1c89d737c`.
Integrity checks passed; isolated copied-database reads took 179 ms for Snapshot
and 1.1 ms for Run, so no speculative validation optimization was added. The
original transient cause remains unproven because the old code discarded it.

Final local evidence (sanitised fixtures, not a connected factory):
`/private/tmp/connected-floor-baseline-desktop.png`,
`/private/tmp/connected-floor-baseline-phone.png`,
`/private/tmp/connected-floor-after-desktop.png`,
`/private/tmp/connected-floor-after-phone.png`, and
`/private/tmp/connected-floor-after-crowded-phone.png`.
The baseline uses main's original three-agent fixture; the after fixture adds
observed paths and an unknown running overseer. A pointer click on the actual
sprite selects Builder One and opens the existing AGENT detail. The minimum
floor width is 384 scene pixels for this topology; narrower views scroll
without rearranging rooms. Browser checks found only the dev favicon 404.

## Checkpoint 2

The actual factory worker implemented the projection and focused tests as
retained Change `7533bfd4b6005337c4dcc79b4397de05`, based on checkpoint-1
main. The host inspected and integrated its two files with the production
renderer. The worker could run syntax checks but lacked installed dependencies;
the integrated host check passes 426 tests.

Tasks reuse served identities and existing task-detail reads.
All observed affected rooms are marked separately from a worker's representative
location. A bounded paper stack opens the existing Queue; there is no second task board.
Queue rows and floor objects share selected task identity and the same detail
beneath Queue. Selection highlights affected rooms or the queued stack.
Actual HumanRequests use the normal attention route. Terminal tasks lose live
footprints; outcomes exist only while supplied by current state. No Change
registry, browser history, or new protocol was added. The existing appearance
projection omission is repaired.

Local visual baseline: `/private/tmp/work-presence-before-desktop.png` and
`/private/tmp/work-presence-before-phone.png` (sanitised labelled fixtures).
The additional real-process test holds two shell providers behind a release
barrier at capacity two, observes their exact process identities, waits for the
scheduler to observe full capacity, verifies the third task remains queued,
then requires all three terminal successes. It and the real-provider watchdog
passed under `-race` through the shared lease (`/private/tmp/work-presence-parallelism.log`).

The dogfooding worker's durable running interval overlapped two overseer
intervals; this is durable-state evidence, not a retrospective process census.
Normal browser pairing and Local Network Access were explicitly authorized.
The hosted connected view was READY with 0 active runs, 12 spaces, 8 resting
agents and empty question/queue counts. Evidence:
`/private/tmp/work-presence-live-desktop.png` and `...-phone.png`.
These show the older hosted UI, not the local checkpoint-2 build.

Runtime diagnostic repair `20ce39fe` was installed through the guarded repository
workflow with zero active runs and a private backup. Web and relay are ready;
schema remains version 10. Dispatch was already paused at preflight and was
preserved. This runtime install does not deploy the hosted UI.

Checkpoint 2 merged as `e2c3a147652c0adb653fa6a161aa8e82044af8ad` (PR661). Checkpoint 3, including the row-corridor clearance repair, merged as `12a8ff4b5f178d08b8a403bd89a6c6097a77c85d` (PR669).

### Follow-up dogfooding work

The user requested factory-owned implementation and review; no further session
sub-agents will be started or reactivated. Queued factory work:

- Terminal status contradiction: `1b3e2da46aa015d2c6e6f4e400738725`.
- Settlement deadline under actual load: `6d9c937eeec14eb29673980e80a3ef5a`.
- Queue typography and supported active-work actions: `9bcd166f897d453094449b88445ab58e`.
- Settings copy and phone layout: `6fcc596fa4b247008190895731078da6`.
- Overseer worker housekeeping: `797a22196ace48568a8d9c76982c391d`.

The second scheduler stop preserved its original error: recording a live runner
exit exceeded its context deadline in task validation. Managed start recovered
the daemon. The terminal-fix submission timed out but was accepted; status
reconciliation prevented a duplicate. No raw database writes were used.

Checkpoint-2 full `local-ci.sh` passed, followed by the final 426-test web check
covering shared Queue selection and the existing queued editor without duplicate
detail. Final sanitised screenshots: `/private/tmp/work-presence-linked-desktop.png`,
`...-running-detail.png`, `...-queued-detail.png`, `...-phone.png`,
`...-phone-running-detail.png`, `...-question.png`, and `...-crowded.png`.
Connected hosted screenshots `.../work-presence-live-active-*.png` are private:
they show the older hosted artifact with one worker plus its overseer, not two
worker slots. Configured worker capacity remains one.
