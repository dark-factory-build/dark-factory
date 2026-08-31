# Agent instructions

This is the repository's canonical agent policy; `CLAUDE.md` points here.
Read [README.md](README.md), [ARCHITECTURE.md](ARCHITECTURE.md), and
[docs/development/WORKFLOW.md](docs/development/WORKFLOW.md) for product and
workflow detail.

Dark Factory is a Darwin-first Go runtime: `factoryd` owns durable work and
provider processes, while `factoryctl` and the loopback web console use the
same local API. It is not a hosted runtime, coding model, or general agent
framework. The shell-provider loop is proven; real Claude/Codex work is not.

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
   unrelated stress. One memory-heavy Go run at a time. A conflict-free merge
   whose tree exactly matches its reviewed, gated parent needs no duplicate
   post-merge gate.
6. Keep commits small and coherent. Update behavior docs and resolve every
   tracked issue the change completes; otherwise record the exact handoff.
7. Never touch `~/.dark-factory`, its launchd job, or a live daemon without a
   separate explicit owner request. Tests use a temporary `DARK_FACTORY_HOME`
   and socket. Real Claude/Codex runs also need separate approval; otherwise use
   the shell provider.
8. Use the shared model policy: Luna for routine workers/reviewers; Sol/xhigh
   only for an explicit high-risk escalation. Do not silently rewrite profiles.
9. Contributor agents use the Maintainer App for every GitHub *effect*. Every
   tool names its `owner/name` repository. First require `maintainer_status`
   for the exact repository you intend to act on to return that same
   repository, a positive numeric ID, and revision `maintainer-operations-v3`.
   Fail closed if the required typed operation is unavailable.

   Standing authorization, `dark-factory-build/*` only: `git fetch`, and
   `git push` to a non-default branch this agent created, through the existing
   host credential helper. The App brokers effects, not git objects — it can
   tell you a branch's head through `observe_ref` but cannot hand you the
   commits behind it, so integrating another agent's work needs a real fetch.
   Requiring a human for that made the App's own purpose unreachable. Re-read
   the exact remote state after each effect.

   Everything else stays closed: never push to the default branch, never touch
   a ref this agent did not create, never `--force` without
   `--force-with-lease`, and never open, review, or merge a pull request or
   mutate issues, releases, Actions, or repository settings outside the App —
   PR authorship stays App-only so review is a distinct principal. Never
   inspect, export, request, or print a credential.
10. Never read/source `.env.txt` directly. Its only agent use is an explicitly
    authorized, independently reviewed fixed command:
    `./scripts/with-cloudflare-env.sh dns status`, `dns publish-app`, or
    `./scripts/bootstrap-maintainer-v2.sh <reviewed-control-plane-tree>`. No
    other Wrangler, Cloudflare-token, deployment, or credential use is implied.
11. Preserve unrelated dirty work and report exactly what passed, failed, was
    skipped, or remains unverified.

Related repositories are read-only unless the task explicitly includes them:
`~/dark-factory-site` (Next.js/Vercel) and `~/rust-hem-runner` (style reference
only). See [CONTRIBUTING.md](CONTRIBUTING.md) and
[docs/providers.md](docs/providers.md) when extending the system.
