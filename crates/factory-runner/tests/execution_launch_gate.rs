//! Causal proof of the production register-before-exec launch gate.
//!
//! These tests enter `factoryd::execution`'s real launch and admitted-run
//! recovery paths with a deterministic `shell` provider. They assert the one
//! externally observable consequence of the kernel's launch ordering: the
//! blocked provider `exec` is released only after the run is durably `running`
//! with its exact provider PID, process group, and birth identity recorded.

use std::{
    fs,
    os::unix::fs::PermissionsExt,
    path::{Path, PathBuf},
    thread,
    time::{Duration, Instant},
};

use factory_core::{
    AgentId, AgentRole, ChangeId, ProjectId, Provider, RunId, RunOutcome, RunPhase,
    RunnerInstanceId, TaskId, paths,
};
use factoryd::{
    daemon_state::DaemonState,
    execution, providers,
    runner_process::{self, LaunchSpec, ProviderEnvironment},
    store::{
        AdmittedRun, ChangeReservation, KernelResource, KernelResourceKind, KernelResourceState,
        NewAgent, NewProject, NewRunAdmission, NewTask, Store,
    },
};

#[path = "support/kernel_process.rs"]
mod support;

use support::{
    OwnedRunner, birth_fingerprint, private_directory, process_absent, process_birth,
    runner_locator, runner_setup_locator, runtime_birth, runtime_locator,
};

const PROJECT_ID: &str = "gate-project";
const AGENT_ID: &str = "gate-agent";
const TASK_ID: &str = "gate-task";
const SEEDED_RUN_ID: &str = "44444444-4444-4444-8444-444444444444";
const SEEDED_RUNNER_INSTANCE_ID: &str = "55555555-5555-4555-8555-555555555555";
const SEEDED_RUNTIME_NONCE: &str = "66666666666646668666666666666666";
const STARTUP_INPUT: &[u8] = b"exactly-one-startup-delivery\n";
const CONVERGENCE_TIMEOUT: Duration = Duration::from_secs(30);
/// The runner's control socket, created once the outer gate has exec'd the
/// real runner. Waiting for it removes runner startup — the load-sensitive
/// part of a launch — from every bound below.
const RUNNER_CONTROL_SOCKET: &str = "control.sock";
/// The runner's durable event spool. A `started` record in it is written only
/// after the runner has released the blocked provider `exec`, and it outlives
/// the runner.
const RUNNER_EVENT_SPOOL: &str = "events.ndjson";
const RUNNER_START_LIMIT: Duration = Duration::from_secs(30);
/// Only a prepare handshake, one fork, and a gate release remain once the
/// runner is listening, so this window is orders of magnitude larger than
/// what a launch that released the gate too early needs to show it.
const EXEC_RELEASE_LIMIT: Duration = Duration::from_secs(5);

struct Fixture {
    _root: tempfile::TempDir,
    root: PathBuf,
    database: PathBuf,
    runtime_root: PathBuf,
    guidance_root: PathBuf,
    marker: PathBuf,
    stdin_copy: PathBuf,
    release: PathBuf,
    provider: PathBuf,
    project_id: ProjectId,
    agent_id: AgentId,
}

impl Fixture {
    /// Seeds one project, one orchestrator agent on the deterministic `shell`
    /// provider, and one queued task assigned to it. An orchestrator keeps the
    /// proof on the launch gate: it needs no Change materialization.
    fn new() -> Self {
        let root = tempfile::tempdir_in("/tmp").unwrap();
        fs::set_permissions(root.path(), fs::Permissions::from_mode(0o700)).unwrap();
        let path = root.path().to_owned();
        let project_id = ProjectId::try_from(PROJECT_ID).unwrap();
        let agent_id = AgentId::try_from(AGENT_ID).unwrap();

        let project = path.join("project");
        private_directory(&project);
        let guidance_root = path.join("guidance");
        private_directory(&guidance_root);
        write_guidance(&paths::project_guidance_path(&guidance_root, &project_id));
        write_guidance(&paths::agent_instructions_path(
            &guidance_root,
            &project_id,
            &agent_id,
        ));
        write_guidance(&paths::agent_memory_path(
            &guidance_root,
            &project_id,
            &agent_id,
        ));

        let marker = path.join("provider-executions");
        let stdin_copy = path.join("provider-stdin");
        let release = path.join("provider-release");
        let provider = path.join("provider.sh");
        fs::write(
            &provider,
            format!(
                "#!/bin/sh\n\
                 set -eu\n\
                 printf x >> {marker}\n\
                 cat >> {stdin}\n\
                 deadline=0\n\
                 while [ ! -e {release} ] && [ \"$deadline\" -lt 6000 ]; do\n\
                 \tdeadline=$((deadline + 1))\n\
                 \t/bin/sleep 0.01\n\
                 done\n\
                 exit 19\n",
                marker = shell_quote(&marker),
                stdin = shell_quote(&stdin_copy),
                release = shell_quote(&release),
            ),
        )
        .unwrap();
        fs::set_permissions(&provider, fs::Permissions::from_mode(0o700)).unwrap();

        let database = path.join("state.db");
        let runtime_root = path.join("runs");
        let mut store = Store::open(&database).unwrap();
        store
            .create_project(
                NewProject {
                    id: project_id.clone(),
                    name: "Launch gate".into(),
                    root: project.to_string_lossy().into_owned(),
                },
                1,
            )
            .unwrap();
        store
            .create_agent_with_model(
                NewAgent {
                    id: agent_id.clone(),
                    project_id: project_id.clone(),
                    parent_agent_id: None,
                    role: AgentRole::Orchestrator,
                    provider: Provider::Shell,
                },
                Some(format!("exec {}", provider.display())),
                2,
            )
            .unwrap();
        store
            .create_assigned_task(
                NewTask {
                    id: TaskId::try_from(TASK_ID).unwrap(),
                    project_id: project_id.clone(),
                    parent_task_id: None,
                    title: "prove the launch gate".into(),
                    body: "fixture only".into(),
                    priority: 0,
                },
                agent_id.clone(),
                3,
            )
            .unwrap();

        Self {
            _root: root,
            root: path,
            database,
            runtime_root,
            guidance_root,
            marker,
            stdin_copy,
            release,
            provider,
            project_id,
            agent_id,
        }
    }

    fn open(&self) -> Store {
        Store::open(&self.database).unwrap()
    }

    fn execution_config(&self) -> execution::Config {
        execution::Config {
            factoryd_program: PathBuf::from("/usr/bin/false"),
            runner_program: PathBuf::from(env!("CARGO_BIN_EXE_factory-runner")),
            factoryctl_path: PathBuf::from("/usr/bin/true"),
            git_program: PathBuf::from("/usr/bin/false"),
            claude_installation: None,
            codex_provider: providers::codex::CodexProvider::new(None),
            cargo_program: None,
            runtime_root: self.runtime_root.clone(),
            changes_root: self.root.join("changes"),
            artifacts_root: self.root.join("artifacts"),
            guidance_root: self.guidance_root.clone(),
            socket_path: self.root.join("factory.sock"),
            max_active_runs: 1,
        }
    }

    fn provider_executions(&self) -> usize {
        fs::read_to_string(&self.marker).map_or(0, |body| body.len())
    }

    fn release_provider(&self) {
        let _ = fs::write(&self.release, b"release");
    }
}

impl Drop for Fixture {
    fn drop(&mut self) {
        // Never leave a blocked provider behind, whatever the assertions did.
        self.release_provider();
    }
}

/// Everything a released provider `exec` leaves behind that outlives the
/// runner: the provider's own first write, the runner's exec-gate file, and
/// the runner's durable `started` record. Any one of them proves the gate was
/// opened, so tearing the runner down cannot erase the evidence.
struct ExecWitness {
    marker: PathBuf,
    gate: PathBuf,
    spool: PathBuf,
}

impl ExecWitness {
    fn new(marker: &Path, runtime: &Path, runner_instance_id: &str) -> Self {
        Self {
            marker: marker.to_path_buf(),
            gate: runtime.join(format!("exec-gate-{runner_instance_id}.activate")),
            spool: runtime.join(RUNNER_EVENT_SPOOL),
        }
    }

    fn released(&self) -> bool {
        self.marker.exists()
            || self.gate.exists()
            || fs::read_to_string(&self.spool).is_ok_and(|body| body.contains("\"started\""))
    }
}

#[derive(Clone, Copy)]
enum RuntimeIdentity {
    /// The runtime birth the ledger holds is the live directory's.
    Exact,
    /// The ledger holds the birth of a runtime that no longer exists at that
    /// path, so no recovered identity can confirm it.
    Superseded,
}

struct LaunchPin {
    run_id: RunId,
    runner_pid: u32,
    control_socket: PathBuf,
    runner_listening: bool,
    exec_released: bool,
    witness: ExecWitness,
}

/// Exactly the durable state and live process a daemon crash leaves behind
/// when it dies after registering the runner PID and before the blocked
/// provider `exec` is released: an `admitted` run, an active runner-process
/// resource, and a live runner that has not been asked to prepare anything.
struct CrashedLaunch {
    run_id: RunId,
    witness: ExecWitness,
    runner: OwnedRunner,
    runner_pid: u32,
    runtime: PathBuf,
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn production_launch_never_releases_the_blocked_exec_without_a_durable_running_run() {
    let fixture = Fixture::new();
    let state = DaemonState::new(fixture.open());
    let (handle, manager) = execution::spawn(fixture.execution_config(), state.clone()).unwrap();
    handle.wake(fixture.project_id.clone(), fixture.agent_id.clone());

    // Revoke the attempt in the exact durable instant its runner PID becomes
    // registered: after the gate PID is bound, before the run can become
    // `running`.
    let pin = revoke_when_runner_is_registered(&state, &fixture).await;
    assert!(
        pin.runner_listening,
        "the launched runner never began listening on {}, so the launch never \
         reached the gate this test pins",
        pin.control_socket.display()
    );

    // `activate_prepared_run` must now refuse this run, so the
    // register-before-exec gate can never open. Anything that opened it
    // anyway had the whole observation window, with the durable commit it is
    // waiting for still withheld, to show itself.
    assert!(
        !pin.exec_released,
        "the blocked provider exec was released before the run was durably running"
    );

    wait_for_terminal(&state, &pin.run_id).await;
    support::await_process_absence(pin.runner_pid).await;
    assert!(
        !pin.witness.released(),
        "the blocked provider exec was released for a run that never became running"
    );

    let store = fixture.open();
    let run = store.kernel_run(&pin.run_id).unwrap().unwrap();
    assert_eq!(run.phase, RunPhase::Terminal);
    assert!(matches!(run.outcome, Some(RunOutcome::Cancelled { .. })));
    let resources = store.kernel_resources(&pin.run_id).unwrap();
    // The earlier gates did run: runtime and runner were durably bound before
    // any process could exist. The provider gates never were.
    assert!(
        resource(&resources, KernelResourceKind::RuntimeRoot).is_some(),
        "the runtime was never durably registered, so this proves no ordering"
    );
    assert!(
        resource(&resources, KernelResourceKind::RunnerProcess).is_some(),
        "the runner PID was never durably registered, so this proves no ordering"
    );
    assert!(
        resource(&resources, KernelResourceKind::ProviderProcess).is_none(),
        "a provider resource exists for a run that never became running"
    );
    assert!(
        resource(&resources, KernelResourceKind::ProcessGroup).is_none(),
        "a provider process group exists for a run that never became running"
    );
    assert!(
        resources
            .iter()
            .all(|resource| resource.state == KernelResourceState::Released),
        "a refused launch left resources unreleased"
    );
    drop(store);

    handle.shutdown().await.unwrap();
    manager.await.unwrap().unwrap();
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn recovery_after_a_crash_at_the_blocked_exec_release_runs_one_provider() {
    let fixture = Fixture::new();
    let mut crashed = crashed_launch(&fixture).await;

    // The cut itself: the daemon is gone, the runner survived, and no provider
    // has executed because the run never became durably running.
    assert_eq!(
        fixture.provider_executions(),
        0,
        "a provider executed before the run was durably running"
    );
    assert_eq!(
        run_phase(&fixture.open(), &crashed.run_id),
        RunPhase::Admitted
    );

    // Restart. Production recovery owns the rest of the launch gate.
    let state = DaemonState::new(fixture.open());
    let (handle, manager) = execution::spawn(fixture.execution_config(), state.clone()).unwrap();

    let running = wait_for_running(&state, &crashed.run_id).await;
    assert_eq!(running.phase, RunPhase::Running);
    let resources = state
        .with_store({
            let run_id = crashed.run_id.clone();
            move |store| store.kernel_resources(&run_id)
        })
        .await
        .unwrap();
    let provider = resource(&resources, KernelResourceKind::ProviderProcess)
        .expect("recovery did not record a provider process");
    let group = resource(&resources, KernelResourceKind::ProcessGroup)
        .expect("recovery did not record a provider process group");
    assert_eq!(provider.state, KernelResourceState::Active);
    assert_eq!(group.state, KernelResourceState::Active);
    let provider_pid = locator_number(&provider.locator, "pid");
    let group_pid = locator_number(&group.locator, "pgid");
    assert!(
        !process_absent(provider_pid),
        "the recorded provider PID is not the live provider"
    );
    // The provider is its own group leader, so exact identity means both
    // recorded numbers resolve to the same live process.
    assert_eq!(provider_pid, group_pid);
    assert!(provider.birth_fingerprint.is_some());

    // One provider execution, receiving the one startup input.
    wait_for_provider_start(&fixture).await;
    assert_eq!(fixture.provider_executions(), 1);
    wait_for_startup_delivery(&fixture).await;

    // Converge: the recovered observer owns the exit even though it never
    // spawned the runner.
    fixture.release_provider();
    assert!(
        tokio::time::timeout(CONVERGENCE_TIMEOUT, crashed.runner.wait())
            .await
            .expect("the recovered observer never acknowledged the runner exit")
            .success()
    );
    wait_for_terminal(&state, &crashed.run_id).await;

    let store = fixture.open();
    let run = store.kernel_run(&crashed.run_id).unwrap().unwrap();
    assert_eq!(run.exit_code, Some(19));
    assert!(
        store
            .kernel_resources(&crashed.run_id)
            .unwrap()
            .iter()
            .all(|resource| resource.state == KernelResourceState::Released)
    );
    drop(store);
    assert!(
        !crashed.runtime.exists(),
        "the exact runtime was not removed"
    );
    // Nothing after the provider exit re-launched it or re-fed it: exactly one
    // execution and exactly one startup delivery survive convergence.
    assert_eq!(
        fixture.provider_executions(),
        1,
        "convergence launched a second provider"
    );
    assert_eq!(
        fs::read(&fixture.stdin_copy).unwrap(),
        STARTUP_INPUT,
        "convergence replayed the one startup input"
    );
    assert!(process_absent(crashed.runner_pid));
    assert!(process_absent(provider_pid));

    handle.shutdown().await.unwrap();
    manager.await.unwrap().unwrap();
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn recovery_of_an_admitted_run_without_a_runner_converges_without_a_provider() {
    let fixture = Fixture::new();
    let mut crashed = crashed_launch(&fixture).await;
    // The same cut, except the gate died with the daemon: an admitted run with
    // declared resources and no recoverable runner.
    crashed.runner.kill_and_reap().unwrap();
    support::await_process_absence(crashed.runner_pid).await;

    let state = DaemonState::new(fixture.open());
    let (handle, manager) = execution::spawn(fixture.execution_config(), state.clone()).unwrap();
    wait_for_terminal(&state, &crashed.run_id).await;

    let store = fixture.open();
    let run = store.kernel_run(&crashed.run_id).unwrap().unwrap();
    assert!(matches!(run.outcome, Some(RunOutcome::Failed { .. })));
    let resources = store.kernel_resources(&crashed.run_id).unwrap();
    assert!(
        resource(&resources, KernelResourceKind::ProviderProcess).is_none(),
        "an unrecoverable admission invented a provider"
    );
    assert!(
        resources
            .iter()
            .all(|resource| resource.state == KernelResourceState::Released)
    );
    drop(store);
    assert_eq!(fixture.provider_executions(), 0);

    handle.shutdown().await.unwrap();
    manager.await.unwrap().unwrap();
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn recovery_refuses_the_blocked_exec_when_the_runtime_identity_is_superseded() {
    let fixture = Fixture::new();
    let crashed = crashed_launch_with(&fixture, RuntimeIdentity::Superseded).await;

    let state = DaemonState::new(fixture.open());
    let (handle, manager) = execution::spawn(fixture.execution_config(), state.clone()).unwrap();

    // Recovery finds the live runner but cannot confirm the runtime identity
    // the ledger bound, so its `running` commit is refused. That refusal must
    // reach the blocked provider: no exec, then convergence without one.
    wait_until_refused_or_released(&state, &crashed).await;
    assert!(
        !crashed.witness.released(),
        "recovery released the blocked exec for a run it could not make running"
    );

    let store = fixture.open();
    let run = store.kernel_run(&crashed.run_id).unwrap().unwrap();
    assert!(matches!(run.outcome, Some(RunOutcome::Failed { .. })));
    // Cleanup uncertainty about a runtime whose identity cannot be confirmed
    // stays visible; it is never an invented absence.
    assert_eq!(run.phase, RunPhase::Finalizing);
    let resources = store.kernel_resources(&crashed.run_id).unwrap();
    assert!(
        resource(&resources, KernelResourceKind::ProviderProcess).is_none(),
        "a refused recovery invented a provider resource"
    );
    assert_ne!(
        resource(&resources, KernelResourceKind::RuntimeRoot)
            .unwrap()
            .state,
        KernelResourceState::Released,
        "a runtime whose bound identity cannot be confirmed was released anyway"
    );
    drop(store);

    handle.shutdown().await.unwrap();
    manager.await.unwrap().unwrap();
}

/// Drives the durable launch gates by hand exactly as far as production does
/// before its `running` commit, then stops. Nothing here is the code under
/// test: it only reconstructs the state a crash at the blocked-exec release
/// leaves behind for `recover_admitted_run` to find.
async fn crashed_launch(fixture: &Fixture) -> CrashedLaunch {
    crashed_launch_with(fixture, RuntimeIdentity::Exact).await
}

async fn crashed_launch_with(fixture: &Fixture, identity: RuntimeIdentity) -> CrashedLaunch {
    let run_id = RunId::try_from(SEEDED_RUN_ID).unwrap();
    let runner_instance_id = RunnerInstanceId::try_from(SEEDED_RUNNER_INSTANCE_ID).unwrap();
    private_directory(&fixture.runtime_root);
    let runtime = fixture.runtime_root.join(SEEDED_RUNTIME_NONCE);
    private_directory(&runtime);
    let policy = runtime.join("policy");
    private_directory(&policy);

    let mut store = fixture.open();
    let admitted = admit(&mut store, fixture, &run_id, &runner_instance_id, &runtime);
    let bound_runtime_birth = match identity {
        RuntimeIdentity::Exact => runtime_birth(&runtime),
        RuntimeIdentity::Superseded => {
            // The birth of a directory that is not the one now at this path:
            // what a replaced or recreated runtime root leaves in the ledger.
            let superseded = fixture.root.join("superseded-runtime");
            private_directory(&superseded);
            runtime_birth(&superseded)
        }
    };
    store
        .register_admitted_runtime(
            &run_id,
            &runtime_locator(&runtime),
            &admitted.target.runtime_claim,
            &bound_runtime_birth,
            5,
        )
        .unwrap();
    let setup = runner_process::prepare_runner(LaunchSpec {
        runner_program: PathBuf::from(env!("CARGO_BIN_EXE_factory-runner")),
        factoryctl_path: PathBuf::from("/usr/bin/true"),
        provider_program: fixture.provider.clone(),
        provider_arguments: Vec::new(),
        provider_environment: ProviderEnvironment::Inherited,
        attempt_environment: Vec::new(),
        run_id: run_id.clone(),
        runner_instance_id: runner_instance_id.clone(),
        runtime_dir: runtime.clone(),
        cwd: policy,
        source_root: fixture.root.clone(),
        startup_input: STARTUP_INPUT.to_vec(),
    })
    .await
    .unwrap();
    let setup_locator = runner_setup_locator(setup.setup_path(), &runner_instance_id);
    let setup_birth = birth_fingerprint(setup.setup_device(), setup.setup_inode());
    store
        .register_admitted_runner_setup(&run_id, &setup_locator, &setup_birth, 6)
        .unwrap();
    let prepared_runner = setup.spawn().unwrap();
    let runner_pid = prepared_runner.child_pid();
    store
        .register_admitted_runner(
            &run_id,
            &setup_locator,
            &setup_birth,
            &runner_locator(runner_pid, &runner_instance_id),
            &process_birth(runner_pid).expect("the spawned gate is live"),
            7,
        )
        .unwrap();
    drop(store);
    let runner = OwnedRunner::new(prepared_runner.activate().await.unwrap());

    CrashedLaunch {
        witness: ExecWitness::new(&fixture.marker, &runtime, runner_instance_id.as_str()),
        run_id,
        runner,
        runner_pid,
        runtime,
    }
}

fn admit(
    store: &mut Store,
    fixture: &Fixture,
    run_id: &RunId,
    runner_instance_id: &RunnerInstanceId,
    runtime: &Path,
) -> AdmittedRun {
    store
        .admit_next_run(
            NewRunAdmission {
                run_id: run_id.clone(),
                project_id: fixture.project_id.clone(),
                agent_id: fixture.agent_id.clone(),
                capability_digest: "b".repeat(64),
                runtime_claim: format!("runtime-claim:{SEEDED_RUNTIME_NONCE}"),
                runner_instance_id: runner_instance_id.clone(),
                runner_runtime: runtime.to_string_lossy().into_owned(),
                max_active_runs: 1,
                change_reservation: ChangeReservation {
                    id: ChangeId::try_from("unused-change").unwrap(),
                    source_root: fixture
                        .root
                        .join("unused-change")
                        .to_string_lossy()
                        .into_owned(),
                    max_factory_changes: 1,
                },
                policy_cwd: runtime.join("policy").to_string_lossy().into_owned(),
            },
            4,
        )
        .unwrap()
        .unwrap()
}

/// Revokes the launching run in the same Store transaction that first observes
/// its registered runner PID, and keeps SQLite — the only writer authority —
/// held while the rest of the launch plays out.
///
/// Two things follow. The revocation is atomic with the observation, so the
/// launch cannot slip past the cut. And withholding the commit turns the
/// launch's remaining ordering into an observable window instead of a
/// microsecond race: a launch that opens its provider gate before that commit
/// leaves the gate open, because nothing can tear the runner down until the
/// commit it is waiting for is refused.
async fn revoke_when_runner_is_registered(state: &DaemonState, fixture: &Fixture) -> LaunchPin {
    let deadline = Instant::now() + CONVERGENCE_TIMEOUT;
    loop {
        let marker = fixture.marker.clone();
        let pinned = state
            .commit_and_publish(move |store| {
                let Some(run) = store
                    .recoverable_kernel_runs()?
                    .into_iter()
                    .find(|run| run.run.phase == RunPhase::Admitted)
                else {
                    return Ok((None, Vec::new()));
                };
                let registered = run
                    .resources
                    .iter()
                    .find(|resource| {
                        resource.kind == KernelResourceKind::RunnerProcess
                            && resource.state == KernelResourceState::Active
                            && resource.locator.contains("\"pid\"")
                    })
                    .map(|resource| locator_number(&resource.locator, "pid"));
                let Some(runner_pid) = registered else {
                    return Ok((None, Vec::new()));
                };
                let (_, events) = store.cancel_admitted_or_running_run(
                    &run.run.id,
                    "launch gate proof".into(),
                    now_ms(),
                )?;

                // Still holding SQLite. First wait on an event rather than a
                // clock: the gate exec'ing the real runner, which is the slow
                // and load-sensitive half of what remains.
                let control_socket = Path::new(&run.runner_runtime).join(RUNNER_CONTROL_SOCKET);
                let runner_listening = wait_for_path(&control_socket, RUNNER_START_LIMIT);
                // Only the prepare handshake, the fork of the blocked child,
                // and a gate release are left; they cannot outlast this.
                let witness = ExecWitness::new(
                    &marker,
                    Path::new(&run.runner_runtime),
                    run.runner_instance_id.as_str(),
                );
                let exec_released =
                    runner_listening && wait_for(EXEC_RELEASE_LIMIT, || witness.released());
                Ok((
                    Some(LaunchPin {
                        run_id: run.run.id.clone(),
                        runner_pid,
                        control_socket,
                        runner_listening,
                        exec_released,
                        witness,
                    }),
                    events,
                ))
            })
            .await
            .unwrap();
        if let Some(pinned) = pinned {
            return pinned;
        }
        assert!(
            Instant::now() < deadline,
            "production launch never registered a runner PID"
        );
        tokio::time::sleep(Duration::from_millis(1)).await;
    }
}

/// Blocking poll used inside the held Store transaction. Returns whether the
/// condition became true before `limit`.
fn wait_for(limit: Duration, mut ready: impl FnMut() -> bool) -> bool {
    let deadline = Instant::now() + limit;
    loop {
        if ready() {
            return true;
        }
        if Instant::now() >= deadline {
            return false;
        }
        thread::sleep(Duration::from_millis(5));
    }
}

fn wait_for_path(path: &Path, limit: Duration) -> bool {
    wait_for(limit, || path.exists())
}

fn resource(resources: &[KernelResource], kind: KernelResourceKind) -> Option<&KernelResource> {
    resources.iter().find(|resource| resource.kind == kind)
}

fn locator_number(locator: &str, field: &str) -> u32 {
    serde_json::from_str::<serde_json::Value>(locator)
        .ok()
        .and_then(|value| value.get(field).and_then(serde_json::Value::as_u64))
        .and_then(|value| u32::try_from(value).ok())
        .unwrap_or_else(|| panic!("locator {locator} has no numeric {field}"))
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

fn run_phase(store: &Store, run_id: &RunId) -> RunPhase {
    store.kernel_run(run_id).unwrap().unwrap().phase
}

async fn wait_for_running(state: &DaemonState, run_id: &RunId) -> factory_core::RunSnapshot {
    let deadline = Instant::now() + CONVERGENCE_TIMEOUT;
    loop {
        let run = state
            .with_store({
                let run_id = run_id.clone();
                move |store| Ok(store.kernel_run(&run_id)?.unwrap())
            })
            .await
            .unwrap();
        if run.phase == RunPhase::Running {
            return run;
        }
        assert!(
            Instant::now() < deadline,
            "recovery never moved the admitted run to running (phase {:?})",
            run.phase
        );
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
}

async fn wait_for_terminal(state: &DaemonState, run_id: &RunId) {
    let deadline = Instant::now() + CONVERGENCE_TIMEOUT;
    loop {
        let phase = state
            .with_store({
                let run_id = run_id.clone();
                move |store| Ok(store.kernel_run(&run_id)?.unwrap().phase)
            })
            .await
            .unwrap();
        if phase == RunPhase::Terminal {
            return;
        }
        assert!(
            Instant::now() < deadline,
            "run did not terminalize (phase {phase:?})"
        );
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
}

async fn wait_for_provider_start(fixture: &Fixture) {
    let deadline = Instant::now() + CONVERGENCE_TIMEOUT;
    while fixture.provider_executions() == 0 {
        assert!(
            Instant::now() < deadline,
            "the recovered run reported running but no provider ever executed"
        );
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
}

/// Waits for the refused recovery to record a durable outcome, then keeps
/// watching the gate for [`EXEC_RELEASE_LIMIT`]. Either loop ends the moment
/// the gate *is* opened, so a released exec is reported instead of timed out.
async fn wait_until_refused_or_released(state: &DaemonState, crashed: &CrashedLaunch) {
    let deadline = Instant::now() + CONVERGENCE_TIMEOUT;
    loop {
        if crashed.witness.released() {
            return;
        }
        let run = state
            .with_store({
                let run_id = crashed.run_id.clone();
                move |store| Ok(store.kernel_run(&run_id)?.unwrap())
            })
            .await
            .unwrap();
        if run.outcome.is_some() {
            break;
        }
        assert!(
            Instant::now() < deadline,
            "the refused recovery neither converged nor released a provider (phase {:?})",
            run.phase
        );
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    // A refusal that reached the gate late is still a refusal that failed.
    let settle = Instant::now() + EXEC_RELEASE_LIMIT;
    while Instant::now() < settle && !crashed.witness.released() {
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
}

async fn wait_for_startup_delivery(fixture: &Fixture) {
    let deadline = Instant::now() + CONVERGENCE_TIMEOUT;
    loop {
        let delivered = fs::read(&fixture.stdin_copy).unwrap_or_default();
        if delivered == STARTUP_INPUT {
            return;
        }
        assert!(
            Instant::now() < deadline,
            "the recovered provider never received its one startup input \
             (saw {} of {} bytes)",
            delivered.len(),
            STARTUP_INPUT.len()
        );
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
}

fn write_guidance(path: &Path) {
    private_directory(path.parent().unwrap());
    fs::write(path, b"fixture guidance\n").unwrap();
    fs::set_permissions(path, fs::Permissions::from_mode(0o600)).unwrap();
}

fn shell_quote(path: &Path) -> String {
    format!("'{}'", path.to_string_lossy().replace('\'', "'\\''"))
}
