# Contributing

Repository context is in [AGENTS.md](AGENTS.md).

Development fixtures use isolated temporary homes, explicit sockets, and
deterministic providers.

## Local development

```sh
git fetch origin main && git worktree add -b <slug> .worktrees/<slug> origin/main
cd .worktrees/<slug>
go build ./...
```

The complete local gate is:

```sh
./scripts/local-ci.sh
```

The routine source gate is:

```sh
./scripts/go-check.sh
```

It runs `gofmt`, `go vet`, ordinary short Go tests, the TypeScript build and
tests, and `git diff --check`. Add focused tests for the changed package; run
process-sensitive Go tests with `-count=1` through `./scripts/with-local-ci-lease.sh`.

The complete local gate is the explicit full check above. It adds repository
fixtures, process and lifecycle checks, and release/package fixtures. Fixed
`./scripts/local-ci.sh --ui`, `--runtime`, and `--release` modes match those
component boundaries; the release mode also runs the routine source check.
CI selects a mode from the complete merge-queue diff;
uncertain or mixed changes use the full gate. Dark Factory is macOS-only,
so the gate is macOS-only. A focused `-race`
check covers concurrency or ownership changes without imposing broad stress
on unrelated changes.

The gate checks:

- `gofmt` and `go vet` are clean. Any affected focused `-race` check treats a
  race report as a failure, not a warning.
- The SQLite schema is defined in `internal/kernel/schema.go`. A change bumps
  `userVersion` and adds one migration step from the version before it,
  proven on a copy of a live home; Open refuses every other version.

## Console development without Go

The console in `web/` needs only Node 22 or later with Corepack; no Go, provider
login or running factory. The routine and full gates above still need Go, and
`./scripts/go-check.sh --ui` is not a Go-free route. From `web/`:

```sh
corepack pnpm install --frozen-lockfile
corepack pnpm test
corepack pnpm run preview:floor
```

`preview:floor` builds the packages and renders
[`packages/ui/examples/floor-preview.mjs`](web/packages/ui/examples/floor-preview.mjs)
to a static HTML file whose path it prints. It feeds a small `PublicWorld` (the
public projection factoryd serves at `/v1/public/PROJECT`) through the exported
`publicFloor` and `FactoryScene`, the same way an embedding page would. Edit
that world or anything under `packages/ui/src`, rerun, and reload the file. The
preview is static: no animation, selection or live updates. The test fixture
builders in `web/fixtures/scene.mjs` and `web/fixtures/graph.mjs` cover shapes
the example does not. `corepack pnpm run preview:floor fixture` instead serves
the whole `FactoryConsole` over the labelled `web/fixtures/state.mjs` state on
`http://127.0.0.1:5196/?fixture`, equally static.

## Where to start

- **A bug or a small gap**: [GitHub issues labelled
  `known-issue`](https://github.com/dark-factory-build/dark-factory/issues?q=is%3Aissue+is%3Aopen+label%3Aknown-issue)
  each have a symptom, evidence (`file:line` or how it was observed), a
  suggested smallest fix, and a `size:S|M|L` label (`decision` when the
  maintainer has to choose, not code) — anything `size:S` is a reasonable
  first change. Found a new one? Open an issue with the bug template and
  label it `known-issue`; a fix closes it in the same PR (`Closes #N`).
- **A new provider**: see [docs/providers.md](docs/providers.md) — the whole
  closed contract is `internal/provider/provider.go`: executable resolution,
  launch construction, and one selected task-delivery mode per launch. Extend
  its deterministic fake-provider proofs before a real run.
- **The web console**: `web/packages/ui/src` is the operator surface, built on
  the framework-neutral client in `web/packages/client`. It renders only
  bounded canonical state and finite errors, never constructs run or session
  coordinates, and its remaining daemon gaps are recorded rather than filled
  with invented state.
- **A kernel causal test**: use the proof matrix in
  [ARCHITECTURE.md](ARCHITECTURE.md) and [SECURITY.md](SECURITY.md). Process
  fixtures must register exact resources before use and include an independent
  post-test verifier; provider/test-process cleanup is not proof.
