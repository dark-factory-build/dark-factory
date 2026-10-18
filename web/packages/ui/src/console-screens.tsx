import { useMemo, type ReactNode } from "react";
import type { AgentItem, HumanRequestItem, OperationalGraphView, StateView, TaskItem } from "@dark-factory/client";
import {
  projectGraph,
  projectFloor,
  projectProposals,
  type RunPathSample,
} from "./console-view.js";
import { FactoryScene } from "./factory-scene/factory-scene.js";
import type { ProductionContraption } from "./production-view.js";
import type { ProjectContentCall } from "./factory-app-controller.js";

const NO_CHANGES: readonly ProductionContraption[] = [];

/** The operational floor: observation and navigation; every action opens an existing control. */
export function FactoryFloor({
  changes = NO_CHANGES, state, graphs, graphErrors, runPaths, lastRunPaths, selectedAgentId, onSelectAgent,
  onSelectHumanRequest, connected = true, tools,
}: {
  tools?: ReactNode;
  changes?: readonly ProductionContraption[];
  state: StateView | undefined;
  graphs: ReadonlyMap<string, OperationalGraphView> | undefined;
  graphErrors?: ReadonlyMap<string, string>;
  runPaths?: ReadonlyMap<string, RunPathSample>;
  lastRunPaths?: ReadonlyMap<string, RunPathSample>;
  selectedAgentId?: string;
  onSelectAgent?: (agent: AgentItem) => void;
  onSelectHumanRequest?: (request: HumanRequestItem) => void;
  connected?: boolean;
  onProjectContent?: ProjectContentCall;
}) {
  const projects = useMemo(() => [...state?.projects.values() ?? []].map(({ id }) => id).sort(), [JSON.stringify([...state?.projects.keys() ?? []].sort())]);
  const prepared = useMemo(() => projectGraph(graphs, projects), [graphs, projects]);
  const scene = useMemo(() => projectFloor(state, prepared, runPaths, lastRunPaths), [state, prepared, runPaths, lastRunPaths]);
  const proposed = useMemo(() => projectProposals(prepared, changes), [prepared, changes]);
  const peerQuestions = useMemo(() => [...(state?.peerQuestions?.values() ?? [])], [state]);
  const unavailable = projects.flatMap((id) => graphs?.get(id)?.sources.filter((source) => source.kind === "unavailable") ?? []);
  const refused = projects.flatMap((id) => { const code = graphErrors?.get(id); return code === undefined ? [] : [`${state?.projects.get(id)?.name ?? id} · ${code}`]; });
  return <div className="dfFactoryFloor">
    <FactoryScene
      tools={<>
        {tools}
        {refused.length > 0 ? <p className="dfFactoryEntityTools__notice" role="status">Plant unavailable: {refused.join(", ")}; retrying.</p> : null}
        {unavailable.length > 0 ? <p className="dfFactoryEntityTools__notice" role="status">Source unavailable for {unavailable.map((source) => source.name).join(", ")}; those units cannot be inferred.</p> : null}
      </>}
      selectedWorkerId={selectedAgentId}
      graph={scene.graph}
      workers={[...scene.workers, ...proposed.reviewers]}
      connected={connected}
      unavailable={refused.length === 0 ? undefined : refused.join(", ")}
      reading={graphs === undefined || projects.some((id) => !graphs.has(id) && !graphErrors?.has(id))}
      tasks={scene.tasks}
      peerQuestions={peerQuestions}
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
