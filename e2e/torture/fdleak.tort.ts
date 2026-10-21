// Inherited descriptors. The compositor forks Xwayland (via wlroots) and any
// $WASH_DISPLAY_EXEC guest; the wire socket to the router is opened without
// SOCK_CLOEXEC, so every child inherits it. A child holding the router
// socket keeps the connection "open" after the compositor dies and can write
// on wash's wire with the compositor's capabilities. This scenario lists
// every socket/pipe inode shared between the compositor and Xwayland, and
// between the compositor and a freshly spawned X client (which should share
// nothing but the X connection it opened itself).
import { test } from '../fixtures/router';
import { bootDisplay, resolveXwayland, Evidence, TkGuest, healthChecks, expectNoDisplayWindows, waitDisplayWindow, sharedFds, fmtShared, sleep } from './lib';

test.use({ routerOpts: { apps: ['session', 'term', 'display'], showHidden: true } });

test('fdleak: compositor descriptors inherited by Xwayland / guests', async ({ page, router }) => {
  const ev = new Evidence('fdleak');
  const info = await bootDisplay(page, router);
  const base = ev.procs('base', info, router.proc.pid ?? 0);
  const g = new TkGuest(info, 'fd-tk');
  await g.ready();
  await waitDisplayWindow(page, /^fd-tk/);
  await sleep(300);
  ev.check('Xwayland process found', resolveXwayland(info) > 0, `pid ${info.xwaylandPid}`);

  const cx = sharedFds(info.compositorPid, info.xwaylandPid);
  ev.metric('sharedCompositorXwayland', cx);
  // Expected: stdio (router log pipes) and the LISTENING X11 sockets wlroots
  // hands Xwayland via -listenfd. Xwayland holds the other ends of its wl/wm
  // socketpairs (different inodes), so a shared CONNECTED socket is an fd the
  // compositor leaked across exec — the router wire socket (dial() has no
  // SOCK_CLOEXEC).
  const leaked = cx.filter((x) => x.kind === 'connected');
  ev.check('Xwayland holds no connected socket of the compositor\'s (wire socket not inherited)', leaked.length === 0, fmtShared(cx));
  const cg = sharedFds(info.compositorPid, g.pid);
  ev.metric('sharedCompositorGuest', cg);
  ev.check('a guest spawned outside the compositor shares nothing with it', cg.length === 0, fmtShared(cg));
  const rx = sharedFds(router.proc.pid ?? 0, info.xwaylandPid).filter((x) => x.kind !== 'stdio');
  ev.metric('sharedRouterXwayland', rx);
  ev.check('Xwayland shares nothing but stdio with the router', rx.length === 0, fmtShared(rx));

  g.kill();
  await g.exited();
  await expectNoDisplayWindows(ev, page);
  healthChecks(ev, router, info, base);
  ev.finish(router);
  ev.assertAll();
});
