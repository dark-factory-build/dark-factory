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
    env,
    fs::{self, File},
    io::Write,
    os::unix::fs::{MetadataExt, PermissionsExt},
    path::{Path, PathBuf},
    process::Command,
};

use factoryctl::launchd_gate::{
    FIXTURE_LABEL_PREFIX, GateError, GateRequest, INSTALLED_LABEL, Launchctl,
    LaunchdGateInvocation, ServiceState, resume,
};

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
            launchctl.clone(),
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
    drop(invocation);

    assert_eq!(resume(&world.ledger, &launchctl).unwrap(), 1);
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
    drop(invocation);

    assert_eq!(resume(&world.ledger, &launchctl).unwrap(), 1);
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
    drop(invocation);

    assert_eq!(resume(&world.ledger, &launchctl).unwrap(), 1);
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
    drop(invocation);
    assert!(world.root.exists());

    assert_eq!(resume(&world.ledger, &launchctl).unwrap(), 1);
    assert!(!world.root.exists());
    assert!(!world.receipt_path().exists());
}

#[test]
fn resume_converges_and_then_has_nothing_left_to_do() {
    let world = World::new("idempotent");
    let launchctl = world.launchctl();
    let invocation = world.open(&launchctl);
    world.bootstrap(&launchctl);
    drop(invocation);

    assert_eq!(resume(&world.ledger, &launchctl).unwrap(), 1);
    let after_first = world.count("bootout");
    assert_eq!(resume(&world.ledger, &launchctl).unwrap(), 0);
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
    drop(invocation);

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
    drop(invocation);

    let denied = world.launchctl_with("denied", 77, "77: Operation not permitted");
    let error = resume(&world.ledger, &denied).unwrap_err();
    assert!(matches!(error, GateError::Retained { .. }), "{error}");
    assert!(world.root.exists());
    assert!(world.receipt_path().exists());
}

#[test]
fn a_replaced_private_root_is_never_deleted() {
    let world = World::new("replaced");
    let launchctl = world.launchctl();
    let invocation = world.open(&launchctl);
    drop(invocation);

    // Someone swapped the path for a different directory after declaration.
    fs::remove_dir_all(&world.root).unwrap();
    fs::create_dir(&world.root).unwrap();
    fs::set_permissions(&world.root, fs::Permissions::from_mode(0o700)).unwrap();
    fs::write(world.root.join("someone-elses-work"), b"keep me").unwrap();

    let error = resume(&world.ledger, &launchctl).unwrap_err();
    assert!(matches!(error, GateError::Retained { .. }), "{error}");
    assert!(world.root.join("someone-elses-work").exists());
}

#[test]
fn the_installed_label_is_refused_at_declaration_and_at_finalization() {
    let world = World::new("installed");
    let launchctl = world.launchctl();
    let (plist, staged) = world.request();
    let refusal = LaunchdGateInvocation::open(
        &world.ledger,
        launchctl.clone(),
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

    // A hand-written or corrupted receipt must not turn resume into a
    // teardown of the operator's real service.
    let planted = world.ledger.join("planted.json");
    let mut file = File::create(&planted).unwrap();
    file.write_all(
        format!(
            r#"{{"domain":"gui/501","label":"{INSTALLED_LABEL}","plist":"{plist}",
                 "runtime_root":"{root}","owner_uid":{uid},"root_device":1,"root_inode":1,
                 "staged_digest":"","first_pid":null}}"#,
            plist = plist.display(),
            root = world.root.display(),
            uid = rustix::process::getuid().as_raw(),
        )
        .as_bytes(),
    )
    .unwrap();
    drop(file);

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
    let identity = fs::symlink_metadata(&victim).unwrap();

    let planted = world.ledger.join("planted.json");
    fs::write(
        &planted,
        serde_json::to_vec(&serde_json::json!({
            "domain": "gui/501",
            "label": world.label,
            "plist": victim.join("x.plist"),
            "runtime_root": victim,
            "owner_uid": rustix::process::getuid().as_raw(),
            "root_device": identity.dev(),
            "root_inode": identity.ino(),
            "staged_digest": "",
            "first_pid": serde_json::Value::Null,
        }))
        .unwrap(),
    )
    .unwrap();

    let error = resume(&world.ledger, &launchctl).unwrap_err();
    assert!(matches!(error, GateError::Retained { .. }), "{error}");
    assert!(
        victim.join("precious").exists(),
        "an unclaimed directory was deleted"
    );
}

#[test]
fn a_foreign_label_is_refused_before_any_effect() {
    let world = World::new("foreign");
    let launchctl = world.launchctl();
    let (plist, staged) = world.request();
    for label in ["com.example.other", "", FIXTURE_LABEL_PREFIX] {
        let error = LaunchdGateInvocation::open(
            &world.ledger,
            launchctl.clone(),
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
        launchctl,
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
    invocation.record_first_pid(4242).unwrap();
    invocation.record_first_pid(9999).unwrap();
    assert_eq!(invocation.receipt().first_pid, Some(4242));

    let stored: serde_json::Value =
        serde_json::from_slice(&fs::read(world.receipt_path()).unwrap()).unwrap();
    assert_eq!(stored["first_pid"], 4242);
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
        launchctl,
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
            "--nocapture",
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

    assert_eq!(resume(&world.ledger, &launchctl).unwrap(), 1);
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
        launchctl,
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
    drop(invocation);

    // Die the way a killed coordinator dies: no unwinding, no destructors, no
    // chance to finalize. Only the receipt on disk can save the job.
    let pid = rustix::process::getpid();
    rustix::process::kill_process(pid, rustix::process::Signal::KILL).unwrap();
    unreachable!("SIGKILL is not catchable");
}
