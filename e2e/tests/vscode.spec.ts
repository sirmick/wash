// wash-vscode — the embedded VS Code window. Full-stack check:
// launching the app spawns/connects the singleton daemon, which (when
// code-server is already installed in the wash cache) starts it on a
// unix socket, publishes a wash ingress route, and the window renders
// the workbench in an <IngressFrame> iframe pointed at /app/<token>/.
//
// Skipped automatically when code-server isn't installed — the daemon
// would (correctly) show the install card instead, and we don't want
// CI pulling a 120 MB release. The companion router unit test
// (TestIngressProxy_RealCodeServer, gated by WASH_VSCODE_LIVE) covers
// the proxy/subpath behavior against a real code-server.

import { test, expect } from '../fixtures/router';
import { existsSync } from 'node:fs';
import { join } from 'node:path';
import { homedir } from 'node:os';
import { execSync } from 'node:child_process';

function codeServerInstalled(): boolean {
  const managed = join(homedir(), '.cache', 'wash', 'code-server', 'current', 'bin', 'code-server');
  if (existsSync(managed)) return true;
  try {
    execSync('command -v code-server', { stdio: 'ignore' });
    return true;
  } catch {
    return false;
  }
}

test.skip(!codeServerInstalled(), 'code-server not installed (run the install once, or it pulls 120 MB)');

test.use({
  routerOpts: {
    apps: ['session', 'about', 'term', 'vscode'],
    // Isolate code-server's user-data/extensions under a per-test
    // XDG dir so the run doesn't touch the developer's config.
    xdgConfig: true,
  },
});

test.describe('vscode app', () => {
  // code-server's first boot (extension scan, workbench build) is the
  // slow part; give it room.
  test.setTimeout(60_000);

  test('manager opens a workbench window that embeds code-server', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    // Launch the VS Code manager via the Apps menu.
    await page.locator('button[title="Apps"]').click();
    await page.getByRole('button', { name: /VS Code/ }).click();
    await expect(page.locator('wash-app-vscode')).toBeVisible();

    // Manager shows the control panel (code-server is installed).
    await expect(page.getByRole('button', { name: /Open Folder/ })).toBeVisible({ timeout: 15_000 });

    // Open the folder picker (directory mode) and confirm the default
    // (current) folder — launches a workbench window for it.
    await page.getByRole('button', { name: /Open Folder/ }).click();
    await page.locator('[data-testid="vscode-folder-picker"]').getByRole('button', { name: /Open Folder/ }).click();

    // A hidden workbench window mounts and renders the code-server
    // iframe at a wash ingress route (one server, shared).
    const wb = page.locator('wash-app-vscode-workbench');
    await expect(wb).toBeVisible({ timeout: 50_000 });
    const frame = wb.locator('[data-testid="ingress-frame"]');
    await expect(frame).toBeVisible({ timeout: 50_000 });
    await expect(frame).toHaveAttribute('src', /\/app\/[0-9a-f]+\//);

    // The router published the ingress route — proves the full
    // manager→spawn→PublishIngress path ran.
    await expect.poll(() => router.log(), { timeout: 5_000 }).toMatch(/ingress publish instance=/);
  });

  test('closing the manager prompts, then tears down workbench windows', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await page.locator('button[title="Apps"]').click();
    await page.getByRole('button', { name: /VS Code/ }).click();
    await expect(page.getByRole('button', { name: /Open Folder/ })).toBeVisible({ timeout: 15_000 });

    // Open a workbench window.
    await page.getByRole('button', { name: /Open Folder/ }).click();
    await page.locator('[data-testid="vscode-folder-picker"]').getByRole('button', { name: /Open Folder/ }).click();
    await expect(page.locator('wash-app-vscode-workbench')).toBeVisible({ timeout: 50_000 });

    // Request close of the manager window → it must PROMPT, not just close.
    await page.evaluate(() => {
      const w = window.wash.windows().find((x) => x.element === 'wash-app-vscode');
      if (w) window.wash.closeWindow(w.windowID);
    });
    await expect(page.locator('[data-testid="vscode-confirm-quit"]')).toBeVisible();
    // Manager still present (close was vetoed pending confirmation).
    await expect(page.locator('wash-app-vscode')).toBeVisible();

    // Confirm quit → manager + workbench windows both go away.
    // dispatchEvent: the overlay's pop-in animation keeps the button
    // from satisfying Playwright's stability check; we just need the
    // real click handler to fire.
    await page.locator('[data-testid="vscode-quit-btn"]').dispatchEvent('click');
    await expect(page.locator('wash-app-vscode-workbench')).toHaveCount(0, { timeout: 10_000 });
    await expect(page.locator('wash-app-vscode')).toHaveCount(0, { timeout: 10_000 });
  });
});
