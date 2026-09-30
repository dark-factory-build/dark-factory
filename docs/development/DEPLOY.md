# Deploying the site and the live service

Two operator scripts. Both take one exact 40-hex commit and refuse a dirty or
mis-positioned worktree.

```sh
./scripts/deploy-site.sh <site-commit-sha>
./scripts/reinstall-service.sh [--home /absolute/factory-home] <commit-sha>
```

`deploy-site.sh` deploys the public site to Vercel production from a detached
worktree of the site repository (`$DARK_FACTORY_SITE`, default
`$HOME/dark-factory-site`) at that commit, then prints `vercel inspect`
for `https://app.darkfactory.build`.

`reinstall-service.sh` builds `factoryctl`, `factoryd` and `factory-runner`
from a detached worktree of this repository at that commit with `GOTOOLCHAIN`
pinned to the Go version that worktree's `go.mod` declares (as the release
workflow pins it), verifies `go version -m` reports the same `vcs.revision`
and `vcs.modified=false`, links and verifies the release build receipt, backs
up the selected home’s `factory.sqlite3` to
`$HOME/.dark-factory-backups/<utc-timestamp>-<sha>.<unique>/`, then runs
`factoryctl service uninstall` and `service install` with the new binaries,
waits (bounded) for the previous daemon to leave and for the new one to accept
on its socket, and prints service, web and remote status. The service keeps
its recorded `--tool-path` and `--toolchain-read-roots`; set
`DARK_FACTORY_TOOL_PATH` and `DARK_FACTORY_TOOLCHAIN_READ_ROOTS` to replace
them, for example to add `~/.cargo/bin` and `~/.rustup` so workers can run the
control-plane's Rust gates. It refuses to run
unless dispatch is off and every non-terminal run is one the new daemon can
adopt, checked once before the build and again right before the uninstall. A
run is adoptable when it is `running` and its runner still publishes
`runtimes/<run id>/takeover.sock`: the new daemon takes that runner's control
capability over at boot and the provider never stops. Every other non-terminal
run — a run still being admitted or finalizing, or an older runner with no
endpoint — still has to drain. A finalizing run is the transient case: its
runner has already exited and the daemon commits the terminal result on its
own, so the check waits up to two minutes for it to settle and only then
refuses. Every refusal here precedes the uninstall and exits 75, so the caller
knows nothing was installed. In one coordinated maintenance window, run
`factoryctl dispatch off`, wait for the work that cannot be adopted to drain,
and keep dispatch off until the reinstall exits and the new service is healthy;
then run `factoryctl dispatch on`. Binaries land in
`.worktrees/bin-<sha>`. If it stops after the uninstall because the previous
daemon still accepts connections or retains `home.lock`, the service is
uninstalled: rerun once that daemon has exited.

For a configured factory, pass the same absolute `--home` to both
`deploy-runtime.py` and `verify-live-runtime.py` (the default remains
`$HOME/.dark-factory`). The installer preserves the existing service receipt’s
label, plist directory, relay origin, tool path, toolchain read roots, and development browser
address. It refuses a missing label or a receipt changed during preparation.

The deployment hook calls `reinstall-service.sh --prepare` while work continues.
Only preparation holds the shared local-CI lease. After it releases the lease,
the hook pauses dispatch and drains the work that cannot be adopted — its drain
loop uses the same running-plus-endpoint predicate — then calls
`--install-prepared`.
That phase validates the clean exact source and all three binaries’ VCS and
release identities without compiling or waiting for the compiler lease.
Preparation does not back up, migrate a browser profile, or alter the service.
The pause, the drain reads and the installation share one compensation scope,
and a pause is compensated only when the daemon accepted it. Because every
accepted control command advances the revision, landing on
`original_revision + 1` is not proof of ownership — a concurrent operator
`dispatch off` lands on the same number. A `dispatch off` the daemon did not
accept therefore restores nothing and says so: the hook reports that it cannot
prove whose pause is standing and names the decision (`factoryctl status`,
then `factoryctl dispatch on` to resume). A non-zero exit is not evidence of
refusal either, since the CLI can fail after the daemon committed the change.
Only an accepted pause is read back out of the store and restored at its own
unchanged revision.
For an accepted pause the factory therefore keeps working on the old build,
and the failure receipt
states what it was left with rather than what the hook attempted:
`dispatch_enabled` comes from the store, so an operator who re-enabled dispatch
themselves is not reported as a paused factory, and is `null` when the store
cannot be read, because an unreadable store is an unknown state and not an off
one. `service_reachable` is a connect on `runtimes/factory.sock`, not a store
read, so a daemon that died during the install is not reported as serving.
A failure that left nothing to undo exits
75, and the release lane then
re-plans that blocked receipt once on its next tick; a blocker that survives
that attempt still needs an explicit `--retry`.

`scripts/local-ci.sh` runs `scripts/test-reinstall-service.sh` and
`scripts/test-deploy-site.sh`, which exercise both scripts against fakes.

## Order matters

**Independent Change Git state.** Only newly created Changes receive private
Git administration. Retained linked worktrees keep their existing layout,
including on retry; legacy canonical Changes remain shared. Follow the normal
installation procedure above, then verify the installed build and fresh Codex
and Claude commit/settlement/source receipts. This is accident prevention,
not an enforced filesystem boundary or a provider-permission change.

**Pairing capability mask.** Both ends accept any subset of the five known
capability bits with `observe` set, and reject any other bit as malformed:
`knownCapabilities` in `internal/browserprotocol/wire.go` and `capabilities()`
in `web/packages/client/src/control.ts`, which bounds the value at 31. So a
change that ADDS a capability bit must deploy the site BEFORE the daemon is
reinstalled: a daemon granting the new bit to a console still running the old
client makes every deployed console reject its own authentication result. A
change that only narrows the granted mask needs no ordering.

**Shared queue.** Queued work no worker has claimed yet reaches the console
in the additive `shared_tasks` member; every `tasks` item still names its
agent, so a console vendored before the shared queue keeps decoding snapshots
in either installation order. Its ANY WORKER control and the Any eligible
worker queue group appear only after the site is re-vendored from a merged
runtime commit that contains them.

**Schema change.** When a change bumps `PRAGMA user_version`, keep the previous
build's `.worktrees/bin-<sha>` for rollback. The daemon migrates the store
before it listens, but a socket timeout does not prove that the new daemon has
stopped. Before restoring, run `factoryctl service uninstall --home
$HOME/.dark-factory` and confirm the daemon released `home.lock`:

```sh
perl -MFcntl=:flock -e 'open my $lock, "+<", shift or exit 1; flock $lock, LOCK_EX | LOCK_NB or exit 1' "$HOME/.dark-factory/home.lock"
```

A daemon that died mid-migration leaves `factory.sqlite3-wal` and
`factory.sqlite3-shm` next to the store; with them still present the daemon
either refuses to open the restored file or replays the newer schema's WAL onto
it. Only after the stop and lock check, copy the backup the script printed over
`$HOME/.dark-factory/factory.sqlite3`, delete both sidecars, then reinstall
from the previous binaries.

## Where the service runs from

`factoryctl service install` copies the binaries into
`$HOME/.dark-factory.service/bin/current`, and launchd runs the service from
there. The build directory is only the source of that copy.

Factory dispatch and capacity commands use strict revision guards. Every accepted
command advances the revision, including an explicit `dispatch off` when already
paused; this preserves an operator's stop intent against automatic restoration.
A stale identical request is refused, not treated as proof of ownership.
Run admission and settlement advance the event head, not this control revision.
Browser snapshots refresh active counts through that existing event head.

The first upgrade from a daemon with the older activity revisions or same-value replay behavior needs a
controlled operator pause and drain, followed by the prepared-runtime installation
and exact live identity check. Keep automated deployment restoration disabled for
that cutover; restore dispatch only from the operator's explicitly owned pause.
After the updated daemon and CLI are verified, the deployment hook restores only
its unchanged pause revision. It does not count settlements or retry commands.
