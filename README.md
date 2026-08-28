# Dark Factory

Dark Factory turns a software backlog into supervised coding-agent work on
your own machine. The Go target has `factoryd` own the durable queue and
provider processes, `factoryctl` provide bootstrap/control operations, and a
hosted browser use the same local API for day-to-day observation. The retained
Rust TUI is historical evidence, not a current product surface. Dark Factory
is not a coding model, hosted service, or general agent framework.

This repository is pre-cutover. Canonical evidence through `359d46a3` includes
production `factoryd` composition and `OperationalHome`, `Store`,
`RuntimeParent`, Local API, and browser ownership; the corrected documentation
contract was merged at `bc48df7f` and still needs an exact-head review. Change
settlement candidate `c675f96e` is under exact review, global admission remains
unintegrated, and shell candidate `1ff2e2e6` is review-BLOCKED. Claude and Codex
remain blocked.

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

Dark Factory is pre-1.0. This docs candidate is not approved for installation
or live provider work. There is no supported install command for this revision:
do not run `factoryctl init`, enable `factoryctl dispatch on`, update a live
installation, or point a source build at `~/.dark-factory`.

Supported installation steps will return only when a revision is approved for
live operation.

## Safe source preview

The old Rust source preview is historical evidence only and is not a current
build or installation instruction. The Go target has no supported live-use
command in this revision. Do not submit provider work, use a Claude or Codex
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
