# Dark Factory

Dark Factory turns a software backlog into supervised coding-agent work on
your own machine. `factoryd` owns the durable queue and provider processes,
`factoryctl` provides bootstrap and control operations, and a web console
served over loopback uses the same local API for day-to-day observation. The
runtime is Go; the Rust implementation it replaced is deleted, and the Ratatui
TUI is gone with it. Dark Factory is not a coding model, hosted service, or
general agent framework.

The Go runtime is the implementation: kernel, daemon, runner, local API, web
console, and the managed launchd service lifecycle. `factoryd` runs a task end
to end — enqueue, schedule, attempt, result, terminal record — proved
black-box against real binaries including SIGKILL crash cuts, using the
deterministic shell provider. The Claude and Codex providers are deliberately
fail-closed and unproven, the console's remaining daemon gaps are recorded
rather than hidden, and the daemon is Darwin-only.

## Model

- A **project** points at a local source repository and holds shared policy.
- An **agent** has a role, provider, settings, and ordered task queue.
- A **task** is durable work; assigning it makes it eligible for admission.
- An **attempt** (stored as a run) is one execution of one task.
- A **Change** is the retained, `.git`-free source tree used by worker attempts
  and retries.
- An **input envelope** is one immutable, bounded, explicitly untrusted
  observation; a **work candidate** is its source revision held in quarantine.

Each admitted attempt gets a fresh runner-owned interactive PTY and provider
process. Its credential resolves only while that run is running, and the daemon
derives the caller's stored attempt identity instead of accepting a
caller-selected one. Attempt authority is revoked before cleanup and configured
completion checks. Dark Factory supplies no commit, push, or pull-request
surface. Closing `factoryctl` or the browser does not stop active work.

The current provider-neutral quarantine is inert. Operator-only
`factoryctl input` and `factoryctl candidate` actions store, list, inspect, and
reject untrusted observations; there is deliberately no accept or materialize
command. Receipt cannot create a task, message, run, Change, prompt, or
scheduling event. Candidate inspection identifies the exact current source
revision needed for a later causal observation. There is still no external HTTP
webhook or GitHub intake adapter.

See [ARCHITECTURE.md](ARCHITECTURE.md) for the detailed lifecycle and
[SECURITY.md](SECURITY.md) for threat boundaries.

## Availability

Dark Factory is pre-1.0. This revision is not approved for installation or
live provider work: no real Claude or Codex attempt has been reviewed, and the
operator's own installation has not been migrated. There is no supported
install command for this revision:
do not run `factoryctl init`, enable `factoryctl dispatch on`, update a live
installation, or point a source build at `~/.dark-factory`.

Supported installation steps will return only when a revision is approved for
live operation.

## Safe source preview

There is no supported live-use command in this revision: the Go runtime runs
end to end under the shell provider, and no real Claude or Codex attempt has
been proven yet. Do not submit provider work, use a Claude or Codex
subscription, or point a source build at the operator's home.

For development work, use an isolated branch/worktree and the fuller
[development workflow](docs/development/WORKFLOW.md). It includes the
authoritative local gate and deterministic provider fixtures.

## Operator surface

These are the planned day-to-day entry points once live operation is supported:

```sh
factoryctl status
factoryctl web status

factoryctl run list --project PROJECT_ID
factoryctl run stop --project PROJECT_ID --run RUN_ID
```

Run `factoryctl --help` or `factoryctl <command> --help` for the exact
bootstrap, service, project, agent, task, input, candidate, and browser-pairing
operations. The CLI and browser are clients of one daemon API; neither owns
policy or lifecycle state.

## Learn more

- [Provider contract](docs/providers.md)
- [Architecture](ARCHITECTURE.md)
- [Security](SECURITY.md)
- [Contributing](CONTRIBUTING.md)
- [Development workflow](docs/development/WORKFLOW.md)

Dark Factory is MIT licensed.
