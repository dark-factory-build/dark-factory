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

function handle(sent, extra = {}) {
  const ids = [attachId, leaseId, inputId];
  return new TerminalHandle({ runId, sessionId, expectedRunRevision: 1n, expectedSessionRevision: 1n, ...extra }, (id, payload) => sent.push({ id, payload }), () => ids.shift() ?? "04".repeat(16));
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
  terminal.receive(control(encodeTerminalInputResult("ff".repeat(16), { session_id: sessionId, generation: 7n, sequence: 1n, status: "accepted", accepted_bytes: 2n })));
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
