// Phase-B B1 exit gate (docs/NET.md §8.3, §11): the wash UI served BY the VM
// loads through the proxy and round-trips a model edit to the in-guest
// com.wash.netd. Driven entirely from outside the VM — Playwright against the
// proxy's HTTP/WS, the full stack (shell + router + net app + netd) running in a
// real Alpine microvm.
//
// The round-trip is the proof: the net window's Apply runs the commit-confirm
// transaction in the in-guest netd service (a separate process the net app
// relays to cross-app), and the resulting await-confirm → committed states come
// back over the serial tunnel onto the widget. Nothing here is host-faked.

import { test, expect, vmSkipReason } from '../fixtures/vm';

test.beforeEach(() => {
  const reason = vmSkipReason();
  test.skip(reason !== null, reason ?? '');
});

test('VM-served wash UI round-trips a model edit to in-guest netd', async ({ vm, page }) => {
  test.setTimeout(90_000); // VM boot + chromium + the full wire bring-up

  await page.goto(vm.url);

  // The desktop rendered — its bundle streamed from the in-guest router over
  // the serial tunnel, not from a host file.
  await expect(page.locator('wash-app-session')).toBeVisible({ timeout: 40_000 });

  // Launch the Network app from the start menu (catalog served by the VM).
  await page.locator('[title="Apps"]').click();
  await page.locator('[data-testid="start-menu-com.wash.net"]').click();

  const net = page.locator('wash-app-net');
  await expect(net).toBeVisible({ timeout: 20_000 });

  // The form rendered, which means the net app's mount-time validate already
  // round-tripped to netd and back (an empty diagnostics reply for the default
  // valid interface). Apply runs the real commit-confirm transaction in netd.
  const apply = net.getByRole('button', { name: 'Apply' });
  await expect(apply).toBeEnabled({ timeout: 20_000 });
  await apply.click();

  // netd applied against its (fake) backend and armed commit-confirm → the
  // status the in-guest service reported comes back over the tunnel.
  await expect(net.locator('.wash-net-status')).toHaveText('await-confirm', { timeout: 20_000 });

  // Keep the change → netd confirms → committed.
  await net.getByRole('button', { name: 'Keep' }).click();
  await expect(net.locator('.wash-net-status')).toHaveText('committed', { timeout: 20_000 });
});
