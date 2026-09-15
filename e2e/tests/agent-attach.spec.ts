// Attaching files and images to a prompt (docs/Review-findings.md P2 →
// agent: "attach files/images or paste an image").
//
// Both adapters have advertised promptCapabilities.image since wash first
// spoke ACP, and wash sent nothing but text. The composer now takes a
// pasted image (by value, as an ACP image block) and a picked file (by
// reference, as a resource_link — so the agent reads it through the same
// confinement and the same permission ask as any other read).
//
// The assertion that matters is not that a chip appeared. The fake
// adapter grew an `echoblocks` keyword that reports the SHAPE of the
// prompt it received — one entry per content block — so this spec sees
// what actually reached the wire rather than what the UI claimed.

import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { Page } from '@playwright/test';
import { test, expect } from '../fixtures/router';
import { AGENT_APPS, FAKE_DIR, startAgentSession } from '../fixtures/agents';

test.use({
  routerOpts: {
    apps: [...AGENT_APPS],
    extraEnv: { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` },
  },
});

// startAgentIn starts a session in `dir` from the Agents manager and
// returns its controller. The folder matters here: the attach picker opens
// on the session's own folder, which is where the fixture file is.
async function startAgentIn(page: Page, url: string, dir: string) {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  return startAgentSession(page, undefined, { cwd: dir });
}

test.describe('agent prompt attachments', () => {
  test.setTimeout(90_000);

  test('Attach… sends the file as a resource_link, not as its bytes', async ({ page, router }) => {
    const dir = mkdtempSync(join(tmpdir(), 'wash-agent-attach-'));
    writeFileSync(join(dir, 'notes.txt'), 'the contents of the file\n');
    const win = await startAgentIn(page, router.url, dir);

    await win.locator('[data-testid="agent-attach"]').click();
    const picker = page.locator('[data-testid="ai-attach-picker"]');
    await expect(picker).toBeVisible();
    // The picker opens on the session's own folder; pick the file in it.
    await picker.locator('[data-testid="fp-entry-notes.txt"]').click();
    await picker.locator('[data-testid="fp-confirm"]').click();
    await expect(picker).toBeHidden();

    // The chip names the file, and the transcript will say whether it
    // reached the agent.
    const chip = win.locator('[data-testid="agent-attachment"]');
    await expect(chip).toHaveAttribute('data-kind', 'file');
    await expect(chip).toContainText('notes.txt');

    const composer = win.locator('textarea');
    await composer.fill('echoblocks');
    await composer.press('Enter');

    // A resource_link with the file: URI — by reference. The bytes are
    // NOT in the prompt: a resource_link is what lets the agent read it
    // through wash's confinement instead of wash inlining it.
    await expect(win.getByText(/BLOCKS<<.*resource_link:file:\/\//)).toBeVisible({ timeout: 20_000 });
    await expect(win.getByText(/BLOCKS<<.*notes\.txt/)).toBeVisible();
    await expect(win.getByText('the contents of the file')).toHaveCount(0);

    // The attachment belongs to the message it was sent with.
    await expect(win.locator('[data-testid="agent-attachment"]')).toHaveCount(0);
  });

  test('a pasted image reaches the agent as an image content block', async ({ page, router }) => {
    const dir = mkdtempSync(join(tmpdir(), 'wash-agent-paste-'));
    const win = await startAgentIn(page, router.url, dir);

    const composer = win.locator('textarea');
    await composer.click();
    // A real clipboard image: a paste event carrying a PNG File, which is
    // exactly what a screenshot paste delivers.
    await composer.evaluate((el: HTMLTextAreaElement) => {
      const png = Uint8Array.from(
        atob('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=='),
        (c) => c.charCodeAt(0),
      );
      const dt = new DataTransfer();
      dt.items.add(new File([png], 'shot.png', { type: 'image/png' }));
      el.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }));
    });

    const chip = win.locator('[data-testid="agent-attachment"]');
    await expect(chip).toHaveAttribute('data-kind', 'image');
    // The thumbnail is a real decoded image, not a broken <img>.
    await expect
      .poll(() => chip.locator('img').evaluate((i: HTMLImageElement) => i.naturalWidth), { timeout: 10_000 })
      .toBeGreaterThan(0);

    await composer.fill('echoblocks');
    await composer.press('Enter');
    await expect(win.getByText(/BLOCKS<<.*image:image\/png:\d+/)).toBeVisible({ timeout: 20_000 });
  });

  test('an attachment can be dropped again before it is sent', async ({ page, router }) => {
    const dir = mkdtempSync(join(tmpdir(), 'wash-agent-unattach-'));
    writeFileSync(join(dir, 'a.txt'), 'x\n');
    const win = await startAgentIn(page, router.url, dir);

    await win.locator('[data-testid="agent-attach"]').click();
    const picker = page.locator('[data-testid="ai-attach-picker"]');
    await expect(picker).toBeVisible();
    await picker.locator('[data-testid="fp-entry-a.txt"]').click();
    await picker.locator('[data-testid="fp-confirm"]').click();
    await expect(win.locator('[data-testid="agent-attachment"]')).toHaveCount(1);

    await win.locator('[data-testid="agent-attachment-remove"]').click();
    await expect(win.locator('[data-testid="agent-attachment"]')).toHaveCount(0);

    const composer = win.locator('textarea');
    await composer.fill('echoblocks');
    await composer.press('Enter');
    // Text only: the removal must reach the wire, not just the chip row.
    await expect(win.getByText(/BLOCKS<<text:10>>/)).toBeVisible({ timeout: 20_000 });
  });
});
