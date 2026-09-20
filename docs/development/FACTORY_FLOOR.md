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

#706 merged as `94c2b80c34c32292365fce8cacd99a4258411063`; installed source
`3302db773a11086f2be30a5b2cf6fb4c7e34b8d8` starts two concurrent workers without
the personal computer-use notification helper. The dedicated test browser now
connects successfully; hosted screenshots still show the older deployed floor.
Six obsolete fixture servers and their helpers were closed (18 processes).

The next real-run failure is task API access: the command sandbox permits the
credential file but refuses the client's verified parent-directory walk. Keep
that validation and the filesystem boundary. A small `factoryctl attempt mcp`
stdio tool exposes the existing attempt/overseer argv through the same parser
and authenticated API, without operator commands or shell execution. Codex
uses this tool for task retrieval and durable outcomes. No daemon wire protocol,
authority, registry or provider observation changes. Installed proof is pending.

#707 merged as `d36c2cb23bf8b4387af680bb791dbd5abd2848bb`; installed source
`9bb51b6ed99487d837f59c0ef85e7c15f0550d2c` exposes the task tool. The real CLI
requires explicit tool approval despite `approval_policy=never`; both workers
were stopped after that refusal. The overseer also had duplicate TOML write
keys because its working directory is its runtime home. The follow-up deduplicates
those paths and authorizes only the factory tool, configured run browser and
overseer Maintainer server. Codex reuses the established Maintainer server name.
No broader filesystem or daemon authority is granted. Installed proof is pending.

The process audit additionally found and closed one verified orphan shell from a
cancelled worker. Its cleanup investigation is task `909f8419cd6147b7bd7bb53c6ea5a9af`,
awaiting the tool-approval fix. `subsystem-visual` worktree removal was refused by
filesystem permissions; residual files and its branch remain. See ignored
`orphan-shell-closure.json` and `subsystem-cleanup-refusal.json`; do not claim
all worktree cleanup or detached-command cleanup is complete.

The tool-approval candidate `632cfe1cf3f2d92cb11b84f7f8cc0d9182ad4b2c` passed
full local CI and independent review (#708). Installed development proof observed
two workers plus one overseer, successful task-tool calls, and an overseer-managed
browser follow-up using the existing worker task. Factory-owned browser captures
at 1280×720 and 390×844 were inspected; they show the labelled production-component
fixture, not a connected factory. Evidence is under ignored
`.tools/factory-floor-evidence/factory-browser-evidence/`. The browser run directory
and observed helpers exited. No computer-use helper was observed.

The process audit closed 22 obsolete processes, including the final fixture
server after its evidence was retained. The cleanup worker produced retained
Change `09b53fafb8ef81fe461b8f241b8be965`; its first host regression failed and
was returned through the overseer. Independent factory review cannot read another
retained Change through its current supported interfaces; host import/checks and
the existing exact-head publication review remain required. No new Change-read
service or filesystem permission expansion was added to bypass that boundary.

Host review also refused the initial detached-process patch: rechecking a
non-child PID's birth before `kill` does not pin it against reuse between those
operations. The source snapshot and failed-test/safety findings remain in ignored
`orphan-source-snapshot.json`, `orphan-host-check-failure.txt`, and
`orphan-host-safety-block.txt`. The overseer was told not to publish that approach.

Final publication: #708 merged as `a492763cac66f56560a0869c40cc148eea4548df`;
the installed development source is its identical reviewed head. Both previously
refused worktree remnants were removed after native cleanup of the checkout-local
read-only Go module cache. Sixteen additional inactive clean worktrees were
removed after exact merged-patch verification.

The cleanup handoff now leaves production signal authority unchanged and adds a
bounded FIFO reproduction of the unsupported detached-child case. Host focused
runner checks and full local CI passed. The local-CI refusal fix uses a scrubbed, absolute Git preflight so a non-Git
checkout fails without creating cache directories. Environment sanitization still
precedes lease acquisition; an initial ordering change was rejected because
inherited Git locators could redirect the lease. The focused shell check supplies
hostile Git locators pointing at a real repository.

Site #62 merged as `8fad739a4960f4bbdcad922b2f88ed9ef0f5ddb9` and deployed
as `dpl_7qL6VucPEZjzgym7SpXvKcy6QKNC` to `app.darkfactory.build`. Both public
packages derive from clean runtime source `a492763cac66f56560a0869c40cc148eea4548df`,
which includes checkpoints 1, 2, 3, 4A, 4B and 5. Artifact verification, formatting,
lint, types, 120 unit/render tests, production build/canary check, and 37 browser
smokes passed (three suite-defined skips). The dedicated paired browser verified
live topology, subsystem navigation, worker selection/configuration and phone
settings. Captures: ignored `deployed-final-desktop.png`,
`deployed-project-desktop.png`, `deployed-project-phone.png`, and
`deployed-final-settings-phone.png`. Wait for topology separately from the state
snapshot before capturing. This live factory was idle; moving/crowded scenarios
remain the labelled fixture evidence above.

Thirty obsolete task/build/site checkouts have been retired, with superseded
dirty files preserved under ignored `retired-checkouts/`. The final handoff and
its old CI-refusal checkout are retained until merge; unrelated operator edits
in the primary runtime/site checkouts remain untouched.


## Scanned inventory

Optional topology-node `inventory` counts eligible regular files from the existing
bounded scan. Missing inventory means unavailable; present zero counts mean an
empty scanned inventory. Dot directories, dependency/build/cache exclusions,
symlinks and special files remain excluded. This is not a count of every file on
disk. `direct` counts files immediately in the node's physical path; `total`
includes those files and descendants. Repository/module/package nodes may share
a path and therefore share counts: never sum overlapping child totals.

Classification uses case-insensitive filenames, with this precedence: recognized
source files with explicit test names (`_test.go`, `.test.`, `.spec.`, `test_`,
`_test`) or inside `test`, `tests`, `__tests__` directories are tests; Markdown,
reStructuredText, AsciiDoc and bare README/LICENSE are documentation; JSON,
YAML, TOML, INI, CFG, lock files and recognized build/configuration filenames are
configuration; recognized image/font/audio/video/PDF extensions are assets;
recognized programming/web source extensions are source; everything else stays
unclassified. A JSON fixture in tests is configuration; unknown `.txt` or binary
content is explicitly unclassified. Test presence means neither success nor
coverage. Import and manifest observations retain their existing partial scope.

`samples` contains up to three lexically ordered immediate filenames, each at
most 128 UTF-8 bytes. `samples_omitted` counts remaining direct files, including
names too long to transmit. No source text is sent. Optional summaries consume
only remaining capacity under the existing response limit; `inventory_omitted`
reports how many served nodes lost their summary to that bound. Missing counts
must never be displayed as zero. The topology digest includes inventory while
node identities remain tied to project, kind and path.


## Inventory equipment and activity

Rooms project the optional scanned subtree inventory into at most six equipment groups: source racks, test benches, document drawers, configuration panels, asset displays and unclassified crates. Counts are partitioned between repeated groups, never one object per file. Direct and subtree counts remain separately inspectable; shared-path totals overlap. Equipment capacity omissions, direct filename sample omissions and missing summaries are explicit. Test benches indicate test-file presence, not success or coverage. Named child cabinets use the existing hierarchy navigation; remaining served siblings are reached by pages.

Room geometry and the five worker standing slots remain independent of inventory contents. Only a busy worker with current observed work in a visible room runs the restrained interaction loop. Reduced motion, hidden tabs, disconnection and idle floors stop the animation clock; reconnection reconciles the current snapshot instead of replaying events. This is an activity projection, not command execution evidence.

## Interactive production sources

The production floor and inspector share one derived view. They do not advance
work: animation is presentation only. The private project-content read reconciles
durable records on connection and a bounded visible-tab refresh; it never calls
GitHub from a sprite or panel.

| Fact | Authority and identity |
| --- | --- |
| Mission | Existing outcome objective/criteria/state, with durable task bindings; task success does not complete the objective |
| Construction | Stored Change and task phase; identity is the Change ID, including after worker completion |
| Publication | Maintainer publication receipt, or an observed exact factory branch and settled head; subsequent heads retain the recorded binding |
| Review | Existing exact-head adversarial verifier over formal reviews; host reviewer journal and matching process-start receipt expose actual activity |
| CI | Repository-level Actions observation; one run ID, revision and PR membership, with head and merge-group scopes kept separate |
| Merge | GitHub merged timestamp and actual merge commit; merge-queue admission is separate |
| Delivery | Existing release journal, verified destination receipt and included-PR membership; merge alone proves no delivery |

The host controller's existing release-observation cadence records bounded facts
through the operator-only `factoryctl production observe --json-stdin` path.
Customer runtimes without this host controller show unavailable external evidence;
there is no fallback to host credentials from an agent or browser. Related task
and conversation content retains its existing private read authority.

Missing or stale review/check evidence cannot approve a new head. Unavailable
reads retain the last durable item and disclose the observation gap. Active,
blocked and undelivered items remain inspectable; completed work has a bounded
visible rack and explicit expansion. External record and relationship limits are
shown, not presented as successful or empty work.
