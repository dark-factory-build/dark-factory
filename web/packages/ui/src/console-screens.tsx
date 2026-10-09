import { useMemo, useState } from "react";
import type { AgentItem, HumanRequestItem, OperationalGraphView, OperationalNodeView, StateView, TaskItem } from "@dark-factory/client";
import {
  agentStatus,
  agentActivity,
  agentCurrentTask,
  projectGraph,
  projectFloor,
  projectProposals,
  type RunPathSample,
} from "./console-view.js";
import { FactoryScene, AgentSprite } from "./factory-scene/factory-scene.js";
import { projectCrates, type ProductionContraption } from "./production-view.js";
import { type ProjectContentCall } from "./project-library.js";
import type { SceneHall, SceneMachine } from "./factory-scene/scene.js";
import { SectionHeader, Status } from "./console-kit.js";
import { KnowledgeActivityList, activityLabel, onBoard, type KnowledgeActivity, type KnowledgeCue } from "./project-board.js";
import { DEFAULT_FLOOR_APPEARANCE, type FloorAppearance } from "./floor-appearance.js";

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

/** The operational floor; every action opens an existing inspector or control. */
export function FactoryFloor({
  changes = NO_CHANGES, changesRead = false, selectedChange, onSelectChange, state, graphs, graphErrors, onRetryGraphs, onLoadNode, onAddTask, runPaths, lastRunPaths, selectedAgentId, onSelectAgent,
  onSelectHumanRequest, selectedTaskId, onSelectTask, onOpenTasks, onOpenMissions, onOpenLibrary, onOpenBoard, requestedEntity, connected = true, floorAppearance = DEFAULT_FLOOR_APPEARANCE, projectId, activity = [], activityCues = [], onOpenActivity,
}: {
  /** Recorded Board and Library operations, newest first, and the few just cued. */
  activity?: readonly KnowledgeActivity[];
  activityCues?: readonly KnowledgeCue[];
  onOpenActivity?: (item: KnowledgeActivity) => void;
  changes?: readonly ProductionContraption[];
  /** Whether `changes` were read on this connection; until then the work line has nothing to look at. */
  changesRead?: boolean;
  selectedChange?: string;
  onSelectChange?: (key: string) => void;
  state: StateView | undefined;
  graphs: ReadonlyMap<string, OperationalGraphView> | undefined;
  graphErrors?: ReadonlyMap<string, string>;
  onRetryGraphs?: () => void;
  onLoadNode?: (projectId: string, nodeId: string) => Promise<OperationalNodeView>;
  onAddTask?: (agent: AgentItem, instruction: string, mode: "queue" | "any") => Promise<boolean>;
  runPaths?: ReadonlyMap<string, RunPathSample>;
  lastRunPaths?: ReadonlyMap<string, RunPathSample>;
  selectedAgentId?: string;
  onSelectAgent?: (agent: AgentItem) => void;
  selectedTaskId?: string;
  onSelectTask?: (taskId: string) => void;
  onOpenTasks?: (projectId?: string) => void;
  onOpenMissions?: (projectId?: string) => void;
  onOpenLibrary?: (projectId?: string) => void;
  onOpenBoard?: (projectId?: string, entity?: string, id?: string, repository?: string) => void;
  requestedEntity?: { id: string };
  onSelectHumanRequest?: (request: HumanRequestItem) => void;
  connected?: boolean;
  floorAppearance: FloorAppearance;
  projectId?: string;
  onProjectContent?: ProjectContentCall;
}) {
  const projects = useMemo(() => [...state?.projects.values() ?? []].map(({ id }) => id).sort(), [JSON.stringify([...state?.projects.keys() ?? []].sort())]);
  const prepared = useMemo(() => projectGraph(graphs, projects), [graphs, projects]);
  const scene = useMemo(() => projectFloor(state, prepared, runPaths, lastRunPaths), [state, prepared, runPaths, lastRunPaths]);
  const proposed = useMemo(() => projectProposals(prepared, changes), [prepared, changes]);
  const crates = useMemo(() => changesRead ? projectCrates(changes) : undefined, [changes, changesRead]);
  const peerQuestions = useMemo(() => [...(state?.peerQuestions?.values() ?? [])], [state]);
  const projectOf = (nodeId: string) => projects.find((id) => graphs?.get(id)?.nodes.some((node) => node.id === nodeId || nodeId.startsWith(`${node.id}:`)));
  const unplaced = proposed.proposals.filter((proposal) => proposal.state === "unavailable" || proposal.operations.some((operation) => operation.roomId === undefined)).length;
  const unavailable = projects.flatMap((id) => graphs?.get(id)?.sources.filter((source) => source.kind === "unavailable") ?? []);
  const investigate = onAddTask === undefined || state === undefined ? undefined : (machine: SceneMachine, hall?: SceneHall) => {
    const project = projectOf(machine.id) ?? projectId;
    const agent = [...state.agents.values()].filter((candidate) => !candidate.archived && candidate.project_id === project).sort((left, right) => Number(left.role === "orchestrator") - Number(right.role === "orchestrator") || left.id.localeCompare(right.id))[0];
    if (agent === undefined) return;
    // Only the node's identity, static label and counts: runtime-only values
    // come from unauthenticated local senders and never enter instructions.
    const reading = machine.reading;
    const label = reading.evidence === "runtime" ? `runtime-only ${machine.kind}` : machine.label;
    void onAddTask(agent, [
      `Investigate the ${machine.kind} "${label}"${hall !== undefined && hall.id !== machine.id ? ` in ${hall.label}` : ""} (operational node ${machine.represented?.join(", ") ?? machine.id}).`,
      `Observation: ${reading.observation}; state: ${reading.state}; evidence: ${reading.evidence}; ${reading.ratePerHour} events/hour; ${reading.errorPermille / 10}% errors; p95 ${reading.latencyMs} ms.`,
      reading.evidence === "runtime" ? "The code does not explain this runtime activity. Find what serves it and make the static model and the code agree, or report why it cannot be explained."
        : reading.observation === "unobserved" ? "Nothing observes this component. Find a way to observe it with the project's existing tooling, or report why it cannot be observed."
        : "Find the cause, fix it, and report what changed. Use the factory's ordinary change, review and release path.",
    ].join("\n"), "any");
  };
  return <div className="dfFactoryFloor">
    <div className="dfFactoryFloor__scene">
    <FactoryScene
      tools={<>
        {graphErrors !== undefined && graphErrors.size > 0 ? <p role="alert">Could not read the operational structure. <button type="button" onClick={onRetryGraphs}>Retry</button></p> : null}
        {onOpenActivity === undefined ? null : <KnowledgeActivityList items={activity} state={state} onOpen={onOpenActivity} />}
        {unplaced > 0 ? <p className="dfFactoryEntityTools__notice" role="status">{unplaced} {unplaced === 1 ? "change is" : "changes are"} not fully placed on the floor; its pull request in Work lists every path.</p> : null}
        {unavailable.length > 0 ? <p className="dfFactoryEntityTools__notice" role="status">Source unavailable for {unavailable.map((source) => source.name).join(", ")}; those halls cannot be inferred.</p> : null}
      </>}
      onLoadNode={onLoadNode === undefined ? undefined : (nodeId) => { const project = projectOf(nodeId); return project === undefined ? Promise.reject(new Error("unknown node")) : onLoadNode(project, nodeId); }}
      onInvestigate={investigate}
      onDiscussSource={onOpenBoard === undefined ? undefined : (nodeId) => { const project = projectOf(nodeId); onOpenBoard(project, project === undefined ? undefined : `${project}:${nodeId}`); }}
      proposals={{ items: proposed.proposals, selected: selectedChange, onSelect: (id) => onSelectChange?.(id) }}
      crates={crates}
      appearance={floorAppearance}
      selectedWorkerId={selectedAgentId}
      selectedTaskId={selectedTaskId}
      graph={scene.graph}
      projectId={projectId}
      workers={[...scene.workers, ...proposed.reviewers.filter((worker) => !selectedChange || worker.review?.proposalId === selectedChange)]}
      connected={connected}
      reading={(graphErrors === undefined || graphErrors.size === 0) && (graphs === undefined || projects.some((id) => !graphs.has(id)))}
      tasks={scene.tasks}
      peerQuestions={peerQuestions}
      knowledgeCues={activityCues.map((cue) => ({ key: cue.key, agentId: cue.agent_id, board: onBoard(cue), reading: cue.operation === "read", label: activityLabel(cue, state), open: onOpenActivity === undefined ? undefined : () => onOpenActivity(cue) }))}
      requestedEntity={requestedEntity}
      onSelectTask={onSelectTask}
      onOpenTasks={onOpenTasks}
      onOpenMissions={onOpenMissions}
      onOpenLibrary={onOpenLibrary}
      onOpenBoard={onOpenBoard}
      onSelectHumanRequest={onSelectHumanRequest === undefined || state === undefined ? undefined : (id) => {
        const request = state.humanRequests.get(id);
        if (request !== undefined) onSelectHumanRequest(request);
      }}
      onSelectWorker={onSelectAgent === undefined || state === undefined ? undefined : (workerID) => {
        const agent = state.agents.get(workerID);
        if (agent !== undefined) onSelectAgent(agent);
        else { const reviewer = proposed.reviewers.find((actor) => actor.id === workerID); if (reviewer?.review) onSelectChange?.(reviewer.review.proposalId); }
      }}
    />
    </div>
  </div>;
}

export function rankLabel(role: AgentItem["role"]): string {
  return role === "orchestrator" ? "Overseer" : "Worker";
}

/** Agents grouped by rank, oversight first. */
export function AgentList({
  state,
  selectedAgentId,
  ready,
  onSelectAgent,
}: {
  state: StateView | undefined;
  selectedAgentId?: string;
  ready: boolean;
  onSelectAgent?: (agent: AgentItem) => void;
}) {
  const [showArchived, setShowArchived] = useState(false);
  if (state === undefined) return <p className="dfFactoryConsole__empty">waiting for the factory</p>;
  const agents = [...state.agents.values()].filter((agent) => showArchived || !agent.archived);
  return (
    <div className="dfAgentList">
      <label><input type="checkbox" checked={showArchived} onChange={(event) => setShowArchived(event.currentTarget.checked)} /> Show archived</label>
      {agents.length === 0 ? <p className="dfFactoryConsole__empty">no agents</p> : null}
      {(["orchestrator", "worker"] as const).map((role) => {
        const members = agents.filter((agent) => agent.role === role);
        if (members.length === 0) return null;
        return (
          <section key={role} aria-label={rankLabel(role)}>
            <SectionHeader title={rankLabel(role)} count={members.length} />
            <ul className="dfConsoleRows">
              {members.map((agent) => (
                <li key={agent.id}>
                  <AgentRow
                    agent={agent}
                    state={state}
                    selected={selectedAgentId === agent.id}
                    ready={ready}
                    onSelectAgent={onSelectAgent}
                  />
                </li>
              ))}
            </ul>
          </section>
        );
      })}
    </div>
  );
}

function AgentRow({
  agent,
  state,
  selected,
  ready,
  onSelectAgent,
}: {
  agent: AgentItem;
  state: StateView;
  selected: boolean;
  ready: boolean;
  onSelectAgent?: (agent: AgentItem) => void;
}) {
  const activity = agentStatus(agent, state);
  const task = agentCurrentTask(agent, state);
  let queued = 0;
  for (const item of state.tasks.values()) if (item.assigned_agent_id === agent.id && item.status === "queued") queued += 1;
  const label = `${agent.name}: ${activity} · ${projectLabel(state, agent.project_id)}`;
  const cells = (
    <>
      <AgentSprite agent={agent} activity={agentActivity(agent, state)} />
      <span className="dfConsoleRow__title">{agent.name} · {projectLabel(state, agent.project_id)}</span>
      <span className="dfAgentList__provider">{agent.effective_model === "" ? agent.provider : `${agent.provider} · ${agent.effective_model}`}</span>
      <span className="dfAgentList__activity"><Status stage={agent.archived ? "archived" : activity} /></span>
      <span className="dfConsoleRow__agent">{task?.title ?? "no current task"}</span>
      <span className="dfAgentList__count">{queued} queued</span>
    </>
  );
  if (onSelectAgent === undefined) return <span className="dfConsoleRow" aria-label={label}>{cells}</span>;
  return (
    <button
      type="button"
      className="dfConsoleRow dfAgentList__row"
      aria-label={label}
      aria-pressed={selected}
      disabled={!ready}
      onClick={() => onSelectAgent(agent)}
    >
      {cells}
    </button>
  );
}
