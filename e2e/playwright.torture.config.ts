import { defineConfig, devices } from '@playwright/test';

// Torture config for the native wash-display compositor — `make display-torture`.
//
// Every scenario in ./torture boots a throwaway router + the REAL compositor
// (out/wash-display) and drives REAL X11/Wayland clients (Tk, Xaw, Chromium…)
// through storms: window churn, menu storms, resize storms, focus thrash,
// clipboard volume, guest crashes, browser reloads. It asserts invariants
// (no compositor crash, bounded RSS/fds/threads, zero orphans, grabs
// released, geometry converges) and writes evidence per scenario under
// tmp/torture/<run>/ — the debug loop: read report.md, open the .log /
// .png next to a red check, fix, rerun one scenario with --grep.
//
// Serial, roomy budgets: these are soak runs, not the 25 s CI suite.
export default defineConfig({
  testDir: './torture',
  testMatch: '**/*.tort.ts',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [['list'], ['json', { outputFile: process.env.WASH_TORTURE_DIR ? `${process.env.WASH_TORTURE_DIR}/playwright.json` : 'tmp/torture/last/playwright.json' }]],
  timeout: 10 * 60_000,
  expect: { timeout: 20_000 },
  globalTeardown: './torture/report.ts',
  use: {
    ...devices['Desktop Chrome'],
    headless: true,
    viewport: { width: 1600, height: 1000 },
    trace: 'off',
    screenshot: 'off',
  },
});
