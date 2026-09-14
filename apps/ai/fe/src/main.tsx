// Shared bundle for two surfaces: the singleton Agents manager renders the
// roster/history/launcher; an Agent controller renders one AgentSession.
// agentd sends the role and remains authoritative for both stores.

import { For, Show, createEffect, createMemo, createSignal, onCleanup, onMount } from 'solid-js';
import { HistoryPanel, historyAction, type SessionMeta } from './HistoryPanel.tsx';
import { defaultAgent, defaultCwd } from './default-agent.ts';
import { isStaleTranscript } from './transcript-guard.ts';
import { applyUsagePatch } from './usage-patch.ts';
import type { Component } from 'solid-js';
import {
  AgentRoster, AgentSession, Button, ConfirmDialog, FilePicker, Input, Menu, MenuBar, MenuItem, MenuSeparator,
  Overlay, Select, Splitter,
  applyAgentEvent, createAppBus, defineWashApp, kbdStyle, mergeAgentEvents, tokens, washCopyText,
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
  /** full working directory — what the launcher refills (N5b) */
  cwd?: string;
  /** short label for display ("wash"), not a path to start in */
  dir?: string;
  /** the agent's own one-line name for what the session was about — or
   *  the person's, when they renamed it (agentd puts theirs here) */
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
  /** a stored default prompt exists — the TEXT is fetched on demand */
  has_default_prompt?: boolean;
}

interface PersistedState {
  session_key?: string;
}

interface PreviewPatchRow {
  key: string;
  preview?: string;
}

const mergeEvents = mergeAgentEvents;

const App: Component<{ instance: string; host: HTMLElement; origin: string }> = (props) => {
	const [role, setRole] = createSignal<'session' | 'manager'>('session');
  const [events, setEvents] = createSignal<AgentEvent[]>([]);
  // One replay request in flight at a time; the snapshot clears it.
  let resyncPending = false;
  const [sessionKey, setSessionKey] = createSignal('');
  const [roster, setRoster] = createSignal<RosterState>({});
  const [error, setError] = createSignal('');
  const [managerSplit, setManagerSplit] = createSignal(68);
  let managerBody!: HTMLDivElement;

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
  // The composer's Attach button. <AgentSession> asks for paths and waits
  // on a promise; the picker is this window's, because the picker needs a
  // BE with a file client and the shared component has neither.
  // "Also allow a folder…" from a roster row. The picker is this
  // window's; the row it widens is whichever row opened it, which is not
  // necessarily the session in the detail pane.
  // A draft handed to this window by another app (`agent_draft` — see
  // apps/ai/be/app.go). The counter is what makes sending the same
  // selection twice insert it twice.
  const [draftIn, setDraftIn] = createSignal<{ text: string; seq: number } | undefined>();
  let draftSeq = 0;

  const [rootFor, setRootFor] = createSignal<{ key: string; start: string } | null>(null);
  const openAddRoot = (key: string, start: string) => setRootFor({ key, start });

  const [attaching, setAttaching] = createSignal(false);
  let attachResolve: ((paths: string[]) => void) | null = null;
  const finishAttach = (paths: string[]) => {
    setAttaching(false);
    const r = attachResolve;
    attachResolve = null;
    r?.(paths);
  };
  // The default prompt (agentd owns the file; this is the editor for it).
  // `draft` is the textarea's contents while the dialog is open — a
  // browser reload loses an unsaved edit, which is the same deal every
  // other unsaved form in wash offers, while the SAVED text survives
  // because it is a file on the host rather than anything this tab holds.
  const [promptOpen, setPromptOpen] = createSignal(false);
  const [promptDraft, setPromptDraft] = createSignal('');
  // promptPending: the dialog has been asked for, and is waiting on
  // agentd's copy of the stored text.
  //
  // The dialog opens WHEN THE TEXT ARRIVES rather than opening empty and
  // filling in later. Filling in later is a race with the user: an
  // asynchronous reply that lands after they have started editing
  // silently replaces what they typed, and Save then stores the old text
  // back. The first cut tried to gate that on "has the user typed yet",
  // which is not knowable — clearing a field that is already empty
  // produces no input event at all, so the gate stayed open and the
  // reply undid the clear. Opening on arrival has no such window.
  //
  // Safe because agentd is a local process on the same machine; this is
  // an IPC round trip, not a network one.
  const [promptPending, setPromptPending] = createSignal(false);
  // History panel (HistoryPanel.tsx). The query round-trips through
  // agentd rather than filtering here: it searches the stored
  // CONVERSATIONS, which the FE has never seen.
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
  onCleanup(() => { if (historyTimer) clearTimeout(historyTimer); });

  // Rename / delete / prune (agentd/session_admin.go). Each is a small
  // dialog over whichever list it was picked from — the roster, the
  // History panel or the Session menu — sending one key-or-id addressed
  // verb. The dialogs are here rather than in the lists because the
  // lists are shared renderers that own no state.
  const [renameFor, setRenameFor] = createSignal<{ key?: string; session_id?: string; title: string } | null>(null);
  const [renameDraft, setRenameDraft] = createSignal('');
  const openRename = (t: { key?: string; session_id?: string; title?: string }) => {
    setRenameDraft(t.title ?? '');
    setRenameFor({ key: t.key, session_id: t.session_id, title: t.title ?? '' });
  };
  const saveRename = () => {
    const t = renameFor();
    if (!t) return;
    setRenameFor(null);
    send({ kind: 'rename', key: t.key ?? '', session_id: t.session_id ?? '', title: renameDraft().trim() });
  };
  const [deleteFor, setDeleteFor] = createSignal<SessionMeta | null>(null);
  const [pruning, setPruning] = createSignal(false);
  // Horizons for "Delete all older than…". 0 is every finished session —
  // the honest word for "clear history", offered here rather than as a
  // separate verb so there is one place history is thrown away.
  const pruneChoices: [string, string][] = [
    [String(24 * 3600e3), 'a day'],
    [String(7 * 24 * 3600e3), 'a week'],
    [String(30 * 24 * 3600e3), 'a month'],
    [String(90 * 24 * 3600e3), 'three months'],
    ['0', 'any age — every finished session'],
  ];
  const [pruneAge, setPruneAge] = createSignal(pruneChoices[2][0]);

  // Why a transcript frame can now be for the wrong session: see
  // transcript-guard.ts.
  const staleTranscript = (m: Record<string, unknown>) => isStaleTranscript(m.key, sessionKey());

  const handleBE = (m: Record<string, unknown>) => {
    switch (m.kind) {
	  case 'role':
		setRole(m.role === 'manager' ? 'manager' : 'session');
		break;
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
	  case 'session_opened':
		setStarting(false);
		setError('');
		break;
	  case 'claim_denied':
		setError('This session is already controlled by another window.');
		break;
      case 'restore_failed':
        setSessionKey('');
        setEvents([]);
        break;
      case 'draft':
        // Another app sent a selection here (agent_draft). It lands in
        // the composer, not on the wire: what someone does with it — add
        // a question above it, trim it, think better of it — is the whole
        // reason it goes to the composer at all.
        draftSeq += 1;
        setDraftIn({ text: String(m.text ?? ''), seq: draftSeq });
        break;
      case 'start_failed':
        setAutostart(null);
        setStarting(false);
        setError(String(m.error ?? 'could not start'));
        break;
      case 'snapshot':
        if (staleTranscript(m)) break;
        resyncPending = false;
        if (typeof m.reset === 'boolean') {
          setEvents((prev) => mergeEvents(prev, (m.events as AgentEvent[]) ?? []));
        } else {
          setEvents(mergeEvents([], (m.events as AgentEvent[]) ?? []));
        }
        break;
      case 'event': {
        const e = m.event as AgentEvent | undefined;
        if (!e || staleTranscript(m)) break;
        // Whole rows replace by seq (agentd mutates a tool row in place);
        // a streamed reply's later chunks arrive as deltas and append. A
        // delta we cannot apply means our base is wrong — a reload landed
        // mid-reply, say — so ask for the snapshot again, once.
        const r = applyAgentEvent(events(), e);
        setEvents(r.events);
        if (r.gap && !resyncPending) {
          resyncPending = true;
          send({ kind: 'resync' });
        }
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

      case 'history_deleted':
      case 'history_pruned':
        // The store changed under the panel: re-ask with the current
        // query rather than editing the list locally, so what is shown is
        // what is on disk.
        if (role() === 'manager') askHistory(historyQuery());
        break;

      case 'default_prompt':
        // Only the reply this dialog asked for opens it. A later echo —
        // agentd answers a save with what it stored — arrives with
        // nothing pending and is ignored, rather than re-opening a
        // dialog the user just dismissed.
        if (promptPending()) {
          setPromptPending(false);
          setPromptDraft(String(m.text ?? ''));
          setPromptOpen(true);
        }
        break;

      case 'roster':
        setRoster((m.state as RosterState) ?? {});
        // Fill the launcher in on the first roster that names the
        // adapters (docs/AGENT_UX.md N5a/N5b): the agent you used last,
        // in the folder you used it in. Once only, and only while the
        // untouched launcher is what's showing — a user who set the
        // select (or a window that's already a session) is never fought.
        if (!agentDefaulted && !sessionKey() && !autostart() && agent() === '') {
          const d = defaultAgent(roster().adapters ?? [], roster().recent ?? []);
          if (d) {
            agentDefaulted = true;
            setAgent(d);
            // Only if the user hasn't typed/picked one — the folder field
            // is editable from the moment the window opens.
            if (cwd() === '') setCwd(defaultCwd(roster().recent ?? []));
          }
        }
        break;

      case 'usage_patch':
        setRoster((prev) => applyUsagePatch(prev, m.rows));
        break;
      case 'preview_patch': {
        const patches = new Map(((m.rows as PreviewPatchRow[] | undefined) ?? []).map((r) => [r.key, r.preview ?? '']));
        setRoster((prev) => ({
          ...prev,
          rows: (prev.rows ?? []).map((r) => patches.has(r.key) ? { ...r, preview: patches.get(r.key) } : r),
        }));
        break;
      }
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

  // History is a permanent manager pane now, so populate it as soon as
  // agentd assigns this window the manager role. Session controllers do
  // not ask for or retain the archive.
  let historyLoaded = false;
  createEffect(() => {
    if (role() === 'manager' && !historyLoaded) {
      historyLoaded = true;
      askHistory(historyQuery());
    }
  });

  // ---- sessions pane (docs/SIDEBAR.md M2) ----
  // Always there, and resizable — the same split every other two-pane wash
  // app uses (fm's preview, edit's file tree): a <Splitter> over a grid,
  // width in percent, dragged not toggled.
  //
  // It used to hide behind a button that opened it on rules — more than
  // one session, a question on another row, an empty window. Every one of
  // those rules was a guess about when the list was worth its width, and
  // the guesses were what made the app hard to predict: the way back to
  // your own agent depended on knowing a toggle existed. A list you can
  // drag to nothing is a better answer than a list that appears on
  // conditions.
  //
  // Width is local to this manager window. The default keeps the launcher
  // and history roomy while leaving enough space to scan running agents.
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
  });
  // A question on another row needed to OPEN the pane once. There is no
  // pane to open now — it is already showing, with the question in it.
  //
  // The clock only ticks while rows are on screen, so an idle window holds
  // no interval.
  createEffect(() => {
    if (rows().length === 0) return;
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
      reason: r?.reason,
      used: r?.used,
      size: r?.size,
      title: r?.title,
      mode: r?.mode,
      modes: r?.modes,
      configs: r?.configs,
      commands: r?.commands,
      yolo: r?.yolo,
      queued: r?.queued,
      roots: r?.roots,
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

  const openPrompt = () => {
    setPromptPending(true);
    send({ kind: 'default_prompt' });
  };

  const start = () => {
    // Same preference the preselect uses (N5a), so a submit from
    // "Choose…" and the visible default cannot disagree about the agent.
    const a = agent() || defaultAgent(adapters(), roster().recent ?? []);
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
    <div style={{ padding: `${tokens.spaceMd}px`, display: 'flex', 'flex-direction': 'column', gap: `${tokens.spaceMd}px` }}>
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

      {/* Stated on the launcher, not hidden in a menu: a default prompt that
          silently prefixes every new session is the kind of magic that
          gets blamed on the agent. One line, and one click to read or
          change it. */}
      <div style={{ display: 'flex', 'align-items': 'center', gap: `${tokens.spaceSm}px`, font: tokens.type.textSm }}>
        <span data-testid="ai-prompt-status" style={{ color: tokens.fgMuted }}>
          {roster().has_default_prompt
            ? 'A default prompt will be sent first.'
            : 'No default prompt.'}
        </span>
        <Button variant="ghost" data-testid="ai-prompt-open" onClick={openPrompt}>
          {roster().has_default_prompt ? 'Edit…' : 'Set…'}
        </Button>
      </div>

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
              <MenuItem
                label="Rename session…"
                disabled={!sessionKey()}
                onClick={() => { close(); openRename({ key: sessionKey(), session_id: row()?.session_id, title: row()?.title }); }}
                data-testid="ai-menu-rename"
              />
              {/* Where the agent is working is exactly where a person
                  wants a shell. Same verb the roster row offers, because
                  the window showing a session and the row naming it are
                  two views of one thing. */}
              <MenuItem
                label="Open terminal here"
                disabled={!row()?.cwd}
                onClick={() => { close(); send({ kind: 'open_terminal', cwd: row()?.cwd ?? '' }); }}
                data-testid="ai-menu-open-terminal"
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
      ]}
    />
  );

  const rosterPane = (
    <div
      data-testid="ai-roster-pane"
      style={{
        'min-width': 0,
        'min-height': 0,
        height: '100%',
        'box-sizing': 'border-box',
        background: tokens.bgInset,
        overflow: 'auto',
        // Wheel past the end of the list and the scroll stops here rather
        // than chaining out to the window frame, which would move this
        // pane and the transcript together — the two panes are separate
        // scrollers, so a gesture aimed at one stays in it.
        'overscroll-behavior': 'contain',
        padding: `${tokens.spaceSm}px`,
      }}
    >
        <AgentRoster
          rows={rows}
          // Off-row questions only. The detail pane on the right already
          // renders the question for the session it is showing, and with
          // the list always open the two were printing the same question
          // twice in one window. The list's job is what you can't see.
          asks={offRowAsks}
          startedAt={(key) => startedAt.get(key) ?? Date.now()}
          now={now}
          activeKey={sessionKey}
          onActivate={(r) => send({ kind: 'select', key: r.key })}
          onReattach={(r) => send({ kind: 'row_reattach', key: r.key })}
          onDetach={(r) => send({ kind: 'row_detach', key: r.key })}
          onCancel={(r) => send({ kind: 'row_cancel', key: r.key })}
          onStop={(r) => send({ kind: 'row_stop', key: r.key })}
          onRename={(r) => openRename({ key: r.key, session_id: r.session_id, title: r.title })}
          onAddRoot={(r) => openAddRoot(r.key, r.cwd ?? '')}
          onOpenTerminal={(r) => send({ kind: 'open_terminal', cwd: r.cwd ?? '' })}
          onAnswer={(a, decision, remember) => send({
            kind: 'answer',
            id: a.id,
            decision,
            rule: remember ? (a.suggested_rule ?? '') : '',
          })}
        />
    </div>
  );

  const historyPanel = (
    <HistoryPanel
      embedded
      sessions={historySessions}
      query={historyQuery}
      loading={historyLoading}
      onQuery={onHistoryQuery}
      onRename={(s) => openRename({ key: s.row_key, session_id: s.session_id, title: s.title })}
      onDelete={(s) => setDeleteFor(s)}
      onPrune={() => setPruning(true)}
      onResume={(s) => {
        const act = historyAction(s);
        if (act === 'reattach') send({ kind: 'row_reattach', key: s.row_key });
        else if (act === 'focus') send({ kind: 'row_focus', key: s.row_key });
        else send({ kind: 'resume', session_id: s.session_id });
      }}
    />
  );

  // The initial-prompt editor. An Overlay like the close dialog, because
  // it is a decision you finish or abandon rather than a panel you leave
  // open — and because dismissing must mean "leave it as it was", which
  // is exactly what not sending set_default prompt does.
  const promptDialog = (
    <Show when={promptOpen()}>
      <Overlay onDismiss={() => setPromptOpen(false)} data-testid="ai-prompt-dialog">
        <div style={{ 'font-weight': 600, 'margin-bottom': `${tokens.spaceSm}px` }}>
          Default prompt
        </div>
        <div style={{ font: tokens.type.textMd, opacity: 0.75, 'max-width': '54ch', 'margin-bottom': `${tokens.spaceMd}px` }}>
          Sent to every new session on this machine, before anything you
          type. Standing instructions — which repo, which conventions,
          what to read first — so you stop retyping them. Existing
          sessions are untouched.
        </div>
        <textarea
          data-testid="ai-prompt-text"
          value={promptDraft()}
          onInput={(e) => setPromptDraft(e.currentTarget.value)}
          rows={10}
          spellcheck={false}
          style={{
            width: '58ch',
            'max-width': '80vw',
            resize: 'vertical',
            font: tokens.type.monoMd,
            color: tokens.fg,
            background: tokens.bgInset,
            border: `1px solid ${tokens.borderMenu}`,
            'border-radius': tokens.radiusMd,
            padding: `${tokens.spaceMd}px`,
          }}
        />
        <div style={{ display: 'flex', gap: `${tokens.spaceMd}px`, 'justify-content': 'flex-end', 'margin-top': `${tokens.spaceLg}px` }}>
          <Button data-testid="ai-prompt-cancel" onClick={() => setPromptOpen(false)}>
            Cancel
          </Button>
          <Button
            variant="primary"
            data-testid="ai-prompt-save"
            onClick={() => {
              send({ kind: 'set_default_prompt', text: promptDraft() });
              setPromptOpen(false);
            }}
          >
            Save
          </Button>
        </div>
      </Overlay>
    </Show>
  );

  // One name box for every list. Enter saves, Escape (the Overlay's
  // dismiss) leaves the name as it was; an empty name clears yours and
  // lets the agent's own show again.
  const renameDialog = (
    <Show when={renameFor()}>
      <Overlay onDismiss={() => setRenameFor(null)} data-testid="ai-rename-dialog">
        <div style={{ 'font-weight': 600, 'margin-bottom': `${tokens.spaceSm}px` }}>Rename session</div>
        <div style={{ font: tokens.type.textMd, opacity: 0.75, 'max-width': '46ch', 'margin-bottom': `${tokens.spaceMd}px` }}>
          Shown wherever this session is listed. Leave it empty to go back to
          the agent's own name.
        </div>
        <Input
          data-testid="ai-rename-input"
          value={renameDraft()}
          ref={(el: HTMLInputElement) => queueMicrotask(() => { el.focus(); el.select(); })}
          onInput={(e: InputEvent) => setRenameDraft((e.currentTarget as HTMLInputElement).value)}
          onKeyDown={(e: KeyboardEvent) => { if (e.key === 'Enter') { e.preventDefault(); saveRename(); } }}
          style={{ width: '46ch', 'max-width': '80vw' }}
        />
        <div style={{ display: 'flex', gap: `${tokens.spaceMd}px`, 'justify-content': 'flex-end', 'margin-top': `${tokens.spaceLg}px` }}>
          <Button data-testid="ai-rename-cancel" onClick={() => setRenameFor(null)}>Cancel</Button>
          <Button variant="primary" data-testid="ai-rename-save" onClick={saveRename}>Save</Button>
        </div>
      </Overlay>
    </Show>
  );

  const deleteDialog = (
    <Show when={deleteFor()}>
      {(s) => (
        <ConfirmDialog
          title="Delete this conversation?"
          confirmLabel="Delete"
          danger
          data-testid="ai-delete-confirm"
          confirmTestid="ai-delete-confirm-yes"
          cancelTestid="ai-delete-confirm-no"
          onCancel={() => setDeleteFor(null)}
          onConfirm={() => {
            const id = s().session_id;
            setDeleteFor(null);
            send({ kind: 'delete_session', session_id: id });
          }}
        >
          <div style={{ font: tokens.type.textMd, opacity: 0.75, 'max-width': '46ch' }}>
            <b>{s().title || s().session_id}</b> — its transcript is removed from disk and it leaves
            History. The agent's own record of the session is not touched.
          </div>
        </ConfirmDialog>
      )}
    </Show>
  );

  const pruneDialog = (
    <Show when={pruning()}>
      <ConfirmDialog
        title="Delete older conversations?"
        confirmLabel="Delete"
        danger
        data-testid="ai-prune-dialog"
        confirmTestid="ai-prune-confirm"
        cancelTestid="ai-prune-cancel"
        onCancel={() => setPruning(false)}
        onConfirm={() => {
          setPruning(false);
          send({ kind: 'prune_history', max_age_ms: Number(pruneAge()) });
        }}
      >
        <div style={{ display: 'flex', 'flex-direction': 'column', gap: `${tokens.spaceMd}px`, 'max-width': '46ch' }}>
          <div style={{ font: tokens.type.textMd, opacity: 0.75 }}>
            Every finished session whose last activity is older than this is
            deleted from disk. Running sessions are kept.
          </div>
          <Select value={pruneAge()} onChange={setPruneAge} options={pruneChoices} data-testid="ai-prune-age" />
        </div>
      </ConfirmDialog>
    </Show>
  );

  // Both pickers live at the window's root rather than inside the
  // launcher fragment: the launcher renders only while there is NO
  // session, and both of these are reached from a running one.
  const attachPicker = (
    <FilePicker
      open={attaching()}
      mode="open"
      host={props.host}
      hostInstanceID={props.instance}
      start={row()?.cwd || cwd()}
      onConfirm={(p) => finishAttach([p])}
      onCancel={() => finishAttach([])}
      data-testid="ai-attach-picker"
    />
  );

  const rootPicker = (
    <Show when={rootFor()}>
      {(r) => (
        <FilePicker
          open
          mode="directory"
          host={props.host}
          hostInstanceID={props.instance}
          start={r().start}
          onConfirm={(p) => {
            // Read the key BEFORE clearing: `r()` is <Show>'s accessor,
            // and it stops reporting the row the moment the condition
            // goes false — so reading it after the clear sent an empty
            // key and the widening silently did nothing.
            const key = r().key;
            setRootFor(null);
            send({ kind: 'row_add_root', key, path: p });
          }}
          onCancel={() => setRootFor(null)}
          data-testid="ai-root-picker"
        />
      )}
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

  // The manager is a stable workspace rather than a sequence of modes:
  // start a session in the compact upper-left pane, find an older one
  // below it, and keep the live roster visible on the right throughout.
  const managerView = (
    <>
      {promptDialog}
      {renameDialog}
      {deleteDialog}
      {pruneDialog}
      {rootPicker}
      <div style={{ height: '100%', display: 'flex', 'flex-direction': 'column' }}>
        <div
          style={{
            padding: `${tokens.spaceSm}px ${tokens.spaceMd}px`,
            border: `0 solid ${tokens.borderMenu}`,
            'border-bottom-width': '1px',
            font: tokens.type.titleSm,
          }}
        >
          Agents
        </div>
        <div
          ref={managerBody}
          style={{
            flex: 1,
            'min-height': 0,
            display: 'grid',
            'grid-template-columns': `minmax(320px, ${managerSplit()}%) 5px minmax(220px, 1fr)`,
            overflow: 'hidden',
          }}
        >
          <div
            style={{
              'min-width': 0,
              'min-height': 0,
              display: 'grid',
              'grid-template-rows': 'minmax(220px, 36%) minmax(0, 1fr)',
            }}
          >
            <section data-testid="agents-new-pane" style={{ overflow: 'auto', 'min-height': 0 }}>
              {launcher}
            </section>
            <section
              data-testid="agents-history-pane"
              style={{ overflow: 'hidden', 'min-height': 0, border: `0 solid ${tokens.borderMenu}`, 'border-top-width': '1px' }}
            >
              {historyPanel}
            </section>
          </div>
          <Splitter
            container={managerBody}
            min={45}
            max={80}
            thickness={5}
            onChange={setManagerSplit}
            data-testid="agents-manager-splitter"
          />
          <section
            data-testid="agents-running-pane"
            style={{ 'min-width': 0, 'min-height': 0, display: 'flex', 'flex-direction': 'column', background: tokens.bgInset }}
          >
            <div style={{ padding: `${tokens.spaceMd}px`, font: tokens.type.titleSm, color: tokens.fg }}>
              Running
            </div>
            <div style={{ flex: 1, 'min-height': 0, overflow: 'hidden' }}>{rosterPane}</div>
          </section>
        </div>
      </div>
    </>
  );

  const sessionView = (
    <>
    {closeDialog}

    {renameDialog}
    {attachPicker}
    <div style={{ height: '100%', display: 'flex', 'flex-direction': 'column' }}>
      {menubar}
      <div
        data-testid="ai-body"
        style={{
          flex: 1,
          'min-height': 0,
		  display: 'flex',
          // One row, clamped to the body — the same `grid-template-rows`
          // + `overflow: hidden` pair wash-edit's and wash-fm's split
          // bodies use. The row is otherwise implicit and auto-sized, so
          // its height is a question about content rather than about the
          // window; minmax(0, 1fr) makes it the body's height flat out,
          // which is the precondition for each pane scrolling its own
          // way. Without it, a row that outgrew the window would hand
          // the scrollbar to the window frame (whose app slot is
          // `overflow: auto`) — one scrollbar moving both panes.
          'grid-template-rows': 'minmax(0, 1fr)',
          overflow: 'hidden',
        }}
      >
		<div style={{ flex: 1, 'min-width': 0, 'min-height': 0, display: 'flex', 'flex-direction': 'column' }}>
          <Show
            when={sessionKey()}
			fallback={<div style={{ padding: `${tokens.spaceXl}px`, color: tokens.fgMuted }}><Show when={autostart()} fallback={<><div style={{ 'margin-bottom': `${tokens.spaceMd}px` }}>This window is not attached to a session.</div><Button onClick={() => send({ kind: 'open_agents' })}>Open Agents</Button></>}>{booting}</Show></div>}
          >
            <AgentSession
              events={events}
              asks={asks}
              status={status}
              onSend={(text, blocks) => send({ kind: 'prompt', text, blocks })}
              onRemoveRoot={(path) => send({ kind: 'row_remove_root', key: sessionKey(), path })}
              insertDraft={draftIn}
              onPickFiles={() =>
                new Promise<string[]>((resolve) => {
                  // A picker already open would strand the earlier waiter.
                  finishAttach([]);
                  attachResolve = resolve;
                  setAttaching(true);
                })
              }
              onAnswer={(id, decision, rule) => send({ kind: 'answer', id, decision, rule: rule ?? '' })}
              onCancel={() => send({ kind: 'cancel' })}
              onSetMode={(mode) => send({ kind: 'set_mode', mode })}
              onSetConfig={(id, value) => send({ kind: 'set_config', id, value })}
              onOpenTool={(e) => {
                // A tool row names a file; clicking it opens that file in
                // whatever app registered for the type (the router's own
                // open routing, so this app does not have to know that
                // .png goes to imageview and .go goes to edit). Standalone
                // Agent had no onOpenTool at all, so every row was inert —
                // only wash-edit's agent tab could act on one.
                const path = e.path || (e.title ?? '').trim();
                if (path) send({ kind: 'open_path', path });
              }}
            />
          </Show>
        </div>
      </div>
    </div>
    </>
  );

  return <Show when={role() === 'manager'} fallback={sessionView}>{managerView}</Show>;
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
defineWashApp('wash-app-agents', App);
