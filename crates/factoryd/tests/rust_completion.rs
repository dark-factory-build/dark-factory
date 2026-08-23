//! Daemon-integrated `RustWorkspaceTest` completion proofs.
//!
//! These tests drive the production execution manager against a real Cargo and
//! rustc toolchain and a generated one-crate workspace held as the attempt's
//! Change. Nothing here fakes Cargo, the verifier worker, the exec gate, or the
//! resource ledger: the only seeded step is the attempt launch itself, which
//! [`crates/factory-runner/tests/execution_observer_restart.rs`] already proves
//! separately.
//!
//! Every wait is bounded only so a hang reports what did not happen. The
//! ordering claims are asserted as implications sampled on every poll, so they
//! hold no matter how slow the machine is.

use std::{
    fs, io,
    os::unix::fs::{MetadataExt, PermissionsExt},
    path::{Path, PathBuf},
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
    thread,
    time::{Duration, Instant},
};

use factory_core::{
    AgentId, AgentRole, ChangeId, CompletionVerification, ProjectId, Provider, RunFailureReason,
    RunId, RunOutcome, RunPhase, RunSnapshot, RunnerInstanceId, TaskId, TaskStatus,
    local::{
        ErrorCode, LocalRequest, LocalResponse, RequestCredential, RequestEnvelope, ServerFrame,
    },
};
use factoryd::{
    daemon_state::DaemonState,
    execution, local_api, providers,
    runner_client::{PreparedRunner, RunnerClient},
    runner_process::{self, LaunchSpec, ProviderEnvironment},
    store::{
        AdmittedRun, ChangeBaseIdentity, ChangeMaterialization, ChangeReservation,
        ChangeSourceIdentity, KernelResource, KernelResourceKind, KernelResourceState, NewAgent,
        NewProject, NewRunAdmission, NewTask, PreparedProcessIdentity, RustCompletionCheck,
        RustCompletionPhase, Store,
    },
};
use rustix::{
    io::Errno,
    process::{Pid, WaitOptions, test_kill_process, waitpid},
};
use sha2::{Digest, Sha256};
use tokio::{
    io::{AsyncBufReadExt, AsyncWriteExt, BufReader},
    net::{UnixListener, UnixStream},
    sync::oneshot,
    task::JoinHandle,
};

const RUN_ID: &str = "11111111-1111-4111-8111-111111111111";
const RUNNER_INSTANCE_ID: &str = "22222222-2222-4222-8222-222222222222";
const RUNTIME_NONCE: &str = "33333333333343338333333333333333";
const CHANGE_ID: &str = "44444444-4444-4444-8444-444444444444";
const BASE_OID: &str = "0123456789abcdef0123456789abcdef01234567";
const BEARER: &str = "attempt-secret";
const OPERATOR: &str = "operator-secret";

/// Generous ceilings. They exist so a hang fails with a precise message, not
/// so the test measures speed: a real Cargo build on a loaded hosted runner is
/// far slower than on a developer machine, and the daemon's own verification
/// budget is thirty minutes.
const VERIFICATION_BUDGET: Duration = Duration::from_secs(600);
const PROCESS_BUDGET: Duration = Duration::from_secs(60);
const POLL: Duration = Duration::from_millis(20);
/// How long the reap gate is held open once the attempt's process group is
/// provably live. See its single use: this tunes mutation sensitivity only.
const REAP_GATE_WINDOW: Duration = Duration::from_secs(3);

#[derive(Clone, Copy, Eq, PartialEq)]
enum FixtureTest {
    /// Passes without touching anything the daemon owns.
    Passes,
    /// Mutates its own exact source snapshot while running.
    MutatesSnapshot,
    /// Leaves one descendant alive in the verifier's process group, then
    /// blocks until the test releases it.
    HoldsGroupOpen,
}

struct Fixture {
    _root: tempfile::TempDir,
    database: PathBuf,
    runtime_root: PathBuf,
    runtime: PathBuf,
    changes_root: PathBuf,
    change: PathBuf,
    artifacts_root: PathBuf,
    guidance_root: PathBuf,
    socket: PathBuf,
    cargo: PathBuf,
    provider: PathBuf,
    provider_marker: PathBuf,
    provider_release: PathBuf,
    ran_log: PathBuf,
    test_started: PathBuf,
    test_release: PathBuf,
    descendant_release: PathBuf,
    attempt_group_release: PathBuf,
    run_id: RunId,
    runner_instance_id: RunnerInstanceId,
    project_id: ProjectId,
    agent_id: AgentId,
    task_id: TaskId,
    change_id: ChangeId,
}

impl Fixture {
    fn new(kind: FixtureTest) -> Self {
        let root = tempfile::Builder::new()
            .prefix("df-completion-")
            .tempdir_in("/tmp")
            .unwrap();
        fs::set_permissions(root.path(), fs::Permissions::from_mode(0o700)).unwrap();
        let base = root.path().to_owned();
        assert!(
            base.to_str()
                .is_some_and(|text| text.is_ascii() && !text.contains(['"', '\\', '\''])),
            "the fixture embeds its temporary paths in generated Rust and shell source"
        );
        let runtime_root = base.join("runs");
        private_directory(&runtime_root);
        let runtime = runtime_root.join(RUNTIME_NONCE);
        private_directory(&runtime);
        private_directory(&runtime.join("policy"));
        let changes_root = base.join("changes");
        private_directory(&changes_root);
        let change_uuid = uuid::Uuid::parse_str(CHANGE_ID).unwrap();
        let change = changes_root.join(change_uuid.simple().to_string());
        private_directory(&change);
        let guidance_root = base.join("guidance");
        private_directory(&guidance_root);

        let fixture = Self {
            database: base.join("factory.db"),
            runtime_root,
            runtime,
            changes_root,
            change,
            artifacts_root: base.join("artifacts"),
            guidance_root,
            socket: base.join("f.sock"),
            cargo: trusted_toolchain_cargo(),
            provider: base.join("provider.sh"),
            provider_marker: base.join("provider-ran"),
            provider_release: base.join("provider-release"),
            ran_log: base.join("fixture-test-ran.log"),
            test_started: base.join("fixture-test-started"),
            test_release: base.join("fixture-test-release"),
            descendant_release: base.join("descendant-release"),
            attempt_group_release: base.join("attempt-group-release"),
            run_id: RunId::try_from(RUN_ID).unwrap(),
            runner_instance_id: RunnerInstanceId::try_from(RUNNER_INSTANCE_ID).unwrap(),
            project_id: ProjectId::try_from("factory").unwrap(),
            agent_id: AgentId::try_from("worker").unwrap(),
            task_id: TaskId::try_from("task-1").unwrap(),
            change_id: ChangeId::try_from(CHANGE_ID).unwrap(),
            _root: root,
        };
        fixture.write_workspace(kind);
        fs::write(
            &fixture.provider,
            "#!/bin/sh\nset -eu\n             (trap '' TERM; while [ ! -e \"$3\" ]; do /bin/sleep 0.05; done) &\n             printf x >> \"$1\"\n             while [ ! -e \"$2\" ]; do /bin/sleep 0.01; done\nexit 0\n",
        )
        .unwrap();
        fs::set_permissions(&fixture.provider, fs::Permissions::from_mode(0o700)).unwrap();
        if kind != FixtureTest::Passes {
            // Only the reap-ordering proof needs the attempt's process group
            // to outlive its leader.
            fs::write(&fixture.attempt_group_release, b"release").unwrap();
        }
        fixture
    }

    /// Writes the smallest real Cargo workspace that still exercises a full
    /// compile, link, and test-binary launch.
    fn write_workspace(&self, kind: FixtureTest) {
        fs::write(
            self.change.join("Cargo.toml"),
            "[package]\nname = \"df-completion-fixture\"\nversion = \"0.0.0\"\nedition = \"2021\"\n\n[lib]\npath = \"src/lib.rs\"\n",
        )
        .unwrap();
        fs::write(
            self.change.join("Cargo.lock"),
            "version = 3\n\n[[package]]\nname = \"df-completion-fixture\"\nversion = \"0.0.0\"\n",
        )
        .unwrap();
        fs::write(self.change.join("revision.txt"), "A\n").unwrap();
        fs::create_dir(self.change.join("src")).unwrap();
        let body = match kind {
            FixtureTest::Passes => String::new(),
            FixtureTest::MutatesSnapshot => {
                "    std::fs::write(\"revision.txt\", b\"B\").unwrap();\n".to_owned()
            }
            FixtureTest::HoldsGroupOpen => format!(
                "    std::process::Command::new(\"/bin/sh\")\n\
                 \x20       .arg(\"-c\")\n\
                 \x20       .arg(\"while [ ! -e '{descendant}' ]; do /bin/sleep 0.05; done\")\n\
                 \x20       .stdin(std::process::Stdio::null())\n\
                 \x20       .stdout(std::process::Stdio::null())\n\
                 \x20       .stderr(std::process::Stdio::null())\n\
                 \x20       .spawn()\n\
                 \x20       .unwrap();\n\
                 \x20   std::fs::write(\"{started}\", b\"started\").unwrap();\n\
                 \x20   while !std::path::Path::new(\"{release}\").exists() {{\n\
                 \x20       std::thread::sleep(std::time::Duration::from_millis(20));\n\
                 \x20   }}\n",
                descendant = self.descendant_release.display(),
                started = self.test_started.display(),
                release = self.test_release.display(),
            ),
        };
        fs::write(
            self.change.join("src/lib.rs"),
            format!(
                "#[test]\n\
                 fn fixture_completion_test() {{\n\
                 \x20   use std::io::Write as _;\n\
                 \x20   let mut log = std::fs::OpenOptions::new()\n\
                 \x20       .create(true)\n\
                 \x20       .append(true)\n\
                 \x20       .open(\"{log}\")\n\
                 \x20       .unwrap();\n\
                 \x20   writeln!(log, \"ran\").unwrap();\n\
                 \x20   log.sync_all().unwrap();\n\
                 {body}}}\n",
                log = self.ran_log.display(),
            ),
        )
        .unwrap();
    }

    fn reopen(&self) -> Store {
        Store::open(&self.database).unwrap()
    }

    fn execution_config(&self) -> execution::Config {
        execution::Config {
            factoryd_program: factoryd_program(),
            runner_program: runner_program(),
            factoryctl_path: PathBuf::from("/usr/bin/true"),
            git_program: PathBuf::from("/usr/bin/false"),
            claude_installation: None,
            codex_provider: providers::codex::CodexProvider::new(None),
            cargo_program: Some(self.cargo.clone()),
            runtime_root: self.runtime_root.clone(),
            changes_root: self.changes_root.clone(),
            artifacts_root: self.artifacts_root.clone(),
            guidance_root: self.guidance_root.clone(),
            socket_path: self.socket.clone(),
            max_active_runs: 1,
        }
    }

    /// Seeds one admitted worker attempt whose Change is the generated
    /// workspace, with the project's fixed Rust completion policy selected.
    fn seed(&self) -> (Store, AdmittedRun) {
        let mut store = self.reopen();
        store
            .create_project(
                NewProject {
                    id: self.project_id.clone(),
                    name: "Factory".into(),
                    root: self.changes_root.to_string_lossy().into_owned(),
                },
                1,
            )
            .unwrap();
        store
            .set_project_completion_verification(
                &self.project_id,
                CompletionVerification::RustWorkspaceTest,
                2,
            )
            .unwrap();
        store
            .create_agent(
                NewAgent {
                    id: self.agent_id.clone(),
                    project_id: self.project_id.clone(),
                    parent_agent_id: None,
                    role: AgentRole::Worker,
                    provider: Provider::Shell,
                },
                3,
            )
            .unwrap();
        store
            .create_assigned_task(
                NewTask {
                    id: self.task_id.clone(),
                    project_id: self.project_id.clone(),
                    parent_task_id: None,
                    title: "prove daemon-integrated Rust completion".into(),
                    body: String::new(),
                    priority: 0,
                },
                self.agent_id.clone(),
                4,
            )
            .unwrap();
        let admitted = store
            .admit_next_run(
                NewRunAdmission {
                    run_id: self.run_id.clone(),
                    project_id: self.project_id.clone(),
                    agent_id: self.agent_id.clone(),
                    capability_digest: capability_digest(BEARER),
                    runtime_claim: format!("runtime-claim:{RUNTIME_NONCE}"),
                    runner_instance_id: self.runner_instance_id.clone(),
                    runner_runtime: self.runtime.to_string_lossy().into_owned(),
                    max_active_runs: 1,
                    change_reservation: ChangeReservation {
                        id: self.change_id.clone(),
                        source_root: self.change.to_string_lossy().into_owned(),
                        max_factory_changes: 1,
                    },
                    policy_cwd: self.runtime.join("policy").to_string_lossy().into_owned(),
                },
                5,
            )
            .unwrap()
            .expect("the queued task must be admitted");

        let base = ChangeBaseIdentity {
            repository_root: self.changes_root.to_string_lossy().into_owned(),
            device: 1,
            inode: 2,
        };
        store
            .record_change_base(&self.project_id, &self.change_id, 0, BASE_OID, &base, 6)
            .unwrap();
        let identity = fs::symlink_metadata(&self.change).unwrap();
        store
            .mark_change_available(
                &self.project_id,
                &self.change_id,
                1,
                &ChangeMaterialization {
                    base_oid: BASE_OID.into(),
                    base,
                    source: ChangeSourceIdentity {
                        source_root: self.change.to_string_lossy().into_owned(),
                        device: identity.dev(),
                        inode: identity.ino(),
                        size_bytes: 4096,
                    },
                },
                7,
            )
            .unwrap();
        (store, admitted)
    }

    /// Launches the exact runner and provider this attempt owns. Only the
    /// launch handshake is manual; every later transition is production code.
    async fn launch(
        &self,
        store: &mut Store,
        admitted: &AdmittedRun,
    ) -> (OwnedRunner, PreparedRunner, u32) {
        store
            .register_admitted_runtime(
                &self.run_id,
                &runtime_locator(&self.runtime),
                &admitted.target.runtime_claim,
                &runtime_birth(&self.runtime),
                8,
            )
            .unwrap();
        let setup = runner_process::prepare_runner(LaunchSpec {
            runner_program: runner_program(),
            factoryctl_path: PathBuf::from("/usr/bin/true"),
            provider_program: self.provider.clone(),
            provider_arguments: vec![
                self.provider_marker.as_os_str().to_owned(),
                self.provider_release.as_os_str().to_owned(),
                self.attempt_group_release.as_os_str().to_owned(),
            ],
            provider_environment: ProviderEnvironment::Inherited,
            attempt_environment: Vec::new(),
            run_id: self.run_id.clone(),
            runner_instance_id: self.runner_instance_id.clone(),
            runtime_dir: self.runtime.clone(),
            cwd: self.change.clone(),
            source_root: self.change.clone(),
            startup_input: Vec::new(),
        })
        .await
        .unwrap();
        let setup_locator = runner_setup_locator(setup.setup_path(), &self.runner_instance_id);
        let setup_birth = format!(
            "unix-device:{}:inode:{}",
            setup.setup_device(),
            setup.setup_inode()
        );
        store
            .register_admitted_runner_setup(&self.run_id, &setup_locator, &setup_birth, 9)
            .unwrap();
        let prepared_runner = setup.spawn().unwrap();
        let runner_pid = prepared_runner.child_pid();
        let runner_locator = runner_locator(runner_pid, &self.runner_instance_id);
        let runner_birth = weak_birth(runner_pid).unwrap();
        store
            .register_admitted_runner(
                &self.run_id,
                &setup_locator,
                &setup_birth,
                &runner_locator,
                &runner_birth,
                10,
            )
            .unwrap();
        let runner = OwnedRunner::new(prepared_runner.activate().await.unwrap());
        let client = RunnerClient::new(
            &self.runtime,
            self.run_id.clone(),
            self.runner_instance_id.clone(),
        );
        let prepared_provider = prepare_with_grace(&client).await;
        let provider_pid = prepared_provider.child_pid();
        let provider_birth = weak_birth(provider_pid).unwrap();
        let (running, _) = store
            .activate_prepared_run(
                &self.run_id,
                PreparedProcessIdentity {
                    runtime_locator: runtime_locator(&self.runtime),
                    runtime_birth_fingerprint: runtime_birth(&self.runtime),
                    runner_locator,
                    runner_birth_fingerprint: runner_birth,
                    provider_locator: serde_json::json!({ "pid": provider_pid }).to_string(),
                    provider_birth_fingerprint: provider_birth.clone(),
                    process_group_locator: serde_json::json!({
                        "pgid": prepared_provider.process_group_id()
                    })
                    .to_string(),
                    process_group_birth_fingerprint: provider_birth,
                },
                11,
            )
            .unwrap();
        assert_eq!(running.phase, RunPhase::Running);
        (runner, prepared_provider, provider_pid)
    }

    fn cache_root(&self) -> PathBuf {
        self.artifacts_root.join("cache")
    }

    fn ran_count(&self) -> usize {
        fs::read_to_string(&self.ran_log)
            .map(|text| text.lines().count())
            .unwrap_or(0)
    }
}

/// Owns one manager, and optionally one local API server, for a daemon
/// lifetime. Dropping is not enough: the tests stop and restart these across
/// an external cut.
struct Daemon {
    handle: execution::Handle,
    manager: JoinHandle<Result<(), execution::Error>>,
    api: Option<(oneshot::Sender<()>, JoinHandle<io::Result<()>>)>,
}

impl Daemon {
    fn start(fixture: &Fixture, state: &DaemonState, serve_api: bool) -> Self {
        let (handle, manager) =
            execution::spawn(fixture.execution_config(), state.clone()).unwrap();
        let api = serve_api.then(|| {
            let listener = UnixListener::bind(&fixture.socket).unwrap();
            let (stop, shutdown) = oneshot::channel();
            let join = tokio::spawn(local_api::serve(
                listener,
                state.clone(),
                handle.clone(),
                fixture.guidance_root.clone(),
                RequestCredential::new(OPERATOR.into()).unwrap(),
                async move {
                    let _ = shutdown.await;
                },
            ));
            (stop, join)
        });
        Self {
            handle,
            manager,
            api,
        }
    }

    async fn stop(self) {
        self.handle.shutdown().await.unwrap();
        self.manager.await.unwrap().unwrap();
        if let Some((stop, join)) = self.api {
            let _ = stop.send(());
            join.await.unwrap().unwrap();
        }
    }
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn rust_completion_verifies_the_exact_snapshot_after_reap_and_terminalizes_last() {
    let fixture = Fixture::new(FixtureTest::Passes);
    let _release = ReleaseOnDrop(fixture.provider_release.clone());
    let (mut store, admitted) = fixture.seed();
    let (mut runner, provider, provider_pid) = fixture.launch(&mut store, &admitted).await;
    drop(store);
    provider.activate().await.unwrap();
    wait_for_path(&fixture.provider_marker, "the provider never started").await;

    let state = DaemonState::new(fixture.reopen());

    // The attempt proposes success. This is the exact durable call
    // `local_api`'s `CompleteAttempt` makes; it is issued with no manager
    // running so the fixture, not the daemon's finalize grace, decides when
    // the provider is reaped. The wire path itself is asserted below and in
    // `crates/factoryd/tests/local_api.rs`.
    request_success(&state, &fixture.run_id).await;
    let (run, check) = snapshot(&state, &fixture.run_id).await;
    assert_eq!(run.phase, RunPhase::Finalizing);
    assert_eq!(run.outcome, Some(RunOutcome::Succeeded));
    assert_eq!(check.unwrap().phase, RustCompletionPhase::Pending);
    assert!(
        !process_absent(provider_pid),
        "finalizing must be durable before anything reaps the provider"
    );

    // Sampling starts before the daemon does, so the ordering claims cover
    // every later transition and depend on no wall clock.
    let ordering = OrderingWatch::start(&state, &fixture, provider_pid);
    let daemon = Daemon::start(&fixture, &state, true);

    // The bearer is revoked at the wire, not merely in a row.
    let replayed = complete_attempt(&fixture.socket).await;
    assert_unauthorized(&replayed);
    let hooked = request(
        &fixture.socket,
        RequestEnvelope::authenticated(
            LocalRequest::ProviderHook {
                event: factory_core::ProviderHookEvent::PreToolUse,
                payload: serde_json::json!({}),
            },
            RequestCredential::new(BEARER.into()).unwrap(),
        ),
    )
    .await;
    assert_unauthorized(&hooked);

    // A successor attempt cannot be admitted before this one is terminal.
    let retried = request(
        &fixture.socket,
        RequestEnvelope::authenticated(
            LocalRequest::RetryTask {
                project_id: fixture.project_id.clone(),
                task_id: fixture.task_id.clone(),
            },
            RequestCredential::new(OPERATOR.into()).unwrap(),
        ),
    )
    .await;
    assert!(
        matches!(
            &retried,
            ServerFrame::Response {
                response: LocalResponse::Error { code, .. },
                ..
            } if *code == ErrorCode::Conflict
        ),
        "a retry before terminal must be refused, got {retried:?}"
    );

    // Production code owns everything from here: the daemon stops the exact
    // runner it registered and reaps the provider. One descendant keeps the
    // attempt's process group alive past its leader, so the reap is provably
    // incomplete while the daemon has every chance to start verifying.
    assert!(runner.wait().await.success());
    await_attempt_group_hold(&state, &fixture).await;
    // Sensitivity window only: on healthy code the group may be held forever
    // and nothing happens, so a slower machine cannot fail here. It exists so
    // a daemon that skipped the reap gate is caught by the sampler above.
    tokio::time::sleep(REAP_GATE_WINDOW).await;
    let (_, check) = snapshot(&state, &fixture.run_id).await;
    assert_eq!(
        check.unwrap().phase,
        RustCompletionPhase::Pending,
        "verification began while the attempt's process group was still live"
    );

    fs::write(&fixture.attempt_group_release, b"release").unwrap();
    let run = await_terminal(&state, &fixture).await;
    ordering.stop().await;

    assert_eq!(run.outcome, Some(RunOutcome::Succeeded));
    let (_, check) = snapshot(&state, &fixture.run_id).await;
    let check = check.unwrap();
    assert_eq!(check.phase, RustCompletionPhase::Passed);
    assert!(check.source_digest.is_some_and(|digest| is_digest(&digest)));
    assert!(check.bundle_digest.is_some_and(|digest| is_digest(&digest)));
    assert_eq!(
        fixture.ran_count(),
        1,
        "the staged test binary must launch exactly once"
    );
    assert_eq!(
        fs::read_dir(fixture.cache_root()).unwrap().count(),
        1,
        "one project/toolchain cache must survive as regenerable storage"
    );
    assert!(
        !fixture
            .artifacts_root
            .join("tmp")
            .join(run_nonce())
            .exists(),
        "the attempt-owned temporary root must be gone before terminal"
    );
    assert_eq!(task_status(&state, &fixture).await, TaskStatus::Succeeded);

    daemon.stop().await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_test_that_mutates_its_snapshot_refines_success_to_failed_unverifiable() {
    let fixture = Fixture::new(FixtureTest::MutatesSnapshot);
    let _release = ReleaseOnDrop(fixture.provider_release.clone());
    let (mut store, admitted) = fixture.seed();
    let (mut runner, provider, provider_pid) = fixture.launch(&mut store, &admitted).await;
    drop(store);
    provider.activate().await.unwrap();
    wait_for_path(&fixture.provider_marker, "the provider never started").await;

    let state = DaemonState::new(fixture.reopen());
    let ordering = OrderingWatch::start(&state, &fixture, provider_pid);
    let daemon = Daemon::start(&fixture, &state, true);
    complete_attempt(&fixture.socket).await;
    assert!(runner.wait().await.success());

    let run = await_terminal(&state, &fixture).await;
    ordering.stop().await;
    assert_eq!(
        run.outcome,
        Some(RunOutcome::Failed {
            reason: RunFailureReason::Unverifiable
        })
    );
    let (_, check) = snapshot(&state, &fixture.run_id).await;
    let check = check.unwrap();
    assert_eq!(check.phase, RustCompletionPhase::Failed);
    assert!(
        check
            .failure
            .as_deref()
            .is_some_and(|failure| failure.contains("source changed")),
        "the recorded failure must name the source change: {:?}",
        check.failure
    );
    assert!(check.source_digest.is_none());
    assert_eq!(fixture.ran_count(), 1);
    assert_eq!(task_status(&state, &fixture).await, TaskStatus::Failed);
    // The live Change is untouched: only the exact private snapshot moved.
    assert_eq!(
        fs::read_to_string(fixture.change.join("revision.txt")).unwrap(),
        "A\n"
    );

    daemon.stop().await;
}

/// External finalization, verifier cut: the verifier leader is killed after it
/// atomically published its result and while one descendant still holds its
/// process group. Recovery must stay nonterminal past the daemon restart, then
/// consume exactly that published result — never a rebuild of the Change
/// contents that arrived after the cut.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn an_externally_killed_verifier_converges_on_its_published_result_after_group_absence() {
    let fixture = Fixture::new(FixtureTest::HoldsGroupOpen);
    let _provider_release = ReleaseOnDrop(fixture.provider_release.clone());
    let _test_release = ReleaseOnDrop(fixture.test_release.clone());
    let _descendant_release = ReleaseOnDrop(fixture.descendant_release.clone());
    let (mut store, admitted) = fixture.seed();
    let (mut runner, provider, _) = fixture.launch(&mut store, &admitted).await;
    drop(store);
    provider.activate().await.unwrap();
    wait_for_path(&fixture.provider_marker, "the provider never started").await;

    let state = DaemonState::new(fixture.reopen());
    let daemon = Daemon::start(&fixture, &state, true);
    complete_attempt(&fixture.socket).await;
    assert!(runner.wait().await.success());

    // The verifier is live and its test is blocked with a descendant holding
    // the group open.
    wait_for_path(
        &fixture.test_started,
        "the verifier never reached the staged test binary",
    )
    .await;
    let effect = effect_resources(&state, &fixture.run_id).await;
    let leader = locator_pid(&effect.0.locator);
    let temporary = fixture.artifacts_root.join("tmp").join(run_nonce());
    let result_path = temporary.join("result.json");
    assert!(!result_path.exists());

    // Cut the daemon first so the verifier publishes with nobody listening;
    // its finish marker is therefore never written by a healthy handshake.
    daemon.stop().await;
    fs::write(&fixture.test_release, b"release").unwrap();
    wait_for_path(&result_path, "the verifier never published its result").await;
    let published: serde_json::Value =
        serde_json::from_slice(&fs::read(&result_path).unwrap()).unwrap();
    let published_digest = published["snapshot_digest"].as_str().unwrap().to_owned();
    assert!(is_digest(&published_digest));

    // The external cut: kill only the verifier leader.
    kill_and_reap(leader);
    assert!(process_absent(leader));
    assert!(
        !process_group_absent(leader),
        "the descendant must still hold the verifier's process group"
    );

    // Later Change contents arrive after the cut. A rebuild would see them.
    fs::write(fixture.change.join("revision.txt"), "B\n").unwrap();

    let state = DaemonState::new(fixture.reopen());
    let daemon = Daemon::start(&fixture, &state, false);

    // Recovery releases the exact absent leader and stops there: leader loss
    // with live descendants stays nonterminal past restart.
    await_effect_process_released(&state, &fixture, leader).await;
    let (run, check) = snapshot(&state, &fixture.run_id).await;
    assert_eq!(run.phase, RunPhase::Finalizing);
    assert_eq!(check.unwrap().phase, RustCompletionPhase::Running);

    // Exact group absence, and only then convergence.
    fs::write(&fixture.descendant_release, b"release").unwrap();
    await_process_group_absence(leader).await;
    let run = await_terminal(&state, &fixture).await;

    assert_eq!(run.outcome, Some(RunOutcome::Succeeded));
    let (_, check) = snapshot(&state, &fixture.run_id).await;
    let check = check.unwrap();
    assert_eq!(check.phase, RustCompletionPhase::Passed);
    assert_eq!(
        check.source_digest.as_deref(),
        Some(published_digest.as_str()),
        "recovery must consume the published result, not a fresh build"
    );
    assert_eq!(
        fixture.ran_count(),
        1,
        "recovery must never rebuild or rerun against later Change contents"
    );
    assert_eq!(
        fs::read_to_string(fixture.change.join("revision.txt")).unwrap(),
        "B\n"
    );
    assert_eq!(task_status(&state, &fixture).await, TaskStatus::Succeeded);

    daemon.stop().await;
}

async fn request_success(state: &DaemonState, run_id: &RunId) {
    let run_id = run_id.clone();
    state
        .commit_and_publish(move |store| {
            let (_, events) = store.request_attempt_outcome(
                &run_id,
                &RunOutcome::Succeeded,
                Some("verified by the fixture workspace"),
                now_ms(),
            )?;
            Ok(((), events))
        })
        .await
        .unwrap();
}

fn now_ms() -> i64 {
    i64::try_from(
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_millis(),
    )
    .unwrap()
}

/// Waits for the exact state where the attempt's leader processes are gone but
/// its process group is provably still live.
async fn await_attempt_group_hold(state: &DaemonState, fixture: &Fixture) {
    let deadline = Instant::now() + PROCESS_BUDGET;
    loop {
        let (_, resources, _) = kernel(state, &fixture.run_id).await;
        let released = |kind| {
            resources.iter().any(|resource| {
                resource.kind == kind && resource.state == KernelResourceState::Released
            })
        };
        let group_held = resources.iter().any(|resource| {
            resource.kind == KernelResourceKind::ProcessGroup
                && resource.state != KernelResourceState::Released
        });
        if released(KernelResourceKind::RunnerProcess)
            && released(KernelResourceKind::ProviderProcess)
            && group_held
        {
            return;
        }
        assert!(
            Instant::now() < deadline,
            "the attempt never reached leader loss with a live process group: {:?}",
            resources
                .iter()
                .map(|resource| (resource.kind, resource.state))
                .collect::<Vec<_>>()
        );
        tokio::time::sleep(POLL).await;
    }
}

/// Samples the durable kernel and the real provider process continuously and
/// asserts the documented completion ordering as implications. Nothing here
/// depends on how fast the machine is: a slower machine only samples a slower
/// run.
struct OrderingWatch {
    stop: Arc<AtomicBool>,
    join: JoinHandle<()>,
}

impl OrderingWatch {
    fn start(state: &DaemonState, fixture: &Fixture, provider_pid: u32) -> Self {
        let stop = Arc::new(AtomicBool::new(false));
        let state = state.clone();
        let run_id = fixture.run_id.clone();
        let ran_log = fixture.ran_log.clone();
        let join = tokio::spawn({
            let stop = Arc::clone(&stop);
            async move {
                while !stop.load(Ordering::Relaxed) {
                    let (run, resources, check) = kernel(&state, &run_id).await;
                    let attempt_released = resources
                        .iter()
                        .filter(|resource| is_attempt_resource(resource.kind))
                        .all(|resource| resource.state == KernelResourceState::Released);
                    assert!(
                        !process_absent(provider_pid) || run.phase != RunPhase::Running,
                        "the provider was reaped before the run durably finalized"
                    );
                    assert!(
                        check
                            .as_ref()
                            .is_none_or(|check| check.phase == RustCompletionPhase::Pending)
                            || attempt_released,
                        "completion verification was claimed before every attempt \
                         resource was released: {resources:?}"
                    );
                    assert!(
                        !ran_log.exists() || attempt_released,
                        "a staged test binary launched before the attempt was reaped"
                    );
                    assert_terminal_is_last(&run, &resources, check.as_ref());
                    tokio::time::sleep(POLL).await;
                }
            }
        });
        Self { stop, join }
    }

    async fn stop(self) {
        self.stop.store(true, Ordering::Relaxed);
        self.join
            .await
            .expect("the ordering watch observed a violation");
    }
}

fn assert_terminal_is_last(
    run: &RunSnapshot,
    resources: &[KernelResource],
    check: Option<&RustCompletionCheck>,
) {
    if run.phase != RunPhase::Terminal {
        return;
    }
    assert!(
        resources
            .iter()
            .all(|resource| resource.state == KernelResourceState::Released),
        "terminal was written while resources were still held: {resources:?}"
    );
    assert!(
        check.is_some_and(|check| check.phase.is_terminal()),
        "terminal was written before the completion check settled"
    );
}

/// Polls until the run is terminal, asserting the documented ordering on every
/// sample rather than at one instant.
async fn await_terminal(state: &DaemonState, fixture: &Fixture) -> RunSnapshot {
    let deadline = Instant::now() + VERIFICATION_BUDGET;
    loop {
        let (run, resources, check) = kernel(state, &fixture.run_id).await;
        let attempt_released = resources
            .iter()
            .filter(|resource| is_attempt_resource(resource.kind))
            .all(|resource| resource.state == KernelResourceState::Released);
        assert!(
            check
                .as_ref()
                .is_none_or(|check| check.phase == RustCompletionPhase::Pending)
                || attempt_released,
            "completion verification was claimed before every attempt resource was released"
        );
        assert!(
            !fixture.ran_log.exists() || attempt_released,
            "a staged test binary launched before the attempt was reaped"
        );
        assert_terminal_is_last(&run, &resources, check.as_ref());
        if run.phase == RunPhase::Terminal {
            return run;
        }
        assert!(
            Instant::now() < deadline,
            "the run never reached terminal; last observation: run phase {:?}, completion {:?}, resources {:?}",
            run.phase,
            check.map(|check| check.phase),
            resources
                .iter()
                .map(|resource| (resource.kind, resource.state))
                .collect::<Vec<_>>()
        );
        tokio::time::sleep(POLL).await;
    }
}

async fn await_effect_process_released(state: &DaemonState, fixture: &Fixture, leader: u32) {
    let deadline = Instant::now() + VERIFICATION_BUDGET;
    loop {
        let (run, resources, _) = kernel(state, &fixture.run_id).await;
        assert_ne!(
            run.phase,
            RunPhase::Terminal,
            "the run terminalized while the verifier's process group was still live"
        );
        let released = resources.iter().any(|resource| {
            resource.kind == KernelResourceKind::EffectProcess
                && resource.state == KernelResourceState::Released
        });
        let group_held = resources.iter().any(|resource| {
            resource.kind == KernelResourceKind::EffectGroup
                && resource.state != KernelResourceState::Released
        });
        assert!(
            group_held || process_group_absent(leader),
            "the verifier's process group was released while it was still live"
        );
        if released && group_held {
            return;
        }
        assert!(
            Instant::now() < deadline,
            "recovery never released the exact absent verifier leader while its group survived: {:?}",
            resources
                .iter()
                .map(|resource| (resource.kind, resource.state))
                .collect::<Vec<_>>()
        );
        tokio::time::sleep(POLL).await;
    }
}

const fn is_attempt_resource(kind: KernelResourceKind) -> bool {
    matches!(
        kind,
        KernelResourceKind::RunnerProcess
            | KernelResourceKind::ProviderProcess
            | KernelResourceKind::ProcessGroup
            | KernelResourceKind::RuntimeRoot
    )
}

async fn kernel(
    state: &DaemonState,
    run_id: &RunId,
) -> (
    RunSnapshot,
    Vec<KernelResource>,
    Option<RustCompletionCheck>,
) {
    let run_id = run_id.clone();
    state
        .with_store(move |store| {
            Ok((
                store.kernel_run(&run_id)?.unwrap(),
                store.kernel_resources(&run_id)?,
                store.rust_completion_check(&run_id)?,
            ))
        })
        .await
        .unwrap()
}

async fn snapshot(
    state: &DaemonState,
    run_id: &RunId,
) -> (RunSnapshot, Option<RustCompletionCheck>) {
    let (run, _, check) = kernel(state, run_id).await;
    (run, check)
}

async fn effect_resources(state: &DaemonState, run_id: &RunId) -> (KernelResource, KernelResource) {
    let (_, resources, _) = kernel(state, run_id).await;
    let find = |kind| {
        resources
            .iter()
            .find(|resource| resource.kind == kind)
            .unwrap_or_else(|| panic!("the verifier registered no {kind:?} resource"))
            .clone()
    };
    (
        find(KernelResourceKind::EffectProcess),
        find(KernelResourceKind::EffectGroup),
    )
}

async fn task_status(state: &DaemonState, fixture: &Fixture) -> TaskStatus {
    let project_id = fixture.project_id.clone();
    let task_id = fixture.task_id.clone();
    state
        .with_store(move |store| Ok(store.get_task(&project_id, &task_id)?.snapshot.status))
        .await
        .unwrap()
}

async fn complete_attempt(socket: &Path) -> ServerFrame {
    request(
        socket,
        RequestEnvelope::authenticated(
            LocalRequest::CompleteAttempt {
                result: "verified by the fixture workspace".into(),
            },
            RequestCredential::new(BEARER.into()).unwrap(),
        ),
    )
    .await
}

fn assert_unauthorized(frame: &ServerFrame) {
    assert!(
        matches!(
            frame,
            ServerFrame::Response {
                response: LocalResponse::Error { code, .. },
                ..
            } if *code == ErrorCode::Unauthorized
        ),
        "a revoked bearer must be refused, got {frame:?}"
    );
}

async fn request(socket: &Path, envelope: RequestEnvelope) -> ServerFrame {
    let mut stream = UnixStream::connect(socket).await.unwrap();
    let mut encoded = serde_json::to_vec(&envelope).unwrap();
    encoded.push(b'\n');
    stream.write_all(&encoded).await.unwrap();
    let mut line = Vec::new();
    BufReader::new(stream)
        .read_until(b'\n', &mut line)
        .await
        .unwrap();
    serde_json::from_slice(&line).unwrap()
}

fn capability_digest(bearer: &str) -> String {
    format!("{:x}", Sha256::digest(bearer.as_bytes()))
}

fn is_digest(value: &str) -> bool {
    value.len() == 64 && value.bytes().all(|byte| byte.is_ascii_hexdigit())
}

fn run_nonce() -> String {
    uuid::Uuid::parse_str(RUN_ID).unwrap().simple().to_string()
}

fn private_directory(path: &Path) {
    fs::create_dir_all(path).unwrap();
    fs::set_permissions(path, fs::Permissions::from_mode(0o700)).unwrap();
}

fn runtime_locator(path: &Path) -> String {
    serde_json::json!({ "path": path }).to_string()
}

fn runtime_birth(path: &Path) -> String {
    let metadata = fs::symlink_metadata(path).unwrap();
    format!("unix-device:{}:inode:{}", metadata.dev(), metadata.ino())
}

fn runner_setup_locator(path: &Path, runner_instance_id: &RunnerInstanceId) -> String {
    serde_json::json!({
        "runner_instance_id": runner_instance_id.as_str(),
        "setup_path": path,
    })
    .to_string()
}

fn runner_locator(pid: u32, runner_instance_id: &RunnerInstanceId) -> String {
    serde_json::json!({
        "pid": pid,
        "runner_instance_id": runner_instance_id.as_str(),
    })
    .to_string()
}

fn locator_pid(locator: &str) -> u32 {
    serde_json::from_str::<serde_json::Value>(locator).unwrap()["pid"]
        .as_u64()
        .unwrap()
        .try_into()
        .unwrap()
}

#[cfg(target_os = "linux")]
fn weak_birth(pid: u32) -> Option<String> {
    let stat = fs::read_to_string(format!("/proc/{pid}/stat")).ok()?;
    let fields = stat.rsplit_once(") ")?.1;
    let ticks = fields.split_whitespace().nth(19)?;
    Some(format!("linux-start-ticks:{ticks}"))
}

#[cfg(not(target_os = "linux"))]
fn weak_birth(pid: u32) -> Option<String> {
    test_kill_process(raw(pid)).ok()?;
    Some("weak-presence-only".into())
}

fn raw(pid: u32) -> Pid {
    Pid::from_raw(i32::try_from(pid).unwrap()).unwrap()
}

fn process_absent(pid: u32) -> bool {
    match test_kill_process(raw(pid)) {
        Err(Errno::SRCH) => true,
        Ok(()) => false,
        Err(error) => panic!("process {pid} presence probe failed: {error}"),
    }
}

fn process_group_absent(pgid: u32) -> bool {
    match rustix::process::test_kill_process_group(raw(pgid)) {
        Err(Errno::SRCH) => true,
        Ok(()) | Err(Errno::PERM) => false,
        Err(error) => panic!("process group {pgid} presence probe failed: {error}"),
    }
}

/// Kills one exact process and reaps it, so a zombie cannot masquerade as a
/// live resource for the daemon's absence probe.
fn kill_and_reap(pid: u32) {
    let target = raw(pid);
    let _ = rustix::process::kill_process(target, rustix::process::Signal::KILL);
    let deadline = Instant::now() + PROCESS_BUDGET;
    loop {
        match waitpid(Some(target), WaitOptions::NOHANG) {
            Ok(Some(_)) | Err(Errno::CHILD) => {}
            Ok(None) | Err(_) => {}
        }
        if process_absent(pid) {
            return;
        }
        assert!(
            Instant::now() < deadline,
            "the killed verifier leader {pid} never became absent"
        );
        thread::sleep(POLL);
    }
}

async fn await_process_group_absence(pgid: u32) {
    let deadline = Instant::now() + PROCESS_BUDGET;
    while !process_group_absent(pgid) {
        assert!(
            Instant::now() < deadline,
            "the released descendant never left process group {pgid}"
        );
        tokio::time::sleep(POLL).await;
    }
}

async fn wait_for_path(path: &Path, failure: &str) {
    let deadline = Instant::now() + VERIFICATION_BUDGET;
    while !path.exists() {
        assert!(
            Instant::now() < deadline,
            "{failure} ({} was never created)",
            path.display()
        );
        tokio::time::sleep(POLL).await;
    }
}

async fn prepare_with_grace(client: &RunnerClient) -> PreparedRunner {
    let deadline = Instant::now() + PROCESS_BUDGET;
    loop {
        match client.prepare().await {
            Ok(prepared) => return prepared,
            Err(error) if Instant::now() < deadline => {
                let _ = error;
                tokio::time::sleep(POLL).await;
            }
            Err(error) => panic!("the runner never prepared a provider: {error}"),
        }
    }
}

/// The daemon binary under test, plus the runner it gates effects through.
/// Cargo only exports `CARGO_BIN_EXE_*` for this package, so the sibling
/// binary is resolved from the same build directory.
fn factoryd_program() -> PathBuf {
    PathBuf::from(env!("CARGO_BIN_EXE_factoryd"))
}

fn runner_program() -> PathBuf {
    let path = factoryd_program()
        .parent()
        .expect("the factoryd test binary must live in a build directory")
        .join("factory-runner");
    assert!(
        path.is_file(),
        "{} is missing; build the workspace first: cargo build --workspace",
        path.display()
    );
    path
}

/// The exact toolchain running this test. `CARGO` resolves through rustup's
/// proxy to a real toolchain directory holding both `cargo` and `rustc`, which
/// is what `rust_verify` requires of a trusted toolchain.
fn trusted_toolchain_cargo() -> PathBuf {
    let cargo = PathBuf::from(
        std::env::var_os("CARGO").expect("CARGO must name the toolchain running this test"),
    );
    let rustc = cargo
        .parent()
        .expect("CARGO must be an absolute toolchain path")
        .join("rustc");
    assert!(
        cargo.is_file() && rustc.is_file(),
        "{} must be a real toolchain directory holding cargo and rustc",
        cargo.display()
    );
    cargo
}

struct ReleaseOnDrop(PathBuf);

impl Drop for ReleaseOnDrop {
    fn drop(&mut self) {
        let _ = fs::write(&self.0, b"release");
    }
}

struct OwnedRunner(Option<tokio::process::Child>);

impl OwnedRunner {
    const fn new(child: tokio::process::Child) -> Self {
        Self(Some(child))
    }

    async fn wait(&mut self) -> std::process::ExitStatus {
        let status = self.0.as_mut().unwrap().wait().await.unwrap();
        self.0.take();
        status
    }
}

impl Drop for OwnedRunner {
    fn drop(&mut self) {
        if let Some(child) = self.0.as_mut() {
            let _ = child.start_kill();
        }
    }
}
