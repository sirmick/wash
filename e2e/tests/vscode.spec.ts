// wash-vscode-workbench — the launchable "VS Code" window.
//
// As of the settings-services refactor, com.wash.vscode is a windowless
// *background service* (owns the single code-server process + ingress
// route; its control UI lives in Settings → Developer). The thing the
// user launches is com.wash.vscode.workbench: one editor window per
// folder. Cold launch flow (docs/SETTINGS.md):
//
//   1. The workbench prompts for a folder FIRST (FilePicker, directory
//      mode) — no IngressFrame yet.
//   2. Confirming a folder persists it (SaveState) and asks the service
//      to ensure code-server is up; the service hands back the ingress
//      path.
//   3. The window renders the workbench in an <IngressFrame> iframe
//      pointed at /app/<token>/?folder=… — one server, shared by every
//      workbench window.
//
// Skipped automatically when code-server isn't installed — the service
// would (correctly) tell the workbench it's absent and never hand back a
// path, and we don't want CI pulling a 120 MB release. The companion
// router unit test (TestIngressProxy_RealCodeServer, gated by
// WASH_VSCODE_LIVE) covers the proxy/subpath behavior against a real
// code-server.

import { test, expect } from '../fixtures/router';
import type { Locator } from '@playwright/test';
import { existsSync } from 'node:fs';
import { join } from 'node:path';
import { homedir } from 'node:os';
import { execSync } from 'node:child_process';

// The folder picker is an Overlay with a pop-in animation, and its
// action row can sit outside the viewport in the tall workbench window —
// both trip Playwright's stability/visibility gate on a real .click().
// dispatchEvent fires the handler directly, which is all we need. Mirror
// of the trick the old manager spec used for the confirm-quit button.
async function clickPickerBtn(picker: Locator, testid: string): Promise<void> {
  await expect(picker).toBeVisible();
  await picker.locator(`[data-testid="${testid}"]`).dispatchEvent('click');
}

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
    // XDG dir so the run doesn't touch the developer's config; also
    // where the workbench's per-instance folder SaveState lands.
    xdgConfig: true,
  },
});

test.describe('vscode workbench', () => {
  // code-server's first boot (extension scan, workbench build) is the
  // slow part; give it room.
  test.setTimeout(60_000);

  test('cold launch prompts for a folder, then embeds code-server', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    // Launch the workbench (the user-facing "VS Code"). It is a window
    // app; the background service spawns on demand when first addressed.
    const launched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.vscode.workbench' });
    expect(launched.t).toBe('launched');

    const wb = page.locator('wash-app-vscode-workbench');
    await expect(wb).toBeVisible();
    // Exactly one workbench window — launching it must not also spin up a
    // second (the background service has no window of its own).
    await expect(wb).toHaveCount(1);

    // The folder picker (directory mode) comes up FIRST — before any
    // IngressFrame. This is the cold-launch prompt.
    const picker = wb.locator('[data-testid="vscode-folder-picker"]');
    await expect(picker).toBeVisible();
    await expect(wb.locator('[data-testid="ingress-frame"]')).toHaveCount(0);

    // Confirm the default folder. In directory mode the confirm button
    // ("Open Folder") is always actionable and falls back to the current
    // directory when nothing is selected.
    await clickPickerBtn(picker, 'fp-confirm');

    // The window now renders the code-server iframe at a wash ingress
    // route — proves workbench → service ensure → PublishIngress ran.
    const frame = wb.locator('[data-testid="ingress-frame"]');
    await expect(frame).toBeVisible({ timeout: 50_000 });
    await expect(frame).toHaveAttribute('src', /\/app\/[0-9a-f]+\//);

    // The router published the ingress route for the service instance.
    await expect.poll(() => router.log(), { timeout: 5_000 }).toMatch(/ingress publish instance=/);
  });

  test('launches from the Apps menu', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    // The workbench is the launchable VS Code entry. (The background
    // service has no window; it never renders one even if clicked.)
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu-com.wash.vscode.workbench"]').click();

    const wb = page.locator('wash-app-vscode-workbench');
    await expect(wb).toBeVisible();
    // Same cold-launch prompt regardless of how it was launched.
    await expect(wb.locator('[data-testid="vscode-folder-picker"]')).toBeVisible();
  });

  test('cancelling the folder prompt closes the window', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    const launched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.vscode.workbench' });
    expect(launched.t).toBe('launched');

    const wb = page.locator('wash-app-vscode-workbench');
    await expect(wb).toBeVisible();
    const picker = wb.locator('[data-testid="vscode-folder-picker"]');

    // Cancelling with no folder chosen closes the (empty) window —
    // there's nothing to render without a workspace.
    await clickPickerBtn(picker, 'fp-cancel');
    await expect(wb).toHaveCount(0, { timeout: 10_000 });
  });

  test('refresh reopens the same folder without re-prompting', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    const launched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.vscode.workbench' });
    expect(launched.t).toBe('launched');

    const wb = page.locator('wash-app-vscode-workbench');
    await expect(wb).toBeVisible();
    await clickPickerBtn(wb.locator('[data-testid="vscode-folder-picker"]'), 'fp-confirm');
    await expect(wb.locator('[data-testid="ingress-frame"]')).toBeVisible({ timeout: 50_000 });

    // Reload: the shell reattaches and the workbench BE replays its
    // persisted folder as wash:state, so the window skips the picker and
    // goes straight back to the workbench.
    await page.reload();
    const wb2 = page.locator('wash-app-vscode-workbench');
    await expect(wb2).toBeVisible({ timeout: 20_000 });
    await expect(wb2.locator('[data-testid="ingress-frame"]')).toBeVisible({ timeout: 50_000 });
    await expect(wb2.locator('[data-testid="vscode-folder-picker"]')).toHaveCount(0);
  });
});
