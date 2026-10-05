# Unattended operation

Start with one explicitly selected repository and project. GitHub is the
backlog; the factory owns execution. Installing the runtime alone does not
authorize importing every issue or unlimited agent use.

## Configuration

Create a factoryd intake source with `factoryctl intake create`: the GitHub
repository (read through the customer GitHub App connection) or Linear team,
the target repository, overseer, a ready label, trusted issue authors,
priorities, poll interval, and admission limit. factoryd polls each enabled
source when its interval is due; no host job is involved. Issue text is source
material, not authority to change these settings. Agents still publish
exclusively through their Maintainer App. No host credentials are passed in
task instructions.

Use preconfigured Luna/Terra workers and a Sol overseer. Set worker capacity,
a standing supervision instruction, and a finite supervision-run allowance.
The overseer may choose among these workers, pause, reorder, stop, and send
work back; it cannot raise model, account, or project limits through its
scoped controls.

Set a finite per-run duration before enabling intake; zero disables the project
ceiling. Non-shell overseer runs are always cancelled after at most 30 minutes:

```sh
factoryctl status
factoryctl project limits --project PROJECT_ID --revision REVISION \
  --run-budget 0 --max-run-seconds 2700
```

The allowance adds this many future admissions to the recorded count; it does
not erase history. Zero disables that ceiling. Duration starts at admission,
includes startup and human waiting, and cancels overdue work through the
normal owned-process cleanup.

Add `--token-budget N` to the same command to cap provider tokens. Each settled
run adds what it spent, read from the provider's own session log (Claude Code
transcripts and Codex rollouts, for work in that run's directory since its
admission). Once `tokens_used` reaches a nonzero `token_limit`, the project
admits nothing more; running work finishes. Like the run allowance, the budget
is additional to what is already recorded, and zero removes the ceiling.
`factoryctl status` reports both figures per project. This counts tokens, not
money, and input tokens include cached ones. The shell provider spends none,
and a run adopted by recovery after a daemon restart records none. Legacy
tool-budget fields are not provider usage accounting.

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
- A worker's success is not issue completion. Inspect its tree and receipts,
  then publish it. factoryd reviews, enqueues, merges and releases every
  published head and returns findings to the original worker; never run gates,
  create review tasks or record a verdict. Act on a published pull request only
  when an `Escalated:` wake names it.
- Repeated publication failures, missing authority, or unclear requirements
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

Preview a source (`factoryctl intake preview`) before enabling it;
`factoryctl intake list` shows each source's last poll. GitHub failure is not
an empty backlog. Acceptances are durable and idempotent, so a factoryd
restart rescans from the first page without duplicating work, and withdrawals
cancel queued work and stop running work.

Failed work is not recreated on every poll. Missing evidence and ambiguous
outcomes remain visible. Pause the source to stop importing work; disable dispatch
to stop future admissions. Existing runs require Stop or their duration limit.
Intake never fast-forwards the project root. Every delegated worker works in
its own linked worktree of the project on its Change branch, made at the
current base. Fresh worker Changes fetch the configured project upstream
through the runtime's owned Git process before selecting one exact commit.
They do not wait for an idle factory or move the registered checkout. Failed
fetches fail source preparation visibly, without using a stale tracking ref.
Retained Changes are not refreshed or rebased; a correction continues on its
exact branch, and integrating it with current main is a separate reviewed
operation. Explicit local revision policies remain local. See the
[installation guide](../install.md) for `--base-revision` and repository base
settings.

An unattended release is complete only when factoryd records `release:<sha>`
verified for the exact merged SHA. Never infer this
from worker success, merge, a TCP socket, or an unchanged alias alone.

## Host scheduling and deployment

factoryd polls intake and reviews, enqueues, observes and releases published
pull requests itself; no host controller remains. Releases are described in
[DEPLOY.md](DEPLOY.md). The site deploys from dark-factory-site `main` via Vercel's Git
integration.
