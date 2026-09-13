import { useState } from "react";
import { SessionError } from "@dark-factory/client";
import { TerminalPanel } from "../../../packages/ui/dist/src/factory-app.js";
import { createRoot } from "react-dom/client";
import { FactoryApp, FactoryConsole, type FactoryConsoleProps } from "@dark-factory/ui";
import "@dark-factory/ui/styles.css";
import "./styles.css";
import { fixtureCrowdedRunPaths, fixtureCrowdedState, fixtureFloorState, fixtureRunPaths, fixtureTopologies } from "../../../fixtures/state.mjs";

// Fixture tour: sample data, no daemon, no authority. Reply/cancel and edit
// handlers are deliberately absent so one-shot actions cannot pretend to
// succeed.
function FixtureTour() {
  const fixture = new URLSearchParams(window.location.search).get("fixture");
  const crowded = fixture === "crowded";
  const terminalFixture = fixture === "terminal";
  const fixtureAgent = fixtureFloorState.agents.values().next().value!;
  const [view, setView] = useState<FactoryConsoleProps["view"]>("floor");
  const [detail, setDetail] = useState<NonNullable<FactoryConsoleProps["detail"]>>(terminalFixture ? "agent" : "needs-you");
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [selectedAgent, setSelectedAgent] = useState<FactoryConsoleProps["selectedAgent"]>(terminalFixture ? { id: fixtureAgent.id, name: fixtureAgent.name, revision: fixtureAgent.revision } : undefined);
  const [selectedHumanRequest, setSelectedHumanRequest] = useState<FactoryConsoleProps["selectedHumanRequest"]>();
  return (
    <>
      <p className="devFixtureBanner" role="note">
        FIXTURE TOUR — sample data, no daemon. Actions that need the factory are inert here.
      </p>
      <FactoryConsole
        status="ready"
        state={crowded ? fixtureCrowdedState : fixtureFloorState}
        topologies={fixtureTopologies}
        runPaths={crowded ? fixtureCrowdedRunPaths : fixtureRunPaths}
        view={view}
        onView={setView}
        detail={detail}
        onDetail={setDetail}
        settingsOpen={settingsOpen}
        onToggleSettings={() => setSettingsOpen((open) => !open)}
        terminalContent={!terminalFixture ? undefined : <TerminalPanel terminal={{
          agentId: fixtureAgent.id, agentName: fixtureAgent.name, agentRevision: fixtureAgent.revision,
          taskTitle: "Sanitised terminal input refusal", phase: "ready", writable: false,
          error: new SessionError("connection"), errorSource: "input", hasOutputSurface: true,
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
