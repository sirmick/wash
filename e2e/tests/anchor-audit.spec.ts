// The interaction layer gives every hit target `position: relative`, which
// makes it a containing block. Any absolutely-positioned DESCENDANT that
// previously escaped to an outer ancestor would now anchor to the control
// instead — a popup or an overlay landing in the wrong place.
//
// Only a hit target that was `static` before the layer touched it can cause
// that. An element whose OWN inline style already sets a non-static position
// was a containing block regardless (wash-session's pager cell is absolute,
// and its window rectangles have always anchored to it), so those are not
// regressions and the check must not flag them — otherwise it is noise
// nobody will act on.

import { test, expect } from '../fixtures/router';

test('the interaction layer re-anchors nothing', async ({ page, router }) => {
  await page.goto(router.url);
  await page.locator('wash-app-session').waitFor();
  // Open a spread of chrome so plenty of hit targets are live: the start
  // menu, a floating window with a titlebar, and the pager.
  await page.locator('button[title="Apps"]').click();
  await page.getByRole('button', { name: /About wash/ }).click();
  await page.locator('wash-app-about').waitFor();
  await page.waitForTimeout(800);

  const suspects = await page.evaluate(() => {
    const out: string[] = [];
    for (const hit of document.querySelectorAll<HTMLElement>('[data-wash-hit]')) {
      // Did the layer, and not the element itself, make this a containing block?
      const own = hit.style.position;
      if (own && own !== 'static') continue;
      for (const d of hit.querySelectorAll<HTMLElement>('*')) {
        // position:fixed ignores a merely-relative containing block.
        if (getComputedStyle(d).position !== 'absolute') continue;
        out.push(
          `${hit.tagName.toLowerCase()}[${hit.getAttribute('data-testid') ?? ''}] > ` +
          `${d.tagName.toLowerCase()}[${d.getAttribute('data-testid') ?? ''}]`,
        );
      }
    }
    return out;
  });
  expect(
    suspects,
    `absolutely-positioned descendants newly anchored by the layer:\n${suspects.join('\n')}`,
  ).toEqual([]);
});
