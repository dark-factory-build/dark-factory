import assert from "node:assert/strict";
import test from "node:test";
import { createTerminalHandle } from "../dist/src/terminal_session.js";
import {
  decodeClientControl,
  decodeServerControl,
  encodeTerminalAttached,
  encodeTerminalDetached,
  encodeTerminalInputResult,
  encodeTerminalLeaseResult,
  encodeTerminalResized,
} from "../dist/src/control.js";

const runId = "11".repeat(16);
const sessionId = "22".repeat(16);
const responseIds = ["01", "02", "03", "04", "05", "06", "07", "08"].map((value) => value.repeat(16));

class VirtualTimer {
  now = 100_000;
  next = 0;
  tasks = new Map();
  delays = [];

  setTimeout(callback, delay) {
    const id = ++this.next;
    this.tasks.set(id, { at: this.now + delay, callback });
    this.delays.push(delay);
    return id;
  }

  clearTimeout(id) { this.tasks.delete(id); }

  advance(milliseconds) {
    this.now += milliseconds;
    while (true) {
      const due = [...this.tasks.entries()]
        .filter(([, task]) => task.at <= this.now)
        .sort((a, b) => a[1].at - b[1].at)[0];
      if (due === undefined) return;
      this.tasks.delete(due[0]);
      due[1].callback();
    }
  }
}

function makeHandle(overrides = {}) {
  const sent = [];
  const timer = overrides.timer ?? new VirtualTimer();
  let id = 0;
  const fatals = [];
  const handle = createTerminalHandle(
    { runId, sessionId, runRevision: 1n, sessionRevision: 1n },
    overrides.options ?? {},
    (requestId, payload) => sent.push({ requestId, payload }),
    () => responseIds[id++] ?? "09".repeat(16),
    (error) => fatals.push(error),
    timer,
    overrides.inputAllowed ?? true,
  );
  return { handle, sent, timer, fatals };
}

function serverFrame(wire) { return decodeServerControl(wire); }
function lastControl(sent) { return decodeClientControl(sent.at(-1).payload); }
function replyAttached(value, frame) {
  return encodeTerminalAttached(frame.id, {
    session_id: sessionId,
    floor: 0n,
    head: 0n,
    acknowledged_sequence: 0n,
    max_unacked_bytes: 65536n,
    ...value,
  });
}

async function attachedWithLease(context, expiresAtMs = BigInt(context.timer.now + 30_000)) {
  const { handle, sent } = context;
  const attach = handle.attach();
  const attachFrame = lastControl(sent);
  handle.receive(serverFrame(replyAttached({}, attachFrame)));
  await attach;
  const acquire = handle.acquireInput();
  const acquireFrame = lastControl(sent);
  handle.receive(serverFrame(encodeTerminalLeaseResult(acquireFrame.id, {
    operation: "acquired",
    run_id: runId,
    session_id: sessionId,
    generation: 1n,
    expires_at_ms: expiresAtMs,
    last_input_sequence: 0n,
    run_revision: 1n,
    session_revision: 1n,
  })));
  await acquire;
  return { attachFrame, acquireFrame };
}

test("acquire arms one ten-second timer and renewal replaces it with one timer", async () => {
  const context = makeHandle();
  await attachedWithLease(context);
  assert.equal(context.handle.writable, true);
  assert.equal(context.timer.tasks.size, 1);
  assert.deepEqual(context.timer.delays, [10_000]);
  context.timer.advance(10_000);
  const renewal = lastControl(context.sent);
  assert.equal(renewal.type, "TERMINAL_LEASE_RENEW");
  assert.equal(context.timer.tasks.size, 1);
  context.handle.receive(serverFrame(encodeTerminalLeaseResult(renewal.id, {
    operation: "renewed", run_id: runId, session_id: sessionId, generation: 1n,
    expires_at_ms: BigInt(context.timer.now + 30_000), last_input_sequence: 0n,
    run_revision: 1n, session_revision: 1n,
  })));
  assert.equal(context.timer.tasks.size, 1);
  assert.deepEqual(context.timer.delays, [10_000, 20_000, 10_000]);
});

test("a due timer waits for input, then renews once before another effect", async () => {
  const context = makeHandle();
  const { attachFrame } = await attachedWithLease(context);
  const input = context.handle.sendInput(new Uint8Array([1]));
  context.timer.advance(10_000);
  assert.equal(context.sent.filter(({ payload }) => typeof payload === "string" && decodeClientControl(payload).type === "TERMINAL_LEASE_RENEW").length, 0);
  context.handle.receive(serverFrame(encodeTerminalInputResult(attachFrame.id, {
    session_id: sessionId, generation: 1n, sequence: 1n, status: "accepted", accepted_bytes: 1n,
  })));
  await input;
  assert.equal(context.sent.filter(({ payload }) => typeof payload === "string" && decodeClientControl(payload).type === "TERMINAL_LEASE_RENEW").length, 1);
});

test("rejected input preserves lease and sequence for deliberate retry", async () => {
  const context = makeHandle();
  const { attachFrame } = await attachedWithLease(context);
  const input = context.handle.sendInput(new Uint8Array([1]));
  context.handle.receive(serverFrame(encodeTerminalInputResult(attachFrame.id, {
    session_id: sessionId, generation: 1n, sequence: 1n, status: "rejected", accepted_bytes: 0n,
  })));
  await input;
  assert.equal(context.handle.writable, true);
  const retry = context.handle.sendInput(new Uint8Array([2]));
  assert.equal(context.sent.at(-1).payload instanceof Uint8Array, true);
  context.handle.receive(serverFrame(encodeTerminalInputResult(attachFrame.id, {
    session_id: sessionId, generation: 1n, sequence: 1n, status: "accepted", accepted_bytes: 1n,
  })));
  await retry;
});

test("partial input fences authority and never replays a suffix", async () => {
  const context = makeHandle();
  const { attachFrame } = await attachedWithLease(context);
  const input = context.handle.sendInput(new Uint8Array([1, 2, 3]));
  context.handle.receive(serverFrame(encodeTerminalInputResult(attachFrame.id, {
    session_id: sessionId, generation: 1n, sequence: 1n, status: "partial", accepted_bytes: 1n,
  })));
  await input;
  assert.equal(context.handle.writable, false);
  assert.throws(() => context.handle.sendInput(new Uint8Array([9])), /terminal lease required/);
  assert.equal(context.sent.filter(({ payload }) => payload instanceof Uint8Array).length, 1);
});

test("release precedes detach and exact release ambiguity never sends detach", async () => {
  const context = makeHandle();
  await attachedWithLease(context);
  const detached = context.handle.detach();
  const release = lastControl(context.sent);
  assert.equal(release.type, "TERMINAL_LEASE_RELEASE");
  context.handle.receive(serverFrame(encodeTerminalLeaseResult(release.id, {
    operation: "released", run_id: runId, session_id: sessionId, generation: 2n,
    last_input_sequence: 0n, run_revision: 1n, session_revision: 1n,
  })));
  const detach = lastControl(context.sent);
  assert.equal(detach.type, "TERMINAL_DETACH");
  context.handle.receive(serverFrame(encodeTerminalDetached(detach.id, { session_id: sessionId })));
  await detached;

  const failed = makeHandle();
  await attachedWithLease(failed);
  const pending = failed.handle.detach();
  const releaseFrame = lastControl(failed.sent);
  assert.equal(failed.handle.receiveError(releaseFrame.id, new Error("release unknown")), true);
  await assert.rejects(pending, /release unknown/);
  assert.equal(failed.fatals.length, 1);
  assert.equal(failed.sent.some(({ payload }) => typeof payload === "string" && decodeClientControl(payload).type === "TERMINAL_DETACH"), false);
});

test("exit and reset clear writable state before callbacks", async () => {
  let terminal;
  let exitWritable;
  const context = makeHandle({ options: { onExit: () => { exitWritable = terminal.writable; } } });
  terminal = context.handle;
  const { attachFrame } = await attachedWithLease(context);
  assert.equal(terminal.receiveExit(attachFrame.id, { session_id: sessionId, exit_code: 0, exit_signal: 0, aborted: false }), true);
  assert.equal(exitWritable, false);
  const resetContext = makeHandle({ options: { onReset: () => { assert.equal(resetContext.handle.writable, false); } } });
  const resetAttach = resetContext.handle.attach();
  const resetAttachFrame = lastControl(resetContext.sent);
  assert.equal(resetContext.handle.receiveReset(resetAttachFrame.id, { session_id: sessionId, floor: 1n, head: 1n }), true);
  await resetAttach;
});

test("expiry watchdog fails once without renewal retry", async () => {
  const context = makeHandle();
  await attachedWithLease(context, BigInt(context.timer.now + 30_000));
  context.timer.advance(10_000);
  const renewal = lastControl(context.sent);
  assert.equal(renewal.type, "TERMINAL_LEASE_RENEW");
  context.timer.advance(20_000);
  assert.equal(context.fatals.length, 1);
  assert.equal(context.handle.writable, false);
  assert.equal(context.sent.filter(({ payload }) => typeof payload === "string" && decodeClientControl(payload).type === "TERMINAL_LEASE_RENEW").length, 1);
});

test("manual lease renewal and raw identities are absent from the public package", async () => {
  const publicApi = await import("../dist/src/index.js");
  assert.equal("TerminalHandle" in publicApi, false);
  assert.equal("createTerminalHandle" in publicApi, false);
  assert.equal("TerminalHandleOptions" in publicApi, false);
});
