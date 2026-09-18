# Unattended operation

Start with one explicitly selected repository and project. GitHub is the
backlog; the factory owns execution. Installing the runtime alone does not
authorize importing every issue or unlimited agent use.

## Configuration

Copy `scripts/factory-intake.example.json` to a private operator-owned file.
Choose the repository, project and overseer IDs, a ready label, eligible issue
authors, priorities, and a bounded queue size. Issue text is source material,
not authority to change these settings. The host reads GitHub; agents still
publish exclusively through their Maintainer App. No host credentials are
passed in task instructions.

Use preconfigured Luna/Terra workers and a Sol overseer. Set worker capacity,
a standing supervision instruction, and a finite supervision-run allowance.
The overseer may choose among these workers, pause, reorder, stop, and send
work back; it cannot raise model, account, or project limits through its
scoped controls.

Set a finite per-run duration before enabling intake; zero run budget means unlimited:

```sh
factoryctl status
factoryctl project limits --project PROJECT_ID --revision REVISION \
  --run-budget 0 --max-run-seconds 2700
```

The allowance adds this many future admissions to the recorded count; it does
not erase history. Zero disables that ceiling. Duration starts at admission,
includes startup and human waiting, and cancels overdue work through the
normal owned-process cleanup. These are run and wall-clock limits, not token
counts or a monetary spending cap. Legacy tool-budget fields are not provider
usage accounting.

## Standing supervision policy

Combine these rules with the project's acceptance criteria and repository
instructions. Supply product priorities and explicit closure criteria.

- Read all pages of `overseer status`. Preserve direct operator interventions.
  Carry the exact `FACTORY_SOURCE OWNER/REPO#NUMBER` marker into every delegated task,
  so later edits and withdrawals can find all linked work.
- Triage first: verify the problem, reject duplicate or already-fixed work
  with evidence, and prefer deletion or no change when sufficient. Close as
  `not_planned` only within the operator's explicit closure policy. Ambiguous
  product decisions go to Needs You.
- Give workers bounded objectives, acceptance checks, source revisions, and
  allowed files. Prioritise impact and dependencies before arrival order.
  Avoid concurrent edits to the same files. Use the cheapest suitable worker.
- On changed or withdrawn sources, cancel queued work and stop running work
  that no longer applies. Observe those outcomes before delegating a successor.
  Intake is a supervisory event, not an instantaneous GitHub-to-process kill.
- A worker's success is input to review, not issue completion. Inspect its
  tree and receipts, run repository gates and independent exact-head review,
  and send findings back. Never author an ALLOW for your own work.
- Allow at most two repair rounds for the same unresolved failure. A review
  finding returns to the original task/Change and must be independently
  re-reviewed at its corrected head; do not create a retry duplicate.
  Repeated publication failures, missing authority, or unclear requirements
  become one human decision. Do not spawn fresh tasks to evade admission
  limits.
- Reuse the source issue when publishing. Observe stable operation IDs before
  retrying writes. Report merged separately from deployed; acceptance criteria
  that require deployment need a verified host deployment receipt.
- Exit after current supervisory actions. Standing worker events wake the
  next run. Do not consume model turns polling or waiting for another worker.

## Questions

Use `attempt request-human` for actual operator decisions. State what must be
decided, your recommendation and why, and the effect of waiting. Keep longer
investigation detail in the task result.

```sh
factoryctl attempt request-human --idempotency-key HEX32 \
  --question 'This overlaps an existing fix. Close it as a duplicate?' \
  --option 'Close as duplicate with the evidence link' \
  --option 'Keep open for a separate investigation'
```

The first suggestion is recommended, never submitted automatically. The user
can select a suggestion, edit it, or write another answer, then select Answer.
Needs You opens a sole request once; collapsing the row stays respected.
Stop task ends its originating task. Non-shell provider questions yield and
release their lane until the answer; shell questions remain live. Ordinary
terminal prose is not a human request.

## Recovery and completion

Run intake once before scheduling it. Its `--status` exposes durable receipts.
GitHub failure is not an empty backlog. Exact tracked issues are reread, and
withdrawals become reconciliation events. Planned enqueues are recorded before
the daemon call; deterministic IDs and read-only database observations recover
lost responses. Only the daemon API writes factory state.

Failed work is not recreated on every poll. Missing evidence and ambiguous
outcomes remain visible. Stop intake to stop importing work; disable dispatch
to stop future admissions. Existing runs require Stop or their duration limit.
Intake never fast-forwards the project root. Every delegated worker works in
its own linked worktree of the project on its Change branch, made at the
current base. Fresh worker Changes fetch the configured project upstream
through the runtime's owned Git process before selecting one exact commit.
They do not wait for an idle factory or move the registered checkout. Failed
fetches fail source preparation visibly, without using a stale tracking ref.
Retained Changes are not refreshed or rebased; a correction continues on its
exact branch, and integrating it with current main is a separate reviewed
operation. Explicit
local revision policies remain local. See README for `--base-revision`.
If a source supervisor reaches its duration limit while a human decision is
unanswered, intake records `needs_operator_recovery`.
It does not repeat that task. Edit the source issue materially to create a new
supervision event after resolving the decision.

An unattended release is complete only when the configured verification hook
observes the exact merged SHA healthy on the actual target. Never infer this
from worker success, merge, a TCP socket, or an unchanged alias alone.

## Host scheduling and deployment

`scripts/factory-autonomy.py CONFIG --once` runs intake and review.
The separate `--release-only` pass uses `release_configs` paths for exact-default-head releases and enqueues
one idempotent verified-delivery follow-up for the same project's overseer.
After verification, the controller fast-forwards its own source checkout to the
exact released commit so the next tick loads the released scripts. The checkout
must be on the configured release branch, have no tracked edits, and use an
`origin` matching the configured GitHub repository (HTTPS or SSH). Untracked
files are preserved. Fetch or ancestry failures, operator edits, and a different
branch are reported without resetting the checkout or changing the verified
runtime receipt; resolve the reported checkout condition before the next pass.
Use `--plist` to generate a launchd StartInterval job. The generated job uses
absolute script/config paths and the host's tool PATH. Install it only after
the one-shot preflight succeeds. Each config gets a separate launchd label. Controllers for the same factory
serialize each lane through its own host lock. Intake/review can continue while
a release waits for productive runs to drain. Use the controller for scheduled work; direct maintenance
hooks are operator tools.
Each tick writes a mode-0600 `.autonomy.json` health receipt beside the intake
journal, containing component names, finite status codes, and fixed source-refresh refusal details for bounded
automation health diagnostics.
For private repositories, optionally set `review_mirror_root` to an existing
bare mirror at `ROOT/OWNER/REPOSITORY` whose `origin` is the configured HTTPS
GitHub repository. Create it with `git clone --bare https://github.com/OWNER/REPOSITORY ROOT/OWNER/REPOSITORY` so it retains base history. The host fetches only the base and `refs/pull/N/head` into
that mirror after GitHub reports the exact head. For open PRs whose footer has
`Refs #N` or `Closes #N` for a tracked source issue, the existing controller runs
one independent cold review per pass after intake and release checks. A tracked
source is an intake-managed issue, or an overseer tracking issue the App created
(its completed `create_issue` receipt) on a PR the App published (its completed
publication receipt); a footer the pass cannot prove is skipped with the reason
in the pass output, never reviewed. The existing
autonomy job and lock stay occupied during that review (up to its 20-minute
owned-group deadline); the next scheduled intake/release tick waits. This change
removes nested-sandbox failures, not that existing serialization limit. The host
needs the selected Codex/Claude installation and the owner-installed Maintainer
bridge on PATH (or `DARK_FACTORY_MAINTAINER_BRIDGE`). These remain host
capabilities; no bridge credentials or provider SDK permissions are granted to
worker local commands.

The existing `.reviews.json` journal pins the head/base and deterministic App
operation before launching. The reviewer runs inside the existing owned process
group deadline and records its own exact-head verdict through the App. A timeout,
crash, or lost reply remains unresolved: later passes observe the same App
operation instead of starting another model call. Completed allow/block results
wake the configured overseer to enqueue or return findings to the original task.
A blocked review is a completed review, not an infrastructure failure. The host
must reconcile an unresolved launch before explicitly authorizing another; do
not erase the attempted marker to retry an ambiguous submission.

Bind the controller config to the actual `factory_home`, `project_id`,
`overseer_agent_id`, external `journal`, and verified `review_mirror_root`; its
PATH must select the matching installed factoryctl plus provider/bridge tools.
Regenerate and replace the existing launchd job only after a one-shot preflight.
An unloaded job or a config pointing at an older factory is not autonomous proof.
No GitHub credential is stored in config or passed to a task.

A release configuration pins `repository`, `base`, `journal`, `deploy_argv`,
`verify_argv`, `review_verifier`, and `command_timeout` (5–1200 seconds).
All command arrays are trusted operator configuration with absolute executable
paths, never source or agent output. The controller appends the full merge SHA.
The runtime hook prepares before draining and waits for productive runs to finish.
Deployment has no elapsed-time ceiling; `command_timeout` bounds verification and
review commands. Preparation, installation, and runtime probes retain their own
command bounds. Install a second launchd job generated with
`factory-autonomy.py CONFIG --plist --release-only`; the ordinary job handles
intake and review, while this independent job handles release and delivery.
Their separate locks keep a draining release from suppressing intake or review.
The existing release journal lock prevents duplicate deployment. An explicit
operator control change still cancels the owned pause; a stuck run must be
resolved through its existing recovery path, never killed to meet a release clock.
For this repository use Python with `scripts/deploy-runtime.py` and
`scripts/verify-live-runtime.py`. For a non-default factory, include
`"--home", "/absolute/factory-home"` in both arrays before the appended SHA.
For the site use `/bin/sh` with
`scripts/deploy-site.sh` and Python with `scripts/verify-live-site.py`.
`review_verifier` runs `/bin/sh` with `scripts/verify-adversarial-review.sh`.
A probe emits the actually installed `sha` and boolean `healthy`; unavailable
or malformed observations block deployment. It must never echo the requested
SHA without observing the target.

`factory-release.py CONFIG --latest --once` requires a merged PR at the
configured default branch, an independent Maintainer ALLOW at its exact head,
and completed passing checks. It records the plan before executing the fixed
hook, then independently probes the live target. A crash or ambiguous effect
is observed before retrying; blocked receipts require explicit `--retry`.
The runtime hook drains active work with dispatch off, uses the stock reinstall
script, checks all three binaries and browser health, and restores dispatch
only if no later operator change superseded its pause. Each hook starts in an
owned process group; a timeout terminates that group before the receipt is
blocked.

Before deployment the release journal records the actually observed live SHA
and maps every merged factory PR in the bounded ancestor range to its one
App-rendered `Refs #N` or `Closes #N` footer. The App operation marker follows
that footer and is accepted. Missing or ambiguous source links, incomplete
range results, and unexpected live tips block deployment. On the first probe,
an already-current live SHA becomes an explicit baseline with no historical
issue sweep; an older observed SHA is mapped before deployment even when it is
unhealthy. A non-ancestor baseline requires explicit
`allow_nonancestor_baseline: true` and deliberately enqueues no delivery work.

For deployment-required source issues, set `close_on_merge: false` when the
App creates the PR. The host's verified-delivery follow-up lets the overseer
close the issue with evidence after deployment. Without an enabled release
configuration, merged work remains a deployment handoff, not automatic delivery.
