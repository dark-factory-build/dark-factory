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

Shell and Codex are proven end to end. A Claude Code worker is proven by one
live run against its signed-in CLI; a Claude Code overseer is fixture-proven
only.

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

Overseers keep enduring acceptance criteria, prerequisites and owner-authority
clarifications in the task's complete base instruction using
`overseer task update --body` while it is queued, preserving its original
acceptance criteria. A send-back replaces the previous feedback; use its note
for the latest findings or pointers, not enduring requirements. See
[the queued correction procedure](development/OVERSEER.md).

`--role orchestrator` names an overseer. A worker's run makes a Change of
the project, a linked Git worktree on the Change's branch, and works there;
an orchestrator's run binds no Change and is given its private runtime home
as its working directory. It requests a worker's settled Change explicitly
with `factoryctl attempt source --task TASK_ID`. The daemon checks that target
task in the authenticated attempt's same project, grants a worker only the
target its own `review handoff` first line names, requires its current settled
retained Change (including blocked, failed, or cancelled outcomes), verifies
the worktree is still at the settled head, and returns the Change ID, base
commit, `head_commit`, `branch`, target task ID, task work revision, current
Change revision, the worktree as `source_path`, the Change's actual Git directory
as `git_directory`, and whether the worktree holds uncommitted work. The
directory is private for new Changes and may be canonical for retained legacy
worktrees, which are not automatically converted. This changes ordinary Git
state ownership, not provider permissions or arbitrary-path access. The
branch head is the work: read it with `git --git-dir=$git_directory`. An
accepted response without that receipt is unusable; never reconstruct a path
or select a project-latest tree. A Codex orchestrator's local commands are
granted the repository's Git directory read-only; they do not receive the
daemon database, the Changes parent, or the daemon home. A later work
revision or Change revision, or a branch that moved since settlement, is
refused.
overseer publishes through the Maintainer App. A Claude Code orchestrator is launched
with that App's MCP bridge, `dark-factory-maintainer-mcp-bridge` resolved on
the fixed tool path, beside the same `factory_attempt` server (`factoryctl
attempt mcp`) a Codex orchestrator already has. A Claude Code worker is launched
with `--strict-mcp-config` and exactly one factory-control MCP server,
`factory_attempt` (`factoryctl attempt mcp`). If the optional installed
browser bridge is present, the worker also receives `factory_browser`; it is a
separate browser capability, not a factory-control server. Nothing else in
the account configuration or in a `.mcp.json` inside the Change reaches it.
The factory-attempt MCP child inherits
`DARK_FACTORY_SOCKET`, `DARK_FACTORY_ATTEMPT_TOKEN_FILE`, and
`DARK_FACTORY_FACTORYCTL`, so every request is authenticated as the live
attempt. The server exposes only the documented attempt/overseer command
allowlist, and the daemon still enforces the worker's task, project, role,
provider, run-state, and source-receipt checks; it is not a general operator
API or a filesystem relay. The bridge
is found on the same ordered path as a CLI but is not committed like one,
since Claude spawns it itself much later and it may be a script: it must be
a regular file, executable by its owner and writable by nobody else, and an
orchestrator launch is refused, naming which, when it is missing or fails
that. Claude workers still have the provider's existing local-command
authority; this MCP boundary does not create a hostile same-user sandbox.
Every Codex launch receives the same `factory_attempt` server, and a Codex
orchestrator receives the Maintainer server beside it.

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

### Native session persistence

A worker's Claude Code launch adds `--session-id UUID` or `--resume UUID`
right after `--permission-mode dontAsk`. The CLI keys a conversation's
own transcript by the exact launch directory under its effective
configuration directory (`<config-home>/projects/<escaped-cwd>/<uuid>.jsonl`,
where every byte of the directory outside `A-Za-z0-9` becomes `-`, so
`~/.dark-factory/changes/ID` is `-Users-op--dark-factory-changes-ID`; the
escape must match the CLI exactly, because a `--session-id` the CLI already
knows is refused as already in use and the provider exits 1 before any
attempt outcome):
`HOME/.claude` by default, or a linked account's own directory when one is
selected, exactly the directory the launch environment names
`CLAUDE_CONFIG_DIR` (`claudeConfigHome` in `internal/provider/provider.go` is
the one place this is computed, shared by the launch environment and by
session discovery so they can never disagree); a worker's launch directory is
its task incarnation's Change worktree, which a send-back retry reuses (see
`internal/kernel/change.go`), so the same UUID keeps a correction in the same
native conversation instead of a fresh one that only repeats the brief. The
UUID is derived (UUID v5, RFC 4122) from provider, agent ID and
task incarnation ID, so no extra state records which session belongs to which
task, and `--resume` is chosen only when that exact transcript file already
exists on disk; a first attempt, or a transcript past the shared rotation
ceiling in `internal/provider/provider.go` (`nativeSessionRotateBytes`), gets
a fresh `--session-id` instead. This does not apply to a Claude Code
orchestrator launch: its working directory is a fresh runtime root on every
run (see `internal/daemon/supervisor_darwin.go` and
`internal/changeworker/worker_darwin.go`), so no chosen ID could ever be found
again; giving the orchestrator role a stable per-agent working directory
across runs is a separate, larger change, so a Claude orchestrator's launch
argv is unaffected.

Codex offers no way to choose or name a session's ID at creation (no
`--session-id`/`--name` flag or config key on `codex`, `codex exec`, or any
subcommand), so a Codex worker or orchestrator launch instead discovers an
already-recorded session and resumes it with a leading `codex resume
SESSION_ID` (all of it added before the same `-c` overrides the launch always
carries; `codex resume [OPTIONS] [SESSION_ID] [PROMPT]` accepts every one of
them identically to bare `codex`, confirmed from its own `--help`). Every
launch also carries the supported `tui.resume_cwd="current"` override. This
binds an explicit resume to the current daemon-authorized attempt directory,
so a session recorded by an earlier runtime cannot stop at Codex's interactive
working-directory chooser. Codex's
own rollout files live under `CODEX_HOME/sessions/YYYY/MM/DD/rollout-<timestamp>-<uuid>.jsonl`,
bucketed by wall-clock date rather than launch directory; each one's first
JSON line records `payload.cwd` and `payload.id` (the resumable session id;
for an ordinary, non-subagent Dark Factory launch this equals the filename's
own trailing UUID and Codex's `session_id`, so a rollout's own filename is
never trusted alone). `codexSessionSelection` walks day directories newest
first, bounded to the newest 30 days (`maxCodexScanDays`), and within a day
reads only each rollout's bounded first line looking for the newest one whose
`cwd` matches; the newest match is resumed while under the same
`nativeSessionRotateBytes` ceiling, otherwise the launch is a fresh one
(unchanged argv), never falling back to an older, smaller match for the same
cwd. A worker's launch cwd is its retained Change worktree (as for Claude),
so a send-back retry finds its own rollout directly. An orchestrator's launch
cwd is a fresh runtime root every run like Claude's, but `codex resume
SESSION_ID` does not require the resuming launch's own cwd to match (cwd
filtering is a convenience of the interactive picker and `--last`, which
`resume --all` exists to disable; an explicit SESSION_ID resolves regardless
of cwd), so Build instead searches by `previousWorkingDirectory`: the same
agent's most recently terminal run's own working directory. That path is
found from `kernel.Store.LatestTerminalRuntimeRoot`, a minimal read joining
`runs` and `resources` for the newest terminal run's `runtime_root` resource
path (retained in that row after release, so it stays readable long after the
directory itself is reclaimed), joined with `changeworker.HomeName`
(`.../home`, the exact directory Codex was launched in) and threaded through
`changeworker.Config.PreviousWorkingDirectory` and
`provider.Request.WithPreviousWorkingDirectory`. This is how an overseer's
distinct standing tasks share one continuing Codex context despite each one's
own fresh runtime root. A first-ever orchestrator run, or one whose prior
Codex session cannot be found, launches unaffected, the same as a worker's
first attempt.

Codex receives the daemon-authorized Change worktree as an invocation-only
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
With the default Darwin tool path, factoryd derives the pinned Node
`v22.20.0` bin directory, Cargo's bin directory, a pinned Homebrew Go's
canonical `libexec/bin` directory, and the selected account's Rustup metadata
when those canonical directories exist. The Homebrew `opt`/`Cellar` symlink
chain is outside the sandbox's read grant, so the tool path uses the
resolved `libexec/bin` directly rather than the usual `/opt/homebrew/bin`
entry point. An explicit `--tool-path` or `--toolchain-read-roots` remains
authoritative.
The paths must be canonical existing directories owned by the daemon user,
or the exact root-owned `/Library/Developer/CommandLineTools` installation,
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
Claude Code receives the same grants through `--settings` (`claudeSettings`):
`dontAsk` refuses any tool call no rule allows, which bounds its file tools,
and its OS sandbox bounds Bash, fails closed when it cannot start, refuses the
unsandboxed escape, and cannot read where user data lives (`/Users`,
`/Volumes`, `/Network`, `/private/tmp`, `/private/var/folders`,
`/private/var/root`) outside the granted paths and the CLI's own scratch
directory. System locations stay readable, as under Codex's minimal profile;
denying every read crashes ordinary tools. `--setting-sources ""` stops user,
project and local Claude settings from merging rules that widen this. The
provider process and MCP servers remain outside this boundary, as for Codex.
Sandboxed Bash cannot reach the attempt API, so every Claude launch carries the
`factory_attempt` stdio tool Codex already uses, and its task lead names it.
A live worker run on CLI 2.1.278 (20 September 2026, isolated home, a
repository whose own `.claude/settings.json` tried to allow its parent) edited
and committed in its Change, was refused reads of a sentinel under `/Users` and
under `/private/tmp` and writes outside its grants, from Bash and from the file
tools, reached the network, and settled `succeeded` through the tool. Git's
xcrun cache and Go telemetry warn that they cannot write outside the grants. A Claude overseer has not had a live run.

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
write of the text fails the attempt and is never replayed. A daemon-delivered
human reply or overseer message reaches Claude and Codex the same way: the
text as one write, then the runner's own Enter once the output is quiet, so
the CLI submits it instead of holding it in its input box.

Codex starts from a fixed, non-secret positional instruction to run
`factoryctl attempt task` first. That command authenticates with the attempt's
private credential and returns the exact effective task as terminal-safe JSON;
body wins, with title
used only when a native task has no body. Codex task text
is absent from argv, environment, and Change-worker configuration, and is
bounded to 8 KiB so the configured 32,768-token tool-result budget cannot
truncate it even under worst-case control-character escaping. The attempt API
serves it only while that exact run is `running`.

Workers and overseers can exchange one durable, task-linked question and
answer across providers using `factoryctl attempt peer status`, `peer ask`,
and `peer answer`. Questions are asynchronous: a queued recipient reads it
when its attempt starts, and neither command grants task or terminal control.
Terminal notification is adapter-specific; when no live adapter is available,
the durable inbox remains readable. A stale paged status must restart from the
first page.

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

Every provider's local commits and generated project-content commits use the
GitHub operator connected in Settings: their login and GitHub-provided
`ID+LOGIN@users.noreply.github.com` address. The host caches the verified public
identity for offline local work; GitHub status/refresh updates it, including
username changes. A home without a verified connected identity uses the single
fallback `Dark Factory <worker@darkfactory.build>`. Disconnect immediately stops
using the cached operator identity for new work.

Workers still receive no credential helper, SSH command, prompt or `gh`
configuration. The Maintainer App publishes with its installation token and
existing operation provenance, deriving each new commit author from the live
connection user rather than worker arguments. Existing commits, co-authors,
and already-landed publication receipts are preserved across upgrades and
renames. Legacy Access publication without a connected GitHub operator retains
its App author. GitHub's final squash attribution follows repository merge
settings; operator authorship/co-authorship can receive contribution credit
when the commit reaches an eligible branch.

Older GitHub accounts using a username-only noreply address may need to enable
the ID-based address in [GitHub email settings](https://github.com/settings/emails)
for contribution credit; DF does not request access to private email addresses.
