# Deploying the site and the live service

The public site (`https://app.darkfactory.build`) deploys to Vercel
production from `main` of the dark-factory-site repository via Vercel's Git
integration; its `vercel.json` build runs the site's own artifact
verification. Nothing here deploys it.

```sh
factoryctl release <commit-sha> [--start] [--wait]
```

`factoryctl release <commit-sha>` only reads that commit's release record.
`--start` asks the running factoryd to install the commit, which must be merged
into `main` of its registered dark-factory checkout and be the running build or
a descendant of it; an older or unrelated commit is refused, so a release never
downgrades the factory. factoryd:

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
   release artifact and reports that identity when run, and writes the
   upgrade marker `<home>.service/upgrade` naming the release. Nothing launchd
   runs has changed.

The running build then shuts down, releasing the home, socket and browser
port, and runs the staged `factoryd` as its own child with the same
arguments. The child is promoted only if it answers a `web_status` call as the
release within 5 minutes and then stops cleanly when asked: the parent swaps
`bin/previous` and `bin/current` in one rename and rebinds the receipt's
program digest. A child that panics, refuses its arguments, hangs or answers
as another build is stopped and nothing is swapped. Either way the parent
exits 75, so launchd (`KeepAlive` on unsuccessful exit) starts `bin/current`,
which settles the release at boot: a marker naming itself is recorded
`verified`; any other marker is recorded `failed` with the reason, after the
backup is restored under the home lock. The staged build never decides its own
rollback, and launchd never runs a build that has not proven itself. If the
parent dies mid-trial, launchd starts the old build, which kills the trial
child's process group (recorded in the marker; launchd does not end it) and
recovers the same way. The record is the
production delivery `release:<sha>`. `--wait` follows it across the restart
and exits 0 when verified, 1 when it failed after staging, and 75 when the
call was refused or the release failed before staging.

factoryd also releases itself. Where the home has a registered checkout of
dark-factory, every two minutes it reads `main`'s tip with `git ls-remote
origin` (no GitHub REST call) and releases that tip when no `release:<sha>`
record exists. A recorded tip, running, verified or failed, is never started
again: a failed release waits for a newer tip or a manual `factoryctl release
<commit-sha> --start`.
Merged work is not followed up after release; a `Closes #N` footer closes its
issue on merge.
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

**New home file.** The running build checks the home before it stages the
release, so it judges the new build's home by its own, older rules. The home
census therefore ignores any regular file at the home root it does not name;
only symlinks, directories and special files there are refused
(`readOperationalCensus` in `internal/install/operational_darwin.go`). A
release may add a plain file to the home without any allowlist edit. A
release that adds anything else at the home root, such as a directory, is
refused by the build before it and must be installed by hand as above.

**Schema change.** A release backs the store up before staging, and the old
build restores that backup whenever the release did not promote, so it never
opens a schema its trial child migrated. To roll back by hand after a promotion,
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
