// An escalation that needs a human says so out loud (docs/SIDEBAR.md M4).
//
// Until this, a pending sudo request announced itself ONLY through the
// rail's priv widget, which reads the local host's queue through the
// session BE gateway. So an escalation raised on a remote host was not
// merely unanswerable from here — it was invisible, with the requesting
// app blocked on a question nobody was ever shown.
//
// A toast has no such problem: the router broadcasts it to every attached
// shell (shellList()), and since M0 a remote one arrives named and tinted
// for the host that raised it.
//
// wash-notify is staged deliberately: the router does not broadcast an
// app's notify itself — it forwards to com.wash.notify, whose re-emit is
// the single authority for toasts. Without the service there is no toast.

import { test, expect } from '../fixtures/router';

const PRIV_APP_ID = 'com.wash.priv';

test.use({
  routerOpts: {
    apps: ['session', 'about', 'test', 'priv', 'notify'],
    showHidden: true,
    fakesudo: true,
  },
});

test('a request that needs a human raises a toast naming who wants what', async ({ page, router }) => {
  test.setTimeout(30_000);
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();

  const privLaunched = await router.controlRequest({ t: 'launch', app_id: PRIV_APP_ID });
  const privInst = privLaunched.instance_id as string;
  const testLaunched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.test' });
  const testInst = testLaunched.instance_id as string;

  // Seed wash-priv's page nonce so it doesn't surprise-lock on the first
  // real message, as priv.spec.ts's drivePriv does.
  await router.controlRequest({
    t: 'msg', instance_id: privInst, data: { kind: 'hello', page_nonce: 'e2e-toast' },
  });

  // wash-test → wash-priv, router-attested. Nothing auto-approves it (we
  // are not root and there is no standing grant), so it parks in the
  // queue waiting for a click — the one case that toasts.
  await router.controlRequest({
    t: 'msg',
    instance_id: testInst,
    data: {
      kind: 'send_to',
      target_app: PRIV_APP_ID,
      payload: {
        kind: 'spawn', req_id: 'r-toast-1',
        app_id: 'com.wash.about', reason: 'e2e toast test',
      },
    },
  });
  await router.waitForLog(/wash-priv enqueue: req_id=r-toast-1 kind=spawn/, 10_000);

  // The toast names the ASKING APP, because that is the part a person
  // judges: sudo for the package manager is routine, sudo for something
  // you did not start is why this queue exists at all.
  const toast = page.locator('[data-testid="notification"]').filter({ hasText: 'Approval needed' });
  await expect(toast).toHaveCount(1, { timeout: 15_000 });
  await expect(toast).toContainText('com.wash.test');

  // Warn, not info: a blocked escalation is a claim on attention, the
  // same call bulk makes for a stalled copy.
  await expect(toast).toHaveAttribute('data-level', 'warn');

  // Nothing is on screen yet. wash-priv autoboots with the session, so
  // its modal has been READY this whole time and has still not painted —
  // that is the anti-phishing rule holding (docs/SIDEBAR.md M4): a prompt
  // that appears unbidden is, by construction, not this one.
  await expect(page.locator('[data-testid="modal-layer"]')).toHaveCount(0);

  // Summon it. The toast's activation resolves the owning app from
  // app.declared — the router's word — and finds a modal there, so it
  // summons rather than launching a window.
  await toast.click();

  const modal = page.locator('[data-testid="modal-layer"]');
  await expect(modal).toBeVisible({ timeout: 10_000 });
  await expect(modal).toHaveAttribute('data-app', PRIV_APP_ID);
  // Chrome draws the host line, not the app: this is the part an
  // impersonating window cannot reproduce.
  await expect(page.locator('[data-testid="modal-host"]')).toContainText('this machine');
  // And the queue rendered inside it is wash-priv's own FE, so the
  // request is answerable here.
  await expect(page.locator('[data-testid="modal-body"] wash-app-priv')).toBeAttached({ timeout: 10_000 });

  // Escape puts it away without answering — the request stays queued.
  await page.keyboard.press('Escape');
  await expect(modal).toHaveCount(0);
});
