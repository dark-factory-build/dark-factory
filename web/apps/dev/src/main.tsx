import { useState } from "react";
import { createRoot } from "react-dom/client";
import { SessionError, type ProtocolError, type SessionStatus } from "@dark-factory/client";
import { FactoryConsole } from "@dark-factory/ui";
import { fixtureState } from "../../../fixtures/state.mjs";
import "@dark-factory/ui/styles.css";
import "./styles.css";

function DevApp() {
  const [status, setStatus] = useState<SessionStatus>("ready");
  const [error, setError] = useState<SessionError | ProtocolError>();

  const retry = () => {
    setError(undefined);
    setStatus("ready");
  };

  return (
    <>
      <p className="dfDevFixture__label">STATIC CONTRIBUTOR FIXTURE · NO DAEMON</p>
      <FactoryConsole status={status} state={fixtureState} error={error} onRetry={retry} />
      <button className="dfDevFixture__simulate" type="button" onClick={() => { setStatus("closed"); setError(new SessionError("connection", true)); }}>
        Simulate connection error
      </button>
    </>
  );
}

createRoot(document.getElementById("root")!).render(<DevApp />);
