# Provider contract

Dark Factory implements `shell`, `claude_code`, and `codex` through one closed
Go boundary:

```go
func Build(Request) (Launch, error)
```

`Build` returns launch facts only. The daemon and runner own the Change working
directory, task input, PTY, process group, output, wait, and cleanup. A provider
cannot select a source path or lifecycle result, and there is no registry,
plugin, fallback, or provider-owned supervision framework.

Cleanup reaches only the runner-owned provider process group while its leader
is unreaped. A provider must not detach command children into another group:
the runtime has no authority to signal those PIDs, and process ancestry, cwd,
or a matching birth record do not create that authority. Native Codex currently
has no provider-owned shutdown capability exposed through this contract.

Shell and Codex are proven end to end. The Claude Code launch path is
fixture-proven in the current source; a live run against its
signed-in CLI remains required before it is included in a release.

## Select an existing provider account

Use `factoryctl account discover` or `factoryctl account list` to inspect existing
provider logins and linked identities. Both return complete paged observations;
linked accounts remain visible with an unavailable reason if their login files
have disappeared. These commands do not start a login flow or export credentials.

```sh
factoryctl account link --provider codex --home /absolute/provider/home --label dogfood
factoryctl agent select-account --agent AGENT_ID --revision REVISION --account ACCOUNT_ID
```

Selection requires an idle worker and its current revision. It changes future
runs; it cannot switch credentials underneath an active run. Use the existing
provider login flow first when the desired account is not discoverable.

## Create an agent

Provider choice is explicit:

```sh
factoryctl agent create --project PROJECT_ID --name worker --provider shell --tool-budget 100
factoryctl agent create --project PROJECT_ID --name worker --provider codex --model MODEL --reasoning-effort medium --tool-budget 100
```

`--model` and `--reasoning-effort` are optional for native providers and are
rejected for `shell`. Claude Code accepts `low`, `medium`, `high`, `xhigh`, or
`max`; Codex additionally accepts `ultra`.

An operator may set the model for an existing worker with its observed
revision:

```sh
factoryctl agent select-model --agent AGENT_ID --revision REVISION --model gpt-5.6-luna --reasoning-effort medium
```

The same provider validation applies. An admitted run retains its immutable
model and effort, so the selection affects only future admissions.

`--role orchestrator` names an overseer. A worker's run materializes a Change
of the project and works there; an orchestrator's run binds no Change and is
given its private runtime home as its working directory. It requests a worker
tree explicitly with `factoryctl attempt source --task TASK_ID`. The daemon
checks that target task in the authenticated attempt's same project, requires
its current settled retained Change (including blocked, failed, or cancelled
outcomes), materializes one private read-only snapshot, and returns the Change ID, base commit, target task ID, task work
revision, current Change revision and daemon-derived `source_path`. An accepted
response without that receipt is unusable; never reconstruct a path or select a
project-latest tree. A Codex launch receives read access only to the private
per-run retained-source root, while the daemon creates only the requested exact
child. It does not receive the daemon database, the Changes parent, or the
daemon home. A later work revision or Change revision is refused against an
older materialization and requires a fresh launch profile. The
daemon drains admitted source materialization before shutting down and
removing the run's private retained-source directory, so cleanup cannot race a
source handoff.
overseer publishes through the Maintainer App. A Claude Code orchestrator is launched
with that App's MCP bridge, `dark-factory-maintainer-mcp-bridge` resolved on
the fixed tool path, as its one MCP server; a Claude Code worker is launched
with `--strict-mcp-config` and no server, so nothing in its account
configuration or in a `.mcp.json` inside the Change reaches it. The bridge
is found on the same ordered path as a CLI but is not committed like one,
since Claude spawns it itself much later and it may be a script: it must be
a regular file, executable by its owner and writable by nobody else, and an
orchestrator launch is refused, naming which, when it is missing or fails
that. Codex orchestrators receive no MCP configuration yet.

## Shell

Shell is fixed to `/bin/sh` with argv `/bin/sh`, `/dev/fd/11`. Its bounded task
is written to that sealed descriptor after the launch gates pass. The PTY is
then available for interactive input.

## Claude Code and Codex

The managed daemon searches this fixed default tool path, never ambient
`PATH`:

```text
~/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin
```

An explicit `factoryd --tool-path` replaces the default; managed installations
accept the same option through `factoryctl service install`. Search is
ordered; an existing candidate that cannot be resolved and committed fails
closed rather than falling through to another executable. A symlink is resolved
once and the direct Mach-O target is committed and reverified before exec. The
Maintainer bridge an orchestrator is given is found on the same path but only
checked, not committed, as described under agent creation.

The launch arguments are defined in `internal/provider/provider.go` and guarded
by the exact-argv checks in `internal/provider/provider_darwin_test.go`. The
configuration and capability boundaries are described below.

Codex receives the daemon-authorized Change directory as an invocation-only
project override with `trust_level="untrusted"`. This suppresses Codex's
interactive directory-trust screen while explicitly refusing project-local
configuration and hooks; the directory is never persisted in Codex config and
the provider cannot choose a different working directory.

Install and sign in to the chosen CLI through its normal local workflow before
dispatching work. Both providers receive a private runtime `TMPDIR`. Claude
uses the operator's normal `HOME`, which is where its CLI keeps the signed-in
account; Codex keeps a private runtime `HOME` and receives its existing account
configuration explicitly:

```text
CODEX_HOME=<account-home>/.codex
```

Codex local commands use a launch-derived permission profile: the Change,
private runtime home and temp directory are writable; current same-project
retained Change trees selected at launch are individually readable; the exact provider
executable, factoryctl, attempt token and socket are readable. Other file
access is denied except Codex's minimal platform/runtime paths, including its
temp exceptions. An optional startup `--toolchain-read-roots` path list adds
read-only access to exact installed software directories (for example one
Node installation including its Corepack libraries, or one Go `libexec`).
This is not inferred from PATH and does not pin every child executable.
The paths must be canonical existing directories owned by the daemon user,
not writable by other users,
and cannot overlap Factory private paths or include account/credential roots.
The managed service install accepts and records the same option; status and
uninstall recover it from the receipt. Changing it requires reinstalling.
Codex Go, Corepack, npm and XDG caches live inside the private runtime home;
operator caches are not inherited. Tool versions and command-specific compiler
or OpenSSL settings remain the task and installation owner's choices.
Command escalation is disabled. The profile name is derived from the private runtime home, avoiding shared
account profile names because Codex merges nested configuration tables. The argv
policy remains bounded by the runner's existing 8 KiB per-argument limit.

This uses Codex permission-profile support tested with CLI0.154.0; strict config
refuses unrecognized configuration rather than silently ignoring it. Account
configuration and explicit model/effort choices remain intact. Network access
is retained; this is a local-command filesystem boundary, not a network policy
or a sandbox for the provider process or MCP servers. Factory Codex launches
and cold reviews disable Codex computer use, browser use and inherited plugins:
plugins can start desktop helpers even when the two built-in tools are disabled.
This does not modify the operator's personal Codex configuration. Explicit MCP
servers, including the Maintainer bridge, remain separate capabilities.
Claude's existing launch has not gained the local-command filesystem boundary.

Interactive Codex workers override `notify=[]` so a personal notification command
cannot launch desktop helpers. They retain account configuration and authentication
through the selected `CODEX_HOME`; this is not blanket configuration isolation.
The exec-only `--ignore-user-config` flag remains confined to cold reviews.
Factory-owned MCP servers and command permissions are supplied explicitly at launch.

## Run-scoped browser tools

An operator can install `scripts/dark-factory-browser-mcp.py` as the executable
`dark-factory-browser-mcp` on the daemon's existing tool path. Both Codex and
Claude then receive the explicit `factory_browser` MCP server; an absent bridge
leaves browser tools unavailable, and an unsafe installed bridge refuses launch.
This reuses Playwright MCP, not Codex desktop control or a personal browser.
The bridge authenticates the live attempt through `factoryctl attempt task`
before starting the server. Chromium starts on the first browser action.

Install a pinned `@playwright/mcp@0.0.80` separately from application dependencies
(`npm install --prefix TOOL_DIRECTORY --ignore-scripts --save-exact
@playwright/mcp@0.0.80`). Install the matching Chromium through Playwright's normal
setup, or explicitly select an existing compatible testing binary. No package or
browser is downloaded during a worker launch. Missing prerequisites fail clearly.

Place a JSON file beside the resolved Python script, replacing `.py` with `.json`.
Use canonical absolute paths to the installed Node executable, MCP `cli.js`, and
browser executable. The script/configuration must be operator-owned and not
writable by other users. For example:

```json
{
  "command": ["/absolute/node", "/absolute/node_modules/@playwright/mcp/cli.js"],
  "executable": "/absolute/testing-browser",
  "origins": ["http://127.0.0.1:5196"],
  "headless": true
}
```

The initial configuration accepts at most eight exact HTTP development origins
on `127.0.0.1`; the standard factory console port is excluded. No wildcard,
personal profile, extension, remote CDP endpoint or saved storage is supplied.
Set `headless` to false for a visible dedicated browser window. Screenshots and
the MCP session record are under the run's private `tmp/browser-*/output`.
Explicit screenshot filenames should use that output directory. Outputs are
live-run evidence, removed with the runtime; this does not introduce permanent
history. Copy only sanitized fixture evidence to the existing development
artifact location before ending a verification run.

The server and browser are provider descendants and use the existing runner
cleanup; no browser process, scheduler, database entity or network listener is
created by the daemon at admission. Retries receive fresh profiles. Browser
helpers receive no provider login or factory credentials in their environment,
and their initial file-access root is the private browser directory.

Limits: Playwright's origin/file guards catch unintended access, but are not a
security boundary; origin lists do not cover redirects or deliberate bypass.
Explicit MCP servers and native providers still run as the operator. This is
session separation, not hostile-code confinement or permission to operate the
live factory. The existing operator-owned `verify-live-browser.mjs` remains the
separate authorized connected-console route. No native desktop-control tool is
provided here.

Run `python3 scripts/test-factory-browser.py` for the configuration checks. With
the real labelled dev fixture running on a configured origin, run
`python3 scripts/test-factory-browser-live.py CONFIG URL OUTPUT_DIRECTORY` for
two-session storage isolation, direct unlisted navigation refusal, independent
closure and desktop/phone screenshots. Use a URL containing `?fixture`.

No provider API key is copied into the environment. The native process still
runs as the operator and may use its normal account or Keychain access. Before a Claude Code launch the Change
worker records the working directory as trusted in that account's
`.claude.json`, the record the CLI's own folder-trust dialog writes; every
Change is a path the CLI has never seen, and without the record the session
would stop at that dialog with the startup task typed into it.

Native providers do not inherit the shell task descriptor. For Claude, the
runner types one fixed instruction plus the terminal-safe JSON-quoted task
into the PTY after provider exec, once the CLI has taken its terminal out of
canonical mode or two seconds have passed, and before reporting the terminal
ready; the carriage return that submits it is the one startup byte sent after
ready, as a keystroke of its own once the CLI's output has been quiet for half
a second (after a one-second floor, or at five seconds regardless), because a
CLI reads text and newline arriving together as a paste, and a paste does not
submit. The complete prepared input must fit 8 KiB; a partial or uncertain
write of the text fails the attempt and is never replayed.

Codex starts from a fixed, non-secret positional instruction to run
`factoryctl attempt task` first. That command authenticates with the attempt's
private credential and returns the exact effective task as terminal-safe JSON;
body wins, with title
used only when a native task has no body. Codex task text
is absent from argv, environment, and Change-worker configuration, and is
bounded to 8 KiB so the configured 32,768-token tool-result budget cannot
truncate it even under worst-case control-character escaping. The attempt API
serves it only while that exact run is `running`.

Codex workers can exchange one durable, task-linked question and answer with
another Codex worker using `factoryctl attempt peer status`, `peer ask`, and
`peer answer`. Questions are asynchronous: a queued recipient reads it when
its attempt starts, and neither command grants task or terminal control. A
stale paged status must restart from the first page.

Subsequent browser terminal input goes directly to the same PTY. The provider
reports its durable outcome through the attempt-scoped `factoryctl` supplied by
the daemon. Before reporting success, a worker removes only generated
dependencies, build output, caches, and temporary metadata it created, then
checks that none remain in its Change.

Provider changes must preserve admission-time selection, daemon-owned process
lifecycle, exact task delivery, and deterministic failure when a required
launch fact is unavailable.

An operator can change an existing worker's model for future runs:

```sh
factoryctl agent select-model --agent AGENT_ID --revision REVISION --model gpt-5.6-luna --reasoning-effort medium
```

Use the current agent revision from `factoryctl status`. The update refuses a stale revision or unsupported provider controls. An already admitted run keeps its model and effort. Omitting effort clears the explicit override for future runs.

Retained source snapshots currently require the Codex read-only local-command
filesystem boundary. Claude and shell source requests return unavailable until
their launch provides equivalent protection; this does not restrict peer
communication or ordinary task execution. Do not substitute a mutable private
copy or claim cross-provider source access is delivered.
