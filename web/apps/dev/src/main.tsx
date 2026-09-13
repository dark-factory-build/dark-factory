import { useState } from "react";
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
const fixtureMovementCrowdedRunPaths = new Map([...fixtureCrowdedRunPaths, ...fixtureMovementRunPaths]);
const fixtureChangedTopologies = new Map(fixtureTopologies).set(fixtureFloorState.projects.keys().next().value!, {
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
  const movement = fixture === "movement";
  const [moved, setMoved] = useState(false);
  const [changedTopology, setChangedTopology] = useState(false);
  const [view, setView] = useState<FactoryConsoleProps["view"]>("floor");
  const [detail, setDetail] = useState<NonNullable<FactoryConsoleProps["detail"]>>("needs-you");
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [selectedTaskId, setSelectedTaskId] = useState<string>();
  const [selectedAgent, setSelectedAgent] = useState<FactoryConsoleProps["selectedAgent"]>();
  const [selectedHumanRequest, setSelectedHumanRequest] = useState<FactoryConsoleProps["selectedHumanRequest"]>();
  return (
    <>
      <p className="devFixtureBanner" role="note">
        FIXTURE TOUR — sample data, no daemon. Actions that need the factory are inert here.
      </p>
      {!movement ? null : <p className="devFixtureBanner" role="note">
        <button type="button" onClick={() => setMoved((value) => !value)}>TOGGLE OBSERVED ROUTE</button>{" "}
        <button type="button" onClick={() => setChangedTopology((value) => !value)}>TOGGLE TOPOLOGY</button>
      </p>}
      <FactoryConsole
        selectedTaskId={selectedTaskId}
        onSelectTask={setSelectedTaskId}
        status="ready"
        state={crowded ? fixtureCrowdedState : fixtureFloorState}
        topologies={changedTopology ? fixtureChangedTopologies : fixtureTopologies}
        runPaths={moved ? crowded ? fixtureMovementCrowdedRunPaths : fixtureMovementRunPaths : crowded ? fixtureTourCrowdedRunPaths : fixtureTourRunPaths}
        view={view}
        onView={setView}
        detail={detail}
        onDetail={setDetail}
        settingsOpen={settingsOpen}
        onToggleSettings={() => setSettingsOpen((open) => !open)}
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
