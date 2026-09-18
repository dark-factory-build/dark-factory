# Dark Factory

**Run supervised coding work on your own Mac.** Dark Factory keeps a durable
queue, launches local coding providers, and gives you one calm place to see
what is running, what needs a decision, and what finished. It is for people
who want coding agents to work in real repositories without handing their
projects to a hosted agent platform.

<picture>
  <source media="(max-width: 600px)" srcset="docs/assets/factory-floor-demo-mobile.png">
  <img src="docs/assets/factory-floor-demo.png" alt="Dark Factory floor demo with an open Needs You request">
</picture>

*Demo: a live codebase topology rendered with sample workers and a synthetic Needs You request; no daemon is connected.*

The daemon runs locally. Your browser and `factoryctl` are authenticated local
clients; closing either does not stop queued work. Dark Factory never commits,
pushes, opens pull requests, or publishes a repository for you.

[Install](docs/install.md) · [Provider support](docs/providers.md) ·
[Architecture](ARCHITECTURE.md) · [Security](SECURITY.md) ·
[Development](docs/development/WORKFLOW.md)

## Why use it

- Keep a visible, durable queue while agents work in separate Git worktrees.
- Use Codex or a shell provider today; Claude Code support is available for an
  existing local CLI, with real-provider smoke coverage still pending.
- Pause, message, interrupt, stop, or send work back with a recorded reason.
- See human questions and completed work without exposing private instructions
  in the public floor view.
- Assign work to one worker or let the next eligible worker claim it.
- Keep source checkout, task lifecycle, and provider processes on your Mac.

## A typical workflow

Create a project from an existing checkout, add a worker, and open the paired
console. Put an instruction in a ready worker’s terminal pane or add a task
from the CLI. The daemon creates a fresh worktree for an admitted task and
starts the configured provider. Watch the floor for progress, answer a Needs
You request when one appears, and review the durable outcome when the task
finishes. You decide what happens to the resulting branch.

## Quick start

Dark Factory **v0.3.5** is macOS-only. Install the current binaries using the
[installation guide](docs/install.md), then initialise a private home and set
the operator environment used by `factoryctl`:

```sh
factoryctl init --home "$HOME/.dark-factory"
factoryctl service install --home "$HOME/.dark-factory"
export DARK_FACTORY_SOCKET="$HOME/.dark-factory/runtimes/factory.sock"
export DARK_FACTORY_OPERATOR_TOKEN_FILE="$HOME/.dark-factory/operator.token"
```

Register the checkout you want to supervise. `factoryctl project create` prints
the project ID; use that value below.

```sh
factoryctl project create --name "My project" --root "$PWD"
factoryctl agent create --project PROJECT_ID --name builder --provider codex \
  --tool-budget 100
factoryctl task add --project PROJECT_ID --agent any --title "Describe the next change"
```

Follow [installation](docs/install.md) to install and start `factoryd`; it
opens the local pairing page at <http://127.0.0.1:43123/pair>. The console is
for observing and directing work, while the CLI remains useful for setup and
repeatable operations.

Repository routing, issue intake, and release scheduling are coordinated for
the next release and are not part of v0.3.5’s public quick start. Technical
operation details remain in the linked documentation.

Dark Factory is MIT licensed.
