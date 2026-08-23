//! Containment proofs for disposable launchd jobs.
//!
//! These run everywhere, including Linux, against a fake `launchctl` that
//! honours the same status and classification contract as the real one. The
//! single real-launchd proof stays in `launchd_release.rs`.
//!
//! Coordinator death is modelled two ways. Most cuts drop the invocation
//! without finalizing: `LaunchdGateInvocation` has no `Drop` behaviour on
//! purpose, so a dropped value is exactly a coordinator that died holding it.
//! One test additionally `SIGKILL`s a real child process, proving the receipt
//! is durable across process boundaries and not merely across a scope.

use std::{
    env, fs,
    os::unix::fs::{MetadataExt, PermissionsExt},
    path::{Path, PathBuf},
    process::Command,
};

#[path = "support/launchd_gate.rs"]
mod launchd_gate;

use launchd_gate::{
    Deadlines, FIXTURE_LABEL_PREFIX, GateError, GateReceipt, GateRequest, INSTALLED_LABEL,
    Launchctl, LaunchdGateInvocation, ServiceState, resume,
};

/// Builds a planted receipt from the real type. A hand-written JSON literal
/// silently stops decoding the moment a field is added, which turns every
/// assertion after it into a check that the parser rejected the file -- not
/// that the guard under test refused it.
fn plant(ledger: &Path, label: &str, root: &Path, claim_token: &str) -> GateReceipt {
    let identity = fs::symlink_metadata(root).unwrap();
    let receipt = GateReceipt {
        domain: "gui/501".to_owned(),
        label: label.to_owned(),
        plist: root.join(format!("{label}.plist")),
        runtime_root: root.to_owned(),
        owner_uid: rustix::process::getuid().as_raw(),
        root_device: identity.dev(),
        root_inode: identity.ino(),
        staged_digest: String::new(),
        claim_token: claim_token.to_owned(),
        observed_pids: Vec::new(),
    };
    // Named for the label it records, so the filename check cannot be what
    // refuses it.
    fs::write(
        ledger.join(format!("{label}.json")),
        serde_json::to_vec(&receipt).unwrap(),
    )
    .unwrap();
    receipt
}

const CHILD_MODE: &str = "DARK_FACTORY_GATE_CHILD";

/// A disposable world: a ledger, a private root, a staged executable, and a
/// fake launchctl whose loaded-service set is a directory.
struct World {
    directory: tempfile::TempDir,
    root: PathBuf,
    ledger: PathBuf,
    state: PathBuf,
    log: PathBuf,
    binaries: PathBuf,
    label: String,
}

impl World {
    fn new(label_suffix: &str) -> Self {
        let directory = tempfile::tempdir().unwrap();
        let base = directory.path().to_owned();
        let root = base.join("root");
        let ledger = base.join("ledger");
        let state = base.join("loaded");
        let binaries = base.join("fakes");
        for path in [&root, &ledger, &state, &binaries] {
            fs::create_dir_all(path).unwrap();
            fs::set_permissions(path, fs::Permissions::from_mode(0o700)).unwrap();
        }
        Self {
            directory,
            root,
            ledger,
            state,
            log: base.join("launchctl.log"),
            binaries,
            label: format!("{FIXTURE_LABEL_PREFIX}{label_suffix}"),
        }
    }

    fn plist(&self) -> PathBuf {
        let path = self.root.join(format!("{}.plist", self.label));
        if !path.exists() {
            fs::write(&path, "<plist/>\n").unwrap();
        }
        path
    }

    fn staged(&self) -> PathBuf {
        let path = self.root.join("factoryd");
        if !path.exists() {
            fs::write(&path, "#!/bin/sh\nexit 0\n").unwrap();
        }
        path
    }

    /// Writes a fake launchctl. `missing_status` and `error_text` are baked in
    /// rather than read from the environment: mutating process-global
    /// environment from a test is both racy and forbidden under this
    /// workspace's `unsafe_code` lint.
    fn launchctl_with(&self, name: &str, missing_status: i32, error_text: &str) -> Launchctl {
        let path = self.binaries.join(name);
        let script = format!(
            r#"#!/bin/sh
state='{state}'
printf '%s\n' "$*" >>'{log}'
case "$1" in
    print)
        label=${{2##*/}}
        test -e "$state/$label" && exit 0
        exit {missing_status}
        ;;
    error) printf '%s\n' '{error_text}' ;;
    bootstrap)
        label=$(basename "$3" .plist)
        : >"$state/$label"
        ;;
    bootout)
        label=${{2##*/}}
        test -e "$state/$label" || exit 113
        rm -f "$state/$label"
        ;;
    *) exit 64 ;;
esac
exit 0
"#,
            state = self.state.display(),
            log = self.log.display(),
        );
        fs::write(&path, script).unwrap();
        fs::set_permissions(&path, fs::Permissions::from_mode(0o700)).unwrap();
        Launchctl::new(path).unwrap()
    }

    fn launchctl(&self) -> Launchctl {
        self.launchctl_with("launchctl", 113, "113: Could not find specified service")
    }

    fn request(&self) -> (PathBuf, PathBuf) {
        (self.plist(), self.staged())
    }

    fn open(&self, launchctl: &Launchctl) -> LaunchdGateInvocation {
        let (plist, staged) = self.request();
        LaunchdGateInvocation::open(
            &self.ledger,
            launchctl,
            GateRequest {
                domain: "gui/501",
                label: &self.label,
                plist: &plist,
                runtime_root: &self.root,
                staged_executable: &staged,
            },
        )
        .unwrap()
    }

    /// Loads the job the way the coordinator would, through the fake.
    fn bootstrap(&self, _launchctl: &Launchctl) {
        assert!(
            Command::new(self.binaries.join("launchctl"))
                .args(["bootstrap", "gui/501"])
                .arg(self.plist())
                .status()
                .unwrap()
                .success()
        );
        assert!(self.loaded());
    }

    fn loaded(&self) -> bool {
        self.state.join(&self.label).exists()
    }

    fn receipt_path(&self) -> PathBuf {
        self.ledger.join(format!("{}.json", self.label))
    }

    fn log(&self) -> String {
        fs::read_to_string(&self.log).unwrap_or_default()
    }

    fn count(&self, verb: &str) -> usize {
        self.log()
            .lines()
            .filter(|line| line.starts_with(verb))
            .count()
    }
}

#[test]
fn a_declared_job_is_resumable_before_it_is_ever_bootstrapped() {
    let world = World::new("declared");
    let launchctl = world.launchctl();
    let invocation = world.open(&launchctl);
    assert!(world.receipt_path().exists());
    // The coordinator dies here, before `launchctl bootstrap`.
    invocation.release();

    assert_eq!(resume(&world.ledger, &launchctl).unwrap().finalized, 1);
    assert!(!world.root.exists());
    assert!(!world.receipt_path().exists());
    assert_eq!(world.count("bootout"), 0, "nothing was loaded to boot out");
}

#[test]
fn a_resumed_receipt_boots_out_exactly_its_own_label() {
    let world = World::new("bootstrapped");
    let launchctl = world.launchctl();
    let invocation = world.open(&launchctl);
    world.bootstrap(&launchctl);
    invocation.release();

    assert_eq!(resume(&world.ledger, &launchctl).unwrap().finalized, 1);
    assert!(!world.loaded());
    assert!(!world.root.exists());
    assert!(!world.receipt_path().exists());
    assert_eq!(world.count("bootout"), 1);
    assert!(
        world
            .log()
            .lines()
            .filter(|line| line.starts_with("bootout"))
            .all(|line| line.ends_with(&world.label))
    );
    assert!(!world.log().contains(INSTALLED_LABEL));
}

#[test]
fn a_lost_bootout_response_does_not_send_a_second_teardown() {
    let world = World::new("lostresponse");
    let launchctl = world.launchctl();
    let invocation = world.open(&launchctl);
    world.bootstrap(&launchctl);
    // launchd applied the bootout; the coordinator died before reading the
    // response, so the receipt still describes a loaded job.
    fs::remove_file(world.state.join(&world.label)).unwrap();
    invocation.release();

    assert_eq!(resume(&world.ledger, &launchctl).unwrap().finalized, 1);
    assert_eq!(
        world.count("bootout"),
        0,
        "an already-absent service must not be booted out again"
    );
    assert!(!world.root.exists());
}

#[test]
fn a_death_before_root_deletion_still_removes_the_root() {
    let world = World::new("beforedelete");
    let launchctl = world.launchctl();
    let invocation = world.open(&launchctl);
    world.bootstrap(&launchctl);
    fs::remove_file(world.state.join(&world.label)).unwrap();
    invocation.release();
    assert!(world.root.exists());

    assert_eq!(resume(&world.ledger, &launchctl).unwrap().finalized, 1);
    assert!(!world.root.exists());
    assert!(!world.receipt_path().exists());
}

#[test]
fn resume_converges_and_then_has_nothing_left_to_do() {
    let world = World::new("idempotent");
    let launchctl = world.launchctl();
    let invocation = world.open(&launchctl);
    world.bootstrap(&launchctl);
    invocation.release();

    assert_eq!(resume(&world.ledger, &launchctl).unwrap().finalized, 1);
    let after_first = world.count("bootout");
    assert_eq!(resume(&world.ledger, &launchctl).unwrap().finalized, 0);
    assert_eq!(
        world.count("bootout"),
        after_first,
        "a second resume must not repeat the teardown"
    );
    assert!(!world.root.exists());
}

#[test]
fn an_ambiguous_absence_classification_retains_the_root() {
    let world = World::new("ambiguous");
    let honest = world.launchctl();
    let invocation = world.open(&honest);
    world.bootstrap(&honest);
    invocation.release();

    // Same status, different meaning: this must never be read as absence.
    let ambiguous = world.launchctl_with("ambiguous", 113, "113: injected ambiguity");
    let error = resume(&world.ledger, &ambiguous).unwrap_err();
    assert!(matches!(error, GateError::Retained { .. }), "{error}");
    assert!(world.root.exists(), "an unproven teardown keeps its root");
    assert!(world.receipt_path().exists(), "the receipt stays resumable");
}

#[test]
fn an_operational_launchctl_failure_retains_the_root() {
    let world = World::new("denied");
    let honest = world.launchctl();
    let invocation = world.open(&honest);
    world.bootstrap(&honest);
    invocation.release();

    let denied = world.launchctl_with("denied", 77, "77: Operation not permitted");
    let error = resume(&world.ledger, &denied).unwrap_err();
    assert!(matches!(error, GateError::Retained { .. }), "{error}");
    assert!(world.root.exists());
    assert!(world.receipt_path().exists());
}

/// A root swapped for a different directory at the same path must never be
/// deleted. The replacement carries a plausible marker, and on a filesystem
/// that recycles an inode straight back after a delete it also carries the
/// recorded device and inode — so only the unguessable claim token
/// distinguishes it.
#[test]
fn a_replaced_private_root_is_never_deleted() {
    let world = World::new("replaced");
    let launchctl = world.launchctl();
    let invocation = world.open(&launchctl);
    invocation.release();

    fs::remove_dir_all(&world.root).unwrap();
    fs::create_dir(&world.root).unwrap();
    fs::set_permissions(&world.root, fs::Permissions::from_mode(0o700)).unwrap();
    fs::write(world.root.join(".dark-factory-launchd-gate"), &world.label).unwrap();
    fs::write(world.root.join("someone-elses-work"), b"keep me").unwrap();

    let error = resume(&world.ledger, &launchctl).unwrap_err();
    assert!(matches!(error, GateError::Retained { .. }), "{error}");
    assert!(world.root.join("someone-elses-work").exists());
}

/// The claim token must do work on its own, independently of the recorded
/// device and inode. The directory is left exactly as it was — same inode,
/// same owner — and only the marker's contents change, so nothing but the
/// token can refuse this. Recreating the directory instead would let the
/// inode check refuse it on a filesystem that does not recycle inodes, and
/// the test would then prove nothing on that platform.
#[test]
fn a_root_whose_claim_no_longer_matches_is_never_deleted() {
    let world = World::new("forgedclaim");
    let launchctl = world.launchctl();
    let invocation = world.open(&launchctl);
    let before = fs::symlink_metadata(&world.root).unwrap();
    invocation.release();

    fs::write(world.root.join("still-in-use"), b"keep me").unwrap();
    // A plausible guess at the claim: the label, which is exactly what the
    // marker held before the token existed.
    fs::write(world.root.join(".dark-factory-launchd-gate"), &world.label).unwrap();
    let after = fs::symlink_metadata(&world.root).unwrap();
    assert_eq!(
        (before.dev(), before.ino()),
        (after.dev(), after.ino()),
        "the recorded identity must be untouched so only the claim can refuse"
    );

    let error = resume(&world.ledger, &launchctl).unwrap_err();
    assert!(matches!(error, GateError::Retained { .. }), "{error}");
    assert!(world.root.join("still-in-use").exists());
}

/// The claim is only worth anything if it cannot be predicted. A fixed token
/// would satisfy every other test here while letting anyone who has read the
/// source write a marker that authorises deletion.
#[test]
fn each_invocation_gets_its_own_unguessable_claim() {
    let first = World::new("claimone");
    let second = World::new("claimtwo");
    let first_open = first.open(&first.launchctl());
    let second_open = second.open(&second.launchctl());

    let first_token = first_open.receipt().claim_token.clone();
    let second_token = second_open.receipt().claim_token.clone();
    assert_ne!(
        first_token, second_token,
        "two invocations share a claim, so it is guessable from any one root"
    );
    assert!(
        !first_token.is_empty() && !first_token.contains(&first.label),
        "the claim must not be derived from the label: {first_token}"
    );
    // Each root carries its own claim and no other.
    assert_eq!(
        fs::read_to_string(first.root.join(".dark-factory-launchd-gate")).unwrap(),
        first_token
    );
    assert_eq!(
        fs::read_to_string(second.root.join(".dark-factory-launchd-gate")).unwrap(),
        second_token
    );
}

/// The recorded device and inode must still be revalidated in their own
/// right: a receipt describing a different directory than the one now at the
/// path is not a receipt this cleanup may act on.
#[test]
fn a_receipt_whose_recorded_identity_no_longer_matches_is_refused() {
    let world = World::new("identity");
    let launchctl = world.launchctl();
    let invocation = world.open(&launchctl);
    invocation.release();
    fs::write(world.root.join("still-in-use"), b"keep me").unwrap();

    let mut receipt: serde_json::Value =
        serde_json::from_slice(&fs::read(world.receipt_path()).unwrap()).unwrap();
    receipt["root_inode"] = serde_json::json!(receipt["root_inode"].as_u64().unwrap() + 1);
    fs::write(world.receipt_path(), serde_json::to_vec(&receipt).unwrap()).unwrap();

    let error = resume(&world.ledger, &launchctl).unwrap_err();
    assert!(matches!(error, GateError::Retained { .. }), "{error}");
    assert!(world.root.join("still-in-use").exists());
}

/// Two coordinators share one ledger. The one that does not own a receipt
/// must leave it alone rather than tearing down a job still in use.
#[test]
fn a_live_owner_is_never_finalized_by_another_coordinator() {
    let world = World::new("liveowner");
    let launchctl = world.launchctl();
    let invocation = world.open(&launchctl);
    world.bootstrap(&launchctl);

    // A second coordinator resumes the shared ledger while the first is live.
    let report = resume(&world.ledger, &launchctl).unwrap();
    assert_eq!(report.finalized, 0);
    assert_eq!(report.live, 1);
    assert!(world.loaded(), "a live coordinator's job was booted out");
    assert!(world.root.exists(), "a live coordinator's root was deleted");
    assert_eq!(world.count("bootout"), 0);

    // Once the owner is gone, the same call finalizes it.
    invocation.release();
    assert_eq!(resume(&world.ledger, &launchctl).unwrap().finalized, 1);
    assert!(!world.loaded());
    assert!(!world.root.exists());
    assert_eq!(world.count("bootout"), 1);
}

/// The receipt is the trust root, so a second declaration of a label the
/// ledger already records must be refused rather than overwriting it.
#[test]
fn a_second_declaration_cannot_overwrite_an_existing_receipt() {
    let world = World::new("collide");
    let launchctl = world.launchctl();
    let first = world.open(&launchctl);
    let recorded = fs::read(world.receipt_path()).unwrap();
    first.release();

    let second = world.root.parent().unwrap().join("second-root");
    fs::create_dir(&second).unwrap();
    fs::set_permissions(&second, fs::Permissions::from_mode(0o700)).unwrap();
    let plist = second.join(format!("{}.plist", world.label));
    fs::write(&plist, "<plist/>\n").unwrap();
    let staged = second.join("factoryd");
    fs::write(&staged, "#!/bin/sh\nexit 0\n").unwrap();

    let error = LaunchdGateInvocation::open(
        &world.ledger,
        &launchctl,
        GateRequest {
            domain: "gui/501",
            label: &world.label,
            plist: &plist,
            runtime_root: &second,
            staged_executable: &staged,
        },
    )
    .unwrap_err();
    assert!(matches!(error, GateError::Refused(_)), "{error}");
    assert_eq!(
        fs::read(world.receipt_path()).unwrap(),
        recorded,
        "the original receipt was clobbered"
    );
}

/// A root already claimed by another invocation must not be re-claimed; the
/// marker is what authorises deletion, so it is created exclusively.
#[test]
fn a_root_already_claimed_cannot_be_claimed_again() {
    let world = World::new("claimed");
    let launchctl = world.launchctl();
    let first = world.open(&launchctl);
    fs::write(world.root.join("first-invocations-work"), b"keep me").unwrap();
    first.release();
    fs::remove_file(world.receipt_path()).unwrap();

    let other = format!("{FIXTURE_LABEL_PREFIX}second");
    let plist = world.root.join(format!("{other}.plist"));
    fs::write(&plist, "<plist/>\n").unwrap();
    let error = LaunchdGateInvocation::open(
        &world.ledger,
        &launchctl,
        GateRequest {
            domain: "gui/501",
            label: &other,
            plist: &plist,
            runtime_root: &world.root,
            staged_executable: &world.staged(),
        },
    )
    .unwrap_err();
    assert!(matches!(error, GateError::Refused(_)), "{error}");
    assert!(world.root.join("first-invocations-work").exists());
}

/// A relative path would resolve against whichever directory a later
/// coordinator ran from, and its absence there would look like an
/// already-clean root. Each path is checked separately so one guard cannot
/// hide behind another.
#[test]
fn relative_paths_are_refused_rather_than_silently_resolved() {
    let world = World::new("relative");
    let launchctl = world.launchctl();
    let (plist, staged) = world.request();
    let relative_root = Path::new("relative-root");
    let cases: [(&Path, &Path, &Path, &Path, &str); 4] = [
        (
            &world.ledger,
            relative_root,
            Path::new("relative-root/x.plist"),
            &staged,
            "private root",
        ),
        (
            &world.ledger,
            &world.root,
            Path::new("relative.plist"),
            &staged,
            "plist",
        ),
        (
            &world.ledger,
            &world.root,
            &plist,
            Path::new("relative-factoryd"),
            "staged executable",
        ),
        (
            Path::new("relative-ledger"),
            &world.root,
            &plist,
            &staged,
            "ledger",
        ),
    ];
    for (ledger, runtime_root, plist, staged_executable, expected) in cases {
        let error = LaunchdGateInvocation::open(
            ledger,
            &launchctl,
            GateRequest {
                domain: "gui/501",
                label: &world.label,
                plist,
                runtime_root,
                staged_executable,
            },
        )
        .unwrap_err();
        let message = error.to_string();
        assert!(
            message.contains(expected) && message.contains("absolute"),
            "expected the {expected} to be refused as relative, got: {message}"
        );
    }
    assert!(!world.receipt_path().exists());
    // A refused relative path must not have been resolved against the
    // process's working directory on the way to being rejected.
    for stray in ["relative-root", "relative-ledger", "relative.plist"] {
        assert!(
            !Path::new(stray).exists(),
            "{stray} was created relative to the working directory"
        );
    }
}

/// launchd domains name a domain, never a service. A receipt that could carry
/// a service component could aim a teardown at something else entirely.
#[test]
fn a_domain_carrying_a_service_component_is_refused() {
    let world = World::new("domainscope");
    let launchctl = world.launchctl();
    let (plist, staged) = world.request();
    for domain in [
        "gui/501/com.dark-factory.factoryd",
        "",
        "GUI/501",
        "gui/abc",
    ] {
        let error = LaunchdGateInvocation::open(
            &world.ledger,
            &launchctl,
            GateRequest {
                domain,
                label: &world.label,
                plist: &plist,
                runtime_root: &world.root,
                staged_executable: &staged,
            },
        )
        .unwrap_err();
        assert!(matches!(error, GateError::Refused(_)), "{domain}: {error}");
    }
    assert_eq!(world.log(), "");
}

/// A private root readable by others could be tampered with between
/// declaration and cleanup.
#[test]
fn a_world_readable_private_root_is_refused() {
    let world = World::new("permissive");
    let launchctl = world.launchctl();
    fs::set_permissions(&world.root, fs::Permissions::from_mode(0o755)).unwrap();
    let (plist, staged) = world.request();
    let error = LaunchdGateInvocation::open(
        &world.ledger,
        &launchctl,
        GateRequest {
            domain: "gui/501",
            label: &world.label,
            plist: &plist,
            runtime_root: &world.root,
            staged_executable: &staged,
        },
    )
    .unwrap_err();
    assert!(matches!(error, GateError::Refused(_)), "{error}");
    assert!(!world.root.join(".dark-factory-launchd-gate").exists());
}

/// A service that never goes away must hold the root back with a visible
/// failure rather than being deleted anyway.
#[test]
fn a_service_that_never_unloads_retains_the_root() {
    let world = World::new("stuck");
    let honest = world.launchctl();
    let invocation = world.open(&honest);
    world.bootstrap(&honest);
    invocation.release();

    // This launchctl accepts the bootout but never stops reporting the job.
    let stuck = world.binaries.join("stuck");
    fs::write(
        &stuck,
        format!(
            "#!/bin/sh\nprintf '%s\\n' \"$*\" >>'{log}'\ncase \"$1\" in print) exit 0 ;; esac\nexit 0\n",
            log = world.log.display()
        ),
    )
    .unwrap();
    fs::set_permissions(&stuck, fs::Permissions::from_mode(0o700)).unwrap();

    let impatient = Launchctl::new(&stuck).unwrap().with_deadlines(Deadlines {
        absence: std::time::Duration::from_millis(300),
        ..Deadlines::default()
    });
    let error = resume(&world.ledger, &impatient).unwrap_err();
    assert!(matches!(error, GateError::Retained { .. }), "{error}");
    assert!(world.root.exists());
    assert!(world.receipt_path().exists());
}

/// A receipt renamed away from its label no longer matches the lock and
/// marker that guard it, so it must not be acted on.
#[test]
fn a_receipt_renamed_away_from_its_label_is_not_finalized() {
    let world = World::new("renamed");
    let launchctl = world.launchctl();
    let invocation = world.open(&launchctl);
    world.bootstrap(&launchctl);
    invocation.release();
    fs::rename(
        world.receipt_path(),
        world.ledger.join("something-else.json"),
    )
    .unwrap();

    let error = resume(&world.ledger, &launchctl).unwrap_err();
    assert!(matches!(error, GateError::Retained { .. }), "{error}");
    assert!(world.loaded(), "a mislabelled receipt drove a teardown");
    assert_eq!(world.count("bootout"), 0);
}

/// A recorded process that is still running must hold the root back: this is
/// what proves no test-owned daemon survives a passing run.
#[test]
fn a_still_running_recorded_process_retains_the_root() {
    let world = World::new("liveproc");
    let launchctl = world.launchctl();
    let mut invocation = world.open(&launchctl);
    let mut child = Command::new("/bin/sh")
        .args(["-c", "sleep 30"])
        .spawn()
        .unwrap();
    invocation.record_pid(child.id()).unwrap();
    invocation.release();

    let impatient = world.launchctl().with_deadlines(Deadlines {
        process: std::time::Duration::from_millis(300),
        ..Deadlines::default()
    });
    let error = resume(&world.ledger, &impatient).unwrap_err();
    assert!(matches!(error, GateError::Retained { .. }), "{error}");
    assert!(world.root.exists());

    child.kill().unwrap();
    child.wait().unwrap();
    assert_eq!(resume(&world.ledger, &launchctl).unwrap().finalized, 1);
    assert!(!world.root.exists());
}

#[test]
fn the_installed_label_is_refused_at_declaration_and_at_finalization() {
    let world = World::new("installed");
    let launchctl = world.launchctl();
    let (plist, staged) = world.request();
    let refusal = LaunchdGateInvocation::open(
        &world.ledger,
        &launchctl,
        GateRequest {
            domain: "gui/501",
            label: INSTALLED_LABEL,
            plist: &plist,
            runtime_root: &world.root,
            staged_executable: &staged,
        },
    )
    .unwrap_err();
    assert!(matches!(refusal, GateError::Refused(_)), "{refusal}");

    // A corrupted receipt must not turn resume into a teardown of the
    // operator's real service. Everything else about it is valid, so the
    // label refusal inside finalization is the only thing that can refuse it.
    plant(&world.ledger, INSTALLED_LABEL, &world.root, "irrelevant");

    let error = resume(&world.ledger, &launchctl).unwrap_err();
    assert!(matches!(error, GateError::Retained { .. }), "{error}");
    assert!(
        !world.log().contains(INSTALLED_LABEL),
        "the installed service was queried or booted out: {}",
        world.log()
    );
    assert!(world.root.exists());
}

/// A receipt is a plain file in a directory. If cleanup trusted the path and
/// identity it names, a corrupt or hand-written receipt could aim
/// `remove_dir_all` at anything and supply that directory's real device and
/// inode. Only a root this gate claimed may be deleted.
#[test]
fn a_receipt_cannot_aim_cleanup_at_a_directory_the_gate_never_claimed() {
    let world = World::new("unclaimed");
    let launchctl = world.launchctl();
    let victim = world.directory.path().join("someone-elses-home");
    fs::create_dir(&victim).unwrap();
    fs::set_permissions(&victim, fs::Permissions::from_mode(0o700)).unwrap();
    fs::write(victim.join("precious"), b"keep me").unwrap();

    // The receipt carries the victim's real device, inode and owner, so the
    // identity revalidation passes and only the missing claim can refuse it.
    plant(
        &world.ledger,
        &world.label,
        &victim,
        "a-token-no-marker-holds",
    );

    let error = resume(&world.ledger, &launchctl).unwrap_err();
    assert!(matches!(error, GateError::Retained { .. }), "{error}");
    assert!(
        victim.join("precious").exists(),
        "an unclaimed directory was deleted"
    );
}

/// A wedged launchctl must fail visibly rather than hanging the gate, and a
/// killed probe must never be read as absence.
#[test]
fn a_wedged_launchctl_times_out_instead_of_hanging() {
    let world = World::new("wedged");
    let wedged = world.binaries.join("wedged");
    fs::write(&wedged, "#!/bin/sh\nsleep 30\n").unwrap();
    fs::set_permissions(&wedged, fs::Permissions::from_mode(0o700)).unwrap();
    let launchctl = Launchctl::new(&wedged).unwrap().with_deadlines(Deadlines {
        command: std::time::Duration::from_millis(200),
        ..Deadlines::default()
    });

    let started = std::time::Instant::now();
    let error = launchctl
        .state(&format!("gui/501/{}", world.label))
        .unwrap_err();
    assert!(
        started.elapsed() < std::time::Duration::from_secs(10),
        "the gate hung"
    );
    assert!(error.contains("exceeded"), "{error}");
}

#[test]
fn a_foreign_label_is_refused_before_any_effect() {
    let world = World::new("foreign");
    let launchctl = world.launchctl();
    let (plist, staged) = world.request();
    for label in ["com.example.other", "", FIXTURE_LABEL_PREFIX] {
        let error = LaunchdGateInvocation::open(
            &world.ledger,
            &launchctl,
            GateRequest {
                domain: "gui/501",
                label,
                plist: &plist,
                runtime_root: &world.root,
                staged_executable: &staged,
            },
        )
        .unwrap_err();
        assert!(matches!(error, GateError::Refused(_)), "{label}: {error}");
    }
    assert_eq!(world.log(), "", "a refused label ran no launchctl command");
}

#[test]
fn an_already_loaded_label_is_refused_rather_than_adopted() {
    let world = World::new("collision");
    let launchctl = world.launchctl();
    fs::write(world.state.join(&world.label), b"").unwrap();

    let (plist, staged) = world.request();
    let error = LaunchdGateInvocation::open(
        &world.ledger,
        &launchctl,
        GateRequest {
            domain: "gui/501",
            label: &world.label,
            plist: &plist,
            runtime_root: &world.root,
            staged_executable: &staged,
        },
    )
    .unwrap_err();
    assert!(matches!(error, GateError::Refused(_)), "{error}");
    assert!(!world.receipt_path().exists());
    assert_eq!(world.count("bootout"), 0);
}

#[test]
fn the_receipt_keeps_the_first_reported_pid() {
    let world = World::new("firstpid");
    let launchctl = world.launchctl();
    let mut invocation = world.open(&launchctl);
    invocation.record_pid(4242).unwrap();
    invocation.record_pid(9999).unwrap();
    assert_eq!(invocation.receipt().first_pid(), Some(4242));

    let stored: serde_json::Value =
        serde_json::from_slice(&fs::read(world.receipt_path()).unwrap()).unwrap();
    assert_eq!(stored["observed_pids"][0], 4242);
    assert_eq!(stored["label"], world.label);
}

#[test]
fn a_plist_outside_the_private_root_is_refused() {
    let world = World::new("outside");
    let launchctl = world.launchctl();
    let outside = world.directory.path().join("elsewhere.plist");
    fs::write(&outside, "<plist/>\n").unwrap();
    let error = LaunchdGateInvocation::open(
        &world.ledger,
        &launchctl,
        GateRequest {
            domain: "gui/501",
            label: &world.label,
            plist: &outside,
            runtime_root: &world.root,
            staged_executable: &world.staged(),
        },
    )
    .unwrap_err();
    assert!(matches!(error, GateError::Refused(_)), "{error}");
}

#[test]
fn state_reports_presence_and_absence_from_the_documented_contract() {
    let world = World::new("states");
    let launchctl = world.launchctl();
    let service = format!("gui/501/{}", world.label);
    assert_eq!(launchctl.state(&service).unwrap(), ServiceState::Absent);
    fs::write(world.state.join(&world.label), b"").unwrap();
    assert_eq!(launchctl.state(&service).unwrap(), ServiceState::Present);
}

/// Hard process death: a real coordinator is `SIGKILL`ed after it bootstrapped
/// its job, then a fresh process resumes the receipt it left behind.
#[test]
fn a_sigkilled_coordinator_leaves_exactly_one_resumable_invocation() {
    let world = World::new("sigkilled");
    let launchctl = world.launchctl();

    let status = Command::new(env::current_exe().unwrap())
        .args([
            "--exact",
            "gate_coordinator_child",
            "--ignored",
            "--test-threads=1",
        ])
        .env(CHILD_MODE, &world.ledger)
        .env("DARK_FACTORY_GATE_CHILD_ROOT", &world.root)
        .env("DARK_FACTORY_GATE_CHILD_LABEL", &world.label)
        .env(
            "DARK_FACTORY_GATE_CHILD_LAUNCHCTL",
            world.binaries.join("launchctl"),
        )
        .status()
        .unwrap();
    assert!(
        status.code().is_none(),
        "the child must die by signal, not exit: {status:?}"
    );

    assert!(world.loaded(), "the child bootstrapped before dying");
    assert!(
        world.receipt_path().exists(),
        "the receipt outlived SIGKILL"
    );

    assert_eq!(resume(&world.ledger, &launchctl).unwrap().finalized, 1);
    assert!(!world.loaded());
    assert!(!world.root.exists());
    assert!(!world.receipt_path().exists());
    assert_eq!(world.count("bootout"), 1);
}

/// The child half of the `SIGKILL` proof. Ignored by default; the parent runs
/// it by exact name so no separate build target is needed.
#[test]
#[ignore = "child process driven by a_sigkilled_coordinator_leaves_exactly_one_resumable_invocation"]
fn gate_coordinator_child() {
    let Ok(ledger) = env::var(CHILD_MODE) else {
        panic!("child mode requires {CHILD_MODE}");
    };
    let root = PathBuf::from(env::var("DARK_FACTORY_GATE_CHILD_ROOT").unwrap());
    let label = env::var("DARK_FACTORY_GATE_CHILD_LABEL").unwrap();
    let binary = PathBuf::from(env::var("DARK_FACTORY_GATE_CHILD_LAUNCHCTL").unwrap());
    let launchctl = Launchctl::new(&binary).unwrap();

    let plist = root.join(format!("{label}.plist"));
    fs::write(&plist, "<plist/>\n").unwrap();
    let staged = root.join("factoryd");
    fs::write(&staged, "#!/bin/sh\nexit 0\n").unwrap();

    let invocation = LaunchdGateInvocation::open(
        Path::new(&ledger),
        &launchctl,
        GateRequest {
            domain: "gui/501",
            label: &label,
            plist: &plist,
            runtime_root: &root,
            staged_executable: &staged,
        },
    )
    .unwrap();
    assert!(
        Command::new(&binary)
            .args(["bootstrap", "gui/501"])
            .arg(&plist)
            .status()
            .unwrap()
            .success()
    );
    invocation.release();

    // Die the way a killed coordinator dies: no unwinding, no destructors, no
    // chance to finalize. Only the receipt on disk can save the job.
    let pid = rustix::process::getpid();
    rustix::process::kill_process(pid, rustix::process::Signal::KILL).unwrap();
    unreachable!("SIGKILL is not catchable");
}
