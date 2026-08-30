import { splitProps } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { WASH_BTN_CLASS, ensureControlStyles } from './controls';

// Variants:
//   default — normal button (Cancel, Skip, etc.)
//   danger  — destructive action (Delete, Replace, Replace All)
//   ghost   — transparent / chrome (button with hover background)
//   icon    — small square icon-only chrome button (toolbar)
//
// Spreads the full HTMLButtonElement attribute set so callers can
// pass title, data-testid, type, disabled, etc. without bespoke
// passthrough. Style is computed from variant + size; callers may
// extend via the `style` prop (merged last).
//
// COLOR LIVES IN THE STYLESHEET, not here. controls.ts owns the resting
// palette per variant plus the hover / press / focus / disabled states,
// because those are selectors and an inline style cannot express them.
// What stays inline is geometry — padding, and the icon variant's fixed
// square footprint — which has no state to it.
//
// To recolor one button, set the custom property rather than
// `background`, so the derived states follow:
//
//     <Button style={{ '--wash-btn-bg': tokens.bgInfo }}>Apply</Button>
//
// Setting `background` directly still works and still wins, but pins the
// button to one color in every state — it will not light up under the
// cursor. Only do that where that's genuinely what you want.
export type ButtonVariant = 'default' | 'danger' | 'ghost' | 'icon';
export type ButtonSize = 'sm' | 'md';

export interface ButtonProps extends JSX.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
}

export const Button: Component<ButtonProps> = (props) => {
  const [local, rest] = splitProps(props, ['variant', 'size', 'style', 'type', 'class', 'children']);
  ensureControlStyles();
  return (
    <button
      type={local.type ?? 'button'}
      // The variant is an attribute rather than a second class so the
      // stylesheet's [data-variant=…] rules can outrank the bare
      // .wash-btn defaults without a specificity fight.
      data-variant={local.variant ?? 'default'}
      class={local.class ? `${WASH_BTN_CLASS} ${local.class}` : WASH_BTN_CLASS}
      style={{
        ...boxStyle(local.variant ?? 'default', local.size ?? 'md'),
        ...((local.style as JSX.CSSProperties | undefined) ?? {}),
      }}
      {...rest}
    >
      {local.children}
    </button>
  );
};

// boxStyle is geometry only — see the note above on why color isn't here.
function boxStyle(v: ButtonVariant, s: ButtonSize): JSX.CSSProperties {
  if (v === 'icon') {
    return {
      padding: '4px',
      display: 'flex',
      'align-items': 'center',
      'justify-content': 'center',
      width: '24px',
      height: '24px',
    };
  }
  return {
    padding: s === 'sm' ? '3px 10px' : '6px 12px',
  };
}
