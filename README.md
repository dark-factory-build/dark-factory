# Dark Factory

**An autonomous software factory you can see and steer.** Give your coding
agents work. Let an overseer coordinate them. Watch the live floor, inspect
results, and step in when decisions need you.

<picture>
  <source media="(max-width: 600px)" srcset="docs/assets/factory-floor-demo-mobile.png">
  <img src="docs/assets/factory-floor-demo.png" alt="Dark Factory floor demo with an open Needs You request">
</picture>

*Demo: a live codebase topology rendered with sample workers and a synthetic Needs You request; no daemon is connected.*

Dark Factory keeps work in a durable queue and starts each task’s configured
local provider. An overseer directs follow-up work, workers leave reviewable
results, and the Maintainer publishes approved work through the configured
GitHub route. The paired browser and `factoryctl` steer the same factory.

[Website](https://www.darkfactory.build) · [Console](https://app.darkfactory.build) ·
[Backlog](https://github.com/dark-factory-build/dark-factory/issues) ·
[Install](docs/install.md) · [Providers](docs/providers.md) ·
[Architecture](ARCHITECTURE.md) · [Security](SECURITY.md)

## What you get

- Close the browser and the factory keeps its queue and running work alive while the host Mac stays awake.
- Give a task to a named worker or let the next available worker take it.
- Let an overseer turn a goal into follow-up work and coordinate the workers.
- Open a live terminal, read a Needs You request, and decide when to step in.
- Inspect a completed result, send it back with feedback, or take it forward for review.
- Pair a phone to watch and direct the same factory away from the Mac.

## How work moves

Choose a checkout, describe the outcome, add workers with accounts on the
machine, then queue work. Dark Factory makes an isolated worktree and starts
the selected provider. The floor shows workers and tasks. Needs You requests
decisions; otherwise the overseer continues. Review completed changes, send
them back, or let the Maintainer publish them.

## Requirements and quick start

Dark Factory **v0.3.5** runs on macOS with Git. Install and sign in to the
Codex CLI (`codex`) before using Codex. Shell and Codex are proven end to end;
Claude Code launches in fixtures, but a live Claude run is unproven. Providers
may receive source and task material; read [provider support](docs/providers.md)
before connecting an account.

Install the v0.3.5 binaries with the [installation guide](docs/install.md),
then initialise a private home, install the service, and set the operator
environment used by `factoryctl`:

```sh
factoryctl init --home "$HOME/.dark-factory"
factoryctl service install --home "$HOME/.dark-factory"
factoryctl service status --home "$HOME/.dark-factory"
export DARK_FACTORY_SOCKET="$HOME/.dark-factory/runtimes/factory.sock"
export DARK_FACTORY_OPERATOR_TOKEN_FILE="$HOME/.dark-factory/operator.token"
factoryctl web status
factoryctl web open
```

The released v0.3.5 setup creates its built-in shell worker. `project create`
prints a JSON `id`; use it as `PROJECT_ID`. `agent create` prints another JSON
`id`; use it as `AGENT_ID`—the released CLI requires a named agent, not `any`.

```sh
factoryctl project create --name "My project" --root "$PWD"
factoryctl agent create --project PROJECT_ID --name builder --tool-budget 100
factoryctl task add --project PROJECT_ID --agent AGENT_ID \
  --title "Describe the next change"
```

`factoryctl web open` opens the paired console for the installed host. Update
that host if a current control is unavailable. Keep the CLI for repeatable
operations. Issue intake and its managed setup are coordinated for the next
release. More detail is in the [development workflow](docs/development/WORKFLOW.md).

Dark Factory is MIT licensed.
