// LinkWidget renders WS link-health in the sidebar (docs/QOS.md): a
// green/amber/red status dot, the session bandwidth totals (↓ router→
// browser, ↑ browser→router) in MB, live throughput, uptime, and
// drop/reconnect counts when non-zero.
//
// Pure renderer — the parent App sources WashLinkHealth from
// window.wash.onLinkStats and passes it in (matches the AboutWidget
// pattern). WashLinkHealth is the ambient type from @wash/ui's
// window-wash.d.ts.

import type { Component } from 'solid-js';
import { Show } from 'solid-js';
import { tokens } from '@wash/ui';

const sumArr = (a: number[] | undefined): number => (a ? a.reduce((x, y) => x + y, 0) : 0);

// Session running totals are shown in MB.
const fmtMB = (n: number): string => (n / 1e6).toFixed(n >= 1e8 ? 0 : 1) + ' MB';

const fmtRate = (bps: number): string => {
  if (bps >= 1e6) return (bps / 1e6).toFixed(1) + ' MB/s';
  if (bps >= 1e3) return (bps / 1e3).toFixed(0) + ' kB/s';
  return Math.round(bps) + ' B/s';
};

const fmtUptime = (ms: number): string => {
  const s = Math.floor(ms / 1000);
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
};

// Green / amber / red health dot. ok = nominal; warn = link straining
// (send buffer high, or queue/credit stalls climbing); bad = frames
// actually dropping. Derived shell-side in linkstats.ts.
const LINK_DOT: Record<'ok' | 'warn' | 'bad', string> = {
  ok: '#46d39a',
  warn: '#e6b450',
  bad: '#e0606d',
};
const LINK_LABEL: Record<'ok' | 'warn' | 'bad', string> = {
  ok: 'healthy',
  warn: 'strained',
  bad: 'dropping',
};

export interface LinkWidgetProps {
  health: () => WashLinkHealth | null;
}

export const LinkWidget: Component<LinkWidgetProps> = (props) => (
  // Sits below the CPU/MEM bars in the About section; a faint top rule
  // separates the link/bandwidth block from the host-load block.
  <div style={{ 'border-top': '1px solid rgba(255,255,255,0.08)', 'padding-top': '9px', 'margin-top': '2px' }}>
  <Show
    when={props.health()}
    fallback={
      <div style={{ font: tokens.type.textSm, color: tokens.fg, opacity: 0.55 }}>
        waiting for telemetry…
      </div>
    }
  >
    {(h) => (
      <div
        data-testid="link-widget"
        style={{ display: 'flex', 'flex-direction': 'column', gap: '8px', font: tokens.type.monoSm, color: tokens.fg }}
      >
        <div style={{ display: 'flex', 'align-items': 'center', 'justify-content': 'space-between', gap: '8px' }}>
          <span style={{ display: 'inline-flex', 'align-items': 'center', gap: '7px' }}>
            <span
              title={h().status}
              style={{
                width: '8px',
                height: '8px',
                'border-radius': '50%',
                background: LINK_DOT[h().status],
                'box-shadow': `0 0 6px ${LINK_DOT[h().status]}`,
                flex: 'none',
              }}
            />
            {LINK_LABEL[h().status]}
          </span>
          <span style={{ opacity: 0.65 }}>up {fmtUptime(h().uptimeMs)}</span>
        </div>
        <div style={{ display: 'flex', 'justify-content': 'space-between', gap: '10px' }}>
          <span title="downloaded (router → browser)">↓ {fmtMB(sumArr(h().session.tx_bytes))}</span>
          <span title="uploaded (browser → router)">↑ {fmtMB(h().session.rx_bytes)}</span>
        </div>
        <div style={{ display: 'flex', 'justify-content': 'space-between', gap: '10px', opacity: 0.8 }}>
          <span>{fmtRate(h().rateDownBps)}</span>
          <span>
            {sumArr(h().live.dropped) > 0 ? `${sumArr(h().live.dropped)} drop` : ''}
            {h().reconnects > 0 ? `${sumArr(h().live.dropped) > 0 ? ' · ' : ''}${h().reconnects} rc` : ''}
          </span>
        </div>
      </div>
    )}
  </Show>
  </div>
);
