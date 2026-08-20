// Toast rendering, with the host attribution added in docs/SIDEBAR.md M0.
// Imports @wash/ui (tokens) via notify.ts, so it runs under vitest rather
// than node:test. jsdom supplies document/requestAnimationFrame.
//
// @vitest-environment jsdom

import { describe, it, expect, beforeEach } from 'vitest';
import { showToast } from './notify.ts';
import { hostColor } from './host-colors.ts';

const stack = () => document.querySelector('[data-testid="notification-stack"]');
const cards = () => Array.from(document.querySelectorAll('[data-testid="notification"]'));

describe('showToast host attribution', () => {
  beforeEach(() => {
    // Clear the cards, NOT document.body — ensureContainer() caches the
    // stack element in module scope, so wiping the body would detach it
    // and every later toast would append to an orphan.
    cards().forEach((c) => c.remove());
  });

  it('a local toast carries no host label and no stripe', () => {
    showToast({ instanceID: 'i-1', title: 'done' });
    const card = cards()[0] as HTMLElement;
    expect(card).toBeTruthy();
    expect(card.querySelector('[data-testid="notification-host"]')).toBeNull();
    expect(card.dataset.origin).toBeUndefined();
  });

  it('passing LOCAL explicitly is still treated as local', () => {
    // hostColor('local') is null, so the origin field alone must not
    // promote a local toast into the remote presentation.
    showToast({ instanceID: 'i-1', title: 'done', origin: 'local' });
    const card = cards()[0] as HTMLElement;
    expect(card.querySelector('[data-testid="notification-host"]')).toBeNull();
    expect(hostColor('local')).toBeNull();
  });

  it('a remote toast names its host and takes that host hue', () => {
    showToast({ instanceID: 'build01␟i-7', title: 'build finished', origin: 'build01' });
    const card = cards()[0] as HTMLElement;
    const who = card.querySelector('[data-testid="notification-host"]');
    expect(who?.textContent).toBe('build01');
    expect(card.dataset.origin).toBe('build01');
    // The stripe itself is deliberately NOT asserted: the accent tokens are
    // `var(--wash-accent-*, #fallback)` and jsdom's CSS parser drops a var()
    // colour inside the border-left shorthand, so any assertion here would
    // be testing jsdom rather than the toast. What matters behaviourally —
    // that this origin resolves to a stripe at all — is asserted via
    // hostColor; the rendered hue belongs to the screenshot tier.
    expect(hostColor('build01')).toBeTruthy();
  });

  it('click activates with the compound id, so focus can find the window', () => {
    let got = '';
    showToast({
      instanceID: 'build01␟i-7',
      title: 'x',
      origin: 'build01',
      onActivate: (id) => { got = id; },
    });
    (cards()[0] as HTMLElement).click();
    expect(got).toBe('build01␟i-7');
  });

  it('toasts from two hosts stack together and stay distinguishable', () => {
    showToast({ instanceID: 'i-1', title: 'local job' });
    showToast({ instanceID: 'build01␟i-7', title: 'remote job', origin: 'build01' });
    expect(stack()).toBeTruthy();
    expect(cards()).toHaveLength(2);
    const origins = cards().map((c) => (c as HTMLElement).dataset.origin);
    expect(origins).toEqual([undefined, 'build01']);
  });
});
