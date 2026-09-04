// Integration tests against a real `wrangler dev --local` child, in the style
// of control-plane/tests/worker.integration.test.mjs: plain `node --test`, a
// spawned Worker with a cleaned environment, and no in-process Worker shim. One
// child is shared for speed; every test mints its own node id for isolation.

import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { mkdtemp, readFile, readdir, rm, stat } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test, { after, before } from 'node:test';

import {
	AMBIENT_DECOYS,
	PWA_ORIGIN,
	RECORD_BINARY,
	RECORD_CLOSE,
	RECORD_OPEN,
	RECORD_REVOKE,
	RECORD_TEXT,
	SUBPROTOCOL,
	corrupt,
	createControllerId,
	createDevice,
	createNode,
	encodeRecord,
	encodeRecords,
	mintHostToken,
	mintProof,
	mintTicket,
	nowSeconds,
	openController,
	openHost,
	readPersistedObjects,
	startWorker,
} from './helpers.mjs';

let persistence;
let worker;

before(async () => {
	persistence = await mkdtemp(join(tmpdir(), 'df-relay-state-'));
	worker = await startWorker(persistence);
}, { timeout: 120_000 });

after(async () => {
	await worker?.stop();
	if (persistence) await rm(persistence, { recursive: true, force: true });
});

/** A node with a live host socket. */
async function withHost({ generation = 1, sequence = 1 } = {}) {
	const node = createNode();
	const host = await openHost(worker.origin, node.id, mintHostToken(node, { generation, sequence }));
	assert.equal(host.status, 101);
	return { node, host };
}

/** A control credential pair for one controller id. */
async function controlCredential(node, controller = createControllerId()) {
	const device = await createDevice();
	const ticket = mintTicket(node, { controller, purpose: 'control', device: device.point });
	return { controller, device, ticket, proof: await mintProof(device, ticket.id) };
}

async function openControl(node, credential) {
	return openController(worker.origin, node.id, [credential.ticket.token, credential.proof]);
}

// -- routing ----------------------------------------------------------------

test('the health endpoint reports the Worker is serving', async () => {
	const response = await fetch(`${worker.origin}/healthz`);
	assert.equal(response.status, 200);
	assert.equal(await response.text(), 'ok\n');
});

test('an unknown path is not found', async () => {
	for (const path of ['/', '/nope', '/host', '/controller', '/v1/github/maintainer/webhook']) {
		assert.equal((await fetch(`${worker.origin}${path}`)).status, 404, path);
	}
});

test('a malformed node id is not found', async () => {
	const valid = createNode().id;
	const malformed = [
		valid.slice(0, 31), // too short
		`${valid}a`, // too long
		valid.replace(/./, 'A'), // uppercase is outside the alphabet
		valid.replace(/./, '0'), // 0, 1, 8 and 9 are outside base32
		valid.replace(/./, '-'),
		`${valid}/extra`,
	];
	for (const node of malformed) {
		for (const kind of ['host', 'controller']) {
			assert.equal((await fetch(`${worker.origin}/${kind}/${node}`)).status, 404, `${kind}/${node}`);
		}
	}
});

// -- host handshake ---------------------------------------------------------

test('a valid host handshake selects the relay subprotocol', async () => {
	const node = createNode();
	const host = await openHost(worker.origin, node.id, mintHostToken(node));
	assert.equal(host.status, 101);
	assert.equal(host.protocol, SUBPROTOCOL);
	host.tap.close(1000, 'done');
});

test('a host token with a bad signature is refused', async () => {
	const node = createNode();
	const refused = await openHost(worker.origin, node.id, corrupt(mintHostToken(node)));
	assert.equal(refused.status, 403);
});

test('a host key that does not hash to the node id is refused', async () => {
	const node = createNode();
	const other = createNode();
	// A correctly self-signed token for `other`'s key, presented at `node`'s id.
	const token = mintHostToken(node, { signer: other, id: node.id, key: other.key });
	const refused = await openHost(worker.origin, node.id, token);
	assert.equal(refused.status, 403);
});

test('a host token issued outside the sixty second skew is refused', async () => {
	const node = createNode();
	for (const issued of [nowSeconds() - 61, nowSeconds() + 61]) {
		const refused = await openHost(worker.origin, node.id, mintHostToken(node, { issued }));
		assert.equal(refused.status, 403, `issued ${issued}`);
	}
	// The same node still connects with an in-window token, so nothing else was wrong.
	const accepted = await openHost(worker.origin, node.id, mintHostToken(node));
	assert.equal(accepted.status, 101);
	accepted.tap.close(1000, 'done');
});

test('an Origin header on the host path is refused', async () => {
	const node = createNode();
	// Even the correct PWA origin is refused: factoryd dials outbound, so a
	// browser holding this socket is by construction the wrong peer.
	const refused = await openHost(worker.origin, node.id, mintHostToken(node), { originHeader: PWA_ORIGIN });
	assert.equal(refused.status, 403);
});

test('a replayed host token is refused', async () => {
	const node = createNode();
	const token = mintHostToken(node, { generation: 7, sequence: 3 });
	const accepted = await openHost(worker.origin, node.id, token);
	assert.equal(accepted.status, 101);
	const replayed = await openHost(worker.origin, node.id, token);
	assert.equal(replayed.status, 403);
	accepted.tap.close(1000, 'done');
});

test('a higher sequence in the same generation reconnects', async () => {
	const node = createNode();
	const first = await openHost(worker.origin, node.id, mintHostToken(node, { generation: 2, sequence: 1 }));
	assert.equal(first.status, 101);
	const second = await openHost(worker.origin, node.id, mintHostToken(node, { generation: 2, sequence: 2 }));
	assert.equal(second.status, 101);
	assert.equal((await first.tap.waitClosed()).code, 4000);
	second.tap.close(1000, 'done');
});

test('an older generation is refused after a newer one', async () => {
	const node = createNode();
	const newer = await openHost(worker.origin, node.id, mintHostToken(node, { generation: 5, sequence: 1 }));
	assert.equal(newer.status, 101);
	const older = await openHost(worker.origin, node.id, mintHostToken(node, { generation: 4, sequence: 9 }));
	assert.equal(older.status, 403);
	// The live socket was untouched by the refusal, and stays that way.
	assert.equal((await newer.tap.quiet(200)).closed, null);
	newer.tap.close(1000, 'done');
});

test('a new generation displaces the old host and its controllers', async () => {
	const node = createNode();
	const first = await openHost(worker.origin, node.id, mintHostToken(node, { generation: 1, sequence: 1 }));
	assert.equal(first.status, 101);
	const credential = await controlCredential(node);
	const controller = await openControl(node, credential);
	assert.equal(controller.status, 101);
	await first.tap.nextRecordOf(RECORD_OPEN);

	const second = await openHost(worker.origin, node.id, mintHostToken(node, { generation: 2, sequence: 1 }));
	assert.equal(second.status, 101);
	assert.equal((await first.tap.waitClosed()).code, 4000);
	assert.equal((await controller.tap.waitClosed()).code, 4001);

	// The replacement host is fully functional.
	const rejoined = await openControl(node, await controlCredential(node));
	assert.equal(rejoined.status, 101);
	const open = await second.tap.nextRecordOf(RECORD_OPEN);
	rejoined.tap.send('after the handover');
	const echoed = await second.tap.nextRecordOf(RECORD_TEXT);
	assert.equal(echoed.connection, open.connection);
	assert.equal(echoed.payload.toString('utf8'), 'after the handover');
	second.tap.close(1000, 'done');
});

// -- controller handshake ---------------------------------------------------

test('a controller from the wrong origin is refused', async () => {
	const { node } = await withHost();
	const credential = await controlCredential(node);
	const refused = await openController(worker.origin, node.id, [credential.ticket.token, credential.proof], {
		originHeader: 'https://app.darkfactory.build.evil.example',
	});
	assert.equal(refused.status, 403);
});

test('a controller without an Origin header is refused', async () => {
	const { node } = await withHost();
	const credential = await controlCredential(node);
	const refused = await openController(worker.origin, node.id, [credential.ticket.token, credential.proof], {
		originHeader: null,
	});
	assert.equal(refused.status, 403);
});

test('a controller is unavailable while no host is connected', async () => {
	const node = createNode();
	const credential = await controlCredential(node);
	assert.equal((await openControl(node, credential)).status, 503);
});

test('a factory with no host refuses even an invalid ticket with 503', async () => {
	const node = createNode();
	const credential = await controlCredential(node);
	// Nothing about a factory's credentials is observable before a host attaches:
	// a ticket that would fail verification is refused with the same 503 as one
	// that would pass, so the two cannot be told apart from outside.
	const forged = corrupt(credential.ticket.token);
	assert.equal((await openController(worker.origin, node.id, [forged, credential.proof])).status, 503);
	assert.equal((await openControl(node, credential)).status, 503);
});

test('a control ticket with a device proof is accepted', async () => {
	const { node, host } = await withHost();
	const credential = await controlCredential(node);
	const accepted = await openControl(node, credential);
	assert.equal(accepted.status, 101);
	assert.equal(accepted.protocol, SUBPROTOCOL);
	await host.tap.nextRecordOf(RECORD_OPEN);
});

test('a pair ticket carries no proof and is accepted', async () => {
	const { node, host } = await withHost();
	const controller = createControllerId();
	const ticket = mintTicket(node, { controller, purpose: 'pair' });
	const accepted = await openController(worker.origin, node.id, [ticket.token]);
	assert.equal(accepted.status, 101);
	const open = await host.tap.nextRecordOf(RECORD_OPEN);
	assert.deepEqual(JSON.parse(open.payload.toString('utf8')), {
		controller,
		purpose: 'pair',
		origin: PWA_ORIGIN,
	});
});

test('an expired ticket is refused', async () => {
	const { node } = await withHost();
	const device = await createDevice();
	const ticket = mintTicket(node, {
		controller: createControllerId(),
		purpose: 'control',
		device: device.point,
		expires: nowSeconds() - 1,
	});
	const proof = await mintProof(device, ticket.id);
	const refused = await openController(worker.origin, node.id, [ticket.token, proof]);
	assert.equal(refused.status, 403);
});

test('a ticket signed by a different node key is refused', async () => {
	const { node } = await withHost();
	const impostor = createNode();
	const device = await createDevice();
	const ticket = mintTicket(node, {
		controller: createControllerId(),
		purpose: 'control',
		device: device.point,
		signer: impostor,
	});
	const proof = await mintProof(device, ticket.id);
	const refused = await openController(worker.origin, node.id, [ticket.token, proof]);
	assert.equal(refused.status, 403);
});

test('a proof signed by a different device key is refused', async () => {
	const { node } = await withHost();
	const enrolled = await createDevice();
	const attacker = await createDevice();
	const ticket = mintTicket(node, {
		controller: createControllerId(),
		purpose: 'control',
		device: enrolled.point,
	});
	const proof = await mintProof(attacker, ticket.id);
	const refused = await openController(worker.origin, node.id, [ticket.token, proof]);
	assert.equal(refused.status, 403);
});

test('a proof for a different ticket id is refused', async () => {
	const { node } = await withHost();
	const device = await createDevice();
	const presented = mintTicket(node, {
		controller: createControllerId(),
		purpose: 'control',
		device: device.point,
	});
	const other = mintTicket(node, { controller: createControllerId(), purpose: 'control', device: device.point });
	const proof = await mintProof(device, other.id);
	const refused = await openController(worker.origin, node.id, [presented.token, proof]);
	assert.equal(refused.status, 403);
});

test('the thirty-third controller socket for a factory is refused', async () => {
	const { node, host } = await withHost();
	const sockets = [];
	for (let index = 0; index < 8; index += 1) {
		const credential = await controlCredential(node);
		for (let repeat = 0; repeat < 4; repeat += 1) {
			const accepted = await openControl(node, credential);
			assert.equal(accepted.status, 101, `socket ${index * 4 + repeat}`);
			sockets.push(accepted);
		}
	}
	assert.equal(sockets.length, 32);
	const overflow = await openControl(node, await controlCredential(node));
	assert.equal(overflow.status, 429);
	// The host saw exactly the accepted openings and no more.
	await host.tap.nextRecords(32);
	assert.equal((await host.tap.quiet()).records, 0);
	for (const socket of sockets) socket.tap.close(1000, 'done');
});

test('the fifth socket for one controller id is refused', async () => {
	const { node } = await withHost();
	const credential = await controlCredential(node);
	for (let index = 0; index < 4; index += 1) {
		assert.equal((await openControl(node, credential)).status, 101, `socket ${index}`);
	}
	assert.equal((await openControl(node, credential)).status, 429);
	// A different controller id on the same factory is unaffected.
	assert.equal((await openControl(node, await controlCredential(node))).status, 101);
});

// -- multiplexing -----------------------------------------------------------

test('two controllers open with distinct nonzero connection ids', async () => {
	const { node, host } = await withHost();
	const first = await controlCredential(node);
	const second = await controlCredential(node);
	assert.equal((await openControl(node, first)).status, 101);
	assert.equal((await openControl(node, second)).status, 101);

	const opens = await host.tap.nextRecords(2);
	assert.deepEqual(
		opens.map(({ type }) => type),
		[RECORD_OPEN, RECORD_OPEN],
	);
	const [left, right] = opens;
	assert.notEqual(left.connection, 0);
	assert.notEqual(right.connection, 0);
	assert.notEqual(left.connection, right.connection);
	assert.deepEqual(JSON.parse(left.payload.toString('utf8')), {
		controller: first.controller,
		purpose: 'control',
		origin: PWA_ORIGIN,
	});
	assert.deepEqual(JSON.parse(right.payload.toString('utf8')), {
		controller: second.controller,
		purpose: 'control',
		origin: PWA_ORIGIN,
	});
});

test('text and binary frames route in both directions to exactly one socket', async () => {
	const { node, host } = await withHost();
	const alpha = await openControl(node, await controlCredential(node));
	const bravo = await openControl(node, await controlCredential(node));
	const [openAlpha, openBravo] = await host.tap.nextRecords(2);

	alpha.tap.send('alpha says hello');
	bravo.tap.send(Buffer.from([0xde, 0xad, 0xbe, 0xef]));
	const first = await host.tap.nextRecord();
	const second = await host.tap.nextRecord();
	assert.deepEqual(
		[
			{ type: first.type, connection: first.connection, body: first.payload.toString('utf8') },
			{ type: second.type, connection: second.connection, body: second.payload.toString('hex') },
		],
		[
			{ type: RECORD_TEXT, connection: openAlpha.connection, body: 'alpha says hello' },
			{ type: RECORD_BINARY, connection: openBravo.connection, body: 'deadbeef' },
		],
	);

	host.tap.send(
		encodeRecords([
			{ type: RECORD_TEXT, connection: openAlpha.connection, payload: 'for alpha only' },
			{ type: RECORD_BINARY, connection: openBravo.connection, payload: Buffer.from([1, 2, 3]) },
		]),
	);
	assert.equal(await alpha.tap.nextMessage(), 'for alpha only');
	assert.equal((await bravo.tap.nextMessage()).toString('hex'), '010203');
	assert.equal((await alpha.tap.quiet()).messages, 0);
	assert.equal(bravo.tap.messages.length, 0);
});

test('one host message with three records for two connections is delivered in order', async () => {
	const { node, host } = await withHost();
	const alpha = await openControl(node, await controlCredential(node));
	const bravo = await openControl(node, await controlCredential(node));
	const [openAlpha, openBravo] = await host.tap.nextRecords(2);

	host.tap.send(
		encodeRecords([
			{ type: RECORD_TEXT, connection: openAlpha.connection, payload: 'one' },
			{ type: RECORD_TEXT, connection: openBravo.connection, payload: 'two' },
			{ type: RECORD_TEXT, connection: openAlpha.connection, payload: 'three' },
		]),
	);
	assert.equal(await alpha.tap.nextMessage(), 'one');
	assert.equal(await alpha.tap.nextMessage(), 'three');
	assert.equal(await bravo.tap.nextMessage(), 'two');
});

test('a controller close reaches the host as a close record for that id', async () => {
	const { node, host } = await withHost();
	const alpha = await openControl(node, await controlCredential(node));
	const bravo = await openControl(node, await controlCredential(node));
	const [openAlpha] = await host.tap.nextRecords(2);

	alpha.tap.close(4321, 'controller is done');
	const closed = await host.tap.nextRecordOf(RECORD_CLOSE);
	assert.equal(closed.connection, openAlpha.connection);
	assert.deepEqual(JSON.parse(closed.payload.toString('utf8')), { code: 4321, reason: 'controller is done' });
	assert.equal((await bravo.tap.quiet(200)).closed, null);
});

test('a host close record chooses the controller close code', async () => {
	const { node, host } = await withHost();
	const alpha = await openControl(node, await controlCredential(node));
	const bravo = await openControl(node, await controlCredential(node));
	const [openAlpha, openBravo] = await host.tap.nextRecords(2);

	host.tap.send(
		encodeRecords([
			{
				type: RECORD_CLOSE,
				connection: openAlpha.connection,
				payload: JSON.stringify({ code: 4100, reason: 'application close' }),
			},
			{
				type: RECORD_CLOSE,
				connection: openBravo.connection,
				payload: JSON.stringify({ code: 1000, reason: 'not an application code' }),
			},
		]),
	);
	assert.equal((await alpha.tap.waitClosed()).code, 4100);
	assert.equal((await bravo.tap.waitClosed()).code, 4005);
	// The relay does not echo a close it was told to perform.
	assert.equal((await host.tap.quiet()).records, 0);
});

test('losing the host socket closes every controller', async () => {
	const { node, host } = await withHost();
	const alpha = await openControl(node, await controlCredential(node));
	const bravo = await openControl(node, await controlCredential(node));
	await host.tap.nextRecords(2);

	host.tap.terminate();
	assert.equal((await alpha.tap.waitClosed()).code, 4001);
	assert.equal((await bravo.tap.waitClosed()).code, 4001);
	// With no host attached the factory is unavailable again.
	assert.equal((await openControl(node, await controlCredential(node))).status, 503);
});

test('a controller dialing while the host drops is never left orphaned', async () => {
	const { node, host } = await withHost();
	const credentials = [];
	for (let index = 0; index < 24; index += 1) credentials.push(await controlCredential(node));

	// Every dial is left in flight at once, so the host's loss falls somewhere in
	// the middle of the batch. The point is not to force one interleaving but to
	// hold the guarantee over whichever one the object happens to take.
	const dials = credentials.map((credential) => openControl(node, credential));
	host.tap.terminate();
	const results = await Promise.all(dials);

	for (const { status } of results) {
		assert.ok(status === 101 || status === 503, `unexpected status ${status}`);
	}
	// The invariant: nothing outlives the host it was accepted for. An accepted
	// socket is closed with 4001; it is never left alive and unannounced.
	for (const result of results.filter(({ status }) => status === 101)) {
		assert.equal((await result.tap.waitClosed()).code, 4001);
	}
	assert.equal((await openControl(node, await controlCredential(node))).status, 503);
});

test('a controller dialing across a host replacement reaches the new host or none', async () => {
	const { node, host } = await withHost();
	const credentials = [];
	for (let index = 0; index < 24; index += 1) credentials.push(await controlCredential(node));

	const dials = credentials.map((credential) => openControl(node, credential));
	host.tap.terminate();
	const replacement = await openHost(
		worker.origin,
		node.id,
		mintHostToken(node, { generation: 2, sequence: 1 }),
	);
	assert.equal(replacement.status, 101);
	const results = await Promise.all(dials);
	await new Promise((resolve) => setTimeout(resolve, 500));

	const announced = new Set(
		replacement.tap.records
			.filter((record) => record.type === RECORD_OPEN)
			.map((record) => JSON.parse(record.payload.toString('utf8')).controller),
	);
	for (const [index, result] of results.entries()) {
		if (result.status === 503) continue;
		assert.equal(result.status, 101);
		const { controller } = credentials[index];
		assert.ok(
			announced.has(controller) || result.tap.closed?.code === 4001,
			`an accepted controller was neither announced to the new host nor closed with 4001`,
		);
	}
});

// -- revocation -------------------------------------------------------------

test('a revoke closes every socket of that controller id and leaves others alone', async () => {
	const { node, host } = await withHost();
	const revoked = await controlCredential(node);
	const spared = await controlCredential(node);
	const first = await openControl(node, revoked);
	const second = await openControl(node, revoked);
	const other = await openControl(node, spared);
	await host.tap.nextRecords(3);

	host.tap.send(
		encodeRecords([
			{ type: RECORD_REVOKE, connection: 0, payload: JSON.stringify({ controller: revoked.controller }) },
		]),
	);
	assert.equal((await first.tap.waitClosed()).code, 4002);
	assert.equal((await second.tap.waitClosed()).code, 4002);
	assert.equal((await other.tap.quiet(200)).closed, null);

	// Nothing is remembered: a fresh credential for either controller reconnects,
	// because the daemon that minted it is the durable authority on revocation.
	assert.equal((await openControl(node, await controlCredential(node, revoked.controller))).status, 101);
	assert.equal((await openControl(node, spared)).status, 101);
});

// -- bounds -----------------------------------------------------------------

test('a controller message over 64 KiB ends that socket', async () => {
	const { node, host } = await withHost();
	const controller = await openControl(node, await controlCredential(node));
	const open = await host.tap.nextRecordOf(RECORD_OPEN);

	controller.tap.send(Buffer.alloc(64 * 1024 + 1, 0x41));
	assert.equal((await controller.tap.waitClosed()).code, 4003);
	const closed = await host.tap.nextRecordOf(RECORD_CLOSE);
	assert.equal(closed.connection, open.connection);
	assert.equal(JSON.parse(closed.payload.toString('utf8')).code, 4003);
	assert.equal((await host.tap.quiet(200)).closed, null);
});

test('a burst past the token bucket ends the controller and tells the host', async () => {
	const { node, host } = await withHost();
	const controller = await openControl(node, await controlCredential(node));
	const open = await host.tap.nextRecordOf(RECORD_OPEN);

	for (let index = 0; index < 200; index += 1) controller.tap.send(`burst-${index}`);
	assert.equal((await controller.tap.waitClosed()).code, 4003);
	const closed = await host.tap.nextRecordOf(RECORD_CLOSE);
	assert.equal(closed.connection, open.connection);
	assert.equal(JSON.parse(closed.payload.toString('utf8')).code, 4003);
	assert.equal((await host.tap.quiet(200)).closed, null);
});

test('a record payload past the cap ends the host and every controller', async () => {
	const { node, host } = await withHost();
	const alpha = await openControl(node, await controlCredential(node));
	const open = await host.tap.nextRecordOf(RECORD_OPEN);

	// One byte past 1 MiB + 64, carried in full, so the refusal is about the
	// bound rather than about a record that ran off the end of the message.
	host.tap.send(encodeRecord(RECORD_BINARY, open.connection, Buffer.alloc(1024 * 1024 + 65, 0x7a)));
	assert.equal((await host.tap.waitClosed()).code, 4003);
	assert.equal((await alpha.tap.waitClosed()).code, 4001);
});

test('a text frame other than ping ends the host socket', async () => {
	const { node, host } = await withHost();
	const controller = await openControl(node, await controlCredential(node));
	await host.tap.nextRecordOf(RECORD_OPEN);

	// `ping` is answered by the auto-responder and never reaches the envelope,
	// which is the whole reason any other text on this socket is off-protocol.
	host.tap.send('ping');
	assert.equal(await host.tap.nextText(), 'pong');
	assert.equal((await host.tap.quiet(200)).closed, null);

	host.tap.send('pong');
	assert.equal((await host.tap.waitClosed()).code, 4004);
	assert.equal((await controller.tap.waitClosed()).code, 4001);
});

test(
	'a megabyte close reason is truncated instead of stalling the object',
	{ timeout: 30_000 },
	async () => {
		const { node, host } = await withHost();
		const alpha = await openControl(node, await controlCredential(node));
		const bravo = await openControl(node, await controlCredential(node));
		const [openAlpha, openBravo] = await host.tap.nextRecords(2);

		const started = Date.now();
		host.tap.send(
			encodeRecords([
				{
					type: RECORD_CLOSE,
					connection: openAlpha.connection,
					payload: JSON.stringify({ code: 4200, reason: 'r'.repeat(1024 * 1024) }),
				},
			]),
		);
		const closed = await alpha.tap.waitClosed();
		assert.equal(closed.code, 4200);
		assert.ok(
			Buffer.byteLength(closed.reason, 'utf8') <= 123,
			`close reason was ${Buffer.byteLength(closed.reason, 'utf8')} bytes`,
		);
		assert.ok(Date.now() - started < 5_000, `the close took ${Date.now() - started}ms`);

		// The object is still serving rather than wedged on the reason it was handed.
		host.tap.send(
			encodeRecords([{ type: RECORD_TEXT, connection: openBravo.connection, payload: 'still serving' }]),
		);
		assert.equal(await bravo.tap.nextMessage(), 'still serving');
	},
);

test('an unknown record type ends the host and every controller', async () => {
	const { node, host } = await withHost();
	const alpha = await openControl(node, await controlCredential(node));
	const bravo = await openControl(node, await controlCredential(node));
	await host.tap.nextRecords(2);

	host.tap.send(encodeRecord(0x09, 1, 'not a record type'));
	assert.equal((await host.tap.waitClosed()).code, 4004);
	assert.equal((await alpha.tap.waitClosed()).code, 4001);
	assert.equal((await bravo.tap.waitClosed()).code, 4001);
});

test('a truncated record ends the host', async () => {
	const { node, host } = await withHost();
	const controller = await openControl(node, await controlCredential(node));
	const open = await host.tap.nextRecordOf(RECORD_OPEN);

	// A well-formed header claiming 100 payload bytes, followed by five.
	const header = Buffer.alloc(9);
	header.writeUInt8(RECORD_TEXT, 0);
	header.writeUInt32BE(open.connection, 1);
	header.writeUInt32BE(100, 5);
	host.tap.send(Buffer.concat([header, Buffer.from('short')]));

	assert.equal((await host.tap.waitClosed()).code, 4004);
	assert.equal((await controller.tap.waitClosed()).code, 4001);
});

// -- secrecy ----------------------------------------------------------------

test('no frame, token, or payload reaches disk or the log', async () => {
	const frameSentinel = `SENTINEL-CONTROL-${randomUUID()}`;
	const tokenSentinel = `SENTINEL-TOKEN-${randomUUID()}`;

	const { node, host } = await withHost();
	const controller = createControllerId();
	const device = await createDevice();
	// An unknown payload member is ignored by the relay, so it rides along
	// through every verification step without ever being stored.
	const ticket = mintTicket(node, {
		controller,
		purpose: 'control',
		device: device.point,
		extra: { note: tokenSentinel },
	});
	const proof = await mintProof(device, ticket.id);
	const socket = await openController(worker.origin, node.id, [ticket.token, proof]);
	assert.equal(socket.status, 101);
	const open = await host.tap.nextRecordOf(RECORD_OPEN);

	socket.tap.send(`${frameSentinel} up`);
	assert.equal((await host.tap.nextRecordOf(RECORD_TEXT)).payload.toString('utf8'), `${frameSentinel} up`);
	socket.tap.send(Buffer.from(`${frameSentinel} binary`, 'utf8'));
	assert.equal((await host.tap.nextRecordOf(RECORD_BINARY)).payload.toString('utf8'), `${frameSentinel} binary`);
	host.tap.send(
		encodeRecords([{ type: RECORD_TEXT, connection: open.connection, payload: `${frameSentinel} down` }]),
	);
	assert.equal(await socket.tap.nextMessage(), `${frameSentinel} down`);
	socket.tap.close(4321, `${frameSentinel} closing`);
	await host.tap.nextRecordOf(RECORD_CLOSE);

	// Give the Durable Object a moment to have flushed anything it meant to.
	await new Promise((resolve) => setTimeout(resolve, 1_000));

	const disk = await readTree(persistence);
	assert.ok(disk.length > 0, 'the persistence directory has files to inspect');
	const blob = disk.map(({ text }) => text).join('\0');
	const transcript = worker.transcript();

	for (const [label, haystack] of [
		['persisted state', blob],
		['wrangler output', transcript],
	]) {
		assert.ok(!haystack.includes(frameSentinel), `${label} contains a frame sentinel`);
		assert.ok(!haystack.includes(tokenSentinel), `${label} contains a token sentinel`);
		assert.ok(!haystack.includes(ticket.token), `${label} contains a ticket token`);
		assert.ok(!haystack.includes(proof), `${label} contains a device proof`);
		// The operator's ambient environment is planted on the Wrangler child on
		// purpose. None of it may reach the child's output or its state.
		for (const [name, decoy] of Object.entries(AMBIENT_DECOYS)) {
			assert.ok(!haystack.includes(decoy), `${label} contains the ${name} decoy`);
		}
	}

	// The positive canary, read out of the Durable Object's own SQLite rows
	// rather than matched in a byte scan: this is what the object kept, and the
	// absence of everything above is therefore a real observation.
	const objects = await readPersistedObjects(persistence);
	const mine = objects.find((object) => object.node === node.id);
	assert.ok(mine !== undefined, 'the factory has a persisted Durable Object');
	assert.deepEqual(mine.records, [
		{ key: 'host', value: { key: node.key, generation: 1, sequence: 1 } },
	]);

	// And no object anywhere in this run wrote a key other than `host`: the
	// ticket and deny lists really are gone, not merely unused by this test.
	assert.deepEqual(
		objects.flatMap(({ node: name, records }) =>
			records.filter(({ key }) => key !== 'host').map(({ key }) => `${name}:${key}`),
		),
		[],
	);
});

async function readTree(directory) {
	const files = [];
	const walk = async (current) => {
		for (const entry of await readdir(current, { withFileTypes: true })) {
			const path = join(current, entry.name);
			if (entry.isDirectory()) {
				await walk(path);
				continue;
			}
			if (!entry.isFile()) continue;
			const info = await stat(path);
			if (info.size > 64 * 1024 * 1024) continue;
			// latin1 keeps every byte addressable, so a sentinel cannot hide in
			// a sequence that is not valid UTF-8.
			files.push({ path, text: (await readFile(path)).toString('latin1') });
		}
	};
	await walk(directory);
	return files;
}
