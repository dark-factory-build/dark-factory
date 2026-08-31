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
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
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
  const sidebar = controller === undefined || snapshot.selectedAgent === undefined || snapshot.terminal === undefined ? undefined : (
    <TerminalSidebar
      terminal={snapshot.terminal}
      snapshot={snapshot}
      collapsed={sidebarCollapsed}
      onToggleCollapsed={() => setSidebarCollapsed((value) => !value)}
      onClose={() => controller.clearAgentTerminal()}
      onTakeControl={() => controller.takeTerminalControl()}
      onHandBack={() => controller.handBackTerminalControl()}
    >
      <TerminalHost key={`${snapshot.selectedAgent.id}:${snapshot.terminal.surfaceVersion}`} controller={controller} surfaceVersion={snapshot.terminal.surfaceVersion} />
    </TerminalSidebar>
  );

  return (
    <FactoryConsole
      {...snapshot}
      screen={screen}
      onNavigate={setScreen}
      onSelectAgent={(agent) => { setSidebarCollapsed(false); void owner.current?.selectAgent(agent); }}
      onOpenTerminalForHumanRequest={(request) => { setSidebarCollapsed(false); owner.current?.openTerminalForHumanRequest(request); }}
      onSelectHumanRequest={(request) => { void owner.current?.selectHumanRequest(request); }}
      onHumanReplyChange={(reply) => owner.current?.setHumanReply(reply)}
      onReplyHumanRequest={() => { void owner.current?.replyHumanRequest(); }}
      onCancelHumanRequest={() => { void owner.current?.cancelHumanRequest(); }}
      onCloseHumanRequest={() => owner.current?.clearHumanRequest()}
      terminalContent={sidebar}
    />
  );
}

const TERMINAL_PHASE_LABELS: Record<FactoryTerminalView["phase"], string> = {
  idle: "WAITING FOR DISPLAY",
  resolving: "RESOLVING TERMINAL",
  attaching: "ATTACHING",
  acquiring: "TAKING CONTROL",
  ready: "READY",
  closing: "CLOSING",
  closed: "CLOSED",
};

/**
 * The persistent terminal sidebar. Collapse is purely visual: the body is
 * clipped by CSS while the terminal, its session, and any held control
 * survive — unmounting on collapse would tear the surface down and forfeit
 * the control lease for a presentation gesture.
 */
export function TerminalSidebar({
  terminal,
  snapshot,
  collapsed,
  onToggleCollapsed,
  onClose,
  onTakeControl,
  onHandBack,
  children,
}: {
  terminal: FactoryTerminalView;
  snapshot: FactoryAppSnapshot;
  collapsed: boolean;
  onToggleCollapsed: () => void;
  onClose: () => void;
  onTakeControl: () => void;
  onHandBack: () => void;
  children: ReactNode;
}) {
  const busyLease = terminal.leaseOperation !== "none";
  return (
    <aside className={`dfConsoleSidebar${collapsed ? " dfConsoleSidebar--collapsed" : ""}`} aria-label={collapsed ? "Terminal (collapsed)" : "Terminal"}>
      {!collapsed ? null : (
        <button type="button" className="dfConsoleSidebar__tab" onClick={onToggleCollapsed} title={`open the terminal for ${terminal.agentName}`}>
          {terminal.agentName}
        </button>
      )}
      <div className="dfConsoleSidebar__body" aria-hidden={collapsed ? "true" : undefined}>
        <div className="dfConsoleSidebar__header">
          <strong>{terminal.agentName}</strong>
          <span className="dfConsoleSidebar__phase">{TERMINAL_PHASE_LABELS[terminal.phase]}</span>
          <span className={`dfConsoleSidebar__control${terminal.writable ? " dfConsoleSidebar__control--held" : ""}`}>
            {terminal.writable ? "you have control" : "watching"}
          </span>
        </div>
        <div className="dfConsoleSidebar__actions">
          {terminal.writable ? (
            <button type="button" disabled={busyLease} onClick={onHandBack}>hand back</button>
          ) : (
            <button type="button" disabled={busyLease || terminal.phase !== "ready"} onClick={onTakeControl}>take control</button>
          )}
          <button
            type="button"
            disabled={busyLease || terminal.writable || terminal.phase !== "ready"}
            title={terminal.writable ? "you have control — type in the terminal" : "takes control so you can type"}
            onClick={() => { if (!terminal.writable) onTakeControl(); }}
          >
            Steer
          </button>
          <button type="button" onClick={onToggleCollapsed} title="collapse the terminal">»</button>
          <button type="button" onClick={onClose} title="close the terminal">×</button>
        </div>
        {terminal.error === undefined ? null : <p className="dfFactoryConsole__terminalError" role="alert">TERMINAL UNAVAILABLE</p>}
        {!terminal.resets ? null : (
          <p className="dfConsoleSidebar__resetBanner" role="status">
            Replay reset — earlier output is no longer retained; showing what the factory still holds.
          </p>
        )}
        {children}
      </div>
    </aside>
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
