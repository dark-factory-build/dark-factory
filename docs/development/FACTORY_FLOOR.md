# Factory-floor checkpoint ledger

One React/SVG scene and generated atlas remain. Geometry, movement and decoration
are browser presentation; the daemon retains work and permission authority.
Deployment was waived where local real-component visual verification suffices.

| Checkpoint | Reviewed PR / merge | Production delta | Verification |
| --- | --- | ---: | --- |
| 1 Connected place | #658 / `5afd73a28d491037e7acc85d7a147f9398767d30` | -53 | Required local CI; 421 web tests; 15 artifact checks; desktop/phone fixtures |
| 2 Physical work | #661 / `e2c3a147652c0adb653fa6a161aa8e82044af8ad` | +138 | Required local CI; task/run, affected-area and attention checks; desktop/phone fixtures |
| 3 Movement | #669 / `12a8ff4b5f178d08b8a403bd89a6c6097a77c85d` | +290 | Required local CI; 80 route segments and reproducible fixture sequence; desktop/phone |
| 4A Structural depth | #675 / `85b114dc74227d6220d00865f3b6973ac64f90a5` | +96 | Required local CI; 433 web tests, zero skips; two hierarchies, Back and off-scope activity inspected |
| 4B Evidenced rooms | #681 / `52c4de91b862f7a53ab34d1dd02700781fe84526` | +196 | Required local CI; 435 web tests, zero skips; endpoint navigation and desktop/phone fixtures |
| 5 Observation gaps | #684 / `28a6d9e06f94131dc6971e692ff97c1db5ceea6c` | +5 | Required local CI; 437 web tests, zero skips; provenance disclosure inspected desktop/phone |

Removed: activity-driven room eviction/reordering, resting-count building shifts,
guessed overseer code locations, closed-door room cards, teleport transitions,
root-only navigation and duplicate task queues. Unknown and outside-scope workers
remain explicit. Existing lists, details, selection and keyboard routes remain.

Bounds: 24 rooms per viewed served scope; four coarse footprints (128x112 through
224x160); dependency payload at most 256 project-local edges and selected display
at most eight links with omission counts. Imports use separate dashed links,
never corridors or animated runtime traffic. Old daemons lacking dependency data
show unavailable, not zero dependencies. No new work registry or history cache.

Checkpoint 5 adds disclosure, not speculative telemetry. Changed-path sampling
is an observed footprint, not current attention, a complete diff, or proof of
editing/testing/review/merge/deployment. Unobserved phases remain unknown.
The sampler returns at most 16 directories, walks at most 50,000 entries and
caches for five seconds; skipped or unreadable areas cannot establish absence.
Samples match project/task/revision/run, and a refused refresh may leave a last
sample displayed. Existing tasks, HumanRequests and recent-work records cover
available handoffs; no additional retained history or provider observation was
justified. Missing build/test/review evidence remains an explicit limit.

Evidence is under `.tools/factory-floor-evidence/` (ignored) and in this task's
rendered screenshots. Images show labelled fixtures unless explicitly stated
otherwise. Historical `/private/tmp` baseline files are no longer present;
conversation images are historical evidence, not current installed-state proof.
The baseline source was `0ba6ea5c21242f3ab9cd2dd201d24d3d7e0c28e5`.
No floor UI deployment or local daemon upgrade is claimed by this ledger.

Dogfooding: real factory workers performed implementation and independent review;
a capacity-two shell barrier exercised simultaneous admission. The overseer
reviewed and enqueued #698, and cancelled obsolete queued work with revision
guards. #683 fixes terminal status; #690 settings; #692 Queue typography;
#673 removes Mac notifications; #694 defers source refresh while runs are active.
These source changes do not establish activation in the old installed runtime.

Remaining runtime blocker: legacy retained resources lack sufficient identity
proof after the filesystem device changed. HumanRequest
`7bdb3121b4cb37e271230fe50369aa58` awaits operator disposition. Preserve the old
home and orphan; do not rebind by inode, bypass installation gates or manufacture
completion. Fresh-home migration requires explicit approval and re-pairing.
#700 relay shutdown merged as `72f7b51d774a4fb67eaac914f53839daaee44b32`;
#701 Codex local-command permissions merged as
`c217767901f81399c8bba3079d40f9a7030207e7`, both after independent ALLOW reviews. Codex permissions do not constrain MCP/browser,
provider-parent or Claude access, and native temporary-directory exceptions
remain. Neither fix is installed. Privacy issue #678 remains open.

Cleanup: preserve dirty/in-use worktrees. Removal of the proven-merged
`nonempty-attempt-result` checkout failed on permissions; its branch was restored
and residual files retained. Cleanup is not complete. No retained daemon Change
was deleted. See ignored `nonempty-result-cleanup.json` for the exact receipt.

Final follow-up: omit the duplicate project heading when the single room has
the same full label, preserving geometry and distinct project headings.
Use Go's native `-modcacherw` in isolated CI so newly downloaded caches do not
block ordinary worktree removal. Existing read-only caches are not changed.
The offline synthetic Go probe verified default 0555 versus 0755 with the flag;
scene tests and CI-environment tests pass. The follow-up PR records final
exact-head CI and independent review.
This follow-up has production delta +2; screenshots at 1280x720 and 390x844
show the actual labelled fixture, not a deployed or connected factory.
