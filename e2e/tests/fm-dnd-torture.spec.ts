// fm DnD / upload TORTURE — large batches and the guard/error paths the
// happy-path specs (fm-dnd, fm-upload) don't reach. Two intents:
//
//   1. Scale: push hundreds of files through the real pipeline (picker
//      upload framing loop, recursive directory walk, bulk-ops move,
//      hundreds-of-conflicts pre-flight) and assert every byte lands.
//   2. Guards: exercise the FE early-rejects that keep an invalid op off
//      the wire — multi-select move into itself (the dispatchBulkMove
//      guard) and the external-drag highlight cleanup on leave.
//
// Per [[wash e2e pattern]] each test asserts BOTH ends: the FS state
// under fmRoot and the BE transition via the router log.

import { test, expect, seedSimpleTree } from '../fixtures/router';
import { existsSync, readFileSync, readdirSync, mkdirSync, writeFileSync, mkdtempSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';

test.use({ routerOpts: { fmRoot: true, fmSeed: seedSimpleTree } });

// Torture batches + a cold router boot blow past the default 15s.
test.describe.configure({ timeout: 90_000 });

async function openFm(page: import('@playwright/test').Page, router: import('../fixtures/router').RouterHandle) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.getByRole('button', { name: /Files/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible();
  await expect(page.locator('[data-testid="fm-path"]')).toHaveValue(router.fmRoot);
}

// dropFiles synthesizes an external (OS) file drop. Real OS drags (and
// their directory recursion) can't be simulated; this drives the
// plain-file external-drop branch and the dragenter/over/leave glue.
async function dragFilesOnto(
  page: import('@playwright/test').Page,
  selector: string,
  files: { name: string; content: string }[],
  event: 'drop' | 'hover',
) {
  const dataTransfer = await page.evaluateHandle((files) => {
    const dt = new DataTransfer();
    for (const f of files) dt.items.add(new File([f.content], f.name, { type: 'text/plain' }));
    return dt;
  }, files);
  const target = page.locator(selector);
  await target.dispatchEvent('dragenter', { dataTransfer });
  await target.dispatchEvent('dragover', { dataTransfer });
  if (event === 'drop') await target.dispatchEvent('drop', { dataTransfer });
}

test.describe('fm torture: upload at scale', () => {
  test('300 files via picker → every byte lands + the bulk job completes', async ({ page, router }) => {
    await openFm(page, router);

    const N = 300;
    const files = Array.from({ length: N }, (_, i) => ({
      name: `big-${String(i).padStart(3, '0')}.txt`,
      mimeType: 'text/plain',
      buffer: Buffer.from(`payload-${i}\n`),
    }));
    await page.locator('[data-testid="fm-upload-input"]').setInputFiles(files);

    await router.waitForLog(/bulk-ops job=up-\S+ op=upload status=done/, 60_000);

    // FS side: the exact count landed and a sample is byte-exact —
    // proves the framed-record loop didn't drop, duplicate, or desync a
    // record across 300 headers.
    const onDisk = readdirSync(router.fmRoot).filter((n) => n.startsWith('big-'));
    expect(onDisk.length).toBe(N);
    for (const i of [0, 1, 149, 298, 299]) {
      const name = `big-${String(i).padStart(3, '0')}.txt`;
      expect(readFileSync(join(router.fmRoot, name), 'utf8')).toBe(`payload-${i}\n`);
    }
  });

  test('deep + wide directory upload → recursive tree, every intermediate dir created', async ({ page, router }) => {
    // Build an OS-side source tree the webkitdirectory picker recurses:
    // 10 dirs × 20 files = 200 leaves, plus a deeply-nested chain to
    // stress the relPath walk + sanitizeRel on long paths.
    const srcParent = mkdtempSync(join(tmpdir(), 'wash-up-'));
    const root = join(srcParent, 'tree');
    for (let d = 0; d < 10; d++) {
      const dir = join(root, `d${d}`);
      mkdirSync(dir, { recursive: true });
      for (let f = 0; f < 20; f++) writeFileSync(join(dir, `f${f}.txt`), `d${d}f${f}`);
    }
    const deep = join(root, 'a', 'b', 'c', 'd', 'e', 'f', 'g');
    mkdirSync(deep, { recursive: true });
    writeFileSync(join(deep, 'leaf.txt'), 'DEEP');

    await openFm(page, router);
    await page.locator('[data-testid="fm-upload-folder-input"]').setInputFiles(root);

    await router.waitForLog(/bulk-ops job=up-\S+ op=upload status=done/, 60_000);

    // Spot-check spread across the tree + the deep leaf.
    expect(readFileSync(join(router.fmRoot, 'tree', 'd0', 'f0.txt'), 'utf8')).toBe('d0f0');
    expect(readFileSync(join(router.fmRoot, 'tree', 'd9', 'f19.txt'), 'utf8')).toBe('d9f19');
    expect(readFileSync(join(router.fmRoot, 'tree', 'a', 'b', 'c', 'd', 'e', 'f', 'g', 'leaf.txt'), 'utf8')).toBe('DEEP');
    expect(readdirSync(join(router.fmRoot, 'tree', 'd5')).length).toBe(20);
  });

  test('hundreds of conflicts → Skip uploads only the new files, leaves every original untouched', async ({ page, router }) => {
    // Pre-seed 200 files that the upload will collide with, plus 50
    // genuinely new ones. The pre-flight upload_check must classify all
    // 250 rels correctly and planUpload(skip) must keep exactly the 50.
    const DUP = 200;
    const NEW = 50;
    for (let i = 0; i < DUP; i++) writeFileSync(join(router.fmRoot, `dup-${i}.txt`), `original-${i}`);

    await openFm(page, router);

    const files = [
      ...Array.from({ length: DUP }, (_, i) => ({
        name: `dup-${i}.txt`, mimeType: 'text/plain', buffer: Buffer.from(`NEW-${i}`),
      })),
      ...Array.from({ length: NEW }, (_, i) => ({
        name: `fresh-${i}.txt`, mimeType: 'text/plain', buffer: Buffer.from(`fresh-${i}`),
      })),
    ];
    await page.locator('[data-testid="fm-upload-input"]').setInputFiles(files);

    // Pre-flight prompt reports the conflict count; choose Skip.
    const overlay = page.locator('[data-testid="fm-upload-conflict"]');
    await expect(overlay).toBeVisible({ timeout: 10_000 });
    await expect(overlay).toContainText(`${DUP} of ${DUP + NEW}`);
    await page.locator('[data-testid="fm-upload-conflict-skip"]').click();

    await router.waitForLog(/bulk-ops job=up-\S+ op=upload status=done/, 60_000);

    // Originals untouched; only the 50 fresh files appeared.
    expect(readFileSync(join(router.fmRoot, 'dup-0.txt'), 'utf8')).toBe('original-0');
    expect(readFileSync(join(router.fmRoot, `dup-${DUP - 1}.txt`), 'utf8')).toBe(`original-${DUP - 1}`);
    expect(existsSync(join(router.fmRoot, 'fresh-0.txt'))).toBe(true);
    expect(readFileSync(join(router.fmRoot, `fresh-${NEW - 1}.txt`), 'utf8')).toBe(`fresh-${NEW - 1}`);
    expect(readdirSync(router.fmRoot).filter((n) => n.startsWith('fresh-')).length).toBe(NEW);
  });
});

test.describe('fm torture: bulk move at scale', () => {
  test('select-all (minus target) of 150 files → one drag moves the whole set via bulk-ops', async ({ page, router }) => {
    const N = 150;
    // "aaa-dest" sorts first so both it and the first file row sit near
    // the top — no scrolling needed to drag one onto the other.
    mkdirSync(join(router.fmRoot, 'aaa-dest'));
    for (let i = 0; i < N; i++) writeFileSync(join(router.fmRoot, `f-${String(i).padStart(3, '0')}.txt`), `m${i}`);

    await openFm(page, router);
    await expect(page.locator('[data-testid="fm-entry-f-000.txt"]')).toBeVisible();

    // Ctrl+A selects every visible row (the 150 files + aaa-dest +
    // seed rows); ctrl-click aaa-dest back off so the target isn't part
    // of the dragged set (that would trip the move-into-itself guard).
    await page.locator('[data-testid="fm-entry-f-000.txt"]').click();
    await page.keyboard.press('Control+a');
    await page.locator('[data-testid="fm-entry-aaa-dest"]').click({ modifiers: ['Control'] });

    // Drag one selected file onto the target; the payload carries all
    // 150 and bulk-ops moves the set in a single job.
    await page.locator('[data-testid="fm-entry-f-000.txt"]')
      .dragTo(page.locator('[data-testid="fm-entry-aaa-dest"]'));

    await router.waitForLog(/bulk-ops job=\S+ op=move status=done/, 30_000);

    // Every file moved; the target dir itself stayed put.
    const inDest = readdirSync(join(router.fmRoot, 'aaa-dest')).filter((n) => n.startsWith('f-'));
    expect(inDest.length).toBe(N);
    expect(readdirSync(router.fmRoot).filter((n) => n.startsWith('f-')).length).toBe(0);
    expect(existsSync(join(router.fmRoot, 'aaa-dest'))).toBe(true);
  });
});

test.describe('fm torture: move guards', () => {
  test('multi-select drop onto a folder that is itself selected is rejected whole — nothing moves', async ({ page, router }) => {
    // Select the docs folder AND hello.txt, then drop the pair onto
    // docs. docs is in the dragged set, so dispatchBulkMove must reject
    // the entire op (matching commitMove's single-file guard) rather
    // than enqueueing a bulk job that tries to move docs into itself.
    await openFm(page, router);

    await page.locator('[data-testid="fm-entry-docs"]').click();
    await page.locator('[data-testid="fm-entry-hello.txt"]').click({ modifiers: ['Control'] });
    await expect(page.locator('[data-testid="fm-status"]')).toContainText(/2 of \d+ selected/);

    await page.locator('[data-testid="fm-entry-hello.txt"]')
      .dragTo(page.locator('[data-testid="fm-entry-docs"]'));

    // Friendly inline reject, and NOTHING moved: hello.txt stays in
    // root, docs stays in root, docs/hello.txt was never created.
    await expect(page.locator('[data-testid="fm-status"]')).toContainText(/cannot move a folder into itself/);
    expect(existsSync(join(router.fmRoot, 'hello.txt'))).toBe(true);
    expect(existsSync(join(router.fmRoot, 'docs'))).toBe(true);
    expect(existsSync(join(router.fmRoot, 'docs', 'hello.txt'))).toBe(false);
  });
});

test.describe('fm torture: external-drag highlight cleanup', () => {
  test('a folder highlighted by an OS-file drag clears when the drag leaves the pane', async ({ page, router }) => {
    // An OS-file drag has no dragend in our app, so onRowDragOver's
    // folder highlight must be torn down by the list-pane dragleave —
    // otherwise the docs row stays highlighted after the drag is gone.
    await openFm(page, router);
    const docs = page.locator('[data-testid="fm-entry-docs"]');

    // Hover (dragenter+dragover) over the docs row with an OS file →
    // it becomes the drop target.
    await dragFilesOnto(page, '[data-testid="fm-entry-docs"]', [{ name: 'x.txt', content: 'x' }], 'hover');
    await expect(docs).toHaveAttribute('data-drop-target', 'true');

    // Drag leaves the pane (relatedTarget outside) → highlight clears.
    await page.locator('[data-testid="fm-list"]').dispatchEvent('dragleave');
    await expect(docs).not.toHaveAttribute('data-drop-target', 'true');
  });
});
