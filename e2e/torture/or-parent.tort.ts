// Fallback parent for X11 override-redirect menus with no WM_TRANSIENT_FOR:
// the compositor remembers only the MOST RECENTLY MAPPED X toplevel
// (g_active_x_toplevel) and clears it when that one unmaps. So: map A (Tk),
// map B (xeyes), close B, open a menu in A → the menu has no parent and is
// dropped ("no parent toplevel — dropping"). The pointer window / focused
// window is the obvious parent; this scenario pins the behaviour either way.
import { test } from '../fixtures/router';
import { bootDisplay, Evidence, TkGuest, spawnClient, healthChecks, expectNoDisplayWindows, waitDisplayWindow, displayEl, focusWin, countMatches, sleep } from './lib';

test.use({ routerOpts: { apps: ['session', 'term', 'display'], showHidden: true } });

test('or-parent: menu in A after the later-mapped B closes', async ({ page, router }) => {
  const ev = new Evidence('or-parent');
  const info = await bootDisplay(page, router);
  const base = ev.procs('base', info, router.proc.pid ?? 0);
  const a = new TkGuest(info, 'orp-A');
  await a.ready();
  const wa = await waitDisplayWindow(page, /^orp-A/);
  const b = spawnClient(info, 'xeyes');
  await waitDisplayWindow(page, /xeyes/i);
  await sleep(300);
  b.kill('SIGKILL');
  const d = Date.now() + 10_000;
  while ((await page.evaluate(() => (window as any).wash.windows().length)) > 1 && Date.now() < d) await sleep(50);

  await focusWin(page, wa.id);
  const el = displayEl(page, wa.id);
  await el.hover({ position: { x: 100, y: 100 } });
  await sleep(200);
  const from = router.logCursor();
  const gfrom = a.cursor();
  // Pointer-driven: the right-click lands in A, so the pointer IS in A.
  await el.click({ button: 'right', position: { x: 100, y: 100 } });
  await a.waitLog(/menu posted/, 5_000, gfrom);
  await sleep(800);
  const mapped = countMatches(router.log().slice(from), /X11 popup mapped/);
  const dropped = countMatches(router.log().slice(from), /no parent toplevel — dropping/);
  ev.metric('mapped', mapped);
  ev.metric('dropped', dropped);
  ev.check('menu in A still overlays after B (the last-mapped toplevel) closed', mapped >= 1 && dropped === 0, `mapped=${mapped} dropped=${dropped}`);
  await ev.shot(page, 'menu');
  await page.keyboard.press('Escape');
  a.kill();
  await a.exited();
  await expectNoDisplayWindows(ev, page);
  healthChecks(ev, router, info, base);
  ev.finish(router);
  ev.assertAll();
});
