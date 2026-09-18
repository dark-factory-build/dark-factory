# Dark Factory

**Turn a repository backlog into supervised coding work on your own Mac.** Dark
Factory gives a programme a durable queue, local coding providers, and a shared
view of work in progress. It coordinates several agents on real repositories,
keeps decisions visible, and carries completed work through independent review
and publication.

<picture>
  <source media="(max-width: 600px)" srcset="docs/assets/factory-floor-demo-mobile.png">
  <img src="docs/assets/factory-floor-demo.png" alt="Dark Factory floor demo with an open Needs You request">
</picture>

*Demo: a live codebase topology rendered with sample workers and a synthetic Needs You request; no daemon is connected.*

The daemon, queue, worktrees, and providers stay on your machine. A paired
console and `factoryctl` direct the same local factory. The Maintainer handles
publication: it can publish the independently reviewed completed change to a
GitHub branch and pull request, leaving a durable record of what happened.

[Website](https://darkfactory.build) · [Console](https://app.darkfactory.build) ·
[Backlog](https://github.com/dark-factory-build/dark-factory/issues) ·
[Install](docs/install.md) · [Provider support](docs/providers.md) ·
[Architecture](ARCHITECTURE.md) · [Security](SECURITY.md)

## Why use it

- Keep a durable, visible queue while several agents work in separate Git worktrees.
- Run local Codex, Claude Code, or shell providers against repositories you choose.
- See active work, questions that need a human decision, and finished outcomes in one floor view.
- Pause, redirect, stop, or send work back with the reason recorded beside it.
- Keep repository checkout, task history, and provider processes on your Mac.
- Publish reviewed completed work through the Maintainer rather than losing the path from task to pull request.

## A typical workflow

Choose a repository and define the work you want done. Add workers with the
provider accounts already available on the machine, then put tasks in the
queue. Dark Factory makes an isolated worktree for each accepted task and
starts the chosen provider. Watch the floor, answer a Needs You request when
judgement is required, and inspect the completed result. The Maintainer then
publishes the reviewed change as a GitHub branch and pull request.

## Quick start

Dark Factory **v0.3.5** runs on macOS. You need Git plus a provider CLI that is
installed and signed in, such as `codex` or `claude`. Install the current
binaries with the [installation guide](docs/install.md), initialise a private
home, and set the operator environment used by `factoryctl`:

```sh
factoryctl init --home "$HOME/.dark-factory"
factoryctl service install --home "$HOME/.dark-factory"
export DARK_FACTORY_SOCKET="$HOME/.dark-factory/runtimes/factory.sock"
export DARK_FACTORY_OPERATOR_TOKEN_FILE="$HOME/.dark-factory/operator.token"
```

Register the checkout you want to supervise. `factoryctl project create` prints
the project ID; use it to add a worker and task.

```sh
factoryctl project create --name "My project" --root "$PWD"
factoryctl agent create --project PROJECT_ID --name builder --provider codex \
  --tool-budget 100
factoryctl task add --project PROJECT_ID --agent any --title "Describe the next change"
```

The installation starts `factoryd` and opens the local pairing page at
<http://127.0.0.1:43123/pair>. Pair the console to observe and direct the
factory; keep the CLI for setup and repeatable operations.

Repository routing, issue intake, and Maintainer publication are coordinated
for the next release; they are not part of the v0.3.5 quick start. More
technical detail is in the [development workflow](docs/development/WORKFLOW.md).

Dark Factory is MIT licensed.
