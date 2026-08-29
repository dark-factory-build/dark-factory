# macOS service

This directory documents Dark Factory's managed macOS installation. The
LaunchAgent keeps `factoryd` running independently of any client.

Managed installation is implemented by the Go `internal/install` service
engine and operated through `factoryctl service install / start / stop /
uninstall / status --home <ABSOLUTE>`. Install places the invoking
factoryctl's own sibling binaries into the sibling `<home>.service`
directory, writes a durable receipt, renders the plist from the one Go
authority (`install.ServicePlist`), and bootstraps the job. Present
service states are provable only through an exact receipt, plist, and
program-digest agreement; anything else reports ambiguous, and
`factoryctl service uninstall` is the resolution path for residue,
including the engine's own staged-write crash residue. Uninstall is
evidence-first: it issues no launchctl verb until a matching receipt or
an exactly rendered plist proves the label maps to the named home, so a
wrong `--home` or a foreign plist refuses without touching launchd.

The `com.dark-factory.factoryd.plist.template` file is the historical
Rust-era template. The Go plist is rendered in code, never from this
template; the file remains only until the Rust deletion removes the
release chain that references it.

Development daemons still run directly with a temporary
`DARK_FACTORY_HOME` and an explicit private socket — see the
[development workflow](../docs/development/WORKFLOW.md). Tests and the
`scripts/go-service-e2e.sh` gate never touch `~/.dark-factory` or the
production label: they use disposable unique labels and temporary
`--plist-dir` roots, and boot their labels out on exit.
