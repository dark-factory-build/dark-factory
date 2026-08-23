//! Bounded-wait helpers for factoryd's inline `#[cfg(test)]` fixtures.
//!
//! `runner_process.rs` and `execution.rs` both drive real child processes
//! from their unit tests and need the same properties: a generous, named
//! budget instead of an ad hoc literal, a failure message that names what
//! did not happen (never a bare `.unwrap()` right after a poll loop), and a
//! shell-script wait that cannot outlive a killed test process. Keeping one
//! copy keeps that budget and that wording consistent across both files.
//! This is the crate-internal counterpart of
//! `crates/factory-runner/tests/support/kernel_process.rs`, which serves the
//! equivalent integration fixtures in `factory-runner`'s own `tests/`
//! binaries; that file cannot be reused here because unit tests compiled
//! into this crate's own library target have no `tests/` directory to
//! `#[path]` into.

use std::{
    path::Path,
    time::{Duration, Instant},
};

/// Deliberately generous, and the same bound
/// `crates/factory-runner/tests/support/kernel_process.rs` uses for the
/// equivalent integration fixtures: these tests run alongside the rest of
/// the suite, so a bound that merely fits an idle machine turns contention
/// into a mystery failure.
pub(crate) const FIXTURE_TIMEOUT: Duration = Duration::from_secs(30);

/// Polls a synchronous `condition` until it is true or [`FIXTURE_TIMEOUT`]
/// elapses, panicking with `what` (naming exactly what did not happen, and
/// any path or PID involved) and the exact budget otherwise. Never proceed
/// past a poll loop without going through this: the failure belongs in the
/// wait, not in an unconditional read after it.
pub(crate) fn wait_for(what: &str, mut condition: impl FnMut() -> bool) {
    let deadline = Instant::now() + FIXTURE_TIMEOUT;
    loop {
        if condition() {
            return;
        }
        assert!(
            Instant::now() < deadline,
            "{what} (waited {FIXTURE_TIMEOUT:?})"
        );
        std::thread::sleep(Duration::from_millis(10));
    }
}

/// Async counterpart of [`wait_for`] for `#[tokio::test]` fixtures.
pub(crate) async fn wait_for_async(what: &str, mut condition: impl FnMut() -> bool) {
    let deadline = Instant::now() + FIXTURE_TIMEOUT;
    loop {
        if condition() {
            return;
        }
        assert!(
            Instant::now() < deadline,
            "{what} (waited {FIXTURE_TIMEOUT:?})"
        );
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
}

/// A `sh` wait for one path that also ends as soon as `root` is gone, and in
/// any case after a fixed iteration cap. Mirrors
/// `crates/factory-runner/tests/support/kernel_process.rs`'s helper of the
/// same name and shape (see its doc comment for why both bounds matter): a
/// backgrounded descendant spawned by a test fixture needs this bound baked
/// into the shell script itself, since a Rust-level `Drop` guard cannot run
/// at all for a killed test process and can race the very `TempDir` cleanup
/// that would otherwise make the awaited path disappear along with
/// everything else.
pub(crate) fn bounded_shell_wait(target: &str, root: &Path) -> String {
    format!(
        "i=0; while [ ! -e {target} ] && [ -d '{root}' ] && [ \"$i\" -lt 6000 ]; \
         do i=$((i + 1)); /bin/sleep 0.05; done",
        root = root.display()
    )
}
