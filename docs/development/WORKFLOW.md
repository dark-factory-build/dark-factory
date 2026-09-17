# Development workflow

This is a reference for local development, checks, releases, and installation.

## Local development

The worktree helper creates a branch and checkout without contacting a remote:

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
implementation, run this check plus focused tests for the changed package.

The full local gate is available when broad local integration proof is needed:

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
./scripts/with-local-ci-lease.sh go test ./internal/daemon/
```

Concurrency, process ownership, finalization, or recovery changes benefit from
a focused `-race` test. SQLite stress is relevant to SQLite open, snapshot,
file-binding, dependency, or toolchain changes. A whole-kernel race run is
exceptional and uses `-timeout 1200s`. One memory-heavy Go run at a time avoids
macOS resource exhaustion.

## Review and merge ordering

Independent exact-head review precedes enqueue. The protected merge queue then
runs required CI on the combined tree before merging. A pending future queue
check is a delivery condition, not by itself a source-review defect. Reviewers
still block concrete defects and false verification claims; neither local test
evidence nor an ALLOW verdict bypasses the protected gates.

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
directory on `factory/<12 hex>` branches and are registered in the project
repository like any other linked worktree. Remove one only after the same
proof: its task is terminal and not queued for correction, no run owns it,
its branch tip is merged into freshly fetched `origin/main` (or its squash
merge is verified as above), and the worktree has no uncommitted work. Then
`git worktree remove <changes/ID>` without force and delete the branch. A
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

Actions restores only the reusable directories under `RUNNER_TEMP` using
`actions/cache`, keyed by platform, actual Go version, Node version and dependency
locks. Cache hits never skip checks. GitHub scopes saved caches by ref; a manual
run on `main` can seed a cache available to subsequent merge-queue refs. Queue
caches alone do not establish reuse across different queue refs.

Process-sensitive checks acquire one kernel-backed lease from the common Git
directory, so linked worktrees cannot stack process-heavy Go runs. The routine
`go-check.sh` remains outside that lease. Set `DARK_FACTORY_LOCAL_CI_WAIT=0`
to fail instead of waiting.

The full lease stress suites are focused checks for changes to the lease
helpers, their entry/owner semantics, or the macOS process primitives they
depend on: `scripts/test-local-ci-lease.sh` and
`scripts/test-local-ci-lease-mutations.sh`.

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
"$df_dev_root/factoryd" --home "$df_dev_home" &

until [ -S "$df_dev_home/runtimes/factory.sock" ]; do sleep 0.2; done
export DARK_FACTORY_SOCKET="$df_dev_home/runtimes/factory.sock"
export DARK_FACTORY_OPERATOR_TOKEN_FILE="$df_dev_home/operator.token"
"$df_dev_root/factoryctl" project create --name dev --root "$PWD"
```

The root is under `/private/tmp` because `/tmp` is a symlink on macOS and the
home walk rejects symlinks. Run `doctor` while the home is stopped. Every
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

When installing this lease layout, drain old CI holders, let their existing
helper clean its lease state, and update active host checkouts before enabling
worker checks. The new helper refuses legacy lease state, then atomically installs a
symlink barrier at the former lock pathname. Old helpers already refuse this
object and cannot create an independent gate. Never grant workers the enclosing
`.git`, and never delete a held lease to complete this cutover.

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
publishes two archives, `SHA256SUMS`, a Homebrew formula candidate, and
`latest.json`. The manifest is release metadata; the runtime has no updater.

The fixed recovery workflow can resume a failed release while remaining bound
to its tag and exact default-branch workflow commit.

If a runtime was installed through an independently verified operator path,
reconcile its receipt without deploying with:
`factory-release.py CONFIG --reconcile --pr N --observed-sha SHA`. This requires
the exact merged PR, current independent review, a healthy live probe for that
SHA, and a complete prior-to-target source range. It writes only the verified
receipt under the release lock; it does not invoke the deployment hook or
change dispatch. Unresolved or running receipts must be settled first.

The current installer is deliberately fresh and small. `factoryctl service
install` copies the exact sibling `factoryd`, `factory-runner`, and `factoryctl`
binaries, writes its receipt and launchd plist, and loads that job. It does not
download a release, update a different existing installation, migrate another
home, maintain version pointers, or promise rollback. See
[the installation guide](../install.md).
