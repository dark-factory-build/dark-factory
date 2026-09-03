# Go local-runtime cutover

The hard cutover is complete. Dark Factory's local runtime is implemented in
Go: `factoryd` owns durable work and provider processes, while `factoryctl` and
the hosted console use the same local API.

The retired Rust local-runtime source is absent. The Rust code under
`control-plane/` is the intentionally separate Maintainer App, not part of the
local runtime.

Production proof paired the hosted console with an installed Go daemon and ran
the shell provider through direct terminal input, output, and close to a durable
successful task result.

Current behavior and policy live in [README.md](../../README.md),
[ARCHITECTURE.md](../../ARCHITECTURE.md), [the installation guide](../install.md),
[the provider contract](../providers.md), and [the development
workflow](WORKFLOW.md). Git history retains the chronological design and proof
record.
