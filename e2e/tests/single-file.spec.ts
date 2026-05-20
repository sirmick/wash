// Single-file-app contract: each app's FE bundle ships as exactly one
// self-contained ES module under <app>/dist/index.js. No code-split
// chunks. No external module imports — the bundle is the whole app
// (apart from the shell-provided window.wash API surface).
//
// This is a wire/contract invariant from the v0.0 prompt; we test it
// here so a build-config slip (accidentally externalizing something,
// or generating a chunk per Solid component) breaks CI.

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
  // Session bundle hosts the chrome (taskbar, start menu, palette,
  // screenshot button via bundled html-to-image). Keep an eye on
  // growth but the budget can flex as features land.
  { name: 'session', dir: 'web/apps/session/dist', maxBytes: 40_000 },
  { name: 'about',   dir: 'web/apps/about/dist',   maxBytes: 10_000 },
  { name: 'test',    dir: 'web/apps/test/dist',    maxBytes: 30_000 },
  // wash-term bundles xterm.js + the fit addon + xterm CSS.
  { name: 'term',    dir: 'web/apps/term/dist',    maxBytes: 600_000 },
  { name: 'fm',      dir: 'web/apps/fm/dist',      maxBytes: 50_000 },
];

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

    test('bundle has no top-level ES module imports', () => {
      const dir = resolve(REPO_ROOT, app.dir);
      const code = readFileSync(resolve(dir, 'index.js'), 'utf8');
      // Static imports the bundler should have inlined.
      expect(code).not.toMatch(/^\s*import\s+/m);
      expect(code).not.toMatch(/^\s*export\s+\*\s*from/m);
      // Dynamic imports — none are expected; the wash host gives the
      // bundle everything it needs through window.wash.
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
      expect(code).toMatch(/customElements\.define/);
    });
  });
}
