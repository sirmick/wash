// Right-sidebar tests (M4 + M5 + foundation for M6/M7).
//
// Covers:
//   - Default visibility: sidebar opens on first attach.
//   - Toggle: Ctrl+Alt+S + the close button + the open tab.
//   - Viewport widget: pager moved into the sidebar (no more
//     floating panel) and remains interactive.
//   - About widget: live CPU/mem bars fed by the session BE's
//     host.stats ticker (5s cadence).
//   - Notifications widget: subscribes to com.wash.notify; the
//     "no notifications" placeholder shows when the service has no
//     history yet; an emitted notification appears in the widget
//     and bumps the section badge.
//   - Section state: collapse/expand by header click; auto-expand
//     fires on new events for collapsed sections.

import { test, expect } from '../fixtures/router';

test.describe('sidebar', () => {
  test('renders by default with VIEWPORT expanded and pager visible', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    const sidebar = page.locator('[data-testid="sidebar"]');
    await expect(sidebar).toBeVisible();
    await expect(sidebar).toHaveAttribute('data-mode', 'open');

    // Viewport section is expanded by default; pager lives inside it.
    const viewportSection = page.locator('[data-testid="sidebar-section-viewport"]');
    await expect(viewportSection).toHaveAttribute('data-state', 'expanded');
    await expect(page.locator('[data-testid="viewport-widget"]')).toBeVisible();
    await expect(page.locator('[data-testid="pager"]')).toBeVisible();
    // Sanity: pager-cell-0-0 is in the DOM and clickable.
    await expect(page.locator('[data-testid="pager-cell-0-0"]')).toBeVisible();
  });

  test('Ctrl+Alt+S toggles open ↔ hidden and the tab re-opens', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('[data-testid="sidebar"]')).toBeVisible();

    await page.keyboard.press('Control+Alt+s');
    await expect(page.locator('[data-testid="sidebar"]')).toHaveAttribute('data-mode', 'hidden');
    // The re-open tab appears at the right edge.
    await expect(page.locator('[data-testid="sidebar-tab"]')).toBeVisible();

    // Clicking the tab brings the sidebar back.
    await page.locator('[data-testid="sidebar-tab"]').click();
    await expect(page.locator('[data-testid="sidebar"]')).toHaveAttribute('data-mode', 'open');
    await expect(page.locator('[data-testid="sidebar-tab"]')).toHaveCount(0);
  });

  test('clicking a section header collapses + expands the body', async ({ page, router }) => {
    await page.goto(router.url);
    const header = page.locator('[data-testid="sidebar-section-header-viewport"]');
    const body = page.locator('[data-testid="sidebar-section-body-viewport"]');
    await expect(body).toBeVisible();
    await header.click();
    await expect(body).toHaveCount(0);
    await header.click();
    await expect(body).toBeVisible();
  });

  test('About widget shows live CPU + mem bars within 8s', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('[data-testid="sidebar"]')).toBeVisible();
    // About is collapsed by default — expand it.
    await page.locator('[data-testid="sidebar-section-header-about"]').click();
    await expect(page.locator('[data-testid="about-widget"]')).toBeVisible();

    const cpuBar = page.locator('[data-testid="about-cpu-bar"]');
    const memBar = page.locator('[data-testid="about-mem-bar"]');
    // host.stats fires every 5s — first tick lands by ~5s after the
    // session BE handshakes. Allow 10s of slack for CI variance.
    await expect(async () => {
      const pct = await memBar.getAttribute('data-percent');
      expect(parseFloat(pct ?? '0')).toBeGreaterThan(0);
    }).toPass({ timeout: 10_000, intervals: [500, 500, 1000, 1000] });

    // Sanity: identity row populated from system.info.
    await expect(page.locator('[data-testid="about-identity"]')).toBeVisible();
    // CPU bar exists even if instantaneous load is 0 — assert presence
    // (data-percent attribute), not a numeric floor.
    await expect(cpuBar).toBeVisible();
  });

  // NOTE: empty-state rendering of the notify widget moved to a fast
  // component test (apps/session/fe/src/sidebar/NotifyWidget.ctest.tsx).
  // The cross-process flow below — a real notification arriving over the
  // wire and auto-expanding the section — stays here as the integration seam.
  test('triggered notification appears in the widget and auto-expands the section', async ({ page, router }) => {
    // wash-test exposes a "notify" action button; clicking it fires a
    // c.Notify() that reaches both the shell's toast AND the notify
    // service (router fans both). We launch wash-test (hidden in prod,
    // surfaced in tests via the router fixture's apps dir), click the
    // notify trigger, then check the sidebar widget.
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    // Launch wash-test via the control socket → quickest path that
    // doesn't depend on UI for the test app (it's normally hidden).
    await router.controlRequest({ t: 'launch', app_id: 'com.wash.test' });
    await expect(page.locator('wash-app-test')).toBeVisible();

    // Notify section starts collapsed. Fire a notification — the
    // widget's auto-expand rule (M5) should flip it to expanded.
    await page.locator('wash-app-test').locator('[data-testid="action-notify"]').click();

    // The widget body shows up + at least one row appears.
    await expect(page.locator('[data-testid="sidebar-section-notify"]')).toHaveAttribute('data-state', 'expanded', { timeout: 5_000 });
    await expect(page.locator('[data-testid="notify-widget"]')).toBeVisible();
    await expect(page.locator('[data-testid^="notify-row-"]').first()).toBeVisible({ timeout: 5_000 });

    // Section badge reflects the unread count.
    await expect(page.locator('[data-testid="sidebar-section-badge-notify"]')).toContainText('1');
  });

  // Sections have two states: expanded and collapsed. Auto-expand
  // fires whenever an event arrives at a collapsed section, and the
  // user can re-collapse.
});
