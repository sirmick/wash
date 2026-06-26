// Native Radio app (docs/RADIO.md) full-stack e2e, hermetic: a local fake
// Icecast stream stands in for the internet. The curated list renders;
// pasting the fake URL adds a station; tuning it reverse-proxies the
// stream over ingress (200) and now-playing reaches the sidebar.
import { test, expect } from '../fixtures/router';
import { createServer, type Server } from 'node:http';
import type { AddressInfo } from 'node:net';

let server: Server;
let streamUrl: string;

// Fake Icecast: when the proxy asks for ICY metadata, interleave a
// StreamTitle block every `metaint` audio bytes.
test.beforeAll(async () => {
  const metaint = 64;
  const title = "StreamTitle='Now Playing Track';";
  const padLen = Math.ceil(title.length / 16) * 16;
  const metaBuf = Buffer.alloc(padLen);
  metaBuf.write(title);
  const lenByte = Buffer.from([padLen / 16]);
  server = createServer((req, res) => {
    const icy = req.headers['icy-metadata'] === '1';
    const headers: Record<string, string> = { 'Content-Type': 'audio/mpeg' };
    if (icy) headers['icy-metaint'] = String(metaint);
    res.writeHead(200, headers);
    const block = () => {
      res.write(Buffer.alloc(metaint));
      if (icy) {
        res.write(lenByte);
        res.write(metaBuf);
      }
    };
    block();
    const iv = setInterval(() => {
      try {
        block();
      } catch {
        clearInterval(iv);
      }
    }, 80);
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
    await expect(page.locator('[data-testid="genre-electronic"]')).toBeVisible();
    await expect(page.locator('[data-testid="subtype-electronic-downtempo-chill"]')).toBeVisible();
    await expect(page.locator('[data-testid="subtype-rock-nu-metal"]')).toBeVisible();

    // Paste the fake stream URL → it's added as a station.
    await page.locator('[data-testid="add-url"]').fill(streamUrl);
    await page.locator('[data-testid="add-station"]').click();
    await expect(list).toContainText('127.0.0.1');

    // Tune the added station → proxied over ingress.
    const rows = list.locator('[data-testid^="media-row-"]');
    const addedRow = rows.filter({ hasText: '127.0.0.1' });
    await addedRow.dblclick();
    const resp = await streamed;
    expect(resp.status()).toBe(200);

    // The tuned row is marked playing.
    await expect(addedRow).toHaveAttribute('data-playing', 'true');

    // The live ICY StreamTitle propagates to the app + the sidebar.
    await expect(page.locator('[data-testid="radio-nowplaying"]')).toContainText('Now Playing Track');
    const nowPlaying = page.locator('[data-testid="audio-nowplaying"]');
    await expect(nowPlaying).toBeVisible();
    await expect(nowPlaying).toContainText('Now Playing Track');
  });

  test('persists pasted stations + favorites across reload; shows offline', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await router.controlRequest({ t: 'launch', app_id: 'com.wash.radio' });
    const list = page.locator('[data-testid="station-list"]');
    await expect(list).toContainText('SomaFM');

    // Paste a custom station + favorite the first curated one.
    await page.locator('[data-testid="add-url"]').fill('http://example.invalid/x');
    await page.locator('[data-testid="add-station"]').click();
    await expect(list).toContainText('example.invalid');
    await page.locator('[data-testid="fav-0"]').click();
    await expect(page.locator('[data-testid="fav-0"]')).toHaveAttribute('data-fav', 'true');

    // Reload → window remounts, state restored from app_state.
    await page.reload();
    await expect(page.locator('wash-app-session')).toBeVisible();
    await expect(list).toContainText('example.invalid'); // pasted station persisted
    expect(await list.getByText('example.invalid').count()).toBe(1); // not duplicated
    await expect(page.locator('[data-testid="fav-0"]')).toHaveAttribute('data-fav', 'true'); // favorite persisted

    // Tuning an unreachable station surfaces an offline state (not silence).
    await page.locator('[data-testid="add-url"]').fill('http://127.0.0.1:1/dead');
    await page.locator('[data-testid="add-station"]').click();
    const deadRow = list.locator('[data-testid^="media-row-"]').filter({ hasText: '127.0.0.1:1' });
    await deadRow.dblclick();
    await expect(page.locator('[data-testid="radio-nowplaying"]')).toContainText('offline', { timeout: 10_000 });
  });
});
