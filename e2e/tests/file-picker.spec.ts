// FilePicker end-to-end: drives the @wash/ui <FilePicker> embedded
// in wash-test. Exercises the full Open and Save flows + overwrite
// confirm.
//
// Topology under test (post-wash-fs-removal):
//
//   wash-test FE ──(sendAppMsg to self)──▶ wash-test BE
//        ▲                                      │
//        │                          sdk.EnableFilePicker(c)
//        │                          dispatches fs.* via
//        │                          internal/fs (sandboxed by
//        └────── c.SendAppMsg ────── Session.Root)
//
// The architectural assertion: the round trip lands the chosen
// path in wash-test's picker-result span, with no separate fs
// service in the loop.

import { test, expect } from '../fixtures/router';
import { seedSimpleTree } from '../fixtures/router';
import { writeFileSync } from 'node:fs';
import { join } from 'node:path';

test.describe('FilePicker', () => {
  test.use({
    routerOpts: {
      kiosk: 'com.wash.test',
      fmRoot: true,
      fmSeed: seedSimpleTree,
    },
  });

  test('Open: lists fixture dir, picks a file, result lands on host', async ({ page, router }) => {
    await page.goto(router.url);
    // wash-test in kiosk mode currently mounts twice (pre-existing
    // shell issue): one as the kiosk root + one as a windowed
    // instance. The windowed one sits on top and intercepts pointer
    // events, so the test drives that one (`.last()` in DOM order).
    const app = page.locator('wash-app-test').last();
    await expect(app).toBeVisible();

    // Pre-condition: picker-result starts blank.
    await expect(app.locator('[data-testid="picker-result"]')).toHaveText('(none)');

    // Open the dialog. The picker boots with the empty "default
    // start" path — the host BE's EnableFilePicker resolves it to
    // Session.Root, so the picker lands at fmRoot automatically.
    await app.locator('[data-testid="action-picker-open"]').click();
    const picker = page.locator('[data-testid="picker"]').last();
    await expect(picker).toBeVisible();

    // Navigate explicitly into fmRoot via the path bar; this
    // doubles as a check that fs.list round-trips.
    const pathInput = picker.locator('[data-testid="fp-path"]');
    await pathInput.fill(router.fmRoot);
    await pathInput.press('Enter');

    // seedSimpleTree drops hello.txt, binary.bin, and a docs/ dir.
    await expect(picker.locator('[data-testid="fp-entry-hello.txt"]')).toBeVisible();
    await expect(picker.locator('[data-testid="fp-entry-docs"]')).toBeVisible();

    // Click hello.txt to select it; click Open to confirm.
    await picker.locator('[data-testid="fp-entry-hello.txt"]').click();
    await picker.locator('[data-testid="fp-confirm"]').click();

    // Picker dismisses; result label carries the chosen path.
    await expect(picker).not.toBeVisible();
    await expect(app.locator('[data-testid="picker-result"]'))
      .toHaveText(join(router.fmRoot, 'hello.txt'));

    // The picker's data ops (list/stat/pick) are served IN-PROCESS by the host
    // BE's EnableFilePicker — the result landing above proves it. Only fs.watch
    // now relays to the shared com.wash.fswatch service (one deliberate hop that
    // collapses N per-app inotify instances), so the log legitimately mentions
    // it; what must NOT appear is a router-level error serving the pick.
    expect(router.log()).not.toMatch(/ERROR|panic/);
  });

  test('Open: double-click file confirms in one gesture', async ({ page, router }) => {
    await page.goto(router.url);
    // wash-test in kiosk mode currently mounts twice (pre-existing
    // shell issue): one as the kiosk root + one as a windowed
    // instance. The windowed one sits on top and intercepts pointer
    // events, so the test drives that one (`.last()` in DOM order).
    const app = page.locator('wash-app-test').last();
    await app.locator('[data-testid="action-picker-open"]').click();
    const picker = page.locator('[data-testid="picker"]').last();
    const pathInput = picker.locator('[data-testid="fp-path"]');
    await pathInput.fill(router.fmRoot);
    await pathInput.press('Enter');

    await picker.locator('[data-testid="fp-entry-hello.txt"]').dblclick();
    await expect(picker).not.toBeVisible();
    await expect(app.locator('[data-testid="picker-result"]'))
      .toHaveText(join(router.fmRoot, 'hello.txt'));
  });

  test('Open: Cancel closes without altering result', async ({ page, router }) => {
    await page.goto(router.url);
    // wash-test in kiosk mode currently mounts twice (pre-existing
    // shell issue): one as the kiosk root + one as a windowed
    // instance. The windowed one sits on top and intercepts pointer
    // events, so the test drives that one (`.last()` in DOM order).
    const app = page.locator('wash-app-test').last();

    await app.locator('[data-testid="action-picker-open"]').click();
    const picker = page.locator('[data-testid="picker"]').last();
    await expect(picker).toBeVisible();

    await picker.locator('[data-testid="fp-cancel"]').click();
    await expect(picker).not.toBeVisible();
    await expect(app.locator('[data-testid="picker-result"]')).toHaveText('(cancelled)');
  });

  test('Open: folder navigation steps into and back out via Up', async ({ page, router }) => {
    await page.goto(router.url);
    // wash-test in kiosk mode currently mounts twice (pre-existing
    // shell issue): one as the kiosk root + one as a windowed
    // instance. The windowed one sits on top and intercepts pointer
    // events, so the test drives that one (`.last()` in DOM order).
    const app = page.locator('wash-app-test').last();
    await app.locator('[data-testid="action-picker-open"]').click();
    const picker = page.locator('[data-testid="picker"]').last();
    const pathInput = picker.locator('[data-testid="fp-path"]');
    await pathInput.fill(router.fmRoot);
    await pathInput.press('Enter');

    // Double-click the docs/ row → cwd moves into docs/.
    await picker.locator('[data-testid="fp-entry-docs"]').dblclick();
    await expect(pathInput).toHaveValue(join(router.fmRoot, 'docs'));
    await expect(picker.locator('[data-testid="fp-entry-readme.md"]')).toBeVisible();

    // Up button → back to fmRoot.
    await picker.locator('[data-testid="fp-up"]').click();
    await expect(pathInput).toHaveValue(router.fmRoot);
    await expect(picker.locator('[data-testid="fp-entry-docs"]')).toBeVisible();
  });

  test('Open: Back retraces navigation; Up / "/" / "~" respect the sandbox root', async ({ page, router }) => {
    await page.goto(router.url);
    // wash-test in kiosk mode currently mounts twice (pre-existing
    // shell issue): one as the kiosk root + one as a windowed
    // instance. The windowed one sits on top and intercepts pointer
    // events, so the test drives that one (`.last()` in DOM order).
    const app = page.locator('wash-app-test').last();
    await app.locator('[data-testid="action-picker-open"]').click();
    const picker = page.locator('[data-testid="picker"]').last();
    const pathInput = picker.locator('[data-testid="fp-path"]');

    // The picker boots at the BE default start = Session.Root.
    await expect(pathInput).toHaveValue(router.fmRoot);
    const back = picker.locator('[data-testid="fp-back"]');
    await expect(back).toBeDisabled();

    // Step into docs/, then Back retraces to fmRoot and disables
    // again (the trail is spent).
    await picker.locator('[data-testid="fp-entry-docs"]').dblclick();
    await expect(pathInput).toHaveValue(join(router.fmRoot, 'docs'));
    await back.click();
    await expect(pathInput).toHaveValue(router.fmRoot);
    await expect(back).toBeDisabled();

    // Up at the sandbox root: the parent is outside_root, so the
    // recovery path re-lists the root — the picker stays put rather
    // than erroring.
    await picker.locator('[data-testid="fp-up"]').click();
    await expect(pathInput).toHaveValue(router.fmRoot);
    await expect(picker.locator('[data-testid="fp-error"]')).not.toBeVisible();

    // "/" and "~" both downshift to the sandbox root on a confined
    // host ("/" via outside_root recovery, "~" via the BE's
    // default-start resolution).
    await picker.locator('[data-testid="fp-root"]').click();
    await expect(pathInput).toHaveValue(router.fmRoot);
    await picker.locator('[data-testid="fp-home"]').click();
    await expect(pathInput).toHaveValue(router.fmRoot);
    await expect(picker.locator('[data-testid="fp-entry-docs"]')).toBeVisible();
  });

  test('Save: filename input + Save returns the joined path', async ({ page, router }) => {
    await page.goto(router.url);
    // wash-test in kiosk mode currently mounts twice (pre-existing
    // shell issue): one as the kiosk root + one as a windowed
    // instance. The windowed one sits on top and intercepts pointer
    // events, so the test drives that one (`.last()` in DOM order).
    const app = page.locator('wash-app-test').last();

    await app.locator('[data-testid="action-picker-save"]').click();
    const picker = page.locator('[data-testid="picker"]').last();
    await expect(picker).toBeVisible();

    // Navigate to the fixture root so the save lands somewhere
    // predictable. The save name input is prefilled with the
    // defaultName ("untitled.txt"); replace it.
    const pathInput = picker.locator('[data-testid="fp-path"]');
    await pathInput.fill(router.fmRoot);
    await pathInput.press('Enter');

    const nameInput = picker.locator('[data-testid="fp-save-name"]');
    await nameInput.fill('fresh-file.txt');

    // Saving a file that doesn't exist takes the happy path — no
    // confirm dialog, immediate close + result.
    await picker.locator('[data-testid="fp-confirm"]').click();
    await expect(picker).not.toBeVisible();
    await expect(app.locator('[data-testid="picker-result"]'))
      .toHaveText(join(router.fmRoot, 'fresh-file.txt'));
  });

  test('Save: existing file triggers Replace confirm; Replace returns the path', async ({ page, router }) => {
    // Pre-seed an extra file we'll collide with.
    writeFileSync(join(router.fmRoot, 'overwriteme.txt'), 'original');

    await page.goto(router.url);
    // wash-test in kiosk mode currently mounts twice (pre-existing
    // shell issue): one as the kiosk root + one as a windowed
    // instance. The windowed one sits on top and intercepts pointer
    // events, so the test drives that one (`.last()` in DOM order).
    const app = page.locator('wash-app-test').last();

    await app.locator('[data-testid="action-picker-save"]').click();
    const picker = page.locator('[data-testid="picker"]').last();
    const pathInput = picker.locator('[data-testid="fp-path"]');
    await pathInput.fill(router.fmRoot);
    await pathInput.press('Enter');

    await picker.locator('[data-testid="fp-save-name"]').fill('overwriteme.txt');
    await picker.locator('[data-testid="fp-confirm"]').click();

    // Replace dialog should appear (from @wash/ui ConfirmDialog).
    const replaceDialog = page.locator('[data-testid="fp-replace-dialog"]');
    await expect(replaceDialog).toBeVisible();

    await page.locator('[data-testid="fp-replace-confirm"]').click();
    await expect(picker).not.toBeVisible();
    await expect(app.locator('[data-testid="picker-result"]'))
      .toHaveText(join(router.fmRoot, 'overwriteme.txt'));
  });

  test('Save: Cancel on Replace dialog returns user to picker without confirming', async ({ page, router }) => {
    writeFileSync(join(router.fmRoot, 'keep-original.txt'), 'original');

    await page.goto(router.url);
    // wash-test in kiosk mode currently mounts twice (pre-existing
    // shell issue): one as the kiosk root + one as a windowed
    // instance. The windowed one sits on top and intercepts pointer
    // events, so the test drives that one (`.last()` in DOM order).
    const app = page.locator('wash-app-test').last();

    await app.locator('[data-testid="action-picker-save"]').click();
    const picker = page.locator('[data-testid="picker"]').last();
    const pathInput = picker.locator('[data-testid="fp-path"]');
    await pathInput.fill(router.fmRoot);
    await pathInput.press('Enter');

    await picker.locator('[data-testid="fp-save-name"]').fill('keep-original.txt');
    await picker.locator('[data-testid="fp-confirm"]').click();

    await page.locator('[data-testid="fp-replace-cancel"]').click();

    // Picker is still open; result label unchanged.
    await expect(picker).toBeVisible();
    await expect(app.locator('[data-testid="picker-result"]')).toHaveText('(none)');
  });

  test('filter dropdown narrows the visible list', async ({ page, router }) => {
    await page.goto(router.url);
    // wash-test in kiosk mode currently mounts twice (pre-existing
    // shell issue): one as the kiosk root + one as a windowed
    // instance. The windowed one sits on top and intercepts pointer
    // events, so the test drives that one (`.last()` in DOM order).
    const app = page.locator('wash-app-test').last();
    await app.locator('[data-testid="action-picker-open"]').click();
    const picker = page.locator('[data-testid="picker"]').last();
    const pathInput = picker.locator('[data-testid="fp-path"]');
    await pathInput.fill(router.fmRoot);
    await pathInput.press('Enter');

    // Default filter "All files" — both hello.txt and binary.bin visible.
    await expect(picker.locator('[data-testid="fp-entry-hello.txt"]')).toBeVisible();
    await expect(picker.locator('[data-testid="fp-entry-binary.bin"]')).toBeVisible();

    // Pick the "Text" filter (index 1 — `\.(txt|md)$`). binary.bin
    // should vanish; folders (docs) always stay visible.
    await picker.locator('[data-testid="fp-filter"]').selectOption({ index: 1 });
    await expect(picker.locator('[data-testid="fp-entry-binary.bin"]')).not.toBeVisible();
    await expect(picker.locator('[data-testid="fp-entry-hello.txt"]')).toBeVisible();
    await expect(picker.locator('[data-testid="fp-entry-docs"]')).toBeVisible();
  });
});
