# Installation

The same three sibling binaries can come from a published macOS release or a
source build. The [development workflow](development/WORKFLOW.md) documents a
temporary `DARK_FACTORY_HOME` and explicit socket for isolated checks.

## Install a release

The release provides one archive for each supported macOS target:

- Apple silicon: `dark-factory-vX.Y.Z-aarch64-apple-darwin.tar.gz`
- Intel: `dark-factory-vX.Y.Z-x86_64-apple-darwin.tar.gz`

Download the matching archive from the
[GitHub releases](https://github.com/dark-factory-build/dark-factory/releases),
verify its entry in that release's `SHA256SUMS`, and put `factoryd`,
`factory-runner`, and `factoryctl` from the archive together on `PATH`. The
release's Homebrew formula installs the same commands if it has been added to a
tap.

factoryd polls issue intake itself; no operator JSON, handwritten launchd
file, or source checkout is needed. See the intake instructions below.

Create and install one managed home. Those two commands are the whole terminal
side of setup: an install that starts a fresh service loads the launchd job,
waits for the daemon to answer, mints a one-shot pairing link with the home's
operator token, and opens it in this machine's default browser, which pairs that
browser. The link is never printed. A repeated install returns the service it
found and opens nothing. `service start` is the
explicit command to use after a later stop:

```sh
factoryctl init --home "$HOME/.dark-factory"
factoryctl service install --home "$HOME/.dark-factory"
```

To upgrade a running installation to a new build, run `factoryctl service
uninstall --home "$HOME/.dark-factory"` with the new `factoryctl`, then
`factoryctl service install --home "$HOME/.dark-factory"`. Only the service's
binaries, plist, and receipt are replaced; the data home is untouched. An
installation that used `--relay-origin` must repeat that flag on the install
after the uninstall, or the new job comes back loopback-only.

Settings → Updates in the console shows the same two commands beside the
running version and the latest published release. To find that release the
daemon reads the public GitHub releases endpoint for this repository at most
once every six hours, and only while a paired console is open. That bound
holds whether the read succeeds or not: a refused read leaves the previously
cached release, or none, in place until the next six-hourly attempt, so an
offline or rate-limited host neither retries more often than a healthy one nor
claims that this build is current. Nothing in the console installs an update.

Before upgrading across a schema change, copy the database to a directory
outside the home. Never into the home itself, and add nothing else there
either: the daemon refuses to open a home holding anything it did not put
there.

```sh
mkdir -p "$HOME/.dark-factory-backups/$(date +%F)"
sqlite3 "$HOME/.dark-factory/factory.sqlite3" \
  ".backup $HOME/.dark-factory-backups/$(date +%F)/factory.sqlite3"
```

That copy is the only way back: the new build migrates the home on its first
start, the migration is one way, and an older build refuses the migrated home.

To reach the factory from the hosted PWA rather than only from this machine's
loopback, install with `factoryctl service install --home "$HOME/.dark-factory"
--relay-origin wss://relay.darkfactory.build`. The flag is optional and has no
default: omitting it installs a loopback-only job exactly as before. The
installed launchd job then starts `factoryd` with that `--relay-origin`, and the
service receipt records the exact argument list it was rendered from, so
`service status` and `service uninstall` reproduce that exact plist from the
receipt and keep proving ownership. Changing or removing the relay origin is
`factoryctl service uninstall` followed by a fresh install carrying the origin
you want: repeating an install with a different origin refuses, printing the
origin already installed, rather than silently keeping or dropping it. Pair a
phone from the console's PAIR A PHONE button.

Pairing a browser with the full grant is an operator action: `factoryctl web
pair` mints a one-shot link (it expires after five minutes) and opens your
default browser already paired, never printing the link. Run it to pair another browser on
this Mac (make it the default first), or to recover when a browser's saved
credential is no longer accepted, for example after `web revoke`. A phone or
another machine pairs with the reduced remote grant from a paired console's PAIR
A PHONE button. The loopback listener itself mints nothing.

Pairing, inspection and revocation run through the operator client. Operator
commands find the default home `$HOME/.dark-factory` on their own; exporting
`DARK_FACTORY_SOCKET` and `DARK_FACTORY_OPERATOR_TOKEN_FILE` is optional for the
default home and required for any other:

```sh
factoryctl web status
factoryctl web pair
factoryctl web list-clients
factoryctl web revoke CLIENT_ID --revision REVISION
factoryctl remote status
```

The CLI cannot delete origin-scoped browser storage; pairing afresh with
`factoryctl web pair` makes that manual browser action unnecessary.

## Start your first worker

After pairing the browser, follow [Your first task](../README.md#your-first-task)
from an existing committed Git checkout. A signed-in `codex` CLI must be
installed where the managed daemon can find it; see [provider
discovery](providers.md). Creating the project also registers its checkout as
the initial repository. `factoryctl account discover` and `factoryctl account
list` help inspect a missing Codex login. Settings → Repositories also supports
project creation and additional checkouts; worker creation currently uses the
CLI.

## Working in the console

Select an agent on the floor or in the roster. Its terminal, queue and Needs
You decisions share the side panel. A terminal instruction to a ready agent
creates durable work; Message steers a running session. Interrupt stops Codex
generation while preserving the task, while Stop ends it. Send a completed
result back with feedback or start a separate replacement without losing its
history. Add to queue can hold later work while an agent is busy.

Assign work to a named worker or Any eligible worker in the project. Named
work is taken first; the next free eligible worker then claims shared work.
Raise or lower numeric priority explicitly when ordering either queue. Recent
Work loads authorized completed and blocked details on demand; private
instructions and results never enter public snapshots. It starts with the
newest ten results; Show more loads ten at a time. Agent state is Ready,
Working, Needs you, or Paused: Working includes startup and cleanup, while
queued work alone remains Ready. Pause prevents future admissions without
stopping current work, and Show archived lets you inspect or restore a drained
worker without erasing its history. Factory capacity counts workers; one
overseer can run alongside them.

On the floor, select the task tray for **Tasks**, the planning
table for **Missions**, the board or the bookshelf. A mission records an
objective and acceptance criteria for an overseer; its related work remains
inspectable after workers finish. **Pause new work** stops new admission while
active processes continue.

The Tasks panel's **New task** form accepts pasted images, dropped files, or
files selected with **Attach files** (up to 8 files and 8 MiB total). Add an
instruction, review or remove the previews, then submit. Attachments commit
with the task in the local daemon database and remain there with its history.
Each attempt gets a fresh copy in its private runtime home, available through
`$DARK_FACTORY_TASK_ATTACHMENTS`; uploads do not enter your Git checkout.
Unsubmitted uploads are temporary and discarded when the connection closes.
File contents remain unchanged; interpretation depends on the provider's tools.

In **Settings → Attachment storage**, enable **Automatically remove attachments
after 30 days** to clean up succeeded and cancelled tasks hourly while the daemon
is running. This saved factory-wide setting is off by default and also applies
to existing tasks. Turning it off stops future cleanup. To remove files sooner, run
`factoryctl task update --task ID --revision REVISION --remove-attachments`.
Queued, running, failed, and blocked tasks are protected. `factoryctl task read`
keeps the original filenames with `removed: true`; sending cleaned tasks back
is refused, so create a new task and attach any required files instead. Normal
runtime cleanup already removes worker copies.

Deletion frees database space for reuse. To return unused space to disk, turn
`factoryctl dispatch off`, let all runs settle, then run
`factoryctl storage compact`. It uses SQLite VACUUM and a WAL checkpoint while
holding the daemon's writer gate. Compaction is optional, may need temporary
free disk space up to twice the database size, and leaves dispatch off.
Compaction remains explicit; automatic attachment cleanup frees database space
for reuse without running VACUUM.

## Inspect a result

`factoryctl status` lists every task with its `status` and current `revision`;
`factoryctl task read --task TASK_ID --revision REVISION` returns its
instruction, any feedback, and the worker's `outcome`. A stale revision is
refused (`local API revision is stale`); read the current one from `status`.

A worker's commits stay in its Change: a worktree under the home's `changes/`
directory and a private Git directory at
`.git/dark-factory-changes/CHANGE_ID/.git` in the registered checkout, on the
branch `factory/` plus the first 12 characters of the Change ID. Fetch it into
your checkout to review it:

```sh
git fetch .git/dark-factory-changes/CHANGE_ID/.git factory/CHANGE_PREFIX:review/my-task
git log --stat main..review/my-task
```

Neither `task read` nor the console names the Change yet; with several
tasks, match the branch by its commit. Nothing is pushed or merged unless a
GitHub connection publishes it.

A failed task keeps its history: a worker that exits without reporting an
outcome settles `failed` with the outcome `provider exited before an attempt
outcome`. Retry it, or a cancelled task, with `factoryctl task update --task
TASK_ID --revision REVISION --retry`, or send a completed model result back with
`factoryctl task send-back --task TASK_ID --note TEXT` (shell tasks take no
note, so send-back refuses them).

When factoryd cannot publish a finished Change, it records the failure and
escalates it to the project's overseer once. A Maintainer refusal, conflict or
unavailability, or a repository disabled for new work, is retried for the same
Change revision about hourly, so a cause fixed outside factoryd heals itself.
Any other rejection, such as invalid input caused by the Change, is final for
that Change revision: send the task back so a corrected revision can publish.

## Try a task without a model

The `shell` provider runs the task body as a `/bin/sh` script in the Change
worktree, so the whole path (queue, Change, commit, outcome) can be exercised
without a provider login or model cost. This uses a disposable home, socket,
token and browser port, and a throwaway repository, so it never touches an
installed factory. Build the three binaries from source into one directory
first (see the [isolated daemon check](development/WORKFLOW.md#isolated-daemon-check)).

```sh
demo=/private/tmp/df-demo; bin=/path/to/built/binaries
git init -q -b main "$demo/repo"
printf '# Demo\n' > "$demo/repo/README.md"
git -C "$demo/repo" add README.md && git -C "$demo/repo" commit -qm "Initial commit"
"$bin/factoryctl" init --home "$demo/home"
"$bin/factoryd" --home "$demo/home" --development-browser-address 127.0.0.1:43999 &
until [ -S "$demo/home/runtimes/factory.sock" ]; do sleep 0.2; done
export DARK_FACTORY_SOCKET="$demo/home/runtimes/factory.sock"
export DARK_FACTORY_OPERATOR_TOKEN_FILE="$demo/home/operator.token"
"$bin/factoryctl" dispatch on
"$bin/factoryctl" project create --name Demo --root "$demo/repo"
"$bin/factoryctl" agent create --project PROJECT_ID --name builder --provider shell --tool-budget 100
"$bin/factoryctl" task add --project PROJECT_ID --agent AGENT_ID --title "Add hello.sh" \
  --body 'printf "#!/bin/sh\necho hello\n" > hello.sh && chmod +x hello.sh && git add hello.sh && git commit -qm "Add hello.sh" && "$DARK_FACTORY_FACTORYCTL" attempt succeed --result "Added hello.sh"'
```

A few seconds later `status` shows the task `succeeded`, `task read` returns
`"outcome":"Added hello.sh"`, and the branch fetched as above holds one commit
by the fallback identity `Dark Factory`. A body that exits without
`attempt succeed`, such as `./does-not-exist.sh`, settles `failed`. Stop the
daemon with `kill %1` and delete the directory when done. The `/private/tmp`
root matters: the home walk rejects symlinks such as `/tmp`, and a Unix socket
path must stay short.

## Feedback and public backlog

Settings → Help and feedback opens the public reporting and backlog pages,
including while the factory is disconnected. From the CLI:

```sh
factoryctl feedback bug --open
factoryctl feedback feature --agent-assisted --factory-name "My workshop"
factoryctl backlog
factoryctl backlog --open
```

Without `--open`, feedback prints its preparation link. Only bounded build
identity and explicitly supplied context are included; no factory settings,
repository names, paths or logs are read. Factory names and agent assistance
are self-reported context. Review the report and submit it under your own GitHub
account. Opening a page does not submit an issue. The reporting page provides
copyable text when a report is too long for a prefilled URL.

`backlog` returns the fixed public backlog as JSON, with its observation time
and truncation indicator. It needs no running daemon, GitHub connection or local
execution subscription. Use the issue's native GitHub thumbs-up reaction to
endorse it. Reports and votes do not accept work or start a local agent.

Create each agent with an explicit provider. `shell` needs no external tool;
`claude_code` and `codex` require the corresponding `claude` or `codex` CLI to
be installed and already signed in through its normal account workflow:

```sh
factoryctl agent create --project PROJECT_ID --name worker --provider shell --tool-budget 100
factoryctl agent create --project PROJECT_ID --name worker --provider claude_code --reasoning-effort medium --tool-budget 100
```

The managed daemon finds native tools on its fixed path and reuses the
operator's existing signed-in Claude or Codex account without copying a
credential into the Dark Factory home. See the [provider
contract](providers.md) for discovery, model, effort, and task-delivery details.

`factoryctl service stop` stops the managed daemon without removing the
installation; restart it with `factoryctl service start --home
"$HOME/.dark-factory"`. `factoryctl service uninstall` is the evidence-first
removal path for that exact home and label. Homebrew does not own the running
service; do not use `brew services` for Dark Factory.

## GitHub connection (v0.4.0+)

This setup requires v0.4.0 or later and an activated Maintainer connection
endpoint. It is not available in older archives. Use the operator socket and
token exports above; these identify your local factory, not someone else's
GitHub account.

**Hosted availability:** the v0.4.0 client includes this flow, but the hosted
service still requires operator activation before new customers can connect.
Installing the client is not sufficient. The remaining service-side GitHub App
and routing setup is documented in [activation prerequisites](development/GITHUB_CONNECTIONS.md#activation-prerequisites-and-proof).
Customers do not need to create an App or deploy their own broker.

Run `factoryctl github connect --open`. Authorize the Dark Factory GitHub App
under your GitHub account. Enter the callback page's one-time code with
`factoryctl github confirm CODE` on the same factory where you started. Do not
send that code to another person. The browser and CLI never receive GitHub App
private keys or installation tokens.

`factoryctl github installations` lists your installations. Follow `next_page`
with `--page N`, including after an empty page. Use
`factoryctl github manage --installation ID --open` for native GitHub access
settings. Organization approval may be needed there; a selected-repository
installation is the supported baseline. A visible repository does not grant
publication rights: your GitHub account must have write permission.

Run `factoryctl github repositories --installation ID --page 1` to discover
repository IDs. To set the connection's maximum delegation, put a JSON array
of `{ "installation_id": 123, "repository_id": 456, "repository": "org/code" }`
in a file and run `factoryctl github delegate --repositories FILE`. This
replaces the whole selection. An empty array removes all repository access.
Delegation does not register a checkout or subscribe to an issue backlog.

The connected GitHub account also supplies commit attribution automatically;
there is no separate author setting. New local worker and generated content
commits use its login and GitHub no-reply address. New publications use that
verified operator as author while the Maintainer App remains the publisher.
Existing history is unchanged. Without a verified connected identity, local
work uses one DF fallback identity. See [provider attribution](providers.md).

Use `factoryctl github status` or `factoryctl github refresh` to check live
access. Lost authorization never falls back to a different operator's account.
`factoryctl github disconnect` disables new host operations before contacting
the broker. If the broker is offline, status reports `disconnect_pending`;
retry disconnect when it is reachable to finish remote revocation. The fixed
private credential record stays in the protected factory home, outside worker
homes, task text and browser snapshots. Keep that record with the home backup.

Git fetch authentication remains separate: use the operator-owned Git setup in
the [provider guide](providers.md). A working Maintainer connection does not
make a private checkout fetchable.

After registering a checkout, check its configured source with
`factoryctl project repository fetch --id REPOSITORY_ID`. This uses the same
Git selection and authentication boundary as new work, preserves operator edits
and branch refs, and reports `ready` or `setup_required`. Configure private Git
access for that checkout using your existing Git setup and retry when needed.
Credentials are never accepted as CLI flags or passed from the GitHub broker.
An explicit check also verifies the checkout identity for migrated projects;
ordinary repository listing performs no fetch or identity changes.

Use `factoryctl project repository github --id REPOSITORY_ID` to bind its
configured publication repository to the live GitHub connection. Fetch readiness
and publication binding are separate checks. A verified publication binding
still requires live write permission for every publication operation.

Once a repository is bound to a project, factoryd also reviews its open pull
requests, including ones it did not publish. A blocking verdict or merge-queue
ejection on a head that has not moved goes back to the task that published the
pull request. For a same-repository pull request factoryd did not publish, it
first creates one `Repair OWNER/REPO#N` worker task and publishes its result to
that pull request's own branch. Each send-back after the second repair round
is also escalated to the project's overseer; it does not stop further repair.
A pull request from a fork only receives the verdict on GitHub.

## Project repositories (v0.4.0+)

Project settings can register several existing Git checkouts. Creating a project
registers its initial checkout; an upgrade preserves the old project root and
all existing task and Change identities. No operation clones or deletes files.

For automation, `factoryctl project repository list --project PROJECT_ID`
returns each private binding and its revision. With v0.4.1 or later, add an
existing checkout with `factoryctl project repository add --project PROJECT_ID
--name NAME --root ABSOLUTE_PATH --base REF`. The CLI creates the binding ID;
use `--id HEX32` only when automation needs a chosen ID. Use that repository's
actual base/upstream ref. Registration verifies the checkout, Git directory
and configured publication origin. Use the `name`,
`base`, `default`, `enable`, `disable` or `remove` subcommands with `--id` and the
current `--revision`; name/base changes also take `--name`/`--base` respectively.

`factoryctl task add --project PROJECT_ID --repository REPOSITORY_ID ...`
selects a checkout explicitly. A sole repository or configured default is used
when selection is omitted; issue prose never chooses a repository. Changing a
default or base does not redirect existing work, retries or retained source
reviews. Disable prevents new selection while preserving history. Removal is
refused while a binding remains referenced and never removes the checkout.
Private Git fetch authentication remains operator-owned and separate from the
Maintainer connection.

For a newly created project's first repository, `factoryd` defaults to
`--base-revision HEAD`; this boot setting initializes the binding and does not
retarget existing work. With `HEAD`, a fresh Change starts from the origin's
default branch as the origin reports it now, whatever the registered checkout
has checked out and however far its local branches lag. A
`refs/remotes/upstream/main` base selects a different remote branch. Each
remote base is fetched into the factory-owned
`refs/factory/base/DIGEST` (a SHA-256 of the remote and branch, so a renamed
default branch never collides with an old one) and the Change is pinned to that commit.
Only a checkout without an `origin` follows its own HEAD: its branch's
upstream, or the local branch or detached HEAD itself. Explicit local refs or
commit IDs stay local. A fetch failure stops source preparation rather than
using a stale ref. Fetching does not move the registered checkout, its
branches, tracking refs or `FETCH_HEAD`. Retained Changes keep their original source and
edits; use the repository `base` setting for future work in that binding. Git
runs noninteractively with a private home and global/system configuration
disabled. Private remotes use repository-local authentication; missing
credentials fail preparation rather than borrowing a worker account.

## Reviewed issue intake (v0.4.0+)

These commands require v0.4.0 or later and an activated GitHub connection.
GitHub holds the backlog; the factory holds execution and acceptance. An issue
repository can feed a different registered code repository. Delegating GitHub
access alone does not subscribe to issues or start work.

Use `factoryctl intake list --project PROJECT_ID` to inspect sources. With
v0.4.1 or later, create a source with `factoryctl intake create --project
PROJECT_ID --repository OWNER/BACKLOG --target-repository REPOSITORY_ID`.
The CLI creates the source ID. New sources start paused, use manual approval,
poll every 60 seconds and admit up to 25 accepted issues. Add `--label LABEL`,
`--policy trusted-authors`, repeated `--trusted-author LOGIN`, `--overseer ID`,
`--poll-seconds N`, `--admission-limit N` or `--priority N` when needed. `@me`
in trusted authors resolves your connected GitHub identity. A trusted-author
policy permits initial acceptance by those authors **or** explicit operator
acceptance; a label is only a filter.

For automation, `--source HEX32` preserves an explicit source ID and
`--configuration JSON` supplies the complete configuration instead of named
flags. The v0.4.0 CLI requires that explicit-ID/JSON form. The destination and
overseer must belong to the project.

Run `factoryctl intake preview --source SOURCE_ID --page 1`, following
`next_page`, and inspect the title, body, destination and eligibility reasons.
Enable the reviewed configuration with `factoryctl intake enable --source
SOURCE_ID --revision REVISION --reviewed-revision REVISION`. An update uses
`intake update` with the full configuration and current revision; it pauses the
source so you can preview the change before enabling it.

Accept the exact displayed content using `factoryctl intake accept --source
SOURCE_ID --revision REVISION --issue NUMBER --hash CONTENT_HASH`. The daemon
checks the current GitHub content again before recording acceptance. The
daemon imports accepted work into the existing queue; comments and reactions
do not create work. A later title/body edit needs fresh acceptance, including
when the original issue author is trusted. While the issue stays open and
labelled, its task is queued again up to three times after an automatic end (a
failure, a run-limit cancel, a 24-hour blocked expiry); an operator's cancel
sticks and completed work is not retried.

factoryd polls each enabled source when its `--poll-seconds` interval is due,
while the GitHub connection (or Linear) is configured. Poll progress is kept in
memory: a restart rescans from the first page, and acceptance and import are
idempotent, so no work is duplicated. `intake list` reports each source's last
poll, last success and current error. An unavailable GitHub connection is not
an empty backlog.

`factoryctl intake pause --source SOURCE_ID --revision REVISION` stops new
imports, not existing work. `factoryctl intake withdraw --acceptance ID`
withdraws that approval, cancels linked queued work and requests the existing
stop mechanism for running work. `withdrawal_pending` requires reconciliation;
it does not promise that an offline host or running process has stopped.
`factoryctl intake import --acceptance ID` reverses a withdrawal while the
issue still matches, and retries its failed or cancelled task.

Priority mappings support at most 25 labels and 2 KiB of JSON after escaping.
