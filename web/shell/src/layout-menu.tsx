// The Layout menu (docs/WM_LAYOUT.md §7): one menu for a viewport cell,
// opened from the desktop background, the taskbar's empty space or a pager
// cell — all of which the session app owns, so it asks for the menu through
// window.wash.openLayoutMenu and the shell renders it here.

import { For, Show, createSignal, type Component } from 'solid-js';
import { Menu, MenuItem, MenuSeparator, tokens } from '@wash/ui';
import { parseLayout, place } from '@wash/ui/split-tree';
import { PRESETS, canonicalShape } from './tiling';
import type { Cell } from './tiling';
import { cellLayout, setCellLayout } from './tiling-runtime';

const [request, setRequest] = createSignal<(Cell & { x: number; y: number }) | null>(null);

export function openLayoutMenu(c: Cell, x: number, y: number): void {
  setRequest({ vx: c.vx, vy: c.vy, x, y });
}

// Above the session app's taskbar layer (z-index 10000), which a menu
// opened from it would otherwise sit under.
const MENU_Z = 10001;

// LayoutIcon draws a shape's panels in a small box: the menu's preview of
// what a preset will do.
const LayoutIcon: Component<{ shape: string }> = (props) => {
  const W = 18;
  const H = 12;
  const rects = () =>
    place(parseLayout(props.shape, () => ({ kind: 'slot' as const })), { x: 0, y: 0, w: W, h: H }, 1.5).leaves;
  return (
    <svg width={W} height={H} viewBox={`0 0 ${W} ${H}`} aria-hidden="true" style={{ 'flex-shrink': 0 }}>
      <For each={rects()}>
        {(l) => (
          <rect
            x={l.rect.x}
            y={l.rect.y}
            width={l.rect.w}
            height={l.rect.h}
            rx="1"
            fill={`color-mix(in srgb, ${tokens.fg} 18%, transparent)`}
            stroke={tokens.fgMuted}
            stroke-width="0.75"
          />
        )}
      </For>
    </svg>
  );
};

const PRESET_SHAPES = PRESETS.map((p) => ({ ...p, canon: canonicalShape(p.shape) }));

export const LayoutMenuLayer: Component = () => (
  <Show when={request()}>
    {(r) => {
      const current = () => {
        const l = cellLayout(r());
        return l && l.shape !== '' ? l.shape : null;
      };
      const pick = (shape: string | null) => {
        // Read the cell before closing: closing ends this <Show>, and its
        // accessor goes stale with it.
        const cell = { vx: r().vx, vy: r().vy };
        setRequest(null);
        setCellLayout(cell, shape);
      };
      return (
        <Menu x={r().x} y={r().y} onDismiss={() => setRequest(null)} zIndex={MENU_Z} data-testid="layout-menu">
          <For each={PRESET_SHAPES}>
            {(p) => (
              <MenuItem
                label={p.name}
                title={p.canon}
                icon={<LayoutIcon shape={p.canon} />}
                trailing={current() === p.canon ? <span aria-label="current">✓</span> : undefined}
                data-testid={`layout-preset-${p.canon}`}
                onClick={() => pick(p.canon)}
              />
            )}
          </For>
          <MenuSeparator />
          <MenuItem
            label="No layout"
            title="Float every window in this viewport again"
            disabled={current() === null}
            data-testid="layout-none"
            onClick={() => pick(null)}
          />
        </Menu>
      );
    }}
  </Show>
);
