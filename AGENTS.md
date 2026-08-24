# Agent instructions

This file is the canonical agent guidance for this repository. `CLAUDE.md`
just points here; if anything else conflicts, this file wins.

## Project overview

Dark Factory is a pure-Rust, terminal-first runtime that turns a software
backlog into continuous agent progress: a durable queue, orchestrator, and
process supervisor for fresh Claude Code and Codex CLI attempts, watched and
directed through `factory-tui`, a Ratatui board. One operator runs many agents
from one machine; `factoryd` owns every admitted attempt and external resource
through finalization, so closing a client never stops admitted work.

It is not an Electron/Tauri/browser app, not a coding model, not an agent
pretending to be an employee, and not a general agent framework. See
[README.md](README.md) for how it works and [ARCHITECTURE.md](ARCHITECTURE.md)
for the invariants that constrain every change.

## Related repos

Read-only context unless a task explicitly asks you to edit them:

- `~/dark-factory-site`: the Next.js site (Vercel), future home of the
  hosted release manifest (see `docs/development/WORKFLOW.md`).
- `~/rust-hem-runner`: an unrelated project. Its `AGENTS.md`,
  `scripts/new-worktree.sh`, and `docs/development/RELEASE_WORKFLOW.md`
  are useful *style* references for worktree/release process, nothing
  more — don't port its product specifics here.

## Critical rules

1. **Work on a branch in its own worktree, never on `main`.**
   `./scripts/new-worktree.sh <slug>` creates `.worktrees/<slug>` on a new
   branch from the locally available `origin/main` (or `main`). The helper
   never contacts a remote; agents obtain a current base and publish through
   the authorized remote-access path below. Don't commit to `main` directly.
2. **Every PR gets an adversarial review before merge.** A second agent (or
   person) — not the author — reviews the diff trying to break it:
   correctness bugs, missed simplification, security. Steps: (a) author
   opens the PR with what changed and why; (b) reviewer reads the diff cold
   and posts findings as PR comments, explicitly including anything they
   tried to break and couldn't; (c) author addresses each finding or
   explains why not; (d) reviewer re-checks; (e) merge only once the
   reviewer is satisfied. The author never merges their own unreviewed PR.

   The reviewer records the outcome through the maintainer App, so the
   `review` status check can see it:

   ```
   submit_pull_request_review  event: ALLOW | REQUEST_CHANGES | COMMENT
                               head_sha: <the exact commit reviewed>
   ```

   `ALLOW` means the reviewer is satisfied and is the only verdict that
   clears the check. `REQUEST_CHANGES` blocks it. `COMMENT` decides nothing
   and is for findings mid-review. The verdict binds to `head_sha`: pushing
   a fix moves the head and needs a fresh verdict, which is the point — a
   verdict can never cover code the reviewer did not read. A blocking
   verdict at a head cannot be cleared by a second ALLOW at that same head;
   it is cleared by pushing the fix.

   **What this checks and what it does not.** It checks that a verdict
   exists, came from the App, and names this exact commit. It does not judge
   the review: an agent that records `ALLOW` without reading anything
   produces a green check. Nor is it forge-proof against the factory itself
   — the reviewing and authoring agents reach the App through the same
   credential, so this is evidence, not attestation. What it removes is the
   failure where a real review happened and nothing recorded it. Independence
   remains a property of running a fresh agent that did not write the code.
3. **Remove or refactor over patch.** Every change should leave the
   codebase smaller or simpler than it found it, not just working. Delete
   dead code paths instead of leaving them unreachable; collapse
   duplicated logic into one place instead of adding a third copy; no
   speculative abstractions (interfaces/traits with one implementation);
   no feature flags for behavior that should just be decided; no silent
   fallbacks that hide a real failure behind a plausible-looking success.
4. **Simplest implementation over cleverness.** Prefer the boring, obvious
   fix. Maintainability beats a clever one-liner.
5. **CLI first.** Every operator action is a `factoryctl` request;
   `factory-tui` calls the exact same local-API request path, never a shortcut
   only the TUI can take. Bootstrap, service-lifecycle, and update actions that
   must work while the daemon is absent live in shared Rust library code.
6. **Tests around the load-bearing paths**: queue and attempt durability,
   event projection (durable state → wire events → client model), exact
   resource registration/finalization, Change ownership, and crash/restart
   recovery. A change to any of these needs a causal test that would have
   caught the bug it fixes.
7. **Run `./scripts/local-ci.sh` before finishing.** It is the
   authoritative gate (fmt, clippy at `-D warnings`, the full test suite,
   `git diff --check`). CI runs the same script on every pull request as
   the `checks` status the `main` ruleset requires; a PR that isn't green
   locally won't be green there either.
8. **Small, coherent commits.** One logical change per commit; don't bundle
   unrelated cleanup into a feature commit.
9. **Docs and issue state are load-bearing.** A change that alters behavior updates
   `README.md`/`ARCHITECTURE.md`/`docs/` in the same PR, not later, and a
   fix resolves every issue it was meant to solve rather than leaving stale
   work behind. For GitHub, use `Closes #N` in the PR when possible. For an
   external tracker or backlog, update its item to the equivalent
   resolved/closed state and link the change. The source of the issue does not
   matter: after the change lands, verify every tracked item is actually closed
   at its source. A doc or issue describing work that no longer exists is worse
   than no record at all.
10. **Never touch the operator's live install from a task.** `~/.dark-factory`,
    the installed `launchd` job, and the running daemon behind them are the
    owner's real system. Use a temporary `$DARK_FACTORY_HOME` and `--socket`
    for every test or manual check (see `docs/development/WORKFLOW.md`).
11. **Provider runs cost the owner's subscription.** Don't send a real
    prompt to `claude`/`codex` unless the task genuinely requires
    exercising a live session; prefer the `shell` provider or existing test
    fixtures for anything that doesn't need a real model.
12. **Report exactly what passed and failed.** State which commands you
    ran and their outcome; never imply a check passed that you didn't run.

13. **Use the shared model policy.** New routine Codex workers and focused
   reviewers use the Luna default; Sol/xhigh is reserved for an explicit
   high-risk escalation with a durable reason. God remains Sol/xhigh. See
   the project guidance for the one operator-facing policy and CLI examples;
   existing profiles are not silently rewritten.
14. **Keep agent automation out of operator credentials.** Agents never use
   ambient `git fetch`, `git pull`, `git clone`, or `git push`; `gh` or
   `gh auth`; SSH-agent state; credential helpers; or the user's keychain for
   authenticated remote reads or writes. Use an explicitly authorized
   credential broker or App-backed tool surface supplied by the host (the
   current coordinator's connected GitHub App is one example; another harness
   may provide an equivalent). Anonymous public reads are allowed only with
   credential lookup and interactive prompts disabled; a host may instead
   supply a short-lived, repository-scoped read credential for checkout.
   Remote writes fail closed unless the broker/App is available. Operator
   approval never authorizes injecting a personal token into an agent process
   for contribution work; human operators may instead perform a separately
   reviewed GitHub action through their normal workflow. One exception: a
   **deployment** the operator starts explicitly, limited to the single
   operation named in that instruction and expiring with it. It never licenses
   ambient credentials for anything else in the same session, and it is not a
   route for contribution writes — publishing a commit is contribution work and
   stays behind the App. Where a dispatched workflow exists, use it: the
   credential then lives in the platform's secret manager and never enters an
   agent process. Direct use is the fallback when no such workflow exists yet,
   and it is strictly weaker, because an agent's instruction stream carries
   untrusted content — issue bodies, pull request text, review comments,
   webhook payloads — and a held credential can be steered into a use the
   operator never authorized. A human terminal has no such input channel. That
   asymmetry is the reason for this rule, so widen the exception only with a
   durable reason, and say what the exposure is before acting on it. Never put
   credentials in a worktree, prompt, command output, or log. This
   contributor-agent boundary is not a future Dark Factory product GitHub
   integration and does not change a human operator's normal workflow.

   Three narrow carve-outs, each chosen because the credential cannot be
   steered somewhere that matters:

   **Backlog.** `gh issue create`, `comment`, `close`, `reopen`, `list`, and
   `view`, against the repository the task works in, so rule 9's
   close-at-source requirement is executable rather than aspirational. Check
   for an existing item before opening one. `gh issue edit` is excluded:
   #126, #153, #188 and #198 each declare their body an immutable source
   revision whose edit creates a new quarantined revision, so body-write
   authority would let an agent mutate the exact artifact that boundary
   protects. On any issue carrying that contract a comment must also not
   carry scope, decisions, evidence, status, or acceptance criteria.

   **Reads.** Authenticated read-only `gh`: `gh pr view`/`checks`/`diff`,
   `gh run view`/`list`, and `gh api` GET. A read cannot be steered into a
   write, and the anonymous path is not a substitute -- it caps at 60
   requests an hour and returns 403 on Actions logs, so an agent that cannot
   read its own CI failure has to interrupt the operator to be told what it
   already had authority to see.

   **Topic-branch push.** `git push` to any ref except the default branch,
   and `--force-with-lease` only, never a bare `--force`, never a ref the
   agent did not create. Pushing `main` stays closed. This is safe for a
   reason worth stating: the default branch is protected by its ruleset, so
   a pushed branch cannot reach it without review and passing checks, and an
   agent that can push is already executing on the machine -- a branch push
   grants it nothing it could not do locally. Opening or merging a pull
   request is not covered and stays with the App or the operator.

## Adding to the system

- New provider, integration, or theme: see [CONTRIBUTING.md](CONTRIBUTING.md)
  for the shortest path and [docs/providers.md](docs/providers.md) for the
  provider contract.
- Known problems and their smallest fix: [GitHub issues labelled
  `known-issue`](https://github.com/dark-factory-build/dark-factory/issues?q=is%3Aissue+is%3Aopen+label%3Aknown-issue).
- Day-to-day workflow and the implemented release/update process:
  [docs/development/WORKFLOW.md](docs/development/WORKFLOW.md).
