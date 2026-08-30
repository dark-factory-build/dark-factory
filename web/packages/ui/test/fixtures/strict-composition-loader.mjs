const clientModule = `
const counters = globalThis.__darkFactoryStrictProbe ??= { clients: 0, sessionCloses: 0, resolves: 0, opens: 0, attaches: 0, acquires: 0, terminals: 0, disposes: 0 };
export class SessionError extends Error { constructor(code) { super(code); this.name = "SessionError"; this.code = code; this.retryable = false; } }
export class ProtocolError extends Error { constructor(code) { super(code); this.name = "ProtocolError"; this.code = code; } }
export const MAX_TERMINAL_PAYLOAD = 4096;
export const MAX_TERMINAL_ROWS = 4096;
export const MAX_TERMINAL_COLS = 4096;
export function consumePairingChallenge() { return null; }
const agent = { id: "21".repeat(16), project_id: "11".repeat(16), name: "Strict Agent", role: "worker", paused: false, revision: 1n };
const state = { head: 1n, sequence: 1n, factory: [{ dispatch_enabled: true, capacity: 1, active_runs: 0, revision: 1n }], projects: new Map([[agent.project_id, { id: agent.project_id, name: "Strict Project", revision: 1n }]]), agents: new Map([[agent.id, agent]]), tasks: new Map(), humanRequests: new Map() };
export function createBrowserClient(options) {
  counters.clients += 1;
  let closed = false;
  const session = {
    resolveAgentTerminal: async () => { counters.resolves += 1; return Object.freeze({}); },
    openTerminal: (_target, callbacks) => {
      counters.opens += 1;
      const handle = { writable: true, attach: async () => { counters.attaches += 1; return {}; }, acquireInput: async () => { counters.acquires += 1; return {}; }, sendInput: async (bytes) => ({ status: "accepted", accepted_bytes: BigInt(bytes.length) }), resize: async (rows, cols) => ({ rows, cols }), close: () => callbacks.onClose?.(new SessionError("closed")) };
      return handle;
    },
    close: () => {
      if (closed) return;
      closed = true;
      counters.sessionCloses += 1;
      options.onStatus?.("closed");
    },
  };
  return {
    session,
    connect: () => { options.onState?.(state); options.onStatus?.("ready"); return Promise.resolve(); },
    close: () => session.close(),
  };
}
`;

const xtermModule = `
const counters = globalThis.__darkFactoryStrictProbe ??= { clients: 0, sessionCloses: 0, resolves: 0, opens: 0, attaches: 0, acquires: 0, terminals: 0, disposes: 0 };
export class Terminal {
  rows = 24;
  cols = 80;
  constructor() { counters.terminals += 1; }
  loadAddon(addon) { addon.activate?.(this); }
  open() {}
  onData() { return { dispose() {} }; }
  onBinary() { return { dispose() {} }; }
  onResize() { return { dispose() {} }; }
  write(_payload, done) { done?.(); }
  focus() {}
  dispose() { counters.disposes += 1; }
}
`;

const fitModule = `export class FitAddon { activate(terminal) { this.terminal = terminal; } fit() {} }`;

function dataModule(source) {
  return `data:text/javascript,${encodeURIComponent(source)}`;
}

export async function resolve(specifier, context, nextResolve) {
  if (specifier === "@dark-factory/client") return { url: dataModule(clientModule), shortCircuit: true };
  if (specifier === "@xterm/xterm") return { url: dataModule(xtermModule), shortCircuit: true };
  if (specifier === "@xterm/addon-fit") return { url: dataModule(fitModule), shortCircuit: true };
  return nextResolve(specifier, context);
}
