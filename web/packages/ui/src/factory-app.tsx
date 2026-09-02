"use client";

import { useEffect, useRef, useState, type ReactNode } from "react";
import { FactoryAppController, type FactoryAppSnapshot, type FactoryAppStatus, type FactoryTerminalView } from "./factory-app-controller.js";
import { FactoryConsole } from "./factory-console.js";
import { XtermTerminal } from "./xterm-terminal.js";
import type { ConsoleScreen } from "./console-view.js";

const INITIAL_SNAPSHOT: FactoryAppSnapshot = { status: "idle" };

export type FactoryAppProps = {
  /** Receives the finite connection lifecycle exposed by the owned controller. */
  onStatusChange?: (status: FactoryAppStatus) => void;
};

/** Complete browser application lifecycle; hosts only render this component. */
export function FactoryApp({ onStatusChange }: FactoryAppProps = {}) {
  const [snapshot, setSnapshot] = useState<FactoryAppSnapshot>(INITIAL_SNAPSHOT);
  const [screen, setScreen] = useState<ConsoleScreen>({ kind: "home" });
  const owner = useRef<FactoryAppController | undefined>(undefined);
  const statusChange = useRef(onStatusChange);
  statusChange.current = onStatusChange;

  useEffect(() => {
    const controller = new FactoryAppController({
      origin: window.location.origin,
      location: window.location,
      history: window.history,
      onChange: setSnapshot,
      onStatusChange: (status) => statusChange.current?.(status),
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
    <TerminalPanel
      terminal={snapshot.terminal}
      onClose={() => controller.clearAgentTerminal()}
    >
      <TerminalHost key={`${snapshot.selectedAgent.id}:${snapshot.terminal.surfaceVersion}`} controller={controller} surfaceVersion={snapshot.terminal.surfaceVersion} />
    </TerminalPanel>
  );

  return (
    <FactoryConsole
      {...snapshot}
      screen={screen}
      onNavigate={setScreen}
      onSelectAgent={(agent) => { void owner.current?.selectAgent(agent); }}
      onOpenTerminalForHumanRequest={(request) => { owner.current?.openTerminalForHumanRequest(request); }}
      onSelectHumanRequest={(request) => { void owner.current?.selectHumanRequest(request); }}
      onHumanReplyChange={(reply) => owner.current?.setHumanReply(reply)}
      onReplyHumanRequest={() => { void owner.current?.replyHumanRequest(); }}
      onCancelHumanRequest={() => { void owner.current?.cancelHumanRequest(); }}
      onCloseHumanRequest={() => owner.current?.clearHumanRequest()}
      terminalContent={terminal}
    />
  );
}

/** The selected agent's terminal is ordinary console content, not a mode. */
export function TerminalPanel({
  terminal,
  onClose,
  children,
}: {
  terminal: FactoryTerminalView;
  onClose: () => void;
  children: ReactNode;
}) {
  return (
    <section className="dfFactoryConsole__terminalPanel" aria-label={`Terminal for ${terminal.agentName}`}>
      <div className="dfFactoryConsole__terminalHeading">
        <p className="dfFactoryConsole__terminalAgent">
          {terminal.agentName}{terminal.taskTitle === undefined ? "" : ` · ${terminal.taskTitle}`}
        </p>
        <button
          type="button"
          disabled={terminal.phase === "closing" || terminal.phase === "closed"}
          onClick={onClose}
          title="close this terminal view; the worker keeps running"
        >
          CLOSE TERMINAL
        </button>
      </div>
      {terminal.error === undefined ? null : (
        <p className="dfFactoryConsole__terminalError" role="alert">
          {terminal.phase === "ready" && !terminal.writable ? "TERMINAL OPEN ELSEWHERE" : "TERMINAL UNAVAILABLE"}
        </p>
      )}
      {!terminal.resets ? null : (
        <p className="dfFactoryConsole__terminalReset" role="status">
          Earlier output is no longer retained; showing what the factory still holds.
        </p>
      )}
      {children}
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
