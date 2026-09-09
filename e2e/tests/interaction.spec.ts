// The interaction layer (web/lib/src/hit.ts): hover, press, and keyboard
// focus feedback on everything clickable.
//
// Asserts the RENDERED state, not the markup — `getComputedStyle(el,
// '::after')` reads the overlay the layer actually draws, so a spec failure
// means a user would really see no feedback. Checking for the attribute
// alone would pass even if the stylesheet were never injected, which is
// exactly the regression worth catching: apps mount into light DOM and get
// the sheet from defineWashApp, the shell injects its own at boot, and
// either path going missing is silent.

import { test, expect } from '../fixtures/router';

/** Computed style of an element's ::after overlay.
 *
 * Waits out the layer's 90ms crossfade first: getComputedStyle reports the
 * value the transition is CURRENTLY at, so reading straight after a hover
 * returns the idle 0 and looks exactly like a missing rule. */
async function overlay(page: import('@playwright/test').Page, sel: string) {
  await page.waitForTimeout(200);
  return page.evaluate((s) => {
    const el = document.querySelector(s);
    if (!el) return null;
    const cs = getComputedStyle(el, '::after');
    return { opacity: cs.opacity, background: cs.backgroundColor, content: cs.content };
  }, sel);
}

test.describe('interaction layer', () => {
  test('the stylesheet reaches both the shell and light-DOM apps', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    // One sheet, injected once, no matter how many apps mounted.
    await expect(page.locator('style#__wash_hit__')).toHaveCount(1);
  });

  test('a taskbar control tints on hover and inverts on press', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    const apps = 'button[title="Apps"]';
    await expect(page.locator(apps)).toHaveAttribute('data-wash-hit', '');

    // Idle: the overlay exists but is fully transparent.
    const idle = await overlay(page, apps);
    expect(idle?.content).not.toBe('none');
    expect(Number(idle?.opacity)).toBe(0);

    // Hover: the currentColor wash comes up.
    await page.locator(apps).hover();
    const hot = await overlay(page, apps);
    expect(Number(hot?.opacity)).toBeCloseTo(0.1, 2);

    // Press: darker AND a different colour than hover — the direction flip
    // is what makes a press read as a press rather than "more hovered".
    await page.mouse.down();
    const down = await overlay(page, apps);
    await page.mouse.up();
    expect(Number(down?.opacity)).toBeGreaterThan(Number(hot?.opacity));
    expect(down?.background).toBe('rgb(0, 0, 0)');
    expect(down?.background).not.toBe(hot?.background);
  });

  test('a menu item inside a launched app gets the same treatment', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await page.locator('button[title="Apps"]').click();

    // Start-menu rows are MenuItems: the "strong" intensity.
    const row = page.getByRole('button', { name: /About wash/ });
    await expect(row).toBeVisible();
    await row.hover();
    await page.waitForTimeout(200);
    const hot = await page.evaluate(() => {
      const el = [...document.querySelectorAll('[data-wash-hit]')].find(
        (e) => /About wash/.test(e.textContent ?? ''),
      );
      return el ? getComputedStyle(el, '::after').opacity : null;
    });
    expect(Number(hot)).toBeGreaterThan(0);
  });

  test('a window titlebar control is a hit target', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await page.locator('button[title="Apps"]').click();
    await page.getByRole('button', { name: /About wash/ }).click();
    await expect(page.locator('wash-app-about')).toBeVisible();

    // Every control in the titlebar strip participates.
    const close = page.locator('[data-testid^="window-close"]').first();
    await expect(close).toBeVisible();
    // hasAttribute rather than toHaveAttribute(''): the matcher cannot
    // distinguish a present-but-empty attribute from a missing one.
    expect(await close.evaluate((el) => el.hasAttribute('data-wash-hit'))).toBe(true);
    await close.hover();
    const hot = await overlay(page, '[data-testid^="window-close"]');
    expect(Number(hot?.opacity)).toBeGreaterThan(0);
  });

  test('hover polarity follows the pack, with no per-pack rule', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    const apps = 'button[title="Apps"]';
    await page.locator(apps).hover();
    await page.waitForTimeout(200);

    // The whole claim of the hover treatment is that it derives its
    // direction from the pack instead of declaring it: the tint is
    // currentColor, which is near-white on the dark packs and near-black on
    // the light ones (Seoul, NT). So the SAME rule lightens dark chrome and
    // darkens light chrome. Drive it by moving --wash-fg the way a pack
    // does, and check the overlay follows.
    const read = () => page.evaluate((s) => {
      const el = document.querySelector(s)!;
      const cs = getComputedStyle(el, '::after');
      return { tint: cs.backgroundColor, opacity: cs.opacity };
    }, apps);

    const onDark = await read();
    await page.evaluate(() => document.documentElement.style.setProperty('--wash-fg', '#222222'));
    await page.waitForTimeout(120);
    const onLight = await read();
    await page.evaluate(() => document.documentElement.style.removeProperty('--wash-fg'));

    const lum = (c: string) => {
      const [r, g, b] = c.match(/\d+/g)!.slice(0, 3).map(Number);
      return 0.2126 * r + 0.7152 * g + 0.0722 * b;
    };
    // Same rule, same opacity — only the direction of the wash changes.
    expect(onLight.opacity).toBe(onDark.opacity);
    expect(lum(onDark.tint)).toBeGreaterThan(128);  // lightens dark chrome
    expect(lum(onLight.tint)).toBeLessThan(128);    // darkens light chrome
  });

  test('releasing a click cross-fades instead of flashing', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    const apps = 'button[title="Apps"]';
    await page.locator(apps).hover();
    await page.waitForTimeout(250);

    // Press, then release while still hovering. The overlay has to travel
    // from the press colour to the hover colour; if only the OPACITY is
    // transitioned the colour snaps first, and the button flashes the hover
    // white at close to press strength before settling. Measured before the
    // fix: 16ms after mouseup, opacity was still 0.185 with the colour
    // already at rgb(238,238,238).
    await page.mouse.down();
    await page.waitForTimeout(150);
    await page.mouse.up();
    await page.waitForTimeout(30);

    const mid = await page.evaluate((sel) => {
      const cs = getComputedStyle(document.querySelector(sel)!, '::after');
      return { bg: cs.backgroundColor, opacity: Number(cs.opacity) };
    }, apps);

    const lum = (c: string) => {
      const [r, g, b] = c.match(/\d+/g)!.slice(0, 3).map(Number);
      return 0.2126 * r + 0.7152 * g + 0.0722 * b;
    };
    // Still on its way down, and the colour is still in transit — not yet
    // arrived at the hover tint.
    expect(mid.opacity).toBeGreaterThan(0.1);
    expect(lum(mid.bg)).toBeLessThan(200);
  });

  test('disabled controls stay inert', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    // Synthesise one: the rule is about the [disabled] attribute, not about
    // any particular app happening to render a disabled control right now.
    const res = await page.evaluate(() => {
      const b = document.createElement('button');
      b.setAttribute('data-wash-hit', '');
      b.disabled = true;
      b.textContent = 'nope';
      document.body.appendChild(b);
      const cs = getComputedStyle(b, '::after');
      const own = getComputedStyle(b);
      const out = { display: cs.display, cursor: own.cursor, opacity: own.opacity };
      b.remove();
      return out;
    });
    expect(res.display).toBe('none');
    expect(res.cursor).toBe('default');
    expect(Number(res.opacity)).toBeCloseTo(0.5, 2);
  });
});
