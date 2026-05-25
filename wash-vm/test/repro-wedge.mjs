// Reproduce the wash-router hvc2 wedge in Playwright.
// Launches a chromium window pointed at the demo, leaves it running
// long enough to watch heartbeat behavior in the server log, exits.
// Usage: node /tmp/repro-wedge.mjs [seconds]   (default 180)
import { chromium } from '/home/mick/wash/e2e/node_modules/@playwright/test/index.mjs';

const TARGET = 'http://localhost:5180/tinyemu.html';
const seconds = Number(process.argv[2] || 180);

const headless = process.env.HEADLESS !== 'false';
console.log(`[repro] launching chromium headless=${headless} → ${TARGET} for ${seconds}s`);
const browser = await chromium.launch({
  headless,
  args: ['--no-sandbox', '--disable-dev-shm-usage'],
});
const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
const page = await ctx.newPage();

page.on('console', (msg) => {
  const t = msg.type();
  if (t === 'error' || t === 'warning') {
    console.log(`[page-${t}]`, msg.text().slice(0, 200));
  }
});
page.on('pageerror', (e) => console.log('[page-error]', String(e).slice(0, 200)));

const t0 = Date.now();
await page.goto(TARGET, { waitUntil: 'domcontentloaded' });
console.log(`[repro] page loaded in ${Date.now() - t0}ms`);

// Keep the page alive while the VM boots and exercises wash-router.
// Print a wallclock tick every 5s so we can correlate with server log.
const ticker = setInterval(() => {
  console.log(`[repro] alive @+${Math.round((Date.now() - t0) / 1000)}s`);
}, 5000);

await new Promise((r) => setTimeout(r, seconds * 1000));
clearInterval(ticker);
await browser.close();
console.log('[repro] done');
