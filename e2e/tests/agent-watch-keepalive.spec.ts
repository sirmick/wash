// A transcript watcher survives agentd's TTL (docs/Review-findings.md,
// 2026-09-08 sweep, "Transcript freezes after 60 s").
//
// agentd drops a transcript watcher it has not heard from within
// WatcherTTL. wash-ai re-affirmed only on the agent_started / attach paths,
// so a window that reached its session through the roster row — the path
// a window opened from the start menu takes — went quiet after a minute:
// the status line kept moving (the roster subscription is the
// StateService's, kept alive by itself) while the transcript stopped at
// the first event after the TTL. The keepalive now starts once per window
// from onReady and re-affirms whatever key the window holds.
//
// The TTL is shrunk through WASH_AGENT_WATCHER_TTL (one seam, read by
// agentd and every host from internal/agentclient), so the test waits past
// several TTLs in seconds rather than minutes.

import { fileURLToPath } from 'node:url';
import type { Page } from '@playwright/test';
import { test, expect } from '../fixtures/router';

const FAKE_DIR = fileURLToPath(new URL('../../out/e2e', import.meta.url));
const TTL_MS = 3_000;

test.use({
  routerOpts: {
    apps: ['session', 'agentd', 'ai', 'notify'],
    extraEnv: {
      PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}`,
      WASH_AGENT_WATCHER_TTL: `${TTL_MS}ms`,
    },
  },
});

async function openAgentWindow(page: Page, nth: number) {
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Agent', exact: true }).click();
  const win = page.locator('wash-app-ai').nth(nth);
  await expect(win).toBeVisible();
  return win;
}

test('a window that picked its session from the roster keeps receiving the transcript past the TTL', async ({
  page,
  router,
}) => {
  test.setTimeout(90_000);
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();

  // Window A starts the session and gets its first reply.
  const a = await openAgentWindow(page, 0);
  await a.locator('select').selectOption('codex');
  await a.getByRole('button', { name: 'Start session' }).click();
  const composerA = a.locator('textarea');
  await expect(composerA).toBeVisible({ timeout: 20_000 });
  await composerA.fill('first prompt before the ttl');
  await composerA.press('Enter');
  await expect(a.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });

  // Window B is opened fresh from the start menu and reaches the SAME
  // session through the roster row — the `select` path, which never
  // started a keepalive.
  const b = await openAgentWindow(page, 1);
  const pane = b.locator('[data-testid="ai-roster-pane"]');
  await expect(pane).toBeVisible({ timeout: 20_000 });
  const row = pane.locator('[data-testid^="agents-row-"]').first();
  await expect(row).toBeVisible({ timeout: 20_000 });
  const cursor = router.logCursor();
  await row.click();
  await expect(b.getByText('first prompt before the ttl')).toBeVisible({ timeout: 20_000 });
  await expect(b.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });

  // B's instance, from the ready line of the second window: the BE
  // assertions below are about THIS watcher, not A's.
  const ready = [...router.log().matchAll(/wash-ai ready instance=(\S+)/g)].map((m) => m[1]);
  expect(ready.length, 'two Agent windows announced themselves').toBeGreaterThanOrEqual(2);
  const instB = ready[ready.length - 1];
  const esc = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  // The select subscribed B exactly once (with a replay).
  await router.waitForLog(new RegExp(`transcript subscribed instance=${esc(instB)} key=\\S+ replay=true`), 10_000, cursor);

  // Idle past several TTLs. Nothing happens on screen; the question is
  // whether agentd still counts B as a watcher afterwards.
  await page.waitForTimeout(TTL_MS * 3);

  // BE half: B's watcher was never expired, and its keepalives were
  // keepalives — a known instance re-affirming logs nothing and gets no
  // second snapshot. A re-subscribe AFTER an expiry would show as another
  // "subscribed" line; a dropped one as "expired".
  const since = router.log().slice(cursor);
  expect(since).not.toMatch(new RegExp(`transcript watcher expired instance=${esc(instB)}`));
  expect(since.match(new RegExp(`transcript subscribed instance=${esc(instB)} `, 'g'))?.length ?? 0).toBe(1);

  // FE half: a new turn — sent from A, so B is a pure observer — still
  // lands in B. Both the prompt line (wash's own record) and the reply.
  await composerA.fill('second prompt after the ttl');
  await composerA.press('Enter');
  await expect(b.getByText('second prompt after the ttl')).toBeVisible({ timeout: 20_000 });
  await expect(b.getByText('Hello from the fake agent.')).toHaveCount(2, { timeout: 20_000 });

  // The TTL is only enforced when agentd next pushes an event, so the
  // decisive BE check is after the push: B was not expired on its way
  // out. Without the keepalive this is where the log says
  // "transcript watcher expired" for B and the FE half above goes red.
  expect(router.log().slice(cursor)).not.toMatch(new RegExp(`transcript watcher expired instance=${esc(instB)}`));
});
