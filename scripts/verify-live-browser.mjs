#!/usr/bin/env node
// Operator-owned browser profile: no user browser session or stored login is touched.
import { homedir } from 'node:os';
import { join } from 'node:path';
import { createRequire } from 'node:module';
import { enforceLoopbackBoundary, verificationProfile } from './verification-profile.mjs';

const require = createRequire(join(process.env.DARK_FACTORY_SITE || join(homedir(), 'dark-factory-site'), 'package.json'));
const { chromium, expect } = require('@playwright/test');
let context;
let stage = 'launch';
try {
  const profile = await verificationProfile();
  context = await chromium.launchPersistentContext(profile, { headless: true, viewport: { width: 1440, height: 900 } });
  let pageFailed = false;
  await enforceLoopbackBoundary(context, () => { pageFailed = true; });
  await context.grantPermissions(['local-network-access'], { origin: 'https://app.darkfactory.build' });
  const page = await context.newPage();
  page.on('pageerror', () => { pageFailed = true; });
  stage = 'navigation';
  await page.goto('https://app.darkfactory.build/factory', { waitUntil: 'domcontentloaded' });
  const consoleView = page.getByRole('group', { name: 'Left view', exact: true });
  const floorButton = consoleView.getByRole('button', { name: 'Floor', exact: true });
  stage = 'readiness';
  await expect(floorButton).toBeEnabled({ timeout: 45000 });
  stage = 'console';
  await consoleView.waitFor({ state: 'visible', timeout: 45000 });
  await expect(floorButton).toBeEnabled({ timeout: 45000 });
  stage = 'settings button';
  await page.locator('header').getByRole('button', { name: 'Settings', exact: true }).click();
  stage = 'accounts';
  await page.getByRole('dialog', { name: 'Settings', exact: true }).getByRole('button', { name: 'REFRESH ACCOUNTS', exact: true }).waitFor({ state: 'visible' });
  if (pageFailed) throw new Error('browser error');
  process.stdout.write(JSON.stringify({ healthy: true }) + '\n');
} catch {
  // Browser errors can contain pairing URLs: expose only a finite result.
  process.stderr.write(`live browser verification failed at ${stage}\n`);
  process.exitCode = 1;
} finally {
  try {
    // The grant must not outlive this run in the persistent profile.
    try { await context?.clearPermissions(); } finally { await context?.close(); }
  } catch {
    process.stderr.write('live browser verification failed at cleanup\n');
    process.exitCode = 1;
  }
}
