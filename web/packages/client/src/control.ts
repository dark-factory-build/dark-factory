import { malformed, normalizeBoundary, ProtocolError } from "./errors.js";
import {
  CAPABILITIES,
  CONTROL_TYPES,
  ERROR_CODES,
  MAX_CONTROL_BYTES,
  PROTOCOL_VERSION,
  type CapabilityMask,
  type ControlType,
  type ErrorCode,
} from "./manifest.js";

export type HelloBody = {
  daemon_id: string;
  boot_id: string;
  connection_nonce: string;
};
export type PairProveBody = {
  challenge: string;
  public_key_sec1: string;
  signature: string;
};
export type PairResultBody = { client_id: string; capabilities: CapabilityMask };
export type AuthProveBody = {
  client_id: string;
  public_key_sec1: string;
  signature: string;
};
export type AuthResultBody = { client_id: string; capabilities: CapabilityMask };
export type ErrorBody = { code: ErrorCode; retryable: boolean };

export type HelloFrame = { v: 1; type: "HELLO"; body: HelloBody };
export type PairProveFrame = { v: 1; type: "PAIR_PROVE"; id: string; body: PairProveBody };
export type PairResultFrame = { v: 1; type: "PAIR_RESULT"; id: string; body: PairResultBody };
export type AuthProveFrame = { v: 1; type: "AUTH_PROVE"; id: string; body: AuthProveBody };
export type AuthResultFrame = { v: 1; type: "AUTH_RESULT"; id: string; body: AuthResultBody };
export type ErrorFrame = { v: 1; type: "ERROR"; id?: string; body: ErrorBody };

export type ServerControlFrame = HelloFrame | PairResultFrame | AuthResultFrame | ErrorFrame;
export type ClientControlFrame = PairProveFrame | AuthProveFrame | ErrorFrame;

const HEX_BYTES = {
  daemon_id: 16,
  boot_id: 16,
  connection_nonce: 32,
  challenge: 32,
  client_id: 16,
  public_key_sec1: 65,
  signature: 64,
} as const;

export function encodeClientControl(frame: ClientControlFrame): string {
  return normalizeBoundary(() => { validateControl(frame, "client"); return encode(frame); });
}

export function encodePairProve(id: string, body: PairProveBody): string {
  return encodeClientControl({ v: 1, type: "PAIR_PROVE", id, body });
}
export function encodeAuthProve(id: string, body: AuthProveBody): string {
  return encodeClientControl({ v: 1, type: "AUTH_PROVE", id, body });
}
export function encodeClientError(body: ErrorBody, id?: string): string {
  return encodeClientControl({ v: 1, type: "ERROR", ...(id === undefined ? {} : { id }), body });
}

export function encodeServerControl(frame: ServerControlFrame): string {
  return normalizeBoundary(() => { validateControl(frame, "server"); return encode(frame); });
}

export function encodeHello(body: HelloBody): string { return encodeServerControl({ v: 1, type: "HELLO", body }); }
export function encodePairResult(id: string, body: PairResultBody): string { return encodeServerControl({ v: 1, type: "PAIR_RESULT", id, body }); }
export function encodeAuthResult(id: string, body: AuthResultBody): string { return encodeServerControl({ v: 1, type: "AUTH_RESULT", id, body }); }
export function encodeServerError(body: ErrorBody, id?: string): string {
  return encodeServerControl({ v: 1, type: "ERROR", ...(id === undefined ? {} : { id }), body });
}

export function decodeClientControl(data: string | Uint8Array): ClientControlFrame {
  return normalizeBoundary(() => decodeControl(data, "client") as ClientControlFrame);
}

export function decodeServerControl(data: string | Uint8Array): ServerControlFrame {
  return normalizeBoundary(() => decodeControl(data, "server") as ServerControlFrame);
}

function encode(frame: ClientControlFrame | ServerControlFrame): string {
  const envelope: Record<string, unknown> = { v: 1, type: frame.type };
  if ("id" in frame) envelope.id = frame.id;
  envelope.body = frame.body;
  const result = JSON.stringify(envelope);
  if (new TextEncoder().encode(result).length > MAX_CONTROL_BYTES) throw new ProtocolError("oversized");
  return result;
}

function decodeControl(data: string | Uint8Array, role: "client" | "server"): ClientControlFrame | ServerControlFrame {
  let text: string;
  try { text = typeof data === "string" ? data : new TextDecoder("utf-8", { fatal: true }).decode(data); } catch { malformed(); }
  if (text.length === 0 || new TextEncoder().encode(text).length > MAX_CONTROL_BYTES) malformed();
  rejectDuplicateKeys(text);
  let value: unknown;
  try {
    value = JSON.parse(text) as unknown;
  } catch {
    malformed();
  }
  if (!isObject(value)) malformed();
  exactKeys(value, ["v", "type", "body"], ["id"]);
  if (value.v !== 1) throw new ProtocolError("unsupported_version");
  if (!isControlType(value.type)) malformed();
  const hasID = Object.prototype.hasOwnProperty.call(value, "id");
  if (hasID && (typeof value.id !== "string" || !validID(value.id))) malformed();
  const requiredID = value.type !== "HELLO" && value.type !== "ERROR";
  if (requiredID !== hasID) malformed();
  if (role === "client" && !["PAIR_PROVE", "AUTH_PROVE", "ERROR"].includes(value.type)) {
    throw new ProtocolError("wrong_direction");
  }
  if (role === "server" && !["HELLO", "PAIR_RESULT", "AUTH_RESULT", "ERROR"].includes(value.type)) {
    throw new ProtocolError("wrong_direction");
  }
  if (!isObject(value.body)) malformed();
  const body = validateBody(value.type, value.body);
  if (value.type === "HELLO") return { v: 1, type: "HELLO", body: body as HelloBody };
  if (value.type === "PAIR_PROVE") return { v: 1, type: "PAIR_PROVE", id: value.id as string, body: body as PairProveBody };
  if (value.type === "PAIR_RESULT") return { v: 1, type: "PAIR_RESULT", id: value.id as string, body: body as PairResultBody };
  if (value.type === "AUTH_PROVE") return { v: 1, type: "AUTH_PROVE", id: value.id as string, body: body as AuthProveBody };
  if (value.type === "AUTH_RESULT") return { v: 1, type: "AUTH_RESULT", id: value.id as string, body: body as AuthResultBody };
  return { v: 1, type: "ERROR", ...(hasID ? { id: value.id as string } : {}), body: body as ErrorBody };
}

function validateControl(frame: ClientControlFrame | ServerControlFrame, role: "client" | "server"): void {
  if (!isObject(frame) || frame.v !== 1 || !isControlType(frame.type)) malformed();
  if ((role === "client" && !["PAIR_PROVE", "AUTH_PROVE", "ERROR"].includes(frame.type)) ||
      (role === "server" && !["HELLO", "PAIR_RESULT", "AUTH_RESULT", "ERROR"].includes(frame.type))) {
    throw new ProtocolError("wrong_direction");
  }
  const requires = frame.type !== "HELLO" && frame.type !== "ERROR";
  if (requires && (!("id" in frame) || !validID(frame.id))) malformed();
  if (!requires && "id" in frame && (typeof frame.id !== "string" || !validID(frame.id))) malformed();
  validateBody(frame.type, frame.body);
}

function validateBody(type: ControlType, body: Record<string, unknown>): HelloBody | PairProveBody | PairResultBody | AuthProveBody | AuthResultBody | ErrorBody {
  if (!isObject(body)) malformed();
  if (type === "HELLO") {
    exactKeys(body, ["daemon_id", "boot_id", "connection_nonce"]);
    fixedHex(body.daemon_id, HEX_BYTES.daemon_id); fixedHex(body.boot_id, HEX_BYTES.boot_id); fixedHex(body.connection_nonce, HEX_BYTES.connection_nonce);
    return body as unknown as HelloBody;
  }
  if (type === "PAIR_PROVE") {
    exactKeys(body, ["challenge", "public_key_sec1", "signature"]);
    fixedHex(body.challenge, HEX_BYTES.challenge); fixedHex(body.public_key_sec1, HEX_BYTES.public_key_sec1, true); fixedHex(body.signature, HEX_BYTES.signature);
    return body as unknown as PairProveBody;
  }
  if (type === "AUTH_PROVE") {
    exactKeys(body, ["client_id", "public_key_sec1", "signature"]);
    fixedHex(body.client_id, HEX_BYTES.client_id); fixedHex(body.public_key_sec1, HEX_BYTES.public_key_sec1, true); fixedHex(body.signature, HEX_BYTES.signature);
    return body as unknown as AuthProveBody;
  }
  if (type === "PAIR_RESULT" || type === "AUTH_RESULT") {
    exactKeys(body, ["client_id", "capabilities"]);
    fixedHex(body.client_id, HEX_BYTES.client_id); validCapabilities(body.capabilities);
    return body as PairResultBody | AuthResultBody;
  }
  exactKeys(body, ["code", "retryable"]);
  if (typeof body.code !== "string" || !(ERROR_CODES as readonly string[]).includes(body.code)) malformed();
  if (typeof body.retryable !== "boolean") malformed();
  return body as unknown as ErrorBody;
}

function fixedHex(value: unknown, bytes: number, requireUncompressed = false): Uint8Array {
  if (typeof value !== "string" || value.length !== bytes * 2 || value !== value.toLowerCase() || !/^[0-9a-f]+$/.test(value)) malformed();
  const result = new Uint8Array(bytes);
  for (let i = 0; i < bytes; i++) result[i] = Number.parseInt(value.slice(i * 2, i * 2 + 2), 16);
  if (requireUncompressed && result[0] !== 4) malformed();
  return result;
}

function validCapabilities(value: unknown): asserts value is number {
  if (typeof value !== "number" || !Number.isInteger(value) || value < 0 || value > 15 || (value & CAPABILITIES.observe) === 0) malformed();
}

function validID(value: string): boolean {
  return value.length > 0 && value.length <= 64 && [...value].every((c) => c.charCodeAt(0) >= 0x21 && c.charCodeAt(0) <= 0x7e);
}
function isControlType(value: unknown): value is ControlType { return typeof value === "string" && (CONTROL_TYPES as readonly string[]).includes(value); }
function isObject(value: unknown): value is Record<string, unknown> { return typeof value === "object" && value !== null && !Array.isArray(value); }
function exactKeys(value: Record<string, unknown>, required: string[], optional: string[] = []): void {
  const allowed = new Set([...required, ...optional]);
  if (required.some((key) => !Object.prototype.hasOwnProperty.call(value, key)) || Object.keys(value).some((key) => !allowed.has(key))) malformed();
}

// JSON.parse permits duplicate names. This small structural scan closes that ambiguity
// without introducing a JSON parser dependency.
function rejectDuplicateKeys(text: string): void {
  let index = 0;
  const stringEnd = (): number => {
    if (text[index] !== '"') malformed();
    const start = index++;
    while (index < text.length) {
      const c = text[index++];
      if (c === "\\") index++;
      else if (c === '"') { JSON.parse(text.slice(start, index)); return index; }
    }
    malformed();
  };
  const whitespace = (): void => { while (/\s/.test(text[index] ?? "")) index++; };
  const value = (depth: number): void => {
    if (depth > 16) malformed();
    whitespace();
    if (text[index] === "{") {
      index++; whitespace(); const keys = new Set<string>();
      if (text[index] === "}") { index++; return; }
      while (true) {
        whitespace(); const start = index; stringEnd(); const key = JSON.parse(text.slice(start, index)) as string;
        if (keys.has(key)) malformed(); keys.add(key); whitespace(); if (text[index++] !== ":") malformed(); value(depth + 1); whitespace();
        if (text[index] === "}") { index++; return; } if (text[index++] !== ",") malformed();
      }
    }
    if (text[index] === "[") {
      index++; whitespace(); if (text[index] === "]") { index++; return; }
      while (true) { value(depth + 1); whitespace(); if (text[index] === "]") { index++; return; } if (text[index++] !== ",") malformed(); }
    }
    if (text[index] === '"') { stringEnd(); return; }
    const start = index; while (index < text.length && !/[\s,\]}]/.test(text[index] ?? "")) index++;
    if (start === index) malformed();
    const token = text.slice(start, index);
    if (token[0] === "-" || (token[0] !== undefined && token[0] >= "0" && token[0] <= "9")) validateJSONNumber(token);
  };
  value(0); whitespace(); if (index !== text.length) malformed();
}

function validateJSONNumber(value: string): void {
  if (value.includes(".") || value.includes("e") || value.includes("E") || !/^-?(0|[1-9][0-9]*)$/.test(value)) malformed();
  const digits = value[0] === "-" ? value.slice(1) : value;
  if (digits.length > 16 || (digits.length === 16 && digits > "9007199254740991")) malformed();
}
