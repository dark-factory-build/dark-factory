// Server-side frame encoders; only the fake daemons in tests send these.
import { encodeServerControl } from "../dist/src/control.js";

export function encodeHello(body) { return encodeServerControl({ type: "HELLO", body }); }
export function encodePairResult(id, body) { return encodeServerControl({ type: "PAIR_RESULT", id, body }); }
export function encodeAuthResult(id, body) { return encodeServerControl({ type: "AUTH_RESULT", id, body }); }
export function encodeStateSnapshot(id, body) { return encodeServerControl({ type: "STATE_SNAPSHOT", id, body }); }
export function encodeStateChanged(id, body) { return encodeServerControl({ type: "STATE_CHANGED", id, body }); }
export function encodeHumanRequestDetail(id, body) { return encodeServerControl({ type: "HUMAN_REQUEST_DETAIL", id, body }); }
export function encodeHumanRequestReplyResult(id, body) { return encodeServerControl({ type: "HUMAN_REQUEST_REPLY_RESULT", id, body }); }
export function encodeHumanRequestCancelRunResult(id, body) { return encodeServerControl({ type: "HUMAN_REQUEST_CANCEL_RUN_RESULT", id, body }); }
export function encodeTaskEnqueueResult(id, body) { return encodeServerControl({ type: "TASK_ENQUEUE_RESULT", id, body }); }
export function encodeProjectCreateResult(id, body) { return encodeServerControl({ type: "PROJECT_CREATE_RESULT", id, body }); }
export function encodeRepositories(id, body) { return encodeServerControl({ type: "REPOSITORIES", id, body }); }
export function encodeRepositoryMutateResult(id, body) { return encodeServerControl({ type: "REPOSITORY_MUTATE_RESULT", id, body }); }
export function encodeAgentControlResult(id, body) { return encodeServerControl({ type: "AGENT_CONTROL_RESULT", id, body }); }
export function encodeTaskHistory(id, body) { return encodeServerControl({ type: "TASK_HISTORY", id, body }); }
export function encodeTaskDetail(id, body) { return encodeServerControl({ type: "TASK_DETAIL", id, body }); }
export function encodeTerminalTarget(id, body) { return encodeServerControl({ type: "TERMINAL_TARGET", id, body }); }
export function encodeTerminalAttached(id, body) { return encodeServerControl({ type: "TERMINAL_ATTACHED", id, body }); }
export function encodeTerminalLeaseResult(id, body) { return encodeServerControl({ type: "TERMINAL_LEASE_RESULT", id, body }); }
export function encodeTerminalResized(id, body) { return encodeServerControl({ type: "TERMINAL_RESIZED", id, body }); }
export function encodeTerminalDetached(id, body) { return encodeServerControl({ type: "TERMINAL_DETACHED", id, body }); }
export function encodeTerminalInputResult(id, body) { return encodeServerControl({ type: "TERMINAL_INPUT_RESULT", id, body }); }
export function encodeTerminalExit(id, body) { return encodeServerControl({ type: "TERMINAL_EXIT", id, body }); }
export function encodeTerminalReset(id, body) { return encodeServerControl({ type: "TERMINAL_RESET", id, body }); }
export function encodeRemoteInviteResult(id, body) { return encodeServerControl({ type: "REMOTE_INVITE_RESULT", id, body }); }
export function encodeServerError(body, id) { return encodeServerControl({ type: "ERROR", ...(id === undefined ? {} : { id }), body }); }
