use factory_core::{
    AgentId, AgentRole, ChangeId, ProjectId, Provider, RunId, RunnerInstanceId, TaskId,
};
use factoryd::store::{
    ChangeBaseIdentity, ChangeMaterialization, ChangeReservation, ChangeSourceIdentity, NewAgent,
    NewProject, NewRunAdmission, NewTask, Store,
};
use sha2::{Digest, Sha256};

fn capability_digest(bearer: &str) -> String {
    let mut hasher = Sha256::new();
    hasher.update(bearer.as_bytes());
    format!("{:x}", hasher.finalize())
}

#[test]
fn storage_completion_requires_zero_live_leases() {
    let directory = tempfile::tempdir().unwrap();
    let database = directory.path().join("factory.db");
    let project_id = ProjectId::try_from("factory").unwrap();
    let agent_id = AgentId::try_from("worker").unwrap();
    let run_id = RunId::try_from("11111111-1111-4111-8111-111111111111").unwrap();
    let change_id = ChangeId::try_from("change-1").unwrap();
    let mut store = Store::open(&database).unwrap();

    store
        .create_project(
            NewProject {
                id: project_id.clone(),
                name: "Factory".into(),
                root: "/tmp/factory".into(),
            },
            1,
        )
        .unwrap();
    store
        .create_agent(
            NewAgent {
                id: agent_id.clone(),
                project_id: project_id.clone(),
                parent_agent_id: None,
                role: AgentRole::Worker,
                provider: Provider::Shell,
            },
            2,
        )
        .unwrap();
    store
        .create_assigned_task(
            NewTask {
                id: TaskId::try_from("task-1").unwrap(),
                project_id: project_id.clone(),
                parent_task_id: None,
                title: "Keep the lease live".into(),
                body: String::new(),
                priority: 0,
            },
            agent_id.clone(),
            3,
        )
        .unwrap();
    store
        .admit_next_run(
            NewRunAdmission {
                run_id: run_id.clone(),
                project_id: project_id.clone(),
                agent_id,
                capability_digest: capability_digest("attempt-secret"),
                runtime_claim: "runtime-claim:55555555555545558555555555555555".into(),
                runner_instance_id: RunnerInstanceId::try_from(
                    "22222222-2222-4222-8222-222222222222",
                )
                .unwrap(),
                runner_runtime: "/tmp/factory-runner".into(),
                max_active_runs: 1,
                change_reservation: ChangeReservation {
                    id: change_id.clone(),
                    source_root: "/tmp/factory-change-1".into(),
                    max_factory_changes: 1,
                },
                policy_cwd: "/tmp/factory-runner/policy".into(),
            },
            4,
        )
        .unwrap()
        .expect("queued task should be admitted");

    let base = ChangeBaseIdentity {
        repository_root: "/tmp/factory".into(),
        device: 1,
        inode: 2,
    };
    let oid = "0123456789abcdef0123456789abcdef01234567";
    store
        .record_change_base(&project_id, &change_id, 0, oid, &base, 5)
        .unwrap();
    store
        .mark_change_available(
            &project_id,
            &change_id,
            1,
            &ChangeMaterialization {
                base_oid: oid.into(),
                base,
                source: ChangeSourceIdentity {
                    source_root: "/tmp/factory-change-1".into(),
                    device: 3,
                    inode: 4,
                    size_bytes: 5,
                },
            },
            5,
        )
        .unwrap();

    let leased = store.change_storage_summary(Some(&project_id)).unwrap();
    assert_eq!(leased.retained_count, 1);
    assert_eq!(leased.measured_bytes, 5);
    assert_eq!(leased.active_leases, 1);
    assert!(!leased.complete);

    drop(store);
    rusqlite::Connection::open(&database)
        .unwrap()
        .execute(
            "UPDATE runs
             SET phase = 'terminal', outcome = 'failed', outcome_detail = 'spawn',
                 finalizing_at_ms = 6, phase_since_ms = 6,
                 updated_at_ms = 6, ended_at_ms = 6
             WHERE id = ?1",
            [run_id.as_str()],
        )
        .unwrap();

    let store = Store::open(&database).unwrap();
    let quiescent = store.change_storage_summary(Some(&project_id)).unwrap();
    assert_eq!(quiescent.retained_count, 1);
    assert_eq!(quiescent.measured_bytes, 5);
    assert_eq!(quiescent.active_leases, 0);
    assert!(quiescent.complete);
}
