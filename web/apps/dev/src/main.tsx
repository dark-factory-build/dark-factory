import actualTopology from "./actual-topology.js";
import { useState } from "react";
import { SessionError } from "@dark-factory/client";
import { TerminalPanel } from "../../../packages/ui/dist/src/factory-app.js";
import { createRoot } from "react-dom/client";
import { FactoryApp, FactoryConsole, type FactoryConsoleProps } from "@dark-factory/ui";
import "@dark-factory/ui/styles.css";
import "./styles.css";
import { fixtureCrowdedRunPaths, fixtureCrowdedState, fixtureFloorState, fixtureRunPaths, fixtureTopologies } from "../../../fixtures/state.mjs";

const [fixtureAgentId, fixtureObservedRun] = fixtureRunPaths.entries().next().value!;
const fixtureTourRunPaths = new Map(fixtureRunPaths).set(fixtureAgentId, {
  ...fixtureObservedRun,
  paths: [...fixtureObservedRun.paths, "web/apps/dev/src/main.tsx"],
});
const fixtureTourCrowdedRunPaths = new Map([...fixtureCrowdedRunPaths, ...fixtureTourRunPaths]);
const fixtureMovementRunPaths = new Map(fixtureRunPaths).set(fixtureAgentId, {
  ...fixtureObservedRun,
  paths: ["web/apps/dev/src/main.tsx"],
});
const fixtureRapidRunPaths = [fixtureRunPaths, fixtureMovementRunPaths,
  new Map(fixtureRunPaths).set(fixtureAgentId, { ...fixtureObservedRun, paths: ["internal/kernel"] }),
  new Map(fixtureRunPaths).set(fixtureAgentId, { ...fixtureObservedRun, paths: ["README.md"] }),
];
const fixtureMovementCrowdedRunPaths = new Map([...fixtureCrowdedRunPaths].map(([id, run]) => [id, {
  ...run,
  paths: ["web/apps/dev/src/main.tsx"],
}]));
const fixtureReturnedState = {
  ...fixtureFloorState,
  factory: { ...fixtureFloorState.factory, active_runs: 1 },
  tasks: new Map(fixtureFloorState.tasks).set(fixtureObservedRun.taskId, { ...fixtureFloorState.tasks.get(fixtureObservedRun.taskId)!, status: "succeeded" }),
};
const fixtureArchiveAgent = {
  ...fixtureFloorState.agents.values().next().value!,
  id: "24".repeat(16),
  name: "Idle archive worker",
  paused: false,
  revision: 20n,
};
const fixtureArchiveTask = {
  ...fixtureFloorState.tasks.values().next().value!,
  id: "36".repeat(16),
  assigned_agent_id: fixtureArchiveAgent.id,
  title: "Completed archive fixture work",
  status: "succeeded" as const,
  revision: 21n,
};
const archiveFixtureState = (archived: boolean) => ({
  ...fixtureFloorState,
  agents: new Map(fixtureFloorState.agents).set(fixtureArchiveAgent.id, { ...fixtureArchiveAgent, archived, paused: archived }),
  tasks: new Map(fixtureFloorState.tasks).set(fixtureArchiveTask.id, fixtureArchiveTask),
});
const fixtureHierarchyTopologies = new Map(fixtureTopologies).set([...fixtureFloorState.projects.keys()][1]!, {
  projectId: [...fixtureFloorState.projects.keys()][1]!, digest: "hierarchy-fixture", sourceRevision: "",
  nodes: [
    { id: "f6".repeat(32), parent_id: "", kind: "repository", path: ".", label: "unrelated root label", language: "", size_bucket: "medium" },
    { id: "g7".repeat(32), parent_id: "f6".repeat(32), kind: "directory", path: "does/not/describe/containment", label: "same label", language: "", size_bucket: "small" },
    { id: "h8".repeat(32), parent_id: "g7".repeat(32), kind: "package", path: "also-flat", label: "same label", language: "", size_bucket: "tiny" },
  ],
});
const fixtureChangedTopologies = new Map(fixtureHierarchyTopologies).set(fixtureFloorState.projects.keys().next().value!, {
  ...fixtureTopologies.values().next().value!,
  digest: "movement-topology",
  nodes: [...fixtureTopologies.values().next().value!.nodes, { id: "e5".repeat(32), parent_id: "a1".repeat(32), kind: "directory", path: "docs", label: "docs", language: "markdown", size_bucket: "tiny" }],
});

const fixturePagedTopologies = new Map(fixtureHierarchyTopologies).set(fixtureObservedRun.projectId, {
  ...fixtureTopologies.get(fixtureObservedRun.projectId)!, digest: "paging-fixture",
  nodes: [...fixtureTopologies.get(fixtureObservedRun.projectId)!.nodes, ...Array.from({ length: 30 }, (_, index) => ({
    id: index.toString(16).padStart(64, "0"), parent_id: "a1".repeat(32), kind: "directory", path: `component-${index}`, label: `Component ${index}`, language: "", size_bucket: "tiny",
  }))],
});

const inventoryCounts = (counts: Partial<Record<"source" | "tests" | "documentation" | "configuration" | "assets" | "unclassified", number>>) => ({ source: 0, tests: 0, documentation: 0, configuration: 0, assets: 0, unclassified: 0, ...counts });
const inventory = (counts: ReturnType<typeof inventoryCounts>, names: string[] = []) => ({ direct: counts, total: counts, samples: names, samples_omitted: Math.max(0, Object.values(counts).reduce((sum, n) => sum + n, 0) - names.length) });
const fixtureInventoryTopologies = new Map(fixtureHierarchyTopologies).set(fixtureObservedRun.projectId, {
  ...fixtureTopologies.get(fixtureObservedRun.projectId)!, digest: "inventory-fixture",
  nodes: [
    { id: "a1".repeat(32), parent_id: "", kind: "repository", path: ".", label: "Workshop", language: "", size_bucket: "large", inventory: { ...inventory(inventoryCounts({ configuration: 3 }), ["package.json", "go.mod", "go.sum"]), total: inventoryCounts({ source: 206, tests: 200, documentation: 36, configuration: 13, assets: 98, unclassified: 4 }) } },
    ...[
      ["b2", "internal/kernel", "Source engine", "large", inventoryCounts({ source: 200, tests: 60, configuration: 4 }), ["store.go", "api.go", "run.go"]],
      ["c3", "tests", "Test workshop", "large", inventoryCounts({ tests: 140, source: 4 }), ["session.test.ts", "store_test.go", "test_queue.py"]],
      ["d4", "web", "Design studio", "large", inventoryCounts({ assets: 98, documentation: 36, configuration: 6 }), ["scene.svg", "README.md", "theme.json"]],
      ["e5", "small", "Small component", "tiny", inventoryCounts({ source: 2, unclassified: 4 }), ["main.ts", "notes.bin", "data"]],
      ["f6", "empty", "Empty component", "empty", inventoryCounts({}), []],
    ].map(([id, path, label, size, counts, names]) => ({ id: String(id).repeat(32), parent_id: "a1".repeat(32), kind: "directory", path, label, language: "", size_bucket: size, inventory: inventory(counts as ReturnType<typeof inventoryCounts>, names as string[]) })),
    { id: "07".repeat(32), parent_id: "a1".repeat(32), kind: "directory", path: "unavailable", label: "Unavailable inventory", language: "", size_bucket: "small" },
  ],
});
const fixtureInventoryState = { ...fixtureFloorState, humanRequests: new Map() };

// Fixture tour: sample data, no daemon, no authority. Fixture-only state
// toggles expose production components; no action reports a daemon result.
function FixtureTour() {
  const fixture = new URLSearchParams(window.location.search).get("fixture");
  const crowded = fixture === "crowded" || fixture === "inventory-crowded";
  const actualNeedsYou = fixture === "actual-needs-you";
  const actual = fixture === "actual" || fixture === "actual-idle" || actualNeedsYou;
  const inventoryFixture = actual || fixture === "inventory" || fixture === "inventory-idle" || fixture === "inventory-crowded";
  const terminalFixture = fixture === "terminal";
  const archiveFixture = fixture === "archive" || fixture === "archived";
  const [inputRefused, setInputRefused] = useState(true);
  const fixtureAgent = fixtureFloorState.agents.values().next().value!;
  const [archivedWorker, setArchivedWorker] = useState(fixture === "archived");
  const archiveAgent = { ...fixtureArchiveAgent, archived: archivedWorker };
  const movement = fixture === "movement";
  const hierarchy = fixture === "hierarchy" || fixture === "movement";
  const [routeStep, setRouteStep] = useState(0);
  const [returned, setReturned] = useState(false);
  const [connected, setConnected] = useState(true);
  const [changedTopology, setChangedTopology] = useState(false);
  const [view, setView] = useState<FactoryConsoleProps["view"]>("floor");
  const [detail, setDetail] = useState<NonNullable<FactoryConsoleProps["detail"]>>(inventoryFixture ? "floor" : terminalFixture || archiveFixture ? "agent" : "needs-you");
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [selectedAgent, setSelectedAgent] = useState<FactoryConsoleProps["selectedAgent"]>(terminalFixture ? { id: fixtureAgent.id, name: fixtureAgent.name, revision: fixtureAgent.revision } : archiveFixture ? { id: archiveAgent.id, name: archiveAgent.name, revision: archiveAgent.revision } : undefined);
  const [selectedTaskId, setSelectedTaskId] = useState<string>();
  const [selectedHumanRequest, setSelectedHumanRequest] = useState<FactoryConsoleProps["selectedHumanRequest"]>();
  return (
    <>
      <p className="devFixtureBanner" role="note">
        {actual ? `ACTUAL CODEBASE SCAN · ${actualTopology.sourceRevision.slice(0, 8)} · sample workers, no daemon.` : "FIXTURE TOUR — sample data, no daemon. Actions that need the factory are inert here."}
      </p>
      {!hierarchy ? null : <p className="devFixtureBanner" role="note">
        HIERARCHY FIXTURE — North and South use different served nesting; labels and paths are deliberately misleading.
      </p>}
      {!movement ? null : <p className="devFixtureBanner" role="note">
        <button type="button" onClick={() => setRouteStep((step) => (step + 1) % fixtureRapidRunPaths.length)}>NEXT RAPID RETARGET</button>{" "}
        <button type="button" onClick={() => setReturned((value) => !value)}>TOGGLE COMMON-SPACE ROUND TRIP</button>{" "}
        <button type="button" onClick={() => setConnected((value) => !value)}>TOGGLE CONNECTION</button>{" "}
        <button type="button" onClick={() => setChangedTopology((value) => !value)}>TOGGLE TOPOLOGY</button>
      </p>}
      {!terminalFixture ? null : <p className="devFixtureBanner"><button type="button" onClick={() => setInputRefused((value) => !value)}>TOGGLE FIXTURE INPUT REFUSAL</button></p>}
      {!archiveFixture ? null : <p className="devFixtureBanner" role="note">
        ARCHIVE FIXTURE — explicit sample state only. <button type="button" onClick={() => setArchivedWorker(false)}>SHOW IDLE WORKER</button>{" "}
        <button type="button" onClick={() => setArchivedWorker(true)}>SHOW ARCHIVED WORKER</button>
      </p>}
      <FactoryConsole
        selectedTaskId={selectedTaskId}
        onSelectTask={setSelectedTaskId}
        status={connected ? "ready" : "closed"}
        state={actual ? { ...fixtureInventoryState, projects: new Map([[actualTopology.projectId, { ...fixtureInventoryState.projects.get(actualTopology.projectId)!, name: "dark-factory" }]]), ...(actualNeedsYou ? { humanRequests: fixtureFloorState.humanRequests } : {}), ...(fixture === "actual-idle" ? { tasks: new Map(), agents: new Map() } : {}) } : inventoryFixture ? fixture === "inventory-idle" ? { ...fixtureInventoryState, tasks: new Map(), agents: new Map() } : crowded ? { ...fixtureCrowdedState, humanRequests: new Map() } : fixtureInventoryState : archiveFixture ? archiveFixtureState(archivedWorker) : returned ? fixtureReturnedState : crowded ? fixtureCrowdedState : fixtureFloorState}
        topologies={actual ? new Map([[actualTopology.projectId, actualTopology]]) : inventoryFixture ? fixtureInventoryTopologies : fixture === "paging" ? fixturePagedTopologies : changedTopology ? fixtureChangedTopologies : hierarchy ? fixtureHierarchyTopologies : fixtureTopologies}
        runPaths={returned ? fixtureRunPaths : crowded ? routeStep === 0 ? fixtureTourCrowdedRunPaths : fixtureMovementCrowdedRunPaths : fixtureRapidRunPaths[routeStep]!}
        view={view}
        onView={setView}
        detail={detail}
        onDetail={setDetail}
        settingsOpen={settingsOpen}
        onToggleSettings={() => setSettingsOpen((open) => !open)}
        agentPanel={archiveFixture ? "config" : undefined}
        terminalContent={!terminalFixture ? undefined : <TerminalPanel terminal={{
          agentId: fixtureAgent.id, agentName: fixtureAgent.name, agentRevision: fixtureAgent.revision,
          taskTitle: "Sanitised terminal input refusal", phase: "ready", writable: !inputRefused,
          error: inputRefused ? new SessionError("connection") : undefined, errorSource: inputRefused ? "input" : undefined, hasOutputSurface: true,
          paused: false, instructionPending: false, instructionDraft: "", historyPending: false,
          taskDetailPending: false, controlReady: false, queued: false, finishing: false,
          resets: 0, surfaceVersion: 0,
        }}><pre className="devFixtureTerminal">{"FIXTURE OUTPUT — no provider connected\nRead-only output remains visible after input lease refusal."}</pre></TerminalPanel>}
        selectedAgent={selectedAgent}
        onSaveAgentConfig={archiveFixture ? () => {} : undefined}
        selectedHumanRequest={selectedHumanRequest}
        onSelectAgent={(agent) => {
          setDetail("agent");
          setSelectedHumanRequest(undefined);
          setSelectedAgent({ id: agent.id, name: agent.name, revision: agent.revision });
        }}
        onLoadTaskDetail={(task) => Promise.resolve({
          taskId: task.id,
          revision: task.revision,
          head: fixtureFloorState.head,
          instruction: "Inspect the current fixture projection and report any mismatch.",
          feedback: "Fixture-only review note; no daemon action was performed.",
          ...(task.status === "succeeded" ? { outcome: "Fixture inspection completed." } : {}),
          peerQuestions: [],
        })}
        onLoadTaskList={archiveFixture ? (agentId) => {
          const tasks = [fixtureArchiveTask].filter((task) => task.assigned_agent_id === agentId);
          return Promise.resolve({ agentId, head: fixtureFloorState.head, total: BigInt(tasks.length), tasks, hasMore: false });
        } : undefined}
        onSelectHumanRequest={(request) => {
          setDetail("needs-you");
          setSelectedHumanRequest({
            request,
            phase: "ready",
            question: "Should the migration also cover the users table? The plan only names accounts.",
            options: ["Keep the migration limited to accounts", "Include users too"],
            canReply: false,
            canCancel: false,
            replyMaxBytes: request.reply_max_bytes,
            reply: "",
          });
        }}
        onCloseHumanRequest={() => setSelectedHumanRequest(undefined)}
      />
    </>
  );
}

const root = createRoot(document.getElementById("root")!);
if (new URLSearchParams(window.location.search).has("fixture")) {
  root.render(<FixtureTour />);
} else {
  root.render(<FactoryApp />);
}
