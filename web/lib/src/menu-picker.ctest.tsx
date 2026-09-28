import { test, expect, afterEach, beforeEach } from 'vitest';
import { render, cleanup, fireEvent } from '@solidjs/testing-library';
import { MenuPicker, filterOptions, PICKER_FILTER_AT } from './menu-picker';
import type { PickerOption } from './menu-picker';

beforeEach(() => {
  Element.prototype.scrollIntoView = () => {};
});
afterEach(cleanup);

const many: PickerOption[] = Array.from({ length: 300 }, (_, i) => ({
  value: `vendor-${i % 3}/model-${i}`,
  label: `Vendor ${i % 3}: Model ${i}`,
}));

// The picker portals into document.body, outside render()'s container.
const rows = () => [...document.body.querySelectorAll('button')];
const byTestId = (id: string) => document.body.querySelector(`[data-testid="${id}"]`);

test('filterOptions matches every word, in label or value', () => {
  const hits = filterOptions(many, 'vendor-2 model-29').map((o) => o.value);
  expect(hits[0]).toBe('vendor-2/model-29');
  expect(hits.every((v) => v.startsWith('vendor-2/model-29'))).toBe(true);
  expect(filterOptions(many, '  ')).toHaveLength(300);
});

test('a long list filters as you type; Enter takes the first match', () => {
  const picked: string[] = [];
  let dismissed = 0;
  render(() => (
    <MenuPicker x={0} y={0} title="Model" options={many} current="vendor-0/model-0"
      onPick={(v) => picked.push(v)} onDismiss={() => dismissed++} data-testid="p" />
  ));
  expect(rows()).toHaveLength(300);
  const filter = byTestId('p-filter') as HTMLInputElement;
  expect(document.activeElement).toBe(filter);
  fireEvent.input(filter, { target: { value: 'model-29' } });
  // model-29 and model-290 … model-299
  expect(rows()).toHaveLength(11);
  fireEvent.keyDown(filter, { key: 'Enter' });
  expect(picked).toEqual(['vendor-2/model-29']);
  expect(dismissed).toBe(1);
});

test('a short list has no filter and ticks the current value', () => {
  const opts = many.slice(0, PICKER_FILTER_AT - 1);
  render(() => (
    <MenuPicker x={0} y={0} options={opts} current={opts[2].value} onPick={() => {}} onDismiss={() => {}} data-testid="p" />
  ));
  expect(byTestId('p-filter')).toBeNull();
  expect(byTestId(`p-${opts[2].value}`)?.textContent).toContain('✓');
});
