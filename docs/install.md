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

The archive also contains optional host controller assets under
`libexec/dark-factory/`. Run `factory-autonomy.py` with an operator-owned JSON
configuration to schedule intake and review; schedule its separate
`--release-only` pass when delivery is wanted. The companion scripts and
`supervision.md` stay together there so the controller works after the archive
is moved away from the source checkout. Homebrew installs the same directory
under its formula `libexec` path. The controller scripts require Python 3 on
`PATH`; they do not require a Dark Factory source checkout.
If review scheduling is enabled with `review_mirror_root`, the host also needs
`git`, the selected Codex or Claude provider, and the operator-installed
Maintainer bridge on `PATH`; those host tools and credentials are not bundled.

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

## GitHub connection (next release)

This setup requires the release containing the GitHub connection commands and
an activated Maintainer connection endpoint. It is not available in older
archives. Use the operator socket and token exports above; these identify your
local factory, not someone else's GitHub account.

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

## Project repositories (next release)

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
