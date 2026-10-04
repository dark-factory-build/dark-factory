# Deploying the site and the live service

```sh
./scripts/deploy-site.sh <site-commit-sha>
factoryctl release <commit-sha> [--wait]
```

`deploy-site.sh` takes one exact 40-hex commit, refuses a dirty or
mis-positioned worktree, deploys the public site to Vercel production from a
detached worktree of the site repository (`$DARK_FACTORY_SITE`, default
`$HOME/dark-factory-site`) at that commit, then prints `vercel inspect`
for `https://app.darkfactory.build`.

`factoryctl release` asks the running factoryd to install a commit merged into
`main` of its registered dark-factory checkout. factoryd:

1. builds `factoryd`, `factoryctl` and `factory-runner` at that commit from a
   disposable clone, one build at a time with the review gate, with
   `GOTOOLCHAIN` pinned to the commit's `go.mod` and the release build receipt
   linked;
2. holds admission in memory (the durable dispatch switch is never written)
   and waits up to 10 minutes for every live run to end or become adoptable: a
   `running` run whose runner publishes `runtimes/<run id>/takeover.sock` is
   taken over by the next daemon without stopping. A run still blocking at the
   limit fails the release as `drain_timeout`, names the run, and releases the
   hold;
3. backs up the store with `VACUUM INTO` to `<home>.service/upgrade.sqlite3`;
4. stages the three binaries as `bin/previous`, checks each is the exact
   release artifact and reports that identity when run, swaps `bin/previous`
   and `bin/current` in one rename, rebinds the receipt's program digest,
   writes the trial marker `<home>.service/upgrade`, and exits 75, so launchd
   (`KeepAlive` on unsuccessful exit) starts the new binaries.

The new build boots on trial. Sixty seconds after it is up it runs all three
installed binaries; if each reports its release identity, the release is
recorded `verified` and the marker and backup are removed. If it crashes or
exits before that, is not promoted within 5 minutes, or fails verification,
its next boot swaps `bin/previous` back, restores the backup when the old
build's schema version differs, and exits 75; the old build then records the
release `failed` with the reason. The record is the production delivery
`release:<sha>`. `--wait` follows it across the restart and exits 0 when
verified, 1 when it failed after the swap or rolled back, and 75 when the call
was refused or the release failed before the swap.

The release lane's hook, `scripts/deploy-runtime.py [--home HOME] SHA`, runs the
installed `factoryctl release SHA --wait` and passes its exit status through.
The build that introduces `factoryctl release` cannot be released by the
daemon before it; install that one by hand: build the three binaries with the
release receipt, stop dispatch and let work drain, then `factoryctl service
uninstall` and `service install` from the new binaries with the existing
settings.

`factoryctl service install` with changed settings (relay origin, tool path,
toolchain read roots, development browser address) re-renders the plist and
receipt in place and restarts the job; the installed binaries stay. Use it,
for example, to add `~/.cargo/bin` and `~/.rustup` so workers can run the
control-plane's Rust gates.

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

**Schema change.** A release backs the store up before the swap, and a
rolled-back trial restores that backup when the schema version moved, so the
old build never opens a newer schema. To roll back by hand after a promotion,
stop the service, confirm the daemon released `home.lock`, restore a backup
over `factory.sqlite3`, delete `factory.sqlite3-wal` and `factory.sqlite3-shm`,
and install the previous binaries.

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
