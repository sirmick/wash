// Screensaver (web/lib/src/screensaver.ts, run per tab by the session app):
//
//   1. desktop.json `screensaver` with a short timeout → the overlay comes up
//      on an idle tab; the waking key is swallowed (the page never sees its
//      keydown or keyup); the next key is the page's again.
//   2. Every animated saver loads over the asset channel and draws: the
//      overlay holds its canvas and the canvas is not black. The waking
//      click tears it down.
//   3. Settings' Screensaver pane (its own rail section) writes the block to
//      desktop.json, shows the chosen saver's own options, and Preview shows
//      the saver.
//
// Short timeouts are real waits rather than a faked clock: page.clock would
// also fake the shell's heartbeat and reconnect timers.

import { test, expect } from '../fixtures/router';
import type { Page } from '@playwright/test';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

test.use({ routerOpts: { apps: ['session', 'settings'] } });

const desktopJSON = (xdg: string) => join(xdg, 'wash', 'desktop.json');

function writeDesktop(xdg: string, value: unknown) {
  mkdirSync(join(xdg, 'wash'), { recursive: true });
  writeFileSync(desktopJSON(xdg), JSON.stringify(value));
}

const overlay = (page: Page) => page.locator('[data-testid="screensaver"]');

// Count the key events the page itself receives (bubble phase on document).
async function spyKeys(page: Page) {
  await page.evaluate(() => {
    const w = window as unknown as { __keys: string[] };
    w.__keys = [];
    for (const t of ['keydown', 'keyup']) document.addEventListener(t, (e) => w.__keys.push(`${e.type}:${(e as KeyboardEvent).key}`));
  });
  return () => page.evaluate(() => (window as unknown as { __keys: string[] }).__keys);
}

// litFraction: share of sampled pixels that aren't near-black, read by
// copying the saver's WebGL canvas into a 2D canvas.
async function litFraction(page: Page): Promise<number> {
  return page.evaluate(
    () =>
      new Promise<number>((resolve) => {
        requestAnimationFrame(() => {
          const src = document.querySelector<HTMLCanvasElement>('[data-testid="screensaver"] canvas');
          if (!src || !src.width) return resolve(0);
          const c = document.createElement('canvas');
          c.width = 64;
          c.height = 36;
          const ctx = c.getContext('2d')!;
          ctx.drawImage(src, 0, 0, c.width, c.height);
          const d = ctx.getImageData(0, 0, c.width, c.height).data;
          let lit = 0;
          for (let i = 0; i < d.length; i += 4) if (d[i] + d[i + 1] + d[i + 2] > 24) lit++;
          resolve(lit / (c.width * c.height));
        });
      }),
  );
}

test.describe('screensaver', () => {
  test('starts on an idle tab; the waking key never reaches the page', async ({ page, router }) => {
    writeDesktop(router.xdgConfigHome, { screensaver: { enabled: true, timeout_s: 2, saver: 'blank' } });
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    const keys = await spyKeys(page);

    await expect(overlay(page)).toBeVisible({ timeout: 10_000 });
    await expect(overlay(page)).toHaveAttribute('data-saver', 'blank');

    await page.keyboard.press('q');
    await expect(overlay(page)).toHaveCount(0);
    expect(await keys()).toEqual([]);

    await page.keyboard.press('w');
    expect(await keys()).toEqual(['keydown:w', 'keyup:w']);
  });

  test('stays off while disabled', async ({ page, router }) => {
    writeDesktop(router.xdgConfigHome, { screensaver: { enabled: false, timeout_s: 1, saver: 'blank' } });
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await page.waitForTimeout(2500);
    await expect(overlay(page)).toHaveCount(0);
  });

  for (const id of ['logowriter3d', 'harbor', 'moire-interference', 'moire-glow']) {
    test(`${id} loads and draws`, async ({ page, router }) => {
      const warnings: string[] = [];
      page.on('console', (m) => {
        if (m.type() === 'error' || /screensaver/.test(m.text())) warnings.push(m.text());
      });
      writeDesktop(router.xdgConfigHome, { screensaver: { enabled: true, timeout_s: 1, saver: id } });
      await page.goto(router.url);
      await expect(page.locator('wash-app-session')).toBeVisible();

      await expect(overlay(page)).toHaveAttribute('data-saver', id, { timeout: 10_000 });
      await expect(overlay(page).locator('canvas')).toHaveCount(1, { timeout: 10_000 });
      await expect.poll(() => litFraction(page), { timeout: 15_000 }).toBeGreaterThan(0.05);

      await page.mouse.click(10, 10);
      await expect(overlay(page)).toHaveCount(0);
      expect(warnings).toEqual([]);
    });
  }

  test('Settings writes the block, shows the saver options, and previews', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await router.controlRequest({ t: 'launch', app_id: 'com.wash.settings' });
    const app = page.locator('wash-app-settings');
    await expect(app).toBeVisible();
    // Its own section in the rail, not part of Desktop.
    await expect(app.locator('[data-testid="screensaver-enabled"]')).toHaveCount(0);
    await app.getByRole('button', { name: 'Screensaver', exact: true }).click();

    await app.locator('[data-testid="screensaver-enabled"]').check();
    await app.locator('[data-testid="screensaver-card-harbor"]').click();
    await expect(app.locator('[data-testid="screensaver-opt-fog"]')).toBeVisible();
    await app.locator('[data-testid="screensaver-opt-bridge"]').selectOption('arch');

    await expect
      .poll(() => {
        try {
          return JSON.parse(readFileSync(desktopJSON(router.xdgConfigHome), 'utf8')).screensaver;
        } catch {
          return null;
        }
      }, { timeout: 5_000 })
      .toMatchObject({ enabled: true, timeout_s: 600, saver: 'harbor', options: { harbor: { bridge: 'arch' } } });

    await app.locator('[data-testid="screensaver-preview"]').click();
    await expect(overlay(page)).toHaveAttribute('data-saver', 'harbor');
    await expect(overlay(page).locator('canvas')).toHaveCount(1, { timeout: 10_000 });
    await page.keyboard.press('Escape');
    await expect(overlay(page)).toHaveCount(0);
    // Settings is still open: the Escape went to the screensaver, not to it.
    await expect(app).toBeVisible();
  });
});
