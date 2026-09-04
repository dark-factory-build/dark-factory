import { malformed, normalizeBoundary, ProtocolError } from "./errors.js";
import { PROTOCOL_VERSION } from "./manifest.js";

const PROTOCOL = new TextEncoder().encode("dark-factory/browser/v2/");
const PAIR = new TextEncoder().encode("pair\0");
const AUTH = new TextEncoder().encode("auth\0");

export type PairTranscriptInput = {
  daemon_id: string; boot_id: string; connection_nonce: string; challenge: string;
  public_key_sec1: string; host: string; origin: string;
};
export type AuthTranscriptInput = {
  daemon_id: string; boot_id: string; connection_nonce: string; client_id: string;
  host: string; origin: string;
};

export function hexBytes(value: string, bytes?: number): Uint8Array {
  return normalizeBoundary(() => {
    if (typeof value !== "string" || (bytes !== undefined && (!Number.isSafeInteger(bytes) || bytes < 0)) || value.length % 2 !== 0 || value !== value.toLowerCase() || !/^[0-9a-f]*$/.test(value)) malformed();
    if (bytes !== undefined && value.length !== bytes * 2) malformed();
    const result = new Uint8Array(value.length / 2);
    for (let i = 0; i < result.length; i++) result[i] = Number.parseInt(value.slice(i * 2, i * 2 + 2), 16);
    return result;
  });
}
function text(value: string): Uint8Array {
  for (const character of value) {
    const code = character.codePointAt(0) ?? 0;
    if (code >= 0xd800 && code <= 0xdfff) malformed();
  }
  const result = new TextEncoder().encode(value);
  if (result.length === 0 || result.length > 4096 || result.includes(0)) malformed();
  return result;
}
function fixed(value: string, bytes: number): Uint8Array { return hexBytes(value, bytes); }
function publicKey(value: string): Uint8Array { const result = fixed(value, 65); if (result[0] !== 4) malformed(); return result; }
function transcript(domain: Uint8Array, fields: Array<{ value: Uint8Array; fixed?: number }>): Uint8Array {
  const parts = [PROTOCOL, domain, new Uint8Array([PROTOCOL_VERSION >>> 8, PROTOCOL_VERSION & 0xff])];
  let size = parts.reduce((n, p) => n + p.length, 0);
  for (const field of fields) { if (field.fixed !== undefined && field.value.length !== field.fixed) malformed(); const length = new Uint8Array(4); new DataView(length.buffer).setUint32(0, field.value.length); parts.push(length, field.value); size += 4 + field.value.length; }
  const result = new Uint8Array(size); let offset = 0; for (const part of parts) { result.set(part, offset); offset += part.length; } return result;
}
export function buildPairTranscript(input: PairTranscriptInput): Uint8Array {
  return normalizeBoundary(() => {
    if (typeof input !== "object" || input === null) malformed();
    return transcript(PAIR, [
      { value: fixed(input.daemon_id, 16), fixed: 16 }, { value: fixed(input.boot_id, 16), fixed: 16 }, { value: fixed(input.connection_nonce, 32), fixed: 32 },
      { value: fixed(input.challenge, 32), fixed: 32 }, { value: publicKey(input.public_key_sec1), fixed: 65 }, { value: text(input.host) }, { value: text(input.origin) },
    ]);
  });
}
export function buildAuthTranscript(input: AuthTranscriptInput): Uint8Array {
  return normalizeBoundary(() => {
    if (typeof input !== "object" || input === null) malformed();
    return transcript(AUTH, [
      { value: fixed(input.daemon_id, 16), fixed: 16 }, { value: fixed(input.boot_id, 16), fixed: 16 }, { value: fixed(input.connection_nonce, 32), fixed: 32 },
      { value: fixed(input.client_id, 16), fixed: 16 }, { value: text(input.host) }, { value: text(input.origin) },
    ]);
  });
}

/** Verify the raw IEEE-P1363 P-256 signature used by browser v1. */
export async function verifyP256Signature(publicKeySEC1: Uint8Array, signature: Uint8Array, signed: Uint8Array): Promise<boolean> {
  if (!(publicKeySEC1 instanceof Uint8Array) || !(signature instanceof Uint8Array) || !(signed instanceof Uint8Array)) throw new ProtocolError("malformed");
  if (publicKeySEC1.length !== 65 || publicKeySEC1[0] !== 4 || signature.length !== 64) return false;
  try {
    const key = await globalThis.crypto.subtle.importKey("raw", publicKeySEC1 as unknown as BufferSource, { name: "ECDSA", namedCurve: "P-256" }, false, ["verify"]);
    return await globalThis.crypto.subtle.verify({ name: "ECDSA", hash: "SHA-256" }, key, signature as unknown as BufferSource, signed as unknown as BufferSource);
  } catch { return false; }
}
