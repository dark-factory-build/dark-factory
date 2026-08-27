import assert from "node:assert/strict";
import test from "node:test";
import {
  decodeClientControl,
  decodeServerControl,
  encodeTerminalAttached,
  encodeTerminalLeaseResult,
  SessionError,
} from "@dark-factory/client";
import { createTerminalHandle } from "../../client/dist/src/terminal_session.js";
import { MAX_PENDING_INPUT_BYTES, TerminalController } from "../dist/src/terminal-controller.js";

const target = Object.freeze({});

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((accept, refuse) => { resolve = accept; reject = refuse; });
  return { promise, resolve, reject };
}

function tick() { return new Promise((resolve) => setTimeout(resolve, 0)); }

function harness({
  resolvedTarget = target,
  onChange = () => {},
  closeOnOpen = false,
  closeOnAcquire = false,
} = {}) {
  const snapshots = [];
  const writes = [];
  const inputCalls = [];
  const resizeCalls = [];
  const targetGate = deferred();
  let callbacks;
  let attachGate = deferred();
  let acquireGate = deferred();
  let sessionCloses = 0;
  let handleClosed = false;
  let targetPending = false;
  let attachPending = false;
  let acquirePending = false;
  const surfaceGate = deferred();
  let surfacePending = false;
  let surfaceAborts = 0;
  const closePending = (gate) => gate.reject(new SessionError("closed"));
  const handle = {
    attach: () => { attachPending = true; return attachGate.promise; },
    acquireInput: () => {
      acquirePending = true;
      if (closeOnAcquire) callbacks.onClose?.(new SessionError("closed"));
      return acquireGate.promise;
    },
    sendInput: (bytes) => {
      const call = { bytes, result: deferred() };
      inputCalls.push(call);
      return call.result.promise;
    },
    resize: (rows, cols) => {
      const call = { rows, cols, result: deferred() };
      resizeCalls.push(call);
      return call.result.promise;
    },
    get writable() { return !handleClosed; },
  };
  const session = {
    resolveAgentTerminal: () => {
      if (typeof resolvedTarget === "function") return resolvedTarget();
      targetPending = true;
      return targetGate.promise;
    },
    openTerminal: (_target, options) => {
      callbacks = options;
      if (closeOnOpen) callbacks.onClose?.(new SessionError("closed"));
      return handle;
    },
    close: () => {
      sessionCloses += 1;
      handleClosed = true;
      if (targetPending) closePending(targetGate);
      if (attachPending) closePending(attachGate);
      if (acquirePending) closePending(acquireGate);
      for (const call of inputCalls) closePending(call.result);
      for (const call of resizeCalls) closePending(call.result);
      callbacks?.onClose?.(new SessionError("closed"));
    },
  };
  const controller = new TerminalController({
    session,
    agentId: "11".repeat(16),
    expectedAgentRevision: 4n,
    expectedHead: 9n,
    surface: {
      write: (payload) => {
        writes.push(payload);
        surfacePending = true;
        return surfaceGate.promise;
      },
      abort: () => {
        surfaceAborts += 1;
        if (surfacePending) {
          surfacePending = false;
          surfaceGate.reject(new Error("surface disposed"));
        }
      },
    },
    onChange: (snapshot) => { snapshots.push(snapshot); onChange(snapshot); },
  });
  return {
    controller,
    snapshots,
    writes,
    inputCalls,
    resizeCalls,
    callbacks: () => callbacks,
    targetGate,
    attachGate: () => attachGate,
    acquireGate: () => acquireGate,
    resetAttach: () => { attachGate = deferred(); },
    resetAcquire: () => { acquireGate = deferred(); },
    surfaceGate,
    surfaceAborts: () => surfaceAborts,
    sessionCloses: () => sessionCloses,
  };
}

async function ready(context) {
  context.controller.start();
  await tick();
  context.targetGate.resolve(target);
  await tick();
  assert.equal(context.snapshots.at(-1).phase, "attaching");
  context.attachGate().resolve({ sessionId: "22".repeat(16), floor: 0n, head: 0n, acknowledgedSequence: 0n, maxUnackedBytes: 65536n });
  await tick();
  assert.equal(context.snapshots.at(-1).phase, "acquiring");
  context.acquireGate().resolve({ generation: 1n });
  await tick();
  assert.equal(context.controller.snapshot.phase, "ready");
  assert.equal(context.controller.snapshot.writable, true);
}

test("attach precedes lease and input is refused before writable", async () => {
  const context = harness();
  context.controller.start();
  await tick();
  context.targetGate.resolve(target);
  await tick();
  assert.equal(context.snapshots.at(-1).phase, "attaching");
  assert.equal(context.controller.sendText("before"), false);
  assert.equal(context.inputCalls.length, 0);
  context.attachGate().resolve({ sessionId: "22".repeat(16), floor: 0n, head: 0n, acknowledgedSequence: 0n, maxUnackedBytes: 65536n });
  await tick();
  assert.equal(context.snapshots.at(-1).phase, "acquiring");
  assert.equal(context.inputCalls.length, 0);
  context.acquireGate().resolve({ generation: 1n });
  await tick();
  assert.equal(context.controller.sendText("after"), true);
  assert.equal(context.inputCalls.length, 1);
});

test("output waits for the display write and copies payload bytes", async () => {
  const context = harness();
  await ready(context);
  const source = new Uint8Array([4, 5]);
  const output = context.callbacks().onOutput({ sequence: 0n, payload: source });
  source[0] = 99;
  await tick();
  assert.deepEqual([...context.writes[0]], [4, 5]);
  let completed = false;
  void output.then(() => { completed = true; });
  await tick();
  assert.equal(completed, false);
  context.surfaceGate.resolve();
  await output;
  assert.equal(completed, true);
});

test("post-close output rejects without writing and cannot ACK", async () => {
  const context = harness();
  await ready(context);
  await context.controller.close();
  await assert.rejects(context.callbacks().onOutput({ sequence: 0n, payload: new Uint8Array([1]) }), (error) => error.code === "closed");
  assert.equal(context.writes.length, 0);
  assert.equal(context.sessionCloses(), 1);
});

test("real TerminalHandle sends no ACK when controller closes during display output", async () => {
  const sent = [];
  const outputGate = deferred();
  let outputPending = false;
  let handle;
  let responseNumber = 0;
  const session = {
    resolveAgentTerminal: async () => target,
    openTerminal: (_target, options) => {
      handle = createTerminalHandle(
        { runId: "33".repeat(16), sessionId: "44".repeat(16), runRevision: 1n, sessionRevision: 1n },
        options,
        (requestId, payload) => sent.push({ requestId, payload }),
        () => `${String(++responseNumber).padStart(2, "0")}`.repeat(16),
        () => {},
        { now: 100_000, setTimeout: () => 1, clearTimeout: () => {} },
        true,
      );
      return handle;
    },
    close: () => handle?.terminate(new SessionError("closed")),
  };
  const controller = new TerminalController({
    session,
    agentId: "55".repeat(16),
    expectedAgentRevision: 1n,
    expectedHead: 1n,
    surface: {
      write: () => { outputPending = true; return outputGate.promise; },
      abort: () => { if (outputPending) outputGate.reject(new Error("surface disposed")); },
    },
    onChange: () => {},
  });
  controller.start();
  await tick();
  let request = decodeClientControl(sent.at(-1).payload);
  handle.receive(decodeServerControl(encodeTerminalAttached(request.id, {
    session_id: "44".repeat(16), floor: 0n, head: 0n, acknowledged_sequence: 0n, max_unacked_bytes: 65536n,
  })));
  await tick();
  request = decodeClientControl(sent.at(-1).payload);
  handle.receive(decodeServerControl(encodeTerminalLeaseResult(request.id, {
    operation: "acquired", run_id: "33".repeat(16), session_id: "44".repeat(16), generation: 1n,
    expires_at_ms: 200_000n, last_input_sequence: 0n, run_revision: 1n, session_revision: 1n,
  })));
  await tick();
  assert.equal(controller.snapshot.writable, true);
  const receiving = handle.receiveBinary({ direction: "output", sessionId: new Uint8Array(16).fill(0x44), sequence: 0n, leaseGeneration: 0n, payload: new Uint8Array([1]) });
  await tick();
  await controller.close();
  assert.equal(await receiving, true);
  assert.equal(sent.filter(({ payload }) => typeof payload === "string" && decodeClientControl(payload).type === "TERMINAL_ACK").length, 0);
});

test("text uses UTF-8 and binary preserves each 0..255 byte", async () => {
  const context = harness();
  await ready(context);
  assert.equal(context.controller.sendText("é"), true);
  assert.deepEqual([...context.inputCalls[0].bytes], [0xc3, 0xa9]);
  context.inputCalls[0].result.resolve({ status: "accepted", accepted_bytes: 2n });
  await tick();
  assert.equal(context.controller.sendBinary(String.fromCharCode(0, 255)), true);
  assert.deepEqual([...context.inputCalls[1].bytes], [0, 255]);
  context.inputCalls[1].result.resolve({ status: "accepted", accepted_bytes: 2n });
  await tick();

  const copy = harness();
  await ready(copy);
  const source = new Uint8Array([7]);
  assert.equal(copy.controller.sendInput(source), true);
  source[0] = 99;
  assert.equal(copy.inputCalls[0].bytes[0], 7);
  copy.inputCalls[0].result.resolve({ status: "accepted", accepted_bytes: 1n });
  await copy.controller.close();
});

test("input is one bounded byte buffer, chunked at the protocol limit", async () => {
  const context = harness();
  await ready(context);
  for (let index = 0; index < MAX_PENDING_INPUT_BYTES / 8192; index += 1) {
    assert.equal(context.controller.sendInput(new Uint8Array(8192)), true);
  }
  assert.equal(context.inputCalls.length, 1);
  assert.equal(context.inputCalls[0].bytes.length, 8192);
  assert.equal(context.controller.sendInput(new Uint8Array(1)), false);
  assert.equal(context.controller.snapshot.error.code, "too_large");
  assert.equal(context.sessionCloses(), 1);
  assert.equal(context.controller.sendInput(new Uint8Array(1)), false);
});

test("resize runs after at most one input and keeps only its latest value", async () => {
  const context = harness();
  await ready(context);
  assert.equal(context.controller.sendText("a"), true);
  assert.equal(context.controller.sendText("b"), true);
  assert.equal(context.controller.resize(24, 80), true);
  assert.equal(context.controller.resize(30, 100), true);
  assert.equal(context.inputCalls.length, 1);
  context.inputCalls[0].result.resolve({ status: "accepted", accepted_bytes: 1n });
  await tick();
  assert.deepEqual(context.resizeCalls.map(({ rows, cols }) => [rows, cols]), [[30, 100]]);
  context.resizeCalls[0].result.resolve({ rows: 30, cols: 100 });
  await tick();
  assert.equal(context.inputCalls.length, 2);
});

test("close is synchronous, once-only, and fences target, attach, acquire, input, resize and output", async () => {
  for (const stage of ["target", "attach", "acquire", "input", "resize", "output"]) {
    const context = harness();
    let output;
    if (stage === "target") {
      context.controller.start();
    } else {
      await ready(context);
      if (stage === "input") {
        context.controller.sendText("a");
      } else if (stage === "resize") {
        context.controller.resize(20, 80);
      } else if (stage === "output") {
        output = context.callbacks().onOutput({ sequence: 0n, payload: new Uint8Array([1]) });
      }
    }
    const close = context.controller.close();
    assert.strictEqual(close, context.controller.close());
    await close;
    assert.equal(context.controller.snapshot.phase, "closed", stage);
    assert.equal(context.controller.snapshot.writable, false, stage);
    assert.equal(context.sessionCloses(), 1, stage);
    assert.equal(context.surfaceAborts(), 1, stage);
    if (output !== undefined) await assert.rejects(output, (error) => error.code === "closed");
  }
});

test("synchronous handle close and onChange close cannot launch or restore authority", async () => {
  const ended = harness({ closeOnOpen: true });
  ended.controller.start();
  await tick();
  ended.targetGate.resolve(target);
  await tick();
  assert.equal(ended.controller.snapshot.phase, "closed");
  assert.equal(ended.controller.snapshot.writable, false);
  assert.equal(ended.controller.sendText("late"), false);
  await ended.controller.close();
  assert.equal(ended.sessionCloses(), 1);

  let reentrant;
  const attaching = harness({ onChange: (snapshot) => {
    if (snapshot.phase === "attaching") void reentrant.controller.close();
  } });
  reentrant = attaching;
  attaching.controller.start();
  await tick();
  attaching.targetGate.resolve(target);
  await tick();
  assert.equal(attaching.controller.snapshot.phase, "closed");
  assert.equal(attaching.sessionCloses(), 1);
});

test("null or stale discovery cannot open a handle", async () => {
  for (const resolvedTarget of [null, () => Promise.reject(new SessionError("stale"))]) {
    const context = harness({ resolvedTarget });
    context.controller.start();
    await tick();
    await context.controller.close();
    assert.equal(context.controller.snapshot.phase, "closed");
    assert.equal(context.sessionCloses(), 1);
  }
});

test("reset, exit and handle close revoke writable authority", async () => {
  for (const event of ["onReset", "onExit", "onClose"]) {
    const context = harness();
    await ready(context);
    context.callbacks()[event]?.(event === "onClose" ? new SessionError("connection") : undefined);
    assert.equal(context.controller.snapshot.phase, "closed", event);
    assert.equal(context.controller.snapshot.writable, false, event);
    assert.equal(context.controller.sendText("late"), false, event);
  }
});
