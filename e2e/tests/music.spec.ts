// M1 full-stack music e2e (docs/AUDIO.md §5): the Winamp-skinned player
// driven in kiosk mode — no shell, no sidebar. Proves the Case-1 audio
// pipeline end to end:
//   1. the wash-music window mounts and Webamp renders (the JS classic
//      Winamp skin engine paints its main window),
//   2. the FE↔BE `tracks` round-trip drives the playlist (our synthesized
//      sample track appears), and
//   3. the audio is actually reachable over the ingress reverse-proxy —
//      a GET for sample.wav returns 200/206 (Range), proving the BE file
//      server + PublishIngress + router proxy chain works.

import { test, expect } from '../fixtures/router';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

// minimalWav builds a valid tiny 16-bit mono PCM WAV (silence) so webamp
// can parse a real audio file off the seeded library dir.
function minimalWav(): Buffer {
  const rate = 8000;
  const n = rate / 10; // 0.1s
  const dataLen = n * 2;
  const buf = Buffer.alloc(44 + dataLen);
  buf.write('RIFF', 0);
  buf.writeUInt32LE(36 + dataLen, 4);
  buf.write('WAVE', 8);
  buf.write('fmt ', 12);
  buf.writeUInt32LE(16, 16);
  buf.writeUInt16LE(1, 20); // PCM
  buf.writeUInt16LE(1, 22); // mono
  buf.writeUInt32LE(rate, 24);
  buf.writeUInt32LE(rate * 2, 28);
  buf.writeUInt16LE(2, 32);
  buf.writeUInt16LE(16, 34);
  buf.write('data', 36);
  buf.writeUInt32LE(dataLen, 40);
  return buf; // samples left as zero (silence)
}

test.describe('music app (kiosk, host-side full stack)', () => {
  test.use({ routerOpts: { kiosk: 'com.wash.music', apps: ['music'] } });

  test('renders Webamp and serves the track over ingress', async ({ page, router }) => {
    // Catch the ingress fetch for the sample track. Register the waiter
    // before navigation so we never race the fetch webamp kicks off once
    // the playlist loads.
    const audio = page.waitForResponse(
      (r) => /\/app\/[^/]+\/sample\.wav/.test(r.url()),
      { timeout: 20_000 },
    );

    await page.goto(router.url);

    // The window mounts.
    await expect(page.locator('wash-app-music')).toBeVisible();

    // Webamp painted — a real, sized control (the volume slider) is
    // visible. The #webamp root itself is 0×0 (its windows are absolutely
    // positioned), so we assert on a control with a bounding box.
    await expect(page.getByRole('slider', { name: 'Volume Bar' })).toBeVisible();

    // The FE↔BE `tracks` round-trip drove the playlist with our sample
    // (served by the BE; "0:03" is the duration webamp read from the WAV).
    await expect(page.locator('#webamp')).toContainText('Wash Test Tone');

    // Audio is reachable over the ingress proxy — webamp fetched the WAV
    // (for metadata) at /app/<token>/sample.wav and got a 200/206 (Range).
    const resp = await audio;
    expect([200, 206]).toContain(resp.status());
  });
});

// M3: the control plane (com.wash.audio) + sidebar AudioWidget. Full
// shell this time. Launching the music window makes its FE register with
// the audio service; the service pushes state through the session gateway
// to the sidebar; and a transport command from the sidebar round-trips
// back to webamp. Proves the whole producer → service → sidebar → producer
// loop (docs/AUDIO.md §3).
test.describe('music + audio control plane (full shell + sidebar)', () => {
  test.use({ routerOpts: { apps: ['session', 'music', 'audio'] } });

  test('now-playing reaches the sidebar; transport round-trips to webamp', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    // Launch the Winamp window. Its FE renders webamp and registers with
    // com.wash.audio (which auto-spawned as a background singleton).
    await router.controlRequest({ t: 'launch', app_id: 'com.wash.music' });
    await expect(page.getByRole('slider', { name: 'Volume Bar' })).toBeVisible();

    // The source propagates music → audio → session gateway → sidebar.
    // A new source auto-expands the Audio section, so the widget shows up.
    const nowPlaying = page.locator('[data-testid="audio-nowplaying"]');
    await expect(nowPlaying).toBeVisible();
    await expect(nowPlaying).toContainText('Wash Test Tone');

    // Press Play in the SIDEBAR (not in webamp). The command relays
    // session → audio → music → webamp.play(); webamp's status flips to
    // playing and reports back up, swapping the button to Pause.
    await page.locator('[data-testid="audio-play"]').click();
    await expect(page.locator('[data-testid="audio-pause"]')).toBeVisible();
    await expect(nowPlaying).toHaveAttribute('data-status', 'playing');
  });
});

// Chromeless window (docs/AUDIO.md §2): the music window drops the wash
// titlebar/border so Webamp's own Winamp titlebar is the only titlebar
// (no double chrome) and sits flush (no black margin). Because Webamp
// renders its UI as a <body> overlay, the FE reparents #webamp into the
// window slot and drives move/close/minimize from Winamp's native chrome
// via window.wash — this proves that wiring.
test.describe('music chromeless window chrome', () => {
  test.use({ routerOpts: { apps: ['session', 'music', 'audio'] } });

  test('no wash titlebar; Winamp titlebar drives move/close/minimize', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await router.controlRequest({ t: 'launch', app_id: 'com.wash.music' });
    await expect(page.getByRole('slider', { name: 'Volume Bar' })).toBeVisible();

    // The frame hosting wash-app-music carries no wash titlebar, and
    // Webamp's root is reparented inside the host (not a body overlay).
    const frame = page.locator('.wash-window', { has: page.locator('wash-app-music') });
    await expect(frame.locator('.wash-titlebar')).toHaveCount(0);
    expect(
      await page.evaluate(() =>
        document.querySelector('wash-app-music')!.contains(document.querySelector('#webamp')),
      ),
    ).toBe(true);

    const winID = await page.evaluate(
      () => window.wash.windows().find((x) => x.element === 'wash-app-music')!.windowID,
    );
    const pos = (id: number) =>
      page.evaluate((i) => {
        const w = window.wash.windows().find((x) => x.windowID === i)!;
        return { x: w.x, y: w.y, state: w.state };
      }, id);

    // Dragging Webamp's main-window titlebar moves the wash window (not
    // just the Winamp window inside it).
    const before = await pos(winID);
    const box = (await page.locator('#title-bar').boundingBox())!;
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await page.mouse.down();
    await page.mouse.move(box.x + box.width / 2 + 120, box.y + box.height / 2 + 90, { steps: 8 });
    await page.mouse.up();
    await expect.poll(() => pos(winID).then((p) => ({ x: p.x, y: p.y }))).toEqual({
      x: before.x + 120,
      y: before.y + 90,
    });

    // Winamp minimize minimizes the wash window; the UI survives.
    await page.locator('#minimize').click();
    await expect.poll(() => pos(winID).then((p) => p.state)).toBe('minimized');

    // Restore, then Winamp close closes the wash window.
    await page.evaluate((id) => window.wash.restoreWindow(id), winID);
    await expect(page.getByRole('slider', { name: 'Volume Bar' })).toBeVisible();
    await page.locator('#close').click();
    await expect(page.locator('wash-app-music')).toHaveCount(0);
  });
});

// M2: the library scan. Point WASH_MUSIC_DIR at a seeded folder and the
// BE serves those files over ingress; the FE builds the webamp playlist
// from them (docs/AUDIO.md §5). Filenames with spaces exercise the
// per-segment URL escaping.
test.describe('music app library scan', () => {
  const dir = mkdtempSync(join(tmpdir(), 'wash-music-lib-'));
  writeFileSync(join(dir, 'Alpha Song.wav'), minimalWav());
  writeFileSync(join(dir, 'Beta Tune.wav'), minimalWav());

  test.use({ routerOpts: { kiosk: 'com.wash.music', apps: ['music'], extraEnv: { WASH_MUSIC_DIR: dir } } });

  test('serves the seeded folder as the playlist', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.getByRole('slider', { name: 'Volume Bar' })).toBeVisible();

    // Both scanned files appear in the playlist (titles = filename stems),
    // and the sample-tone fallback does NOT (a real library took over).
    await expect(page.locator('#webamp')).toContainText('Alpha Song');
    await expect(page.locator('#webamp')).toContainText('Beta Tune');
    await expect(page.locator('#webamp')).not.toContainText('Wash Test Tone');
  });
});
