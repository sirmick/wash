// panel-kit — the small set of styled primitives settings panels share:
// Section, Row, ServiceBadge, SmallBtn, Select. Lifted out of the
// settings app once vscode + netd (+ display) each needed the same
// pieces for their app-supplied panels. Framework-Solid, shipped in the
// @wash/ui vendor bundle so every panel resolves them via the importmap.

import { For } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { tokens } from './tokens';

const sectionTitleStyle: JSX.CSSProperties = {
  font: `600 ${tokens.fontSizeBase} ${tokens.fontSans}`,
  opacity: 0.7,
  'text-transform': 'uppercase',
  'letter-spacing': '0.04em',
  'margin-bottom': '8px',
};

/** Section is a titled block in a settings pane. */
export const Section: Component<{ title: string; children: JSX.Element }> = (props) => (
  <div>
    <div style={sectionTitleStyle}>{props.title}</div>
    {props.children}
  </div>
);

/** Row is a label + control grid line. */
export const Row: Component<{ label: string; children: JSX.Element }> = (props) => (
  <div style={{ display: 'grid', 'grid-template-columns': '140px 1fr', gap: '12px', 'align-items': 'center' }}>
    <div style={{ opacity: 0.7, font: `${tokens.fontSizeBase} ${tokens.fontSans}` }}>{props.label}</div>
    {props.children}
  </div>
);

const smallBtnStyle: JSX.CSSProperties = {
  display: 'inline-flex',
  'align-items': 'center',
  gap: '5px',
  background: tokens.bgMenu,
  color: tokens.fg,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusMd}`,
  padding: '4px 10px',
  font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
  cursor: 'pointer',
};

/** SmallBtn is the compact action button used across panes. */
export const SmallBtn: Component<{
  onClick: () => void;
  'data-testid'?: string;
  children: JSX.Element;
}> = (props) => (
  <button type="button" data-testid={props['data-testid']} onClick={props.onClick} style={smallBtnStyle}>
    {props.children}
  </button>
);

const selectStyle: JSX.CSSProperties = {
  background: tokens.bgMenu,
  color: tokens.fg,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusMd}`,
  padding: '4px 8px',
  font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
  cursor: 'pointer',
};

/** Select is a styled <select> over [value, label] option pairs. */
export const Select: Component<{
  value: string;
  options: [string, string][];
  onChange: (v: string) => void;
}> = (props) => (
  <select value={props.value} onInput={(e) => props.onChange(e.currentTarget.value)} style={selectStyle}>
    <For each={props.options}>{([v, l]) => <option value={v}>{l}</option>}</For>
  </select>
);

/** ServiceBadge is the small status pill shared by service panels. */
export const ServiceBadge: Component<{ tone: 'on' | 'off' | 'busy' | 'absent'; label: string }> = (props) => {
  const palette: Record<string, { bg: string; fg: string }> = {
    on: { bg: '#1c3d24', fg: '#86efac' },
    off: { bg: '#1f1f2a', fg: tokens.fgDim },
    busy: { bg: '#3a3a1c', fg: '#fde047' },
    absent: { bg: '#3d1c1c', fg: '#fca5a5' },
  };
  const c = palette[props.tone] ?? palette.off;
  return (
    <span
      data-testid="service-badge"
      data-tone={props.tone}
      style={{
        display: 'inline-flex',
        'align-items': 'center',
        padding: '2px 10px',
        'border-radius': `${tokens.radiusSm}`,
        font: `${tokens.fontSizeSm} ${tokens.fontSans}`,
        background: c.bg,
        color: c.fg,
      }}
    >
      {props.label}
    </span>
  );
};
