import { splitProps } from 'solid-js';
import type { JSX, ParentComponent } from 'solid-js';
import { tokens } from './tokens';

// Chip is the one shape for a small state/kind label in a row — a
// service's enabled state, a package's "installed", a collection kind.
// Tones map onto the semantic bg/fg pairs in tokens.ts, so every app's
// chips read the same and re-skin with the pack. Filled, not outlined:
// an outline in currentColor reads as a button, and as heavier than the
// row text it annotates.
export type ChipTone = 'neutral' | 'success' | 'warning' | 'info' | 'danger';

export interface ChipProps extends JSX.HTMLAttributes<HTMLSpanElement> {
  tone?: ChipTone;
}

const TONES: Record<ChipTone, { bg: string; fg: string }> = {
  neutral: { bg: tokens.bgNeutral, fg: tokens.fgMuted },
  success: { bg: tokens.bgSuccess, fg: tokens.fgSuccess },
  warning: { bg: tokens.bgWarning, fg: tokens.fgWarning },
  info: { bg: tokens.bgInfo, fg: tokens.fgInfo },
  danger: { bg: tokens.bgDanger, fg: tokens.fgDanger },
};

export const Chip: ParentComponent<ChipProps> = (props) => {
  const [local, rest] = splitProps(props, ['tone', 'style', 'children']);
  const t = () => TONES[local.tone ?? 'neutral'];
  return (
    <span
      style={{
        display: 'inline-flex',
        'align-items': 'center',
        'justify-content': 'center',
        gap: '4px',
        padding: '1px 7px',
        'border-radius': `${tokens.radiusSm}`,
        background: t().bg,
        color: t().fg,
        font: tokens.type.textSm,
        'white-space': 'nowrap',
        ...((local.style as JSX.CSSProperties | undefined) ?? {}),
      }}
      {...rest}
    >
      {local.children}
    </span>
  );
};
