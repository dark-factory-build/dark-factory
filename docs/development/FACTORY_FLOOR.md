# Factory-floor checkpoint ledger

Each checkpoint ships sequentially: implement, required checks, inspect the
real component, exact-head review, merge, refresh the existing site artifacts,
deploy, verify. Fixture evidence is labelled; hosted UI deployment does not
upgrade a local daemon. No animation or geometry is lifecycle authority.

| Checkpoint | Scope | Status |
| --- | --- | --- |
| 1 | Connected project buildings, corridors, open doors, neutral workstations and reachable standing points; fixed topology membership/coordinates despite worker changes | Implementation and verification in progress on `connected-floor` |
| 2 | Existing task work objects, all observed affected areas, queued counts, HumanRequest attention, truthful outcomes and provenance | Pending checkpoint 1 deployment |
| 3 | Browser-local elapsed-time waypoint movement, one clock, layered sprites, retarget/reconnect/visibility reconciliation and reduced motion | Pending checkpoint 2 deployment |
| 4A | Bounded served-hierarchy navigation, breadcrumbs, discoverable off-scope activity and stable viewport | Pending checkpoint 3 deployment |
| 4B | Bounded dependency evidence and compatibility; size-bucket footprints; contents from kind, language/composition, manifests, subcomponents and endpoints | Pending checkpoint 4A deployment |
| 5 | Inspect the resulting experience; close only evidenced observation gaps and document unknowns | Pending checkpoint 4B deployment |

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
  passed all 421 tests after the hit-target/artwork cleanup. Commit/PR and
  deployment: pending. Production line delta: -53 (tests/docs/dev fixtures
  and preview HTML excluded; binary atlas uncounted).
- Sanitised visual evidence: `/private/tmp/connected-floor-*.png` (labelled
  fixtures; desktop and phone). Final filenames/results recorded after checks.
- Limits: static positions, uniform footprints, one workstation and five
  standing slots per room. Common areas can extend below established rooms.
  Corridors express access, not imports. Unknown locations remain unknown.
- Dogfooding: the existing local factory is running, dispatch enabled, and
  idle (head 3346, zero active runs). Use its Codex overseer for the review/merge
  handoff and workers for subsequent checkpoints. No authorised browser session
  was found; native browser inventory reported the Mac locked.
