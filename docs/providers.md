# Provider contract

This is the fresh Go provider contract. The old Rust launch guide, profile
catalogue, and compatibility matrix are deleted; they are not implementation
instructions for the cutover. V1 has one concrete provider boundary:

```go
func Build(Request) (Launch, error)
```

Admission freezes provider/model/effort. After admission the daemon resolves and
seals one `Installation` and the other executable/configuration/auth launch
facts. `Build` consumes and revalidates that sealed input; it need not perform
locator discovery itself. `Launch` contains only one exact absolute executable,
one ordered argv, and one complete ordered environment. It cannot choose a
provider, source path, working directory, authority, credential, or lifecycle
result.
The runner owns the already-open descriptor-bound Change cwd, fresh interactive
PTY, input, process group, wait/reap, output, and cleanup. `Build` is the only
provider boundary; there is no registry, plugin, profile, fallback, or
provider-owned supervision framework.

## V1 matrix

| Provider | V1 authority | Model/effort | Launch status |
|---|---|---|---|
| `shell` | unrestricted interactive | neither value is present | contract only; package `1ff2e2e6` is unintegrated and not shipped |
| `claude_code` | unrestricted interactive | each optional independently | blocked pending exact provider integration and witness review |
| `codex` | unrestricted interactive | each optional independently | blocked pending exact provider integration and witness review |

No bounded provider authority is shipped in V1. The schema and wire contract
have no permission-mode/profile field. A later bounded provider contract must
prove its actual filesystem, process, socket, and network effects with a real
OS witness before it can be added. Unsupported combinations fail typed
`FailureSpawn` after admission; they never make work ineligible and never
fall through to another provider.

The current metadata-only review recorded Claude Code `2.1.245` and Codex CLI
`0.149.0`. Those versions are evidence for the launch tests, not a permanent
compatibility promise. A provider version, executable digest, or native flag
semantic change fails closed until a new exact review updates the committed
launch table. No paid session is part of this proof.

## Launch facts

Admission freezes provider, optional model and optional reasoning effort in the
Run. It does not commit executable, version, or digest fields. After admission,
the daemon resolves and seals the exact `Installation` and native
executable/configuration/auth launch facts. `Build` consumes and revalidates
that sealed input; immediately before provider release, the daemon/runner
revalidates it and the final Change descriptor identity, generated-config path,
and config digest. A changed, missing, non-regular, or ambiguous object fails
closed. PATH lookup is never executable authority; unavailable resolution maps
to typed `FailureSpawn`.

The shell launch is exactly:

```text
executable: /bin/sh
argv:      ["/bin/sh", "-s"]
```

The provider-specific Claude and Codex argv are ordered, version-sealed launch
facts. In the metadata-only review of Claude Code `2.1.245`, the explicit
unrestricted bypass is `--dangerously-skip-permissions`; in Codex CLI
`0.149.0`, it is `--dangerously-bypass-approvals-and-sandbox`. These flags are
part of the sealed launch witness, not caller input. The argv contains only
the admitted optional model/effort settings supported by that reviewed
version.

For those reviewed versions, the deterministic templates are:

```text
Claude 2.1.245:
  [ABS_CLAUDE, "--dangerously-skip-permissions",
   ("--model", MODEL)?, ("--effort", EFFORT)?]

Codex 0.149.0:
  [ABS_CODEX, "--dangerously-bypass-approvals-and-sandbox",
   ("--model", MODEL)?, ("-c", "model_reasoning_effort=\"EFFORT\"")?]
```

Optional pairs are emitted in the shown order and are omitted independently.
In shell candidate `1ff2e2e6`, `Installation` construction precedes `Build`,
but both occur post-admission; integration may refine that internal ordering
without moving either operation before admission.
There is no trailing prompt argument: the interactive PTY is the only task
input. A future reviewed version may replace a template only after its
metadata/help and fake-witness tests prove the same properties.
They never contain the task body, prompt text, Change path, auth secret,
resume/session selector, remote/cloud selector, browser/plugin selector, or
operator control. The launch tests assert the exact bytes and reject Claude
`-p`/`--print`, Codex `exec`, resume/continue, remote/cloud/app-server paths,
browser/plugin loading, and any unreviewed flag. If a reviewed version cannot
represent one admitted optional setting without a hidden fallback, `Build`
returns typed `FailureSpawn`.

The body is canonical bounded UTF-8. After both durable launch gates have
passed, the runner writes it exactly once to the PTY and appends exactly one
provider-specific terminator. It does not put the body in argv or environment,
and it never reconstructs or replays it after an uncertain write. Output is
opaque observation and never lifecycle authority.

## Environment, roots, and auth

The runner starts from `env_clear` and one closed, ordered environment builder.
Only daemon-approved identity, locale, temporary-root, provider-state,
generated-config, auth-reference, and daemon-control values are included. A
single daemon-sealed validated `PATH` is allowed for non-authoritative child
behavior; `/bin/sh` and all authority helpers are absolute. Inherited API,
proxy, Git/GitHub, SSH, credential-helper, dynamic-loader, plugin, and user
configuration variables are absent. No caller can merge additions.

Every run receives private owner-only `HOME`, `TMP`, provider-state, config,
and runtime roots below the daemon-owned runtime capability. The Change cwd is
descriptor-bound and `.git`-free. The provider cannot substitute a pathname or
escape those roots through a provider-side default. Unrestricted Claude/Codex
API/model network access is not constrained by this command contract; V1 makes
no contrary network-sandbox claim.

An `AuthRef` is either a copied, sealed owner-only regular file or a
metadata-only Keychain reference. Copying is no-fallback and no-secret-read
outside the selected source. The secret value never occurs in argv, the
environment, PTY input, generated logs/configuration, errors, events, or
replay. Auth cannot widen provider, filesystem, process, or network authority.
Replacing the source, changing its identity, or losing access is a typed
post-admission spawn failure; another source is never tried.

## Required causal proof

Provider tests use fake executables and witnesses, never a real provider
session. They inspect exact argv, ordered environment, descriptor cwd, PTY
task bytes, terminator, private-root census, auth privacy, Change/config
identity and digest, and executable replacement immediately before release.
They mutate ambient environment, auth source and replacement, provider state,
config, Change identity, and output/exit timing. They assert no prompt appears
in argv/env/replay, no forbidden print/resume/remote/browser/plugin path is
accepted, and output cannot terminalize a run. Runner/Store tests separately
prove both gates, register-before-exec, typed post-admission availability
failure, cleanup uncertainty remaining finalizing, and exact reap.

There are no V1 tests for bounded authority beyond the typed deferred result.
Any future bounded mode requires real filesystem/socket/network witnesses that
prove the claimed OS effects causally.
