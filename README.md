# Dark Factory

**An autonomous software factory you can see and steer.**

Give your coding agents work. Let an overseer coordinate them. Watch progress
on a live factory floor, inspect results, and step in when they need a decision.
Keep parallel work in one place instead of juggling separate agent sessions.

<picture>
  <source media="(max-width: 600px)" srcset="docs/assets/factory-floor-demo-mobile.png">
  <img src="docs/assets/factory-floor-demo.png" alt="Dark Factory demo showing kernel and store rooms, sample workers, queued work, and an open Needs You decision">
</picture>

*Actual console with labelled demo data: sample workers, codebase rooms, and a
synthetic Needs You decision. No daemon is connected.*

[Get started](#quick-start) · [Website](https://www.darkfactory.build) ·
[Console (requires pairing)](https://app.darkfactory.build) ·
[Documentation](docs/install.md) · [Community backlog](https://www.darkfactory.build/backlog)

## What you can do

- **See the work.** Explore your codebase on the factory floor and select a worker to see its activity.
- **Coordinate a team.** Let an overseer break down goals, assign workers, and follow up on their results.
- **Keep work moving.** Queue and prioritize work across projects and repositories. Assign a named agent or the next available worker.
- **Stay in control.** Open agent terminals, send instructions, and answer Needs You decisions from the same console.
- **Review and improve.** Inspect completed work and review findings, request corrections, and publish reviewed pull requests when GitHub is configured.
- **Contain the work.** Every worker attempt gets its own Git worktree and private runtime home; an overseer works from a private runtime home alone. Codex and Claude Code commands run in an OS sandbox that can write only there and cannot read the rest of your home directory.
- **Set the budget.** Cap a project by runs, by wall-clock time per run, and by provider tokens. At a ceiling the project admits nothing new and running work finishes.
- **Choose the model per worker.** Codex and Claude Code workers share one floor, each with its own provider, model and effort.
- **Step away from the browser.** Work continues while your Mac stays awake. With remote access configured, a paired phone can steer the same factory.

## A working day

Give the overseer a bounded goal, such as improving a confusing setup flow.
It coordinates workers while you follow their progress on the floor. Open a
worker to inspect its terminal or answer a question. Read the completed result,
request a correction if needed, and take the reviewed change forward.

You can also start small with one worker and one task. Adding teammates does
not mean managing another set of disconnected sessions.

## Quick start

Start with macOS, Git, Python 3.9 or newer, an existing committed checkout, and
a signed-in Codex CLI. [Install the latest release](docs/install.md#install-a-release),
keeping the complete release directory together, including `libexec`, and its
commands on `PATH`. Homebrew supplies Python. Then run:

```sh
factoryctl init --home "$HOME/.dark-factory"
factoryctl service install --home "$HOME/.dark-factory"
```

A fresh installation opens the browser pairing page. Confirm pairing, then
follow [Start your first worker](docs/install.md#start-your-first-worker) to
register your checkout, enable work, and give a worker its first task. That
short CLI setup is still required; once the worker appears, use its console
panel to inspect results and queue more work. Try one small documentation
correction before handing over a larger goal.

The Tasks panel's **New task** form accepts pasted images, dropped files, or
files selected with **Attach files** (up to 8 files and 8 MiB total). Add an
instruction, review or remove the previews, then submit. Attachments commit
with the task in the local daemon database and remain there with its history.
Each attempt gets a fresh copy in its private runtime home, available through
`$DARK_FACTORY_TASK_ATTACHMENTS`; uploads do not enter your Git checkout.
Unsubmitted uploads are temporary and discarded when the connection closes.
File contents remain unchanged; interpretation depends on the provider's tools.

In **Settings → Attachment storage**, enable **Automatically remove attachments
after 30 days** to clean up succeeded and cancelled tasks hourly while the daemon
is running. This saved factory-wide setting is off by default and also applies
to existing tasks. Turning it off stops future cleanup. To remove files sooner, run
`factoryctl task update --task ID --revision REVISION --remove-attachments`.
Queued, running, failed, and blocked tasks are protected. `factoryctl task read`
keeps the original filenames with `removed: true`; sending cleaned tasks back
is refused, so create a new task and attach any required files instead. Normal
runtime cleanup already removes worker copies.

Deletion frees database space for reuse. To return unused space to disk, turn
`factoryctl dispatch off`, let all runs settle, then run
`factoryctl storage compact`. It uses SQLite VACUUM and a WAL checkpoint while
holding the daemon's writer gate. Compaction is optional, may need temporary
free disk space up to twice the database size, and leaves dispatch off.
Compaction remains explicit; automatic attachment cleanup frees database space
for reuse without running VACUUM.

## Status and further reading

Repository management and issue intake ship in v0.4.0. **Hosted GitHub
connection for new customers still awaits service activation**; installing
v0.4.0 alone does not enable it. Manual local work and the public
[reporting page](https://www.darkfactory.build/feedback) remain available.

Sandboxed Claude Code workers and provider token budgets ship in v0.5.0. Codex
workers and overseers are proven with real work. Claude Code workers are
proven by a live run; a Claude Code overseer has fixture proof only, so use a
Codex overseer for now. Agents run on your Mac as your user. The sandbox
confines the commands and file tools an agent uses; the provider process and
its MCP servers sit outside it, and configured providers receive task and
repository material. Token budgets count tokens, including cached input, not
money. Remote access and GitHub publication need additional setup. See
[installation and recovery](docs/install.md) and
[provider support](docs/providers.md).

For deeper detail: [Architecture](ARCHITECTURE.md) · [Security](SECURITY.md) ·
[Contributing](CONTRIBUTING.md) · [Development](docs/development/WORKFLOW.md) ·
[Deployment](docs/development/DEPLOY.md). Dark Factory is MIT licensed.
