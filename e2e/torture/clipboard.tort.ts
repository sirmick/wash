// Clipboard volume + hygiene through the X11 selection bridge (xwm) and the
// wash router: guest→wash (100 KB, then 20 small copies), wash→guest (200 KB
// paste), content verified by sha1 on both ends; compositor thread count must
// return to baseline (every selection read/write spawns a detached thread).
import { createHash } from 'node:crypto';
import { test } from '../fixtures/router';
import { bootDisplay, Evidence, TkGuest, healthChecks, expectNoDisplayWindows, waitDisplayWindow, focusWin, procStats, sleep } from './lib';

test.use({ routerOpts: { apps: ['session', 'term', 'display'], showHidden: true } });

const sha1 = (s: string) => createHash('sha1').update(s).digest('hex');

test('clipboard: guest→wash 100 KB, 20× small, wash→guest 200 KB, thread hygiene', async ({ page, router }) => {
  const ev = new Evidence('clipboard');
  const info = await bootDisplay(page, router);
  const base = ev.procs('base', info, router.proc.pid ?? 0);
  const g = new TkGuest(info, 'clip-tk');
  await g.ready();
  const w = await waitDisplayWindow(page, /^clip-tk/);
  await focusWin(page, w.id);
  await sleep(500);

  // guest → wash, 100 KB.
  let from = router.logCursor();
  await g.cmd('clip set 100000');
  const guestSha = /clip set 100000 sha1=([0-9a-f]+)/.exec(g.log())?.[1] ?? '';
  let bridged = true;
  try { await router.waitForLog(/clipboard guest->wash mime=/, 10_000, from); } catch { bridged = false; }
  await sleep(500);
  const washText = await page.evaluate(() => (window as any).wash.clipboardGetText() as string | Promise<string>);
  ev.metric('guestToWashBytes', (washText ?? '').length);
  ev.check('guest copy is bridged into wash (log)', bridged);
  ev.check('wash clipboard text matches the guest\'s 100 KB (sha1)', !!washText && sha1(washText) === guestSha, `wash has ${(washText ?? '').length} bytes; sha ${washText ? sha1(washText).slice(0, 8) : '-'} vs guest ${guestSha.slice(0, 8)}`);

  // 20 small copies in a row.
  from = router.logCursor();
  for (let i = 0; i < 20; i++) await g.cmd(`clip set ${100 + i}`);
  await sleep(1500);
  const n = (router.log().slice(from).match(/clipboard guest->wash/g) ?? []).length;
  ev.metric('smallCopiesBridged', n);
  ev.check('all 20 small copies bridged', n === 20, `${n}/20`);
  const lastText = await page.evaluate(() => (window as any).wash.clipboardGetText() as string | Promise<string>);
  ev.check('wash holds the LAST copy (119 bytes)', (lastText ?? '').length === 119, `${(lastText ?? '').length} bytes`);

  // wash → guest, 200 KB.
  const big = Array.from({ length: 200_000 }, (_, i) => String.fromCharCode(33 + (i * 7) % 90)).join('');
  from = router.logCursor();
  await page.evaluate((t) => (window as any).wash.clipboardSetText(t), big);
  let offered = true;
  try { await router.waitForLog(/clipboard wash->guest offered/, 10_000, from); } catch { offered = false; }
  ev.check('wash copy claims the guest selection (log)', offered);
  const gfrom = g.cursor();
  await g.cmd('clip get', 20_000);
  const got = /clip get (\d+) sha1=([0-9a-f]+)/.exec(g.log().slice(gfrom));
  ev.metric('washToGuestBytes', got ? +got[1] : 0);
  ev.check('guest paste gets the 200 KB with the right sha1', !!got && +got[1] === 200_000 && got[2] === sha1(big), got ? `${got[1]} bytes sha ${got[2].slice(0, 8)} vs ${sha1(big).slice(0, 8)}` : (/clip get error (.*)/.exec(g.log().slice(gfrom))?.[1] ?? 'no clip get line'));

  // Paste again 10× (each paste = one detached writer thread in the compositor).
  for (let i = 0; i < 10; i++) await g.cmd('clip get', 20_000);
  await sleep(1500);
  const th = procStats(info.compositorPid).threads;
  ev.metric('compositorThreadsAfter', th);
  ev.check('compositor threads back to baseline after 30 selection transfers', th <= base.compositor.threads + 1, `${base.compositor.threads} → ${th}`);

  g.kill();
  await g.exited();
  await expectNoDisplayWindows(ev, page);
  healthChecks(ev, router, info, base, { rssPct: 60 });
  ev.finish(router);
  ev.assertAll();
});
