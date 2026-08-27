import { malformed, normalizeBoundary, ProtocolError } from "./errors.js";
import {
  CAPABILITIES,
  CONTROL_TYPES,
  ERROR_CODES,
  MAX_AGENT_NAME_BYTES,
  MAX_ARRAY_ITEMS,
  MAX_CONTROL_BYTES,
  MAX_CURSOR_BYTES,
  MAX_FACTORY_PAGE_ITEMS,
  MAX_FACTORY_CAPACITY,
  MAX_HUMAN_QUESTION_BYTES,
  MAX_HUMAN_REPLY_BYTES,
  MAX_JSON_DEPTH,
  MAX_OBJECT_MEMBERS,
  MAX_PROJECT_NAME_BYTES,
  MAX_SQLITE_INTEGER,
  MAX_STATE_PAGE_ITEMS,
  MAX_TERMINAL_COLS,
  MAX_TERMINAL_PAYLOAD,
  MAX_TERMINAL_ROWS,
  MAX_TERMINAL_UNACKED_BYTES,
  MAX_TASK_PRIORITY,
  MAX_TASK_TITLE_BYTES,
  type CapabilityMask,
  type ControlType,
  type ErrorCode,
} from "./manifest.js";

export type HelloBody = { daemon_id: string; boot_id: string; connection_nonce: string };
export type PairProveBody = { challenge: string; public_key_sec1: string; signature: string };
export type PairResultBody = { client_id: string; capabilities: CapabilityMask };
export type AuthProveBody = { client_id: string; signature: string };
export type AuthResultBody = { client_id: string; capabilities: CapabilityMask };
export type ErrorBody = { code: ErrorCode; retryable: boolean };

export type StateKind = "factory" | "project" | "agent" | "task" | "human_request";
export type FactoryItem = { dispatch_enabled: boolean; capacity: number; active_runs: number; revision: bigint };
export type ProjectItem = { id: string; name: string; revision: bigint };
export type AgentItem = { id: string; project_id: string; name: string; role: "orchestrator" | "worker"; paused: boolean; revision: bigint };
export type TaskItem = { id: string; project_id: string; assigned_agent_id: string; title: string; status: "queued" | "running" | "blocked" | "succeeded" | "failed" | "cancelled"; priority: number; revision: bigint };
export type HumanRequestItem = {
  id: string; project_id: string; agent_id: string; task_id: string; run_id: string;
  created_at: bigint; updated_at: bigint; revision: bigint; kind: "question";
  status: "open" | "delivering" | "delivery_unknown"; reply_max_bytes: number;
  can_reply: boolean; can_open_terminal: boolean;
};

export type StateGetBody = { cursor: string | null };
export type StateSnapshotBody =
  | { head: bigint; kind: "factory"; items: [FactoryItem]; next_cursor: string | null }
  | { head: bigint; kind: "project"; items: ProjectItem[]; next_cursor: string | null }
  | { head: bigint; kind: "agent"; items: AgentItem[]; next_cursor: string | null }
  | { head: bigint; kind: "task"; items: TaskItem[]; next_cursor: string | null }
  | { head: bigint; kind: "human_request"; items: HumanRequestItem[]; next_cursor: string | null };
export type RestartReason = "head_changed" | "gap" | "pruned" | "hidden_dependency";
export type StateRestartBody = { head: bigint; floor: bigint; reason: RestartReason };
export type StateSubscribeBody = { after: bigint };
export type EntityChangedEvent = { event: "entity_changed"; sequence: bigint; head: bigint; entity_kind: StateKind; entity_id: string; revision: bigint; deleted: boolean };
export type HiddenAdvanceEvent = { event: "hidden_advance"; sequence: bigint; head: bigint };
export type StateEventBody = EntityChangedEvent | HiddenAdvanceEvent;
export type StateEntityGetBody = { kind: StateKind; id: string };
type DeletedStateEntity = { head: bigint; kind: StateKind; id: string; revision: bigint; deleted: true; item: null };
export type StateEntityBody =
  | DeletedStateEntity
  | { head: bigint; kind: "factory"; id: "factory"; revision: bigint; deleted: false; item: FactoryItem }
  | { head: bigint; kind: "project"; id: string; revision: bigint; deleted: false; item: ProjectItem }
  | { head: bigint; kind: "agent"; id: string; revision: bigint; deleted: false; item: AgentItem }
  | { head: bigint; kind: "task"; id: string; revision: bigint; deleted: false; item: TaskItem }
  | { head: bigint; kind: "human_request"; id: string; revision: bigint; deleted: false; item: HumanRequestItem };
export type HumanRequestDetailGetBody = { request_id: string; expected_revision: bigint };
export type HumanRequestDetailBody = { request_id: string; revision: bigint; question: string };
export type HumanRequestReplyBody = { run_id: string; request_id: string; expected_revision: bigint; reply: string };
export type HumanRequestReplyResultBody = { request_id: string; revision: bigint; status: "resolved" | "delivery_unknown" };
export type HumanRequestCancelRunBody = { run_id: string; request_id: string; expected_request_revision: bigint; expected_run_revision: bigint };
export type HumanRequestActionResultBody = { action: "cancel_run"; run_id: string; run_revision: bigint; request_id: string; request_revision: bigint; status: "resolved" };
export type TerminalAttachBody = { run_id: string; session_id: string; expected_run_revision: bigint; expected_session_revision: bigint; after_sequence: bigint };
export type TerminalAttachedBody = { session_id: string; floor: bigint; head: bigint; acknowledged_sequence: bigint; max_unacked_bytes: bigint };
export type TerminalAckBody = { session_id: string; next_sequence: bigint };
export type TerminalLeaseAcquireBody = { run_id: string; session_id: string; expected_run_revision: bigint; expected_session_revision: bigint };
export type TerminalLeaseRenewBody = { run_id: string; session_id: string; generation: bigint; expected_run_revision: bigint; expected_session_revision: bigint };
export type TerminalLeaseReleaseBody = { run_id: string; session_id: string; generation: bigint; expected_run_revision: bigint; expected_session_revision: bigint };
export type TerminalLeaseResultBody =
  | { operation: "acquired" | "renewed"; run_id: string; session_id: string; generation: bigint; expires_at_ms: bigint; last_input_sequence: bigint; run_revision: bigint; session_revision: bigint }
  | { operation: "released"; run_id: string; session_id: string; generation: bigint; last_input_sequence: bigint; run_revision: bigint; session_revision: bigint };
export type TerminalResizeBody = { run_id: string; session_id: string; generation: bigint; expected_run_revision: bigint; expected_session_revision: bigint; rows: number; cols: number };
export type TerminalResizedBody = { session_id: string; generation: bigint; rows: number; cols: number };
export type TerminalDetachBody = { session_id: string };
export type TerminalDetachedBody = { session_id: string };
export type TerminalInputResultBody = { session_id: string; generation: bigint; sequence: bigint; status: "accepted" | "rejected" | "partial" | "uncertain"; accepted_bytes: bigint };
export type TerminalEOFBody = { session_id: string };
export type TerminalExitBody = { session_id: string; exit_code: number; exit_signal: number; aborted: boolean };
export type TerminalResetBody = { session_id: string; floor: bigint; head: bigint };

export type HelloFrame = { v: 1; type: "HELLO"; body: HelloBody };
export type PairProveFrame = { v: 1; type: "PAIR_PROVE"; id: string; body: PairProveBody };
export type PairResultFrame = { v: 1; type: "PAIR_RESULT"; id: string; body: PairResultBody };
export type AuthProveFrame = { v: 1; type: "AUTH_PROVE"; id: string; body: AuthProveBody };
export type AuthResultFrame = { v: 1; type: "AUTH_RESULT"; id: string; body: AuthResultBody };
export type StateGetFrame = { v: 1; type: "STATE_GET"; id: string; body: StateGetBody };
export type StateSnapshotFrame = { v: 1; type: "STATE_SNAPSHOT"; id: string; body: StateSnapshotBody };
export type StateRestartFrame = { v: 1; type: "STATE_RESTART"; id: string; body: StateRestartBody };
export type StateSubscribeFrame = { v: 1; type: "STATE_SUBSCRIBE"; id: string; body: StateSubscribeBody };
export type StateEventFrame = { v: 1; type: "STATE_EVENT"; id: string; body: StateEventBody };
export type StateEntityGetFrame = { v: 1; type: "STATE_ENTITY_GET"; id: string; body: StateEntityGetBody };
export type StateEntityFrame = { v: 1; type: "STATE_ENTITY"; id: string; body: StateEntityBody };
export type HumanRequestDetailGetFrame = { v: 1; type: "HUMAN_REQUEST_DETAIL_GET"; id: string; body: HumanRequestDetailGetBody };
export type HumanRequestDetailFrame = { v: 1; type: "HUMAN_REQUEST_DETAIL"; id: string; body: HumanRequestDetailBody };
export type ErrorFrame = { v: 1; type: "ERROR"; id?: string; body: ErrorBody };
export type TerminalControlFrame =
  | { v: 1; type: "TERMINAL_ATTACH"; id: string; body: TerminalAttachBody }
  | { v: 1; type: "TERMINAL_ACK"; body: TerminalAckBody }
  | { v: 1; type: "TERMINAL_LEASE_ACQUIRE"; id: string; body: TerminalLeaseAcquireBody }
  | { v: 1; type: "TERMINAL_LEASE_RENEW"; id: string; body: TerminalLeaseRenewBody }
  | { v: 1; type: "TERMINAL_LEASE_RELEASE"; id: string; body: TerminalLeaseReleaseBody }
  | { v: 1; type: "TERMINAL_RESIZE"; id: string; body: TerminalResizeBody }
  | { v: 1; type: "TERMINAL_DETACH"; id: string; body: TerminalDetachBody };
export type TerminalServerControlFrame =
  | { v: 1; type: "TERMINAL_ATTACHED"; id: string; body: TerminalAttachedBody }
  | { v: 1; type: "TERMINAL_LEASE_RESULT"; id: string; body: TerminalLeaseResultBody }
  | { v: 1; type: "TERMINAL_RESIZED"; id: string; body: TerminalResizedBody }
  | { v: 1; type: "TERMINAL_DETACHED"; id: string; body: TerminalDetachedBody }
  | { v: 1; type: "TERMINAL_INPUT_RESULT"; id: string; body: TerminalInputResultBody }
  | { v: 1; type: "TERMINAL_EOF"; id: string; body: TerminalEOFBody }
  | { v: 1; type: "TERMINAL_EXIT"; id: string; body: TerminalExitBody }
  | { v: 1; type: "TERMINAL_RESET"; id: string; body: TerminalResetBody };

export type ServerControlFrame = HelloFrame | PairResultFrame | AuthResultFrame | StateSnapshotFrame | StateRestartFrame | StateEventFrame | StateEntityFrame | HumanRequestDetailFrame
  | { v: 1; type: "HUMAN_REQUEST_REPLY_RESULT"; id: string; body: HumanRequestReplyResultBody }
  | { v: 1; type: "HUMAN_REQUEST_ACTION_RESULT"; id: string; body: HumanRequestActionResultBody }
  | TerminalServerControlFrame | ErrorFrame;
export type ClientControlFrame = PairProveFrame | AuthProveFrame | StateGetFrame | StateSubscribeFrame | StateEntityGetFrame | HumanRequestDetailGetFrame
  | { v: 1; type: "HUMAN_REQUEST_REPLY"; id: string; body: HumanRequestReplyBody }
  | { v: 1; type: "HUMAN_REQUEST_CANCEL_RUN"; id: string; body: HumanRequestCancelRunBody }
  | TerminalControlFrame | ErrorFrame;
type ControlBody = ClientControlFrame["body"] | ServerControlFrame["body"];

const HEX_BYTES = { daemon_id: 16, boot_id: 16, connection_nonce: 32, challenge: 32, client_id: 16, public_key_sec1: 65, signature: 64 } as const;
const CLIENT_TYPES: readonly ControlType[] = ["PAIR_PROVE", "AUTH_PROVE", "STATE_GET", "STATE_SUBSCRIBE", "STATE_ENTITY_GET", "HUMAN_REQUEST_DETAIL_GET", "HUMAN_REQUEST_REPLY", "HUMAN_REQUEST_CANCEL_RUN", "TERMINAL_ATTACH", "TERMINAL_ACK", "TERMINAL_LEASE_ACQUIRE", "TERMINAL_LEASE_RENEW", "TERMINAL_LEASE_RELEASE", "TERMINAL_RESIZE", "TERMINAL_DETACH", "ERROR"];
const SERVER_TYPES: readonly ControlType[] = ["HELLO", "PAIR_RESULT", "AUTH_RESULT", "STATE_SNAPSHOT", "STATE_RESTART", "STATE_EVENT", "STATE_ENTITY", "HUMAN_REQUEST_DETAIL", "HUMAN_REQUEST_REPLY_RESULT", "HUMAN_REQUEST_ACTION_RESULT", "TERMINAL_ATTACHED", "TERMINAL_LEASE_RESULT", "TERMINAL_RESIZED", "TERMINAL_DETACHED", "TERMINAL_INPUT_RESULT", "TERMINAL_EOF", "TERMINAL_EXIT", "TERMINAL_RESET", "ERROR"];

export function encodeClientControl(frame: ClientControlFrame): string { return normalizeBoundary(() => encode(frame, validateControl(frame, "client"))); }
export function encodePairProve(id: string, body: PairProveBody): string { return encodeClientControl({ v: 1, type: "PAIR_PROVE", id, body }); }
export function encodeAuthProve(id: string, body: AuthProveBody): string { return encodeClientControl({ v: 1, type: "AUTH_PROVE", id, body }); }
export function encodeStateGet(id: string, body: StateGetBody): string { return encodeClientControl({ v: 1, type: "STATE_GET", id, body }); }
export function encodeStateSubscribe(id: string, body: StateSubscribeBody): string { return encodeClientControl({ v: 1, type: "STATE_SUBSCRIBE", id, body }); }
export function encodeStateEntityGet(id: string, body: StateEntityGetBody): string { return encodeClientControl({ v: 1, type: "STATE_ENTITY_GET", id, body }); }
export function encodeHumanRequestDetailGet(id: string, body: HumanRequestDetailGetBody): string { return encodeClientControl({ v: 1, type: "HUMAN_REQUEST_DETAIL_GET", id, body }); }
export function encodeHumanRequestReply(id: string, body: HumanRequestReplyBody): string { return encodeClientControl({ v: 1, type: "HUMAN_REQUEST_REPLY", id, body }); }
export function encodeHumanRequestCancelRun(id: string, body: HumanRequestCancelRunBody): string { return encodeClientControl({ v: 1, type: "HUMAN_REQUEST_CANCEL_RUN", id, body }); }
export function encodeTerminalAttach(id: string, body: TerminalAttachBody): string { return encodeClientControl({ v: 1, type: "TERMINAL_ATTACH", id, body }); }
export function encodeTerminalAck(body: TerminalAckBody): string { return encodeClientControl({ v: 1, type: "TERMINAL_ACK", body }); }
export function encodeTerminalLeaseAcquire(id: string, body: TerminalLeaseAcquireBody): string { return encodeClientControl({ v: 1, type: "TERMINAL_LEASE_ACQUIRE", id, body }); }
export function encodeTerminalLeaseRenew(id: string, body: TerminalLeaseRenewBody): string { return encodeClientControl({ v: 1, type: "TERMINAL_LEASE_RENEW", id, body }); }
export function encodeTerminalLeaseRelease(id: string, body: TerminalLeaseReleaseBody): string { return encodeClientControl({ v: 1, type: "TERMINAL_LEASE_RELEASE", id, body }); }
export function encodeTerminalResize(id: string, body: TerminalResizeBody): string { return encodeClientControl({ v: 1, type: "TERMINAL_RESIZE", id, body }); }
export function encodeTerminalDetach(id: string, body: TerminalDetachBody): string { return encodeClientControl({ v: 1, type: "TERMINAL_DETACH", id, body }); }
export function encodeClientError(body: ErrorBody, id?: string): string { return encodeClientControl({ v: 1, type: "ERROR", ...(id === undefined ? {} : { id }), body }); }

export function encodeServerControl(frame: ServerControlFrame): string { return normalizeBoundary(() => encode(frame, validateControl(frame, "server"))); }
export function encodeHello(body: HelloBody): string { return encodeServerControl({ v: 1, type: "HELLO", body }); }
export function encodePairResult(id: string, body: PairResultBody): string { return encodeServerControl({ v: 1, type: "PAIR_RESULT", id, body }); }
export function encodeAuthResult(id: string, body: AuthResultBody): string { return encodeServerControl({ v: 1, type: "AUTH_RESULT", id, body }); }
export function encodeStateSnapshot(id: string, body: StateSnapshotBody): string { return encodeServerControl({ v: 1, type: "STATE_SNAPSHOT", id, body }); }
export function encodeStateRestart(id: string, body: StateRestartBody): string { return encodeServerControl({ v: 1, type: "STATE_RESTART", id, body }); }
export function encodeStateEvent(id: string, body: StateEventBody): string { return encodeServerControl({ v: 1, type: "STATE_EVENT", id, body }); }
export function encodeStateEntity(id: string, body: StateEntityBody): string { return encodeServerControl({ v: 1, type: "STATE_ENTITY", id, body }); }
export function encodeHumanRequestDetail(id: string, body: HumanRequestDetailBody): string { return encodeServerControl({ v: 1, type: "HUMAN_REQUEST_DETAIL", id, body }); }
export function encodeHumanRequestReplyResult(id: string, body: HumanRequestReplyResultBody): string { return encodeServerControl({ v: 1, type: "HUMAN_REQUEST_REPLY_RESULT", id, body }); }
export function encodeHumanRequestActionResult(id: string, body: HumanRequestActionResultBody): string { return encodeServerControl({ v: 1, type: "HUMAN_REQUEST_ACTION_RESULT", id, body }); }
export function encodeTerminalAttached(id: string, body: TerminalAttachedBody): string { return encodeServerControl({ v: 1, type: "TERMINAL_ATTACHED", id, body }); }
export function encodeTerminalLeaseResult(id: string, body: TerminalLeaseResultBody): string { return encodeServerControl({ v: 1, type: "TERMINAL_LEASE_RESULT", id, body }); }
export function encodeTerminalResized(id: string, body: TerminalResizedBody): string { return encodeServerControl({ v: 1, type: "TERMINAL_RESIZED", id, body }); }
export function encodeTerminalDetached(id: string, body: TerminalDetachedBody): string { return encodeServerControl({ v: 1, type: "TERMINAL_DETACHED", id, body }); }
export function encodeTerminalInputResult(id: string, body: TerminalInputResultBody): string { return encodeServerControl({ v: 1, type: "TERMINAL_INPUT_RESULT", id, body }); }
export function encodeTerminalEOF(id: string, body: TerminalEOFBody): string { return encodeServerControl({ v: 1, type: "TERMINAL_EOF", id, body }); }
export function encodeTerminalExit(id: string, body: TerminalExitBody): string { return encodeServerControl({ v: 1, type: "TERMINAL_EXIT", id, body }); }
export function encodeTerminalReset(id: string, body: TerminalResetBody): string { return encodeServerControl({ v: 1, type: "TERMINAL_RESET", id, body }); }
export function encodeServerError(body: ErrorBody, id?: string): string { return encodeServerControl({ v: 1, type: "ERROR", ...(id === undefined ? {} : { id }), body }); }
export function decodeClientControl(data: string | Uint8Array): ClientControlFrame { return normalizeBoundary(() => decodeControl(data, "client") as ClientControlFrame); }
export function decodeServerControl(data: string | Uint8Array): ServerControlFrame { return normalizeBoundary(() => decodeControl(data, "server") as ServerControlFrame); }

function encode(frame: ClientControlFrame | ServerControlFrame, body: ControlBody): string {
  const envelope: Record<string, unknown> = { v: 1, type: frame.type };
  if ("id" in frame) envelope.id = frame.id;
  envelope.body = wireValue(body);
  const result = JSON.stringify(envelope);
  if (new TextEncoder().encode(result).length > MAX_CONTROL_BYTES) throw new ProtocolError("oversized");
  return result;
}
function wireValue(value: unknown): unknown {
  if (typeof value === "bigint") return value.toString(10);
  if (Array.isArray(value)) return value.map(wireValue);
  if (isObject(value)) { const result: Record<string, unknown> = {}; for (const [key, item] of Object.entries(value)) result[key] = wireValue(item); return result; }
  return value;
}

function decodeControl(data: string | Uint8Array, role: "client" | "server"): ClientControlFrame | ServerControlFrame {
  let text: string;
  try { text = typeof data === "string" ? data : new TextDecoder("utf-8", { fatal: true }).decode(data); } catch { malformed(); }
  if (text.length === 0 || new TextEncoder().encode(text).length > MAX_CONTROL_BYTES) malformed();
  rejectDuplicateKeys(text);
  let value: unknown;
  try { value = JSON.parse(text) as unknown; } catch { malformed(); }
  if (!isObject(value)) malformed();
  exactKeys(value, ["v", "type", "body"], ["id"]);
  if (value.v !== 1) throw new ProtocolError("unsupported_version");
  if (!isControlType(value.type)) malformed();
  const hasID = Object.prototype.hasOwnProperty.call(value, "id");
  if (hasID && (typeof value.id !== "string" || !validID(value.id))) malformed();
  const requiredID = value.type !== "HELLO" && value.type !== "ERROR" && value.type !== "TERMINAL_ACK";
  if (requiredID !== hasID) malformed();
  if (role === "client" && !CLIENT_TYPES.includes(value.type)) throw new ProtocolError("wrong_direction");
  if (role === "server" && !SERVER_TYPES.includes(value.type)) throw new ProtocolError("wrong_direction");
  const body = validateBody(value.type, value.body, true);
  return { v: 1, type: value.type, ...(hasID ? { id: value.id as string } : {}), body } as ClientControlFrame | ServerControlFrame;
}
function validateControl(frame: ClientControlFrame | ServerControlFrame, role: "client" | "server"): ControlBody {
  if (!isObject(frame) || frame.v !== 1 || !isControlType(frame.type)) malformed();
  if (role === "client" && !CLIENT_TYPES.includes(frame.type) || role === "server" && !SERVER_TYPES.includes(frame.type)) throw new ProtocolError("wrong_direction");
  const requires = frame.type !== "HELLO" && frame.type !== "ERROR" && frame.type !== "TERMINAL_ACK";
  if (requires && (!("id" in frame) || !validID(frame.id))) malformed();
  if (!requires && "id" in frame && (typeof frame.id !== "string" || !validID(frame.id))) malformed();
  return validateBody(frame.type, frame.body, false);
}

function validateBody(type: ControlType, body: unknown, wire: boolean): ControlBody {
  if (!isObject(body)) malformed();
  switch (type) {
    case "HELLO": exactKeys(body, ["daemon_id", "boot_id", "connection_nonce"]); return { daemon_id: fixedHex(body.daemon_id, HEX_BYTES.daemon_id), boot_id: fixedHex(body.boot_id, HEX_BYTES.boot_id), connection_nonce: fixedHex(body.connection_nonce, HEX_BYTES.connection_nonce) };
    case "PAIR_PROVE": exactKeys(body, ["challenge", "public_key_sec1", "signature"]); return { challenge: fixedHex(body.challenge, HEX_BYTES.challenge), public_key_sec1: fixedHex(body.public_key_sec1, HEX_BYTES.public_key_sec1, true), signature: fixedHex(body.signature, HEX_BYTES.signature) };
    case "AUTH_PROVE": exactKeys(body, ["client_id", "signature"]); return { client_id: fixedHex(body.client_id, HEX_BYTES.client_id), signature: fixedHex(body.signature, HEX_BYTES.signature) };
    case "PAIR_RESULT": case "AUTH_RESULT": exactKeys(body, ["client_id", "capabilities"]); return { client_id: fixedHex(body.client_id, HEX_BYTES.client_id), capabilities: capabilities(body.capabilities) };
    case "STATE_GET": exactKeys(body, ["cursor"]); return { cursor: cursor(body.cursor) };
    case "STATE_SNAPSHOT": return stateSnapshot(body, wire);
    case "STATE_RESTART": {
      exactKeys(body, ["head", "floor", "reason"]); const head = decimal(body.head, wire); const floor = decimal(body.floor, wire);
      if ((head === 0n ? floor !== 1n : floor > head) || typeof body.reason !== "string" || !["head_changed", "gap", "pruned", "hidden_dependency"].includes(body.reason)) malformed();
      return { head, floor, reason: body.reason as RestartReason };
    }
    case "STATE_SUBSCRIBE": exactKeys(body, ["after"]); return { after: decimal(body.after, wire) };
    case "STATE_EVENT": return stateEvent(body, wire);
    case "STATE_ENTITY_GET": { exactKeys(body, ["kind", "id"]); const kind = stateKind(body.kind); return { kind, id: entityID(kind, body.id) }; }
    case "STATE_ENTITY": return stateEntity(body, wire);
    case "HUMAN_REQUEST_DETAIL_GET": exactKeys(body, ["request_id", "expected_revision"]); return { request_id: dynamicID(body.request_id), expected_revision: decimal(body.expected_revision, wire, true) };
    case "HUMAN_REQUEST_DETAIL": exactKeys(body, ["request_id", "revision", "question"]); return { request_id: dynamicID(body.request_id), revision: decimal(body.revision, wire, true), question: boundedText(body.question, 1, MAX_HUMAN_QUESTION_BYTES) };
    case "HUMAN_REQUEST_REPLY": exactKeys(body, ["run_id", "request_id", "expected_revision", "reply"]); return { run_id: dynamicID(body.run_id), request_id: dynamicID(body.request_id), expected_revision: decimal(body.expected_revision, wire, true), reply: boundedText(body.reply, 1, MAX_HUMAN_REPLY_BYTES) };
    case "HUMAN_REQUEST_REPLY_RESULT": exactKeys(body, ["request_id", "revision", "status"]); if (body.status !== "resolved" && body.status !== "delivery_unknown") malformed(); return { request_id: dynamicID(body.request_id), revision: decimal(body.revision, wire, true), status: body.status };
    case "HUMAN_REQUEST_CANCEL_RUN": exactKeys(body, ["run_id", "request_id", "expected_request_revision", "expected_run_revision"]); return { run_id: dynamicID(body.run_id), request_id: dynamicID(body.request_id), expected_request_revision: decimal(body.expected_request_revision, wire, true), expected_run_revision: decimal(body.expected_run_revision, wire, true) };
    case "HUMAN_REQUEST_ACTION_RESULT": exactKeys(body, ["action", "run_id", "run_revision", "request_id", "request_revision", "status"]); if (body.action !== "cancel_run" || body.status !== "resolved") malformed(); return { action: "cancel_run", run_id: dynamicID(body.run_id), run_revision: decimal(body.run_revision, wire, true), request_id: dynamicID(body.request_id), request_revision: decimal(body.request_revision, wire, true), status: "resolved" };
    case "TERMINAL_ATTACH": exactKeys(body, ["run_id", "session_id", "expected_run_revision", "expected_session_revision", "after_sequence"]); return { run_id: dynamicID(body.run_id), session_id: dynamicID(body.session_id), expected_run_revision: decimal(body.expected_run_revision, wire, true), expected_session_revision: decimal(body.expected_session_revision, wire, true), after_sequence: decimal(body.after_sequence, wire) };
    case "TERMINAL_ATTACHED": exactKeys(body, ["session_id", "floor", "head", "acknowledged_sequence", "max_unacked_bytes"]); { const floor = decimal(body.floor, wire); const head = decimal(body.head, wire); const acknowledged_sequence = decimal(body.acknowledged_sequence, wire); const max_unacked_bytes = decimal(body.max_unacked_bytes, wire); if (floor > head || acknowledged_sequence > head || max_unacked_bytes !== BigInt(MAX_TERMINAL_UNACKED_BYTES)) malformed(); return { session_id: dynamicID(body.session_id), floor, head, acknowledged_sequence, max_unacked_bytes }; }
    case "TERMINAL_ACK": exactKeys(body, ["session_id", "next_sequence"]); return { session_id: dynamicID(body.session_id), next_sequence: decimal(body.next_sequence, wire, true) };
    case "TERMINAL_LEASE_ACQUIRE": exactKeys(body, ["run_id", "session_id", "expected_run_revision", "expected_session_revision"]); return { run_id: dynamicID(body.run_id), session_id: dynamicID(body.session_id), expected_run_revision: decimal(body.expected_run_revision, wire, true), expected_session_revision: decimal(body.expected_session_revision, wire, true) };
    case "TERMINAL_LEASE_RENEW": case "TERMINAL_LEASE_RELEASE": exactKeys(body, ["run_id", "session_id", "generation", "expected_run_revision", "expected_session_revision"]); return { run_id: dynamicID(body.run_id), session_id: dynamicID(body.session_id), generation: decimal(body.generation, wire, true), expected_run_revision: decimal(body.expected_run_revision, wire, true), expected_session_revision: decimal(body.expected_session_revision, wire, true) };
    case "TERMINAL_LEASE_RESULT": exactKeys(body, ["operation", "run_id", "session_id", "generation", "last_input_sequence", "run_revision", "session_revision"], ["expires_at_ms"]); if (body.operation !== "acquired" && body.operation !== "renewed" && body.operation !== "released") malformed(); { const operation = body.operation; const run_id = dynamicID(body.run_id); const session_id = dynamicID(body.session_id); const generation = decimal(body.generation, wire, true); const expires_at_ms = Object.prototype.hasOwnProperty.call(body, "expires_at_ms") ? decimal(body.expires_at_ms, wire, true) : undefined; const last_input_sequence = decimal(body.last_input_sequence, wire); const run_revision = decimal(body.run_revision, wire, true); const session_revision = decimal(body.session_revision, wire, true); if (operation === "released") { if (expires_at_ms !== undefined) malformed(); return { operation, run_id, session_id, generation, last_input_sequence, run_revision, session_revision }; } if (expires_at_ms === undefined) malformed(); return { operation, run_id, session_id, generation, expires_at_ms, last_input_sequence, run_revision, session_revision }; }
    case "TERMINAL_RESIZE": exactKeys(body, ["run_id", "session_id", "generation", "expected_run_revision", "expected_session_revision", "rows", "cols"]); return { run_id: dynamicID(body.run_id), session_id: dynamicID(body.session_id), generation: decimal(body.generation, wire, true), expected_run_revision: decimal(body.expected_run_revision, wire, true), expected_session_revision: decimal(body.expected_session_revision, wire, true), rows: integer(body.rows, 1, MAX_TERMINAL_ROWS), cols: integer(body.cols, 1, MAX_TERMINAL_COLS) };
    case "TERMINAL_RESIZED": exactKeys(body, ["session_id", "generation", "rows", "cols"]); return { session_id: dynamicID(body.session_id), generation: decimal(body.generation, wire, true), rows: integer(body.rows, 1, MAX_TERMINAL_ROWS), cols: integer(body.cols, 1, MAX_TERMINAL_COLS) };
    case "TERMINAL_DETACH": exactKeys(body, ["session_id"]); return { session_id: dynamicID(body.session_id) };
    case "TERMINAL_DETACHED": exactKeys(body, ["session_id"]); return { session_id: dynamicID(body.session_id) };
    case "TERMINAL_INPUT_RESULT": exactKeys(body, ["session_id", "generation", "sequence", "status", "accepted_bytes"]); if (body.status !== "accepted" && body.status !== "rejected" && body.status !== "partial" && body.status !== "uncertain") malformed(); { const accepted_bytes = decimal(body.accepted_bytes, wire); if (!validTerminalInputResult(body.status, accepted_bytes)) malformed(); return { session_id: dynamicID(body.session_id), generation: decimal(body.generation, wire, true), sequence: decimal(body.sequence, wire, true), status: body.status, accepted_bytes }; }
    case "TERMINAL_EOF": exactKeys(body, ["session_id"]); return { session_id: dynamicID(body.session_id) };
    case "TERMINAL_EXIT": exactKeys(body, ["session_id", "exit_code", "exit_signal", "aborted"]); if (typeof body.aborted !== "boolean") malformed(); return { session_id: dynamicID(body.session_id), exit_code: integer(body.exit_code, 0, Number.MAX_SAFE_INTEGER), exit_signal: integer(body.exit_signal, 0, Number.MAX_SAFE_INTEGER), aborted: body.aborted };
    case "TERMINAL_RESET": exactKeys(body, ["session_id", "floor", "head"]); { const floor = decimal(body.floor, wire); const head = decimal(body.head, wire); if (floor > head) malformed(); return { session_id: dynamicID(body.session_id), floor, head }; }
    case "ERROR": exactKeys(body, ["code", "retryable"]); if (typeof body.code !== "string" || !(ERROR_CODES as readonly string[]).includes(body.code) || typeof body.retryable !== "boolean") malformed(); return { code: body.code as ErrorCode, retryable: body.retryable };
  }
}

function stateSnapshot(body: Record<string, unknown>, wire: boolean): StateSnapshotBody {
  exactKeys(body, ["head", "kind", "items", "next_cursor"]);
  const head = decimal(body.head, wire); const kind = stateKind(body.kind); const next_cursor = cursor(body.next_cursor);
  if (!Array.isArray(body.items) || body.items.length > MAX_STATE_PAGE_ITEMS) malformed();
  switch (kind) {
    case "factory": if (body.items.length !== MAX_FACTORY_PAGE_ITEMS || next_cursor === null) malformed(); return { head, kind, items: [factoryItem(body.items[0], wire)], next_cursor };
    case "project": if (next_cursor === null) malformed(); return { head, kind, items: body.items.map((item) => projectItem(item, wire)), next_cursor };
    case "agent": if (next_cursor === null) malformed(); return { head, kind, items: body.items.map((item) => agentItem(item, wire)), next_cursor };
    case "task": if (next_cursor === null) malformed(); return { head, kind, items: body.items.map((item) => taskItem(item, wire)), next_cursor };
    // A full final page is valid: exactly MAX_STATE_PAGE_ITEMS can be the
    // terminal page when the traversal has no more rows. Short pages remain
    // terminal, while any non-final page must be full.
    case "human_request": if (body.items.length < MAX_STATE_PAGE_ITEMS && next_cursor !== null) malformed(); return { head, kind, items: body.items.map((item) => humanRequestItem(item, wire)), next_cursor };
  }
}
function stateEvent(body: Record<string, unknown>, wire: boolean): StateEventBody {
  if (body.event === "entity_changed") {
    exactKeys(body, ["event", "sequence", "head", "entity_kind", "entity_id", "revision", "deleted"]);
    const sequence = decimal(body.sequence, wire, true); const head = decimal(body.head, wire); const entity_kind = stateKind(body.entity_kind);
    if (head < sequence || typeof body.deleted !== "boolean") malformed();
    return { event: "entity_changed", sequence, head, entity_kind, entity_id: entityID(entity_kind, body.entity_id), revision: decimal(body.revision, wire, true), deleted: body.deleted };
  }
  if (body.event === "hidden_advance") {
    exactKeys(body, ["event", "sequence", "head"]); const sequence = decimal(body.sequence, wire, true); const head = decimal(body.head, wire);
    if (head < sequence) malformed(); return { event: "hidden_advance", sequence, head };
  }
  malformed();
}
function stateEntity(body: Record<string, unknown>, wire: boolean): StateEntityBody {
  exactKeys(body, ["head", "kind", "id", "revision", "deleted", "item"]);
  const head = decimal(body.head, wire); const kind = stateKind(body.kind); const id = entityID(kind, body.id); const revision = decimal(body.revision, wire, true);
  if (typeof body.deleted !== "boolean") malformed();
  if (body.deleted) { if (body.item !== null) malformed(); return { head, kind, id, revision, deleted: true, item: null }; }
  if (!isObject(body.item)) malformed();
  switch (kind) {
    case "factory": { const item = factoryItem(body.item, wire); if (item.revision !== revision) malformed(); return { head, kind, id: "factory", revision, deleted: false, item }; }
    case "project": { const item = projectItem(body.item, wire); if (item.id !== id || item.revision !== revision) malformed(); return { head, kind, id, revision, deleted: false, item }; }
    case "agent": { const item = agentItem(body.item, wire); if (item.id !== id || item.revision !== revision) malformed(); return { head, kind, id, revision, deleted: false, item }; }
    case "task": { const item = taskItem(body.item, wire); if (item.id !== id || item.revision !== revision) malformed(); return { head, kind, id, revision, deleted: false, item }; }
    case "human_request": { const item = humanRequestItem(body.item, wire); if (item.id !== id || item.revision !== revision) malformed(); return { head, kind, id, revision, deleted: false, item }; }
  }
}

function factoryItem(value: unknown, wire: boolean): FactoryItem {
  if (!isObject(value)) malformed(); exactKeys(value, ["dispatch_enabled", "capacity", "active_runs", "revision"]);
  if (typeof value.dispatch_enabled !== "boolean") malformed(); const capacity = integer(value.capacity, 1, MAX_FACTORY_CAPACITY); const active_runs = integer(value.active_runs, 0, MAX_FACTORY_CAPACITY);
  if (active_runs > capacity) malformed(); return { dispatch_enabled: value.dispatch_enabled, capacity, active_runs, revision: decimal(value.revision, wire, true) };
}
function projectItem(value: unknown, wire: boolean): ProjectItem { if (!isObject(value)) malformed(); exactKeys(value, ["id", "name", "revision"]); return { id: dynamicID(value.id), name: boundedText(value.name, 1, MAX_PROJECT_NAME_BYTES), revision: decimal(value.revision, wire, true) }; }
function agentItem(value: unknown, wire: boolean): AgentItem {
  if (!isObject(value)) malformed(); exactKeys(value, ["id", "project_id", "name", "role", "paused", "revision"]);
  if (value.role !== "orchestrator" && value.role !== "worker" || typeof value.paused !== "boolean") malformed();
  return { id: dynamicID(value.id), project_id: dynamicID(value.project_id), name: boundedText(value.name, 1, MAX_AGENT_NAME_BYTES), role: value.role, paused: value.paused, revision: decimal(value.revision, wire, true) };
}
function taskItem(value: unknown, wire: boolean): TaskItem {
  if (!isObject(value)) malformed(); exactKeys(value, ["id", "project_id", "assigned_agent_id", "title", "status", "priority", "revision"]);
  if (typeof value.status !== "string" || !["queued", "running", "blocked", "succeeded", "failed", "cancelled"].includes(value.status)) malformed();
  return { id: dynamicID(value.id), project_id: dynamicID(value.project_id), assigned_agent_id: dynamicID(value.assigned_agent_id), title: boundedText(value.title, 1, MAX_TASK_TITLE_BYTES), status: value.status as TaskItem["status"], priority: integer(value.priority, -MAX_TASK_PRIORITY, MAX_TASK_PRIORITY), revision: decimal(value.revision, wire, true) };
}
function humanRequestItem(value: unknown, wire: boolean): HumanRequestItem {
  if (!isObject(value)) malformed(); exactKeys(value, ["id", "project_id", "agent_id", "task_id", "run_id", "created_at", "updated_at", "revision", "kind", "status", "reply_max_bytes", "can_reply", "can_open_terminal"]);
  const created_at = decimal(value.created_at, wire); const updated_at = decimal(value.updated_at, wire);
  if (updated_at < created_at || value.kind !== "question" || typeof value.status !== "string" || !["open", "delivering", "delivery_unknown"].includes(value.status) || typeof value.can_reply !== "boolean" || typeof value.can_open_terminal !== "boolean") malformed();
  return { id: dynamicID(value.id), project_id: dynamicID(value.project_id), agent_id: dynamicID(value.agent_id), task_id: dynamicID(value.task_id), run_id: dynamicID(value.run_id), created_at, updated_at, revision: decimal(value.revision, wire, true), kind: "question", status: value.status as HumanRequestItem["status"], reply_max_bytes: integer(value.reply_max_bytes, 1, MAX_HUMAN_REPLY_BYTES), can_reply: value.can_reply, can_open_terminal: value.can_open_terminal };
}

function decimal(value: unknown, wire: boolean, positive = false): bigint {
  let result: bigint;
  if (wire) { if (typeof value !== "string" || !/^(0|[1-9][0-9]*)$/.test(value)) malformed(); try { result = BigInt(value); } catch { malformed(); } }
  else { if (typeof value !== "bigint") malformed(); result = value; }
  if (result < 0n || result > MAX_SQLITE_INTEGER || positive && result === 0n) malformed(); return result;
}
function stateKind(value: unknown): StateKind { if (value !== "factory" && value !== "project" && value !== "agent" && value !== "task" && value !== "human_request") malformed(); return value; }
function entityID(kind: StateKind, value: unknown): string { if (kind === "factory") { if (value !== "factory") malformed(); return "factory"; } return dynamicID(value); }
function dynamicID(value: unknown): string { if (typeof value !== "string" || !/^[0-9a-f]{32}$/.test(value) || /^0{32}$/.test(value)) malformed(); return value; }
function cursor(value: unknown): string | null { if (value === null) return null; if (typeof value !== "string" || value.length === 0 || new TextEncoder().encode(value).length > MAX_CURSOR_BYTES || !/^[A-Za-z0-9_-]+$/.test(value)) malformed(); return value; }
function boundedText(value: unknown, minimum: number, maximum: number): string { if (typeof value !== "string" || hasLoneSurrogate(value)) malformed(); const length = new TextEncoder().encode(value).length; if (length < minimum || length > maximum) malformed(); return value; }
function hasLoneSurrogate(value: string): boolean {
  for (let index = 0; index < value.length; index++) {
    const code = value.charCodeAt(index);
    if (code >= 0xd800 && code <= 0xdbff) {
      if (index + 1 >= value.length) return true;
      const next = value.charCodeAt(index + 1);
      if (next < 0xdc00 || next > 0xdfff) return true;
      index++;
    } else if (code >= 0xdc00 && code <= 0xdfff) return true;
  }
  return false;
}
function integer(value: unknown, minimum: number, maximum: number): number { if (typeof value !== "number" || !Number.isSafeInteger(value) || value < minimum || value > maximum) malformed(); return value; }
function validTerminalInputResult(status: string, acceptedBytes: bigint): boolean { return status === "accepted" || status === "partial" ? acceptedBytes >= 1n && acceptedBytes <= BigInt(MAX_TERMINAL_PAYLOAD) : status === "rejected" || status === "uncertain" ? acceptedBytes === 0n : false; }
function fixedHex(value: unknown, bytes: number, requireUncompressed = false): string { if (typeof value !== "string" || value.length !== bytes * 2 || !/^[0-9a-f]+$/.test(value)) malformed(); if (requireUncompressed && !value.startsWith("04")) malformed(); return value; }
function capabilities(value: unknown): number { const result = integer(value, 0, 15); if ((result & CAPABILITIES.observe) === 0) malformed(); return result; }
function validID(value: string): boolean { return value.length > 0 && value.length <= 64 && [...value].every((character) => character.charCodeAt(0) >= 0x21 && character.charCodeAt(0) <= 0x7e); }
function isControlType(value: unknown): value is ControlType { return typeof value === "string" && (CONTROL_TYPES as readonly string[]).includes(value); }
function isObject(value: unknown): value is Record<string, unknown> { return typeof value === "object" && value !== null && !Array.isArray(value); }
function exactKeys(value: Record<string, unknown>, required: string[], optional: string[] = []): void { const allowed = new Set([...required, ...optional]); if (required.some((key) => !Object.prototype.hasOwnProperty.call(value, key)) || Object.keys(value).some((key) => !allowed.has(key))) malformed(); }

// JSON.parse permits duplicate names and unsafe integers. This structural scan
// applies the same duplicate/depth/member/array/number bounds as Go.
function rejectDuplicateKeys(text: string): void {
  let index = 0;
  const stringEnd = (): number => { if (text[index] !== "\"") malformed(); const start = index++; while (index < text.length) { const character = text[index++]; if (character === "\\") index++; else if (character === "\"") { JSON.parse(text.slice(start, index)); return index; } } malformed(); };
  const whitespace = (): void => { while (/\s/.test(text[index] ?? "")) index++; };
  const value = (depth: number): void => {
    if (depth > MAX_JSON_DEPTH) malformed(); whitespace();
    if (text[index] === "{") { index++; whitespace(); const keys = new Set<string>(); if (text[index] === "}") { index++; return; } while (true) { whitespace(); const start = index; stringEnd(); const key = JSON.parse(text.slice(start, index)) as string; if (keys.has(key) || keys.size === MAX_OBJECT_MEMBERS) malformed(); keys.add(key); whitespace(); if (text[index++] !== ":") malformed(); value(depth + 1); whitespace(); if (text[index] === "}") { index++; return; } if (text[index++] !== ",") malformed(); } }
    if (text[index] === "[") { index++; whitespace(); if (text[index] === "]") { index++; return; } let count = 0; while (true) { if (count++ === MAX_ARRAY_ITEMS) malformed(); value(depth + 1); whitespace(); if (text[index] === "]") { index++; return; } if (text[index++] !== ",") malformed(); } }
    if (text[index] === "\"") { stringEnd(); return; }
    const start = index; while (index < text.length && !/[\s,\]}]/.test(text[index] ?? "")) index++; if (start === index) malformed(); const token = text.slice(start, index);
    if (token[0] === "-" || token[0] !== undefined && token[0] >= "0" && token[0] <= "9") validateJSONNumber(token);
  };
  value(0); whitespace(); if (index !== text.length) malformed();
}
function validateJSONNumber(value: string): void { if (!/^-?(0|[1-9][0-9]*)$/.test(value) || value === "-0") malformed(); let parsed: bigint; try { parsed = BigInt(value); } catch { malformed(); } if (parsed < -9_007_199_254_740_991n || parsed > 9_007_199_254_740_991n) malformed(); }
