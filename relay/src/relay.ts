// One Durable Object per factory node. It holds one current host socket and
// many controller sockets and forwards opaque frames between them. It never
// interprets a Dark Factory message, never queues while a peer is away, and
// never logs a payload, token, or key.
//
// It persists exactly one record, `host`. Revocation and ticket single-use are
// deliberately not remembered here: the daemon's own challenge is the durable
// authority, and a second copy of it would only be a second thing to get wrong.

import { decodeBase64UrlExact } from './encoding.js';
import {
	CONTROLLER_MESSAGE_LIMIT,
	HOST_MESSAGE_LIMIT,
	RECORD_BINARY,
	RECORD_CLOSE,
	RECORD_OPEN,
	RECORD_REVOKE,
	RECORD_TEXT,
	encodeRecords,
	parseHostMessage,
	textRecord,
	type RelayRecord,
} from './envelope.js';
import { NODE_ID_PATTERN, verifyHostToken, verifyProof, verifyTicket, type TicketPurpose } from './tokens.js';

export const SUBPROTOCOL = 'dark-factory-relay';

/** The previous host socket, displaced by a strictly newer boot or dial. */
const CLOSE_HOST_REPLACED = 4000;
/** No host is attached, so a controller has nothing to talk to. */
const CLOSE_HOST_GONE = 4001;
/** The host revoked this controller id. */
const CLOSE_REVOKED = 4002;
/** A size or rate bound was broken. */
const CLOSE_LIMIT = 4003;
/** The envelope was malformed. */
const CLOSE_PROTOCOL = 4004;
/** A host CLOSE record named a code outside the 3000–4999 application range. */
const CLOSE_SUBSTITUTED = 4005;

const CONTROLLER_SOCKETS_PER_NODE = 32;
const CONTROLLER_SOCKETS_PER_CONTROLLER = 4;

/** Token bucket: 120 messages of burst, refilled at 60 per second. */
const BURST_MESSAGES = 120;
const SUSTAINED_MESSAGES_PER_SECOND = 60;

const APPLICATION_CLOSE_MIN = 3000;
const APPLICATION_CLOSE_MAX = 4999;

export interface Env {
	PWA_ORIGIN: string;
	FACTORY_RELAY: DurableObjectNamespace;
}

interface StoredHost {
	key: string;
	generation: number;
	sequence: number;
}

type Role = 'host' | 'controller' | 'retired';

interface Attachment {
	role: Role;
	connection: number;
	controller: string;
}

interface Bucket {
	tokens: number;
	at: number;
}

function refuse(status: number): Response {
	// Deliberately bodiless: a refusal tells a caller nothing it did not already
	// know, and a reason string is one more channel that can leak.
	return new Response(null, { status });
}

function attachmentOf(ws: WebSocket): Attachment | null {
	let raw: unknown;
	try {
		raw = ws.deserializeAttachment();
	} catch {
		return null;
	}
	if (typeof raw !== 'object' || raw === null) return null;
	const candidate = raw as Partial<Attachment>;
	if (candidate.role !== 'host' && candidate.role !== 'controller' && candidate.role !== 'retired') return null;
	if (typeof candidate.connection !== 'number' || typeof candidate.controller !== 'string') return null;
	return { role: candidate.role, connection: candidate.connection, controller: candidate.controller };
}

function truncateReason(reason: string): string {
	// A close reason is capped at 123 UTF-8 bytes by the protocol.
	const encoder = new TextEncoder();
	let text = reason;
	while (encoder.encode(text).length > 123) text = text.slice(0, -1);
	return text;
}

function closeRecord(connection: number, code: number, reason: string): RelayRecord {
	return textRecord(RECORD_CLOSE, connection, JSON.stringify({ code, reason }));
}

export class FactoryRelay implements DurableObject {
	readonly #ctx: DurableObjectState;
	readonly #env: Env;
	/** Rate-limit state is per live socket only; hibernation resets a bucket that
	 * has necessarily been idle long enough to have refilled anyway. */
	readonly #buckets = new WeakMap<WebSocket, Bucket>();

	constructor(ctx: DurableObjectState, env: Env) {
		this.#ctx = ctx;
		this.#env = env;
		ctx.setWebSocketAutoResponse(new WebSocketRequestResponsePair('ping', 'pong'));
	}

	async fetch(request: Request): Promise<Response> {
		const url = new URL(request.url);
		const match = /^\/(host|controller)\/([^/]+)$/.exec(url.pathname);
		if (match === null) return refuse(404);
		const [, kind, node] = match as unknown as [string, string, string];
		if (!NODE_ID_PATTERN.test(node)) return refuse(404);
		if (request.method !== 'GET') return refuse(404);
		if ((request.headers.get('Upgrade') ?? '').toLowerCase() !== 'websocket') return refuse(426);
		const protocols = (request.headers.get('Sec-WebSocket-Protocol') ?? '')
			.split(',')
			.map((entry) => entry.trim())
			.filter((entry) => entry.length > 0);
		if (protocols[0] !== SUBPROTOCOL) return refuse(403);
		return kind === 'host'
			? await this.#acceptHost(request, node, protocols)
			: await this.#acceptController(request, node, protocols);
	}

	// -- handshakes ---------------------------------------------------------

	async #acceptHost(request: Request, node: string, protocols: string[]): Promise<Response> {
		// The outbound factoryd dial: no browser is involved, so an Origin header
		// means something other than factoryd is holding the socket.
		if (request.headers.get('Origin') !== null) return refuse(403);
		if (protocols.length !== 2) return refuse(403);
		const now = Math.floor(Date.now() / 1000);
		const token = await verifyHostToken(protocols[1] as string, node, now);
		if (token === null) return refuse(403);

		const stored = (await this.#ctx.storage.get<StoredHost>('host')) ?? null;
		if (stored !== null) {
			// Strictly newer (generation, sequence): a replayed token compares equal
			// and an older boot compares smaller, so both are refused here.
			const newer =
				token.generation > stored.generation ||
				(token.generation === stored.generation && token.sequence > stored.sequence);
			if (!newer) return refuse(403);
		}
		await this.#ctx.storage.put<StoredHost>('host', {
			key: token.key,
			generation: token.generation,
			sequence: token.sequence,
		});

		for (const previous of this.#ctx.getWebSockets('host')) {
			this.#retire(previous, CLOSE_HOST_REPLACED, 'host replaced', false);
		}
		this.#dropControllers(CLOSE_HOST_GONE, 'host replaced');

		return this.#upgrade({ role: 'host', connection: 0, controller: '' }, ['host']);
	}

	async #acceptController(request: Request, node: string, protocols: string[]): Promise<Response> {
		const origin = request.headers.get('Origin');
		if (origin === null || origin !== this.#env.PWA_ORIGIN) return refuse(403);
		if (protocols.length !== 2 && protocols.length !== 3) return refuse(403);

		const host = this.#host();
		if (host === null) return refuse(503);
		const stored = (await this.#ctx.storage.get<StoredHost>('host')) ?? null;
		if (stored === null) return refuse(503);
		const nodeKey = decodeBase64UrlExact(stored.key, 32);
		if (nodeKey === null) return refuse(503);

		const now = Math.floor(Date.now() / 1000);
		const ticket = await verifyTicket(protocols[1] as string, node, nodeKey);
		if (ticket === null) return refuse(403);
		if (ticket.expires <= now) return refuse(403);

		if (ticket.purpose === 'control') {
			if (protocols.length !== 3 || ticket.device === null) return refuse(403);
			const point = decodeBase64UrlExact(ticket.device, 65);
			if (point === null) return refuse(403);
			if ((await verifyProof(protocols[2] as string, point, ticket.ticket, now)) === null) return refuse(403);
		} else if (protocols.length !== 2) {
			return refuse(403);
		}

		const live = this.#controllers();
		if (live.length >= CONTROLLER_SOCKETS_PER_NODE) return refuse(429);
		const mine = live.filter((ws) => attachmentOf(ws)?.controller === ticket.controller);
		if (mine.length >= CONTROLLER_SOCKETS_PER_CONTROLLER) return refuse(429);

		const connection = this.#mintConnection();
		if (connection === null) return refuse(503);
		const response = this.#upgrade({ role: 'controller', connection, controller: ticket.controller }, [
			'controller',
			`c:${connection}`,
		]);
		this.#send(host, [
			textRecord(
				RECORD_OPEN,
				connection,
				JSON.stringify({
					controller: ticket.controller,
					purpose: ticket.purpose satisfies TicketPurpose,
					origin,
				}),
			),
		]);
		return response;
	}

	#upgrade(attachment: Attachment, tags: string[]): Response {
		const pair = new WebSocketPair();
		const client = pair[0];
		const server = pair[1];
		this.#ctx.acceptWebSocket(server, tags);
		server.serializeAttachment(attachment);
		return new Response(null, {
			status: 101,
			webSocket: client,
			headers: { 'Sec-WebSocket-Protocol': SUBPROTOCOL },
		});
	}

	#mintConnection(): number | null {
		for (let attempt = 0; attempt < 16; attempt += 1) {
			const candidate = crypto.getRandomValues(new Uint32Array(1))[0] as number;
			if (candidate === 0) continue;
			if (this.#ctx.getWebSockets(`c:${candidate}`).length === 0) return candidate;
		}
		return null;
	}

	// -- socket events ------------------------------------------------------

	webSocketMessage(ws: WebSocket, message: string | ArrayBuffer): void {
		const attachment = attachmentOf(ws);
		if (attachment === null || attachment.role === 'retired') return;
		if (attachment.role === 'host') {
			this.#onHostMessage(ws, message);
			return;
		}
		this.#onControllerMessage(ws, attachment, message);
	}

	webSocketClose(ws: WebSocket, code: number, reason: string): void {
		const attachment = attachmentOf(ws);
		if (attachment === null || attachment.role === 'retired') return;
		this.#markRetired(ws, attachment);
		if (attachment.role === 'controller') {
			this.#sendToHost([closeRecord(attachment.connection, code, reason)]);
			return;
		}
		this.#dropControllers(CLOSE_HOST_GONE, 'host disconnected');
	}

	webSocketError(ws: WebSocket): void {
		this.webSocketClose(ws, 1006, '');
	}

	// -- host traffic -------------------------------------------------------

	#onHostMessage(ws: WebSocket, message: string | ArrayBuffer): void {
		if (typeof message === 'string') {
			// `ping` never reaches here: setWebSocketAutoResponse answers it. Any
			// other text on the host socket is off-envelope.
			this.#failHost(ws, CLOSE_PROTOCOL, 'text on host socket');
			return;
		}
		if (message.byteLength > HOST_MESSAGE_LIMIT) {
			this.#failHost(ws, CLOSE_LIMIT, 'host message too large');
			return;
		}
		const parsed = parseHostMessage(new Uint8Array(message));
		if (!parsed.ok) {
			this.#failHost(
				ws,
				parsed.failure === 'oversize-record' ? CLOSE_LIMIT : CLOSE_PROTOCOL,
				parsed.failure,
			);
			return;
		}
		for (const record of parsed.records) {
			if (!this.#dispatch(ws, record)) return;
		}
	}

	/** Returns false when the host socket was torn down by this record. */
	#dispatch(ws: WebSocket, record: RelayRecord): boolean {
		if (record.type === RECORD_REVOKE) {
			const body = decodeJson(record.payload);
			if (body === null || typeof body.controller !== 'string') {
				this.#failHost(ws, CLOSE_PROTOCOL, 'malformed revoke');
				return false;
			}
			// Nothing is remembered: the daemon's own challenge is the durable
			// authority on whether a controller may come back.
			for (const socket of this.#controllers()) {
				if (attachmentOf(socket)?.controller === body.controller) {
					this.#retire(socket, CLOSE_REVOKED, 'revoked', false);
				}
			}
			return true;
		}

		const target = this.#controller(record.connection);
		if (target === null) return true; // Nothing is queued while a peer is away.

		if (record.type === RECORD_TEXT) {
			let text: string;
			try {
				text = new TextDecoder('utf-8', { fatal: true, ignoreBOM: false }).decode(record.payload);
			} catch {
				this.#failHost(ws, CLOSE_PROTOCOL, 'text record is not utf-8');
				return false;
			}
			trySend(target, text);
			return true;
		}
		if (record.type === RECORD_BINARY) {
			trySend(target, record.payload.slice().buffer);
			return true;
		}
		// RECORD_CLOSE
		const body = record.payload.length === 0 ? {} : decodeJson(record.payload);
		if (body === null) {
			this.#failHost(ws, CLOSE_PROTOCOL, 'malformed close');
			return false;
		}
		const requested = typeof body.code === 'number' && Number.isSafeInteger(body.code) ? body.code : null;
		const code =
			requested !== null && requested >= APPLICATION_CLOSE_MIN && requested <= APPLICATION_CLOSE_MAX
				? requested
				: CLOSE_SUBSTITUTED;
		const reason = typeof body.reason === 'string' ? body.reason : '';
		this.#retire(target, code, reason, false);
		return true;
	}

	#failHost(ws: WebSocket, code: number, reason: string): void {
		this.#retire(ws, code, reason, false);
		this.#dropControllers(CLOSE_HOST_GONE, 'host disconnected');
	}

	// -- controller traffic -------------------------------------------------

	#onControllerMessage(ws: WebSocket, attachment: Attachment, message: string | ArrayBuffer): void {
		if (!this.#consume(ws)) {
			this.#retire(ws, CLOSE_LIMIT, 'controller message rate exceeded', true);
			return;
		}
		if (typeof message === 'string') {
			const payload = new TextEncoder().encode(message);
			if (payload.length > CONTROLLER_MESSAGE_LIMIT) {
				this.#retire(ws, CLOSE_LIMIT, 'controller message too large', true);
				return;
			}
			this.#sendToHost([{ type: RECORD_TEXT, connection: attachment.connection, payload }]);
			return;
		}
		if (message.byteLength > CONTROLLER_MESSAGE_LIMIT) {
			this.#retire(ws, CLOSE_LIMIT, 'controller message too large', true);
			return;
		}
		this.#sendToHost([{ type: RECORD_BINARY, connection: attachment.connection, payload: new Uint8Array(message) }]);
	}

	#consume(ws: WebSocket): boolean {
		const now = Date.now();
		let bucket = this.#buckets.get(ws);
		if (bucket === undefined) {
			bucket = { tokens: BURST_MESSAGES, at: now };
			this.#buckets.set(ws, bucket);
		}
		const refill = ((now - bucket.at) / 1000) * SUSTAINED_MESSAGES_PER_SECOND;
		bucket.tokens = Math.min(BURST_MESSAGES, bucket.tokens + Math.max(0, refill));
		bucket.at = now;
		if (bucket.tokens < 1) return false;
		bucket.tokens -= 1;
		return true;
	}

	// -- socket bookkeeping -------------------------------------------------

	#host(): WebSocket | null {
		for (const ws of this.#ctx.getWebSockets('host')) {
			if (attachmentOf(ws)?.role === 'host') return ws;
		}
		return null;
	}

	#controllers(): WebSocket[] {
		return this.#ctx.getWebSockets('controller').filter((ws) => attachmentOf(ws)?.role === 'controller');
	}

	#controller(connection: number): WebSocket | null {
		if (connection === 0) return null;
		for (const ws of this.#ctx.getWebSockets(`c:${connection}`)) {
			if (attachmentOf(ws)?.role === 'controller') return ws;
		}
		return null;
	}

	#sendToHost(records: readonly RelayRecord[]): void {
		const host = this.#host();
		if (host !== null) this.#send(host, records);
	}

	#send(host: WebSocket, records: readonly RelayRecord[]): void {
		trySend(host, encodeRecords(records));
	}

	#markRetired(ws: WebSocket, attachment: Attachment): void {
		try {
			ws.serializeAttachment({
				role: 'retired',
				connection: attachment.connection,
				controller: attachment.controller,
			});
		} catch {
			// The socket is already gone; there is nothing left to mark.
		}
	}

	#retire(ws: WebSocket, code: number, reason: string, notifyHost: boolean): void {
		const attachment = attachmentOf(ws);
		if (attachment === null || attachment.role === 'retired') return;
		this.#markRetired(ws, attachment);
		if (notifyHost && attachment.role === 'controller') {
			this.#sendToHost([closeRecord(attachment.connection, code, reason)]);
		}
		try {
			ws.close(code, truncateReason(reason));
		} catch {
			// Already closing: the close code the peer sees is the first one sent.
		}
	}

	#dropControllers(code: number, reason: string): void {
		for (const ws of this.#controllers()) this.#retire(ws, code, reason, false);
	}

}

function trySend(ws: WebSocket, payload: string | ArrayBuffer): void {
	try {
		ws.send(payload);
	} catch {
		// The peer vanished between lookup and send; nothing is queued for it.
	}
}

function decodeJson(payload: Uint8Array): Record<string, unknown> | null {
	try {
		const value: unknown = JSON.parse(new TextDecoder('utf-8', { fatal: true, ignoreBOM: false }).decode(payload));
		if (typeof value !== 'object' || value === null || Array.isArray(value)) return null;
		return value as Record<string, unknown>;
	} catch {
		return null;
	}
}
