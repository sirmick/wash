// Menu storm on the X11 override-redirect path (Tk menus are OR toplevels
// with no WM_TRANSIENT_FOR — the compositor's "fallback parent" path), then
// the grab-stack hygiene around it:
//   - a real right-click menu opens as an overlay, an item click reaches the
//     guest ("menu pick"), the overlay is removed and the parent gets input
//     again (grab released);
//   - 100 post/unpost toggles: every popup channel is opened on the parent
//     window and NEVER closed on the wire (compositor sends no channel.close),
//     so router RSS growth ≈ 256 KiB × menus is the leak signature to watch;
//   - an UNTYPED override-redirect window (tooltips, OSDs, drag icons in
//     older toolkits) is treated as a grabbing menu: while it is mapped the
//     parent receives no pointer input at all. Recorded as a check so the
//     behaviour is visible; it fails if parent input is swallowed.
import { test } from '../fixtures/router';
import { bootDisplay, Evidence, TkGuest, healthChecks, expectNoDisplayWindows, waitDisplayWindow, displayEl, focusWin, popupOverlays, countMatches, sleep } from './lib';

test.use({ routerOpts: { apps: ['session', 'term', 'display'], showHidden: true } });

test('menus: right-click menu round-trip, 100-toggle storm, untyped OR window', async ({ page, router }) => {
  const ev = new Evidence('menus');
  const info = await bootDisplay(page, router);
  const g = new TkGuest(info, 'menus-tk', { width: 420, height: 320 });
  await g.ready();
  const w = await waitDisplayWindow(page, /^menus-tk/);
  const el = displayEl(page, w.id);
  await focusWin(page, w.id);
  await sleep(500);
  const base = ev.procs('base', info, router.proc.pid ?? 0);

  // 1. Real pointer-driven menu.
  let from = router.logCursor();
  let gfrom = g.cursor();
  await el.click({ button: 'right', position: { x: 150, y: 120 } });
  await router.waitForLog(/wash-display: X11 popup mapped/, 10_000, from);
  await g.waitLog(/menu posted/, 5_000, gfrom);
  await sleep(300);
  ev.check('menu opened as overlay canvas', (await popupOverlays(page)) >= 1, `${await popupOverlays(page)} overlays`);
  await ev.shot(page, 'menu-open');
  // Click the first item ("Alpha") near the top of the overlay.
  const overlay = page.locator('body > canvas[style*="fixed"]').first();
  gfrom = g.cursor();
  await overlay.click({ position: { x: 20, y: 12 }, force: true });
  let picked = '';
  try {
    picked = await g.waitLog(/menu pick (\w+)/, 5_000, gfrom);
  } catch {
    /* recorded below */
  }
  ev.check('menu item click reaches the guest', picked !== '', picked || 'no "menu pick" in guest log');
  await sleep(300);
  ev.check('overlay removed after pick', (await popupOverlays(page)) === 0, `${await popupOverlays(page)} overlays`);
  // Parent gets input again (grab popped).
  gfrom = g.cursor();
  await el.click({ position: { x: 60, y: 60 } });
  let got = true;
  try { await g.waitLog(/button down 1/, 5_000, gfrom); } catch { got = false; }
  ev.check('parent window receives clicks after the menu closed', got);

  // 2. Storm.
  from = router.logCursor();
  gfrom = g.cursor();
  await g.send('menu toggle 100 60');
  await g.waitLog(/menu storm done 100/, 60_000, gfrom);
  await sleep(1000);
  const mappedN = countMatches(router.log().slice(from), /wash-display: X11 popup mapped/);
  const droppedN = countMatches(router.log().slice(from), /no parent toplevel — dropping/);
  ev.metric('stormPopupsMapped', mappedN);
  ev.metric('stormPopupsDropped', droppedN);
  ev.check('every storm post mapped as a popup', mappedN >= 50, `${mappedN} mapped, ${droppedN} dropped (expected 50)`);
  ev.check('no overlay left after the storm', (await popupOverlays(page)) === 0, `${await popupOverlays(page)} overlays`);
  gfrom = g.cursor();
  await el.click({ position: { x: 60, y: 60 } });
  got = true;
  try { await g.waitLog(/button down 1/, 5_000, gfrom); } catch { got = false; }
  ev.check('parent receives clicks after the storm (grab stack empty)', got);
  const mid = ev.procs('after-storm', info, router.proc.pid ?? 0);
  const chanIds = [...router.log().slice(from).matchAll(/X11 popup mapped parent_win=\d+ chan=(\d+)/g)].map((m) => Number(m[1]));
  ev.metric('popupChannelsOpened', chanIds.length);
  ev.metric('popupChannelIdRange', chanIds.length ? [chanIds[0], chanIds[chanIds.length - 1]] : []);
  ev.note(`router after ${mappedN} popups: RSS ${base.router.rssKb} → ${mid.router.rssKb} KB, VmData ${base.router.vmDataKb} → ${mid.router.vmDataKb} KB (${Math.round((mid.router.vmDataKb - base.router.vmDataKb) / Math.max(1, mappedN))} KB/popup); compositor RSS ${base.compositor.rssKb} → ${mid.compositor.rssKb} KB`);
  // Every popup opens a fresh video-popup channel on the parent window and
  // nothing ever closes it on the wire, so the router keeps one
  // channelBinding + a 256 KiB ring per popup until the PARENT window dies.
  // The ring is never written for video, so it shows in VmData, not RSS.
  const perPopupKb = (mid.router.vmDataKb - base.router.vmDataKb) / Math.max(1, mappedN);
  ev.check('router heap growth per popup < 64 KB (popup channels get closed)', perPopupKb < 64, `${Math.round(perPopupKb)} KB VmData per popup (a leaked channel costs ≈256 KB)`);

  // 3. Untyped override-redirect window (no menu type, no grab in X terms).
  from = router.logCursor();
  await g.cmd('or show 120 60');
  await router.waitForLog(/X11 popup mapped .*grab=(0|1)/, 10_000, from);
  const grabFlag = /X11 popup mapped .*grab=(\d)/.exec(router.log().slice(from))?.[1];
  ev.metric('untypedORGrab', grabFlag);
  gfrom = g.cursor();
  await el.click({ position: { x: 300, y: 250 } }); // far from the 120x60 OR box at (30,30)
  got = true;
  try { await g.waitLog(/button down 1 (\d+) (\d+)/, 4_000, gfrom); } catch { got = false; }
  ev.check('parent still gets clicks while an untyped OR window is mapped', got, `compositor classified it grab=${grabFlag}`);
  await g.cmd('or hide');
  await sleep(300);
  ev.check('OR window overlay removed on unmap', (await popupOverlays(page)) === 0);

  await ev.shot(page, 'end');
  g.kill();
  await g.exited();
  await expectNoDisplayWindows(ev, page);
  healthChecks(ev, router, info, base, { rssPct: 60 });
  ev.finish(router);
  ev.assertAll();
});
