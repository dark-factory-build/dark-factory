//! Containment for disposable, test-owned launchd jobs.
//!
//! A fixture that bootstraps a throwaway launchd job must be able to remove
//! exactly that job after the target, the fixture driver, or the coordinator
//! is killed. Ownership therefore lives in a durable receipt written *before*
//! `launchctl bootstrap`, never in a `Drop` guard or a background verifier
//! that dies with its parent. A later coordinator resumes the same receipt and
//! finishes the same operation, so response loss cannot create a second
//! bootstrap or a second cleanup authority.
//!
//! This is a release and managed-service fixture. It is deliberately not an
//! attempt `KernelResource`: no Dark Factory attempt creates a launchd job,
//! and ordinary run terminalization never waits for one.
//!
//! Every proof here is exact. Absence is only launchctl's documented
//! not-found classification for one selected service; nothing scans global
//! processes, guesses success from an operational failure, or touches a label
//! this invocation did not create.

use std::{
    ffi::OsStr,
    fs::{self, File},
    io::{self, Write},
    os::unix::fs::{MetadataExt, PermissionsExt},
    path::{Path, PathBuf},
    process::{Command, Output, Stdio},
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
    thread,
    time::{Duration, Instant},
};

use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

/// Every disposable label this gate will create or finalize starts here. A
/// receipt naming anything else is refused rather than booted out.
pub const FIXTURE_LABEL_PREFIX: &str = "com.dark-factory.fixture.";

/// The operator's real service. It is never a gate label, never booted out,
/// and never removed.
pub const INSTALLED_LABEL: &str = "com.dark-factory.factoryd";

const COMMAND_TIMEOUT: Duration = Duration::from_secs(3);
const ABSENCE_TIMEOUT: Duration = Duration::from_secs(30);
const ABSENCE_POLL: Duration = Duration::from_millis(50);

/// launchctl's documented status and text for a service that is not loaded.
/// Status alone is not absence: a future launchctl could reuse the number, so
/// the classification string must agree before anything is deleted.
const NOT_FOUND_STATUS: i32 = 113;
const NOT_FOUND_TEXT: &str = "113: Could not find specified service";

/// Written inside a private root when it is declared, and required before that
/// root is ever deleted. The receipt's device and inode alone are not enough:
/// a corrupt or hand-written receipt could name any directory and carry that
/// directory's real identity. Only a root this gate actually created holds a
/// marker naming the same label, so nothing else can be removed.
const ROOT_MARKER: &str = ".dark-factory-launchd-gate";

#[derive(Debug, thiserror::Error)]
pub enum GateError {
    /// Refused before any external effect. Nothing was bootstrapped, booted
    /// out, or deleted.
    #[error("{0}")]
    Refused(String),
    /// Finalization could not prove absence or identity, so the private root
    /// is deliberately kept for inspection instead of being deleted.
    #[error("launchd gate retained {}: {reason}", root.display())]
    Retained { root: PathBuf, reason: String },
}

impl GateError {
    fn refused(message: impl Into<String>) -> Self {
        Self::Refused(message.into())
    }
}

type GateResult<T> = Result<T, GateError>;

/// Whether one exact service is loaded. Any answer that is not one of these
/// two is an operational failure, never absence.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ServiceState {
    Present,
    Absent,
}

/// A selected `launchctl` binary. Injecting the path keeps the deterministic
/// source and Linux tests on a fake with the same contract, without a trait
/// that would have one real implementation.
#[derive(Clone, Debug)]
pub struct Launchctl {
    binary: PathBuf,
}

impl Launchctl {
    /// Selects the launchctl binary. The path must be absolute and
    /// executable, so a fixture cannot inherit one from `PATH`.
    pub fn new(binary: impl Into<PathBuf>) -> GateResult<Self> {
        let binary = binary.into();
        if !binary.is_absolute() {
            return Err(GateError::refused(format!(
                "launchctl path must be absolute: {}",
                binary.display()
            )));
        }
        let metadata = fs::metadata(&binary).map_err(|error| {
            GateError::refused(format!("launchctl {}: {error}", binary.display()))
        })?;
        if !metadata.is_file() || metadata.permissions().mode() & 0o111 == 0 {
            return Err(GateError::refused(format!(
                "launchctl is not an executable file: {}",
                binary.display()
            )));
        }
        Ok(Self { binary })
    }

    /// Classifies one exact service. `Absent` requires both the documented
    /// status and the documented classification text.
    pub fn state(&self, service: &str) -> Result<ServiceState, String> {
        let output = self.run(&["print".as_ref(), service.as_ref()])?;
        if output.status.success() {
            return Ok(ServiceState::Present);
        }
        let status = output
            .status
            .code()
            .ok_or_else(|| format!("launchctl print {service} was signalled"))?;
        if status == NOT_FOUND_STATUS && self.classification(status)? == NOT_FOUND_TEXT {
            return Ok(ServiceState::Absent);
        }
        Err(format!(
            "launchctl print {service} failed (status {status}): {}",
            String::from_utf8_lossy(&output.stderr).trim()
        ))
    }

    fn classification(&self, status: i32) -> Result<String, String> {
        let status = status.to_string();
        let output = self.run(&["error".as_ref(), status.as_ref()])?;
        if !output.status.success() {
            return Err(format!("launchctl error {status} failed"));
        }
        Ok(String::from_utf8_lossy(&output.stdout).trim().to_owned())
    }

    fn bootout(&self, service: &str) -> Result<(), String> {
        let output = self.run(&["bootout".as_ref(), service.as_ref()])?;
        if output.status.success() {
            return Ok(());
        }
        // A concurrent teardown may have removed the job first. That is not a
        // failure by itself; the absence proof that follows decides.
        Err(format!(
            "launchctl bootout {service} failed: {}",
            String::from_utf8_lossy(&output.stderr).trim()
        ))
    }

    /// Runs one launchctl command under a deadline. A launchctl that wedges
    /// must fail the fixture visibly, never hang the gate forever.
    fn run(&self, args: &[&OsStr]) -> Result<Output, String> {
        let child = Command::new(&self.binary)
            .args(args)
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .spawn()
            .map_err(|error| format!("could not run {}: {error}", self.binary.display()))?;
        // The watchdog only fires while this thread is still inside
        // `wait_with_output`, so the child is unreaped and its PID cannot yet
        // have been recycled onto an unrelated process.
        let pid = child.id();
        let finished = Arc::new(AtomicBool::new(false));
        let watchdog = {
            let finished = Arc::clone(&finished);
            thread::spawn(move || {
                let deadline = Instant::now() + COMMAND_TIMEOUT;
                while Instant::now() < deadline {
                    if finished.load(Ordering::Relaxed) {
                        return false;
                    }
                    thread::sleep(Duration::from_millis(20));
                }
                if finished.load(Ordering::Relaxed) {
                    return false;
                }
                kill(pid);
                true
            })
        };
        let output = child
            .wait_with_output()
            .map_err(|error| format!("could not wait for launchctl: {error}"));
        finished.store(true, Ordering::Relaxed);
        let timed_out = watchdog.join().unwrap_or(false);
        let output = output?;
        if timed_out {
            return Err(format!(
                "{} {} exceeded {COMMAND_TIMEOUT:?}",
                self.binary.display(),
                args.join(OsStr::new(" ")).to_string_lossy()
            ));
        }
        Ok(output)
    }
}

fn kill(pid: u32) {
    if let Ok(pid) = i32::try_from(pid)
        && let Some(pid) = rustix::process::Pid::from_raw(pid)
    {
        let _ = rustix::process::kill_process(pid, rustix::process::Signal::KILL);
    }
}

/// The durable record of one disposable launchd job. It is written before the
/// job exists and removed only after that job is proven absent and its
/// private root is gone.
#[derive(Clone, Debug, Deserialize, PartialEq, Eq, Serialize)]
pub struct GateReceipt {
    pub domain: String,
    pub label: String,
    pub plist: PathBuf,
    pub runtime_root: PathBuf,
    pub owner_uid: u32,
    /// Identity of the private root at declaration. Cleanup refuses a
    /// replaced root rather than deleting whatever now occupies the path.
    pub root_device: u64,
    pub root_inode: u64,
    /// The executable the job was first staged to run. Recorded for
    /// diagnosis; the fixture legitimately activates other versions later, so
    /// it is not a cleanup precondition.
    pub staged_digest: String,
    /// The first PID launchd reported, when it reported one. Diagnostic only:
    /// a PID observed minutes earlier can be recycled, so the authoritative
    /// proof is the launchctl absence of the exact label.
    pub first_pid: Option<u32>,
}

impl GateReceipt {
    #[must_use]
    pub fn service(&self) -> String {
        format!("{}/{}", self.domain, self.label)
    }
}

/// What a caller must supply to declare a disposable job.
#[derive(Clone, Debug)]
pub struct GateRequest<'a> {
    pub domain: &'a str,
    pub label: &'a str,
    pub plist: &'a Path,
    pub runtime_root: &'a Path,
    pub staged_executable: &'a Path,
}

/// One declared disposable launchd job, owned by the coordinator that holds
/// it. Dropping this value deliberately does nothing: the receipt on disk is
/// the authority, and only [`Self::finalize`] or [`resume`] may remove it.
#[derive(Debug)]
pub struct LaunchdGateInvocation {
    receipt: GateReceipt,
    receipt_path: PathBuf,
    launchctl: Launchctl,
}

impl LaunchdGateInvocation {
    /// Declares one disposable job and persists its receipt. Returns only
    /// after the receipt is durable, so the caller may bootstrap next and a
    /// crash between the two leaves a resumable record rather than an orphan.
    pub fn open(ledger: &Path, launchctl: Launchctl, request: GateRequest<'_>) -> GateResult<Self> {
        let label = request.label;
        if label == INSTALLED_LABEL || !label.starts_with(FIXTURE_LABEL_PREFIX) {
            return Err(GateError::refused(format!(
                "launchd gate label must start with {FIXTURE_LABEL_PREFIX}: {label}"
            )));
        }
        let suffix = &label[FIXTURE_LABEL_PREFIX.len()..];
        if suffix.is_empty() || !suffix.chars().all(|c| c.is_ascii_alphanumeric()) {
            return Err(GateError::refused(format!(
                "launchd gate label suffix must be a non-empty alphanumeric token: {label}"
            )));
        }
        if !is_domain_target(request.domain) {
            return Err(GateError::refused(format!(
                "launchd gate domain must be a domain target such as gui/501: {}",
                request.domain
            )));
        }

        let owner_uid = rustix::process::getuid().as_raw();
        let root = private_directory(request.runtime_root, owner_uid)?;
        if !request.plist.starts_with(request.runtime_root) {
            return Err(GateError::refused(format!(
                "launchd gate plist {} must live inside {}",
                request.plist.display(),
                request.runtime_root.display()
            )));
        }
        let staged_digest = digest(request.staged_executable)?;

        let receipt = GateReceipt {
            domain: request.domain.to_owned(),
            label: label.to_owned(),
            plist: request.plist.to_owned(),
            runtime_root: request.runtime_root.to_owned(),
            owner_uid,
            root_device: root.0,
            root_inode: root.1,
            staged_digest,
            first_pid: None,
        };

        // Refuse a label that is somehow already loaded rather than adopting
        // and later booting out a job this invocation did not create.
        match launchctl.state(&receipt.service()) {
            Ok(ServiceState::Absent) => {}
            Ok(ServiceState::Present) => {
                return Err(GateError::refused(format!(
                    "launchd gate label is already loaded: {}",
                    receipt.service()
                )));
            }
            Err(reason) => return Err(GateError::refused(reason)),
        }

        let receipt_path = receipt_path(ledger, label);
        create_private_dir(ledger)?;
        // The marker precedes the receipt: a receipt is only ever published
        // for a root this gate has already claimed.
        fs::write(request.runtime_root.join(ROOT_MARKER), label.as_bytes()).map_err(|error| {
            GateError::refused(format!(
                "could not claim launchd gate private root {}: {error}",
                request.runtime_root.display()
            ))
        })?;
        write_receipt(&receipt_path, &receipt)?;
        Ok(Self {
            receipt,
            receipt_path,
            launchctl,
        })
    }

    #[must_use]
    pub fn receipt(&self) -> &GateReceipt {
        &self.receipt
    }

    #[must_use]
    pub fn service(&self) -> String {
        self.receipt.service()
    }

    /// Records the first PID launchd reported. Later reports are ignored so
    /// the receipt keeps describing one invocation.
    pub fn record_first_pid(&mut self, pid: u32) -> GateResult<()> {
        if self.receipt.first_pid.is_some() {
            return Ok(());
        }
        self.receipt.first_pid = Some(pid);
        write_receipt(&self.receipt_path, &self.receipt)
    }

    /// Boots out exactly this label, proves it absent, revalidates the private
    /// root, removes it, and only then drops the receipt.
    pub fn finalize(self) -> GateResult<()> {
        finalize_receipt(&self.launchctl, &self.receipt, &self.receipt_path)
    }
}

/// Finalizes every receipt left in `ledger` by an earlier coordinator. It is
/// idempotent: a receipt whose job is already absent and whose root is already
/// gone is simply retired.
pub fn resume(ledger: &Path, launchctl: &Launchctl) -> GateResult<usize> {
    let mut paths = match fs::read_dir(ledger) {
        Ok(entries) => entries
            .filter_map(|entry| {
                let path = entry.ok()?.path();
                (path.extension() == Some(OsStr::new("json"))).then_some(path)
            })
            .collect::<Vec<_>>(),
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(0),
        Err(error) => {
            return Err(GateError::refused(format!(
                "could not read launchd gate ledger {}: {error}",
                ledger.display()
            )));
        }
    };
    paths.sort();

    let mut finalized = 0;
    let mut retained = Vec::new();
    for path in paths {
        match read_receipt(&path) {
            Ok(receipt) => match finalize_receipt(launchctl, &receipt, &path) {
                Ok(()) => finalized += 1,
                Err(error) => retained.push(error.to_string()),
            },
            Err(error) => retained.push(error.to_string()),
        }
    }
    if retained.is_empty() {
        return Ok(finalized);
    }
    Err(GateError::Retained {
        root: ledger.to_owned(),
        reason: retained.join("; "),
    })
}

fn finalize_receipt(
    launchctl: &Launchctl,
    receipt: &GateReceipt,
    receipt_path: &Path,
) -> GateResult<()> {
    if receipt.label == INSTALLED_LABEL || !receipt.label.starts_with(FIXTURE_LABEL_PREFIX) {
        return Err(GateError::Retained {
            root: receipt.runtime_root.clone(),
            reason: format!("refusing to finalize foreign label {}", receipt.label),
        });
    }
    let service = receipt.service();
    let retain = |reason: String| GateError::Retained {
        root: receipt.runtime_root.clone(),
        reason,
    };

    // Boot out only when the job is actually loaded, so a resumed receipt
    // does not send a second teardown for an operation already completed.
    match launchctl.state(&service).map_err(retain)? {
        ServiceState::Present => launchctl.bootout(&service).map_err(retain)?,
        ServiceState::Absent => {}
    }
    wait_absent(launchctl, &service).map_err(retain)?;

    match fs::symlink_metadata(&receipt.runtime_root) {
        Ok(metadata) => {
            if !metadata.is_dir()
                || metadata.uid() != receipt.owner_uid
                || metadata.dev() != receipt.root_device
                || metadata.ino() != receipt.root_inode
            {
                return Err(retain(format!(
                    "private root identity changed since {service} was declared"
                )));
            }
            let marker = receipt.runtime_root.join(ROOT_MARKER);
            if fs::read(&marker).ok().as_deref() != Some(receipt.label.as_bytes()) {
                return Err(retain(format!(
                    "{} does not claim {service}; refusing to delete it",
                    marker.display()
                )));
            }
            fs::remove_dir_all(&receipt.runtime_root)
                .map_err(|error| retain(format!("could not remove private root: {error}")))?;
        }
        Err(error) if error.kind() == io::ErrorKind::NotFound => {}
        Err(error) => return Err(retain(format!("could not inspect private root: {error}"))),
    }

    fs::remove_file(receipt_path).or_else(|error| match error.kind() {
        io::ErrorKind::NotFound => Ok(()),
        _ => Err(retain(format!("could not retire receipt: {error}"))),
    })
}

fn wait_absent(launchctl: &Launchctl, service: &str) -> Result<(), String> {
    let deadline = Instant::now() + ABSENCE_TIMEOUT;
    loop {
        match launchctl.state(service)? {
            ServiceState::Absent => return Ok(()),
            ServiceState::Present => {}
        }
        if Instant::now() >= deadline {
            return Err(format!(
                "{service} was still loaded after {ABSENCE_TIMEOUT:?}"
            ));
        }
        thread::sleep(ABSENCE_POLL);
    }
}

/// Accepts only the launchd domain targets a per-user fixture can own:
/// `system`, or a bare `<kind>/<uid>` such as `gui/501`. Anything carrying a
/// service name, whitespace, or a traversal component is refused, so a
/// receipt can never widen into another domain's service.
fn is_domain_target(domain: &str) -> bool {
    match domain.split_once('/') {
        None => domain == "system",
        Some((kind, uid)) => {
            !kind.is_empty()
                && kind.chars().all(|c| c.is_ascii_lowercase())
                && !uid.is_empty()
                && uid.chars().all(|c| c.is_ascii_digit())
        }
    }
}

fn receipt_path(ledger: &Path, label: &str) -> PathBuf {
    ledger.join(format!("{label}.json"))
}

fn private_directory(path: &Path, owner_uid: u32) -> GateResult<(u64, u64)> {
    let metadata = fs::symlink_metadata(path).map_err(|error| {
        GateError::refused(format!(
            "launchd gate private root {}: {error}",
            path.display()
        ))
    })?;
    if !metadata.is_dir() {
        return Err(GateError::refused(format!(
            "launchd gate private root must be a directory, not a symlink or file: {}",
            path.display()
        )));
    }
    if metadata.uid() != owner_uid || metadata.permissions().mode() & 0o077 != 0 {
        return Err(GateError::refused(format!(
            "launchd gate private root must be owned by {owner_uid} and unreadable by others: {}",
            path.display()
        )));
    }
    Ok((metadata.dev(), metadata.ino()))
}

fn digest(path: &Path) -> GateResult<String> {
    let metadata = fs::symlink_metadata(path).map_err(|error| {
        GateError::refused(format!(
            "launchd gate staged executable {}: {error}",
            path.display()
        ))
    })?;
    if !metadata.is_file() {
        return Err(GateError::refused(format!(
            "launchd gate staged executable must be a regular file: {}",
            path.display()
        )));
    }
    let mut file = File::open(path).map_err(|error| {
        GateError::refused(format!("could not read {}: {error}", path.display()))
    })?;
    let mut hasher = Sha256::new();
    io::copy(&mut file, &mut hasher).map_err(|error| {
        GateError::refused(format!("could not read {}: {error}", path.display()))
    })?;
    Ok(format!("{:x}", hasher.finalize()))
}

fn create_private_dir(path: &Path) -> GateResult<()> {
    fs::create_dir_all(path)
        .and_then(|()| fs::set_permissions(path, fs::Permissions::from_mode(0o700)))
        .map_err(|error| {
            GateError::refused(format!(
                "could not create launchd gate ledger {}: {error}",
                path.display()
            ))
        })
}

/// Publishes the receipt atomically and durably. The rename and the parent
/// directory are both flushed, so a power loss or kill after this call still
/// leaves a resumable record.
fn write_receipt(path: &Path, receipt: &GateReceipt) -> GateResult<()> {
    let publish = || -> io::Result<()> {
        let temp = path.with_extension("json.tmp");
        let mut file = File::create(&temp)?;
        file.set_permissions(fs::Permissions::from_mode(0o600))?;
        file.write_all(&serde_json::to_vec(receipt)?)?;
        file.sync_all()?;
        drop(file);
        fs::rename(&temp, path)?;
        if let Some(parent) = path.parent() {
            File::open(parent)?.sync_all()?;
        }
        Ok(())
    };
    publish().map_err(|error| {
        GateError::refused(format!(
            "could not record launchd gate receipt {}: {error}",
            path.display()
        ))
    })
}

fn read_receipt(path: &Path) -> GateResult<GateReceipt> {
    let content = fs::read(path).map_err(|error| {
        GateError::refused(format!(
            "could not read launchd gate receipt {}: {error}",
            path.display()
        ))
    })?;
    serde_json::from_slice(&content).map_err(|error| {
        GateError::refused(format!(
            "unreadable launchd gate receipt {}: {error}",
            path.display()
        ))
    })
}
