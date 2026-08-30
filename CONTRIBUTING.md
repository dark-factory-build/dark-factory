# Contributing

See [AGENTS.md](AGENTS.md) for the full workflow (one development worktree per change,
mandatory adversarial PR review, simplification over patch) — this
file is just the shortest path to a useful first change.

Development fixtures use isolated temporary homes, explicit sockets, and
deterministic providers; never use the operator's installation.

## Before you send a change

```sh
./scripts/new-worktree.sh <slug>
cd .worktrees/<slug>
go build ./...
```

Run the complete gate:

```sh
./scripts/local-ci.sh
```

The routine source gate is the Go gate: `gofmt`, `go vet`, the short serial Go
test suite, one TypeScript client proof, and the real browser, daemon and
service end-to-end lifecycles, followed by
`git diff --check`. It additionally checks release-source, publisher, and
package fixtures. The daemon is Darwin-only today, so the gate is macOS-only;
Linux support is #120/#141-144. CI requires the runtime gate and the
control-plane gate through the aggregate `required` context, so run the gate
before opening a PR. Also run affected-package tests and focused `-race`
checks when the change touches concurrency or ownership; do not impose broad
stress suites on unrelated changes.

A few repository-wide rules the gate enforces, worth knowing up front:

- `gofmt` and `go vet` are clean. Any affected focused `-race` check treats a
  race report as a failure, not a warning.
- The SQLite schema is one fresh set of statements in
  `internal/kernel/schema.go`. There is deliberately no migration directory
  and no upcaster: the Go home and schema are new, so a schema change is an
  edit to that set plus the causal tests that pin it.
- Never touch a real `$DARK_FACTORY_HOME` (default `~/.dark-factory`) or
  `launchd` from a test or a manual check — see
  [docs/development/WORKFLOW.md](docs/development/WORKFLOW.md) for a
  throwaway daemon on a temp directory instead.

## Where to start

- **A bug or a small gap**: [GitHub issues labelled
  `known-issue`](https://github.com/dark-factory-build/dark-factory/issues?q=is%3Aissue+is%3Aopen+label%3Aknown-issue)
  each have a symptom, evidence (`file:line` or how it was observed), a
  suggested smallest fix, and a `size:S|M|L` label (`decision` when the
  maintainer has to choose, not code) — anything `size:S` is a reasonable
  first change. Found a new one? Open an issue with the bug template and
  label it `known-issue`; a fix closes it in the same PR (`Closes #N`).
- **A new provider**: see [docs/providers.md](docs/providers.md) — the whole
  contract is `internal/provider/provider.go`, whose `NewShellInstallation`
  is the minimal reference implementation to copy from. Only the shell
  provider is proven end to end today; the Claude and Codex constructors are
  deliberately fail-closed until a real attempt is reviewed.
- **The web console**: `web/packages/ui/src` is the operator surface, built on
  the framework-neutral client in `web/packages/client`. It renders only
  bounded canonical state and finite errors, never constructs run or session
  coordinates, and its remaining daemon gaps are recorded rather than filled
  with invented state.
- **A kernel causal test**: use the proof matrix in
  [ARCHITECTURE.md](ARCHITECTURE.md) and [SECURITY.md](SECURITY.md). Process
  fixtures must register exact resources before use and include an independent
  post-test verifier; provider/test-process cleanup is not proof.

Every change updates docs in the same PR when it changes behavior — see
`AGENTS.md`'s "docs are load-bearing" rule.
