// Playback-state persistence (wash-reconnect): the playing track and
// volume survive a browser reload. The player restores to a
// paused-at-track state (no autoplay) with the saved track selected,
// so one click resumes — instead of the pre-fix behaviour where the
// reload forgot everything but the folder.
import { test, expect } from '../fixtures/router';
import { mkdtempSync, writeFileSync } from 'node:fs';
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

test.describe('music app playback persistence', () => {
  const dir = mkdtempSync(join(tmpdir(), 'wash-music-resume-'));
  writeFileSync(join(dir, 'Alpha Song.wav'), minimalWav());
  writeFileSync(join(dir, 'Beta Tune.wav'), minimalWav());

  test.use({ routerOpts: { apps: ['session', 'music', 'audio'], extraEnv: { WASH_MUSIC_DIR: dir } } });

  test('playing track survives reload as paused selection', async ({ page, router }) => {
    test.setTimeout(30_000);
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await router.controlRequest({ t: 'launch', app_id: 'com.wash.music' });

    const list = page.locator('[data-testid="track-list"]');
    await expect(list).toBeVisible();
    await expect(list).toContainText('Beta Tune');

    // Play the SECOND track so the restore can't pass by defaulting
    // to the first row.
    await page.locator('[data-testid="media-row-1"]').dblclick();
    await expect(page.locator('[data-testid="media-row-1"]')).toHaveAttribute('data-playing', 'true');

    // Let the 300ms persist debounce flush before reloading.
    await page.waitForTimeout(600);
    await page.reload();

    // Restored: the same track is the current one (now-playing shows
    // its title) without any user interaction.
    const nowPlaying = page.locator('[data-testid="music-nowplaying"]');
    await expect(nowPlaying).toBeVisible({ timeout: 10_000 });
    await expect(nowPlaying).toContainText('Beta Tune', { timeout: 10_000 });
    await expect(page.locator('[data-testid="media-row-1"]')).toHaveAttribute('data-playing', 'true');
  });
});
