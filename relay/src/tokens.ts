// Credential parsing and signature verification.
//
// Every credential is base64url(payload) + "." + base64url(signature) with no
// padding. The signed bytes are a domain prefix followed by the *exact*
// base64url payload text, so verification never re-serialises the JSON and a
// signature can never be replayed across token kinds.

import { concatBytes, decodeBase64Url, decodeBase64UrlExact, encodeBase32Lower, utf8 } from './encoding.js';

export const HOST_DOMAIN = 'dark-factory-relay/host\n';
export const TICKET_DOMAIN = 'dark-factory-relay/ticket\n';
export const PROOF_DOMAIN = 'dark-factory-relay/proof\n';

/** Clock skew tolerated on an `issued` claim, in seconds, either way. */
export const SKEW_SECONDS = 60;

export const NODE_ID_PATTERN = /^[a-z2-7]{32}$/;

export interface HostToken {
	node: string;
	key: string;
	generation: number;
	sequence: number;
	issued: number;
}

export type TicketPurpose = 'pair' | 'control';

export interface Ticket {
	node: string;
	controller: string;
	purpose: TicketPurpose;
	ticket: string;
	expires: number;
	device: string | null;
}

export interface Proof {
	ticket: string;
	issued: number;
	nonce: string;
}

interface SplitToken {
	payloadText: string;
	payload: Record<string, unknown>;
	signature: Uint8Array;
}

function isSafeInteger(value: unknown): value is number {
	return typeof value === 'number' && Number.isSafeInteger(value);
}

function nonEmptyString(value: unknown): value is string {
	return typeof value === 'string' && value.length > 0;
}

function split(token: string): SplitToken | null {
	const parts = token.split('.');
	if (parts.length !== 2) return null;
	const [payloadText, signatureText] = parts as [string, string];
	if (payloadText.length === 0 || signatureText.length === 0) return null;
	const payloadBytes = decodeBase64Url(payloadText);
	const signature = decodeBase64Url(signatureText);
	if (payloadBytes === null || signature === null) return null;
	let payload: unknown;
	try {
		payload = JSON.parse(new TextDecoder('utf-8', { fatal: true, ignoreBOM: false }).decode(payloadBytes));
	} catch {
		return null;
	}
	if (typeof payload !== 'object' || payload === null || Array.isArray(payload)) return null;
	return { payloadText, payload: payload as Record<string, unknown>, signature };
}

async function verifyEd25519(publicKey: Uint8Array, signature: Uint8Array, signed: Uint8Array): Promise<boolean> {
	if (signature.length !== 64) return false;
	try {
		const key = await crypto.subtle.importKey('raw', publicKey, { name: 'Ed25519' }, false, ['verify']);
		return await crypto.subtle.verify({ name: 'Ed25519' }, key, signature, signed);
	} catch {
		return false;
	}
}

async function verifyEcdsaP256(point: Uint8Array, signature: Uint8Array, signed: Uint8Array): Promise<boolean> {
	// Raw r‖s only: DER is not accepted, so there is one spelling per signature.
	if (signature.length !== 64) return false;
	try {
		const key = await crypto.subtle.importKey('raw', point, { name: 'ECDSA', namedCurve: 'P-256' }, false, [
			'verify',
		]);
		return await crypto.subtle.verify({ name: 'ECDSA', hash: 'SHA-256' }, key, signature, signed);
	} catch {
		return false;
	}
}

/** Node id: lowercase base32 of the first 20 bytes of SHA-256(node public key). */
export async function nodeIdForKey(publicKey: Uint8Array): Promise<string> {
	const digest = new Uint8Array(await crypto.subtle.digest('SHA-256', publicKey));
	return encodeBase32Lower(digest.subarray(0, 20));
}

export function withinSkew(issued: number, nowSeconds: number): boolean {
	return Math.abs(issued - nowSeconds) <= SKEW_SECONDS;
}

/**
 * Verifies a host token against the node id it claims. The node id is
 * self-certifying: the presented key must hash to the object's own name, so the
 * relay needs no directory of factories.
 */
export async function verifyHostToken(token: string, node: string, nowSeconds: number): Promise<HostToken | null> {
	const parts = split(token);
	if (parts === null) return null;
	const { payload } = parts;
	if (!nonEmptyString(payload.node) || !nonEmptyString(payload.key)) return null;
	if (!isSafeInteger(payload.generation) || !isSafeInteger(payload.sequence) || !isSafeInteger(payload.issued)) {
		return null;
	}
	if (payload.generation < 0 || payload.sequence < 0) return null;
	if (payload.node !== node) return null;
	if (!withinSkew(payload.issued, nowSeconds)) return null;
	const publicKey = decodeBase64UrlExact(payload.key, 32);
	if (publicKey === null) return null;
	if ((await nodeIdForKey(publicKey)) !== node) return null;
	const signed = concatBytes(utf8(HOST_DOMAIN), utf8(parts.payloadText));
	if (!(await verifyEd25519(publicKey, parts.signature, signed))) return null;
	return {
		node: payload.node,
		key: payload.key,
		generation: payload.generation,
		sequence: payload.sequence,
		issued: payload.issued,
	};
}

/** Verifies a ticket against the node public key already recorded by a host. */
export async function verifyTicket(token: string, node: string, nodeKey: Uint8Array): Promise<Ticket | null> {
	const parts = split(token);
	if (parts === null) return null;
	const { payload } = parts;
	if (!nonEmptyString(payload.node) || !nonEmptyString(payload.controller) || !nonEmptyString(payload.ticket)) {
		return null;
	}
	if (payload.purpose !== 'pair' && payload.purpose !== 'control') return null;
	if (!isSafeInteger(payload.expires)) return null;
	if (payload.node !== node) return null;
	if (decodeBase64UrlExact(payload.controller, 16) === null) return null;
	if (decodeBase64UrlExact(payload.ticket, 16) === null) return null;
	let device: string | null = null;
	if (payload.purpose === 'control') {
		if (!nonEmptyString(payload.device)) return null;
		const point = decodeBase64UrlExact(payload.device, 65);
		if (point === null || point[0] !== 0x04) return null;
		device = payload.device;
	}
	const signed = concatBytes(utf8(TICKET_DOMAIN), utf8(parts.payloadText));
	if (!(await verifyEd25519(nodeKey, parts.signature, signed))) return null;
	return {
		node: payload.node,
		controller: payload.controller,
		purpose: payload.purpose,
		ticket: payload.ticket,
		expires: payload.expires,
		device,
	};
}

/** Verifies a device proof against the device key named by a control ticket. */
export async function verifyProof(
	token: string,
	devicePoint: Uint8Array,
	ticketId: string,
	nowSeconds: number,
): Promise<Proof | null> {
	const parts = split(token);
	if (parts === null) return null;
	const { payload } = parts;
	if (!nonEmptyString(payload.ticket) || !nonEmptyString(payload.nonce)) return null;
	if (!isSafeInteger(payload.issued)) return null;
	if (payload.ticket !== ticketId) return null;
	if (!withinSkew(payload.issued, nowSeconds)) return null;
	if (decodeBase64UrlExact(payload.nonce, 16) === null) return null;
	const signed = concatBytes(utf8(PROOF_DOMAIN), utf8(parts.payloadText));
	if (!(await verifyEcdsaP256(devicePoint, parts.signature, signed))) return null;
	return { ticket: payload.ticket, issued: payload.issued, nonce: payload.nonce };
}
