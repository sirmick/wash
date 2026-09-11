// wash-term tab strip: switching tabs and overflowing the strip.
//
// 1. A background tab is display:none, and xterm's DOM renderer measures
//    glyph widths with offsetWidth — 0 while hidden. A hidden tab that
//    re-rendered (a font change reaches every tab) painted its first frame
//    back double-spaced before settling. <Terminal> now re-measures and
//    re-renders in the ResizeObserver tick of the reveal, before the paint.
// 2. The strip is a fixed-height overlay, so a horizontal scrollbar ate the
//    tabs and scrolling carried the controls away. Tabs now shrink, then
//    scroll in their own bar-less scroller with the controls pinned.

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

async function openTerminal(page: Page, url: string) {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();
  await expect(page.locator('wash-app-term')).toBeVisible();
  const host = page.locator('[data-testid="term-host"]').first();
  await expect(host).toBeVisible();
  await expect.poll(() => bufferOf(host), { timeout: 10_000 }).toMatch(/[$#%>][ ]?/);
  return host;
}

async function channelIds(page: Page): Promise<number[]> {
  return page.locator('[data-testid="term-host"]').evaluateAll((els) =>
    els.map((e) => Number((e as HTMLElement).dataset.channel)));
}

async function newTab(page: Page): Promise<number> {
  const before = await channelIds(page);
  await page.locator('[data-testid="term-new-tab"]').first().click();
  await expect.poll(async () => (await channelIds(page)).length).toBe(before.length + 1);
  return (await channelIds(page)).find((id) => !before.includes(id))!;
}

test.describe('terminal tab strip', () => {
  test.setTimeout(45_000);

  test('a background tab is revealed at its settled metrics, not double-spaced', async ({ page, router }) => {
    const first = await openTerminal(page, router.url);
    const a = (await channelIds(page))[0];
    await first.click();
    await page.keyboard.type('echo WWWWWWWWWW iiiiiiiiii 0123456789\n');
    await expect.poll(() => bufferOf(first)).toContain('WWWWWWWWWW iiiiiiiiii');

    const b = await newTab(page);
    await expect(first).toBeHidden();

    // Change the font size while `a` is hidden: every tab re-renders.
    const stage = page.locator('[data-testid="term-stage"]');
    const box = (await stage.boundingBox())!;
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await page.keyboard.down('Control');
    await page.mouse.wheel(0, -100);
    await page.keyboard.up('Control');
    await page.waitForTimeout(300);

    // Sample `a` just before its reveal frame paints: an observer created
    // now is delivered after <Terminal>'s own, in the same frame.
    await first.evaluate((el: any) => {
      const ro = new ResizeObserver(() => {
        if (el.getBoundingClientRect().width < 10) return;
        ro.disconnect();
        const rows = el.querySelector('.xterm-rows') as HTMLElement;
        const row = Array.from(rows.children).find((r) => (r.textContent ?? '').startsWith('WWWW'));
        let textW = -1;
        if (row) {
          const range = document.createRange();
          range.selectNodeContents(row);
          textW = range.getBoundingClientRect().width;
        }
        (window as any).__reveal = {
          letterSpacing: parseFloat(rows.style.letterSpacing || '0'),
          textW,
          rows: Array.from(rows.children).map((r) => (r.textContent ?? '').trimEnd()).filter(Boolean).slice(0, 6),
        };
      });
      ro.observe(el);
    });
    await page.locator(`[data-testid="term-tab-${a}"]`).click();
    await expect(first).toBeVisible();
    await expect.poll(() => page.evaluate(() => (window as any).__reveal)).toBeTruthy();
    await page.waitForTimeout(300);

    const settled = await first.evaluate((el: any) => {
      const rows = el.querySelector('.xterm-rows') as HTMLElement;
      const row = Array.from(rows.children).find((r) => (r.textContent ?? '').startsWith('WWWW')) as HTMLElement;
      const range = document.createRange();
      range.selectNodeContents(row);
      const cell = el.__washTerm._core._renderService.dimensions.css.cell.width;
      return { textW: range.getBoundingClientRect().width, cell, len: row.textContent!.trimEnd().length };
    });
    const reveal = await page.evaluate(() => (window as any).__reveal);
    // The row's text spans `len` cells; double-spacing would be ~2x that.
    expect(settled.textW).toBeLessThan(settled.len * settled.cell * 1.1);
    expect(reveal.letterSpacing, `reveal frame rows: ${JSON.stringify(reveal.rows)}`).toBeLessThan(1);
    expect(Math.abs(reveal.textW - settled.textW), `reveal frame rows: ${JSON.stringify(reveal.rows)}`).toBeLessThan(1);
    expect(b).not.toBe(a);
  });

  test('overflowing tabs scroll without a bar, controls stay put, active stays in view', async ({ page, router }) => {
    await openTerminal(page, router.url);
    const strip = page.locator('[data-testid="term-tabbar"]').first();
    const scroller = strip.locator('[data-testid="term-tabs-scroll"]');
    const ids = [(await channelIds(page))[0]];
    for (let i = 0; i < 14; i++) ids.push(await newTab(page));

    await expect.poll(() => scroller.evaluate((el) => el.scrollWidth > el.clientWidth + 1)).toBe(true);

    const geo = await strip.evaluate((s) => {
      const sc = s.querySelector('[data-testid="term-tabs-scroll"]') as HTMLElement;
      const sb = s.getBoundingClientRect();
      const tab = sc.querySelector('button[data-testid^="term-tab-"]') as HTMLElement;
      const plus = s.querySelector('[data-testid="term-new-tab"]') as HTMLElement;
      const pb = plus.getBoundingClientRect();
      return {
        barH: sc.offsetHeight - sc.clientHeight,
        scrollerH: sc.clientHeight,
        stripContentH: s.clientHeight - 3, // minus padding-top
        tabW: tab.getBoundingClientRect().width,
        plusInside: pb.left >= sb.left && pb.right <= sb.right + 0.5 && pb.width > 0,
        mask: getComputedStyle(sc).maskImage || getComputedStyle(sc).webkitMaskImage,
      };
    });
    expect(geo.barH, 'the tab scroller must not take a scrollbar').toBe(0);
    expect(geo.scrollerH).toBeGreaterThanOrEqual(geo.stripContentH - 1);
    expect(geo.plusInside, 'new-tab control stays inside the strip').toBe(true);
    expect(geo.tabW).toBeGreaterThanOrEqual(119);
    expect(geo.mask).toContain('gradient');

    const inView = (id: number) => scroller.evaluate((sc, tid) => {
      const t = sc.querySelector(`[data-testid="term-tab-${tid}"]`) as HTMLElement;
      const a = sc.getBoundingClientRect();
      const b = t.getBoundingClientRect();
      return b.left >= a.left - 0.5 && b.right <= a.right + 0.5;
    }, id);

    // The newest (active) tab is scrolled into view.
    await expect.poll(() => inView(ids[ids.length - 1])).toBe(true);

    // The wheel scrolls the row sideways.
    const before = await scroller.evaluate((el) => el.scrollLeft);
    await scroller.hover();
    await page.mouse.wheel(0, -400);
    await expect.poll(() => scroller.evaluate((el) => el.scrollLeft)).toBeLessThan(before);

    // Alt+1 jumps to the first tab, which must come into view.
    await page.mouse.wheel(0, 4000);
    await expect.poll(() => inView(ids[0])).toBe(false);
    await page.locator(`[data-testid="term-host"][data-channel="${ids[ids.length - 1]}"]`).click();
    await page.keyboard.press('Alt+1');
    await expect.poll(() => inView(ids[0])).toBe(true);
  });
});
