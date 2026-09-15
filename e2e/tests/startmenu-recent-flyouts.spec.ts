// Start menu Recent flyouts beyond files: the folder fm was showing when it
// closed, the stations Radio played, and the agent sessions agentd
// remembers — each on its own row (Files ›, Radio ›, Agent ›), each
// replayable from there.
//
// Both halves per test: the FE (the flyout, the window it brings back and
// what that window shows) and the BE (the apps' recent-note lines, the
// session BE's noted/open/play lines, and the verb agentd received).

import { fileURLToPath } from 'node:url';
import { mkdirSync, writeFileSync } from 'node:fs';
import { createServer, type Server } from 'node:http';
import type { AddressInfo } from 'node:net';
import { join } from 'node:path';
import type { Page } from '@playwright/test';
import { test, expect } from '../fixtures/router';

async function openFlyout(page: Page, group: string) {
  await page.locator('button[title="Apps"]').click();
  const menu = page.locator('[data-testid="start-menu"]');
  await expect(menu).toBeVisible();
  await menu.locator(`[data-testid="start-menu-recent-group-${group}"]`).click();
  const flyout = page.locator('[data-testid="start-menu-flyout"]');
  await expect(flyout).toBeVisible();
  return { menu, flyout, items: flyout.locator('[data-testid="start-menu-flyout-item"]') };
}

const escapeRe = (s: string) => s.replace(/[.*+?^${}()|[\]\\/]/g, '\\$&');

test.describe('Files ›: the folder fm closed on', () => {
  test.use({
    routerOpts: {
      apps: ['session', 'fm'],
      fmRoot: true,
      fmSeed: (root: string) => {
        mkdirSync(join(root, 'projects', 'alpha'), { recursive: true });
        writeFileSync(join(root, 'projects', 'alpha', 'plan.md'), '# plan\n');
      },
    },
  });

  test('closing fm records its folder; the flyout reopens fm there', async ({ page, router }) => {
    const alpha = join(router.fmRoot, 'projects', 'alpha');
    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
    const fm = page.locator('wash-app-fm');
    await expect(fm).toBeVisible();
    const fmPath = fm.locator('[data-testid="fm-path"]');
    await expect(fmPath).not.toHaveValue('');
    await fmPath.fill(alpha);
    await fmPath.press('Enter');
    await expect(fm.locator('[data-testid="fm-entry-plan.md"]')).toBeVisible();

    // Nothing yet: the Files row is there, its flyout says so.
    {
      const { flyout } = await openFlyout(page, 'files');
      await expect(flyout.locator('[data-testid="start-menu-flyout-empty"]')).toHaveText('No recent folders');
      // Escape closes the flyout first, then the menu, like a submenu.
      await page.keyboard.press('Escape');
      await expect(flyout).toHaveCount(0);
      await page.keyboard.press('Escape');
      await expect(page.locator('[data-testid="start-menu"]')).toHaveCount(0);
    }

    const from = router.logCursor();
    await page.locator('.wash-window', { has: fm }).locator('[data-testid="window-close"]').click();
    await expect(fm).toHaveCount(0);
    await router.waitForLog(new RegExp(`wash-fm: recent note path="${escapeRe(alpha)}"`), 10_000, from);
    await router.waitForLog(new RegExp(`wash-session: recent noted path="${escapeRe(alpha)}" app=com\\.wash\\.fm via=note`), 10_000, from);

    const { menu, flyout, items } = await openFlyout(page, 'files');
    await expect(items).toHaveCount(1);
    await expect(items.first()).toContainText('alpha');
    const open = router.logCursor();
    await items.first().click();
    await expect(menu).toHaveCount(0);
    await expect(flyout).toHaveCount(0);
    // A folder has no extension to route by, so the session BE spawns fm
    // on it rather than issuing an open.request the router would drop.
    await router.waitForLog(new RegExp(`wash-session: recent open dir="${escapeRe(alpha)}" app=com\\.wash\\.fm`), 10_000, open);
    await router.waitForLog(new RegExp(`wash-fm: launch open=${escapeRe(alpha)} dir=${escapeRe(alpha)}`), 15_000, open);
    await expect(fm).toBeVisible({ timeout: 15_000 });
    await expect(fm.locator('[data-testid="fm-path"]')).toHaveValue(alpha);
    await expect(fm.locator('[data-testid="fm-entry-plan.md"]')).toBeVisible();
  });
});

// A local fake stream stands in for the internet, as in radio.spec.ts.
// Stations are written to Radio's station config before Radio starts, so
// they are fixed stations — a pasted one lives in the window's state and
// would not survive closing it, which this test needs to do.
test.describe('Radio ›: stations played', () => {
  let server: Server;
  let stream = '';
  test.beforeAll(async () => {
    server = createServer((req, res) => {
      res.writeHead(200, { 'Content-Type': 'audio/mpeg' });
      const iv = setInterval(() => {
        try {
          res.write(Buffer.alloc(256));
        } catch {
          clearInterval(iv);
        }
      }, 80);
      req.on('close', () => clearInterval(iv));
    });
    await new Promise<void>((r) => server.listen(0, '127.0.0.1', () => r()));
    stream = `http://127.0.0.1:${(server.address() as AddressInfo).port}/stream`;
  });
  test.afterAll(() => server?.close());

  test.use({ routerOpts: { apps: ['session', 'radio', 'audio'] } });

  const radioRow = (page: Page, name: string) =>
    page.locator('[data-testid="station-list"] [data-testid^="media-row-"]').filter({ hasText: name });

  test('a played station is listed; picking it relaunches Radio tuned, or retunes a running one', async ({ page, router }) => {
    test.setTimeout(60_000);
    mkdirSync(join(router.xdgConfigHome, 'wash'), { recursive: true });
    const station = (name: string, q: string) => ({ name, url: `${stream}?${q}`, codec: 'stream', genre: 'Custom', source: 'Custom' });
    writeFileSync(
      join(router.xdgConfigHome, 'wash', 'radio-stations.json'),
      JSON.stringify({ version: 1, stations: [station('Fake FM', 'a'), station('Fake Two', 'b')] }),
    );
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await router.controlRequest({ t: 'launch', app_id: 'com.wash.radio' });
    const radio = page.locator('wash-app-radio');
    await expect(radio).toBeVisible();
    await page.locator('[data-testid="genre-custom"]').click();

    let from = router.logCursor();
    await radioRow(page, 'Fake FM').dblclick();
    await expect(radioRow(page, 'Fake FM')).toHaveAttribute('data-playing', 'true');
    await router.waitForLog(/wash-radio: recent note name="Fake FM"/, 15_000, from);
    await router.waitForLog(/wash-session: recent noted name="Fake FM" app=com\.wash\.radio via=note/, 10_000, from);

    // Close Radio; its window and process go.
    await page.locator('.wash-window', { has: radio }).locator('[data-testid="window-close"]').click();
    await expect(radio).toHaveCount(0);
    await expect.poll(() => page.evaluate(() => window.wash.windows().some((w) => w.element === 'wash-app-radio'))).toBe(false);

    // A fresh Radio has no list when the tune arrives: the BE holds the
    // name and delivers it with the first station list.
    {
      const { items } = await openFlyout(page, 'radio');
      await expect(items).toHaveCount(1);
      await expect(items.first()).toContainText('Fake FM');
      from = router.logCursor();
      await items.first().click();
    }
    await router.waitForLog(/wash-session: recent play app=com\.wash\.radio name="Fake FM"/, 10_000, from);
    await router.waitForLog(/wash-session: recent play sent instance=\S+ name="Fake FM"/, 15_000, from);
    await router.waitForLog(/wash-radio: tune name="Fake FM" (delivered with station list|forwarded)/, 15_000, from);
    await expect(radio).toBeVisible();
    await expect(radioRow(page, 'Fake FM')).toHaveAttribute('data-playing', 'true', { timeout: 15_000 });

    // Play the other station in Radio, then pick the first from the menu
    // while Radio is running: the tune is forwarded straight to its FE.
    await radioRow(page, 'Fake Two').dblclick();
    await expect(radioRow(page, 'Fake Two')).toHaveAttribute('data-playing', 'true');
    {
      const { items } = await openFlyout(page, 'radio');
      await expect(items).toHaveCount(2);
      await expect(items.nth(0)).toContainText('Fake Two');
      await expect(items.nth(1)).toContainText('Fake FM');
      from = router.logCursor();
      await items.nth(1).click();
    }
    await router.waitForLog(/wash-radio: tune name="Fake FM" forwarded/, 15_000, from);
    await expect(radioRow(page, 'Fake FM')).toHaveAttribute('data-playing', 'true', { timeout: 15_000 });
    await expect(radio).toHaveCount(1);
  });
});

const FAKE_DIR = fileURLToPath(new URL('../../out/e2e', import.meta.url));

test.describe('Agent ›: agent sessions', () => {
  test.use({
    routerOpts: {
      apps: ['session', 'agentd', 'agents', 'ai', 'notify'],
      extraEnv: { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` },
    },
  });

  test('focus a running session, reattach a detached one, resume a finished one', async ({ page, router }) => {
    test.setTimeout(90_000);
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Agents', exact: true }).click();
    const manager = page.locator('wash-app-agents');
    await expect(manager).toBeVisible();
    await manager.locator('select').selectOption('codex');
    await manager.getByRole('button', { name: 'Start session' }).click();
    const controller = page.locator('wash-app-ai');
    const composer = controller.locator('textarea');
    await expect(composer).toBeVisible({ timeout: 20_000 });
    await composer.fill('remember the flyouts');
    await composer.press('Enter');
    await expect(controller.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });

    // Running with a window: the item focuses it.
    let from = router.logCursor();
    {
      const { flyout } = await openFlyout(page, 'agent');
      const item = flyout.locator('[data-kind="agent"]');
      await expect(item).toHaveCount(1, { timeout: 15_000 });
      await expect(item).toHaveAttribute('data-action', 'focus');
      await item.locator('[data-testid="start-menu-flyout-item"]').click();
    }
    await router.waitForLog(/wash-session: agent open action=focus session=\S+ key=acp:\S+/, 10_000, from);
    await expect(controller).toHaveCount(1);

    // Detached: still running, no window. The item reattaches by row key.
    await page.locator('.wash-window', { has: controller }).locator('[data-testid="window-close"]').click();
    await page.locator('[data-testid="ai-close-confirm"]').getByRole('button', { name: 'Detach' }).click();
    await expect(controller).toHaveCount(0);
    await router.waitForLog(/agentd: acp detached key=/, 10_000, from);
    from = router.logCursor();
    {
      const { flyout } = await openFlyout(page, 'agent');
      const item = flyout.locator('[data-kind="agent"]');
      await expect(item).toHaveAttribute('data-action', 'reattach', { timeout: 10_000 });
      await item.locator('[data-testid="start-menu-flyout-item"]').click();
    }
    await router.waitForLog(/wash-session: agent open action=reattach session=\S+ key=acp:\S+/, 10_000, from);
    await expect(controller).toHaveCount(1, { timeout: 15_000 });
    await expect(controller.getByText('remember the flyouts')).toBeVisible({ timeout: 15_000 });

    // Terminated: history only. The item resumes it natively.
    await page.locator('.wash-window', { has: controller }).locator('[data-testid="window-close"]').click();
    await page.locator('[data-testid="ai-close-confirm"]').getByRole('button', { name: 'Terminate' }).click();
    await expect(controller).toHaveCount(0);
    await router.waitForLog(/agentd: acp session ended key=/, 15_000, from);
    from = router.logCursor();
    {
      const { flyout } = await openFlyout(page, 'agent');
      const item = flyout.locator('[data-kind="agent"]');
      await expect(item).toHaveAttribute('data-action', 'resume', { timeout: 10_000 });
      await item.locator('[data-testid="start-menu-flyout-item"]').click();
    }
    await router.waitForLog(/wash-session: agent open action=resume session=\S+ key=/, 10_000, from);
    await router.waitForLog(/agentd: acp session resumed key=/, 20_000, from);
    await expect(controller).toHaveCount(1, { timeout: 20_000 });
    await expect(controller.getByText('remember the flyouts')).toBeVisible({ timeout: 20_000 });
  });
});
