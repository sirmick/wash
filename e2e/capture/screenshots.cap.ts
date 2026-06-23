// Capture the marketing screenshots in docs/screenshots/ deterministically.
//
// Run via `make screenshots` — this lives under e2e/capture/ and is driven by
// its OWN config (playwright.screenshots.config.ts), so it is NOT part of the
// `make e2e-test` suite. Each test boots a throwaway router, poses one or more
// real app windows, and writes a PNG straight into docs/screenshots/.
//
// Determinism: a fixed-seed PRNG drives the window nudges + sidebar state, so
// re-running produces a stable layout — only genuine UI changes move pixels.
// (The terminal/display shots show live content — clock, uname — so those few
// are naturally not byte-stable; everything else is.)

import { test, expect, displaySkipReason } from '../fixtures/router';
import type { Page, Locator } from '@playwright/test';
import { mkdirSync, writeFileSync, existsSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';
import { deflateSync } from 'node:zlib';

const SHOTS = join(dirname(fileURLToPath(import.meta.url)), '..', '..', 'docs', 'screenshots');
mkdirSync(SHOTS, { recursive: true });

// --- a tiny self-contained PNG encoder ------------------------------------
// So the image-viewer shot has real pictures to thumbnail without committing
// binary fixtures. RGB, 8-bit, one zlib IDAT — decodable by internal/thumbs.
function crc32(buf: Buffer): number {
  let c = ~0;
  for (let i = 0; i < buf.length; i++) {
    c ^= buf[i];
    for (let k = 0; k < 8; k++) c = (c >>> 1) ^ (0xedb88320 & -(c & 1));
  }
  return ~c >>> 0;
}
function pngChunk(type: string, data: Buffer): Buffer {
  const len = Buffer.alloc(4); len.writeUInt32BE(data.length, 0);
  const td = Buffer.concat([Buffer.from(type, 'ascii'), data]);
  const crc = Buffer.alloc(4); crc.writeUInt32BE(crc32(td), 0);
  return Buffer.concat([len, td, crc]);
}
function writePng(path: string, w: number, h: number,
                  px: (x: number, y: number) => [number, number, number]): void {
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(w, 0); ihdr.writeUInt32BE(h, 4);
  ihdr[8] = 8; ihdr[9] = 2; // 8-bit depth, colour type 2 (RGB)
  const raw = Buffer.alloc((w * 3 + 1) * h);
  let o = 0;
  for (let y = 0; y < h; y++) {
    raw[o++] = 0; // filter: none
    for (let x = 0; x < w; x++) {
      const [r, g, b] = px(x, y);
      raw[o++] = r & 255; raw[o++] = g & 255; raw[o++] = b & 255;
    }
  }
  writeFileSync(path, Buffer.concat([
    Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]),
    pngChunk('IHDR', ihdr),
    pngChunk('IDAT', deflateSync(raw)),
    pngChunk('IEND', Buffer.alloc(0)),
  ]));
}

// mulberry32 — a tiny deterministic PRNG so the montage layout is reproducible.
function mkRng(seed: number): () => number {
  return () => {
    seed |= 0;
    seed = (seed + 0x6d2b79f5) | 0;
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

// A small, lived-in starter tree so the file manager (and editor) have
// something worth showing.
function seedShowcase(root: string): void {
  for (const d of ['Documents', 'Pictures', 'Projects', 'Music', 'Downloads']) {
    mkdirSync(join(root, d), { recursive: true });
  }
  writeFileSync(join(root, 'README.md'),
    '# wash\n\nA full desktop environment that runs in your browser.\n\n' +
    '- Files, Terminal, Editor\n- System Monitor, Services, Packages\n- Networking, Disks, Audio\n');
  writeFileSync(join(root, 'Documents', 'notes.txt'), 'todo: ship 0.9.0\n');
  writeFileSync(join(root, 'Documents', 'budget.csv'), 'item,cost\nrouter,0\nrent,0\n');
  writeFileSync(join(root, 'Projects', 'hello.go'),
    'package main\n\nimport "fmt"\n\nfunc main() {\n\tfmt.Println("hello from wash")\n}\n');
  writeFileSync(join(root, 'Music', 'playlist.m3u'), '#EXTM3U\n');
  writeFileSync(join(root, '.profile'), 'export EDITOR=wash-edit\n');

  // Generated images so the Image Viewer has real thumbnails. These live in
  // Pictures/ INSIDE the sandbox root, so the fs-root confinement (below) lets
  // imageview read them — WASH_IMAGEVIEW_DIR points here.
  const pics = join(root, 'Pictures');
  writePng(join(pics, 'sunset.png'), 480, 320, (_x, y) => {
    const t = y / 320;
    return [Math.round(240 - 60 * t), Math.round(90 + 70 * t), Math.round(120 + 110 * t)];
  });
  writePng(join(pics, 'aurora.png'), 480, 320, (x, y) => {
    const v = Math.sin(x / 36) * Math.cos(y / 48);
    return [Math.round(20 + 30 * v), Math.round(150 + 90 * v), Math.round(130 + 80 * v)];
  });
  writePng(join(pics, 'grid.png'), 400, 400, (x, y) =>
    ((x >> 5) + (y >> 5)) & 1 ? [38, 42, 64] : [120, 200, 220]);
  writePng(join(pics, 'rings.png'), 420, 300, (x, y) => {
    const d = Math.hypot(x - 210, y - 150);
    const s = (Math.sin(d / 12) + 1) / 2;
    return [Math.round(60 + 180 * s), Math.round(40 + 120 * (1 - s)), Math.round(160 + 80 * s)];
  });
}

// A FIXED, pre-seeded sandbox root. The router honours WASH_FM_ROOT as the
// global fs-root, confining EVERY app to it (internal/runner/router resolves
// it via firstNonEmpty(--fs-root, WASH_FS_ROOT, WASH_FM_ROOT)). The fixture's
// own fmRoot is a random tmpdir, which means imageview can't be pointed at a
// known folder inside it — so we use a stable root we seed here at module load
// and hand the router via WASH_FM_ROOT below. Workers=1 ⇒ no cross-run races.
const ROOT = join(tmpdir(), 'wash-shot-root');
mkdirSync(ROOT, { recursive: true });
seedShowcase(ROOT);
const PICS = join(ROOT, 'Pictures');

// Every app to stage into the per-test apps dir. NOTE: passing `apps` REPLACES
// the fixture's default core set, so the shell's own apps (session/about/notify)
// must be listed explicitly or the desktop never mounts. netd is the net app's
// backend singleton (auto-spawned when its binary is present).
const STAGE = ['session', 'about', 'test', 'notify',
  'fm', 'term', 'edit', 'washamp', 'net', 'netd', 'settings', 'top',
  'services', 'packages', 'disks', 'journal', 'display',
  'radio', 'imageview', 'connect', 'remote'] as const;

// Theme each app shot with a different pack so the README grid shows the
// range. The montage + a few apps stay on the default (Midnight) dark.
// Packs: midnight (default dark), tokyo (neon), seoul (light paper),
// copland (Mac OS 9), oslo (cool slate). See web/lib/src/packs.ts.
const THEME: Record<string, string> = {
  edit: 'tokyo', about: 'tokyo', radio: 'tokyo',
  settings: 'seoul', services: 'seoul', imageview: 'seoul',
  packages: 'copland', connect: 'copland',
  net: 'oslo', top: 'oslo', disks: 'oslo',
};

test.describe('screenshots', () => {
  test.use({ routerOpts: {
    apps: [...STAGE], xdgConfig: true,
    extraEnv: {
      // Fixed, pre-seeded fs-root (sandbox) shared by every app this run, so
      // imageview can be pointed at a known folder inside it.
      WASH_FM_ROOT: ROOT,
      WASH_IMAGEVIEW_DIR: PICS,
      // Deterministic LAN peers for the Connect "On your network" shot; the
      // no-advertise flag keeps headless runs from broadcasting mDNS. Only the
      // remote/connect apps read these — harmless for every other shot.
      WASH_DISCOVERY_STATIC: 'labbox=10.42.0.9:2222,prod-db=10.42.0.10,build01=10.42.0.11',
      WASH_DISCOVERY_NO_ADVERTISE: '1',
    },
  } });

  // --- helpers ---------------------------------------------------------------
  // Seed the active theme pack into the isolated desktop.json BEFORE the
  // browser connects — wash-session reads it at spawn and the shell applies
  // the scheme to the document root, so every app window re-skins.
  function setPack(xdgConfigHome: string, packId: string): void {
    if (!xdgConfigHome) return;
    const dir = join(xdgConfigHome, 'wash');
    mkdirSync(dir, { recursive: true });
    writeFileSync(join(dir, 'desktop.json'), JSON.stringify({ pack: packId }));
  }
  async function bootDesktop(page: Page, url: string): Promise<void> {
    await page.goto(url);
    await expect(page.locator('wash-app-session')).toBeVisible();
  }
  // Boot themed: apply the pack assigned to `appKey` (if any) first.
  async function bootThemed(page: Page, router: { url: string; xdgConfigHome: string }, appKey: string): Promise<void> {
    if (THEME[appKey]) setPack(router.xdgConfigHome, THEME[appKey]);
    await bootDesktop(page, router.url);
  }
  async function openApp(page: Page, appId: string): Promise<void> {
    await page.locator('button[title="Apps"]').click();
    await page.locator(`[data-testid="start-menu-${appId}"]`).click();
  }
  function win(page: Page, el: string): Locator {
    return page.locator('.wash-window', { has: page.locator(el) });
  }
  async function settle(page: Page, ms = 700): Promise<void> {
    await page.waitForTimeout(ms);
  }
  // Drag a window so its top-left lands at (x, y). No-op for chromeless
  // windows (washamp) — they have no .wash-titlebar to grab.
  async function moveWinTo(page: Page, w: Locator, x: number, y: number): Promise<void> {
    const tb = w.locator('.wash-titlebar');
    if ((await tb.count()) === 0) return;
    const b = await tb.boundingBox();
    if (!b) return;
    const grabDx = 90;
    await page.mouse.move(b.x + grabDx, b.y + b.height / 2);
    await page.mouse.down();
    await page.mouse.move(x + grabDx, y + b.height / 2, { steps: 16 });
    await page.mouse.up();
  }

  // --- the montage hero (multiple windows, naturally arranged) ---------------
  test('desktop-montage', async ({ page, router }) => {
    test.setTimeout(45_000);
    const rng = mkRng(0x5a17c0de);
    const jit = (n: number) => Math.round((rng() - 0.5) * n); // seeded natural jitter
    await bootDesktop(page, router.url);

    // Terminal first, while it's the only window — focus is unambiguous so
    // typing lands cleanly. Tidy the prompt (cd ~ + clear) and run a couple of
    // path-free commands; no `ls` of the harness dir.
    await openApp(page, 'com.wash.term');
    await expect(win(page, 'wash-app-term')).toBeVisible();
    await page.waitForSelector('wash-app-term .xterm-rows', { timeout: 10_000 });
    await page.locator('wash-app-term').click();
    await page.keyboard.type('cd ~ && clear\n', { delay: 8 });
    await settle(page, 250);
    await page.keyboard.type('uname -snrm\n', { delay: 10 });
    await page.keyboard.type("echo 'wash — a Linux desktop, in your browser'\n", { delay: 8 });
    await settle(page, 300);
    // Terminal to the upper-middle, clear of the (top-left) Washamp stack.
    await moveWinTo(page, win(page, 'wash-app-term'), 372 + jit(36), 70 + jit(24));

    // File manager, lower-right.
    await openApp(page, 'com.wash.fm');
    await expect(win(page, 'wash-app-fm')).toBeVisible();
    await settle(page, 300);
    await moveWinTo(page, win(page, 'wash-app-fm'), 612 + jit(48), 452 + jit(34));

    // Washamp last → frontmost & fully visible at its top-left spawn
    // (chromeless, can't be moved).
    await openApp(page, 'com.wash.washamp');
    await expect(win(page, 'wash-app-washamp')).toBeVisible();
    await settle(page, 900); // let Webamp paint its skin

    // Ensure the sidebar (widgets) is OPEN for the hero shot.
    const sidebar = page.locator('[data-testid="sidebar"]');
    if (!(await sidebar.isVisible())) {
      await page.locator('[data-testid="sidebar-toggle"]').click().catch(() => {});
      await settle(page, 450);
    }

    // Hide the desktop banner — it prints the host's real hostname and
    // interface addresses (incl. a public IPv6), which must not land in a
    // committed marketing shot. Shadow-piercing walk (session is a custom
    // element). Done last, right before the capture.
    await page.evaluate(() => {
      const hide = (root: Document | ShadowRoot) => {
        root.querySelectorAll<HTMLElement>(
          '[data-testid="desktop-banner"],[data-testid="desktop-banner-placeholder"]',
        ).forEach((el) => { el.style.display = 'none'; });
        root.querySelectorAll('*').forEach((el) => {
          if ((el as HTMLElement).shadowRoot) hide((el as HTMLElement).shadowRoot!);
        });
      };
      hide(document);
    });
    await settle(page, 150);

    await page.screenshot({ path: join(SHOTS, 'desktop-montage.png') });
  });

  // --- per-app window shots --------------------------------------------------
  test('fm', async ({ page, router }) => {
    await bootDesktop(page, router.url);
    await openApp(page, 'com.wash.fm');
    const w = win(page, 'wash-app-fm');
    await expect(w).toBeVisible();
    await settle(page);
    await w.screenshot({ path: join(SHOTS, 'fm.png') });
  });

  test('term', async ({ page, router }) => {
    test.setTimeout(25_000);
    await bootDesktop(page, router.url);
    await openApp(page, 'com.wash.term');
    const w = win(page, 'wash-app-term');
    await expect(w).toBeVisible();
    await page.waitForSelector('wash-app-term .xterm-rows', { timeout: 10_000 });
    await w.click();
    // Tidy prompt + path-free commands (don't expose the harness dir).
    await page.keyboard.type('cd ~ && clear\n', { delay: 8 });
    await settle(page, 250);
    await page.keyboard.type('uname -snrm\n', { delay: 12 });
    await page.keyboard.type('whoami\n', { delay: 12 });
    await page.keyboard.type("echo 'wash — a real pty (xterm.js) in your browser'\n", { delay: 8 });
    await settle(page);
    await w.screenshot({ path: join(SHOTS, 'term.png') });
  });

  test('edit', async ({ page, router }) => {
    await bootThemed(page, router, 'edit');
    await openApp(page, 'com.wash.edit');
    const w = win(page, 'wash-app-edit');
    await expect(w).toBeVisible();
    // Open a file so the editor pane has content (best-effort — the entry
    // testid depends on the editor's sandbox root matching the fm seed).
    await w.locator('[data-testid="edit-entry-Projects"]').dblclick().catch(() => {});
    await w.locator('[data-testid="edit-entry-hello.go"]').dblclick().catch(() => {});
    await settle(page);
    await w.screenshot({ path: join(SHOTS, 'edit.png') });
  });

  test('music', async ({ page, router }) => {
    await bootDesktop(page, router.url);
    await openApp(page, 'com.wash.washamp');
    const w = win(page, 'wash-app-washamp');
    await expect(w).toBeVisible();
    await settle(page, 1200); // let Webamp paint its skin
    await w.screenshot({ path: join(SHOTS, 'music.png') });
  });

  test('settings', async ({ page, router }) => {
    await bootThemed(page, router, 'settings');
    await openApp(page, 'com.wash.settings');
    const w = win(page, 'wash-app-settings');
    await expect(w).toBeVisible();
    await settle(page);
    await w.screenshot({ path: join(SHOTS, 'settings.png') });
  });

  test('net', async ({ page, router }) => {
    await bootThemed(page, router, 'net');
    await openApp(page, 'com.wash.net');
    const w = win(page, 'wash-app-net');
    await expect(w).toBeVisible();
    // The Interfaces tab shows real content (links/addresses); the default
    // Networks tab is empty on a fresh sandbox.
    await w.getByText('Interfaces', { exact: true }).first().click().catch(() => {});
    await settle(page, 1200); // let it enumerate interfaces
    await w.screenshot({ path: join(SHOTS, 'net.png') });
  });

  test('top', async ({ page, router }) => {
    await bootThemed(page, router, 'top');
    await openApp(page, 'com.wash.top');
    const w = win(page, 'wash-app-top');
    await expect(w).toBeVisible();
    await w.locator('[data-testid="top-statusbar"]').waitFor({ timeout: 8000 }).catch(() => {});
    await settle(page, 900); // let a couple of /proc samples land
    await w.screenshot({ path: join(SHOTS, 'top.png') });
  });

  test('services', async ({ page, router }) => {
    await bootThemed(page, router, 'services');
    await openApp(page, 'com.wash.services');
    const w = win(page, 'wash-app-services');
    await expect(w).toBeVisible();
    await w.locator('[data-testid="srv-list"]').waitFor({ timeout: 8000 }).catch(() => {});
    await settle(page, 700);
    await w.screenshot({ path: join(SHOTS, 'services.png') });
  });

  test('packages', async ({ page, router }) => {
    await bootThemed(page, router, 'packages');
    await openApp(page, 'com.wash.packages');
    const w = win(page, 'wash-app-packages');
    await expect(w).toBeVisible();
    await w.locator('[data-testid="pkg-status"]').waitFor({ timeout: 8000 }).catch(() => {});
    await settle(page, 700);
    await w.screenshot({ path: join(SHOTS, 'packages.png') });
  });

  test('disks', async ({ page, router }) => {
    await bootThemed(page, router, 'disks');
    await openApp(page, 'com.wash.disks');
    const w = win(page, 'wash-app-disks');
    await expect(w).toBeVisible();
    await w.locator('[data-testid="disks-list"]').waitFor({ timeout: 8000 }).catch(() => {});
    await settle(page, 900); // let it scan block devices
    await w.screenshot({ path: join(SHOTS, 'disks.png') });
  });

  test('radio', async ({ page, router }) => {
    await bootThemed(page, router, 'radio');
    await openApp(page, 'com.wash.radio');
    const w = win(page, 'wash-app-radio');
    await expect(w).toBeVisible();
    await w.locator('[data-testid="station-list"]').waitFor({ timeout: 8000 }).catch(() => {});
    await settle(page, 700);
    await w.screenshot({ path: join(SHOTS, 'radio.png') });
  });

  test('about', async ({ page, router }) => {
    await bootThemed(page, router, 'about');
    await openApp(page, 'com.wash.about');
    const w = win(page, 'wash-app-about');
    await expect(w).toBeVisible();
    await settle(page, 900); // host facts + the live runtime process table
    await w.screenshot({ path: join(SHOTS, 'about.png') });
  });

  test('imageview', async ({ page, router }) => {
    await bootThemed(page, router, 'imageview');
    await openApp(page, 'com.wash.imageview');
    const w = win(page, 'wash-app-imageview');
    await expect(w).toBeVisible();
    // The viewer auto-scans ~/Pictures (seeded above); open the first image.
    await w.locator('[data-testid="iv-list"]').waitFor({ timeout: 8000 }).catch(() => {});
    await w.locator('[data-testid^="iv-thumb-"]').first().click().catch(() => {});
    await settle(page, 900); // let the full image + thumbnails paint
    await w.screenshot({ path: join(SHOTS, 'imageview.png') });
  });

  // Connect / Remote — the "On your network" mDNS discovery list, fed by the
  // deterministic WASH_DISCOVERY_STATIC seam set in routerOpts above.
  test('connect', async ({ page, router }) => {
    await bootThemed(page, router, 'connect');
    await openApp(page, 'com.wash.connect');
    const w = win(page, 'wash-app-connect');
    await expect(w).toBeVisible();
    // Discovery is async; wait for the candidate list to populate.
    await w.locator('[data-testid="connect-candidates"]').waitFor({ timeout: 12000 }).catch(() => {});
    await settle(page, 800);
    await w.screenshot({ path: join(SHOTS, 'connect.png') });
  });

  // --- the display compositor (best-effort: needs wash-display + an X client) -
  // Real X11 over the native compositor. Timing matters (DISPLAY_ENV.md §5): the
  // shell reads DISPLAY at spawn time, so we wait for the autostarted compositor
  // to bring up Xwayland AND publish its env BEFORE launching the terminal.
  test('display', async ({ page, router }) => {
    const reason = displaySkipReason() ?? (existsSync('/usr/bin/xclock') ? null : '/usr/bin/xclock not installed');
    test.skip(reason !== null, reason ?? '');
    test.setTimeout(60_000);
    await bootDesktop(page, router.url);

    await router.waitForLog(/Xwayland ready on DISPLAY=/, 25_000);
    await router.waitForLog(/env\.publish from /, 25_000);

    await openApp(page, 'com.wash.term');
    await page.waitForSelector('wash-app-term .xterm-rows', { timeout: 10_000 });
    await win(page, 'wash-app-term').click();
    await page.keyboard.type('xclock\n');

    await router.waitForLog(/window\.create .*element="wash-app-display"/, 20_000);
    const display = win(page, 'wash-app-display');
    await expect(display).toBeVisible({ timeout: 20_000 });
    await settle(page, 1200);
    await display.screenshot({ path: join(SHOTS, 'display.png') });
  });
});
