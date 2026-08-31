import type { AgentItem, StateView, TaskItem } from "@dark-factory/client";

/** The task stages the daemon actually serves today. */
export type TaskStage = "queued" | "building" | "done" | "failed";

export const STAGE_SEQUENCE: readonly TaskStage[] = ["queued", "building"];

export type AgentActivity = "busy" | "waiting" | "needs-you" | "idle";

/** The durable task status projected into the console stage vocabulary. */
export function stageOfTask(task: TaskItem): TaskStage {
  switch (task.status) {
    case "queued":
      return "queued";
    case "running":
    case "blocked":
      return "building";
    case "succeeded":
      return "done";
    case "failed":
    case "cancelled":
      return "failed";
  }
}

/** Segments filled by the durable stage; done fills the complete meter. */
export function stageMeterFill(stage: TaskStage): number {
  if (stage === "done") return STAGE_SEQUENCE.length;
  if (stage === "failed") return 0;
  return STAGE_SEQUENCE.indexOf(stage) + 1;
}

/** Tasks an agent is on right now (durable assignment, live statuses). */
export function agentCurrentTask(agent: AgentItem, state: StateView): TaskItem | undefined {
  for (const task of state.tasks.values()) {
    if (
      task.assigned_agent_id === agent.id &&
      (task.status === "running" || task.status === "blocked")
    ) {
      return task;
    }
  }
  return undefined;
}

/** Precedence: an open question outranks activity; pause outranks waiting. */
export function agentActivity(agent: AgentItem, state: StateView): AgentActivity {
  for (const request of state.humanRequests.values()) {
    if (request.agent_id === agent.id) return "needs-you";
  }
  if (agentCurrentTask(agent, state) !== undefined) return "busy";
  return agent.paused ? "idle" : "waiting";
}

/**
 * The glyph derives from durable facts only: the orchestrator role and the
 * served provider identity. C is Claude, X is Codex, s is the shell provider.
 */
export function agentGlyph(agent: AgentItem): string {
  if (agent.role === "orchestrator") return "◆";
  switch (agent.provider) {
    case "claude_code":
      return "C";
    case "codex":
      return "X";
    case "shell":
      return "s";
  }
}

export type FactoryCounters = Readonly<{
  queued: number | undefined;
  needsYou: number | undefined;
}>;

export function factoryCounters(state: StateView | undefined): FactoryCounters {
  if (state === undefined) return { queued: undefined, needsYou: undefined };
  let queued = 0;
  for (const task of state.tasks.values()) {
    if (stageOfTask(task) === "queued") queued += 1;
  }
  return { queued, needsYou: state.humanRequests.size };
}

/** Active work first, then queued, then finished; priority breaks ties. */
export function orderTasksForHome(state: StateView): readonly TaskItem[] {
  const rank: Record<TaskStage, number> = { building: 0, queued: 1, done: 2, failed: 3 };
  return [...state.tasks.values()].sort((left, right) => {
    const byStage = rank[stageOfTask(left)] - rank[stageOfTask(right)];
    return byStage !== 0 ? byStage : right.priority - left.priority;
  });
}

export type ConsoleScreen = { kind: "home" } | { kind: "queue" } | { kind: "needs-you" };
