// wash-app-syslogs: tail any file under /var/log/.
//
// Same general layout as wash-app-journal — sidebar + log pane —
// because the deeper structures (auto-follow, priority colouring,
// filter input, perm-denied banner) overlap. Kept as a separate
// component for now: when a third tailing app appears we can extract
// the shared parts into @wash/ui as a custom element.
//
// Differences vs journal:
//   - sidebar shows file paths + size/mtime, not units
//   - no priority filter button (syslog files are mostly unstructured)
//   - selecting a file is required (no auto-stream on launch)
//   - perm-denied is rare in the user-visible state: the BE
//     auto-retries via wash-priv when the unprivileged tail fails,
//     so the user only sees the banner if priv itself was rejected

import { For, Show, createMemo, createSignal, onCleanup, onMount } from 'solid-js';
import type { Component } from 'solid-js';
import { defineWashApp, severityColor, tokens } from '@wash/ui';
import { RefreshCw, ShieldAlert, Search, FileText } from 'lucide-solid';

interface LogFile {
  path: string;
  name: string;
  size: number;
  mtime: number; // unix seconds
}

interface LogEntry {
  ts: number;        // µs since epoch (0 if unparsed)
  priority: number;  // -1 unknown; 0..7 RFC
  ident: string;
  pid: number;
  message: string;
  raw: string;
}

type StreamStatus = 'idle' | 'running' | 'perm_denied' | 'failed' | 'closed';

const MAX_LINES = 10_000;

// Severity → line color comes from @wash/ui's shared severityColor so
// syslogs and journal can't drift apart.

function fmtTime(ts: number): string {
  if (!ts) return '            ';
  const d = new Date(ts / 1000);
  const hh = String(d.getHours()).padStart(2, '0');
  const mm = String(d.getMinutes()).padStart(2, '0');
  const ss = String(d.getSeconds()).padStart(2, '0');
  const ms = String(d.getMilliseconds()).padStart(3, '0');
  return `${hh}:${mm}:${ss}.${ms}`;
}

function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} K`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} M`;
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} G`;
}

function fmtRelTime(mtime: number): string {
  if (!mtime) return '';
  const now = Date.now() / 1000;
  const diff = now - mtime;
  if (diff < 60) return `${Math.round(diff)}s`;
  if (diff < 3600) return `${Math.round(diff / 60)}m`;
  if (diff < 86400) return `${Math.round(diff / 3600)}h`;
  return `${Math.round(diff / 86400)}d`;
}

const App: Component<{ instance: string; host: HTMLElement }> = (props) => {
  const send = (msg: unknown) => window.wash.sendAppMsg(props.instance, msg);

  // ----- state -----

  const [files, setFiles] = createSignal<LogFile[]>([]);
  const [selected, setSelected] = createSignal<string>('');
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
      case 'files':
        setFiles(Array.isArray(m.files) ? (m.files as LogFile[]) : []);
        return;
      case 'lines': {
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
        if (typeof m.as_root === 'boolean') setAsRoot(m.as_root);
        return;
    }
  };

  // ----- send select / lifecycle -----

  const requestStream = (path: string, opts?: { asRoot?: boolean }) => {
    if (!path) return;
    const useRoot = opts?.asRoot ?? asRoot();
    setAsRoot(useRoot);
    setLines([]);
    setStatus('running');
    setStatusError('');
    send({ kind: 'select', path, as_root: useRoot });
  };

  const onPickFile = (path: string) => {
    if (selected() === path) return;
    setSelected(path);
    requestStream(path, { asRoot: false });
  };

  const retryAsRoot = () => requestStream(selected(), { asRoot: true });
  const refreshFiles = () => send({ kind: 'refresh_files' });

  // ----- scroll handling -----

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
      l.ident.toLowerCase().includes(f) ||
      String(l.pid).includes(f),
    );
  });

  const sortedFiles = createMemo(() => {
    // Files arrive mtime-desc from the BE; respect that order.
    return files();
  });

  // ----- mount -----

  onMount(() => {
    const onMsg = (ev: Event) => handleBE((ev as CustomEvent).detail);
    props.host.addEventListener('wash:msg', onMsg);
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
    'grid-template-columns': '260px 1fr',
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

  const fileRowStyle = (sel: boolean) => ({
    padding: `4px ${tokens.spaceMd}px`,
    background: sel ? tokens.bgRowSelected : 'transparent',
    cursor: 'pointer',
    'font-size': tokens.fontSizeMd,
    'white-space': 'nowrap' as const,
    'text-overflow': 'ellipsis',
    overflow: 'hidden',
    display: 'flex',
    'align-items': 'baseline',
    gap: '6px',
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

  const filterWrapStyle = {
    display: 'flex',
    'align-items': 'center',
    gap: '4px',
    'margin-left': 'auto',
    background: tokens.bgMenu,
    border: `1px solid ${tokens.borderMenu}`,
    'border-radius': `${tokens.radiusSm}`,
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
    background: kind === 'denied' ? tokens.bgDenied : tokens.bgDanger,
    'border-bottom': `1px solid ${kind === 'denied' ? tokens.borderDenied : tokens.borderDanger}`,
    color: tokens.fg,
    'font-size': tokens.fontSizeMd,
  });

  const logPaneStyle = {
    flex: 1,
    overflow: 'auto',
    font: `${tokens.fontSizeMd} ${tokens.fontMono}`,
    padding: `${tokens.spaceSm}px 0`,
  };

  // Structured rows (time + ident + message) get a 3-column grid.
  // Unstructured rows (parser couldn't extract any prefix — e.g. apt's
  // "Get:1 file:/cdrom/..." lines) render flat so the message gets
  // the full pane width instead of 200px of dead left-margin.
  const rowStyle = (p: number) => ({
    display: 'grid',
    'grid-template-columns': '90px 110px 1fr',
    gap: `${tokens.spaceMd}px`,
    padding: `1px ${tokens.spaceMd}px`,
    'border-left': `2px solid ${severityColor(p)}`,
    'white-space': 'pre-wrap' as const,
    'word-break': 'break-word' as const,
  });

  const flatRowStyle = (p: number) => ({
    display: 'block',
    padding: `1px ${tokens.spaceMd}px 1px ${tokens.spaceLg}px`,
    'border-left': `2px solid ${severityColor(p)}`,
    'white-space': 'pre-wrap' as const,
    'word-break': 'break-word' as const,
  });

  const isStructured = (l: LogEntry) => l.ts > 0 || l.ident !== '';

  // ----- render -----

  return (
    <div style={layoutStyle} data-testid="syslogs-root">
      {/* sidebar */}
      <aside style={sidebarStyle} data-testid="syslogs-sidebar">
        <div style={sidebarHeadStyle}>
          <span>/var/log</span>
          <button
            onClick={refreshFiles}
            title="Refresh file list"
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
          <Show
            when={sortedFiles().length > 0}
            fallback={
              <div style={{ padding: tokens.spaceLg + 'px', color: tokens.fgMuted, 'font-style': 'italic', 'font-size': tokens.fontSizeMd }}>
                no readable files in /var/log — click Refresh, or pick a file that requires root (you'll be prompted)
              </div>
            }
          >
            <For each={sortedFiles()}>
              {(f) => (
                <div
                  data-testid="syslogs-file-row"
                  data-file-path={f.path}
                  style={fileRowStyle(selected() === f.path)}
                  onClick={() => onPickFile(f.path)}
                  title={`${f.path}\nsize ${fmtBytes(f.size)} · modified ${fmtRelTime(f.mtime)} ago`}
                >
                  <FileText size={11} style={{ 'flex-shrink': 0, opacity: 0.6 }} />
                  <span style={{ flex: 1, overflow: 'hidden', 'text-overflow': 'ellipsis' }}>{f.name}</span>
                  <span style={{ color: tokens.fgDim, 'font-size': tokens.fontSizeSm }}>
                    {fmtRelTime(f.mtime)}
                  </span>
                </div>
              )}
            </For>
          </Show>
        </div>
      </aside>

      {/* main pane */}
      <section style={mainStyle}>
        <div style={toolbarStyle} data-testid="syslogs-toolbar">
          <span style={{ 'font-size': tokens.fontSizeMd, color: tokens.fgMuted }}>
            {selected() || '(select a file)'}
          </span>

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
              data-testid="syslogs-filter"
              type="text"
              placeholder="filter (press /)"
              value={filterText()}
              onInput={(e) => setFilterText(e.currentTarget.value)}
              style={filterInputStyle}
            />
          </label>
        </div>

        <Show when={status() === 'perm_denied'}>
          <div style={banner('denied')} data-testid="syslogs-banner-denied">
            <ShieldAlert size={14} />
            <span style={{ flex: 1 }}>
              This file requires root access.{' '}
              <span style={{ opacity: 0.7 }}>{statusError()}</span>
            </span>
            <button
              data-testid="syslogs-retry-root"
              onClick={retryAsRoot}
              style={{
                background: '#5a3a12',
                color: tokens.fg,
                border: `1px solid ${tokens.borderDenied}`,
                'border-radius': `${tokens.radiusSm}`,
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
              tail: <span style={{ opacity: 0.85 }}>{statusError() || 'failed'}</span>
            </span>
            <button
              onClick={() => requestStream(selected())}
              style={{
                background: 'transparent',
                color: tokens.fg,
                border: `1px solid ${tokens.borderDanger}`,
                'border-radius': `${tokens.radiusSm}`,
                padding: '3px 10px',
                cursor: 'pointer',
                'font-size': tokens.fontSizeMd,
              }}
            >
              Retry
            </button>
          </div>
        </Show>

        <div ref={logEl} data-testid="syslogs-log" style={logPaneStyle} onScroll={onLogScroll}>
          <For each={filteredLines()}>
            {(l) => {
              if (!isStructured(l)) {
                return (
                  <div data-testid="syslogs-row" data-flat="true" style={flatRowStyle(l.priority)}>
                    {l.message || l.raw}
                  </div>
                );
              }
              return (
                <div data-testid="syslogs-row" style={rowStyle(l.priority)}>
                  <span style={{ color: tokens.fgDim }}>{fmtTime(l.ts)}</span>
                  <span
                    style={{
                      color: severityColor(l.priority),
                      'white-space': 'nowrap',
                      overflow: 'hidden',
                      'text-overflow': 'ellipsis',
                    }}
                  >
                    {l.ident || ''}
                  </span>
                  <span>{l.message || l.raw}</span>
                </div>
              );
            }}
          </For>
          <Show when={filteredLines().length === 0 && status() === 'idle'}>
            <div style={{ padding: tokens.spaceLg + 'px', color: tokens.fgMuted, 'font-style': 'italic' }}>
              pick a file in the sidebar to start tailing
            </div>
          </Show>
          <Show when={filteredLines().length === 0 && status() === 'running'}>
            <div style={{ padding: tokens.spaceLg + 'px', color: tokens.fgMuted, 'font-style': 'italic' }}>
              waiting for entries…
            </div>
          </Show>
        </div>
      </section>
    </div>
  );
};

defineWashApp('wash-app-syslogs', (props) => <App {...props} />, {
  style: 'display:block; height:100%; width:100%; overflow:hidden;',
});
