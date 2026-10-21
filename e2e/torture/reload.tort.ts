// Browser reload mid-stream. The shell reconnects, the router rebinds the
// video channels and asks the compositor for whole frames (window.force_frame
// → reset_delta); the window must reappear with a NON-BLANK canvas without
// the guest repainting, and input must work again on the new element.
import { test } from '../fixtures/router';
import { bootDisplay, Evidence, TkGuest, healthChecks, expectNoDisplayWindows, waitDisplayWindow, displayEl, focusWin, canvasStats, sleep } from './lib';

test.use({ routerOpts: { apps: ['session', 'term', 'display'], showHidden: true } });

test('reload: window + pixels + input survive a page reload', async ({ page, router }) => {
  const ev = new Evidence('reload');
  const info = await bootDisplay(page, router);
  const base = ev.procs('base', info, router.proc.pid ?? 0);
  const g = new TkGuest(info, 'reload-tk');
  await g.ready();
  const w = await waitDisplayWindow(page, /^reload-tk/);
  await sleep(1000);
  const before = await canvasStats(displayEl(page, w.id));
  ev.metric('canvasBefore', before);

  for (let i = 0; i < 3; i++) {
    const from = router.logCursor();
    await page.reload();
    const w2 = await waitDisplayWindow(page, /^reload-tk/, 20_000);
    // Wait for the force-frame to land: poll the canvas up to 10 s.
    let cs = await canvasStats(displayEl(page, w2.id));
    const d = Date.now() + 10_000;
    while (cs.nonBlankPct < 80 && Date.now() < d) { await sleep(200); cs = await canvasStats(displayEl(page, w2.id)); }
    ev.metric(`canvasAfterReload${i + 1}`, cs);
    ev.check(`reload ${i + 1}: canvas repainted without a guest repaint`, cs.nonBlankPct >= 80 && cs.w === before.w && cs.h === before.h, `${cs.w}x${cs.h} ${cs.nonBlankPct}% non-blank`);
    ev.check(`reload ${i + 1}: router asked the compositor for a full frame`, /force_frame|force-frame|window\.force_frame/.test(router.log().slice(from)), 'window.force_frame in log');
    await focusWin(page, w2.id);
    const gfrom = g.cursor();
    await displayEl(page, w2.id).click({ position: { x: 60, y: 60 } });
    let got = true;
    try { await g.waitLog(/button down 1/, 5_000, gfrom); } catch { got = false; }
    ev.check(`reload ${i + 1}: input works on the re-mounted element`, got);
  }
  await ev.shot(page, 'end');
  g.kill();
  await g.exited();
  await expectNoDisplayWindows(ev, page);
  healthChecks(ev, router, info, base);
  ev.finish(router);
  ev.assertAll();
});
