// wash-app-ai — one window onto one managed agent session.
//
// The window is a thin host around <AgentSession> from @wash/ui: agentd
// owns the session, the transcript and the approval queue, so everything
// here is a subscription and a form. An unstarted window renders the
// launcher, which is why there is no separate "new session" dialog.

import { For, Show, createMemo, createSignal, onCleanup, onMount } from 'solid-js';
import type { Component } from 'solid-js';
import {
  AgentSession, Button, FilePicker, Menu, MenuBar, MenuItem, MenuSeparator, Overlay, Select,
  createAppBus, defineWashApp, kbdStyle, tokens, washCopyText,
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
  yolo?: boolean;
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

function mergeEvents(prev: AgentEvent[], next: AgentEvent[]): AgentEvent[] {
  const bySeq = new Map<number, AgentEvent>();
  for (const e of prev) bySeq.set(e.seq, e);
  for (const e of next) bySeq.set(e.seq, e);
  return Array.from(bySeq.values()).sort((a, b) => a.seq - b.seq);
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
  // Closing the window does not end the session — agentd owns the adapter
  // — so the user chooses what happens to it.
  const [confirmClose, setConfirmClose] = createSignal(false);
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
        setEvents([]);
        setStarting(false);
        setError('');
        break;
      case 'start_failed':
        setAutostart(null);
        setStarting(false);
        setError(String(m.error ?? 'could not start'));
        break;
      case 'snapshot':
        if (typeof m.reset === 'boolean') {
          setEvents((prev) => mergeEvents(prev, (m.events as AgentEvent[]) ?? []));
        } else {
          setEvents(mergeEvents([], (m.events as AgentEvent[]) ?? []));
        }
        break;
      case 'event': {
        const e = m.event as AgentEvent | undefined;
        if (!e) break;
        // agentd mutates a tool row in place, so an event with a seq we
        // already hold replaces it rather than appending a duplicate.
        setEvents((prev) => mergeEvents(prev, [e]));
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
      yolo: r?.yolo,
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

  const recent = () => (roster().recent ?? []).filter((s) => !s.live);
  const configs = () => row()?.configs ?? [];

  // The menus advertise these, so they have to exist. A menu that shows a
  // shortcut it does not implement is worse than one that shows none.
  onMount(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!e.ctrlKey && !e.metaKey) return;
      const k = e.key.toLowerCase();
      if (k === 's' && !e.shiftKey && events().length > 0) {
        e.preventDefault();
        setSaving(true);
      }
      if (k === 'c' && e.shiftKey && lastReply()) {
        e.preventDefault();
        void washCopyText(lastReply());
      }
    };
    window.addEventListener('keydown', onKey);
    onCleanup(() => window.removeEventListener('keydown', onKey));
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
        <Select
          value={agent()}
          onChange={setAgent}
          data-testid="ai-agent-select"
          options={[
            ['', 'Choose…'],
            ...adapters().map((a) => [a.id, a.available ? a.name : `${a.name} — ${a.note ?? 'not installed'}`] as [string, string]),
          ]}
        />
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
    <MenuBar
      testidPrefix="ai-menubar"
      menus={[
        {
          id: 'file',
          label: 'File',
          render: (at, close) => (
            <Menu x={at.x} y={at.y} onDismiss={close} data-testid="ai-menu-file">
              <MenuItem label="Save transcript…" trailing={<kbd style={kbdStyle}>Ctrl+S</kbd>}
                disabled={events().length === 0}
                onClick={() => { close(); setSaving(true); }} data-testid="ai-menu-save" />
              <MenuSeparator />
              <MenuItem label="Detach" disabled={!sessionKey()}
                onClick={() => { close(); send({ kind: 'detach' }); }} data-testid="ai-menu-detach" />
              <MenuItem label="Terminate" disabled={!sessionKey()}
                onClick={() => { close(); send({ kind: 'terminate' }); }} data-testid="ai-menu-terminate" />

            </Menu>
          ),
        },
        {
          id: 'edit',
          label: 'Edit',
          render: (at, close) => (
            <Menu x={at.x} y={at.y} onDismiss={close} data-testid="ai-menu-edit">
              <MenuItem label="Copy last reply" trailing={<kbd style={kbdStyle}>Ctrl+Shift+C</kbd>}
                disabled={!lastReply()}
                onClick={() => { close(); void washCopyText(lastReply()); }} data-testid="ai-menu-copy-last" />
              <MenuItem label="Copy transcript" disabled={events().length === 0}
                onClick={() => { close(); void washCopyText(transcriptText()); }} data-testid="ai-menu-copy-all" />
            </Menu>
          ),
        },
        {
          id: 'session',
          label: 'Session',
          render: (at, close) => (
            <Menu x={at.x} y={at.y} onDismiss={close} data-testid="ai-menu-session">
              {/* Host-side auto-approval, next to the agent's own settings
                  because that is what it governs — but named for what it
                  does rather than dressed up. Turning it on stops wash
                  asking; the transcript records the switch and every
                  approval it then makes. */}
              <MenuItem
                label={status().yolo ? 'Stop auto-approving (yolo)' : 'Auto-approve everything (yolo)'}
                disabled={!sessionKey()}
                onClick={() => { close(); send({ kind: 'set_yolo', on: !status().yolo }); }}
                data-testid="ai-menu-yolo"
              />
              <MenuSeparator />
              <Show when={configs().length === 0}>
                <MenuItem label="No settings offered" disabled onClick={() => {}} />
              </Show>
              {/* One group per setting the agent exposes — the same
                  generic block the status bar renders, with room for the
                  names and the tick. */}
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
                          onClick={() => { close(); send({ kind: 'set_config', id: cfg.id, value: v.value }); }}
                          data-testid={`ai-menu-config-${cfg.id}-${v.value}`}
                        />
                      )}
                    </For>
                  </>
                )}
              </For>
            </Menu>
          ),
        },
        {
          id: 'history',
          label: 'History',
          render: (at, close) => (
            <Menu x={at.x} y={at.y} onDismiss={close} data-testid="ai-menu-history">
              <Show when={recent().length === 0}>
                <MenuItem label="No earlier sessions" disabled onClick={() => {}} />
              </Show>
              <For each={recent()}>
                {(s) => (
                  <MenuItem
                    label={s.title ? `${s.title}  —  ${s.dir ?? ''}` : `${s.agent} · ${s.dir ?? ''}`}
                    onClick={() => { close(); send({ kind: 'resume', session_id: s.session_id }); }}
                    data-testid="ai-menu-resume"
                  />
                )}
              </For>
            </Menu>
          ),
        },
      ]}
    />
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
