// wash-app-ai — one window onto one managed agent session.
//
// The window is a thin host around <AgentSession> from @wash/ui: agentd
// owns the session, the transcript and the approval queue, so everything
// here is a subscription and a form. An unstarted window renders the
// launcher, which is why there is no separate "new session" dialog.

import { For, Show, createMemo, createSignal } from 'solid-js';
import type { Component } from 'solid-js';
import { AgentSession, Button, FilePicker, createAppBus, defineWashApp, tokens } from '@wash/ui';
import type { AgentAsk, AgentEvent, AgentStatus } from '@wash/ui';

interface Adapter {
  id: string;
  name: string;
  available: boolean;
  note?: string;
}

interface RosterRow {
  key: string;
  agent: string;
  state: string;
  dir?: string;
  branch?: string;
  dirty?: boolean;
}

interface RosterState {
  rows?: RosterRow[];
  asks?: (AgentAsk & { row_key: string })[];
  adapters?: Adapter[];
}

const App: Component<{ instance: string; host: HTMLElement }> = (props) => {
  const [events, setEvents] = createSignal<AgentEvent[]>([]);
  const [sessionKey, setSessionKey] = createSignal('');
  const [roster, setRoster] = createSignal<RosterState>({});
  const [error, setError] = createSignal('');

  // Launcher form.
  const [agent, setAgent] = createSignal('');
  const [cwd, setCwd] = createSignal('');
  const [starting, setStarting] = createSignal(false);
  const [picking, setPicking] = createSignal(false);
  // Launched with --agent/--cwd: show what is starting rather than an
  // empty form that is about to be replaced.
  const [autostart, setAutostart] = createSignal<{ agent: string; cwd: string } | null>(null);

  const handleBE = (m: Record<string, unknown>) => {
    switch (m.kind) {
      case 'autostart':
        setAutostart({ agent: String(m.agent ?? ''), cwd: String(m.cwd ?? '') });
        setAgent(String(m.agent ?? ''));
        setCwd(String(m.cwd ?? ''));
        setStarting(true);
        break;
      case 'started':
        setSessionKey(String(m.key ?? ''));
        setStarting(false);
        setError('');
        break;
      case 'start_failed':
        setAutostart(null);
        setStarting(false);
        setError(String(m.error ?? 'could not start'));
        break;
      case 'snapshot':
        setEvents((m.events as AgentEvent[]) ?? []);
        break;
      case 'event': {
        const e = m.event as AgentEvent | undefined;
        if (!e) break;
        // agentd mutates a tool row in place, so an event with a seq we
        // already hold replaces it rather than appending a duplicate.
        setEvents((prev) => {
          const at = prev.findIndex((p) => p.seq === e.seq);
          if (at < 0) return [...prev, e];
          const next = prev.slice();
          next[at] = e;
          return next;
        });
        break;
      }
      case 'roster':
        setRoster((m.state as RosterState) ?? {});
        break;
    }
  };

  const { send } = createAppBus(props, { onMsg: handleBE });

  const adapters = () => roster().adapters ?? [];
  const row = createMemo(() => (roster().rows ?? []).find((r) => r.key === sessionKey()));
  const asks = createMemo<AgentAsk[]>(() =>
    (roster().asks ?? []).filter((a) => a.row_key === sessionKey()),
  );
  const status = createMemo<AgentStatus>(() => {
    const r = row();
    return {
      agent: r?.agent ?? agent(),
      dir: r?.dir,
      branch: r?.branch,
      dirty: r?.dirty,
      state: r?.state,
    };
  });

  const start = () => {
    const a = agent() || adapters().find((x) => x.available)?.id;
    if (!a) return;
    setStarting(true);
    setError('');
    send({ kind: 'start', agent: a, cwd: cwd() });
  };

  const booting = (
    <div
      style={{
        padding: `${tokens.spaceXl}px`,
        display: 'flex',
        'flex-direction': 'column',
        gap: `${tokens.spaceMd}px`,
        color: tokens.fgMuted,
        font: tokens.type.textMd,
      }}
    >
      <div style={{ color: tokens.fg, font: tokens.type.titleSm }}>
        Starting {autostart()?.agent}…
      </div>
      <div style={{ font: tokens.type.monoMd }}>{autostart()?.cwd || 'Home'}</div>
    </div>
  );

  const launcher = (
    <div style={{ padding: `${tokens.spaceXl}px`, display: 'flex', 'flex-direction': 'column', gap: `${tokens.spaceLg}px` }}>
      <div style={{ font: tokens.type.titleSm, color: tokens.fg }}>New session</div>

      <label style={{ display: 'flex', 'flex-direction': 'column', gap: `${tokens.spaceXs}px` }}>
        <span style={labelStyle}>agent</span>
        <select
          value={agent()}
          onChange={(e) => setAgent(e.currentTarget.value)}
          style={fieldStyle}
        >
          <option value="">Choose…</option>
          <For each={adapters()}>
            {(a) => (
              <option value={a.id} disabled={!a.available}>
                {a.name}
                {a.available ? '' : ` — ${a.note ?? 'not installed'}`}
              </option>
            )}
          </For>
        </select>
      </label>

      <div style={{ display: 'flex', 'flex-direction': 'column', gap: `${tokens.spaceXs}px` }}>
        <span style={labelStyle}>folder</span>
        <div style={{ display: 'flex', gap: `${tokens.spaceSm}px`, 'align-items': 'stretch' }}>
          <div
            style={{
              ...fieldStyle,
              font: tokens.type.monoMd,
              flex: 1,
              'min-width': 0,
              overflow: 'hidden',
              'text-overflow': 'ellipsis',
              'white-space': 'nowrap',
              color: cwd() ? tokens.fg : tokens.fgDim,
            }}
            title={cwd() || 'Home'}
          >
            {cwd() || 'Home'}
          </div>
          <Button onClick={() => setPicking(true)}>Choose…</Button>
        </div>
      </div>

      <FilePicker
        open={picking()}
        mode="directory"
        host={props.host}
        hostInstanceID={props.instance}
        start={cwd()}
        onConfirm={(p) => {
          setCwd(p);
          setPicking(false);
        }}
        onCancel={() => setPicking(false)}
        data-testid="ai-folder-picker"
      />

      <Show when={error()}>
        <div
          style={{
            font: tokens.type.textSm,
            color: tokens.fgDanger,
            background: tokens.bgDanger,
            border: `1px solid ${tokens.borderDanger}`,
            'border-radius': tokens.radiusMd,
            padding: `${tokens.spaceMd}px`,
          }}
        >
          {error()}
        </div>
      </Show>

      <Button variant="primary" disabled={starting()} onClick={start}>
        {starting() ? 'Starting…' : 'Start session'}
      </Button>
    </div>
  );

  return (
    <Show when={sessionKey()} fallback={<Show when={autostart()} fallback={launcher}>{booting}</Show>}>
      <AgentSession
        events={events}
        asks={asks}
        status={status}
        onSend={(text) => send({ kind: 'prompt', text })}
        onAnswer={(id, decision, rule) => send({ kind: 'answer', id, decision, rule: rule ?? '' })}
      />
    </Show>
  );
};

const labelStyle = {
  font: tokens.type.monoSm,
  'letter-spacing': '0.09em',
  'text-transform': 'uppercase' as const,
  color: tokens.fgDim,
};

const fieldStyle = {
  background: tokens.bgInset,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': tokens.radiusMd,
  padding: `${tokens.spaceSm}px ${tokens.spaceMd}px`,
  font: tokens.type.textMd,
  color: tokens.fg,
  outline: 'none',
};

defineWashApp('wash-app-ai', App);
