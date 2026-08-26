export type ProtocolErrorCode =
  | "malformed"
  | "oversized"
  | "unsupported_version"
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
