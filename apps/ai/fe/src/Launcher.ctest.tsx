// Component test (Tier B) for the new-session form: catalog, model, folder,
// the Permissions row, and the Advanced settings. The form owns no launch
// logic, so what is tested is what it shows and what Start would send;
// agentd's resolution is Go's.

import { test, expect, afterEach } from 'vitest';
import { createSignal } from 'solid-js';
import { render, fireEvent, cleanup } from '@solidjs/testing-library';
import type { agentproto } from '@wash/ui';
import { Launcher, emptyForm, launchBlocker, startMessage, type LaunchForm } from './Launcher.tsx';

afterEach(cleanup);

const adapters: agentproto.Adapter[] = [
  { id: 'codex', name: 'Codex', available: false, note: 'needs codex-acp on PATH' },
  { id: 'claude', name: 'Claude Code', available: true },
  { id: 'opencode', name: 'OpenCode', available: true },
];

const slots = (adapter: string, extra: Partial<agentproto.SlotView> = {}) =>
  ['frontier', 'coding', 'small'].map((slot) => ({ slot, adapter, model: `${adapter}-${slot}`, available: true, ...extra })) as agentproto.SlotView[];

const catalogs: agentproto.CatalogView[] = [
  { id: 'anthropic', name: 'Anthropic', adapter: 'claude', available: true, builtin: true },
  { id: 'anthropic-pro', name: 'Anthropic pro', available: true, builtin: true, slots: slots('claude') },
  { id: 'openai', name: 'OpenAI', adapter: 'codex', available: false, note: 'Codex: needs codex-acp on PATH', builtin: true },
  { id: 'openrouter-budget', name: 'OpenRouter budget', available: true, builtin: true, slots: slots('opencode', { connection: 'opencode@openrouter', effort: 'high' }) },
];

// What Claude Code reported the last time it ran: its presets, models and
// settings. OpenCode has never run here.
const adapterOptions: agentproto.AdapterOptions[] = [{
  adapter: 'claude', version: '0.81.2',
  modes: [{ id: 'default', name: 'Default' }, { id: 'acceptEdits', name: 'Accept edits' }, { id: 'plan', name: 'Plan' }],
  configs: [
    { id: 'mode', name: 'Mode', category: 'mode', values: [{ value: 'default', name: 'Default' }, { value: 'plan', name: 'Plan' }] },
    { id: 'model', name: 'Model', category: 'model', values: [{ value: 'claude-frontier', name: 'Frontier' }, { value: 'sonnet', name: 'Sonnet' }, { value: 'haiku', name: 'Haiku' }] },
    { id: 'effort', name: 'Effort', category: 'thought_level', values: [{ value: 'low', name: 'Low' }, { value: 'high', name: 'High' }] },
    { id: 'fast', name: 'Fast mode', values: [{ value: 'off', name: 'Off' }, { value: 'on', name: 'On' }] },
  ],
}];

const mount = (initial: Partial<LaunchForm> = {}, over: { catalogs?: agentproto.CatalogView[]; launch?: agentproto.LaunchPrefs } = {}) => {
  const [form, setForm] = createSignal<LaunchForm>({ ...emptyForm(), catalog: 'anthropic-pro', ...initial });
  const [list, setCatalogs] = createSignal(over.catalogs ?? catalogs);
  const [launch, setLaunch] = createSignal<agentproto.LaunchPrefs>(over.launch ?? {});
  const started: agentproto.AgentStart[] = [];
  const launches: agentproto.LaunchPrefs[] = [];
  const r = render(() => (
    <Launcher
      catalogs={list()}
      adapters={adapters}
      adapterOptions={adapterOptions}
      launch={launch()}
      onLaunch={(p) => { launches.push(p); setLaunch(p); }}
      form={form()}
      onForm={(p) => setForm((f) => ({ ...f, ...p }))}
      onStart={() => started.push(startMessage(form(), list(), launch()))}
      onPickFolder={() => {}}
      starting={false}
      error=""
    />
  ));
  return { ...r, form, setCatalogs, started, launches };
};

const options = (el: HTMLElement) =>
  [...(el as HTMLSelectElement).options].map((o) => ({ value: o.value, label: o.textContent, disabled: o.disabled }));

test('it offers the catalogs, greying the ones that cannot start with the reason', () => {
  const { getByTestId } = mount();
  const opts = options(getByTestId('ai-catalog-select'));
  expect(opts.map((o) => o.value)).toEqual(['', 'anthropic', 'anthropic-pro', 'openai', 'openrouter-budget']);
  const openai = opts.find((o) => o.value === 'openai')!;
  expect(openai.disabled).toBe(true);
  expect(openai.label).toContain('needs codex-acp on PATH');
});

// The catalog and the list of catalogs arrive in the same roster push. The
// select must show the catalog the form holds, not the first option.
test('the catalog select shows the form\'s catalog even when the catalogs arrive after it', () => {
  const { getByTestId, setCatalogs } = mount({ catalog: 'openrouter-budget' }, { catalogs: [] });
  const sel = getByTestId('ai-catalog-select') as HTMLSelectElement;
  expect(sel.value).toBe('');
  setCatalogs(catalogs);
  expect(sel.value).toBe('openrouter-budget');
});

test('a curated catalog lists its slots, then the other models its adapter reported', () => {
  const { getByTestId } = mount();
  const sel = getByTestId('ai-model-select') as HTMLSelectElement;
  expect(options(sel).map((o) => o.value)).toEqual(['frontier', 'coding', 'small', 'sonnet', 'haiku']);
  expect(options(sel)[0].label).toBe('Frontier — claude-frontier');
  expect(sel.value).toBe('frontier');
  expect(getByTestId('ai-model-summary').textContent).toBe('Claude Code · claude-frontier');
});

test('an auto catalog lists what its adapter reported, or only its default', () => {
  const seen = mount({ catalog: 'anthropic' });
  expect(options(seen.getByTestId('ai-model-select')).map((o) => o.value)).toEqual(['', 'claude-frontier', 'sonnet', 'haiku']);
  expect(seen.getByTestId('ai-model-summary').textContent).toBe('Claude Code · default model');
  cleanup();
  const unseen = mount({ catalog: 'openai' }, { catalogs: [{ ...catalogs[2], available: true, note: undefined }] });
  expect(options(unseen.getByTestId('ai-model-select')).map((o) => o.label)).toEqual(["Codex's default"]);
});

test('Start sends the catalog and only the choices made', () => {
  const { getByTestId, started } = mount({ catalog: 'openrouter-budget', cwd: '/w' });
  fireEvent.click(getByTestId('ai-start'));
  fireEvent.input(getByTestId('ai-model-select'), { target: { value: 'small' } });
  fireEvent.click(getByTestId('ai-start'));
  expect(started).toEqual([
    { kind: 'agent_start', cwd: '/w', open: true, catalog: 'openrouter-budget' },
    { kind: 'agent_start', cwd: '/w', open: true, catalog: 'openrouter-budget', model: 'small' },
  ]);
  expect(getByTestId('ai-model-summary').textContent).toBe('OpenCode · opencode-small · effort high · via openrouter');
});

// Advanced holds the adapter's own settings, by the names it reported, and
// says so where it has never run.
test('Advanced settings go with the start and show on the summary line', () => {
  const { getByTestId, started } = mount({ catalog: 'anthropic-pro', model: 'coding' });
  expect(options(getByTestId('ai-config-effort'))[0].label).toBe('default');
  fireEvent.input(getByTestId('ai-config-effort'), { target: { value: 'high' } });
  fireEvent.input(getByTestId('ai-config-fast'), { target: { value: 'on' } });
  expect(getByTestId('ai-model-summary').textContent).toBe('Claude Code · claude-coding · effort high · fast mode on');
  fireEvent.click(getByTestId('ai-start'));
  expect(started[0]).toEqual({ kind: 'agent_start', cwd: '', open: true, catalog: 'anthropic-pro', model: 'coding', configs: { effort: 'high', fast: 'on' } });
  cleanup();
  const unseen = mount({ catalog: 'openrouter-budget' });
  expect(unseen.getByTestId('ai-advanced-empty').textContent).toContain('after it has run once');
});

test('changing the catalog clears the model and settings chosen for the old one', () => {
  const { getByTestId, form } = mount({ catalog: 'anthropic-pro', model: 'coding', configs: { effort: 'high' } });
  fireEvent.input(getByTestId('ai-catalog-select'), { target: { value: 'anthropic' } });
  expect(form()).toMatchObject({ catalog: 'anthropic', model: '', configs: {} });
});

// The Permissions row shows the remembered default for the adapter that
// will run, a change writes the default, and Start sends what the row
// shows.
test('the Permissions row is the remembered default, and goes with the start', () => {
  const { getByTestId, started, launches } = mount({ catalog: 'anthropic-pro' }, { launch: { mode: { claude: 'acceptEdits' } } });
  const mode = getByTestId('ai-mode-select') as HTMLSelectElement;
  expect(options(mode).map((o) => o.value)).toEqual(['', 'default', 'acceptEdits', 'plan']);
  expect(mode.value).toBe('acceptEdits');
  fireEvent.input(mode, { target: { value: 'plan' } });
  fireEvent.click(getByTestId('ai-yolo'));
  expect(launches).toEqual([
    { mode: { claude: 'plan' } },
    { mode: { claude: 'plan' }, yolo: true },
  ]);
  fireEvent.click(getByTestId('ai-start'));
  expect(started[0]).toMatchObject({ catalog: 'anthropic-pro', mode: 'plan', yolo: true });
});

// Permissions are collapsed like Advanced, so the summary line has to say
// what the next start gets — above all, that auto-approve is on.
test('the Permissions expander starts collapsed and summarises the default', () => {
  const { getByTestId } = mount({ catalog: 'anthropic-pro' }, { launch: { mode: { claude: 'acceptEdits' } } });
  expect((getByTestId('ai-permissions') as HTMLDetailsElement).open).toBe(false);
  expect(getByTestId('ai-permissions-summary').textContent).toBe('Accept edits');
  fireEvent.click(getByTestId('ai-yolo'));
  expect(getByTestId('ai-permissions-summary').textContent).toBe('Accept edits · auto-approve on');
});

test('the mode is remembered per adapter, and an adapter never seen has no presets to offer', () => {
  const { getByTestId, started } = mount({ catalog: 'openrouter-budget' }, { launch: { mode: { claude: 'acceptEdits' } } });
  const mode = getByTestId('ai-mode-select') as HTMLSelectElement;
  expect(mode.disabled).toBe(true);
  expect(options(mode)[0].label).toContain('after it has run once');
  fireEvent.click(getByTestId('ai-start'));
  // Claude's remembered mode does not leak onto an OpenCode start.
  expect(started[0].mode).toBeUndefined();
});

test('an unavailable catalog cannot be started, and says why', () => {
  const { getByTestId, started } = mount({ catalog: 'openai' });
  expect((getByTestId('ai-start') as HTMLButtonElement).disabled).toBe(true);
  expect(getByTestId('ai-start-blocker').textContent).toContain('needs codex-acp');
  fireEvent.click(getByTestId('ai-start'));
  expect(started).toEqual([]);
});

test('launchBlocker with nothing chosen asks for a catalog', () => {
  expect(launchBlocker({ ...emptyForm() }, catalogs)).toContain('Choose a catalog');
  expect(launchBlocker({ ...emptyForm(), catalog: 'anthropic' }, catalogs)).toBe('');
});
