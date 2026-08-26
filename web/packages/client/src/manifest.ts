export const PROTOCOL_VERSION = 1 as const;
export const MAX_CONTROL_BYTES = 64 * 1024;
export const MAX_TERMINAL_PAYLOAD = 64 * 1024;
export const TERMINAL_HEADER_BYTES = 40;

export const CAPABILITIES = {
  observe: 1,
  private_human_request_detail: 2,
  human_actions: 4,
  terminal_input: 8,
} as const;

export type CapabilityName = keyof typeof CAPABILITIES;
export type CapabilityMask = number;

export const CONTROL_MANIFEST = [
  { type: "HELLO", direction: "server", id: "forbidden", fixture: "hello.json" },
  { type: "PAIR_PROVE", direction: "client", id: "required", fixture: "pair_prove.json" },
  { type: "PAIR_RESULT", direction: "server", id: "required", fixture: "pair_result.json" },
  { type: "AUTH_PROVE", direction: "client", id: "required", fixture: "auth_prove.json" },
  { type: "AUTH_RESULT", direction: "server", id: "required", fixture: "auth_result.json" },
  { type: "ERROR", direction: "both", id: "optional", fixture: "error.json" },
] as const;
export const CONTROL_TYPES = CONTROL_MANIFEST.map((entry) => entry.type);
export type ControlType = (typeof CONTROL_TYPES)[number];

export const ERROR_CODES = [
  "unauthorized",
  "invalid_request",
  "unsupported_version",
  "rate_limited",
  "not_found",
  "stale",
  "internal",
] as const;
export type ErrorCode = (typeof ERROR_CODES)[number];

export const TERMINAL_OPCODES = {
  TERMINAL_INPUT: 1,
  TERMINAL_OUTPUT: 2,
} as const;

export const BROWSER_MANIFEST = {
  version: PROTOCOL_VERSION,
  capabilities: CAPABILITIES,
  control: CONTROL_MANIFEST,
  terminal: {
    magic: "DF",
    version: PROTOCOL_VERSION,
    headerBytes: TERMINAL_HEADER_BYTES,
    maxPayloadBytes: MAX_TERMINAL_PAYLOAD,
    opcodes: TERMINAL_OPCODES,
  },
} as const;
