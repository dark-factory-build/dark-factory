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

The archive includes the intake controller under `libexec/dark-factory/`.
Keep that directory with the release binaries, or use Homebrew's installed
commands. **Starting with v0.4.1**, `factoryctl service install` also schedules
the packaged intake controller. On v0.4.0, run `factoryctl intake service install
--home "$HOME/.dark-factory"` separately. Python 3.9 or newer is required;
Homebrew installs it. No operator JSON, handwritten launchd file, or source
checkout is needed. See the managed intake and explicit legacy migration
instructions below.

Create and install one managed home. Those two commands are the whole terminal
side of setup: an install that starts a fresh service loads the launchd job,
waits for the daemon to listen, and opens <http://127.0.0.1:43123/pair> in this
machine's default browser, where confirming pairs that browser. A repeated
install returns the service it found and opens nothing. `service start` is the
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

Pairing never needs the terminal. Another browser on this machine pairs from the
console's PAIR THIS BROWSER link, which goes to
<http://127.0.0.1:43123/pair>; the daemon serves that one first-party page and
nothing else. Pairing again there is also how a browser replaces a saved
credential this daemon no longer accepts.

Inspection and revocation still run through the operator client, so those
commands need the socket and token exported:

```sh
export DARK_FACTORY_SOCKET="$HOME/.dark-factory/runtimes/factory.sock"
export DARK_FACTORY_OPERATOR_TOKEN_FILE="$HOME/.dark-factory/operator.token"
factoryctl web status
factoryctl web list-clients
factoryctl web revoke CLIENT_ID --revision REVISION
factoryctl remote status
```

The CLI cannot delete origin-scoped browser storage; pairing afresh from the
pair page makes that manual browser action unnecessary.

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
returns each private binding and its revision. Add an existing checkout with
`factoryctl project repository add --id HEX32 --project PROJECT_ID --name NAME
--root ABSOLUTE_PATH --base REF`, using a fresh 32-character lowercase hexadecimal
ID and that repository's actual base/upstream ref. Registration verifies the
checkout, Git directory and configured publication origin. Use the `name`,
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
retarget existing work. With `HEAD` on a branch that has a configured remote
upstream, a fresh Change fetches and pins that upstream's exact commit. A
`refs/remotes/upstream/main` base selects a different remote branch. Detached
HEAD, branches without an upstream, and explicit local refs or commit IDs stay
local. A configured fetch failure stops source preparation rather than using
a stale tracking ref. Fetching does not move the registered checkout or update
tracking refs or `FETCH_HEAD`. Retained Changes keep their original source and
edits; use the repository `base` setting for future work in that binding. Git
runs noninteractively with a private home and global/system configuration
disabled. Private remotes use repository-local authentication; missing
credentials fail preparation rather than borrowing a worker account.

## Reviewed issue intake (v0.4.0+)

These commands require v0.4.0 or later and an activated GitHub connection.
GitHub holds the backlog; the factory holds execution and acceptance. An issue
repository can feed a different registered code repository. Delegating GitHub
access alone does not subscribe to issues or start work.

Use `factoryctl intake list --project PROJECT_ID` to inspect sources. Create a
source with `factoryctl intake create --source HEX32 --project PROJECT_ID
--configuration JSON`; choose a fresh 32-character lowercase hexadecimal source
ID. The JSON fields are `repository` (`owner/backlog`), `target_repository_id`,
`overseer_agent_id`, `label` (empty for no filter), `policy` (`manual` or
`trusted_authors`), `trusted_authors` (an array of GitHub logins), `poll_seconds`
and `admission_limit`. The destination and overseer must belong to that project.
`@me` in trusted authors resolves your connected GitHub identity. A trusted-author
policy permits initial acceptance by those authors **or** explicit operator
acceptance; a label is only a filter. New sources start paused.

Run `factoryctl intake preview --source SOURCE_ID --page 1`, following
`next_page`, and inspect the title, body, destination and eligibility reasons.
Enable the reviewed configuration with `factoryctl intake enable --source
SOURCE_ID --revision REVISION --reviewed-revision REVISION`. An update uses
`intake update` with the full configuration and current revision; it pauses the
source so you can preview the change before enabling it.

Accept the exact displayed content using `factoryctl intake accept --source
SOURCE_ID --revision REVISION --issue NUMBER --hash CONTENT_HASH`. The daemon
checks the current GitHub content again before recording acceptance. The
controller imports accepted work into the existing queue; comments and reactions
do not create work. A later title/body edit needs fresh acceptance, including
when the original issue author is trusted. Existing failed or completed work is
not automatically retried.

Starting with v0.4.1, installing the managed service from a release or Homebrew
also schedules the packaged controller. `factoryctl service status --home
"$HOME/.dark-factory"` reports whether intake is scheduled, absent, or
unavailable. A source-built CLI without packaged assets reports intake as
unavailable while the daemon remains usable; use a release archive or Homebrew
for issue intake.

If controller setup fails after the daemon starts, installation reports the
failure and the repair command: `factoryctl intake service install --home
"$HOME/.dark-factory"`. Use that same command for v0.4.0's separate setup step;
`status` and `uninstall` use the same home argument. In v0.4.1, managed-service
uninstall removes its scheduled controller first and refuses to remove the
daemon if safe controller removal fails. Existing legacy schedules still need
explicit migration; setup never silently replaces them.

Python 3.9 or newer is required (Homebrew installs it). The controller owns its
private journal and sync status beside the factory home; back up that directory
with the home.
`intake list` reports the last successful sync and current error. An unavailable
GitHub connection is not an empty backlog.

`factoryctl intake pause --source SOURCE_ID --revision REVISION` stops new
imports, not existing work. `factoryctl intake withdraw --acceptance ID`
withdraws that approval, cancels linked queued work and requests the existing
stop mechanism for running work. `withdrawal_pending` requires reconciliation;
it does not promise that an offline host or running process has stopped.

Existing legacy controllers remain supported until explicit cutover. Managed
installation refuses another legacy intake schedule for the same factory home.
It leaves that schedule, its configuration and journal untouched. Release-only
schedules and other factory homes remain independent.

To migrate a generated legacy intake schedule, use the installed release's
`factoryctl` with its packaged controller and Python 3.9 or newer:

```sh
factoryctl intake service migrate --home "$HOME/.dark-factory" \
  --legacy-config /absolute/path/to/existing-intake.json --preview
factoryctl intake service migrate --home "$HOME/.dark-factory" \
  --legacy-config /absolute/path/to/existing-intake.json --plan REVIEWED_PLAN_HASH
```

Preview reads the existing configuration and version-2 journal, verifies current
GitHub identities through the connected Maintainer, and shows historical task
links, unresolved history, configuration and policy changes. It changes no
schedule or journal. Preview resolves and freezes the actual project default
repository as the destination; a default change requires a new preview. Mixed
historical destinations are refused explicitly. The source preserves the label
filter and trusted human authors,
poll interval, default priority and maximum matching label priority. Priority
label changes update the same queued task, without new acceptance or recovery.
Review companion settings and its operation journal are retained. After cutover,
the companion uses the installed private operator adapter and the connected
Maintainer for PR reads, independent review, enqueue and merge observation.
It keeps the source issue repository separate from the frozen publication
destination and uses each PR's actual base branch. Preview requires that
publication destination to be bound and delegated and its configured Git review
mirror to be ready before stopping the old schedule. Git fetch uses the
operator's existing Git authentication; no broker credential is copied.

The independent reviewer sees only status, observation of its own operation,
and submission for its exact repository, PR, head and review UUID. Disconnect,
expiry or withdrawal refuses new publication through live host checks; it never
falls back to `gh` or the old owner bridge. A standalone old companion stops
after customer opt-in. Existing legacy broker receipts require explicit
administrative transfer with their original request proof before the customer
connection can observe them; their UUIDs and stored outcomes remain unchanged.
Initial customer connection waits until an existing controller/review pass
releases its ownership lock; finish or stop that pass and retry Connect.
A legacy configuration with `release_configs` cannot enter customer cutover yet:
its release pass still uses the host's `gh` identity. Preview and apply refuse
before stopping either schedule or changing the journal. Keep the existing
release-only pass under its explicit operator configuration until a
customer-scoped release path is available.

Legacy `app/…` and `…[bot]` author entries are listed as a policy narrowing in
preview. Already imported work and receipts are preserved. Future bot-authored
issues require explicit manual acceptance; App authorship never establishes
human approval. Apply a plan containing those entries with the additional
`--acknowledge-policy-narrowing` flag. This acknowledgement is recorded in the
reviewed cutover receipt, so an interrupted cutover can resume with the same
plan. An app-only legacy author list becomes a manual source.

All current matching backlog and journal-tracked issues become suppression
receipts, **not** accepted content. The version-2 journal has no first-sync
cutoff; migration does not invent one. Unprocessed baseline issues need explicit
acceptance. An unchanged processed issue returns its existing task, or an
unresolved-history explanation when the old bytes cannot be proved. Use the
existing task controls to retry failed work. A reviewed title/body edit may be
accepted as new work; comments, reactions and label edits cannot create it.
The automatic trusted-author policy applies to new issues after cutover.

Legacy workers predate durable intake lineage, so cutover requires the entire
project to have no queued, running or blocked tasks or nonterminal runs. Use
the existing task controls to finish or cancel work; migration does not stop
that work on your behalf. Priority mappings support at most 25 labels and 2 KiB of JSON after escaping.
Cutover also refuses uncertain legacy work, changed task
incarnations, unsupported configuration and more than 200 baseline issues or
journal entries. This bounded migration returns an explicit overflow instead
of accepting a partial backlog. Settle the reported legacy work before retrying.
The kernel commits the paused source, canonical suppression receipts and
migration receipt atomically. The controller archives the exact old files,
stops only the proven old launchd job, removes its restart plist, and starts one
managed schedule. An originally running source is enabled only after that
baseline exists; an originally stopped source remains paused.

After an interruption, run the same command with the same plan hash. The private
`HOME.intake/migration.json` receipt resumes the recorded phase without starting
both controllers or repeating imported work. An ambiguous daemon reply leaves
legacy stopped and the new source paused until the exact receipt is recovered.
If GitHub content changed before commit, preview again and explicitly apply the
new plan hash; the old controller stays stopped during that review. Operator
source edits during cutover are preserved and require operator resolution.
Back up `HOME.intake` and the existing journal alongside the factory home. Do not
restore an older runtime that lacks these suppression and ownership boundaries
once a migration has committed.

The retained review companion reads new managed issue lineage through the private
operator API using the migration's frozen repository and destination. It requires
an imported, nonwithdrawn acceptance and the PR's completed publication receipt;
human-authored source issues need no App-created issue marker. The old intake
journal remains unchanged, and editing the source settings does not retarget
reviews of retained work.
