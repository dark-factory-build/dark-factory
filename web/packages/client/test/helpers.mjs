import { ProtocolError } from "../dist/src/errors.js";
import { MAX_TERMINAL_PAYLOAD, TERMINAL_FRAME_VERSION, TERMINAL_HEADER_BYTES } from "../dist/src/manifest.js";

export class MemoryRemoteStore {
  #bindings = new Map();

  async list() { return [...this.#bindings.values()].map(copy); }
  async put(binding) { this.#bindings.set(binding.nodeId, copy(binding)); }
  async forgetBinding(nodeId) { this.#bindings.delete(nodeId); }
  async forgetDevice() { this.#bindings.clear(); }
}

function copy(binding) {
  const result = { ...binding };
  if (binding.publicKeySEC1 !== undefined) result.publicKeySEC1 = binding.publicKeySEC1.slice();
  return result;
}

export async function verifyP256Signature(publicKeySEC1, signature, signed) {
  if (!(publicKeySEC1 instanceof Uint8Array) || !(signature instanceof Uint8Array) || !(signed instanceof Uint8Array)) throw new ProtocolError("malformed");
  if (publicKeySEC1.length !== 65 || publicKeySEC1[0] !== 4 || signature.length !== 64) return false;
  try {
    const key = await globalThis.crypto.subtle.importKey("raw", publicKeySEC1, { name: "ECDSA", namedCurve: "P-256" }, false, ["verify"]);
    return await globalThis.crypto.subtle.verify({ name: "ECDSA", hash: "SHA-256" }, key, signature, signed);
  } catch { return false; }
}

export function encodeTerminalOutput(sessionId, sequence, payload) {
  return encode({ direction: "output", sessionId, sequence, leaseGeneration: 0n, payload });
}

export function decodeTerminalInput(data) { return decode(data, "input"); }

function encode(frame) {
  if (!(frame.sessionId instanceof Uint8Array) || !(frame.payload instanceof Uint8Array) || typeof frame.sequence !== "bigint" || typeof frame.leaseGeneration !== "bigint") malformed();
  if (frame.sessionId.length !== 16 || frame.sessionId.every((b) => b === 0) || frame.payload.length === 0 || frame.payload.length > MAX_TERMINAL_PAYLOAD) malformed();
  if (frame.sequence < 0n || frame.leaseGeneration < 0n || frame.sequence > 0xffff_ffff_ffff_ffffn || frame.leaseGeneration > 0xffff_ffff_ffff_ffffn) malformed();
  if (frame.direction === "input" && (frame.sequence === 0n || frame.leaseGeneration === 0n)) malformed();
  if (frame.direction === "output" && (frame.leaseGeneration !== 0n || frame.sequence + BigInt(frame.payload.length) >= 0x1_0000_0000_0000_0000n)) malformed();
  const result = new Uint8Array(TERMINAL_HEADER_BYTES + frame.payload.length); const view = new DataView(result.buffer);
  result.set([0x44, 0x46, TERMINAL_FRAME_VERSION, frame.direction === "input" ? 1 : 2], 0); result.set(frame.sessionId, 4);
  view.setBigUint64(20, frame.sequence); view.setBigUint64(28, frame.leaseGeneration); view.setUint32(36, frame.payload.length); result.set(frame.payload, TERMINAL_HEADER_BYTES); return result;
}

function decode(data, direction) {
  if (!(data instanceof Uint8Array)) malformed();
  if (data.length < TERMINAL_HEADER_BYTES || data.length > TERMINAL_HEADER_BYTES + MAX_TERMINAL_PAYLOAD) malformed();
  const view = new DataView(data.buffer, data.byteOffset, data.byteLength); if (data[0] !== 0x44 || data[1] !== 0x46 || data[2] !== TERMINAL_FRAME_VERSION) malformed();
  const opcode = data[3]; if ((direction === "input" && opcode !== 1) || (direction === "output" && opcode !== 2)) malformed();
  const sessionId = data.slice(4, 20); const sequence = view.getBigUint64(20); const leaseGeneration = view.getBigUint64(28); const length = view.getUint32(36);
  if (sessionId.every((b) => b === 0) || length === 0 || length > MAX_TERMINAL_PAYLOAD || length + TERMINAL_HEADER_BYTES !== data.length) malformed();
  if ((direction === "input" && (sequence === 0n || leaseGeneration === 0n)) || (direction === "output" && (leaseGeneration !== 0n || sequence + BigInt(length) >= 0x1_0000_0000_0000_0000n))) malformed();
  return { direction, sessionId, sequence, leaseGeneration, payload: data.slice(TERMINAL_HEADER_BYTES) };
}

function malformed() { throw new ProtocolError("malformed"); }
