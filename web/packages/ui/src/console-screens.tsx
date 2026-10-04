import { useEffect, useMemo, useRef, useState } from "react";
import type { AgentItem, HumanRequestItem, StateView, TaskItem, TopologyView } from "@dark-factory/client";
import {
  agentStatus,
  agentActivity,
  agentCurrentTask,
  prepareFloor,
  selectFloor,
  projectFloor,
  projectProposals,
  type RunPathSample,
} from "./console-view.js";
import { FactoryScene, AgentSprite } from "./factory-scene/factory-scene.js";
import { productionKey, type ProductionContraption } from "./production-view.js";
import { knowledgeMetadata, type ProjectContentCall } from "./project-library.js";
import type { SceneNode } from "./factory-scene/scene.js";
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
                <span className="dfConsoleStrip__agentPhase">{status === "needs-you" ? "! needs you" : status}</span>
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

/** Flat source projection; every action opens an existing inspector or control. */
export function FactoryFloor({
  changes = NO_CHANGES, selectedChange, onSelectChange, state, topologies, runPaths, lastRunPaths, selectedAgentId, onSelectAgent,
  onSelectHumanRequest, selectedTaskId, onSelectTask, onOpenTasks, onOpenMissions, onOpenLibrary, onOpenBoard, requestedEntity, connected = true, floorAppearance = DEFAULT_FLOOR_APPEARANCE, projectId, onProjectContent,
}: {
  changes?: readonly ProductionContraption[];
  selectedChange?: string;
  onSelectChange?: (key: string) => void;
  state: StateView | undefined;
  topologies: ReadonlyMap<string, TopologyView> | undefined;
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
  onProject?: (projectId: string | undefined) => void;
  onProjectContent?: ProjectContentCall;
}) {
  const [selectedEntity, setSelectedEntity] = useState<string>();
  useEffect(() => { if (requestedEntity) setSelectedEntity(requestedEntity.id); }, [requestedEntity]);
  const projectsKey = JSON.stringify([...state?.projects.values() ?? []].map(({ id, name }) => [id, name]).sort(([left], [right]) => left!.localeCompare(right!)));
  const prepared = useMemo(() => prepareFloor(state?.projects, topologies), [projectsKey, topologies]);
  const selected = useMemo(() => selectFloor(prepared, floorAppearance.detail ?? "auto"), [prepared, floorAppearance.detail]);
  const scene = useMemo(() => projectFloor(state, selected, runPaths, lastRunPaths), [state, selected, runPaths, lastRunPaths]);
  const proposed = useMemo(() => projectProposals(selected, changes), [selected, changes]);
  const peerQuestions = useMemo(() => [...(state?.peerQuestions?.values() ?? [])], [state]);
  const inventoryOmitted = [...(state?.projects.keys() ?? [])].reduce((count, id) => count + (topologies?.get(id)?.inventoryOmitted ?? 0), 0);
  const entity = selected.detailByID.get(selectedEntity ?? "");
  const related = proposed.proposals.filter((proposal) => proposal.operations.some((operation) => operation.entityId === selectedEntity || operation.roomId === selectedEntity) || proposal.relationships?.some((edge) => edge.fromId === selectedEntity || edge.toId === selectedEntity));
  return <div className="dfFactoryFloor">
    <details className="dfFactoryFloor__source"><summary>Integrated source · {state?.projects.size ?? 0} projects</summary>
      {[...(state?.projects.values() ?? [])].map((project) => { const topology = topologies?.get(project.id); return <p key={project.id}>{project.name}: {topology?.sources?.length ? topology.sources.map((source) => <span key={source.repository_id}> · {source.repository_id} · {source.target_ref || "target unavailable"} · {source.kind} · {source.revision ? <code>{source.revision}</code> : "revision unavailable"}{source.reason ? `: ${source.reason}` : ""}</span>) : topology?.sourceRevision ? <code>{topology.sourceRevision}</code> : "integrated revision unavailable"}</p>; })}
    </details>
    {inventoryOmitted === 0 && scene.aggregatedLocations === 0 ? null : <p role="status">{inventoryOmitted > 0 ? `${inventoryOmitted} source inventories unavailable. ` : ""}{scene.aggregatedLocations > 0 ? `${scene.aggregatedLocations} areas aggregated into their visible ancestors; search still reaches every served entity.` : ""}</p>}
    {proposed.aggregatedProposals === 0 ? null : <p role="status">{proposed.aggregatedProposals} proposed new areas are marked in their owning rooms. All observed paths remain in the Change inspector.</p>}
    <div className="dfFactoryFloor__changes" aria-label="Source observation notices">
      {changes.filter((item) => proposed.proposals.some((proposal) => proposal.id === productionKey(item)) && (item.source.omitted > 0 || item.source.kind === "unavailable")).map((item) => <p key={productionKey(item)} role="status">{item.pullRequest?.title || item.construction?.title}: {item.source.omitted > 0 ? `${item.source.omitted} paths outside this bounded observation. ` : ""}{item.source.reason} Inspect the Change for full evidence.</p>)}
    </div>
    <div className="dfFactoryFloor__scene">
    <FactoryScene
      proposals={{ items: proposed.proposals, selected: selectedChange, onSelect: (id) => onSelectChange?.(id) }}
      appearance={floorAppearance}
      selectedWorkerId={selectedAgentId}
      selectedTaskId={selectedTaskId}
      topology={proposed.topology}
      projectId={projectId}
      detailNodes={selected.detailByID}
      workers={[...scene.workers, ...proposed.reviewers.filter((worker) => !selectedChange || worker.review?.proposalId === selectedChange)]}
      connected={connected}
      tasks={scene.tasks}
      peerQuestions={peerQuestions}
      omittedLocations={scene.omittedLocations}
      onSelectEntity={setSelectedEntity}
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
    {entity === undefined ? null : <section aria-label="Source contents">
      <p>Stable source reference <code>{entity.id}</code></p>
      <SourceNotices key={`${entity.id}:${topologies?.get(entity.project?.id ?? "")?.digest}`} entity={entity} topology={topologies?.get(entity.project?.id ?? "")} call={connected ? onProjectContent : undefined} open={onOpenBoard} />
      <SourceContents key={`${entity.id}:${topologies?.get(entity.project?.id ?? "")?.digest}`} node={entity} topology={topologies?.get(entity.project?.id ?? "")} call={connected ? onProjectContent : undefined} />
      {related.map((proposal) => <button key={proposal.id} type="button" onClick={() => onSelectChange?.(proposal.id)}>Inspect change: {proposal.title}</button>)}
    </section>}
  </div>;
}


const sourceForNode = (node: SceneNode, topology?: TopologyView) => [...topology?.sources ?? []].filter((source) => source.prefix === "" || node.path === source.prefix || node.path.startsWith(`${source.prefix}/`)).sort((a, b) => b.prefix.length - a.prefix.length)[0];

function SourceNotices({ entity, topology, call, open }: { entity: SceneNode; topology?: TopologyView; call?: ProjectContentCall; open?: (project?: string, entity?: string, id?: string, repository?: string) => void }) {
  const repository = sourceForNode(entity, topology)?.repository_id;
  const [items, setItems] = useState<Readonly<Record<string, unknown>>[]>([]), [notice, setNotice] = useState(""), [next, setNext] = useState(0), [pending, setPending] = useState(false);
  const epoch = useRef(0);
  useEffect(() => () => { epoch.current++; }, []);
  const read = async (offset = 0) => {
    if (!call || !entity.project) return;
    const generation = epoch.current; setPending(true);
    try { const result = await call("search", { project_id: entity.project.id, repository_id: repository ?? "", entity: entity.id, kind: "discussion", open_only: true, offset, limit: 4 }); if (generation !== epoch.current) return; setItems(Array.isArray(result.items) ? result.items as Readonly<Record<string, unknown>>[] : []); setNext(Number(result.next_offset ?? 0)); setNotice("Notices are retained project threads. Open a thread for its evidence."); }
    catch { if (generation === epoch.current) setNotice("Notices unavailable."); }
    finally { if (generation === epoch.current) setPending(false); }
  };
  return <section aria-label="Source notices"><button type="button" disabled={!call || pending} onClick={() => void read()}>Read notices for this source</button><button type="button" disabled={!open} onClick={() => open?.(entity.project?.id, entity.id, undefined, repository)}>Discuss this source</button>{items.filter((item) => !knowledgeMetadata(item.source_references).resolved).map((item) => <button type="button" key={String(item.id)} onClick={() => open?.(entity.project?.id, entity.id, String(item.id), repository)}>{String(item.title)} · unresolved · r{String(item.revision)}{item.projected_status === "needs_revalidation" ? " · needs revalidation" : ""}</button>)}{next ? <button type="button" disabled={pending} onClick={() => void read(next)}>More source notices</button> : null}{notice ? <p role="status">{notice}</p> : null}</section>;
}

function SourceContents({ node, topology, call }: { node: SceneNode; topology?: TopologyView; call?: ProjectContentCall }) {
  const [files, setFiles] = useState<Readonly<Record<string, unknown>>[]>([]), [next, setNext] = useState(0), [loaded, setLoaded] = useState(false), [pending, setPending] = useState(false), [notice, setNotice] = useState("");
  const epoch = useRef(0);
  useEffect(() => () => { epoch.current++; }, []);
  useEffect(() => { epoch.current++; setPending(false); }, [call]);
  const read = async () => {
    if (!call || !node.project || pending) return;
    const generation = ++epoch.current;
    setPending(true); setNotice("");
    try {
      const result = await call("source_files", { project_id: node.project.id, id: node.id.slice(node.project.id.length + 1), tested_source: sourceForNode(node, topology)?.revision ?? topology?.sourceRevision ?? "", offset: loaded ? next : 0, limit: 32 });
      if (generation !== epoch.current) return;
      if (result.unavailable) { setNotice(String(result.unavailable)); return; }
      setFiles((old) => [...old, ...(Array.isArray(result.files) ? result.files as Readonly<Record<string, unknown>>[] : [])]);
      setNext(Number(result.next_offset) || 0); setLoaded(true);
      setNotice(`${Number(result.total) || 0} directly owned files at ${String(result.revision || "unknown revision")}. Descendant files belong to their own entities.`);
    } catch { if (generation === epoch.current) setNotice("Source contents unavailable. Refresh the topology before retrying."); }
    finally { if (generation === epoch.current) setPending(false); }
  };
  return <>
    {loaded && next === 0 ? null : <button type="button" disabled={!call || pending} onClick={() => void read()}>{pending ? "Reading source…" : loaded ? "More source files" : "Read exact source contents"}</button>}
    {notice ? <p role="status">{notice}</p> : null}
    {files.length === 0 ? null : <ul>{files.map((file) => <li key={String(file.path)}><code>{String(file.path)}</code> · {String(file.kind)} · {String(file.bytes)} bytes</li>)}</ul>}
  </>;
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
  const label = `${agent.name}: ${activity} · ${projectLabel(state, agent.project_id)}`;
  const cells = (
    <>
      <AgentSprite agent={agent} activity={agentActivity(agent, state)} />
      <span className="dfConsoleRow__title">{agent.name} · {projectLabel(state, agent.project_id)}</span>
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
