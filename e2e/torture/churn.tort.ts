// Window churn: many real X clients mapped and torn down in quick succession,
// plus a Tk guest opening/closing batches of child toplevels. Invariants:
// every wash window disappears, the compositor/Xwayland survive, and RSS /
// fds / threads stay bounded against the post-boot baseline (DISPLAY_E2E.md
// P4 "soak" — here as a fast per-run check).
import { test } from '../fixtures/router';
import { bootDisplay, Evidence, TkGuest, spawnClient, healthChecks, expectNoDisplayWindows, displayWindows, sleep, alive } from './lib';

test.use({ routerOpts: { apps: ['session', 'term', 'display'], showHidden: true } });

test('churn: 25 open/close cycles of xeyes+xcalc, 5 batches of 8 Tk toplevels', async ({ page, router }) => {
  const ev = new Evidence('churn');
  const info = await bootDisplay(page, router);
  const base = ev.procs('base', info, router.proc.pid ?? 0);

  const cycles = 25;
  let mapped = 0;
  for (let i = 0; i < cycles; i++) {
    const a = spawnClient(info, 'xeyes');
    const b = spawnClient(info, 'xcalc');
    // Wait for both to map (window count 2), then kill; occasionally kill
    // BEFORE the map lands to race create_window against unmap.
    const early = i % 5 === 4;
    if (!early) {
      const deadline = Date.now() + 10_000;
      while ((await displayWindows(page)).length < 2 && Date.now() < deadline) await sleep(50);
      mapped += (await displayWindows(page)).length >= 2 ? 1 : 0;
    } else {
      await sleep(30);
    }
    a.kill('SIGKILL');
    b.kill('SIGKILL');
    const deadline = Date.now() + 10_000;
    while ((await displayWindows(page)).length > 0 && Date.now() < deadline) await sleep(50);
  }
  ev.metric('cycles', cycles);
  ev.metric('cyclesWithBothMapped', mapped);
  ev.check('both clients mapped on every full cycle', mapped === cycles - Math.floor(cycles / 5), `${mapped}/${cycles - Math.floor(cycles / 5)}`);
  await expectNoDisplayWindows(ev, page);

  // Tk child-toplevel batches (same client, many windows).
  const g = new TkGuest(info, 'churn-tk');
  await g.ready();
  for (let i = 0; i < 5; i++) {
    await g.cmd('child 8');
    const deadline = Date.now() + 15_000;
    while ((await displayWindows(page)).length < 9 && Date.now() < deadline) await sleep(50);
    const n = (await displayWindows(page)).length;
    if (i === 0) ev.check('8 child toplevels + parent all mapped', n === 9, `${n} display windows`);
    await g.cmd('child close');
    const d2 = Date.now() + 15_000;
    while ((await displayWindows(page)).length > 1 && Date.now() < d2) await sleep(50);
  }
  const left = (await displayWindows(page)).length;
  ev.check('children torn down, parent remains', left === 1, `${left} display windows`);
  await ev.shot(page, 'end');
  g.kill();
  await g.exited();
  await expectNoDisplayWindows(ev, page);

  const winCreates = (router.log().match(/window\.create instance=/g) ?? []).length;
  ev.metric('windowCreates', winCreates);
  healthChecks(ev, router, info, base, { rssPct: 40 });
  ev.check('no stray guest processes', !alive(g.pid));
  ev.finish(router);
  ev.assertAll();
});
