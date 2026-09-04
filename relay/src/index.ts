// The relay Worker: a router thin enough to hold no state of its own. Every
// decision that matters happens inside the per-node Durable Object.

import { FactoryRelay, type Env } from './relay.js';
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
		const match = /^\/(?:host|controller)\/([^/]+)$/.exec(url.pathname);
		if (match === null) return new Response(null, { status: 404 });
		const node = match[1] as string;
		// Validate the name before it can name an object: an unbounded path
		// segment would let a caller conjure Durable Objects at will.
		if (!NODE_ID_PATTERN.test(node)) return new Response(null, { status: 404 });
		return await env.FACTORY_RELAY.getByName(node).fetch(request);
	},
} satisfies ExportedHandler<Env>;
