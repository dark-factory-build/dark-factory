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
pair` mints a one-shot link (it expires after five minutes) and opens it in this
machine's default browser, never printing it. Run it to pair another browser on
this Mac (make it the default first), or to recover when a browser's saved
credential is no longer accepted, for example after `web revoke`. A phone or
another machine pairs with the reduced remote grant from a paired console's PAIR
A PHONE button. The loopback listener itself mints nothing.

Pairing, inspection and revocation run through the operator client, so those
commands need the socket and token exported:

```sh
export DARK_FACTORY_SOCKET="$HOME/.dark-factory/runtimes/factory.sock"
export DARK_FACTORY_OPERATOR_TOKEN_FILE="$HOME/.dark-factory/operator.token"
factoryctl web status
factoryctl web pair
factoryctl web list-clients
factoryctl web revoke CLIENT_ID --revision REVISION
factoryctl remote status
```

The CLI cannot delete origin-scoped browser storage; pairing afresh with
`factoryctl web pair` makes that manual browser action unnecessary.

## Start your first worker

After pairing the browser, use the CLI once to create a project and its first
worker. A signed-in `codex` CLI must be installed where the managed daemon can
find it; see [provider discovery](providers.md). From an existing committed
Git checkout, run:

```sh
export DARK_FACTORY_SOCKET="$HOME/.dark-factory/runtimes/factory.sock"
export DARK_FACTORY_OPERATOR_TOKEN_FILE="$HOME/.dark-factory/operator.token"
factoryctl dispatch on
factoryctl project create --name "My project" --root "$PWD"
```

Copy the returned project ID into `PROJECT_ID` below. Creating the project also
registers its checkout as the initial repository. Then copy the returned agent
ID into `AGENT_ID`:

```sh
factoryctl agent create --project PROJECT_ID --name builder \
  --provider codex --tool-budget 100
factoryctl task add --project PROJECT_ID --agent AGENT_ID \
  --title "Improve one documented setup step" \
  --body "Read README.md and the project layout. Correct one concise setup or contributor instruction supported by the code, run git diff --check, and report the files changed."
```

Select **builder** on the paired factory floor to watch its terminal and inspect
its result. Send feedback or queue the next instruction from that panel.
`factoryctl account discover` and `factoryctl account list` help inspect a
missing Codex login. Settings → Repositories also supports project creation and
additional checkouts; worker creation currently uses the CLI.

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
when the original issue author is trusted. Existing failed or completed work is
not automatically retried.

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

Priority mappings support at most 25 labels and 2 KiB of JSON after escaping.
