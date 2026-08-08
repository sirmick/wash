// Headless boot smoke for the in-browser WASM VM (wash-vm, TinyEMU RISC-V).
// Loads the dev server, switches to the wash view, and asserts the wash desktop
// actually mounts (#root populated, or the login form shown) — the regression
// guard for the gzip-bundle bootstrap break (shell-bootstrap.ts). Run via
// `make browser-vm-test`, which starts the dev server and points SMOKE_URL here.
//
// Resolves @playwright/test from e2e/node_modules via a path relative to THIS
// file (ESM resolves specifiers relative to the importer, not cwd).
// @playwright/test is CommonJS, so default-import then destructure.
import pw from '../e2e/node_modules/@playwright/test/index.js';
const { chromium } = pw;

const URL = process.env.SMOKE_URL || 'http://localhost:12060';
const BOOT_TIMEOUT = Number(process.env.BOOT_TIMEOUT || 240000);

const browser = await chromium.launch();
const page = await browser.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push('pageerror: ' + e.message));

let reached = '';
try {
  await page.goto(URL, { waitUntil: 'domcontentloaded', timeout: 30000 });
  await page.locator('#view-wash').click({ timeout: 30000 }).catch(() => {});
  const deadline = Date.now() + BOOT_TIMEOUT;
  while (Date.now() < deadline) {
    const s = await page.evaluate(() => ({
      login: !!document.querySelector('#washlogin-form') &&
        getComputedStyle(document.querySelector('#washlogin') || document.body).display !== 'none',
      root: (document.querySelector('#root')?.childElementCount || 0) > 0,
    }));
    if (s.login) { reached = 'login-form'; break; }
    if (s.root) { reached = 'desktop-mounted'; break; }
    await page.waitForTimeout(2000);
  }
} catch (e) {
  errors.push('fatal: ' + (e?.message || e));
}

let bootLog = [];
try { bootLog = await page.evaluate(() => (window.__washBootLog || []).slice(-4)); } catch { /* page gone */ }
await browser.close();

if (reached) {
  console.log(`browser-vm-smoke: PASS (${reached})`);
  process.exit(0);
}
console.error('browser-vm-smoke: FAIL — desktop never mounted (no login form, #root empty)');
console.error('last boot marks:', JSON.stringify(bootLog));
if (errors.length) console.error('page errors:', JSON.stringify(errors.slice(0, 10)));
process.exit(1);
