import type { JSX, ParentComponent } from 'solid-js';
import { tokens } from './tokens';

// StatusBar is the muted-mono text strip at the bottom of an
// app window. Apps drop children inside; the component owns the
// font, padding, alignment, and muted opacity. Consumers can
// extend via the `style` prop (merged last) and add testids /
// drop-target attrs via the rest spread.
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
        padding: '0 10px',
        font: `${tokens.fontSizeSm} ${tokens.fontMono}`,
        opacity: 0.6,
        display: 'flex',
        'align-items': 'center',
        ...(props.style ?? {}),
      }}
    >
      {props.children}
    </div>
  );
};
