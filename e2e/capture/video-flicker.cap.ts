// Video flicker probe — a busy guest (one that repaints every frame, e.g. an
// emulator at 60-70 fps) must not make its window flash transparent.
//
// The bug this guards: the video channel's frames outran the 64 KiB FE credit
// window, the router marked the channel "behind", and every channel.resync
// made <wash-app-display> clear its canvas → the desktop showed through the
// window several times a second. Fixed by dropping video frames on a
// would-block (router) and keeping the last frame on resync (FE).
//
// Manual probe (needs the real compositor + a continuously-animating guest):
//   cd e2e && WASH_E2E_MULTICALL=1 \
//     WASH_FLICKER_GUEST='retroarch -L …dosbox_pure_libretro.so game.dosz' \
//     WASH_FLICKER_CPU=4 \
//     npx playwright test -c playwright.screenshots.config.ts capture/video-flicker.cap.ts
//
// WASH_FLICKER_LATENCY_MS puts a TCP proxy between browser and router that
// delays every chunk by half that each way — a remote browser: credit comes
// back one round trip after the bytes left, so more than ~64 KiB per RTT
// exhausts the window. (CDP network emulation doesn't delay WebSocket frames.)
// WASH_FLICKER_CPU throttles the page's CPU (a slow browser).
// Results: <repo-root>/screenshots/video-flicker.*

import { test, expect, displaySkipReason } from '../fixtures/router';
import type { Page, Locator } from '@playwright/test';
import { mkdirSync, writeFileSync } from 'node:fs';
import { createServer, connect, type Socket } from 'node:net';
import { join, dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const SHOTS = resolve(__dirname, '..', '..', 'screenshots');
mkdirSync(SHOTS, { recursive: true });

const GUEST = process.env.WASH_FLICKER_GUEST ?? '';
const CPU = Number(process.env.WASH_FLICKER_CPU ?? '1');
const SAMPLE_MS = Number(process.env.WASH_FLICKER_MS ?? '10000');
const LATENCY_MS = Number(process.env.WASH_FLICKER_LATENCY_MS ?? '0');

test.use({ routerOpts: { apps: ['session', 'term', 'display'], showHidden: true } });

function win(page: Page, el: string): Locator {
  return page.locator('.wash-window', { has: page.locator(el) });
}

// latencyProxy forwards 127.0.0.1:<new port> → the router, holding every chunk
// for `oneWayMs` in each direction (constant delay keeps order). Returns the
// proxied URL; sockets die with the test process.
async function latencyProxy(target: string, oneWayMs: number): Promise<string> {
  const t = new URL(target);
  const pipe = (from: Socket, to: Socket) =>
    from.on('data', (chunk) => setTimeout(() => to.write(chunk), oneWayMs)).on('close', () => to.destroy());
  const server = createServer((client) => {
    const up = connect(Number(t.port), t.hostname);
    pipe(client, up);
    pipe(up, client);
    client.on('error', () => up.destroy());
    up.on('error', () => client.destroy());
  });
  await new Promise<void>((ok) => server.listen(0, '127.0.0.1', ok));
  const addr = server.address();
  if (addr === null || typeof addr === 'string') throw new Error('latency proxy: no port');
  return `${t.protocol}//127.0.0.1:${addr.port}${t.pathname}${t.search}`;
}

// Sample the display canvas on every animation frame for `ms`; a sample is
// "transparent" when most of the canvas has alpha 0 — what a cleared canvas
// looks like between a resync and the next frame.
async function sampleTransparency(el: Locator, ms: number) {
  return el.evaluate((node, dur) => new Promise<{ samples: number; transparent: number; worstPct: number }>((done) => {
    const root = (node as HTMLElement).shadowRoot ?? node;
    const c = (root.querySelector('canvas') ?? (node as HTMLElement).querySelector('canvas')) as HTMLCanvasElement;
    const ctx = c.getContext('2d')!;
    let samples = 0, transparent = 0, worstPct = 0;
    const end = performance.now() + dur;
    const tick = () => {
      const { width: w, height: h } = c;
      if (w && h) {
        const d = ctx.getImageData(0, 0, w, h).data;
        let clear = 0, n = 0;
        for (let i = 3; i < d.length; i += 4 * 97) { n++; if (d[i] === 0) clear++; }
        const pct = n ? (clear * 100) / n : 0;
        samples++;
        if (pct > 50) transparent++;
        if (pct > worstPct) worstPct = pct;
      }
      if (performance.now() < end) requestAnimationFrame(tick);
      else done({ samples, transparent, worstPct: Math.round(worstPct) });
    };
    requestAnimationFrame(tick);
  }), ms);
}

test('video flicker — busy guest never flashes transparent', async ({ page, router }) => {
  const reason = displaySkipReason();
  test.skip(reason !== null, reason ?? '');
  test.skip(!GUEST, 'set WASH_FLICKER_GUEST to a continuously-animating guest command');
  test.setTimeout(120_000);

  const url = LATENCY_MS > 0 ? await latencyProxy(router.url, LATENCY_MS / 2) : router.url;
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  await router.waitForLog(/Xwayland ready on DISPLAY=/, 25_000);
  await router.waitForLog(/env\.publish from /, 25_000);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu-com.wash.term"]').click();
  await expect(win(page, 'wash-app-term')).toBeVisible();
  await page.waitForSelector('wash-app-term .xterm-rows', { timeout: 10_000 });
  await page.locator('wash-app-term').click();
  await page.keyboard.type(`${GUEST}\n`, { delay: 2 });

  await router.waitForLog(/window\.create .*element="wash-app-display"/, 30_000);
  const display = win(page, 'wash-app-display').first();
  await expect(display).toBeVisible({ timeout: 20_000 });
  if (CPU > 1) {
    const cdp = await page.context().newCDPSession(page);
    await cdp.send('Emulation.setCPUThrottlingRate', { rate: CPU });
  }
  await page.waitForTimeout(4000); // let the guest reach steady-state animation
  const before = router.log().length;
  const stats = await sampleTransparency(display, SAMPLE_MS);
  const log = router.log().slice(before);
  const count = (re: RegExp) => (log.match(re) ?? []).length;
  const result = {
    cpuThrottle: CPU,
    latencyMs: LATENCY_MS,
    ...stats,
    feBehind: count(/FE behind/g),
    resyncs: count(/resync complete/g),
    videoDropLines: count(/video dropped/g),
    mapped: (router.log().match(/wash-display: mapped .*/g) ?? []).slice(-1)[0] ?? '',
  };
  await display.screenshot({ path: join(SHOTS, 'video-flicker.win.png') });
  writeFileSync(join(SHOTS, 'video-flicker.json'), JSON.stringify(result, null, 2) + '\n');
  writeFileSync(join(SHOTS, 'video-flicker.log'), log);
  console.log('[video-flicker]', JSON.stringify(result));

  expect(stats.samples).toBeGreaterThan(0);
  expect(result.transparent, 'canvas samples that were mostly transparent').toBe(0);
});
