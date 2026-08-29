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
  onSessionClose = () => {},
  closeOnOpen = false,
  closeOnAcquire = false,
  releaseBusy = 0,
  attachImpl = undefined,
  attachRetryDelay = undefined,
} = {}) {
  const snapshots = [];
  const writes = [];
  const inputCalls = [];
  const resizeCalls = [];
  const targetGate = deferred();
  let callbacks;
  let attachGate = deferred();
  let acquireGate = deferred();
  let releaseGate = deferred();
  let sessionCloses = 0;
  let handleClosed = false;
  let targetPending = false;
  let attachPending = false;
  let acquirePending = false;
  let releasePending = false;
  let acquireCalls = 0;
  let releaseCalls = 0;
  let releaseBusyRemaining = releaseBusy;
  const surfaceGate = deferred();
  let surfacePending = false;
  let surfaceAborts = 0;
  const closePending = (gate) => gate.reject(new SessionError("closed"));
  const handle = {
    attach: () => {
      if (attachImpl !== undefined) return attachImpl();
      attachPending = true;
      return attachGate.promise;
    },
    acquireInput: () => {
      acquirePending = true;
      acquireCalls += 1;
      if (closeOnAcquire) callbacks.onClose?.(new SessionError("closed"));
      return acquireGate.promise;
    },
    releaseInput: () => {
      releaseCalls += 1;
      if (releaseBusyRemaining > 0) {
        releaseBusyRemaining -= 1;
        return Promise.reject(new SessionError("invalid_request"));
      }
      releasePending = true;
      return releaseGate.promise;
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
      if (releasePending) closePending(releaseGate);
      for (const call of inputCalls) closePending(call.result);
      for (const call of resizeCalls) closePending(call.result);
      callbacks?.onClose?.(new SessionError("closed"));
      onSessionClose();
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
    ...(attachRetryDelay === undefined ? {} : { attachRetryDelay }),
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
    resetAcquire: () => { acquireGate = deferred(); acquirePending = false; },
    releaseGate: () => releaseGate,
    resetRelease: () => { releaseGate = deferred(); releasePending = false; },
    acquireCalls: () => acquireCalls,
    releaseCalls: () => releaseCalls,
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

test("handle end aborts a pending display and fences a post-await output", async () => {
  for (const event of ["onReset", "onExit", "onClose"]) {
    const context = harness();
    await ready(context);
    const pending = context.callbacks().onOutput({ sequence: 0n, payload: new Uint8Array([1]) });
    await tick();
    context.callbacks()[event]?.(event === "onClose" ? new SessionError("connection") : undefined);
    await assert.rejects(pending, (error) => error.code === "closed");
    assert.equal(context.surfaceAborts(), 1, event);
    await assert.rejects(context.callbacks().onOutput({ sequence: 1n, payload: new Uint8Array([2]) }), (error) => error.code === "closed");
  }

  const postAwait = harness();
  await ready(postAwait);
  const output = postAwait.callbacks().onOutput({ sequence: 0n, payload: new Uint8Array([3]) });
  await tick();
  postAwait.surfaceGate.resolve();
  postAwait.callbacks().onReset();
  await assert.rejects(output, (error) => error.code === "closed");
  assert.equal(postAwait.surfaceAborts(), 1);
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

test("real TerminalHandle close during display output aborts without closing the session or ACKing", async () => {
  const sent = [];
  const outputGate = deferred();
  let handle;
  let responseNumber = 0;
  let sessionCloses = 0;
  const session = {
    resolveAgentTerminal: async () => target,
    openTerminal: (_target, options) => {
      handle = createTerminalHandle(
        { runId: "66".repeat(16), sessionId: "77".repeat(16), runRevision: 1n, sessionRevision: 1n },
        options,
        (requestId, payload) => sent.push({ requestId, payload }),
        () => `${String(++responseNumber).padStart(2, "0")}`.repeat(16),
        () => {},
        { now: 100_000, setTimeout: () => 1, clearTimeout: () => {} },
        true,
      );
      return handle;
    },
    close: () => { sessionCloses += 1; handle?.terminate(new SessionError("closed")); },
  };
  let outputPending = false;
  const controller = new TerminalController({
    session,
    agentId: "88".repeat(16),
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
    session_id: "77".repeat(16), floor: 0n, head: 0n, acknowledged_sequence: 0n, max_unacked_bytes: 65536n,
  })));
  await tick();
  request = decodeClientControl(sent.at(-1).payload);
  handle.receive(decodeServerControl(encodeTerminalLeaseResult(request.id, {
    operation: "acquired", run_id: "66".repeat(16), session_id: "77".repeat(16), generation: 1n,
    expires_at_ms: 200_000n, last_input_sequence: 0n, run_revision: 1n, session_revision: 1n,
  })));
  await tick();
  const receiving = handle.receiveBinary({ direction: "output", sessionId: new Uint8Array(16).fill(0x77), sequence: 0n, leaseGeneration: 0n, payload: new Uint8Array([1]) });
  await tick();
  handle.terminate(new SessionError("closed"));
  assert.equal(await receiving, true);
  assert.equal(sessionCloses, 0);
  assert.equal(sent.filter(({ payload }) => typeof payload === "string" && decodeClientControl(payload).type === "TERMINAL_ACK").length, 0);
  await controller.close();
  assert.equal(sessionCloses, 1);
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

test("fatal closing state is published before synchronous session close reentry", async () => {
  const events = [];
  let sessionCloseObserved = false;
  const context = harness({
    onChange: (snapshot) => events.push({ phase: snapshot.phase, sessionCloseObserved }),
    onSessionClose: () => { sessionCloseObserved = true; events.push({ event: "session-close" }); },
  });
  context.controller.start();
  await tick();
  context.targetGate.resolve(null);
  await tick();
  const closing = events.findIndex((event) => event.phase === "closing");
  const sessionClose = events.findIndex((event) => event.event === "session-close");
  assert.notEqual(closing, -1);
  assert.notEqual(sessionClose, -1);
  assert.ok(closing < sessionClose);
  assert.equal(events[closing].sessionCloseObserved, false);
  assert.equal(context.sessionCloses(), 1);
});

test("rejected input remains ready and writable without closing the session", async () => {
  const context = harness();
  await ready(context);
  assert.equal(context.controller.sendText("first"), true);
  context.inputCalls[0].result.resolve({ status: "rejected", accepted_bytes: 0n });
  await tick();
  assert.equal(context.controller.snapshot.phase, "ready");
  assert.equal(context.controller.snapshot.writable, true);
  assert.equal(context.sessionCloses(), 0);
  assert.equal(context.controller.sendText("second"), true);
  assert.equal(context.inputCalls.length, 2);
  context.inputCalls[1].result.resolve({ status: "accepted", accepted_bytes: 6n });
  await context.controller.close();
});

test("lease refusal keeps an attached observer ready and read-only", async () => {
  const context = harness();
  context.controller.start();
  await tick();
  context.targetGate.resolve(target);
  await tick();
  context.attachGate().resolve({ sessionId: "22".repeat(16), floor: 0n, head: 0n, acknowledgedSequence: 0n, maxUnackedBytes: 65536n });
  await tick();
  context.acquireGate().reject(new SessionError("invalid_request"));
  await tick();
  assert.equal(context.controller.snapshot.phase, "ready");
  assert.equal(context.controller.snapshot.writable, false);
  assert.equal(context.sessionCloses(), 0);
  const output = context.callbacks().onOutput({ sequence: 0n, payload: new Uint8Array([7]) });
  await tick();
  context.surfaceGate.resolve();
  await output;
  assert.deepEqual([...context.writes[0]], [7]);
  await context.controller.close();
});

test("take control after lease refusal acquires exactly once and reports its pending state", async () => {
  const context = harness();
  context.controller.start();
  await tick();
  context.targetGate.resolve(target);
  await tick();
  context.attachGate().resolve({ sessionId: "22".repeat(16), floor: 0n, head: 0n, acknowledgedSequence: 0n, maxUnackedBytes: 65536n });
  await tick();
  context.acquireGate().reject(new SessionError("invalid_request"));
  await tick();
  assert.equal(context.controller.snapshot.writable, false);
  assert.equal(context.acquireCalls(), 1);

  context.resetAcquire();
  assert.equal(context.controller.takeControl(), true);
  assert.equal(context.controller.snapshot.leaseOperation, "taking");
  assert.equal(context.controller.takeControl(), false, "one lease operation at a time");
  context.acquireGate().resolve({ generation: 2n });
  await tick();
  assert.equal(context.controller.snapshot.writable, true);
  assert.equal(context.controller.snapshot.leaseOperation, "none");
  assert.equal(context.controller.snapshot.error, undefined);
  assert.equal(context.acquireCalls(), 2);
  assert.equal(context.controller.takeControl(), false, "already writable");
  await context.controller.close();
});

test("take control failure keeps the read-only observer alive and retryable", async () => {
  const context = harness();
  context.controller.start();
  await tick();
  context.targetGate.resolve(target);
  await tick();
  context.attachGate().resolve({ sessionId: "22".repeat(16), floor: 0n, head: 0n, acknowledgedSequence: 0n, maxUnackedBytes: 65536n });
  await tick();
  context.acquireGate().reject(new SessionError("invalid_request"));
  await tick();
  context.resetAcquire();
  assert.equal(context.controller.takeControl(), true);
  context.acquireGate().reject(new SessionError("invalid_request"));
  await tick();
  assert.equal(context.controller.snapshot.phase, "ready");
  assert.equal(context.controller.snapshot.writable, false);
  assert.equal(context.controller.snapshot.leaseOperation, "none");
  assert.equal(context.controller.snapshot.error.code, "invalid_request");
  assert.equal(context.sessionCloses(), 0);
  const output = context.callbacks().onOutput({ sequence: 0n, payload: new Uint8Array([9]) });
  await tick();
  context.surfaceGate.resolve();
  await output;
  assert.deepEqual([...context.writes[0]], [9]);
  context.resetAcquire();
  assert.equal(context.controller.takeControl(), true, "failure leaves the retry live");
  await context.controller.close();
});

test("hand back stops input before releasing and stays an attached observer", async () => {
  const context = harness();
  await ready(context);
  assert.equal(context.controller.sendText("ls\n"), true);
  await tick();
  assert.equal(context.inputCalls.length, 1);
  assert.equal(context.controller.handBack(), true);
  assert.equal(context.controller.snapshot.writable, false, "input authority ends before the release settles");
  assert.equal(context.controller.snapshot.leaseOperation, "releasing");
  assert.equal(context.controller.sendText("blocked\n"), false);
  assert.equal(context.controller.handBack(), false, "one lease operation at a time");
  assert.equal(context.releaseCalls(), 0, "release is not sent while input is still draining");
  context.inputCalls[0].result.resolve({ status: "accepted", acceptedBytes: 3n });
  await tick();
  await tick();
  assert.equal(context.releaseCalls(), 1, "release waits for the drained input effect");
  context.releaseGate().resolve({ released: true });
  await tick();
  assert.equal(context.controller.snapshot.phase, "ready");
  assert.equal(context.controller.snapshot.writable, false);
  assert.equal(context.controller.snapshot.leaseOperation, "none");
  assert.equal(context.sessionCloses(), 0);
  context.resetAcquire();
  assert.equal(context.controller.takeControl(), true, "control can be taken again after handing back");
  await context.controller.close();
});

test("a failed hand-back release is authority uncertainty and closes", async () => {
  const context = harness();
  await ready(context);
  assert.equal(context.controller.handBack(), true);
  // A sent release that errors fatals the real client handle
  // (terminal_session.ts settleError: kind "release" -> #fatal), so the
  // rejection always arrives with a dead handle. Model both effects.
  context.callbacks().onClose(new SessionError("connection"));
  context.releaseGate().reject(new SessionError("connection"));
  await tick();
  assert.equal(context.controller.snapshot.phase, "closed");
  assert.equal(context.controller.snapshot.writable, false);
  assert.equal(context.controller.snapshot.error.code, "connection");
});

test("stale generations fence late lease results after close", async () => {
  const context = harness();
  context.controller.start();
  await tick();
  context.targetGate.resolve(target);
  await tick();
  context.attachGate().resolve({ sessionId: "22".repeat(16), floor: 0n, head: 0n, acknowledgedSequence: 0n, maxUnackedBytes: 65536n });
  await tick();
  context.acquireGate().reject(new SessionError("invalid_request"));
  await tick();
  context.resetAcquire();
  assert.equal(context.controller.takeControl(), true);
  await context.controller.close();
  const before = context.controller.snapshot;
  context.acquireGate().resolve({ generation: 9n });
  await tick();
  assert.deepEqual(context.controller.snapshot, before, "a late lease result cannot mutate a closed controller");
  assert.equal(context.controller.snapshot.phase, "closed");
  assert.equal(context.controller.snapshot.writable, false);
});

test("a busy pre-send release rejection is retryable and keeps control, not uncertainty", async () => {
  const context = harness({ releaseBusy: 1 });
  await ready(context);
  assert.equal(context.controller.handBack(), true);
  await tick();
  await tick();
  assert.equal(context.releaseCalls(), 1);
  assert.equal(context.controller.snapshot.phase, "ready", "a never-sent release cannot close the terminal");
  assert.equal(context.controller.snapshot.writable, true, "the lease is still ours, so control returns");
  assert.equal(context.controller.snapshot.leaseOperation, "none");
  assert.equal(context.controller.snapshot.error, undefined);
  assert.equal(context.sessionCloses(), 0);
  assert.equal(context.controller.sendText("still mine\n"), true, "input continues after the busy rejection");
  await tick();
  context.inputCalls[0].result.resolve({ status: "accepted", acceptedBytes: 10n });
  await tick();
  assert.equal(context.controller.handBack(), true, "the release retries once the client is free");
  await tick();
  await tick();
  assert.equal(context.releaseCalls(), 2);
  context.releaseGate().resolve({ released: true });
  await tick();
  assert.equal(context.controller.snapshot.phase, "ready");
  assert.equal(context.controller.snapshot.writable, false);
  assert.equal(context.controller.snapshot.leaseOperation, "none");
  assert.equal(context.sessionCloses(), 0);
  await context.controller.close();
});

test("a retryable attach refusal is retried in place and converges", async () => {
  // A fresh run's terminal can be durably active before the daemon's live
  // owner is ready; the server types that window rate_limited/retryable.
  let attachAttempts = 0;
  const delays = [];
  const context = harness({
    attachRetryDelay: (attempt) => { delays.push(attempt); return Promise.resolve(); },
    attachImpl: () => {
      attachAttempts += 1;
      if (attachAttempts === 1) return Promise.reject(new SessionError("rate_limited", true));
      return Promise.resolve({ sessionId: "22".repeat(16), floor: 0n, head: 0n, acknowledgedSequence: 0n, maxUnackedBytes: 65536n });
    },
  });
  context.controller.start();
  await tick();
  context.targetGate.resolve(target);
  await tick();
  await tick();
  assert.equal(attachAttempts, 2, "one retryable refusal earns exactly one retry");
  assert.deepEqual(delays, [0], "the delay seam is awaited between attempts");
  assert.equal(context.snapshots.at(-1).phase, "acquiring");
  context.acquireGate().resolve({ generation: 1n });
  await tick();
  assert.equal(context.controller.snapshot.phase, "ready");
  assert.equal(context.controller.snapshot.error, undefined);
  await context.controller.close();
});

test("attach retries are bounded and non-retryable attach errors fail immediately", async () => {
  let boundedAttempts = 0;
  const bounded = harness({
    attachRetryDelay: () => Promise.resolve(),
    attachImpl: () => { boundedAttempts += 1; return Promise.reject(new SessionError("rate_limited", true)); },
  });
  bounded.controller.start();
  await tick();
  bounded.targetGate.resolve(target);
  for (let i = 0; i < 16; i += 1) await tick();
  assert.equal(bounded.controller.snapshot.phase, "closed");
  assert.equal(bounded.controller.snapshot.error.code, "rate_limited", "the bound surfaces the typed error, not a fabricated one");
  assert.equal(boundedAttempts, 6, "initial attempt plus the bounded retries, then stop");

  let fatalAttempts = 0;
  const fatal = harness({
    attachRetryDelay: () => Promise.resolve(),
    attachImpl: () => { fatalAttempts += 1; return Promise.reject(new SessionError("internal")); },
  });
  fatal.controller.start();
  await tick();
  fatal.targetGate.resolve(target);
  await tick();
  await tick();
  assert.equal(fatal.controller.snapshot.phase, "closed");
  assert.equal(fatal.controller.snapshot.error.code, "internal");
  assert.equal(fatalAttempts, 1, "a non-retryable refusal is never retried");
});
