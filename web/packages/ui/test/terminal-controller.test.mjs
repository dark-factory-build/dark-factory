import assert from "node:assert/strict";
import test from "node:test";
import { SessionError } from "@dark-factory/client";
import { MAX_PENDING_INPUT_BYTES, TerminalController } from "../dist/src/index.js";

const target = Object.freeze({});

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((accept, refuse) => { resolve = accept; reject = refuse; });
  return { promise, resolve, reject };
}

function tick() { return new Promise((resolve) => setTimeout(resolve, 0)); }

function harness({ resolvedTarget = target } = {}) {
  const snapshots = [];
  const writes = [];
  const inputCalls = [];
  const resizeCalls = [];
  let callbacks;
  let attachGate = deferred();
  let acquireGate = deferred();
  let detached = 0;
  const surfaceGate = deferred();
  let surfacePending = false;
  let surfaceAborts = 0;
  const handle = {
    attach: () => attachGate.promise,
    acquireInput: () => acquireGate.promise,
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
    detach: async () => {
      detached += 1;
      callbacks.onClose?.();
    },
    get writable() { return true; },
  };
  const session = {
    resolveAgentTerminal: async () => typeof resolvedTarget === "function" ? resolvedTarget() : resolvedTarget,
    openTerminal: (_target, options) => {
      callbacks = options;
      return handle;
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
    onChange: (snapshot) => snapshots.push(snapshot),
  });
  return {
    controller,
    snapshots,
    writes,
    inputCalls,
    resizeCalls,
    handle,
    callbacks: () => callbacks,
    attachGate: () => attachGate,
    acquireGate: () => acquireGate,
    resetAttach: () => { attachGate = deferred(); },
    resetAcquire: () => { acquireGate = deferred(); },
    surfaceGate,
    surfaceAborts: () => surfaceAborts,
    detached: () => detached,
  };
}

async function ready(context) {
  context.controller.start();
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

test("output ACK chronology waits for the display write promise", async () => {
  const context = harness();
  await ready(context);
  const output = context.callbacks().onOutput({ sequence: 0n, payload: new Uint8Array([4, 5]) });
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
});

test("input is single-flight and bounded without replaying partial delivery", async () => {
  const context = harness();
  await ready(context);
  assert.equal(context.controller.sendText("a"), true);
  assert.equal(context.controller.sendText("b"), true);
  assert.equal(context.inputCalls.length, 1);
  context.inputCalls[0].result.resolve({ status: "accepted", accepted_bytes: 1n });
  await tick();
  assert.equal(context.inputCalls.length, 2);
  context.inputCalls[1].result.resolve({ status: "partial", accepted_bytes: 1n });
  await context.controller.close();
  assert.equal(context.detached(), 1);
  assert.equal(context.controller.snapshot.writable, false);

  const overflow = harness();
  await ready(overflow);
  assert.equal(overflow.controller.sendText("a"), true);
  assert.equal(overflow.controller.sendInput(new Uint8Array(MAX_PENDING_INPUT_BYTES)), false);
  overflow.inputCalls[0].result.resolve({ status: "accepted", accepted_bytes: 1n });
  await overflow.controller.close();
  assert.equal(overflow.detached(), 1);
  assert.equal(overflow.controller.snapshot.error.code, "too_large");
});

test("resize coalesces to the latest bounded dimensions after the current effect", async () => {
  const context = harness();
  await ready(context);
  assert.equal(context.controller.sendText("a"), true);
  assert.equal(context.controller.resize(24, 80), true);
  assert.equal(context.controller.resize(30, 100), true);
  assert.equal(context.resizeCalls.length, 0);
  context.inputCalls[0].result.resolve({ status: "accepted", accepted_bytes: 1n });
  await tick();
  assert.deepEqual(context.resizeCalls.map(({ rows, cols }) => [rows, cols]), [[30, 100]]);
  context.resizeCalls[0].result.resolve({ rows: 30, cols: 100 });
  await tick();
});

test("close waits for attach or output, detaches exactly once, and does not stop the provider", async () => {
  const attaching = harness();
  attaching.controller.start();
  await tick();
  const firstClose = attaching.controller.close();
  assert.strictEqual(firstClose, attaching.controller.close());
  attaching.attachGate().resolve({ sessionId: "22".repeat(16), floor: 0n, head: 0n, acknowledgedSequence: 0n, maxUnackedBytes: 65536n });
  await firstClose;
  assert.equal(attaching.detached(), 1);

  const output = harness();
  await ready(output);
  const outputPending = output.callbacks().onOutput({ sequence: 0n, payload: new Uint8Array([1]) });
  await tick();
  const close = output.controller.close();
  assert.equal(output.surfaceAborts(), 1);
  assert.equal(output.detached(), 0);
  await assert.rejects(outputPending, /surface disposed/);
  await close;
  assert.equal(output.detached(), 1);
});

test("null or stale discovery cannot open a handle", async () => {
  for (const resolvedTarget of [null, () => Promise.reject(new SessionError("stale"))]) {
    const context = harness({ resolvedTarget });
    context.controller.start();
    await tick();
    await context.controller.close();
    assert.equal(context.detached(), 0);
    assert.equal(context.controller.snapshot.phase, "closed");
    assert.equal(context.controller.snapshot.error.code, resolvedTarget === null ? "not_found" : "stale");
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
