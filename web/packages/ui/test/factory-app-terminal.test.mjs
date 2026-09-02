import assert from "node:assert/strict";
import test from "node:test";
import { ProtocolError, SessionError } from "@dark-factory/client";
import { FactoryAppController } from "../dist/src/factory-app-controller.js";
import { fixtureState } from "../../../fixtures/state.mjs";

const agent = [...fixtureState.agents.values()][0];
const secondAgent = [...fixtureState.agents.values()][1];
const request = [...fixtureState.humanRequests.values()][0];
const target = Object.freeze({});

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((accept, refuse) => { resolve = accept; reject = refuse; });
  return { promise, resolve, reject };
}

async function flush() {
  for (let index = 0; index < 5; index += 1) await Promise.resolve();
}

function stateAt(head, overrides = {}) {
  return { ...fixtureState, head: BigInt(head), sequence: BigInt(head), ...overrides };
}

function terminalHarness({ closeStatus = false, fail, failError = new SessionError("connection"), detachImpl } = {}) {
  let attachReset = false;
  const snapshots = [];
  const calls = [];
  const targetGates = [];
  const handles = [];
  let sessionCloses = 0;
  let clientOptions;
  let handleOptions;
  const session = {
    getHumanRequestDetail: async () => ({
      requestId: request.id,
      revision: request.revision,
      question: "Continue?",
      canReply: true,
      replyMaxBytes: 8192,
      terminalTarget: Object.freeze({}),
      cancelRun: Object.freeze({ requestId: request.id, expectedRequestRevision: request.revision, expectedRunRevision: 17n }),
    }),
    replyHumanRequest: async () => ({ status: "resolved" }),
    cancelHumanRequest: async () => ({ request_id: request.id }),
    resolveAgentTerminal: (value) => {
      calls.push({ kind: "resolve", value });
      const gate = deferred();
      targetGates.push(gate);
      return gate.promise;
    },
    openTerminal: (value, callbacks) => {
      calls.push({ kind: "open", value });
      if (fail === "open") throw failError;
      handleOptions = callbacks;
      const handle = {
        attach: async () => { calls.push({ kind: "attach" }); if (fail === "attach") throw failError; if (attachReset) return { kind: "reset", freshAttachRequired: true, sessionId: "31".repeat(16), floor: 5n, head: 9n }; return { sessionId: "31".repeat(16), floor: 0n, head: 0n, acknowledgedSequence: 0n, maxUnackedBytes: 65536n }; },
        acquireInput: async () => { calls.push({ kind: "acquire" }); if (fail === "acquire") throw failError; return { generation: 1n }; },
        sendInput: async (bytes) => { calls.push({ kind: "input", bytes }); if (fail === "input") throw failError; return { status: "accepted", accepted_bytes: BigInt(bytes.length) }; },
        resize: async (rows, cols) => { calls.push({ kind: "resize", rows, cols }); if (fail === "resize") throw failError; return { rows, cols }; },
        detach: async () => { calls.push({ kind: "detach" }); await detachImpl?.(); callbacks.onClose?.(); },
        get writable() { return true; },
      };
      handles.push(handle);
      return handle;
    },
    close: () => { sessionCloses += 1; if (closeStatus) clientOptions?.onStatus("closed"); },
  };
  const client = {
    session,
    connect: () => Promise.resolve(),
    close: () => { sessionCloses += 1; },
  };
  const controller = new FactoryAppController({
    origin: "https://app.darkfactory.build",
    location: { hash: "", pathname: "/factory", search: "" },
    history: { state: null, replaceState: () => {} },
    onChange: (snapshot) => snapshots.push(snapshot),
    clientFactory: (value) => { clientOptions = value; return client; },
  });
  return {
    controller,
    session,
    client,
    snapshots,
    calls,
    handles,
    targetGates,
    sessionCloses: () => sessionCloses,
    clientOptions: () => clientOptions,
    handleOptions: () => handleOptions,
    surfaceFailure: fail === "surface",
    setAttachReset: (value) => { attachReset = value; },
    latest: () => snapshots.at(-1),
    ready: () => { clientOptions.onState(fixtureState); clientOptions.onStatus("ready"); },
  };
}

async function openTerminal(context, selectedAgent = agent) {
  context.controller.selectAgent(selectedAgent);
  const token = {};
  const writes = [];
  context.controller.beginTerminalSurface(token);
  context.controller.setTerminalSurface(token, {
    write: async (bytes) => { writes.push(bytes); if (context.surfaceFailure) throw new Error("display failed"); },
    abort: () => {},
  });
  await flush();
  assert.equal(context.targetGates.length > 0, true);
  context.targetGates.at(-1).resolve(target);
  await flush();
  assert.deepEqual(context.calls.slice(-3).map((call) => call.kind), ["open", "attach", "acquire"]);
  assert.equal(context.latest().terminal.phase, "ready");
  return { token, writes, options: context.handleOptions() };
}

test("public terminal composition waits for surface and writable authority", async () => {
  const context = terminalHarness();
  context.controller.start();
  context.ready();
  context.controller.selectAgent(agent);
  assert.equal(context.latest().terminal.phase, "idle");
  assert.equal(context.calls.some((call) => call.kind === "resolve"), false);

  const token = {};
  context.controller.beginTerminalSurface(token);
  context.controller.sendTerminalText(token, "before");
  assert.equal(context.calls.some((call) => call.kind === "input"), false);
  context.controller.setTerminalSurface(token, { write: async () => {}, abort: () => {} });
  await flush();
  assert.equal(context.calls.at(-1).kind, "resolve");
  assert.deepEqual(context.calls.at(-1).value, { agentId: agent.id, expectedAgentRevision: agent.revision, expectedHead: fixtureState.head });
  context.targetGates[0].resolve(target);
  await flush();
  assert.deepEqual(context.calls.slice(-3).map((call) => call.kind), ["open", "attach", "acquire"]);
  context.controller.sendTerminalText(token, "after");
  context.controller.sendTerminalBinary(token, String.fromCharCode(0, 255));
  await flush();
  assert.deepEqual(context.calls.filter((call) => call.kind === "input").map((call) => [...call.bytes]), [[97, 102, 116, 101, 114], [0, 255]]);
});

test("a later state head does not restart a live terminal, while waiting discovery captures the latest head", async () => {
  const context = terminalHarness();
  context.controller.start();
  context.ready();
  const live = await openTerminal(context);
  const resolveCount = context.targetGates.length;
  context.clientOptions().onState(stateAt(43));
  context.controller.selectAgent(agent);
  assert.equal(context.sessionCloses(), 0);
  assert.equal(context.targetGates.length, resolveCount);
  context.controller.sendTerminalText(live.token, "same session");
  await flush();
  assert.equal(context.calls.at(-1).kind, "input");

  const waiting = terminalHarness();
  waiting.controller.start();
  waiting.ready();
  waiting.controller.selectAgent(agent);
  waiting.clientOptions().onState(stateAt(44));
  const token = {};
  waiting.controller.beginTerminalSurface(token);
  waiting.controller.setTerminalSurface(token, { write: async () => {}, abort: () => {} });
  await flush();
  assert.equal(waiting.calls.at(-1).value.expectedHead, 44n);
});

async function assertNoAutomaticRestart(context) {
  const attempts = context.targetGates.length;
  context.clientOptions().onStatus("connecting");
  context.clientOptions().onState(fixtureState);
  context.clientOptions().onStatus("ready");
  await flush();
  assert.equal(context.targetGates.length, attempts);
  assert.equal(context.latest().selectedAgent, undefined);
}

test("active-task target absence waits for the next public head before retrying", async () => {
  const context = terminalHarness();
  context.controller.start();
  context.ready();
  context.controller.selectAgent(agent);
  const token = {};
  context.controller.beginTerminalSurface(token);
  context.controller.setTerminalSurface(token, { write: async () => {}, abort: () => {} });
  await flush();
  context.targetGates[0].resolve(null);
  await flush();
  assert.equal(context.latest().selectedAgent.id, agent.id);
  assert.equal(context.latest().terminal.phase, "idle");
  assert.equal(context.latest().error, undefined);
  assert.equal(context.sessionCloses(), 0);

  await remountSurface(context);
  assert.equal(context.targetGates.length, 1, "same public head must not spin discovery");
  context.clientOptions().onState(stateAt(43));
  await flush();
  assert.equal(context.targetGates.length, 2);
  assert.equal(context.calls.at(-1).value.expectedHead, 43n);
  context.targetGates[1].resolve(target);
  await flush();
  assert.equal(context.latest().terminal.phase, "ready");
});

test("stale active-task discovery retries the newer state already received", async () => {
  const context = terminalHarness();
  context.controller.start();
  context.ready();
  context.controller.selectAgent(agent);
  const token = {};
  context.controller.beginTerminalSurface(token);
  context.controller.setTerminalSurface(token, { write: async () => {}, abort: () => {} });
  await flush();
  context.clientOptions().onState(stateAt(43));
  context.targetGates[0].reject(new SessionError("stale"));
  await flush();
  assert.equal(context.latest().selectedAgent.id, agent.id);
  assert.equal(context.sessionCloses(), 0);
  await remountSurface(context);
  assert.equal(context.targetGates.length, 2);
  assert.equal(context.calls.at(-1).value.expectedHead, 43n);
});

test("stale discovery preserves an idle observation until newer running state arrives", async () => {
  const context = terminalHarness();
  const idleTasks = new Map(fixtureState.tasks);
  const running = [...idleTasks.values()].find(
    (task) => task.assigned_agent_id === agent.id && task.status === "running",
  );
  assert.notEqual(running, undefined);
  idleTasks.set(running.id, { ...running, status: "queued" });

  context.controller.start();
  context.clientOptions().onState(stateAt(42, { tasks: idleTasks }));
  context.clientOptions().onStatus("ready");
  context.controller.selectAgent(agent);
  const token = {};
  context.controller.beginTerminalSurface(token);
  context.controller.setTerminalSurface(token, { write: async () => {}, abort: () => {} });
  await flush();

  context.targetGates[0].reject(new SessionError("stale"));
  await flush();
  assert.equal(context.latest().selectedAgent.id, agent.id);
  assert.equal(context.latest().terminal.phase, "idle");
  assert.equal(context.sessionCloses(), 0);

  await remountSurface(context);
  assert.equal(context.targetGates.length, 1, "same stale head must not spin discovery");
  context.clientOptions().onState(stateAt(42, { tasks: idleTasks }));
  await flush();
  assert.equal(context.latest().selectedAgent.id, agent.id);
  assert.equal(context.targetGates.length, 1, "same-head entity refresh must preserve stale waiting");

  context.clientOptions().onState(stateAt(43, { tasks: idleTasks }));
  await flush();
  assert.equal(context.targetGates.length, 2);
  assert.equal(context.calls.at(-1).value.expectedHead, 43n);
});

test("same-socket state resync preserves pending terminal discovery", async () => {
  const context = terminalHarness();
  context.controller.start();
  context.ready();
  context.controller.selectAgent(agent);
  const token = {};
  context.controller.beginTerminalSurface(token);
  context.controller.setTerminalSurface(token, { write: async () => {}, abort: () => {} });
  await flush();

  context.clientOptions().onStatus("syncing");
  assert.equal(context.latest().selectedAgent.id, agent.id);
  assert.equal(context.latest().terminal.phase, "resolving");
  assert.equal(context.sessionCloses(), 0);
  context.targetGates[0].reject(new SessionError("stale"));
  await flush();
  assert.equal(context.latest().selectedAgent.id, agent.id);
  assert.equal(context.sessionCloses(), 0);

  await remountSurface(context);
  context.clientOptions().onState(stateAt(43));
  await flush();
  assert.equal(context.targetGates.length, 1, "syncing cannot start target discovery");
  context.clientOptions().onStatus("ready");
  await flush();
  assert.equal(context.targetGates.length, 2);
  assert.equal(context.calls.at(-1).value.expectedHead, 43n);
});

test("same-socket state resync preserves one live writable terminal", async () => {
  const context = terminalHarness();
  context.controller.start();
  context.ready();
  const live = await openTerminal(context);
  const resolves = context.targetGates.length;

  context.clientOptions().onStatus("syncing");
  assert.equal(context.latest().terminal.phase, "ready");
  assert.equal(context.latest().terminal.writable, true);
  assert.equal(context.sessionCloses(), 0);
  context.controller.sendTerminalText(live.token, "during resync");
  await flush();
  assert.equal(context.calls.at(-1).kind, "input");
  assert.equal(new TextDecoder().decode(context.calls.at(-1).bytes), "during resync");

  context.clientOptions().onState(stateAt(43));
  context.clientOptions().onStatus("ready");
  await flush();
  assert.equal(context.targetGates.length, resolves);
  assert.equal(context.latest().terminal.phase, "ready");
  assert.equal(context.latest().terminal.writable, true);
  context.controller.sendTerminalText(live.token, "after resync");
  await flush();
  assert.equal(new TextDecoder().decode(context.calls.at(-1).bytes), "after resync");
});

test("target absence for an agent without active work closes only the terminal panel", async () => {
  const context = terminalHarness();
  context.controller.start();
  context.ready();
  context.controller.selectAgent(secondAgent);
  const token = {};
  context.controller.beginTerminalSurface(token);
  context.controller.setTerminalSurface(token, { write: async () => {}, abort: () => {} });
  await flush();
  context.targetGates[0].resolve(null);
  await flush();
  assert.equal(context.latest().selectedAgent, undefined);
  assert.equal(context.latest().terminal, undefined);
  assert.equal(context.sessionCloses(), 0);
});

test("target absence for a blocked task does not wait for an impossible activation", async () => {
  const context = terminalHarness();
  const tasks = new Map(fixtureState.tasks);
  const running = [...tasks.values()].find((task) => task.assigned_agent_id === agent.id && task.status === "running");
  assert.notEqual(running, undefined);
  tasks.set(running.id, { ...running, status: "blocked" });
  context.controller.start();
  context.clientOptions().onState(stateAt(43, { tasks }));
  context.clientOptions().onStatus("ready");
  context.controller.selectAgent(agent);
  const token = {};
  context.controller.beginTerminalSurface(token);
  context.controller.setTerminalSurface(token, { write: async () => {}, abort: () => {} });
  await flush();
  context.targetGates[0].resolve(null);
  await flush();
  assert.equal(context.latest().selectedAgent, undefined);
  assert.equal(context.latest().terminal, undefined);
  assert.equal(context.sessionCloses(), 0);
  context.clientOptions().onState(stateAt(44, { tasks }));
  await flush();
  assert.equal(context.targetGates.length, 1, "unrelated heads must not retry a terminal outcome");
});

test("same-head task refresh closes discovery waiting after the run becomes terminal", async () => {
  const context = terminalHarness();
  context.controller.start();
  context.ready();
  context.controller.selectAgent(agent);
  const token = {};
  context.controller.beginTerminalSurface(token);
  context.controller.setTerminalSurface(token, { write: async () => {}, abort: () => {} });
  await flush();
  context.targetGates[0].resolve(null);
  await flush();
  assert.equal(context.latest().selectedAgent.id, agent.id);

  const tasks = new Map(fixtureState.tasks);
  const running = [...tasks.values()].find((task) => task.assigned_agent_id === agent.id && task.status === "running");
  assert.notEqual(running, undefined);
  tasks.set(running.id, { ...running, status: "blocked", revision: running.revision + 1n });
  context.clientOptions().onState(stateAt(fixtureState.head, { tasks }));
  await flush();
  assert.equal(context.latest().selectedAgent, undefined);
  assert.equal(context.latest().terminal, undefined);
  assert.equal(context.sessionCloses(), 0);
  assert.equal(context.targetGates.length, 1);
});

test("fatal discovery and terminal failures disarm selection before reconnect can rediscover", async () => {
  for (const failure of ["throw", "open", "attach", "input", "resize", "surface"]) {
    const context = terminalHarness({ closeStatus: true, fail: failure === "throw" ? undefined : failure });
    context.controller.start();
    context.ready();
    context.controller.selectAgent(agent);
    const token = {};
    context.controller.beginTerminalSurface(token);
    context.controller.setTerminalSurface(token, {
      write: async (bytes) => { if (failure === "surface") throw new Error("display failed"); },
      abort: () => {},
    });
    await flush();
    assert.equal(context.targetGates.length, 1, failure);
    if (failure === "throw") context.targetGates[0].reject(new SessionError("connection"));
    else context.targetGates[0].resolve(target);
    await flush();
    if (failure === "input") {
      context.controller.sendTerminalText(token, "input");
      await flush();
    } else if (failure === "resize") {
      context.controller.resizeTerminal(token, 24, 80);
      await flush();
    } else if (failure === "surface") {
      await assert.rejects(context.handleOptions().onOutput({ sequence: 0n, payload: new Uint8Array([1]) }));
      await flush();
    }
    assert.equal(context.targetGates.length, 1, failure);
    assert.equal(context.sessionCloses(), 1, failure);
    await assertNoAutomaticRestart(context);
  }
});

test("fatal lifecycle state, not an error-code exception, fences reusable failure codes", async () => {
  for (const error of [new SessionError("invalid_request"), new ProtocolError("malformed")]) {
    for (const failure of ["resolve", "open", "attach", "input", "resize", "surface"]) {
      const context = terminalHarness({ closeStatus: true, fail: failure === "resolve" ? undefined : failure, failError: error });
      context.controller.start();
      context.ready();
      context.controller.selectAgent(agent);
      const token = {};
      context.controller.beginTerminalSurface(token);
      context.controller.setTerminalSurface(token, {
        write: async () => { if (failure === "surface") throw error; },
        abort: () => {},
      });
      await flush();
      if (failure === "resolve") context.targetGates[0].reject(error);
      else context.targetGates[0].resolve(target);
      await flush();
      if (failure === "input") {
        context.controller.sendTerminalText(token, "input");
        await flush();
      } else if (failure === "resize") {
        context.controller.resizeTerminal(token, 24, 80);
        await flush();
      } else if (failure === "surface") {
        await assert.rejects(context.handleOptions().onOutput({ sequence: 0n, payload: new Uint8Array([1]) }));
        await flush();
      }
      assert.equal(context.sessionCloses(), 1, `${error.code}:${failure}`);
      await assertNoAutomaticRestart(context);
    }
  }
});

test("a fresh explicit selection can start one new terminal attempt after a fatal failure", async () => {
  const context = terminalHarness({ closeStatus: true });
  context.controller.start();
  context.ready();
  context.controller.selectAgent(agent);
  const token = {};
  context.controller.beginTerminalSurface(token);
  context.controller.setTerminalSurface(token, { write: async () => {}, abort: () => {} });
  await flush();
  context.targetGates[0].reject(new SessionError("connection"));
  await flush();
  assert.equal(context.latest().selectedAgent, undefined);
  assert.equal(context.sessionCloses(), 1);
  context.clientOptions().onStatus("connecting");
  context.clientOptions().onState(fixtureState);
  context.clientOptions().onStatus("ready");
  context.controller.selectAgent(agent);
  const freshToken = {};
  context.controller.beginTerminalSurface(freshToken);
  context.controller.setTerminalSurface(freshToken, { write: async () => {}, abort: () => {} });
  await flush();
  assert.equal(context.targetGates.length, 2);
});

test("selection detaches the old controller before resolving the new public agent and fences stale callbacks", async () => {
  const detachGate = deferred();
  const context = terminalHarness({ detachImpl: () => detachGate.promise });
  context.controller.start();
  context.ready();
  const first = await openTerminal(context, agent);
  const oldOptions = first.options;
  context.controller.selectAgent(secondAgent);
  assert.equal(context.sessionCloses(), 0);
  assert.equal(context.latest().selectedAgent.id, agent.id, "old terminal stays mounted while detach is pending");
  assert.equal(context.latest().terminal.phase, "closing");
  assert.equal(context.targetGates.length, 1);
  detachGate.resolve();
  await flush();
  assert.equal(context.latest().selectedAgent.id, secondAgent.id);
  assert.equal(context.sessionCloses(), 0);

  const secondToken = {};
  context.controller.beginTerminalSurface(secondToken);
  context.controller.setTerminalSurface(secondToken, { write: async () => {}, abort: () => {} });
  await flush();
  assert.equal(context.targetGates.length, 2);
  assert.equal(context.targetGates[1] !== undefined, true);
  oldOptions.onExit();
  assert.equal(context.latest().selectedAgent.id, secondAgent.id);
  context.targetGates[1].resolve(target);
  await flush();
  assert.deepEqual(context.calls.slice(-3).map((call) => call.kind), ["open", "attach", "acquire"]);
  assert.equal(context.latest().terminal.phase, "ready");
});

test("selected-agent revision drift cannot escalate an explicit detach to session close", async () => {
  const detachGate = deferred();
  const context = terminalHarness({ detachImpl: () => detachGate.promise });
  context.controller.start();
  context.ready();
  await openTerminal(context, agent);

  context.controller.clearAgentTerminal();
  assert.equal(context.latest().terminal.phase, "closing");
  const revisedAgent = { ...agent, revision: agent.revision + 1n };
  const revisedAgents = new Map(fixtureState.agents);
  revisedAgents.set(revisedAgent.id, revisedAgent);
  context.clientOptions().onState(stateAt(43, { agents: revisedAgents }));
  assert.equal(context.sessionCloses(), 0);
  assert.equal(context.latest().selectedAgent.id, agent.id, "closing host remains mounted until detach resolves");

  detachGate.resolve();
  await flush();
  assert.equal(context.sessionCloses(), 0);
  assert.equal(context.latest().selectedAgent, undefined);
  assert.equal(context.latest().error, undefined, "the explicit close intent wins over concurrent revision drift");
});

test("output failure during replacement closes the session and never installs the queued agent", async () => {
  const detachGate = deferred();
  const context = terminalHarness({ detachImpl: () => detachGate.promise, fail: "surface" });
  context.controller.start();
  context.ready();
  const first = await openTerminal(context, agent);

  context.controller.selectAgent(secondAgent);
  await assert.rejects(first.options.onOutput({ sequence: 0n, payload: new Uint8Array([1]) }), (error) => error.code === "closed");
  first.options.onClose(new SessionError("connection"));
  await flush();
  assert.equal(context.sessionCloses(), 1);
  assert.equal(context.latest().selectedAgent, undefined);
  assert.equal(context.targetGates.length, 1, "replacement discovery never started");

  detachGate.resolve();
  await flush();
  assert.equal(context.targetGates.length, 1, "late detach completion stays fenced");
});

test("a prior lease refusal does not turn a clean terminal switch into session failure", async () => {
  const context = terminalHarness({ fail: "acquire" });
  context.controller.start();
  context.ready();
  await openTerminal(context, agent);
  assert.equal(context.latest().terminal.writable, false);
  assert.equal(context.latest().terminal.error.code, "connection");

  context.controller.selectAgent(secondAgent);
  await flush();
  assert.equal(context.sessionCloses(), 0);
  assert.equal(context.latest().selectedAgent.id, secondAgent.id);
  assert.equal(context.latest().terminal.error, undefined);
});

test("normal terminal exit drops only UI ownership and does not become a connection error", async () => {
  const context = terminalHarness();
  context.controller.start();
  context.ready();
  const terminal = await openTerminal(context);
  terminal.options.onExit();
  assert.equal(context.latest().selectedAgent, undefined);
  assert.equal(context.latest().terminal, undefined);
  assert.equal(context.latest().error, undefined);
  assert.equal(context.sessionCloses(), 0);
});

test("HumanRequest terminal intent uses only its current public agent relationship and preserves detail draft authority", async () => {
  const context = terminalHarness();
  context.controller.start();
  context.ready();
  await context.controller.selectHumanRequest(request);
  context.controller.setHumanReply("keep this draft");
  context.controller.openTerminalForHumanRequest(request);
  assert.equal(context.latest().selectedAgent.id, agent.id);
  assert.equal(context.latest().selectedHumanRequest.reply, "keep this draft");

  const stale = { ...request, revision: request.revision + 1n };
  context.controller.clearAgentTerminal();
  context.controller.openTerminalForHumanRequest(stale);
  assert.equal(context.latest().error.code, "stale");

  const missingAgent = { ...request, agent_id: "ab".repeat(16) };
  context.clientOptions().onState(stateAt(42, { humanRequests: new Map([[missingAgent.id, missingAgent]]) }));
  context.controller.openTerminalForHumanRequest(missingAgent);
  assert.equal(context.latest().error.code, "stale");
});

test("selected terminal close detaches once, keeps the session ready, and permits reopen", async () => {
  const context = terminalHarness();
  context.controller.start();
  context.ready();
  await openTerminal(context);
  context.controller.clearAgentTerminal();
  context.controller.clearAgentTerminal();
  await flush();
  assert.deepEqual(context.calls.slice(-1).map((call) => call.kind), ["detach"]);
  assert.equal(context.sessionCloses(), 0);
  assert.equal(context.latest().error, undefined);
  assert.equal(context.latest().selectedAgent, undefined);

  context.clientOptions().onState(fixtureState);
  context.clientOptions().onStatus("ready");
  context.controller.selectAgent(agent);
  const token = {};
  context.controller.beginTerminalSurface(token);
  context.controller.setTerminalSurface(token, { write: async () => {}, abort: () => {} });
  await flush();
  context.targetGates.at(-1).resolve(target);
  await flush();
  assert.equal(context.latest().terminal.phase, "ready");
});

test("terminal failure is finite and never exposes protocol authority or output bytes in the public snapshot", async () => {
  const context = terminalHarness();
  context.controller.start();
  context.ready();
  const terminal = await openTerminal(context);
  terminal.options.onClose(new SessionError("connection"));
  const snapshotText = JSON.stringify(context.latest(), (_key, value) => typeof value === "bigint" ? value.toString() : value);
  assert.equal(snapshotText.includes("runId"), false);
  assert.equal(snapshotText.includes("sessionId"), false);
  assert.equal(snapshotText.includes("lease"), false);
  assert.equal(snapshotText.includes("same session"), false);
  assert.equal(context.latest().terminal, undefined);
});

test("post-target stale closure cannot re-enter discovery retry", async () => {
  const context = terminalHarness();
  context.controller.start();
  context.ready();
  await openTerminal(context);
  context.handleOptions().onClose(new SessionError("stale"));
  await flush();
  assert.equal(context.latest().selectedAgent, undefined);
  assert.equal(context.latest().error.code, "stale");
  assert.equal(context.sessionCloses(), 0);
  context.clientOptions().onState(stateAt(43));
  await flush();
  assert.equal(context.targetGates.length, 1);
});

async function remountSurface(context) {
  const token = {};
  const writes = [];
  context.controller.beginTerminalSurface(token);
  context.controller.setTerminalSurface(token, { write: async (bytes) => { writes.push(bytes); }, abort: () => {} });
  await flush();
  return { token, writes };
}

test("a server replay reset recovers in place instead of surfacing an error", async () => {
  const context = terminalHarness();
  context.controller.start();
  context.ready();
  await openTerminal(context);
  const resolvesBefore = context.targetGates.length;
  const versionBefore = context.latest().terminal.surfaceVersion;

  context.handleOptions().onReset({ sessionId: "31".repeat(16), floor: 5n, head: 9n });
  await flush();
  let view = context.latest();
  assert.notEqual(view.selectedAgent, undefined, "reset must keep the terminal selection");
  assert.equal(view.error, undefined, "reset is not an error");
  assert.equal(view.terminal.resets, 1);
  assert.equal(view.terminal.phase, "idle", "old controller gone, awaiting the remounted display");
  assert.equal(view.terminal.surfaceVersion, versionBefore + 1, "display remounts to clear the stale scrollback");
  assert.equal(context.sessionCloses(), 0);

  await remountSurface(context);
  assert.equal(context.targetGates.length, resolvesBefore + 1, "a fresh controller re-resolves the target");
  context.targetGates.at(-1).resolve(target);
  await flush();
  view = context.latest();
  assert.equal(view.terminal.phase, "ready");
  assert.equal(view.terminal.resets, 1, "the banner state survives the successful re-replay");
});

test("a reset while holding control recovers and re-acquires through the normal path", async () => {
  const context = terminalHarness();
  context.controller.start();
  context.ready();
  await openTerminal(context);
  assert.equal(context.latest().terminal.writable, true);

  context.handleOptions().onReset({ sessionId: "31".repeat(16), floor: 5n, head: 9n });
  await flush();
  assert.equal(context.latest().terminal.writable, false, "reset revokes local control honestly");

  await remountSurface(context);
  context.targetGates.at(-1).resolve(target);
  await flush();
  const view = context.latest();
  assert.equal(view.terminal.phase, "ready");
  assert.equal(view.terminal.writable, true, "control returns only through the normal acquire path");
  assert.equal(view.terminal.resets, 1);
});

test("a reset racing buffered input drops the input without replay or error", async () => {
  const context = terminalHarness();
  context.controller.start();
  context.ready();
  const live = await openTerminal(context);
  context.controller.sendTerminalText(live.token, "racing");
  context.handleOptions().onReset({ sessionId: "31".repeat(16), floor: 5n, head: 9n });
  await flush();
  assert.equal(context.latest().error, undefined);
  const inputsBeforeRemount = context.calls.filter((call) => call.kind === "input").length;

  await remountSurface(context);
  context.targetGates.at(-1).resolve(target);
  await flush();
  assert.equal(context.latest().terminal.phase, "ready");
  const inputsAfter = context.calls.filter((call) => call.kind === "input").length;
  assert.equal(inputsAfter, inputsBeforeRemount, "recovery never replays input the reset dropped");
});

test("a reset storm is bounded: past three recoveries the stale teardown stands", async () => {
  const context = terminalHarness();
  context.setAttachReset(true);
  context.controller.start();
  context.ready();
  context.controller.selectAgent(agent);
  for (let attempt = 0; attempt < 4; attempt += 1) {
    await remountSurface(context);
    const gate = context.targetGates.at(-1);
    if (gate === undefined) break;
    gate.resolve(target);
    await flush();
  }
  const view = context.latest();
  assert.equal(view.selectedAgent, undefined, "past the bound the ordinary teardown stands");
  assert.equal(view.error?.code, "stale");
});
