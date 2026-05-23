// wash-app-journal: live systemd-journald viewer.
//
// Layout:
//   ┌──────────────┬─────────────────────────────────────────────┐
//   │ sidebar      │ toolbar: range · priority · filter          │
//   │ "System"     ├─────────────────────────────────────────────┤
//   │ sshd.service │ log pane (virtualized, priority-colored)    │
//   │ ...          │                                             │
//   └──────────────┴─────────────────────────────────────────────┘
//
// One journalctl subprocess per window — restarted on every
// select-change. Lines arrive batched (~100ms cadence); the FE keeps
// a rolling buffer capped at MAX_LINES so a chatty unit doesn't OOM
// the tab. Stale generations (from a previous selection) are filtered
// out by `gen` matching.

import { For, Show, createMemo, createSignal, onCleanup, onMount } from 'solid-js';
import type { Component } from 'solid-js';
import { defineWashApp, tokens } from '@wash/ui';
import { RefreshCw, ShieldAlert, Search } from 'lucide-solid';

// ----- types (mirror cmd/wash-journal wire structs) -----

interface Unit {
  name: string;
  active: string;
  sub: string;
  description: string;
}

interface LogEntry {
  ts: number;        // µs since epoch
  priority: number;  // 0..7
  unit: string;
  ident: string;
  pid: number;
  message: string;
}

type StreamStatus = 'idle' | 'running' | 'perm_denied' | 'failed' | 'closed';

// MAX_LINES bounds the in-memory log ring per window. ~10k lines at
// ~120 bytes ≈ 1.2 MB; still snappy in solid's <For>, and enough
// scrollback for a meaningful debugging session. Older lines fall off
// the front as new ones arrive.
const MAX_LINES = 10_000;

// SYSTEM_KEY is the sentinel name for the "System (all units)" entry
// in the sidebar. The wire protocol uses empty-string Unit to mean
// "no -u filter"; we use this string in the FE so the selection
// model is total.
const SYSTEM_KEY = '__system__';

// Lucide-style colour cues per priority. journald priority levels
// (man 3 syslog):
//   0 emerg · 1 alert · 2 crit · 3 err · 4 warning · 5 notice · 6 info · 7 debug
const priorityColor = (p: number): string => {
  if (p <= 3) return '#ff7a7a';  // err and worse — red
  if (p === 4) return '#f0c050';  // warning — amber
  if (p === 5) return '#c0d8ff';  // notice — pale blue
  if (p === 7) return '#666';     // debug — dim
  return '#bbb';                   // info / unknown
};

const priorityLabel = (p: number): string =>
  ['emerg', 'alert', 'crit', 'err', 'warn', 'notice', 'info', 'debug'][p] ?? '?';

// fmtTime renders a journald µs timestamp as HH:MM:SS.mmm in local
// time. Matches `journalctl -o short-precise` enough that users feel
// at home.
function fmtTime(ts: number): string {
  if (!ts) return '            ';
  const d = new Date(ts / 1000);
  const hh = String(d.getHours()).padStart(2, '0');
  const mm = String(d.getMinutes()).padStart(2, '0');
  const ss = String(d.getSeconds()).padStart(2, '0');
  const ms = String(d.getMilliseconds()).padStart(3, '0');
  return `${hh}:${mm}:${ss}.${ms}`;
}

// shortUnit trims a unit name for the row prefix. We drop `.service`
// because every visible row is .service in v1; saves ~9 chars.
function shortUnit(u: string, ident: string): string {
  const tag = u || ident || '';
  return tag.replace(/\.service$/, '');
}

const App: Component<{ instance: string; host: HTMLElement }> = (props) => {
  const send = (msg: unknown) => window.wash.sendAppMsg(props.instance, msg);

  // ----- state -----

  const [units, setUnits] = createSignal<Unit[]>([]);
  const [selected, setSelected] = createSignal(SYSTEM_KEY);
  const [range, setRange] = createSignal<'boot' | 'hour' | 'day' | 'all'>('boot');
  const [priority, setPriority] = createSignal(0); // 0 = no filter
  const [asRoot, setAsRoot] = createSignal(false);
  const [filterText, setFilterText] = createSignal('');
  const [follow, setFollow] = createSignal(true);

  const [status, setStatus] = createSignal<StreamStatus>('idle');
  const [statusError, setStatusError] = createSignal('');
  const [currentGen, setCurrentGen] = createSignal(0);

  const [lines, setLines] = createSignal<LogEntry[]>([]);

  let logEl: HTMLDivElement | undefined;
  let filterEl: HTMLInputElement | undefined;

  // ----- BE messages -----

  const handleBE = (m: any) => {
    if (!m || typeof m !== 'object') return;
    switch (m.kind) {
      case 'units':
        setUnits(Array.isArray(m.units) ? (m.units as Unit[]) : []);
        return;
      case 'lines': {
        // Drop stale-generation batches — happens when the user
        // switches units before the previous stream's flushNow lands.
        if (typeof m.gen === 'number' && m.gen !== currentGen()) return;
        const batch = (m.lines as LogEntry[]) || [];
        if (batch.length === 0) return;
        setLines((prev) => {
          const next = prev.concat(batch);
          if (next.length > MAX_LINES) next.splice(0, next.length - MAX_LINES);
          return next;
        });
        if (follow()) queueScroll();
        return;
      }
      case 'stream_state':
        if (typeof m.gen === 'number') setCurrentGen(m.gen);
        setStatus(m.status as StreamStatus);
        setStatusError(typeof m.error === 'string' ? m.error : '');
        // Server-side authoritative `as_root`: keeps the toggle in sync
        // when the user accepts a priv prompt.
        if (typeof m.as_root === 'boolean') setAsRoot(m.as_root);
        return;
    }
  };

  // ----- send select / lifecycle -----

  const requestStream = (opts?: { asRoot?: boolean }) => {
    const useRoot = opts?.asRoot ?? asRoot();
    setAsRoot(useRoot);
    setLines([]);
    setStatus('running');
    setStatusError('');
    send({
      kind: 'select',
      unit: selected() === SYSTEM_KEY ? '' : selected(),
      priority: priority(),
      range: range(),
      as_root: useRoot,
    });
  };

  const onPickUnit = (name: string) => {
    if (selected() === name) return;
    setSelected(name);
    // Switching unit always retries unprivileged first.
    requestStream({ asRoot: false });
  };

  const onPickRange = (r: 'boot' | 'hour' | 'day' | 'all') => {
    if (range() === r) return;
    setRange(r);
    requestStream();
  };

  const onPickPriority = (p: number) => {
    if (priority() === p) return;
    setPriority(p);
    requestStream();
  };

  const retryAsRoot = () => requestStream({ asRoot: true });
  const refreshUnits = () => send({ kind: 'refresh_units' });

  // ----- scroll handling -----

  // queueScroll defers a scroll-to-bottom to the next animation frame
  // so the DOM has rendered the new rows. Cheaper than after every
  // line; Solid batches the createSignal update anyway.
  let scrollScheduled = false;
  const queueScroll = () => {
    if (scrollScheduled) return;
    scrollScheduled = true;
    requestAnimationFrame(() => {
      scrollScheduled = false;
      if (logEl) logEl.scrollTop = logEl.scrollHeight;
    });
  };

  const onLogScroll = () => {
    if (!logEl) return;
    // Detach follow-mode when the user scrolls up; reattach when they
    // scroll back near the bottom. 32px slop tolerates the row height.
    const nearBottom = logEl.scrollHeight - logEl.scrollTop - logEl.clientHeight < 32;
    if (follow() !== nearBottom) setFollow(nearBottom);
  };

  // ----- derived -----

  const filteredLines = createMemo(() => {
    const f = filterText().trim().toLowerCase();
    const all = lines();
    if (!f) return all;
    return all.filter((l) =>
      l.message.toLowerCase().includes(f) ||
      l.unit.toLowerCase().includes(f) ||
      l.ident.toLowerCase().includes(f) ||
      String(l.pid).includes(f),
    );
  });

  const sortedUnits = createMemo(() => {
    // Active first, then alphabetic. Failed units always pinned to
    // the very top — they're the most likely target.
    return units().slice().sort((a, b) => {
      const score = (u: Unit) => {
        if (u.active === 'failed') return 0;
        if (u.active === 'active') return 1;
        if (u.active === 'activating') return 2;
        return 3;
      };
      const sa = score(a), sb = score(b);
      if (sa !== sb) return sa - sb;
      return a.name.localeCompare(b.name);
    });
  });

  // ----- mount -----

  onMount(() => {
    const onMsg = (ev: Event) => handleBE((ev as CustomEvent).detail);
    props.host.addEventListener('wash:msg', onMsg);
    // Slash key focuses the filter — terminal-app muscle memory.
    const onKey = (ev: KeyboardEvent) => {
      if (ev.key === '/' && document.activeElement !== filterEl) {
        ev.preventDefault();
        filterEl?.focus();
      }
    };
    props.host.addEventListener('keydown', onKey as EventListener);
    onCleanup(() => {
      props.host.removeEventListener('wash:msg', onMsg);
      props.host.removeEventListener('keydown', onKey as EventListener);
    });
  });

  // ----- styles -----

  const layoutStyle = {
    display: 'grid',
    'grid-template-columns': '240px 1fr',
    height: '100%',
    width: '100%',
    background: tokens.bgWindow,
    color: tokens.fg,
    font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
    'box-sizing': 'border-box' as const,
    overflow: 'hidden',
  };

  const sidebarStyle = {
    'border-right': `1px solid ${tokens.borderMenu}`,
    display: 'flex',
    'flex-direction': 'column' as const,
    overflow: 'hidden',
  };

  const sidebarHeadStyle = {
    display: 'flex',
    'align-items': 'center',
    'justify-content': 'space-between',
    padding: `${tokens.spaceSm}px ${tokens.spaceMd}px`,
    'border-bottom': `1px solid ${tokens.borderMenu}`,
    font: `${tokens.fontSizeMd} ${tokens.fontSans}`,
    opacity: 0.8,
  };

  const unitRowStyle = (active: boolean, sel: boolean) => ({
    padding: `4px ${tokens.spaceMd}px`,
    background: sel ? tokens.bgRowSelected : 'transparent',
    cursor: 'pointer',
    'font-size': tokens.fontSizeMd,
    color: active ? tokens.fg : tokens.fgMuted,
    'white-space': 'nowrap' as const,
    'text-overflow': 'ellipsis',
    overflow: 'hidden',
  });

  const mainStyle = {
    display: 'flex',
    'flex-direction': 'column' as const,
    overflow: 'hidden',
  };

  const toolbarStyle = {
    display: 'flex',
    'align-items': 'center',
    gap: `${tokens.spaceMd}px`,
    padding: `${tokens.spaceSm}px ${tokens.spaceMd}px`,
    'border-bottom': `1px solid ${tokens.borderMenu}`,
    'flex-wrap': 'wrap' as const,
  };

  const segGroupStyle = {
    display: 'inline-flex',
    border: `1px solid ${tokens.borderMenu}`,
    'border-radius': `${tokens.radiusSm}px`,
    overflow: 'hidden',
  };

  const segBtn = (sel: boolean) => ({
    background: sel ? tokens.bgRowSelected : 'transparent',
    color: sel ? tokens.fg : tokens.fgMuted,
    border: 'none',
    padding: '3px 8px',
    'font-size': tokens.fontSizeMd,
    cursor: 'pointer',
  });

  const filterWrapStyle = {
    display: 'flex',
    'align-items': 'center',
    gap: '4px',
    'margin-left': 'auto',
    background: tokens.bgMenu,
    border: `1px solid ${tokens.borderMenu}`,
    'border-radius': `${tokens.radiusSm}px`,
    padding: '2px 6px',
  };

  const filterInputStyle = {
    background: 'transparent',
    border: 'none',
    outline: 'none',
    color: tokens.fg,
    font: `${tokens.fontSizeMd} ${tokens.fontMono}`,
    width: '160px',
  };

  const banner = (kind: 'denied' | 'failed') => ({
    display: 'flex',
    'align-items': 'center',
    gap: `${tokens.spaceMd}px`,
    padding: `${tokens.spaceSm}px ${tokens.spaceMd}px`,
    background: kind === 'denied' ? '#3a2a12' : tokens.bgDanger,
    'border-bottom': `1px solid ${kind === 'denied' ? '#7a5a20' : tokens.borderDanger}`,
    color: tokens.fg,
    'font-size': tokens.fontSizeMd,
  });

  const logPaneStyle = {
    flex: 1,
    overflow: 'auto',
    font: `${tokens.fontSizeMd} ${tokens.fontMono}`,
    padding: `${tokens.spaceSm}px 0`,
  };

  const rowStyle = (p: number) => ({
    display: 'grid',
    'grid-template-columns': '90px 110px 1fr',
    gap: `${tokens.spaceMd}px`,
    padding: `1px ${tokens.spaceMd}px`,
    'border-left': `2px solid ${priorityColor(p)}`,
    'white-space': 'pre-wrap' as const,
    'word-break': 'break-word' as const,
  });

  // ----- render -----

  return (
    <div style={layoutStyle} data-testid="journal-root">
      {/* sidebar */}
      <aside style={sidebarStyle} data-testid="journal-sidebar">
        <div style={sidebarHeadStyle}>
          <span>Units</span>
          <button
            onClick={refreshUnits}
            title="Refresh unit list"
            style={{
              background: 'transparent',
              border: 'none',
              color: tokens.fgMuted,
              cursor: 'pointer',
              padding: '2px',
              display: 'flex',
            }}
          >
            <RefreshCw size={13} />
          </button>
        </div>
        <div style={{ overflow: 'auto', flex: 1 }}>
          <div
            data-testid="journal-system-row"
            style={unitRowStyle(true, selected() === SYSTEM_KEY)}
            onClick={() => onPickUnit(SYSTEM_KEY)}
          >
            <span style={{ 'font-weight': 600 }}>System</span>
            <span style={{ 'margin-left': '6px', color: tokens.fgDim, 'font-size': tokens.fontSizeSm }}>
              all units
            </span>
          </div>
          <For each={sortedUnits()}>
            {(u) => {
              const shown = u.name.replace(/\.service$/, '');
              const active = u.active === 'active' || u.active === 'activating';
              const failed = u.active === 'failed';
              return (
                <div
                  data-testid="journal-unit-row"
                  data-unit-name={u.name}
                  style={unitRowStyle(active || failed, selected() === u.name)}
                  onClick={() => onPickUnit(u.name)}
                  title={`${u.name}\n${u.active}/${u.sub}\n${u.description}`}
                >
                  <span style={{ color: failed ? '#ff7a7a' : 'inherit' }}>
                    {failed ? '● ' : ''}
                    {shown}
                  </span>
                </div>
              );
            }}
          </For>
        </div>
      </aside>

      {/* main pane */}
      <section style={mainStyle}>
        {/* toolbar */}
        <div style={toolbarStyle} data-testid="journal-toolbar">
          <div style={segGroupStyle}>
            <button style={segBtn(range() === 'boot')} onClick={() => onPickRange('boot')}>
              boot
            </button>
            <button style={segBtn(range() === 'hour')} onClick={() => onPickRange('hour')}>
              1h
            </button>
            <button style={segBtn(range() === 'day')} onClick={() => onPickRange('day')}>
              1d
            </button>
            <button style={segBtn(range() === 'all')} onClick={() => onPickRange('all')}>
              all
            </button>
          </div>

          <div style={segGroupStyle}>
            <button style={segBtn(priority() === 0)} onClick={() => onPickPriority(0)}>
              all
            </button>
            <button style={segBtn(priority() === 6)} onClick={() => onPickPriority(6)}>
              ≥info
            </button>
            <button style={segBtn(priority() === 4)} onClick={() => onPickPriority(4)}>
              ≥warn
            </button>
            <button style={segBtn(priority() === 3)} onClick={() => onPickPriority(3)}>
              ≥err
            </button>
          </div>

          <Show when={asRoot()}>
            <span
              title="Reading via wash-priv"
              style={{
                color: '#ff8080',
                'font-size': tokens.fontSizeSm,
                display: 'inline-flex',
                'align-items': 'center',
                gap: '3px',
              }}
            >
              <ShieldAlert size={12} />
              root
            </span>
          </Show>

          <span style={{ 'font-size': tokens.fontSizeSm, opacity: 0.6 }}>
            {filteredLines().length === lines().length
              ? `${lines().length} lines`
              : `${filteredLines().length} / ${lines().length} lines`}
            {!follow() ? ' · paused (scroll to bottom to resume)' : ''}
          </span>

          <label style={filterWrapStyle}>
            <Search size={12} />
            <input
              ref={filterEl}
              data-testid="journal-filter"
              type="text"
              placeholder="filter (press /)"
              value={filterText()}
              onInput={(e) => setFilterText(e.currentTarget.value)}
              style={filterInputStyle}
            />
          </label>
        </div>

        {/* status banners */}
        <Show when={status() === 'perm_denied'}>
          <div style={banner('denied')} data-testid="journal-banner-denied">
            <ShieldAlert size={14} />
            <span style={{ flex: 1 }}>
              These logs require root access.{' '}
              <span style={{ opacity: 0.7 }}>{statusError()}</span>
            </span>
            <button
              data-testid="journal-retry-root"
              onClick={retryAsRoot}
              style={{
                background: '#5a3a12',
                color: tokens.fg,
                border: `1px solid #7a5a20`,
                'border-radius': `${tokens.radiusSm}px`,
                padding: '4px 10px',
                cursor: 'pointer',
                'font-size': tokens.fontSizeMd,
              }}
            >
              Read with root
            </button>
          </div>
        </Show>
        <Show when={status() === 'failed'}>
          <div style={banner('failed')}>
            <span style={{ flex: 1 }}>
              journalctl: <span style={{ opacity: 0.85 }}>{statusError() || 'failed'}</span>
            </span>
            <button
              onClick={() => requestStream()}
              style={{
                background: 'transparent',
                color: tokens.fg,
                border: `1px solid ${tokens.borderDanger}`,
                'border-radius': `${tokens.radiusSm}px`,
                padding: '3px 10px',
                cursor: 'pointer',
                'font-size': tokens.fontSizeMd,
              }}
            >
              Retry
            </button>
          </div>
        </Show>

        {/* log pane */}
        <div ref={logEl} data-testid="journal-log" style={logPaneStyle} onScroll={onLogScroll}>
          <For each={filteredLines()}>
            {(l) => (
              <div data-testid="journal-row" style={rowStyle(l.priority)}>
                <span style={{ color: tokens.fgDim }}>{fmtTime(l.ts)}</span>
                <span style={{ color: priorityColor(l.priority), 'white-space': 'nowrap', overflow: 'hidden', 'text-overflow': 'ellipsis' }}>
                  {shortUnit(l.unit, l.ident)}
                </span>
                <span>{l.message}</span>
              </div>
            )}
          </For>
          <Show when={filteredLines().length === 0 && status() === 'running'}>
            <div style={{ padding: tokens.spaceLg + 'px', color: tokens.fgMuted, 'font-style': 'italic' }}>
              waiting for entries…
              <span style={{ 'margin-left': '6px', color: tokens.fgDim }}>
                (prio {priorityLabel(priority())} · {range()})
              </span>
            </div>
          </Show>
        </div>
      </section>
    </div>
  );
};

defineWashApp('wash-app-journal', (props) => <App {...props} />, {
  style: 'display:block; height:100%; width:100%; overflow:hidden;',
});
