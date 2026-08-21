// wash-app-ai — roster + session, master-detail (docs/SIDEBAR.md M2).
//
// The window is a thin host around <AgentSession> from @wash/ui: agentd
// owns the session, the transcript and the approval queue, so everything
// here is a subscription and a form. An unstarted window renders the
// launcher, which is why there is no separate "new session" dialog.
//
// The left pane is <AgentRoster> — every session agentd knows about, the
// same renderer the desktop rail used. Picking a row re-points the detail
// pane at that session. The roster data was already arriving here (the
// window subscribes for its own status line and the launcher's adapter
// list); M2 renders it and, in M2b, acts on it.

import { For, Show, createEffect, createMemo, createSignal, onCleanup, onMount } from 'solid-js';
import { HistoryPanel, historyAction, type SessionMeta } from './HistoryPanel.tsx';
import { defaultAgent } from './default-agent.ts';
import type { Component } from 'solid-js';
import {
  AgentRoster, AgentSession, Button, FilePicker, Menu, MenuBar, MenuItem, MenuSeparator, Overlay, Select,
  createAppBus, defineWashApp, kbdStyle, tokens, washCopyText,
} from '@wash/ui';
import type {
  AgentAsk, AgentEvent, AgentStatus, RosterAsk, RosterRow,
} from '@wash/ui';

interface Adapter {
  id: string;
  name: string;
  available: boolean;
  note?: string;
}

interface RecentSession {
  session_id: string;
  agent: string;
  dir?: string;
  /** the agent's own one-line name for what the session was about */
  title?: string;
  last_seen: number;
  /** running right now — in the roster above, not something to resume */
  live?: boolean;
  /** running, but with no window pointing at it: reattach, do not resume */
  detached?: boolean;
  /** the roster key a reattach names; present only while it has a row */
  row_key?: string;
}

interface RosterState {
  rows?: RosterRow[];
  asks?: RosterAsk[];
  adapters?: Adapter[];
  recent?: RecentSession[];
}

interface PersistedState {
  session_key?: string;
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

  // Launcher form. agentDefaulted latches N5a's one-shot preselect so
  // later roster pushes can't overwrite a deliberate "Choose…".
  let agentDefaulted = false;
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
  // History panel (HistoryPanel.tsx). The query round-trips through
  // agentd rather than filtering here: it searches the stored
  // CONVERSATIONS, which the FE has never seen.
  const [historyOpen, setHistoryOpen] = createSignal(false);
  const [historyQuery, setHistoryQuery] = createSignal('');
  const [historySessions, setHistorySessions] = createSignal<SessionMeta[]>([]);
  const [historyLoading, setHistoryLoading] = createSignal(false);
  let historyTimer: ReturnType<typeof setTimeout> | undefined;
  const askHistory = (q: string) => {
    setHistoryLoading(true);
    send({ kind: 'history', query: q });
  };
  // Debounced: every keystroke would otherwise grep every transcript on
  // the machine.
  const onHistoryQuery = (q: string) => {
    setHistoryQuery(q);
    if (historyTimer) clearTimeout(historyTimer);
    historyTimer = setTimeout(() => askHistory(q), 150);
  };
  const openHistory = () => {
    setHistoryOpen(true);
    askHistory(historyQuery());
  };
  onCleanup(() => { if (historyTimer) clearTimeout(historyTimer); });

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
      case 'restore_failed':
        setSessionKey('');
        setEvents([]);
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
      case 'history':
        // Ignore an answer to a query we have already moved past, or the
        // list flickers back to stale results as you type.
        if (String(m.query ?? '') === historyQuery()) {
          setHistorySessions((m.sessions as SessionMeta[]) ?? []);
          setHistoryLoading(false);
        }
        break;

      case 'roster':
        setRoster((m.state as RosterState) ?? {});
        // Preselect the launcher's agent on the first roster that names
        // the adapters (docs/AGENT_UX.md N5a). Once only, and only while
        // the untouched launcher is what's showing — a user who set the
        // select (or a window that's already a session) is never fought.
        if (!agentDefaulted && !sessionKey() && !autostart() && agent() === '') {
          const d = defaultAgent(roster().adapters ?? []);
          if (d) {
            agentDefaulted = true;
            setAgent(d);
          }
        }
        break;
    }
  };

  const { send } = createAppBus(props, {
    onMsg: handleBE,
    onState: (state) => {
      const saved = state as PersistedState | null;
      const key = typeof saved?.session_key === 'string' ? saved.session_key : '';
      if (!key) return;
      // wash:state lands before queued wash:msg events on every remount.
      // Restore the view immediately, then ask the still-running backend
      // for an authoritative transcript snapshot.
      setSessionKey(key);
      send({ kind: 'restore', key });
    },
  });

  // ---- roster pane (docs/SIDEBAR.md M2) ----
  // Show/hide is local: the pane is a view of this window, not state agentd
  // or anyone else owns. Open by default when there is more than one
  // session to choose between, because that is when a list earns its width.
  const [rosterOpen, setRosterOpen] = createSignal(false);
  const rows = () => roster().rows ?? [];
  // Elapsed per row, anchored on arrival: since_ms is measured at PUSH
  // time, so it can't be compared against a local clock directly. Same
  // trick the rail used, and the reason the roster takes startedAt/now
  // rather than reading a clock itself.
  const startedAt = new Map<string, number>();
  const [now, setNow] = createSignal(Date.now());
  createEffect(() => {
    const arrival = Date.now();
    const live = new Set<string>();
    for (const r of rows()) {
      live.add(r.key);
      startedAt.set(r.key, arrival - Math.max(0, r.since_ms || 0));
    }
    for (const key of [...startedAt.keys()]) if (!live.has(key)) startedAt.delete(key);
    // A second session appearing is what makes the list worth its width.
    if (rows().length > 1) setRosterOpen(true);
  });
  // A question on another row opens the pane that can show it — but only
  // once per question, so a pane the user deliberately closed stays closed
  // until something genuinely new needs an answer. Same rule the desktop
  // rail follows when it auto-expands its agents section on a new ask.
  const openedFor = new Set<string>();
  createEffect(() => {
    let fresh = false;
    const live = new Set<string>();
    for (const a of offRowAsks()) {
      live.add(a.id);
      if (!openedFor.has(a.id)) {
        openedFor.add(a.id);
        fresh = true;
      }
    }
    for (const id of [...openedFor]) if (!live.has(id)) openedFor.delete(id);
    if (fresh) setRosterOpen(true);
  });
  // The clock only ticks while rows are on screen, so an idle window holds
  // no interval.
  createEffect(() => {
    if (!rosterOpen() || rows().length === 0) return;
    setNow(Date.now());
    const t = setInterval(() => setNow(Date.now()), 1000);
    onCleanup(() => clearInterval(t));
  });

  const adapters = () => roster().adapters ?? [];
  const row = createMemo(() => (roster().rows ?? []).find((r) => r.key === sessionKey()));
  const asks = createMemo<AgentAsk[]>(() =>
    (roster().asks ?? []).filter((a) => a.row_key === sessionKey()),
  );
  // Questions on a row this window is not showing. The session pane above
  // can only render its own — a window is one session's view — but the
  // roster pane renders every row's, and answering is key-addressed, so
  // this window can answer them perfectly well. The gap is purely that the
  // roster pane can be closed, and a closed pane says nothing about what
  // is waiting behind it.
  const offRowAsks = createMemo<AgentAsk[]>(() =>
    (roster().asks ?? []).filter((a) => a.row_key !== sessionKey()),
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

  // Live and REACHABLE are not the same thing. Hiding live sessions is
  // right — offering to resume one that is already running would be
  // offering to duplicate it — but a DETACHED session is live and is
  // exactly what you opened this menu to get back. It gets listed with a
  // reattach verb instead of a resume one.
  const recent = () => (roster().recent ?? []).filter((s) => historyAction(s) !== 'none');
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
    // Same preference the preselect uses (N5a), so a submit from
    // "Choose…" and the visible default cannot disagree about the agent.
    const a = agent() || defaultAgent(adapters());
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
              {/* The menu keeps the fast path — reopen one of the last
                  few — and hands everything else to the panel, which is
                  searchable and can carry the metadata a menu item
                  cannot. */}
              <MenuItem
                label="Browse all history…"
                data-testid="ai-menu-history-browse"
                onClick={() => { close(); openHistory(); }}
              />
              <MenuSeparator />
              <Show when={recent().length === 0}>
                <MenuItem label="No earlier sessions" disabled onClick={() => {}} />
              </Show>
              <For each={recent()}>
                {(s) => {
                  const base = s.title ? `${s.title}  —  ${s.dir ?? ''}` : `${s.agent} · ${s.dir ?? ''}`;
                  // Two verbs, never confused: a detached session is
                  // already running and must be reattached to by row key,
                  // and reattach is key-addressed rather than
                  // session-id-addressed. Sending `resume` here would
                  // start a second copy of a session you already have.
                  return historyAction(s) === 'reattach'
                    ? (
                      <MenuItem
                        label={`Reattach — ${base}`}
                        onClick={() => { close(); send({ kind: 'row_reattach', key: s.row_key }); }}
                        data-testid="ai-menu-reattach"
                      />
                    )
                    : (
                      <MenuItem
                        label={base}
                        onClick={() => { close(); send({ kind: 'resume', session_id: s.session_id }); }}
                        data-testid="ai-menu-resume"
                      />
                    );
                }}
              </For>
            </Menu>
          ),
        },
      ]}
    />
  );

  // rosterPane is the master half. Verbs arrive in M2b; for now it lists
  // and selects, which is already the thing the rail could not do for a
  // remote host.
  const rosterPane = (
    <Show when={rosterOpen()}>
      <div
        data-testid="ai-roster-pane"
        style={{
          width: '230px',
          'flex-shrink': 0,
          'border-right': `1px solid ${tokens.borderMenu}`,
          background: tokens.bgInset,
          overflow: 'auto',
          padding: `${tokens.spaceSm}px`,
        }}
      >
        {/* The verbs are key-addressed, so they act on the row rather than
            on whatever this window happens to be showing — including a
            session with no window at all. That is the whole difference
            between here and the rail: an app talking to its own host's
            agentd has a router-attested sender, so it may act. */}
        <AgentRoster
          rows={rows}
          asks={() => roster().asks ?? []}
          startedAt={(key) => startedAt.get(key) ?? Date.now()}
          now={now}
          activeKey={sessionKey}
          onActivate={(r) => send({ kind: 'select', key: r.key })}
          onReattach={(r) => send({ kind: 'row_reattach', key: r.key })}
          onDetach={(r) => send({ kind: 'row_detach', key: r.key })}
          onCancel={(r) => send({ kind: 'row_cancel', key: r.key })}
          onStop={(r) => send({ kind: 'row_stop', key: r.key })}
          onAnswer={(a, decision, remember) => send({
            kind: 'answer',
            id: a.id,
            decision,
            rule: remember ? (a.suggested_rule ?? '') : '',
          })}
        />
      </div>
    </Show>
  );

  // The toggle lives in the window's own chrome rather than the desktop's:
  // it is this window's layout, and the rail is no longer in the business
  // of driving what an app shows.
  //
  // Labelled "Roster", not "Sessions": the menubar next to it already has a
  // Session menu, and two adjacent controls whose names differ by an "s"
  // is a coin toss for the reader. It is also the word the docs use.
  const rosterToggle = (
    <Button
      data-testid="ai-roster-toggle"
      title={
        rosterOpen()
          ? 'Hide the roster'
          : offRowAsks().length > 0
            ? `${offRowAsks().length} question(s) waiting on another session`
            : 'Show every agent session on this host'
      }
      onClick={() => setRosterOpen(!rosterOpen())}
    >
      {rosterOpen() ? '\u25c0 Roster' : '\u25b6 Roster'}
      {/* A closed pane must still say that something is blocked behind it.
          An agent waiting on a human is the one status worth a badge. */}
      <Show when={!rosterOpen() && offRowAsks().length > 0}>
        <span
          data-testid="ai-roster-ask-badge"
          style={{
            'margin-left': '6px',
            padding: '0 5px',
            'border-radius': '8px',
            background: tokens.accentAmber,
            color: tokens.bgCanvas,
            'font-size': '10px',
            'font-weight': '600',
          }}
        >
          {offRowAsks().length}
        </span>
      </Show>
    </Button>
  );

  const historyPanel = (
    <Show when={historyOpen()}>
      <HistoryPanel
        sessions={historySessions}
        query={historyQuery}
        loading={historyLoading}
        onQuery={onHistoryQuery}
        onClose={() => setHistoryOpen(false)}
        onResume={(s) => {
          setHistoryOpen(false);
          // Same predicate as the menu, same two verbs. The panel used to
          // send `resume` unconditionally, which for an already-running
          // session meant duplicating it — the exact outcome the menu's
          // filter existed to prevent, in the view that had no filter.
          if (historyAction(s) === 'reattach') send({ kind: 'row_reattach', key: s.row_key });
          else send({ kind: 'resume', session_id: s.session_id });
        }}
      />
    </Show>
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

  // Master-detail. The menubar and the roster toggle sit in one header
  // ABOVE the split, so the roster survives every detail state — including
  // the launcher, which is what makes "new session while three are
  // running" a coherent thing to do rather than a mode you leave the list
  // to enter.
  const header = (
    <div style={{ display: 'flex', 'align-items': 'center', gap: `${tokens.spaceSm}px` }}>
      <div style={{ flex: 1, 'min-width': 0 }}>{menubar}</div>
      {rosterToggle}
    </div>
  );

  return (
    <>
    {closeDialog}
    {historyPanel}
    <div style={{ height: '100%', display: 'flex', 'flex-direction': 'column' }}>
      {header}
      <div style={{ flex: 1, 'min-height': 0, display: 'flex' }}>
        {rosterPane}
        <div style={{ flex: 1, 'min-width': 0, display: 'flex', 'flex-direction': 'column' }}>
          <Show
            when={sessionKey()}
            fallback={
              <div style={{ flex: 1, 'min-height': 0, overflow: 'auto' }}>
                <Show when={autostart()} fallback={launcher}>{booting}</Show>
              </div>
            }
          >
            <AgentSession
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
        </div>
      </div>
    </div>
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
