// What a tiled cell draws besides its windows (docs/WM_LAYOUT.md §7): the
// dividers in its gutters, a faint outline on each empty panel (somewhere
// to drop a window), and the highlight on the panel a dragged window would
// land in. Lives inside the camera, in plane coordinates, so it pans with
// the windows.

import { For, Index, Show, type Component } from 'solid-js';
import { tokens } from '@wash/ui';
import { formatLayout, minFractionFor, parseLayout, place, resizeSplit } from '@wash/ui/split-tree';
import type { PlacedDivider } from '@wash/ui/split-tree';
import { TILE_GUTTER, slotFrames } from './tiling';
import type { Cell } from './tiling';
import { cellLayout, dragHover, effectiveShape, setCellShape, setPreview } from './tiling-runtime';
import { VIEWPORTS_PER_AXIS, cellWorkArea } from './wm';

// The smallest a panel's frame may be dragged: a titlebar's buttons and a
// little content. Divider drags clamp to it.
const MIN_PANEL_W = 160;
const MIN_PANEL_H = 100;

// Dividers grab a little beyond the gutter, onto the neighbours' borders,
// so the 4px seam is not a pixel hunt. Above every window in the camera.
const GRAB = 3;
const DIVIDER_Z = 900_001;
const HIGHLIGHT_Z = 900_000;

type Slot = { kind: 'slot' };
const slot = (): Slot => ({ kind: 'slot' });

const CELLS: Cell[] = Array.from({ length: VIEWPORTS_PER_AXIS * VIEWPORTS_PER_AXIS }, (_, i) => ({
  vx: i % VIEWPORTS_PER_AXIS,
  vy: Math.floor(i / VIEWPORTS_PER_AXIS),
}));

// startDividerDrag trades size between the two panels either side of a
// divider. The tree it edits is the one the drag started from, so the
// pointer's total travel maps to one resize — no drift from accumulating
// rounded steps. Commits once, on release.
function startDividerDrag(c: Cell, d: PlacedDivider, ev: PointerEvent): void {
  if (ev.button !== 0) return;
  ev.preventDefault();
  ev.stopPropagation();
  const start = effectiveShape(c);
  if (!start) return;
  const tree = parseLayout(start, slot);
  const target = ev.currentTarget as HTMLElement;
  target.setPointerCapture(ev.pointerId);
  const x0 = ev.clientX;
  const y0 = ev.clientY;
  const minFrac = minFractionFor(d.dir, d.span, MIN_PANEL_W, MIN_PANEL_H);
  let latest = start;
  let raf: number | null = null;
  const onMove = (m: PointerEvent) => {
    const delta = d.dir === 'row' ? m.clientX - x0 : m.clientY - y0;
    latest = formatLayout(resizeSplit(tree, d.path, d.index, delta / d.span, { empty: slot }, minFrac));
    if (raf == null) {
      raf = requestAnimationFrame(() => {
        raf = null;
        setPreview(c, latest);
      });
    }
  };
  const onUp = () => {
    target.removeEventListener('pointermove', onMove);
    target.removeEventListener('pointerup', onUp);
    target.releasePointerCapture(ev.pointerId);
    if (raf != null) cancelAnimationFrame(raf);
    setCellShape(c, latest);
    setPreview(c, null);
  };
  target.addEventListener('pointermove', onMove);
  target.addEventListener('pointerup', onUp);
}

const CellOverlay: Component<{ cell: Cell }> = (props) => {
  const shape = () => effectiveShape(props.cell);
  const area = () => cellWorkArea(props.cell.vx, props.cell.vy);
  const frames = () => slotFrames(shape() ?? '', area());
  const dividers = () => (shape() ? place(parseLayout(shape()!, slot), area(), TILE_GUTTER).dividers : []);
  const empty = (i: number) => cellLayout(props.cell)?.slots[i] == null;
  const hovered = (i: number) => {
    const h = dragHover();
    return !!h && h.cell.vx === props.cell.vx && h.cell.vy === props.cell.vy && h.slot === i;
  };
  return (
    <>
      <For each={frames()}>
        {(f, i) => (
          <Show when={empty(i()) || hovered(i())}>
            <div
              data-testid={`tile-panel-${props.cell.vx}-${props.cell.vy}-${i()}`}
              data-hover={hovered(i()) ? 'true' : undefined}
              style={{
                position: 'absolute',
                left: `${f.x}px`,
                top: `${f.y}px`,
                width: `${f.w}px`,
                height: `${f.h}px`,
                'box-sizing': 'border-box',
                'pointer-events': 'none',
                'border-radius': tokens.radiusSm,
                border: hovered(i())
                  ? `2px solid ${tokens.accentBlue}`
                  : `1px dashed color-mix(in srgb, ${tokens.fg} 22%, transparent)`,
                background: hovered(i()) ? `color-mix(in srgb, ${tokens.accentBlue} 18%, transparent)` : 'transparent',
                'z-index': hovered(i()) ? HIGHLIGHT_Z : 0,
              }}
            />
          </Show>
        )}
      </For>
      {/* Index, not For: a drag re-places the dividers every frame, and For
          would swap in new elements, dropping the one holding the pointer
          capture mid-drag. The structure is fixed while dragging. */}
      <Index each={dividers()}>
        {(d) => (
          <div
            data-testid={`tile-divider-${props.cell.vx}-${props.cell.vy}-${d().path || 'root'}-${d().index}`}
            onPointerDown={(ev) => startDividerDrag(props.cell, d(), ev)}
            style={{
              position: 'absolute',
              left: `${d().dir === 'row' ? d().rect.x - GRAB : d().rect.x}px`,
              top: `${d().dir === 'col' ? d().rect.y - GRAB : d().rect.y}px`,
              width: `${d().dir === 'row' ? d().rect.w + GRAB * 2 : d().rect.w}px`,
              height: `${d().dir === 'col' ? d().rect.h + GRAB * 2 : d().rect.h}px`,
              cursor: d().dir === 'row' ? 'col-resize' : 'row-resize',
              'pointer-events': 'auto',
              'z-index': DIVIDER_Z,
            }}
          />
        )}
      </Index>
    </>
  );
};

export const TilingOverlay: Component = () => (
  <For each={CELLS}>
    {(c) => (
      <Show when={(cellLayout(c)?.shape ?? '') !== ''}>
        <CellOverlay cell={c} />
      </Show>
    )}
  </For>
);
