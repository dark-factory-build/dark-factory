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


## One inhabited factory: source and display contract

The preceding ledger records historical checkpoints. The current interface has
one flat floor, one SVG renderer and one movement system. The former nested
replacement floors, separate production machines and expanding production area
are removed. Changes open the existing evidence inspector; tasks, missions,
workers and the project library retain their existing controls and authority.

### Reading the factory

The default floor shows source search, a Changes entry point and Help. Help
contains the legend and integrated-source provenance. One concise notice links
to Changes when observations cannot be fully mapped; a verified empty diff is
not missing evidence. Changes owns proposal selection, before/proposed paths,
review and checks. The floor retains a clearable selected-proposal indicator.

Selecting equipment opens one source inspector. Its closed Source details
contains canonical identity, counts, exact files and relationships. Discuss this
source opens the existing scoped Board directly. Library reading leads with
content and consequential state; task use, sources/access and management are
separate disclosures. Detail and social preferences remain only in Settings.

### Integrated source

Topology reads an immutable archive of each registered repository's configured
local target ref. The archive is checked against Git blob identities; dirty
checkout files and export-attribute rewrites cannot become finished equipment.
`sources[]` records repository ID, displayed prefix, target ref, resolved revision,
observation time and integrated/unavailable state. An unregistered checkout may
provide an explicitly unavailable legacy observation, never integrated proof.
No topology read fetches, changes a ref or advances the checkout. A remote merge
will appear only after the configured local target ref has actually advanced and
the topology refresh reads it. The revision remains inspectable throughout.

Eligible regular files use the existing bounded scanner. Dot directories,
vendor/dependency/build/cache directories, symlinks, submodules and special files
are excluded. Missing inventories are unavailable, not zero. Classification is
by filename/extension: explicitly named source tests first, then documentation,
configuration, assets, source and unclassified. A JSON test fixture remains
configuration; a test rig establishes file presence, not test success or coverage.

`inventory.direct` counts immediate files; `inventory.total` counts descendants.
Repository/module/package wrappers can share a physical path. The projection
chooses one owner for that path (package, then module, directory, repository),
retains wrapper IDs as aliases, and sums only canonical direct counts. A room's
displayed total contains its assigned assemblies, excluding separately displayed
child rooms. Inspectors, containment controls and dependency links resolve to the
canonical owner, retaining that owner's direct and subtree counts rather than
offering a second alias inspector with contradictory exact contents.
Up to 32 exact immediate filenames are sampled evenly across the sorted list,
including its endpoints; small lists remain complete. This gives large packages
broader filename coverage without guessing responsibilities. `samples_omitted`
and the existing frame budget's `inventory_omitted` disclose omissions. No source
text is sent.

### Stable references and grouping

A source entity reference is `<project ID>:<served topology node ID>`. The served
ID is derived from normalized relative path and node kind, independent of display
grouping, file counts, activity or labels. Library and message-board links
store this reference plus the exact source revision; optional file links
also store the repository-relative path. A rename relationship is established
only by observed Git rename evidence; path identity alone never proves continuity.

The browser's persisted detail setting selects coarse areas, automatic useful
areas, or fine directories on the same floor. Automatic grouping collapses
short namespace chains such as src/packages/apps, while broad namespaces remain
areas containing package assemblies. Large direct packages inside internal/ get
their own rooms. Tiny leaf areas stay in their ancestor. Command entry points
remain assemblies in their shared `cmd` area. These choices use integrated source,
never live activity or proposed file counts.
Up to 96 rooms aggregate the remaining canonical entities under visible
ancestors; all canonical source entities remain searchable. Each room pictures at most twelve
assemblies with an explicit overflow label. Search focuses an entity's owning
room and retains its exact source identity. Viewport culling bounds rendered
rooms. Changing live work or proposal selection does not choose the room order.
Explicit detail changes may regroup the floor while preserving selected identity.
Assembled rooms share regular walls and corridor rows; equipment uses each room's
work zone. File counts scale equipment within that zone. Room footprints use bounded bays
and assembly counts. The entrance, task tray and common seating sit beside the
rooms; crowded seating extends downward without moving source areas. Detail and
social controls live only in Settings.

Assemblies combine source machinery, test rigs, control cabinets, document shelves
and asset racks. Eligible file counts select clamped size buckets: 1–4, 5–20,
21–80 and 81+. Size is scope, never quality or completion. Name-based movement,
messaging, selection, admission and storage motifs are disclosed as filename
hints, not verified semantic analysis. Parts and secondary resource silhouettes
belong to the same display assembly and do not count files again. Package
assemblies include their resource directories, while substantial nested source
directories remain named equipment. `representedIds` lists the canonical source
areas assigned exclusively to each display assembly. Its aggregate inventory
sums their direct counts; source inspectors still expose each canonical area
and its directly owned files. Display grouping never changes those identities.

The explicit `source_files` project-content read takes project ID, served node ID,
`tested_source` (the exact target SHA), offset and limit (maximum 32). It returns
canonical directly owned paths, kinds and bytes with total/next offset. A changed
revision is refused instead of returning newer files under an older inspection.
Missing or omitted contents remain visible as unavailable or incomplete.

### Proposed versions and evidence

The existing production observation supplies source `{kind, base, target, head,
fingerprint, observed_at, paths, omitted, reason}`. `base` is the actual merge-base
used for the comparison; `target` is the observed target endpoint. A committed
proposal names its real head. Dirty work is identified as a working-tree
observation with a content fingerprint. Plans and terminal prose produce no
source objects. Missing local commits/base information refuses the observation.
Up to 32 exact path operations include additions, modifications, deletions and
renames with old path and the existing scanner's filename-based resource class;
omitted paths are disclosed beside the Change and full diff. Dirty fingerprints
frame each untracked path, mode and content digest; repeat path/content reads and
a final HEAD/dirty check refuse changes detected during observation.

Committed observations reuse the same static analyzer on exact base/head archives.
`relationships` contains at most 32 added/removed `{from_path, to_path, weight}`
entries; `relationships_omitted` and `relationships_unavailable` disclose bounds or
missing analysis. Dirty working-tree relationships are explicitly unavailable.
Dashed +/× dependency cables and the existing Change inspector show these deltas
without changing the integrated graph. Observed proposed resource counts use the
same equipment vocabulary; their `?` marker does not assert final inventory or
scale. Concurrent versions of a new area remain separate proposed assemblies. Selection
filters versions before composition; overview caps pictures without ever summing
competing versions into a fictitious aggregate. Selecting any Change reaches its
own proposed version even when it is beyond the overview picture limit.

Each proposal is independently keyed by project, repository and visual Change ID.
Frames, repairs, dismantling marks and move destinations use the same assemblies
as finished code. Overview marks every observed footprint; selecting a Change
isolates its operations without combining overlapping futures. New areas append
provisional rooms without shifting finished rooms. Closed/cancelled/merged
proposals leave the overlay and remain in inspectable Change history. Merge
metadata does not edit topology: the actual integrated target, including conflict
resolution, is the only source of the next finished factory.

Reviews and checks remain attached to their examined head. Dirty work and
mismatched heads cannot inherit current approval or applicable checks. Stale,
incomplete and unavailable observations remain labelled. The inspector keeps
before/proposed paths, findings, checks, task/mission links and separate delivery
receipts accessible from affected equipment. No elapsed-time or edit-volume
completion percentage is produced. Deployment receipts never stand for merge,
and disconnected views never establish current runtime confirmation.

Review sprites represent scoped observed reviewer runs, one identity per run,
using worker movement. Their inspectable scope is the assignment and changed
areas, not a claim that each pictured file was inspected. Resting workers have
deterministic local seats and shelf/coffee visits; the persisted social setting
chooses valid nearby furniture or common tables. These visits are decoration,
invoke no models or knowledge reads, and cannot delay work or review. Paused,
waiting, needs-you and disconnected state remains authoritative. Reduced motion
and hidden/disconnected scenes stop ambient activity. Bookshelves open the existing
project library; explicit document actions alone read its content.
