// Byte encodings the relay credentials are written in. Every decoder is strict:
// it rejects padding, non-alphabet characters, and non-zero trailing bits, so a
// credential has exactly one legal spelling and cannot be smuggled in variants.

const BASE32_LOWER = 'abcdefghijklmnopqrstuvwxyz234567';
const BASE64URL = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_';

/** RFC 4648 base32, lowercase, no padding. */
export function encodeBase32Lower(bytes: Uint8Array): string {
	let text = '';
	let buffer = 0;
	let bits = 0;
	for (const byte of bytes) {
		buffer = (buffer << 8) | byte;
		bits += 8;
		while (bits >= 5) {
			bits -= 5;
			text += BASE32_LOWER[(buffer >> bits) & 31];
		}
	}
	if (bits > 0) {
		text += BASE32_LOWER[(buffer << (5 - bits)) & 31];
	}
	return text;
}

/** RFC 4648 base64url, no padding. Returns null when the text is not canonical. */
export function decodeBase64Url(text: string): Uint8Array | null {
	if (text.length % 4 === 1) return null;
	const bytes = new Uint8Array((text.length * 3) >> 2);
	let written = 0;
	let buffer = 0;
	let bits = 0;
	for (let index = 0; index < text.length; index += 1) {
		const value = BASE64URL.indexOf(text[index] as string);
		if (value < 0) return null;
		buffer = (buffer << 6) | value;
		bits += 6;
		if (bits >= 8) {
			bits -= 8;
			bytes[written] = (buffer >> bits) & 0xff;
			written += 1;
		}
	}
	// Canonical base64url leaves the unused low bits of the final character zero.
	if (bits > 0 && (buffer & ((1 << bits) - 1)) !== 0) return null;
	return bytes.subarray(0, written);
}

/** base64url of an exact byte count, or null. */
export function decodeBase64UrlExact(text: string, length: number): Uint8Array | null {
	const bytes = decodeBase64Url(text);
	return bytes !== null && bytes.length === length ? bytes : null;
}

export function utf8(text: string): Uint8Array {
	return new TextEncoder().encode(text);
}

export function concatBytes(head: Uint8Array, tail: Uint8Array): Uint8Array {
	const joined = new Uint8Array(head.length + tail.length);
	joined.set(head, 0);
	joined.set(tail, head.length);
	return joined;
}
