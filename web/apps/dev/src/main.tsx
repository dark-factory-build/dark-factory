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

// Fixture tour: sample data, no daemon, no authority. Reply/cancel and edit
// handlers are deliberately absent so one-shot actions cannot pretend to
// succeed.
function FixtureTour() {
  const fixture = new URLSearchParams(window.location.search).get("fixture");
  const crowded = fixture === "crowded";
  const terminalFixture = fixture === "terminal";
  const [inputRefused, setInputRefused] = useState(true);
  const fixtureAgent = fixtureFloorState.agents.values().next().value!;
  const movement = fixture === "movement";
  const hierarchy = fixture === "hierarchy" || fixture === "movement";
  const [routeStep, setRouteStep] = useState(0);
  const [returned, setReturned] = useState(false);
  const [connected, setConnected] = useState(true);
  const [changedTopology, setChangedTopology] = useState(false);
  const [view, setView] = useState<FactoryConsoleProps["view"]>("floor");
  const [detail, setDetail] = useState<NonNullable<FactoryConsoleProps["detail"]>>(terminalFixture ? "agent" : "needs-you");
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [selectedAgent, setSelectedAgent] = useState<FactoryConsoleProps["selectedAgent"]>(terminalFixture ? { id: fixtureAgent.id, name: fixtureAgent.name, revision: fixtureAgent.revision } : undefined);
  const [selectedTaskId, setSelectedTaskId] = useState<string>();
  const [selectedHumanRequest, setSelectedHumanRequest] = useState<FactoryConsoleProps["selectedHumanRequest"]>();
  return (
    <>
      <p className="devFixtureBanner" role="note">
        FIXTURE TOUR — sample data, no daemon. Actions that need the factory are inert here.
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
      <FactoryConsole
        selectedTaskId={selectedTaskId}
        onSelectTask={setSelectedTaskId}
        status={connected ? "ready" : "closed"}
        state={returned ? fixtureReturnedState : crowded ? fixtureCrowdedState : fixtureFloorState}
        topologies={changedTopology ? fixtureChangedTopologies : hierarchy ? fixtureHierarchyTopologies : fixtureTopologies}
        runPaths={returned ? fixtureRunPaths : crowded ? routeStep === 0 ? fixtureTourCrowdedRunPaths : fixtureMovementCrowdedRunPaths : fixtureRapidRunPaths[routeStep]!}
        view={view}
        onView={setView}
        detail={detail}
        onDetail={setDetail}
        settingsOpen={settingsOpen}
        onToggleSettings={() => setSettingsOpen((open) => !open)}
        terminalContent={!terminalFixture ? undefined : <TerminalPanel terminal={{
          agentId: fixtureAgent.id, agentName: fixtureAgent.name, agentRevision: fixtureAgent.revision,
          taskTitle: "Sanitised terminal input refusal", phase: "ready", writable: !inputRefused,
          error: inputRefused ? new SessionError("connection") : undefined, errorSource: inputRefused ? "input" : undefined, hasOutputSurface: true,
          paused: false, instructionPending: false, instructionDraft: "", historyPending: false,
          taskDetailPending: false, controlReady: false, queued: false, finishing: false,
          resets: 0, surfaceVersion: 0,
        }}><pre className="devFixtureTerminal">{"FIXTURE OUTPUT — no provider connected\nRead-only output remains visible after input lease refusal."}</pre></TerminalPanel>}
        selectedAgent={selectedAgent}
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
