// Splitter — draggable vertical divider between two panes.
//
// Usage: parent owns the split percentage signal and a ref to the
// flex/grid container whose width the splitter measures against.
// On drag, the splitter calls onChange(pct) for each mousemove (so
// the caller can re-render layout reactively) and onCommit() once
// at mouseup (the natural moment to persist).
//
// We capture mousemove/mouseup on the window, not the splitter
// element, so fast drags that move the cursor outside the bar
// don't lose the gesture. While dragging, body's user-select and
// cursor get pinned so text-selection drift and cursor flicker
// don't show through.

import type { Component, JSX } from 'solid-js';
import { tokens } from './tokens';

export interface SplitterProps {
  // The container the splitter divides. The pct is computed
  // against this element's width.
  container: HTMLElement;
  // Pixel width of the splitter handle. Default 4.
  width?: number;
  // Min/max percent (left pane share) the splitter clamps to.
  // Defaults 15 / 85.
  min?: number;
  max?: number;
  // Fires on each mousemove during drag with the new clamped pct.
  onChange: (pct: number) => void;
  // Fires once on mouseup. Use this to persist the new split.
  onCommit?: () => void;
  // testid for e2e selectors.
  'data-testid'?: string;
  // CSS color override for the handle background.
  color?: string;
}

export const Splitter: Component<SplitterProps> = (props) => {
  const onMouseDown = (ev: MouseEvent) => {
    ev.preventDefault();
    const rect = props.container.getBoundingClientRect();
    const width = rect.width;
    const leftEdge = rect.left;
    const minPct = props.min ?? 15;
    const maxPct = props.max ?? 85;
    const onMove = (e: MouseEvent) => {
      const pct = ((e.clientX - leftEdge) / width) * 100;
      props.onChange(Math.max(minPct, Math.min(maxPct, pct)));
    };
    const onUp = () => {
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onUp);
      document.body.style.userSelect = '';
      document.body.style.cursor = '';
      props.onCommit?.();
    };
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
    document.body.style.userSelect = 'none';
    document.body.style.cursor = 'col-resize';
  };
  const style: JSX.CSSProperties = {
    background: props.color ?? tokens.borderMenu,
    cursor: 'col-resize',
    'user-select': 'none',
    width: `${props.width ?? 4}px`,
  };
  return (
    <div
      data-testid={props['data-testid']}
      style={style}
      onMouseDown={onMouseDown}
    />
  );
};
