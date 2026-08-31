import type { AgentItem, StateView } from "@dark-factory/client";
import {
  STAGE_SEQUENCE,
  agentActivity,
  agentCurrentTask,
  agentGlyph,
  factoryCounters,
  orderTasksForHome,
  stageMeterFill,
  stageOfTask,
  type ConsoleScreen,
  type TaskStage,
} from "./console-view.js";

const STAGE_GLYPHS: Record<TaskStage, string> = {
  queued: "▒",
  building: "░",
  done: "✓",
  failed: "×",
};

function shortID(value: string): string {
  return value.slice(0, 8);
}

function projectLabel(state: StateView | undefined, projectID: string): string {
  return state?.projects.get(projectID)?.name ?? `project ${shortID(projectID)}`;
}

function agentLabel(state: StateView | undefined, agentID: string): string {
  return state?.agents.get(agentID)?.name ?? `agent ${shortID(agentID)}`;
}

/** The load-bearing cross-screen status bar: agents plus served counters. */
export function AgentStrip({
  state,
  selectedAgentId,
  ready,
  onSelectAgent,
  onNavigate,
}: {
  state: StateView | undefined;
  selectedAgentId?: string;
  ready: boolean;
  onSelectAgent?: (agent: AgentItem) => void;
  onNavigate?: (screen: ConsoleScreen) => void;
}) {
  const counters = factoryCounters(state);
  return (
    <nav className="dfConsoleStrip" aria-label="Agents and factory counters">
      <ul className="dfConsoleStrip__agents">
        {state === undefined ? (
          <li className="dfConsoleStrip__empty">waiting for snapshot</li>
        ) : state.agents.size === 0 ? (
          <li className="dfConsoleStrip__empty">no agents</li>
        ) : (
          [...state.agents.values()].map((agent) => {
            const activity = agentActivity(agent, state);
            const task = agentCurrentTask(agent, state);
            const phase =
              activity === "busy" && task !== undefined ? stageOfTask(task) : activity;
            const cell = (
              <>
                <span className="dfConsoleStrip__glyph" aria-hidden="true">
                  {agentGlyph(agent)}
                </span>
                <span className="dfConsoleStrip__agentName">{agent.name}</span>
                <span className="dfConsoleStrip__agentPhase">
                  {activity === "needs-you" ? "! needs you" : phase}
                </span>
              </>
            );
            const className = `dfConsoleStrip__agent dfConsoleStrip__agent--${activity}`;
            return (
              <li key={agent.id}>
                {onSelectAgent === undefined ? (
                  <span className={className} aria-label={`${agent.name}: ${phase}`}>
                    {cell}
                  </span>
                ) : (
                  <button
                    type="button"
                    className={className}
                    aria-pressed={selectedAgentId === agent.id}
                    aria-label={`${agent.name}: ${phase}`}
                    disabled={!ready}
                    onClick={() => onSelectAgent(agent)}
                  >
                    {cell}
                  </button>
                )}
              </li>
            );
          })
        )}
      </ul>
      <div className="dfConsoleStrip__counters">
        <Counter
          glyph="▒"
          label={`${counters.queued ?? "—"} queued`}
          onActivate={onNavigate === undefined ? undefined : () => onNavigate({ kind: "queue" })}
        />
        <Counter
          glyph="!"
          label={`${counters.needsYou ?? "—"} NEEDS YOU`}
          alert={(counters.needsYou ?? 0) > 0}
          onActivate={
            onNavigate === undefined ? undefined : () => onNavigate({ kind: "needs-you" })
          }
        />
      </div>
    </nav>
  );
}

function Counter({
  glyph,
  label,
  alert,
  onActivate,
}: {
  glyph: string;
  label: string;
  alert?: boolean;
  onActivate?: () => void;
}) {
  const className = `dfConsoleStrip__counter${alert === true ? " dfConsoleStrip__counter--alert" : ""}`;
  if (onActivate === undefined) {
    return (
      <span className={className}>
        <span aria-hidden="true">{glyph}</span> {label}
      </span>
    );
  }
  return (
    <button type="button" className={className} onClick={onActivate}>
      <span aria-hidden="true">{glyph}</span> {label}
    </button>
  );
}

/** Segments fill only from durable task status. */
export function StageMeter({ stage }: { stage: TaskStage }) {
  const filled = stageMeterFill(stage);
  return (
    <span className="dfStageMeter" role="img" aria-label={`stage: ${stage}`}>
      {STAGE_SEQUENCE.map((name, index) => (
        <span
          key={name}
          className={`dfStageMeter__segment${index < filled ? " dfStageMeter__segment--filled" : ""}`}
          aria-hidden="true"
        />
      ))}
      <span
        className={`dfStageMeter__terminal${stage === "done" ? " dfStageMeter__terminal--done" : ""}${stage === "failed" ? " dfStageMeter__terminal--failed" : ""}`}
        aria-hidden="true"
      >
        {stage === "done" ? "✓" : stage === "failed" ? "×" : ""}
      </span>
    </span>
  );
}

/** Home: the complete durable task projection. */
export function HomeScreen({ state }: { state: StateView | undefined }) {
  if (state === undefined) {
    return <p className="dfFactoryConsole__empty">waiting for the factory</p>;
  }
  const tasks = orderTasksForHome(state);
  return (
    <section className="dfFactoryConsole__section" aria-label="Tasks">
      <div className="dfFactoryConsole__sectionHeading">
        <h2>TASKS</h2>
        <span>
          {tasks.length} {tasks.length === 1 ? "task" : "tasks"}
        </span>
      </div>
      {tasks.length === 0 ? (
        <p className="dfFactoryConsole__empty">no tasks yet</p>
      ) : (
        <ul className="dfConsoleRows">
          {tasks.map((task) => {
            const stage = stageOfTask(task);
            return (
              <li key={task.id}>
                <div className="dfConsoleRow">
                  <span className="dfConsoleRow__glyph" aria-hidden="true">
                    {STAGE_GLYPHS[stage]}
                  </span>
                  <span className="dfConsoleRow__title">{task.title}</span>
                  <span className="dfConsoleRow__agent">
                    {agentLabel(state, task.assigned_agent_id)}
                  </span>
                  <StageMeter stage={stage} />
                </div>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

/** Queue: the durable queued tasks, with no invented mutation controls. */
export function QueueScreen({ state }: { state: StateView | undefined }) {
  const queued =
    state === undefined
      ? undefined
      : [...state.tasks.values()].filter((task) => stageOfTask(task) === "queued");
  return (
    <section className="dfFactoryConsole__section" aria-label="Queue">
      <div className="dfFactoryConsole__sectionHeading">
        <h2>QUEUE</h2>
        <span>{queued === undefined ? "— queued" : `${queued.length} queued`}</span>
      </div>
      {queued === undefined ? (
        <p className="dfFactoryConsole__empty">waiting for snapshot</p>
      ) : queued.length === 0 ? (
        <p className="dfFactoryConsole__empty">the queue is empty</p>
      ) : (
        <ul className="dfConsoleRows">
          {queued.map((task) => (
            <li key={task.id}>
              <div className="dfConsoleRow">
                <span className="dfConsoleRow__glyph" aria-hidden="true">
                  ▒
                </span>
                <span className="dfConsoleRow__title">{task.title}</span>
                <span className="dfConsoleRow__agent">
                  priority {task.priority} · {projectLabel(state, task.project_id)}
                </span>
                <StageMeter stage="queued" />
              </div>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
