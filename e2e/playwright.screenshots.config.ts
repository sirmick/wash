import { defineConfig, devices } from '@playwright/test';

// Dedicated config for `make screenshots` — captures the docs/screenshots/
// marketing PNGs. Kept SEPARATE from playwright.config.ts so the normal
// `make e2e-test` suite never runs the capture specs (and vice-versa).
//
// Single worker on purpose: the capture specs pose windows and all write into
// the same docs/screenshots/ dir; serial keeps layouts clean and avoids
// chromium/router resource contention skewing a shot mid-capture.
export default defineConfig({
  testDir: './capture',
  testMatch: '**/*.cap.ts',
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: 0,
  reporter: 'list',
  timeout: 60_000,
  expect: { timeout: 10_000 },
  use: {
    ...devices['Desktop Chrome'],
    headless: true,
    // A roomy, crisp canvas for the shots.
    viewport: { width: 1600, height: 1000 },
    deviceScaleFactor: 2,
    trace: 'off',
    screenshot: 'off',
  },
});
