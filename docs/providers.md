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

Shell is proven end to end. The native launch paths are fixture-proven in the
current source; a separately approved run against each signed-in CLI remains
required before they are included in a release.

## Create an agent

Provider choice is explicit:

```sh
factoryctl agent create --project PROJECT_ID --name worker --provider shell --tool-budget 100
factoryctl agent create --project PROJECT_ID --name worker --provider codex --model MODEL --reasoning-effort medium --tool-budget 100
```

`--model` and `--reasoning-effort` are optional for native providers and are
rejected for `shell`. Claude Code accepts `low`, `medium`, `high`, `xhigh`, or
`max`; Codex additionally accepts `ultra`.

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
once and the direct Mach-O target is committed and reverified before exec.

The native argv templates are:

```text
claude --dangerously-skip-permissions [--model MODEL] [--effort EFFORT]
codex --dangerously-bypass-approvals-and-sandbox --no-alt-screen [--model MODEL] [-c 'model_reasoning_effort="EFFORT"']
```

Install and sign in to the chosen CLI through its normal local workflow before
dispatching work. The provider still receives a private runtime `HOME` and
`TMPDIR`; Dark Factory points only that provider at its existing account
configuration:

```text
CLAUDE_CONFIG_DIR=<account-home>/.claude
CODEX_HOME=<account-home>/.codex
```

No provider API key is copied into the environment. The native process runs as
the operator with unrestricted interactive authority and may use that account's
normal configuration or Keychain access.

Native providers do not inherit the shell task descriptor. The runner writes
one fixed instruction plus the JSON-quoted task to the PTY after provider exec
and before reporting the terminal ready. The complete prepared input must fit
8 KiB; a partial or uncertain write fails the attempt and is never replayed.
Subsequent browser terminal input goes directly to the same PTY. The provider
reports its durable outcome through the attempt-scoped `factoryctl` supplied by
the daemon.

Provider changes must preserve admission-time selection, daemon-owned process
lifecycle, exact task delivery, and deterministic failure when a required
launch fact is unavailable.
