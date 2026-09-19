import { lstat, mkdir, mkdtemp, rename } from 'node:fs/promises';
import { homedir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';

async function directory(path) {
  try {
    const entry = await lstat(path);
    if (!entry.isDirectory() || entry.isSymbolicLink()) throw new Error('unsafe browser verification profile path');
    return true;
  } catch (error) {
    if (error?.code === 'ENOENT') return false;
    throw error;
  }
}

async function archive(legacy, home) {
  const target = await mkdtemp(join(home, '.dark-factory-verification-browser-legacy-'));
  await rename(legacy, join(target, 'profile'));
}

export async function verificationProfile(home = homedir()) {
  const profile = join(home, '.dark-factory-verification-browser');
  const runtimeHome = join(home, '.dark-factory');
  const legacy = join(runtimeHome, 'verification-browser');
  const hasRuntimeHome = await directory(runtimeHome);
  const [hasProfile, hasLegacy] = await Promise.all([directory(profile), hasRuntimeHome ? directory(legacy) : false]);
  if (hasLegacy) {
    if (hasProfile) await archive(legacy, home);
    else await rename(legacy, profile);
  }
  await mkdir(profile, { recursive: true, mode: 0o700 });
  return profile;
}

// The local-network-access grant is origin-wide, so the context itself is fenced:
// only the hosted console and the exact loopback listener of SECURITY.md are reachable.
export function insideLoopbackBoundary(url) {
  const { origin, href } = new URL(url);
  return origin === 'https://app.darkfactory.build' || href === 'ws://127.0.0.1:43123/browser';
}

export async function enforceLoopbackBoundary(context, escaped) {
  const outside = (url) => !insideLoopbackBoundary(url);
  await context.route(outside, (route) => route.abort());
  await context.routeWebSocket(outside, (socket) => socket.close());
  // ponytail: routing never sees redirect hops, so a hop that leaves the boundary fails the proof after the fact; fulfil via route.fetch with maxRedirects 0 if that must be blocked.
  context.on('request', (request) => { if (request.redirectedFrom() && outside(request.url())) escaped(); });
}

if (process.argv[1] !== undefined && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try { await verificationProfile(); } catch {
    process.stderr.write('browser verification profile migration failed\n');
    process.exitCode = 1;
  }
}
