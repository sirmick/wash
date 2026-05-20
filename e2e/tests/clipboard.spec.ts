// Clipboard service: a single (mime, data) entry held by the router.
// Apps set via ClipboardSet; the router broadcasts OnClipboardChanged
// to every OTHER app; any app can call ClipboardGet to read.

import { test, expect } from '../fixtures/router';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

test.describe('clipboard service', () => {
  test.use({ routerOpts: { showHidden: true } });

  test('set in one instance, broadcast + readable in another', async ({ page, router }) => {
    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.getByRole('button', { name: /wash test/ }).click();
    const test1 = page.locator('wash-app-test').first();
    await expect(test1).toBeVisible();

    // Spawn a second instance.
    await test1.locator('[data-testid="action-spawn-self"]').click();
    await expect(page.locator('wash-app-test')).toHaveCount(2);
    const test2 = page.locator('wash-app-test').nth(1);

    // Use taskbar pills to switch front window — simpler than
    // clicking slivers of overlapping frames.
    const pills = page.locator('wash-app-session button', { hasText: /^wash test$/ });

    // Bring test1 to front, set clipboard.
    await pills.first().click();
    await test1.locator('[data-testid="action-clipboard-set"]').click();

    // test2 (the non-setter) sees the changed event.
    await expect(test2.locator('[data-testid="clipboard-changes"]')).toHaveText('1');
    // Setter is excluded from broadcast.
    await expect(test1.locator('[data-testid="clipboard-changes"]')).toHaveText('0');

    // Switch to test2 and read.
    await pills.nth(1).click();
    await test2.locator('[data-testid="action-clipboard-get"]').click();
    await expect(test2.locator('[data-testid="clipboard-mime"]')).toHaveText('text/plain');
    await expect(test2.locator('[data-testid="clipboard-text"]')).toHaveText('hello from wash-test');
  });

  test('wash-fm "Copy path" populates the clipboard for other apps', async ({ page, router }) => {
    const root = mkdtempSync(join(tmpdir(), 'wash-fm-clip-'));
    writeFileSync(join(root, 'paste-me.txt'), 'x\n');

    await page.goto(router.url);
    // Open wash-test first so we have a clipboard observer.
    await page.locator('button[title="Apps"]').click();
    await page.getByRole('button', { name: /wash test/ }).click();
    const t = page.locator('wash-app-test');
    await expect(t).toBeVisible();

    // Then wash-fm.
    await page.locator('button[title="Apps"]').click();
    await page.getByRole('button', { name: /Files/ }).click();
    const fm = page.locator('wash-app-fm');
    await expect(fm).toBeVisible();

    // Navigate fm to fixture.
    const input = page.locator('[data-testid="fm-path"]');
    await input.fill(root);
    await input.press('Enter');

    // Right-click the file → Copy path.
    await page.locator('[data-testid="fm-entry-paste-me.txt"]').click({ button: 'right' });
    await page.locator('[data-testid="fm-ctx-copy"]').click();

    // wash-test (the observer) sees the change and reads the path.
    await expect(t.locator('[data-testid="clipboard-changes"]')).toHaveText('1');
    // Switch front window to wash-test via its pill, then get.
    const testPill = page.locator('wash-app-session button', { hasText: /^wash test$/ });
    await testPill.click();
    await t.locator('[data-testid="action-clipboard-get"]').click();
    await expect(t.locator('[data-testid="clipboard-text"]')).toHaveText(join(root, 'paste-me.txt'));
  });
});
