//! Shared process-identity helpers for the process-level fixtures.
//!
//! `execution_launch_gate.rs` and `execution_observer_restart.rs` drive real
//! `factory-runner` processes through `factoryd`'s durable resource ledger,
//! so both need the exact locator and birth-fingerprint shapes the Store
//! stores, an owner that never leaks a runner, and waits that say what did
//! not happen. `runner.rs` and `exec_gate.rs` drive the runner binary
//! directly and share the same bounded-wait and clear-failure needs even
//! without a Store in the picture. Keeping one copy keeps those shapes
//! honest: a fixture that quietly disagreed with the Store would prove
//! nothing, and a wait that panics with a generic message trains people to
//! re-run a gate until it goes green instead of trusting what it reports.
#![allow(dead_code)]

use std::{
    fs, io,
    os::unix::fs::{DirBuilderExt, MetadataExt, PermissionsExt},
    path::Path,
    thread,
    time::{Duration, Instant},
};

use factory_core::RunnerInstanceId;
use rustix::{
    io::Errno,
    process::{Pid, Signal, kill_process, test_kill_process},
};
use tokio::process::Child;

/// Deliberately generous. These fixtures run alongside the rest of the
/// process-level suite, so a bound that merely fits an idle machine turns
/// contention into a mystery failure.
pub const FIXTURE_TIMEOUT: Duration = Duration::from_secs(30);

/// Polls a synchronous `condition` until it is true or [`FIXTURE_TIMEOUT`]
/// elapses. `what` must name exactly what the caller is waiting for (and any
/// path or PID involved); on expiry that name and the exact budget become the
/// panic message, so a timeout is diagnosed as a timeout and never mistaken
/// for whatever a caller does with state next. Never proceed past a poll loop
/// without going through this (or [`wait_for_async`]): the failure belongs in
/// the wait, not in an unconditional read after it.
pub fn wait_for(what: &str, mut condition: impl FnMut() -> bool) {
    let deadline = Instant::now() + FIXTURE_TIMEOUT;
    loop {
        if condition() {
            return;
        }
        assert!(
            Instant::now() < deadline,
            "{what} (waited {FIXTURE_TIMEOUT:?})"
        );
        thread::sleep(Duration::from_millis(10));
    }
}

/// Async counterpart of [`wait_for`] for fixtures already driving a Tokio
/// runtime.
pub async fn wait_for_async(what: &str, mut condition: impl FnMut() -> bool) {
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

/// Async counterpart of [`wait_for`] for a condition that itself needs to
/// await an inner future on every poll (a Store query, a socket round trip),
/// rather than a plain synchronous check.
pub async fn wait_for_condition<F, Fut>(what: &str, mut condition: F)
where
    F: FnMut() -> Fut,
    Fut: std::future::Future<Output = bool>,
{
    let deadline = Instant::now() + FIXTURE_TIMEOUT;
    loop {
        if condition().await {
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
/// any case after a fixed iteration cap. Both bounds exist purely so a
/// panicking or killed test can never leak the shell process running this
/// snippet as a launchd orphan; no assertion depends on either. Mirrors
/// `crates/factoryd/tests/rust_completion.rs`'s helper of the same name and
/// shape: a background descendant that outlives its Rust owner needs this
/// bound baked into the shell script itself, since a `Drop` guard can only
/// help when it runs at all (never true for a killed test process) and can
/// race the very `TempDir` cleanup that would otherwise make the awaited path
/// disappear along with everything else.
pub fn bounded_shell_wait(target: &str, root: &Path) -> String {
    format!(
        "i=0; while [ ! -e {target} ] && [ -d '{root}' ] && [ \"$i\" -lt 6000 ]; \
         do i=$((i + 1)); /bin/sleep 0.05; done",
        root = root.display()
    )
}

/// A runner child this test owns and must reap, whatever the assertions do.
pub struct OwnedRunner(Option<Child>);

impl OwnedRunner {
    pub fn new(child: Child) -> Self {
        Self(Some(child))
    }

    pub async fn wait(&mut self) -> std::process::ExitStatus {
        let status = self.0.as_mut().unwrap().wait().await.unwrap();
        self.0.take();
        status
    }

    pub fn kill_and_reap(&mut self) -> io::Result<std::process::ExitStatus> {
        let Some(child) = self.0.as_mut() else {
            return Err(io::Error::other("owned runner is already reaped"));
        };
        child.start_kill()?;
        let deadline = Instant::now() + FIXTURE_TIMEOUT;
        loop {
            if let Some(status) = child.try_wait()? {
                self.0.take();
                return Ok(status);
            }
            if Instant::now() >= deadline {
                return Err(io::Error::new(
                    io::ErrorKind::TimedOut,
                    "owned runner did not reap after exact-child kill",
                ));
            }
            thread::sleep(Duration::from_millis(10));
        }
    }

    pub fn is_live(&self) -> bool {
        self.0.is_some()
    }
}

impl Drop for OwnedRunner {
    fn drop(&mut self) {
        if self.0.is_some()
            && let Err(error) = self.kill_and_reap()
            && !thread::panicking()
        {
            panic!("failed to reap owned runner during cleanup: {error}");
        }
    }
}

/// Kills the exact PID unconditionally when dropped, whatever the test does
/// in between. For a fixture that deliberately leaves a descendant alive past
/// its leader (so a group-absence proof has something to observe), this is
/// the reaper: a `Drop` guard runs on an ordinary panic just as it does on a
/// clean return, so the descendant only escapes it if the whole test process
/// is killed outright -- the case every bounded shell-loop budget in this
/// module exists to bound independently.
pub struct KillOnDrop(pub Pid);

impl Drop for KillOnDrop {
    fn drop(&mut self) {
        let _ = kill_process(self.0, Signal::KILL);
    }
}

pub fn pid(value: u32) -> Option<Pid> {
    i32::try_from(value).ok().and_then(Pid::from_raw)
}

pub fn process_absent(raw_pid: u32) -> bool {
    match test_kill_process(pid(raw_pid).expect("process identity is representable")) {
        Err(Errno::SRCH) => true,
        Ok(()) => false,
        Err(error) => panic!("process {raw_pid} presence probe failed: {error}"),
    }
}

pub async fn await_process_absence(raw_pid: u32) {
    wait_for_async(
        &format!("process {raw_pid} survived its release gate"),
        || process_absent(raw_pid),
    )
    .await;
}

pub async fn await_path(path: &Path) {
    wait_for_async(&format!("{} was never created", path.display()), || {
        path.exists()
    })
    .await;
}

pub fn private_directory(path: &Path) {
    fs::DirBuilder::new()
        .recursive(true)
        .mode(0o700)
        .create(path)
        .unwrap();
    fs::set_permissions(path, fs::Permissions::from_mode(0o700)).unwrap();
}

pub fn runtime_locator(path: &Path) -> String {
    serde_json::json!({ "path": path }).to_string()
}

pub fn runtime_birth(path: &Path) -> String {
    let metadata = fs::symlink_metadata(path).unwrap();
    birth_fingerprint(metadata.dev(), metadata.ino())
}

pub fn birth_fingerprint(device: u64, inode: u64) -> String {
    format!("unix-device:{device}:inode:{inode}")
}

pub fn runner_setup_locator(path: &Path, runner_instance_id: &RunnerInstanceId) -> String {
    serde_json::json!({
        "runner_instance_id": runner_instance_id.as_str(),
        "setup_path": path,
    })
    .to_string()
}

pub fn runner_locator(pid: u32, runner_instance_id: &RunnerInstanceId) -> String {
    serde_json::json!({
        "pid": pid,
        "runner_instance_id": runner_instance_id.as_str(),
    })
    .to_string()
}

#[cfg(target_os = "linux")]
pub fn process_birth(pid: u32) -> Option<String> {
    let stat = fs::read_to_string(format!("/proc/{pid}/stat")).ok()?;
    let fields = stat.rsplit_once(") ")?.1;
    let ticks = fields.split_whitespace().nth(19)?;
    Some(format!("linux-start-ticks:{ticks}"))
}

#[cfg(not(target_os = "linux"))]
pub fn process_birth(raw_pid: u32) -> Option<String> {
    test_kill_process(pid(raw_pid)?).ok()?;
    Some("weak-presence-only".into())
}
