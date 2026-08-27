import assert from "node:assert/strict";
import test from "node:test";
import { SessionError } from "@dark-factory/client";
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

function terminalHarness({ closeStatus = false, fail } = {}) {
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
      if (fail === "open") throw new SessionError("connection");
      handleOptions = callbacks;
      const handle = {
        attach: async () => { calls.push({ kind: "attach" }); if (fail === "attach") throw new SessionError("connection"); return { sessionId: "31".repeat(16), floor: 0n, head: 0n, acknowledgedSequence: 0n, maxUnackedBytes: 65536n }; },
        acquireInput: async () => { calls.push({ kind: "acquire" }); if (fail === "acquire") throw new SessionError("connection"); return { generation: 1n }; },
        sendInput: async (bytes) => { calls.push({ kind: "input", bytes }); if (fail === "input") throw new SessionError("connection"); return { status: "accepted", accepted_bytes: BigInt(bytes.length) }; },
        resize: async (rows, cols) => { calls.push({ kind: "resize", rows, cols }); if (fail === "resize") throw new SessionError("connection"); return { rows, cols }; },
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

test("fatal discovery and terminal failures disarm selection before reconnect can rediscover", async () => {
  for (const failure of ["null", "throw", "open", "attach", "acquire", "input", "resize", "surface"]) {
    const context = terminalHarness({ closeStatus: true, fail: failure === "null" || failure === "throw" ? undefined : failure });
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
    if (failure === "null") context.targetGates[0].resolve(null);
    else if (failure === "throw") context.targetGates[0].reject(new SessionError("connection"));
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

test("a fresh explicit selection can start one new terminal attempt after a fatal failure", async () => {
  const context = terminalHarness({ closeStatus: true });
  context.controller.start();
  context.ready();
  context.controller.selectAgent(agent);
  const token = {};
  context.controller.beginTerminalSurface(token);
  context.controller.setTerminalSurface(token, { write: async () => {}, abort: () => {} });
  await flush();
  context.targetGates[0].resolve(null);
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

test("selection replaces the old controller before resolving the new public agent and fences stale callbacks", async () => {
  const context = terminalHarness();
  context.controller.start();
  context.ready();
  const first = await openTerminal(context, agent);
  const oldOptions = first.options;
  context.controller.selectAgent(secondAgent);
  assert.equal(context.sessionCloses(), 1);
  assert.equal(context.latest().selectedAgent.id, secondAgent.id);
  assert.equal(context.targetGates.length, 1);

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

test("explicit terminal teardown closes the exact session once and rejects later surface intents", async () => {
  const context = terminalHarness();
  context.controller.start();
  context.ready();
  const terminal = await openTerminal(context);
  context.controller.clearAgentTerminal();
  context.controller.clearAgentTerminal();
  assert.equal(context.sessionCloses(), 1);
  terminal.options.onExit();
  assert.equal(context.latest().selectedAgent, undefined);
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
