# Overseer runbook

An overseer is an agent with `--role orchestrator`. It turns an operator's
objective into worker tasks, supervises them, and publishes their finished
changes through its Maintainer App. Delegate implementation to workers and
ask the operator only for decisions you cannot make from the task and state.

## Own the completion loop

An accepted objective remains your responsibility through implementation,
independent review, returned fixes, required checks and the observed merge.
Worker success is a handoff, not completion. Use existing task identities and
send-back feedback; do not create replacement tasks for each review round.
Delegate independent work to available qualified workers within the actual
admission limits. A configured `max_run_seconds: 0` disables the run deadline;
intake honors that operator choice and does not require a finite duration.
The overseer lane is not an extra worker slot. Do not infer
capacity from the number of visible terminals or raise limits to clear a queue.

On each supervision wake, reconcile the current objective and its outstanding
worker/review/publication actions before assigning more work. Obtain the
independent exact-head review described below; never substitute your own verdict.
Return actionable findings to the responsible worker and review its resulting
head again. Record the next action and its existing task, Change, PR or operation
identity in the retained result so a later pass can continue without duplication.
An unavailable authority or exhausted repair allowance needs a precise escalation,
not a claim that delegation completed the objective.

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
ordinary causal wake, the standing task appends a `Factory causal wake` record
with affected worker task IDs and, after the first run, the prior overseer task
identity. Read that prior task first, then each named worker task, with
`overseer status --task ID` and no `--head`: admission itself appends journal
events, so an enqueue-time head is not a valid read fence. Retain the first
returned current head for related text/history reads. `mode=full` is the
explicit initial/pruned-cursor recovery signal. Do not reconstruct an unchanged
project merely because the overseer woke. Status returns four entries from each collection. When
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
including unblocking other workers while a change awaits correction. When no
actionable work remains, report a concise durable checkpoint and exit; do not
poll or keep a paid session idle waiting for a worker. With a standing instruction
configured, worker completion, questions, and explicit interventions wake you
again. Events received while you are queued or running
remain pending for the next supervision task. A factory-wide overseer slot lets
you supervise alongside workers even when worker capacity is one.

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

Read this runbook from that clone or the task-provided checkout. If neither
is available, report the missing checkout. Scope searches to that checkout
and the private runtime home; never search the operator’s home or personal
folders for instructions or tools. Use `command -v` and the repository’s setup
instructions for tools, and report unavailable prerequisites. These launch
instructions guide agents; they do not add an OS filesystem sandbox.

Everything below assumes the launch-scoped private runtime home and `TMPDIR`,
no `gh` credential, git without any remote credential, and the Maintainer App
as the one MCP server (`maintainer`). A Codex overseer has no daemon database,
Changes-parent, or operator-home access; it reads a settled Change's work
from the project repository's Git directory, which its local commands are
granted read-only, by the branch head an explicit source request names. Each
source request is checked against current task authority. Any number of
eligible same-project handoffs can be read in one attempt.

## What you may and may not do

- Publish only through the App: `publish_commit`, `create_issue`,
  `create_pull_request`, `update_pull_request_body`, `enqueue_pull_request`,
  and the observe tools. Never
  `git push`, never edit the operator's checkout, never write into a retained
  Change's worktree or branch.
- Never record a review verdict yourself. The review is a separate headless
  session started by the host review controller; you read its App receipt.
  Do not run a nested provider or grant local commands Maintainer credentials.
- Prioritize actionable changes and throughput blockers; one blocked change
  does not prevent handling another independent change in the same session.
- A review that asks for changes is not a decision for a human: send the task
  back to its worker with the findings (`attempt send-back`, section 5) and
  the worker's next run continues from the tree it left.
- When a step needs a decision you are not sure of, or a publication is
  blocked twice, raise it with `attempt request-human` and keep the native
  attempt alive for the reply (section 6). The request is the NEEDS YOU card
  on the operator's console while the run lives.

For unattended projects, also follow [UNATTENDED.md](UNATTENDED.md).

## 1. Find what a worker finished

The implementing worker uses its assigned writable checkout directly, including
a correction after send-back. Do not require that worker to obtain an
`attempt source` receipt for its own reopened Change: the retained source
operation is for settled review targets. A reviewer must still follow the exact
receipt procedure below; this distinction grants no access to other Changes.

An installed runtime upgrade does not update source in a retained Change.
Before repeating a sandbox or toolchain blocker, compare its pinned source base
with already-merged prerequisite fixes. Integrate an exact reviewed prerequisite
into the assigned writable checkout when required, preserving current edits and
source lineage; publication against current main must exclude changes already
merged. Do not reset the Change or mistake the daemon revision for its source.

Never read the daemon SQLite database, the whole daemon home, or the Changes
parent. As overseer, run `overseer status --task TASK_ID` for the task
you are handling. Its `retained_change_handoffs` carry only identities (Change
ID, base commit, settled `head_commit`, task, work revision, Change revision).
Then explicitly run `factoryctl attempt source --task TASK_ID`; its response
must contain exactly one usable receipt naming the Change ID, base commit,
`head_commit`, `branch` (`factory/<first 12 hex of change_id>`), target task
ID, task work revision, current retained Change revision, the worktree as
`source_path`, the repository's Git directory as `git_directory`, and `dirty`.
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

An independently delegated Codex reviewer uses the same explicit source
request and reads the same head from `git_directory`. It must match Change
ID, base commit, head commit, target task ID, task work revision and Change
revision first; the local branch is evidence of the worker Change, not the
published pull request head, which the cold review reads on its own. A
reviewer is not an overseer and cannot use `overseer status` to discover a
Change. No receipt or a changed identity is a refusal, not a candidate for
path reconstruction.

A change is finished when its `enqueue-HEAD8`
operation (step 5) for its current head is `completed` in the App journal
and the merge was observed; anything short of that is resumed at the first
step whose operation is not completed, as section 2 says. A task sent back
after a review (section 5) comes around again as the same change id at a
higher `work_revision`, with its branch and pull request already open:
section 3 publishes the new tree on top of that branch.

## 2. Derive one operation id per step, and check the journal first

Every App write takes an `operation_id`. Derive it from the change and the
step so a retry is a replay, never a second publication:

```sh
opid() { python3 -c "import sys,uuid; print(uuid.uuid5(uuid.NAMESPACE_URL, 'dark-factory:' + sys.argv[1] + ':' + sys.argv[2]))" "$1" "$2"; }
# opid CHANGE_ID STEP   with STEP one of:
#   issue, pr                                  once per change
#   publish-1, publish-2, ...                  the first publication, one per commit
#   publish-HEAD8-1, publish-HEAD8-2, ...      a follow-up on top of branch head HEAD8
#   body-HEAD8                                  replacement body for pull request head HEAD8
#   review-HEAD8, review-HEAD8-2               the review of pull request head HEAD8
#   enqueue-HEAD8                              the enqueue of pull request head HEAD8
```

`HEAD8` is the first eight hex digits of the commit named, so a follow-up
commit, its review and its enqueue each get ids of their own.

One id per App write: a change that needs several commits (step 3) uses
`publish-1`, `publish-2` and so on, one per commit, and the same request under
the same id is a replay while a different request under it is refused. Before
each write call `observe_operation` with its id. `completed` means that write
already happened: take its result (for a commit, the head it returned) and go
on to the next step, which for a multi-commit publication is the next commit,
not the pull request. Never received or `planned` means it has not happened.
`executing` or `indeterminate` means stop and raise a human request with the
id.

Every successful Maintainer MCP reply carries the authoritative typed result in
`structuredContent`; its short text `content` is only an acknowledgement. Consume
that structured result on the first successful reply. Do not repeat the identical
read merely to obtain another representation, and never replay a write after an
acknowledgement. If a write response is ambiguous, observe its existing operation
id and resume from that observation.

## 3. Publish the change as a branch

The branch is `factory/<first 12 hex of change_id>`. The task's
`work_revision` from section 1 says which publication this is:

- `1`: the first publication. The commit goes on top of `base_commit`, under
  `publish-1`, `publish-2`, and so on. If the branch already exists, an
  earlier run of yours stopped partway: resume at the first `publish-N` the
  journal lacks (its `expected_head_sha` is the head the last completed one
  returned), then at the issue and the pull request, as section 2 says.
- above `1`: the task was sent back and the worker continued on the branch
  it left. `observe_ref` for the branch; it exists, at `branch_head`, and the
  commit goes on top of it under `publish-<HEAD8 of branch_head>-1`, with the
  diff against that head, not the base. A branch that does not exist here
  means the earlier publication never happened: treat it as the first.

Set `from` to `base_commit` or `branch_head` accordingly, and set
`branch_exists=1` when the publication branch exists or `0` when it does not.
The published
`branch_head` is an App-authored commit that is not in the local repository;
fetch it and the worker's head into your clone,
`git -C repo fetch -q origin main "$from"` and then
`git -C repo fetch -q "$git_directory" "$head_commit"`, and run the commands
below against `repo/.git` instead of `$git_directory`.

A worker that integrated a merged prerequisite has main in its head's
ancestry. Copying that head's files onto `from` reproduces the tree but not
the ancestry, so GitHub merges main's own hunks against main again and
reports a conflict that no source correction can clear. Let the script decide
what the publication's parents are:

```sh
set -- $(repo/scripts/publication-parents.sh repo/.git "$from" "$head_commit" "$(git -C repo rev-parse origin/main)" "$branch_exists")
from=$1 diff_from=$2 merge_parent=$3
```

`diff_from` replaces `from` in every diff below. When the branch does not
exist yet, `from` becomes the integrated main commit itself, the valid current
base. When it exists, `merge_parent` names the integrated commit and the first
`publish_commit` of this publication carries it as `merge_parent_sha`: the
published commit is then the merge the worker made, with the branch head first
and its changes applied to the integrated tree. `-` means nothing to carry.

The diff is between two commits in the repository's Git directory; nothing
is checked out and no index is touched. The worker's own `.gitignore` decides
what it committed, but that does not relax the worker cleanup requirement:
generated dependencies, build output, caches, and temporary metadata must be
absent from the head.

```sh
git --git-dir="$git_directory" diff --no-renames --name-status "$diff_from" "$head_commit" > changed.txt   # A / M / D per path, never R
git --git-dir="$git_directory" diff --no-renames --numstat "$diff_from" "$head_commit"                     # for the delta paragraph
git --git-dir="$git_directory" ls-tree -r "$head_commit"                                               # mode and blob per path
```

If `changed.txt` is empty, report `nothing to publish` for that change and
continue; do not build entries or create an issue or pull request.

`--no-renames` matters: a rename would otherwise arrive as one `R` line
with two paths, and the old path's deletion would never reach the App.

Prepare the entries once, into a file, rather than pasting base64 into the
call by hand:

```sh
python3 - "$git_directory" "$head_commit" changed.txt > entries.json <<'PY'
import base64, json, subprocess, sys
git_dir, head, listing = sys.argv[1], sys.argv[2], sys.argv[3]
modes = {}
for line in subprocess.run(['git', '--git-dir', git_dir, 'ls-tree', '-r', '-z', head], check=True, capture_output=True).stdout.decode().split('\0'):
    if line:
        meta, path = line.split('\t', 1)
        modes[path] = meta.split(' ')[0]
entries = []
for line in open(listing):                     # changed.txt from above
    status, path = line.rstrip('\n').split('\t', 1)
    if status == 'D':
        entries.append({'path': path})
        continue
    content = subprocess.run(['git', '--git-dir', git_dir, 'cat-file', 'blob', head + ':' + path], check=True, capture_output=True).stdout
    entries.append({'path': path, 'mode': modes[path], 'content_base64': base64.b64encode(content).decode()})
json.dump(entries, sys.stdout)
PY
```

Build the `changes` array for `publish_commit`: added and modified paths carry
`content_base64` and the `mode` the tree entry shows (`100644` or
`100755`); deleted paths carry only `path`. The App takes at most 50 entries
per commit and 1,000,000 base64 characters per file (about 732 KiB of
content), and refuses the `.github` directory itself, `.github/workflows`,
the CODEOWNERS locations and the dependabot config (other `.github` paths
are publishable). More than 50 files means several commits on the same
branch, each bound to the head the previous one returned. A file over that
bound, a symlink (tree mode `120000`), or a refused path is a human request,
not a workaround.

Then, with `branch = factory/<first 12 hex of change_id>`:

1. `observe_ref` for `main` and keep the answer as `main_head`. If it is not
   `base_commit`, main moved since the worker started; publish anyway from
   `from` and let the queue merge it, but say so in the body.
2. `publish_commit` with `branch`, `expected_head_sha = from`,
   `merge_parent_sha = merge_parent` on this first commit unless it is `-`,
   `message` = the task title and nothing else (the App takes exactly one
   line: no blank line, no trailers, no session link; a second line is refused
   as `invalid_input`), and the first (or only) 50 entries. The operation id is
   `opid "$change_id" publish-1` for a first publication and
   `opid "$change_id" publish-<HEAD8 of from>-1` for a follow-up. It returns
   the new head commit; a second commit uses the next number and that head,
   and so on. The last returned head is the pull request head.

## 4. Open the issue and the pull request

A follow-up publication (`work_revision` above 1 with the branch present)
already has both: the journal shows `issue` and `pr` completed, and the
pull request number is in the `pr` operation's result. Do not create either
again. Before review, replace its body under `opid "$change_id"
"body-$HEAD8"`. Build a fresh body in the repository's shape, including
what changed and why and verification, and restate its **cumulative**
production-line delta from the review merge base to `HEAD_SHA`; never append
only the follow-up commit's delta. Fetch the new head and calculate that base
and numstat directly:

```sh
git -C repo fetch -q origin main "$HEAD_SHA"
review_base=$(git -C repo merge-base origin/main "$HEAD_SHA")
git -C repo diff --numstat "$review_base" "$HEAD_SHA"
```

For source-linked work, preserve the exact `Refs #NUMBER` (deployment required)
or `Closes #NUMBER` source footer as the final line of the replacement body.
A body update does not reconstruct that link automatically.

The supplied body must be the fresh text only: do not copy an App operation
marker or a `Dark-Factory-Review:` line from the old body. Write it to
`body.md`, call `observe_operation` for `body-HEAD8`, and if it is not
completed call `update_pull_request_body` with the PR number and the contents
of `body.md`. The App adds its own marker and returns the head it observed
with the replacement; this metadata write has no atomic expected-head
condition. If that head differs from `HEAD_SHA`, rebuild the cumulative body
from the returned head under its own `body-HEAD8` operation before review.
Then fetch the rendered body for the cold review:

```sh
curl -s "https://api.github.com/repos/OWNER/REPO/pulls/$PR" | python3 -c 'import json,sys; print(json.load(sys.stdin)["body"])' > body.md
```

A resumed first publication whose `pr` is completed but whose `body.md` is
not in this run's directory takes its body the same way. Then go to the review.

For GitHub-imported work, preserve its `FACTORY_SOURCE OWNER/REPO#NUMBER` marker in
worker tasks and reuse that issue here. Observe its current state before
publication; withdrawn or changed sources require reconciliation. Create a new
tracking issue only when the task has no source issue.

`create_pull_request` needs an issue. `create_issue` with `opid "$change_id" issue`, the
task title (cut to 256 characters, the App's bound), and a body of the task
text plus the change id; it returns the issue number. Then read
`observe_ref` for `main` again, immediately before the call, and use that
answer: `create_pull_request` with `opid "$change_id" pr`, `issue_number` from that
result, `head = branch`, `head_sha` = the last published commit, `base =
main`, `close_on_merge = false` when acceptance requires deployment, `base_sha` = main's head as just read (the App verifies the base
branch is at that commit at that moment; `base_commit` is wrong whenever
main moved, and a stale read is wrong whenever main moves between the read
and the call), `draft = false`, the same title, and a body in this
repository's shape. An operation
id belongs to the exact request it was first sent with, so a request the App
refuses for its content cannot be corrected under the same id: that is a
human request, not a retry.

- What changed and why: from the task and the diff, in prose.
- Production-line delta: added minus deleted outside tests, docs and fixtures,
  from the numstat, with the largest files named.
- How it was verified: what the worker's result text says it ran, and that
  the merge queue runs `scripts/local-ci.sh`. Claim nothing you did not see.
- Never an email, an org name, an account id, or a `/Users/<name>` path.
- End with the repository's generated-with line and nothing after it. You
  have no session link; never invent one.

Write that body to a file; the review needs it.

## 5. Get the cold review, then merge

The existing host `factory-review-intake.py` controller handles independent
review for published PRs linked to tracked source issues. It pins the observed
PR head and base in its review journal, runs at most one fresh
`cold-review.sh` per pass, and sends an idempotent task containing the exact
Maintainer operation and its result. Intake and release checks run first.
The reviewer is a separate read-only session, never the author or overseer.

Observe the operation named in that task before acting. Only a completed App
`submit_pull_request_review` result for the exact head is a verdict. A retained
Change review is useful source evidence, not a published-head approval. The
controller preserves uncertainty after an interrupted launch and observes the
same operation on later passes; it does not replay the launch automatically.
If no controller is configured, report that missing host capability rather than
launching a nested provider from the worker sandbox.

Host operators can still invoke `scripts/cold-review.sh` directly with the
repository, PR, exact head, pinned base and body file. Optional exact-head gate
evidence supplements rather than replaces required checks. Blocking findings
need a concrete reproducer or reachable code path through the current guards;
unavailable read-only checks are deferred delivery conditions, not defects.

- Unresolved: observe the supplied operation and report its concrete host
  infrastructure failure. Do not manufacture a verdict or start another review.
- ALLOW: `enqueue_pull_request` with `opid "$change_id" enqueue-HEAD8`, the PR number, the head
  and `base = main`. Then `observe_pull_request_merge`, with the PR number,
  the head, `base = main` and the enqueue operation id, every 60 s for up to
  30 minutes;
  never faster. Merged: done. Still queued after 30 minutes: the change is
  not finished; raise a human request naming the PR and stop, and the next
  run observes it again. No longer queued and not merged:
  the queue's run failed or dropped the entry, and the App cannot read a
  queue run's log or rerun it (`read_pull_request_job_log` and
  `rerun_failed_pull_request_jobs` bind to the pull request's own runs), so
  raise a human request with the PR link; never enqueue again on your own.
  An ALLOW the App did not record shows up the same way: the queue's review
  check refuses the entry.
- REQUEST_CHANGES: you do not fix code. Send the task back to its worker
  with a note that names the pull request and tells the worker how to read
  the findings; the findings themselves live on the pull request's reviews,
  which the worker can fetch without a credential, so the note needs only
  the pull request number and head:

  ```sh
  # task_id is the reviewed target task from the explicit source receipt.
  # Refresh `attempt source --task "$task_id"` and require the same Change
  # ID, task work revision and Change revision before this mutation.
  note="Pull request https://github.com/OWNER/REPO/pull/$PR (head $HEAD_SHA) was blocked by its cold review with must-change findings. Read them with: curl -s https://api.github.com/repos/OWNER/REPO/pulls/$PR/reviews | python3 -c 'import json,sys; [print(r[\"body\"]) for r in json.load(sys.stdin)]' and fix each in the tree you left; the pull request stays open."
  "$DARK_FACTORY_FACTORYCTL" attempt send-back --task "$task_id" --note "$note"
  ```

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
On a resumed run, a published change needs no second review if the host
controller's exact operation already completed. Read the operation ID from its
follow-up task: `allow` permits protected enqueue; `block` returns findings to
the original task unless it is already queued for correction. Missing,
`executing`, or `indeterminate` is not a verdict. Preserve that operation and
report unresolved infrastructure; do not derive a replacement ID or launch a
second reviewer. The `review` check runs only in the merge queue, so its absence
before enqueue is not a signal to repeat review.

## 6. Hand off and finish

If a merged PR touched `cmd/` or `internal/`, it may need a live-service
reinstall, and if it touched `web/`, it may need a site re-vendor. Before a
runtime reinstall request, inspect `go version -m "$DARK_FACTORY_FACTORYCTL"`.
With its exact `vcs.revision` as `installed`, run `git -C repo fetch origin "$installed" "<merge-commit>"`, then verify each with `git -C repo rev-parse --verify "<revision>^{commit}"`. Run `git -C repo merge-base --is-ancestor
<merge-commit> "$installed"`: status 0 means that merge is already installed,
so skip that request and never recommend an older merge; only status 1 says it
is absent. Require `vcs.modified=false`; missing, malformed, or modified
metadata, a fetch or verification failure, or any other ancestry error warrants
a human request to verify the installed source, not a claim that the merge is
absent. If the runtime merge is absent, or the site needs a re-vendor, raise
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

After `request-human`, leave the native attempt running at its prompt; do not
call `attempt block` or `attempt succeed` until the answer arrives. The
console delivers and submits a Codex answer, then you continue from it. The
operator can cancel the existing NEEDS YOU card if waiting is no longer useful.
Once the reply resolves the work, end with `attempt succeed` or, for an
ordinary non-human failure, `attempt block` (detail cut to its 4 KiB bound;
the question allows 8 KiB). The next standing-instruction run picks up where
the journal says you stopped.

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

Operator supervision can use `factoryctl task recovery --task TASK_ID --incarnation INCARNATION_ID` for current result and blocker text without
opening runtime files. `result_truncated` explicitly marks a UTF-8-safe 65536-byte
excerpt; existing overseer or browser task-detail paging retrieves the full
stored result. `run_outcome` and `run_detail` describe only the returned run at
`run_work_revision`, which may precede the current task work revision after
send-back. They are not a result for the new correction.
