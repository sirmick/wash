import type { JSX, ParentComponent } from 'solid-js';
import { tokens } from './tokens';

// StatusBar is the muted-mono text strip at the bottom of an
// app window. Apps drop children inside; the component owns the
// font, padding, alignment, surface and divider. Consumers can
// extend via the `style` prop (merged last) and add testids /
// drop-target attrs via the rest spread.
//
// It sizes itself (22px, never shrinks) rather than trusting the host
// layout: in a flex column a shrinkable strip collapses to its text
// height and the scrolling list above runs into it. Muting is by color,
// not opacity, so a child that asks for fgDim stays legible instead of
// fading twice.
export interface StatusBarProps {
  'data-testid'?: string;
  style?: JSX.CSSProperties;
  children?: JSX.Element;
}

export const StatusBar: ParentComponent<StatusBarProps> = (props) => {
  return (
    <div
      data-testid={props['data-testid']}
      style={{
        'flex-shrink': 0,
        'min-height': '22px',
        'box-sizing': 'border-box',
        padding: '0 10px',
        'border-top': `1px solid ${tokens.borderMenu}`,
        background: tokens.bgMenu,
        color: tokens.fgMuted,
        font: tokens.type.monoSm,
        'white-space': 'nowrap',
        overflow: 'hidden',
        display: 'flex',
        'align-items': 'center',
        ...(props.style ?? {}),
      }}
    >
      {props.children}
    </div>
  );
};
