import { useMemo } from "react";
import type { AgentItem, HumanRequestItem, OperationalGraphView, StateView, TaskItem } from "@dark-factory/client";
import {
  agentStatus,
  agentActivity,
  projectGraph,
  projectFloor,
  projectProposals,
  type RunPathSample,
} from "./console-view.js";
import { FactoryScene, AgentSprite } from "./factory-scene/factory-scene.js";
import { projectCrates, type ProductionContraption } from "./production-view.js";
import type { ProjectContentCall } from "./factory-app-controller.js";
import { Status } from "./console-kit.js";

function shortID(value: string): string {
  return value.slice(0, 8);
}

function projectLabel(state: StateView | undefined, projectID: string): string {
  return state?.projects.get(projectID)?.name ?? `project ${shortID(projectID)}`;
}

/** The phone's floor: every agent as its own sprite, with what it is doing. */
export function AgentStrip({ state, ask }: { state: StateView | undefined; ask?: (agentId: string) => (() => void) | undefined }) {
  return (
    <nav className="dfConsoleStrip" aria-label="Agents">
      <ul className="dfConsoleStrip__agents">
        {state === undefined ? (
          <li className="dfConsoleStrip__empty">waiting for snapshot</li>
        ) : state.agents.size === 0 ? (
          <li className="dfConsoleStrip__empty">no agents</li>
        ) : (
          [...state.agents.values()].filter((agent) => !agent.archived).map((agent) => {
            const status = agentStatus(agent, state);
            const cell = (
              <>
                <AgentSprite agent={agent} activity={agentActivity(agent, state)} />
                <span className="dfConsoleStrip__agentName">{agent.name} · {projectLabel(state, agent.project_id)}</span>
                <span className="dfConsoleStrip__agentPhase"><Status stage={status} /></span>
              </>
            );
            const className = `dfConsoleStrip__agent dfConsoleStrip__agent--${status}`;
            return (
              <li key={agent.id}>
                {ask?.(agent.id) !== undefined
                  ? <button type="button" className={className} onClick={ask(agent.id)}>{cell}</button>
                  : <span className={className}>{cell}</span>}
              </li>
            );
          })
        )}
      </ul>
    </nav>
  );
}

/** Segments fill only from durable task status. */
export function StageMeter({ stage }: { stage: TaskItem["status"] }) {
  const filled = stage === "queued" ? 1 : stage === "running" || stage === "succeeded" ? 2 : 0;
  return (
    <span className="dfStageMeter" role="img" aria-label={`stage: ${stage}`}>
      {["queued", "running"].map((name, index) => (
        <span
          key={name}
          className={`dfStageMeter__segment${index < filled ? " dfStageMeter__segment--filled" : ""}`}
          aria-hidden="true"
        />
      ))}
      <span
        className={`dfStageMeter__terminal${stage === "blocked" ? " dfStageMeter__terminal--blocked" : ""}${stage === "succeeded" ? " dfStageMeter__terminal--done" : ""}${stage === "failed" ? " dfStageMeter__terminal--failed" : ""}`}
        aria-hidden="true"
      >
        {stage === "blocked" ? "!" : stage === "succeeded" ? "✓" : stage === "failed" ? "×" : stage === "cancelled" ? "−" : ""}
      </span>
    </span>
  );
}

const NO_CHANGES: readonly ProductionContraption[] = [];

/** The operational floor: observation and navigation; every action opens an existing control. */
export function FactoryFloor({
  changes = NO_CHANGES, changesRead = false, state, graphs, graphErrors, runPaths, lastRunPaths, selectedAgentId, onSelectAgent,
  onSelectHumanRequest, selectedTaskId, onSelectTask, onOpenTasks, connected = true, projectId,
}: {
  changes?: readonly ProductionContraption[];
  /** Whether `changes` were read on this connection; until then the work line has nothing to look at. */
  changesRead?: boolean;
  state: StateView | undefined;
  graphs: ReadonlyMap<string, OperationalGraphView> | undefined;
  graphErrors?: ReadonlyMap<string, string>;
  runPaths?: ReadonlyMap<string, RunPathSample>;
  lastRunPaths?: ReadonlyMap<string, RunPathSample>;
  selectedAgentId?: string;
  onSelectAgent?: (agent: AgentItem) => void;
  selectedTaskId?: string;
  onSelectTask?: (taskId: string) => void;
  onOpenTasks?: (projectId?: string) => void;
  onSelectHumanRequest?: (request: HumanRequestItem) => void;
  connected?: boolean;
  projectId?: string;
  onProjectContent?: ProjectContentCall;
}) {
  const projects = useMemo(() => [...state?.projects.values() ?? []].map(({ id }) => id).sort(), [JSON.stringify([...state?.projects.keys() ?? []].sort())]);
  const prepared = useMemo(() => projectGraph(graphs, projects), [graphs, projects]);
  const scene = useMemo(() => projectFloor(state, prepared, runPaths, lastRunPaths), [state, prepared, runPaths, lastRunPaths]);
  const proposed = useMemo(() => projectProposals(prepared, changes), [prepared, changes]);
  const crates = useMemo(() => changesRead ? projectCrates(changes) : undefined, [changes, changesRead]);
  const peerQuestions = useMemo(() => [...(state?.peerQuestions?.values() ?? [])], [state]);
  const unavailable = projects.flatMap((id) => graphs?.get(id)?.sources.filter((source) => source.kind === "unavailable") ?? []);
  const refused = projects.flatMap((id) => { const code = graphErrors?.get(id); return code === undefined ? [] : [`${state?.projects.get(id)?.name ?? id} · ${code}`]; });
  return <div className="dfFactoryFloor">
    <FactoryScene
      tools={<>
        {refused.length > 0 ? <p className="dfFactoryEntityTools__notice" role="status">Plant unavailable: {refused.join(", ")}; retrying.</p> : null}
        {unavailable.length > 0 ? <p className="dfFactoryEntityTools__notice" role="status">Source unavailable for {unavailable.map((source) => source.name).join(", ")}; those units cannot be inferred.</p> : null}
      </>}
      crates={crates}
      selectedWorkerId={selectedAgentId}
      selectedTaskId={selectedTaskId}
      graph={scene.graph}
      projectId={projectId}
      workers={[...scene.workers, ...proposed.reviewers]}
      connected={connected}
      unavailable={refused.length === 0 ? undefined : refused.join(", ")}
      reading={graphs === undefined || projects.some((id) => !graphs.has(id) && !graphErrors?.has(id))}
      tasks={scene.tasks}
      peerQuestions={peerQuestions}
      onSelectTask={onSelectTask}
      onOpenTasks={onOpenTasks}
      onSelectHumanRequest={onSelectHumanRequest === undefined || state === undefined ? undefined : (id) => {
        const request = state.humanRequests.get(id);
        if (request !== undefined) onSelectHumanRequest(request);
      }}
      onSelectWorker={onSelectAgent === undefined || state === undefined ? undefined : (workerID) => {
        const agent = state.agents.get(workerID);
        if (agent !== undefined) onSelectAgent(agent);
      }}
    />
  </div>;
}

export function rankLabel(role: AgentItem["role"] | "specialist"): string {
  return role === "orchestrator" ? "Overseer" : role === "specialist" ? "Specialist" : "Worker";
}
