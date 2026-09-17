// The observe verb's fallbacks, end to end (docs/COMMANDER.md §4.1):
// auto means auto. About has no pty, saves no state and registers no
// provider, so the router holds nothing for it — and answers none but
// eligible. The shell then reads the window's rendered text, and the
// caller gets a `dom` observation with what is on screen.
//
// Both halves: the router's log says it answered none (and never quotes
// anything); the FE observation carries the About window's text.

import { test, expect } from '../fixtures/router';

test.use({ routerOpts: { apps: ['session', 'about'] } });

test('an eligible window the router holds nothing for is observed from its DOM', async ({ page, router }) => {
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();

  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu-com.wash.about"]').click();
  const about = page.locator('wash-app-about');
  await expect(about).toBeVisible();
  await expect(about.locator('[data-testid="about-journal"]')).toBeVisible();

  const from = router.logCursor();
  const o = await page.evaluate(async () => {
    const w = window.wash.windows().find((x) => x.appID === 'com.wash.about')!;
    return window.wash.observe(w.origin || undefined, w.instanceID);
  });
  // FE half: the fallback read the screen.
  expect(o.source).toBe('dom');
  expect(o.eligible).toBe(true);
  expect(o.content_type).toBe('text/plain');
  expect(o.content).toMatch(/entries today|--no-activity/);
  expect(o.window?.app).toBe('com.wash.about');
  expect(o.revision).toMatch(/^dom:/);

  // BE half: the router answered from nothing it holds, and said so.
  const log = router.log().slice(from);
  expect(log).toMatch(/observe: instance=\S+ app=com\.wash\.about source=none bytes=0 by=shell/);

  // The same call with the bare id and an explicit origin resolves the same
  // window (the ids WindowInfo carries are origin-tagged).
  const again = await page.evaluate(async () => {
    const w = window.wash.windows().find((x) => x.appID === 'com.wash.about')!;
    return window.wash.observe('local', w.instanceID);
  });
  expect(again.source).toBe('dom');
  expect(again.revision).toBe(o.revision);
});
