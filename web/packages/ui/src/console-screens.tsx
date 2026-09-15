import { useEffect, useMemo, useState } from "react";
import type { AgentItem, HumanRequestItem, StateView, TaskItem, TopologyView } from "@dark-factory/client";
import {
  agentStatus,
  agentActivity,
  agentCurrentTask,
  agentGlyph,
  factoryCounters,
  prepareFloor,
  selectFloor,
  projectFloor,
  type RunPathSample,
} from "./console-view.js";
import { FactoryScene, AgentSprite } from "./factory-scene/factory-scene.js";

function shortID(value: string): string {
  return value.slice(0, 8);
}

function projectLabel(state: StateView | undefined, projectID: string): string {
  return state?.projects.get(projectID)?.name ?? `project ${shortID(projectID)}`;
}

/** The load-bearing cross-screen status bar: agents plus served counters. */
export function AgentStrip({
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
  const counters = factoryCounters(state);
  return (
    <nav className="dfConsoleStrip" aria-label="Agents and factory counters">
      <ul className="dfConsoleStrip__agents">
        {state === undefined ? (
          <li className="dfConsoleStrip__empty">waiting for snapshot</li>
        ) : state.agents.size === 0 ? (
          <li className="dfConsoleStrip__empty">no agents</li>
        ) : (
          [...state.agents.values()].filter((agent) => !agent.archived).map((agent) => {
            const activity = agentStatus(agent, state);
            const phase = activity;
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
        <Counter glyph="▒" label={`${counters.queued ?? "—"} queued`} />
        <Counter glyph="!" label={`${counters.needsYou ?? "—"} NEEDS YOU`} alert={(counters.needsYou ?? 0) > 0} />
      </div>
    </nav>
  );
}

function Counter({ glyph, label, alert }: { glyph: string; label: string; alert?: boolean }) {
  return (
    <span className={`dfConsoleStrip__counter${alert === true ? " dfConsoleStrip__counter--alert" : ""}`}>
      <span aria-hidden="true">{glyph}</span> {label}
    </span>
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

/** The floor shares the normal task detail and HumanRequest routes. */
export function FactoryFloor({
  state, topologies, runPaths, lastRunPaths, selectedAgentId, onSelectAgent,
  onSelectHumanRequest, selectedTaskId, onSelectTask, onOpenQueue, connected = true,
}: {
  state: StateView | undefined;
  topologies: ReadonlyMap<string, TopologyView> | undefined;
  runPaths?: ReadonlyMap<string, RunPathSample>;
  lastRunPaths?: ReadonlyMap<string, RunPathSample>;
  selectedAgentId?: string;
  onSelectAgent?: (agent: AgentItem) => void;
  onOpenQueue?: () => void;
  selectedTaskId?: string;
  onSelectTask?: (taskId: string) => void;
  onSelectHumanRequest?: (request: HumanRequestItem) => void;
  connected?: boolean;
}) {
  const [{ scopeId, page }, setView] = useState<{ scopeId?: string; page: number }>({ page: 0 });
  const setScopeId = (scopeId: string | undefined) => setView({ scopeId, page: 0 });
  // Snapshot decoding replaces the projects Map even when only live work changed.
  const projectsKey = JSON.stringify([...state?.projects.values() ?? []].map(({ id, name }) => [id, name]).sort(([left], [right]) => left!.localeCompare(right!)));
  const prepared = useMemo(() => prepareFloor(state?.projects, topologies), [projectsKey, topologies]);
  const selected = useMemo(() => selectFloor(prepared, scopeId, page), [prepared, scopeId, page]);
  const scene = useMemo(() => projectFloor(state, selected, runPaths, lastRunPaths), [state, selected, runPaths, lastRunPaths]);
  useEffect(() => {
    if (scopeId !== scene.navigation.scopeId || page !== scene.navigation.page) setView({ scopeId: scene.navigation.scopeId, page: scene.navigation.page });
  }, [scopeId, page, scene.navigation.scopeId, scene.navigation.page]);
  const inventoryOmitted = [...(state?.projects.keys() ?? [])].reduce((count, id) => count + (topologies?.get(id)?.inventoryOmitted ?? 0), 0);
  return <div className="dfFactoryFloor">
    <nav className="dfFactoryFloor__navigation" aria-label="Floor hierarchy">
      {scene.navigation.breadcrumbs.map((crumb, index) => <span key={crumb.id ?? "root"}>
        {index === 0 ? null : <span aria-hidden="true"> / </span>}
        <button type="button" aria-current={index === scene.navigation.breadcrumbs.length - 1 ? "page" : undefined} disabled={index === scene.navigation.breadcrumbs.length - 1} onClick={() => setScopeId(crumb.id)}>{crumb.label}</button>
      </span>)}
      {scene.navigation.scopeId === undefined ? null : <button type="button" onClick={() => setScopeId(scene.navigation.backScopeId)}>BACK</button>}
    </nav>
    {inventoryOmitted === 0 ? null : <p role="status">{inventoryOmitted} room inventories omitted from the served projects; those rooms show inventory unavailable.</p>}
    {scene.navigation.pageCount <= 1 ? null : <nav aria-label="Floor pages">
      <button type="button" disabled={scene.navigation.page === 0} onClick={() => setView({ scopeId, page: scene.navigation.page - 1 })}>Previous spaces</button>
      <span> Page {scene.navigation.page + 1} of {scene.navigation.pageCount} · {scene.navigation.omittedChildren} spaces on other pages </span>
      <button type="button" disabled={scene.navigation.page + 1 === scene.navigation.pageCount} onClick={() => setView({ scopeId, page: scene.navigation.page + 1 })}>Next spaces</button>
    </nav>}
    {scene.navigation.outsideScopeActivity === 0 && scene.navigation.hiddenScopeActivity === 0 ? null : <p className="dfFactoryFloor__scopeSummary" role="status">
      {scene.navigation.outsideScopeActivity === 0 ? null : `${scene.navigation.outsideScopeActivity} active tasks outside this scope. `}
      {scene.navigation.hiddenScopeActivity === 0 ? null : `${scene.navigation.hiddenScopeActivity} active tasks within this scope, outside displayed rooms.`}
    </p>}
    <div className="dfFactoryFloor__scene">
    <FactoryScene
      selectedWorkerId={selectedAgentId}
      selectedTaskId={selectedTaskId}
      topology={scene.topology}
      workers={scene.workers}
      connected={connected}
      tasks={scene.tasks}
      omittedLocations={scene.omittedLocations}
      enterableRoomIds={scene.navigation.enterableIds}
      onEnterRoom={setScopeId}
      onSelectTask={onSelectTask}
      onOpenQueue={onOpenQueue}
      onSelectHumanRequest={onSelectHumanRequest === undefined || state === undefined ? undefined : (id) => {
        const request = state.humanRequests.get(id);
        if (request !== undefined) onSelectHumanRequest(request);
      }}
      onSelectWorker={onSelectAgent === undefined || state === undefined ? undefined : (workerID) => {
        const agent = state.agents.get(workerID);
        if (agent !== undefined) onSelectAgent(agent);
      }}
    />
    </div>
  </div>;
}

/** Rank is the served role: an orchestrator oversees, a worker builds. */
export function rankLabel(role: AgentItem["role"]): string {
  return role === "orchestrator" ? "OVERSEER" : "WORKER";
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
            <div className="dfFactoryConsole__sectionHeading">
              <h2>{rankLabel(role)}</h2>
              <span>{members.length}</span>
            </div>
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
  const label = `${agent.name}: ${activity}`;
  const cells = (
    <>
      <AgentSprite agent={agent} activity={agentActivity(agent, state)} />
      <span className="dfConsoleRow__title">{agent.name}</span>
      <span className="dfAgentList__provider">{agent.effective_model === "" ? agent.provider : `${agent.provider} · ${agent.effective_model}`}</span>
      <span className="dfAgentList__activity">{agent.archived ? "archived" : activity === "needs-you" ? "! needs you" : activity}</span>
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

/** Queue: the durable queued tasks, with no invented mutation controls. */
export function QueueScreen({ state }: { state: StateView | undefined }) {
  const queued =
    state === undefined
      ? undefined
      : [...state.tasks.values()].filter((task) => task.status === "queued");
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
