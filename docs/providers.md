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

Shell and Codex are proven end to end. The Claude Code launch path is
fixture-proven in the current source; a live run against its
signed-in CLI remains required before it is included in a release.

## Create an agent

Provider choice is explicit:

```sh
factoryctl agent create --project PROJECT_ID --name worker --provider shell --tool-budget 100
factoryctl agent create --project PROJECT_ID --name worker --provider codex --model MODEL --reasoning-effort medium --tool-budget 100
```

`--model` and `--reasoning-effort` are optional for native providers and are
rejected for `shell`. Claude Code accepts `low`, `medium`, `high`, `xhigh`, or
`max`; Codex additionally accepts `ultra`.

`--role orchestrator` names an overseer. A worker's run materializes a Change
of the project and works there; an orchestrator's run binds no Change and is
given its private runtime home as its working directory, from which it reads
what workers retained and publishes through the Maintainer App. Neither role
is confined beyond that: both run as the operator with the authority the
environment section below describes. A Claude Code orchestrator is launched
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

An explicit `factoryd --tool-path` replaces the default. Search is
ordered; an existing candidate that cannot be resolved and committed fails
closed rather than falling through to another executable. A symlink is resolved
once and the direct Mach-O target is committed and reverified before exec. The
Maintainer bridge an orchestrator is given is found on the same path but only
checked, not committed, as described under agent creation.

The native argv templates are:

```text
claude --dangerously-skip-permissions [--model MODEL] [--effort EFFORT] --strict-mcp-config [--mcp-config '{"mcpServers":{"maintainer":{"command":"BRIDGE"}}}']
codex --strict-config --no-alt-screen -c check_for_update_on_startup=false -c tool_output_token_limit=32768 -c 'projects={"CHANGE-DIRECTORY"={trust_level="untrusted"}}' -c 'default_permissions="RUNTIME-PROFILE"' -c 'approval_policy="never"' -c 'permissions.RUNTIME-PROFILE=DERIVED-PROFILE' [--model MODEL] [-c 'model_reasoning_effort="EFFORT"'] 'FIXED BOOTSTRAP INSTRUCTION'
```

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
private runtime home and temp directory are writable; the exact provider
executable, factoryctl, attempt token and socket are readable. Other file
access is denied except Codex's minimal platform/runtime paths, including its
temp exceptions. Command escalation is disabled. The profile name is derived from the private runtime home, avoiding shared
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
body wins, with title used only when a native task has no body. Codex task text
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
