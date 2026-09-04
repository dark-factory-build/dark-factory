// The host-socket record envelope.
//
//   record  := type(u8) connection(u32 BE) length(u32 BE) payload(length bytes)
//   message := record+
//
// Many small controller frames share one host message, so the header is fixed
// width and the parser is total: every message either yields a complete record
// list or names the exact bound it broke.

export const RECORD_HEADER_BYTES = 9;

export const RECORD_OPEN = 0x01;
export const RECORD_TEXT = 0x02;
export const RECORD_BINARY = 0x03;
export const RECORD_CLOSE = 0x04;
export const RECORD_REVOKE = 0x05;

/** A host message may not exceed 4 MiB. */
export const HOST_MESSAGE_LIMIT = 4 * 1024 * 1024;
/** A controller message may not exceed 64 KiB. */
export const CONTROLLER_MESSAGE_LIMIT = 64 * 1024;
/** One record payload may not exceed 1 MiB + 64 bytes. */
export const RECORD_PAYLOAD_LIMIT = 1024 * 1024 + 64;

export interface RelayRecord {
	type: number;
	connection: number;
	payload: Uint8Array;
}

export type ParseFailure = 'truncated' | 'unknown-type' | 'oversize-record';

export type ParseResult = { ok: true; records: RelayRecord[] } | { ok: false; failure: ParseFailure };

/** Types a host is allowed to send. OPEN is relay→host only. */
const HOST_SENDABLE = new Set([RECORD_TEXT, RECORD_BINARY, RECORD_CLOSE, RECORD_REVOKE]);

export function parseHostMessage(bytes: Uint8Array): ParseResult {
	const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
	const records: RelayRecord[] = [];
	let offset = 0;
	while (offset < bytes.length) {
		if (bytes.length - offset < RECORD_HEADER_BYTES) return { ok: false, failure: 'truncated' };
		const type = view.getUint8(offset);
		const connection = view.getUint32(offset + 1, false);
		const length = view.getUint32(offset + 5, false);
		if (length > RECORD_PAYLOAD_LIMIT) return { ok: false, failure: 'oversize-record' };
		if (!HOST_SENDABLE.has(type)) return { ok: false, failure: 'unknown-type' };
		const start = offset + RECORD_HEADER_BYTES;
		if (bytes.length - start < length) return { ok: false, failure: 'truncated' };
		records.push({ type, connection, payload: bytes.subarray(start, start + length) });
		offset = start + length;
	}
	return { ok: true, records };
}

export function encodeRecords(records: readonly RelayRecord[]): ArrayBuffer {
	let total = 0;
	for (const record of records) total += RECORD_HEADER_BYTES + record.payload.length;
	const buffer = new ArrayBuffer(total);
	const bytes = new Uint8Array(buffer);
	const view = new DataView(buffer);
	let offset = 0;
	for (const record of records) {
		view.setUint8(offset, record.type);
		view.setUint32(offset + 1, record.connection >>> 0, false);
		view.setUint32(offset + 5, record.payload.length, false);
		bytes.set(record.payload, offset + RECORD_HEADER_BYTES);
		offset += RECORD_HEADER_BYTES + record.payload.length;
	}
	return buffer;
}

export function textRecord(type: number, connection: number, text: string): RelayRecord {
	return { type, connection, payload: new TextEncoder().encode(text) };
}
