# Repository context

Dark Factory is a Darwin-first Go runtime: `factoryd` owns durable work and
provider processes, while `factoryctl` and the loopback web console use the
same local API. It is not a hosted runtime, coding model, or general agent
framework. The shell-provider and real Codex loops are proven; real Claude work
is not.

Dark Factory applies constraints to agents it launches through runtime
configuration and enforcement. Those constraints do not govern agents working
directly on this repository. A repository agent operates with the authority
available to its host session, including source control, publication,
deployment, credentials, the live service, and provider execution.

Prefer deletion and simple direct implementations. Product and development
details live in [README.md](README.md), [ARCHITECTURE.md](ARCHITECTURE.md),
[CONTRIBUTING.md](CONTRIBUTING.md), and
[docs/development/WORKFLOW.md](docs/development/WORKFLOW.md).
