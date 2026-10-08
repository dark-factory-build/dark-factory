/** The browser contract has no generation: it is unversioned and tolerant of
 * added members by owner decision on 4 September 2026. This name identifies
 * the contract; it never moves. */
export const BROWSER_PROTOCOL_NAME = "dark-factory/browser" as const;
/** The fixed third byte of every binary terminal frame, beside the "DF" magic. */
export const TERMINAL_FRAME_VERSION = 1 as const;
export const MAX_TASK_ATTACHMENTS = 8;
export const MAX_TASK_CONTENT = 8;
export const MAX_TASK_ATTACHMENT_BYTES = 8 * 1024 * 1024;
export const TASK_ATTACHMENT_CHUNK_BYTES = 24 * 1024;
export const MAX_CONTROL_BYTES = 64 * 1024;
export const MAX_TERMINAL_PAYLOAD = 8 * 1024;
export const TERMINAL_HEADER_BYTES = 40;
export const MAX_JSON_DEPTH = 16;
export const MAX_ARRAY_ITEMS = 32;
export const MAX_OBJECT_MEMBERS = 32;
/** Only bounded server observations may exceed MAX_CONTROL_BYTES. */
export const MAX_SNAPSHOT_BYTES = 1024 * 1024;
export const MAX_SNAPSHOT_ENTITIES = 4096;
export const MAX_PROJECT_NAME_BYTES = 128;
export const MAX_AGENT_NAME_BYTES = 128;
export const MAX_TASK_TITLE_BYTES = 1024;
export const MAX_TASK_BLOCKED_REASON_BYTES = 200;
export const MAX_HUMAN_QUESTION_BYTES = 8192;
export const MAX_HUMAN_REPLY_BYTES = 8192;
export const MAX_TASK_INSTRUCTION_BYTES = 32768;
export const MAX_FACTORY_CAPACITY = 1024;
export const MAX_TASK_PRIORITY = 1_000_000;
export const MAX_SQLITE_INTEGER = 9_223_372_036_854_775_807n;
export const MAX_TERMINAL_UNACKED_BYTES = 65_536;
export const TERMINAL_ACK_TIMEOUT_MS = 10_000;
export const TERMINAL_LEASE_RENEW_INTERVAL_MS = 10_000;
export const MAX_TERMINAL_ROWS = 4096;
export const MAX_TERMINAL_COLS = 4096;
export const MAX_AGENT_MODEL_BYTES = 128;
export const MAX_MODEL_SOURCE_BYTES = 1024;
export const MAX_REMOTE_INVITE_LINK_BYTES = 8192;
export const MAX_REMOTE_INVITE_SVG_BYTES = 32768;
export const MAX_IDLE_AFTER_SECONDS = 604800;
export const MAX_IDLE_RUN_BUDGET = 1000000;

export const CAPABILITIES = {
  observe: 1,
  private_human_request_detail: 2,
  human_actions: 4,
  terminal_input: 8,
  administration: 16,
} as const;

export type CapabilityName = keyof typeof CAPABILITIES;
export type CapabilityMask = number;

export const CONTROL_MANIFEST = [
  { type: "HELLO", direction: "server" },
  { type: "PAIR_PROVE", direction: "client" }, { type: "PAIR_RESULT", direction: "server" },
  { type: "AUTH_PROVE", direction: "client" }, { type: "AUTH_RESULT", direction: "server" },
  { type: "STATE_GET", direction: "client" }, { type: "STATE_SNAPSHOT", direction: "server" },
  { type: "STATE_WATCH", direction: "client" }, { type: "STATE_CHANGED", direction: "server" },
  { type: "HUMAN_REQUEST_DETAIL_GET", direction: "client" }, { type: "HUMAN_REQUEST_DETAIL", direction: "server" },
  { type: "HUMAN_REQUEST_REPLY", direction: "client" }, { type: "HUMAN_REQUEST_REPLY_RESULT", direction: "server" },
  { type: "HUMAN_REQUEST_CANCEL_RUN", direction: "client" }, { type: "HUMAN_REQUEST_CANCEL_RUN_RESULT", direction: "server" },
  { type: "ATTACHMENT_RETENTION", direction: "client" }, { type: "ATTACHMENT_RETENTION_RESULT", direction: "server" },
  { type: "FACTORY_DISPATCH", direction: "client" }, { type: "FACTORY_DISPATCH_RESULT", direction: "server" },
  { type: "TASK_ATTACHMENT", direction: "client" }, { type: "TASK_ATTACHMENT_RESULT", direction: "server" },
  { type: "TASK_ENQUEUE", direction: "client" }, { type: "TASK_ENQUEUE_RESULT", direction: "server" },
  { type: "TERMINAL_TARGET_GET", direction: "client" }, { type: "TERMINAL_TARGET", direction: "server" },
  { type: "TERMINAL_ATTACH", direction: "client" }, { type: "TERMINAL_ATTACHED", direction: "server" },
  { type: "TERMINAL_ACK", direction: "client" },
  { type: "TERMINAL_LEASE_ACQUIRE", direction: "client" },
  { type: "TERMINAL_LEASE_RENEW", direction: "client" },
  { type: "TERMINAL_LEASE_RELEASE", direction: "client" }, { type: "TERMINAL_LEASE_RESULT", direction: "server" },
  { type: "TERMINAL_RESIZE", direction: "client" }, { type: "TERMINAL_RESIZED", direction: "server" },
  { type: "TERMINAL_DETACH", direction: "client" }, { type: "TERMINAL_DETACHED", direction: "server" },
  { type: "TERMINAL_INPUT_RESULT", direction: "server" },
  { type: "TERMINAL_EOF", direction: "server" },
  { type: "TERMINAL_EXIT", direction: "server" },
  { type: "TERMINAL_RESET", direction: "server" },
  { type: "AGENT_UPDATE", direction: "client" }, { type: "AGENT_UPDATE_RESULT", direction: "server" },
  { type: "PROJECT_LIMITS", direction: "client" }, { type: "PROJECT_LIMITS_RESULT", direction: "server" },
  { type: "PROJECT_CREATE", direction: "client" }, { type: "PROJECT_CREATE_RESULT", direction: "server" },
  { type: "REPOSITORIES_GET", direction: "client" }, { type: "REPOSITORIES", direction: "server" },
  { type: "REPOSITORY_MUTATE", direction: "client" }, { type: "REPOSITORY_MUTATE_RESULT", direction: "server" },
  { type: "INTAKE", direction: "client" }, { type: "INTAKE_RESULT", direction: "server" },
  { type: "TASK_UPDATE", direction: "client" }, { type: "TASK_UPDATE_RESULT", direction: "server" },
  { type: "OPERATIONAL_GRAPH_GET", direction: "client" }, { type: "OPERATIONAL_GRAPH", direction: "server" },
  { type: "OPERATIONAL_NODE_GET", direction: "client" }, { type: "OPERATIONAL_NODE", direction: "server" },
  { type: "RUN_PATHS_GET", direction: "client" }, { type: "RUN_PATHS", direction: "server" },
  { type: "ACCOUNTS_DISCOVER", direction: "client" }, { type: "ACCOUNTS", direction: "server" },
  { type: "ACCOUNT_LINK", direction: "client" }, { type: "ACCOUNT_LINK_RESULT", direction: "server" },
  { type: "ACCOUNT_UPDATE", direction: "client" }, { type: "ACCOUNT_UPDATE_RESULT", direction: "server" },
  { type: "BROWSER_CLIENTS_GET", direction: "client" }, { type: "BROWSER_CLIENTS", direction: "server" },
  { type: "BROWSER_CLIENT_REVOKE", direction: "client" }, { type: "BROWSER_CLIENT_REVOKE_RESULT", direction: "server" },
  { type: "GITHUB_CONNECTION", direction: "client" }, { type: "GITHUB_CONNECTION_RESULT", direction: "server" },
  { type: "REMOTE_INVITE", direction: "client" }, { type: "REMOTE_INVITE_RESULT", direction: "server" },
  { type: "PUSH_SUBSCRIBE", direction: "client" }, { type: "PUSH_SUBSCRIBE_RESULT", direction: "server" },
  { type: "TELEMETRY_INGEST", direction: "client" }, { type: "TELEMETRY_INGEST_RESULT", direction: "server" },
  { type: "ERROR", direction: "both" },
  { type: "AGENT_CONTROL", direction: "client" }, { type: "AGENT_CONTROL_RESULT", direction: "server" },
  { type: "TASK_HISTORY_GET", direction: "client" }, { type: "TASK_HISTORY", direction: "server" },
  { type: "TASK_DETAIL_GET", direction: "client" }, { type: "TASK_DETAIL", direction: "server" },
  { type: "TASK_LIST_GET", direction: "client" }, { type: "TASK_LIST", direction: "server" },
  { type: "PROJECT_CONTENT", direction: "client" }, { type: "PROJECT_CONTENT_RESULT", direction: "server" },
] as const;
export const CONTROL_TYPES = CONTROL_MANIFEST.map((entry) => entry.type);
export type ControlType = (typeof CONTROL_TYPES)[number];

export const ERROR_CODES = [
  "unauthorized",
  "invalid_request",
  "rate_limited",
  "not_found",
  "stale",
  "too_large",
  "internal",
  "unsupported",
] as const;
export type ErrorCode = (typeof ERROR_CODES)[number];

export const TERMINAL_OPCODES = {
  TERMINAL_INPUT: 1,
  TERMINAL_OUTPUT: 2,
} as const;
