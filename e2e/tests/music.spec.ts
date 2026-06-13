// Native Music app (docs/MUSIC.md) full-stack e2e: a seeded folder is
// scanned over the BE, the track list renders, double-clicking a track
// streams it over ingress, and now-playing reaches the sidebar via
// com.wash.audio.
import { test, expect } from '../fixtures/router';
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

function minimalWav(): Buffer {
  const rate = 8000;
  const n = rate / 5; // 0.2s
  const dataLen = n * 2;
  const buf = Buffer.alloc(44 + dataLen);
  buf.write('RIFF', 0);
  buf.writeUInt32LE(36 + dataLen, 4);
  buf.write('WAVE', 8);
  buf.write('fmt ', 12);
  buf.writeUInt32LE(16, 16);
  buf.writeUInt16LE(1, 20);
  buf.writeUInt16LE(1, 22);
  buf.writeUInt32LE(rate, 24);
  buf.writeUInt32LE(rate * 2, 28);
  buf.writeUInt16LE(2, 32);
  buf.writeUInt16LE(16, 34);
  buf.write('data', 36);
  buf.writeUInt32LE(dataLen, 40);
  return buf;
}

test.describe('music app (native player)', () => {
  const dir = mkdtempSync(join(tmpdir(), 'wash-music-native-'));
  writeFileSync(join(dir, 'Alpha Song.wav'), minimalWav());
  writeFileSync(join(dir, 'Beta Tune.wav'), minimalWav());

  test.use({ routerOpts: { apps: ['session', 'music', 'audio'], extraEnv: { WASH_MUSIC_DIR: dir } } });

  test('scans a folder, plays over ingress, reaches the sidebar', async ({ page, router }) => {
    const audio = page.waitForResponse(
      (r) => /\/app\/[^/]+\/lib\/Alpha%20Song\.wav/.test(r.url()),
      { timeout: 20_000 },
    );

    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await router.controlRequest({ t: 'launch', app_id: 'com.wash.music' });

    // The scanned folder renders as the track list.
    const list = page.locator('[data-testid="track-list"]');
    await expect(list).toBeVisible();
    await expect(list).toContainText('Alpha Song');
    await expect(list).toContainText('Beta Tune');

    // Double-click the first track → it plays and streams over ingress.
    await page.locator('[data-testid="media-row-0"]').dblclick();
    const resp = await audio;
    expect([200, 206]).toContain(resp.status());

    // The playing row is marked, and now-playing reaches the sidebar.
    await expect(page.locator('[data-testid="media-row-0"]')).toHaveAttribute('data-playing', 'true');
    const nowPlaying = page.locator('[data-testid="audio-nowplaying"]');
    await expect(nowPlaying).toBeVisible();
    await expect(nowPlaying).toContainText('Alpha Song');
  });
});

// Folder selection persists across reload: pick a subfolder via the
// FilePicker (narrowing the recursive list), then reload and confirm the
// app restored that folder rather than the default.
test.describe('music app folder persistence', () => {
  const dir = mkdtempSync(join(tmpdir(), 'wash-music-persist-'));
  writeFileSync(join(dir, 'Root Song.wav'), minimalWav());
  mkdirSync(join(dir, 'sub'));
  writeFileSync(join(dir, 'sub', 'Sub Song.wav'), minimalWav());

  // Confine the fs sandbox to `dir` so the FilePicker browses it (and the
  // BE's session-root → os-path mapping is exercised).
  test.use({ routerOpts: { apps: ['session', 'music', 'audio'], extraEnv: { WASH_MUSIC_DIR: dir, WASH_FS_ROOT: dir } } });

  test('remembers the picked folder across reload', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await router.controlRequest({ t: 'launch', app_id: 'com.wash.music' });

    const list = page.locator('[data-testid="track-list"]');
    await expect(list).toContainText('Root Song');
    await expect(list).toContainText('Sub Song'); // recursive scan sees both

    // Pick the sub/ folder via the FilePicker (directory mode).
    await page.locator('[data-testid="pick-folder"]').click();
    const picker = page.locator('[data-testid="folder-picker"]');
    await expect(picker).toBeVisible();
    await picker.locator('[data-testid="fp-entry-sub"]').click();
    await picker.locator('[data-testid="fp-confirm"]').click();

    // Now only the subfolder's track.
    await expect(list).toContainText('Sub Song');
    await expect(list).not.toContainText('Root Song');

    // Reload → the picked folder is restored (still only Sub Song).
    await page.reload();
    await expect(page.locator('wash-app-session')).toBeVisible();
    await expect(list).toContainText('Sub Song');
    await expect(list).not.toContainText('Root Song');
  });
});
