# Dark Factory

Dark Factory is a macOS-local runtime for supervised coding-agent work on your own machine. It keeps durable work, provider processes, task history, and cleanup under `factoryd`, while `factoryctl` and the paired browser console use the same authenticated local API. Close the CLI or browser and admitted work keeps its daemon-owned Change and provider session.

<p align="center">
  <img src="docs/assets/factory-floor-demo.png" alt="Dark Factory floor fixture showing a dispatcher and two workers" width="100%" />
</p>

*Product capture from the actual codebase scan fixture: three sample workers, no daemon or live task authority.*

The operator floor shows project rooms, agent status, queues, recent work, and requests that need your attention. Select an agent to inspect its terminal and task history, send an instruction, intervene with a recorded message, interrupt Codex generation, stop a run, or start a replacement. Queue work for one worker or any eligible worker; admission and execution remain daemon-owned. Every admitted attempt gets a fresh provider process and a linked Git worktree on its own branch. The runtime does not commit, push, open pull requests, or publish repositories for you.

## Install and pair

Dark Factory supports macOS. Download the release archive for Apple silicon or Intel, verify its entry in `SHA256SUMS`, and put `factoryd`, `factory-runner`, and `factoryctl` together on `PATH`. Then create the managed home and install the service:

```sh
factoryctl init --home "$HOME/.dark-factory"
factoryctl service install --home "$HOME/.dark-factory"
```

Installation opens the daemon's pair page at <http://127.0.0.1:43123/pair>. Confirm that browser to connect the console. The shell provider needs no external tool; Codex requires its normal local CLI and signed-in account. Shell and Codex are proven end to end. Claude Code launch is fixture-proven, while its real-provider smoke remains outstanding.

For release archives, upgrades, relay pairing, service inspection, and backups, read the [installation guide](docs/install.md). The [provider contract](docs/providers.md) explains discovery and delivery. [Architecture](ARCHITECTURE.md), [security](SECURITY.md), and the [development workflow](docs/development/WORKFLOW.md) cover the boundaries and local checks.

The hosted site is a separately reviewed deployment of packed console artifacts. A runtime change that needs new browser capabilities or community routes becomes available to users only after the matching site release; this repository's installation does not provide hosted execution, public HTTP intake, or an in-runtime updater.

Dark Factory is MIT licensed.
