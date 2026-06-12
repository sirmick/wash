// fm DnD ERROR / GUARD branches — the rejects and no-ops that keep an
// invalid drag off the wire. The happy paths (move, multi-move, upload,
// collision→Replace, alt-drag symlink→Replace) live in fm-dnd,
// fm-upload, and fm-replace; this file pins the *failure* arms:
//
//   - move a folder into its own descendant            → friendly reject
//   - copy a folder into itself / its descendant        → friendly reject
//   - create-symlink where link path == the source      → friendly reject
//   - the DropMenu Cancel button                        → silent no-op
//   - a foreign / malformed drag payload                → silent no-op
//   - a multi-move where every source is already there  → no job, no-op
//
// These drive the FE drop handlers with SYNTHETIC wash-drags: a
// DataTransfer carrying application/x-wash-paths, dispatched as
// dragenter/over/drop. Native dragTo can't reach the descendant guard
// (expanding the parent mid-drag makes Playwright lose the source row),
// and synthetic events let us set altKey to open the Alt menu and craft
// malformed payloads — neither of which a real drag exposes. A probe
// confirmed getData(DRAG_MIME) survives into the synthetic drop handler.

import { test, expect, seedSimpleTree } from '../fixtures/router';
import { existsSync, mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

const DRAG_MIME = 'application/x-wash-paths';

test.use({ routerOpts: { fmRoot: true, fmSeed: seedSimpleTree } });

async function openFm(page: import('@playwright/test').Page, router: import('../fixtures/router').RouterHandle) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.getByRole('button', { name: /Files/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible();
}

// makeDrag builds a DataTransfer carrying an arbitrary set of formats.
// `wash` → the JSON path array under DRAG_MIME; `text` → a text/plain
// payload (a foreign drag has only this); `raw` → an exact, possibly
// malformed, DRAG_MIME string.
function makeDrag(
  page: import('@playwright/test').Page,
  spec: { wash?: string[]; text?: string; raw?: string },
) {
  return page.evaluateHandle(({ mime, spec }) => {
    const dt = new DataTransfer();
    if (spec.wash) dt.setData(mime, JSON.stringify(spec.wash));
    if (spec.raw !== undefined) dt.setData(mime, spec.raw);
    if (spec.text) dt.setData('text/plain', spec.text);
    return dt;
  }, { mime: DRAG_MIME, spec });
}

// drop dispatches the full dragenter/over/drop sequence on a target,
// optionally with Alt held (which routes through the Move/Copy/Symlink
// menu). clientX/Y place the menu inside the viewport.
async function drop(
  target: import('@playwright/test').Locator,
  dataTransfer: Awaited<ReturnType<typeof makeDrag>>,
  opts: { alt?: boolean } = {},
) {
  const init: Record<string, unknown> = { dataTransfer, clientX: 120, clientY: 120 };
  if (opts.alt) init.altKey = true;
  await target.dispatchEvent('dragenter', init);
  await target.dispatchEvent('dragover', init);
  await target.dispatchEvent('drop', init);
}

const status = (page: import('@playwright/test').Page) => page.locator('[data-testid="fm-status"]');

test.describe('fm DnD guards: move', () => {
  test('move a folder into its own descendant → friendly reject, nothing moves', async ({ page, router }) => {
    mkdirSync(join(router.fmRoot, 'box', 'inner'), { recursive: true });
    writeFileSync(join(router.fmRoot, 'box', 'inner', 'keep.txt'), 'keep');

    await openFm(page, router);
    // Expand box so its child row exists to drop onto (no native drag in
    // flight, so the expand re-render is harmless).
    await page.locator('[data-testid="fm-entry-box"]').dblclick();
    await expect(page.locator('[data-testid="fm-entry-inner"]')).toBeVisible();

    const dt = await makeDrag(page, { wash: [join(router.fmRoot, 'box')] });
    await drop(page.locator('[data-testid="fm-entry-inner"]'), dt);

    await expect(status(page)).toContainText(/move: cannot move a folder into itself/);
    // box stayed put; no box was moved under inner.
    expect(existsSync(join(router.fmRoot, 'box', 'inner', 'keep.txt'))).toBe(true);
    expect(existsSync(join(router.fmRoot, 'box', 'inner', 'box'))).toBe(false);
  });

  test('drag-move whose BE rename fails (replace a non-empty dir) surfaces the error to status', async ({ page, router }) => {
    // thing.txt dropped onto dest collides with dest/thing.txt — which
    // is a NON-EMPTY directory. Replace can't clobber it, so the BE's
    // rename_err(not_empty_dir) must reach the status bar (the generic
    // BE-error arm of commitMove), with both sides left intact.
    writeFileSync(join(router.fmRoot, 'thing.txt'), 'file');
    mkdirSync(join(router.fmRoot, 'dest', 'thing.txt'), { recursive: true });
    writeFileSync(join(router.fmRoot, 'dest', 'thing.txt', 'inner.txt'), 'inner');
    await openFm(page, router);

    const dt = await makeDrag(page, { wash: [join(router.fmRoot, 'thing.txt')] });
    await drop(page.locator('[data-testid="fm-entry-dest"]'), dt);

    await expect(page.locator('[data-testid="fm-confirm-replace"]')).toBeVisible({ timeout: 3_000 });
    await page.locator('[data-testid="fm-confirm-replace-yes"]').click();

    await expect(status(page)).toContainText(/move: .*non-empty directory/);
    // Source file and the populated dest dir both survive untouched.
    expect(existsSync(join(router.fmRoot, 'thing.txt'))).toBe(true);
    expect(existsSync(join(router.fmRoot, 'dest', 'thing.txt', 'inner.txt'))).toBe(true);
  });

  test('multi-move where every source already lives in the target dir → no-op, no error', async ({ page, router }) => {
    // Two files already in root, dropped (as a 2-set) onto the root list
    // pane = their own parent. dispatchBulkMove must drop both as
    // same-parent no-ops and enqueue nothing — not a redundant bulk job.
    writeFileSync(join(router.fmRoot, 'm1.txt'), '1');
    writeFileSync(join(router.fmRoot, 'm2.txt'), '2');
    await openFm(page, router);

    const dt = await makeDrag(page, {
      wash: [join(router.fmRoot, 'm1.txt'), join(router.fmRoot, 'm2.txt')],
    });
    await drop(page.locator('[data-testid="fm-list"]'), dt);

    // Give any (erroneous) job a beat to surface, then assert clean.
    await page.waitForTimeout(300);
    await expect(status(page)).not.toContainText(/move:/);
    expect(existsSync(join(router.fmRoot, 'm1.txt'))).toBe(true);
    expect(existsSync(join(router.fmRoot, 'm2.txt'))).toBe(true);
    // No bulk move job was ever enqueued.
    expect(router.log()).not.toMatch(/bulk-ops job=\S+ op=move/);
  });
});

test.describe('fm DnD guards: copy (Alt menu)', () => {
  test('copy a folder into itself → friendly reject, no nested copy', async ({ page, router }) => {
    mkdirSync(join(router.fmRoot, 'photos'));
    writeFileSync(join(router.fmRoot, 'photos', 'a.jpg'), 'a');
    await openFm(page, router);

    const dt = await makeDrag(page, { wash: [join(router.fmRoot, 'photos')] });
    await drop(page.locator('[data-testid="fm-entry-photos"]'), dt, { alt: true });

    // Alt menu opens; choose Copy here.
    await expect(page.locator('[data-testid="fm-drop-menu"]')).toBeVisible();
    await page.locator('[data-testid="fm-drop-copy"]').click();

    await expect(status(page)).toContainText(/copy: cannot copy a folder into itself/);
    expect(existsSync(join(router.fmRoot, 'photos', 'photos'))).toBe(false);
  });

  test('copy a folder into its own descendant → friendly reject', async ({ page, router }) => {
    mkdirSync(join(router.fmRoot, 'tree', 'leaf'), { recursive: true });
    await openFm(page, router);
    await page.locator('[data-testid="fm-entry-tree"]').dblclick();
    await expect(page.locator('[data-testid="fm-entry-leaf"]')).toBeVisible();

    const dt = await makeDrag(page, { wash: [join(router.fmRoot, 'tree')] });
    await drop(page.locator('[data-testid="fm-entry-leaf"]'), dt, { alt: true });

    await expect(page.locator('[data-testid="fm-drop-menu"]')).toBeVisible();
    await page.locator('[data-testid="fm-drop-copy"]').click();

    await expect(status(page)).toContainText(/copy: cannot copy a folder into itself/);
    expect(existsSync(join(router.fmRoot, 'tree', 'leaf', 'tree'))).toBe(false);
  });
});

test.describe('fm DnD guards: symlink (Alt menu)', () => {
  test('create-symlink where the link path equals the source → friendly reject', async ({ page, router }) => {
    // Alt-drop hello.txt onto the list pane of its OWN dir (root): the
    // link would be root/hello.txt — i.e. the source itself. commitSymlink
    // refuses the self-link rather than clobbering the original.
    await openFm(page, router);

    const dt = await makeDrag(page, { wash: [join(router.fmRoot, 'hello.txt')] });
    await drop(page.locator('[data-testid="fm-list"]'), dt, { alt: true });

    await expect(page.locator('[data-testid="fm-drop-menu"]')).toBeVisible();
    await page.locator('[data-testid="fm-drop-symlink"]').click();

    await expect(status(page)).toContainText(/symlink: link path equals target/);
    // The original is untouched and is still a regular file (not replaced
    // by a dangling self-symlink).
    expect(existsSync(join(router.fmRoot, 'hello.txt'))).toBe(true);
  });
});

test.describe('fm DnD: dismissed / inert drags', () => {
  test('DropMenu Cancel closes the menu and moves nothing', async ({ page, router }) => {
    await openFm(page, router);

    const dt = await makeDrag(page, { wash: [join(router.fmRoot, 'hello.txt')] });
    await drop(page.locator('[data-testid="fm-entry-docs"]'), dt, { alt: true });

    await expect(page.locator('[data-testid="fm-drop-menu"]')).toBeVisible();
    await page.locator('[data-testid="fm-drop-cancel"]').click();

    await expect(page.locator('[data-testid="fm-drop-menu"]')).toHaveCount(0);
    // hello.txt never moved into docs.
    expect(existsSync(join(router.fmRoot, 'hello.txt'))).toBe(true);
    expect(existsSync(join(router.fmRoot, 'docs', 'hello.txt'))).toBe(false);
  });

  test('a foreign drag (no wash payload) is ignored — no move, no error', async ({ page, router }) => {
    await openFm(page, router);

    const dt = await makeDrag(page, { text: 'just some text from another app' });
    await drop(page.locator('[data-testid="fm-entry-docs"]'), dt);

    await page.waitForTimeout(200);
    await expect(status(page)).not.toContainText(/move:|copy:|symlink:/);
    expect(existsSync(join(router.fmRoot, 'docs', 'hello.txt'))).toBe(false);
  });

  test('a malformed wash payload (bad JSON) is ignored — no move, no error', async ({ page, router }) => {
    await openFm(page, router);

    const dt = await makeDrag(page, { raw: '{ not valid json' });
    await drop(page.locator('[data-testid="fm-entry-docs"]'), dt);

    await page.waitForTimeout(200);
    await expect(status(page)).not.toContainText(/move:|copy:|symlink:/);
    expect(existsSync(join(router.fmRoot, 'docs', 'hello.txt'))).toBe(false);
  });
});
