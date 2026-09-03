# Development workflow

`AGENTS.md` is the policy. This file is the short operational runbook for
source changes, isolated runtime checks, repository publication, and releases.

## Protect the live installation

Never point development code or a test at `~/.dark-factory`, its launchd job,
or retained production Changes. Use a temporary home, an explicit socket, a
disposable service label where needed, and an independent cleanup check. A
source change, merge, release, install, provider run, and live-site check are
separate effects.

Real Claude or Codex runs consume the operator's subscription and need separate
approval. Routine development uses the deterministic shell provider.

## Make and review a change

1. Start from a current local `origin/main` through the authorized remote path,
   then create one branch in one worktree. The helper never contacts a remote.

   ```sh
   ./scripts/new-worktree.sh <slug>
   cd .worktrees/<slug>
   ```

2. Make one coherent change. Prefer deleting obsolete behavior and duplicated
   machinery over preserving it behind compatibility code or a feature flag.

3. Run the smallest checks that exercise the changed risk. Process-sensitive
   checks share the repository lease:

   ```sh
   ./scripts/with-local-ci-lease.sh go test ./internal/daemon/
   ```

   Concurrency, process ownership, finalization, or recovery changes need a
   focused `-race` test. SQLite stress is relevant only to SQLite open,
   snapshot, file-binding, dependency, or toolchain changes. A whole-kernel
   race run is exceptional; if a broad kernel change genuinely requires it,
   run it alone with `-timeout 1200s`.

4. Run the routine authoritative gate once on the exact head:

   ```sh
   ./scripts/local-ci.sh
   ```

   It runs repository/release fixtures, Go formatting and vetting, risk-scoped
   short Go suites, the TypeScript client proof, normal browser/daemon
   end-to-end checks, and `git diff --check`. It is macOS-only while the daemon
   is Darwin-only; there is no Linux runtime lane yet. The gate uses the
   isolated Go build and module caches and clears inherited live-factory
   identity variables. One memory-heavy Go run may execute at a time.

5. Commit the exact reviewed tree, publish it, and open a pull request. Record
   what changed, what was deleted, the exact head, checks run, and unverified
   lanes.

6. A reviewer who did not author the change reads the diff cold, tries to break
   correctness, security, and simplification, and returns `ALLOW <sha>` or
   `BLOCK <sha>`. Fix every finding and obtain a new verdict on the new head.

7. Merge only the reviewed head after its required checks pass. Use
   `enqueue_pull_request` where the base has a merge queue. Where GitHub does
   not offer a queue, use only `merge_pull_request_at_head`: it refuses a
   queue-enabled base and requires the default base and exact head, a completed
   Maintainer `ALLOW`, and completed non-failing checks. A protected base needs
   an active strict squash ruleset. Exact rules-read 403 on a private repo
   instead needs `protected:false`, squash enabled, explicit no-queue reads,
   and an unchanged base immediately before merge. On the protected path it
   proves the Maintainer App is absent from every active ruleset's disclosed
   bypass list; a missing or hidden list refuses that path. Legacy classic
   branch protection alone is unsupported. This operation alone mints
   Administration write for fixed ruleset reads because GitHub otherwise hides
   bypass actors; it exposes no administration mutation. It never falls back
   between paths.
   A conflict-free merge whose tree matches the reviewed, gated parent does
   not need another local or post-merge gate.

## Shared local-CI lease

`scripts/local-ci.sh` acquires one kernel-backed lease from the common Git
directory, so linked worktrees cannot stack process-heavy Go runs. Set
`DARK_FACTORY_LOCAL_CI_WAIT=0` to fail instead of waiting. Do not bypass the
lease for process, daemon, browser, service, race, or stress fixtures.

The full lease stress suites are focused checks, not routine gates. Run
`scripts/test-local-ci-lease.sh` and `scripts/test-local-ci-lease-mutations.sh`
when changing the lease helpers, their entry/owner semantics, or the macOS
process primitives they depend on; unrelated source and documentation changes
still exercise the real lease by entering `scripts/local-ci.sh` normally.

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

## GitHub boundary

The normal automation surface is the Maintainer App. Every tool names the
`owner/name` repository it acts on, and the App reaches only repositories it is
installed on. Before a write, require `maintainer_status` for that exact
repository to report:

- the `owner/name` you asked for, compared case-insensitively (it answers
  with GitHub's canonical spelling);
- a positive numeric repository ID; and
- permission revision `maintainer-operations-v5`.

Use only its typed, exact-head operations. Retain a write's operation UUID and
canonical request until the result is reconciled. Never expose App keys,
installation tokens, personal tokens, credential-helper output, or keychain
contents to an agent or worktree.

Close a superseded pull request with `close_pull_request`, naming its current
head SHA. Record the reason first with the existing exact-head `COMMENT`
review when the PR conversation does not already explain the closure.

Reading another agent's branch needs no `git fetch`: `observe_ref` names its
head, `observe_tree` lists what is in a commit, `observe_file` returns one
file's bytes, and `publish_commit` writes the result back. Compare a commit
against an ancestor to see what a branch changed; `observe_tree` reports
parents, and comparing two unrelated commits answers a different question. If a task seems to
need git, the missing thing is usually a typed operation.

Two things still need the owner: a broker that is unavailable, and the paths
`publish_commit` refuses by design — `.github` itself, `.github/workflows/**`,
the three CODEOWNERS locations, and `.github/dependabot.{yml,yaml}`. The rest of
`.github`, such as issue and PR templates, is publishable without authorization.
Native CODEOWNER approval is limited to the few files that can rewrite the
merge authority; ordinary changes use the Maintainer's exact-head ALLOW alone.
For refused paths the owner authorizes a finite host-credential fallback naming
the exact repository and operations; run only those named
`git`/`gh` commands outside the sandbox and re-read the exact remote ref, PR,
check, release, or workflow state after each effect. No force push, ref
deletion, repository-settings or access change, credential inspection, or
implicit expansion is allowed — those hold whatever the owner authorizes for
the task.

Pull-request and merge-queue runs use fresh hosted workers. A manual workflow
dispatch is the explicit path for a platform investigation on the persistent
Mac. There is no automatic duplicate full gate after a successful merge.
Changes to App-refused paths remain owner-authored. Every path still requires
the independent exact-head review and combined-tree queue gate.

The Maintainer App design and operation set are documented in
[GITHUB_APP.md](GITHUB_APP.md).

## Cloudflare credentials

Cloudflare authority is separate from GitHub authority. Never source or print
the ignored common-root `.env.txt`. Exactly three fixed commands may read it.
For a separately authorized DNS operation, use an independently reviewed clean
commit and exactly one finite helper command:

```sh
./scripts/with-cloudflare-env.sh dns status
./scripts/with-cloudflare-env.sh dns publish-app
```

The helper admits only `CLOUDFLARE_API_TOKEN` and
`CLOUDFLARE_ACCOUNT_ID`, clears the surrounding environment, and never writes
the credential into a worktree, prompt, child argv, or log. Authentication does
not authorize any other Cloudflare, Wrangler, deployment, or DNS effect.

Routine control-plane deployment uses the fixed protected GitHub workflow via
the typed Maintainer operation. It runs the standalone `control-plane` gate,
uploads one Worker version, promotes it, verifies `/healthz` and `/readyz`, and
restores the previous version if readiness fails. The public site is a separate
repository and deployment.

The owner-run break-glass path for that deployment is
`./scripts/bootstrap-maintainer-v2.sh <tree>`, the third authorized `.env.txt`
use. Its one mandatory argument is the reviewed `control-plane` tree SHA-1
from `git rev-parse <reviewed-head>:control-plane`, which names the exact
runtime the activation may ship; from the operator machine it performs the
same gated staging, promotion, and readiness verification as the workflow.
The path exists because `dispatch_control_plane_deploy` is itself one of the
repository-observing operations, so any defect in that path disables the App's
ability to deploy its own repair, and the workflow job requires
`github.actor == 'dark-factory-maintainer[bot]'`, so a human `gh workflow run`
cannot substitute. It is not one-time: it shipped exactly that repair on
30 August 2026.

## Release and installation

Release only a reviewed, green default-branch commit whose `VERSION` matches the
immutable semver tag. Publishing the tag triggers `.github/workflows/release.yml`,
which builds the three Go commands for Apple silicon and Intel macOS and
publishes two archives, `SHA256SUMS`, a Homebrew formula candidate, and
`latest.json`. The manifest is release metadata; the runtime has no updater.

Observe the exact tag, source commit, workflow run, release assets, and their
GitHub-reported digests. A failed release may be resumed only by the fixed
recovery workflow bound to that tag and exact default-branch workflow commit.

The current installer is deliberately fresh and small. `factoryctl service
install` copies the exact sibling `factoryd`, `factory-runner`, and `factoryctl`
binaries, writes its receipt and launchd plist, and loads that job. It does not
download a release, update a different existing installation, migrate another
home, maintain version pointers, or promise rollback. See
[the installation guide](../install.md).

## Reporting

Report every command actually run and whether it passed, failed, or was
skipped. Keep local proof, hosted CI, independent review, merge, release,
installation, provider execution, site deployment, and live browser proof
distinct.
