// Component test (Tier B) for the text-field context menu.
//
// The two things worth pinning are not the rendering but the judgement
// calls: which surfaces this menu must keep its hands off (the terminal
// and the VM display own right-click, and an app that handled the event
// first has said it wants to), and that a password field's contents never
// reach a clipboard every app can read.

import { test, expect, afterEach, vi } from 'vitest';
import { render, cleanup, fireEvent } from '@solidjs/testing-library';
import { fieldFromEvent, TextFieldMenu } from './text-menu.tsx';

// Menu portals into document.body, so items are never inside the render
// container — query the document, and read `disabled` off the attribute
// rather than assuming a jest-dom matcher.
const item = (id: string): HTMLButtonElement => {
  const el = document.querySelector(`[data-testid="${id}"]`);
  if (!el) throw new Error(`no menu item ${id}`);
  return el as HTMLButtonElement;
};
const isDisabled = (id: string) => item(id).disabled;

afterEach(() => {
  cleanup();
  document.body.innerHTML = '';
});

const ctx = (): MouseEvent => new MouseEvent('contextmenu', { bubbles: true, cancelable: true });

// mount puts real nodes in the document so closest() has a tree to walk.
function mount(html: string): HTMLElement {
  const host = document.createElement('div');
  host.innerHTML = html;
  document.body.appendChild(host);
  return host;
}

test('claims ordinary text fields', () => {
  const host = mount('<input type="text"><textarea></textarea><input type="search">');
  for (const el of Array.from(host.children)) {
    const ev = ctx();
    el.dispatchEvent(ev);
    expect(fieldFromEvent(ev)).toBe(el);
  }
});

test('leaves the terminal and the VM display alone', () => {
  // xterm's helper textarea is a real textarea; the terminal's own menu has
  // plain-right-click behaviour this would preempt. Right-click on the VM
  // display belongs to the guest.
  const host = mount('<div class="xterm"><textarea></textarea></div><wash-app-display><input></wash-app-display>');
  for (const el of Array.from(host.querySelectorAll('textarea, input'))) {
    const ev = ctx();
    el.dispatchEvent(ev);
    expect(fieldFromEvent(ev)).toBeNull();
  }
});

test('yields to an app that handled the event itself', () => {
  const host = mount('<input type="text">');
  const el = host.firstElementChild!;
  const ev = ctx();
  el.dispatchEvent(ev);
  ev.preventDefault();
  expect(fieldFromEvent(ev)).toBeNull();
});

test('ignores inputs with nothing to edit, and disabled ones', () => {
  const host = mount('<input type="checkbox"><input type="range"><input type="file"><input type="text" disabled>');
  for (const el of Array.from(host.children)) {
    const ev = ctx();
    el.dispatchEvent(ev);
    expect(fieldFromEvent(ev)).toBeNull();
  }
});

test('a password field offers Paste but never Copy or Cut', () => {
  const host = mount('<input type="password" value="hunter2">');
  const el = host.firstElementChild as HTMLInputElement;
  el.setSelectionRange(0, 7);
  render(() => <TextFieldMenu x={0} y={0} el={el} onDismiss={() => {}} />);
  expect(isDisabled('text-ctx-copy')).toBe(true);
  expect(isDisabled('text-ctx-cut')).toBe(true);
  expect(isDisabled('text-ctx-paste')).toBe(false);
});

test('Copy and Cut need a selection; Cut and Paste need a writable field', () => {
  const host = mount('<input type="text" value="abc"><input type="text" value="abc" readonly>');
  const [plain, ro] = Array.from(host.children) as HTMLInputElement[];

  render(() => <TextFieldMenu x={0} y={0} el={plain} onDismiss={() => {}} />);
  expect(isDisabled('text-ctx-copy')).toBe(true); // no selection yet
  expect(isDisabled('text-ctx-paste')).toBe(false);
  cleanup();

  ro.setSelectionRange(0, 3);
  render(() => <TextFieldMenu x={0} y={0} el={ro} onDismiss={() => {}} />);
  expect(isDisabled('text-ctx-copy')).toBe(false); // reading is fine
  expect(isDisabled('text-ctx-cut')).toBe(true);
  expect(isDisabled('text-ctx-paste')).toBe(true);
});

test('Copy puts the selection on the clipboard and hands the field back', () => {
  const setText = vi.fn();
  (window as unknown as { wash: unknown }).wash = { clipboardSetText: setText, clipboardGetText: async () => '' };
  const host = mount('<input type="text" value="hello world">');
  const el = host.firstElementChild as HTMLInputElement;
  el.setSelectionRange(6, 11);
  const onDismiss = vi.fn();
  render(() => <TextFieldMenu x={0} y={0} el={el} onDismiss={onDismiss} />);
  fireEvent.click(item('text-ctx-copy'));
  expect(setText).toHaveBeenCalledWith('world');
  expect(onDismiss).toHaveBeenCalled();
  // The selection is read when the menu opens, because pressing an item
  // moves focus out of the field and the browser drops it.
  expect(document.activeElement).toBe(el);
  expect([el.selectionStart, el.selectionEnd]).toEqual([6, 11]);
});
