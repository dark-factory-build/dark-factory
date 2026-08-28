import { useState } from "react";
import { createRoot } from "react-dom/client";
import { FactoryApp, FactoryConsole, type FactoryConsoleProps } from "@dark-factory/ui";
import "@dark-factory/ui/styles.css";
import "./styles.css";
import { fixtureConsoleExtras } from "../../../fixtures/console.mjs";
import { fixtureState } from "../../../fixtures/state.mjs";

// Fixture tour: sample data, no daemon, no authority. Reply/cancel handlers
// are deliberately absent so one-shot actions cannot pretend to succeed.
function FixtureTour() {
  const [screen, setScreen] = useState<FactoryConsoleProps["screen"]>({ kind: "home" });
  const [selectedAgent, setSelectedAgent] = useState<FactoryConsoleProps["selectedAgent"]>();
  const [selectedHumanRequest, setSelectedHumanRequest] = useState<FactoryConsoleProps["selectedHumanRequest"]>();
  return (
    <>
      <p className="devFixtureBanner" role="note">
        FIXTURE TOUR — sample data, no daemon. Actions that need the factory are inert here.
      </p>
      <FactoryConsole
        status="ready"
        state={fixtureState}
        extras={fixtureConsoleExtras}
        screen={screen}
        selectedAgent={selectedAgent}
        selectedHumanRequest={selectedHumanRequest}
        onNavigate={setScreen}
        onSelectAgent={(agent) => setSelectedAgent({ id: agent.id, name: agent.name, revision: agent.revision })}
        onSelectHumanRequest={(request) =>
          setSelectedHumanRequest({
            request,
            phase: "ready",
            question: "Should the migration also cover the users table? The plan only names accounts.",
            canReply: false,
            canCancel: false,
            replyMaxBytes: request.reply_max_bytes,
            reply: "",
          })
        }
        onCloseHumanRequest={() => setSelectedHumanRequest(undefined)}
        terminalContent={
          selectedAgent === undefined ? undefined : (
            <pre className="devFixtureTerminal">
              fixture tour — the terminal attaches only to a live daemon
            </pre>
          )
        }
      />
    </>
  );
}

const root = createRoot(document.getElementById("root")!);
if (new URLSearchParams(window.location.search).has("fixture")) {
  root.render(<FixtureTour />);
} else {
  root.render(<FactoryApp extras={fixtureConsoleExtras} />);
}
