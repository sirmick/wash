// AboutWidget renders ambient host status in the sidebar: live CPU%
// and memory% bars plus a one-line identity row. Data sources:
//
//   - system.info (one-shot push from session BE on connect; cached
//     by the parent App and passed in as `info`).
//   - host.stats (5s ticker push from session BE; the parent App
//     receives it via wash:msg and passes the latest as `stats`).
//
// No own subscription wiring — the widget is a pure renderer so the
// App can decide where to source the data from. Makes testing
// straightforward (mock the props).

import type { Component, JSX } from 'solid-js';
import { Show } from 'solid-js';
import { tokens } from '@wash/ui';

export interface AboutSystemInfo {
  hostname?: string;
  fqdn?: string;
  username?: string;
  cpus?: number;
  mem_bytes?: number;
}

export interface AboutHostStats {
  cpu_pct: number;
  mem_used: number;
  mem_total: number;
  mem_pct: number;
  captured_at_sec: number;
}

export interface AboutWidgetProps {
  info: () => AboutSystemInfo | null;
  stats: () => AboutHostStats | null;
}

export const AboutWidget: Component<AboutWidgetProps> = (props) => {
  return (
    <div
      data-testid="about-widget"
      style={{ display: 'flex', 'flex-direction': 'column', gap: '10px' }}
    >
      <IdentityRow info={props.info} />
      <Bar
        label="CPU"
        testid="about-cpu-bar"
        // host.stats arrives every 5s; first tick may not have landed
        // yet on a fresh mount. Render a flat 0% bar so the layout
        // doesn't jump when data first arrives.
        percent={() => props.stats()?.cpu_pct ?? 0}
        valueLabel={() => `${(props.stats()?.cpu_pct ?? 0).toFixed(1)}%`}
        accent={() => percentAccent(props.stats()?.cpu_pct ?? 0)}
      />
      <Bar
        label="MEM"
        testid="about-mem-bar"
        percent={() => props.stats()?.mem_pct ?? 0}
        valueLabel={() => memLabel(props.stats())}
        accent={() => percentAccent(props.stats()?.mem_pct ?? 0)}
      />
    </div>
  );
};

const IdentityRow: Component<{ info: () => AboutSystemInfo | null }> = (props) => {
  // Hardware (cores + total mem) shows in the desktop banner already
  // and the MEM bar carries the runtime view; the identity row stays
  // host + user only so the widget reads at a glance.
  const host = () => {
    const i = props.info();
    if (!i) return '—';
    return i.fqdn || i.hostname || '?';
  };
  const user = () => props.info()?.username ?? '?';
  return (
    <div
      data-testid="about-identity"
      style={{
        font: `11px ${tokens.fontMono}`,
        opacity: 0.7,
        'line-height': 1.4,
      }}
    >
      <div data-testid="about-host" style={{ 'font-weight': 600, color: tokens.fg }}>{host()}</div>
      <div data-testid="about-user">{user()}</div>
    </div>
  );
};

/** percentAccent returns the bar fill colour for a given utilisation
 *  percentage. Green-ish at low/mid load, amber past 70%, red past 90%.
 *  Matches the visual language htop uses for ambient monitoring. */
function percentAccent(pct: number): string {
  if (pct >= 90) return tokens.accentRed;
  if (pct >= 70) return tokens.accentAmber;
  return tokens.accentBlue;
}

function memLabel(s: AboutHostStats | null): string {
  if (!s) return '—';
  return `${(s.mem_used / 1024 / 1024 / 1024).toFixed(1)} / ${(s.mem_total / 1024 / 1024 / 1024).toFixed(1)} GB`;
}

const Bar: Component<{
  label: string;
  testid: string;
  percent: () => number;
  valueLabel: () => string;
  accent: () => string;
}> = (props) => {
  // Clamp visible width to [0, 100] — the BE could in principle ship
  // a >100 value under exotic conditions (cgroup re-accounting, NMI
  // jiffies) and we don't want the bar to overflow.
  const widthPct = () => Math.max(0, Math.min(100, props.percent()));
  const labelRow: JSX.CSSProperties = {
    display: 'flex',
    'align-items': 'baseline',
    'justify-content': 'space-between',
    'font-size': '11px',
    'margin-bottom': '3px',
  };
  return (
    <div data-testid={props.testid} data-percent={widthPct().toFixed(1)}>
      <div style={labelRow}>
        <span style={{ opacity: 0.65, 'letter-spacing': '0.05em' }}>{props.label}</span>
        <span style={{ 'font-variant-numeric': 'tabular-nums', color: tokens.fg }}>{props.valueLabel()}</span>
      </div>
      <div
        style={{
          height: '6px',
          background: tokens.bgRowHover,
          'border-radius': tokens.radiusSm,
          overflow: 'hidden',
        }}
      >
        <div
          style={{
            width: `${widthPct()}%`,
            height: '100%',
            background: props.accent(),
            transition: 'width 0.4s ease-out, background 0.2s',
          }}
        />
      </div>
    </div>
  );
};
