// An Agent window that reconnects asks only for what changed.
//
// Every reconnect used to re-send each Agent window's whole transcript. A
// background tab whose heartbeat the browser throttled got reaped and
// reconnected every ~95s, so a long session's megabytes of history (and its
// screenshots) went into a page nobody was looking at, over and over, until
// the tab ran out of memory. Now the window says how much it holds (an
// epoch and the highest rev) and agentd sends only the rows after it.
//
// A reload still asks for everything: a fresh page holds nothing.

import { test, expect } from '../fixtures/router';
import { AGENT_APPS, FAKE_DIR, startAgentSession } from '../fixtures/agents';

test.use({
  routerOpts: {
    apps: [...AGENT_APPS],
    extraEnv: { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` },
  },
});

test('a reconnect replays only what changed; a reload replays everything', async ({ page, router }) => {
  test.setTimeout(90_000);
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  const win = await startAgentSession(page, 'first prompt');
  await expect(win.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });

  const before = await page.evaluate(() => (window as any).__washDiag?.().conn?.totalReconnects ?? 0);
  const drop = router.logCursor();
  await page.evaluate(() => (window as any).__washDropSocket?.());
  await expect.poll(async () => {
    const d = await page.evaluate(() => (window as any).__washDiag?.().conn ?? null);
    return !!(d && d.state === 'open' && d.totalReconnects > before);
  }, { timeout: 15_000 }).toBe(true);

  // The window said what it holds, so agentd answered with the rest only.
  await router.waitForLog(/transcript subscribed instance=\S+ key=\S+ replay=true since=[1-9]\d*/, 10_000, drop);
  // Nothing lost, nothing doubled.
  await expect(win.getByText('first prompt')).toHaveCount(1);
  await expect(win.getByText('Hello from the fake agent.')).toHaveCount(1);

  // And the stream carries on from where it was.
  const composer = win.locator('textarea');
  await composer.fill('second prompt');
  await composer.press('Enter');
  await expect(win.getByText('Hello from the fake agent.')).toHaveCount(2, { timeout: 20_000 });

  const reload = router.logCursor();
  await page.reload();
  const restored = page.locator('wash-app-ai');
  await expect(restored.getByText('Hello from the fake agent.')).toHaveCount(2, { timeout: 20_000 });
  await expect(restored.getByText('second prompt')).toHaveCount(1);
  await router.waitForLog(/transcript subscribed instance=\S+ key=\S+ replay=true since=0/, 10_000, reload);
});
