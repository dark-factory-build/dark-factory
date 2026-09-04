import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import process from "node:process";
import WebSocket from "ws";
import { BrowserClient, SessionError } from "../../dist/src/index.js";

const DEADLINE_MS = 25_000;

class MemoryKeys {
  value = null;
  async load() { return this.value; }
  async save(value) { this.value = value; }
}

class Signals {
  version = 0;
  waiters = new Set();

  pulse() {
    this.version += 1;
    for (const wake of this.waiters) wake();
    this.waiters.clear();
  }

  async until(predicate, label) {
    const deadline = Date.now() + DEADLINE_MS;
    for (;;) {
      const value = predicate();
      if (value !== undefined && value !== false && value !== null) return value;
      const remaining = deadline - Date.now();
      if (remaining <= 0) throw new Error(`timeout waiting for ${label}`);
      const observed = this.version;
      await new Promise((resolve, reject) => {
        const wake = () => { clearTimeout(timer); this.waiters.delete(wake); resolve(); };
        const timer = setTimeout(() => { this.waiters.delete(wake); reject(new Error(`timeout waiting for ${label}`)); }, remaining);
        this.waiters.add(wake);
        if (this.version !== observed) wake();
      });
    }
  }
}

function socketFactory(config, sockets, signals, frames) {
  const startedAt = Date.now();
  return (url) => {
    const socket = new WebSocket(url, { headers: { Origin: config.origin }, perMessageDeflate: false });
    sockets.push(socket);
    socket.addEventListener("close", () => signals.pulse());
    socket.on("message", (data, binary) => {
      if (binary) {
        frames.push({ atMs: Date.now() - startedAt, type: "BINARY" });
      } else {
        try {
          const frame = JSON.parse(String(data));
          let type = String(frame.type);
          if (frame.type === "TERMINAL_EXIT") type = `${frame.type}:${frame.body?.exit_code}/${frame.body?.exit_signal}`;
          else if (frame.type === "ERROR") type = `${frame.type}:${frame.id ?? "none"}:${frame.body?.code}`;
          frames.push({ atMs: Date.now() - startedAt, type });
        } catch {
          frames.push({ atMs: Date.now() - startedAt, type: "INVALID_JSON" });
        }
      }
      if (frames.length > 32) frames.shift();
    });
    return socket;
  };
}

function exactAgent(client, agentId) {
  return client.state?.agents.get(agentId);
}

async function resolveTarget(client, agentId, signals) {
  for (;;) {
    const session = client.session;
    const state = client.state;
    const agent = state?.agents.get(agentId);
    if (session !== undefined && session.status === "ready" && state !== undefined && agent !== undefined) {
      try {
        const target = await session.resolveAgentTerminal({ agentId, expectedAgentRevision: agent.revision, expectedHead: state.head });
        if (target !== null) return { session, target };
      } catch (error) {
        if (!(error instanceof SessionError) || !["stale", "connection", "closed", "revision_conflict"].includes(error.code)) throw error;
      }
    }
    const generation = client.session;
    await signals.until(() => client.session !== generation || client.state?.head !== state?.head || exactAgent(client, agentId)?.revision !== agent?.revision, "a live terminal target");
  }
}

function terminalObserver(signals) {
  const decoder = new TextDecoder();
  let text = "";
  let cursor = 0n;
  let exit;
  let exitCount = 0;
  let reset;
  let closed;
  const options = (afterSequence) => ({
    afterSequence,
    onOutput(output) {
      assert.equal(output.sequence, cursor, "terminal output sequence must remain contiguous across reconnect");
      cursor += BigInt(output.payload.length);
      text += decoder.decode(output.payload, { stream: true });
      // The client deliberately excludes control operations until its output
      // callback has returned and the ACK is queued. Wake the harness on the
      // next event-loop turn, after that owner has cleared the exclusion.
      setImmediate(() => signals.pulse());
    },
    onExit(value) { exit = value; exitCount += 1; signals.pulse(); },
    onReset(value) { reset = value; signals.pulse(); },
    onClose(error) { closed = error ?? true; signals.pulse(); },
  });
  return {
    options,
    get text() { return text; },
    get cursor() { return cursor; },
    get exit() { return exit; },
    get exitCount() { return exitCount; },
    get reset() { return reset; },
    get closed() { return closed; },
    count(marker) { return text.split(marker).length - 1; },
  };
}

async function openTerminal(client, agentId, signals, observer) {
  // A fresh run's terminal is durably active moments before the daemon's
  // live owner is ready to serve attachments; the server types that window
  // as retryable. Re-resolve and retry briefly; anything non-retryable
  // still fails immediately.
  for (let attempt = 0; ; attempt += 1) {
    const { session, target } = await resolveTarget(client, agentId, signals);
    const handle = session.openTerminal(target, observer.options(observer.cursor));
    try {
      const attached = await handle.attach();
      assert.equal("kind" in attached, false, "retained cursor unexpectedly required a terminal reset");
      assert.equal(attached.acknowledgedSequence, observer.cursor);
      return { session, handle };
    } catch (error) {
      if (!(error instanceof SessionError) || error.retryable !== true || attempt >= 20) throw error;
      await handle.detach().catch(() => {});
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
  }
}

async function waitForHumanRequest(client, signals) {
  return signals.until(() => {
    for (const request of client.state?.humanRequests.values() ?? []) {
      if (request.status === "open") return request;
    }
    return undefined;
  }, "an open HumanRequest");
}

async function proveSocketSurvivesTerminalExit(client, session, agentId, signals) {
  for (;;) {
    assert.equal(client.session, session, "terminal exit replaced the authenticated WebSocket generation");
    const state = client.state;
    const agent = state?.agents.get(agentId);
    if (client.status !== "ready" || state === undefined || agent === undefined) {
      await signals.until(() => client.session !== session || client.status === "ready" && client.state?.agents.has(agentId), "post-exit canonical ready state");
      continue;
    }
    try {
      const target = await session.resolveAgentTerminal({ agentId, expectedAgentRevision: agent.revision, expectedHead: state.head });
      assert.equal(target, null, "terminal run remained targetable after exit");
      assert.equal(client.session, session, "post-exit request changed the WebSocket generation");
      const current = client.state;
      const currentAgent = current?.agents.get(agentId);
      if (client.status !== "ready" || current === undefined || currentAgent === undefined || current.head !== state.head || currentAgent.revision !== agent.revision) continue;
      return { requestSucceeded: true, head: state.head.toString(), agentRevision: agent.revision.toString() };
    } catch (error) {
      if (!(error instanceof SessionError) || !["stale", "revision_conflict"].includes(error.code)) throw error;
      await signals.until(() => client.state?.head !== state.head || exactAgent(client, agentId)?.revision !== agent.revision, "post-exit canonical state");
    }
  }
}

function assertHumanRequestProjection(client, request, config) {
  assert.equal(request.agent_id, config.agentId);
  assert.equal(request.kind, "question");
  assert.equal(request.status, "open");
  assert.equal(request.can_reply, true);
  assert.ok(request.reply_max_bytes > 0);
  assert.ok(client.state?.projects.has(request.project_id));
  assert.equal(client.state?.tasks.get(request.task_id)?.assigned_agent_id, config.agentId);
  assert.equal("question" in request, false, "private question leaked into public state");
}

function accepted(result, label) {
  assert.equal(result.status, "accepted", `${label} was not accepted`);
  assert.ok(result.accepted_bytes > 0n, `${label} accepted no bytes`);
}

async function scenarioInteractive(config, client, signals, sockets, errors, frames) {
  const observer = terminalObserver(signals);
  const first = await openTerminal(client, config.agentId, signals, observer);
  await signals.until(() => observer.text.includes("E2E_READY"), "initial PTY output");
  const firstLease = await first.handle.acquireInput();
  assert.equal(firstLease.generation, 1n, "fresh run did not start at the first lease generation");
  accepted(await first.handle.sendInput(new TextEncoder().encode("input-one\n")), "first terminal input");
  await signals.until(() => observer.text.includes("E2E_INPUT:input-one"), "provider input response");
  const resized = await first.handle.resize(37, 111);
  assert.equal(resized.rows, 37);
  assert.equal(resized.cols, 111);
  accepted(await first.handle.sendInput(new TextEncoder().encode("measure\n")), "terminal resize witness input");
  await signals.until(() => observer.text.includes("E2E_SIZE:37:111"), "provider-visible PTY resize");

  // The connection dies while still holding the lease, as a page reload
  // does; the same paired client must reacquire before that lease expires.
  assert.equal(first.handle.writable, true);
  const firstSession = client.session;
  const firstSocket = sockets.at(-1);
  assert.ok(firstSocket !== undefined);
  firstSocket.terminate();
  await signals.until(() => client.session !== firstSession && client.status === "ready", "authenticated reconnect");
  assert.equal(first.handle.writable, false, "old connection retained input authority");

  const second = await openTerminal(client, config.agentId, signals, observer);
  const secondSession = second.session;
  const secondSocket = sockets.at(-1);
  assert.notEqual(secondSession, firstSession, "reconnect reused the closed BrowserSession generation");
  assert.ok(secondSocket !== undefined && secondSocket !== firstSocket, "reconnect reused the closed WebSocket");
  assert.equal(sockets.length, 2, "reconnect created more than one replacement WebSocket");
  // Generation 2 proves the reacquire superseded the still-held lease rather
  // than taking a lease that had already expired during the reconnect.
  const secondLease = await second.handle.acquireInput();
  assert.equal(secondLease.generation, firstLease.generation + 1n, "reacquire did not supersede the unexpired lease");
  accepted(await second.handle.sendInput(new TextEncoder().encode("proceed\n")), "post-reconnect terminal input");
  const request = await waitForHumanRequest(client, signals);
  assertHumanRequestProjection(client, request, config);
  const detail = await second.session.getHumanRequestDetail({ requestId: request.id, expectedRevision: request.revision });
  assert.equal(detail.question, config.question);
  assert.equal(detail.canReply, true);
  assert.ok(detail.terminalTarget !== null);
  assert.ok(detail.cancelRun !== null);
  const reply = await second.session.replyHumanRequest(detail, "human-answer");
  assert.equal(reply.status, "resolved");
  await signals.until(() => observer.text.includes("E2E_REPLY:human-answer"), "exact HumanRequest reply output");
  try {
    await signals.until(() => observer.exit !== undefined, "terminal exit");
  } catch (error) {
    const timeline = frames.map((frame) => `${frame.atMs}ms:${frame.type}`).join(",");
    throw new Error(`${error.message}; client=${client.status}; terminal_closed=${String(observer.closed)}; errors=${errors.join(",")}; frames=${timeline}; cursor=${observer.cursor}; tail=${JSON.stringify(observer.text.slice(-256))}`);
  }
  assert.ok(
    observer.exit.exitSignal === 0 && observer.exit.exitCode >= 0 || observer.exit.exitCode === 0 && observer.exit.exitSignal > 0,
    "Scenario A provider exit was not a canonical normal-or-signaled arm",
  );
  const postExit = await proveSocketSurvivesTerminalExit(client, secondSession, config.agentId, signals);
  assert.equal(client.session, secondSession, "restart or terminal exit replaced the authenticated session");
  assert.equal(sockets.length, 2, "restart or terminal exit created another WebSocket");
  assert.equal(sockets.at(-1), secondSocket, "post-exit request did not use the replacement WebSocket");
  assert.equal(secondSocket.readyState, WebSocket.OPEN, "post-exit request left the replacement WebSocket closed");
  assert.deepEqual(errors, ["connection"], "intentional disconnect was not the sole connection error");
  assert.equal(frames.some((frame) => frame.type === "INVALID_JSON"), false, "real server emitted a malformed frame");
  assert.equal(client.status, "ready", "state repair closed the client after terminal exit");
  assert.ok(client.state !== undefined && client.state.head.toString() === postExit.head, "post-exit canonical state did not remain coherent");
  assert.equal(observer.reset, undefined);
  assert.equal(observer.count("E2E_INPUT:input-one"), 1, "terminal input was replayed");
  assert.equal(observer.count("E2E_SIZE:37:111"), 1, "provider-visible resize witness was duplicated");
  assert.equal(observer.count("E2E_REPLY:human-answer"), 1, "HumanRequest reply was duplicated");
  assert.equal(observer.exitCount, 1, "terminal exit was duplicated");
  return {
    scenario: "interactive",
    requestId: request.id,
    outputCursor: observer.cursor.toString(),
    inputMarkers: observer.count("E2E_INPUT:input-one"),
    resizeMarkers: observer.count("E2E_SIZE:37:111"),
    replyMarkers: observer.count("E2E_REPLY:human-answer"),
    exit: { sessionId: observer.exit.sessionId, exitCode: observer.exit.exitCode, exitSignal: observer.exit.exitSignal },
    exitCount: observer.exitCount,
    postExitRequest: postExit.requestSucceeded,
    canonicalHead: postExit.head,
    canonicalAgentRevision: postExit.agentRevision,
    reconnects: sockets.length - 1,
  };
}

async function scenarioCancel(config, client, signals) {
  const observer = terminalObserver(signals);
  const terminal = await openTerminal(client, config.agentId, signals, observer);
  await signals.until(() => observer.text.includes("E2E_WAITING"), "cancel fixture PTY output");
  await terminal.handle.acquireInput();
  const request = await waitForHumanRequest(client, signals);
  assertHumanRequestProjection(client, request, config);
  const detail = await terminal.session.getHumanRequestDetail({ requestId: request.id, expectedRevision: request.revision });
  assert.equal(detail.question, config.question);
  assert.ok(detail.cancelRun !== null);
  const cancelled = await terminal.session.cancelHumanRequest(detail.cancelRun);
  assert.equal(cancelled.request_id, request.id);

  // Let a completed terminal-output callback queue its ACK before exercising
  // the one irreversible post-cancel input attempt.
  await new Promise((resolve) => setImmediate(resolve));
  let staleInput = "locally_rejected";
  try {
    const result = await terminal.handle.sendInput(new TextEncoder().encode("forbidden-after-cancel\n"));
    staleInput = result.status;
    assert.equal(result.status, "rejected", "post-finalizing input was not definitively rejected");
  } catch (error) {
    assert.match(String(error), /closed|not writable|terminal exited/i, "post-finalizing input failed before reaching a definitive fence");
  }
  await signals.until(() => observer.exit !== undefined, "cancelled terminal exit");
  assert.equal(observer.exit.exitCode, 0, "signaled provider exit exposed a contradictory code");
  assert.ok(observer.exit.exitSignal > 0, "cancel did not expose the canonical provider signal");
  assert.equal(terminal.handle.writable, false, "terminal exit retained writable authority");
  const postExit = await proveSocketSurvivesTerminalExit(client, terminal.session, config.agentId, signals);
  assert.equal(observer.text.includes("E2E_FORBIDDEN"), false, "post-cancel input reached the provider");
  assert.equal(observer.exitCount, 1, "terminal exit was duplicated");
  return {
    scenario: "cancel",
    requestId: request.id,
    runId: cancelled.run_id,
    staleInput,
    exit: { sessionId: observer.exit.sessionId, exitCode: observer.exit.exitCode, exitSignal: observer.exit.exitSignal },
    exitCount: observer.exitCount,
    postExitRequest: postExit.requestSucceeded,
    canonicalHead: postExit.head,
    canonicalAgentRevision: postExit.agentRevision,
  };
}

async function main() {
  if (process.argv.length !== 3) throw new Error("one E2E configuration path is required");
  const config = JSON.parse(await readFile(process.argv[2], "utf8"));
  assert.match(config.url, /^ws:\/\/127\.0\.0\.1:[0-9]+\/browser\/v1$/);
  assert.match(config.host, /^127\.0\.0\.1:[0-9]+$/);
  assert.equal(config.origin, "https://app.darkfactory.build");
  assert.match(config.challenge, /^[0-9a-f]{64}$/);
  assert.match(config.agentId, /^[0-9a-f]{32}$/);

  const signals = new Signals();
  const sockets = [];
  const errors = [];
  const frames = [];
  const client = new BrowserClient({
    url: config.url,
    host: config.host,
    origin: config.origin,
    challenge: config.challenge,
    keyStore: new MemoryKeys(),
    socketFactory: socketFactory(config, sockets, signals, frames),
    reconnectInitialDelayMs: 10,
    reconnectMaxDelayMs: 20,
    onStatus: () => signals.pulse(),
    onState: () => signals.pulse(),
    onError: (error) => { errors.push(error.code); signals.pulse(); },
  });

  try {
    await client.connect();
    await signals.until(() => client.status === "ready", "initial browser readiness");
    const result = config.scenario === "interactive"
      ? await scenarioInteractive(config, client, signals, sockets, errors, frames)
      : config.scenario === "cancel"
        ? await scenarioCancel(config, client, signals)
        : (() => { throw new Error("unknown E2E scenario"); })();
    result.clientId = client.session?.clientId;
    result.capabilities = client.session?.capabilities;
    result.connectionErrors = errors.filter((code) => code === "connection").length;
    assert.ok(errors.every((code) => code === "connection"), `unexpected client errors: ${errors.join(",")}`);
    process.stdout.write(`${JSON.stringify(result)}\n`);
  } finally {
    client.close();
    for (const socket of sockets) {
      if (socket.readyState !== WebSocket.CLOSED) socket.terminate();
    }
  }
}

await main();
