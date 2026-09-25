// Component test (Tier B) for the new-session form: stack, tier, folder, and
// the Advanced overrides. The form owns no launch logic, so what is tested is
// what it shows and what Start would send; agentd's resolution is Go's.

import { test, expect, afterEach } from 'vitest';
import { createSignal } from 'solid-js';
import { render, fireEvent, cleanup } from '@solidjs/testing-library';
import type { agentproto } from '@wash/ui';
import { Launcher, launchBlocker, startMessage, type LaunchForm } from './Launcher.tsx';

afterEach(cleanup);

const adapters: agentproto.Adapter[] = [
  { id: 'codex', name: 'Codex', available: false, note: 'needs codex-acp on PATH' },
  { id: 'claude', name: 'Claude Code', available: true },
  { id: 'opencode', name: 'OpenCode', available: true },
];

const tiers = (adapter: string, over: Partial<agentproto.StackView['tiers']> = [], extra: Record<string, unknown> = {}) =>
  ['frontier', 'coding', 'review', 'small'].map((tier, i) => ({
    tier,
    adapter,
    model: `${adapter}-${tier}`,
    available: true,
    ...(tier === 'review' ? { read_only: adapter === 'claude' ? 'enforced' : 'instruction' } : {}),
    ...extra,
    ...(over?.[i] ?? {}),
  })) as agentproto.StackView['tiers'];

const stacks: agentproto.StackView[] = [
  { id: 'anthropic', name: 'All Anthropic', available: true, tiers: tiers('claude') },
  { id: 'openai', name: 'All OpenAI', available: false, note: 'Codex: needs codex-acp on PATH', tiers: tiers('codex', [], { available: false, note: 'Codex: needs codex-acp on PATH' }) },
  {
    id: 'openrouter', name: 'OpenRouter budget', available: true,
    tiers: tiers('opencode', [], { connection: 'opencode@openrouter', thinking: 'high' }),
  },
];

const mount = (initial: Partial<LaunchForm> = {}, over: { stacks?: agentproto.StackView[] } = {}) => {
  const [form, setForm] = createSignal<LaunchForm>({ stack: 'anthropic', tier: 'frontier', agent: '', model: '', cwd: '', ...initial });
  const started: agentproto.AgentStart[] = [];
  const r = render(() => (
    <Launcher
      stacks={over.stacks ?? stacks}
      adapters={adapters}
      form={form()}
      onForm={(p) => setForm((f) => ({ ...f, ...p }))}
      onStart={() => started.push(startMessage(form()))}
      onPickFolder={() => {}}
      starting={false}
      error=""
      hasDefaultPrompt={false}
      onOpenPrompt={() => {}}
    />
  ));
  return { ...r, form, started };
};

const options = (el: HTMLElement) =>
  [...(el as HTMLSelectElement).options].map((o) => ({ value: o.value, label: o.textContent, disabled: o.disabled }));

test('it offers the stacks, greying the ones that cannot start with the reason', () => {
  const { getByTestId } = mount();
  const opts = options(getByTestId('ai-stack-select'));
  expect(opts.map((o) => o.value)).toEqual(['', 'anthropic', 'openai', 'openrouter']);
  const openai = opts.find((o) => o.value === 'openai')!;
  expect(openai.disabled).toBe(true);
  expect(openai.label).toContain('needs codex-acp on PATH');
  expect(opts.find((o) => o.value === 'anthropic')!.disabled).toBe(false);
});

test('it offers the four tiers, frontier first and chosen', () => {
  const { getByTestId } = mount();
  const sel = getByTestId('ai-tier-select') as HTMLSelectElement;
  expect(options(sel).map((o) => o.value)).toEqual(['frontier', 'coding', 'review', 'small']);
  expect(sel.value).toBe('frontier');
});

test('the tier line says what will run, including the connection', () => {
  const { getByTestId } = mount({ stack: 'openrouter', tier: 'coding' });
  expect(getByTestId('ai-tier-summary').textContent).toBe('OpenCode · opencode-coding · effort high · via openrouter');
});

// Read-only reviewers are enforced only on Claude Code; anywhere else the
// form must not imply more than an instruction.
test('a review tier says whether read-only is enforced', () => {
  const claude = mount({ stack: 'anthropic', tier: 'review' });
  expect(claude.getByTestId('ai-tier-readonly').textContent).toContain('enforced');
  cleanup();
  const other = mount({ stack: 'openrouter', tier: 'review' });
  expect(other.getByTestId('ai-tier-readonly').textContent).toContain('by instruction only');
  cleanup();
  const coding = mount({ stack: 'openrouter', tier: 'coding' });
  expect(coding.queryByTestId('ai-tier-readonly')).toBeNull();
});

test('Start sends the stack and tier, and only the overrides chosen', () => {
  const { getByTestId, started } = mount({ stack: 'openrouter', cwd: '/w' });
  fireEvent.input(getByTestId('ai-tier-select'), { target: { value: 'small' } });
  fireEvent.click(getByTestId('ai-start'));
  expect(started).toEqual([{ kind: 'agent_start', cwd: '/w', open: true, stack: 'openrouter', tier: 'small' }]);
});

test('Advanced overrides go with the start', () => {
  const { getByTestId, started } = mount({ stack: 'anthropic' });
  fireEvent.input(getByTestId('ai-model-input'), { target: { value: 'opus[1m]' } });
  fireEvent.click(getByTestId('ai-start'));
  expect(started[0]).toEqual({ kind: 'agent_start', cwd: '', open: true, stack: 'anthropic', tier: 'frontier', model: 'opus[1m]' });
});

test('an unavailable stack cannot be started, and says why', () => {
  const { getByTestId, started } = mount({ stack: 'openai' });
  expect((getByTestId('ai-start') as HTMLButtonElement).disabled).toBe(true);
  expect(getByTestId('ai-start-blocker').textContent).toContain('needs codex-acp');
  fireEvent.click(getByTestId('ai-start'));
  expect(started).toEqual([]);
});

// Another adapter replaces the tier, so the stack being unavailable no longer
// matters; only that adapter has to start.
test('another adapter under Advanced starts even from an unavailable stack', () => {
  const { getByTestId, started } = mount({ stack: 'openai' });
  fireEvent.input(getByTestId('ai-agent-select'), { target: { value: 'claude' } });
  expect(getByTestId('ai-tier-summary').textContent).toBe('Claude Code · its default model');
  fireEvent.click(getByTestId('ai-start'));
  expect(started[0]).toMatchObject({ stack: 'openai', agent: 'claude' });
});

test('an uninstalled adapter is greyed under Advanced', () => {
  const { getByTestId } = mount();
  const codex = options(getByTestId('ai-agent-select')).find((o) => o.value === 'codex')!;
  expect(codex.disabled).toBe(true);
  expect(codex.label).toContain('needs codex-acp on PATH');
});

test('launchBlocker with nothing chosen asks for a stack or an agent', () => {
  expect(launchBlocker({ stack: '', tier: 'frontier', agent: '', model: '', cwd: '' }, stacks, adapters)).toContain('Choose a stack');
  expect(launchBlocker({ stack: '', tier: 'frontier', agent: 'claude', model: '', cwd: '' }, stacks, adapters)).toBe('');
});
