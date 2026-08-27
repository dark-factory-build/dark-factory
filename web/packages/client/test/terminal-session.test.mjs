import assert from "node:assert/strict";
import test from "node:test";
import {
  TerminalHandle,
  decodeClientControl,
  decodeServerControl,
  decodeTerminalInput,
  encodeTerminalAttached,
  encodeTerminalInputResult,
  encodeTerminalLeaseResult,
  encodeTerminalOutput,
} from "../dist/src/index.js";

const runId = "11".repeat(16);
const sessionId = "22".repeat(16);
const attachId = "01".repeat(16);
const leaseId = "02".repeat(16);
const inputId = "03".repeat(16);
const control = (wire) => decodeServerControl(wire);

function handle(sent, extra = {}, send = (id, payload) => sent.push({ id, payload })) {
  const ids = [attachId, leaseId, inputId];
  return new TerminalHandle({ runId, sessionId, expectedRunRevision: 1n, expectedSessionRevision: 1n, ...extra }, send, () => ids.shift() ?? "04".repeat(16));
}

test("TerminalHandle attaches, ACKs only after accepted output, and advances exact input sequence", async () => {
  const sent = [];
  const output = [];
  const terminal = handle(sent, { onOutput: async (value) => output.push(value) });
  const attached = terminal.attach();
  assert.equal(sent.length, 1);
  assert.equal(decodeClientControl(sent[0].payload).type, "TERMINAL_ATTACH");
  terminal.receive(control(encodeTerminalAttached(attachId, { session_id: sessionId, floor: 0n, head: 0n, acknowledged_sequence: 0n, max_unacked_bytes: 65536n })));
  assert.deepEqual(await attached, { sessionId, floor: 0n, head: 0n, acknowledgedSequence: 0n, maxUnackedBytes: 65536n });

  const lease = terminal.acquireLease();
  terminal.receive(control(encodeTerminalLeaseResult(leaseId, { operation: "acquired", run_id: runId, session_id: sessionId, generation: 7n, expires_at_ms: 100n, last_input_sequence: 0n, run_revision: 1n, session_revision: 1n })));
  await lease;
  const input = terminal.sendInput(new Uint8Array([4, 5]));
  assert.deepEqual([...decodeTerminalInput(sent.at(-1).payload).payload], [4, 5]);
  terminal.receive(control(encodeTerminalInputResult(attachId, { session_id: sessionId, generation: 7n, sequence: 1n, status: "accepted", accepted_bytes: 2n })));
  assert.equal((await input).status, "accepted");
  assert.equal(terminal.nextInputSequence, 3n);

  await terminal.receiveBinary({ direction: "output", sessionId: new Uint8Array(16).fill(0x22), sequence: 0n, leaseGeneration: 0n, payload: new Uint8Array([9]) });
  assert.deepEqual([...output[0].payload], [9]);
  assert.equal(decodeClientControl(sent.at(-1).payload).type, "TERMINAL_ACK");
  assert.equal(decodeClientControl(sent.at(-1).payload).body.next_sequence, 1n);
});

test("callback failure closes the handle without acknowledging output and uncertain input clears lease", async () => {
  const sent = [];
  const terminal = handle(sent, { onOutput: () => { throw new Error("private callback detail"); } });
  const attach = terminal.attach();
  terminal.receive(control(encodeTerminalAttached(attachId, { session_id: sessionId, floor: 0n, head: 0n, acknowledged_sequence: 0n, max_unacked_bytes: 65536n })));
  await attach;
  await terminal.receiveBinary({ direction: "output", sessionId: new Uint8Array(16).fill(0x22), sequence: 0n, leaseGeneration: 0n, payload: new Uint8Array([1]) });
  assert.equal(terminal.closed, true);
  assert.equal(sent.some(({ payload }) => typeof payload !== "string" && payload.length === 40), false);
});

test("pending attach reset is correlated, resolves explicitly, and emits reset once", async () => {
  const sent = [];
  let resets = 0;
  const terminal = handle(sent, { onReset: () => { resets += 1; } });
  const pending = terminal.attach();
  assert.equal(terminal.receiveReset(attachId, { session_id: sessionId, floor: 4n, head: 9n }), true);
  assert.deepEqual(await pending, { sessionId, floor: 4n, head: 9n, kind: "reset", freshAttachRequired: true });
  assert.equal(resets, 1);
  assert.equal(terminal.closed, true);
  assert.equal(terminal.receiveReset(attachId, { session_id: sessionId, floor: 4n, head: 9n }), false);
  assert.equal(resets, 1);
  assert.equal(sent.length, 1);
});

test("attach is single-flight and forged input results cannot consume a pending frame", async () => {
  const sent = [];
  const terminal = handle(sent);
  const first = terminal.attach();
  await assert.rejects(terminal.attach(), /already attached/);
  assert.equal(sent.length, 1);
  terminal.receive(control(encodeTerminalAttached(attachId, { session_id: sessionId, floor: 0n, head: 0n, acknowledged_sequence: 0n, max_unacked_bytes: 65536n })));
  await first;
  const lease = terminal.acquireLease();
  terminal.receive(control(encodeTerminalLeaseResult(leaseId, { operation: "acquired", run_id: runId, session_id: sessionId, generation: 7n, expires_at_ms: 100n, last_input_sequence: 0n, run_revision: 1n, session_revision: 1n })));
  await lease;
  const input = terminal.sendInput(new Uint8Array([1]));
  assert.equal(terminal.receive(control(encodeTerminalInputResult("ff".repeat(16), { session_id: sessionId, generation: 7n, sequence: 1n, status: "accepted", accepted_bytes: 1n }))), false);
  assert.equal(terminal.closed, false);
  terminal.receive(control(encodeTerminalInputResult(attachId, { session_id: sessionId, generation: 7n, sequence: 1n, status: "accepted", accepted_bytes: 1n })));
  await input;
});

test("lease mutations are single-flight and cannot reorder authority", async () => {
  const sent = [];
  const terminal = handle(sent);
  const attached = terminal.attach();
  terminal.receive(control(encodeTerminalAttached(attachId, { session_id: sessionId, floor: 0n, head: 0n, acknowledged_sequence: 0n, max_unacked_bytes: 65536n })));
  await attached;
  const acquire = terminal.acquireLease();
  await assert.rejects(terminal.acquireLease(), /lease operation pending/);
  assert.equal(sent.filter(({ payload }) => typeof payload === "string" && decodeClientControl(payload).type === "TERMINAL_LEASE_ACQUIRE").length, 1);
  terminal.receive(control(encodeTerminalLeaseResult(leaseId, { operation: "acquired", run_id: runId, session_id: sessionId, generation: 7n, expires_at_ms: 100n, last_input_sequence: 0n, run_revision: 1n, session_revision: 1n })));
  await acquire;
  const renew = terminal.renewLease();
  await assert.rejects(terminal.releaseLease(), /lease operation pending/);
  terminal.receive(control(encodeTerminalLeaseResult(inputId, { operation: "renewed", run_id: runId, session_id: sessionId, generation: 7n, expires_at_ms: 200n, last_input_sequence: 0n, run_revision: 1n, session_revision: 1n })));
  assert.equal((await renew).generation, 7n);
});

test("recursive output callback closes without a duplicate callback or ACK", async () => {
  const sent = [];
  let calls = 0;
  let terminal;
  terminal = handle(sent, { onOutput: async () => {
    calls += 1;
    if (calls === 1) await terminal.receiveBinary({ direction: "output", sessionId: new Uint8Array(16).fill(0x22), sequence: 0n, leaseGeneration: 0n, payload: new Uint8Array([2]) });
  } });
  const attached = terminal.attach();
  terminal.receive(control(encodeTerminalAttached(attachId, { session_id: sessionId, floor: 0n, head: 0n, acknowledged_sequence: 0n, max_unacked_bytes: 65536n })));
  await attached;
  await terminal.receiveBinary({ direction: "output", sessionId: new Uint8Array(16).fill(0x22), sequence: 0n, leaseGeneration: 0n, payload: new Uint8Array([1]) });
  assert.equal(calls, 1);
  assert.equal(terminal.closed, true);
  assert.equal(sent.filter(({ payload }) => typeof payload === "string" && decodeClientControl(payload).type === "TERMINAL_ACK").length, 0);
});

test("input send and ACK send failures preserve uncertainty and the last sent cursor", async () => {
  const sent = [];
  const terminal = handle(sent, {}, (id, payload) => {
    sent.push({ id, payload });
    if (payload instanceof Uint8Array) throw new Error("input send failed");
  });
  const attached = terminal.attach();
  terminal.receive(control(encodeTerminalAttached(attachId, { session_id: sessionId, floor: 0n, head: 0n, acknowledged_sequence: 0n, max_unacked_bytes: 65536n })));
  await attached;
  const lease = terminal.acquireLease();
  terminal.receive(control(encodeTerminalLeaseResult(leaseId, { operation: "acquired", run_id: runId, session_id: sessionId, generation: 7n, expires_at_ms: 100n, last_input_sequence: 0n, run_revision: 1n, session_revision: 1n })));
  await lease;
  const input = terminal.sendInput(new Uint8Array([1]));
  // The test transport cannot transmit the frame; no retry is allowed.
  const inputFrame = sent.at(-1).payload;
  await assert.rejects(input, (error) => error.code === "connection");
  assert.equal(inputFrame.length, 41);
  assert.equal(terminal.closed, true);
  assert.equal(terminal.lease, undefined);
  assert.equal(sent.filter(({ payload }) => payload instanceof Uint8Array).length, 1);

  const ackSent = [];
  const outputTerminal = handle(ackSent, { onOutput: () => {} }, (id, payload) => {
    if (typeof payload === "string" && decodeClientControl(payload).type === "TERMINAL_ACK") throw new Error("ACK send failed");
    ackSent.push({ id, payload });
  });
  const outputAttach = outputTerminal.attach();
  outputTerminal.receive(control(encodeTerminalAttached(attachId, { session_id: sessionId, floor: 0n, head: 0n, acknowledged_sequence: 0n, max_unacked_bytes: 65536n })));
  await outputAttach;
  await outputTerminal.receiveBinary({ direction: "output", sessionId: new Uint8Array(16).fill(0x22), sequence: 0n, leaseGeneration: 0n, payload: new Uint8Array([1]) });
  // The failed ACK must not claim progress that was never sent.
  assert.equal(outputTerminal.closed, true);
  assert.equal(outputTerminal.acknowledgedSequence, 0n);
  assert.equal(ackSent.some(({ payload }) => typeof payload === "string" && decodeClientControl(payload).type === "TERMINAL_ACK"), false);
});
