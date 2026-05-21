// Splitter — draggable divider between two panes. Vertical (the
// default) drags column width; horizontal drags row height.
//
// Usage: parent owns the split percentage signal and a ref to the
// flex/grid container the splitter measures against. On drag, the
// splitter calls onChange(pct) for each mousemove (so the caller
// can re-render layout reactively) and onCommit() once at mouseup
// (the natural moment to persist).
//
// We capture mousemove/mouseup on the window, not the splitter
// element, so fast drags that move the cursor outside the bar
// don't lose the gesture. While dragging, body's user-select and
// cursor get pinned so text-selection drift and cursor flicker
// don't show through.

import type { Component, JSX } from 'solid-js';
import { tokens } from './tokens';

export type SplitterOrientation = 'vertical' | 'horizontal';

export interface SplitterProps {
  // The container the splitter divides. The pct is computed
  // against this element's width (vertical) or height (horizontal).
  container: HTMLElement;
  // 'vertical' (default) = drag a column width. 'horizontal' =
  // drag a row height.
  orientation?: SplitterOrientation;
  // Pixel size of the splitter handle along its drag axis.
  // Default 4.
  thickness?: number;
  // Min/max percent the splitter clamps to. Defaults 15 / 85.
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
    const horizontal = props.orientation === 'horizontal';
    const totalSize = horizontal ? rect.height : rect.width;
    const startEdge = horizontal ? rect.top : rect.left;
    const minPct = props.min ?? 15;
    const maxPct = props.max ?? 85;
    const cursor = horizontal ? 'row-resize' : 'col-resize';
    const onMove = (e: MouseEvent) => {
      const cursorPos = horizontal ? e.clientY : e.clientX;
      const pct = ((cursorPos - startEdge) / totalSize) * 100;
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
    document.body.style.cursor = cursor;
  };
  const horizontal = props.orientation === 'horizontal';
  const thickness = `${props.thickness ?? 4}px`;
  const style: JSX.CSSProperties = horizontal
    ? {
        background: props.color ?? tokens.borderMenu,
        cursor: 'row-resize',
        'user-select': 'none',
        height: thickness,
        width: '100%',
      }
    : {
        background: props.color ?? tokens.borderMenu,
        cursor: 'col-resize',
        'user-select': 'none',
        width: thickness,
        height: '100%',
      };
  return (
    <div
      data-testid={props['data-testid']}
      style={style}
      onMouseDown={onMouseDown}
    />
  );
};
