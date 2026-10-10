// The relay Worker: a router thin enough to hold no state of its own. Every
// decision that matters happens inside the per-node Durable Object.

import { FactoryRelay, type Env } from './relay.js';
import { INGEST_LIMIT } from './envelope.js';
import { NODE_ID_PATTERN } from './tokens.js';

export { FactoryRelay };

export default {
	async fetch(request: Request, env: Env): Promise<Response> {
		const url = new URL(request.url);
		if (url.pathname === '/healthz') {
			return new Response('ok\n', {
				status: 200,
				headers: { 'content-type': 'text/plain; charset=utf-8', 'cache-control': 'no-store' },
			});
		}
		const read = /^\/public\/([^/]+)$/.exec(url.pathname);
		if (read !== null) return await readWorld(request, env, read[1] as string);
		const ingest = /^\/ingest\/([^/]+)\/v1\/traces$/.exec(url.pathname);
		if (ingest !== null) return await routeIngest(request, env, ingest[1] as string);
		const match = /^\/(?:host|controller)\/([^/]+)$/.exec(url.pathname);
		if (match === null) return new Response(null, { status: 404 });
		const node = match[1] as string;
		// Validate the name before it can name an object: an unbounded path
		// segment would let a caller conjure Durable Objects at will.
		if (!NODE_ID_PATTERN.test(node)) return new Response(null, { status: 404 });
		return await env.FACTORY_RELAY.getByName(node).fetch(request);
	},
} satisfies ExportedHandler<Env>;

/**
 * The read-only public feed: the latest world one factory published, by its
 * public id. There is no listing and no history; only the site origin may read
 * it from a browser, and each client address gets a bounded number of reads.
 */
async function readWorld(request: Request, env: Env, id: string): Promise<Response> {
	const headers = {
		'access-control-allow-origin': env.SITE_ORIGIN,
		'cache-control': 'public, max-age=15',
		'x-content-type-options': 'nosniff',
	};
	if (!NODE_ID_PATTERN.test(id)) return new Response(null, { status: 404, headers });
	if (request.method !== 'GET') return new Response(null, { status: 405, headers: { ...headers, allow: 'GET' } });
	const { success } = await env.PUBLIC_READS.limit({ key: request.headers.get('CF-Connecting-IP') ?? '' });
	if (!success) return new Response(null, { status: 429, headers });
	// A fresh GET: nothing of the caller's request reaches the object.
	const world = await env.FACTORY_RELAY.getByName(`public:${id}`).fetch(`https://relay/public/${id}`);
	const served = new Headers(world.headers);
	for (const [name, value] of Object.entries(headers)) served.set(name, value);
	return new Response(world.body, { status: world.status, headers: served });
}

/**
 * A remote platform's OTLP traces push. Everything that needs no state is
 * refused here, bodiless, before any object wakes: the node object then
 * checks the secret, the media type and its rate before reading the body.
 */
async function routeIngest(request: Request, env: Env, node: string): Promise<Response> {
	if (!NODE_ID_PATTERN.test(node)) return new Response(null, { status: 404 });
	if (request.method !== 'POST') return new Response(null, { status: 405, headers: { allow: 'POST' } });
	if (!/^Bearer [A-Za-z0-9_-]{43}$/.test(request.headers.get('Authorization') ?? '')) {
		return new Response(null, { status: 401 });
	}
	const length = request.headers.get('Content-Length');
	if (length === null || !/^\d{1,7}$/.test(length)) return new Response(null, { status: 411 });
	if (Number(length) > INGEST_LIMIT) return new Response(null, { status: 413 });
	return await env.FACTORY_RELAY.getByName(node).fetch(request);
}
