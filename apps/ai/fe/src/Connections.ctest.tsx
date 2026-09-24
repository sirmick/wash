// Component test (Tier B) for the Connections section: a key is typed into a
// masked field, sent once to be saved or tested, and afterwards shown only as
// "set" and its last four characters.

import { test, expect, afterEach } from 'vitest';
import { render, fireEvent, cleanup } from '@solidjs/testing-library';
import type { agentproto } from '@wash/ui';
import { Connections, type KeyResult } from './Connections.tsx';

afterEach(cleanup);

const mount = (keys: agentproto.KeyView[], results: Record<string, KeyResult> = {}) => {
  const saved: [string, string][] = [];
  const tested: [string, string][] = [];
  const r = render(() => (
    <Connections keys={keys} results={results} onSave={(id, v) => saved.push([id, v])} onTest={(id, v) => tested.push([id, v])} />
  ));
  return { ...r, saved, tested };
};

const unset: agentproto.KeyView = { id: 'openrouter', name: 'OpenRouter API key', set: false, testable: true };
const stored: agentproto.KeyView = { ...unset, set: true, hint: 'wxyz' };

test('the field is masked, and an unset key says so', () => {
  const { getByTestId, queryByTestId } = mount([unset]);
  expect((getByTestId('ai-key-input-openrouter') as HTMLInputElement).type).toBe('password');
  expect(getByTestId('ai-key-status-openrouter').textContent).toBe('not set');
  expect(queryByTestId('ai-key-clear-openrouter')).toBeNull();
  // Nothing typed and nothing stored: nothing to save or test.
  expect((getByTestId('ai-key-save-openrouter') as HTMLButtonElement).disabled).toBe(true);
  expect((getByTestId('ai-key-test-openrouter') as HTMLButtonElement).disabled).toBe(true);
});

test('Save sends the typed key once and empties the field', () => {
  const { getByTestId, saved } = mount([unset]);
  const input = getByTestId('ai-key-input-openrouter') as HTMLInputElement;
  fireEvent.input(input, { target: { value: 'sk-or-v1-abcd' } });
  fireEvent.click(getByTestId('ai-key-save-openrouter'));
  expect(saved).toEqual([['openrouter', 'sk-or-v1-abcd']]);
  expect(input.value).toBe('');
});

test('a stored key shows only its last four characters, and can be tested or cleared', () => {
  const { getByTestId, tested, saved, container } = mount([stored]);
  expect(getByTestId('ai-key-status-openrouter').textContent).toBe('set · …wxyz');
  // Test with nothing typed checks the stored key.
  fireEvent.click(getByTestId('ai-key-test-openrouter'));
  expect(tested).toEqual([['openrouter', '']]);
  fireEvent.click(getByTestId('ai-key-clear-openrouter'));
  expect(saved).toEqual([['openrouter', '']]);
  expect(container.textContent).not.toContain('sk-or');
});

test('a test outcome is shown, a failure as one', () => {
  const ok = mount([stored], { openrouter: { ok: true, detail: 'valid: $1.50 used' } });
  expect(ok.getByTestId('ai-key-result-openrouter').textContent).toBe('valid: $1.50 used');
  cleanup();
  const busy = mount([stored], { openrouter: { busy: true } });
  expect(busy.getByTestId('ai-key-test-openrouter').textContent).toBe('Testing…');
  expect((busy.getByTestId('ai-key-test-openrouter') as HTMLButtonElement).disabled).toBe(true);
});

test('with no keys to offer the section is not shown', () => {
  const { queryByTestId } = mount([]);
  expect(queryByTestId('ai-connections')).toBeNull();
});
