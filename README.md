# Dark Factory

Dark Factory is a macOS-local runtime for supervised coding-agent work on
your own machine. `factoryd` owns the durable queue, attempts, provider
processes, and cleanup. `factoryctl` is the operator CLI. A hosted web console
opens through `factoryctl web open` and connects to the paired daemon's
authenticated loopback API.

The runtime is not a hosted coding service, a coding model, or a general agent
framework. It keeps work running when the CLI or browser closes, and provides
no commit, push, pull-request, or repository-publication operation.

## Current support

- macOS only.
- The `shell` provider is proven end to end. Native `claude_code` and `codex`
  launch paths are implemented for an existing local CLI and signed-in account;
  a real-provider smoke remains required before release. See the [provider
  contract](docs/providers.md).
- The browser console shows durable factory, agent, and task state and supports
  terminal observation and input, HumanRequest reply, and cancellation. Queue
  setup stays in `factoryctl`; the console does not display controls the daemon
  cannot serve.
- There is no external HTTP/GitHub intake and no in-runtime updater.

Each project has agents and durable tasks. An admitted attempt gets a fresh
provider process and a daemon-owned `.git`-free Change. The browser and CLI
remain clients of the same local API; neither owns lifecycle or policy.

## Installation

Install a published release, not the development branch. The [installation
guide](docs/install.md) covers the three binaries, managed service, and paired
browser console.

## Development

Use an isolated worktree, a temporary `DARK_FACTORY_HOME`, and an explicit
private socket for source development. The
[development workflow](docs/development/WORKFLOW.md) has the checked command
sequence and deterministic shell-provider fixtures. Run
`./scripts/local-ci.sh` before sending a change.

## Learn more

- [Installation](docs/install.md)
- [Provider contract](docs/providers.md)
- [Architecture](ARCHITECTURE.md)
- [Security](SECURITY.md)
- [Contributing](CONTRIBUTING.md)
- [Development workflow](docs/development/WORKFLOW.md)

Dark Factory is MIT licensed.
