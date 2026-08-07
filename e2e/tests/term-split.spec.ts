// wash-term split panes, M1 + M2 (docs/TERM_LAYOUT.md §9): a window is a tree
// of tab groups, each with its own strip, and the panes are laid out as
// computed rects over flat terminal hosts.
//
// The assertions are deliberately full-stack rather than DOM-only:
// geometry comes from the real bounding boxes, and every pane is proven to
// have reached its pty by asking the shell itself what grid it is running
// at (`stty size`). A split that painted correctly but never resized the
// pty would pass a DOM-only test and fail this one.

import { test, expect } from '../fixtures/router';
import type { Locator, Page } from '@playwright/test';

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

// panes returns the visible terminal hosts, left-to-right then
// top-to-bottom — i.e. in reading order, which is how the assertions below
// talk about them.
async function panes(page: Page): Promise<Array<{ host: Locator; x: number; y: number; w: number; h: number }>> {
  const all = page.locator('[data-testid="term-host"]:visible');
  const n = await all.count();
  const out = [];
  for (let i = 0; i < n; i++) {
    const host = all.nth(i);
    const b = await host.boundingBox();
    if (b) out.push({ host, x: b.x, y: b.y, w: b.width, h: b.height });
  }
  out.sort((a, b) => (Math.abs(a.y - b.y) > 4 ? a.y - b.y : a.x - b.x));
  return out;
}

async function openTerminal(page: Page, url: string) {
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

// runIn types into a specific pane. It clicks the host first: the strips
// and menubar are focusable chrome, and a click there would send the
// keystrokes to the page instead of the pty.
async function runIn(page: Page, host: Locator, cmd: string) {
  await host.click();
  await page.keyboard.type(cmd);
  await page.keyboard.press('Enter');
}

// colsOf asks the pty in a pane what its grid is — the far end of the
// chain a split has to drive: rect → ResizeObserver → fit → resize app_msg
// → BE → TIOCSWINSZ.
//
// Every probe carries a unique tag. Without one the poll matches the
// PREVIOUS probe's output, which is still sitting in the scrollback, and
// the measurement silently reports the pre-split width — the stale-match
// failure mode recorded in docs/FLAKE_LOG.md. The tag can only appear in
// the expansion, never in the echoed command line ($(…) is literal there).
let probeSeq = 0;
async function colsOf(page: Page, host: Locator): Promise<number> {
  const tag = `SZ${++probeSeq}`;
  await runIn(page, host, `echo ${tag}=$(stty size | cut -d' ' -f2)`);
  let cols = 0;
  await expect.poll(async () => {
    const text = await bufferOf(host);
    const m = text.match(new RegExp(`^${tag}=(\\d+)$`, 'm'));
    if (!m) return 0;
    cols = Number(m[1]);
    return cols;
  }, { timeout: 10_000 }).toBeGreaterThan(0);
  return cols;
}

test.describe('term split panes (M1 + M2)', () => {
  test.setTimeout(60_000);

  test('Ctrl+Shift+D splits right: two panes, two strips, both ptys resized', async ({ page, router }) => {
    const first = await openTerminal(page, router.url);
    const fullCols = await colsOf(page, first);
    const fullBox = (await panes(page))[0];

    await expect(page.locator('[data-testid="term-tabbar"]')).toHaveCount(1);
    await expect(page.locator('[data-testid="term-statusbar"]')).toHaveCount(1);
    await expect(page.locator('[data-testid="term-divider"]')).toHaveCount(0);

    await page.keyboard.press('Control+Shift+D');

    // Two panes, side by side, each about half the width.
    await expect(page.locator('[data-testid="term-host"]:visible')).toHaveCount(2, { timeout: 10_000 });
    const two = await panes(page);
    expect(two).toHaveLength(2);
    expect(Math.abs(two[0].w - two[1].w)).toBeLessThan(8);
    expect(two[0].w).toBeLessThan(fullBox.w * 0.6);
    expect(two[0].x + two[0].w).toBeLessThanOrEqual(two[1].x + 1);
    // Same height, same top — a row split, not a column one.
    expect(Math.abs(two[0].y - two[1].y)).toBeLessThan(2);
    expect(Math.abs(two[0].h - two[1].h)).toBeLessThan(2);

    // Chrome followed: a strip per group and one divider between them.
    await expect(page.locator('[data-testid="term-tabbar"]')).toHaveCount(2);
    await expect(page.locator('[data-testid="term-statusbar"]')).toHaveCount(2);
    await expect(page.locator('[data-testid="term-divider"]')).toHaveCount(1);
    await expect(page.locator('[data-testid="term-divider"]')).toHaveAttribute('data-dir', 'row');

    // The pty in each half actually learned its new width.
    const leftCols = await colsOf(page, two[0].host);
    const rightCols = await colsOf(page, two[1].host);
    expect(leftCols).toBeLessThan(fullCols);
    expect(Math.abs(leftCols - rightCols)).toBeLessThanOrEqual(2);

    // The BE opened a second pty for the new pane.
    await router.waitForLog(/wash-term tab opened ch=\d+/, 10_000);
  });

  test('panes are independent shells', async ({ page, router }) => {
    const first = await openTerminal(page, router.url);
    await page.keyboard.press('Control+Shift+D');
    await expect(page.locator('[data-testid="term-host"]:visible')).toHaveCount(2, { timeout: 10_000 });
    const [left, right] = await panes(page);

    await runIn(page, left.host, 'echo LEFTPANE');
    await runIn(page, right.host, 'echo RIGHTPANE');

    await expect.poll(() => bufferOf(left.host), { timeout: 10_000 }).toContain('LEFTPANE');
    await expect.poll(() => bufferOf(right.host), { timeout: 10_000 }).toContain('RIGHTPANE');
    // Each pane saw only its own output — one pty per pane, not a mirror.
    expect(await bufferOf(left.host)).not.toContain('RIGHTPANE');
    expect(await bufferOf(right.host)).not.toContain('LEFTPANE');
    void first;
  });

  test('splits nest: a column inside a row', async ({ page, router }) => {
    await openTerminal(page, router.url);
    await page.keyboard.press('Control+Shift+D');
    await expect(page.locator('[data-testid="term-host"]:visible')).toHaveCount(2, { timeout: 10_000 });
    // Focus is in the new right-hand pane, so this splits that one down.
    await page.keyboard.press('Control+Shift+E');
    await expect(page.locator('[data-testid="term-host"]:visible')).toHaveCount(3, { timeout: 10_000 });

    const three = await panes(page);
    // The un-split half is the tall one; the other two are the column.
    const left = three.reduce((tallest, p) => (p.h > tallest.h ? p : tallest), three[0]);
    const rights = three.filter((p) => p !== left).sort((a, b) => a.y - b.y);
    expect(rights).toHaveLength(2);
    // The right column shares an x and stacks.
    expect(Math.abs(rights[0].x - rights[1].x)).toBeLessThan(2);
    expect(rights[0].y + rights[0].h).toBeLessThanOrEqual(rights[1].y + 1);
    // The left pane still spans the full height.
    expect(left.h).toBeGreaterThan(rights[0].h * 1.5);
    await expect(page.locator('[data-testid="term-divider"]')).toHaveCount(2);
    await expect(page.locator('[data-testid="term-divider"][data-dir="col"]')).toHaveCount(1);
  });

  test('Ctrl+Shift+arrows move the focus ring geometrically', async ({ page, router }) => {
    await openTerminal(page, router.url);
    await page.keyboard.press('Control+Shift+D');
    await expect(page.locator('[data-testid="term-host"]:visible')).toHaveCount(2, { timeout: 10_000 });

    const focused = page.locator('[data-testid="term-tabbar"][data-focused="true"]');
    await expect(focused).toHaveCount(1);
    const rightBox = await focused.boundingBox();

    await page.keyboard.press('Control+Shift+ArrowLeft');
    await expect.poll(async () => (await focused.boundingBox())?.x, { timeout: 5_000 })
      .toBeLessThan(rightBox!.x);

    await page.keyboard.press('Control+Shift+ArrowRight');
    await expect.poll(async () => (await focused.boundingBox())?.x, { timeout: 5_000 })
      .toBe(rightBox!.x);
  });

  test('closing the last tab in a pane collapses it and gives back the space', async ({ page, router }) => {
    const first = await openTerminal(page, router.url);
    const fullBox = (await panes(page))[0];
    await page.keyboard.press('Control+Shift+D');
    await expect(page.locator('[data-testid="term-host"]:visible')).toHaveCount(2, { timeout: 10_000 });

    // Ctrl+Shift+W closes the focused tab; it is the only one in its group,
    // so the pane goes with it (docs/TERM_LAYOUT.md §5).
    await page.keyboard.press('Control+Shift+W');
    // Every close is confirmed now (docs/TERM_LAYOUT.md) — answer the dialog.
    await page.locator('[data-testid="term-close-confirm-ok"]').click();
    await expect(page.locator('[data-testid="term-host"]:visible')).toHaveCount(1, { timeout: 10_000 });
    await expect(page.locator('[data-testid="term-divider"]')).toHaveCount(0);
    await expect(page.locator('[data-testid="term-tabbar"]')).toHaveCount(1);

    const back = (await panes(page))[0];
    expect(Math.abs(back.w - fullBox.w)).toBeLessThan(2);
    void first;
  });

  test('the Split menu drives the same commands, and greys out at one pane', async ({ page, router }) => {
    await openTerminal(page, router.url);

    await page.locator('[data-testid="term-menu-split-btn"]').click();
    const menu = page.locator('[data-testid="term-menu-split"]');
    await expect(menu).toBeVisible();
    // Nothing to move to or close with a single pane.
    await expect(page.locator('[data-testid="term-menu-next-pane"]')).toBeDisabled();
    await expect(page.locator('[data-testid="term-menu-close-pane"]')).toBeDisabled();

    await page.locator('[data-testid="term-menu-split-right"]').click();
    await expect(page.locator('[data-testid="term-host"]:visible')).toHaveCount(2, { timeout: 10_000 });

    // With two panes the pane verbs light up.
    await page.locator('[data-testid="term-menu-split-btn"]').click();
    await expect(page.locator('[data-testid="term-menu-next-pane"]')).toBeEnabled();
    await expect(page.locator('[data-testid="term-menu-close-pane"]')).toBeEnabled();
  });

  test('the strip controls split their own pane', async ({ page, router }) => {
    await openTerminal(page, router.url);
    await page.locator('[data-testid="term-split-down"]').first().click();
    await expect(page.locator('[data-testid="term-host"]:visible')).toHaveCount(2, { timeout: 10_000 });

    const two = await panes(page);
    // A column split: same x, stacked.
    expect(Math.abs(two[0].x - two[1].x)).toBeLessThan(2);
    expect(two[0].y + two[0].h).toBeLessThanOrEqual(two[1].y + 1);
    await expect(page.locator('[data-testid="term-divider"]')).toHaveAttribute('data-dir', 'col');

    // Each strip carries its own new-tab button; using the second one adds
    // a tab to THAT group, not the focused one.
    await page.locator('[data-testid="term-tabbar"]').first().locator('[data-testid="term-new-tab"]').click();
    await expect(page.locator('[data-testid="term-tabbar"]').first().locator('button[data-testid^="term-tab-"]'))
      .toHaveCount(2, { timeout: 10_000 });
    // Still two panes — a new tab is not a new pane.
    await expect(page.locator('[data-testid="term-host"]:visible')).toHaveCount(2);
  });

  test('the layout survives a reload', async ({ page, router }) => {
    await openTerminal(page, router.url);
    await page.keyboard.press('Control+Shift+D');
    await expect(page.locator('[data-testid="term-host"]:visible')).toHaveCount(2, { timeout: 10_000 });
    const before = await panes(page);

    await page.reload();
    await expect(page.locator('wash-app-term')).toBeVisible();
    await expect(page.locator('[data-testid="term-host"]:visible')).toHaveCount(2, { timeout: 15_000 });

    const after = await panes(page);
    expect(after).toHaveLength(2);
    expect(Math.abs(after[0].w - before[0].w)).toBeLessThan(8);
    expect(Math.abs(after[1].x - before[1].x)).toBeLessThan(8);
    await expect(page.locator('[data-testid="term-tabbar"]')).toHaveCount(2);
  });

  // ---- M2: direct manipulation ----

  test('dragging a divider resizes both panes, and commits on release', async ({ page, router }) => {
    await openTerminal(page, router.url);
    await page.keyboard.press('Control+Shift+D');
    await expect(page.locator('[data-testid="term-host"]:visible')).toHaveCount(2, { timeout: 10_000 });
    const before = await panes(page);

    const divider = page.locator('[data-testid="term-divider"]');
    const box = (await divider.boundingBox())!;
    const cx = box.x + box.width / 2;
    const cy = box.y + box.height / 2;

    await page.mouse.move(cx, cy);
    await page.mouse.down();
    await page.mouse.move(cx - 120, cy, { steps: 8 });

    // Mid-drag: the preview has moved but the PANES have not. This is the
    // whole point of commit-on-release — no reflow storm, no resize frame
    // per pty per tick.
    await expect(page.locator('[data-testid="term-divider-preview"]')).toBeVisible();
    const during = await panes(page);
    expect(Math.abs(during[0].w - before[0].w)).toBeLessThan(2);

    await page.mouse.up();
    await expect(page.locator('[data-testid="term-divider-preview"]')).toHaveCount(0);

    // Released: the left pane is ~120px narrower and the right one has it.
    await expect.poll(async () => (await panes(page))[0].w, { timeout: 5_000 })
      .toBeLessThan(before[0].w - 100);
    const after = await panes(page);
    expect(after[1].w).toBeGreaterThan(before[1].w + 100);
    expect(Math.abs((after[0].w + after[1].w) - (before[0].w + before[1].w))).toBeLessThan(4);

    // The narrowed pty learned its new width too.
    const cols = await colsOf(page, after[0].host);
    expect(cols).toBeLessThan(60);
  });

  test('a divider drag cannot squeeze a pane below a readable width', async ({ page, router }) => {
    await openTerminal(page, router.url);
    await page.keyboard.press('Control+Shift+D');
    await expect(page.locator('[data-testid="term-host"]:visible')).toHaveCount(2, { timeout: 10_000 });

    const divider = page.locator('[data-testid="term-divider"]');
    const box = (await divider.boundingBox())!;
    const cy = box.y + box.height / 2;
    await page.mouse.move(box.x + box.width / 2, cy);
    await page.mouse.down();
    // Way past the left edge of the window.
    await page.mouse.move(box.x - 2000, cy, { steps: 10 });
    await page.mouse.up();

    const after = await panes(page);
    expect(after).toHaveLength(2);
    expect(after[0].w).toBeGreaterThanOrEqual(120);
  });

  test('zoom fills the stage with one pane and restores exactly', async ({ page, router }) => {
    await openTerminal(page, router.url);
    await page.keyboard.press('Control+Shift+D');
    await expect(page.locator('[data-testid="term-host"]:visible')).toHaveCount(2, { timeout: 10_000 });
    const before = await panes(page);

    await page.keyboard.press('Control+Shift+Z');
    await expect(page.locator('[data-testid="term-host"]:visible')).toHaveCount(1, { timeout: 5_000 });
    const zoomed = (await panes(page))[0];
    expect(zoomed.w).toBeGreaterThan(before[0].w * 1.8);
    // No divider to drag while one pane owns the stage.
    await expect(page.locator('[data-testid="term-divider"]')).toHaveCount(0);
    await expect(page.locator('[data-testid="term-tabbar"]')).toHaveCount(1);

    await page.keyboard.press('Control+Shift+Z');
    await expect(page.locator('[data-testid="term-host"]:visible')).toHaveCount(2, { timeout: 5_000 });
    const after = await panes(page);
    // Restored EXACTLY: zoom never touched the tree.
    expect(Math.abs(after[0].w - before[0].w)).toBeLessThan(2);
    expect(Math.abs(after[1].x - before[1].x)).toBeLessThan(2);
  });

  test('Equalize evens a lopsided window', async ({ page, router }) => {
    await openTerminal(page, router.url);
    await page.keyboard.press('Control+Shift+D');
    await expect(page.locator('[data-testid="term-host"]:visible')).toHaveCount(2, { timeout: 10_000 });

    const divider = page.locator('[data-testid="term-divider"]');
    const box = (await divider.boundingBox())!;
    const cy = box.y + box.height / 2;
    await page.mouse.move(box.x + box.width / 2, cy);
    await page.mouse.down();
    await page.mouse.move(box.x - 150, cy, { steps: 6 });
    await page.mouse.up();
    await expect.poll(async () => {
      const p = await panes(page);
      return Math.abs(p[0].w - p[1].w);
    }, { timeout: 5_000 }).toBeGreaterThan(200);

    await page.locator('[data-testid="term-menu-split-btn"]').click();
    await page.locator('[data-testid="term-menu-equalize"]').click();

    await expect.poll(async () => {
      const p = await panes(page);
      return Math.abs(p[0].w - p[1].w);
    }, { timeout: 5_000 }).toBeLessThan(8);
  });

  test('the pane context menu carries the split verbs', async ({ page, router }) => {
    const host = await openTerminal(page, router.url);
    // Shift+right-click is the terminal's own menu; the pane verbs are
    // appended to it. Plain right-click stays copy/paste.
    const box = (await host.boundingBox())!;
    await page.keyboard.down('Shift');
    await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2, { button: 'right' });
    await page.keyboard.up('Shift');

    await expect(page.locator('[data-testid="term-context-menu"]')).toBeVisible();
    await expect(page.locator('[data-testid="term-ctx-copy"]')).toBeVisible();
    await expect(page.locator('[data-testid="term-ctx-zoom"]')).toBeDisabled();
    await page.locator('[data-testid="term-ctx-split-right"]').click();
    await expect(page.locator('[data-testid="term-host"]:visible')).toHaveCount(2, { timeout: 10_000 });
  });

  test('a tab dragged to another strip moves panes without losing its buffer', async ({ page, router }) => {
    const first = await openTerminal(page, router.url);
    // Two tabs in one group, then split so there are two strips.
    await runIn(page, first, 'echo ORIGINALPANE');
    await expect.poll(() => bufferOf(first), { timeout: 10_000 }).toContain('ORIGINALPANE');
    await page.keyboard.press('Control+Shift+D');
    await expect(page.locator('[data-testid="term-host"]:visible')).toHaveCount(2, { timeout: 10_000 });

    const leftStrip = page.locator('[data-testid="term-tabbar"]').first();
    const rightStrip = page.locator('[data-testid="term-tabbar"]').nth(1);
    const leftTab = leftStrip.locator('button[data-testid^="term-tab-"]').first();
    const rightTab = rightStrip.locator('button[data-testid^="term-tab-"]').first();

    await leftTab.dragTo(rightTab);

    // The left group is empty, so its pane collapsed and the right one
    // owns the window — with BOTH tabs in one strip.
    await expect(page.locator('[data-testid="term-tabbar"]')).toHaveCount(1, { timeout: 10_000 });
    await expect(page.locator('[data-testid="term-divider"]')).toHaveCount(0);
    await expect(page.locator('button[data-testid^="term-tab-"]')).toHaveCount(2);

    // The moved terminal kept its scrollback — the DOM node never moved,
    // which is the whole reason the layout is rects (docs/TERM_LAYOUT.md §2).
    const moved = page.locator('[data-testid="term-host"]:visible');
    await expect.poll(() => bufferOf(moved), { timeout: 5_000 }).toContain('ORIGINALPANE');
  });
});
