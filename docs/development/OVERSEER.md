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
admission limits. The overseer lane is not an extra worker slot. Do not infer
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
files owned by your attempt; retained Changes and refused trees remain governed by
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
continue with `--text-offset N --head HEAD` while `next_text_offset` is set. Use the `overseer` commands to
assign or reorder queued work, message or interrupt a worker, answer its
question, stop or replace its objective, send work back, and pause or resume
future admission. These commands use your attempt credential; an operator
credential is neither available nor required. Run a command with `--help` for
its exact flags. All targets must remain in your project.
`overseer task update --task ID --revision REVISION --title TEXT --body TEXT`
edits only a queued worker task. The body replaces its base instruction while
the latest retained send-back note remains attached as read-only review
feedback; it does not create a new `work_revision`. Use priority, assignment,
or cancel alone when changing a legacy task whose stored prompt is no longer
accepted by its provider.
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

After delegating or handling the current events, report your durable outcome
and exit. Do not poll a worker until it finishes. With a standing instruction
and remaining run budget configured, worker completion, questions, and explicit
interventions wake you again. Events received while you are queued or running
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
Changes-parent, or operator-home access; its only retained-source access is
the exact eligible same-project target requested by the admitted task and
selected by the daemon at launch. Retained history is not a broad launch
grant: the one-tree bound keeps the Codex permission argument below its fixed
limit.

## What you may and may not do

- Publish only through the App: `publish_commit`, `create_issue`,
  `create_pull_request`, `update_pull_request_body`, `enqueue_pull_request`,
  and the observe tools. Never
  `git push`, never edit the operator's checkout, never write into a retained
  Change.
- Never record a review verdict yourself. The review is a separate headless
  session started by `scripts/cold-review.sh`; you read its verdict.
- One change at a time, in the order the runs finished.
- A review that asks for changes is not a decision for a human: send the task
  back to its worker with the findings (`attempt send-back`, section 5) and
  the worker's next run continues from the tree it left.
- When a step needs a decision you are not sure of, or a publication is
  blocked twice, raise it with `attempt request-human` and keep the native
  attempt alive for the reply (section 6). The request is the NEEDS YOU card
  on the operator's console while the run lives.

For unattended projects, also follow [UNATTENDED.md](UNATTENDED.md).

## 1. Find what a worker finished

Never read the daemon SQLite database, the whole daemon home, or the Changes
parent. As overseer, run `overseer status --task TASK_ID` for the task
you are handling. This may return task-only status: the retained tree is not
materialized by status. After the task-only response, explicitly run
`factoryctl attempt source --task TASK_ID`; its response must contain exactly
one usable receipt naming the Change ID, base commit, target task ID, task work
revision, current retained Change revision, and daemon-derived `source_path`
for that exact tree. Match every identity value to the requested task and
status, then read only that returned path. The daemon verifies and materializes
the target into the reader's private runtime; never construct a
`$home/changes/...` path, read `factory.sqlite3`, or infer source from a
published branch. If the task was sent back or any task/work/Change revision
changed, the source request is refused and must use exact current task state.
Source requests are lifetime-bound to the live attempt: shutdown first refuses
new requests and waits for admitted materialization to finish before runtime
cleanup removes the private snapshot.
The daemon verifies the selected retained root and manifest commitment before
copying, then verifies the private copy again; a changed, mixed, symlinked or
escaped tree is refused rather than attached to the old receipt. The old
snapshot is not the reopened Change, so an overseer can send back the task it
reviewed without cancelling itself. A status record without its
launch-scoped tree access still reports ordinary task state, but is not a
source handoff.

An independently delegated Codex reviewer uses the same explicit source
request. It must match Change ID, base commit, target task ID, task work
revision and Change revision before reading `source_path`; the readable
retained tree is deliberately Git-free, so it is evidence of the worker Change
rather than a substitute published branch. A reviewer is not an overseer and
cannot use `overseer status` to discover a tree. No receipt, a changed
identity, or a path outside that launch's read permission is a refusal, not a
candidate for path reconstruction.

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
- above `1`: the task was sent back and the worker continued from the tree it
  left. `observe_ref` for the branch; it exists, at `branch_head`, and the
  commit goes on top of it under `publish-<HEAD8 of branch_head>-1`, with the
  diff against that head, not the base. A branch that does not exist here
  means the earlier publication never happened: treat it as the first.

Set `from` to `base_commit` or `branch_head` accordingly.

Compute the diff against `from` without a checkout: the clone's object
store, its index filled from `from`, and the retained tree as the work tree.
`git add -A` respects the tree's own `.gitignore`, but that does not relax
the worker cleanup requirement: generated dependencies, build output, caches,
and temporary metadata must be removed before settlement.

```sh
export GIT_DIR=$PWD/repo/.git GIT_WORK_TREE=$source_path GIT_INDEX_FILE=$PWD/change.index
git fetch -q origin "$from"
git read-tree "$from" && git add -A
git diff --cached --no-renames --name-status "$from" > changed.txt   # A / M / D per path, never R
git diff --cached --no-renames --numstat "$from"                     # for the delta paragraph
git ls-files --stage                                                 # mode and blob per path
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
```

If `changed.txt` is empty, report `nothing to publish` for that change and
continue; do not build entries or create an issue or pull request.

`--no-renames` matters: a rename would otherwise arrive as one `R` line
with two paths, and the old path's deletion would never reach the App.

Prepare the entries once, into a file, rather than pasting base64 into the
call by hand:

```sh
tree=$source_path
python3 - "$tree" changed.txt > entries.json <<'PY'
import base64, json, os, sys
tree, listing = sys.argv[1], sys.argv[2]
entries = []
for line in open(listing):                     # changed.txt from above
    status, path = line.rstrip('\n').split('\t', 1)
    if status == 'D':
        entries.append({'path': path})
        continue
    full = os.path.join(tree, path)
    mode = '100755' if os.access(full, os.X_OK) else '100644'
    entries.append({'path': path, 'mode': mode, 'content_base64': base64.b64encode(open(full, 'rb').read()).decode()})
json.dump(entries, sys.stdout)
PY
```

Build the `changes` array for `publish_commit`: added and modified paths carry
`content_base64` and the `mode` the staged entry shows (`100644` or
`100755`); deleted paths carry only `path`. The App takes at most 50 entries
per commit and 1,000,000 base64 characters per file (about 732 KiB of
content), and refuses the `.github` directory itself, `.github/workflows`,
the CODEOWNERS locations and the dependabot config (other `.github` paths
are publishable). More than 50 files means several commits on the same
branch, each bound to the head the previous one returned. A file over that
bound, a symlink (staged mode `120000`), or a refused path is a human request,
not a workaround.

Then, with `branch = factory/<first 12 hex of change_id>`:

1. `observe_ref` for `main` and keep the answer as `main_head`. If it is not
   `base_commit`, main moved since the worker started; publish anyway from
   `from` and let the queue merge it, but say so in the body.
2. `publish_commit` with `branch`, `expected_head_sha = from`, `message` =
   the task title and nothing else (the App takes exactly one line: no blank
   line, no trailers, no session link; a second line is refused as
   `invalid_input`), and the first (or only) 50 entries. The operation id is
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
git -C repo fetch -q origin "$HEAD_SHA"
review_base=$(git -C repo merge-base "$base_commit" "$HEAD_SHA")
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

```sh
DARK_FACTORY_REVIEW_OPERATION_ID=$(opid "$change_id" "review-$(printf '%s' "$HEAD_SHA" | cut -c1-8)") \
    repo/scripts/cold-review.sh OWNER/REPO PR HEAD_SHA "$base_commit" body.md "first review"
```

`cold-review.sh` uses a fresh read-only Codex review with `gpt-5.6-sol` by
default. Set `DARK_FACTORY_REVIEW_MODEL=gpt-5.6-terra` for the lower-cost
variant, or `DARK_FACTORY_REVIEW_PROVIDER=claude` only when Claude is needed.

The fourth argument is the change's `base_commit`, the commit the branch was
published from, never main's live head: the reviewer's diff runs from the
merge base of that commit and the pull request head, and its rules are that
commit's `AGENTS.md`.

When an exact-head gate receipt is available, pass its path without changing
the review's read-only tools or permissions:

```sh
DARK_FACTORY_REVIEW_EVIDENCE_FILE=gate.json \
DARK_FACTORY_REVIEW_OPERATION_ID=$(opid "$change_id" "review-$(printf '%s' "$HEAD_SHA" | cut -c1-8)") \
    repo/scripts/cold-review.sh OWNER/REPO PR HEAD_SHA "$base_commit" body.md "first review"
```

The JSON receipt supplements, never replaces, the required gate. The helper
requires matching `head` and `base` strings and integer `exit_code: 0` before
starting the reviewer. A blocking review finding needs a concrete reproducer or a
reachable code path through the current guards to a missing or ineffective
check. Reviewers inspect the documented threat model before security claims;
an unverified hypothetical or an unavailable read-only test is a deferred
note, not a block.

The verdict is recorded under that operation id, so `observe_operation`
with it answers `completed` with the verdict (`allow` or `block`) once a
review exists, and nothing until then. The script exits 0 for ALLOW, 1 for
REQUEST_CHANGES, 3 when the session reported no verdict, 4 when the pull
request is no longer at that head, 2 for an argument or a tool it refuses,
and 5 when it could not prepare the checkout, and leaves
`review-PR-HEAD8.log` in the current directory.

- Exit 3: run it once more; a second 3 is a human request with the log's
  last lines.
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

  Never recover a task ID or a retained-tree path from SQLite. The task/status
  handoff is authoritative and cross-project, stale, refused, missing, or
  non-retained handoffs are refusals, not candidates for reconstruction.

  The note goes at the end of the task's body, replacing the note of any
  earlier send-back, and the worker's provider receives that body whole;
  the daemon refuses a send-back the provider could not be handed
  (`too_large`), so keep the note to that shape: a pointer, never the
  findings pasted. A shell agent's task is a program and cannot be sent
  back at all (`invalid_request`); that is a human request. A `too_large`
  for a note of that shape means the task's own instruction is at the
  provider's bound and no note fits: raise a human request naming the pull
  request and the task, and end the run with `attempt block`, since the
  change would otherwise come around again to the same refusal. The task
  is queued again and the worker's next run continues from the retained
  tree; a later run of yours finds the same change id at the next work
  revision and publishes the new tree on top of the branch (section 3).
  Stop handling this change for now.
- Exit 4: the pull request is no longer at the head you published, which
  only a person can have done; raise a human request.
- Exit 2 or 5: the script refused its arguments or could not prepare the
  checkout; the log was not written. Check the head and base you passed once,
  then raise a human request with the script's message.

On a resumed run, a change whose `pr` is completed but whose `enqueue-HEAD8`
for the current head is not needs no second review if one was recorded:
`observe_operation` with `opid "$change_id"
review-HEAD8` for the pull request head answers `completed` with verdict
`allow` (enqueue), `block` (blocked: send the task back with the note
above, which needs only the pull request and its head, if the task is not
already queued at the next work revision, then stop), `note`
(a comment that decided nothing: run the review again under a fresh id, the
head's plus `-2`), or nothing (run the review); `executing` or
`indeterminate` is a human request, as in section 2. The `review` check
itself runs only in the merge queue, so it is never the signal here.

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
worker run whose tree the daemon refused ends failed with the reason and
leaves the tree at `$home/changes/<change_id>.refused-<run8>` for a person
to read and remove; it is never yours to publish. Then report, one line
per change: change id, PR number, and merged commit or the reason it
stopped.

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

Workers use `attempt peer status` to discover eligible same-project Codex tasks
and read questions or answers linked to their own task. Follow `next_offset`
with `--offset` and `head` with `--head` for older conversations, and
`next_target_offset` with `--target-offset` under the same head for more
targets. A stale continuation must restart from the first page. `attempt peer
ask` and `attempt peer answer` are asynchronous: queued recipients can read
the question when admitted, and neither operation changes capacity or waits for
another worker. Reuse the same idempotency key when retrying an uncertain
request.

The overseer can read peer conversations with `overseer status --task TASK_ID`;
follow its existing `next_offset` and head fence. Peers provide collaboration
context, not operator instructions or authority to control another task.
Browser task history exposes the same question, answer, and notification
receipts, including after either task finishes.
