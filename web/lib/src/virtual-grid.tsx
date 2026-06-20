// VirtualGrid — a windowed (virtualized) tile grid for large collections.
// Only the tiles near the viewport are mounted, so a folder of thousands of
// images renders ~a few dozen DOM nodes instead of thousands (which would
// otherwise stall layout/paint). Works for a multi-column grid (fm's folder
// preview) and a single-column list (the image viewer) — column count is
// derived from the container width and tileWidth.
//
// Requirements: tiles are a FIXED tileHeight (the row pitch is computed, not
// measured). Pass the raw item objects — <For> keys by reference, so tiles
// that stay in the window across a scroll are reused, not remounted.

import { For, createMemo, createSignal, onCleanup, onMount } from 'solid-js';
import type { JSX } from 'solid-js';

export interface VirtualGridProps<T> {
  items: readonly T[];
  /** minimum tile width (px); column count = floor(width / tileWidth). */
  tileWidth: number;
  /** fixed tile height (px) — the row pitch, so tiles must not exceed it. */
  tileHeight: number;
  /** gap between tiles (px). Default 6. */
  gap?: number;
  /** extra rows rendered above/below the viewport. Default 2. */
  overscan?: number;
  renderItem: (item: T) => JSX.Element;
  /** style for the scroll container (background, padding, border, …). */
  style?: JSX.CSSProperties;
  'data-testid'?: string;
  /** optional drag-drop handlers on the scroll container, so the grid can
   *  act as a drop target for its own backing collection (e.g. fm's folder
   *  grid accepting a drop into the shown directory). Tiles that want their
   *  own drop behaviour stopPropagation in renderItem. */
  onDragOver?: (ev: DragEvent) => void;
  onDragLeave?: (ev: DragEvent) => void;
  onDrop?: (ev: DragEvent) => void;
}

export function VirtualGrid<T>(props: VirtualGridProps<T>): JSX.Element {
  const gap = () => props.gap ?? 6;
  const overscan = () => props.overscan ?? 2;
  let container!: HTMLDivElement;
  const [scrollTop, setScrollTop] = createSignal(0);
  const [vw, setVw] = createSignal(0);
  const [vh, setVh] = createSignal(0);

  onMount(() => {
    const measure = () => {
      setVw(container.clientWidth);
      setVh(container.clientHeight);
    };
    const ro = new ResizeObserver(measure);
    ro.observe(container);
    measure();
    onCleanup(() => ro.disconnect());
  });

  // padding eats into usable width; assume the caller's style padding is
  // symmetric and small — column math uses the content box clientWidth.
  const cols = createMemo(() => Math.max(1, Math.floor((vw() + gap()) / (props.tileWidth + gap()))));
  const rowH = createMemo(() => props.tileHeight + gap());
  const totalRows = createMemo(() => Math.ceil(props.items.length / cols()));
  const totalH = createMemo(() => Math.max(0, totalRows() * rowH() - gap()));
  const startRow = createMemo(() => Math.max(0, Math.floor(scrollTop() / rowH()) - overscan()));
  const endRow = createMemo(() => Math.min(totalRows(), Math.ceil((scrollTop() + vh()) / rowH()) + overscan()));
  // Slice the original refs so <For> reuses tiles that persist across scroll.
  const visible = createMemo(() => props.items.slice(startRow() * cols(), endRow() * cols()));

  return (
    <div
      ref={container}
      data-testid={props['data-testid']}
      onScroll={(e) => setScrollTop(e.currentTarget.scrollTop)}
      onDragOver={props.onDragOver}
      onDragLeave={props.onDragLeave}
      onDrop={props.onDrop}
      style={{ overflow: 'auto', position: 'relative', ...(props.style ?? {}) }}
    >
      {/* full-height spacer so the scrollbar reflects the whole collection */}
      <div style={{ height: `${totalH()}px`, position: 'relative' }}>
        <div
          style={{
            position: 'absolute',
            top: `${startRow() * rowH()}px`,
            left: 0,
            right: 0,
            display: 'grid',
            'grid-template-columns': `repeat(${cols()}, 1fr)`,
            'grid-auto-rows': `${props.tileHeight}px`,
            gap: `${gap()}px`,
          }}
        >
          <For each={visible()}>{(item) => props.renderItem(item)}</For>
        </div>
      </div>
    </div>
  );
}
