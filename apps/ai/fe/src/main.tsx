// wash-app-ai — one window onto one managed agent session.
//
// The window is a thin host around <AgentSession> from @wash/ui: agentd
// owns the session, the transcript and the approval queue, so everything
// here is a subscription and a form. An unstarted window renders the
// launcher, which is why there is no separate "new session" dialog.

import { For, Show, createMemo, createSignal } from 'solid-js';
import type { Component } from 'solid-js';
import {
  AgentSession, Button, FilePicker, Menu, MenuItem, MenuSeparator, Overlay,
  createAppBus, defineWashApp, tokens, washCopyText,
} from '@wash/ui';
import type { AgentAsk, AgentConfig, AgentEvent, AgentStatus } from '@wash/ui';

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
  used?: number;
  size?: number;
  title?: string;
  mode?: string;
  modes?: { id: string; name: string; description?: string }[];
  configs?: AgentConfig[];
  commands?: { name: string; description?: string }[];
}

interface RecentSession {
  session_id: string;
  agent: string;
  dir?: string;
  /** the agent's own one-line name for what the session was about */
  title?: string;
  last_seen: number;
  live?: boolean;
}

interface RosterState {
  rows?: RosterRow[];
  asks?: (AgentAsk & { row_key: string })[];
  adapters?: Adapter[];
  recent?: RecentSession[];
}

type MenuID = '' | 'file' | 'edit' | 'session' | 'history';

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
  // Closing the window does not end the session — agentd owns the adapter
  // — so the user chooses what happens to it.
  const [confirmClose, setConfirmClose] = createSignal(false);
  const [openMenu, setOpenMenu] = createSignal<MenuID>('');
  const [menuAt, setMenuAt] = createSignal({ x: 0, y: 0 });
  const [saving, setSaving] = createSignal(false);

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
      case 'confirm_close':
        setConfirmClose(true);
        break;
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
      used: r?.used,
      size: r?.size,
      title: r?.title,
      mode: r?.mode,
      modes: r?.modes,
      configs: r?.configs,
      commands: r?.commands,
    };
  });

  // The transcript as plain text: what Copy and Save both produce. Tool
  // rows keep their kind so a saved log reads like the session did, and
  // images are named rather than dumped as base64 — a transcript you can
  // read beats one you can round-trip.
  const transcriptText = () =>
    events()
      .map((e) => {
        if (e.kind === 'user') return '> ' + (e.text ?? '');
        if (e.kind === 'tool') return `[${e.tool_kind ?? 'tool'}] ${e.title ?? e.text ?? ''}`;
        if (e.kind === 'image') return `[image ${e.mime ?? 'image'}]`;
        return e.text ?? '';
      })
      .join('\n\n');

  const lastReply = () => {
    const msgs = events().filter((e) => e.kind === 'message');
    return msgs.length ? (msgs[msgs.length - 1].text ?? '') : '';
  };

  const openMenuFor = (id: MenuID, ev: MouseEvent) => {
    const r = (ev.currentTarget as HTMLElement).getBoundingClientRect();
    setMenuAt({ x: r.left, y: r.bottom });
    setOpenMenu((cur) => (cur === id ? '' : id));
  };
  const closeMenu = () => setOpenMenu('');
  const run = (fn: () => void) => () => {
    closeMenu();
    fn();
  };

  const recent = () => (roster().recent ?? []).filter((s) => !s.live);
  const configs = () => row()?.configs ?? [];

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
        open={saving()}
        mode="save"
        host={props.host}
        hostInstanceID={props.instance}
        defaultName={(row()?.title || 'transcript').replace(/[^\w.-]+/g, '-').slice(0, 60) + '.md'}
        onConfirm={(p) => {
          setSaving(false);
          send({ kind: 'save_transcript', path: p, text: transcriptText() });
        }}
        onCancel={() => setSaving(false)}
        data-testid="ai-save-picker"
      />

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

  // Three outcomes, not two: dismissing the dialog must ABORT the close,
  // not pick one of the destructive options for you. That is why this is
  // an Overlay rather than a ConfirmDialog — the latter maps dismiss onto
  // its cancel action, which here would mean "terminate".
  const menubar = (
    <div
      style={{
        display: 'flex',
        'align-items': 'center',
        gap: '2px',
        flex: 'none',
        padding: `2px ${tokens.spaceXs}px`,
        background: tokens.bgMenu,
        'border-bottom': `1px solid ${tokens.borderMenu}`,
      }}
    >
      <MenuBarButton id="file" label="File" active={openMenu() === 'file'} onClick={openMenuFor} />
      <MenuBarButton id="edit" label="Edit" active={openMenu() === 'edit'} onClick={openMenuFor} />
      <MenuBarButton id="session" label="Session" active={openMenu() === 'session'} onClick={openMenuFor} />
      <MenuBarButton id="history" label="History" active={openMenu() === 'history'} onClick={openMenuFor} />

      <Show when={openMenu() === 'file'}>
        <Menu x={menuAt().x} y={menuAt().y} onDismiss={closeMenu} data-testid="ai-menu-file">
          <MenuItem label="Save transcript…" disabled={events().length === 0}
            onClick={run(() => setSaving(true))} data-testid="ai-menu-save" />
          <MenuSeparator />
          <MenuItem label="Detach" disabled={!sessionKey()}
            onClick={run(() => send({ kind: 'detach' }))} data-testid="ai-menu-detach" />
          <MenuItem label="Terminate" disabled={!sessionKey()}
            onClick={run(() => send({ kind: 'terminate' }))} data-testid="ai-menu-terminate" />
        </Menu>
      </Show>

      <Show when={openMenu() === 'edit'}>
        <Menu x={menuAt().x} y={menuAt().y} onDismiss={closeMenu} data-testid="ai-menu-edit">
          <MenuItem label="Copy last reply" disabled={!lastReply()}
            onClick={run(() => void washCopyText(lastReply()))} data-testid="ai-menu-copy-last" />
          <MenuItem label="Copy transcript" disabled={events().length === 0}
            onClick={run(() => void washCopyText(transcriptText()))} data-testid="ai-menu-copy-all" />
        </Menu>
      </Show>

      <Show when={openMenu() === 'session'}>
        <Menu x={menuAt().x} y={menuAt().y} onDismiss={closeMenu} data-testid="ai-menu-session">
          <Show when={configs().length === 0}>
            <MenuItem label="No settings offered" disabled onClick={() => {}} />
          </Show>
          {/* One group per setting the agent exposes — model, reasoning
              effort, plan mode. The same generic block the status bar
              renders, given room to show its descriptions. */}
          <For each={configs()}>
            {(cfg, ci) => (
              <>
                <Show when={ci() > 0}><MenuSeparator /></Show>
                <MenuItem label={cfg.name} disabled onClick={() => {}} />
                <For each={cfg.values ?? []}>
                  {(v) => (
                    <MenuItem
                      label={'   ' + v.name}
                      trailing={v.value === cfg.current ? <span>✓</span> : undefined}
                      onClick={run(() => send({ kind: 'set_config', id: cfg.id, value: v.value }))}
                      data-testid={`ai-menu-config-${cfg.id}-${v.value}`}
                    />
                  )}
                </For>
              </>
            )}
          </For>
        </Menu>
      </Show>

      <Show when={openMenu() === 'history'}>
        <Menu x={menuAt().x} y={menuAt().y} onDismiss={closeMenu} data-testid="ai-menu-history">
          <Show when={recent().length === 0}>
            <MenuItem label="No earlier sessions" disabled onClick={() => {}} />
          </Show>
          <For each={recent()}>
            {(s) => (
              <MenuItem
                label={s.title ? `${s.title}  —  ${s.dir ?? ''}` : `${s.agent} · ${s.dir ?? ''}`}
                onClick={run(() => send({ kind: 'resume', session_id: s.session_id }))}
                data-testid="ai-menu-resume"
              />
            )}
          </For>
        </Menu>
      </Show>
    </div>
  );

  const closeDialog = (
    <Show when={confirmClose()}>
      <Overlay onDismiss={() => setConfirmClose(false)} data-testid="ai-close-confirm">
        <div style={{ 'font-weight': 600, 'margin-bottom': `${tokens.spaceSm}px` }}>
          Leave this session running?
        </div>
        <div style={{ font: tokens.type.textMd, opacity: 0.75, 'max-width': '46ch', 'margin-bottom': `${tokens.spaceLg}px` }}>
          The agent keeps working after this window closes. Detach to come back to it
          from the Agents sidebar, or terminate it and keep it in your history.
        </div>
        <div style={{ display: 'flex', gap: `${tokens.spaceMd}px`, 'justify-content': 'flex-end' }}>
          <Button data-testid="ai-close-cancel" onClick={() => setConfirmClose(false)}>
            Keep open
          </Button>
          <Button
            data-testid="ai-close-terminate"
            variant="danger"
            onClick={() => {
              setConfirmClose(false);
              send({ kind: 'terminate' });
            }}
          >
            Terminate
          </Button>
          <Button
            data-testid="ai-close-detach"
            variant="primary"
            onClick={() => {
              setConfirmClose(false);
              send({ kind: 'detach' });
            }}
          >
            Detach
          </Button>
        </div>
      </Overlay>
    </Show>
  );

  return (
    <>
    {closeDialog}
    <Show when={sessionKey()} fallback={<Show when={autostart()} fallback={launcher}>{booting}</Show>}>
      <AgentSession
        header={menubar}
        events={events}
        asks={asks}
        status={status}
        onSend={(text) => send({ kind: 'prompt', text })}
        onAnswer={(id, decision, rule) => send({ kind: 'answer', id, decision, rule: rule ?? '' })}
        onCancel={() => send({ kind: 'cancel' })}
        onSetMode={(mode) => send({ kind: 'set_mode', mode })}
        onSetConfig={(id, value) => send({ kind: 'set_config', id, value })}
      />
    </Show>
    </>
  );
};

const MenuBarButton: Component<{
  id: MenuID;
  label: string;
  active: boolean;
  onClick: (id: MenuID, ev: MouseEvent) => void;
}> = (props) => (
  <button
    type="button"
    data-testid={`ai-menubar-${props.id}`}
    onClick={(ev) => props.onClick(props.id, ev)}
    style={{
      font: tokens.type.textSm,
      padding: `2px ${tokens.spaceMd}px`,
      'border-radius': tokens.radiusSm,
      border: '1px solid transparent',
      background: props.active ? tokens.bgRowSelected : 'transparent',
      color: tokens.fg,
      cursor: 'pointer',
    }}
  >
    {props.label}
  </button>
);

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
