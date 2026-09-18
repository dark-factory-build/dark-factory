# Dark Factory

Dark Factory coordinates coding agents on your Mac while you keep the final
say. Give it useful work, watch the factory floor as work moves through rooms,
and step in when an agent needs a decision. Work continues under the local
service when you close the terminal or browser.

<p align="center">
  <img src="docs/assets/factory-floor-demo.png" alt="Dark Factory floor with several workers and an open decision" width="100%" />
</p>

*Fixture capture from the real floor component: sample workers and an open
decision are labelled in the banner; no live factory is claimed.*

## What you get

- **Autonomous coordination:** queue work for one worker or any eligible worker;
  admission, capacity, retries, and cleanup stay recorded by the service.
- **A visible floor:** see projects, rooms, queues, active work, and the paths
  an agent has reported through the codebase.
- **Human control:** inspect a request, send a durable instruction, pause or
  stop work, and choose how an agent should continue.
- **Recoverable work:** each admitted attempt has a durable task record, a
  provider process, and an isolated Git worktree on its own branch.
- **Local accounts:** use the shell provider without another tool, or connect
  your existing Codex or Claude Code installation through its normal sign-in.

The floor shows what the factory has observed and keeps unknown or unavailable
information visible. Review changes in the worktree, then use your normal
review and publication path when they are ready.

## Install and do useful work

Download the macOS release for Apple silicon or Intel, verify its entry in
`SHA256SUMS`, and put `factoryd`, `factory-runner`, and `factoryctl` on `PATH`.
Create the local service and pair the browser:

```sh
factoryctl init --home "$HOME/.dark-factory"
factoryctl service install --home "$HOME/.dark-factory"
export DARK_FACTORY_SOCKET="$HOME/.dark-factory/runtimes/factory.sock"
export DARK_FACTORY_OPERATOR_TOKEN_FILE="$HOME/.dark-factory/operator.token"
factoryctl project create --name "My project" --root "$PWD"
factoryctl agent create --project PROJECT_ID --name builder --provider shell --tool-budget 100
factoryctl task add --project PROJECT_ID --agent AGENT_ID --title "Describe the first useful change" --body "Include the acceptance checks."
```

Use the project and agent IDs printed by the create commands in the next
command. For a supervised queue, add an orchestrator and send work through its
lane:

```sh
factoryctl agent create --project PROJECT_ID --name overseer --provider codex --role orchestrator --tool-budget 100
factoryctl overseer task add --agent any --title "Coordinate the next useful change" --body "Inspect, delegate, and report the acceptance checks."
factoryctl overseer status
```

Open the pair page printed by installation, then use the console to follow the
task, inspect the floor, and answer requests. The [installation guide](docs/install.md) covers
upgrades, relay pairing, service inspection, and backups. Read the
[provider contract](docs/providers.md) before adding Codex or Claude Code;
[security](SECURITY.md) and [architecture](ARCHITECTURE.md) describe the
boundaries behind the product.

At this revision, the local service, console, and floor are included. Shell and
Codex are proven end to end; Claude Code launch is fixture-proven while its
real-provider smoke remains outstanding. Community feedback and backlog
handoffs target a coordinated runtime and website release and are not part of
this install flow yet.

Dark Factory is MIT licensed.
