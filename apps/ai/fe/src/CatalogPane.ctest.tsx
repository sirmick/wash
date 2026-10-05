// Component test (Tier B) for the Setup tab: a curated catalog is three
// model slots, an auto one is an adapter; an edit is a draft until Save
// sends the whole catalog; a built-in with an override resets, the user's
// own deletes; a new catalog needs an id.

import { test, expect, afterEach } from 'vitest';
import { createSignal } from 'solid-js';
import { render, fireEvent, cleanup } from '@solidjs/testing-library';
import type { agentproto } from '@wash/ui';
import { CatalogPane, draftOf, specOf, type CatalogResult } from './CatalogPane.tsx';

afterEach(cleanup);

const adapters: agentproto.Adapter[] = [
  { id: 'codex', name: 'Codex', available: false, note: 'needs codex-acp on PATH' },
  { id: 'claude', name: 'Claude Code', available: true },
  { id: 'opencode', name: 'OpenCode', available: true },
];
const connections: agentproto.ConnectionView[] = [
  { id: 'claude@openrouter', adapter: 'claude', key: 'openrouter' },
  { id: 'opencode@openrouter', adapter: 'opencode', key: 'openrouter' },
];
const adapterOptions: agentproto.AdapterOptions[] = [{
  adapter: 'claude', version: '0.81.2',
  configs: [
    { id: 'model', name: 'Model', category: 'model', values: [{ value: 'sonnet', name: 'Sonnet' }, { value: 'haiku', name: 'Haiku' }] },
    { id: 'effort', name: 'Effort', category: 'thought_level', values: [{ value: 'low', name: 'Low' }, { value: 'high', name: 'High' }] },
  ],
}];
const slot = (s: string, adapter: string, model: string, extra: Partial<agentproto.SlotView> = {}): agentproto.SlotView =>
  ({ slot: s, adapter, model, available: true, ...extra });
const pro: agentproto.CatalogView = {
  id: 'anthropic-pro', name: 'Anthropic pro', available: true, builtin: true,
  slots: [slot('frontier', 'claude', 'claude-fable-5-1[1m]'), slot('coding', 'claude', 'sonnet'), slot('small', 'claude', 'haiku')],
};
const auto: agentproto.CatalogView = { id: 'openrouter', name: 'OpenRouter', adapter: 'opencode', connection: 'opencode@openrouter', available: true, builtin: true };
const mine: agentproto.CatalogView = {
  id: 'mine', name: 'Mine', available: true,
  slots: [slot('frontier', 'opencode', 'openrouter/x', { connection: 'opencode@openrouter', effort: 'high' }), slot('coding', 'opencode', 'openrouter/y', { connection: 'opencode@openrouter' }), slot('small', 'opencode', 'openrouter/z', { connection: 'opencode@openrouter' })],
};

const mount = (
  catalogs: agentproto.CatalogView[],
  results: Record<string, CatalogResult> = {},
  launch: agentproto.LaunchPrefs = {},
) => {
  const [list, setCatalogs] = createSignal(catalogs);
  const saved: [string, agentproto.CatalogSpec][] = [];
  const deleted: string[] = [];
  const defaults: [string, string][] = [];
  const r = render(() => (
    <CatalogPane
      catalogs={list()}
      adapters={adapters}
      connections={connections}
      adapterOptions={adapterOptions}
      results={results}
      launch={launch}
      onSave={(id, s) => saved.push([id, s])}
      onDelete={(id) => deleted.push(id)}
      onDefault={(c, m) => defaults.push([c, m])}
    />
  ));
  return { ...r, saved, deleted, defaults, setCatalogs };
};

const options = (el: HTMLElement) => [...(el as HTMLSelectElement).options].map((o) => o.value);

test('a curated catalog is three model slots, offering what its adapter last reported', () => {
  const { getByTestId, queryByTestId } = mount([pro]);
  for (const t of ['frontier', 'coding', 'small']) expect(getByTestId(`ai-catalog-slot-anthropic-pro-${t}`)).toBeTruthy();
  expect(queryByTestId('ai-catalog-slot-anthropic-pro-review')).toBeNull();
  expect((getByTestId('ai-catalog-model-anthropic-pro-coding') as HTMLInputElement).value).toBe('sonnet');
  expect(options(getByTestId('ai-catalog-effort-anthropic-pro-coding'))).toEqual(['', 'low', 'high']);
  expect(options(getByTestId('ai-catalog-connection-anthropic-pro-coding'))).toEqual(['', 'claude@openrouter']);
  expect(getByTestId('ai-catalog-origin-anthropic-pro').textContent).toBe('built in');
  // Nothing about permissions anywhere on the card.
  expect(getByTestId('ai-catalog-anthropic-pro').textContent).not.toMatch(/read-only|reviewer|capability/i);
});

test('an auto catalog is an adapter and a connection, and saves as such', () => {
  const { getByTestId, queryByTestId, saved } = mount([auto]);
  expect(queryByTestId('ai-catalog-slot-openrouter-frontier')).toBeNull();
  expect((getByTestId('ai-catalog-adapter-openrouter') as HTMLSelectElement).value).toBe('opencode');
  expect((getByTestId('ai-catalog-connection-openrouter') as HTMLSelectElement).value).toBe('opencode@openrouter');
  fireEvent.input(getByTestId('ai-catalog-connection-openrouter'), { target: { value: '' } });
  fireEvent.click(getByTestId('ai-catalog-save-openrouter'));
  expect(saved).toEqual([['openrouter', { name: 'OpenRouter', adapter: 'opencode' }]]);
});

test('an edit is a draft until Save, which sends the whole catalog', () => {
  const { getByTestId, saved } = mount([pro]);
  const save = getByTestId('ai-catalog-save-anthropic-pro') as HTMLButtonElement;
  expect(save.disabled).toBe(true);
  fireEvent.input(getByTestId('ai-catalog-model-anthropic-pro-coding'), { target: { value: 'opus[1m]' } });
  fireEvent.input(getByTestId('ai-catalog-effort-anthropic-pro-coding'), { target: { value: 'high' } });
  expect(save.disabled).toBe(false);
  fireEvent.click(save);
  expect(saved).toEqual([['anthropic-pro', {
    name: 'Anthropic pro',
    slots: {
      frontier: { adapter: 'claude', model: 'claude-fable-5-1[1m]' },
      coding: { adapter: 'claude', model: 'opus[1m]', effort: 'high' },
      small: { adapter: 'claude', model: 'haiku' },
    },
  }]]);
});

test('Discard drops the draft, and a roster push does not eat an edit in progress', () => {
  const { getByTestId, setCatalogs } = mount([pro]);
  const model = getByTestId('ai-catalog-model-anthropic-pro-small') as HTMLInputElement;
  fireEvent.input(model, { target: { value: 'sonnet' } });
  setCatalogs([{ ...pro, name: 'Anthropic pro (renamed elsewhere)' }]);
  // The same input, still on the page: a push must not rebuild the card
  // under the cursor.
  expect(model.isConnected).toBe(true);
  expect(model.value).toBe('sonnet');
  fireEvent.click(getByTestId('ai-catalog-discard-anthropic-pro'));
  expect(model.value).toBe('haiku');
  expect((getByTestId('ai-catalog-name-anthropic-pro') as HTMLInputElement).value).toBe('Anthropic pro (renamed elsewhere)');
});

test('changing the adapter of a slot clears its connection, model and effort', () => {
  const { getByTestId } = mount([mine]);
  fireEvent.input(getByTestId('ai-catalog-adapter-mine-frontier'), { target: { value: 'claude' } });
  expect((getByTestId('ai-catalog-model-mine-frontier') as HTMLInputElement).value).toBe('');
  expect((getByTestId('ai-catalog-connection-mine-frontier') as HTMLSelectElement).value).toBe('');
  expect(options(getByTestId('ai-catalog-connection-mine-frontier'))).toEqual(['', 'claude@openrouter']);
});

test('an overridden built-in resets; the user\'s own deletes; a plain built-in offers neither', () => {
  const { getByTestId, queryByTestId, deleted } = mount([{ ...pro, overridden: true }, mine]);
  expect(getByTestId('ai-catalog-origin-anthropic-pro').textContent).toBe('built in, changed here');
  expect(queryByTestId('ai-catalog-delete-anthropic-pro')).toBeNull();
  expect(queryByTestId('ai-catalog-reset-mine')).toBeNull();
  fireEvent.click(getByTestId('ai-catalog-reset-anthropic-pro'));
  fireEvent.click(getByTestId('ai-catalog-delete-mine'));
  expect(deleted).toEqual(['anthropic-pro', 'mine']);
  cleanup();
  const plain = mount([pro]);
  expect(plain.queryByTestId('ai-catalog-reset-anthropic-pro')).toBeNull();
  expect(plain.queryByTestId('ai-catalog-delete-anthropic-pro')).toBeNull();
});

test('a new catalog needs an id, a name and an adapter per slot, or an adapter alone', () => {
  const { getByTestId, saved } = mount([pro]);
  fireEvent.click(getByTestId('ai-catalog-add'));
  const save = getByTestId('ai-catalog-new-save') as HTMLButtonElement;
  expect(save.disabled).toBe(true);
  fireEvent.input(getByTestId('ai-catalog-new-id'), { target: { value: 'anthropic-pro' } });
  expect(save.title).toContain('already a catalog');
  fireEvent.input(getByTestId('ai-catalog-new-id'), { target: { value: 'budget' } });
  fireEvent.input(getByTestId('ai-catalog-name-budget'), { target: { value: 'Budget' } });
  for (const t of ['frontier', 'coding', 'small']) {
    fireEvent.input(getByTestId(`ai-catalog-adapter-budget-${t}`), { target: { value: 'opencode' } });
    fireEvent.input(getByTestId(`ai-catalog-connection-budget-${t}`), { target: { value: 'opencode@openrouter' } });
    fireEvent.input(getByTestId(`ai-catalog-model-budget-${t}`), { target: { value: `openrouter/${t}` } });
  }
  expect(save.disabled).toBe(false);
  fireEvent.click(save);
  expect(saved).toEqual([['budget', {
    name: 'Budget',
    slots: {
      frontier: { adapter: 'opencode', connection: 'opencode@openrouter', model: 'openrouter/frontier' },
      coding: { adapter: 'opencode', connection: 'opencode@openrouter', model: 'openrouter/coding' },
      small: { adapter: 'opencode', connection: 'opencode@openrouter', model: 'openrouter/small' },
    },
  }]]);
  // An adapter's own list: no slots at all.
  fireEvent.click(getByTestId('ai-catalog-add'));
  fireEvent.input(getByTestId('ai-catalog-new-id'), { target: { value: 'work' } });
  fireEvent.input(getByTestId('ai-catalog-new-kind'), { target: { value: 'auto' } });
  fireEvent.input(getByTestId('ai-catalog-name-work'), { target: { value: 'Work' } });
  fireEvent.input(getByTestId('ai-catalog-adapter-work'), { target: { value: 'claude' } });
  fireEvent.input(getByTestId('ai-catalog-connection-work'), { target: { value: 'claude@openrouter' } });
  fireEvent.click(getByTestId('ai-catalog-new-save'));
  expect(saved[1]).toEqual(['work', { name: 'Work', adapter: 'claude', connection: 'claude@openrouter' }]);
});

test('a save outcome is shown on its card, an error as one; an invalid catalog still lists its slots', () => {
  const broken: agentproto.CatalogView = { id: 'half', name: 'Half', available: false, note: 'catalog "half": has no coding slot', slots: [slot('frontier', 'claude', 'sonnet', { available: false })] };
  const { getByTestId } = mount([pro, broken], { 'anthropic-pro': { ok: false, detail: 'slot small: unknown adapter' } });
  expect(getByTestId('ai-catalog-result-anthropic-pro').textContent).toBe('slot small: unknown adapter');
  expect(getByTestId('ai-catalog-note-half').textContent).toContain('has no coding slot');
  expect((getByTestId('ai-catalog-adapter-half-coding') as HTMLSelectElement).value).toBe('');
});

test('draftOf and specOf round-trip a catalog, leaving empty fields out', () => {
  expect(specOf(draftOf(mine))).toEqual({
    name: 'Mine',
    slots: {
      frontier: { adapter: 'opencode', connection: 'opencode@openrouter', model: 'openrouter/x', effort: 'high' },
      coding: { adapter: 'opencode', connection: 'opencode@openrouter', model: 'openrouter/y' },
      small: { adapter: 'opencode', connection: 'opencode@openrouter', model: 'openrouter/z' },
    },
  });
  expect(specOf(draftOf(auto))).toEqual({ name: 'OpenRouter', adapter: 'opencode', connection: 'opencode@openrouter' });
});

// The default row: what "start an agent" means when nobody says otherwise
// (docs/PLACES.md §4.5). Editing it sends the WHOLE launch block, because
// agent_set_launch replaces it.
test('the default picker offers the catalogs that can start, and No default', () => {
  const r = mount([pro, auto, { ...mine, available: false, note: 'no key' }]);
  const sel = r.getByTestId('ai-default-catalog-select') as HTMLSelectElement;
  expect(options(sel)).toEqual(['', 'anthropic-pro', 'openrouter']);
  // A catalog that cannot start is not offerable as a default.
  expect(options(sel)).not.toContain('mine');
});

test('picking a default reports it, and clears the model with it', () => {
  const r = mount([pro, auto], {}, { catalog: 'auto-gone', model: 'stale' });
  fireEvent.input(r.getByTestId('ai-default-catalog-select'), { target: { value: 'anthropic-pro' } });
  // The model is cleared on a catalog change: a model id from the old
  // catalog means nothing in the new one.
  expect(r.defaults).toEqual([['anthropic-pro', '']]);
});

test('the model select appears only once a default catalog is chosen', () => {
  expect(mount([pro, auto]).queryByTestId('ai-default-model-select')).toBeNull();
  const r = mount([pro, auto], {}, { catalog: 'anthropic-pro' });
  const sel = r.getByTestId('ai-default-model-select') as HTMLSelectElement;
  // A curated catalog offers its slots, then anything else its adapter
  // reported — sonnet and haiku ARE this catalog's coding and small slots,
  // so they appear once as slots rather than twice.
  expect(options(sel)).toEqual(['frontier', 'coding', 'small']);
  fireEvent.input(sel, { target: { value: 'coding' } });
  expect(r.defaults).toEqual([['anthropic-pro', 'coding']]);
});

test('a curated default lists its frontier adapter\'s other models', () => {
  // The catalog itself carries no adapter — its slots do. Reading
  // catalog.adapter here (undefined) silently offered no models at all.
  const sparse: agentproto.CatalogView = {
    id: 'sparse', name: 'Sparse', available: true,
    slots: [slot('frontier', 'claude', 'opus'), slot('coding', 'claude', 'opus'), slot('small', 'claude', 'opus')],
  };
  const r = mount([sparse], {}, { catalog: 'sparse' });
  expect(options(r.getByTestId('ai-default-model-select'))).toEqual(['frontier', 'coding', 'small', 'sonnet', 'haiku']);
});

test('a stored default that can no longer start stays visible, disabled', () => {
  // Otherwise the select reads "No default" while every launch still
  // resolves against the broken one.
  const broken = { ...mine, available: false, note: 'no key' };
  const r = mount([pro, broken], {}, { catalog: 'mine' });
  const sel = r.getByTestId('ai-default-catalog-select') as HTMLSelectElement;
  expect(options(sel)).toContain('mine');
  const opt = [...sel.options].find((o) => o.value === 'mine')!;
  expect(opt.disabled).toBe(true);
  expect(opt.textContent).toContain('cannot start here');
  expect(sel.value).toBe('mine');
});

// A model the adapter did not list last time is a warning on the slot, not
// a refusal: the launch runs on the adapter's default and says so. The
// warning is what catches a typo before a workspace does.
test('a slot model not in the adapter list is flagged, a listed one is not', () => {
  const { getByTestId, queryByTestId } = mount([pro, mine]);
  expect(getByTestId('ai-catalog-model-note-anthropic-pro-frontier').textContent).toContain('claude-fable-5-1[1m] is not in the list Claude Code last reported');
  expect(queryByTestId('ai-catalog-model-note-anthropic-pro-coding')).toBeNull();
  // An adapter that has not reported a list yet cannot flag anything.
  expect(queryByTestId('ai-catalog-model-note-mine-frontier')).toBeNull();
  // Typing a listed model clears the note.
  fireEvent.input(getByTestId('ai-catalog-model-anthropic-pro-frontier'), { target: { value: 'haiku' } });
  expect(queryByTestId('ai-catalog-model-note-anthropic-pro-frontier')).toBeNull();
});
