// Guest and server crashes. (1) SIGKILL a guest while its menu is open: the
// overlay must go, the window must go, and the grab stack must not keep
// swallowing input for the NEXT guest. (2) SIGKILL Xwayland itself: the
// compositor must survive, every X window must be torn down, and (wlroots
// lazy mode) a new X client must map again on the respawned server. (3) A
// guest that blocks its event loop for 4 s must not stall the compositor:
// another guest keeps receiving input meanwhile.
import { test } from '../fixtures/router';
import { bootDisplay, resolveXwayland, Evidence, TkGuest, spawnClient, healthChecks, expectNoDisplayWindows, waitDisplayWindow, displayEl, focusWin, popupOverlays, displayWindows, findPid, alive, procStats, sleep } from './lib';

test.use({ routerOpts: { apps: ['session', 'term', 'display'], showHidden: true } });

test('crash: guest dies mid-menu; Xwayland dies; a wedged guest does not wedge the compositor', async ({ page, router }) => {
  const ev = new Evidence('crash');
  const info = await bootDisplay(page, router);
  const base = ev.procs('base', info, router.proc.pid ?? 0);

  // (1) guest dies with its menu open.
  const a = new TkGuest(info, 'crash-A');
  await a.ready();
  const wa = await waitDisplayWindow(page, /^crash-A/);
  await focusWin(page, wa.id);
  let from = router.logCursor();
  await displayEl(page, wa.id).click({ button: 'right', position: { x: 120, y: 120 } });
  await router.waitForLog(/X11 popup mapped/, 10_000, from);
  await sleep(300);
  a.kill('SIGKILL');
  await a.exited();
  const d = Date.now() + 10_000;
  while (((await displayWindows(page)).length > 0 || (await popupOverlays(page)) > 0) && Date.now() < d) await sleep(50);
  ev.check('window torn down after SIGKILL mid-menu', (await displayWindows(page)).length === 0, `${(await displayWindows(page)).length} windows`);
  ev.check('overlay torn down after SIGKILL mid-menu', (await popupOverlays(page)) === 0, `${await popupOverlays(page)} overlays`);
  const b = new TkGuest(info, 'crash-B');
  await b.ready();
  const wb = await waitDisplayWindow(page, /^crash-B/);
  await focusWin(page, wb.id);
  await sleep(300);
  let bfrom = b.cursor();
  await displayEl(page, wb.id).click({ position: { x: 80, y: 80 } });
  let got = true;
  try { await b.waitLog(/button down 1/, 5_000, bfrom); } catch { got = false; }
  ev.check('next guest receives clicks (grab stack not stuck on the dead menu)', got);

  // (3) wedged guest: B blocks 4 s; C must still get input.
  const c = new TkGuest(info, 'crash-C');
  await c.ready();
  const wc = await waitDisplayWindow(page, /^crash-C/);
  await page.evaluate(([id]) => (window as any).wash.moveWindow(id, 700, 100), [wc.id]);
  await sleep(300);
  await b.send('busy 4000');
  await sleep(500);
  await focusWin(page, wc.id);
  const cfrom = c.cursor();
  const t0 = Date.now();
  await displayEl(page, wc.id).click({ position: { x: 80, y: 80 } });
  got = true;
  try { await c.waitLog(/button down 1/, 3_000, cfrom); } catch { got = false; }
  ev.metric('clickLatencyWhileBWedgedMs', Date.now() - t0);
  // Two X toplevels at the same X root position: the click must reach the
  // window the wash pointer is over (C), not whichever Xwayland stacks on
  // top (B) — and B being wedged must not delay it either.
  const strayToB = /button down 1 80 80/.test(b.log().slice(b.log().lastIndexOf('busy start')));
  ev.check('click on C reaches C (not the other, stacked-above, wedged guest B)', got && !strayToB, `${Date.now() - t0} ms; B got it: ${strayToB}`);
  await b.waitLog(/busy end/, 10_000);

  // (2) Xwayland dies.
  from = router.logCursor();
  ev.check('Xwayland process found', resolveXwayland(info) > 0, `pid ${info.xwaylandPid}`);
  const oldX = info.xwaylandPid;
  if (oldX) process.kill(oldX, 'SIGKILL');
  const d2 = Date.now() + 15_000;
  while ((await displayWindows(page)).length > 0 && Date.now() < d2) await sleep(100);
  ev.check('all X windows torn down when Xwayland dies', (await displayWindows(page)).length === 0, `${(await displayWindows(page)).length} windows`);
  await sleep(1000);
  ev.check('compositor survives Xwayland dying', alive(info.compositorPid));
  b.kill(); c.kill();
  await b.exited(); await c.exited();
  // Respawn: a new X client should bring Xwayland back (wlroots lazy start).
  const e = spawnClient(info, 'xeyes');
  let remapped = false;
  try { await waitDisplayWindow(page, /xeyes/i, 20_000); remapped = true; } catch { /* recorded */ }
  const newX = findPid((argv) => /(^|\/)Xwayland\0/.test(argv) && argv.includes(`\0${info.x}\0`));
  ev.metric('xwaylandRespawnPid', newX);
  ev.check('Xwayland respawns and a new X client maps', remapped && newX > 0 && newX !== oldX, `remapped=${remapped} old=${oldX} new=${newX}`);
  e.kill('SIGKILL');
  await expectNoDisplayWindows(ev, page);
  // Report against the respawned Xwayland.
  info.xwaylandPid = newX;
  ev.metric('compositorAfter', procStats(info.compositorPid));
  healthChecks(ev, router, info, base, { rssPct: 60 });
  ev.finish(router);
  ev.assertAll();
});
