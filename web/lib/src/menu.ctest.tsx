// Component test (Tier B) for Menu's dismiss-on-outside-press.
//
// The bug that earned this file: an open menu stayed on screen while the
// window it belonged to was dragged away, leaving it anchored to
// coordinates that no longer meant anything. Menu listened for mousedown,
// and the shell's titlebar drag calls preventDefault() on pointerdown —
// which suppresses the compatibility mousedown entirely, so the listener
// never heard the gesture that moved the window.

import { test, expect, afterEach } from 'vitest';
import { render, cleanup } from '@solidjs/testing-library';
import { Menu, MenuItem } from './menu.tsx';

afterEach(cleanup);

// press dispatches the gesture the way a real pointer does — pointerdown
// first, then the mouse compatibility event — with `prevented` standing in
// for a handler that called preventDefault() on the pointer event, which
// is what stops the browser emitting mousedown at all.
function press(target: EventTarget, opts: { prevented?: boolean } = {}) {
  target.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, cancelable: true }));
  if (!opts.prevented) {
    target.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true }));
  }
}

// The dismiss listener is installed on a setTimeout(0), so the click that
// opened the menu can't immediately close it.
const armed = () => new Promise((r) => setTimeout(r, 1));

function open() {
  let dismissed = 0;
  const view = render(() => (
    <Menu x={10} y={10} onDismiss={() => { dismissed++; }} data-testid="m">
      <MenuItem label="Item" onClick={() => {}} />
    </Menu>
  ));
  return { ...view, dismissed: () => dismissed };
}

test('a press outside dismisses', async () => {
  const m = open();
  await armed();
  press(document.body);
  expect(m.dismissed()).toBe(1);
});

test('a press whose mouse event is suppressed still dismisses', async () => {
  // The titlebar-drag case: preventDefault() on pointerdown means no
  // mousedown ever arrives.
  const m = open();
  await armed();
  press(document.body, { prevented: true });
  expect(m.dismissed()).toBe(1);
});

test('a press that stops propagating still dismisses', async () => {
  // Window chrome stops pointerdown bubbling so the titlebar's drag does
  // not start under its own buttons. A bubbling-phase listener would
  // never see it; the capture phase runs first.
  const m = open();
  await armed();
  const quiet = document.createElement('div');
  quiet.addEventListener('pointerdown', (ev) => ev.stopPropagation());
  document.body.appendChild(quiet);
  press(quiet);
  expect(m.dismissed()).toBe(1);
  quiet.remove();
});

test('a press inside the menu does not dismiss', async () => {
  const m = open();
  await armed();
  // Queried off document, not the render container: every menu portals
  // into document.body so an overflow:auto ancestor cannot clip it.
  const el = document.querySelector('[data-testid="m"]')!;
  expect(el).toBeTruthy();
  press(el);
  expect(m.dismissed()).toBe(0);
});

test('the press that opened the menu does not immediately close it', () => {
  // Listener not yet armed: no await.
  const m = open();
  press(document.body);
  expect(m.dismissed()).toBe(0);
});
