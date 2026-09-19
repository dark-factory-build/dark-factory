import assert from 'node:assert/strict';
import test from 'node:test';
import { lstat, mkdir, mkdtemp, readFile, readdir, rm, symlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { enforceLoopbackBoundary, verificationProfile } from './verification-profile.mjs';

async function home(t) {
  const path = await mkdtemp(join(tmpdir(), 'dark-factory-verification-profile-'));
  t.after(() => rm(path, { recursive: true, force: true }));
  return path;
}

test('verification profile preserves fresh, legacy, and colliding profiles', async (t) => {
  const fresh = await home(t);
  const profile = await verificationProfile(fresh);
  assert.equal((await lstat(profile)).isDirectory(), true);

  const legacyOnly = await home(t);
  const legacy = join(legacyOnly, '.dark-factory', 'verification-browser');
  await mkdir(legacy, { recursive: true });
  await writeFile(join(legacy, 'session'), 'keep');
  assert.equal(await verificationProfile(legacyOnly), join(legacyOnly, '.dark-factory-verification-browser'));
  assert.equal(await readFile(join(legacyOnly, '.dark-factory-verification-browser', 'session'), 'utf8'), 'keep');
  await assert.rejects(lstat(legacy), { code: 'ENOENT' });

  const both = await home(t);
  const current = join(both, '.dark-factory-verification-browser');
  const old = join(both, '.dark-factory', 'verification-browser');
  await mkdir(current, { recursive: true });
  await mkdir(old, { recursive: true });
  await writeFile(join(current, 'current'), 'new');
  await writeFile(join(old, 'session'), 'old');
  await verificationProfile(both);
  assert.equal(await readFile(join(current, 'current'), 'utf8'), 'new');
  const archived = (await readdir(both)).find((name) => name.startsWith('.dark-factory-verification-browser-legacy-'));
  assert.ok(archived);
  assert.equal(await readFile(join(both, archived, 'profile', 'session'), 'utf8'), 'old');
  await assert.rejects(lstat(old), { code: 'ENOENT' });
  await verificationProfile(both);
  assert.equal(await readFile(join(current, 'current'), 'utf8'), 'new');
});

test('verification profile rejects a legacy symlink', async (t) => {
  const path = await home(t);
  await mkdir(join(path, '.dark-factory'), { recursive: true });
  await symlink(join(path, 'elsewhere'), join(path, '.dark-factory', 'verification-browser'));
  await assert.rejects(verificationProfile(path), /unsafe browser verification profile path/);
});

test('live browser context reaches only the hosted console and the exact loopback listener', async () => {
  const routes = [];
  let onRequest;
  const context = {
    route: async (matches, handler) => routes.push([matches, handler, 'abort']),
    routeWebSocket: async (matches, handler) => routes.push([matches, handler, 'close']),
    on: (event, handler) => { assert.equal(event, 'request'); onRequest = handler; },
  };
  let escapes = 0;
  await enforceLoopbackBoundary(context, () => { escapes += 1; });
  assert.equal(routes.length, 2);
  const inside = ['https://app.darkfactory.build/factory', 'ws://127.0.0.1:43123/browser'];
  const outside = [
    'http://127.0.0.1:43123/pair', 'ws://127.0.0.1:43124/browser', 'ws://127.0.0.1:43123/other', 'ws://localhost:43123/browser',
    'ws://192.168.1.1/browser', 'http://10.0.0.1/', 'http://[::1]:43123/', 'https://app.darkfactory.build.example/', 'http://app.darkfactory.build/',
  ];
  for (const [matches, handler, verb] of routes) {
    for (const url of inside) assert.equal(matches(new URL(url)), false, url);
    for (const url of outside) assert.equal(matches(new URL(url)), true, url);
    let blocked = 0;
    handler({ [verb]: () => { blocked += 1; } });
    assert.equal(blocked, 1);
  }
  onRequest({ redirectedFrom: () => null, url: () => outside[4] });
  onRequest({ redirectedFrom: () => ({}), url: () => inside[0] });
  assert.equal(escapes, 0);
  onRequest({ redirectedFrom: () => ({}), url: () => outside[4] });
  assert.equal(escapes, 1);

  const script = await readFile(new URL('./verify-live-browser.mjs', import.meta.url), 'utf8');
  assert.ok(script.indexOf('enforceLoopbackBoundary(context') < script.indexOf('grantPermissions('), 'boundary precedes the grant');
  assert.ok(script.indexOf('clearPermissions()') < script.indexOf('context?.close()'), 'grant is cleared before close');
});
