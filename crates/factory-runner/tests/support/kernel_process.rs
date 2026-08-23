//! Shared process-identity helpers for the execution fixtures.
//!
//! `execution_launch_gate.rs` and `execution_observer_restart.rs` both drive
//! real `factory-runner` processes through `factoryd`'s durable resource
//! ledger, so both need the exact locator and birth-fingerprint shapes the
//! Store stores, an owner that never leaks a runner, and waits that say what
//! did not happen. Keeping one copy keeps those shapes honest: a fixture that
//! quietly disagreed with the Store would prove nothing.
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
    process::{Pid, test_kill_process},
};
use tokio::process::Child;

/// Deliberately generous. These fixtures run alongside the rest of the
/// process-level suite, so a bound that merely fits an idle machine turns
/// contention into a mystery failure.
pub const FIXTURE_TIMEOUT: Duration = Duration::from_secs(30);

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
    let deadline = Instant::now() + FIXTURE_TIMEOUT;
    while !process_absent(raw_pid) {
        assert!(
            Instant::now() < deadline,
            "process {raw_pid} survived its release gate"
        );
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
}

pub async fn await_path(path: &Path) {
    let deadline = Instant::now() + FIXTURE_TIMEOUT;
    while !path.exists() {
        assert!(
            Instant::now() < deadline,
            "{} was never created",
            path.display()
        );
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
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
