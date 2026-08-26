import { malformed } from "./errors.js";
import { MAX_TERMINAL_PAYLOAD, TERMINAL_HEADER_BYTES } from "./manifest.js";

export type TerminalDirection = "input" | "output";
export type TerminalFrame = {
  direction: TerminalDirection;
  sessionId: Uint8Array;
  sequence: bigint;
  leaseGeneration: bigint;
  payload: Uint8Array;
};

export function encodeTerminalInput(sessionId: Uint8Array, sequence: bigint, leaseGeneration: bigint, payload: Uint8Array): Uint8Array {
  return encode({ direction: "input", sessionId, sequence, leaseGeneration, payload });
}
export function encodeTerminalOutput(sessionId: Uint8Array, sequence: bigint, payload: Uint8Array): Uint8Array {
  return encode({ direction: "output", sessionId, sequence, leaseGeneration: 0n, payload });
}
export function decodeTerminalInput(data: Uint8Array): TerminalFrame { return decode(data, "input"); }
export function decodeTerminalOutput(data: Uint8Array): TerminalFrame { return decode(data, "output"); }

function encode(frame: TerminalFrame): Uint8Array {
  if (frame.sessionId.length !== 16 || frame.sessionId.every((b) => b === 0) || frame.payload.length === 0 || frame.payload.length > MAX_TERMINAL_PAYLOAD) malformed();
  if (frame.sequence < 0n || frame.leaseGeneration < 0n || frame.sequence > 0xffff_ffff_ffff_ffffn || frame.leaseGeneration > 0xffff_ffff_ffff_ffffn) malformed();
  if (frame.direction === "input" && (frame.sequence === 0n || frame.leaseGeneration === 0n)) malformed();
  if (frame.direction === "output" && (frame.leaseGeneration !== 0n || frame.sequence + BigInt(frame.payload.length) >= 0x1_0000_0000_0000_0000n)) malformed();
  const result = new Uint8Array(TERMINAL_HEADER_BYTES + frame.payload.length); const view = new DataView(result.buffer);
  result.set([0x44, 0x46, 1, frame.direction === "input" ? 1 : 2], 0); result.set(frame.sessionId, 4);
  view.setBigUint64(20, frame.sequence); view.setBigUint64(28, frame.leaseGeneration); view.setUint32(36, frame.payload.length); result.set(frame.payload, TERMINAL_HEADER_BYTES); return result;
}
function decode(data: Uint8Array, direction: TerminalDirection): TerminalFrame {
  if (data.length < TERMINAL_HEADER_BYTES || data.length > TERMINAL_HEADER_BYTES + MAX_TERMINAL_PAYLOAD) malformed();
  const view = new DataView(data.buffer, data.byteOffset, data.byteLength); if (data[0] !== 0x44 || data[1] !== 0x46 || data[2] !== 1) malformed();
  const opcode = data[3]; if ((direction === "input" && opcode !== 1) || (direction === "output" && opcode !== 2)) malformed();
  const sessionId = data.slice(4, 20); const sequence = view.getBigUint64(20); const leaseGeneration = view.getBigUint64(28); const length = view.getUint32(36);
  if (sessionId.every((b) => b === 0) || length === 0 || length > MAX_TERMINAL_PAYLOAD || length + TERMINAL_HEADER_BYTES !== data.length) malformed();
  if ((direction === "input" && (sequence === 0n || leaseGeneration === 0n)) || (direction === "output" && (leaseGeneration !== 0n || sequence + BigInt(length) >= 0x1_0000_0000_0000_0000n))) malformed();
  return { direction, sessionId, sequence, leaseGeneration, payload: data.slice(TERMINAL_HEADER_BYTES) };
}
