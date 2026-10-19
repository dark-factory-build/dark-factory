"use client";

import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { browserEndpoint, FactoryAppController, type FactoryAppSnapshot, type FactoryAppStatus, type FactoryTerminalView } from "./factory-app-controller.js";
import { FactoryConsole } from "./factory-console.js";
import { primaryAgent } from "./console-view.js";
import { XtermTerminal } from "./xterm-terminal.js";

const INITIAL_SNAPSHOT: FactoryAppSnapshot = { status: "idle" };

export type FactoryAppProps = {
  /** Receives the finite connection lifecycle exposed by the owned controller. */
  onStatusChange?: (status: FactoryAppStatus) => void;
  /** Optional loopback port for an isolated development listener. */
  browserPort?: number;
};

/** Complete browser application lifecycle; hosts only render this component. */
export function FactoryApp({ onStatusChange, browserPort }: FactoryAppProps = {}) {
  const [snapshot, setSnapshot] = useState<FactoryAppSnapshot>(INITIAL_SNAPSHOT);
  const [selectedTaskId, setSelectedTaskId] = useState<string>();
  const browser = useMemo(() => browserEndpoint(browserPort), [browserPort]);
  const owner = useRef<FactoryAppController | undefined>(undefined);
  const defaultedController = useRef<FactoryAppController | undefined>(undefined);
  const statusChange = useRef(onStatusChange);
  statusChange.current = onStatusChange;

  useEffect(() => {
    const controller = new FactoryAppController({
      origin: window.location.origin,
      location: window.location,
      history: window.history,
      onChange: setSnapshot,
      onStatusChange: (status) => statusChange.current?.(status),
      browser,
    });
    owner.current = controller;
    controller.start();
    return () => {
      if (owner.current === controller) owner.current = undefined;
      controller.close();
    };
  }, [browser]);

  // The primary overseer is useful immediately, but only once per owned
  // controller: closing a pane remains the operator's choice.
  useEffect(() => {
    const controller = owner.current;
    if (controller === undefined || defaultedController.current === controller || snapshot.status !== "ready" || snapshot.selectedAgent !== undefined || snapshot.state === undefined) return;
    const agent = primaryAgent(snapshot.state);
    if (agent === undefined) return;
    defaultedController.current = controller;
    controller.selectAgent(agent);
  }, [snapshot]);

  // Refresh the floor on a fresh session or a change to the project set.
  const projectKey = snapshot.state === undefined ? "" : [...snapshot.state.projects.keys()].join(" ");
  useEffect(() => {
    if (snapshot.status === "ready") owner.current?.loadGraphs();
  }, [snapshot.status, projectKey]);

  const selectedProjectID = snapshot.selectedAgent === undefined ? undefined : snapshot.state?.agents.get(snapshot.selectedAgent.id)?.project_id;
  useEffect(() => {
    if (snapshot.status === "ready" && selectedProjectID !== undefined) void owner.current?.loadRepositories(selectedProjectID);
  }, [snapshot.status, selectedProjectID]);

  // Keep worker locations live while the console is connected.
  useEffect(() => {
    if (snapshot.status !== "ready") return;
    const controller = owner.current;
    controller?.watchRunPaths(true);
    return () => controller?.watchRunPaths(false);
  }, [snapshot.status]);

  const controller = owner.current;
  const agentTerminal = controller === undefined || snapshot.selectedAgent === undefined ? undefined : snapshot.terminal;
  const terminal = agentTerminal === undefined || controller === undefined ? undefined : (
    <TerminalPanel
      terminal={agentTerminal}
    >
      <TerminalContent terminal={agentTerminal} controller={controller} />
    </TerminalPanel>
  );
  return (
    <FactoryConsole
      selectedTaskId={selectedTaskId}
      onSelectTask={setSelectedTaskId}
      {...snapshot}
      onSelectAgent={(agent) => owner.current?.selectAgent(agent)}
      onProjectContent={(operation, input) => owner.current?.projectContent(operation, input) ?? Promise.reject(new Error("closed"))}
      onEditTask={(task, change) => owner.current?.editTask(task, change) ?? Promise.resolve(false)}
      onOpenTerminalForHumanRequest={(request) => owner.current?.openTerminalForHumanRequest(request)}
      onSelectHumanRequest={(request) => { void owner.current?.selectHumanRequest(request); }}
      onHumanReplyChange={(reply) => owner.current?.setHumanReply(reply)}
      onReplyHumanRequest={() => { void owner.current?.replyHumanRequest(); }}
      onCancelHumanRequest={() => { void owner.current?.cancelHumanRequest(); }}
      onCloseHumanRequest={() => owner.current?.clearHumanRequest()}
      onLoadIntake={snapshot.status === "ready" ? (projectId) => { void owner.current?.loadIntake(projectId); } : undefined}
      onIntakeAction={(projectId, request) => { void owner.current?.intakeAction(projectId, request); }}
      terminalContent={terminal}
    />
  );
}

/** The selected agent's terminal is a sidebar, not a replacement screen. */
export function TerminalPanel({
  terminal,
  children,
}: {
  terminal: FactoryTerminalView;
  children: ReactNode;
}) {
  return (
    <section className="dfFactoryConsole__terminalPanel" aria-label={`Agent console for ${terminal.agentName}`}>
      {terminal.taskTitle === undefined ? null : <p className="dfFactoryConsole__terminalAgent">{terminal.taskTitle}</p>}
      {terminal.error === undefined ? null : (
        <p className="dfFactoryConsole__terminalError" role="alert">
          {terminal.errorSource === "display"
            ? terminal.hasOutputSurface ? "Terminal display error" : "Terminal display unavailable"
            : terminal.errorSource === "input"
              ? terminal.phase === "ready"
                ? terminal.error.code === "stale" ? "Terminal open elsewhere" : terminal.error.code === "invalid_request"
                  ? "Input rejected"
                  : "Input unavailable"
                : terminal.error.code === "connection"
                  ? "Terminal input connection lost"
                  : "Terminal input unavailable"
              : terminal.phase === "ready" && !terminal.writable && terminal.error.code === "stale"
            ? "Terminal open elsewhere"
            : "Terminal attach unavailable"}
        </p>
      )}
      {!terminal.resets ? null : (
        <p className="dfFactoryConsole__terminalReset" role="status">
          Earlier output is no longer retained; showing new output.
        </p>
      )}
      {terminal.taskTitle !== undefined && terminal.paused ? <p className="dfFactoryConsole__instructionState">Queue paused</p> : null}
      {children}
    </section>
  );
}

/** Chooses the one safe surface for the selected agent's durable state. */
export function TerminalContent({
  terminal,
  controller,
}: {
  terminal: FactoryTerminalView;
  controller: FactoryAppController;
}) {
  const terminalHost = terminal.hasOutputSurface || (terminal.taskTitle !== undefined && !terminal.finishing)
    ? <TerminalHost key={`${terminal.agentId}:${terminal.surfaceVersion}`} controller={controller} surfaceVersion={terminal.surfaceVersion} />
    : undefined;
  if (terminal.finishing) return <>{terminalHost}<p className="dfFactoryConsole__instructionState">Finishing</p></>;
  if (terminal.taskTitle !== undefined) return <>{terminalHost}{terminal.phase === "ready" ? null : <p className="dfFactoryConsole__instructionState">Starting</p>}</>;
  if (terminal.paused) return <>{terminalHost}<p className="dfFactoryConsole__instructionState">Paused</p></>;
  if (terminal.queued) return <>{terminalHost}<p className="dfFactoryConsole__instructionState">Queued · waiting for capacity</p></>;
  return terminalHost ?? null;
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
