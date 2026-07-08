// Remote ingress (issue #15, docs/REMOTE.md §17): an /app/<token>/ ingress
// published on host B must be reachable through host A's origin — that is
// where a remote app's iframe (vscode-workbench et al) always loads from.
//
// Two routers stand in for the ssh topology, exactly like remote-apps.spec:
// B serves its ingress registry on a unix socket (--listen-ingress, the
// socket com.wash.remote would ssh -L forward), and A pre-registers it via
// the --peer-ingress seam (what EvtPeerRegister does in production). The
// test app on B publishes a trivial HTTP backend when
// WASH_TEST_INGRESS_FILE is set and writes its minted /app/ path there.
//
// Before the fix, A answered 410 "unknown or expired ingress" for B's
// token; now it resolves the token against its peers and reverse-proxies.

import { test, expect } from '@playwright/test';
import { startRouter, stopRouter, type RouterHandle } from '../fixtures/router';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';

test('an ingress published on remote host B serves through host A\'s origin', async ({ page }) => {
  let a: RouterHandle | undefined;
  let b: RouterHandle | undefined;
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'wash-ring-'));
  const bIngressSock = path.join(dir, 'b.i.sock');
  const ingressFile = path.join(dir, 'published-path');
  try {
    b = await startRouter({
      apps: ['test'],
      extraArgs: ['--no-session', '--allow-cross-origin', '--listen-ingress', `unix:${bIngressSock}`],
      extraEnv: { WASH_TEST_INGRESS_FILE: ingressFile },
    });
    a = await startRouter({
      apps: ['session', 'about'],
      extraArgs: ['--peer-ingress', `remoteB=${bIngressSock}`],
    });

    // Launch the test app ON B; its BE publishes the ingress backend into
    // B's registry and writes the minted path for us to read.
    const launched = await b.controlRequest({ t: 'launch', app_id: 'com.wash.test' });
    expect(launched.t).toBe('launched');
    await expect
      .poll(() => fs.existsSync(ingressFile), { timeout: 15_000, message: 'test app never published its ingress path' })
      .toBe(true);
    const appPath = fs.readFileSync(ingressFile, 'utf8').trim();
    expect(appPath).toMatch(/^\/app\/[0-9a-f]{32}\/$/);

    // Fetch B's ingress THROUGH A's origin from the browser — same-origin,
    // exactly as an iframe src would resolve. This is the request that
    // 410'd before the fix.
    await page.goto(a.url);
    await expect(page.locator('[data-testid="wash-cam"]')).toBeAttached({ timeout: 10_000 });
    const viaA = await page.evaluate(async (p) => {
      const resp = await fetch(p + 'hello', { credentials: 'same-origin' });
      return { status: resp.status, body: await resp.text() };
    }, appPath);
    expect(viaA.status).toBe(200);
    expect(viaA.body).toBe('wash-test-ingress /hello');

    // An unknown token still answers 410 — the peer fallthrough must not
    // turn "no such ingress" into anything else.
    const bogus = await page.evaluate(async () => {
      const resp = await fetch('/app/00000000000000000000000000000000/x');
      return resp.status;
    });
    expect(bogus).toBe(410);
  } finally {
    if (b && !fs.existsSync(ingressFile)) console.log('=== B router log ===\n' + b.log());
    if (a) await stopRouter(a);
    if (b) await stopRouter(b);
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
