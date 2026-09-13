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

The routine baseline is:

```sh
./scripts/local-ci.sh
```

It covers repository/release fixtures, Go formatting and vetting, risk-scoped
short Go suites, the TypeScript client proof, browser/daemon end-to-end checks,
and `git diff --check`. It is macOS-only while the daemon is Darwin-only.

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

`scripts/local-ci.sh` acquires one kernel-backed lease from the common Git
directory, so linked worktrees cannot stack process-heavy Go runs. Set
`DARK_FACTORY_LOCAL_CI_WAIT=0` to fail instead of waiting.

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
They must prove the provider receives a daemon-owned `.git`-free Change and
must independently prove descendants and disposable paths are gone. A shell
trap, sleep, `Drop`, broad PID scan, or cleanup performed only by the process a
test kills is not absence proof.

The real disposable launchd check is `scripts/go-service-e2e.sh`. Run it only
when install or service ownership changes; it is not a routine extra gate.

## Release and installation

Publishing an immutable semver tag whose name matches `VERSION` triggers
`.github/workflows/release.yml`, which builds the three Go commands for Apple
silicon and Intel macOS and
publishes two archives, `SHA256SUMS`, a Homebrew formula candidate, and
`latest.json`. The manifest is release metadata; the runtime has no updater.

The fixed recovery workflow can resume a failed release while remaining bound
to its tag and exact default-branch workflow commit.

The current installer is deliberately fresh and small. `factoryctl service
install` copies the exact sibling `factoryd`, `factory-runner`, and `factoryctl`
binaries, writes its receipt and launchd plist, and loads that job. It does not
download a release, update a different existing installation, migrate another
home, maintain version pointers, or promise rollback. See
[the installation guide](../install.md).
