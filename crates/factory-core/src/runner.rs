//! Stable, provider-blind control and event wire for one runner process.

use std::{ffi::OsString, path::Path};

use serde::{Deserialize, Serialize};

use crate::{RunId, RunnerInstanceId};

/// Runner wire version, intentionally independent from the local factory API.
pub const RUNNER_PROTOCOL_VERSION: u16 = 2;
/// Maximum JSON payload size. The newline delimiter is not part of this limit.
pub const MAX_RUNNER_FRAME_BYTES: usize = 1024 * 1024;
/// Maximum UTF-8 bytes in an error or spawn-failure message.
pub const MAX_RUNNER_ERROR_BYTES: usize = 16 * 1024;
/// Maximum task bytes transferred from the daemon to a new runner over stdin.
pub const MAX_STARTUP_STDIN_BYTES: usize = 1024 * 1024;
/// Private startup-input file whose advisory lock proves whether an outer
/// runner gate created before durable PID registration can still exist.
pub const RUNNER_STARTUP_LEASE_FILE: &str = "runner-startup.lease";
/// The gate's mode selector. Only the gate's own parser and the fixtures that
/// stand in for it spell this; construction goes through [`exec_gate_argv`].
pub const EXEC_GATE_FLAG: &str = "--exec-gate";
/// The spawning parent's PID, taken before the spawn so the gate can refuse to
/// exec for anything but the parent that prepared it.
pub const EXEC_GATE_EXPECTED_PARENT_PID_FLAG: &str = "--expected-parent-pid";
/// The complete exec-gate prefix, in the one order the gate accepts.
///
/// `exec_gate` in `factory-runner` reads these positionally, so the order is
/// as much of the contract as the spellings are. Building the whole prefix
/// here keeps the daemon, the runner's nested provider gate, and the gate's
/// own parser from drifting apart in either respect. The trailing `--` ends
/// the gate's own options; the caller appends the program and its arguments.
/// `expected_parent` is the caller's own `getpid`, which is always positive.
#[must_use]
pub fn exec_gate_argv(activation: &Path, expected_parent: u32) -> [OsString; 5] {
    [
        OsString::from(EXEC_GATE_FLAG),
        activation.as_os_str().to_owned(),
        OsString::from(EXEC_GATE_EXPECTED_PARENT_PID_FLAG),
        OsString::from(expected_parent.to_string()),
        OsString::from("--"),
    ]
}

/// Private per-runner activation file inside the run's runtime directory.
/// Creating it releases the provider exec gate that runner prepared, so the
/// runner and anything observing the launch must derive the same name.
#[must_use]
pub fn exec_gate_file_name(runner_instance_id: &RunnerInstanceId) -> String {
    format!("exec-gate-{}.activate", runner_instance_id.as_str())
}

/// One authenticated, newline-delimited request sent to a runner.
#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct RequestEnvelope {
    pub protocol_version: u16,
    pub run_id: RunId,
    pub runner_instance_id: RunnerInstanceId,
    pub request: RunnerRequest,
}

impl RequestEnvelope {
    #[must_use]
    pub const fn new(
        run_id: RunId,
        runner_instance_id: RunnerInstanceId,
        request: RunnerRequest,
    ) -> Self {
        Self {
            protocol_version: RUNNER_PROTOCOL_VERSION,
            run_id,
            runner_instance_id,
            request,
        }
    }
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(tag = "type", content = "data", rename_all = "snake_case")]
pub enum RunnerRequest {
    /// Starts the provider exec gate but does not authorize provider code to
    /// run. The runner replies with [`RunnerFrame::Prepared`] and keeps this
    /// connection as the activation leash.
    Prepare {
        command_id: String,
    },
    /// Authorizes the exact prepared process on the same control connection.
    Activate {
        command_id: String,
    },
    Subscribe {
        after_sequence: i64,
    },
    Stop {
        command_id: String,
        grace_ms: u64,
    },
    AcknowledgeExit {
        command_id: String,
        terminal_sequence: i64,
    },
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum RunnerErrorCode {
    InvalidRequest,
    UnsupportedProtocol,
    Unauthorized,
    Conflict,
    Internal,
}

/// One newline-delimited frame sent by a runner.
#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(tag = "type", content = "data", rename_all = "snake_case")]
pub enum RunnerFrame {
    /// The stable exec-gate process exists, but provider code has not run.
    /// `child_pid` remains unchanged when the gate execs the provider.
    Prepared {
        protocol_version: u16,
        command_id: String,
        runner_pid: u32,
        child_pid: u32,
        process_group_id: u32,
    },
    Hello {
        protocol_version: u16,
        run_id: RunId,
        runner_instance_id: RunnerInstanceId,
        runner_pid: u32,
        replay_through: i64,
        terminal_sequence: Option<i64>,
    },
    Event {
        protocol_version: u16,
        event: RunnerEventEnvelope,
    },
    CaughtUp {
        protocol_version: u16,
        sequence: i64,
    },
    CommandAck {
        protocol_version: u16,
        command_id: String,
    },
    Error {
        protocol_version: u16,
        code: RunnerErrorCode,
        message: String,
    },
}

impl RunnerFrame {
    #[must_use]
    pub const fn protocol_version(&self) -> u16 {
        match self {
            Self::Prepared {
                protocol_version, ..
            }
            | Self::Hello {
                protocol_version, ..
            }
            | Self::Event {
                protocol_version, ..
            }
            | Self::CaughtUp {
                protocol_version, ..
            }
            | Self::CommandAck {
                protocol_version, ..
            }
            | Self::Error {
                protocol_version, ..
            } => *protocol_version,
        }
    }
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct RunnerEventEnvelope {
    pub protocol_version: u16,
    pub sequence: i64,
    pub occurred_at_ms: i64,
    pub event: RunnerEvent,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(tag = "type", content = "data", rename_all = "snake_case")]
pub enum RunnerEvent {
    Started {
        child_pid: u32,
    },
    SpawnFailed {
        message: String,
    },
    Exited {
        exit_code: Option<i32>,
        signal: Option<i32>,
    },
}
