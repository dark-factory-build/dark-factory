# Factory-floor checkpoint ledger

Each checkpoint ships sequentially: implement, required checks, inspect the
real component, exact-head review, merge. The owner subsequently waived
deployments when local visual verification is available. Fixture evidence is
labelled; local screenshots are not deployed or connected-state proof. No animation or geometry is lifecycle authority.

| Checkpoint | Scope | Status |
| --- | --- | --- |
| 1 | Connected project buildings, corridors, open doors, neutral workstations and reachable standing points; fixed topology membership/coordinates despite worker changes | Implemented and locally verified; PR #658 blocked before review/merge |
| 2 | Existing task work objects, all observed affected areas, queued counts, HumanRequest attention, truthful outcomes and provenance | Pending checkpoint 1 merge |
| 3 | Browser-local elapsed-time waypoint movement, one clock, layered sprites, retarget/reconnect/visibility reconciliation and reduced motion | Pending checkpoint 2 merge |
| 4A | Bounded served-hierarchy navigation, breadcrumbs, discoverable off-scope activity and stable viewport | Pending checkpoint 3 merge |
| 4B | Bounded dependency evidence and compatibility; size-bucket footprints; contents from kind, language/composition, manifests, subcomponents and endpoints | Pending checkpoint 4A merge |
| 5 | Inspect the resulting experience; close only evidenced observation gaps and document unknowns | Pending checkpoint 4B merge |

For 4B, footprint means coarse size, contents mean evidenced composition or
responsibility, and connections mean observed relationships. Use a small set
of deterministic footprints, never linear byte scaling or decoration that
claims unmeasured quality, complexity or runtime activity.

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

### Review handoff blocker

The existing factory accepted the checkpoint review task and started an
attempt. Its daemon then stopped with:

> sqlite transaction outcome is unknown: corrupt kernel state: admitted attempt returned no run

The service reports `installed` rather than running, while the exact review
attempt remains durably `running`. No Maintainer review verdict or merge was
recorded. No restart, database modification, duplicate task, or gate bypass was
attempted. Resolve the runtime's uncertain admission state before resuming the
factory handoff; the review must bind the then-current PR head.

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
