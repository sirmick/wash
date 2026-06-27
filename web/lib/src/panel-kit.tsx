// panel-kit — the small set of styled primitives settings panels share:
// Section, Row, ServiceBadge, SmallBtn, Select, Input, Checkbox. Lifted out
// of the settings app once vscode + netd (+ display) each needed the same
// pieces for their app-supplied panels. Input/Checkbox were later lifted
// from the identical inline field style every app had copied. Framework-Solid,
// shipped in the @wash/ui vendor bundle so every panel resolves them via the
// importmap.

import { For, splitProps } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { tokens } from './tokens';

const sectionTitleStyle: JSX.CSSProperties = {
  font: tokens.type.titleSm,
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
    <div style={{ opacity: 0.7, font: tokens.type.textMd }}>{props.label}</div>
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
  font: tokens.type.textMd,
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
  font: tokens.type.textMd,
  cursor: 'pointer',
};

/** Select is a styled <select> over [value, label] option pairs. */
export const Select: Component<{
  value: string;
  options: [string, string][];
  onChange: (v: string) => void;
  'data-testid'?: string;
}> = (props) => (
  <select
    value={props.value}
    data-testid={props['data-testid']}
    onInput={(e) => props.onChange(e.currentTarget.value)}
    style={selectStyle}
  >
    <For each={props.options}>{([v, l]) => <option value={v}>{l}</option>}</For>
  </select>
);

const inputStyle: JSX.CSSProperties = {
  background: tokens.bgInset,
  color: tokens.fg,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusSm}`,
  padding: '4px 8px',
  font: tokens.type.textMd,
  outline: 'none',
};

/**
 * Input is the shared sunken text/number/password/search field — the inline
 * style that every app had copied verbatim. Spreads native input attributes
 * (value, type, placeholder, onInput, disabled, data-testid…); callers extend
 * via `style` (merged last).
 */
export const Input: Component<JSX.InputHTMLAttributes<HTMLInputElement>> = (props) => {
  const [local, rest] = splitProps(props, ['style']);
  return <input {...rest} style={{ ...inputStyle, ...((local.style as JSX.CSSProperties | undefined) ?? {}) }} />;
};

const checkboxLabelStyle: JSX.CSSProperties = {
  display: 'inline-flex',
  'align-items': 'center',
  gap: '6px',
  font: tokens.type.textMd,
  color: tokens.fg,
  cursor: 'pointer',
  'user-select': 'none',
};

/** Checkbox is the shared label + accent-themed box + text form control. */
export const Checkbox: Component<{
  checked: boolean;
  onChange: (v: boolean) => void;
  label?: JSX.Element;
  disabled?: boolean;
  'data-testid'?: string;
}> = (props) => (
  <label style={{ ...checkboxLabelStyle, ...(props.disabled ? { opacity: 0.5, cursor: 'default' } : {}) }}>
    <input
      type="checkbox"
      checked={props.checked}
      disabled={props.disabled}
      data-testid={props['data-testid']}
      onChange={(e) => props.onChange(e.currentTarget.checked)}
      style={{ 'accent-color': tokens.accentBlue, cursor: 'inherit', margin: 0 }}
    />
    {props.label != null ? <span>{props.label}</span> : null}
  </label>
);

/** ServiceBadge is the small status pill shared by service panels. */
export const ServiceBadge: Component<{ tone: 'on' | 'off' | 'busy' | 'absent'; label: string }> = (props) => {
  const palette: Record<string, { bg: string; fg: string }> = {
    on: { bg: tokens.bgSuccess, fg: tokens.fgSuccess },
    off: { bg: tokens.bgNeutral, fg: tokens.fgDim },
    busy: { bg: tokens.bgWarning, fg: tokens.fgWarning },
    absent: { bg: tokens.bgDanger, fg: tokens.fgDanger },
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
        font: tokens.type.textSm,
        background: c.bg,
        color: c.fg,
      }}
    >
      {props.label}
    </span>
  );
};
