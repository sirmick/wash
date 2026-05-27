// wash-app-about: a windowed "about this system" panel.
//
// Three BE-fed blocks plus a browser block:
//
//   • wash header — name, license, refresh
//   • Build — router version/commit/built timestamp, app version (about.info)
//   • Host — hostname, user, workdir, about-app uptime (about.info)
//   • Processes — live table from the router's runtime.query peer:
//     every running wash process with goroutines, heap, RSS, etc.,
//     plus a "Total RSS" footer.
//   • Registered apps — every catalog entry (from window.wash.catalog)
//   • Browser — UA, platform, viewport, capabilities

import { For, Show, createSignal, onCleanup, onMount } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { defineWashApp, tokens } from '@wash/ui';

// ----- wire types -----

interface BuildBlock {
  app_version: string;
  protocol_version: number;
  router_version?: string;
  router_commit?: string;
  router_built?: string;
  router_dev?: boolean;
}

interface HostBlock {
  uid: number;
  username?: string;
  hostname?: string;
  workdir?: string;
  uptime_sec: number;
}

interface AboutInfo {
  kind: 'about.info';
  build: BuildBlock;
  host: HostBlock;
}

interface RuntimeRow {
  instance_id: string;
  app_id?: string;
  name: string;
  pid: number;
  // has_heartbeat is true once at least one runtime.stats frame
  // landed for this instance; false means just-spawned or doesn't
  // ship the heartbeat protocol yet. runtime_kind ("go", "rust", …)
  // tells the FE whether to render the Go-specific columns.
  has_heartbeat?: boolean;
  runtime_kind?: string;
  go_version?: string;
  num_goroutine?: number;
  num_cgo_call?: number;
  heap_alloc?: number;
  heap_inuse?: number;
  heap_objects?: number;
  stack_inuse?: number;
  sys?: number;
  num_gc?: number;
  gc_pause_total_ns?: number;
  uptime_sec?: number;
  rss_bytes?: number;
  vsize_bytes?: number;
  last_seen_age_sec?: number;
}

interface RuntimeTable {
  kind: 'runtime.table';
  rows: RuntimeRow[];
  total_rss: number;
  captured_at_sec: number;
}

type CatalogApp = ReturnType<typeof window.wash.catalog>[number];

// ----- format helpers -----

function fmtBytes(n: number): string {
  if (!n) return '—';
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
}

function fmtUptime(sec: number): string {
  if (sec < 60) return `${sec}s`;
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  if (m < 60) return `${m}m ${s}s`;
  const h = Math.floor(m / 60);
  const mm = m % 60;
  if (h < 24) return `${h}h ${mm}m`;
  const d = Math.floor(h / 24);
  const hh = h % 24;
  return `${d}d ${hh}h`;
}

function formatBuilt(s: string): string {
  const m = s.match(/^(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2})/);
  if (m) return `${m[1]} ${m[2]}`;
  return s;
}

function formatCapturedAt(unixSec: number): string {
  if (!unixSec) return '';
  const d = new Date(unixSec * 1000);
  return d.toLocaleTimeString();
}

// ----- browser info (FE-only) -----

interface BrowserInfo {
  user_agent: string;
  platform: string;
  language: string;
  languages: string;
  hw_concurrency: number;
  device_memory?: number;
  max_touch_points: number;
  online: boolean;
  cookies_enabled: boolean;
  prefers_dark: boolean;
  viewport_w: number;
  viewport_h: number;
  device_pixel_ratio: number;
  timezone: string;
}

function readBrowser(): BrowserInfo {
  const nav: any = navigator;
  return {
    user_agent: navigator.userAgent,
    platform: nav.userAgentData?.platform ?? navigator.platform ?? '',
    language: navigator.language,
    languages: (navigator.languages ?? [navigator.language]).join(', '),
    hw_concurrency: navigator.hardwareConcurrency,
    device_memory: nav.deviceMemory,
    max_touch_points: navigator.maxTouchPoints,
    online: navigator.onLine,
    cookies_enabled: navigator.cookieEnabled,
    prefers_dark: window.matchMedia('(prefers-color-scheme: dark)').matches,
    viewport_w: window.innerWidth,
    viewport_h: window.innerHeight,
    device_pixel_ratio: window.devicePixelRatio,
    timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
  };
}

const SpriteIcon: Component<{ name: string; size: number }> = (props) => (
  <svg
    width={props.size}
    height={props.size}
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    style={{ display: 'block' }}
  >
    <use href={`/icons.svg#${props.name}`} />
  </svg>
);

// ----- app -----

type SortKey = 'name' | 'rss' | 'heap' | 'goroutines' | 'uptime' | 'pid';

const App: Component<{ instance: string; host: HTMLElement }> = (props) => {
  const [info, setInfo] = createSignal<AboutInfo | null>(null);
  const [table, setTable] = createSignal<RuntimeTable | null>(null);
  const [browser, setBrowser] = createSignal<BrowserInfo>(readBrowser());
  const [catalog, setCatalog] = createSignal<CatalogApp[]>([]);
  const [sortKey, setSortKey] = createSignal<SortKey>('rss');
  const [sortDesc, setSortDesc] = createSignal(true);

  const send = (msg: unknown) => window.wash.sendAppMsg(props.instance, msg);

  const handleBE = (m: any) => {
    if (m?.kind === 'about.info') setInfo(m as AboutInfo);
    else if (m?.kind === 'runtime.table') setTable(m as RuntimeTable);
  };

  onMount(() => {
    const onMsg = (ev: Event) => handleBE((ev as CustomEvent).detail);
    props.host.addEventListener('wash:msg', onMsg);
    send({ kind: 'refresh', id: `init-${Date.now()}` });
    const onResize = () => setBrowser(readBrowser());
    window.addEventListener('resize', onResize);
    setCatalog(window.wash.catalog() ?? []);
    const offCatalog = window.wash.onCatalog((apps) => setCatalog(apps ?? []));
    onCleanup(() => {
      props.host.removeEventListener('wash:msg', onMsg);
      window.removeEventListener('resize', onResize);
      offCatalog();
    });
  });

  const refresh = () => {
    setBrowser(readBrowser());
    send({ kind: 'refresh', id: `r-${Date.now()}` });
  };

  const onSort = (k: SortKey) => {
    if (sortKey() === k) setSortDesc(!sortDesc());
    else {
      setSortKey(k);
      // Most columns: descending is the useful default (biggest
      // heap, longest uptime). Name is the obvious exception.
      setSortDesc(k !== 'name');
    }
  };

  return (
    <div style={shellStyle}>
      <Header onRefresh={refresh} />
      <div style={bodyStyle}>
        <Show when={info()} fallback={<div style={loadingStyle}>loading…</div>}>
          <Section title="Build">
            <BuildPanel build={info()!.build} />
          </Section>
          <Section title="Host">
            <HostPanel host={info()!.host} />
          </Section>
        </Show>
        <ProcessSection
          table={table()}
          sortKey={sortKey()}
          sortDesc={sortDesc()}
          onSort={onSort}
        />
        <RegistrySection apps={catalog()} />
        <Section title="Browser">
          <BrowserPanel browser={browser()} />
        </Section>
        <Section title="License">
          <div style={paraStyle}>
            Released under <strong>AGPL-3.0</strong>. Source available at the
            project repository.
          </div>
        </Section>
      </div>
    </div>
  );
};

// ----- header -----

const Header: Component<{ onRefresh: () => void }> = (props) => (
  <div style={headerStyle}>
    <div style={headerIconStyle}>
      <SpriteIcon name="info" size={28} />
    </div>
    <div style={{ flex: 1, 'min-width': 0 }}>
      <div style={headerTitleStyle}>wash</div>
      <div style={headerSubtitleStyle}>Web Application SHell</div>
    </div>
    <button
      type="button"
      onClick={props.onRefresh}
      style={refreshBtnStyle}
      title="Re-read runtime stats"
      data-testid="about-refresh"
    >
      Refresh
    </button>
  </div>
);

// ----- build panel -----

const BuildPanel: Component<{ build: BuildBlock }> = (props) => (
  <KVList>
    <KVRow k="App version" v={`v${props.build.app_version}`} />
    <KVRow k="Protocol" v={`v${props.build.protocol_version}`} />
    <Show when={props.build.router_version}>
      <KVRow
        k="Router"
        v={
          <span>
            v{props.build.router_version}
            <Show when={props.build.router_commit}>
              <span style={mutedStyle}> · {props.build.router_commit}</span>
            </Show>
            <Show when={props.build.router_dev}>
              <span style={devBadgeStyle} data-testid="about-router-dev">DEV</span>
            </Show>
          </span>
        }
      />
    </Show>
    <Show when={props.build.router_built}>
      <KVRow k="Built" v={formatBuilt(props.build.router_built!)} />
    </Show>
  </KVList>
);

// ----- host panel -----

const HostPanel: Component<{ host: HostBlock }> = (props) => (
  <KVList>
    <Show when={props.host.hostname}>
      <KVRow k="Host" v={props.host.hostname!} />
    </Show>
    <Show when={props.host.username}>
      <KVRow k="User" v={`${props.host.username} (UID ${props.host.uid})`} />
    </Show>
    <Show when={props.host.workdir}>
      <KVRow k="Workdir" v={<span style={pathStyle}>{props.host.workdir!}</span>} />
    </Show>
    <KVRow k="About uptime" v={fmtUptime(props.host.uptime_sec)} />
  </KVList>
);

// ----- process table -----

const ProcessSection: Component<{
  table: RuntimeTable | null;
  sortKey: SortKey;
  sortDesc: boolean;
  onSort: (k: SortKey) => void;
}> = (props) => {
  const title = () => {
    const t = props.table;
    if (!t) return 'Processes';
    const rss = fmtBytes(t.total_rss);
    return `Processes (${t.rows.length}) · total RSS ${rss}`;
  };
  return (
    <Section title={title()}>
      <Show when={props.table} fallback={<div style={loadingStyle}>waiting for router…</div>}>
        <ProcessTable
          rows={props.table!.rows}
          sortKey={props.sortKey}
          sortDesc={props.sortDesc}
          onSort={props.onSort}
        />
        <div style={tableFooterStyle}>
          <span>as of {formatCapturedAt(props.table!.captured_at_sec)}</span>
        </div>
      </Show>
    </Section>
  );
};

// num coerces an optional metric to 0 for comparison purposes. The
// table renders "—" for undefined, but sort needs a real number.
const num = (v: number | undefined) => v ?? 0;

function sortedRows(rows: RuntimeRow[], key: SortKey, desc: boolean): RuntimeRow[] {
  // Router row is always pinned at the top regardless of sort —
  // mirroring htop's "kernel threads first" convention.
  const router = rows.find((r) => r.instance_id === 'router');
  const others = rows.filter((r) => r.instance_id !== 'router');
  const dir = desc ? -1 : 1;
  others.sort((a, b) => {
    switch (key) {
      case 'name': return a.name.localeCompare(b.name) * dir;
      case 'rss': return (num(a.rss_bytes) - num(b.rss_bytes)) * dir;
      case 'heap': return (num(a.heap_alloc) - num(b.heap_alloc)) * dir;
      case 'goroutines': return (num(a.num_goroutine) - num(b.num_goroutine)) * dir;
      case 'uptime': return (num(a.uptime_sec) - num(b.uptime_sec)) * dir;
      case 'pid': return (a.pid - b.pid) * dir;
    }
  });
  return router ? [router, ...others] : others;
}

const ProcessTable: Component<{
  rows: RuntimeRow[];
  sortKey: SortKey;
  sortDesc: boolean;
  onSort: (k: SortKey) => void;
}> = (props) => (
  <div style={tableScrollStyle}>
    <table style={tableStyle} data-testid="about-process-table">
      <thead>
        <tr style={tableHeadRowStyle}>
          <SortableTh k="name" label="Process" current={props.sortKey} desc={props.sortDesc} onSort={props.onSort} align="left" />
          <SortableTh k="pid" label="PID" current={props.sortKey} desc={props.sortDesc} onSort={props.onSort} align="right" />
          <SortableTh k="rss" label="RSS" current={props.sortKey} desc={props.sortDesc} onSort={props.onSort} align="right" />
          <SortableTh k="heap" label="Heap" current={props.sortKey} desc={props.sortDesc} onSort={props.onSort} align="right" />
          <SortableTh k="goroutines" label="Gor." current={props.sortKey} desc={props.sortDesc} onSort={props.onSort} align="right" />
          <th style={{ ...thStyle, 'text-align': 'right' }}>GC</th>
          <SortableTh k="uptime" label="Up" current={props.sortKey} desc={props.sortDesc} onSort={props.onSort} align="right" />
          <th style={{ ...thStyle, 'text-align': 'right' }}>Last</th>
        </tr>
      </thead>
      <tbody>
        <For each={sortedRows(props.rows, props.sortKey, props.sortDesc)}>
          {(r) => <ProcessRow row={r} />}
        </For>
      </tbody>
    </table>
  </div>
);

const SortableTh: Component<{
  k: SortKey;
  label: string;
  current: SortKey;
  desc: boolean;
  onSort: (k: SortKey) => void;
  align: 'left' | 'right';
}> = (props) => {
  const active = () => props.current === props.k;
  return (
    <th
      style={{
        ...thStyle,
        'text-align': props.align,
        cursor: 'pointer',
        background: active() ? tokens.bgRowSelected : 'transparent',
      }}
      onClick={() => props.onSort(props.k)}
      data-testid={`about-sort-${props.k}`}
    >
      {props.label}{active() ? (props.desc ? ' ↓' : ' ↑') : ''}
    </th>
  );
};

// goCell renders a Go-runtime metric, or "—" when the row's BE
// doesn't report Go stats. Discriminator is runtime_kind, not
// "field is undefined": JSON omitempty collapses 0-valued counters
// (NumGC for a just-started process, NumGoroutine for the rare
// goroutineless app) with truly-missing fields. For Go rows we
// treat undefined as 0; for non-Go rows we dash regardless of the
// value's presence.
function goCell(row: RuntimeRow, v: number | undefined, format: (n: number) => string): string {
  if (!row.has_heartbeat || row.runtime_kind !== 'go') return '—';
  return format(v ?? 0);
}

// lastSeenCell renders the heartbeat-age column. "now" for the
// router (always fresh), "—" when no heartbeat ever arrived (just-
// spawned or non-protocol app), otherwise "Ns".
function lastSeenCell(row: RuntimeRow): string {
  if (row.instance_id === 'router') return 'now';
  if (!row.has_heartbeat) return '—';
  return `${row.last_seen_age_sec ?? 0}s`;
}

const ProcessRow: Component<{ row: RuntimeRow }> = (props) => {
  const r = props.row;
  const isRouter = r.instance_id === 'router';
  // Dim a row whose heartbeat is stale (> 2 intervals); for the
  // router self-row this is always 0 so no dimming. Apps without
  // heartbeats are NOT dimmed — they may be a healthy non-Go BE,
  // we just don't have Go metrics for them.
  const age = r.last_seen_age_sec ?? 0;
  const stale = !isRouter && r.has_heartbeat && age > 12;
  return (
    <tr style={{ ...tdRowStyle, opacity: stale ? 0.55 : 1 }} data-testid={`about-row-${r.instance_id}`}>
      <td style={{ ...tdStyle, 'font-weight': isRouter ? 600 : 400 }}>
        {r.name}
        <Show when={r.app_id}>
          <div style={appIDInRowStyle}>{r.app_id}</div>
        </Show>
      </td>
      <td style={{ ...tdStyle, 'text-align': 'right' }}>{r.pid || '—'}</td>
      <td style={{ ...tdStyle, 'text-align': 'right' }}>{r.rss_bytes && r.rss_bytes > 0 ? fmtBytes(r.rss_bytes) : '—'}</td>
      <td style={{ ...tdStyle, 'text-align': 'right' }}>{goCell(r, r.heap_alloc, fmtBytes)}</td>
      <td style={{ ...tdStyle, 'text-align': 'right' }}>{goCell(r, r.num_goroutine, (n) => String(n))}</td>
      <td style={{ ...tdStyle, 'text-align': 'right' }}>{goCell(r, r.num_gc, (n) => String(n))}</td>
      <td style={{ ...tdStyle, 'text-align': 'right' }}>
        {r.has_heartbeat ? fmtUptime(r.uptime_sec ?? 0) : '—'}
      </td>
      <td style={{ ...tdStyle, 'text-align': 'right', opacity: 0.7 }}>{lastSeenCell(r)}</td>
    </tr>
  );
};

// ----- registered apps -----

const RegistrySection: Component<{ apps: CatalogApp[] | undefined }> = (props) => {
  const list = () => (Array.isArray(props.apps) ? props.apps : []);
  return (
    <Section title={`Registered apps (${list().length})`}>
      <div style={appsGridStyle} data-testid="about-apps">
        <For each={list()}>{(app) => <AppCard app={app} />}</For>
      </div>
    </Section>
  );
};

const AppCard: Component<{ app: CatalogApp }> = (props) => {
  const a = props.app;
  return (
    <div style={appCardStyle} data-testid={`about-app-${a.id}`}>
      <div style={appCardHeaderStyle}>
        <span style={appIconStyle}>
          <Show when={a.icon} fallback={<span style={{ opacity: 0.3 }}>·</span>}>
            <SpriteIcon name={a.icon!} size={18} />
          </Show>
        </span>
        <span style={appNameStyle}>{a.name}</span>
      </div>
      <div style={appIDStyle}>{a.id}</div>
      <div style={appMetaStyle}>
        <span>{a.surface}</span>
        <span style={dotStyle}>·</span>
        <span>{a.instancing}</span>
        <Show when={a.disabled}>
          <span style={dotStyle}>·</span>
          <span style={{ ...tagStyle, color: '#c8a04a' }} title={a.reason}>disabled</span>
        </Show>
        <Show when={a.root_variant}>
          <span style={dotStyle}>·</span>
          <span style={{ ...tagStyle, color: '#d97757' }}>root variant</span>
        </Show>
      </div>
    </div>
  );
};

// ----- browser -----

const BrowserPanel: Component<{ browser: BrowserInfo }> = (props) => {
  const b = () => props.browser;
  return (
    <KVList>
      <KVRow k="User agent" v={<span style={uaStyle}>{b().user_agent}</span>} />
      <Show when={b().platform}>
        <KVRow k="Platform" v={b().platform} />
      </Show>
      <KVRow k="Language" v={b().languages || b().language} />
      <KVRow k="Timezone" v={b().timezone} />
      <KVRow k="Concurrency" v={`${b().hw_concurrency} logical cores`} />
      <Show when={b().device_memory !== undefined}>
        <KVRow k="Device memory" v={`${b().device_memory} GB`} />
      </Show>
      <KVRow
        k="Viewport"
        v={`${b().viewport_w}×${b().viewport_h} @ ${b().device_pixel_ratio}× DPR`}
      />
      <KVRow
        k="Capabilities"
        v={
          <span>
            <span style={tagStyle}>{b().online ? 'online' : 'offline'}</span>
            <span style={dotStyle}>·</span>
            <span style={tagStyle}>{b().cookies_enabled ? 'cookies' : 'no cookies'}</span>
            <span style={dotStyle}>·</span>
            <span style={tagStyle}>{b().prefers_dark ? 'dark' : 'light'}</span>
            <Show when={b().max_touch_points > 0}>
              <span style={dotStyle}>·</span>
              <span style={tagStyle}>touch ({b().max_touch_points})</span>
            </Show>
          </span>
        }
      />
    </KVList>
  );
};

// ----- shared pieces -----

const Section: Component<{ title: string; children: JSX.Element }> = (props) => (
  <section style={sectionStyle}>
    <h3 style={sectionTitleStyle}>{props.title}</h3>
    {props.children}
  </section>
);

const KVList: Component<{ children: JSX.Element }> = (props) => (
  <div style={kvListStyle}>{props.children}</div>
);

const KVRow: Component<{ k: string; v: JSX.Element | string | number }> = (props) => (
  <div style={kvRowStyle}>
    <div style={kvKeyStyle}>{props.k}</div>
    <div style={kvValStyle}>{props.v}</div>
  </div>
);

// ----- styles -----

const shellStyle: JSX.CSSProperties = {
  display: 'grid',
  'grid-template-rows': 'auto 1fr',
  height: '100%',
  background: tokens.bgWindow,
  color: tokens.fg,
  font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
  overflow: 'hidden',
};

const headerStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  gap: '12px',
  padding: '14px 18px',
  'border-bottom': `1px solid ${tokens.borderMenu}`,
  background: tokens.bgMenu,
};

const headerIconStyle: JSX.CSSProperties = {
  width: '36px',
  height: '36px',
  display: 'flex',
  'align-items': 'center',
  'justify-content': 'center',
  color: tokens.fg,
  opacity: 0.85,
};

const headerTitleStyle: JSX.CSSProperties = {
  font: `600 18px ${tokens.fontSans}`,
  'line-height': 1.1,
};

const headerSubtitleStyle: JSX.CSSProperties = {
  font: `${tokens.fontSizeMd} ${tokens.fontSans}`,
  color: tokens.fgMuted,
  'margin-top': '2px',
};

const refreshBtnStyle: JSX.CSSProperties = {
  background: 'transparent',
  color: tokens.fg,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusSm}px`,
  padding: '4px 10px',
  font: `${tokens.fontSizeMd} ${tokens.fontSans}`,
  cursor: 'pointer',
};

const bodyStyle: JSX.CSSProperties = {
  overflow: 'auto',
  padding: '14px 18px 20px',
};

const loadingStyle: JSX.CSSProperties = {
  padding: '8px 0',
  color: tokens.fgMuted,
  font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
};

const sectionStyle: JSX.CSSProperties = {
  'margin-bottom': '18px',
};

const sectionTitleStyle: JSX.CSSProperties = {
  font: `600 ${tokens.fontSizeBase} ${tokens.fontSans}`,
  color: tokens.fg,
  margin: '0 0 8px',
  'padding-bottom': '4px',
  'border-bottom': `1px solid ${tokens.borderMenu}`,
  'text-transform': 'uppercase',
  'letter-spacing': '0.06em',
  'font-size': '11px',
  opacity: 0.7,
};

const kvListStyle: JSX.CSSProperties = {
  display: 'grid',
  gap: '4px 0',
};

const kvRowStyle: JSX.CSSProperties = {
  display: 'grid',
  'grid-template-columns': '120px 1fr',
  gap: '12px',
  'align-items': 'baseline',
  font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
};

const kvKeyStyle: JSX.CSSProperties = {
  color: tokens.fgMuted,
  font: `${tokens.fontSizeMd} ${tokens.fontSans}`,
};

const kvValStyle: JSX.CSSProperties = {
  font: `${tokens.fontSizeBase} ${tokens.fontMono}`,
  'word-break': 'break-word',
  'min-width': 0,
};

const pathStyle: JSX.CSSProperties = {
  font: `${tokens.fontSizeMd} ${tokens.fontMono}`,
  opacity: 0.85,
};

const uaStyle: JSX.CSSProperties = {
  font: `${tokens.fontSizeSm} ${tokens.fontMono}`,
  opacity: 0.85,
  'word-break': 'break-all',
};

const mutedStyle: JSX.CSSProperties = {
  color: tokens.fgMuted,
};

const devBadgeStyle: JSX.CSSProperties = {
  background: '#a04040',
  color: '#fff',
  padding: '0 6px',
  'border-radius': '2px',
  'font-weight': 700,
  'letter-spacing': '0.05em',
  'margin-left': '8px',
  font: `${tokens.fontSizeSm} ${tokens.fontSans}`,
};

const tableScrollStyle: JSX.CSSProperties = {
  overflow: 'auto',
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusSm}px`,
};

const tableStyle: JSX.CSSProperties = {
  width: '100%',
  'border-collapse': 'collapse',
  font: `${tokens.fontSizeMd} ${tokens.fontMono}`,
};

const tableHeadRowStyle: JSX.CSSProperties = {
  background: tokens.bgMenu,
  'border-bottom': `1px solid ${tokens.borderMenu}`,
};

const thStyle: JSX.CSSProperties = {
  font: `${tokens.fontSizeSm} ${tokens.fontSans}`,
  color: tokens.fgMuted,
  padding: '6px 10px',
  'text-align': 'left',
  'white-space': 'nowrap',
  'user-select': 'none',
};

const tdRowStyle: JSX.CSSProperties = {
  'border-bottom': `1px solid ${tokens.borderMenu}`,
};

const tdStyle: JSX.CSSProperties = {
  padding: '6px 10px',
  'vertical-align': 'top',
  'white-space': 'nowrap',
};

const appIDInRowStyle: JSX.CSSProperties = {
  font: `${tokens.fontSizeSm} ${tokens.fontMono}`,
  color: tokens.fgMuted,
  opacity: 0.7,
  'margin-top': '1px',
};

const tableFooterStyle: JSX.CSSProperties = {
  display: 'flex',
  'justify-content': 'flex-end',
  'padding-top': '4px',
  font: `${tokens.fontSizeSm} ${tokens.fontMono}`,
  color: tokens.fgMuted,
};

const appsGridStyle: JSX.CSSProperties = {
  display: 'grid',
  'grid-template-columns': 'repeat(auto-fill, minmax(220px, 1fr))',
  gap: '8px',
};

const appCardStyle: JSX.CSSProperties = {
  display: 'flex',
  'flex-direction': 'column',
  gap: '4px',
  padding: '8px 10px',
  background: tokens.bgMenu,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusSm}px`,
  'min-width': 0,
};

const appCardHeaderStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  gap: '6px',
  'min-width': 0,
};

const appIconStyle: JSX.CSSProperties = {
  width: '18px',
  height: '18px',
  display: 'flex',
  'align-items': 'center',
  'justify-content': 'center',
  color: tokens.fg,
  opacity: 0.85,
  flex: '0 0 auto',
};

const appNameStyle: JSX.CSSProperties = {
  font: `600 ${tokens.fontSizeBase} ${tokens.fontSans}`,
  flex: 1,
  overflow: 'hidden',
  'text-overflow': 'ellipsis',
  'white-space': 'nowrap',
};

const appIDStyle: JSX.CSSProperties = {
  font: `${tokens.fontSizeSm} ${tokens.fontMono}`,
  color: tokens.fgMuted,
  opacity: 0.85,
  overflow: 'hidden',
  'text-overflow': 'ellipsis',
  'white-space': 'nowrap',
};

const appMetaStyle: JSX.CSSProperties = {
  display: 'flex',
  'flex-wrap': 'wrap',
  'align-items': 'center',
  gap: '4px',
  font: `${tokens.fontSizeSm} ${tokens.fontMono}`,
  color: tokens.fgMuted,
};

const dotStyle: JSX.CSSProperties = {
  opacity: 0.5,
};

const tagStyle: JSX.CSSProperties = {
  font: `${tokens.fontSizeSm} ${tokens.fontMono}`,
  color: tokens.fg,
  opacity: 0.7,
};

const paraStyle: JSX.CSSProperties = {
  font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
  color: tokens.fg,
  opacity: 0.85,
  'line-height': 1.5,
};

defineWashApp('wash-app-about', (props) => <App {...props} />, {
  style: `display:block;width:100%;height:100%;overflow:hidden;background:${tokens.bgWindow};color:${tokens.fg};font:${tokens.fontSizeBase} ${tokens.fontSans};box-sizing:border-box`,
});
