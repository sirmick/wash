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
import { closeButtonOf, freshHistory, openAgents, startAgentSession, windowOf } from '../fixtures/agents';
import type { Page, Locator } from '@playwright/test';
import { mkdirSync, writeFileSync, existsSync, readdirSync, copyFileSync, rmSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { tmpdir, homedir } from 'node:os';
import { fileURLToPath } from 'node:url';
import { deflateSync } from 'node:zlib';
import { execSync } from 'node:child_process';

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

// The display shot's dev server: a tiny storefront a person might be
// building, so the Chromium window previews something real.
const WEBSHOP_MAIN = `package main

import (
	"fmt"
	"log"
	"net/http"
)

const page = \`<!doctype html><title>webshop</title>
<style>
body{margin:0;font:16px system-ui;background:#f6f1e7;color:#2b2118}
header{padding:20px 32px;background:#2b2118;color:#f6f1e7;font-weight:700;font-size:22px}
main{padding:28px 32px;display:grid;gap:16px}
.item{display:flex;justify-content:space-between;background:#fff;padding:16px 20px;border-radius:10px}
.total{font-weight:700;font-size:20px;text-align:right}
button{font:inherit;padding:10px 18px;border:0;border-radius:8px;background:#c2410c;color:#fff}
</style>
<header>webshop · dev</header>
<main>
<div class=item><span>Sencha tea × 2</span><span>$9.00</span></div>
<div class=item><span>Stoneware cup × 1</span><span>$12.00</span></div>
<div class=total>Total $21.00</div>
<div style=text-align:right><button>Check out</button></div>
</main>\`

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, page) })
	log.Println("listening on :18080")
	log.Fatal(http.ListenAndServe(":18080", nil))
}
`;

// The Agent shots start a session from the real Agents manager
// (com.wash.agents) and pose the com.wash.ai controller window agentd opens
// for it, against the e2e fake ACP adapter (staged on PATH as codex-acp), with its replies posed via the
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
  'radio', 'imageview', 'connect', 'remote', 'agents', 'ai', 'agentd'] as const;

// Theme each app shot with a different pack so the README grid shows the
// range. The montage + a few apps stay on the default (Midnight) dark.
// Packs: midnight (default dark), tokyo (neon), seoul (light paper),
// copland (Mac OS 9), oslo (cool slate), lisbon (whitewash + cobalt light),
// isfahan (lapis night), moscow (Constructivist light). See
// web/lib/src/packs.ts.
const THEME: Record<string, string> = {
  edit: 'tokyo', about: 'tokyo',
  settings: 'seoul', imageview: 'seoul',
  fm: 'lisbon', services: 'lisbon',
  packages: 'copland', connect: 'copland',
  net: 'oslo', disks: 'oslo',
  top: 'isfahan', radio: 'isfahan',
  music: 'moscow',
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
      // Deterministic LAN peers for the Connect "On your network" shot. NO_MDNS
      // stops it browsing the real LAN — otherwise the capture host's actual
      // neighbours (names + addresses) land in a public screenshot — and
      // no-advertise keeps headless runs from broadcasting. Only the
      // remote/connect apps read these — harmless for every other shot.
      WASH_DISCOVERY_STATIC: 'labbox=10.42.0.9:2222,prod-db=10.42.0.10,build01=10.42.0.11,' +
        'nas=10.42.0.4,pi-garage=10.42.0.23,router=10.42.0.1,media=10.42.0.12',
      WASH_DISCOVERY_NO_MDNS: '1',
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

  async function hideDesktopBanner(page: Page): Promise<void> {
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
    // Sized at the top-left (where its grip is reachable), then posed: at
    // its default size it would run under the open sidebar and the taskbar.
    await moveWinTo(page, win(page, 'wash-app-fm'), 40, 40);
    await resizeWinTo(page, win(page, 'wash-app-fm'), 630, 490);
    await moveWinTo(page, win(page, 'wash-app-fm'), 612 + jit(48), 452 + jit(34));

    // The Agent — wash's day-one AI seat — lower-left, mid-conversation
    // (the fake adapter on PATH answers with the posed MONTAGE_SCRIPT reply).
    // A session starts from the Agents manager, and what the shot poses is
    // the controller window agentd opens for it; the manager is closed once
    // it has done that, so the hero shows the conversation, not the launcher.
    const aiApp = await startAgentSession(page,
      'Tidy my Downloads folder — archive anything older than a month.');
    const ai = windowOf(page, aiApp);
    // Raised first: the controller agentd just opened sits over it.
    const manager = await openAgents(page);
    await closeButtonOf(page, manager).click();
    await expect(manager).toHaveCount(0, { timeout: 10_000 });
    await expect(ai.getByText(/Want me to do this weekly\?/)).toBeVisible({ timeout: 20_000 });
    // Let the turn finish before posing it — a "working… / Stop" composer
    // in the hero shot reads as a hung agent.
    await expect(ai.locator('[data-testid="agent-stop"]')).toHaveCount(0, { timeout: 20_000 });
    // Up to the top-left first: agentd opens the controller cascaded below
    // the manager, where its resize grip is under the taskbar.
    await moveWinTo(page, ai, 40, 40);
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
    await bootThemed(page, router, 'fm');
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

  // The same Files window under three packs that change shape as well as
  // colour, for the site's themes section (theme-<pack>.png).
  for (const pack of ['moscow', 'lisbon', 'copland']) {
    test(`theme-${pack}`, async ({ page, router }) => {
      setPack(router.xdgConfigHome, pack);
      await bootDesktop(page, router.url);
      await openApp(page, 'com.wash.fm');
      const w = win(page, 'wash-app-fm');
      await expect(w).toBeVisible();
      await w.locator('[data-testid="fm-entry-Projects"]').click().catch(() => {});
      await moveWinTo(page, w, 120, 90);
      await resizeWinTo(page, w, 900, 600);
      const sidebar = page.locator('[data-testid="sidebar"]');
      if (await sidebar.isVisible()) {
        await page.locator('[data-testid="sidebar-toggle"]').click().catch(() => {});
      }
      await hideDesktopBanner(page);
      await settle(page);
      // Window, wallpaper and taskbar together: a pack is all three.
      await page.screenshot({ path: join(SHOTS, `theme-${pack}.png`), clip: { x: 0, y: 40, width: 1180, height: 960 } });
    });
  }

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

  // --- the display compositor: a FULL-DESKTOP shot with a real X11 client ------
  // A dev server started from a wash terminal, and Chromium previewing it,
  // composited into a native wash window by wash-display. Timing matters
  // (DISPLAY_ENV.md §5): the shell reads DISPLAY at spawn time, so we wait for
  // the autostarted compositor to bring up Xwayland AND publish its env BEFORE
  // launching the terminal. Captures the whole page, not one window.
  const chromiumBin = ['/snap/bin/chromium', '/usr/bin/chromium', '/usr/bin/chromium-browser']
    .find((p) => existsSync(p));
  test('display', async ({ page, router }) => {
    const reason = displaySkipReason()
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

    // The practical case: a web app's dev server running on this machine,
    // previewed in a real Chromium in a wash window, no port forwarding.
    const site = join(ROOT, 'Projects', 'webshop-web');
    mkdirSync(site, { recursive: true });
    writeFileSync(join(site, 'go.mod'), 'module example.com/webshop-web\n\ngo 1.24\n');
    writeFileSync(join(site, 'main.go'), WEBSHOP_MAIN);
    // The visible command is a plain `chromium <url>`; a shell function
    // defined before the `clear` adds the flags a headless capture needs.
    // --no-sandbox: containers (Incus, docker) deny the user-namespace
    // sandbox and chromium aborts at startup; --test-type hides the
    // "unsupported flag" infobar --no-sandbox would draw.
    const chromeFlags =
      '--ozone-platform=x11 --disable-gpu --no-first-run --no-sandbox --test-type ' +
      '--no-default-browser-check --hide-crash-restore-bubble --disable-features=SigninInterception ' +
      '--guest --user-data-dir=/tmp/wash-shot-chrome';
    await term.click();
    await page.keyboard.type(
      `rm -rf /tmp/wash-shot-chrome; cd ${site} && chromium() { ${chromiumBin} ${chromeFlags} "$@" >/dev/null 2>&1; } && ` +
      `PS1='webshop-web $ ' && PROMPT_COMMAND= && printf '\\033]0;webshop-web\\007' && clear\n`, { delay: 2 });
    await settle(page, 300);
    await page.keyboard.type('go run . &\n', { delay: 10 });
    await expect(term).toContainText('listening on :18080', { timeout: 60_000 });
    await page.keyboard.type('chromium http://localhost:18080/ &\n', { delay: 10 });

    // Wait for chromium's window, then let the page paint.
    await expect(async () => {
      expect(await win(page, 'wash-app-display').count()).toBeGreaterThanOrEqual(1);
    }).toPass({ timeout: 100_000 });
    // Terminal and browser side by side.
    await page.evaluate(() => {
      for (const el of ['wash-app-display', 'wash-app-term']) {
        const w = window.wash.windows().find((x) => x.element === el);
        if (w) window.wash.focusWindow(w.windowID, w.origin);
      }
      window.wash.setCellLayout(0, 0, 'r[1@38 1]');
    });
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

// --- one bug, four apps: the IDE story (docs/WM_LAYOUT.md) ------------------
// Its own router, confined to a small Go project with a real off-by-one in
// it. The terminal runs the project's real tests, the file reaches the agent
// by a real drag from Files, and the editor reloads the fix from disk; only
// the agent's words are scripted, and the capture writes the fix it
// describes. One frame per step (ide-1..4) plus the whole tiled desktop at
// the end (ide-tiled), which the site uses as its hero.
test.describe('ide screenshot', () => {
  const IDE_ROOT = join(tmpdir(), 'webshop');
  const CART = join(IDE_ROOT, 'store', 'cart.go');
  const cartGo = (loop: string) =>
    'package store\n\n// Cart holds line items, priced in cents.\ntype Cart struct {\n\titems []Item\n}\n\n' +
    '// Item is one line in a cart.\ntype Item struct {\n\tSKU   string\n\tPrice int\n\tQty   int\n}\n\n' +
    '// NewCart returns an empty cart.\nfunc NewCart() *Cart { return &Cart{} }\n\n' +
    '// Add puts qty of a SKU in the cart at price cents each.\n' +
    'func (c *Cart) Add(sku string, price, qty int) {\n\tc.items = append(c.items, Item{sku, price, qty})\n}\n\n' +
    '// Total is the cart\'s value in cents.\nfunc (c *Cart) Total() int {\n\tt := 0\n' + loop + '\treturn t\n}\n';
  const BUGGY = '\tfor i := 1; i < len(c.items); i++ {\n\t\tt += c.items[i].Price * c.items[i].Qty\n\t}\n';
  const FIXED = '\tfor _, it := range c.items {\n\t\tt += it.Price * it.Qty\n\t}\n';
  rmSync(IDE_ROOT, { recursive: true, force: true });
  mkdirSync(join(IDE_ROOT, 'store'), { recursive: true });
  writeFileSync(join(IDE_ROOT, 'go.mod'), 'module example.com/webshop\n\ngo 1.24\n');
  writeFileSync(join(IDE_ROOT, 'README.md'), '# webshop\n\nA small shop backend.\n');
  writeFileSync(join(IDE_ROOT, 'main.go'),
    'package main\n\nimport (\n\t"fmt"\n\n\t"example.com/webshop/store"\n)\n\n' +
    'func main() {\n\tc := store.NewCart()\n\tc.Add("tea", 450, 2)\n\tfmt.Println(c.Total())\n}\n');
  writeFileSync(CART, cartGo(BUGGY));
  writeFileSync(join(IDE_ROOT, 'store', 'cart_test.go'),
    'package store\n\nimport "testing"\n\n' +
    'func TestTotal(t *testing.T) {\n\tc := NewCart()\n\tc.Add("tea", 450, 2)\n\tc.Add("cup", 1200, 1)\n' +
    '\tif got := c.Total(); got != 2100 {\n\t\tt.Fatalf("Total = %d, want 2100", got)\n\t}\n}\n\n' +
    'func TestEmptyCart(t *testing.T) {\n\tif got := NewCart().Total(); got != 0 {\n' +
    '\t\tt.Fatalf("Total = %d, want 0", got)\n\t}\n}\n');
  const IDE_SCRIPT = join(AGENT_SCRIPTS, 'ide.json');
  writeFileSync(IDE_SCRIPT, JSON.stringify([
    '`Total` starts counting at index **1**, so the first line in the cart is never added: ' +
    'tea is 2 × 450 = 900, and 2100 − 900 is the 1200 the test saw.\n\n' +
    'Fixed in `store/cart.go`: the loop now ranges over every item.\n\n' +
    '```go\nfor _, it := range c.items {\n\tt += it.Price * it.Qty\n}\n```\n\n' +
    'Re-run `go test ./store` and both tests pass.\n',
  ]));

  test.use({ routerOpts: {
    apps: ['session', 'about', 'notify', 'fm', 'term', 'edit', 'agents', 'ai', 'agentd'],
    extraEnv: {
      WASH_FM_ROOT: IDE_ROOT,
      PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}`,
      ACP_FAKE_SCRIPT: IDE_SCRIPT,
    },
  } });

  test('ide-tiled', async ({ page, router }) => {
    test.setTimeout(150_000);
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    const win = (el: string) => page.locator('.wash-window', { has: page.locator(el) });
    const open = async (appId: string) => {
      await page.locator('button[title="Apps"]').click();
      await page.locator(`[data-testid="start-menu-${appId}"]`).click();
    };
    const focus = (els: string[]) => page.evaluate((els) => {
      for (const el of els) {
        const w = window.wash.windows().find((x) => x.element === el);
        if (w) window.wash.focusWindow(w.windowID, w.origin);
      }
    }, els);
    // Hide the desktop banner: it prints the host's real name and addresses.
    const hideBanner = () => page.evaluate(() => {
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
    // A frame of the story: the union of some windows' boxes, page-clipped.
    const frame = async (name: string, wins: Locator[], maxHeight = Infinity) => {
      await hideBanner();
      await page.waitForTimeout(400);
      const boxes = (await Promise.all(wins.map((w) => w.boundingBox()))).filter((b) => b !== null);
      const x = Math.min(...boxes.map((b) => b.x)), y = Math.min(...boxes.map((b) => b.y));
      const r = Math.max(...boxes.map((b) => b.x + b.width)), btm = Math.max(...boxes.map((b) => b.y + b.height));
      await page.screenshot({ path: join(SHOTS, name), clip: { x, y, width: r - x, height: Math.min(btm - y, maxHeight) } });
    };

    // Terminal first, while it is the only window, so typing lands in it.
    await open('com.wash.term');
    await page.waitForSelector('wash-app-term .xterm-rows', { timeout: 10_000 });
    await page.locator('wash-app-term').click();
    // A short prompt and an explicit title, so neither shows the capture
    // host's name or checkout path.
    await page.keyboard.type(`cd ${IDE_ROOT} && PS1='webshop $ ' && PROMPT_COMMAND= && export GOFLAGS=-count=1 && printf '\\033]0;webshop\\007' && clear\n`, { delay: 5 });
    await page.waitForTimeout(300);
    await page.keyboard.type('go test ./store\n', { delay: 10 });
    const term = win('wash-app-term');
    await expect(term).toContainText('want 2100', { timeout: 30_000 });
    // From the prompt, into the editor.
    await page.keyboard.type('wash-edit store/cart.go\n', { delay: 10 });
    const edit = win('wash-app-edit');
    await expect(edit).toBeVisible({ timeout: 20_000 });

    await open('com.wash.fm');
    const fm = win('wash-app-fm');
    await expect(fm).toBeVisible();

    // The agent, started in the project, with nothing asked yet.
    const aiApp = await startAgentSession(page, undefined, { cwd: IDE_ROOT });
    const ai = windowOf(page, aiApp);
    const manager = await openAgents(page);
    await closeButtonOf(page, manager).click();
    await expect(manager).toHaveCount(0, { timeout: 10_000 });

    // Tile all four: Files down the left, the editor and the agent side by
    // side, the terminal across the bottom. Slots fill by most recent focus
    // in tree order, so focus them last-slot first.
    await focus(['wash-app-term', 'wash-app-ai', 'wash-app-edit', 'wash-app-fm']);
    await page.evaluate(() => window.wash.setCellLayout(0, 0, 'r[1@19 c[r2@64 1]]'));
    await expect.poll(() => page.evaluate(() => window.wash.cellLayout(0, 0))).not.toBeNull();
    await page.waitForTimeout(1200);

    // 1: the failing test, and the command that opened the editor.
    await frame('ide-1-fail.png', [term], 210);

    // 2: cart.go dragged from Files onto the agent's composer (fm's real
    // drag payload, dispatched; a native drag can't be scripted), then the
    // question typed after the @-reference the drop put at the caret.
    await fm.locator('[data-testid="fm-entry-store"]').dblclick().catch(() => {});
    await fm.locator('[data-testid="fm-entry-cart.go"]').click({ timeout: 5_000 }).catch(() => {});
    await page.waitForTimeout(600);
    const composer = aiApp.locator('textarea');
    const dt = await page.evaluateHandle((path) => {
      const d = new DataTransfer();
      d.setData('application/x-wash-paths', JSON.stringify([path]));
      d.setData('text/plain', path);
      return d;
    }, CART);
    await composer.dispatchEvent('dragenter', { dataTransfer: dt });
    await composer.dispatchEvent('dragover', { dataTransfer: dt });
    await composer.dispatchEvent('drop', { dataTransfer: dt });
    await expect(composer).toHaveValue(/cart\.go/, { timeout: 5_000 });
    await composer.focus();
    await page.keyboard.press('End');
    await page.keyboard.type(' TestTotal gets 1200, want 2100. Why?', { delay: 8 });
    await frame('ide-2-ask.png', [fm, ai]);

    // 3: the agent's answer, and its fix reloaded in the editor.
    await composer.press('Enter');
    await expect(ai.getByText(/ranges over every item/)).toBeVisible({ timeout: 20_000 });
    await expect(ai.locator('[data-testid="agent-stop"]')).toHaveCount(0, { timeout: 20_000 });
    writeFileSync(CART, cartGo(FIXED));
    await expect(edit).toContainText('range c.items', { timeout: 15_000 });
    await frame('ide-3-fix.png', [edit, ai]);

    // 4: the same test, green.
    await term.locator('.xterm-rows').click();
    await page.keyboard.type('clear\n', { delay: 10 });
    await page.waitForTimeout(300);
    await page.keyboard.type('go test -v ./store\n', { delay: 10 });
    await expect(term).toContainText(/ok\s+example\.com\/webshop\/store/, { timeout: 30_000 });
    await frame('ide-4-pass.png', [term], 210);

    await page.mouse.move(800, 980);
    await hideBanner();
    await page.waitForTimeout(400);
    await page.screenshot({ path: join(SHOTS, 'ide-tiled.png') });
  });
});

// --- a root action, approved in the browser (docs/PRIV.md) --------------------
// Services' Restart goes through wash-priv, which asks the browser for the
// password: the prompt in this shot is the real one. The e2e sudo stub
// (wash-priv-fakesudo) stands in for sudo and the capture stops at the
// prompt, so nothing is restarted. The list is filtered to one unit, so
// the shot shows nothing else of the capture host's services.
test.describe('admin screenshot', () => {
  test.use({ routerOpts: {
    apps: ['session', 'about', 'notify', 'services', 'priv'],
    fakesudo: true, xdgConfig: true,
  } });

  test('admin-restart', async ({ page, router }) => {
    test.setTimeout(60_000);
    mkdirSync(join(router.xdgConfigHome, 'wash'), { recursive: true });
    writeFileSync(join(router.xdgConfigHome, 'wash', 'desktop.json'), JSON.stringify({ pack: 'lisbon' }));
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu-com.wash.services"]').click();
    const w = page.locator('.wash-window', { has: page.locator('wash-app-services') });
    await expect(w).toBeVisible();
    await w.locator('[data-testid="srv-list"]').waitFor({ timeout: 10_000 });
    await w.locator('[data-testid="srv-search"]').fill('ssh');
    const unit = w.locator('[data-testid^="srv-row-ssh"]').first();
    await expect(unit).toBeVisible({ timeout: 10_000 });
    const name = (await unit.getAttribute('data-testid'))!.slice('srv-row-'.length);
    await w.locator(`[data-testid="srv-restart-${name}"]`).click();
    const modal = page.locator('[data-testid="priv-unlock-modal"]');
    await expect(modal).toBeVisible({ timeout: 15_000 });
    await page.locator('[data-testid="priv-unlock-input"]').pressSequentially('correct horse', { delay: 20 });
    // Hide the desktop banner: it prints the host's real name and addresses.
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
    await page.waitForTimeout(500);
    // The window and the prompt over it, one frame.
    const a = (await w.boundingBox())!, b = (await modal.boundingBox())!;
    const x = Math.min(a.x, b.x) - 24, y = Math.min(a.y, b.y) - 24;
    const r = Math.max(a.x + a.width, b.x + b.width) + 24, btm = Math.max(a.y + a.height, b.y + b.height) + 24;
    await page.screenshot({ path: join(SHOTS, 'admin-restart.png'), clip: { x, y, width: r - x, height: btm - y } });
  });
});

// --- apps from another machine, over real ssh (docs/REMOTE.md) ---------------
// The remote host is an arm64 Debian container (e2e/capture/remote-host/, run
// under qemu-user) with sshd and the arm64 wash build, named pi-garage. The
// desktop's Connect dials it with plain `ssh`; a wrapper first on PATH points
// that ssh at a throwaway config, key and known_hosts, so the capture never
// reads or writes ~/.ssh. Needs docker and `make OUT=out-arm64 GOARCH=arm64
// multicall-bin`; skips without them.
test.describe('remote screenshot', () => {
  const HERE = dirname(fileURLToPath(import.meta.url));
  const ARM64_WASH = join(HERE, '..', '..', 'out-arm64', 'wash');
  const DIR = join(tmpdir(), 'wash-shot-remote');
  const BIN = join(DIR, 'bin');
  const CONTAINER = 'wash-shot-pi';
  const TARGET = 'pi@pi-garage';
  let skip: string | null = null;

  test.beforeAll(() => {
    test.setTimeout(600_000);
    try { execSync('docker version', { stdio: 'ignore' }); } catch { skip = 'docker not available'; return; }
    if (!existsSync(ARM64_WASH)) { skip = 'no out-arm64/wash (make OUT=out-arm64 GOARCH=arm64 multicall-bin)'; return; }
    const ctx = join(HERE, 'remote-host');
    copyFileSync(ARM64_WASH, join(ctx, 'wash'));
    execSync(`docker build -q -t ${CONTAINER} .`, { cwd: ctx, stdio: 'ignore' });
    rmSync(DIR, { recursive: true, force: true });
    mkdirSync(BIN, { recursive: true });
    execSync(`ssh-keygen -q -t ed25519 -N '' -f ${join(DIR, 'id')}`);
    execSync(`docker rm -f ${CONTAINER}`, { stdio: 'ignore' });
    execSync(`docker run -d --name ${CONTAINER} --hostname pi-garage --platform linux/arm64 ` +
      `-e AUTHORIZED_KEY="$(cat ${join(DIR, 'id.pub')})" ${CONTAINER}`, { stdio: 'ignore' });
    const ip = execSync(`docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' ${CONTAINER}`)
      .toString().trim();
    writeFileSync(join(DIR, 'ssh_config'),
      `Host pi-garage\n  HostName ${ip}\n  IdentityFile ${join(DIR, 'id')}\n  IdentitiesOnly yes\n` +
      `  UserKnownHostsFile ${join(DIR, 'known_hosts')}\n`);
    writeFileSync(join(BIN, 'ssh'), `#!/bin/sh\nexec /usr/bin/ssh -F ${join(DIR, 'ssh_config')} "$@"\n`, { mode: 0o755 });
  });
  test.afterAll(() => {
    if (!skip) execSync(`docker rm -f ${CONTAINER}`, { stdio: 'ignore' });
  });

  test.use({ routerOpts: {
    apps: ['session', 'about', 'notify', 'term', 'connect', 'remote'],
    extraEnv: {
      PATH: `${BIN}:${process.env.PATH ?? ''}`,
      // Keep the capture host's real LAN out of Connect's discovery list.
      WASH_DISCOVERY_STATIC: 'pi-garage=10.42.0.23',
      WASH_DISCOVERY_NO_MDNS: '1',
      WASH_DISCOVERY_NO_ADVERTISE: '1',
    },
  } });

  test('remote', async ({ page, router }) => {
    test.skip(skip !== null, skip ?? '');
    test.setTimeout(240_000);
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    const open = async (appId: string) => {
      await page.locator('button[title="Apps"]').click();
      await page.locator(`[data-testid="start-menu-${appId}"]`).click();
    };

    // A local terminal first, while it is the only window.
    await open('com.wash.term');
    await page.waitForSelector('wash-app-term .xterm-rows', { timeout: 10_000 });
    await page.locator('wash-app-term').click();
    await page.keyboard.type('cd ~ && clear\n', { delay: 5 });
    await page.waitForTimeout(300);
    await page.keyboard.type('uname -nm\n', { delay: 10 });

    // Connect to the Pi and open three of its apps.
    await open('com.wash.connect');
    const connect = page.locator('wash-app-connect');
    await expect(connect).toBeVisible({ timeout: 30_000 });
    await connect.locator('[data-testid="connect-host-input"]').fill(TARGET);
    await connect.locator('[data-testid="connect-submit"]').click();
    await expect(connect.locator('[data-testid="connect-host-status"]'))
      .toHaveAttribute('data-status', 'up', { timeout: 120_000 });
    const launch = async (appId: string) => {
      // Raise Connect: the last remote window opened over it.
      await page.evaluate(() => {
        const c = window.wash.windows().find((w) => w.element === 'wash-app-connect');
        if (c) window.wash.focusWindow(c.windowID, c.origin);
      });
      const item = connect.locator(`[data-testid="connect-launch-${appId}"]`);
      for (let i = 0; i < 3 && !(await item.isVisible().catch(() => false)); i++) {
        await connect.locator('[data-testid="connect-launch"]').click();
      }
      await expect(item).toBeVisible({ timeout: 30_000 });
      await item.click();
    };
    const remoteWins = () => page.evaluate(() =>
      window.wash.windows().filter((w) => w.origin && w.origin !== 'local').length);
    await launch('com.wash.fm');
    await expect.poll(remoteWins, { timeout: 60_000 }).toBe(1);
    await launch('com.wash.term');
    await expect.poll(remoteWins, { timeout: 60_000 }).toBe(2);

    // The Pi's terminal.
    const remoteTerm = page.locator('.wash-window', { has: page.locator('[data-testid="wash-host-stripe"]') })
      .filter({ has: page.locator('.xterm-rows') });
    await expect(remoteTerm).toBeVisible({ timeout: 60_000 });
    await remoteTerm.locator('.xterm-rows').click();
    await page.keyboard.type('cd ~ && clear\n', { delay: 5 });
    await page.waitForTimeout(500);
    await page.keyboard.type('uname -nm; tail -3 garage/readings.csv\n', { delay: 10 });
    await expect(remoteTerm).toContainText('aarch64', { timeout: 20_000 });
    // The Pi's own wash-edit opens the file in the Pi's Editor, on this desktop.
    await page.keyboard.type('wash-edit garage/door.py\n', { delay: 10 });
    await expect.poll(remoteWins, { timeout: 60_000 }).toBe(3);

    await page.evaluate(() => {
      const c = window.wash.windows().find((w) => w.element === 'wash-app-connect');
      if (c) window.wash.focusWindow(c.windowID, c.origin);
    });
    await page.locator('.wash-window', { has: connect }).getByRole('button', { name: 'Close window' }).click();
    await expect(connect).toHaveCount(0);

    // Two terminals in a short top row (local, then the Pi's), the Pi's
    // Editor and Files below; slots fill by most recent focus in tree order.
    await page.evaluate(() => {
      const ws = window.wash.windows();
      const local = ws.find((w) => (!w.origin || w.origin === 'local') && w.element === 'wash-app-term');
      const remote = (el: string) => ws.find((w) => w.origin && w.origin !== 'local' && w.element.startsWith(el));
      for (const w of [remote('wash-app-fm'), remote('wash-app-edit'), remote('wash-app-term'), local]) {
        if (w) window.wash.focusWindow(w.windowID, w.origin);
      }
      window.wash.setCellLayout(0, 0, 'c[r2@32 r2]');
    });
    await expect.poll(() => page.evaluate(() => window.wash.cellLayout(0, 0))).not.toBeNull();
    await page.waitForTimeout(2500);

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
    await page.screenshot({ path: join(SHOTS, 'remote.png') });
  });
});

// --- the Agent controller window, in its own router --------------------------
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
    apps: ['session', 'about', 'notify', 'agents', 'ai', 'agentd'],
    extraEnv: {
      PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}`,
      ACP_FAKE_SCRIPT: AGENT_SCRIPT,
    },
  } });

  test('agent', async ({ page, router }) => {
    test.setTimeout(60_000);
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    // Point the manager's launcher at the posed project folder and start;
    // the shot is of the controller window agentd opens for the session.
    // The manager is closed so nothing can sit over the element screenshot.
    const aiApp = await startAgentSession(page, undefined, { cwd: PROJ });
    const w = windowOf(page, aiApp);
    await expect(w).toBeVisible();
    // Raised first: the controller agentd just opened sits over it.
    const manager = await openAgents(page);
    await closeButtonOf(page, manager).click();
    await expect(manager).toHaveCount(0, { timeout: 10_000 });
    const composer = w.locator('textarea');

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
    // transcript is half empty pane. Moved to the top-left first — agentd
    // opens the controller cascaded below the manager, which puts its
    // resize grip under the taskbar where a drag cannot reach it.
    const tb = await w.locator('.wash-titlebar').boundingBox();
    if (tb) {
      await page.mouse.move(tb.x + 90, tb.y + tb.height / 2);
      await page.mouse.down();
      await page.mouse.move(40 + 90, 40 + tb.height / 2, { steps: 16 });
      await page.mouse.up();
    }
    const grip = w.locator('[data-testid="window-resize"]');
    const g = await grip.boundingBox();
    const b = await w.boundingBox();
    if (g && b) {
      await page.mouse.move(g.x + g.width / 2, g.y + g.height / 2);
      await page.mouse.down();
      await page.mouse.move(b.x + 760, b.y + 640, { steps: 12 });
      await page.mouse.up();
    }
    await page.waitForTimeout(700);
    await w.screenshot({ path: join(SHOTS, 'agent.png') });
  });

  // The Agents manager: the launcher, with a live session in Running and
  // the conversation it came from in History.
  test('agents', async ({ page, router }) => {
    test.setTimeout(60_000);
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    const aiApp = await startAgentSession(page, 'Why is the checkout test flaky?', { cwd: PROJ });
    await expect(aiApp.getByText('Comparing sequence numbers')).toBeVisible({ timeout: 20_000 });
    await expect(aiApp.locator('[data-testid="agent-stop"]')).toHaveCount(0, { timeout: 20_000 });
    // Reopened so its lists are asked again, after the turn; that also raises it.
    await freshHistory(page);
    const manager = windowOf(page, page.locator('wash-app-agents'));
    const tb = await manager.locator('.wash-titlebar').boundingBox();
    if (tb) {
      await page.mouse.move(tb.x + 90, tb.y + tb.height / 2);
      await page.mouse.down();
      await page.mouse.move(40 + 90, 40 + tb.height / 2, { steps: 16 });
      await page.mouse.up();
    }
    await page.waitForTimeout(700);
    await manager.screenshot({ path: join(SHOTS, 'agents.png') });
  });
});
