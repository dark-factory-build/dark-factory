# Agent instructions

This file is the canonical agent guidance for this repository. `CLAUDE.md`
just points here; if anything else conflicts, this file wins.

## Project overview

Dark Factory is a Go runtime that turns a software backlog into continuous
agent progress: a durable queue, orchestrator, and process supervisor for
fresh Claude Code and Codex CLI attempts, watched and directed through a web
console served over loopback. One operator runs many agents from one machine;
`factoryd` owns every admitted attempt and external resource through
finalization, so closing a client never stops admitted work. The daemon is
Darwin-only today (Linux is #120/#141-144), the console's remaining daemon
gaps are recorded rather than hidden, and no real Claude or Codex attempt has
been proven end to end yet — the proven loop runs the shell provider.

It is not a hosted web application, not an Electron/Tauri app, not a coding
model, not an agent pretending to be an employee, and not a general agent
framework: the console is a local surface for a local daemon, and every
operator action it takes is the same local-API request `factoryctl` makes. See
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

   The check runs in the merge queue, not on the pull request: recording a
   verdict fires no workflow event, so a pull-request-time check could never
   turn green after the reviewer acted. A change with no verdict enqueues and
   is ejected.

   `ALLOW` means the reviewer is satisfied and is the only verdict that
   clears the check. `REQUEST_CHANGES` blocks it. `COMMENT` decides nothing
   and is for findings mid-review. The verdict binds to `head_sha`: pushing
   a fix moves the head and needs a fresh verdict, which is the point — a
   verdict can never cover code the reviewer did not read. A blocking
   verdict at a head cannot be cleared by a second ALLOW at that same head;
   it is cleared by pushing the fix.

   All three are the App's own words, not GitHub review states. The App
   authors the pull requests it reviews and GitHub refuses a self-review
   that takes a side — `APPROVE` and `REQUEST_CHANGES` alike — so every
   verdict is submitted as a GitHub `COMMENT` and the verdict itself rides
   in a line the App writes and refuses in caller text. That line is what
   the check reads to tell one verdict from another. It is not all it
   reads: an App review at this head whose GitHub state is
   `CHANGES_REQUESTED` blocks on that state alone, ahead of whatever the
   line says — a fail-closed backstop no verdict this App submits can now
   reach, kept so the gate does not depend on that staying true.

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
5. **CLI first.** Every operator action is a `factoryctl` request; the web
   console calls the exact same local-API request path, never a shortcut only
   the console can take. Bootstrap, service-lifecycle, and update actions that
   must work while the daemon is absent live in shared Go library code under
   `internal/install`.
6. **Tests around the load-bearing paths**: queue and attempt durability,
   event projection (durable state → wire events → client model), exact
   resource registration/finalization, Change ownership, and crash/restart
   recovery. A change to any of these needs a causal test that would have
   caught the bug it fixes.
7. **Run `./scripts/local-ci.sh` before finishing.** It is the
   authoritative gate (gofmt, `go vet`, the full serial and race Go
   suites, the TypeScript client proof, the browser, daemon and service
   E2Es, `git diff --check`). It takes no arguments. CI runs the same
   script on every pull request, and the `main` ruleset requires the
   aggregate `required` context that job feeds; a PR that isn't green
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
   at its source. If the authorized broker does not expose the required issue
   operation, record the exact human handoff instead of using operator
   credentials. A doc or issue describing work that no longer exists is worse
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
14. **Use only the provider-neutral Maintainer App for GitHub authority.** The
   sole remote path for contributor agents is the Dark Factory Maintainer MCP
   at `https://maintainer.darkfactory.build/mcp`, exposed as authenticated App
   tools by the host or coordinator. It is not the curated OpenAI/Codex GitHub
   plugin, a provider login, or a future Dark Factory product integration.
   Before the first remote operation in every session, call
   `maintainer_status` and continue only when it binds
   `dark-factory-build/dark-factory` with numeric repository ID `1335380107`
   and permission revision `maintainer-operations-v2`.
   The typed surface verifies authority, observes the exact default head and
   durable operation outcomes, manages bounded issues, publishes exact
   commits and pull requests, records exact-head review verdicts, diagnoses
   and reruns exact-head CI, observes merge results, enqueues through the merge
   queue, publishes and observes immutable releases, and dispatches only the
   fixed reviewed release-recovery and control-plane deployment workflows.
   Use those typed tools only. Direct merge and arbitrary GitHub or workflow
   operations are not exposed. If the MCP or the needed
   typed tool is absent, denied, unavailable, or indeterminate, fail closed
   and report the exact missing authority path; continue with local-only work
   where useful.

   Never substitute `gh`, `gh auth`, authenticated `git fetch`/`pull`/`clone`/
   `push`, a PAT, `GH_TOKEN`/`GITHUB_TOKEN`, SSH-agent state or keys, credential
   helpers, browser sessions, the user's keychain, or any model-provider GitHub
   connector. Do not read or source `.env.txt` directly or inspect or expose
   the Cloudflare Access service-token values. Never put a GitHub, Maintainer,
   provider, or Access credential in a worktree, prompt, command output, or log.
   Host registration must isolate transport credentials from provider, tool,
   and shell process environments; never export the Access pair into a
   coordinator-wide environment.

   The one local credential-file path agents may use is an explicitly
   owner-authorized Cloudflare DNS operation. Run only
   `./scripts/with-cloudflare-env.sh dns status` or
   `./scripts/with-cloudflare-env.sh dns publish-app`; the helper reads
   only `CLOUDFLARE_API_TOKEN` and `CLOUDFLARE_ACCOUNT_ID` from the ignored,
   mode-`0600` `.env.txt` at the common worktree root through an atomic,
   no-symlink file open. Its finite implementation admits no other Cloudflare
   operation. Its public launcher replaces itself with an explicit empty
   environment before starting any setup child, and the compiled operation
   never hands the token to Wrangler, another child process, or process
   arguments. The token
   path may be used only from an independently reviewed commit: the wrapper
   captures one exact commit, refuses mutable helper source or a moving `HEAD`,
   and builds an offline Git export of that commit in an isolated home/cache
   before it reads the credential file. Its link-time wrapper receipt prevents
   accidental direct invocation; it is deliberately not authentication. A
   process already running as the operator can read any mode-`0600` file owned
   by that operator, so this policy and helper are not a same-UID security
   boundary. Isolating a hostile same-UID process requires a separate OS
   identity or credential broker. The token must be scoped to the named
   Cloudflare account/zone operation. Never copy it into another env file or
   export it to tests, providers, GitHub tools, or an agent-wide shell. This
   narrow exception grants no GitHub, Maintainer-App, Cloudflare-Access,
   deployment, or live install authority; each named remote mutation still
   requires its own task authorization.
   Human operators may perform a separately reviewed GitHub action through
   their normal workflow. A deployment the operator explicitly requests
   remains limited to that named operation and should use the repository's
   dispatched workflow; it never authorizes contribution writes outside the
   Maintainer App.

## Adding to the system

- New provider, integration, or theme: see [CONTRIBUTING.md](CONTRIBUTING.md)
  for the shortest path and [docs/providers.md](docs/providers.md) for the
  provider contract.
- Known problems and their smallest fix: [GitHub issues labelled
  `known-issue`](https://github.com/dark-factory-build/dark-factory/issues?q=is%3Aissue+is%3Aopen+label%3Aknown-issue).
- Day-to-day workflow and the implemented release/update process:
  [docs/development/WORKFLOW.md](docs/development/WORKFLOW.md).
