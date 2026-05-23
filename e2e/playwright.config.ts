import { defineConfig, devices } from '@playwright/test';

// Worker count comes from the test runner (test.sh defaults to
// nproc/2 capped at 8). fullyParallel:false lets the runner control
// concurrency without splitting within a file. Tests that genuinely
// need longer (terminal/wash-priv flows etc.) override with
// test.setTimeout(); the default below is tight on purpose so a
// hung test surfaces fast.
export default defineConfig({
  testDir: './tests',
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: 0,
  reporter: process.env.CI ? 'line' : 'list',
  timeout: 5_000,
  expect: { timeout: 5_000 },
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
