import { defineConfig, devices } from '@playwright/test';
import { cpus } from 'node:os';

// Worker cap. Each worker drives a chromium tab + a per-test wash-router
// + ~5-7 BE app processes; the BE control-socket round-trips and ingress
// propagation chains are scheduler-sensitive, so over-subscribing the box
// (running far more workers than the band-aids — 15s timeout, 12s control
// socket — were tuned for) makes whole specs fall over, not just flake.
//
// This used to be supplied by test.sh (`--workers $(nproc/2, capped at 8)`).
// When `make` replaced test.sh, the bare `playwright test` fell back to
// Playwright's default of nproc/2 UNCAPPED — so on a 32-core box the suite
// silently ran at 16 workers and ~33 specs (login/priv/auth/services PTY
// chains) failed *both* attempts from raw contention. We pin the old cap
// here so it survives the runner, and lives where the timeouts it's coupled
// to live: min(nproc/2, 8), at least 1. CI honours its own low core count
// (so it isn't over-subscribed) and never exceeds 8.
const WORKER_CAP = Math.max(1, Math.min(8, Math.floor(cpus().length / 2)));

export default defineConfig({
  testDir: './tests',
  // See WORKER_CAP above — pin the suite's concurrency so a big dev box
  // doesn't over-subscribe and tip BE-heavy specs into failure.
  workers: WORKER_CAP,
  // Pre-flight: fail loudly if inotify instances are near the per-user
  // cap (leaked watchers break fs.watch silently mid-run). See
  // global-setup.ts.
  globalSetup: './global-setup.ts',
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  // 1 retry: the suite runs parallel (WORKER_CAP workers × routers + ~40 BE
  // apps), so individual specs occasionally lose a timing race (control-socket
  // round-trip, ingress now-playing propagation) and flake — each passes in
  // isolation. Playwright re-runs ONLY the failed spec; a genuine failure
  // still fails both attempts. Without this, one flake fails the whole gate.
  retries: 1,
  reporter: process.env.CI ? 'line' : 'list',
  // 15s per test + 10s per expect under load (WORKER_CAP workers can keep
  // that many chromium tabs + routers + ~40 BE apps alive simultaneously).
  // A genuinely-hung assertion still surfaces in ~10s; one slow BE
  // round-trip doesn't tank the test.
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
