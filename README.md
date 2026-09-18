# Dark Factory

**An autonomous software factory you can see and steer.** Give your coding
agents work. Let an overseer coordinate them. Watch the live floor, inspect
results, and step in when decisions need you.

<picture>
  <source media="(max-width: 600px)" srcset="docs/assets/factory-floor-demo-mobile.png">
  <img src="docs/assets/factory-floor-demo.png" alt="Dark Factory floor demo with an open Needs You request">
</picture>

*Demo: a live codebase topology rendered with sample workers and a synthetic Needs You request; no daemon is connected.*

Dark Factory keeps the programme's work in a durable queue and starts the local
provider you configured for each task. An overseer can direct follow-up work,
workers leave reviewable results, and the Maintainer publishes approved work
through the configured GitHub route. The paired browser and `factoryctl` steer
the same factory without becoming its single point of failure.

[Website](https://www.darkfactory.build) · [Console](https://app.darkfactory.build) ·
[Backlog](https://github.com/dark-factory-build/dark-factory/issues) ·
[Install](docs/install.md) · [Providers](docs/providers.md) ·
[Architecture](ARCHITECTURE.md) · [Security](SECURITY.md)

## What you get

- Close the browser and the factory keeps its queue and running work alive.
- Give a task to a named worker or let the next available worker take it.
- Let an overseer turn a goal into follow-up work and coordinate the workers.
- Open a live terminal, read a Needs You request, and decide when to step in.
- Inspect a completed result, send it back with feedback, or take it forward for review.
- Pair a phone to watch and direct the same factory away from the Mac.

## How work moves

Choose a checkout and describe the outcome. Add workers with provider accounts
already present on the machine, then put work into the queue. Dark Factory
creates an isolated worktree for accepted work and starts its selected provider.
The floor shows the workers and their current tasks. When the factory needs a
human decision, it asks in Needs You; otherwise the overseer can continue the
programme. Review the finished change, send it back if needed, or let the
Maintainer publish the approved result.

## Requirements and quick start

Dark Factory **v0.3.5** runs on macOS and needs Git plus a signed-in provider
account. Shell and Codex are proven end to end. Claude Code's launch path is
fixture-proven, and a live Claude provider run is still unproven. Providers may
receive source and task material needed to perform the work; read the
[provider support](docs/providers.md) before connecting an account.

Install the current binaries with the [installation guide](docs/install.md),
then initialise a private home and set the operator environment used by
`factoryctl`:

```sh
factoryctl init --home "$HOME/.dark-factory"
factoryctl service install --home "$HOME/.dark-factory"
export DARK_FACTORY_SOCKET="$HOME/.dark-factory/runtimes/factory.sock"
export DARK_FACTORY_OPERATOR_TOKEN_FILE="$HOME/.dark-factory/operator.token"
```

Create a project from the checkout you want to supervise. The command prints a
project ID; use it to add a worker and its first task.

```sh
factoryctl project create --name "My project" --root "$PWD"
factoryctl agent create --project PROJECT_ID --name builder --provider codex \
  --tool-budget 100
factoryctl task add --project PROJECT_ID --agent any --title "Describe the next change"
```

The installation starts `factoryd` and opens the local pairing page at
<http://127.0.0.1:43123/pair>. Pair the console to observe and direct the
factory; keep the CLI for setup and repeatable operations. Issue intake and its
managed setup are coordinated for the next release. More technical detail is in
the [development workflow](docs/development/WORKFLOW.md).

Dark Factory is MIT licensed.
