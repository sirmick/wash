// The Agents rail's per-session verbs, end to end (GH #21).
//
// agentd has handled agent_detach / agent_cancel / agent_stop since the
// ACP tier landed. What #21 hit is that nothing could reach them: the
// rail rendered live rows with no actions, and the session BE gateway
// stopped at reattach. So the rail logged no backend request — not
// because state was lost (agent-rail.spec.ts proves it isn't), but
// because there was no button to send one.
//
// This drives the whole chain the issue said was missing: a click in the
// sidebar → the session BE gateway → agentd → the ACP session actually
// ending. Both halves are asserted, because either alone can pass while
// the feature is broken: the row could vanish from an FE that never
// reached the backend, and agentd could end a session the rail still
// shows.

import { fileURLToPath } from 'node:url';
import type { Page } from '@playwright/test';
import { test, expect } from '../fixtures/router';

const FAKE_DIR = fileURLToPath(new URL('../../out/e2e', import.meta.url));

test.use({
  routerOpts: {
    apps: ['session', 'agentd', 'ai', 'notify'],
    extraEnv: { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` },
  },
});

async function startSession(page: Page, url: string) {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  await page.locator('button[title="Apps"]').click();
  await page
    .locator('[data-testid="start-menu"]')
    .getByRole('button', { name: 'Agent', exact: true })
    .click();
  const win = page.locator('wash-app-ai').first();
  await expect(win).toBeVisible();
  await win.locator('select').selectOption('codex');
  await win.getByRole('button', { name: 'Start session' }).click();
  const composer = win.locator('textarea');
  await expect(composer).toBeVisible({ timeout: 20_000 });
  await composer.fill('say something');
  await composer.press('Enter');
  await expect(win.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });
  return win;
}

async function openRail(page: Page) {
  const header = page.locator('[data-testid="sidebar-section-header-agents"]');
  await expect(header).toBeVisible();
  const body = page.locator('[data-testid="sidebar-section-body-agents"]');
  if ((await body.count()) === 0) await header.click();
  await expect(body).toBeVisible();
  return body;
}

test.describe('agents rail per-session verbs', () => {
  test('End ends the session — the menu asks first, then reaches agentd', async ({
    page,
    router,
  }) => {
    test.setTimeout(60_000);
    await startSession(page, router.url);
    const body = await openRail(page);
    const row = body.locator('[data-testid^="agents-row-"]').first();
    await expect(row).toBeVisible({ timeout: 15_000 });

    // Everything after this point must be caused by our clicks, so read
    // the log from here rather than from 0.
    const cursor = router.logCursor();

    // The verbs live in a per-row menu. It portals to document.body, so
    // it is addressed from the page, not from inside the row.
    await row.locator('[data-testid="agents-verbs-btn"]').click();
    const menu = page.locator('[data-testid="agents-row-actions"]');
    await expect(menu).toBeVisible();

    // Picking End asks first. Asserted rather than assumed: End sits one
    // row from Detach, and an End that fired on the pick would be a
    // session lost to a slip.
    await menu.locator('[data-testid="agents-menu-end"]').click();
    expect(router.log().slice(cursor)).not.toMatch(/acp session ended/);

    // Confirming fires: FE → session BE gateway → agentd → the adapter.
    await menu.locator('[data-testid="agents-menu-end-confirm"]').click();

    // BE half: agentd really retired the session.
    await router.waitForLog(/agentd: acp session ended key=/, 15_000, cursor);

    // FE half: the roster reflects it. A rail that only *looked* right
    // would pass the BE assertion on its own.
    await expect(body.locator('[data-testid^="agents-row-"]')).toHaveCount(0, { timeout: 15_000 });
    await expect(body.locator('[data-testid="agents-empty"]')).toBeVisible();
  });

  test('Detach keeps the session running and offers it back', async ({ page, router }) => {
    test.setTimeout(60_000);
    await startSession(page, router.url);
    const body = await openRail(page);
    const row = body.locator('[data-testid^="agents-row-"]').first();
    await expect(row).toBeVisible({ timeout: 15_000 });

    const cursor = router.logCursor();
    await row.locator('[data-testid="agents-verbs-btn"]').click();
    const menu = page.locator('[data-testid="agents-row-actions"]');
    await expect(menu).toBeVisible();
    await menu.locator('[data-testid="agents-menu-detach"]').click();

    // agentd marks it detached and republishes — it does NOT end.
    await router.waitForLog(/agentd: acp detached key=/, 15_000, cursor);
    expect(router.log().slice(cursor)).not.toMatch(/acp session ended/);

    // The row stays (that is the whole point of detach) and Detach goes
    // disabled, because there is no longer a window to let go of.
    await expect(row).toBeVisible();
    await row.locator('[data-testid="agents-verbs-btn"]').click();
    await expect(page.locator('[data-testid="agents-menu-detach"]')).toBeDisabled({
      timeout: 15_000,
    });
    await page.keyboard.press('Escape');
    await page.mouse.click(5, 5);
    // And it now says what it is, rather than advertising a terminal it
    // does not have — the duplicate-`title` bug this shipped with.
    await expect(row).toHaveAttribute('title', /Detached/);

    // Reload discards component-local state. The new roster snapshot must
    // still identify the session as detached, and one double-click must
    // open one window with the existing transcript.
    await expect(page.locator('wash-app-ai')).toHaveCount(0, { timeout: 15_000 });
    await page.reload();
    await expect(page.locator('wash-app-session')).toBeVisible();
    const restoredBody = await openRail(page);
    const restoredRow = restoredBody.locator('[data-testid^="agents-row-"]').first();
    await expect(restoredRow).toHaveAttribute('title', /Detached/, { timeout: 15_000 });
    await restoredRow.dblclick();

    const reopened = page.locator('wash-app-ai');
    await expect(reopened).toHaveCount(1, { timeout: 15_000 });
    await expect(reopened.locator('textarea')).toBeVisible({ timeout: 20_000 });
    await expect(reopened.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });
  });
});
