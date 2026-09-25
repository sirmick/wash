// The new-session form: stack, tier and folder, with the adapter and model
// under Advanced for one-off overrides.
//
// A stack (agentd's stacks.go) is a named set of four tiers, each naming an
// adapter, connection, model and effort. Picking one is the question a person
// actually has ("the cheap one, for coding") rather than which binary to run
// and which of its forty model IDs to type. The form renders what agentd
// publishes and owns no launch logic: Start sends {stack, tier, agent?,
// model?, cwd} and agentd resolves it.

import { Show, createMemo, type Component, type JSX } from 'solid-js';
import { Button, Input, Select, tokens, type agentproto } from '@wash/ui';

export interface LaunchForm {
  stack: string;
  tier: string;
  /** Advanced: another adapter, on its own defaults */
  agent: string;
  /** Advanced: another model for the chosen adapter */
  model: string;
  cwd: string;
}

export const DEFAULT_TIER = 'frontier';

/** What Start sends agentd: empty overrides are left out, so agentd sees
 *  only what the person actually chose. The launcher lives in the manager,
 *  which hands each session to a window of its own (open). */
export function startMessage(f: LaunchForm): agentproto.AgentStart {
  const msg: agentproto.AgentStart = { kind: 'agent_start', cwd: f.cwd, open: true };
  if (f.stack) {
    msg.stack = f.stack;
    msg.tier = f.tier || DEFAULT_TIER;
  }
  if (f.agent) msg.agent = f.agent;
  if (f.model) msg.model = f.model;
  return msg;
}

/** Whether the form can start anything, and if not, why. */
export function launchBlocker(f: LaunchForm, stacks: agentproto.StackView[], adapters: agentproto.Adapter[]): string {
  const stack = stacks.find((s) => s.id === f.stack);
  const tier = stack?.tiers?.find((t) => t.tier === (f.tier || DEFAULT_TIER));
  // Another adapter replaces the tier outright, so only it has to start.
  if (f.agent && f.agent !== tier?.adapter) {
    const a = adapters.find((x) => x.id === f.agent);
    return a?.available ? '' : `${a?.name ?? f.agent} cannot start here${a?.note ? `: ${a.note}` : ''}`;
  }
  if (!stack) return 'Choose a stack, or an agent under Advanced.';
  if (!tier) return stack.note ?? `${stack.name} cannot start here`;
  return tier.available ? '' : tier.note ?? `${stack.name} cannot start here`;
}

function tierLabel(t: string): string {
  return t.charAt(0).toUpperCase() + t.slice(1);
}

export const Launcher: Component<{
  stacks: agentproto.StackView[];
  adapters: agentproto.Adapter[];
  form: LaunchForm;
  onForm: (patch: Partial<LaunchForm>) => void;
  onStart: () => void;
  onPickFolder: () => void;
  starting: boolean;
  error: string;
  hasDefaultPrompt: boolean;
  onOpenPrompt: () => void;
  /** rendered after Advanced: the Connections section */
  children?: JSX.Element;
}> = (props) => {
  const stack = createMemo(() => props.stacks.find((s) => s.id === props.form.stack));
  const tier = createMemo(() => stack()?.tiers?.find((t) => t.tier === (props.form.tier || DEFAULT_TIER)));
  const adapterName = (id: string) => props.adapters.find((a) => a.id === id)?.name ?? id;
  const blocker = createMemo(() => launchBlocker(props.form, props.stacks, props.adapters));
  const overridden = () => !!props.form.agent && props.form.agent !== tier()?.adapter;

  const summary = () => {
    const t = tier();
    if (!t) return '';
    if (overridden()) return `${adapterName(props.form.agent)} · ${props.form.model || 'its default model'}`;
    const parts = [adapterName(t.adapter), props.form.model || t.model || 'default model'];
    if (t.thinking) parts.push(`effort ${t.thinking}`);
    if (t.connection) parts.push(`via ${t.connection.split('@')[1] ?? t.connection}`);
    return parts.join(' · ');
  };

  const start = () => {
    if (!props.starting && !blocker()) props.onStart();
  };

  return (
    <div style={{ padding: `${tokens.spaceMd}px`, display: 'flex', 'flex-direction': 'column', gap: `${tokens.spaceMd}px` }}>
      <div style={{ font: tokens.type.titleSm, color: tokens.fg }}>New session</div>

      <label style={fieldStyle}>
        <span style={labelStyle}>stack</span>
        <Select
          value={props.form.stack}
          onChange={(v) => props.onForm({ stack: v })}
          data-testid="ai-stack-select"
          options={[
            ['', 'Choose…'],
            ...props.stacks.map((s) => [s.id, s.available ? s.name : `${s.name} — ${s.note ?? 'unavailable'}`, !s.available] as [string, string, boolean]),
          ]}
        />
      </label>

      <label style={fieldStyle}>
        <span style={labelStyle}>tier</span>
        <Select
          value={props.form.tier || DEFAULT_TIER}
          onChange={(v) => props.onForm({ tier: v })}
          data-testid="ai-tier-select"
          options={(stack()?.tiers ?? []).map((t) => [t.tier, t.available ? tierLabel(t.tier) : `${tierLabel(t.tier)} — ${t.note ?? 'unavailable'}`, !t.available] as [string, string, boolean])}
        />
        <Show when={summary()}>
          <span data-testid="ai-tier-summary" style={{ font: tokens.type.monoSm, color: tokens.fgMuted }}>{summary()}</span>
        </Show>
        {/* Read-only is a promise only Claude Code can keep (its reviewer
            capability restricts the tools). Elsewhere a reviewer is asked
            not to write, and the form says so rather than implying more. */}
        <Show when={!overridden() && tier()?.read_only}>
          <span data-testid="ai-tier-readonly" style={{ font: tokens.type.textSm, color: tier()?.read_only === 'enforced' ? tokens.fgMuted : tokens.fgWarning }}>
            {tier()?.read_only === 'enforced'
              ? 'Read-only, enforced: the reviewer cannot edit files or run commands.'
              : 'Read-only by instruction only: this adapter can still edit files and run commands.'}
          </span>
        </Show>
      </label>

      <div style={fieldStyle}>
        <span style={labelStyle}>folder</span>
        <div style={{ display: 'flex', gap: `${tokens.spaceSm}px`, 'align-items': 'stretch' }}>
          <Input
            data-testid="ai-folder-input"
            spellcheck={false}
            placeholder="Home"
            value={props.form.cwd}
            title={props.form.cwd || 'Home'}
            onInput={(e: InputEvent) => props.onForm({ cwd: (e.currentTarget as HTMLInputElement).value })}
            onKeyDown={(e: KeyboardEvent) => { if (e.key === 'Enter') { e.preventDefault(); start(); } }}
            style={{ font: tokens.type.monoMd, flex: 1, 'min-width': 0 }}
          />
          <Button onClick={() => props.onPickFolder()}>Choose…</Button>
        </div>
      </div>

      <details data-testid="ai-advanced" open={!!(props.form.agent || props.form.model)}>
        <summary data-wash-hit style={labelStyle}>advanced</summary>
        <div style={{ display: 'flex', 'flex-direction': 'column', gap: `${tokens.spaceMd}px`, 'margin-top': `${tokens.spaceSm}px` }}>
          <label style={fieldStyle}>
            <span style={labelStyle}>agent</span>
            <Select
              value={props.form.agent}
              onChange={(v) => props.onForm({ agent: v })}
              data-testid="ai-agent-select"
              options={[
                ['', tier() ? `The tier's (${adapterName(tier()!.adapter)})` : 'The tier\'s'],
                ...props.adapters.map((a) => [a.id, a.available ? a.name : `${a.name} — ${a.note ?? 'not installed'}`, !a.available] as [string, string, boolean]),
              ]}
            />
          </label>
          <label style={fieldStyle}>
            <span style={labelStyle}>model</span>
            <Input
              data-testid="ai-model-input"
              spellcheck={false}
              placeholder={overridden() ? 'its default' : tier()?.model ?? 'default'}
              value={props.form.model}
              onInput={(e: InputEvent) => props.onForm({ model: (e.currentTarget as HTMLInputElement).value.trim() })}
              style={{ font: tokens.type.monoMd }}
            />
          </label>
        </div>
      </details>

      {props.children}

      <Show when={props.error}>
        <div data-testid="ai-start-error" style={errorStyle}>{props.error}</div>
      </Show>

      {/* Stated on the launcher, not hidden in a menu: a default prompt that
          silently prefixes every new session is the kind of magic that
          gets blamed on the agent. One line, and one click to read or
          change it. */}
      <div style={{ display: 'flex', 'align-items': 'center', gap: `${tokens.spaceSm}px`, font: tokens.type.textSm }}>
        <span data-testid="ai-prompt-status" style={{ color: tokens.fgMuted }}>
          {props.hasDefaultPrompt ? 'A default prompt will be sent first.' : 'No default prompt.'}
        </span>
        <Button variant="ghost" data-testid="ai-prompt-open" onClick={() => props.onOpenPrompt()}>
          {props.hasDefaultPrompt ? 'Edit…' : 'Set…'}
        </Button>
      </div>

      <Show when={blocker() && props.form.stack}>
        <div data-testid="ai-start-blocker" style={{ font: tokens.type.textSm, color: tokens.fgMuted }}>{blocker()}</div>
      </Show>
      <Button variant="primary" data-testid="ai-start" disabled={props.starting || !!blocker()} onClick={start}>
        {props.starting ? 'Starting…' : 'Start session'}
      </Button>
    </div>
  );
};

const fieldStyle: JSX.CSSProperties = { display: 'flex', 'flex-direction': 'column', gap: `${tokens.spaceXs}px` };

export const labelStyle: JSX.CSSProperties = {
  font: tokens.type.monoSm,
  'letter-spacing': '0.09em',
  'text-transform': 'uppercase',
  color: tokens.fgDim,
};

export const errorStyle: JSX.CSSProperties = {
  font: tokens.type.textSm,
  color: tokens.fgDanger,
  background: tokens.bgDanger,
  border: `1px solid ${tokens.borderDanger}`,
  'border-radius': tokens.radiusMd,
  padding: `${tokens.spaceMd}px`,
};
