// Component tests for the screensaver overlay and host (jsdom). The kernels
// (idle timer, config) are unit-tested in screensaver.test.ts.

import { afterEach, describe, expect, it, vi } from 'vitest';
import { showScreensaver, startScreensaverHost, WAKE_GRACE_MS, WAKE_MOVE_PX } from './screensaver';

const overlay = () => document.querySelector<HTMLElement>('[data-testid="screensaver"]');

// What the page underneath sees: listeners in the bubble phase on document.
function pageSpy() {
  const seen: string[] = [];
  const types = ['keydown', 'keyup', 'pointerdown', 'pointerup', 'click', 'wheel'];
  const fn = (e: Event) => seen.push(e.type);
  for (const t of types) document.addEventListener(t, fn);
  return { seen, stop: () => types.forEach((t) => document.removeEventListener(t, fn)) };
}

afterEach(() => {
  vi.useRealTimers();
  document.body.innerHTML = '';
});

describe('showScreensaver', () => {
  it('covers the page and dismisses on a key, swallowing keydown and keyup', () => {
    const page = pageSpy();
    let dismissed = 0;
    showScreensaver('blank', undefined, () => dismissed++);
    expect(overlay()).not.toBeNull();
    expect(overlay()!.style.zIndex).toBe('2147483647');

    document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'a', bubbles: true, cancelable: true }));
    expect(overlay()).toBeNull();
    expect(dismissed).toBe(1);
    document.body.dispatchEvent(new KeyboardEvent('keyup', { key: 'a', bubbles: true, cancelable: true }));
    expect(page.seen).toEqual([]);

    // The next key after that is the page's again.
    document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'b', bubbles: true }));
    expect(page.seen).toEqual(['keydown']);
    page.stop();
  });

  it('a click wakes it without the press, release or click reaching the page', () => {
    const page = pageSpy();
    showScreensaver('blank', undefined);
    for (const t of ['pointerdown', 'pointerup']) document.body.dispatchEvent(new Event(t, { bubbles: true, cancelable: true }));
    document.body.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    expect(overlay()).toBeNull();
    expect(page.seen).toEqual([]);
    page.stop();
  });

  it('ignores mouse motion during the grace period and small jitter after it', () => {
    vi.useFakeTimers({ toFake: ['performance', 'setTimeout', 'requestAnimationFrame'] });
    showScreensaver('blank', undefined);
    const move = (x: number, y: number) =>
      document.body.dispatchEvent(new MouseEvent('pointermove', { clientX: x, clientY: y, bubbles: true, cancelable: true }));
    move(0, 0);
    move(500, 500);
    expect(overlay()).not.toBeNull();
    vi.advanceTimersByTime(WAKE_GRACE_MS + 10);
    move(100, 100);
    move(100 + WAKE_MOVE_PX - 2, 100);
    expect(overlay()).not.toBeNull();
    move(100 + WAKE_MOVE_PX + 1, 100);
    expect(overlay()).toBeNull();
  });

  it('takes focus off the focused element and gives it back', () => {
    const input = document.createElement('input');
    document.body.appendChild(input);
    input.focus();
    expect(document.activeElement).toBe(input);
    showScreensaver('blank', undefined);
    expect(document.activeElement).not.toBe(input);
    document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
    expect(document.activeElement).toBe(input);
  });
});

describe('startScreensaverHost', () => {
  it('shows the saver after the idle timeout and not while disabled', () => {
    vi.useFakeTimers();
    const host = startScreensaverHost(window);
    host.configure({ enabled: false, timeout_s: 1, saver: 'blank' });
    vi.advanceTimersByTime(5000);
    expect(overlay()).toBeNull();

    host.configure({ enabled: true, timeout_s: 2, saver: 'blank' });
    vi.advanceTimersByTime(1000);
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'x' })); // activity resets
    vi.advanceTimersByTime(1500);
    expect(overlay()).toBeNull();
    vi.advanceTimersByTime(600);
    expect(overlay()?.dataset.saver).toBe('blank');
    expect(host.active).toBe(true);

    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'x', cancelable: true }));
    expect(overlay()).toBeNull();
    expect(host.active).toBe(false);
    // And it comes back after another idle period.
    vi.advanceTimersByTime(2000);
    expect(overlay()).not.toBeNull();
    host.dispose();
    expect(overlay()).toBeNull();
  });
});
