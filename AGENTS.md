# Agent instructions

This is the repository's canonical agent policy; `CLAUDE.md` points here.
Read [README.md](README.md), [ARCHITECTURE.md](ARCHITECTURE.md), and
[docs/development/WORKFLOW.md](docs/development/WORKFLOW.md) for product and
workflow detail.

Dark Factory is a Darwin-first Go runtime: `factoryd` owns durable work and
provider processes, while `factoryctl` and the loopback web console use the
same local API. It is not a hosted runtime, coding model, or general agent
framework. The shell-provider and real Codex loops are proven; real Claude work
is not.

## Rules

1. Work on a branch in its own worktree, never on `main`. Use
   `./scripts/new-worktree.sh <slug>`; it does not contact a remote.
2. Every PR needs an independent cold adversarial review of its exact head.
   Record ALLOW/BLOCK through the Maintainer App; never merge unreviewed work.
3. Prefer deletion and simplification over patches, duplication, feature flags,
   speculative abstractions, or silent fallbacks.
4. Keep operator actions CLI-first. The web console must use the same local API
   as `factoryctl`; absent-daemon lifecycle/update code belongs in shared Go.
5. Test according to changed risk. Run the small `./scripts/local-ci.sh`
   baseline plus affected-package and focused race/stress tests only when the
   change can affect those boundaries. Load-bearing durability, projection,
   ownership, finalization, and recovery changes need causal tests. Do not run
   unrelated stress. The protected queue classifier selects source gates from
   the whole combined-tree diff and sends unknown paths through the Darwin
   gate. One memory-heavy Go run at a time. A conflict-free merge whose tree
   exactly matches its reviewed, gated parent needs no duplicate post-merge
   gate.
6. Keep commits small and coherent. Update behavior docs and resolve every
   tracked issue the change completes; otherwise record the exact handoff.
7. Never touch `~/.dark-factory`, its launchd job, or a live daemon without a
   separate explicit owner request. Tests use a temporary `DARK_FACTORY_HOME`
   and socket. Real Claude/Codex runs also need separate approval; otherwise use
   the shell provider.
8. Use the shared model policy: Luna for routine workers/reviewers; Sol/xhigh
   only for an explicit high-risk escalation. Do not silently rewrite profiles.
9. Contributor agents use **only** the Maintainer App for GitHub, with the one
   carve-out named below. Every tool names its `owner/name` repository. First
   require `maintainer_status` for the exact repository you intend to act on to
   return that repository — compared case-insensitively, because it answers
   with GitHub's canonical spelling and the caller's may differ — plus a
   positive numeric ID and revision `maintainer-operations-v5`. Fail closed if
   the required typed operation is unavailable.

   The App is expected to be sufficient, and that expectation is the point: an
   agent that reaches around it stops dogfooding the thing being built. Reading
   another agent's work needs no `git fetch` — `observe_ref` names a branch's
   head, `observe_tree` lists what is in a commit, `observe_file` returns one
   file's exact bytes at that commit, and `publish_commit` writes the result
   back. Compare a commit against an ancestor to see what a branch changed —
   `observe_tree` reports a commit's parents so ancestry is walkable, and
   comparing two unrelated commits answers a different question than the one
   you meant. What a difference *means* is the caller's to decide, not this
   service's. If a task seems to need git, the
   missing thing is usually a typed operation; add it rather than route around
   the surface.

   Merges have two explicit paths, never an automatic fallback. Use
   `enqueue_pull_request` where the base branch has a merge queue. Use
   `merge_pull_request_at_head` only where GitHub reports no queue. Its normal
   path requires an active ruleset with up-to-date checks and squash; exact
   rules-read 403 on a private repository instead requires `protected:false`,
   squash enabled, nonempty all-green checks, explicit no-queue reads, and an
   unchanged default base immediately before merge. The operation always binds
   the exact head and a completed Maintainer `ALLOW`. On the protected path it
   also proves the Maintainer App is absent from every active ruleset's
   disclosed bypass list; a missing or hidden list refuses that path. GitHub
   exposes that list only to ruleset writers, so this operation alone mints
   Administration write for the fixed ruleset reads; the broker exposes no
   administration mutation or caller-selected merge method. Classic branch
   protection alone is unsupported.

   The one carve-out is explicit owner authorization, per task, and it must name
   the exact repository and a finite command and target set — refs, PR
   head/base, tags, or fixed workflows. It is not a general grant. It exists
   because one boundary is deliberately unreachable: `publish_commit` refuses
   `.github` itself, `.github/workflows/**`, the three CODEOWNERS locations and
   `.github/dependabot.{yml,yaml}`, since an agent that could rewrite the CI
   gating its own work would be escalating its authority. The rest of `.github`
   — issue and PR templates — is publishable. Native CODEOWNER approval is
   limited to the few files that can rewrite the merge authority; ordinary
   changes use the exact-head Maintainer ALLOW as their repository review.
   Changes to refused paths need the owner to make them at all. Re-read the
   exact remote state after each authorized effect.

   Absent that authorization everything else is closed, **reads included**: no
   `git fetch`, `pull`, `push`, `clone`, no `gh`, no direct GitHub REST or
   GraphQL call, and no mutation of issues, releases, or Actions outside the
   App.

   Regardless of any authorization — an owner approving a task is not an owner
   approving these: never force-push, never delete a ref, never change
   repository settings or repository/organization access, never inspect,
   export, request, or print a credential, and never open, review, or merge a
   pull request outside the App, so that review stays a distinct principal.
   Branch protection and required checks are settings, so an authorization to
   run a named `gh` command never reaches them.
10. Never read/source `.env.txt` directly. Its only agent use is an explicitly
    authorized, independently reviewed fixed command:
    `./scripts/with-cloudflare-env.sh dns status`, `dns publish-app`, or
    `./scripts/bootstrap-maintainer-v2.sh <reviewed-control-plane-tree>`. No
    other Wrangler, Cloudflare-token, deployment, or credential use is implied.
11. Preserve unrelated dirty work and report exactly what passed, failed, was
    skipped, or remains unverified.
12. Do not bump `VERSION`, create a tag, publish, or promote a release without
    the owner's explicit approval for that release. Merge and prove unreleased
    work locally; a pull request does not create a release boundary.

Related repositories are read-only unless the task explicitly includes them:
`~/dark-factory-site` (Next.js/Vercel) and `~/rust-hem-runner` (style reference
only). See [CONTRIBUTING.md](CONTRIBUTING.md) and
[docs/providers.md](docs/providers.md) when extending the system.
