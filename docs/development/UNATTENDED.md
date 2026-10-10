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

Non-shell overseer runs are always cancelled after at most 30 minutes. Specialist
review limits can be adjusted before enabling intake:

```sh
factoryctl status
factoryctl project limits --project PROJECT_ID --revision REVISION \
  --specialist-runs 0 --specialist-open-proposals 0
```

These limits apply only to specialist review scheduling; normal project
admission has no lifetime run or token ceiling.

Provider token usage is retained per project for status and efficiency metrics. Each settled
run adds what it spent, read from the provider's own session log (Claude Code
transcripts and Codex rollouts, for work in that run's directory since its
admitted work finishes. `factoryctl status` reports usage per project. This counts billable
tokens, not money: Codex counts uncached input plus output, while Claude counts
input plus cache creation plus output, excluding cache reads. The shell provider spends none,
and a run adopted by recovery after a daemon restart records none. Legacy
tool-budget fields are not provider usage accounting.

## Standing supervision policy

Combine these rules with the project's acceptance criteria and repository
instructions. Supply product priorities and explicit closure criteria.

- factoryd queues each accepted issue directly as a worker task for any idle
  worker: the issue title, its body, and a `Source:` link with the
  `FACTORY_SOURCE OWNER/REPO#NUMBER` line. It cancels or stops that work when
  the source is withdrawn. The overseer does not triage or delegate intake; it
  acts on the worker's outcome when woken.
- Read all pages of `overseer status`. Preserve direct operator interventions.
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
