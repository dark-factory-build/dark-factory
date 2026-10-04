#!/usr/bin/env node
// Run against the isolated Vite fixture, never a daemon or installed factory.
// Uses the same optional Playwright installation as verify-live-browser.mjs.
import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { mkdir, writeFile } from 'node:fs/promises';
import { homedir } from 'node:os';
import { join, resolve } from 'node:path';
import { createRequire } from 'node:module';

const origin = process.env.DARK_FACTORY_FIXTURE_URL || 'http://127.0.0.1:5197';
const address = new URL(origin);
assert.equal(address.hostname, '127.0.0.1', 'only an isolated loopback fixture is permitted');
const require = createRequire(process.env.DARK_FACTORY_PLAYWRIGHT_PACKAGE || join(homedir(), 'dark-factory-site', 'package.json'));
const { chromium, expect } = require('@playwright/test');
const output = resolve(process.env.DARK_FACTORY_VISUAL_OUTPUT || 'output/playwright/inhabited');
await mkdir(output, { recursive: true });
const browser = await chromium.launch({ headless: true });
const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
const page = await context.newPage();
const errors = [], checks = [], screenshots = [];
page.on('pageerror', (error) => errors.push(error.message));
await context.route('**/*', (route) => {
  const url = new URL(route.request().url());
  return ['data:', 'blob:'].includes(url.protocol) || url.origin === address.origin ? route.continue() : route.abort();
});
const check = (name) => { checks.push(name); process.stdout.write(`PASS ${name}\n`); };
const shot = async (name) => { screenshots.push(`${name}.png`); await page.evaluate(() => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done)))); await page.screenshot({ path: join(output, `${name}.png`), fullPage: false }); };
const phase = async (value) => { await page.getByLabel('Proposal observation', { exact: true }).selectOption(value); };
const map = page.getByRole('region', { name: 'Scrollable codebase floor', exact: true });
const positions = () => page.locator('[data-room-id]:not([data-proposed-room])').evaluateAll((rooms) => rooms.map((room) => ({ id: room.getAttribute('data-room-id'), rect: [...room.querySelector('rect').attributes].filter((attribute) => ['x', 'y', 'width', 'height'].includes(attribute.name)).map((attribute) => `${attribute.name}:${attribute.value}`).join(' ') })));
const openEntity = async (path) => { await page.getByRole('searchbox', { name: 'Find source', exact: true }).fill(path); await page.locator('.dfFactoryEntityTools__results').getByRole('button', { name: path, exact: true }).click(); };
try {
  await page.goto(`${origin}/?fixture=inhabited`, { waitUntil: 'networkidle' });
  await expect(map).toBeVisible();
  await expect(page.getByRole('combobox', { name: 'Topology detail', exact: true })).toHaveValue('auto');
  await expect(page.locator('[data-proposed-room]')).toHaveCount(1);
  for (const resource of ['tests', 'documentation', 'configuration']) await expect(page.locator(`[data-proposed-room] [data-associated-equipment="${resource}"]`)).toBeAttached();
  await expect(page.locator('[data-proposed-room] [data-equipment-scale="unknown"]')).toBeAttached();
  for (const status of ['added', 'removed']) await expect(page.locator(`[data-proposed-relationship="${status}"]`)).toBeAttached();
  for (const kind of ['modification', 'addition', 'removal', 'move']) await expect(page.locator(`[data-proposal-kind="${kind}"]`).first()).toBeAttached();
  await expect(page.getByRole('button', { name: /^Morgan · visual review, reviewer,/ })).toHaveCount(1);
  const ordinaryPositions = await positions();
  await shot('01-automatic');
  check('real repository, four operation kinds, new area, one reviewer');

  for (const [path, name] of [['internal/kernel', '14-dark-factory-kernel'], ['internal/daemon', '15-dark-factory-daemon'], ['internal/linear', '16-dark-factory-small-package'], ['web/packages/ui/src', '17-dark-factory-web-ui']]) {
    await openEntity(path);
    await page.getByRole('button', { name: 'Focus on floor', exact: true }).click();
    await shot(name);
  }
  check('actual Dark Factory kernel, daemon, small package and web UI equipment');

  await openEntity('web/packages/ui/src/factory-scene');
  await page.getByRole('button', { name: 'Read exact source contents', exact: true }).click();
  const contents = page.getByRole('region', { name: 'Source contents', exact: true });
  await expect(contents).toContainText('web/packages/ui/src/factory-scene/movement.ts');
  const identity = await contents.locator('code').first().textContent();
  await page.getByRole('button', { name: 'Focus on floor', exact: true }).click();
  await shot('02-mixed-equipment-and-overlap');
  for (const detail of ['coarse', 'fine']) {
    await page.getByRole('combobox', { name: 'Topology detail', exact: true }).selectOption(detail);
    await expect(contents.locator('code').first()).toHaveText(identity);
    await page.getByRole('button', { name: 'Focus on floor', exact: true }).click();
    await shot(`03-${detail}`);
  }
  await page.reload({ waitUntil: 'networkidle' });
  await expect(page.getByRole('combobox', { name: 'Topology detail', exact: true })).toHaveValue('fine');
  await page.getByRole('combobox', { name: 'Topology detail', exact: true }).selectOption('auto');
  check('source inspection, identity across detail changes, persisted detail');

  await page.locator('.dfFactoryProposals').getByRole('button', { name: /^Alternative route clearance/ }).click();
  await expect(page.getByRole('region', { name: 'Production inspection', exact: true })).toContainText('Alternative route clearance');
  await expect(page.locator('[data-proposal-kind="removal"]')).toHaveCount(0);
  await expect(page.locator('[data-proposed-room]')).toHaveCount(0);
  await expect(page.locator('[data-proposed-relationship]')).toHaveCount(0);
  await expect(page.locator('[data-proposal-kind="modification"]')).toHaveCount(1);
  await shot('04-isolated-overlap');
  await page.locator('.dfFactoryProposals').getByRole('button', { name: 'All changes', exact: true }).click();
  assert.deepEqual(await positions(), ordinaryPositions, 'ordinary selection must preserve integrated spatial map');
  check('overlapping proposals remain separate and selection does not move rooms');

  await phase('stale');
  await page.locator('.dfFactoryProposals').getByRole('button', { name: /^Rework routes and add dispatch gates/ }).click();
  await expect(page.getByRole('region', { name: 'Production inspection', exact: true })).toContainText('Review stale');
  await shot('05-stale-evidence');
  await phase('abandoned');
  await expect(page.locator('[data-proposed-room]')).toHaveCount(0);
  await expect(page.locator('[data-proposal-kind="removal"]')).toHaveCount(0);
  await expect(page.locator('[data-proposed-relationship]')).toHaveCount(0);
  await shot('06-abandoned');
  assert.deepEqual(await positions(), ordinaryPositions, 'abandonment must preserve integrated spatial map');
  await phase('integrated');
  await page.getByText('Integrated source · 1 projects', { exact: true }).click();
  await expect(page.locator('.dfFactoryFloor__source')).toContainText('c3'.repeat(20));
  await openEntity('internal/dispatch-gates');
  await page.getByRole('button', { name: 'Read exact source contents', exact: true }).click();
  await expect(page.getByRole('region', { name: 'Source contents', exact: true })).toContainText('internal/dispatch-gates/admission.go');
  await shot('07-integrated-source-contents');
  await page.getByRole('button', { name: 'Focus on floor', exact: true }).click();
  await shot('07-confirmed-integration');
  check('stale evidence, abandonment, and explicit integrated snapshot');

  await phase('observed');
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await page.getByLabel('Population', { exact: true }).selectOption('resting');
  await page.getByRole('combobox', { name: 'Social furniture', exact: true }).selectOption('nearby');
  await expect(page.getByRole('button', { name: /^Builder One, worker, waiting/ })).toHaveCount(1);
  await page.getByRole('button', { name: /^Builder One, worker, waiting/ }).locator(':scope > rect').scrollIntoViewIfNeeded();
  await shot('08-resting-within-base');
  const shelf = page.getByRole('button', { name: 'Open project library', exact: true }).first();
  await shelf.focus(); await page.keyboard.press('Enter');
  const library = page.getByRole('dialog', { name: 'Project library', exact: true });
  await expect(library).toBeVisible();
  await library.getByText('Instructions & outcomes', { exact: true }).click();
  await library.getByRole('button', { name: 'Browse library', exact: true }).click();
  await library.getByRole('button', { name: /Factory source map · procedure/ }).click();
  await library.getByRole('button', { name: 'Read body', exact: true }).click();
  await expect(library).toContainText('Shelf visits are ambient activity');
  await shot('09-bookshelf-library-keyboard');
  await page.keyboard.press('Escape');
  await expect(library).toHaveCount(0);
  check('resting workers, keyboard bookshelf, existing library document read');

  await page.getByLabel('Population', { exact: true }).selectOption('crowded');
  await openEntity('web/packages/ui/src/factory-scene');
  await page.getByRole('button', { name: 'Focus on floor', exact: true }).click();
  await shot('10-crowded');
  const uiRoom = page.locator('[data-room-id]').filter({ has: page.getByRole('button', { name: 'Inspect assembly factory-scene', exact: true }) });
  const remoteLabel = uiRoom.getByRole('button', { name: 'Inspect assembly remote', exact: true }).locator(':scope > text').first();
  await remoteLabel.scrollIntoViewIfNeeded();
  const labelBounds = await remoteLabel.boundingBox();
  assert.ok(labelBounds);
  await page.mouse.click(labelBounds.x + labelBounds.width / 2, labelBounds.y + labelBounds.height / 2);
  await expect(page.getByRole('combobox', { name: 'Inspect room', exact: true }).locator('option:checked')).toContainText('web/packages/ui/src/remote');
  await shot('18-crowded-source-label-click');
  check('crowded source labels receive clicks without worker interception');
  await page.getByRole('button', { name: 'Disconnect fixture', exact: true }).click();
  await expect(page.getByLabel('Connection status: CLOSED', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Focus on floor', exact: true }).click();
  await shot('11-disconnected');
  await page.getByRole('button', { name: 'Reconnect fixture', exact: true }).click();
  await page.getByLabel('Population', { exact: true }).selectOption('empty');
  await expect(page.locator('[data-room-id]')).toHaveCount(0);
  await shot('12-empty-factory');
  check('crowded, disconnected, and empty factory');

  await page.setViewportSize({ width: 390, height: 844 });
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await page.getByLabel('Population', { exact: true }).selectOption('working');
  await page.getByRole('navigation', { name: 'Console views', exact: true }).getByRole('button', { name: 'Floor', exact: true }).click();
  await openEntity('web/packages/ui/src/factory-scene');
  await map.focus(); await page.keyboard.press('ArrowRight');
  await shot('13-mobile-reduced-motion');
  const viewport = await page.evaluate(() => ({ width: innerWidth, scroll: document.documentElement.scrollWidth, outside: [...document.querySelectorAll('body *')].filter((element) => element instanceof HTMLElement && element.getBoundingClientRect().right > innerWidth && element.getBoundingClientRect().width > 100).slice(0, 20).map((element) => ({ tag: element.tagName, class: String(element.className), width: element.getBoundingClientRect().width })) }));
  assert.ok(viewport.scroll <= viewport.width, `mobile document overflow: ${JSON.stringify(viewport)}`);
  check('small screen, keyboard floor access, reduced motion');
  assert.deepEqual(errors, [], 'browser JavaScript errors');
  await writeFile(join(output, 'report.json'), JSON.stringify({ sourceHead: execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim(), trackedChanges: spawnSync('git', ['diff', '--quiet', 'HEAD']).status !== 0, url: `${origin}/?fixture=inhabited`, verifiedAt: new Date().toISOString(), checks, screenshots, browserErrors: errors }, null, 2));
} finally { await context.close(); await browser.close(); }
