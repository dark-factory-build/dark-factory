// Exercise the real handler with a controlled clock: transport delivery speed
// must not decide whether 200 client writes constitute an excessive burst.
import assert from 'node:assert/strict';
import { register } from 'node:module';
import test from 'node:test';

register('./ts-loader.mjs', import.meta.url);
const { FactoryRelay } = await import('../src/relay.ts');
const { parseHostMessage, RECORD_TEXT, RECORD_CLOSE } = await import('../src/envelope.ts');

test('controller rate limits use arrival time, close the offender and notify the host once', (t) => {
	let now = 10_000;
	t.mock.method(Date, 'now', () => now);
	const originalPair = globalThis.WebSocketRequestResponsePair;
	globalThis.WebSocketRequestResponsePair = class {};
	t.after(() => {
		if (originalPair === undefined) delete globalThis.WebSocketRequestResponsePair;
		else globalThis.WebSocketRequestResponsePair = originalPair;
	});
	const records = [];
	let hostClosed = false;
	const host = {
		deserializeAttachment: () => ({ role: 'host', connection: 0, controller: '' }),
		send(message) {
			const parsed = parseHostMessage(new Uint8Array(message));
			assert.equal(parsed.ok, true);
			records.push(...parsed.records);
		},
		close() { hostClosed = true; },
	};
	const relay = new FactoryRelay({
		setWebSocketAutoResponse() {},
		getWebSockets: (tag) => tag === 'host' ? [host] : [],
	}, {});
	function controller(connection) {
		let attachment = { role: 'controller', connection, controller: `controller-${connection}` };
		return {
			closed: null,
			deserializeAttachment: () => attachment,
			serializeAttachment(value) { attachment = value; },
			close(code, reason) { this.closed = { code, reason }; },
		};
	}

	// Reproduce the failed integration test's invalid assumption: 200 messages
	// spread by transport scheduling over four seconds are within the rate.
	const paced = controller(1);
	for (let index = 0; index < 200; index += 1) {
		relay.webSocketMessage(paced, `paced-${index}`);
		now += 20;
	}
	assert.equal(paced.closed, null);
	assert.equal(records.length, 200);
	assert.ok(records.every((record) => record.type === RECORD_TEXT && record.connection === 1));

	for (const [connection, elapsed, allowance] of [[2, 0, 0], [3, 500, 30], [4, 60_000, 120]]) {
		const socket = controller(connection);
		const start = records.length;
		for (let index = 0; index < 120; index += 1) relay.webSocketMessage(socket, 'initial burst');
		assert.equal(socket.closed, null);
		now += elapsed;
		for (let index = 0; index < allowance; index += 1) relay.webSocketMessage(socket, 'refilled');
		assert.equal(socket.closed, null);
		assert.equal(records.length - start, 120 + allowance);
		relay.webSocketMessage(socket, 'over limit');
		assert.equal(socket.closed.code, 4003);
		const closed = records.at(-1);
		assert.equal(closed.type, RECORD_CLOSE);
		assert.equal(closed.connection, connection);
		assert.equal(JSON.parse(new TextDecoder().decode(closed.payload)).code, 4003);
		assert.equal(records.length - start, 121 + allowance);
		relay.webSocketMessage(socket, 'after retirement');
		relay.webSocketClose(socket, 4003, 'closed');
		assert.equal(records.length - start, 121 + allowance);
	}
	assert.equal(hostClosed, false);
	relay.webSocketMessage(paced, 'another controller remains usable');
	assert.equal(paced.closed, null);
	assert.equal(records.at(-1).connection, 1);
	assert.equal(records.at(-1).type, RECORD_TEXT);
});
