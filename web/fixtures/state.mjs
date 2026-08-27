const projectID = "11".repeat(16);
const secondProjectID = "12".repeat(16);
const agentID = "21".repeat(16);
const secondAgentID = "22".repeat(16);
const taskID = "31".repeat(16);
const requestID = "41".repeat(16);

export const fixtureState = {
  head: 42n,
  sequence: 42n,
  factory: [{ dispatch_enabled: true, capacity: 8, active_runs: 2, revision: 42n }],
  projects: new Map([
    [projectID, { id: projectID, name: "North Workshop", revision: 4n }],
    [secondProjectID, { id: secondProjectID, name: "South Workshop", revision: 5n }],
  ]),
  agents: new Map([
    [agentID, { id: agentID, project_id: projectID, name: "Builder One", role: "worker", paused: false, revision: 10n }],
    [secondAgentID, { id: secondAgentID, project_id: secondProjectID, name: "Dispatch Lead", role: "orchestrator", paused: true, revision: 11n }],
  ]),
  tasks: new Map([
    [taskID, { id: taskID, project_id: projectID, assigned_agent_id: agentID, title: "Review the state projection", status: "running", priority: 10, revision: 12n }],
  ]),
  humanRequests: new Map([
    [requestID, { id: requestID, project_id: secondProjectID, agent_id: secondAgentID, task_id: taskID, run_id: "51".repeat(16), created_at: 40n, updated_at: 42n, revision: 13n, kind: "question", status: "open", reply_max_bytes: 8192, can_reply: true, can_open_terminal: false }],
  ]),
};
