import { useMemo, useState, type ReactNode } from "react";
import type { AgentItem } from "@dark-factory/client";
import { type FactoryAgentSelection, type FactoryAppSnapshot, type FactoryHumanRequestView } from "./factory-app-controller.js";
import { FactoryFloor } from "./console-screens.js";
import { AgentPanel, HumanRequestPanel, WorkPanel } from "./console-sidebar.js";
import type { ProjectContentCall } from "./factory-app-controller.js";
import { useProduction } from "./production-data.js";
import { deriveProductionView, inProgressProduction } from "./production-view.js";
import { workRows } from "./console-view.js";

export type FactoryConsoleProps = FactoryAppSnapshot & {
  selectedAgent?: FactoryAgentSelection;
  onSelectAgent?: (agent: AgentItem) => void;
  onProjectContent?: ProjectContentCall;
  onSelectHumanRequest?: (request: FactoryHumanRequestView["request"]) => void;
  onHumanReplyChange?: (reply: string) => void;
  onReplyHumanRequest?: () => void;
  onCancelHumanRequest?: () => void;
  onCloseHumanRequest?: () => void;
  /** The selected agent's mounted terminal and durable composer. */
  terminalContent?: ReactNode;
};

const STATUS_LABELS: Record<FactoryAppSnapshot["status"], string> = {
  idle: "Idle",
  connecting: "Connecting",
  authenticating: "Authenticating",
  syncing: "Syncing",
  ready: "Ready",
  closed: "Closed",
};

const ERROR_LABELS = new Map<string, string>([
  ["connection", "Connection unavailable."],
  ["closed", "Connection closed."],
  ["pairing_required", "Pair this browser client before connecting."],
  ["pairing_uncertain", "Pairing result is uncertain. Pair this browser client again."],
  ["storage_unavailable", "Browser key storage is unavailable."],
  ["crypto_unavailable", "Browser cryptography is unavailable."],
  ["malformed", "The server sent an invalid frame."],
  ["oversized", "The server frame exceeded the protocol limit."],
  ["wrong_direction", "The server sent an invalid frame direction."],
  ["unauthorized", "This browser client is not authorized."],
  ["invalid_request", "The request was rejected."],
  ["rate_limited", "The request was rate limited."],
  ["not_found", "The requested item was not found."],
  ["stale", "The requested state is stale."],
  ["too_large", "The request was too large."],
  ["internal", "The server could not complete the request."],
  ["unsupported", "The factory does not support this request yet."],
]);

/** One screen: a factory or agent list beside one operator detail panel. */
export function FactoryConsole({
  status,
  state,
  error,
  graphs,
  graphErrors,
  runPaths,
  lastRunPaths,
  selectedHumanRequest,
  selectedAgent,
  onSelectAgent,
  onProjectContent,
  onSelectHumanRequest,
  onHumanReplyChange,
  onReplyHumanRequest,
  onCancelHumanRequest,
  onCloseHumanRequest,
  terminalContent,
}: FactoryConsoleProps) {
  const [chosenProjectId, setProjectId] = useState<string>();
  const projectId = chosenProjectId !== undefined && state?.projects.has(chosenProjectId) ? chosenProjectId : state?.projects.size === 1 ? [...state.projects.keys()][0] : undefined;
  const pickRequest = onSelectHumanRequest === undefined ? undefined : (request: Parameters<NonNullable<typeof onSelectHumanRequest>>[0]) => { setProjectId(request.project_id); onSelectHumanRequest(request); };
  const scopedState = useMemo(() => state === undefined || projectId === undefined ? state : {
    ...state,
    projects: new Map([...state.projects].filter(([id]) => id === projectId)),
    agents: new Map([...state.agents].filter(([, agent]) => agent.project_id === projectId)),
    tasks: new Map([...state.tasks].filter(([, task]) => task.project_id === projectId)),
    humanRequests: new Map([...state.humanRequests].filter(([, request]) => request.project_id === projectId)),
  }, [state, projectId]);
  const ready = status === "ready";
  const productionData = useProduction([...(scopedState?.projects.keys() ?? [])].sort(), ready ? onProjectContent : undefined);
  const productionView = useMemo(() => deriveProductionView(productionData.records, Date.now()), [productionData.records]);
  const productionItems = useMemo(() => Object.values(productionView.contraptions).sort((a, b) => Number(a.completed) - Number(b.completed) || (a.completed ? a.completedAt - b.completedAt : 0) || a.visualId.localeCompare(b.visualId)), [productionView]);
  const inProgressItems = useMemo(() => productionItems.filter(inProgressProduction), [productionItems]);
  const rows = useMemo(() => workRows(scopedState, inProgressItems), [scopedState, inProgressItems]);
  const agent = selectedAgent === undefined ? undefined : scopedState?.agents.get(selectedAgent.id);
  const liveAgent = agent?.archived === true ? undefined : agent;

  return (
    <div className="dfConsoleShell">
      <main className="dfFactoryConsole" aria-label="Factory operator console">
        {error === undefined ? null : (
          <p className="dfFactoryConsole__error" role="alert">
            {ERROR_LABELS.get(error.code) ?? "The connection could not continue."}
          </p>
        )}

        <div className="dfConsoleLayout">
          <section className="dfConsoleLayout__left dfFactoryConsole__section" aria-label="Factory floor">
            <FactoryFloor tools={<>
              {state?.projects.size === 1 ? null : <label>Project <select aria-label="Project" value={projectId ?? ""} onChange={(event) => setProjectId(event.currentTarget.value || undefined)}>
                <option value="">All projects</option>
                {[...state?.projects.values() ?? []].sort((a, b) => a.name.localeCompare(b.name) || a.id.localeCompare(b.id)).map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}
              </select></label>}
              {ready || error !== undefined ? null : <p className="dfFactoryConsole__status" role="status" aria-live="polite" aria-atomic="true" aria-label={`Connection status: ${STATUS_LABELS[status]}`}>
                <span className={`dfFactoryConsole__statusDot dfFactoryConsole__statusDot--${status}`} aria-hidden="true" />
                {STATUS_LABELS[status]}
              </p>}
            </>} changes={productionItems} onProjectContent={onProjectContent} selectedAgentId={liveAgent?.id} state={scopedState} graphs={graphs} graphErrors={graphErrors} runPaths={runPaths} lastRunPaths={lastRunPaths} onSelectAgent={ready ? onSelectAgent : undefined} onSelectHumanRequest={ready ? pickRequest : undefined} connected={ready} />
          </section>

          {/* Work (decisions first) always shows; the selected agent's terminal joins it below. */}
          <aside className="dfConsoleSidebar" aria-label="Selected detail">
            <WorkPanel
              state={scopedState}
              rows={rows}
              projectId={projectId}
              ready={ready}
              selectedHumanRequest={selectedHumanRequest}
              onSelectHumanRequest={pickRequest}
              onCloseHumanRequest={onCloseHumanRequest}
              requestContent={selectedHumanRequest === undefined ? null : <HumanRequestPanel
                selected={selectedHumanRequest}
                onReplyChange={onHumanReplyChange}
                onReply={onReplyHumanRequest}
                onCancel={onCancelHumanRequest}
              />}
            />
            {liveAgent === undefined ? null : <AgentPanel
              key={liveAgent.id}
              agent={liveAgent}
              state={scopedState}
              terminalContent={terminalContent}
            />}
          </aside>
        </div>
      </main>
    </div>
  );
}
