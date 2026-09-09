// `wash open` and the xdg-open shim (docs/Review-findings.md, cross-app:
// "`wash open <path>`, `xdg-open`, `$EDITOR`, `$BROWSER` from a wash
// terminal (only `wash-edit --open <abs>` works, undocumented)").
//
// Both are exercised the only way that proves anything: typed into a real
// wash terminal, with the window they open asserted on the desktop.
//
// $EDITOR is deliberately NOT set by wash — see apps/term/be/shims.go: it
// is a contract to BLOCK until the file is closed, and wash-edit has no
// such mode, so `git commit` would silently commit an empty message.

import { test, expect } from '../fixtures/router';
import type { Locator, Page } from '@playwright/test';
import { mkdtempSync, writeFileSync, realpathSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

async function bufferOf(host: Locator): Promise<string> {
  return await host.evaluate((el: any) => {
    const term = el.__washTerm;
    if (!term) return '';
    const buf = term.buffer.active;
    let out = '';
    for (let y = 0; y < buf.length; y++) {
      const line = buf.getLine(y);
      if (line) out += line.translateToString(true) + '\n';
    }
    return out;
  });
}

async function openTerminal(page: Page, url: string): Promise<Locator> {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();
  await expect(page.locator('wash-app-term')).toBeVisible();
  const host = page.locator('[data-testid="term-host"]').first();
  await expect(host).toBeVisible();
  await expect.poll(() => bufferOf(host), { timeout: 10_000 }).toMatch(/[$#%>][ ]?/);
  await host.click();
  return host;
}

async function run(page: Page, host: Locator, cmd: string) {
  await host.click();
  await page.keyboard.type(cmd);
  await page.keyboard.press('Enter');
}

test.describe('wash open', () => {
  test.setTimeout(90_000);

  test('opens a file in the app that registered its extension', async ({ page, router }) => {
    const dir = realpathSync(mkdtempSync(join(tmpdir(), 'wash-open-')));
    const file = join(dir, 'notes.md');
    writeFileSync(file, '# opened by wash open\n');

    const host = await openTerminal(page, router.url);
    await run(page, host, `wash open ${file}`);
    // It says what it chose — a silent open is indistinguishable from a
    // silent failure at a prompt.
    await expect.poll(() => bufferOf(host), { timeout: 10_000 }).toMatch(/wash open: .*notes\.md → wash-edit/);
    await expect(page.locator('wash-app-edit')).toBeVisible({ timeout: 20_000 });
  });

  test('opens a directory in the file manager', async ({ page, router }) => {
    const dir = realpathSync(mkdtempSync(join(tmpdir(), 'wash-open-')));
    const host = await openTerminal(page, router.url);
    await run(page, host, `wash open ${dir}`);
    await expect(page.locator('wash-app-fm')).toBeVisible({ timeout: 20_000 });
  });

  test('says so when nothing handles the extension, and when the file is not there', async ({ page, router }) => {
    const dir = realpathSync(mkdtempSync(join(tmpdir(), 'wash-open-')));
    const odd = join(dir, 'thing.zzz');
    writeFileSync(odd, 'x');
    const host = await openTerminal(page, router.url);

    await run(page, host, `wash open ${odd}`);
    await expect.poll(() => bufferOf(host), { timeout: 10_000 }).toMatch(/no wash app handles "\.zzz"/);
    // …and lists what it does handle, rather than leaving the user guessing.
    // The list wraps over several terminal lines, so match the label and
    // then look for an extension anywhere in what followed.
    expect(await bufferOf(host)).toMatch(/handled: /);
    expect(await bufferOf(host)).toMatch(/\.md/);

    await run(page, host, `wash open ${join(dir, 'missing.md')}`);
    await expect.poll(() => bufferOf(host), { timeout: 10_000 }).toMatch(/missing\.md: .*no such file/i);
  });

  test('a URL goes to $BROWSER, and says which — or says why not', async ({ page, router }) => {
    const host = await openTerminal(page, router.url);
    // No $BROWSER: refuse, out loud. wash has no browser app of its own,
    // so there is nothing to fall back to.
    await run(page, host, 'BROWSER= wash open https://example.invalid/x');
    await expect.poll(() => bufferOf(host), { timeout: 10_000 }).toMatch(/no wash app handles URLs.*\$BROWSER is unset/);

    // With one, it names the exact command it ran.
    await run(page, host, "BROWSER='true %s' wash open https://example.invalid/x");
    await expect.poll(() => bufferOf(host), { timeout: 10_000 })
      .toMatch(/wash open: https:\/\/example\.invalid\/x → true https:\/\/example\.invalid\/x/);
  });

  test('xdg-open is on a wash terminal’s PATH and is wash open', async ({ page, router }) => {
    const dir = realpathSync(mkdtempSync(join(tmpdir(), 'wash-open-')));
    const file = join(dir, 'shim.md');
    writeFileSync(file, 'shimmed\n');

    const host = await openTerminal(page, router.url);
    // The shim shadows any system xdg-open: it is first on PATH.
    await run(page, host, 'command -v xdg-open');
    await expect.poll(() => bufferOf(host), { timeout: 10_000 }).toMatch(/wash-shims-\d+\/xdg-open/);

    await run(page, host, `xdg-open ${file}`);
    await expect.poll(() => bufferOf(host), { timeout: 10_000 }).toMatch(/wash open: .*shim\.md → wash-edit/);
    await expect(page.locator('wash-app-edit')).toBeVisible({ timeout: 20_000 });
  });
});
