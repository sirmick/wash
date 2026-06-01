// Component test (Tier B) for the notifications sidebar widget — the pure
// renderer of the notify service snapshot. Covers empty/populated rendering
// and the mark-read / clear-all callbacks in jsdom, fast. The cross-process
// "a real notification arrives over the wire and the section auto-expands"
// flow stays in sidebar.spec.ts (that's the integration seam).

import { test, expect, afterEach } from 'vitest';
import { render, fireEvent, cleanup } from '@solidjs/testing-library';
import { createSignal } from 'solid-js';
import { NotifyWidget, type NotifyEntry } from './NotifyWidget.tsx';

afterEach(cleanup);

const entry = (over: Partial<NotifyEntry> = {}): NotifyEntry => ({
  id: 'n1',
  source_app: 'com.wash.test',
  source_instance: 'i1',
  title: 'Hello',
  body: 'world',
  level: 'info',
  created_sec: 0,
  read: false,
  ...over,
});

test('empty: shows the hint, no clear-all button', () => {
  const { queryByTestId } = render(() => (
    <NotifyWidget notifications={() => []} onMarkRead={() => {}} onClearAll={() => {}} />
  ));
  expect(queryByTestId('notify-empty')).toBeTruthy();
  expect(queryByTestId('notify-clear-all')).toBeNull();
});

test('populated: one row per entry, level + read reflected, clear-all present', () => {
  const notes = [entry({ id: 'a', level: 'error', read: false }), entry({ id: 'b', level: 'info', read: true })];
  const { queryByTestId, getByTestId } = render(() => (
    <NotifyWidget notifications={() => notes} onMarkRead={() => {}} onClearAll={() => {}} />
  ));
  expect(queryByTestId('notify-empty')).toBeNull();
  expect(getByTestId('notify-row-a').getAttribute('data-level')).toBe('error');
  expect(getByTestId('notify-row-a').getAttribute('data-read')).toBe('false');
  expect(getByTestId('notify-row-b').getAttribute('data-read')).toBe('true');
  expect(queryByTestId('notify-clear-all')).toBeTruthy();
});

test('clicking a row marks it read by id; clear-all fires once', () => {
  const marked: string[] = [];
  let cleared = 0;
  const notes = [entry({ id: 'x' })];
  const { getByTestId } = render(() => (
    <NotifyWidget notifications={() => notes} onMarkRead={(id) => marked.push(id)} onClearAll={() => { cleared++; }} />
  ));
  fireEvent.click(getByTestId('notify-row-x'));
  expect(marked).toEqual(['x']);
  fireEvent.click(getByTestId('notify-clear-all'));
  expect(cleared).toBe(1);
});

test('reactively flips empty → populated when the signal changes', () => {
  const [notes, setNotes] = createSignal<NotifyEntry[]>([]);
  const { queryByTestId } = render(() => (
    <NotifyWidget notifications={notes} onMarkRead={() => {}} onClearAll={() => {}} />
  ));
  expect(queryByTestId('notify-empty')).toBeTruthy();
  setNotes([entry({ id: 'z' })]);
  expect(queryByTestId('notify-empty')).toBeNull();
  expect(queryByTestId('notify-row-z')).toBeTruthy();
});
