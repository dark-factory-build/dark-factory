"use client";

import { useEffect, useRef, useState, type ReactNode } from "react";
import { FactoryAppController, type FactoryAppSnapshot, type FactoryTerminalView } from "./factory-app-controller.js";
import { FactoryConsole } from "./factory-console.js";
import { XtermTerminal } from "./xterm-terminal.js";

const INITIAL_SNAPSHOT: FactoryAppSnapshot = { status: "idle" };

/** Complete browser application lifecycle; hosts only render this component. */
export function FactoryApp() {
  const [snapshot, setSnapshot] = useState<FactoryAppSnapshot>(INITIAL_SNAPSHOT);
  const owner = useRef<FactoryAppController | undefined>(undefined);

  useEffect(() => {
    const controller = new FactoryAppController({
      origin: window.location.origin,
      location: window.location,
      history: window.history,
      onChange: setSnapshot,
    });
    owner.current = controller;
    controller.start();
    return () => {
      if (owner.current === controller) owner.current = undefined;
      controller.close();
    };
  }, []);

  const controller = owner.current;
  const terminal = controller === undefined || snapshot.selectedAgent === undefined || snapshot.terminal === undefined ? undefined : (
    <TerminalPanel terminal={snapshot.terminal} onClose={() => controller.clearAgentTerminal()}>
      <TerminalHost key={`${snapshot.selectedAgent.id}:${snapshot.terminal.surfaceVersion}`} controller={controller} surfaceVersion={snapshot.terminal.surfaceVersion} />
    </TerminalPanel>
  );

  return (
    <FactoryConsole
      {...snapshot}
      onSelectAgent={(agent) => { void owner.current?.selectAgent(agent); }}
      onOpenTerminalForHumanRequest={(request) => owner.current?.openTerminalForHumanRequest(request)}
      onSelectHumanRequest={(request) => { void owner.current?.selectHumanRequest(request); }}
      onHumanReplyChange={(reply) => owner.current?.setHumanReply(reply)}
      onReplyHumanRequest={() => { void owner.current?.replyHumanRequest(); }}
      onCancelHumanRequest={() => { void owner.current?.cancelHumanRequest(); }}
      onCloseHumanRequest={() => owner.current?.clearHumanRequest()}
      terminalContent={terminal}
    />
  );
}

const TERMINAL_PHASE_LABELS: Record<FactoryTerminalView["phase"], string> = {
  idle: "WAITING FOR DISPLAY",
  resolving: "RESOLVING TERMINAL",
  attaching: "ATTACHING",
  acquiring: "ACQUIRING INPUT",
  ready: "READY",
  closing: "CLOSING",
  closed: "CLOSED",
};

function TerminalPanel({ terminal, onClose, children }: { terminal: FactoryTerminalView; onClose: () => void; children: ReactNode }) {
  return (
    <section className="dfFactoryConsole__terminalPanel" aria-label="AGENT TERMINAL">
      <div className="dfFactoryConsole__sectionHeading">
        <h2>AGENT TERMINAL</h2>
        <span>{TERMINAL_PHASE_LABELS[terminal.phase]}</span>
      </div>
      <p className="dfFactoryConsole__terminalAgent">{terminal.agentName} · {terminal.writable ? "INPUT ENABLED" : "READ ONLY"}</p>
      {terminal.error === undefined ? null : <p className="dfFactoryConsole__terminalError" role="alert">TERMINAL UNAVAILABLE</p>}
      {children}
      <button type="button" onClick={onClose}>CLOSE TERMINAL</button>
    </section>
  );
}

function TerminalHost({ controller, surfaceVersion }: { controller: FactoryAppController; surfaceVersion: number }) {
  const token = useRef<object>({});
  useEffect(() => {
    controller.beginTerminalSurface(token.current, surfaceVersion);
  }, [controller, surfaceVersion]);
  return (
    <XtermTerminal
      onSurface={(surface) => {
        if (surface === undefined) controller.endTerminalSurface(token.current, surfaceVersion);
        else {
          controller.beginTerminalSurface(token.current, surfaceVersion);
          controller.setTerminalSurface(token.current, surface, surfaceVersion);
        }
      }}
      onError={() => controller.terminalError(token.current, surfaceVersion)}
      onData={(value) => controller.sendTerminalText(token.current, value, surfaceVersion)}
      onBinary={(value) => controller.sendTerminalBinary(token.current, value, surfaceVersion)}
      onResize={(rows, cols) => controller.resizeTerminal(token.current, rows, cols, surfaceVersion)}
    />
  );
}
