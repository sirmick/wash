// wash-launch: a CLI that asks the router to spawn an app via the
// per-router Unix control socket. The router sets WASH_CONTROL_SOCKET
// in every app's env, so a shell running inside wash-term inherits
// it and `wash-launch <app-id>` Just Works.

import { test, expect } from '../fixtures/router';
import { execFileSync } from 'node:child_process';
import type { Page } from '@playwright/test';

async function activeBufferText(page: Page): Promise<string> {
  return await page
    .locator('[data-testid="term-host"]')
    .locator('visible=true')
    .evaluate((host: any) => {
      const term = host.__washTerm;
      if (!term) return '';
      const buf = term.buffer.active;
      let out = '';
      for (let y = 0; y < buf.length; y++) {
        const line = buf.getLine(y);
        if (!line) continue;
        out += line.translateToString(true) + '\n';
      }
      return out;
    });
}

test.describe('wash-launch', () => {
  test('CLI invoked directly: spawns an app via the control socket', async ({ router, page }) => {
    // Open the chrome so the spawned About window has somewhere to mount.
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    const out = execFileSync(router.launchBin, ['com.wash.about'], {
      env: { ...process.env, WASH_CONTROL_SOCKET: router.controlSocket },
      timeout: 5_000,
      encoding: 'utf8',
    });
    expect(out).toMatch(/launched com\.wash\.about/);
    await expect(page.locator('wash-app-about')).toBeVisible();
  });

  test('CLI refuses to launch the session app', async ({ router }) => {
    let exitCode = 0;
    let stderr = '';
    try {
      execFileSync(router.launchBin, ['com.wash.session'], {
        env: { ...process.env, WASH_CONTROL_SOCKET: router.controlSocket },
        timeout: 5_000,
        encoding: 'utf8',
      });
    } catch (err) {
      const e = err as { status?: number; stderr?: Buffer | string };
      exitCode = e.status ?? -1;
      stderr = String(e.stderr ?? '');
    }
    expect(exitCode).not.toBe(0);
    expect(stderr).toMatch(/forbidden|desktop-surface/);
  });

  test('wash-launch from inside a wash-term shell opens a window', async ({ page, router }) => {
    test.setTimeout(20_000);
    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.getByRole('button', { name: /Terminal/ }).click();
    await expect(page.locator('wash-app-term')).toBeVisible();
    await expect.poll(() => activeBufferText(page), { timeout: 8_000 }).toMatch(/[$#%>][ ]?/);

    // bash inside wash-term inherited WASH_CONTROL_SOCKET. Run
    // wash-launch by absolute path (it lives next to the apps in
    // /tmp/wash-e2e-apps-XXX but we use the repo path for clarity).
    const host = page.locator('[data-testid="term-host"]').first();
    await host.click();
    await page.keyboard.type(`${router.launchBin} com.wash.about`);
    await page.keyboard.press('Enter');

    // About window appears.
    await expect(page.locator('wash-app-about')).toBeVisible();
    // wash-launch printed its success line into the terminal.
    await expect
      .poll(() => activeBufferText(page), { timeout: 5_000 })
      .toMatch(/launched com\.wash\.about/);
  });
});
