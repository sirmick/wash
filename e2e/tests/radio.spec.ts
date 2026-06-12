// Native Radio app (docs/RADIO.md) full-stack e2e, hermetic: a local fake
// Icecast stream stands in for the internet. The curated list renders;
// pasting the fake URL adds a station; tuning it reverse-proxies the
// stream over ingress (200) and now-playing reaches the sidebar.
import { test, expect } from '../fixtures/router';
import { createServer, type Server } from 'node:http';
import type { AddressInfo } from 'node:net';

let server: Server;
let streamUrl: string;

test.beforeAll(async () => {
  server = createServer((req, res) => {
    res.writeHead(200, { 'Content-Type': 'audio/mpeg' });
    res.write(Buffer.alloc(8192));
    const iv = setInterval(() => {
      try {
        res.write(Buffer.alloc(1024));
      } catch {
        clearInterval(iv);
      }
    }, 200);
    req.on('close', () => clearInterval(iv));
  });
  await new Promise<void>((r) => server.listen(0, '127.0.0.1', () => r()));
  streamUrl = `http://127.0.0.1:${(server.address() as AddressInfo).port}/stream`;
});

test.afterAll(() => server?.close());

test.describe('radio app (native player)', () => {
  test.use({ routerOpts: { apps: ['session', 'radio', 'audio'] } });

  test('lists stations, adds a URL, proxies it over ingress to the sidebar', async ({ page, router }) => {
    const streamed = page.waitForResponse((r) => /\/app\/[^/]+\/stream\?i=/.test(r.url()), { timeout: 15_000 });

    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await router.controlRequest({ t: 'launch', app_id: 'com.wash.radio' });

    // The curated list renders.
    const list = page.locator('[data-testid="station-list"]');
    await expect(list).toContainText('SomaFM');

    // Paste the fake stream URL → it's added as a station.
    await page.locator('[data-testid="add-url"]').fill(streamUrl);
    await page.locator('[data-testid="add-station"]').click();
    await expect(list).toContainText('127.0.0.1');

    // Tune the added station (last row) → proxied over ingress.
    const rows = list.locator('[data-testid^="media-row-"]');
    await rows.last().dblclick();
    const resp = await streamed;
    expect(resp.status()).toBe(200);

    // The tuned row is marked playing and now-playing reaches the sidebar.
    await expect(rows.last()).toHaveAttribute('data-playing', 'true');
    const nowPlaying = page.locator('[data-testid="audio-nowplaying"]');
    await expect(nowPlaying).toBeVisible();
    await expect(nowPlaying).toContainText('127.0.0.1');
  });
});
