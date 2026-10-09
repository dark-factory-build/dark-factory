# Development workflow

This is a reference for local development, checks, releases, and installation.

## Local development

The worktree helper fetches the configured origin default branch before creating
the branch and checkout:

```sh
./scripts/new-worktree.sh <slug>
cd .worktrees/<slug>
```

Prefer deleting obsolete behavior and duplicated machinery over compatibility
code, feature flags, or speculative abstractions.

The routine source check is:

```sh
./scripts/go-check.sh
```

It runs Go formatting, vetting, ordinary short tests, the TypeScript build and
tests, and `git diff --check`. It does not acquire the process lease. During
implementation, run this check plus the focused tests for the changed package
that your environment can run. A factory worker reports a check its sandbox
cannot run as "not run locally; required by the gate" instead of blocking on
it.

factoryd does not run the full gate before review: it reviews the exact head,
and the protected merge queue's required CI is the full gate on the combined
tree. A queue ejection comes back to the author naming the failing checks. Run
the full local gate yourself when broad local integration proof is needed:

```sh
./scripts/local-ci.sh
```

It includes repository and release fixtures, ordinary checks, and every
process-sensitive Go and end-to-end check. Smaller fixed modes are available
when their inputs are known: `./scripts/local-ci.sh --ui` runs the UI source
and leased browser smoke checks, `--runtime` runs ordinary and process gates,
and `--release` runs the source check plus release and packaging fixtures. The selector in the
protected CI workflow chooses these modes from the complete merge-queue diff;
uncertain or mixed paths use the full gate.

For CI edits, run the affected gate fixtures and source checks, adding a full
local run where that resolves a concrete risk. Authors and reviewers do not
repeat the entire suite merely because a PR is about to enter the queue.

Additional checks follow changed risk. Process-sensitive checks share the
repository lease:

```sh
./scripts/with-local-ci-lease.sh go test -count=1 ./internal/daemon/
```

Concurrency, process ownership, finalization, or recovery changes benefit from
a focused `-race` test. SQLite stress is relevant to SQLite open, snapshot,
file-binding, dependency, or toolchain changes. A whole-kernel race run is
exceptional and uses `-timeout 1200s`. One memory-heavy Go run at a time avoids
macOS resource exhaustion.

## Review and merge ordering

factoryd independently reviews every published head, then enqueues,
merges and releases it; overseers and workers never review or enqueue a
published head themselves. The protected merge queue
runs required CI on the combined tree before merging. A pending future queue
check is a delivery condition, not by itself a source-review defect. Reviewers
still block concrete defects and false verification claims; neither local test
evidence nor an ALLOW verdict bypasses the protected gates.

Only a trusted publisher records a verdict: an account whose GitHub
`author_association` on the review is `OWNER`, `MEMBER` or `COLLABORATOR`, or
a numeric user id listed in the repository variable
`DF_REVIEW_TRUSTED_PUBLISHERS` (comma-separated; administrators set it outside
pull-request files, for example to the Maintainer App bot's user id). An
unset or empty variable trusts maintainers only; a malformed one fails the
gate. Anyone else's review is listed as ordinary feedback: its markers and
`CHANGES_REQUESTED` neither allow nor block.

Trust is about the publisher, not the reviewer. The assessment must come from
a separate reviewer; identify that reviewer, the examined head, findings and
checks in the review body. The publisher attests to that work. GitHub
authenticates the publisher, but neither the account identity nor the verdict
marker proves reviewer independence. An authorised maintainer can publish an
independently obtained agent review with host credentials:

```sh
gh api -X POST repos/OWNER/REPO/pulls/N/reviews -f commit_id=HEAD \
  -f event=COMMENT -f body="Reviewer: ... Findings: ... Checks: ...
Dark-Factory-Review: allow HEAD"
```

A review carries `Dark-Factory-Review: allow HEAD`, `block` or `note`
(`factoryctl review` writes it). Use `block` for an unresolved finding or `note` for evidence without approval.
A plain GitHub approval without the explicit verdict does not satisfy this
gate. Pending and dismissed reviews do not count. A trusted block or
`CHANGES_REQUESTED` at the same head wins over an allow, whichever trusted
publisher recorded it.
A new head requires fresh review. An operation-bound correction must come from
the original block's publisher and name that exact operation. The publisher
attests that the finding was resolved or withdrawn; another publisher's
correction or an ordinary second opinion cannot clear a same-head block.
The Maintainer App is another publisher of the same record. Its automated
intake retains its own operation journal and uncertainty handling.

## Shared project knowledge

Board threads and library entries use the existing Git-backed content revisions.
The board's replies are retained discussion data, never peer-question or human
reply delivery. Link those existing records and use their existing reply actions.
Document revision is independent of `source_revision`, which identifies the code
a claim discusses. Bodies and historical pages remain outside active state.

The provider-independent CLI (also available through the existing attempt MCP) is:

```sh
factoryctl attempt content search --project ID --query "delivery" --limit 4
factoryctl attempt content body --id DOCUMENT_ID --revision 1 --limit 8192
factoryctl attempt content create --project ID --kind observation --title "Delivery finding" \
  --body-file finding.md --source-references '{"status":"tentative","evidence":["path/to/check"],"source_revision":"FULL_SOURCE_SHA"}'
```

Knowledge kinds are `project_brief`, `decision`, `lesson`, `observation`,
`discussion`, `discussion_reply`, and procedures with structured metadata.
Legacy free-form procedure references remain readable. Metadata accepts `status`
(`tentative`, `current`, `needs_revalidation`, `superseded`), `evidence`, `entities`
(stable project-ID:operational-node-ID references), `source_revision`, `branch`,
`environment`, `thread_id`, `task_id`, `change_id`, `record_type`/`record_id`,
`mentions`, `resolved`, `pinned`, `supersedes`, and `scope` (repository by default,
or project-wide for operator-authored material). Unknown/duplicate fields are
rejected. Decisions and lessons require evidence. Authors come from authenticated
identity; agent notes cannot create project briefs, promote current guidance, or
revise another author's protected knowledge. Operator capability is required for
promotion. Text cannot grant capabilities.

Task preparation freezes a bounded selection of exact document revisions,
prioritizing explicit attachments and the project brief. The normal knowledge
addition is at most 3 KiB, plus at most 4 KiB for all explicitly attached
revision identifiers; bodies are loaded in bounded prefixes or on demand.
Context includes author, a bounded evidence reference and any explicit supersedes
link. A correction claim does not silently rewrite or invalidate earlier evidence;
mark obsolete guidance superseded through its ordinary revision action.
Codex and Claude fetch the actual assignment through `attempt task` when knowledge
is present. Access records distinguish selection, context bytes served, and body
pages served; none claims understanding or application. A later assignment fetch
shows a checkpoint when a selected document has changed. Unknown branch or
environment applicability fails closed for automatic selection; explicit historical
references remain inspectable. Source revalidation compares only linked entity
paths and preserves the author's immutable claim.

The Board and the Library are separate destinations over the same records: the
Board holds threads, replies and a read-only list of direct task-to-task
questions (opened through the existing task conversation, which shows their
delivery state); the Library holds documents, including conclusions saved from
a thread with it as evidence. Posting to the Board never assigns work or
notifies an agent. The browser's `activity` read lists recorded revisions and
supplied/read receipts, newest first, without recording anything. The floor
cues new reads and writes once, briefly. An agent idling in the common break
room gets up, uses the bookshelf or board for a few seconds and sits back down,
as it fetches newly started work from the task tray. An agent at work is never
moved: a read flies from the shelf or board to wherever it is, a write flies
from it to them (one per agent). Nothing walks or flies under reduced motion.
The cue then rests beside the sprite and on the board or shelf. What a run is supplied at launch is
listed but never cued. Flights, marks and the Activity list open the exact
thread or revision, the last for agents that are off-screen or not drawn; a
direct question's pulse opens the Board. The first listing after connecting or
reconnecting is history and is never cued. Browser fixture demonstrations are simulated UI operations; isolated
kernel/daemon/provider-boundary tests establish persistence and delivery. No model
learning is inferred from either test.

## Operator human requests

With the existing operator socket and token-file environment configured,
`factoryctl human list` shows open, delivering, and delivery-unknown requests,
including overseer questions. Answer an open request with:

```sh
factoryctl human reply --operation-id HEX32 --request ID --revision REVISION --reply "Answer"
```

Use the listed request revision and a fresh 32-character hexadecimal operation
ID. Inspect the returned `human_reply.state`; `delivery_unknown` means input
may have been delivered and must not be replayed. Operator credentials are
required; a worker attempt token does not grant this authority.

For CLI supervision, `factoryctl status` includes agent model, reasoning,
standing-instruction policy and counters, and tool-budget counters. Read a
revision-bound task with `factoryctl task read --task ID --revision REVISION
[--offset N]`; follow `next_offset` to page instruction, feedback and outcome.
`factoryctl agent paths --agent ID` returns sampled modified paths relative to
the current Change, not a process working directory.

`factoryctl terminal observe --project ID --task ID --run ID` reads a bounded,
redacted terminal window for a worker or overseer through operator authority,
including retained diagnostics after settlement. Use its cursor and byte-budget options for subsequent windows.
`factoryctl task update --task ID --revision REVISION` supports edits, cancel,
or retry; retry may include reassignment but cannot be combined with text edits.
`factoryctl agent pause|resume|archive|restore --agent ID --revision REVISION`
uses the existing lifecycle guards; restoring an archived worker leaves it paused.
These operator commands require the socket and token-file environment above.

## Finish and clean up

After a confirmed merge, the repository agent that owns the checkout removes its
completed worktree and local branch as part of finishing the task. First verify
that the exact branch tip is reachable from freshly fetched `origin/main`, the
checkout has no tracked or untracked changes, and no active task, review, dev
server or installed build still uses the path. Preserve dirty, unmerged, unknown
or in-use worktrees and report the reason. Age alone is not evidence of disuse. A squash merge may not retain the branch
tip as an ancestor. For that case, verify the merged PR's exact head equals the
local branch tip, its recorded merge commit is reachable from `origin/main`, and
its patch is incorporated. Keep the branch if any proof is missing.

Factory-owned Change worktrees live under the daemon home's `changes`
directory on `factory/<12 hex>` branches. New Changes use their own bare Git
administration under the project's `.git/dark-factory-changes/<Change ID>/.git`;
legacy retained Changes may still use the project's canonical administration.
Use the exact settled source receipt's `git_directory`, not an assumed canonical
branch. Remove one only after the same
proof: its task is terminal and not queued for correction, no run owns it,
its branch tip is merged into freshly fetched `origin/main` (or its squash
merge is verified as above), and the worktree has no uncommitted work. Then
`git --git-dir=<receipt git_directory> worktree remove <changes/ID>` without
force and delete the branch from that same administration. A
retained Change of an open task, a dirty worktree, or a worktree whose
identity you have not verified is preserved and reported. Never `git
worktree prune` a shared repository: it removes every registration whose
directory is gone, other people's included.

Run these commands from a surviving checkout of the same repository, never
from the worktree being removed. Run `git worktree remove <owned-path>` without force. If removal fails, stop and
report any residual files; do not report cleanup complete. For an ancestor merge,
use `git branch -d <owned-branch>`. For the separately verified squash case, use
`git update-ref -d refs/heads/<owned-branch> <verified-head>`: the expected head
makes deletion fail if the branch has moved. Remove that branch's local tracking
configuration with `git config --remove-section branch.<owned-branch>` if present.
Never apply this path to an active branch or a branch with unverified changes.
Keep screenshots and reports in their existing artifact location.
Do not delete retained daemon Changes or build directories used by an installed
runtime. Remote branch deletion follows the existing merge workflow; do not bulk
prune other people's branches. An overseer operating in a private runtime home
reports operator-checkout candidates to its host instead of crossing that boundary.

## Shared local-CI lease

Reusable Go build/module caches, Corepack downloads and the pnpm store live
outside checkouts at `$HOME/Library/Caches/dark-factory/local-ci/trusted`.
`DF_CI_CACHE_ROOT` selects another absolute directory and survives the clean
test environment. Share it only between trusted worktrees; use a separate root
for untrusted code. Factory runtime homes remain separate trust contexts under
their existing filesystem grants. Test homes, generated outputs and XDG
data/state remain local or temporary; `git clean -ffdx` still removes them.

Native workers consume only the trusted `go-mod` subtree from that cache as a
read-only `GOMODCACHE` with `GOPROXY=off`; their Go build cache remains in the
disposable runtime home. The local-CI environment accepts this projection only
when the path is an existing, canonical directory outside the checkout, so an
empty or unsafe shared module cache fails closed instead of falling back to
proxy TLS or operator state.

Actions restores only the reusable directories under `RUNNER_TEMP` using
`actions/cache`, keyed by platform, actual Go version, Node version and dependency
locks. Cache hits never skip checks. GitHub scopes saved caches by ref; a manual
run on `main` with runner `macos-latest` seeds the matching hosted toolchain
cache for subsequent merge-queue refs. The default manual runner remains
`dark-factory-mac`; its Go patch version may differ. Queue caches alone do not
establish reuse across different queue refs.

Process-sensitive checks take one blocking `lockf` lock from the common Git
directory, so linked worktrees cannot stack process-heavy Go runs. A contender
waits; the kernel releases the lock when its holder exits. The routine
`go-check.sh` remains outside that lease.

The supervisor currently runs worker attempts with `VerificationNone`. The
schema recognizes other roles and verification values, but unsupported
combinations fail before provider execution and are not routine gate lanes.

## Isolated daemon check

Build all three sibling binaries. `go run` cannot satisfy the daemon's exact
sibling-binary boundary.

```sh
df_dev_root="$(mktemp -d /private/tmp/df-dev.XXXXXX)"
chmod 700 "$df_dev_root"
go build -o "$df_dev_root/factoryd" ./cmd/factoryd
go build -o "$df_dev_root/factoryctl" ./cmd/factoryctl
go build -o "$df_dev_root/factory-runner" ./cmd/factory-runner

df_dev_home="$df_dev_root/factory"
"$df_dev_root/factoryctl" init --home "$df_dev_home"
"$df_dev_root/factoryctl" doctor --home "$df_dev_home"
"$df_dev_root/factoryd" --home "$df_dev_home" \
  --development-browser-address 127.0.0.1:43999 &

until [ -S "$df_dev_home/runtimes/factory.sock" ]; do sleep 0.2; done
export DARK_FACTORY_SOCKET="$df_dev_home/runtimes/factory.sock"
export DARK_FACTORY_OPERATOR_TOKEN_FILE="$df_dev_home/operator.token"
"$df_dev_root/factoryctl" project create --name dev --root "$PWD"
```

The root is under `/private/tmp` because `/tmp` is a symlink on macOS and the
home walk rejects symlinks. The browser address keeps the check off an
installed factory's `127.0.0.1:43123`. Run `doctor` while the home is stopped. Every
operator request needs both client environment variables.

Lifecycle fixtures use a tiny temporary Git repository and the shell provider.
They must prove the provider receives a daemon-owned linked worktree of that
repository on the Change's own branch, that its commits settle as the Change's
head, and must independently prove descendants and disposable paths are gone. A shell
trap, sleep, `Drop`, broad PID scan, or cleanup performed only by the process a
test kills is not absence proof.

The real disposable launchd check is `scripts/go-service-e2e.sh`. Run it only
when install or service ownership changes; it is not a routine extra gate.

The local CI lease lives entirely in `dark-factory-local-ci` beneath the
repository's canonical Git common directory. Workers receive that subtree
through `DARK_FACTORY_LOCAL_CI_DIRECTORY`; use
`scripts/with-local-ci-lease.sh <focused check>` to share the host gate.
Full `local-ci.sh` and `--release` hold the lease for their complete suite
because repository and release fixtures can also use process state. The
`--runtime` and `--ui` modes acquire it only around their process-sensitive
checks.
If lease preparation is unavailable or refused, source work can still start with
no lease grant and an explicit startup diagnostic; required CI remains blocked.

## Cutover to worktree Changes

Independent Git administration applies only to new Changes. Retained linked
worktrees keep their source, Gitfile, index and refs in the original layout,
including on retry. Legacy canonical Changes are not independent; do not
migrate or prune them as part of installation. Use each exact source receipt's
`git_directory` for review and publication. Provider permissions are unchanged;
independent Git state is not an enforced filesystem boundary.

The worktree runtime changes the SQLite schema (`changes` gains `head_commit`
and loses the manifest facts) and reads every retained Change through Git.
Install it at a settled boundary: turn dispatch off, let every running attempt
reach terminal, then install and restart. The migration keeps every Change
row, base and settled run; no Change gets a head until its Git-free tree is
adopted, which happens on the next reopen (a send-back correction) or
`attempt source` request, in place and with the worker's edits kept as
uncommitted work at the recorded base. An adopted Change's branch is
`factory/<12 hex>` in the project repository; an existing local branch of
that name at another commit refuses the adoption and is the operator's to
resolve. A build from before the migration refuses the newer `user_version`;
the rollback plan for an operator home is a backup taken before install.

To check an older exact host checkout after cutover without changing its source,
run the current helper from that checkout:
`/absolute/current/scripts/with-local-ci-lease.sh /bin/sh ./scripts/local-ci.sh`.
The process body is `./scripts/go-ci-owned.sh`; wrap it with the same helper for
a focused process check.

Native checks use the already-pinned `DARK_FACTORY_FACTORYCTL` binary for only
same-user process birth and group identity because macOS's setuid `/bin/ps`
cannot execute inside the provider sandbox. Host checks retain `/bin/ps`.
Neither path exposes process arguments, credentials, or a new daemon operation.

## Release and installation

Publishing an immutable semver tag whose name matches `VERSION` triggers
`.github/workflows/release.yml`, which builds the three Go commands for Apple
silicon and Intel macOS and
publishes two archives, `SHA256SUMS`, and a Homebrew formula candidate. The
runtime has no updater.

A half-published release is fixed by re-running that tag's Release workflow or
publishing manually with the `release-artifact` tool.

factoryd releases merged commits into itself; see [DEPLOY.md](DEPLOY.md).

The current installer is deliberately fresh and small. `factoryctl service
install` copies the exact sibling `factoryd`, `factory-runner`, and `factoryctl`
binaries, writes its receipt and launchd plist, and loads that job. It does not
download a release, update a different existing installation, migrate another
home, maintain version pointers, or promise rollback. See
[the installation guide](../install.md).
