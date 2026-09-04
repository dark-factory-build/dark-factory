export type ProtocolErrorCode =
  | "malformed"
  | "oversized"
  | "wrong_direction"
  | "unauthorized";

/** Errors exposed to a browser are finite and contain no daemon diagnostics. */
export class ProtocolError extends Error {
  readonly code: ProtocolErrorCode;

  constructor(code: ProtocolErrorCode) {
    super(code);
    this.name = "ProtocolError";
    this.code = code;
  }
}

export function malformed(): never {
  throw new ProtocolError("malformed");
}

/** Normalize errors at public boundaries; parser/runtime details never escape. */
export function normalizeBoundary<T>(operation: () => T): T {
  try {
    return operation();
  } catch (error) {
    if (error instanceof ProtocolError) throw error;
    throw new ProtocolError("malformed");
  }
}
