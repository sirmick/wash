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
import { mkdirSync, writeFileSync, existsSync, readdirSync, copyFileSync, rmSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { tmpdir, homedir } from 'node:os';
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
  // Tracks for the native Music player shot: medialib titles are filename
  // stems, so empty files with good names are a full library to the list.
  for (const t of [
    '01 Neon Skyline.mp3', '02 Cassette Sunset.mp3', '03 Midnight Drive.mp3',
    '04 Signal Path.mp3', '05 Copper Wire.flac', '06 Terminal Velocity.mp3',
    '07 Low Orbit.ogg', '08 Afterglow.mp3',
  ]) writeFileSync(join(root, 'Music', t), '');
  writeFileSync(join(root, '.profile'), 'export EDITOR=wash-edit\n');
  seedPictures(join(root, 'Pictures'));
}

// Populate the Image Viewer's folder. Prefer the user's REAL ~/Pictures (an
// even spread so the grid isn't ten near-identical frames), copied INTO the
// sandbox root so the fs-root confinement still lets imageview read them
// (WASH_IMAGEVIEW_DIR points here). Falls back to generated images on a box
// with no ~/Pictures so the capture never breaks.
function seedPictures(pics: string): void {
  try {
    const dir = join(homedir(), 'Pictures');
    const all = readdirSync(dir).filter((n) => /\.(jpe?g|png|gif|webp)$/i.test(n)).sort();
    if (all.length) {
      const want = Math.min(9, all.length);
      for (let i = 0; i < want; i++) {
        const name = all[Math.floor((i * all.length) / want)];
        copyFileSync(join(dir, name), join(pics, name));
      }
      return;
    }
  } catch { /* no ~/Pictures — fall through to generated */ }

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
rmSync(ROOT, { recursive: true, force: true }); // fresh each run — no stale files from a prior capture
mkdirSync(ROOT, { recursive: true });
seedShowcase(ROOT);
const PICS = join(ROOT, 'Pictures');

// The Agent shots drive the real com.wash.ai window against the e2e fake
// ACP adapter (staged on PATH as codex-acp), with its replies posed via the
// ACP_FAKE_SCRIPT seam — the UI, transcript renderer, status bar and agentd
// plumbing in the shot are all real; only the words are scripted.
const FAKE_DIR = join(dirname(fileURLToPath(import.meta.url)), '..', '..', 'out', 'e2e');
const AGENT_SCRIPTS = join(tmpdir(), 'wash-shot-agent');
mkdirSync(AGENT_SCRIPTS, { recursive: true });
const MONTAGE_SCRIPT = join(AGENT_SCRIPTS, 'montage.json');
writeFileSync(MONTAGE_SCRIPT, JSON.stringify([
  'Tidied **Downloads** — 34 files, 2.1 GB reclaimed:\n\n' +
  '- 21 stale installers → `Archive/2026-07/`\n' +
  '- 9 duplicate images removed (kept the largest of each)\n' +
  '- 4 PDFs you opened this week left in place\n\n' +
  'Nothing was deleted outside `~/Downloads`. Want me to do this weekly?\n',
]));
const AGENT_SCRIPT = join(AGENT_SCRIPTS, 'agent.json');
writeFileSync(AGENT_SCRIPT, JSON.stringify([
  'The flake is in `checkout_test.go` — the test asserts on wall-clock order:\n\n' +
  '```go\nif events[0].At.After(events[1].At) {\n\tt.Fatal("out of order")\n}\n```\n\n' +
  '- Both events are stamped in the same millisecond on fast runners\n' +
  '- The queue only guarantees *delivery* order, not timestamp order\n\n' +
  'Comparing sequence numbers instead of timestamps fixes it.\n',
  'Done — `checkout_test.go` now compares `events[i].Seq`. ' +
  'The full package is green:\n\n' +
  '| Package | Result |\n| --- | ---: |\n| store/checkout | ok · 41 tests |\n| store/cart | ok · 17 tests |\n',
]));

// Every app to stage into the per-test apps dir. NOTE: passing `apps` REPLACES
// the fixture's default core set, so the shell's own apps (session/about/notify)
// must be listed explicitly or the desktop never mounts. netd is the net app's
// backend singleton (auto-spawned when its binary is present).
const STAGE = ['session', 'about', 'test', 'notify',
  'fm', 'term', 'edit', 'washamp', 'music', 'audio', 'net', 'netd', 'settings', 'top',
  'services', 'packages', 'disks', 'journal', 'display',
  'radio', 'imageview', 'connect', 'remote', 'ai', 'agentd'] as const;

// Theme each app shot with a different pack so the README grid shows the
// range. The montage + a few apps stay on the default (Midnight) dark.
// Packs: midnight (default dark), tokyo (neon), seoul (light paper),
// copland (Mac OS 9), oslo (cool slate). See web/lib/src/packs.ts.
const THEME: Record<string, string> = {
  edit: 'tokyo', about: 'tokyo', radio: 'tokyo',
  settings: 'seoul', services: 'seoul', imageview: 'seoul', music: 'seoul',
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
      // The native Music player's library root (medialib.DefaultDir) — it
      // auto-scans this on first launch, so the shot needs no picker dance.
      WASH_MUSIC_DIR: join(ROOT, 'Music'),
      // The montage's Agent window: fake adapter on PATH + posed replies.
      PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}`,
      ACP_FAKE_SCRIPT: MONTAGE_SCRIPT,
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
  // Drag the bottom-right grip so the window ends up width × height. Used
  // where a default-sized window would leave the shot mostly empty pane.
  async function resizeWinTo(page: Page, w: Locator, width: number, height: number): Promise<void> {
    const grip = w.locator('[data-testid="window-resize"]');
    const g = await grip.boundingBox();
    const b = await w.boundingBox();
    if (!g || !b) return;
    await page.mouse.move(g.x + g.width / 2, g.y + g.height / 2);
    await page.mouse.down();
    await page.mouse.move(b.x + width, b.y + height, { steps: 12 });
    await page.mouse.up();
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

    // The Agent — wash's day-one AI seat — lower-left, mid-conversation
    // (the fake adapter on PATH answers with the posed MONTAGE_SCRIPT reply).
    await openApp(page, 'com.wash.ai');
    const ai = win(page, 'wash-app-ai');
    await expect(ai).toBeVisible();
    await ai.locator('select').first().selectOption('codex');
    await ai.getByRole('button', { name: 'Start session' }).click();
    const composer = ai.locator('textarea');
    await expect(composer).toBeVisible({ timeout: 20_000 });
    await composer.fill('Tidy my Downloads folder — archive anything older than a month.');
    await composer.press('Enter');
    await expect(ai.getByText(/Want me to do this weekly\?/)).toBeVisible({ timeout: 20_000 });
    // Let the turn finish before posing it — a "working… / Stop" composer
    // in the hero shot reads as a hung agent.
    await expect(ai.locator('[data-testid="agent-stop"]')).toHaveCount(0, { timeout: 20_000 });
    await resizeWinTo(page, ai, 680, 340);
    await moveWinTo(page, ai, 40 + jit(24), 596 + jit(18));

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

  test('washamp', async ({ page, router }) => {
    await bootDesktop(page, router.url);
    await openApp(page, 'com.wash.washamp');
    const w = win(page, 'wash-app-washamp');
    await expect(w).toBeVisible();
    await settle(page, 1200); // let Webamp paint its skin
    await w.screenshot({ path: join(SHOTS, 'washamp.png') });
  });

  test('music', async ({ page, router }) => {
    await bootThemed(page, router, 'music');
    await openApp(page, 'com.wash.music');
    const w = win(page, 'wash-app-music');
    await expect(w).toBeVisible();
    // WASH_MUSIC_DIR points at the seeded library; the first-launch scan
    // fills the track list on its own.
    await expect(w.locator('[data-testid="track-list"]')).toContainText('Neon Skyline');
    await settle(page);
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

  // --- the display compositor: a FULL-DESKTOP shot with real X11 clients ------
  // Chromium (a real browser) + xclock are launched from a wash terminal and
  // composited into native wash windows by wash-display. Timing matters
  // (DISPLAY_ENV.md §5): the shell reads DISPLAY at spawn time, so we wait for
  // the autostarted compositor to bring up Xwayland AND publish its env BEFORE
  // launching the terminal. Captures the whole page, not one window.
  const chromiumBin = ['/snap/bin/chromium', '/usr/bin/chromium', '/usr/bin/chromium-browser']
    .find((p) => existsSync(p));
  test('display', async ({ page, router }) => {
    const reason = displaySkipReason()
      ?? (existsSync('/usr/bin/xclock') ? null : '/usr/bin/xclock not installed')
      ?? (chromiumBin ? null : 'chromium not installed');
    test.skip(reason !== null, reason ?? '');
    test.setTimeout(160_000);
    await bootDesktop(page, router.url);

    await router.waitForLog(/Xwayland ready on DISPLAY=/, 25_000);
    await router.waitForLog(/env\.publish from /, 25_000);

    const term = win(page, 'wash-app-term');
    await openApp(page, 'com.wash.term');
    await page.waitForSelector('wash-app-term .xterm-rows', { timeout: 10_000 });
    await moveWinTo(page, term, 40, 40); // terminal top-left, clear of the rest

    // wash-display windows are chromeless (no wash titlebar) so they can't be
    // dragged after spawn — we control the layout by SPAWN ORDER (last mapped
    // sits on top) and chromium's --window-position. Launch BOTH from a SINGLE
    // typed line so we never have to re-click the terminal once chromium covers
    // it: chromium first, a short sleep, then xclock — so xclock maps last and
    // stays visible on top.
    const html =
      'data:text/html,<body style=margin:0;height:100vh;background:%230b1020;color:%23e6edf3;' +
      'font-family:system-ui;display:grid;place-items:center>' +
      '<div style=text-align:center><div style=font-size:46px;font-weight:600>wash</div>' +
      '<div style=font-size:17px;opacity:.55;margin-top:10px>a real browser, composited into a real window</div></div></body>';
    const chrome =
      `${chromiumBin} --ozone-platform=x11 --disable-gpu --no-first-run ` +
      `--no-default-browser-check --hide-crash-restore-bubble ` +
      `--window-position=300,235 --window-size=940,600 ` +
      `--user-data-dir=/tmp/wash-shot-chrome '${html}'`;
    await term.click();
    // Fresh chromium profile (no "Restore pages?" bubble) → chromium → sleep →
    // xclock on top, all in one line.
    await page.keyboard.type(
      `rm -rf /tmp/wash-shot-chrome; ${chrome} >/dev/null 2>&1 & ` +
      `sleep 4; xclock -geometry 190x190 &\n`);

    // Wait for both display windows (chromium + xclock), then let them paint.
    await expect(async () => {
      expect(await win(page, 'wash-app-display').count()).toBeGreaterThanOrEqual(2);
    }).toPass({ timeout: 100_000 });
    await settle(page, 5000);

    // Sidebar open + hide the host banner (privacy), like the montage.
    const sidebar = page.locator('[data-testid="sidebar"]');
    if (!(await sidebar.isVisible())) {
      await page.locator('[data-testid="sidebar-toggle"]').click().catch(() => {});
      await settle(page, 400);
    }
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
    await settle(page, 400);

    await page.screenshot({ path: join(SHOTS, 'display.png') });
    // No in-test cleanup: router teardown kills wash-term, whose PTY hangup
    // takes the backgrounded chromium with it. The next run's `rm -rf` +
    // --hide-crash-restore-bubble handle any dirty-profile leftover.
  });
});

// --- the Agent window, in its own router -------------------------------------
// Separate describe: this one must NOT be confined by WASH_FM_ROOT — the
// folder picked in the launcher becomes the adapter's real working
// directory, so it has to be a genuine host path.
test.describe('agent screenshot', () => {
  const PROJ = join(tmpdir(), 'wash-shot-agent', 'webshop');
  rmSync(PROJ, { recursive: true, force: true });
  mkdirSync(join(PROJ, 'store'), { recursive: true });
  writeFileSync(join(PROJ, 'go.mod'), 'module example.com/webshop\n\ngo 1.24\n');
  writeFileSync(join(PROJ, 'store', 'checkout_test.go'),
    'package store\n\n// posed fixture for the screenshot\n');

  test.use({ routerOpts: {
    apps: ['session', 'about', 'notify', 'ai', 'agentd'],
    extraEnv: {
      PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}`,
      ACP_FAKE_SCRIPT: AGENT_SCRIPT,
    },
  } });

  test('agent', async ({ page, router }) => {
    test.setTimeout(60_000);
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu-com.wash.ai"]').click();
    const w = page.locator('.wash-window', { has: page.locator('wash-app-ai') });
    await expect(w).toBeVisible();

    // Point the session at the posed project folder, then start.
    await w.getByRole('button', { name: 'Choose…' }).click();
    const picker = page.locator('[data-testid="ai-folder-picker"]');
    await expect(picker).toBeVisible();
    const bar = picker.locator('[data-testid="fp-path"]');
    await bar.click();
    await bar.fill(PROJ);
    await bar.press('Enter');
    await picker.locator('[data-testid="fp-confirm"]').click();
    await expect(picker).toBeHidden();
    await w.locator('select').first().selectOption('codex');
    await w.getByRole('button', { name: 'Start session' }).click();
    const composer = w.locator('textarea');
    await expect(composer).toBeVisible({ timeout: 20_000 });

    // Two posed turns (AGENT_SCRIPT replies): diagnose, then fix + verify.
    await composer.fill('Why is the checkout test flaky?');
    await composer.press('Enter');
    await expect(w.getByText('Comparing sequence numbers')).toBeVisible({ timeout: 20_000 });
    await composer.fill('Fix it and re-run the tests.');
    await composer.press('Enter');
    await expect(w.getByText('store/checkout')).toBeVisible({ timeout: 20_000 });
    // The turn must be OVER before the shot — otherwise the composer row
    // shows the "working… / Stop" state, which reads as a hung agent.
    await expect(w.locator('[data-testid="agent-stop"]')).toHaveCount(0, { timeout: 20_000 });

    // Size the window to the conversation: at the default height the
    // transcript is half empty pane.
    const grip = w.locator('[data-testid="window-resize"]');
    const g = await grip.boundingBox();
    const b = await w.boundingBox();
    if (g && b) {
      await page.mouse.move(g.x + g.width / 2, g.y + g.height / 2);
      await page.mouse.down();
      await page.mouse.move(b.x + 760, b.y + 530, { steps: 12 });
      await page.mouse.up();
    }
    await page.waitForTimeout(700);
    await w.screenshot({ path: join(SHOTS, 'agent.png') });
  });
});
