import { defineConfig, devices } from '@playwright/test';

// Worker count comes from the test runner (test.sh defaults to
// nproc/2 capped at 8). fullyParallel:false lets the runner control
// concurrency without splitting within a file. Tests that genuinely
// need longer (terminal/wash-priv flows etc.) override with
// test.setTimeout(); the default below is tight on purpose so a
// hung test surfaces fast.
export default defineConfig({
  testDir: './tests',
  // Pre-flight: fail loudly if inotify instances are near the per-user
  // cap (leaked watchers break fs.watch silently mid-run). See
  // global-setup.ts.
  globalSetup: './global-setup.ts',
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  // 1 retry: the suite runs fully parallel (8 workers × routers + ~40 BE
  // apps), so individual specs occasionally lose a timing race (control-socket
  // round-trip, ingress now-playing propagation) and flake — each passes in
  // isolation. Playwright re-runs ONLY the failed spec; a genuine failure
  // still fails both attempts. Without this, one flake fails the whole gate.
  retries: 1,
  reporter: process.env.CI ? 'line' : 'list',
  // 15s per test + 10s per expect under load (test.sh --workers 8
  // can keep 8 chromium tabs + 8 routers + ~40 BE apps alive
  // simultaneously). A genuinely-hung assertion still surfaces in
  // ~10s; one slow BE round-trip doesn't tank the test.
  timeout: 15_000,
  expect: { timeout: 10_000 },
  use: {
    headless: true,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
});
