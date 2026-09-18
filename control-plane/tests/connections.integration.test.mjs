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
  let wrongApp = false;
  let revoked = false, removed = false, push = true, bobWrite = false, refreshed = 0, exchanged = 0;
  let unavailable = '', unavailableStatus = 503, sourceVisible = true, wrongGrant = false, wrongInstallation = false, replacedPath = false, repositoryReads = 0, sourceReads = 0;
  const permissionSet = { contents: 'write', issues: 'write', metadata: 'read', pull_requests: 'write' };
  const grants = [];
  const repository = name => ({ id: 2, full_name: 'team/shared', permissions: { pull: true, push: name === 'alice' ? push : bobWrite } });
  const mf = new Miniflare(convertV4MiniflareOptions({ durableObjectsPersist: persistence, name: "fixture",
    modules: [{ type: 'ESModule', path: resolve('build/index.js'), contents: await readFile('build/index.js', 'utf8') }, { type: 'CompiledWasm', path: resolve('build/index_bg.wasm'), contents: await readFile('build/index_bg.wasm') }],
    compatibilityDate: '2026-08-22',
    durableObjects: { DARK_FACTORY_MAINTAINER_DELIVERIES: { className: 'MaintainerDeliveryJournal', useSQLite: true } },
    outboundService: async request => {
      const url = new URL(request.url);
      assert.ok(['github.com', 'api.github.com'].includes(url.hostname));
      if (url.pathname === unavailable) return json({}, unavailableStatus);
      if (url.pathname === '/app') return json({ id: wrongApp ? 999 : 5678, slug: 'fixture-maintainer' });
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
      if (url.pathname.endsWith('/installation')) return json({ id: wrongInstallation ? 8 : 7, app_id: 5678, account: { id: 1 }, repository_selection: 'selected', permissions: permissionSet, events: [], suspended_at: null });
      if (url.pathname === '/app/installations/7/access_tokens') {
        const body = await request.json();
        assert.equal(body.repositories, undefined, 'customer tokens cannot select by mutable name');
        assert.equal(body.repository_ids.length, 1);
        const id = body.repository_ids[0];
        assert.ok([2, 3].includes(id));
        grants.push(id);
        return json({ token: `app-${id}-fixture-installation-token`, permissions: body.permissions, repositories: [{ id: wrongGrant ? 999 : id, full_name: id === 2 ? 'team/shared' : 'team/backlog', owner: { id: 1 } }] });
      }
      if (request.headers.get('authorization')?.startsWith('Bearer app-')) {
        repositoryReads++;
        if (replacedPath) { assert.equal(request.headers.get('authorization'), 'Bearer app-2-fixture-installation-token'); return json({}, 404); }
        if (url.pathname.includes('/git/ref/')) return json({ ref: 'refs/heads/main', object: { type: 'commit', sha: 'a'.repeat(40) } });
        if (url.pathname.endsWith('/pulls')) return json([]);
        if (url.pathname === '/repos/team/shared/issues' || url.pathname === '/repos/team/shared/issues/9') {
          if (url.pathname.endsWith('/issues')) {
            assert.equal(url.searchParams.get('per_page'), '25');
            assert.ok(['needs triage', ''].includes(url.searchParams.get('labels')));
          }
          const issue = { id: 81, node_id: 'I_fixture', number: 9, html_url: 'https://github.com/team/shared/issues/9', title: 'review me', body: 'exact content', user: { login: 'outsider', type: 'User' }, state: 'open', updated_at: '2026-09-18T12:00:00Z', labels: [{ name: 'needs triage' }, { name: 'bug, urgent' }] };
          return json(url.pathname.endsWith('/9') ? {...issue, state: 'closed'} : [issue]);
        }
        if (url.pathname === '/repos/team/backlog/issues/1') {
          assert.equal(request.headers.get('authorization'), 'Bearer app-3-fixture-installation-token'); sourceReads++;
          return json({ number: 1, html_url: 'https://github.com/team/backlog/issues/1', title: 'source', body: 'source', state: 'closed' });
        }
        if (['/repos/team/shared', '/repos/team/backlog'].includes(url.pathname)) return json({ id: url.pathname.endsWith('/backlog') ? 3 : 2, full_name: url.pathname.slice(7), default_branch: 'main', private: true });
        throw new Error(`unexpected installation-token request ${url.pathname}`);
      }
      const who = request.headers.get('authorization')?.replace('Bearer ', '').split('-')[0];
      assert.ok(['alice', 'bob'].includes(who), 'only broker-held user tokens leave for GitHub');
      if (revoked && who === 'alice') return json({}, 401);
      if (url.pathname === '/user') return json({ id: who === 'alice' ? 10 : 20, login: who });
      if (url.pathname === '/user/installations') return json({ installations: url.searchParams.get('page') === '1'
        ? Array.from({ length: 100 }, (_, i) => ({ id: i + 100, app_id: 999, repository_selection: 'selected', suspended_at: null, account: { id: 1, login: 'other' } }))
        : [{ id: 7, app_id: 5678, repository_selection: 'selected', suspended_at: null, account: { id: 1, login: 'team', type: 'Organization' }, html_url: 'https://github.com/organizations/team/settings/installations/7' }, { id: 8, app_id: 5678, repository_selection: 'selected', suspended_at: '2026-09-18', account: { id: 1, login: 'team' } }, { id: 9, app_id: 5678, repository_selection: 'all', suspended_at: null, account: { id: 1, login: 'team' } }] });
      if (url.pathname === '/user/installations/7/repositories') return json({ repositories: url.searchParams.get('page') === '1'
        ? Array.from({ length: 100 }, (_, i) => ({ id: i + 100, full_name: `team/other-${i}`, permissions: { pull: true } }))
        : removed ? [] : [repository(who), ...(sourceVisible ? [{ id: 3, full_name: 'team/backlog', permissions: { pull: true } }] : [])] });
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
  const connect = async (who, confirm = true) => {
    const started = await send(prefix, 'POST', {});
    assert.equal(started.status, 201);
    assert.equal(started.headers.get('cache-control'), 'no-store');
    const c = await started.json();
    assert.equal(hash(c.credential), c.connection_id);
    const url = new URL(c.authorization_url);
    assert.equal(url.searchParams.get('code_challenge_method'), 'S256');
    const callback = `${prefix}/callback?state=${url.searchParams.get('state')}&code=${who}`;
    assert.equal((await send(`${prefix}/callback?state=${c.connection_id}.wrong&code=${who}`)).status, 401);
    const completed = await send(callback);
    assert.equal(completed.status, 200);
    const code = (await completed.text()).match(/Confirmation code: ([0-9A-F]{10})/)[1];
    const path = `${prefix}/${c.connection_id}`;
    assert.deepEqual(await (await send(path, 'GET', undefined, c.credential)).json(), { state: 'awaiting_confirmation', connection_id: c.connection_id, repositories: [] });
    assert.equal((await send(`${path}/installations`, 'GET', undefined, c.credential)).status, 401);
    assert.equal((await send(`${path}/mcp`, 'POST', { jsonrpc: '2.0', id: 1, method: 'tools/list' }, c.credential)).status, 401);
    if (confirm) {
      assert.equal((await send(`${path}/confirm`, 'POST', { code }, c.credential)).status, 200);
      assert.equal((await send(`${path}/confirm`, 'POST', { code }, c.credential)).status, 404, 'confirmation is one-use');
    }
    assert.equal((await send(callback)).status, 401);
    return { ...c, path, code };
  };
  try {
    // An attacker owns the start credential but forwards the OAuth link to
    // the victim. Only the victim's callback browser receives the second code.
    const forwarded = await connect('alice', false);
    for (let attempt = 0; attempt < 5; attempt++) assert.equal((await send(`${forwarded.path}/confirm`, 'POST', { code: 'wrong' }, forwarded.credential)).status, 401);
    assert.equal((await send(`${forwarded.path}/confirm`, 'POST', { code: forwarded.code }, forwarded.credential)).status, 401, 'five guesses invalidate even the correct code');
    const alice = await connect('alice'), bob = await connect('bob');
    assert.equal(exchanged, 3);
    await Promise.all([send(bob.path, 'GET', undefined, bob.credential), send(bob.path, 'GET', undefined, bob.credential)]);
    assert.equal(refreshed, 1, 'concurrent requests serialize one refresh');
    assert.equal((await send(`${alice.path}/mcp`, 'POST', {}, bob.credential)).status, 401);
    assert.equal((await send(`${alice.path}/mcp`, 'POST', {})).status, 401);
    assert.deepEqual(await (await send(`${alice.path}/installations`, 'GET', undefined, alice.credential)).json(), { installations: [], next_page: 2, installation_url: 'https://github.com/apps/fixture-maintainer/installations/new' });
    wrongApp = true;
    assert.equal((await send(`${alice.path}/installations`, 'GET', undefined, alice.credential)).status, 503, 'installation link must belong to the configured App');
    wrongApp = false; unavailable = '/app';
    assert.equal((await send(`${alice.path}/installations`, 'GET', undefined, alice.credential)).status, 503, 'unavailable App identity is not an empty installation list');
    unavailable = '';
    const second = await (await send(`${alice.path}/installations?page=2`, 'GET', undefined, alice.credential)).json();
    assert.equal(second.installations[0].id, 7);
    assert.equal(second.installations[0].account.type, 'Organization');
    assert.equal(second.installations[0].html_url, 'https://github.com/organizations/team/settings/installations/7');
    assert.deepEqual(second.installations.map(i => i.eligibility), ['available', 'suspended', 'all_repositories_unsupported']);
    assert.equal((await send(`${alice.path}/repositories?installation_id=8`, 'GET', undefined, alice.credential)).status, 401);
    assert.equal((await send(`${alice.path}/repositories?installation_id=9`, 'GET', undefined, alice.credential)).status, 401);
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
    const issuePage = (await (await call(bob, 'list_issues', { repository: 'team/shared', page: 1, label: 'needs triage' })).json()).result.structuredContent;
    assert.equal(issuePage.repository_id, 2);
    assert.equal(issuePage.issues[0].body, 'exact content');
    assert.equal(issuePage.issues[0].author.login, 'outsider');
    assert.equal(issuePage.next_page, null);
    const commaPage = (await (await call(bob, 'list_issues', { repository: 'team/shared', page: 1, label: 'bug, urgent' })).json()).result.structuredContent;
    assert.equal(commaPage.issues[0].number, 9);
    const noMatch = (await (await call(bob, 'list_issues', { repository: 'team/shared', page: 1, label: 'different, label' })).json()).result.structuredContent;
    assert.deepEqual(noMatch.issues, []);
    const exact = (await (await call(bob, 'list_issues', { repository: 'team/shared', page: 1, issue_number: 9 })).json()).result.structuredContent;
    assert.equal(exact.issues[0].state, 'closed');
    assert.equal(exact.issues[0].node_id, 'I_fixture');
    unavailable = '/repos/team/shared/issues';
    for (const status of [403, 404, 429, 503]) {
      unavailableStatus = status;
      const failure = await (await call(bob, 'list_issues', { repository: 'team/shared', page: 1 })).json();
      assert.equal(failure.result.isError, true);
      assert.match(failure.result.content[0].text, /unavailable/);
    }
    unavailable = ''; unavailableStatus = 503;
    assert.equal((await call(bob, 'list_issues', { repository: 'team/guessed', page: 1 })).status, 401);
    const observe = { repository: 'team/shared', operation_id: id };
    assert.equal((await (await call(alice, 'observe_operation', observe)).json()).result.structuredContent.state, 'completed');
    assert.equal((await (await call(bob, 'observe_operation', observe)).json()).result.structuredContent.state, 'missing');
    assert.equal((await call(bob, 'create_issue', args)).status, 401);
    bobWrite = true;
    assert.equal((await (await call(bob, 'create_issue', args)).json()).result.isError, true, 'second writer cannot replay first owner');
    bobWrite = false;
    const cross = { repository: 'team/shared', operation_id: '6d1f0f8e-7f1f-11f0-952e-acde48001123', issue_number: 1, source_repository: 'team/backlog', head: 'topic', head_sha: 'b'.repeat(40), base: 'main', base_sha: 'a'.repeat(40), title: 'cross repository', body: 'accepted bytes', draft: true };
    assert.ok(listed.result.tools.find(tool => tool.name === 'create_pull_request').inputSchema.properties.source_repository);
    assert.equal((await call(alice, 'create_pull_request', cross)).status, 401, 'supported source repository requires its own delegation');
    assert.equal((await send(`${alice.path}/repositories`, 'PUT', { repositories: [{ installation_id: 7, repository_id: 2, repository: 'team/shared' }, { installation_id: 7, repository_id: 3, repository: 'team/backlog' }] }, alice.credential)).status, 200);
    const crossReply = await (await call(alice, 'create_pull_request', cross)).json();
    assert.equal(crossReply.result.isError, true, 'closed source issue refuses publication');
    assert.equal(sourceReads, 1, JSON.stringify({ crossReply, grants, repositoryReads }));
    assert.deepEqual(grants.slice(-2), [2, 3]);
    const crossOperation = { operation_id: cross.operation_id, kind: 'create_pull_request', request_digest: hash(JSON.stringify(cross)) };
    const crossShard = namespace.get(namespace.idFromName(`maintainer:5678:operation:${hash(cross.operation_id).slice(0, 2)}`));
    const crossResult = { number: 456, url: 'https://github.com/team/shared/pull/456', head_sha: cross.head_sha, base_sha: cross.base_sha };
    assert.equal((await crossShard.fetch('https://journal.internal/operation/mark', { method: 'POST', body: JSON.stringify({ operation: crossOperation, scope, transition: { completed: JSON.stringify(crossResult) } }) })).status, 200);
    assert.equal((await (await call(alice, 'create_pull_request', cross)).json()).result.structuredContent.number, 456, 'typed cross-repository completed replay succeeds while both grants hold');
    sourceVisible = false;
    assert.equal((await call(alice, 'create_pull_request', cross)).status, 401, 'lost source access refuses replay before App authority');
    sourceVisible = true;
    const readArgs = { repository: 'team/shared', branch: 'main' };
    assert.equal((await (await call(alice, 'observe_ref', readArgs)).json()).result.structuredContent.head_sha, 'a'.repeat(40));
    wrongGrant = true;
    const beforeWrongGrant = repositoryReads;
    assert.equal((await (await call(alice, 'observe_ref', readArgs)).json()).result.isError, true);
    assert.equal(repositoryReads, beforeWrongGrant, 'replacement ID refused before private repository read');
    wrongGrant = false; wrongInstallation = true;
    const beforeWrongInstallation = grants.length;
    assert.equal((await (await call(alice, 'observe_ref', readArgs)).json()).result.isError, true);
    assert.equal(grants.length, beforeWrongInstallation, 'changed installation is refused before token mint');
    wrongInstallation = false; replacedPath = true;
    const replaced = await (await call(alice, 'observe_ref', readArgs)).json();
    assert.equal(replaced.result.structuredContent.head_sha, null, 'old numeric token cannot read replacement name');
    replacedPath = false;
    for (const endpoint of ['/user', '/user/installations', '/user/installations/7/repositories']) {
      unavailable = endpoint;
      assert.equal((await call(alice, 'observe_operation', observe)).status, 503, 'GitHub outage is unavailable, never revocation or empty success');
    }
    unavailable = '/user'; unavailableStatus = 403;
    assert.equal((await call(alice, 'observe_operation', observe)).status, 503, 'ambiguous 403 rate limit must not revoke the connection');
    unavailableStatus = 503; unavailable = '';
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
    const awaitingRefresh = await connect('bob', false);
    unavailable = '/login/oauth/access_token';
    assert.equal((await send(`${awaitingRefresh.path}/confirm`, 'POST', { code: awaitingRefresh.code }, awaitingRefresh.credential)).status, 503, 'refresh outage is unavailable and does not activate staged tokens');
    assert.equal((await (await send(awaitingRefresh.path, 'GET', undefined, awaitingRefresh.credential)).json()).state, 'awaiting_confirmation');
    unavailable = '';
    assert.equal((await send(`${awaitingRefresh.path}/confirm`, 'POST', { code: awaitingRefresh.code }, awaitingRefresh.credential)).status, 200, 'confirmation can retry a temporary refresh failure');
  } finally { await mf.dispose(); await rm(persistence, { recursive: true, force: true }); }
});
