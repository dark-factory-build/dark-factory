import assert from 'node:assert/strict';
import { createHash, generateKeyPairSync } from 'node:crypto';
import { mkdtemp, rm, readFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import test from 'node:test';
import { Miniflare, convertV4MiniflareOptions } from 'miniflare';
const hash = value => createHash('sha256').update(value).digest('hex');
const prefix = '/v1/github/connections';
const json = (body, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });

test('two principals: callback, pagination, refresh, replay, grants and revocation', async () => {
  const persistence = await mkdtemp(join(tmpdir(), 'df-connections-'));
  const { privateKey } = generateKeyPairSync('rsa', { modulusLength: 2048 });
  let revoked = false, removed = false, push = true, bobWrite = false, refreshed = 0, exchanged = 0;
  const repository = name => ({ id: 2, full_name: 'team/shared', permissions: { pull: true, push: name === 'alice' ? push : bobWrite } });
  const mf = new Miniflare(convertV4MiniflareOptions({ durableObjectsPersist: persistence, name: "fixture",
    modules: [{ type: 'ESModule', path: resolve('build/index.js'), contents: await readFile('build/index.js', 'utf8') }, { type: 'CompiledWasm', path: resolve('build/index_bg.wasm'), contents: await readFile('build/index_bg.wasm') }],
    compatibilityDate: '2026-08-22',
    durableObjects: { DARK_FACTORY_MAINTAINER_DELIVERIES: { className: 'MaintainerDeliveryJournal', useSQLite: true } },
    outboundService: async request => {
      const url = new URL(request.url);
      assert.ok(['github.com', 'api.github.com'].includes(url.hostname));
      if (url.pathname === '/login/oauth/access_token') {
        assert.equal(request.headers.get('accept'), 'application/json');
        const body = await request.json();
        assert.equal(body.client_id, 'Iv1.fixture');
        assert.equal(body.client_secret, 'fixture-client-secret-value');
        const who = body.grant_type ? body.refresh_token.split('-')[0] : body.code;
        if (body.grant_type) refreshed++;
        else { exchanged++; assert.match(body.code_verifier, /^[0-9a-f]{64}$/); }
        return json({ access_token: `${who}-access`, refresh_token: `${who}-refresh`, token_type: 'bearer', expires_in: who === 'bob' && !body.grant_type ? 30 : 3600, refresh_token_expires_in: 86400 });
      }
      const who = request.headers.get('authorization')?.replace('Bearer ', '').split('-')[0];
      assert.ok(['alice', 'bob'].includes(who), 'only broker-held user tokens leave for GitHub');
      if (revoked && who === 'alice') return json({}, 401);
      if (url.pathname === '/user') return json({ id: who === 'alice' ? 10 : 20, login: who });
      if (url.pathname === '/user/installations') return json({ installations: url.searchParams.get('page') === '1'
        ? Array.from({ length: 100 }, (_, i) => ({ id: i + 100, app_id: 999, repository_selection: 'selected', suspended_at: null, account: { id: 1, login: 'other' } }))
        : [{ id: 7, app_id: 5678, repository_selection: 'selected', suspended_at: null, account: { id: 1, login: 'team' } }] });
      if (url.pathname === '/user/installations/7/repositories') return json({ repositories: url.searchParams.get('page') === '1'
        ? Array.from({ length: 100 }, (_, i) => ({ id: i + 100, full_name: `team/other-${i}`, permissions: { pull: true } }))
        : removed ? [] : [repository(who)] });
      throw new Error(`unexpected GitHub request ${request.method} ${url.pathname}`);
    },
    bindings: {
      DARK_FACTORY_MAINTAINER_WEBHOOK_SECRET: '0123456789abcdef0123456789abcdef',
      DARK_FACTORY_MAINTAINER_WEBHOOK_SECRET_REVISION: 'fixture-v1', DARK_FACTORY_MAINTAINER_APP_ID: '5678',
      DARK_FACTORY_MAINTAINER_PRIVATE_KEY_PKCS8: privateKey.export({ type: 'pkcs8', format: 'der' }).toString('base64'),
      DARK_FACTORY_MAINTAINER_PERMISSION_REVISION: 'maintainer-operations-v6',
      DARK_FACTORY_MAINTAINER_OPERATOR_EMAIL_SHA256: 'a'.repeat(64),
      DARK_FACTORY_CLOUDFLARE_ACCESS_TEAM_DOMAIN: 'https://fixture.cloudflareaccess.com',
      DARK_FACTORY_CLOUDFLARE_ACCESS_AUD: 'b'.repeat(64), DARK_FACTORY_MAINTAINER_CLIENT_ID: 'Iv1.fixture',
      DARK_FACTORY_MAINTAINER_CLIENT_SECRET: 'fixture-client-secret-value',
      DARK_FACTORY_MAINTAINER_CALLBACK_URL: `https://broker.example${prefix}/callback`,
    },
  }));
  const send = (path, method = 'GET', body, credential) => mf.dispatchFetch(`https://broker.example${path}`, {
    method, headers: { 'content-type': 'application/json', ...(credential ? { authorization: `Bearer ${credential}` } : {}) },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
  const connect = async who => {
    const started = await send(prefix, 'POST', {});
    assert.equal(started.status, 201);
    assert.equal(started.headers.get('cache-control'), 'no-store');
    const c = await started.json();
    assert.equal(hash(c.credential), c.connection_id);
    const url = new URL(c.authorization_url);
    assert.equal(url.searchParams.get('code_challenge_method'), 'S256');
    const callback = `${prefix}/callback?state=${url.searchParams.get('state')}&code=${who}`;
    assert.equal((await send(`${prefix}/callback?state=${c.connection_id}.wrong&code=${who}`)).status, 401);
    assert.equal((await send(callback)).status, 200);
    assert.equal((await send(callback)).status, 401);
    return { ...c, path: `${prefix}/${c.connection_id}` };
  };
  try {
    const alice = await connect('alice'), bob = await connect('bob');
    assert.equal(exchanged, 2);
    await Promise.all([send(bob.path, 'GET', undefined, bob.credential), send(bob.path, 'GET', undefined, bob.credential)]);
    assert.equal(refreshed, 1, 'concurrent requests serialize one refresh');
    assert.equal((await send(`${alice.path}/mcp`, 'POST', {}, bob.credential)).status, 401);
    assert.equal((await send(`${alice.path}/mcp`, 'POST', {})).status, 401);
    assert.deepEqual(await (await send(`${alice.path}/installations`, 'GET', undefined, alice.credential)).json(), { installations: [], next_page: 2 });
    const second = await (await send(`${alice.path}/installations?page=2`, 'GET', undefined, alice.credential)).json();
    assert.equal(second.installations[0].id, 7);
    assert.equal((await send(`${alice.path}/installations?page=invalid`, 'GET', undefined, alice.credential)).status, 400);
    assert.equal((await send(`${alice.path}/installations?page=1&page=2`, 'GET', undefined, alice.credential)).status, 400);
    const listed = await (await send(`${alice.path}/mcp`, 'POST', { jsonrpc:'2.0', id:1, method:'tools/list' }, alice.credential)).json();
    assert.ok(listed.result.tools.find(tool => tool.name === 'observe_operation').inputSchema.required.includes('repository'));
    const repos = await (await send(`${alice.path}/repositories?installation_id=7&page=2`, 'GET', undefined, alice.credential)).json();
    assert.equal(repos.repositories[0].id, 2);
    for (const c of [alice, bob]) assert.equal((await send(`${c.path}/repositories`, 'PUT', { repositories: [{ installation_id: 7, repository_id: 2, repository: 'team/shared' }] }, c.credential)).status, 200);
    assert.equal(refreshed, 1);
    const id = '6d1f0f8e-7f1f-11f0-952e-acde48001122';
    const args = { repository: 'team/shared', operation_id: id, title: 'private fixture', body: 'accepted bytes' };
    const operation = { operation_id: id, kind: 'create_issue', request_digest: hash(JSON.stringify(args)) };
    const scope = { owner: alice.connection_id, repository: 'github:2' };
    const namespace = await mf.getDurableObjectNamespace('DARK_FACTORY_MAINTAINER_DELIVERIES');
    const shard = namespace.get(namespace.idFromName(`maintainer:5678:operation:${hash(id).slice(0, 2)}`));
    for (const [path, extra] of [['begin', {}], ['mark', { transition: { completed: JSON.stringify({ number: 123, url: 'https://github.com/team/shared/issues/123' }) } }]])
      assert.equal((await shard.fetch(`https://journal.internal/operation/${path}`, { method: 'POST', body: JSON.stringify({ operation, scope, ...extra }) })).status, 200);
    const call = (c, name, arguments_) => send(`${c.path}/mcp`, 'POST', { jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name, arguments: arguments_ } }, c.credential);
    const observe = { repository: 'team/shared', operation_id: id };
    assert.equal((await (await call(alice, 'observe_operation', observe)).json()).result.structuredContent.state, 'completed');
    assert.equal((await (await call(bob, 'observe_operation', observe)).json()).result.structuredContent.state, 'missing');
    assert.equal((await call(bob, 'create_issue', args)).status, 401);
    bobWrite = true;
    assert.equal((await (await call(bob, 'create_issue', args)).json()).result.isError, true, 'second writer cannot replay first owner');
    bobWrite = false;
    assert.equal((await call(alice, 'create_issue', { ...args, source_repository: 'other/private' })).status, 401, 'source repository requires its own delegation');
    assert.equal((await call(bob, 'observe_release_workflow', observe)).status, 401, 'remote receipt markers require their owner');
    assert.equal((await (await call(alice, 'create_issue', args)).json()).result.structuredContent.number, 123);
    push = false;
    assert.equal((await call(alice, 'create_issue', args)).status, 401, 'write loss blocks completed replay');
    push = true; removed = true;
    assert.equal((await call(alice, 'observe_operation', observe)).status, 401, 'installation loss blocks receipt');
    removed = false; revoked = true;
    assert.equal((await call(alice, 'observe_operation', observe)).status, 401, 'authorization revocation blocks receipt');
    revoked = false;
    assert.equal((await send(alice.path, 'DELETE', undefined, alice.credential)).status, 200);
    assert.equal((await call(alice, 'observe_operation', observe)).status, 401, 'disconnect has no owner fallback');
  } finally { await mf.dispose(); await rm(persistence, { recursive: true, force: true }); }
});
