// Resize storm + geometry truth. 30 shell-driven resizes in quick succession
// (window.resize → X11 configure → guest repaints → window.geometry back),
// then: the loop must CONVERGE (no further window.geometry lines once idle),
// the guest's final ConfigureNotify size equals what the compositor last
// configured, the canvas backing store equals the guest size, and the
// quadrant colours of the Tk testcard sit where they should (no crop/offset/
// scale drift). Then the guest resizes ITSELF and the shell frame follows.
import { test } from '../fixtures/router';
import { bootDisplay, Evidence, TkGuest, healthChecks, expectNoDisplayWindows, waitDisplayWindow, displayEl, focusWin, resizeWin, shellWindows, canvasStats, patchMean, near, countMatches, sleep } from './lib';

test.use({ routerOpts: { apps: ['session', 'term', 'display'], showHidden: true } });

test('resize: 30-step storm converges; pixels match the guest testcard; guest-initiated resize follows', async ({ page, router }) => {
  const ev = new Evidence('resize');
  const info = await bootDisplay(page, router);
  const base = ev.procs('base', info, router.proc.pid ?? 0);
  const g = new TkGuest(info, 'resize-tk', { width: 400, height: 300 });
  await g.ready();
  const w = await waitDisplayWindow(page, /^resize-tk/);
  const el = displayEl(page, w.id);
  await focusWin(page, w.id);
  await sleep(800);

  // Storm.
  let seed = 7;
  const rnd = () => (seed = (seed * 1103515245 + 12345) & 0x7fffffff) / 0x7fffffff;
  const sizes: [number, number][] = [];
  for (let i = 0; i < 30; i++) sizes.push([200 + Math.floor(rnd() * 700), 150 + Math.floor(rnd() * 500)]);
  sizes.push([640, 480]);
  const from = router.logCursor();
  for (const [sw, sh] of sizes) {
    await resizeWin(page, w.id, sw, sh);
    await sleep(60);
  }
  // Converge: wait until no new window.geometry line for 2 s.
  let last = countMatches(router.log().slice(from), /window\.geometry|X11 configure/);
  let quietSince = Date.now();
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    await sleep(250);
    const n = countMatches(router.log().slice(from), /window\.geometry|X11 configure/);
    if (n !== last) { last = n; quietSince = Date.now(); }
    else if (Date.now() - quietSince > 2000) break;
  }
  const quietFrom = router.logCursor();
  await sleep(2000);
  const afterQuiet = countMatches(router.log().slice(quietFrom), /X11 configure win=/);
  ev.metric('configuresDuringStorm', last);
  ev.check('resize loop converges (no configure traffic once idle)', afterQuiet === 0, `${afterQuiet} configures in a 2 s quiet window`);

  const lastCfg = [...router.log().slice(from).matchAll(/X11 configure win=\d+ -> (\d+)x(\d+)/g)].pop();
  const guestCfg = [...g.log().matchAll(/configure (\d+) (\d+)/g)].pop();
  ev.metric('lastCompositorConfigure', lastCfg ? [+lastCfg[1], +lastCfg[2]] : null);
  ev.metric('lastGuestConfigure', guestCfg ? [+guestCfg[1], +guestCfg[2]] : null);
  ev.check('guest final size == last compositor configure', !!lastCfg && !!guestCfg && +lastCfg[1] === +guestCfg[1] && +lastCfg[2] === +guestCfg[2], `compositor ${lastCfg?.[1]}x${lastCfg?.[2]} guest ${guestCfg?.[1]}x${guestCfg?.[2]}`);
  const shellW = (await shellWindows(page)).find((x) => x.id === w.id)!;
  const cs = await canvasStats(el);
  ev.metric('shellWindow', [shellW.w, shellW.h]);
  ev.metric('canvas', [cs.w, cs.h]);
  ev.check('canvas backing store == guest size', !!guestCfg && cs.w === +guestCfg[1] && cs.h === +guestCfg[2], `canvas ${cs.w}x${cs.h} guest ${guestCfg?.[1]}x${guestCfg?.[2]}`);
  ev.check('canvas non-blank', cs.nonBlankPct > 80, `${cs.nonBlankPct}% non-blank, ${cs.uniqueColors} colours`);
  // Testcard quadrants (Tk canvas area; the status bar at the bottom is ~22px).
  if (cs.w && cs.h) {
    // Sample off the diagonals (which pass through the quadrant centres) and
    // away from the centre label.
    const ch = cs.h - 24;
    const q = {
      tl: await patchMean(el, Math.floor(cs.w * 0.25), Math.floor(ch * 0.12)),
      tr: await patchMean(el, Math.floor(cs.w * 0.75), Math.floor(ch * 0.12)),
      bl: await patchMean(el, Math.floor(cs.w * 0.25), Math.floor(ch * 0.88)),
      br: await patchMean(el, Math.floor(cs.w * 0.75), Math.floor(ch * 0.88)),
    };
    ev.metric('quadrants', q);
    const ok = near(q.tl, [0xc0, 0x30, 0x30]) && near(q.tr, [0x30, 0xc0, 0x30]) && near(q.bl, [0x30, 0x30, 0xc0]) && near(q.br, [0xf0, 0xf0, 0xf0]);
    ev.check('testcard quadrants land where expected (no crop/offset/scale drift)', ok, JSON.stringify(q));
  }
  await ev.shot(page, 'after-storm');

  // Guest-initiated resize.
  const from2 = router.logCursor();
  const gfrom = g.cursor();
  await g.cmd('resize 500 350');
  let gotCfg = '';
  try { gotCfg = await g.waitLog(/configure 500 350/, 8_000, gfrom); } catch { /* checked below */ }
  ev.check('guest self-resize is honoured (ConfigureNotify 500x350)', gotCfg !== '');
  await sleep(1500);
  const geom = [...router.log().slice(from2).matchAll(/window\.geometry[^\n]*?w=(\d+)[^\n]*?h=(\d+)|"w":(\d+),"h":(\d+)/g)].pop();
  const shellW2 = (await shellWindows(page)).find((x) => x.id === w.id)!;
  const cs2 = await canvasStats(el);
  ev.metric('afterSelfResize', { shell: [shellW2.w, shellW2.h], canvas: [cs2.w, cs2.h], geometryLine: geom?.[0] ?? null });
  ev.check('shell frame follows a guest self-resize', cs2.w === 500 && cs2.h === 350, `canvas ${cs2.w}x${cs2.h}, shell ${shellW2.w}x${shellW2.h}`);
  await ev.shot(page, 'end');

  g.kill();
  await g.exited();
  await expectNoDisplayWindows(ev, page);
  healthChecks(ev, router, info, base, { rssPct: 60 });
  ev.finish(router);
  ev.assertAll();
});
