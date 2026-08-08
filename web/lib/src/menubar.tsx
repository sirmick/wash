// <MenuBar> / <MenuBarItem> — the desktop's application menu strip.
//
// Extracted from wash-edit when wash-ai became the second app to want
// one. Two hand-rolled menubars had already started to drift (different
// heights, different active-state colours), which is the whole argument
// for this file: the strip along the top of a window should look the same
// in every window.
//
// It owns only presentation and which menu is open. The MENUS themselves
// stay with the app — <Menu>/<MenuItem> are already shared, and what goes
// in File differs per app by definition.

import { For, Show, createSignal } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { tokens } from './tokens';

export interface MenuBarMenu {
  /** stable id, also used for the testid */
  id: string;
  label: string;
  /** rendered when open; `at` is where to anchor the <Menu>, and
   *  `close` dismisses it — the bar owns which menu is open, the app
   *  owns what is in it. */
  render: (at: { x: number; y: number }, close: () => void) => JSX.Element;
}

export interface MenuBarProps {
  menus: MenuBarMenu[];
  /** testid prefix; items get `<prefix>-<id>` */
  testidPrefix?: string;
  /** trailing content, right-aligned — a title, a status chip */
  trailing?: JSX.Element;
}

/** Keyboard-hint styling for a menu item's `trailing`. */
export const kbdStyle: JSX.CSSProperties = {
  font: tokens.type.monoSm,
  color: tokens.fgMuted,
  background: 'transparent',
  padding: '0 4px',
};

export const MenuBar: Component<MenuBarProps> = (props) => {
  const [open, setOpen] = createSignal('');
  const [at, setAt] = createSignal({ x: 0, y: 0 });

  const toggle = (id: string, ev: MouseEvent) => {
    const r = (ev.currentTarget as HTMLElement).getBoundingClientRect();
    setAt({ x: r.left, y: r.bottom });
    setOpen((cur) => (cur === id ? '' : id));
  };

  return (
    <div
      style={{
        display: 'flex',
        'align-items': 'center',
        background: tokens.bgMenu,
        'border-bottom': `1px solid ${tokens.borderMenu}`,
        'min-height': '24px',
        'flex-shrink': 0,
        position: 'relative',
        'user-select': 'none',
      }}
    >
      <For each={props.menus}>
        {(m) => (
          <>
            <button
              type="button"
              data-testid={`${props.testidPrefix ?? 'menubar'}-${m.id}`}
              onClick={(ev) => toggle(m.id, ev)}
              style={{
                background: open() === m.id ? tokens.bgRowSelected : 'transparent',
                color: tokens.fg,
                border: 'none',
                padding: '2px 10px',
                height: '24px',
                cursor: 'pointer',
                font: tokens.type.textMd,
              }}
            >
              {m.label}
            </button>
            <Show when={open() === m.id}>{m.render(at(), () => setOpen(''))}</Show>
          </>
        )}
      </For>
      <Show when={props.trailing}>
        <div style={{ 'margin-left': 'auto', display: 'flex', 'align-items': 'center' }}>
          {props.trailing}
        </div>
      </Show>
    </div>
  );
};
