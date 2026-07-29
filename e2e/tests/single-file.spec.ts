// Single-file-app contract: each app's FE bundle ships as exactly one
// ES module under <app>/dist/index.js. No code-split chunks. The only
// external modules permitted are the shared runtimes served by the
// router under /vendor/* (solid-js, @xterm/*, @wash/ui) and resolved
// via the import map in web/shell/index.html — everything else must
// be inlined into the bundle.
//
// This is a wire/contract invariant from the v0.0 prompt, generalised
// to permit the import-map externals introduced in the bundle-size
// reduction pass; a build-config slip (accidentally pulling in a new
// dependency, or generating a chunk per Solid component) breaks CI.

import { test, expect } from '@playwright/test';
import { readdirSync, readFileSync, existsSync, statSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);
const REPO_ROOT = resolve(__dirname, '..', '..');

interface AppBundle {
  name: string;
  dir: string;
  /** Max bytes for the bundle. Generous; we just don't want sneaky
   *  growth (e.g. accidentally bundling all of Solid into wash-test). */
  maxBytes: number;
}

const APPS: AppBundle[] = [
  // Caps after the bundle-size pass: solid-js, @xterm/*, @wash/ui are
  // import-map externals (served from /vendor/*) so they don't add to
  // the per-app bundle. Re-evaluate caps when adding a new shared dep.
  //
  // Session grew significantly in M4–M7: sidebar shell, 5 widget
  // components, bulk conflict + priv unlock overlays, and noble crypto
  // (P-256 / HKDF / AES-GCM, ~30 KB) for the priv password handshake.
  // +~8 KB for the Remote sidebar widget's per-host app-launch dropdown
  // (docs/REMOTE.md): icon list + popup menu.
  // +~1 KB: start-menu brand header (logo + "wash <version>"), the CPU-arch
  // banner field, the radius-token sweep across the sidebar widgets, and the
  // themeable start-menu offset.
  // +~1.5 KB: the link.stats desktop info panel (LinkWidget — host/cores/iface
  // /router-version readout), the boot splash + busy cursor, and the shared
  // createAppBus saveState() persist helper. Bumped to keep ~8 KB headroom.
  { name: 'session', dir: 'apps/session/fe/dist', maxBytes: 205_000 },
  // about grew into the system-info panel: build/host/Go-runtime
  // process table/registered-apps/browser sections + sortable table.
  // +~0.1 KB from the shared createAppBus saveState()/flushState() persist
  // helper (in every app's @wash/ui). Headroom still brakes further growth.
  { name: 'about',   dir: 'apps/about/fe/dist',   maxBytes: 32_000 },
  { name: 'test',    dir: 'apps/test/fe/dist',    maxBytes: 30_000 },
  // term: +~1 KB for the agent status surface (docs/AGENT_TERM.md M1) —
  // the per-tab agent side map, the tab-chip state dot and the status-line
  // clause with its elapsed ticker. Bumped to keep ~3 KB headroom.
  { name: 'term',    dir: 'apps/term/fe/dist',    maxBytes: 34_000 },
  // fm grew with the image pipeline: folder-grid preview + VirtualGrid +
  // file-client, the ~360-extension lucide icon map (~20 new glyphs), the
  // uid-aware display-hint colours/badges, and the cross-origin DnD guard.
  // ~14 KB raw (only ~28 KB gzipped on the wire); headroom still brakes bloat.
  { name: 'fm',      dir: 'apps/fm/fe/dist',      maxBytes: 120_000 },
  // vscode is the install/launch state machine + IngressFrame; the
  // workbench is code-server (external), so this stays small. (The old
  // apps/vscode/fe bundle was removed — its FE folded into the workbench
  // shell — so only vscode-workbench has a bundle to check now.)
  { name: 'vscode-workbench', dir: 'apps/vscode-workbench/fe/dist', maxBytes: 30_000 },
  // wash-bulk + wash-priv shipped FE bundles before M6 / M7; they're
  // now background services with no FE — their UI lives in the
  // session sidebar. Nothing to bundle-check here anymore.
];

// ALLOWED_EXTERNALS are the bare specifiers served via the import map
// in web/shell/index.html — each must also be built into
// web/shell/public/vendor/ by web/shell/build-vendor.mjs. Adding a
// new entry here without wiring the vendor build will silently break
// every app at load time.
const ALLOWED_EXTERNALS = new Set([
  'solid-js',
  'solid-js/web',
  'solid-js/store',
  '@xterm/xterm',
  '@xterm/addon-fit',
  '@wash/ui',
]);

for (const app of APPS) {
  test.describe(`${app.name} app bundle`, () => {
    test('builds to exactly one index.js (no chunks)', () => {
      const dir = resolve(REPO_ROOT, app.dir);
      if (!existsSync(dir)) {
        throw new Error(
          `dist missing: ${dir}\nRun \`make TEST_APP=1\` from the repo root first.`,
        );
      }
      const jsFiles = readdirSync(dir)
        .filter((f) => f.endsWith('.js'))
        .sort();
      expect(jsFiles).toEqual(['index.js']);
    });

    test('bundle only imports allowed externals', () => {
      const dir = resolve(REPO_ROOT, app.dir);
      const code = readFileSync(resolve(dir, 'index.js'), 'utf8');
      // Static imports: every bare specifier MUST be in
      // ALLOWED_EXTERNALS. Anything else means a missed inline.
      const importPat = /(?:^|\n|;)\s*(?:import|export\s*\*)\s+(?:[\s\S]*?\s+from\s+)?["']([^"']+)["']/g;
      for (const m of code.matchAll(importPat)) {
        const spec = m[1];
        expect(ALLOWED_EXTERNALS.has(spec)).toBe(true);
      }
      // Dynamic imports — none are expected; the wash host gives the
      // bundle everything it needs through window.wash + externals.
      expect(code).not.toMatch(/\bimport\s*\(/);
      // require() is a CommonJS construct that shouldn't appear in
      // an ES library build at all.
      expect(code).not.toMatch(/\brequire\s*\(/);
    });

    test('bundle size stays under the cap', () => {
      const dir = resolve(REPO_ROOT, app.dir);
      const size = statSync(resolve(dir, 'index.js')).size;
      expect(size).toBeLessThanOrEqual(app.maxBytes);
    });

    test('bundle registers the expected custom element', () => {
      const dir = resolve(REPO_ROOT, app.dir);
      const code = readFileSync(resolve(dir, 'index.js'), 'utf8');
      const elem = `wash-app-${app.name}`;
      expect(code).toContain(elem);
      // Two ways to register:
      //   (a) a direct customElements.define() call in the bundle
      //       (older apps that don't use @wash/ui), or
      //   (b) a defineWashApp import from @wash/ui — the registration
      //       call itself lives in the externalized vendor/wash-ui.js,
      //       but importing the helper is enough to prove the bundle
      //       drives the registration path.
      const direct = /customElements\.define/.test(code);
      const viaUI = /defineWashApp/.test(code) && /from\s+["']@wash\/ui["']/.test(code);
      expect(direct || viaUI).toBe(true);
    });
  });
}
