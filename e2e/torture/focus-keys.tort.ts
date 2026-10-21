// Focus thrash + keyboard truth across two X11 guests. Keys typed while A is
// focused must reach A only (with the right modifier state); switching focus
// to B with Shift held must release Shift in A (the FE's held-key release)
// and B must see clean modifier state; a wheel over B lands in B.
import { test } from '../fixtures/router';
import { bootDisplay, Evidence, TkGuest, healthChecks, expectNoDisplayWindows, waitDisplayWindow, displayEl, focusWin, countMatches, sleep } from './lib';

test.use({ routerOpts: { apps: ['session', 'term', 'display'], showHidden: true } });

test('focus-keys: two guests, modifiers, held-key release on focus change', async ({ page, router }) => {
  const ev = new Evidence('focus-keys');
  const info = await bootDisplay(page, router);
  const base = ev.procs('base', info, router.proc.pid ?? 0);
  const a = new TkGuest(info, 'fk-A');
  await a.ready();
  const wa = await waitDisplayWindow(page, /^fk-A/);
  const b = new TkGuest(info, 'fk-B');
  await b.ready();
  const wb = await waitDisplayWindow(page, /^fk-B/);
  // Put B away from A so clicks don't overlap.
  await page.evaluate(([id]) => (window as any).wash.moveWindow(id, 700, 80), [wb.id]);
  await page.evaluate(([id]) => (window as any).wash.moveWindow(id, 40, 80), [wa.id]);
  await sleep(500);
  const elA = displayEl(page, wa.id), elB = displayEl(page, wb.id);

  // Round 1: A gets "abc", Shift+a, Ctrl+c.
  let aFrom = a.cursor(), bFrom = b.cursor();
  await elA.click({ position: { x: 100, y: 100 } });
  await focusWin(page, wa.id);
  await sleep(200);
  await page.keyboard.type('abc');
  await page.keyboard.press('Shift+A');
  await page.keyboard.press('Control+c');
  await a.waitLog(/key down c mods=ctrl/, 5_000, aFrom).catch(() => {});
  await sleep(300);
  const aLog = a.log().slice(aFrom), bLog = b.log().slice(bFrom);
  ev.check('A saw a,b,c', /key down a /.test(aLog) && /key down b /.test(aLog) && /key down c mods=-/.test(aLog), aLog.split('\n').filter((l) => /key down/.test(l)).join(' / ').slice(0, 300));
  ev.check('A saw Shift+A with shift modifier', /key down A mods=shift/.test(aLog));
  ev.check('A saw Ctrl+c with ctrl modifier', /key down c mods=ctrl/.test(aLog));
  ev.check('B saw no keys while A was focused', countMatches(bLog, /key down/) === 0, `${countMatches(bLog, /key down/)} keys in B`);
  ev.check('every key down in A has a key up', countMatches(aLog, /key down/) === countMatches(aLog, /key up/), `${countMatches(aLog, /key down/)} down / ${countMatches(aLog, /key up/)} up`);

  // Round 2: hold Shift in A, switch focus to B by clicking B, release Shift.
  aFrom = a.cursor(); bFrom = b.cursor();
  await page.keyboard.down('Shift');
  await sleep(150);
  await elB.click({ position: { x: 100, y: 100 } });
  await focusWin(page, wb.id);
  await sleep(300);
  await page.keyboard.up('Shift');
  await page.keyboard.type('x');
  await b.waitLog(/key down x /, 5_000, bFrom).catch(() => {});
  await sleep(300);
  const aLog2 = a.log().slice(aFrom), bLog2 = b.log().slice(bFrom);
  // X key state is server-wide in Xwayland: the release the FE sends on blur
  // reaches whichever X window is focused by then (A or B) and clears the
  // modifier either way. What must NOT happen is the release going nowhere.
  ev.check('Shift release sent on focus change reached a guest (no latched modifier)', /key up Shift_L/.test(aLog2) || /key up Shift_L/.test(bLog2), (aLog2 + bLog2).split('\n').filter((l) => /Shift/.test(l)).join(' / ').slice(0, 300));
  ev.check('B received x with clean modifiers after the focus change', /key down x mods=-/.test(bLog2), bLog2.split('\n').filter((l) => /key down/.test(l)).join(' / ').slice(0, 300));
  ev.check('A saw no x', !/key down x/.test(aLog2));

  // Round 3: wheel over B.
  bFrom = b.cursor();
  await elB.hover({ position: { x: 120, y: 120 } });
  await page.mouse.wheel(0, 240);
  let wheel = true;
  try { await b.waitLog(/wheel down/, 5_000, bFrom); } catch { wheel = false; }
  ev.check('wheel over B reaches B as button 5', wheel);

  // Round 4: pointer leave — hover A then B; A should get a leave.
  aFrom = a.cursor();
  await elA.hover({ position: { x: 50, y: 50 } });
  await sleep(200);
  await elB.hover({ position: { x: 50, y: 50 } });
  await focusWin(page, wb.id);
  await sleep(500);
  ev.check('A gets a pointer leave when the pointer moves to B', /leave/.test(a.log().slice(aFrom)), 'tracks REVIEW-X11-WAYLAND #9: leave only on unfocus');

  await ev.shot(page, 'end');
  a.kill(); b.kill();
  await a.exited(); await b.exited();
  await expectNoDisplayWindows(ev, page);
  healthChecks(ev, router, info, base);
  ev.finish(router);
  ev.assertAll();
});
