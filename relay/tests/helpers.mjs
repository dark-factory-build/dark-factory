// Test-side implementation of the relay's client halves: node keys, device
// keys, credential minting, the record envelope, and `ws` clients that can set
// the headers the handshake is specified in terms of. Nothing here imports the
// Worker, so a test failure means the deployed behaviour is wrong rather than
// that two copies of one bug agree.

import { generateKeyPairSync, randomBytes, sign as nodeSign, createHash } from 'node:crypto';
import { once } from 'node:events';
import { spawn } from 'node:child_process';
import { join } from 'node:path';
import WebSocket from 'ws';

export const SUBPROTOCOL = 'dark-factory-relay';
export const PWA_ORIGIN = 'https://app.darkfactory.build';

export const RECORD_OPEN = 0x01;
export const RECORD_TEXT = 0x02;
export const RECORD_BINARY = 0x03;
export const RECORD_CLOSE = 0x04;
export const RECORD_REVOKE = 0x05;

const BASE32_LOWER = 'abcdefghijklmnopqrstuvwxyz234567';

export function base64url(bytes) {
	return Buffer.from(bytes).toString('base64url');
}

export function base32Lower(bytes) {
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
	if (bits > 0) text += BASE32_LOWER[(buffer << (5 - bits)) & 31];
	return text;
}

export function nowSeconds() {
	return Math.floor(Date.now() / 1000);
}

// -- identities -------------------------------------------------------------

/** An Ed25519 node key plus the node id derived from it. */
export function createNode() {
	const { publicKey, privateKey } = generateKeyPairSync('ed25519');
	const raw = Buffer.from(publicKey.export({ format: 'jwk' }).x, 'base64url');
	const digest = createHash('sha256').update(raw).digest();
	return {
		id: base32Lower(digest.subarray(0, 20)),
		key: base64url(raw),
		privateKey,
	};
}

/** A non-exportable-in-spirit ECDSA P-256 device key, minted through WebCrypto. */
export async function createDevice() {
	const pair = await globalThis.crypto.subtle.generateKey({ name: 'ECDSA', namedCurve: 'P-256' }, true, [
		'sign',
		'verify',
	]);
	const point = new Uint8Array(await globalThis.crypto.subtle.exportKey('raw', pair.publicKey));
	return { point: base64url(point), privateKey: pair.privateKey };
}

export function createControllerId() {
	return base64url(randomBytes(16));
}

export function createTicketId() {
	return base64url(randomBytes(16));
}

// -- credentials ------------------------------------------------------------

function edToken(domain, payload, privateKey) {
	const payloadText = base64url(Buffer.from(JSON.stringify(payload), 'utf8'));
	const signature = nodeSign(null, Buffer.from(domain + payloadText, 'utf8'), privateKey);
	return `${payloadText}.${base64url(signature)}`;
}

export function mintHostToken(node, { generation = 1, sequence = 1, issued = nowSeconds(), signer, id, key } = {}) {
	const holder = signer ?? node;
	return edToken(
		'dark-factory-relay/host\n',
		{ node: id ?? node.id, key: key ?? holder.key, generation, sequence, issued },
		holder.privateKey,
	);
}

/** Flips one character of the signature half, leaving the token well-formed. */
export function corrupt(token) {
	const [payload, signature] = token.split('.');
	return `${payload}.${signature[0] === 'A' ? 'B' : 'A'}${signature.slice(1)}`;
}

export function mintTicket(node, { controller, purpose, ticket = createTicketId(), expires, device, extra, signer } = {}) {
	const payload = {
		node: node.id,
		controller,
		purpose,
		ticket,
		expires: expires ?? nowSeconds() + 300,
		...(purpose === 'control' ? { device } : {}),
		...(extra ?? {}),
	};
	return { id: payload.ticket, token: edToken('dark-factory-relay/ticket\n', payload, (signer ?? node).privateKey) };
}

export async function mintProof(device, ticketId, { issued = nowSeconds(), nonce = base64url(randomBytes(16)) } = {}) {
	const payload = { ticket: ticketId, issued, nonce };
	const payloadText = base64url(Buffer.from(JSON.stringify(payload), 'utf8'));
	const signature = new Uint8Array(
		await globalThis.crypto.subtle.sign(
			{ name: 'ECDSA', hash: 'SHA-256' },
			device.privateKey,
			Buffer.from('dark-factory-relay/proof\n' + payloadText, 'utf8'),
		),
	);
	return `${payloadText}.${base64url(signature)}`;
}

// -- envelope ---------------------------------------------------------------

export function encodeRecord(type, connection, payload) {
	const bytes = typeof payload === 'string' ? Buffer.from(payload, 'utf8') : Buffer.from(payload);
	const record = Buffer.alloc(9 + bytes.length);
	record.writeUInt8(type, 0);
	record.writeUInt32BE(connection >>> 0, 1);
	record.writeUInt32BE(bytes.length, 5);
	bytes.copy(record, 9);
	return record;
}

export function encodeRecords(records) {
	return Buffer.concat(records.map(({ type, connection, payload }) => encodeRecord(type, connection, payload)));
}

export function decodeRecords(bytes) {
	const buffer = Buffer.from(bytes);
	const records = [];
	let offset = 0;
	while (offset + 9 <= buffer.length) {
		const type = buffer.readUInt8(offset);
		const connection = buffer.readUInt32BE(offset + 1);
		const length = buffer.readUInt32BE(offset + 5);
		records.push({ type, connection, payload: buffer.subarray(offset + 9, offset + 9 + length) });
		offset += 9 + length;
	}
	return records;
}

// -- sockets ----------------------------------------------------------------

const DEFAULT_TIMEOUT = 10_000;

class Tap {
	constructor(socket) {
		this.socket = socket;
		this.messages = [];
		this.records = [];
		this.closed = null;
		this.wakers = [];
		this.closingSince = null;
		// `wrangler dev`'s local proxy can hold the TCP connection open for about
		// ten seconds after a close frame, and `ws` withholds its close event until
		// the socket ends. The frame itself arrives in about a millisecond and
		// already carries the code, so finish the teardown here; `ws` then reports
		// the code and reason it read off the wire rather than 1006.
		this.watchdog = setInterval(() => {
			if (socket.readyState !== WebSocket.CLOSING) {
				this.closingSince = null;
				return;
			}
			this.closingSince ??= Date.now();
			if (Date.now() - this.closingSince > 150) socket.terminate();
		}, 20);
		this.watchdog.unref();
		socket.on('close', () => clearInterval(this.watchdog));
		socket.on('message', (data, isBinary) => {
			this.messages.push(isBinary ? Buffer.from(data) : Buffer.from(data).toString('utf8'));
			if (isBinary) this.records.push(...decodeRecords(data));
			this.#wake();
		});
		socket.on('close', (code, reason) => {
			this.closed = { code, reason: Buffer.from(reason).toString('utf8') };
			this.#wake();
		});
		socket.on('error', () => this.#wake());
	}

	#wake() {
		const waiters = this.wakers;
		this.wakers = [];
		for (const waker of waiters) waker();
	}

	async #until(predicate, label, timeout) {
		const deadline = Date.now() + timeout;
		while (!predicate()) {
			if (Date.now() >= deadline) throw new Error(`timed out waiting for ${label}`);
			await new Promise((resolve) => {
				const timer = setTimeout(resolve, 25);
				this.wakers.push(() => {
					clearTimeout(timer);
					resolve();
				});
			});
		}
	}

	async nextMessage(timeout = DEFAULT_TIMEOUT) {
		await this.#until(() => this.messages.length > 0 || this.closed !== null, 'a message', timeout);
		if (this.messages.length === 0) throw new Error(`socket closed (${this.closed?.code}) before a message arrived`);
		return this.messages.shift();
	}

	async nextRecord(timeout = DEFAULT_TIMEOUT) {
		await this.#until(() => this.records.length > 0 || this.closed !== null, 'a record', timeout);
		if (this.records.length === 0) throw new Error(`socket closed (${this.closed?.code}) before a record arrived`);
		return this.records.shift();
	}

	async nextRecordOf(type, timeout = DEFAULT_TIMEOUT) {
		const deadline = Date.now() + timeout;
		for (;;) {
			const record = await this.nextRecord(Math.max(1, deadline - Date.now()));
			if (record.type === type) return record;
		}
	}

	async nextRecords(count, timeout = DEFAULT_TIMEOUT) {
		const collected = [];
		for (let index = 0; index < count; index += 1) collected.push(await this.nextRecord(timeout));
		return collected;
	}

	async waitClosed(timeout = DEFAULT_TIMEOUT) {
		await this.#until(() => this.closed !== null, 'the close frame', timeout);
		return this.closed;
	}

	/** Asserts nothing else arrives for a short settle window. */
	async quiet(window = 400) {
		await new Promise((resolve) => setTimeout(resolve, window));
		return { messages: this.messages.length, records: this.records.length, closed: this.closed };
	}

	send(payload) {
		this.socket.send(payload);
	}

	close(code, reason) {
		this.socket.close(code, reason);
	}

	terminate() {
		this.socket.terminate();
	}
}

function connect(url, protocols, options) {
	return new Promise((resolve, reject) => {
		const socket = new WebSocket(url, protocols, { followRedirects: false, handshakeTimeout: 10_000, ...options });
		let settled = false;
		const settle = (value) => {
			if (settled) return;
			settled = true;
			resolve(value);
		};
		socket.on('upgrade', (response) => {
			socket.selectedProtocolHeader = response.headers['sec-websocket-protocol'];
		});
		socket.on('open', () => settle({ status: 101, protocol: socket.protocol, tap: new Tap(socket) }));
		socket.on('unexpected-response', (request, response) => {
			response.resume();
			request.destroy();
			settle({ status: response.statusCode, protocol: null, tap: null });
		});
		socket.on('error', (error) => {
			if (!settled) {
				settled = true;
				reject(error);
			}
		});
	});
}

export function openHost(origin, node, token, { originHeader = null } = {}) {
	const options = originHeader === null ? {} : { origin: originHeader };
	return connect(`${origin.replace('http', 'ws')}/host/${node}`, [SUBPROTOCOL, token], options);
}

export function openController(origin, node, protocols, { originHeader = PWA_ORIGIN } = {}) {
	const options = originHeader === null ? {} : { origin: originHeader };
	return connect(`${origin.replace('http', 'ws')}/controller/${node}`, [SUBPROTOCOL, ...protocols], options);
}

// -- the wrangler child -----------------------------------------------------

export async function startWorker(persistence) {
	const root = process.cwd();
	const wrangler = join(root, 'node_modules', 'wrangler', 'bin', 'wrangler.js');
	const cleanEnvironment = join(root, 'scripts', 'with-clean-wrangler-env.sh');
	const child = spawn(
		cleanEnvironment,
		[
			process.execPath,
			wrangler,
			'dev',
			'--local',
			'--ip',
			'127.0.0.1',
			'--port',
			'0',
			'--persist-to',
			persistence,
			'--log-level',
			'log',
		],
		{
			cwd: root,
			env: {
				PATH: process.env.PATH ?? '/usr/bin:/bin',
				HOME: '/operator-home',
				TMPDIR: '/operator-tmp',
				CLOUDFLARE_API_TOKEN: 'ambient-token-must-not-cross',
				CLOUDFLARE_ACCOUNT_ID: 'ambient-account-must-not-cross',
				WRANGLER_AUTH_TOKEN: 'ambient-oauth-must-not-cross',
				XDG_CONFIG_HOME: '/operator-config',
				SSH_AUTH_SOCK: '/operator-keychain-socket',
			},
			stdio: ['ignore', 'pipe', 'pipe'],
		},
	);
	let transcript = '';
	const capture = (chunk) => {
		transcript = `${transcript}${chunk}`;
		if (transcript.length > 8_000_000) transcript = transcript.slice(-8_000_000);
	};
	child.stdout.on('data', capture);
	child.stderr.on('data', capture);

	for (let attempt = 0; attempt < 240; attempt += 1) {
		if (child.exitCode !== null) throw new Error(`wrangler exited before readiness:\n${transcript}`);
		const ready = /Ready on (http:\/\/127\.0\.0\.1:\d+)/.exec(transcript);
		if (ready) {
			try {
				const response = await fetch(`${ready[1]}/healthz`);
				if (response.status === 200) {
					return { origin: ready[1], transcript: () => transcript, stop: () => stop(child) };
				}
			} catch {
				// The local socket is not listening yet.
			}
		}
		await new Promise((resolve) => setTimeout(resolve, 250));
	}
	await stop(child);
	throw new Error(`wrangler did not become ready:\n${transcript}`);
}

async function stop(child) {
	if (child.exitCode !== null) return;
	const exited = once(child, 'exit');
	child.kill('SIGTERM');
	const timeout = new Promise((resolve) => setTimeout(resolve, 5_000, 'timeout'));
	if ((await Promise.race([exited, timeout])) === 'timeout') {
		child.kill('SIGKILL');
		await exited;
	}
}
