import { splitProps } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { tokens } from './tokens';

// Variants:
//   default — normal button (Cancel, Skip, etc.)
//   danger  — destructive action (Delete, Replace, Replace All)
//   ghost   — transparent / chrome (button with hover background)
//   icon    — small square icon-only chrome button (toolbar)
//
// Every variant carries the interaction layer (hit.ts): hover tint,
// press well, keyboard focus ring, and the inert treatment when
// `disabled` is set. That is why the styles below declare no :hover
// colours of their own — an inline style object cannot, and the
// pseudo-element overlay derives its tint from whatever the variant
// paints, so `danger` darkens red on press without a second palette.
//
// Spreads the full HTMLButtonElement attribute set so callers can
// pass title, data-testid, type, disabled, etc. without bespoke
// passthrough. Style is computed from variant + size; callers may
// extend via the `style` prop (merged last).
export type ButtonVariant = 'default' | 'danger' | 'ghost' | 'icon';
export type ButtonSize = 'sm' | 'md';

export interface ButtonProps extends JSX.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
}

export const Button: Component<ButtonProps> = (props) => {
  const [local, rest] = splitProps(props, ['variant', 'size', 'style', 'type', 'children']);
  return (
    <button
      type={local.type ?? 'button'}
      // Written literally, and before {...rest}: literally so the
      // check-interactive guard can see it, and first so a caller can still
      // dial the intensity down on a specific button.
      data-wash-hit=""
      style={{
        ...baseStyle(local.variant ?? 'default', local.size ?? 'md'),
        ...((local.style as JSX.CSSProperties | undefined) ?? {}),
      }}
      {...rest}
    >
      {local.children}
    </button>
  );
};

function baseStyle(v: ButtonVariant, s: ButtonSize): JSX.CSSProperties {
  const padY = s === 'sm' ? '3px' : '6px';
  const padX = s === 'sm' ? '10px' : '12px';
  switch (v) {
    case 'danger':
      return {
        background: tokens.bgDanger,
        color: tokens.fg,
        border: `1px solid ${tokens.borderDanger}`,
        'border-radius': `${tokens.radiusSm}`,
        padding: `${padY} ${padX}`,
        cursor: 'pointer',
        font: tokens.type.textMd,
      };
    case 'ghost':
      return {
        background: 'transparent',
        color: tokens.fg,
        border: `1px solid ${tokens.borderMenu}`,
        'border-radius': `${tokens.radiusSm}`,
        padding: `${padY} ${padX}`,
        cursor: 'pointer',
        font: tokens.type.textMd,
      };
    case 'icon':
      return {
        background: 'transparent',
        color: tokens.fg,
        border: 'none',
        'border-radius': `${tokens.radiusSm}`,
        padding: '4px',
        cursor: 'pointer',
        display: 'flex',
        'align-items': 'center',
        'justify-content': 'center',
        width: '24px',
        height: '24px',
      };
    case 'default':
    default:
      return {
        background: tokens.bgMenu,
        color: tokens.fg,
        border: `1px solid ${tokens.borderMenu}`,
        'border-radius': `${tokens.radiusSm}`,
        padding: `${padY} ${padX}`,
        cursor: 'pointer',
        font: tokens.type.textMd,
      };
  }
}
