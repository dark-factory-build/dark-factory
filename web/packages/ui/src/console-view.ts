import type { AgentItem, StateView, TaskItem } from "@dark-factory/client";

/**
 * Slice-1 console vocabulary (docs/internal/web-console-design.md, Language):
 * task stages are queued → building → reviewing → merging → done / failed.
 * Stage information beyond the durable task status is not served by the
 * daemon yet; `ConsoleExtras` is the typed stand-in surface (fixtures today,
 * a live feed after the daemon gap closes) so wiring is a swap, not a
 * rewrite. Nothing here invents coordinates or authority.
 */
export type TaskStage = "queued" | "building" | "reviewing" | "merging" | "done" | "failed";

export const STAGE_SEQUENCE: readonly TaskStage[] = ["queued", "building", "reviewing", "merging"];

export type AgentActivity = "busy" | "waiting" | "needs-you" | "idle";

export type TickerEntry = Readonly<{ at: string; text: string }>;

export type SuggestionOrigin = "github" | "linear" | "slack" | "telemetry";

export type SuggestionItem = Readonly<{
  id: string;
  title: string;
  origin: SuggestionOrigin;
  detail: string;
}>;

export type TaskReview = Readonly<{ outcome: "approved" | "blocked" | "pending"; findings: readonly string[] }>;

export type TaskDiffStat = Readonly<{ additions: number; deletions: number }>;

export type TaskRecordFile = Readonly<{ path: string; additions: number; deletions: number; patch: string }>;

export type TaskRecord = Readonly<{
  asked: string;
  happened: string;
  runNumber: number;
  review?: TaskReview;
  checks?: readonly string[];
  files?: readonly TaskRecordFile[];
  transcript?: string;
}>;

/**
 * Data slice 1 renders but the daemon does not serve yet. Every field is
 * optional; an absent field renders as an explicit "not yet served" state,
 * never as an invented value. See the GAPS section of the design doc for
 * the daemon API each field needs.
 */
export type ConsoleExtras = Readonly<{
  ticker?: readonly TickerEntry[];
  suggestions?: readonly SuggestionItem[];
  awaitingDeploy?: number;
  stages?: ReadonlyMap<string, TaskStage>;
  reviews?: ReadonlyMap<string, TaskReview>;
  diffs?: ReadonlyMap<string, TaskDiffStat>;
  rowTicks?: ReadonlyMap<string, string>;
  records?: ReadonlyMap<string, TaskRecord>;
}>;

/** The durable task status projected into the console stage vocabulary. */
export function stageOfTask(task: TaskItem, extras?: ConsoleExtras): TaskStage {
  const served = extras?.stages?.get(task.id);
  if (served !== undefined) return served;
  switch (task.status) {
    case "queued": return "queued";
    case "running": return "building";
    case "blocked": return "building";
    case "succeeded": return "done";
    case "failed": return "failed";
    case "cancelled": return "failed";
  }
}

/** How many meter segments are filled for a stage; done fills all four. */
export function stageMeterFill(stage: TaskStage): number {
  if (stage === "done") return STAGE_SEQUENCE.length;
  if (stage === "failed") return 0;
  return STAGE_SEQUENCE.indexOf(stage) + 1;
}

export function stageLabel(stage: TaskStage): string {
  return stage;
}

/** Tasks an agent is on right now (durable assignment, live statuses). */
export function agentCurrentTask(agent: AgentItem, state: StateView): TaskItem | undefined {
  for (const task of state.tasks.values()) {
    if (task.assigned_agent_id === agent.id && (task.status === "running" || task.status === "blocked")) return task;
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
 * served provider identity. C is Claude, X is Codex, s is the shell
 * provider.
 */
export function agentGlyph(agent: AgentItem): string {
  if (agent.role === "orchestrator") return "◆";
  switch (agent.provider) {
    case "claude_code": return "C";
    case "codex": return "X";
    case "shell": return "s";
  }
}

export type FactoryCounters = Readonly<{
  queued: number;
  needsYou: number;
  awaitingDeploy: number | undefined;
}>;

export function factoryCounters(state: StateView | undefined, extras?: ConsoleExtras): FactoryCounters {
  let queued = 0;
  if (state !== undefined) {
    for (const task of state.tasks.values()) {
      if (stageOfTask(task, extras) === "queued") queued += 1;
    }
  }
  return {
    queued,
    needsYou: state?.humanRequests.size ?? 0,
    awaitingDeploy: extras?.awaitingDeploy,
  };
}

/** Active work first, then queued, then finished; priority breaks ties. */
export function orderTasksForHome(state: StateView, extras?: ConsoleExtras): readonly TaskItem[] {
  const rank: Record<TaskStage, number> = { building: 0, reviewing: 1, merging: 2, queued: 3, done: 4, failed: 5 };
  return [...state.tasks.values()].sort((left, right) => {
    const byStage = rank[stageOfTask(left, extras)] - rank[stageOfTask(right, extras)];
    return byStage !== 0 ? byStage : right.priority - left.priority;
  });
}

/**
 * A console action the daemon has no API for yet. Fail closed: the caller
 * renders the reason; nothing pretends to succeed. `needs` names the missing
 * daemon surface exactly so the eventual wiring is discoverable.
 */
export type ConsoleActionUnavailable = Readonly<{ kind: "unavailable"; needs: string }>;

export type QueueActions = Readonly<{
  addWork(): ConsoleActionUnavailable;
  editTask(taskId: string): ConsoleActionUnavailable;
  commentTask(taskId: string): ConsoleActionUnavailable;
  deleteTask(taskId: string): ConsoleActionUnavailable;
  acceptSuggestion(suggestionId: string): ConsoleActionUnavailable;
  dismissSuggestion(suggestionId: string): ConsoleActionUnavailable;
  stopRun(agentId: string): ConsoleActionUnavailable;
}>;

const UNAVAILABLE_NEEDS: Record<keyof QueueActions, string> = {
  addWork: "daemon task-creation API (queue write)",
  editTask: "daemon task-edit API (queue write)",
  commentTask: "daemon task-comment API (queue write)",
  deleteTask: "daemon task-delete API (queue write)",
  acceptSuggestion: "daemon suggestion-accept API",
  dismissSuggestion: "daemon suggestion-accept API",
  stopRun: "daemon per-run cancel authority outside NEEDS YOU",
};

export function unavailableQueueActions(): QueueActions {
  return {
    addWork: () => ({ kind: "unavailable", needs: UNAVAILABLE_NEEDS.addWork }),
    editTask: () => ({ kind: "unavailable", needs: UNAVAILABLE_NEEDS.editTask }),
    commentTask: () => ({ kind: "unavailable", needs: UNAVAILABLE_NEEDS.commentTask }),
    deleteTask: () => ({ kind: "unavailable", needs: UNAVAILABLE_NEEDS.deleteTask }),
    acceptSuggestion: () => ({ kind: "unavailable", needs: UNAVAILABLE_NEEDS.acceptSuggestion }),
    dismissSuggestion: () => ({ kind: "unavailable", needs: UNAVAILABLE_NEEDS.dismissSuggestion }),
    stopRun: () => ({ kind: "unavailable", needs: UNAVAILABLE_NEEDS.stopRun }),
  };
}

export type ConsoleScreen =
  | { kind: "home" }
  | { kind: "queue" }
  | { kind: "needs-you" }
  | { kind: "task"; taskId: string };
