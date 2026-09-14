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
No floor UI deployment is claimed by this ledger.

Dogfooding: real factory workers performed implementation and independent review;
a capacity-two shell barrier exercised simultaneous admission. The overseer
reviewed and enqueued #698, and cancelled obsolete queued work with revision
guards. #683 fixes terminal status; #690 settings; #692 Queue typography;
#673 removes Mac notifications; #694 defers source refresh while runs are active.

Recovery (14 September): the operator approved a fresh home. The old service and
automation are stopped and disabled, preserving the old home and its legacy orphan;
no inode rebinding, database migration or fabricated completion occurred. The old
HumanRequest `7bdb3121b4cb37e271230fe50369aa58` is stale. The recovered project
preserves unlimited run admissions and the 2700-second per-run limit. Only two
unfinished diagnostics were carried forward through the API. Browser pairing
succeeded, but a connected hosted-console snapshot is not yet verified.

#700 relay shutdown and #701 command permissions reached the replacement runtime.
#703 merged as `8ee4ab1c709d3d41b00fd99080ef3a4b149fbda0`; its installed source
`3c86e520b50b6eb056f870c3992ea29e235d0363` disabled Codex browser/computer tools,
but a real worker still started Computer Use helpers through inherited plugins.
The service is stopped pending #704's plugin isolation and real-launch check.
Both interrupted verification attempts and the queued launchd diagnostic are
preserved; overseer supervision has not yet been restored in the fresh home.
See ignored `recovery-status.json`, `no-computer-use-live.json` and
`plugin-isolation-checks.json` for bounded receipts. Privacy issue #678 remains
open: local-command permissions do not constrain MCP servers, provider-parent
or Claude access, and native temporary-directory exceptions remain.

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

Browser follow-up (#705): local fixture proof uses the real component at
1280x720 and 390x844 with two isolated Playwright MCP sessions. Evidence is in
ignored `run-browser-current/`; no hosted or connected-factory claim. #704 merged
as `87a2d2c5692336d030b86ff5ad128255ca078c04`, but its installed worker still
started computer-use helpers through a personal Codex `notify` hook. The factory
was stopped and dispatch disabled. #705 ignores personal Codex configuration,
keeps the overseer's Maintainer explicit, and adds the optional run browser.
Final installed-worker proof and restoration of supervision remain pending.

#705 merged as `95d1febe490ae3913a20db0370a91102743cea69`. Installed attempts
failed immediately because interactive Codex rejects the exec-only
`--ignore-user-config` flag. Dispatch is disabled. The follow-up uses the
supported `notify=[]` override for the observed personal notification hook;
no blanket personal-configuration isolation is claimed.
