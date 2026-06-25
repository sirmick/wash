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
  // No retries. A retry just re-runs a flaked spec and hides the flake; a
  // timeout under load is far more likely a real bug (a wedged round-trip, a
  // missing settle/await) that we want to SEE and fix, not paper over. The
  // job of the timeouts below is to be generous enough that legitimate
  // under-load latency never trips them — so a timeout that DOES fire is
  // signal, not noise. Paired with the WORKER_CAP above (no over-subscription)
  // this keeps the gate honest.
  retries: 0,
  reporter: process.env.CI ? 'line' : 'list',
  // 25s per test + 15s per expect: sized for WORKER_CAP workers each driving a
  // chromium tab + router + ~5-7 BE apps, where a BE list/clipboard/session
  // round-trip can take a few seconds. Generous on purpose (a genuinely hung
  // test still fails in ≤25s and the whole suite runs in ~3min), so a fire is
  // a real bug to investigate — not a too-tight assertion losing a race.
  timeout: 25_000,
  expect: { timeout: 15_000 },
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
