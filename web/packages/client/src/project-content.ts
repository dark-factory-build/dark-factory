import { ProtocolError } from "./errors.js";

export const PROJECT_CONTENT_OPERATIONS = ["list", "read", "body", "create", "revise", "deprecate", "evidence", "evidence_list", "attach", "attachments", "outcome_list", "outcome_read", "outcome_write"] as const;
export type ProjectContentOperation = typeof PROJECT_CONTENT_OPERATIONS[number];
export type ProjectContentInput = Readonly<Record<string, unknown>>;
export type ProjectContentOutput = Readonly<Record<string, unknown>>;
export type ProjectContentRequest = Readonly<{ operation: ProjectContentOperation; input: ProjectContentInput }>;
export type ProjectContentResult = Readonly<{ operation: ProjectContentOperation; output: ProjectContentOutput }>;

export function projectContentOperation(value: unknown): ProjectContentOperation {
  if (typeof value !== "string" || !(PROJECT_CONTENT_OPERATIONS as readonly string[]).includes(value)) throw new ProtocolError("malformed");
  return value as ProjectContentOperation;
}

/** Content uses bounded JSON numbers; refuse precision loss at the boundary. */
export function projectContentObject(value: unknown): Record<string, unknown> {
  if (value === null || typeof value !== "object" || Array.isArray(value)) throw new ProtocolError("malformed");
  const visit = (item: unknown, depth: number): void => {
    if (depth > 16 || typeof item === "number" && (!Number.isSafeInteger(item) || item < 0)) throw new ProtocolError("malformed");
    if (Array.isArray(item)) { if (item.length > 64) throw new ProtocolError("malformed"); item.forEach((child) => visit(child, depth + 1)); }
    else if (item !== null && typeof item === "object") Object.values(item).forEach((child) => visit(child, depth + 1));
    else if (typeof item !== "string" && typeof item !== "boolean" && typeof item !== "number" && item !== null) throw new ProtocolError("malformed");
  };
  visit(value, 0);
  return value as Record<string, unknown>;
}
