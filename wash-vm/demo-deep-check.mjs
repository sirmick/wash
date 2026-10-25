// Deep check for the in-browser demo: goes past browser-vm-smoke.mjs (which
// stops at the login form) — logs in as wash/wash, waits for the desktop, opens
// every app in the start menu, and reports which ones render a window, plus
// page errors and missing assets. Point it at a dev server or the live site:
//
//   DEMO_URL=https://sirmick.github.io/wash/demo/ node wash-vm/demo-deep-check.mjs
//
// SHOTS=<dir> also saves a screenshot per app (handy for eyeballing).
import pw from '../e2e/node_modules/@playwright/test/index.js';
import { mkdirSync } from 'node:fs';
import { join } from 'node:path';
const { chromium } = pw;

const URL = process.env.DEMO_URL || 'http://localhost:12000/';
const BOOT_TIMEOUT = Number(process.env.BOOT_TIMEOUT || 240000);
const SHOTS = process.env.SHOTS || '';
if (SHOTS) mkdirSync(SHOTS, { recursive: true });

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
const errors = [];
const missing = [];
page.on('pageerror', (e) => errors.push(e.message));
page.on('response', (r) => {
  if (r.status() >= 400) missing.push(`${r.status()} ${new globalThis.URL(r.url()).pathname}`);
});
const t0 = Date.now();
const secs = () => ((Date.now() - t0) / 1000).toFixed(1) + 's';
const fail = async (msg) => {
  console.error(`demo-deep-check: FAIL — ${msg}`);
  if (SHOTS) await page.screenshot({ path: join(SHOTS, 'fail.png') }).catch(() => {});
  report();
  await browser.close();
  process.exit(1);
};
function report() {
  if (errors.length) console.error('page errors:', JSON.stringify([...new Set(errors)].slice(0, 15), null, 1));
  if (missing.length) console.error('http errors:', JSON.stringify([...new Set(missing)], null, 1));
}

await page.goto(URL, { waitUntil: 'domcontentloaded', timeout: 60000 });
await page.locator('#view-wash').click({ timeout: 30000 }).catch(() => {});

// Boot → login form (or straight to a mounted desktop).
const deadline = Date.now() + BOOT_TIMEOUT;
let state = '';
while (Date.now() < deadline) {
  state = await page.evaluate(() => {
    const l = document.querySelector('#washlogin');
    if (l && getComputedStyle(l).display !== 'none') return 'login';
    if (document.querySelector('button[title="Apps"]')) return 'desktop';
    return '';
  }).catch(() => '');
  if (state) break;
  await page.waitForTimeout(1000);
}
if (!state) await fail('never reached the login form or the desktop');
console.log(`reached ${state} at ${secs()}`);

if (state === 'login') {
  await page.fill('#washlogin-user', 'wash');
  await page.fill('#washlogin-pass', 'wash');
  await page.click('#washlogin-go');
  try {
    await page.locator('button[title="Apps"]').waitFor({ timeout: 120000 });
  } catch {
    const err = await page.locator('#washlogin-err').textContent().catch(() => '');
    await fail(`login did not mount the desktop (err: ${JSON.stringify(err)})`);
  }
  console.log(`desktop mounted at ${secs()}`);
}
await page.waitForTimeout(3000);
if (SHOTS) await page.screenshot({ path: join(SHOTS, '00-desktop.png') });

// Enumerate the start menu, then open each app in turn.
await page.locator('button[title="Apps"]').click();
const ids = await page.locator('[data-testid^="start-menu-com."]').evaluateAll(
  (els) => els.map((e) => e.getAttribute('data-testid').slice('start-menu-'.length)));
await page.keyboard.press('Escape');
console.log(`start menu: ${ids.length} apps — ${ids.join(' ')}`);
if (!ids.length) await fail('start menu is empty');

// APPS=term,top limits (and orders) the run; WIN_TIMEOUT is per app, in ms.
const only = (process.env.APPS || '').split(',').filter(Boolean).map((a) => a.includes('.') ? a : `com.wash.${a}`);
const WIN_TIMEOUT = Number(process.env.WIN_TIMEOUT || 45000);
const results = [];
for (const id of only.length ? only : ids) {
  const name = id.replace(/^com\.wash\./, '');
  const w = page.locator('.wash-window', { has: page.locator(`wash-app-${name}`) });
  const errsBefore = errors.length;
  const tApp = Date.now();
  await page.locator('button[title="Apps"]').click();
  await page.locator(`[data-testid="start-menu-${id}"]`).click();
  let ok = false;
  for (let i = 0; i < WIN_TIMEOUT / 500 && !ok; i++) {
    ok = (await w.count()) > 0;
    if (!ok) await page.waitForTimeout(500);
  }
  const took = ((Date.now() - tApp) / 1000).toFixed(1);
  await page.waitForTimeout(ok ? 2500 : 0);
  const newErrs = errors.slice(errsBefore);
  if (SHOTS) await page.screenshot({ path: join(SHOTS, `${name}.png`) });
  results.push({ id, window: ok, errors: newErrs.length });
  console.log(`${ok ? 'ok  ' : 'NONE'} ${id} ${took}s${newErrs.length ? `  (${newErrs.length} page errors)` : ''}`);
  // Close it again so windows don't pile up (Escape the menu first if open).
  await page.keyboard.press('Escape');
  if (ok) {
    await w.locator('[data-testid="window-close"]').first().click({ timeout: 3000 }).catch(() => {});
    await w.waitFor({ state: 'detached', timeout: 15000 }).catch(() => console.log(`     ${id}: window did not close`));
  }
}

// DUMP_LOGS=1 prints the tail of the kernel console and the wash log (the
// guest's dmesg is where an OOM kill shows up).
if (process.env.DUMP_LOGS) {
  for (const [btn, host] of [['#view-kernel', '#term_container'], ['#view-washlog', '#washlog_host']]) {
    await page.locator(btn).click().catch(() => {});
    await page.waitForTimeout(1500);
    const text = await page.locator(host).innerText().catch((e) => `(${e.message})`);
    console.log(`\n--- ${btn} (tail) ---\n${text.split('\n').slice(-60).join('\n')}`);
  }
}

const bad = results.filter((r) => !r.window);
console.log(`\n${results.length - bad.length}/${results.length} apps opened a window; total ${secs()}`);
report();
await browser.close();
process.exit(bad.length || errors.length ? 1 : 0);
