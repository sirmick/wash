// App-state opt-in: wash-fm saves path + expanded + sort + show-hidden
// + info-pane open via window.wash.saveState. After a browser refresh
// the shell receives session.snapshot with the saved blob and dispatches
// it as wash:state — the app re-applies and the user sees the same view.

import { test, expect } from '../fixtures/router';
import { mkdtempSync, writeFileSync, mkdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

test.describe('app state opt-in', () => {
  // Race-prone under parallel workers + tight default timeout:
  // BE round-trips for list/clipboard sync can exceed the 10s
  // playwright.config default under concurrent load. 20s gives
  // the same headroom the pre-5s-default 30s did.
  test.setTimeout(20_000);

  test('wash-fm path, show-hidden, sort, info-pane survive a refresh', async ({ page, router }) => {
    // Build a fixture tree so the path we navigate to actually exists.
    const root = mkdtempSync(join(tmpdir(), 'wash-fm-state-'));
    mkdirSync(join(root, 'subdir'));
    writeFileSync(join(root, '.hidden'), 'x\n');
    writeFileSync(join(root, 'file.txt'), 'hi\n');

    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
    const fm = page.locator('wash-app-fm');
    await expect(fm).toBeVisible();

    // Navigate into the fixture root. Wait for the initial home
    // list_ok first — the late setPathInputValue(home) callback
    // would otherwise race our fill and stomp it.
    const fmPath = fm.locator('[data-testid="fm-path"]');
    await expect(fmPath).not.toHaveValue('');
    await fmPath.fill(root);
    await fmPath.press('Enter');
    await expect(fmPath).toHaveValue(root);
    await expect(fm.locator('[data-testid="fm-entry-file.txt"]')).toBeVisible();
    // Hidden file not yet shown.
    await expect(fm.locator('[data-testid="fm-entry-.hidden"]')).toHaveCount(0);

    // Turn on show-hidden via the sort menu.
    await fm.locator('[data-testid="fm-sort"]').click();
    await fm.locator('[data-testid="fm-show-hidden"]').click();
    await expect(fm.locator('[data-testid="fm-entry-.hidden"]')).toBeVisible();

    // Open the info pane.
    await fm.locator('[data-testid="fm-info-toggle"]').click();
    await expect(fm.locator('[data-testid="fm-info"]')).toBeVisible();

    // Wait for the router log line confirming the most-recent save
    // round-trip — without this the refresh can land before the
    // persisted state reaches the router.
    await router.waitForLog(/app_state\.set instance=/, 5_000);

    // Refresh. The router keeps the wash-fm process running and the
    // app_state blob; the shell re-subscribes via session.snapshot.
    await page.reload();
    const fm2 = page.locator('wash-app-fm');
    await expect(fm2).toBeVisible();

    // Same path, hidden file visible, info pane open — all restored.
    await expect(fm2.locator('[data-testid="fm-path"]')).toHaveValue(root);
    await expect(fm2.locator('[data-testid="fm-entry-.hidden"]')).toBeVisible();
    await expect(fm2.locator('[data-testid="fm-entry-file.txt"]')).toBeVisible();
    await expect(fm2.locator('[data-testid="fm-info"]')).toBeVisible();
  });
});
