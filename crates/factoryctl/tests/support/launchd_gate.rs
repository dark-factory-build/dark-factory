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
//! Liveness is an advisory lock held open by the owning process. The kernel
//! releases it on death — including `SIGKILL` — so a resuming coordinator can
//! distinguish "the owner died holding this" from "another coordinator is
//! using this right now" without consulting a PID that may have been recycled.
//!
//! This is a release and managed-service fixture, not production code: it
//! lives in test support so nothing ships it in the operator CLI. It is
//! deliberately not an attempt `KernelResource` either — no Dark Factory
//! attempt creates a launchd job, and run terminalization never waits on one.
//!
//! Every proof here is exact. Absence is only launchctl's documented
//! not-found classification for one selected service; nothing scans global
//! processes, guesses success from an operational failure, or touches a label
//! this invocation did not create.

// Two test targets share this module and each uses a subset of it.
#![allow(dead_code)]

use std::{
    ffi::OsStr,
    fs::{self, File, OpenOptions},
    io::{self, Write},
    os::unix::{
        fs::{MetadataExt, PermissionsExt},
        process::CommandExt,
    },
    path::{Path, PathBuf},
    process::{Command, Output, Stdio},
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
    thread,
    time::{Duration, Instant},
};

use rustix::{
    fs::{FlockOperation, flock},
    io::Errno,
};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

/// Every disposable label this gate will create or finalize starts here. A
/// receipt naming anything else is refused rather than booted out.
pub const FIXTURE_LABEL_PREFIX: &str = "com.dark-factory.fixture.";

/// The operator's real service. It is never a gate label, never booted out,
/// and never removed.
pub const INSTALLED_LABEL: &str = "com.dark-factory.factoryd";

const ABSENCE_POLL: Duration = Duration::from_millis(50);
/// Polling launchctl costs two process spawns per attempt, so the interval
/// grows towards this ceiling instead of hammering a service that is taking
/// its time to unload.
const ABSENCE_POLL_MAX: Duration = Duration::from_millis(500);

/// How long each stage may take before it is a visible failure. Tests that
/// deliberately exercise a stuck service shorten these so they do not spend
/// the real budget waiting.
#[derive(Clone, Copy, Debug)]
pub struct Deadlines {
    pub command: Duration,
    pub absence: Duration,
    pub process: Duration,
}

impl Default for Deadlines {
    fn default() -> Self {
        Self {
            command: Duration::from_secs(15),
            absence: Duration::from_secs(30),
            process: Duration::from_secs(10),
        }
    }
}

/// launchctl's documented status and text for a service that is not loaded.
/// Status alone is not absence: a future launchctl could reuse the number, so
/// the classification string must agree before anything is deleted.
const NOT_FOUND_STATUS: i32 = 113;
const NOT_FOUND_TEXT: &str = "113: Could not find specified service";

/// Written inside a private root when it is claimed, and required before that
/// root is ever deleted. It holds a random claim token, not the label: a
/// corrupt or hand-written receipt could name any directory and carry that
/// directory's real device and inode, and those are not even distinguishing
/// on a filesystem that recycles an inode straight back after a delete. Only
/// a root this exact invocation created holds its unguessable token, so
/// nothing else can be removed. It is created exclusively, so a second
/// invocation cannot claim a root already in use.
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
    deadlines: Deadlines,
}

impl Launchctl {
    /// Selects the launchctl binary. The path must be absolute and
    /// executable, so a fixture cannot inherit one from `PATH`.
    pub fn new(binary: impl Into<PathBuf>) -> GateResult<Self> {
        let binary = binary.into();
        absolute(&binary, "launchctl")?;
        let metadata = fs::metadata(&binary).map_err(|error| {
            GateError::refused(format!("launchctl {}: {error}", binary.display()))
        })?;
        if !metadata.is_file() || metadata.permissions().mode() & 0o111 == 0 {
            return Err(GateError::refused(format!(
                "launchctl is not an executable file: {}",
                binary.display()
            )));
        }
        Ok(Self {
            binary,
            deadlines: Deadlines::default(),
        })
    }

    /// Shortens this launchctl's deadlines. Only fixtures that deliberately
    /// wait on a stuck service need it.
    #[must_use]
    pub fn with_deadlines(mut self, deadlines: Deadlines) -> Self {
        self.deadlines = deadlines;
        self
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

    /// Requests teardown of one exact service. The returned error is advisory:
    /// launchd reports routine teardown races (`3: No such process`,
    /// `36: Operation now in progress`) as failures even though the job is on
    /// its way out. Only the absence proof that follows decides.
    fn bootout(&self, service: &str) -> Result<(), String> {
        let output = self.run(&["bootout".as_ref(), service.as_ref()])?;
        if output.status.success() {
            return Ok(());
        }
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
            // Its own process group, so the watchdog can reach any child it
            // spawned. Killing only the direct child would leave a grandchild
            // holding the pipes open and `wait_with_output` blocked well past
            // the deadline the watchdog exists to enforce.
            .process_group(0)
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .spawn()
            .map_err(|error| format!("could not run {}: {error}", self.binary.display()))?;
        let pid = child.id();
        let command_timeout = self.deadlines.command;
        // `reaped` is set only after `wait_with_output` returns, so the
        // watchdog can never signal a PID this process has already reaped and
        // which the kernel could therefore have handed to someone else.
        let reaped = Arc::new(AtomicBool::new(false));
        let watchdog = {
            let reaped = Arc::clone(&reaped);
            thread::spawn(move || {
                let deadline = Instant::now() + command_timeout;
                while Instant::now() < deadline {
                    if reaped.load(Ordering::SeqCst) {
                        return false;
                    }
                    thread::sleep(Duration::from_millis(20));
                }
                // Re-check under the same ordering immediately before
                // signalling; a reap that beats this load leaves the child
                // unsignalled and the command simply succeeds.
                if reaped.load(Ordering::SeqCst) {
                    return false;
                }
                kill_group(pid);
                true
            })
        };
        let output = child
            .wait_with_output()
            .map_err(|error| format!("could not wait for launchctl: {error}"));
        reaped.store(true, Ordering::SeqCst);
        let timed_out = watchdog.join().unwrap_or(true);
        let output = output?;
        // A process that exited on its own produced a real answer, however
        // long the machine took to schedule it. Only one this watchdog
        // actually killed — and so left signalled — is a timeout.
        if timed_out && output.status.code().is_none() {
            return Err(format!(
                "{} {} exceeded {command_timeout:?}",
                self.binary.display(),
                args.join(OsStr::new(" ")).to_string_lossy()
            ));
        }
        Ok(output)
    }
}

/// Kills the command's whole process group. The child is spawned as its own
/// group leader, so this can never reach this process or its siblings.
fn kill_group(pid: u32) {
    if let Ok(pid) = i32::try_from(pid)
        && let Some(pid) = rustix::process::Pid::from_raw(pid)
    {
        let _ = rustix::process::kill_process_group(pid, rustix::process::Signal::KILL);
    }
}

/// Observation of one exact PID. Absence is only `ESRCH`; a permission error
/// means the process exists, and anything else is an operational failure.
fn process_present(pid: u32) -> Result<bool, String> {
    let Ok(raw) = i32::try_from(pid) else {
        return Err(format!("implausible pid {pid}"));
    };
    let Some(handle) = rustix::process::Pid::from_raw(raw) else {
        return Err(format!("implausible pid {pid}"));
    };
    match rustix::process::test_kill_process(handle) {
        Ok(()) => Ok(true),
        Err(Errno::PERM) => Ok(true),
        Err(Errno::SRCH) => Ok(false),
        Err(error) => Err(format!("could not observe pid {pid}: {error}")),
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
    /// Random per-invocation claim. The private root is deleted only if its
    /// marker still holds this exact value. Defaulted so a receipt written by
    /// an earlier build still decodes: an undecodable receipt fails every
    /// later run, and cannot be told apart from one describing a live job.
    #[serde(default)]
    pub claim_token: String,
    /// The executable the job was first staged to run. Recorded so a retained
    /// root can be traced back to what it was running.
    pub staged_digest: String,
    /// Every PID launchd reported for this job, in order. Finalization waits
    /// for each to be gone after proving the service absent, so a passing run
    /// leaves no test-owned daemon behind.
    #[serde(default)]
    pub observed_pids: Vec<u32>,
}

impl GateReceipt {
    #[must_use]
    pub fn service(&self) -> String {
        format!("{}/{}", self.domain, self.label)
    }

    #[must_use]
    pub fn first_pid(&self) -> Option<u32> {
        self.observed_pids.first().copied()
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
/// it. Dropping this value deliberately performs no teardown: the receipt on
/// disk is the authority and [`resume`] is the only finalizer. Dropping does
/// release the ownership lock, which is what lets the next coordinator tell
/// this invocation is no longer live.
#[derive(Debug)]
pub struct LaunchdGateInvocation {
    receipt: GateReceipt,
    receipt_path: PathBuf,
    lock: File,
}

impl LaunchdGateInvocation {
    /// Declares one disposable job and persists its receipt. Returns only
    /// after the receipt is durable, so the caller may bootstrap next and a
    /// crash between the two leaves a resumable record rather than an orphan.
    pub fn open(
        ledger: &Path,
        launchctl: &Launchctl,
        request: GateRequest<'_>,
    ) -> GateResult<Self> {
        let label = request.label;
        validate_label(label)?;
        if !is_domain_target(request.domain) {
            return Err(GateError::refused(format!(
                "launchd gate domain must be a domain target such as gui/501: {}",
                request.domain
            )));
        }

        // Every path is absolute. A relative root would resolve against
        // whichever directory a later coordinator happened to run from, and
        // its absence there would look like an already-clean root.
        absolute(ledger, "launchd gate ledger")?;
        absolute(request.runtime_root, "launchd gate private root")?;
        absolute(request.plist, "launchd gate plist")?;
        absolute(request.staged_executable, "launchd gate staged executable")?;
        if ledger.starts_with(request.runtime_root) {
            return Err(GateError::refused(format!(
                "launchd gate ledger {} must not live inside the root it records",
                ledger.display()
            )));
        }
        if !request.plist.starts_with(request.runtime_root) {
            return Err(GateError::refused(format!(
                "launchd gate plist {} must live inside {}",
                request.plist.display(),
                request.runtime_root.display()
            )));
        }

        let owner_uid = rustix::process::getuid().as_raw();
        let (root_device, root_inode) = private_directory(request.runtime_root, owner_uid)?;
        let staged_digest = digest(request.staged_executable)?;

        let receipt = GateReceipt {
            domain: request.domain.to_owned(),
            label: label.to_owned(),
            plist: request.plist.to_owned(),
            runtime_root: request.runtime_root.to_owned(),
            owner_uid,
            root_device,
            root_inode,
            staged_digest,
            claim_token: uuid::Uuid::new_v4().to_string(),
            observed_pids: Vec::new(),
        };

        create_private_dir(ledger)?;
        let receipt_path = receipt_path(ledger, label);
        // Take ownership before looking at the world, so two coordinators
        // racing on one label cannot both proceed.
        let lock = claim_lock(&lock_path(ledger, label))?;
        if receipt_path.exists() {
            return Err(GateError::refused(format!(
                "launchd gate ledger already records {label}; finalize it before reusing the label"
            )));
        }

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

        // The marker is exclusive and precedes the receipt: a receipt is only
        // ever published for a root this invocation alone has claimed.
        OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(request.runtime_root.join(ROOT_MARKER))
            .and_then(|mut file| file.write_all(receipt.claim_token.as_bytes()))
            .map_err(|error| {
                GateError::refused(format!(
                    "could not claim launchd gate private root {}: {error}",
                    request.runtime_root.display()
                ))
            })?;
        write_receipt(&receipt_path, &receipt)?;
        Ok(Self {
            receipt,
            receipt_path,
            lock,
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

    /// Records a PID launchd reported for this job. Repeats are ignored so the
    /// receipt stays a set of distinct observations.
    pub fn record_pid(&mut self, pid: u32) -> GateResult<()> {
        if self.receipt.observed_pids.contains(&pid) {
            return Ok(());
        }
        self.receipt.observed_pids.push(pid);
        write_receipt(&self.receipt_path, &self.receipt)
    }

    /// Releases ownership without finalizing, exactly as a killed coordinator
    /// would. The receipt stays for [`resume`].
    pub fn release(self) {
        drop(self.lock);
    }
}

/// What one resume pass did.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct ResumeReport {
    /// Receipts whose job is proven absent and whose root is gone.
    pub finalized: usize,
    /// Receipts skipped because a live coordinator still owns them.
    pub live: usize,
}

/// Finalizes every receipt in `ledger` whose owner is gone. A receipt still
/// held by a live coordinator is left alone, so concurrent runs cannot tear
/// down each other's jobs. It is idempotent: a receipt whose job is already
/// absent and whose root is already gone is simply retired.
pub fn resume(ledger: &Path, launchctl: &Launchctl) -> GateResult<ResumeReport> {
    let mut paths = match fs::read_dir(ledger) {
        Ok(entries) => entries
            .filter_map(|entry| {
                let path = entry.ok()?.path();
                (path.extension() == Some(OsStr::new("json"))).then_some(path)
            })
            .collect::<Vec<_>>(),
        Err(error) if error.kind() == io::ErrorKind::NotFound => {
            return Ok(ResumeReport::default());
        }
        Err(error) => {
            return Err(GateError::refused(format!(
                "could not read launchd gate ledger {}: {error}",
                ledger.display()
            )));
        }
    };
    paths.sort();

    let mut report = ResumeReport::default();
    let mut retained = Vec::new();
    for path in paths {
        let receipt = match read_receipt(&path) {
            Ok(receipt) => receipt,
            Err(error) => {
                retained.push(error.to_string());
                continue;
            }
        };
        // The filename is part of the identity: a receipt moved or renamed no
        // longer describes the label whose lock and marker guard it.
        if path.file_stem() != Some(OsStr::new(receipt.label.as_str())) {
            retained.push(format!(
                "{} does not match the label it records ({})",
                path.display(),
                receipt.label
            ));
            continue;
        }
        let lock = match try_claim_lock(&lock_path(ledger, &receipt.label)) {
            Ok(Some(lock)) => lock,
            Ok(None) => {
                report.live += 1;
                continue;
            }
            Err(error) => {
                retained.push(error.to_string());
                continue;
            }
        };
        match finalize_receipt(launchctl, &receipt, &path) {
            Ok(()) => {
                report.finalized += 1;
                drop(lock);
                let _ = fs::remove_file(lock_path(ledger, &receipt.label));
            }
            Err(error) => retained.push(error.to_string()),
        }
    }
    if retained.is_empty() {
        return Ok(report);
    }
    // The aggregate names the ledger; each retained root appears in its own
    // rendered reason.
    Err(GateError::Retained {
        root: ledger.to_owned(),
        reason: format!(
            "{} receipt(s) not finalized: {}",
            retained.len(),
            retained.join("; ")
        ),
    })
}

fn finalize_receipt(
    launchctl: &Launchctl,
    receipt: &GateReceipt,
    receipt_path: &Path,
) -> GateResult<()> {
    if validate_label(&receipt.label).is_err() {
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

    // Boot out only when the job is loaded, so a resumed receipt does not send
    // a second teardown for an operation already completed. A bootout error is
    // advisory: the absence proof decides, and only if that also fails does the
    // bootout diagnosis reach the operator.
    let bootout_error = match launchctl.state(&service).map_err(retain)? {
        ServiceState::Present => launchctl.bootout(&service).err(),
        ServiceState::Absent => None,
    };
    wait_absent(launchctl, &service).map_err(|reason| {
        retain(match bootout_error {
            Some(bootout) => format!("{reason} ({bootout})"),
            None => reason,
        })
    })?;
    // launchd reaps a service's process before reporting it absent, so this
    // should already hold; it is what proves no test-owned daemon survives.
    wait_processes_absent(&receipt.observed_pids, launchctl.deadlines.process).map_err(retain)?;

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
            // An absent claim is never a match. The field is defaulted so an
            // older receipt still decodes, and without this an empty marker
            // file would satisfy an empty token and authorise the deletion.
            let marker = receipt.runtime_root.join(ROOT_MARKER);
            if receipt.claim_token.is_empty() {
                return Err(retain(format!(
                    "the receipt for {service} carries no claim; refusing to delete {}",
                    receipt.runtime_root.display()
                )));
            }
            if fs::read(&marker).ok().as_deref() != Some(receipt.claim_token.as_bytes()) {
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
    let timeout = launchctl.deadlines.absence;
    let deadline = Instant::now() + timeout;
    let mut poll = ABSENCE_POLL;
    loop {
        match launchctl.state(service)? {
            ServiceState::Absent => return Ok(()),
            ServiceState::Present => {}
        }
        if Instant::now() >= deadline {
            return Err(format!("{service} was still loaded after {timeout:?}"));
        }
        thread::sleep(poll);
        poll = (poll * 2).min(ABSENCE_POLL_MAX);
    }
}

/// Waits for every recorded PID to be gone. A PID recorded before a crash can
/// in principle be recycled onto an unrelated process by the time a later
/// coordinator resumes; that can only hold the root back with a visible
/// failure, never turn a live job into a false success.
fn wait_processes_absent(pids: &[u32], timeout: Duration) -> Result<(), String> {
    let deadline = Instant::now() + timeout;
    for pid in pids {
        loop {
            if !process_present(*pid)? {
                break;
            }
            if Instant::now() >= deadline {
                return Err(format!("pid {pid} was still running after {timeout:?}"));
            }
            thread::sleep(ABSENCE_POLL);
        }
    }
    Ok(())
}

/// Takes the advisory ownership lock for one label, refusing if a live
/// coordinator already holds it.
fn claim_lock(path: &Path) -> GateResult<File> {
    match try_claim_lock(path) {
        Ok(Some(lock)) => Ok(lock),
        Ok(None) => Err(GateError::refused(format!(
            "another live coordinator owns {}",
            path.display()
        ))),
        Err(error) => Err(error),
    }
}

fn try_claim_lock(path: &Path) -> GateResult<Option<File>> {
    let file = OpenOptions::new()
        .create(true)
        .truncate(false)
        .read(true)
        .write(true)
        .open(path)
        .map_err(|error| {
            GateError::refused(format!(
                "could not open launchd gate lock {}: {error}",
                path.display()
            ))
        })?;
    match flock(&file, FlockOperation::NonBlockingLockExclusive) {
        Ok(()) => Ok(Some(file)),
        Err(Errno::WOULDBLOCK) => Ok(None),
        Err(error) => Err(GateError::refused(format!(
            "could not lock {}: {error}",
            path.display()
        ))),
    }
}

fn validate_label(label: &str) -> GateResult<()> {
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
    Ok(())
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

fn absolute(path: &Path, what: &str) -> GateResult<()> {
    if path.is_absolute() {
        return Ok(());
    }
    Err(GateError::refused(format!(
        "{what} must be an absolute path: {}",
        path.display()
    )))
}

fn receipt_path(ledger: &Path, label: &str) -> PathBuf {
    ledger.join(format!("{label}.json"))
}

fn lock_path(ledger: &Path, label: &str) -> PathBuf {
    ledger.join(format!("{label}.lock"))
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
            "unreadable launchd gate receipt {}: {error}. Confirm the service \
             it names is not loaded, then remove the file to unblock later runs",
            path.display()
        ))
    })
}
