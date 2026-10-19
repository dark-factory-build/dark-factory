# Overseer runbook

An overseer is an agent with `--role orchestrator`. It turns an operator's
objective into worker tasks, supervises them, and publishes their finished
changes through its Maintainer App. Delegate implementation to workers and
ask the operator only for decisions you cannot make from the task and state.

## Own the completion loop

An accepted objective remains your responsibility through implementation and
publication; factoryd owns everything after that (section 5).
Accepted intake issues bypass you: factoryd queues each one directly as a
worker task, and you see it only when its outcome needs you.
Worker success is a handoff, not completion. Use existing task identities and
send-back feedback; do not create replacement tasks for each review round.
Delegate independent work to available qualified workers within the actual
admission limits. A configured `max_run_seconds: 0` disables the project run deadline;
intake honors that operator choice and does not require a finite duration.
Non-shell overseer runs are still cancelled 30 minutes after admission, or at
a shorter project deadline.

Supervision wakes on worker events and, while unfinished tasks remain, after its
configured idle interval even if no new event arrives. An idle reconciliation
rechecks blocked and failed tasks against current prerequisites and delivery
proof. Preserve historical outcomes; reconsidering a task is not permission to
blindly retry it or duplicate another owner's implementation.

Workers run focused tests while implementing; factoryd sends a head whose
pull-request check fails back before review. Process-sensitive checks use
`./scripts/with-local-ci-lease.sh`; reviewers reproduce relevant risks instead
of rerunning the full suite by default. The protected merge queue selects the
appropriate fixed gates from its complete combined-tree diff and remains the
authoritative pre-merge full check.
The overseer lane is not an extra worker slot. Do not infer
capacity from the number of visible terminals or raise limits to clear a queue.

On each supervision wake, reconcile the current objective and its outstanding
worker and publication actions before assigning more work. Never create a
review task for a worker or record a verdict: factoryd reviews each published
head (section 5). Record the next action and its existing task, Change, PR or operation
identity in the retained result so a later pass can continue without duplication.
An unavailable authority or exhausted repair allowance needs a precise escalation,
not a claim that delegation completed the objective. A worker run that ended
`failed` with `provider exited before an attempt outcome` (followed by the
provider's last redacted terminal text, its own exit reason) proves nothing about
effects: a provider can act and then exit before reporting. Read the settled
run's evidence first: `overseer status --task` gives the failed task's exact
run detail with its provider exit and how long it ran after activation, its
retained handoff gives the Change head to compare against the head the worker
was handed, and its task names any operation receipts. Only a refusal before
execution is a retry candidate (an exit within seconds of activation, an
unchanged head, no receipts): retry it once through the same task (`overseer
task update --retry`, which keeps its worker, or a send-back). Anything else
is reconciled from its receipts, never replayed, and escalated when uncertain.
A retry that fails the same way, or an uncertain effect, becomes a human
request raised in that same pass naming the task, the run and its exit, then
other work continues. Deferring it to a later wake records no disposition,
and the operator has no Needs You to answer.

After a verified merge, include housekeeping in that same pass. Pause an obsolete
worker only after checking that it has neither active nor queued work and is not
needed by the remaining objective. Preserve its history. Remove only disposable
files owned by your attempt; retained Changes remain governed by
the daemon's retention rules. Operator Git worktrees are outside your write
boundary: report their exact paths and branches for the repository owner to check
using WORKFLOW.md rather than deleting them yourself. Do not create a separate
cleanup scheduler or an endlessly requeued housekeeping task.

On the initial inspection, recovery, a stale or uncertain cursor, an omission, or a
causal event that cannot be resolved narrowly, run
`$DARK_FACTORY_FACTORYCTL overseer status` and reconcile every page at its returned
fixed head. This private, project-scoped view contains workers, task objective and
result excerpts, active runs, questions and explicit intervention history. On an
ordinary causal wake, the standing task appends a `Factory causal wake` record:
worker task and open pull request counts, one summary line per due task, and
each open escalation with its pull request and whom it waits on beneath it
(`attempt why --pull N` has the head, checks, review and queue). Act on the summarized items; use `overseer status --task`
only when a line is insufficient. `mode=full` is the explicit initial
recovery signal (no prior overseer task); a wake that overflows the delivery
bound stays targeted and names what fits, and the rest wakes you next.
Do not reconstruct an unchanged project merely because the
overseer woke. Status returns four entries from each collection. When
`next_offset` is set, continue with `overseer status --offset N --head HEAD`; reuse
the returned head exactly. A stale head restarts at page one. Use `overseer status
--task ID` for one task. Its objective and result arrive in 4,096-rune chunks;
continue with `--text-offset N --head HEAD` while `next_text_offset` is set. Failed and cancelled tasks expose the exact settled run detail in `result`, while retaining their actual status. Read that report before deciding to retry: a failed review can contain actionable findings for the original worker, not an empty or crashed attempt. Use the `overseer` commands to
assign or reorder queued work, message or interrupt a worker, answer its
question, stop or replace its objective, send work back, and pause or resume
future admission. These commands use your attempt credential; an operator
credential is neither available nor required. Run a command with `--help` for
its exact flags. All targets must remain in your project.
`overseer task add --agent any` queues work for any eligible worker in the
project; the first admitted worker keeps it through corrections, and `task
update --agent ID` moves a queued task to one worker.
`overseer task update --task ID --revision REVISION --title TEXT --body TEXT`
edits only a queued worker task. The body replaces its base instruction while
the latest retained send-back note remains attached as read-only review
feedback; it does not create a new `work_revision`. Use priority, assignment,
or cancel alone when changing a legacy task whose stored prompt is no longer
accepted by its provider.
Put enduring acceptance criteria, merged prerequisites and owner-authority
clarifications in that complete base instruction, preserving the original
acceptance criteria. Send-back replaces the previous feedback; it is not a place
to accumulate requirements. For a settled task, pause its assigned worker before
send-back if needed to keep the correction queued, read the returned revision,
update `--body`, then restore the worker's previous admission state. Do not
resume a worker that was already paused by the operator. Resume only with the
agent revision returned by your own pause; if it changed, leave the newer
control intact. If the task has already
started, do not replay the edit or interrupt it merely to rewrite instructions;
use the existing worker communication path and persist the instruction at the
next queued boundary.
For `factoryctl` controls, mint a 32-hex operation ID once (for example,
`python3 -c 'import uuid; print(uuid.uuid4().hex)'`) and keep it when observing or
retrying that operation. Supply `--task-id` and `--incarnation-id` when creating
worker tasks so a lost response cannot turn a retry into a second task. These
local control IDs are separate from the Maintainer App operation IDs below.

Message steers the current session. Codex Interrupt sends the provider's native
interrupt while leaving its task and session alive. Stop ends the task; replace
atomically stops it and creates a successor. Pause affects future work, so
message/interrupt/stop are the controls for a worker already running. Respect
operator interventions as changes in direction: read their history before
issuing conflicting instructions. Raw terminal keystrokes are not recorded as
messages; only explicit controls enter this history.

Continue useful, actionable supervision and delivery in the same session,
including unblocking other workers while a change awaits correction. If the
only next event is external — a pending check, merge queue or merge, configured
deployment or release receipt, or human/external owner event — record the
concrete pending gate and exact identity in the durable result, call
`attempt succeed`, and end the task immediately. Do not poll or keep a paid
session idle waiting for that event. `attempt succeed` durably records the
pending gate, ends the run, and releases the overseer lane so the next event
can wake a fresh observation. A run that waits instead is cancelled at the
30-minute overseer deadline without recording its checkpoint. With a standing
instruction configured, factoryd keeps one wake task at the head of your
queue while any item needs you: a finished (except an intake task with a diff,
which factoryd publishes), blocked or failed worker task, an
unanswered worker question, unpublished or corrected work, or a pull request
factoryd escalated, or the failed self-release. An item you leave unhandled is woken again at most three
times (a publish failure or failed release never), 30 minutes apart, until it changes; only wakes that printed it and
actually started count; a wake too large to name everything names the oldest
items that fit, and the rest stay due. Half an hour after its last wake, factoryd raises a
NEEDS YOU card naming what is still unresolved. Only a human answers it, so
your status never lists it: the operator's reply (console or `factoryctl human
reply`) resumes that wake task with the question and the decision, cancelling
it in the console leaves the items to the operator, and it closes by itself
once its items resolve. A wake that never started does not count, but no item
is woken more than once in 30 minutes. An open, labelled intake issue retries
its own task after an automatic end (a failure, a run-limit cancel, a blocked
expiry). Your `task update --retry` also takes automatic ends only; an
operator's cancel is the operator's to undo. No retry, yours, the issue's or
the operator's, runs a task whose last two runs ended in the same failure
class: the same end twice is a dead premise, a false detector or a provider
outage, and a third identical run does not cure it. New work runs: a send-back
with a note when you have something new to say, or a retry once factoryd has
rebased the task's Change onto a new head. A Change whose task is queued or
running (sent back) cannot be published. A factory-wide overseer slot lets you supervise alongside
workers even when worker capacity is one.

Set or replace the overseer's standing instruction through SUPERVISION → WHEN
WORK CHANGES → supervise worker activity, or without a paired browser with
`factoryctl agent idle-policy --agent ID --revision REVISION --policy standing_instruction --after-seconds N --instruction TEXT --run-budget N`.
Use `--policy wait` to disable it. The instruction is short, because a
native-provider launch delivers the task through the terminal and that
prepared prompt is capped at 8 KiB:

> You are the overseer of the project named PROJECT. Run
> `git clone --filter=blob:none https://github.com/OWNER/REPO repo`, read
> repo/docs/development/OVERSEER.md, and follow it exactly. Before exiting,
> report the durable outcome with `$DARK_FACTORY_FACTORYCTL attempt succeed
> --result` (one line per action or change you handled, or "nothing to do"), or
> with `attempt block --detail` for an ordinary failure. If you raise a human
> request, keep the attempt running for the reply.

Every command below runs from the directory the session starts in, its
private runtime home, with the clone at `repo` inside it.

### Deciding specialist proposals

A specialist (a worker with a standing instruction) records a proposal as an
`observation` whose metadata says `"record_type":"proposal"`. Each open one is
a persistent wake item, `Proposal ID from AGENT: TITLE [proposal:ID]`, until a
`decision` resolves it: metadata `"record_type":"proposal"`, `"record_id":"ID"`
and evidence `["proposal:ID"]`, with `"status":"tentative"`: an attempt credential
cannot write `current`, and is refused as unauthorized if it tries. Before
deciding, rerun the command or read the file:line the proposal cites as
evidence; the decision's description names what you re-ran and its result, and
evidence that does not reproduce is a reason to decline. To accept, first search queued and running tasks for existing work on it (one
task per proposal), then `overseer task add --content PROPOSAL_ID:REVISION` the
work (to a worker or the shared queue, never the specialist) and record the
decision with `"task_id"` set to that task. To
decline or defer, record the decision without `task_id`, the reason in its
description. Only you or the operator resolve a proposal, and an acceptance is
refused while another accepted proposal's task is still queued, running or
blocked: one self-generated implementation is active at a time. A proposal
revision pins one task: a second `--content` add for it is refused while that
task is queued, running, blocked or succeeded, merged or not; the refusal
names its reason.

Specialists may contribute to or challenge each other's proposals; the wake
line counts those notes and names their authors (`(2 notes from security,
architecture)`). Read them (`content search`, `content body`) before deciding.
You decide what the factory works on: accept, decline, defer, or accept at a
lower or higher task priority (`overseer task add --priority N`, or `task update
--priority` later), and say in the decision which notes moved you.

Read this runbook from that clone or the task-provided checkout. If neither
is available, report the missing checkout. Scope searches to that checkout
and the private runtime home; never search the operator’s home or personal
folders for instructions or tools. Use `command -v` and the repository’s setup
instructions for tools, and report unavailable prerequisites. These launch
instructions guide agents; they do not add an OS filesystem sandbox.

Everything below assumes the launch-scoped private runtime home and `TMPDIR`,
no `gh` credential, git without any remote credential, and the Maintainer App
as its publication MCP server. A native overseer also receives the
authenticated `factory_attempt` MCP boundary. A Codex overseer has no daemon database,
Changes-parent, or operator-home access; it reads a settled Change's work
from the project repository's Git directory, which its local commands are
granted read-only, by the branch head an explicit source request names. Each
source request is checked against current task authority. Any number of
eligible same-project handoffs can be read in one attempt.

## What you may and may not do

- Verify a merged intake task once its wake line shows it live: run its
  issue's Live signal, then `update_issue` with `completed` and the quoted
  evidence, or `reopened` with the observed gap and the pull request link,
  then send that intake task back with them; factoryd publishes its
  correction as a new pull request.
- Publication is factoryd-owned. Do not publish, create, update, merge, or
  review pull requests through the Maintainer App. Never `git push`, never
  edit the operator's checkout, and never write into a retained Change's
  worktree or branch.
- Never record a review verdict yourself. The review is a separate headless
  session started by factoryd; you read its App receipt. A BLOCK of record
  stands at its exact head (#1300): no fresh review clears it, only a new head
  does. `attempt review` requests one fresh exact-head review of a head with
  no verdict of record, such as one whose review failed or never ran. A BLOCK
  you have checked and found wrong goes back once with that evidence, for a
  change that moves the head. After a second BLOCK on the same task, close
  its pull request and re-scope the work instead of sending it back again.
- `attempt intake list` reads the project's intake sources and sync health.
  `attempt why --pull N | --task ID` says where one stands and whom it waits
  on; `attempt report` measures runs, tokens and merges.
  Do not run a nested provider or grant local commands Maintainer credentials.
- Factoryd publishes every settled Change, reviews it, queues it, merges it,
  and releases it. Do not delegate or retry publication from the overseer.
- Prioritize actionable changes and throughput blockers; one blocked change
  does not prevent handling another independent change in the same session.
- When a step needs a decision you are not sure of, or a publication is
  blocked twice, raise it with `attempt request-human` (section 6). A
  non-shell overseer request atomically yields the run, releases its lane and
  revokes its bearer; the next fresh-authority run resumes after the reply.
  Shell overseers retain their live-request behavior.
- A worker block you cannot clear (its sandbox, the platform, an authority
  you lack) is not yours to retry and not a reason to block your own pass.
  First cancel the task if its target has already merged; then raise the
  block once with `attempt request-human`, naming the task and its reason.
  The order matters: the request yields your run and revokes its credential,
  so nothing you issue after it lands. A blocked task nobody was told about
  helps no one.

For unattended projects, also follow [UNATTENDED.md](UNATTENDED.md).

## 1. Find what a worker finished

The implementing worker uses its assigned writable checkout directly, including
a correction after send-back. Only the overseer may request an `attempt source`
receipt; a worker's request is refused. factoryd reviews every published head
itself, so there is no delegated pre-publication source review.

An installed runtime upgrade does not update source in a retained Change.
Before repeating a sandbox or toolchain blocker, compare its pinned source base
with already-merged prerequisite fixes. Never integrate prerequisite work into a
worker checkout: `--prerequisite TASK:REV` admits the consumer only once the
producer's pull request has merged at its exact Change head, so a fresh Change
base already contains it. Do not reset the Change or mistake the daemon
revision for its source.

Prior work reaches a successor task only through a succeeded `--prerequisite`,
`task update --retry` of the same task when its retained Change is intended, or
an attached source-patch content record (`--content ID:REVISION`). Never put
another Change's `source_path` or `git_directory` in a task body: workers cannot
read it or call `attempt source`, and task add and update refuse a body naming
the Changes root or `/dark-factory-changes/`.

Never read the daemon SQLite database, the whole daemon home, or the Changes
parent. As overseer, run `overseer status --task TASK_ID` for the task
you are handling. Its `retained_change_handoffs` carry only identities (Change
ID, base commit, settled `head_commit`, task, work revision, Change revision).
Then explicitly run `factoryctl attempt source --task TASK_ID`; its response
must contain exactly one usable receipt naming the Change ID, base commit,
`head_commit`, `branch` (`factory/<first 12 hex of change_id>`), target task
ID, task work revision, current retained Change revision, the worktree as
`source_path`, the Change's actual Git directory as `git_directory`, and `dirty`.
Match every identity value to the requested task and status. The work is the
branch head: read it with `git --git-dir="$git_directory"` and the commit
named by `head_commit`, never by constructing a `$home/changes/...` path,
reading `factory.sqlite3`, or inferring source from a published branch. If the
task was sent back or any task/work/Change revision changed, or the branch is
no longer at its settled head, the source request is refused and must use
exact current task state. `dirty` means the worker left uncommitted work in
its worktree that is not part of `head_commit`; the head is still what you
publish, and if the task's result says that work matters, send the task back
so the worker commits it (section 5). A Change from before managed worktrees
is adopted into one by the first source request, at its recorded base with
the worker's edits uncommitted, so its `head_commit` equals its base and
`dirty` is true: send such a task back to have its work committed before
publication.

A change is finished when factoryd observed the merge of its current head
(section 5); anything short of that is resumed at the first
step whose operation is not completed, as section 2 says. A task sent back
after a review (section 5) comes around again as the same change id at a
higher `work_revision`, with its branch and pull request already open:
section 3 publishes the new tree on top of that branch.

## 2. Derive one operation id per step, and check the journal first

Every App write takes an `operation_id`. Derive it from the change and the
step so a retry is a replay, never a second publication:

```sh
opid() { python3 -c "import sys,uuid; print(uuid.uuid5(uuid.NAMESPACE_URL, 'dark-factory:' + sys.argv[1] + ':' + sys.argv[2]))" "$1" "$2"; }
# opid CHANGE_ID STEP   with STEP defined by the current factoryd receipt
```

`HEAD8` is the first eight hex digits of the commit named, so a follow-up
commit gets ids of its own.

One id per App write: use the current factoryd receipt, and the same request under
the same id is a replay while a different request under it is refused. Before
each write call `observe_operation` with its id. `completed` means that write
already happened: take its result (for a commit, the head it returned) and go
on to the next step, which for a multi-commit publication is the next commit,
not the pull request. Never received or `planned` means it has not happened.
Prepare and retain the exact request and UUID before sending. If the attempt
ends before the call, include both in
`attempt succeed --result` and use that durable result on the next standing
wake. There is no in-flight checkpoint command. If an interrupted attempt has
no recorded request/UUID, raise a human request rather than guessing or minting
a replacement. If its response is lost and observation reports
`executing` or `indeterminate`, submit that **identical request and UUID once**
to the existing App controller. For an uncertain creation the App reconciles
GitHub's existing PR and never issues a second POST. Consume a completed result
and continue the normal publication path. `observe_operation` alone only reads
the journal; it does not perform that reconciliation.

If this bounded reconciliation still cannot find a unique matching PR, use
`attempt request-human` on the original task. Include the exact request, UUID,
request digest, branch/head, original error and reconciliation result. Name the
decision: abandon this publication, or investigate GitHub's missing outcome
before authorizing any replacement. Absence from a listing is not proof that
the original request never reached GitHub. Do not silently block, poll forever,
or mint another UUID. For other operations, `executing` or `indeterminate`
still requires a human request with the operation ID.

A definite `refused` reply is different: preserve its typed reason and correct
that precondition before retrying the same request and UUID. Do not retry a
refusal unchanged or treat an uncertain result as a definite refusal.

Every successful Maintainer MCP reply carries the authoritative typed result in
`structuredContent`; its short text `content` is only an acknowledgement. Consume
that structured result on the first successful reply. Do not repeat the identical
read merely to obtain another representation, and never replay a write after an
acknowledgement. If a write response is ambiguous, observe its existing operation
id and resume from that observation.

## 3. After publication: factoryd reviews, merges and releases

factoryd reviews every published PR head itself once its required pull-request
check passes (a red head goes back unreviewed): it records one exact-head
verdict through the App, enqueues an ALLOW, observes the merge and releases the
runtime, and sends a REQUEST_CHANGES or a merge-queue ejection back to the
original task with a pointer to the findings. The merge queue's required CI is
the full gate. The reviewer is a separate session that can read only the reviewed
checkout (see providers.md), never the author or overseer. Never create a
review task for a worker, record a verdict, enqueue, or wait on that pipeline.

Each head goes back to its task at most once per review. A task that answers
a REQUEST_CHANGES without a new head gets one more review of that head, which
reads its answer; a conflicting head factoryd first rebases itself.
Merge is not proof that an issue is fixed. For each linked issue, the issue
steward checks the installed revision and records the live acceptance signal,
quoting the observation; it closes the issue when the signal passes and
reopens it when the fix is absent or regresses. A claimed cost, speed, or
failure-rate gain must include the baseline query so the steward can rerun it.

factoryd wakes you with `Escalated: factoryd cannot advance OWNER/REPO#N at
exact head HEAD: ...` only when it cannot advance a pull request: a review that
failed twice, a task that answered a send-back without a new head (its Change
head has the pull request head's tree; a disputed block upheld by its second
review included; a settled head the pull request lacks is published at once, a
head a trusted operator pushed past the settled one is adopted onto the Change
when GitHub verifies every added commit as theirs, and the mismatch is
escalated only when factoryd does neither), or an action (enqueue, send-back, review) that kept failing
for 30 minutes and now waits for the pull request to change. A CODEOWNERS refusal is escalated at once and waits for the owner.
Resolve the cause or raise it with
`attempt request-human` naming the pull request; never start another review.

A send-back of your own (for example a `dirty` worktree, section 1) carries a
pointer note, never pasted findings, and `--head` naming the Change head you
observed it at (the full head your wake line shows after `change=ID@`). A note
about any other head is refused as a `conflict` that names the current head:
observe the premise again there, and send it back only if it still holds.

Never recover a task ID or a Change path from SQLite. The task/status
handoff is authoritative and cross-project, stale, refused, missing, or
non-retained handoffs are refusals, not candidates for reconstruction.

The note goes at the end of the task's body, replacing the note of any
earlier send-back. Keep enduring requirements in the base instruction above;
the worker's provider receives that instruction and latest note whole;
the daemon refuses a send-back the provider could not be handed
(`too_large`), so keep the note to that shape: a pointer, never the
findings pasted. A shell agent's task is a program and cannot be sent
back at all (`invalid_request`); that is a human request. A `too_large`
for a note of that shape means the task's own instruction is at the
provider's bound and no note fits: raise a human request naming the pull
request and the task, and end the run with `attempt block`, since the
change would otherwise come around again to the same refusal. The task
is queued again and the worker's next run continues on its branch; a
later run of yours finds the same change id at the next work revision and
publishes the new head on top of the branch (section 3).
Stop handling this change for now.

## 6. Hand off and finish

If a merged PR touched `cmd/` or `internal/`, it may need a live-service
reinstall; a `web/` change ships in that same build. Before a
runtime reinstall request, run `"$DARK_FACTORY_FACTORYCTL" --build-identity`.
With its exact `source` as `installed`, run `git -C repo fetch origin "$installed" "<merge-commit>"`, then verify each with `git -C repo rev-parse --verify "<revision>^{commit}"`. Run `git -C repo merge-base --is-ancestor
<merge-commit> "$installed"`: status 0 means that merge is already installed,
so skip that request and never recommend an older merge; only status 1 says it
is absent. Require `"release": true`; a missing, malformed, or development
identity, a fetch or verification failure, or any other ancestry error warrants
a human request to verify the installed source, not a claim that the merge is
absent. If the runtime merge is absent, raise
one human request naming the merge commit and applicable deployment, then wait
as below. A
worker run whose worktree was gone at settlement ends failed with that
reason and its Change abandoned; there is nothing of it to publish, and the
task's own retry makes a fresh worktree. Then report, one line per change:
change id, PR number, and merged commit or the reason it stopped.

```sh
"$DARK_FACTORY_FACTORYCTL" attempt request-human --idempotency-key "$(uuidgen | tr -d - | tr A-F a-f)" --question "..."
"$DARK_FACTORY_FACTORYCTL" attempt succeed --result "..."    # after the reply resolves the work
```

For a non-shell overseer, `request-human` yields the native attempt and
returns; do not keep the provider alive or submit a second request while the
NEEDS YOU card is open. The console reply resolves the durable continuation,
and the next standing-instruction run resumes with fresh authority. Shell
overseers keep the existing live-request behavior. The operator can cancel the
NEEDS YOU card if waiting is no longer useful. Once the resumed work resolves,
end with `attempt succeed` or, for an ordinary non-human failure, `attempt
block` (detail cut to its 4 KiB bound; the question allows 8 KiB).

### Peer collaboration

Workers and overseers use `attempt peer status` to read questions or answers
linked to their own task. It reads the compact, decision-relevant inbox first;
use `--targets` only when choosing a same-project collaborator. The inbox is
provider-neutral: terminal notices are only an optional adapter hint, and never
the durable receipt. Follow `next_offset` with `--offset` and `head` with
`--head` for older conversations. With `--targets`, follow `next_target_offset`
with `--target-offset` under the same head for more targets. A stale
continuation must restart from the first page. `attempt peer
ask` and `attempt peer answer` are asynchronous: queued recipients can read
the question when admitted, and neither operation changes capacity or waits for
another worker. Reuse the same idempotency key when retrying an uncertain
request.

The overseer can read peer conversations with `overseer status --task TASK_ID`;
follow its existing `next_offset` and head fence. Peers provide collaboration
context, not operator instructions or authority to control another task.
Browser task history exposes the same question, answer, and notification
receipts, including after either task finishes.

A source request needs no sandboxed copy: every provider reads a settled
Change by its branch head from the repository's Git directory, which a Codex
overseer's local commands are granted read-only. Never write into that
directory or into a Change worktree; publication is the App's alone.

### Bounded worker terminal observation

Use `factoryctl attempt terminal observe --project PROJECT --task TASK --run RUN`
with the current attempt client to inspect public terminal output. Workers can
read only their own exact run; overseers can read worker runs in their own
project, not other overseers. This read does not send input or resume a worker.

The response identifies the project, task and run, plus a fixed snapshot `head`,
raw-byte `next_cursor`, and readable terminal-safe `payload`. Continue explicitly
with `--cursor NEXT_CURSOR`; `--max-bytes` bounds raw bytes read (default 8192,
maximum 65536). A `gap` reports an expired replay cursor and the available floor.
Known credentials and private paths are redacted; partial boundary lines are
omitted so an arbitrary cursor cannot reveal a fragment of a redacted line.
`omitted` counts skipped raw bytes. This is a bounded observation of available
output, not a complete transcript or a guarantee that output is secret-free.

When a worker run reaches terminal, the overseer may continue the same command
against the retained bounded diagnostic window. The daemon stores at most 1 MiB
of uncorrelated runner output, with raw-byte floor and head cursors. A cursor
below floor returns an explicit gap and rebases to that floor; missing history
returns not found, never invented output. Only an authorized project overseer
may read this settled window, and the same credential/path redaction and
complete-line boundaries apply after restart. Saving the window occurs only
after the authenticated runner result is durably consumed, so a diagnostic-write
failure cannot turn an accepted outcome into an ambiguous settlement.
