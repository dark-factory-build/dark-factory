export const PROTOCOL_VERSION = 1 as const;
export const MAX_CONTROL_BYTES = 64 * 1024;
export const MAX_TERMINAL_PAYLOAD = 8 * 1024;
export const TERMINAL_HEADER_BYTES = 40;
export const MAX_JSON_DEPTH = 16;
export const MAX_ARRAY_ITEMS = 32;
export const MAX_OBJECT_MEMBERS = 32;
export const MAX_STATE_PAGE_ITEMS = 8;
export const MAX_FACTORY_PAGE_ITEMS = 1;
export const MAX_CURSOR_BYTES = 256;
export const MAX_PROJECT_NAME_BYTES = 128;
export const MAX_AGENT_NAME_BYTES = 128;
export const MAX_TASK_TITLE_BYTES = 1024;
export const MAX_HUMAN_QUESTION_BYTES = 8192;
export const MAX_HUMAN_REPLY_BYTES = 8192;
export const MAX_FACTORY_CAPACITY = 1024;
export const MAX_TASK_PRIORITY = 1_000_000;
export const MAX_SQLITE_INTEGER = 9_223_372_036_854_775_807n;
export const MAX_TERMINAL_UNACKED_BYTES = 65_536;
export const TERMINAL_ACK_TIMEOUT_MS = 10_000;
export const MAX_TERMINAL_ROWS = 4096;
export const MAX_TERMINAL_COLS = 4096;

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
  { type: "STATE_GET", direction: "client", id: "required", fixture: "state_get.json" },
  { type: "STATE_SNAPSHOT", direction: "server", id: "required", fixture: "state_snapshot.json" },
  { type: "STATE_RESTART", direction: "server", id: "required", fixture: "state_restart.json" },
  { type: "STATE_SUBSCRIBE", direction: "client", id: "required", fixture: "state_subscribe.json" },
  { type: "STATE_EVENT", direction: "server", id: "required", fixture: "state_event.json" },
  { type: "STATE_ENTITY_GET", direction: "client", id: "required", fixture: "state_entity_get.json" },
  { type: "STATE_ENTITY", direction: "server", id: "required", fixture: "state_entity.json" },
  { type: "HUMAN_REQUEST_DETAIL_GET", direction: "client", id: "required", fixture: "human_request_detail_get.json" },
  { type: "HUMAN_REQUEST_DETAIL", direction: "server", id: "required", fixture: "human_request_detail.json" },
  { type: "HUMAN_REQUEST_REPLY", direction: "client", id: "required", fixture: "human_request_reply.json" },
  { type: "HUMAN_REQUEST_REPLY_RESULT", direction: "server", id: "required", fixture: "human_request_reply_result.json" },
  { type: "HUMAN_REQUEST_CANCEL_RUN", direction: "client", id: "required", fixture: "human_request_cancel_run.json" },
  { type: "HUMAN_REQUEST_ACTION_RESULT", direction: "server", id: "required", fixture: "human_request_action_result.json" },
  { type: "TERMINAL_ATTACH", direction: "client", id: "required", fixture: "terminal_attach.json" },
  { type: "TERMINAL_ATTACHED", direction: "server", id: "required", fixture: "terminal_attached.json" },
  { type: "TERMINAL_ACK", direction: "client", id: "forbidden", fixture: "terminal_ack.json" },
  { type: "TERMINAL_LEASE_ACQUIRE", direction: "client", id: "required", fixture: "terminal_lease_acquire.json" },
  { type: "TERMINAL_LEASE_RENEW", direction: "client", id: "required", fixture: "terminal_lease_renew.json" },
  { type: "TERMINAL_LEASE_RELEASE", direction: "client", id: "required", fixture: "terminal_lease_release.json" },
  { type: "TERMINAL_LEASE_RESULT", direction: "server", id: "required", fixture: "terminal_lease_result.json" },
  { type: "TERMINAL_RESIZE", direction: "client", id: "required", fixture: "terminal_resize.json" },
  { type: "TERMINAL_RESIZED", direction: "server", id: "required", fixture: "terminal_resized.json" },
  { type: "TERMINAL_DETACH", direction: "client", id: "required", fixture: "terminal_detach.json" },
  { type: "TERMINAL_DETACHED", direction: "server", id: "required", fixture: "terminal_detached.json" },
  { type: "TERMINAL_INPUT_RESULT", direction: "server", id: "required", fixture: "terminal_input_result.json" },
  { type: "TERMINAL_EOF", direction: "server", id: "required", fixture: "terminal_eof.json" },
  { type: "TERMINAL_EXIT", direction: "server", id: "required", fixture: "terminal_exit.json" },
  { type: "TERMINAL_RESET", direction: "server", id: "required", fixture: "terminal_reset.json" },
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
  "too_large",
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
  bounds: {
    maxControlBytes: MAX_CONTROL_BYTES,
    maxJSONDepth: MAX_JSON_DEPTH,
    maxArrayItems: MAX_ARRAY_ITEMS,
    maxObjectMembers: MAX_OBJECT_MEMBERS,
    maxStatePageItems: MAX_STATE_PAGE_ITEMS,
    maxFactoryPageItems: MAX_FACTORY_PAGE_ITEMS,
    maxCursorBytes: MAX_CURSOR_BYTES,
    maxProjectNameBytes: MAX_PROJECT_NAME_BYTES,
    maxAgentNameBytes: MAX_AGENT_NAME_BYTES,
    maxTaskTitleBytes: MAX_TASK_TITLE_BYTES,
    maxHumanQuestionBytes: MAX_HUMAN_QUESTION_BYTES,
    maxHumanReplyBytes: MAX_HUMAN_REPLY_BYTES,
    maxFactoryCapacity: MAX_FACTORY_CAPACITY,
    maxTaskPriority: MAX_TASK_PRIORITY,
    maxSQLiteInteger: MAX_SQLITE_INTEGER,
    maxTerminalUnackedBytes: MAX_TERMINAL_UNACKED_BYTES,
    terminalAckTimeoutMs: TERMINAL_ACK_TIMEOUT_MS,
    maxTerminalRows: MAX_TERMINAL_ROWS,
    maxTerminalCols: MAX_TERMINAL_COLS,
  },
  control: CONTROL_MANIFEST,
  terminal: {
    magic: "DF",
    version: PROTOCOL_VERSION,
    headerBytes: TERMINAL_HEADER_BYTES,
    maxPayloadBytes: MAX_TERMINAL_PAYLOAD,
    opcodes: TERMINAL_OPCODES,
  },
} as const;
