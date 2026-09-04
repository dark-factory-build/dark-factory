import { SessionError } from "../session.js";

/**
 * Relay credentials are `base64url(payload) + "." + base64url(signature)` with
 * no padding, so every one of them is a legal WebSocket subprotocol token. The
 * signed bytes are a fixed domain prefix followed by the exact base64url
 * payload text, so a verifier never re-serialises JSON and this minter never
 * has to promise a canonical encoder.
 */
const BASE64URL = /^[A-Za-z0-9_-]*$/;
const PROOF_DOMAIN = "dark-factory-relay/proof\n";
const NODE_ID = /^[a-z2-7]{32}$/;
const NONCE_BYTES = 16;
const TICKET_ID_BYTES = 16;
const CONTROLLER_BYTES = 16;
const DEVICE_KEY_BYTES = 65;
const SIGNATURE_BYTES = 64;
const MAX_TOKEN_BYTES = 1024;
/** Unix seconds stay well inside the safe-integer range for any real clock. */
const MAX_UNIX_SECONDS = 4_102_444_800;

export function base64urlEncode(bytes: Uint8Array): string {
  if (!(bytes instanceof Uint8Array)) throw new SessionError("invalid_request");
  return btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

/** Strict and canonical: no padding, no alien characters, no stray low bits. */
export function base64urlDecode(text: string): Uint8Array {
  if (typeof text !== "string" || text.length > MAX_TOKEN_BYTES || !BASE64URL.test(text)) throw new SessionError("invalid_request");
  let bytes: Uint8Array;
  try { bytes = Uint8Array.from(atob(text.replace(/-/g, "+").replace(/_/g, "/")), (character) => character.charCodeAt(0)); }
  catch { throw new SessionError("invalid_request"); }
  // One text per byte string: padding, an alien character and a stray low bit
  // all fail to round-trip, so a token cannot be silently rewritten.
  if (base64urlEncode(bytes) !== text) throw new SessionError("invalid_request");
  return bytes;
}

/**
 * One proof is good for one dial: the nonce is fresh random bytes and `issued`
 * is checked by the relay within 60 seconds either way, so a captured proof
 * cannot be replayed into a later connection.
 */
export async function mintProof(ticketId: string, key: CryptoKey): Promise<string> {
  const source = globalThis.crypto;
  if (source === undefined || source.subtle === undefined) throw new SessionError("crypto_unavailable");
  if (key.extractable || key.algorithm.name !== "ECDSA") throw new SessionError("crypto_unavailable");
  if (base64urlDecode(ticketId).length !== TICKET_ID_BYTES) throw new SessionError("invalid_request");
  const nonce = new Uint8Array(NONCE_BYTES);
  try { source.getRandomValues(nonce); } catch { throw new SessionError("crypto_unavailable"); }
  const issued = Math.floor(Date.now() / 1000);
  const payload = base64urlEncode(new TextEncoder().encode(JSON.stringify({ ticket: ticketId, issued, nonce: base64urlEncode(nonce) })));
  let signature: ArrayBuffer;
  try {
    signature = await source.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, key, new TextEncoder().encode(PROOF_DOMAIN + payload) as unknown as BufferSource);
  } catch { throw new SessionError("crypto_unavailable"); }
  if (!(signature instanceof ArrayBuffer) || signature.byteLength !== SIGNATURE_BYTES) throw new SessionError("crypto_unavailable");
  return `${payload}.${base64urlEncode(new Uint8Array(signature))}`;
}

export type RelayTicket = Readonly<{
  node: string;
  controller: string;
  purpose: "pair" | "control";
  ticket: string;
  expires: number;
  device?: string;
}>;

/**
 * Reads a ticket without verifying it. The relay holds the node public key and
 * is the only verifier; a browser reads a ticket only to learn which ticket id
 * to prove, whether a proof is wanted at all, and when to stop offering it.
 */
export function parseTicket(token: string): RelayTicket {
  if (typeof token !== "string" || token.length > MAX_TOKEN_BYTES) throw new SessionError("invalid_request");
  const parts = token.split(".");
  if (parts.length !== 2) throw new SessionError("invalid_request");
  const [payload, signature] = parts as [string, string];
  if (payload.length === 0 || base64urlDecode(signature).length !== SIGNATURE_BYTES) throw new SessionError("invalid_request");
  let text: string;
  try { text = new TextDecoder("utf-8", { fatal: true }).decode(base64urlDecode(payload)); } catch { throw new SessionError("invalid_request"); }
  let value: unknown;
  try { value = JSON.parse(text) as unknown; } catch { throw new SessionError("invalid_request"); }
  if (typeof value !== "object" || value === null || Array.isArray(value)) throw new SessionError("invalid_request");
  const body = value as Record<string, unknown>;
  const node = body.node;
  const purpose = body.purpose;
  if (typeof node !== "string" || !NODE_ID.test(node)) throw new SessionError("invalid_request");
  if (purpose !== "pair" && purpose !== "control") throw new SessionError("invalid_request");
  const controller = fixedBase64url(body.controller, CONTROLLER_BYTES);
  const ticket = fixedBase64url(body.ticket, TICKET_ID_BYTES);
  const expires = unixSeconds(body.expires);
  // A control ticket names the one device key whose proof it accepts; a pair
  // ticket names none, because pairing is the act that mints that key.
  const hasDevice = Object.prototype.hasOwnProperty.call(body, "device");
  if (purpose === "control" ? !hasDevice : hasDevice) throw new SessionError("invalid_request");
  if (purpose === "pair") return Object.freeze({ node, controller, purpose, ticket, expires });
  const device = body.device;
  if (typeof device !== "string" || base64urlDecode(device).length !== DEVICE_KEY_BYTES || base64urlDecode(device)[0] !== 4) throw new SessionError("invalid_request");
  return Object.freeze({ node, controller, purpose, ticket, expires, device });
}

export function unixSeconds(value: unknown): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 1 || value > MAX_UNIX_SECONDS) throw new SessionError("invalid_request");
  return value;
}

function fixedBase64url(value: unknown, bytes: number): string {
  if (typeof value !== "string" || base64urlDecode(value).length !== bytes) throw new SessionError("invalid_request");
  return value;
}
