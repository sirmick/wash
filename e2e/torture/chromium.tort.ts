// A heavy real-world client on BOTH backends: Chromium renders into
// wl_subsurfaces on Wayland (the M7 tree-composite path) and is a plain X
// client under --ozone-platform=x11. Checks: the page's coloured blocks
// reach the canvas (pixel truth), a 60-event wheel storm is survived with
// frames still flowing, delivered fps from the compositor log, clean close.
import { existsSync, mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from '../fixtures/router';
import { bootDisplay, Evidence, spawnClient, healthChecks, expectNoDisplayWindows, waitDisplayWindow, displayEl, focusWin, canvasStats, countMatches, sleep } from './lib';

test.use({ routerOpts: { apps: ['session', 'term', 'display'], showHidden: true } });

const PAGE = 'data:text/html,' + encodeURIComponent(`<!doctype html><title>wash-torture</title><body style="margin:0">
<div style="display:grid;grid-template-columns:1fr 1fr;height:300vh">
<div style="background:#c03030"></div><div style="background:#30c030"></div>
<div style="background:#3030c0"></div><div style="background:#f0f0f0"></div>
<div style="background:#c0c030"></div><div style="background:#30c0c0"></div></div>
<script>let n=0;setInterval(()=>{document.title='wash-torture '+(n++)},500)</script></body>`);

function fpsFromLog(log: string): number {
  const re = /(\d\d):(\d\d):(\d\d)\.(\d\d\d).*frame seq=(\d+)/g;
  const pts: { t: number; seq: number }[] = [];
  let m: RegExpExecArray | null;
  while ((m = re.exec(log))) pts.push({ t: +m[1] * 3600 + +m[2] * 60 + +m[3] + +m[4] / 1000, seq: +m[5] });
  if (pts.length < 2) return 0;
  const a = pts[0], b = pts[pts.length - 1];
  return b.t > a.t ? Math.round((b.seq - a.seq) / (b.t - a.t)) : 0;
}

for (const platform of ['wayland', 'x11'] as const) {
  test(`chromium (${platform}): subsurfaces render, wheel storm, title relay, clean close`, async ({ page, router }) => {
    test.skip(!existsSync('/usr/bin/chromium'), 'chromium missing');
    const ev = new Evidence(`chromium-${platform}`);
    const info = await bootDisplay(page, router);
    const base = ev.procs('base', info, router.proc.pid ?? 0);
    const profile = mkdtempSync(join(tmpdir(), 'wash-tort-chromium-'));
    const from = router.logCursor();
    const p = spawnClient(info, 'chromium', [
      `--ozone-platform=${platform}`, '--no-sandbox', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
      `--user-data-dir=${profile}`, '--window-size=900,600', '--disable-features=TranslateUI', PAGE,
    ]);
    let w;
    try {
      w = await waitDisplayWindow(page, /wash-torture|Chromium|New Tab|data:/i, 45_000);
    } catch (e) {
      ev.check('chromium window maps', false, String(e).slice(0, 200));
      p.kill('SIGKILL');
      ev.finish(router);
      ev.assertAll();
      return;
    }
    ev.check('chromium window maps', true, `title "${w.title}" ${w.w}x${w.h}`);
    const el = displayEl(page, w.id);
    await focusWin(page, w.id);
    await sleep(4000);
    let cs = await canvasStats(el);
    ev.metric('canvasInitial', cs);
    ev.check('page content reaches the canvas (non-blank, many colours)', cs.nonBlankPct > 60 && cs.uniqueColors > 3, `${cs.w}x${cs.h} ${cs.nonBlankPct}% ${cs.uniqueColors} colours`);
    await ev.shot(page, 'mapped');

    // Title relay (the page retitles every 500 ms).
    await sleep(1500);
    const t1 = (await page.evaluate((id) => (window as any).wash.windows().find((x: any) => x.windowID === id)?.title, w.id)) as string;
    await sleep(1500);
    const t2 = (await page.evaluate((id) => (window as any).wash.windows().find((x: any) => x.windowID === id)?.title, w.id)) as string;
    ev.check('post-map title changes are relayed', t1 !== t2 && /wash-torture/.test(t2 ?? ''), `"${t1}" → "${t2}"`);

    // Wheel storm.
    const fFrom = router.logCursor();
    await el.hover({ position: { x: 300, y: 300 } });
    for (let i = 0; i < 60; i++) {
      await page.mouse.wheel(0, i % 10 === 9 ? -400 : 120);
      await sleep(30);
    }
    await sleep(2500);
    const frames = countMatches(router.log().slice(fFrom), /inject win=\d+ |frame seq=/);
    const fps = fpsFromLog(router.log().slice(from));
    ev.metric('fpsFromLog', fps);
    ev.metric('logLinesDuringStorm', frames);
    cs = await canvasStats(el);
    ev.check('frames keep flowing through the wheel storm', fps > 0 || countMatches(router.log().slice(fFrom), /frame seq=/) > 0, `fps≈${fps}`);
    ev.check('canvas still non-blank after the storm', cs.nonBlankPct > 60, `${cs.nonBlankPct}%`);
    await ev.shot(page, 'after-storm');

    // Keyboard: Ctrl+L then type a URL-ish string; just must not crash anything.
    await page.keyboard.press('Control+l');
    await page.keyboard.type('about:blank');
    await page.keyboard.press('Escape');
    await sleep(500);

    p.kill('SIGTERM');
    const d = Date.now() + 15_000;
    while (await el.count() > 0 && Date.now() < d) await sleep(100);
    if (await el.count() > 0) p.kill('SIGKILL');
    await expectNoDisplayWindows(ev, page, 15_000);
    healthChecks(ev, router, info, base, { rssPct: 150 });
    ev.finish(router);
    ev.assertAll();
  });
}
